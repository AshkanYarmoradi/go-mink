package mongodb

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go-mink.dev/adapters"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

var _ adapters.SagaStore = (*SagaStore)(nil)

// SagaStore provides a MongoDB implementation of adapters.SagaStore.
type SagaStore struct {
	adapter *MongoAdapter
	coll    *mongo.Collection
}

// SagaStoreOption configures a SagaStore.
type SagaStoreOption func(*SagaStore)

// WithSagaCollection sets the saga collection name.
func WithSagaCollection(name string) SagaStoreOption {
	return func(s *SagaStore) {
		if name != "" {
			s.coll = s.adapter.collection(name)
		}
	}
}

// NewSagaStoreFromAdapter creates a saga store from an existing adapter.
func NewSagaStoreFromAdapter(adapter *MongoAdapter, opts ...SagaStoreOption) *SagaStore {
	store := &SagaStore{adapter: adapter, coll: adapter.sagas()}
	for _, opt := range opts {
		opt(store)
	}
	return store
}

type sagaDoc struct {
	ID              string                 `bson:"_id"`
	Type            string                 `bson:"type"`
	CorrelationID   string                 `bson:"correlation_id,omitempty"`
	Status          int                    `bson:"status"`
	CurrentStep     int                    `bson:"current_step"`
	Data            map[string]interface{} `bson:"data,omitempty"`
	ProcessedEvents []string               `bson:"processed_events,omitempty"`
	Steps           []adapters.SagaStep    `bson:"steps,omitempty"`
	FailureReason   string                 `bson:"failure_reason,omitempty"`
	StartedAt       time.Time              `bson:"started_at"`
	UpdatedAt       time.Time              `bson:"updated_at"`
	CompletedAt     *time.Time             `bson:"completed_at,omitempty"`
	Version         int64                  `bson:"version"`
}

// Initialize creates saga indexes.
func (s *SagaStore) Initialize(ctx context.Context) error {
	return s.adapter.createIndexes(ctx)
}

// Save persists a saga state with optimistic concurrency control.
func (s *SagaStore) Save(ctx context.Context, state *adapters.SagaState) error {
	if state == nil {
		return adapters.ErrNilAggregate
	}
	if state.ID == "" {
		return adapters.ErrEmptyStreamID
	}
	now := time.Now().UTC()

	if state.Version == 0 {
		doc := sagaDocFromState(state)
		doc.Version = 1
		doc.UpdatedAt = now
		if doc.StartedAt.IsZero() {
			doc.StartedAt = now
		}
		if _, err := s.coll.InsertOne(ctx, doc); err != nil {
			if mongo.IsDuplicateKeyError(err) {
				return adapters.NewConcurrencyError(state.ID, 0, s.currentVersion(ctx, state.ID))
			}
			return fmt.Errorf("mink/mongodb/saga: failed to insert saga: %w", err)
		}
		state.Version = doc.Version
		return nil
	}

	updateDoc := sagaDocFromState(state)
	updateDoc.UpdatedAt = now
	update := bson.M{
		"$set": bson.M{
			"type":             updateDoc.Type,
			"correlation_id":   updateDoc.CorrelationID,
			"status":           updateDoc.Status,
			"current_step":     updateDoc.CurrentStep,
			"data":             updateDoc.Data,
			"processed_events": updateDoc.ProcessedEvents,
			"steps":            updateDoc.Steps,
			"failure_reason":   updateDoc.FailureReason,
			"started_at":       updateDoc.StartedAt,
			"updated_at":       updateDoc.UpdatedAt,
			"completed_at":     updateDoc.CompletedAt,
		},
		"$inc": bson.M{"version": 1},
	}
	opts := options.FindOneAndUpdate().SetReturnDocument(options.After)
	var saved sagaDoc
	err := s.coll.FindOneAndUpdate(ctx, bson.M{"_id": state.ID, "version": state.Version}, update, opts).Decode(&saved)
	if errors.Is(err, mongo.ErrNoDocuments) {
		if !s.exists(ctx, state.ID) {
			return &adapters.SagaNotFoundError{SagaID: state.ID}
		}
		return adapters.NewConcurrencyError(state.ID, state.Version, s.currentVersion(ctx, state.ID))
	}
	if err != nil {
		return fmt.Errorf("mink/mongodb/saga: failed to update saga: %w", err)
	}
	state.Version = saved.Version
	return nil
}

// Load retrieves a saga state by ID.
func (s *SagaStore) Load(ctx context.Context, sagaID string) (*adapters.SagaState, error) {
	var doc sagaDoc
	err := s.coll.FindOne(ctx, bson.M{"_id": sagaID}).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, &adapters.SagaNotFoundError{SagaID: sagaID}
	}
	if err != nil {
		return nil, err
	}
	return doc.toState(), nil
}

// FindByCorrelationID finds a saga by its correlation ID.
func (s *SagaStore) FindByCorrelationID(ctx context.Context, correlationID string) (*adapters.SagaState, error) {
	var doc sagaDoc
	err := s.coll.FindOne(ctx,
		bson.M{"correlation_id": correlationID},
		options.FindOne().SetSort(bson.D{{Key: "started_at", Value: -1}}),
	).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, &adapters.SagaNotFoundError{CorrelationID: correlationID}
	}
	if err != nil {
		return nil, err
	}
	return doc.toState(), nil
}

// FindByType finds all sagas of a given type with the specified status.
func (s *SagaStore) FindByType(ctx context.Context, sagaType string, statuses ...adapters.SagaStatus) ([]*adapters.SagaState, error) {
	filter := bson.M{"type": sagaType}
	if len(statuses) > 0 {
		values := make([]int, len(statuses))
		for i, status := range statuses {
			values[i] = int(status)
		}
		filter["status"] = bson.M{"$in": values}
	}
	cursor, err := s.coll.Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "started_at", Value: -1}}))
	if err != nil {
		return nil, err
	}
	defer func() { _ = cursor.Close(ctx) }()

	var states []*adapters.SagaState
	for cursor.Next(ctx) {
		var doc sagaDoc
		if err := cursor.Decode(&doc); err != nil {
			return nil, err
		}
		states = append(states, doc.toState())
	}
	return states, cursor.Err()
}

// Delete removes a saga state.
func (s *SagaStore) Delete(ctx context.Context, sagaID string) error {
	res, err := s.coll.DeleteOne(ctx, bson.M{"_id": sagaID})
	if err != nil {
		return err
	}
	if res.DeletedCount == 0 {
		return &adapters.SagaNotFoundError{SagaID: sagaID}
	}
	return nil
}

// Close releases resources held by the store.
func (s *SagaStore) Close() error {
	return nil
}

// Cleanup removes old terminal sagas.
func (s *SagaStore) Cleanup(ctx context.Context, olderThan time.Duration) (int64, error) {
	cutoff := time.Now().Add(-olderThan).UTC()
	res, err := s.coll.DeleteMany(ctx, bson.M{
		"status": bson.M{"$in": []int{
			int(adapters.SagaStatusCompleted),
			int(adapters.SagaStatusFailed),
			int(adapters.SagaStatusCompensated),
		}},
		"completed_at": bson.M{"$lt": cutoff},
	})
	if err != nil {
		return 0, err
	}
	return res.DeletedCount, nil
}

// CountByStatus returns the count of sagas by status.
func (s *SagaStore) CountByStatus(ctx context.Context) (map[adapters.SagaStatus]int64, error) {
	pipeline := mongo.Pipeline{
		bson.D{{Key: "$group", Value: bson.D{{Key: "_id", Value: "$status"}, {Key: "count", Value: bson.D{{Key: "$sum", Value: 1}}}}}},
	}
	cursor, err := s.coll.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, err
	}
	defer func() { _ = cursor.Close(ctx) }()

	counts := make(map[adapters.SagaStatus]int64)
	for cursor.Next(ctx) {
		var row struct {
			Status int   `bson:"_id"`
			Count  int64 `bson:"count"`
		}
		if err := cursor.Decode(&row); err != nil {
			return nil, err
		}
		counts[adapters.SagaStatus(row.Status)] = row.Count
	}
	return counts, cursor.Err()
}

func (s *SagaStore) exists(ctx context.Context, sagaID string) bool {
	count, err := s.coll.CountDocuments(ctx, bson.M{"_id": sagaID})
	return err == nil && count > 0
}

func (s *SagaStore) currentVersion(ctx context.Context, sagaID string) int64 {
	var doc sagaDoc
	if err := s.coll.FindOne(ctx, bson.M{"_id": sagaID}).Decode(&doc); err == nil {
		return doc.Version
	}
	return 0
}

func sagaDocFromState(state *adapters.SagaState) sagaDoc {
	return sagaDoc{
		ID:              state.ID,
		Type:            state.Type,
		CorrelationID:   state.CorrelationID,
		Status:          int(state.Status),
		CurrentStep:     state.CurrentStep,
		Data:            state.Data,
		ProcessedEvents: state.ProcessedEvents,
		Steps:           state.Steps,
		FailureReason:   state.FailureReason,
		StartedAt:       state.StartedAt,
		UpdatedAt:       state.UpdatedAt,
		CompletedAt:     state.CompletedAt,
		Version:         state.Version,
	}
}

func (d sagaDoc) toState() *adapters.SagaState {
	return &adapters.SagaState{
		ID:              d.ID,
		Type:            d.Type,
		CorrelationID:   d.CorrelationID,
		Status:          adapters.SagaStatus(d.Status),
		CurrentStep:     d.CurrentStep,
		Data:            d.Data,
		ProcessedEvents: d.ProcessedEvents,
		Steps:           d.Steps,
		FailureReason:   d.FailureReason,
		StartedAt:       d.StartedAt,
		UpdatedAt:       d.UpdatedAt,
		CompletedAt:     d.CompletedAt,
		Version:         d.Version,
	}
}
