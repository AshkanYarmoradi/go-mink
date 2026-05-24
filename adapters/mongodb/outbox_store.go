package mongodb

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go-mink.dev/adapters"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

var _ adapters.OutboxStore = (*OutboxStore)(nil)

// OutboxStore provides a MongoDB implementation of adapters.OutboxStore.
type OutboxStore struct {
	adapter *MongoAdapter
	coll    *mongo.Collection
}

// OutboxStoreOption configures an OutboxStore.
type OutboxStoreOption func(*OutboxStore)

// WithOutboxCollection sets the outbox collection name.
func WithOutboxCollection(name string) OutboxStoreOption {
	return func(s *OutboxStore) {
		if name != "" {
			s.coll = s.adapter.collection(name)
		}
	}
}

// NewOutboxStoreFromAdapter creates an outbox store from an existing adapter.
func NewOutboxStoreFromAdapter(adapter *MongoAdapter, opts ...OutboxStoreOption) *OutboxStore {
	store := &OutboxStore{adapter: adapter, coll: adapter.outbox()}
	for _, opt := range opts {
		opt(store)
	}
	return store
}

type outboxDoc struct {
	ID            string            `bson:"_id"`
	AggregateID   string            `bson:"aggregate_id"`
	EventType     string            `bson:"event_type"`
	Destination   string            `bson:"destination"`
	Payload       bson.Binary       `bson:"payload"`
	Headers       map[string]string `bson:"headers,omitempty"`
	Status        int               `bson:"status"`
	Attempts      int               `bson:"attempts"`
	MaxAttempts   int               `bson:"max_attempts"`
	LastError     string            `bson:"last_error,omitempty"`
	ScheduledAt   time.Time         `bson:"scheduled_at"`
	LastAttemptAt *time.Time        `bson:"last_attempt_at,omitempty"`
	ProcessedAt   *time.Time        `bson:"processed_at,omitempty"`
	CreatedAt     time.Time         `bson:"created_at"`
}

// Initialize creates outbox indexes.
func (s *OutboxStore) Initialize(ctx context.Context) error {
	return s.adapter.createIndexes(ctx)
}

// Schedule stores outbox messages for later processing.
func (s *OutboxStore) Schedule(ctx context.Context, messages []*adapters.OutboxMessage) error {
	return s.insertMessages(ctx, messages)
}

// ScheduleInTx stores outbox messages within an existing MongoDB session context.
func (s *OutboxStore) ScheduleInTx(ctx context.Context, tx interface{}, messages []*adapters.OutboxMessage) error {
	txCtx, ok := tx.(context.Context)
	if !ok || txCtx == nil {
		txCtx = ctx
	}
	return s.insertMessages(txCtx, messages)
}

func (s *OutboxStore) insertMessages(ctx context.Context, messages []*adapters.OutboxMessage) error {
	if len(messages) == 0 {
		return nil
	}
	docs := make([]any, len(messages))
	now := time.Now().UTC()
	for i, msg := range messages {
		if msg.ID == "" {
			msg.ID = uuid.NewString()
		}
		if msg.ScheduledAt.IsZero() {
			msg.ScheduledAt = now
		}
		if msg.CreatedAt.IsZero() {
			msg.CreatedAt = now
		}
		if msg.MaxAttempts == 0 {
			msg.MaxAttempts = 5
		}
		msg.Status = adapters.OutboxPending
		msg.Attempts = 0
		docs[i] = outboxDocFromMessage(msg)
	}
	if _, err := s.coll.InsertMany(ctx, docs); err != nil {
		return fmt.Errorf("mink/mongodb/outbox: failed to schedule messages: %w", err)
	}
	return nil
}

// FetchPending atomically claims up to limit pending messages.
func (s *OutboxStore) FetchPending(ctx context.Context, limit int) ([]*adapters.OutboxMessage, error) {
	if limit <= 0 {
		limit = 100
	}
	messages := make([]*adapters.OutboxMessage, 0, limit)
	now := time.Now().UTC()
	for len(messages) < limit {
		filter := bson.M{
			"status":       int(adapters.OutboxPending),
			"scheduled_at": bson.M{"$lte": now},
		}
		update := bson.M{
			"$set": bson.M{
				"status":          int(adapters.OutboxProcessing),
				"last_attempt_at": now,
			},
			"$inc": bson.M{"attempts": 1},
		}
		opts := options.FindOneAndUpdate().
			SetSort(bson.D{{Key: "scheduled_at", Value: 1}}).
			SetReturnDocument(options.After)
		var doc outboxDoc
		err := s.coll.FindOneAndUpdate(ctx, filter, update, opts).Decode(&doc)
		if errors.Is(err, mongo.ErrNoDocuments) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("mink/mongodb/outbox: failed to fetch pending message: %w", err)
		}
		messages = append(messages, doc.toMessage())
	}
	return messages, nil
}

// MarkCompleted marks messages as successfully delivered.
func (s *OutboxStore) MarkCompleted(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	now := time.Now().UTC()
	_, err := s.coll.UpdateMany(ctx, bson.M{"_id": bson.M{"$in": ids}}, bson.M{"$set": bson.M{"status": int(adapters.OutboxCompleted), "processed_at": now}})
	return err
}

// MarkFailed marks a message as failed.
func (s *OutboxStore) MarkFailed(ctx context.Context, id string, lastErr error) error {
	errMsg := ""
	if lastErr != nil {
		errMsg = lastErr.Error()
	}
	res, err := s.coll.UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": bson.M{"status": int(adapters.OutboxFailed), "last_error": errMsg}})
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return adapters.ErrOutboxMessageNotFound
	}
	return nil
}

// RetryFailed resets eligible failed messages to pending.
func (s *OutboxStore) RetryFailed(ctx context.Context, maxAttempts int) (int64, error) {
	res, err := s.coll.UpdateMany(ctx,
		bson.M{"status": int(adapters.OutboxFailed), "attempts": bson.M{"$lt": maxAttempts}},
		bson.M{"$set": bson.M{"status": int(adapters.OutboxPending)}})
	if err != nil {
		return 0, err
	}
	return res.ModifiedCount, nil
}

// MoveToDeadLetter transitions exhausted messages to dead letter.
func (s *OutboxStore) MoveToDeadLetter(ctx context.Context, maxAttempts int) (int64, error) {
	res, err := s.coll.UpdateMany(ctx,
		bson.M{"status": int(adapters.OutboxFailed), "attempts": bson.M{"$gte": maxAttempts}},
		bson.M{"$set": bson.M{"status": int(adapters.OutboxDeadLetter)}})
	if err != nil {
		return 0, err
	}
	return res.ModifiedCount, nil
}

// GetDeadLetterMessages retrieves dead-lettered messages.
func (s *OutboxStore) GetDeadLetterMessages(ctx context.Context, limit int) ([]*adapters.OutboxMessage, error) {
	if limit <= 0 {
		limit = 100
	}
	cursor, err := s.coll.Find(ctx,
		bson.M{"status": int(adapters.OutboxDeadLetter)},
		options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}}).SetLimit(int64(limit)))
	if err != nil {
		return nil, err
	}
	defer func() { _ = cursor.Close(ctx) }()
	return scanOutboxMessages(ctx, cursor)
}

// Cleanup removes old completed messages.
func (s *OutboxStore) Cleanup(ctx context.Context, olderThan time.Duration) (int64, error) {
	cutoff := time.Now().Add(-olderThan).UTC()
	res, err := s.coll.DeleteMany(ctx, bson.M{
		"status":       int(adapters.OutboxCompleted),
		"processed_at": bson.M{"$lt": cutoff},
	})
	if err != nil {
		return 0, err
	}
	return res.DeletedCount, nil
}

// Close releases resources held by the store.
func (s *OutboxStore) Close() error {
	return nil
}

func scanOutboxMessages(ctx context.Context, cursor *mongo.Cursor) ([]*adapters.OutboxMessage, error) {
	var messages []*adapters.OutboxMessage
	for cursor.Next(ctx) {
		var doc outboxDoc
		if err := cursor.Decode(&doc); err != nil {
			return nil, err
		}
		messages = append(messages, doc.toMessage())
	}
	return messages, cursor.Err()
}

func outboxDocFromMessage(msg *adapters.OutboxMessage) outboxDoc {
	return outboxDoc{
		ID:            msg.ID,
		AggregateID:   msg.AggregateID,
		EventType:     msg.EventType,
		Destination:   msg.Destination,
		Payload:       bson.Binary{Subtype: bson.TypeBinaryGeneric, Data: msg.Payload},
		Headers:       msg.Headers,
		Status:        int(msg.Status),
		Attempts:      msg.Attempts,
		MaxAttempts:   msg.MaxAttempts,
		LastError:     msg.LastError,
		ScheduledAt:   msg.ScheduledAt,
		LastAttemptAt: msg.LastAttemptAt,
		ProcessedAt:   msg.ProcessedAt,
		CreatedAt:     msg.CreatedAt,
	}
}

func (d outboxDoc) toMessage() *adapters.OutboxMessage {
	return &adapters.OutboxMessage{
		ID:            d.ID,
		AggregateID:   d.AggregateID,
		EventType:     d.EventType,
		Destination:   d.Destination,
		Payload:       d.Payload.Data,
		Headers:       d.Headers,
		Status:        adapters.OutboxStatus(d.Status),
		Attempts:      d.Attempts,
		MaxAttempts:   d.MaxAttempts,
		LastError:     d.LastError,
		ScheduledAt:   d.ScheduledAt,
		LastAttemptAt: d.LastAttemptAt,
		ProcessedAt:   d.ProcessedAt,
		CreatedAt:     d.CreatedAt,
	}
}
