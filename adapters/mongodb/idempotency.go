package mongodb

import (
	"context"
	"fmt"
	"time"

	"go-mink.dev/adapters"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

var _ adapters.IdempotencyStore = (*IdempotencyStore)(nil)

// IdempotencyStore provides a MongoDB implementation of adapters.IdempotencyStore.
type IdempotencyStore struct {
	adapter *MongoAdapter
	coll    *mongo.Collection
}

// IdempotencyStoreOption configures an IdempotencyStore.
type IdempotencyStoreOption func(*IdempotencyStore)

// WithIdempotencyCollection sets the idempotency collection name.
func WithIdempotencyCollection(name string) IdempotencyStoreOption {
	return func(s *IdempotencyStore) {
		if name != "" {
			s.coll = s.adapter.collection(name)
		}
	}
}

// NewIdempotencyStoreFromAdapter creates an idempotency store from an existing adapter.
func NewIdempotencyStoreFromAdapter(adapter *MongoAdapter, opts ...IdempotencyStoreOption) *IdempotencyStore {
	store := &IdempotencyStore{adapter: adapter, coll: adapter.idempotency()}
	for _, opt := range opts {
		opt(store)
	}
	return store
}

type idempotencyDoc struct {
	Key         string      `bson:"_id"`
	CommandType string      `bson:"command_type"`
	AggregateID string      `bson:"aggregate_id,omitempty"`
	Version     int64       `bson:"version,omitempty"`
	Response    bson.Binary `bson:"response,omitempty"`
	Error       string      `bson:"error,omitempty"`
	Success     bool        `bson:"success"`
	ProcessedAt time.Time   `bson:"processed_at"`
	ExpiresAt   time.Time   `bson:"expires_at"`
}

// Initialize creates idempotency indexes.
func (s *IdempotencyStore) Initialize(ctx context.Context) error {
	return s.adapter.createIndexes(ctx)
}

// Exists checks if a command with the given key was already processed.
func (s *IdempotencyStore) Exists(ctx context.Context, key string) (bool, error) {
	count, err := s.coll.CountDocuments(ctx, bson.M{"_id": key, "expires_at": bson.M{"$gt": time.Now().UTC()}})
	return count > 0, err
}

// Store records that a command was processed.
func (s *IdempotencyStore) Store(ctx context.Context, record *adapters.IdempotencyRecord) error {
	if record == nil {
		return fmt.Errorf("mink/mongodb/idempotency: record is nil")
	}
	doc := idempotencyDoc{
		Key:         record.Key,
		CommandType: record.CommandType,
		AggregateID: record.AggregateID,
		Version:     record.Version,
		Response:    bson.Binary{Subtype: bson.TypeBinaryGeneric, Data: record.Response},
		Error:       record.Error,
		Success:     record.Success,
		ProcessedAt: record.ProcessedAt,
		ExpiresAt:   record.ExpiresAt,
	}
	_, err := s.coll.ReplaceOne(ctx, bson.M{"_id": record.Key}, doc, options.Replace().SetUpsert(true))
	return err
}

// Get retrieves the idempotency record for a key.
func (s *IdempotencyStore) Get(ctx context.Context, key string) (*adapters.IdempotencyRecord, error) {
	var doc idempotencyDoc
	err := s.coll.FindOne(ctx, bson.M{"_id": key, "expires_at": bson.M{"$gt": time.Now().UTC()}}).Decode(&doc)
	if err == mongo.ErrNoDocuments {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &adapters.IdempotencyRecord{
		Key:         doc.Key,
		CommandType: doc.CommandType,
		AggregateID: doc.AggregateID,
		Version:     doc.Version,
		Response:    doc.Response.Data,
		Error:       doc.Error,
		Success:     doc.Success,
		ProcessedAt: doc.ProcessedAt,
		ExpiresAt:   doc.ExpiresAt,
	}, nil
}

// Delete removes an idempotency record.
func (s *IdempotencyStore) Delete(ctx context.Context, key string) error {
	_, err := s.coll.DeleteOne(ctx, bson.M{"_id": key})
	return err
}

// Cleanup removes expired records.
func (s *IdempotencyStore) Cleanup(ctx context.Context, olderThan time.Duration) (int64, error) {
	cutoff := time.Now().Add(-olderThan).UTC()
	res, err := s.coll.DeleteMany(ctx, bson.M{"$or": []bson.M{
		{"processed_at": bson.M{"$lt": cutoff}},
		{"expires_at": bson.M{"$lt": time.Now().UTC()}},
	}})
	if err != nil {
		return 0, err
	}
	return res.DeletedCount, nil
}

// Count returns the total number of records.
func (s *IdempotencyStore) Count(ctx context.Context) (int64, error) {
	return s.coll.CountDocuments(ctx, bson.M{})
}

// Clear removes all records.
func (s *IdempotencyStore) Clear(ctx context.Context) error {
	_, err := s.coll.DeleteMany(ctx, bson.M{})
	return err
}
