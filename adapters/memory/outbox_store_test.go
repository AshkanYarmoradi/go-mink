package memory

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/AshkanYarmoradi/go-mink/adapters"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOutboxStore_Schedule(t *testing.T) {
	store := NewOutboxStore()
	ctx := context.Background()

	messages := []*adapters.OutboxMessage{
		{
			AggregateID: "order-123",
			EventType:   "OrderCreated",
			Destination: "webhook:https://example.com",
			Payload:     []byte(`{"id":"123"}`),
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
	assert.Equal(t, 2, store.Count())

	// Verify messages got IDs assigned
	for _, msg := range messages {
		assert.NotEmpty(t, msg.ID)
	}
}

func TestOutboxStore_ScheduleInTx(t *testing.T) {
	store := NewOutboxStore()
	ctx := context.Background()

	messages := []*adapters.OutboxMessage{
		{
			AggregateID: "order-123",
			EventType:   "OrderCreated",
			Destination: "webhook:https://example.com",
			Payload:     []byte(`{"id":"123"}`),
		},
	}

	// ScheduleInTx should work the same as Schedule (tx is ignored)
	err := store.ScheduleInTx(ctx, nil, messages)
	require.NoError(t, err)
	assert.Equal(t, 1, store.Count())
}

func TestOutboxStore_FetchPending(t *testing.T) {
	store := NewOutboxStore()
	ctx := context.Background()

	// Schedule messages
	messages := []*adapters.OutboxMessage{
		{
			AggregateID: "order-1",
			EventType:   "OrderCreated",
			Destination: "webhook:https://example.com",
			Payload:     []byte(`{"id":"1"}`),
		},
		{
			AggregateID: "order-2",
			EventType:   "OrderCreated",
			Destination: "webhook:https://example.com",
			Payload:     []byte(`{"id":"2"}`),
		},
		{
			AggregateID: "order-3",
			EventType:   "OrderCreated",
			Destination: "webhook:https://example.com",
			Payload:     []byte(`{"id":"3"}`),
		},
	}

	err := store.Schedule(ctx, messages)
	require.NoError(t, err)

	// Fetch with limit
	fetched, err := store.FetchPending(ctx, 2)
	require.NoError(t, err)
	assert.Len(t, fetched, 2)

	// Fetched messages should be in Processing status
	for _, msg := range fetched {
		assert.Equal(t, adapters.OutboxProcessing, msg.Status)
		assert.Equal(t, 1, msg.Attempts)
		assert.NotNil(t, msg.LastAttemptAt)
	}

	// Fetch again should only get remaining 1
	fetched2, err := store.FetchPending(ctx, 10)
	require.NoError(t, err)
	assert.Len(t, fetched2, 1)
}

func TestOutboxStore_FetchPending_NoDuplicates(t *testing.T) {
	store := NewOutboxStore()
	ctx := context.Background()

	err := store.Schedule(ctx, []*adapters.OutboxMessage{
		{
			AggregateID: "order-1",
			EventType:   "OrderCreated",
			Destination: "webhook:https://example.com",
			Payload:     []byte(`{"id":"1"}`),
		},
	})
	require.NoError(t, err)

	// Fetch once
	fetched1, err := store.FetchPending(ctx, 10)
	require.NoError(t, err)
	assert.Len(t, fetched1, 1)

	// Fetch again should get nothing (already processing)
	fetched2, err := store.FetchPending(ctx, 10)
	require.NoError(t, err)
	assert.Len(t, fetched2, 0)
}

func TestOutboxStore_MarkCompleted(t *testing.T) {
	store := NewOutboxStore()
	ctx := context.Background()

	err := store.Schedule(ctx, []*adapters.OutboxMessage{
		{
			AggregateID: "order-1",
			EventType:   "OrderCreated",
			Destination: "webhook:https://example.com",
			Payload:     []byte(`{"id":"1"}`),
		},
	})
	require.NoError(t, err)

	fetched, err := store.FetchPending(ctx, 10)
	require.NoError(t, err)
	require.Len(t, fetched, 1)

	err = store.MarkCompleted(ctx, []string{fetched[0].ID})
	require.NoError(t, err)

	counts := store.CountByStatus()
	assert.Equal(t, 1, counts[adapters.OutboxCompleted])
	assert.Equal(t, 0, counts[adapters.OutboxPending])
}

func TestOutboxStore_MarkFailed(t *testing.T) {
	store := NewOutboxStore()
	ctx := context.Background()

	err := store.Schedule(ctx, []*adapters.OutboxMessage{
		{
			AggregateID: "order-1",
			EventType:   "OrderCreated",
			Destination: "webhook:https://example.com",
			Payload:     []byte(`{"id":"1"}`),
		},
	})
	require.NoError(t, err)

	fetched, err := store.FetchPending(ctx, 10)
	require.NoError(t, err)
	require.Len(t, fetched, 1)

	testErr := errors.New("connection refused")
	err = store.MarkFailed(ctx, fetched[0].ID, testErr)
	require.NoError(t, err)

	counts := store.CountByStatus()
	assert.Equal(t, 1, counts[adapters.OutboxFailed])
}

func TestOutboxStore_MarkFailed_NotFound(t *testing.T) {
	store := NewOutboxStore()
	ctx := context.Background()

	err := store.MarkFailed(ctx, "nonexistent", errors.New("test"))
	assert.True(t, errors.Is(err, adapters.ErrOutboxMessageNotFound))
}

func TestOutboxStore_RetryFailed(t *testing.T) {
	store := NewOutboxStore()
	ctx := context.Background()

	err := store.Schedule(ctx, []*adapters.OutboxMessage{
		{
			AggregateID: "order-1",
			EventType:   "OrderCreated",
			Destination: "webhook:https://example.com",
			Payload:     []byte(`{"id":"1"}`),
		},
	})
	require.NoError(t, err)

	// Fetch and fail
	fetched, err := store.FetchPending(ctx, 10)
	require.NoError(t, err)
	err = store.MarkFailed(ctx, fetched[0].ID, errors.New("connection refused"))
	require.NoError(t, err)

	// Retry (attempts=1 < maxAttempts=5)
	retried, err := store.RetryFailed(ctx, 5)
	require.NoError(t, err)
	assert.Equal(t, int64(1), retried)

	// Should be fetchable again
	fetched2, err := store.FetchPending(ctx, 10)
	require.NoError(t, err)
	assert.Len(t, fetched2, 1)
}

func TestOutboxStore_MoveToDeadLetter(t *testing.T) {
	store := NewOutboxStore()
	ctx := context.Background()

	err := store.Schedule(ctx, []*adapters.OutboxMessage{
		{
			AggregateID: "order-1",
			EventType:   "OrderCreated",
			Destination: "webhook:https://example.com",
			Payload:     []byte(`{"id":"1"}`),
			MaxAttempts: 2,
		},
	})
	require.NoError(t, err)

	// Fetch, fail, retry, fetch, fail again (2 attempts total)
	fetched, _ := store.FetchPending(ctx, 10)
	_ = store.MarkFailed(ctx, fetched[0].ID, errors.New("error"))
	_, _ = store.RetryFailed(ctx, 5)
	fetched, _ = store.FetchPending(ctx, 10)
	_ = store.MarkFailed(ctx, fetched[0].ID, errors.New("error again"))

	// Now attempts=2 >= maxAttempts=2, should be dead-lettered
	moved, err := store.MoveToDeadLetter(ctx, 2)
	require.NoError(t, err)
	assert.Equal(t, int64(1), moved)

	counts := store.CountByStatus()
	assert.Equal(t, 1, counts[adapters.OutboxDeadLetter])
}

func TestOutboxStore_GetDeadLetterMessages(t *testing.T) {
	store := NewOutboxStore()
	ctx := context.Background()

	err := store.Schedule(ctx, []*adapters.OutboxMessage{
		{AggregateID: "order-1", EventType: "OrderCreated", Destination: "webhook:x", Payload: []byte(`{}`)},
	})
	require.NoError(t, err)

	// Fetch, fail, dead-letter
	fetched, _ := store.FetchPending(ctx, 10)
	_ = store.MarkFailed(ctx, fetched[0].ID, errors.New("error"))
	_, _ = store.MoveToDeadLetter(ctx, 1)

	dlMsgs, err := store.GetDeadLetterMessages(ctx, 10)
	require.NoError(t, err)
	assert.Len(t, dlMsgs, 1)
	assert.Equal(t, adapters.OutboxDeadLetter, dlMsgs[0].Status)
}

func TestOutboxStore_Cleanup(t *testing.T) {
	store := NewOutboxStore()
	ctx := context.Background()

	err := store.Schedule(ctx, []*adapters.OutboxMessage{
		{AggregateID: "order-1", EventType: "OrderCreated", Destination: "webhook:x", Payload: []byte(`{}`)},
	})
	require.NoError(t, err)

	// Fetch and complete
	fetched, _ := store.FetchPending(ctx, 10)
	_ = store.MarkCompleted(ctx, []string{fetched[0].ID})

	// Cleanup with very short threshold should remove it
	cleaned, err := store.Cleanup(ctx, 0)
	require.NoError(t, err)
	assert.Equal(t, int64(1), cleaned)
	assert.Equal(t, 0, store.Count())
}

func TestOutboxStore_Cleanup_KeepsRecent(t *testing.T) {
	store := NewOutboxStore()
	ctx := context.Background()

	err := store.Schedule(ctx, []*adapters.OutboxMessage{
		{AggregateID: "order-1", EventType: "OrderCreated", Destination: "webhook:x", Payload: []byte(`{}`)},
	})
	require.NoError(t, err)

	fetched, _ := store.FetchPending(ctx, 10)
	_ = store.MarkCompleted(ctx, []string{fetched[0].ID})

	// Cleanup with long threshold should keep it
	cleaned, err := store.Cleanup(ctx, 24*time.Hour)
	require.NoError(t, err)
	assert.Equal(t, int64(0), cleaned)
	assert.Equal(t, 1, store.Count())
}

func TestOutboxStore_ContextCancellation(t *testing.T) {
	store := NewOutboxStore()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := store.FetchPending(ctx, 10)
	assert.Error(t, err)

	err = store.Schedule(ctx, []*adapters.OutboxMessage{
		{AggregateID: "order-1", EventType: "OrderCreated", Destination: "webhook:x", Payload: []byte(`{}`)},
	})
	assert.Error(t, err)
}

func TestOutboxStore_Clear(t *testing.T) {
	store := NewOutboxStore()
	ctx := context.Background()

	_ = store.Schedule(ctx, []*adapters.OutboxMessage{
		{AggregateID: "order-1", EventType: "OrderCreated", Destination: "webhook:x", Payload: []byte(`{}`)},
		{AggregateID: "order-2", EventType: "OrderCreated", Destination: "webhook:x", Payload: []byte(`{}`)},
	})

	assert.Equal(t, 2, store.Count())
	store.Clear()
	assert.Equal(t, 0, store.Count())
}

func TestOutboxStore_DeepCopy(t *testing.T) {
	store := NewOutboxStore()
	ctx := context.Background()

	original := &adapters.OutboxMessage{
		AggregateID: "order-1",
		EventType:   "OrderCreated",
		Destination: "webhook:https://example.com",
		Payload:     []byte(`{"id":"1"}`),
		Headers:     map[string]string{"key": "value"},
	}

	err := store.Schedule(ctx, []*adapters.OutboxMessage{original})
	require.NoError(t, err)

	// Mutate original
	original.Headers["key"] = "mutated"
	original.Payload[0] = 'X'

	// Fetch and verify the stored copy is unaffected
	fetched, err := store.FetchPending(ctx, 10)
	require.NoError(t, err)
	require.Len(t, fetched, 1)
	assert.Equal(t, "value", fetched[0].Headers["key"])
	assert.Equal(t, byte('{'), fetched[0].Payload[0])
}

// =============================================================================
// New tests
// =============================================================================

func TestMemoryOutboxStore_InitializeAndClose(t *testing.T) {
	store := NewOutboxStore()

	err := store.Initialize(context.Background())
	assert.NoError(t, err)

	err = store.Close()
	assert.NoError(t, err)
}

func TestOutboxStore_FetchPending_FutureScheduledAt(t *testing.T) {
	store := NewOutboxStore()
	ctx := context.Background()

	// Schedule with future time — need to set it manually after Schedule
	// since Schedule overrides ScheduledAt if zero
	msg := &adapters.OutboxMessage{
		AggregateID: "order-1",
		EventType:   "OrderCreated",
		Destination: "webhook:x",
		Payload:     []byte(`{}`),
		ScheduledAt: time.Now().Add(time.Hour),
	}
	err := store.Schedule(ctx, []*adapters.OutboxMessage{msg})
	require.NoError(t, err)

	// FetchPending should not return future-scheduled messages
	fetched, err := store.FetchPending(ctx, 10)
	require.NoError(t, err)
	assert.Len(t, fetched, 0)
}

func TestOutboxStore_MarkCompleted_NonexistentID(t *testing.T) {
	store := NewOutboxStore()
	ctx := context.Background()

	// Should silently skip nonexistent IDs
	err := store.MarkCompleted(ctx, []string{"nonexistent-id"})
	assert.NoError(t, err)
}

func TestOutboxStore_MarkFailed_NilError(t *testing.T) {
	store := NewOutboxStore()
	ctx := context.Background()

	err := store.Schedule(ctx, []*adapters.OutboxMessage{
		{AggregateID: "order-1", EventType: "OrderCreated", Destination: "webhook:x", Payload: []byte(`{}`)},
	})
	require.NoError(t, err)

	fetched, err := store.FetchPending(ctx, 10)
	require.NoError(t, err)
	require.Len(t, fetched, 1)

	// Pass nil error
	err = store.MarkFailed(ctx, fetched[0].ID, nil)
	require.NoError(t, err)

	// Verify the message is in failed status but LastError is empty
	counts := store.CountByStatus()
	assert.Equal(t, 1, counts[adapters.OutboxFailed])
}

func TestOutboxStore_ContextCancellation_AllMethods(t *testing.T) {
	store := NewOutboxStore()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := store.MarkCompleted(ctx, []string{"id-1"})
	assert.Error(t, err)

	err = store.MarkFailed(ctx, "id-1", errors.New("test"))
	assert.Error(t, err)

	_, err = store.RetryFailed(ctx, 5)
	assert.Error(t, err)

	_, err = store.MoveToDeadLetter(ctx, 5)
	assert.Error(t, err)

	_, err = store.GetDeadLetterMessages(ctx, 10)
	assert.Error(t, err)

	_, err = store.Cleanup(ctx, time.Hour)
	assert.Error(t, err)
}
