package mink

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/AshkanYarmoradi/go-mink/adapters"
	"github.com/AshkanYarmoradi/go-mink/adapters/memory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOutboxRoute_MatchesEvent(t *testing.T) {
	tests := []struct {
		name      string
		route     OutboxRoute
		eventType string
		want      bool
	}{
		{
			name:      "empty event types matches all",
			route:     OutboxRoute{Destination: "webhook:x"},
			eventType: "OrderCreated",
			want:      true,
		},
		{
			name:      "matches specific event type",
			route:     OutboxRoute{EventTypes: []string{"OrderCreated", "OrderShipped"}, Destination: "webhook:x"},
			eventType: "OrderCreated",
			want:      true,
		},
		{
			name:      "does not match unregistered event type",
			route:     OutboxRoute{EventTypes: []string{"OrderCreated"}, Destination: "webhook:x"},
			eventType: "OrderShipped",
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.route.matchesEvent(tt.eventType)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestNewEventStoreWithOutbox(t *testing.T) {
	adapter := memory.NewAdapter()
	store := New(adapter)
	outboxStore := memory.NewOutboxStore()

	routes := []OutboxRoute{
		{EventTypes: []string{"OrderCreated"}, Destination: "webhook:https://example.com"},
	}

	esWithOutbox := NewEventStoreWithOutbox(store, outboxStore, routes,
		WithOutboxMaxAttempts(10),
	)

	assert.NotNil(t, esWithOutbox)
	assert.Equal(t, store, esWithOutbox.Store())
	assert.NotNil(t, esWithOutbox.OutboxStore())
}

func TestEventStoreWithOutbox_Append(t *testing.T) {
	adapter := memory.NewAdapter()
	_ = adapter.Initialize(context.Background())
	store := New(adapter)

	// Register event type
	type OrderCreated struct {
		OrderID string `json:"orderId"`
	}
	store.RegisterEvents(OrderCreated{})

	outboxStore := memory.NewOutboxStore()

	routes := []OutboxRoute{
		{EventTypes: []string{"OrderCreated"}, Destination: "webhook:https://example.com"},
	}

	esWithOutbox := NewEventStoreWithOutbox(store, outboxStore, routes)
	ctx := context.Background()

	// Append events (non-atomic fallback since memory adapter doesn't implement OutboxAppender)
	err := esWithOutbox.Append(ctx, "Order-123", []interface{}{
		OrderCreated{OrderID: "123"},
	})
	require.NoError(t, err)

	// Verify event was stored
	events, err := store.Load(ctx, "Order-123")
	require.NoError(t, err)
	assert.Len(t, events, 1)

	// Verify outbox message was scheduled
	assert.Equal(t, 1, outboxStore.Count())

	// Fetch the outbox messages
	msgs, err := outboxStore.FetchPending(ctx, 10)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	assert.Equal(t, "webhook:https://example.com", msgs[0].Destination)
	assert.Equal(t, "OrderCreated", msgs[0].EventType)
	assert.Equal(t, "Order-123", msgs[0].AggregateID)
}

func TestEventStoreWithOutbox_Append_NoMatchingRoute(t *testing.T) {
	adapter := memory.NewAdapter()
	_ = adapter.Initialize(context.Background())
	store := New(adapter)

	type UserRegistered struct {
		UserID string `json:"userId"`
	}
	store.RegisterEvents(UserRegistered{})

	outboxStore := memory.NewOutboxStore()

	routes := []OutboxRoute{
		{EventTypes: []string{"OrderCreated"}, Destination: "webhook:https://example.com"},
	}

	esWithOutbox := NewEventStoreWithOutbox(store, outboxStore, routes)
	ctx := context.Background()

	err := esWithOutbox.Append(ctx, "User-456", []interface{}{
		UserRegistered{UserID: "456"},
	})
	require.NoError(t, err)

	// No outbox messages should be created (no matching route)
	assert.Equal(t, 0, outboxStore.Count())
}

func TestEventStoreWithOutbox_Append_ValidationErrors(t *testing.T) {
	adapter := memory.NewAdapter()
	store := New(adapter)
	outboxStore := memory.NewOutboxStore()

	esWithOutbox := NewEventStoreWithOutbox(store, outboxStore, nil)
	ctx := context.Background()

	err := esWithOutbox.Append(ctx, "", []interface{}{struct{}{}})
	assert.ErrorIs(t, err, ErrEmptyStreamID)

	err = esWithOutbox.Append(ctx, "Test-1", nil)
	assert.ErrorIs(t, err, ErrNoEvents)
}

func TestNoopOutboxMetrics(t *testing.T) {
	// Ensure noop metrics don't panic
	m := &noopOutboxMetrics{}
	m.RecordMessageProcessed("webhook", true)
	m.RecordMessageFailed("webhook")
	m.RecordMessageDeadLettered()
	m.RecordBatchDuration(0)
	m.RecordPendingMessages(0)
}

func TestOutboxStatus_Aliases(t *testing.T) {
	// Verify type aliases work correctly
	assert.Equal(t, adapters.OutboxPending, OutboxPending)
	assert.Equal(t, adapters.OutboxProcessing, OutboxProcessing)
	assert.Equal(t, adapters.OutboxCompleted, OutboxCompleted)
	assert.Equal(t, adapters.OutboxFailed, OutboxFailed)
	assert.Equal(t, adapters.OutboxDeadLetter, OutboxDeadLetter)
}

// =============================================================================
// Test aggregate for SaveAggregate tests
// =============================================================================

type outboxTestOrderCreated struct {
	OrderID string `json:"orderId"`
}

type outboxTestOrderAggregate struct {
	AggregateBase
	orderID string
}

func newOutboxTestOrderAggregate(id string) *outboxTestOrderAggregate {
	return &outboxTestOrderAggregate{
		AggregateBase: NewAggregateBase(id, "Order"),
	}
}

func (a *outboxTestOrderAggregate) ApplyEvent(event interface{}) error {
	switch e := event.(type) {
	case outboxTestOrderCreated:
		a.orderID = e.OrderID
	default:
		return fmt.Errorf("unknown event type: %T", event)
	}
	return nil
}

func (a *outboxTestOrderAggregate) CreateOrder(orderID string) {
	a.Apply(outboxTestOrderCreated{OrderID: orderID})
	a.orderID = orderID
}

// =============================================================================
// Mock OutboxAppender adapter for atomic path testing
// =============================================================================

type mockOutboxAppenderAdapter struct {
	*memory.MemoryAdapter
	appendWithOutboxCalled bool
	appendWithOutboxErr    error
	appendedOutboxMsgs     []*adapters.OutboxMessage
}

func newMockOutboxAppenderAdapter() *mockOutboxAppenderAdapter {
	return &mockOutboxAppenderAdapter{
		MemoryAdapter: memory.NewAdapter(),
	}
}

func (m *mockOutboxAppenderAdapter) AppendWithOutbox(ctx context.Context, streamID string, events []adapters.EventRecord, expectedVersion int64, outboxMessages []*adapters.OutboxMessage) ([]adapters.StoredEvent, error) {
	m.appendWithOutboxCalled = true
	m.appendedOutboxMsgs = outboxMessages
	if m.appendWithOutboxErr != nil {
		return nil, m.appendWithOutboxErr
	}
	return m.MemoryAdapter.Append(ctx, streamID, events, expectedVersion)
}

// Compile-time check
var _ adapters.OutboxAppender = (*mockOutboxAppenderAdapter)(nil)

// =============================================================================
// Mock OutboxStore for Schedule error testing
// =============================================================================

type failingOutboxStore struct {
	*memory.OutboxStore
	scheduleErr error
}

func (s *failingOutboxStore) Schedule(ctx context.Context, messages []*adapters.OutboxMessage) error {
	if s.scheduleErr != nil {
		return s.scheduleErr
	}
	return s.OutboxStore.Schedule(ctx, messages)
}

// =============================================================================
// Mock logger for testing
// =============================================================================

type recordingLogger struct {
	errors []string
	warns  []string
	infos  []string
}

func (l *recordingLogger) Debug(msg string, args ...interface{}) {}
func (l *recordingLogger) Info(msg string, args ...interface{})  { l.infos = append(l.infos, msg) }
func (l *recordingLogger) Warn(msg string, args ...interface{})  { l.warns = append(l.warns, msg) }
func (l *recordingLogger) Error(msg string, args ...interface{}) { l.errors = append(l.errors, msg) }

// =============================================================================
// SaveAggregate tests
// =============================================================================

func TestSaveAggregate_HappyPath(t *testing.T) {
	adapter := memory.NewAdapter()
	_ = adapter.Initialize(context.Background())
	store := New(adapter)
	store.RegisterEvents(outboxTestOrderCreated{})
	outboxStore := memory.NewOutboxStore()

	routes := []OutboxRoute{
		{EventTypes: []string{"outboxTestOrderCreated"}, Destination: "webhook:https://example.com"},
	}

	esWithOutbox := NewEventStoreWithOutbox(store, outboxStore, routes)
	ctx := context.Background()

	agg := newOutboxTestOrderAggregate("123")
	agg.CreateOrder("123")

	err := esWithOutbox.SaveAggregate(ctx, agg)
	require.NoError(t, err)

	// Verify events were stored
	events, err := store.LoadRaw(ctx, "Order-123", 0)
	require.NoError(t, err)
	assert.Len(t, events, 1)

	// Verify outbox message was scheduled
	assert.Equal(t, 1, outboxStore.Count())

	// Verify aggregate version was updated and events cleared
	assert.Equal(t, int64(1), agg.Version())
	assert.Empty(t, agg.UncommittedEvents())
}

func TestSaveAggregate_NilAggregate(t *testing.T) {
	adapter := memory.NewAdapter()
	store := New(adapter)
	outboxStore := memory.NewOutboxStore()

	esWithOutbox := NewEventStoreWithOutbox(store, outboxStore, nil)

	err := esWithOutbox.SaveAggregate(context.Background(), nil)
	assert.ErrorIs(t, err, ErrNilAggregate)
}

func TestSaveAggregate_NoUncommittedEvents(t *testing.T) {
	adapter := memory.NewAdapter()
	store := New(adapter)
	outboxStore := memory.NewOutboxStore()

	esWithOutbox := NewEventStoreWithOutbox(store, outboxStore, nil)

	agg := newOutboxTestOrderAggregate("123")
	// No events applied

	err := esWithOutbox.SaveAggregate(context.Background(), agg)
	assert.NoError(t, err)
	assert.Equal(t, 0, outboxStore.Count())
}

func TestSaveAggregate_NoMatchingRoute(t *testing.T) {
	adapter := memory.NewAdapter()
	_ = adapter.Initialize(context.Background())
	store := New(adapter)
	store.RegisterEvents(outboxTestOrderCreated{})
	outboxStore := memory.NewOutboxStore()

	routes := []OutboxRoute{
		{EventTypes: []string{"UserRegistered"}, Destination: "webhook:https://example.com"},
	}

	esWithOutbox := NewEventStoreWithOutbox(store, outboxStore, routes)
	ctx := context.Background()

	agg := newOutboxTestOrderAggregate("123")
	agg.CreateOrder("123")

	err := esWithOutbox.SaveAggregate(ctx, agg)
	require.NoError(t, err)

	// Events stored but no outbox messages
	events, err := store.LoadRaw(ctx, "Order-123", 0)
	require.NoError(t, err)
	assert.Len(t, events, 1)
	assert.Equal(t, 0, outboxStore.Count())
}

func TestSaveAggregate_AtomicPath(t *testing.T) {
	adapter := newMockOutboxAppenderAdapter()
	_ = adapter.Initialize(context.Background())
	store := New(adapter)
	store.RegisterEvents(outboxTestOrderCreated{})
	outboxStore := memory.NewOutboxStore()

	routes := []OutboxRoute{
		{EventTypes: []string{"outboxTestOrderCreated"}, Destination: "kafka:orders"},
	}

	esWithOutbox := NewEventStoreWithOutbox(store, outboxStore, routes)
	ctx := context.Background()

	agg := newOutboxTestOrderAggregate("456")
	agg.CreateOrder("456")

	err := esWithOutbox.SaveAggregate(ctx, agg)
	require.NoError(t, err)

	// Verify AppendWithOutbox was called
	assert.True(t, adapter.appendWithOutboxCalled)
	assert.Len(t, adapter.appendedOutboxMsgs, 1)
	assert.Equal(t, "kafka:orders", adapter.appendedOutboxMsgs[0].Destination)
}

func TestSaveAggregate_AtomicPath_Error(t *testing.T) {
	adapter := newMockOutboxAppenderAdapter()
	adapter.appendWithOutboxErr = errors.New("atomic write failed")
	_ = adapter.Initialize(context.Background())
	store := New(adapter)
	store.RegisterEvents(outboxTestOrderCreated{})
	outboxStore := memory.NewOutboxStore()

	routes := []OutboxRoute{
		{EventTypes: []string{"outboxTestOrderCreated"}, Destination: "kafka:orders"},
	}

	esWithOutbox := NewEventStoreWithOutbox(store, outboxStore, routes)
	ctx := context.Background()

	agg := newOutboxTestOrderAggregate("789")
	agg.CreateOrder("789")

	err := esWithOutbox.SaveAggregate(ctx, agg)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "atomic write failed")
}

func TestSaveAggregate_FallbackScheduleError(t *testing.T) {
	adapter := memory.NewAdapter()
	_ = adapter.Initialize(context.Background())
	store := New(adapter)
	store.RegisterEvents(outboxTestOrderCreated{})

	failStore := &failingOutboxStore{
		OutboxStore: memory.NewOutboxStore(),
		scheduleErr: errors.New("schedule failed"),
	}

	routes := []OutboxRoute{
		{EventTypes: []string{"outboxTestOrderCreated"}, Destination: "webhook:https://example.com"},
	}

	logger := &recordingLogger{}
	esWithOutbox := NewEventStoreWithOutbox(store, failStore, routes, WithOutboxLogger(logger))
	ctx := context.Background()

	agg := newOutboxTestOrderAggregate("err-1")
	agg.CreateOrder("err-1")

	// SaveAggregate logs the error but doesn't return it (unlike Append which does)
	err := esWithOutbox.SaveAggregate(ctx, agg)
	assert.NoError(t, err)

	// Verify the error was logged
	assert.NotEmpty(t, logger.errors)
}

// =============================================================================
// Append: Transform and Filter tests
// =============================================================================

func TestAppend_WithTransform(t *testing.T) {
	adapter := memory.NewAdapter()
	_ = adapter.Initialize(context.Background())
	store := New(adapter)
	store.RegisterEvents(outboxTestOrderCreated{})
	outboxStore := memory.NewOutboxStore()

	routes := []OutboxRoute{
		{
			EventTypes:  []string{"outboxTestOrderCreated"},
			Destination: "webhook:https://example.com",
			Transform: func(event interface{}, stored StoredEvent) ([]byte, error) {
				return []byte(`{"transformed":true}`), nil
			},
		},
	}

	esWithOutbox := NewEventStoreWithOutbox(store, outboxStore, routes)
	ctx := context.Background()

	// buildOutboxMessages is tested via Append fallback path which calls buildOutboxMessages
	// but Append builds outbox messages inline (not via buildOutboxMessages).
	// We test the inline path here.
	err := esWithOutbox.Append(ctx, "Order-1", []interface{}{
		outboxTestOrderCreated{OrderID: "1"},
	})
	require.NoError(t, err)

	// Outbox messages scheduled via non-atomic path do NOT apply Transform
	// (Transform is only applied in buildOutboxMessages which operates on StoredEvents)
	// Verify the message was scheduled
	assert.Equal(t, 1, outboxStore.Count())
}

func TestBuildOutboxMessages_WithTransform(t *testing.T) {
	adapter := memory.NewAdapter()
	store := New(adapter)
	outboxStore := memory.NewOutboxStore()

	routes := []OutboxRoute{
		{
			EventTypes:  []string{"OrderCreated"},
			Destination: "webhook:https://example.com",
			Transform: func(event interface{}, stored StoredEvent) ([]byte, error) {
				return []byte(`{"transformed":true}`), nil
			},
		},
	}

	esWithOutbox := NewEventStoreWithOutbox(store, outboxStore, routes)

	storedEvents := []adapters.StoredEvent{
		{
			ID:       "evt-1",
			StreamID: "Order-1",
			Type:     "OrderCreated",
			Data:     []byte(`{"original":true}`),
		},
	}

	msgs := esWithOutbox.buildOutboxMessages(storedEvents)
	require.Len(t, msgs, 1)
	assert.Equal(t, []byte(`{"transformed":true}`), msgs[0].Payload)
}

func TestBuildOutboxMessages_TransformError(t *testing.T) {
	adapter := memory.NewAdapter()
	store := New(adapter)
	outboxStore := memory.NewOutboxStore()

	routes := []OutboxRoute{
		{
			EventTypes:  []string{"OrderCreated"},
			Destination: "webhook:https://example.com",
			Transform: func(event interface{}, stored StoredEvent) ([]byte, error) {
				return nil, errors.New("transform error")
			},
		},
	}

	logger := &recordingLogger{}
	esWithOutbox := NewEventStoreWithOutbox(store, outboxStore, routes, WithOutboxLogger(logger))

	storedEvents := []adapters.StoredEvent{
		{
			ID:       "evt-1",
			StreamID: "Order-1",
			Type:     "OrderCreated",
			Data:     []byte(`{"original":true}`),
		},
	}

	msgs := esWithOutbox.buildOutboxMessages(storedEvents)
	assert.Empty(t, msgs)
	assert.NotEmpty(t, logger.errors)
}

func TestBuildOutboxMessages_WithFilter(t *testing.T) {
	adapter := memory.NewAdapter()
	store := New(adapter)
	outboxStore := memory.NewOutboxStore()

	routes := []OutboxRoute{
		{
			EventTypes:  []string{"OrderCreated"},
			Destination: "webhook:https://example.com",
			Filter: func(event interface{}, stored StoredEvent) bool {
				return true
			},
		},
	}

	esWithOutbox := NewEventStoreWithOutbox(store, outboxStore, routes)

	storedEvents := []adapters.StoredEvent{
		{
			ID:       "evt-1",
			StreamID: "Order-1",
			Type:     "OrderCreated",
			Data:     []byte(`{}`),
		},
	}

	msgs := esWithOutbox.buildOutboxMessages(storedEvents)
	assert.Len(t, msgs, 1)
}

func TestBuildOutboxMessages_FilterRejects(t *testing.T) {
	adapter := memory.NewAdapter()
	store := New(adapter)
	outboxStore := memory.NewOutboxStore()

	routes := []OutboxRoute{
		{
			EventTypes:  []string{"OrderCreated"},
			Destination: "webhook:https://example.com",
			Filter: func(event interface{}, stored StoredEvent) bool {
				return false
			},
		},
	}

	esWithOutbox := NewEventStoreWithOutbox(store, outboxStore, routes)

	storedEvents := []adapters.StoredEvent{
		{
			ID:       "evt-1",
			StreamID: "Order-1",
			Type:     "OrderCreated",
			Data:     []byte(`{}`),
		},
	}

	msgs := esWithOutbox.buildOutboxMessages(storedEvents)
	assert.Empty(t, msgs)
}

func TestAppend_FallbackScheduleError(t *testing.T) {
	adapter := memory.NewAdapter()
	_ = adapter.Initialize(context.Background())
	store := New(adapter)
	store.RegisterEvents(outboxTestOrderCreated{})

	failStore := &failingOutboxStore{
		OutboxStore: memory.NewOutboxStore(),
		scheduleErr: errors.New("schedule failed"),
	}

	routes := []OutboxRoute{
		{EventTypes: []string{"outboxTestOrderCreated"}, Destination: "webhook:https://example.com"},
	}

	logger := &recordingLogger{}
	esWithOutbox := NewEventStoreWithOutbox(store, failStore, routes, WithOutboxLogger(logger))
	ctx := context.Background()

	err := esWithOutbox.Append(ctx, "Order-1", []interface{}{
		outboxTestOrderCreated{OrderID: "1"},
	})
	// Append returns error when schedule fails
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "outbox scheduling failed")
}

// =============================================================================
// Options test
// =============================================================================

func TestWithOutboxLogger(t *testing.T) {
	adapter := memory.NewAdapter()
	store := New(adapter)
	outboxStore := memory.NewOutboxStore()
	logger := &recordingLogger{}

	esWithOutbox := NewEventStoreWithOutbox(store, outboxStore, nil, WithOutboxLogger(logger))
	assert.Equal(t, logger, esWithOutbox.logger)
}

// =============================================================================
// Append via atomic path tests
// =============================================================================

func TestAppend_AtomicPath(t *testing.T) {
	adapter := newMockOutboxAppenderAdapter()
	_ = adapter.Initialize(context.Background())
	store := New(adapter)
	store.RegisterEvents(outboxTestOrderCreated{})
	outboxStore := memory.NewOutboxStore()

	routes := []OutboxRoute{
		{EventTypes: []string{"outboxTestOrderCreated"}, Destination: "kafka:orders"},
	}

	esWithOutbox := NewEventStoreWithOutbox(store, outboxStore, routes)
	ctx := context.Background()

	err := esWithOutbox.Append(ctx, "Order-1", []interface{}{
		outboxTestOrderCreated{OrderID: "1"},
	})
	require.NoError(t, err)

	assert.True(t, adapter.appendWithOutboxCalled)
	assert.Len(t, adapter.appendedOutboxMsgs, 1)
}

func TestAppend_AtomicPath_Error(t *testing.T) {
	adapter := newMockOutboxAppenderAdapter()
	adapter.appendWithOutboxErr = errors.New("atomic failed")
	_ = adapter.Initialize(context.Background())
	store := New(adapter)
	store.RegisterEvents(outboxTestOrderCreated{})
	outboxStore := memory.NewOutboxStore()

	routes := []OutboxRoute{
		{EventTypes: []string{"outboxTestOrderCreated"}, Destination: "kafka:orders"},
	}

	esWithOutbox := NewEventStoreWithOutbox(store, outboxStore, routes)
	ctx := context.Background()

	err := esWithOutbox.Append(ctx, "Order-1", []interface{}{
		outboxTestOrderCreated{OrderID: "1"},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "atomic failed")
}

func TestBuildOutboxMessages_HeadersPopulated(t *testing.T) {
	adapter := memory.NewAdapter()
	store := New(adapter)
	outboxStore := memory.NewOutboxStore()

	routes := []OutboxRoute{
		{Destination: "webhook:https://example.com"},
	}

	esWithOutbox := NewEventStoreWithOutbox(store, outboxStore, routes, WithOutboxMaxAttempts(3))

	storedEvents := []adapters.StoredEvent{
		{
			ID:       "evt-1",
			StreamID: "Order-1",
			Type:     "OrderCreated",
			Data:     []byte(`{}`),
			Metadata: adapters.Metadata{
				CorrelationID: "corr-123",
				CausationID:   "cause-456",
			},
		},
	}

	msgs := esWithOutbox.buildOutboxMessages(storedEvents)
	require.Len(t, msgs, 1)
	assert.Equal(t, "evt-1", msgs[0].Headers["event-id"])
	assert.Equal(t, "Order-1", msgs[0].Headers["stream-id"])
	assert.Equal(t, "OrderCreated", msgs[0].Headers["event-type"])
	assert.Equal(t, "corr-123", msgs[0].Headers["correlation-id"])
	assert.Equal(t, "cause-456", msgs[0].Headers["causation-id"])
	assert.Equal(t, 3, msgs[0].MaxAttempts)
	assert.Equal(t, adapters.OutboxPending, msgs[0].Status)
}
