package postgres

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/AshkanYarmoradi/go-mink/adapters"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupOutboxStore(t *testing.T) (*OutboxStore, context.Context) {
	t.Helper()

	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	connStr := os.Getenv("TEST_DATABASE_URL")
	if connStr == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}

	adapter, err := NewAdapter(connStr)
	require.NoError(t, err)

	ctx := context.Background()

	store := NewOutboxStoreFromAdapter(adapter, WithOutboxTableName("mink_outbox_test"))
	err = store.Initialize(ctx)
	require.NoError(t, err)

	// Clean up before test
	_, err = store.db.ExecContext(ctx, "DELETE FROM "+store.fullTableName())
	require.NoError(t, err)

	t.Cleanup(func() {
		adapter.Close()
	})

	return store, ctx
}

func TestPostgresOutboxStore_ScheduleAndFetch(t *testing.T) {
	store, ctx := setupOutboxStore(t)

	messages := []*adapters.OutboxMessage{
		{
			AggregateID: "order-123",
			EventType:   "OrderCreated",
			Destination: "webhook:https://example.com",
			Payload:     []byte(`{"id":"123"}`),
			Headers:     map[string]string{"key": "value"},
			MaxAttempts: 5,
		},
		{
			AggregateID: "order-456",
			EventType:   "OrderShipped",
			Destination: "kafka:orders",
			Payload:     []byte(`{"id":"456"}`),
			MaxAttempts: 3,
		},
	}

	err := store.Schedule(ctx, messages)
	require.NoError(t, err)

	// Verify IDs were assigned
	for _, msg := range messages {
		assert.NotEmpty(t, msg.ID)
	}

	// Fetch pending
	fetched, err := store.FetchPending(ctx, 10)
	require.NoError(t, err)
	assert.Len(t, fetched, 2)

	for _, msg := range fetched {
		assert.Equal(t, adapters.OutboxProcessing, msg.Status)
		assert.Equal(t, 1, msg.Attempts)
		assert.NotNil(t, msg.LastAttemptAt)
	}

	// Fetch again should return empty
	fetched2, err := store.FetchPending(ctx, 10)
	require.NoError(t, err)
	assert.Len(t, fetched2, 0)
}

func TestPostgresOutboxStore_MarkCompletedAndFailed(t *testing.T) {
	store, ctx := setupOutboxStore(t)

	err := store.Schedule(ctx, []*adapters.OutboxMessage{
		{AggregateID: "order-1", EventType: "OrderCreated", Destination: "webhook:x", Payload: []byte(`{}`)},
		{AggregateID: "order-2", EventType: "OrderCreated", Destination: "webhook:x", Payload: []byte(`{}`)},
	})
	require.NoError(t, err)

	fetched, err := store.FetchPending(ctx, 10)
	require.NoError(t, err)
	require.Len(t, fetched, 2)

	// Mark first as completed
	err = store.MarkCompleted(ctx, []string{fetched[0].ID})
	require.NoError(t, err)

	// Mark second as failed
	err = store.MarkFailed(ctx, fetched[1].ID, errors.New("connection refused"))
	require.NoError(t, err)

	// Verify states
	msg1, err := store.get(ctx, fetched[0].ID)
	require.NoError(t, err)
	assert.Equal(t, adapters.OutboxCompleted, msg1.Status)
	assert.NotNil(t, msg1.ProcessedAt)

	msg2, err := store.get(ctx, fetched[1].ID)
	require.NoError(t, err)
	assert.Equal(t, adapters.OutboxFailed, msg2.Status)
	assert.Equal(t, "connection refused", msg2.LastError)
}

func TestPostgresOutboxStore_RetryAndDeadLetter(t *testing.T) {
	store, ctx := setupOutboxStore(t)

	err := store.Schedule(ctx, []*adapters.OutboxMessage{
		{AggregateID: "order-1", EventType: "OrderCreated", Destination: "webhook:x", Payload: []byte(`{}`), MaxAttempts: 2},
	})
	require.NoError(t, err)

	// Fetch, fail
	fetched, err := store.FetchPending(ctx, 10)
	require.NoError(t, err)
	err = store.MarkFailed(ctx, fetched[0].ID, errors.New("error"))
	require.NoError(t, err)

	// Retry (attempts=1 < maxAttempts=2, but we use global maxAttempts param)
	retried, err := store.RetryFailed(ctx, 5)
	require.NoError(t, err)
	assert.Equal(t, int64(1), retried)

	// Fetch and fail again
	fetched, err = store.FetchPending(ctx, 10)
	require.NoError(t, err)
	require.Len(t, fetched, 1)
	err = store.MarkFailed(ctx, fetched[0].ID, errors.New("error again"))
	require.NoError(t, err)

	// Now move to dead letter (attempts=2 >= maxAttempts=2)
	moved, err := store.MoveToDeadLetter(ctx, 2)
	require.NoError(t, err)
	assert.Equal(t, int64(1), moved)

	// Verify dead letter
	dlMsgs, err := store.GetDeadLetterMessages(ctx, 10)
	require.NoError(t, err)
	assert.Len(t, dlMsgs, 1)
	assert.Equal(t, adapters.OutboxDeadLetter, dlMsgs[0].Status)
}

func TestPostgresOutboxStore_Cleanup(t *testing.T) {
	store, ctx := setupOutboxStore(t)

	err := store.Schedule(ctx, []*adapters.OutboxMessage{
		{AggregateID: "order-1", EventType: "OrderCreated", Destination: "webhook:x", Payload: []byte(`{}`)},
	})
	require.NoError(t, err)

	fetched, err := store.FetchPending(ctx, 10)
	require.NoError(t, err)
	err = store.MarkCompleted(ctx, []string{fetched[0].ID})
	require.NoError(t, err)

	// Cleanup with 0 threshold should remove it
	cleaned, err := store.Cleanup(ctx, 0)
	require.NoError(t, err)
	assert.Equal(t, int64(1), cleaned)
}

func TestPostgresOutboxStore_Cleanup_KeepsRecent(t *testing.T) {
	store, ctx := setupOutboxStore(t)

	err := store.Schedule(ctx, []*adapters.OutboxMessage{
		{AggregateID: "order-1", EventType: "OrderCreated", Destination: "webhook:x", Payload: []byte(`{}`)},
	})
	require.NoError(t, err)

	fetched, err := store.FetchPending(ctx, 10)
	require.NoError(t, err)
	err = store.MarkCompleted(ctx, []string{fetched[0].ID})
	require.NoError(t, err)

	// Cleanup with long threshold should keep it
	cleaned, err := store.Cleanup(ctx, 24*time.Hour)
	require.NoError(t, err)
	assert.Equal(t, int64(0), cleaned)
}

func TestPostgresOutboxStore_MarkFailed_NotFound(t *testing.T) {
	store, ctx := setupOutboxStore(t)

	err := store.MarkFailed(ctx, "00000000-0000-0000-0000-000000000000", errors.New("test"))
	assert.True(t, errors.Is(err, adapters.ErrOutboxMessageNotFound))
}

func TestPostgresOutboxStore_Headers(t *testing.T) {
	store, ctx := setupOutboxStore(t)

	headers := map[string]string{
		"correlation-id": "abc-123",
		"event-type":     "OrderCreated",
	}

	err := store.Schedule(ctx, []*adapters.OutboxMessage{
		{
			AggregateID: "order-1",
			EventType:   "OrderCreated",
			Destination: "webhook:x",
			Payload:     []byte(`{}`),
			Headers:     headers,
		},
	})
	require.NoError(t, err)

	fetched, err := store.FetchPending(ctx, 10)
	require.NoError(t, err)
	require.Len(t, fetched, 1)
	assert.Equal(t, "abc-123", fetched[0].Headers["correlation-id"])
	assert.Equal(t, "OrderCreated", fetched[0].Headers["event-type"])
}
