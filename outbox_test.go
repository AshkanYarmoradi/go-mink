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
	env := newOutboxTestEnv(t, "outboxTestOrderCreated", "webhook:https://example.com")

	err := env.es.Append(env.ctx, "Order-123", []interface{}{
		outboxTestOrderCreated{OrderID: "123"},
	})
	require.NoError(t, err)

	events, err := env.store.Load(env.ctx, "Order-123")
	require.NoError(t, err)
	assert.Len(t, events, 1)

	assert.Equal(t, 1, env.outbox.Count())

	msgs, err := env.outbox.FetchPending(env.ctx, 10)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	assert.Equal(t, "webhook:https://example.com", msgs[0].Destination)
	assert.Equal(t, "outboxTestOrderCreated", msgs[0].EventType)
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

	err := esWithOutbox.Append(context.Background(), "User-456", []interface{}{
		UserRegistered{UserID: "456"},
	})
	require.NoError(t, err)
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
	m := &noopOutboxMetrics{}
	m.RecordMessageProcessed("webhook", true)
	m.RecordMessageFailed("webhook")
	m.RecordMessageDeadLettered()
	m.RecordBatchDuration(0)
	m.RecordPendingMessages(0)
}

func TestOutboxStatus_Aliases(t *testing.T) {
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
// Shared test setup
// =============================================================================

// outboxTestEnv holds the common dependencies for outbox tests.
type outboxTestEnv struct {
	adapter *memory.MemoryAdapter
	store   *EventStore
	outbox  *memory.OutboxStore
	es      *EventStoreWithOutbox
	ctx     context.Context
}

// newOutboxTestEnv creates an initialized adapter, store, outbox store,
// and EventStoreWithOutbox with a single route matching the given event type.
func newOutboxTestEnv(t *testing.T, eventType, destination string, opts ...OutboxOption) *outboxTestEnv {
	t.Helper()
	adapter := memory.NewAdapter()
	_ = adapter.Initialize(context.Background())
	store := New(adapter)
	store.RegisterEvents(outboxTestOrderCreated{})

	outboxStore := memory.NewOutboxStore()
	routes := []OutboxRoute{
		{EventTypes: []string{eventType}, Destination: destination},
	}

	return &outboxTestEnv{
		adapter: adapter,
		store:   store,
		outbox:  outboxStore,
		es:      NewEventStoreWithOutbox(store, outboxStore, routes, opts...),
		ctx:     context.Background(),
	}
}

// newBuildOutboxEnv creates a minimal setup for testing buildOutboxMessages.
func newBuildOutboxEnv(t *testing.T, route OutboxRoute, opts ...OutboxOption) *EventStoreWithOutbox {
	t.Helper()
	store := New(memory.NewAdapter())
	outboxStore := memory.NewOutboxStore()
	return NewEventStoreWithOutbox(store, outboxStore, []OutboxRoute{route}, opts...)
}

// testStoredEvent creates a single StoredEvent for buildOutboxMessages tests.
func testStoredEvent(eventType string) []adapters.StoredEvent {
	return []adapters.StoredEvent{
		{ID: "evt-1", StreamID: "Order-1", Type: eventType, Data: []byte(`{}`)},
	}
}

// =============================================================================
// Mock types
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

var _ adapters.OutboxAppender = (*mockOutboxAppenderAdapter)(nil)

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
	env := newOutboxTestEnv(t, "outboxTestOrderCreated", "webhook:https://example.com")

	agg := newOutboxTestOrderAggregate("123")
	agg.CreateOrder("123")

	err := env.es.SaveAggregate(env.ctx, agg)
	require.NoError(t, err)

	events, err := env.store.LoadRaw(env.ctx, "Order-123", 0)
	require.NoError(t, err)
	assert.Len(t, events, 1)
	assert.Equal(t, 1, env.outbox.Count())
	assert.Equal(t, int64(1), agg.Version())
	assert.Empty(t, agg.UncommittedEvents())
}

func TestSaveAggregate_NilAggregate(t *testing.T) {
	adapter := memory.NewAdapter()
	store := New(adapter)
	esWithOutbox := NewEventStoreWithOutbox(store, memory.NewOutboxStore(), nil)
	assert.ErrorIs(t, esWithOutbox.SaveAggregate(context.Background(), nil), ErrNilAggregate)
}

func TestSaveAggregate_NoUncommittedEvents(t *testing.T) {
	adapter := memory.NewAdapter()
	store := New(adapter)
	outboxStore := memory.NewOutboxStore()
	esWithOutbox := NewEventStoreWithOutbox(store, outboxStore, nil)

	agg := newOutboxTestOrderAggregate("123")
	err := esWithOutbox.SaveAggregate(context.Background(), agg)
	assert.NoError(t, err)
	assert.Equal(t, 0, outboxStore.Count())
}

func TestSaveAggregate_NoMatchingRoute(t *testing.T) {
	env := newOutboxTestEnv(t, "UserRegistered", "webhook:https://example.com")

	agg := newOutboxTestOrderAggregate("123")
	agg.CreateOrder("123")

	err := env.es.SaveAggregate(env.ctx, agg)
	require.NoError(t, err)

	events, err := env.store.LoadRaw(env.ctx, "Order-123", 0)
	require.NoError(t, err)
	assert.Len(t, events, 1)
	assert.Equal(t, 0, env.outbox.Count())
}

func TestSaveAggregate_AtomicPath(t *testing.T) {
	adapter := newMockOutboxAppenderAdapter()
	_ = adapter.Initialize(context.Background())
	store := New(adapter)
	store.RegisterEvents(outboxTestOrderCreated{})

	routes := []OutboxRoute{
		{EventTypes: []string{"outboxTestOrderCreated"}, Destination: "kafka:orders"},
	}
	esWithOutbox := NewEventStoreWithOutbox(store, memory.NewOutboxStore(), routes)

	agg := newOutboxTestOrderAggregate("456")
	agg.CreateOrder("456")

	require.NoError(t, esWithOutbox.SaveAggregate(context.Background(), agg))
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

	routes := []OutboxRoute{
		{EventTypes: []string{"outboxTestOrderCreated"}, Destination: "kafka:orders"},
	}
	esWithOutbox := NewEventStoreWithOutbox(store, memory.NewOutboxStore(), routes)

	agg := newOutboxTestOrderAggregate("789")
	agg.CreateOrder("789")

	err := esWithOutbox.SaveAggregate(context.Background(), agg)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "atomic write failed")
}

func TestSaveAggregate_FallbackScheduleError(t *testing.T) {
	env := newOutboxTestEnv(t, "outboxTestOrderCreated", "webhook:https://example.com")
	logger := &recordingLogger{}

	failStore := &failingOutboxStore{
		OutboxStore: memory.NewOutboxStore(),
		scheduleErr: errors.New("schedule failed"),
	}
	routes := []OutboxRoute{
		{EventTypes: []string{"outboxTestOrderCreated"}, Destination: "webhook:https://example.com"},
	}
	esWithOutbox := NewEventStoreWithOutbox(env.store, failStore, routes, WithOutboxLogger(logger))

	agg := newOutboxTestOrderAggregate("err-1")
	agg.CreateOrder("err-1")

	err := esWithOutbox.SaveAggregate(env.ctx, agg)
	assert.NoError(t, err)
	assert.NotEmpty(t, logger.errors)
}

// =============================================================================
// Append: Transform and Filter tests
// =============================================================================

func TestAppend_WithTransform(t *testing.T) {
	env := newOutboxTestEnv(t, "outboxTestOrderCreated", "webhook:https://example.com")

	err := env.es.Append(env.ctx, "Order-1", []interface{}{
		outboxTestOrderCreated{OrderID: "1"},
	})
	require.NoError(t, err)
	assert.Equal(t, 1, env.outbox.Count())
}

func TestBuildOutboxMessages_WithTransform(t *testing.T) {
	es := newBuildOutboxEnv(t, OutboxRoute{
		EventTypes:  []string{"OrderCreated"},
		Destination: "webhook:https://example.com",
		Transform: func(event interface{}, stored StoredEvent) ([]byte, error) {
			return []byte(`{"transformed":true}`), nil
		},
	})

	msgs := es.buildOutboxMessages(testStoredEvent("OrderCreated"))
	require.Len(t, msgs, 1)
	assert.Equal(t, []byte(`{"transformed":true}`), msgs[0].Payload)
}

func TestBuildOutboxMessages_TransformError(t *testing.T) {
	logger := &recordingLogger{}
	es := newBuildOutboxEnv(t, OutboxRoute{
		EventTypes:  []string{"OrderCreated"},
		Destination: "webhook:https://example.com",
		Transform: func(event interface{}, stored StoredEvent) ([]byte, error) {
			return nil, errors.New("transform error")
		},
	}, WithOutboxLogger(logger))

	msgs := es.buildOutboxMessages(testStoredEvent("OrderCreated"))
	assert.Empty(t, msgs)
	assert.NotEmpty(t, logger.errors)
}

func TestBuildOutboxMessages_WithFilter(t *testing.T) {
	es := newBuildOutboxEnv(t, OutboxRoute{
		EventTypes:  []string{"OrderCreated"},
		Destination: "webhook:https://example.com",
		Filter: func(event interface{}, stored StoredEvent) bool {
			return true
		},
	})

	msgs := es.buildOutboxMessages(testStoredEvent("OrderCreated"))
	assert.Len(t, msgs, 1)
}

func TestBuildOutboxMessages_FilterRejects(t *testing.T) {
	es := newBuildOutboxEnv(t, OutboxRoute{
		EventTypes:  []string{"OrderCreated"},
		Destination: "webhook:https://example.com",
		Filter: func(event interface{}, stored StoredEvent) bool {
			return false
		},
	})

	msgs := es.buildOutboxMessages(testStoredEvent("OrderCreated"))
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

	err := esWithOutbox.Append(context.Background(), "Order-1", []interface{}{
		outboxTestOrderCreated{OrderID: "1"},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "outbox scheduling failed")
}

// =============================================================================
// Options test
// =============================================================================

func TestWithOutboxLogger(t *testing.T) {
	adapter := memory.NewAdapter()
	store := New(adapter)
	logger := &recordingLogger{}
	esWithOutbox := NewEventStoreWithOutbox(store, memory.NewOutboxStore(), nil, WithOutboxLogger(logger))
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

	routes := []OutboxRoute{
		{EventTypes: []string{"outboxTestOrderCreated"}, Destination: "kafka:orders"},
	}
	esWithOutbox := NewEventStoreWithOutbox(store, memory.NewOutboxStore(), routes)

	err := esWithOutbox.Append(context.Background(), "Order-1", []interface{}{
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

	routes := []OutboxRoute{
		{EventTypes: []string{"outboxTestOrderCreated"}, Destination: "kafka:orders"},
	}
	esWithOutbox := NewEventStoreWithOutbox(store, memory.NewOutboxStore(), routes)

	err := esWithOutbox.Append(context.Background(), "Order-1", []interface{}{
		outboxTestOrderCreated{OrderID: "1"},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "atomic failed")
}

func TestBuildOutboxMessages_HeadersPopulated(t *testing.T) {
	es := newBuildOutboxEnv(t, OutboxRoute{
		Destination: "webhook:https://example.com",
	}, WithOutboxMaxAttempts(3))

	storedEvents := []adapters.StoredEvent{
		{
			ID: "evt-1", StreamID: "Order-1", Type: "OrderCreated", Data: []byte(`{}`),
			Metadata: adapters.Metadata{CorrelationID: "corr-123", CausationID: "cause-456"},
		},
	}

	msgs := es.buildOutboxMessages(storedEvents)
	require.Len(t, msgs, 1)
	assert.Equal(t, "evt-1", msgs[0].Headers["event-id"])
	assert.Equal(t, "Order-1", msgs[0].Headers["stream-id"])
	assert.Equal(t, "OrderCreated", msgs[0].Headers["event-type"])
	assert.Equal(t, "corr-123", msgs[0].Headers["correlation-id"])
	assert.Equal(t, "cause-456", msgs[0].Headers["causation-id"])
	assert.Equal(t, 3, msgs[0].MaxAttempts)
	assert.Equal(t, adapters.OutboxPending, msgs[0].Status)
}
