package postgres

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go-mink.dev/adapters"
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
		_ = adapter.Close()
	})

	return store, ctx
}

func TestPostgresOutboxStore_DeleteBySubject(t *testing.T) {
	store, ctx := setupOutboxStore(t)
	require.NoError(t, store.Schedule(ctx, []*adapters.OutboxMessage{
		{AggregateID: "u1", EventType: "E", Destination: "webhook:x", Payload: []byte("{}"), MaxAttempts: 5},
		{AggregateID: "u1", EventType: "E", Destination: "webhook:x", Payload: []byte("{}"), MaxAttempts: 5},
		{AggregateID: "u2", EventType: "E", Destination: "webhook:x", Payload: []byte("{}"), MaxAttempts: 5},
	}))

	n, err := store.DeleteOutboxBySubject(ctx, "u1")
	require.NoError(t, err)
	assert.Equal(t, int64(2), n)

	n, err = store.DeleteOutboxBySubject(ctx, "")
	require.NoError(t, err)
	assert.Equal(t, int64(0), n, "empty subject is a no-op")
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

// TestPostgresOutboxStore_ReclaimStale verifies that a message stuck in the
// processing state is reclaimed only once it exceeds the staleness threshold, is
// left alone while still fresh, and becomes fetchable again afterwards without
// duplication.
func TestPostgresOutboxStore_ReclaimStale(t *testing.T) {
	store, ctx := setupOutboxStore(t)

	require.NoError(t, store.Schedule(ctx, []*adapters.OutboxMessage{
		{AggregateID: "agg-stale", EventType: "E", Destination: "kafka:t", Payload: []byte(`{}`), MaxAttempts: 5},
	}))

	// FetchPending moves it into Processing and stamps last_attempt_at = now.
	fetched, err := store.FetchPending(ctx, 10)
	require.NoError(t, err)
	require.Len(t, fetched, 1)
	require.Equal(t, adapters.OutboxProcessing, fetched[0].Status)

	// A fresh in-flight message must NOT be reclaimed by a large threshold.
	reclaimed, err := store.ReclaimStale(ctx, time.Hour)
	require.NoError(t, err)
	assert.Equal(t, int64(0), reclaimed, "a just-fetched message must not be reclaimed")

	// It is still invisible to a normal fetch (still Processing).
	none, err := store.FetchPending(ctx, 10)
	require.NoError(t, err)
	assert.Empty(t, none)

	// Backdate the in-flight message deterministically (avoids sleep-based
	// flakiness on slow/loaded CI), then it is reclaimed back to pending.
	_, err = store.db.ExecContext(ctx,
		"UPDATE "+store.fullTableName()+" SET last_attempt_at = NOW() - make_interval(secs => 3600)")
	require.NoError(t, err)
	reclaimed, err = store.ReclaimStale(ctx, time.Minute)
	require.NoError(t, err)
	assert.Equal(t, int64(1), reclaimed, "a stale processing message must be reclaimed")

	// And is fetchable again — exactly one row, no duplication.
	refetched, err := store.FetchPending(ctx, 10)
	require.NoError(t, err)
	require.Len(t, refetched, 1)
	assert.Equal(t, "agg-stale", refetched[0].AggregateID)
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

	// Cleanup should remove the just-completed message. Use a cutoff safely in the
	// future (negative olderThan) rather than 0: processed_at is stamped by the server
	// clock (NOW()) while Cleanup's cutoff is the client clock, so a 0 threshold is a
	// clock-tie coin-flip that makes this test flaky.
	cleaned, err := store.Cleanup(ctx, -time.Minute)
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
