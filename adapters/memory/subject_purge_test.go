package memory

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go-mink.dev/adapters"
)

func TestAuditStore_DeleteAuditBySubject(t *testing.T) {
	ctx := context.Background()
	s := NewAuditStore()
	require.NoError(t, s.Append(ctx, &adapters.AuditEntry{ID: "1", Actor: "u1", Timestamp: time.Now()}))
	require.NoError(t, s.Append(ctx, &adapters.AuditEntry{ID: "2", AggregateID: "u1", Timestamp: time.Now()}))
	require.NoError(t, s.Append(ctx, &adapters.AuditEntry{ID: "3", Actor: "u2", Timestamp: time.Now()}))

	n, err := s.DeleteAuditBySubject(ctx, "u1") // actor OR aggregate_id == u1
	require.NoError(t, err)
	assert.Equal(t, int64(2), n)
	assert.Equal(t, 1, s.Len())

	n, err = s.DeleteAuditBySubject(ctx, "")
	require.NoError(t, err)
	assert.Equal(t, int64(0), n)
}

func TestSagaStore_DeleteSagasBySubject(t *testing.T) {
	ctx := context.Background()
	s := NewSagaStore()
	require.NoError(t, s.Save(ctx, &adapters.SagaState{ID: "s1", CorrelationID: "u1"}))
	require.NoError(t, s.Save(ctx, &adapters.SagaState{ID: "s2", CorrelationID: "u1"}))
	require.NoError(t, s.Save(ctx, &adapters.SagaState{ID: "s3", CorrelationID: "u2"}))

	n, err := s.DeleteSagasBySubject(ctx, "u1")
	require.NoError(t, err)
	assert.Equal(t, int64(2), n)

	_, err = s.Load(ctx, "s1")
	assert.Error(t, err)
	got, err := s.Load(ctx, "s3")
	require.NoError(t, err)
	assert.Equal(t, "u2", got.CorrelationID)

	n, err = s.DeleteSagasBySubject(ctx, "")
	require.NoError(t, err)
	assert.Equal(t, int64(0), n)
}

func TestOutboxStore_DeleteOutboxBySubject(t *testing.T) {
	ctx := context.Background()
	s := NewOutboxStore()
	require.NoError(t, s.Schedule(ctx, []*adapters.OutboxMessage{
		{AggregateID: "u1", EventType: "E", Destination: "webhook:x", Payload: []byte("{}")},
		{AggregateID: "u1", EventType: "E", Destination: "webhook:x", Payload: []byte("{}")},
		{AggregateID: "u2", EventType: "E", Destination: "webhook:x", Payload: []byte("{}")},
	}))

	n, err := s.DeleteOutboxBySubject(ctx, "u1")
	require.NoError(t, err)
	assert.Equal(t, int64(2), n)
	assert.Equal(t, 1, s.Count())

	n, err = s.DeleteOutboxBySubject(ctx, "")
	require.NoError(t, err)
	assert.Equal(t, int64(0), n)
}

func TestIdempotencyStore_DeleteIdempotencyBySubject(t *testing.T) {
	ctx := context.Background()
	s := NewIdempotencyStore()
	exp := time.Now().Add(time.Hour)
	require.NoError(t, s.Store(ctx, &adapters.IdempotencyRecord{Key: "k1", AggregateID: "u1", ProcessedAt: time.Now(), ExpiresAt: exp}))
	require.NoError(t, s.Store(ctx, &adapters.IdempotencyRecord{Key: "k2", AggregateID: "u2", ProcessedAt: time.Now(), ExpiresAt: exp}))

	n, err := s.DeleteIdempotencyBySubject(ctx, "u1")
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)
	assert.Equal(t, 1, s.Len())

	n, err = s.DeleteIdempotencyBySubject(ctx, "")
	require.NoError(t, err)
	assert.Equal(t, int64(0), n)
}
