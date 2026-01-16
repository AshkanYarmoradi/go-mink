package mink

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/AshkanYarmoradi/go-mink/adapters"
	"github.com/AshkanYarmoradi/go-mink/adapters/memory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ====================================================================
// Test Doubles
// ====================================================================

// mockSagaStore implements SagaStore for testing
type mockSagaStore struct {
	mu              sync.RWMutex
	sagas           map[string]*SagaState
	saveError       error
	loadError       error
	findError       error
	findByTypeError error
	deleteError     error
	saveCalls       int
}

func newMockSagaStore() *mockSagaStore {
	return &mockSagaStore{
		sagas: make(map[string]*SagaState),
	}
}

func (m *mockSagaStore) Save(ctx context.Context, state *SagaState) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.saveCalls++
	if m.saveError != nil {
		return m.saveError
	}
	// Deep copy the state
	copied := *state
	copied.Data = copyMap(state.Data)
	m.sagas[state.ID] = &copied
	return nil
}

func (m *mockSagaStore) Load(ctx context.Context, sagaID string) (*SagaState, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.loadError != nil {
		return nil, m.loadError
	}
	state, ok := m.sagas[sagaID]
	if !ok {
		return nil, ErrSagaNotFound
	}
	copied := *state
	return &copied, nil
}

func (m *mockSagaStore) FindByCorrelationID(ctx context.Context, correlationID string) (*SagaState, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.findError != nil {
		return nil, m.findError
	}
	for _, state := range m.sagas {
		if state.CorrelationID == correlationID {
			copied := *state
			return &copied, nil
		}
	}
	return nil, ErrSagaNotFound
}

func (m *mockSagaStore) FindByType(ctx context.Context, sagaType string, statuses ...SagaStatus) ([]*SagaState, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.findByTypeError != nil {
		return nil, m.findByTypeError
	}
	var result []*SagaState
	for _, state := range m.sagas {
		if state.Type == sagaType {
			if len(statuses) == 0 {
				copied := *state
				result = append(result, &copied)
			} else {
				for _, s := range statuses {
					if state.Status == s {
						copied := *state
						result = append(result, &copied)
						break
					}
				}
			}
		}
	}
	return result, nil
}

func (m *mockSagaStore) Delete(ctx context.Context, sagaID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.deleteError != nil {
		return m.deleteError
	}
	if _, ok := m.sagas[sagaID]; !ok {
		return ErrSagaNotFound
	}
	delete(m.sagas, sagaID)
	return nil
}

func (m *mockSagaStore) Close() error {
	return nil
}

func copyMap(m map[string]interface{}) map[string]interface{} {
	if m == nil {
		return nil
	}
	result := make(map[string]interface{})
	for k, v := range m {
		result[k] = v
	}
	return result
}

// mockSubscriptionAdapter implements SubscriptionAdapter for testing
type mockSubscriptionAdapter struct {
	adapters.EventStoreAdapter
	mu         sync.Mutex
	eventCh    chan adapters.StoredEvent
	events     []adapters.StoredEvent
	subscribed bool
	subErr     error
}

func newMockSubscriptionAdapter() *mockSubscriptionAdapter {
	return &mockSubscriptionAdapter{
		eventCh: make(chan adapters.StoredEvent, 100),
	}
}

func (m *mockSubscriptionAdapter) SubscribeAll(ctx context.Context, fromPosition uint64, opts ...adapters.SubscriptionOptions) (<-chan adapters.StoredEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.subErr != nil {
		return nil, m.subErr
	}
	m.subscribed = true

	// Return existing events above position
	go func() {
		m.mu.Lock()
		// Make a proper copy of the slice to avoid race with SendEvent
		events := make([]adapters.StoredEvent, len(m.events))
		copy(events, m.events)
		m.mu.Unlock()

		for _, e := range events {
			if e.GlobalPosition >= fromPosition {
				select {
				case m.eventCh <- e:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	return m.eventCh, nil
}

func (m *mockSubscriptionAdapter) SendEvent(event adapters.StoredEvent) {
	m.mu.Lock()
	m.events = append(m.events, event)
	m.mu.Unlock()
	// Send to channel outside the lock to avoid blocking while holding lock
	// The channel is buffered, so this is safe
	m.eventCh <- event
}

func (m *mockSubscriptionAdapter) Close() error {
	close(m.eventCh)
	return nil
}

// nonSubscriptionAdapter wraps an EventStoreAdapter without subscription support
type nonSubscriptionAdapter struct {
	adapters.EventStoreAdapter
}

// Ensure it doesn't implement SubscriptionAdapter
var _ adapters.EventStoreAdapter = (*nonSubscriptionAdapter)(nil)

// mockEventStoreWithSubscription wraps memory adapter to add subscription support
type mockEventStoreWithSubscription struct {
	*memory.MemoryAdapter
	subAdapter *mockSubscriptionAdapter
}

func (m *mockEventStoreWithSubscription) SubscribeAll(ctx context.Context, fromPosition uint64, opts ...adapters.SubscriptionOptions) (<-chan adapters.StoredEvent, error) {
	return m.subAdapter.SubscribeAll(ctx, fromPosition, opts...)
}

// testSaga is a simple test saga implementation
type testSaga struct {
	SagaBase
	data             map[string]interface{}
	commandsToReturn []Command
	compensateToRet  []Command
	handleError      error
	compensateError  error
	complete         bool
}

func newTestSaga(id string) Saga {
	s := &testSaga{
		SagaBase: NewSagaBase(id, "TestSaga"),
	}
	return s
}

func (s *testSaga) HandledEvents() []string {
	return []string{"OrderCreated", "OrderShipped", "OrderFailed"}
}

func (s *testSaga) HandleEvent(ctx context.Context, event StoredEvent) ([]Command, error) {
	if s.handleError != nil {
		return nil, s.handleError
	}
	s.complete = s.commandsToReturn == nil
	return s.commandsToReturn, nil
}

func (s *testSaga) Compensate(ctx context.Context, failedStep int, err error) ([]Command, error) {
	if s.compensateError != nil {
		return nil, s.compensateError
	}
	return s.compensateToRet, nil
}

func (s *testSaga) IsComplete() bool {
	return s.complete
}

func (s *testSaga) Data() map[string]interface{} {
	return s.data
}

func (s *testSaga) SetData(data map[string]interface{}) {
	s.data = data
}

// testCommand is a simple command for testing
type testCommand struct {
	commandType string
	valid       bool
}

func (c *testCommand) CommandType() string { return c.commandType }
func (c *testCommand) Validate() error {
	if !c.valid {
		return errors.New("invalid command")
	}
	return nil
}

// mockCommandHandler is a handler for test commands
type mockCommandHandler struct {
	cmdType    string
	handleFunc func(ctx context.Context, cmd Command) (CommandResult, error)
	err        error
}

func (h *mockCommandHandler) CommandType() string {
	return h.cmdType
}

func (h *mockCommandHandler) Handle(ctx context.Context, cmd Command) (CommandResult, error) {
	if h.handleFunc != nil {
		return h.handleFunc(ctx, cmd)
	}
	if h.err != nil {
		return NewErrorResult(h.err), h.err
	}
	return NewSuccessResult("", 0), nil
}

// ====================================================================
// SagaManager Option Tests
// ====================================================================

func TestWithSagaStore(t *testing.T) {
	store := newMockSagaStore()
	opt := WithSagaStore(store)

	m := &SagaManager{}
	opt(m)

	assert.Equal(t, store, m.store)
}

func TestWithSagaLogger(t *testing.T) {
	logger := &testLogger{}
	opt := WithSagaLogger(logger)

	m := &SagaManager{}
	opt(m)

	assert.Equal(t, logger, m.logger)
}

func TestWithSagaSerializer(t *testing.T) {
	serializer := NewJSONSerializer()
	opt := WithSagaSerializer(serializer)

	m := &SagaManager{}
	opt(m)

	assert.Equal(t, serializer, m.serializer)
}

func TestWithCommandBus(t *testing.T) {
	bus := NewCommandBus()
	opt := WithCommandBus(bus)

	m := &SagaManager{}
	opt(m)

	assert.Equal(t, bus, m.commandBus)
}

func TestWithSagaPollInterval(t *testing.T) {
	interval := 500 * time.Millisecond
	opt := WithSagaPollInterval(interval)

	m := &SagaManager{}
	opt(m)

	assert.Equal(t, interval, m.pollInterval)
}

func TestWithSagaRetryAttempts(t *testing.T) {
	attempts := 5
	opt := WithSagaRetryAttempts(attempts)

	m := &SagaManager{}
	opt(m)

	assert.Equal(t, attempts, m.retryAttempts)
}

func TestWithSagaRetryDelay(t *testing.T) {
	delay := 2 * time.Second
	opt := WithSagaRetryDelay(delay)

	m := &SagaManager{}
	opt(m)

	assert.Equal(t, delay, m.retryDelay)
}

// ====================================================================
// NewSagaManager Tests
// ====================================================================

func TestNewSagaManager(t *testing.T) {
	memAdapter := memory.NewAdapter()
	store := New(memAdapter)

	manager := NewSagaManager(store)

	assert.NotNil(t, manager)
	assert.NotNil(t, manager.registry)
	assert.NotNil(t, manager.correlations)
	assert.NotNil(t, manager.eventHandlers)
	assert.Equal(t, 100*time.Millisecond, manager.pollInterval)
	assert.Equal(t, 3, manager.retryAttempts)
	assert.Equal(t, time.Second, manager.retryDelay)
}

func TestNewSagaManager_WithOptions(t *testing.T) {
	memAdapter := memory.NewAdapter()
	store := New(memAdapter)
	sagaStore := newMockSagaStore()
	cmdBus := NewCommandBus()
	logger := &sagaTestLogger{}

	manager := NewSagaManager(store,
		WithSagaStore(sagaStore),
		WithCommandBus(cmdBus),
		WithSagaLogger(logger),
		WithSagaPollInterval(200*time.Millisecond),
		WithSagaRetryAttempts(5),
		WithSagaRetryDelay(2*time.Second),
	)

	assert.Equal(t, sagaStore, manager.store)
	assert.Equal(t, cmdBus, manager.commandBus)
	assert.Equal(t, logger, manager.logger)
	assert.Equal(t, 200*time.Millisecond, manager.pollInterval)
	assert.Equal(t, 5, manager.retryAttempts)
	assert.Equal(t, 2*time.Second, manager.retryDelay)
}

// ====================================================================
// Register Tests
// ====================================================================

func TestSagaManager_Register(t *testing.T) {
	memAdapter := memory.NewAdapter()
	store := New(memAdapter)
	manager := NewSagaManager(store)

	correlation := SagaCorrelation{
		SagaType:       "TestSaga",
		StartingEvents: []string{"OrderCreated"},
		CorrelationIDFunc: func(event StoredEvent) string {
			return event.StreamID
		},
	}

	manager.Register("TestSaga", newTestSaga, correlation)

	// Verify registration
	assert.Contains(t, manager.registry, "TestSaga")
	assert.Contains(t, manager.correlations, "TestSaga")
	assert.Contains(t, manager.eventHandlers["OrderCreated"], "TestSaga")
	assert.Contains(t, manager.eventHandlers["OrderShipped"], "TestSaga")
	assert.Contains(t, manager.eventHandlers["OrderFailed"], "TestSaga")
}

func TestSagaManager_RegisterSimple(t *testing.T) {
	memAdapter := memory.NewAdapter()
	store := New(memAdapter)
	manager := NewSagaManager(store)

	manager.RegisterSimple("TestSaga", newTestSaga, "OrderCreated")

	// Verify registration
	assert.Contains(t, manager.registry, "TestSaga")
	assert.Contains(t, manager.correlations, "TestSaga")
	assert.Contains(t, manager.eventHandlers["OrderCreated"], "TestSaga")

	// Verify correlation uses stream ID
	correlations := manager.correlations["TestSaga"]
	require.Len(t, correlations, 1)
	assert.Equal(t, []string{"OrderCreated"}, correlations[0].StartingEvents)

	event := StoredEvent{StreamID: "order-123"}
	assert.Equal(t, "order-123", correlations[0].CorrelationIDFunc(event))
}

// ====================================================================
// Start/Stop Tests
// ====================================================================

func TestSagaManager_Start_MissingStore(t *testing.T) {
	memAdapter := memory.NewAdapter()
	eventStore := New(memAdapter)
	cmdBus := NewCommandBus()
	manager := NewSagaManager(eventStore, WithCommandBus(cmdBus))

	ctx := context.Background()
	err := manager.Start(ctx)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "saga store is required")
}

func TestSagaManager_Start_MissingCommandBus(t *testing.T) {
	memAdapter := memory.NewAdapter()
	eventStore := New(memAdapter)
	sagaStore := newMockSagaStore()
	manager := NewSagaManager(eventStore, WithSagaStore(sagaStore))

	ctx := context.Background()
	err := manager.Start(ctx)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "command bus is required")
}

func TestSagaManager_Start_AlreadyRunning(t *testing.T) {
	subAdapter := newMockSubscriptionAdapter()
	mockES := &mockEventStoreWithSubscription{
		MemoryAdapter: memory.NewAdapter(),
		subAdapter:    subAdapter,
	}
	eventStore := New(mockES)
	sagaStore := newMockSagaStore()
	cmdBus := NewCommandBus()

	manager := NewSagaManager(eventStore,
		WithSagaStore(sagaStore),
		WithCommandBus(cmdBus),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start in background
	go func() {
		_ = manager.Start(ctx)
	}()

	// Wait for manager to start
	time.Sleep(50 * time.Millisecond)
	assert.True(t, manager.IsRunning())

	// Try to start again
	err := manager.Start(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already running")
}

func TestSagaManager_Start_SubscriptionNotSupported(t *testing.T) {
	// Use adapter that does NOT support subscriptions
	baseAdapter := memory.NewAdapter()
	noSubAdapter := &nonSubscriptionAdapter{EventStoreAdapter: baseAdapter}
	eventStore := New(noSubAdapter)
	sagaStore := newMockSagaStore()
	cmdBus := NewCommandBus()

	manager := NewSagaManager(eventStore,
		WithSagaStore(sagaStore),
		WithCommandBus(cmdBus),
	)

	ctx := context.Background()
	err := manager.Start(ctx)

	assert.ErrorIs(t, err, ErrSubscriptionNotSupported)
}

func TestSagaManager_Stop(t *testing.T) {
	subAdapter := newMockSubscriptionAdapter()
	mockES := &mockEventStoreWithSubscription{
		MemoryAdapter: memory.NewAdapter(),
		subAdapter:    subAdapter,
	}
	eventStore := New(mockES)
	sagaStore := newMockSagaStore()
	cmdBus := NewCommandBus()

	manager := NewSagaManager(eventStore,
		WithSagaStore(sagaStore),
		WithCommandBus(cmdBus),
	)

	ctx := context.Background()
	done := make(chan error)

	go func() {
		done <- manager.Start(ctx)
	}()

	// Wait for manager to start
	time.Sleep(50 * time.Millisecond)
	assert.True(t, manager.IsRunning())

	// Stop the manager
	manager.Stop()

	// Wait for completion
	err := <-done
	assert.ErrorIs(t, err, context.Canceled)
	assert.False(t, manager.IsRunning())
}

func TestSagaManager_IsRunning(t *testing.T) {
	memAdapter := memory.NewAdapter()
	eventStore := New(memAdapter)
	manager := NewSagaManager(eventStore)

	assert.False(t, manager.IsRunning())
}

// ====================================================================
// Position Tests
// ====================================================================

func TestSagaManager_Position(t *testing.T) {
	memAdapter := memory.NewAdapter()
	eventStore := New(memAdapter)
	manager := NewSagaManager(eventStore)

	assert.Equal(t, uint64(0), manager.Position())
}

func TestSagaManager_SetPosition(t *testing.T) {
	memAdapter := memory.NewAdapter()
	eventStore := New(memAdapter)
	manager := NewSagaManager(eventStore)

	manager.SetPosition(100)
	assert.Equal(t, uint64(100), manager.Position())
}

// ====================================================================
// ProcessEvent Tests
// ====================================================================

func TestSagaManager_ProcessEvent_NoHandlers(t *testing.T) {
	memAdapter := memory.NewAdapter()
	eventStore := New(memAdapter)
	sagaStore := newMockSagaStore()
	cmdBus := NewCommandBus()

	manager := NewSagaManager(eventStore,
		WithSagaStore(sagaStore),
		WithCommandBus(cmdBus),
	)

	ctx := context.Background()
	event := StoredEvent{
		ID:       "event-1",
		StreamID: "order-123",
		Type:     "UnknownEvent",
		Data:     []byte(`{}`),
	}

	// Should not error when no handlers exist
	err := manager.ProcessEvent(ctx, event)
	assert.NoError(t, err)
}

func TestSagaManager_ProcessEvent_CreatesNewSaga(t *testing.T) {
	memAdapter := memory.NewAdapter()
	eventStore := New(memAdapter)
	sagaStore := newMockSagaStore()
	cmdBus := NewCommandBus()

	manager := NewSagaManager(eventStore,
		WithSagaStore(sagaStore),
		WithCommandBus(cmdBus),
	)

	// Register saga
	manager.RegisterSimple("TestSaga", newTestSaga, "OrderCreated")

	ctx := context.Background()
	event := StoredEvent{
		ID:       "event-1",
		StreamID: "order-123",
		Type:     "OrderCreated",
		Data:     []byte(`{"orderId": "order-123"}`),
	}

	err := manager.ProcessEvent(ctx, event)
	assert.NoError(t, err)

	// Verify saga was created
	state, err := sagaStore.FindByCorrelationID(ctx, "order-123")
	assert.NoError(t, err)
	assert.NotNil(t, state)
	assert.Equal(t, "TestSaga", state.Type)
	assert.Equal(t, "order-123", state.CorrelationID)
}

func TestSagaManager_ProcessEvent_ExistingSaga(t *testing.T) {
	memAdapter := memory.NewAdapter()
	eventStore := New(memAdapter)
	sagaStore := newMockSagaStore()
	cmdBus := NewCommandBus()

	manager := NewSagaManager(eventStore,
		WithSagaStore(sagaStore),
		WithCommandBus(cmdBus),
	)

	// Pre-create a saga
	existingState := &SagaState{
		ID:            "TestSaga-order-123",
		Type:          "TestSaga",
		CorrelationID: "order-123",
		Status:        SagaStatusStarted,
		StartedAt:     time.Now(),
		Version:       1,
	}
	err := sagaStore.Save(context.Background(), existingState)
	require.NoError(t, err)

	// Register saga
	manager.RegisterSimple("TestSaga", newTestSaga, "OrderCreated")

	ctx := context.Background()
	event := StoredEvent{
		ID:       "event-2",
		StreamID: "order-123",
		Type:     "OrderShipped",
		Data:     []byte(`{}`),
	}

	err = manager.ProcessEvent(ctx, event)
	assert.NoError(t, err)

	// Verify saga was updated
	assert.Equal(t, 2, sagaStore.saveCalls)
}

func TestSagaManager_ProcessEvent_TerminalSaga(t *testing.T) {
	memAdapter := memory.NewAdapter()
	eventStore := New(memAdapter)
	sagaStore := newMockSagaStore()
	cmdBus := NewCommandBus()
	logger := &sagaTestLogger{}

	manager := NewSagaManager(eventStore,
		WithSagaStore(sagaStore),
		WithCommandBus(cmdBus),
		WithSagaLogger(logger),
	)

	// Pre-create a completed saga
	completed := time.Now()
	existingState := &SagaState{
		ID:            "TestSaga-order-123",
		Type:          "TestSaga",
		CorrelationID: "order-123",
		Status:        SagaStatusCompleted, // Terminal state
		StartedAt:     time.Now(),
		CompletedAt:   &completed,
		Version:       1,
	}
	err := sagaStore.Save(context.Background(), existingState)
	require.NoError(t, err)

	// Register saga
	manager.RegisterSimple("TestSaga", newTestSaga, "OrderCreated")

	ctx := context.Background()
	event := StoredEvent{
		ID:       "event-2",
		StreamID: "order-123",
		Type:     "OrderShipped",
		Data:     []byte(`{}`),
	}

	err = manager.ProcessEvent(ctx, event)
	assert.NoError(t, err)

	// Saga should not be updated again (only initial save)
	assert.Equal(t, 1, sagaStore.saveCalls)
}

func TestSagaManager_ProcessEvent_WithCommands(t *testing.T) {
	memAdapter := memory.NewAdapter()
	eventStore := New(memAdapter)
	sagaStore := newMockSagaStore()
	cmdBus := NewCommandBus()

	// Register command handler
	var dispatched bool
	cmdBus.Register(&mockCommandHandler{
		cmdType: "SendEmail",
		handleFunc: func(ctx context.Context, cmd Command) (CommandResult, error) {
			dispatched = true
			return NewSuccessResult("", 0), nil
		},
	})

	// Create saga that returns commands
	factoryWithCommands := func(id string) Saga {
		s := &testSaga{
			SagaBase:         NewSagaBase(id, "TestSaga"),
			commandsToReturn: []Command{&testCommand{commandType: "SendEmail", valid: true}},
		}
		return s
	}

	manager := NewSagaManager(eventStore,
		WithSagaStore(sagaStore),
		WithCommandBus(cmdBus),
	)

	manager.RegisterSimple("TestSaga", factoryWithCommands, "OrderCreated")

	ctx := context.Background()
	event := StoredEvent{
		ID:       "event-1",
		StreamID: "order-123",
		Type:     "OrderCreated",
		Data:     []byte(`{}`),
	}

	err := manager.ProcessEvent(ctx, event)
	assert.NoError(t, err)
	assert.True(t, dispatched)
}

func TestSagaManager_ProcessEvent_CommandDispatchFailure(t *testing.T) {
	memAdapter := memory.NewAdapter()
	eventStore := New(memAdapter)
	sagaStore := newMockSagaStore()
	cmdBus := NewCommandBus()

	// Register failing command handler
	cmdBus.Register(&mockCommandHandler{
		cmdType: "FailingCommand",
		err:     errors.New("dispatch failed"),
	})

	// Create saga that returns commands
	factoryWithCommands := func(id string) Saga {
		s := &testSaga{
			SagaBase:         NewSagaBase(id, "TestSaga"),
			commandsToReturn: []Command{&testCommand{commandType: "FailingCommand", valid: true}},
		}
		return s
	}

	manager := NewSagaManager(eventStore,
		WithSagaStore(sagaStore),
		WithCommandBus(cmdBus),
		WithSagaRetryAttempts(1), // Minimize test time
		WithSagaRetryDelay(time.Millisecond),
	)

	manager.RegisterSimple("TestSaga", factoryWithCommands, "OrderCreated")

	ctx := context.Background()
	event := StoredEvent{
		ID:       "event-1",
		StreamID: "order-123",
		Type:     "OrderCreated",
		Data:     []byte(`{}`),
	}

	err := manager.ProcessEvent(ctx, event)
	assert.NoError(t, err) // Errors are logged but don't propagate

	// Verify saga was compensated
	state, err := sagaStore.Load(ctx, "TestSaga-order-123")
	require.NoError(t, err)
	assert.Equal(t, SagaStatusCompensated, state.Status)
}

func TestSagaManager_ProcessEvent_HandleEventError(t *testing.T) {
	memAdapter := memory.NewAdapter()
	eventStore := New(memAdapter)
	sagaStore := newMockSagaStore()
	cmdBus := NewCommandBus()

	// Create saga that returns error on handle
	factoryWithError := func(id string) Saga {
		s := &testSaga{
			SagaBase:    NewSagaBase(id, "TestSaga"),
			handleError: errors.New("handle failed"),
		}
		return s
	}

	manager := NewSagaManager(eventStore,
		WithSagaStore(sagaStore),
		WithCommandBus(cmdBus),
	)

	manager.RegisterSimple("TestSaga", factoryWithError, "OrderCreated")

	ctx := context.Background()
	event := StoredEvent{
		ID:       "event-1",
		StreamID: "order-123",
		Type:     "OrderCreated",
		Data:     []byte(`{}`),
	}

	err := manager.ProcessEvent(ctx, event)
	assert.NoError(t, err)

	// Verify saga was compensated
	state, err := sagaStore.Load(ctx, "TestSaga-order-123")
	require.NoError(t, err)
	assert.Equal(t, SagaStatusCompensated, state.Status)
}

func TestSagaManager_ProcessEvent_SagaCompletes(t *testing.T) {
	memAdapter := memory.NewAdapter()
	eventStore := New(memAdapter)
	sagaStore := newMockSagaStore()
	cmdBus := NewCommandBus()

	// Create saga that completes immediately
	factoryComplete := func(id string) Saga {
		s := &testSaga{
			SagaBase: NewSagaBase(id, "TestSaga"),
			complete: true, // IsComplete() returns true
		}
		return s
	}

	manager := NewSagaManager(eventStore,
		WithSagaStore(sagaStore),
		WithCommandBus(cmdBus),
	)

	manager.RegisterSimple("TestSaga", factoryComplete, "OrderCreated")

	ctx := context.Background()
	event := StoredEvent{
		ID:       "event-1",
		StreamID: "order-123",
		Type:     "OrderCreated",
		Data:     []byte(`{}`),
	}

	err := manager.ProcessEvent(ctx, event)
	assert.NoError(t, err)

	// Verify saga completed
	state, err := sagaStore.Load(ctx, "TestSaga-order-123")
	require.NoError(t, err)
	assert.Equal(t, SagaStatusCompleted, state.Status)
	assert.NotNil(t, state.CompletedAt)
}

// ====================================================================
// GetSaga Tests
// ====================================================================

func TestSagaManager_GetSaga(t *testing.T) {
	memAdapter := memory.NewAdapter()
	eventStore := New(memAdapter)
	sagaStore := newMockSagaStore()

	manager := NewSagaManager(eventStore,
		WithSagaStore(sagaStore),
	)

	// Save a saga
	state := &SagaState{
		ID:        "saga-123",
		Type:      "TestSaga",
		Status:    SagaStatusRunning,
		StartedAt: time.Now(),
		Version:   1,
	}
	err := sagaStore.Save(context.Background(), state)
	require.NoError(t, err)

	// Get saga
	result, err := manager.GetSaga(context.Background(), "saga-123")
	assert.NoError(t, err)
	assert.Equal(t, "saga-123", result.ID)
	assert.Equal(t, "TestSaga", result.Type)
}

func TestSagaManager_GetSaga_NotFound(t *testing.T) {
	memAdapter := memory.NewAdapter()
	eventStore := New(memAdapter)
	sagaStore := newMockSagaStore()

	manager := NewSagaManager(eventStore,
		WithSagaStore(sagaStore),
	)

	result, err := manager.GetSaga(context.Background(), "nonexistent")
	assert.ErrorIs(t, err, ErrSagaNotFound)
	assert.Nil(t, result)
}

// ====================================================================
// FindSagaByCorrelationID Tests
// ====================================================================

func TestSagaManager_FindSagaByCorrelationID(t *testing.T) {
	memAdapter := memory.NewAdapter()
	eventStore := New(memAdapter)
	sagaStore := newMockSagaStore()

	manager := NewSagaManager(eventStore,
		WithSagaStore(sagaStore),
	)

	// Save a saga
	state := &SagaState{
		ID:            "saga-123",
		Type:          "TestSaga",
		CorrelationID: "order-456",
		Status:        SagaStatusRunning,
		StartedAt:     time.Now(),
		Version:       1,
	}
	err := sagaStore.Save(context.Background(), state)
	require.NoError(t, err)

	// Find saga
	result, err := manager.FindSagaByCorrelationID(context.Background(), "order-456")
	assert.NoError(t, err)
	assert.Equal(t, "saga-123", result.ID)
	assert.Equal(t, "order-456", result.CorrelationID)
}

func TestSagaManager_FindSagaByCorrelationID_NotFound(t *testing.T) {
	memAdapter := memory.NewAdapter()
	eventStore := New(memAdapter)
	sagaStore := newMockSagaStore()

	manager := NewSagaManager(eventStore,
		WithSagaStore(sagaStore),
	)

	result, err := manager.FindSagaByCorrelationID(context.Background(), "nonexistent")
	assert.ErrorIs(t, err, ErrSagaNotFound)
	assert.Nil(t, result)
}

// ====================================================================
// SagaState JSON Tests
// ====================================================================

func TestSagaStateToJSON(t *testing.T) {
	completed := time.Now()
	state := &SagaState{
		ID:            "saga-123",
		Type:          "TestSaga",
		CorrelationID: "order-456",
		Status:        SagaStatusCompleted,
		CurrentStep:   3,
		Data:          map[string]interface{}{"key": "value"},
		StartedAt:     time.Now(),
		CompletedAt:   &completed,
		Version:       5,
	}

	data, err := SagaStateToJSON(state)
	assert.NoError(t, err)
	assert.NotEmpty(t, data)

	// Parse back
	parsed, err := SagaStateFromJSON(data)
	assert.NoError(t, err)
	assert.Equal(t, state.ID, parsed.ID)
	assert.Equal(t, state.Type, parsed.Type)
	assert.Equal(t, state.CorrelationID, parsed.CorrelationID)
	assert.Equal(t, state.Status, parsed.Status)
	assert.Equal(t, state.CurrentStep, parsed.CurrentStep)
	assert.Equal(t, state.Version, parsed.Version)
}

func TestSagaStateFromJSON_Invalid(t *testing.T) {
	state, err := SagaStateFromJSON([]byte("invalid json"))
	assert.Error(t, err)
	assert.Nil(t, state)
}

// ====================================================================
// isStartingEvent Tests
// ====================================================================

func TestIsStartingEvent(t *testing.T) {
	tests := []struct {
		name           string
		startingEvents []string
		eventType      string
		expected       bool
	}{
		{
			name:           "event is starting event",
			startingEvents: []string{"OrderCreated", "OrderImported"},
			eventType:      "OrderCreated",
			expected:       true,
		},
		{
			name:           "event is not starting event",
			startingEvents: []string{"OrderCreated", "OrderImported"},
			eventType:      "OrderShipped",
			expected:       false,
		},
		{
			name:           "empty starting events",
			startingEvents: []string{},
			eventType:      "OrderCreated",
			expected:       false,
		},
		{
			name:           "nil starting events",
			startingEvents: nil,
			eventType:      "OrderCreated",
			expected:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isStartingEvent(tt.startingEvents, tt.eventType)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// ====================================================================
// handleSagaFailure Tests
// ====================================================================

func TestSagaManager_handleSagaFailure_CompensationFails(t *testing.T) {
	memAdapter := memory.NewAdapter()
	eventStore := New(memAdapter)
	sagaStore := newMockSagaStore()
	cmdBus := NewCommandBus()
	logger := &sagaTestLogger{}

	manager := NewSagaManager(eventStore,
		WithSagaStore(sagaStore),
		WithCommandBus(cmdBus),
		WithSagaLogger(logger),
	)

	// Create saga that fails on compensation
	saga := &testSaga{
		SagaBase:        NewSagaBase("saga-123", "TestSaga"),
		compensateError: errors.New("compensation failed"),
	}

	ctx := context.Background()
	err := manager.handleSagaFailure(ctx, saga, errors.New("original error"))
	assert.NoError(t, err)

	// Verify saga is in failed state
	state, err := sagaStore.Load(ctx, "saga-123")
	require.NoError(t, err)
	assert.Equal(t, SagaStatusFailed, state.Status)
}

func TestSagaManager_handleSagaFailure_CompensationCommandFails(t *testing.T) {
	memAdapter := memory.NewAdapter()
	eventStore := New(memAdapter)
	sagaStore := newMockSagaStore()
	cmdBus := NewCommandBus()
	logger := &sagaTestLogger{}

	// Register failing command handler for compensation
	cmdBus.Register(&mockCommandHandler{
		cmdType: "CompensateOrder",
		err:     errors.New("compensation dispatch failed"),
	})

	manager := NewSagaManager(eventStore,
		WithSagaStore(sagaStore),
		WithCommandBus(cmdBus),
		WithSagaLogger(logger),
		WithSagaRetryAttempts(1),
		WithSagaRetryDelay(time.Millisecond),
	)

	// Create saga that returns compensation commands which will fail
	saga := &testSaga{
		SagaBase:        NewSagaBase("saga-456", "TestSaga"),
		compensateToRet: []Command{&testCommand{commandType: "CompensateOrder", valid: true}},
	}

	ctx := context.Background()
	err := manager.handleSagaFailure(ctx, saga, errors.New("original error"))
	assert.NoError(t, err)

	// Verify saga is in compensation_failed state
	state, err := sagaStore.Load(ctx, "saga-456")
	require.NoError(t, err)
	assert.Equal(t, SagaStatusCompensationFailed, state.Status)
	assert.NotNil(t, state.CompletedAt)
}

// ====================================================================
// adaptEvent Tests
// ====================================================================

func TestSagaManager_adaptEvent(t *testing.T) {
	memAdapter := memory.NewAdapter()
	eventStore := New(memAdapter)
	manager := NewSagaManager(eventStore)

	now := time.Now()
	adapterEvent := adapters.StoredEvent{
		ID:             "event-123",
		StreamID:       "stream-456",
		Type:           "TestEvent",
		Data:           []byte(`{"key":"value"}`),
		Metadata:       adapters.Metadata{CorrelationID: "corr-789"},
		Version:        5,
		GlobalPosition: 100,
		Timestamp:      now,
	}

	result := manager.adaptEvent(adapterEvent)

	assert.Equal(t, "event-123", result.ID)
	assert.Equal(t, "stream-456", result.StreamID)
	assert.Equal(t, "TestEvent", result.Type)
	assert.Equal(t, []byte(`{"key":"value"}`), result.Data)
	assert.Equal(t, "corr-789", result.Metadata.CorrelationID)
	assert.Equal(t, int64(5), result.Version)
	assert.Equal(t, uint64(100), result.GlobalPosition)
	assert.Equal(t, now, result.Timestamp)
}

// ====================================================================
// dispatchCommand Tests
// ====================================================================

func TestSagaManager_dispatchCommand_SuccessOnRetry(t *testing.T) {
	memAdapter := memory.NewAdapter()
	eventStore := New(memAdapter)
	sagaStore := newMockSagaStore()
	cmdBus := NewCommandBus()

	// Handler that fails first, then succeeds
	attempts := 0
	cmdBus.Register(&mockCommandHandler{
		cmdType: "TestCommand",
		handleFunc: func(ctx context.Context, cmd Command) (CommandResult, error) {
			attempts++
			if attempts < 2 {
				return NewErrorResult(errors.New("temporary failure")), errors.New("temporary failure")
			}
			return NewSuccessResult("", 0), nil
		},
	})

	manager := NewSagaManager(eventStore,
		WithSagaStore(sagaStore),
		WithCommandBus(cmdBus),
		WithSagaRetryAttempts(3),
		WithSagaRetryDelay(time.Millisecond),
	)

	saga := &testSaga{SagaBase: NewSagaBase("saga-123", "TestSaga")}

	ctx := context.Background()
	err := manager.dispatchCommand(ctx, saga, &testCommand{commandType: "TestCommand", valid: true})

	assert.NoError(t, err)
	assert.Equal(t, 2, attempts)
}

func TestSagaManager_dispatchCommand_AllRetriesFail(t *testing.T) {
	memAdapter := memory.NewAdapter()
	eventStore := New(memAdapter)
	sagaStore := newMockSagaStore()
	cmdBus := NewCommandBus()

	cmdBus.Register(&mockCommandHandler{
		cmdType: "TestCommand",
		err:     errors.New("persistent failure"),
	})

	manager := NewSagaManager(eventStore,
		WithSagaStore(sagaStore),
		WithCommandBus(cmdBus),
		WithSagaRetryAttempts(2),
		WithSagaRetryDelay(time.Millisecond),
	)

	saga := &testSaga{SagaBase: NewSagaBase("saga-123", "TestSaga")}

	ctx := context.Background()
	err := manager.dispatchCommand(ctx, saga, &testCommand{commandType: "TestCmd", valid: true})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed after 2 attempts")
}

func TestSagaManager_dispatchCommand_ContextCanceled(t *testing.T) {
	memAdapter := memory.NewAdapter()
	eventStore := New(memAdapter)
	sagaStore := newMockSagaStore()
	cmdBus := NewCommandBus()

	cmdBus.Register(&mockCommandHandler{
		cmdType: "TestCommand",
		err:     errors.New("failure"),
	})

	manager := NewSagaManager(eventStore,
		WithSagaStore(sagaStore),
		WithCommandBus(cmdBus),
		WithSagaRetryAttempts(5),
		WithSagaRetryDelay(100*time.Millisecond),
	)

	saga := &testSaga{SagaBase: NewSagaBase("saga-123", "TestSaga")}

	ctx, cancel := context.WithCancel(context.Background())

	// Cancel after a short delay
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	err := manager.dispatchCommand(ctx, saga, &testCommand{commandType: "TestCmd", valid: true})
	assert.ErrorIs(t, err, context.Canceled)
}

// ====================================================================
// Integration Test: Full Saga Lifecycle
// ====================================================================

func TestSagaManager_FullLifecycle(t *testing.T) {
	subAdapter := newMockSubscriptionAdapter()
	mockES := &mockEventStoreWithSubscription{
		MemoryAdapter: memory.NewAdapter(),
		subAdapter:    subAdapter,
	}
	eventStore := New(mockES)
	sagaStore := newMockSagaStore()
	cmdBus := NewCommandBus()

	// Track dispatched commands
	var dispatchedCommands []string
	cmdBus.Register(&mockCommandHandler{
		cmdType: "SendEmail",
		handleFunc: func(ctx context.Context, cmd Command) (CommandResult, error) {
			dispatchedCommands = append(dispatchedCommands, cmd.CommandType())
			return NewSuccessResult("", 0), nil
		},
	})

	manager := NewSagaManager(eventStore,
		WithSagaStore(sagaStore),
		WithCommandBus(cmdBus),
	)

	// Create saga that progresses through steps
	step := 0
	factoryProgressive := func(id string) Saga {
		s := &testSaga{SagaBase: NewSagaBase(id, "TestSaga")}
		return s
	}

	manager.RegisterSimple("TestSaga", factoryProgressive, "OrderCreated")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error)
	go func() {
		done <- manager.Start(ctx)
	}()

	// Wait for manager to start
	time.Sleep(50 * time.Millisecond)

	// Send events
	subAdapter.SendEvent(adapters.StoredEvent{
		ID:             "e1",
		StreamID:       "order-1",
		Type:           "OrderCreated",
		Data:           []byte(`{}`),
		GlobalPosition: 1,
	})

	// Wait for processing
	time.Sleep(100 * time.Millisecond)

	// Verify saga was created
	state, err := sagaStore.FindByCorrelationID(ctx, "order-1")
	require.NoError(t, err)
	assert.NotNil(t, state)

	// Stop manager
	cancel()
	<-done

	step++ // Use variable to avoid unused warning
	_ = step
}

// ====================================================================
// Concurrency Tests
// ====================================================================

func TestSagaManager_ConcurrentEventProcessing(t *testing.T) {
	memAdapter := memory.NewAdapter()
	eventStore := New(memAdapter)
	sagaStore := newMockSagaStore()
	cmdBus := NewCommandBus()

	manager := NewSagaManager(eventStore,
		WithSagaStore(sagaStore),
		WithCommandBus(cmdBus),
	)

	manager.RegisterSimple("TestSaga", newTestSaga, "OrderCreated")

	ctx := context.Background()
	var wg sync.WaitGroup

	// Process events concurrently
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			event := StoredEvent{
				ID:       string(rune('a' + idx)),
				StreamID: "order-" + string(rune('0'+idx)),
				Type:     "OrderCreated",
				Data:     []byte(`{}`),
			}
			err := manager.ProcessEvent(ctx, event)
			assert.NoError(t, err)
		}(i)
	}

	wg.Wait()

	// Verify all sagas were created
	for i := 0; i < 10; i++ {
		correlationID := "order-" + string(rune('0'+i))
		state, err := sagaStore.FindByCorrelationID(ctx, correlationID)
		assert.NoError(t, err)
		assert.NotNil(t, state)
	}
}

// concurrencyConflictSagaStore simulates concurrency conflicts
type concurrencyConflictSagaStore struct {
	*mockSagaStore
	mu              sync.Mutex
	conflictCount   int
	maxConflicts    int
	saveCalls       int
	successfulSaves int
}

func newConcurrencyConflictSagaStore(maxConflicts int) *concurrencyConflictSagaStore {
	return &concurrencyConflictSagaStore{
		mockSagaStore: newMockSagaStore(),
		maxConflicts:  maxConflicts,
	}
}

func (c *concurrencyConflictSagaStore) Save(ctx context.Context, state *SagaState) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.saveCalls++

	if c.conflictCount < c.maxConflicts {
		c.conflictCount++
		return adapters.ErrConcurrencyConflict
	}

	c.successfulSaves++
	return c.mockSagaStore.Save(ctx, state)
}

func TestSagaManager_ProcessEvent_ConcurrencyConflictRetry(t *testing.T) {
	memAdapter := memory.NewAdapter()
	eventStore := New(memAdapter)

	// Create store that fails with concurrency conflict twice, then succeeds
	sagaStore := newConcurrencyConflictSagaStore(2)

	cmdBus := NewCommandBus()

	manager := NewSagaManager(eventStore,
		WithSagaStore(sagaStore),
		WithCommandBus(cmdBus),
		WithSagaRetryAttempts(3),
		WithSagaRetryDelay(10*time.Millisecond),
	)

	manager.RegisterSimple("TestSaga", newTestSaga, "OrderCreated")

	ctx := context.Background()
	event := StoredEvent{
		ID:             "event-1",
		StreamID:       "order-123",
		Type:           "OrderCreated",
		Data:           []byte(`{}`),
		GlobalPosition: 1,
	}

	err := manager.ProcessEvent(ctx, event)
	require.NoError(t, err)

	// Verify retries happened
	assert.Equal(t, 3, sagaStore.saveCalls, "Expected 3 save calls (2 conflicts + 1 success)")
	assert.Equal(t, 1, sagaStore.successfulSaves, "Expected 1 successful save")

	// Verify saga was created
	state, err := sagaStore.FindByCorrelationID(ctx, "order-123")
	require.NoError(t, err)
	assert.NotNil(t, state)
}

func TestSagaManager_ProcessEvent_ConcurrencyConflictExhausted(t *testing.T) {
	memAdapter := memory.NewAdapter()
	eventStore := New(memAdapter)

	// Create store that always fails with concurrency conflict
	sagaStore := newConcurrencyConflictSagaStore(100) // More conflicts than retries

	cmdBus := NewCommandBus()

	manager := NewSagaManager(eventStore,
		WithSagaStore(sagaStore),
		WithCommandBus(cmdBus),
		WithSagaRetryAttempts(2),
		WithSagaRetryDelay(10*time.Millisecond),
	)

	manager.RegisterSimple("TestSaga", newTestSaga, "OrderCreated")

	ctx := context.Background()
	event := StoredEvent{
		ID:             "event-1",
		StreamID:       "order-123",
		Type:           "OrderCreated",
		Data:           []byte(`{}`),
		GlobalPosition: 1,
	}

	// Note: ProcessEvent (public API) logs errors but continues processing other saga types,
	// so it returns nil even when a specific saga fails. This is by design to allow
	// processing multiple saga types for the same event.
	err := manager.ProcessEvent(ctx, event)
	assert.NoError(t, err, "ProcessEvent logs errors but continues, returns nil")

	// Verify all retries were attempted (initial + retryAttempts = 3)
	assert.Equal(t, 3, sagaStore.saveCalls, "Expected 3 save calls (initial + 2 retries)")
	assert.Equal(t, 3, sagaStore.conflictCount, "Expected 3 conflicts")

	// Saga should not have been saved successfully
	_, err = sagaStore.FindByCorrelationID(ctx, "order-123")
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrSagaNotFound)
}

func TestSagaManager_ProcessEvent_IdempotencyOnRetry(t *testing.T) {
	memAdapter := memory.NewAdapter()
	eventStore := New(memAdapter)

	// Use a regular mock store for this test
	sagaStore := newMockSagaStore()
	cmdBus := NewCommandBus()

	manager := NewSagaManager(eventStore,
		WithSagaStore(sagaStore),
		WithCommandBus(cmdBus),
	)

	manager.RegisterSimple("TestSaga", newTestSaga, "OrderCreated")

	ctx := context.Background()
	event := StoredEvent{
		ID:             "event-1",
		StreamID:       "order-123",
		Type:           "OrderCreated",
		Data:           []byte(`{}`),
		GlobalPosition: 1,
	}

	// Process event first time
	err := manager.ProcessEvent(ctx, event)
	require.NoError(t, err)

	// Get the saga state
	state, err := sagaStore.FindByCorrelationID(ctx, "order-123")
	require.NoError(t, err)

	// Verify that the processed event was recorded
	processedEvents, ok := state.Data["_processedEvents"]
	require.True(t, ok, "Expected _processedEvents to be recorded")
	events, ok := processedEvents.([]interface{})
	require.True(t, ok)
	assert.Len(t, events, 1)
	assert.Equal(t, "event-1:1", events[0])
}

func TestSagaManager_ProcessEvent_ConcurrencyConflictContextCancelled(t *testing.T) {
	memAdapter := memory.NewAdapter()
	eventStore := New(memAdapter)

	// Create store that always fails with concurrency conflict
	sagaStore := newConcurrencyConflictSagaStore(100)

	cmdBus := NewCommandBus()

	manager := NewSagaManager(eventStore,
		WithSagaStore(sagaStore),
		WithCommandBus(cmdBus),
		WithSagaRetryAttempts(10),
		WithSagaRetryDelay(100*time.Millisecond),
	)

	manager.RegisterSimple("TestSaga", newTestSaga, "OrderCreated")

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	event := StoredEvent{
		ID:             "event-1",
		StreamID:       "order-123",
		Type:           "OrderCreated",
		Data:           []byte(`{}`),
		GlobalPosition: 1,
	}

	// Note: ProcessEvent (public API) logs errors but continues processing,
	// returning nil. The context cancellation is handled during retry delays.
	err := manager.ProcessEvent(ctx, event)
	assert.NoError(t, err, "ProcessEvent logs errors but returns nil")

	// Verify that processing stopped early due to context cancellation
	// (shouldn't reach all 10 retries)
	assert.Less(t, sagaStore.saveCalls, 5, "Should stop before exhausting retries due to context timeout")
}

// sagaTestLogger implements Logger for testing
type sagaTestLogger struct {
	mu       sync.Mutex
	messages []string
}

func (l *sagaTestLogger) Debug(msg string, keyvals ...interface{}) {
	l.log("DEBUG", msg, keyvals...)
}
func (l *sagaTestLogger) Info(msg string, keyvals ...interface{}) {
	l.log("INFO", msg, keyvals...)
}
func (l *sagaTestLogger) Warn(msg string, keyvals ...interface{}) {
	l.log("WARN", msg, keyvals...)
}
func (l *sagaTestLogger) Error(msg string, keyvals ...interface{}) {
	l.log("ERROR", msg, keyvals...)
}

func (l *sagaTestLogger) log(level, msg string, keyvals ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.messages = append(l.messages, level+": "+msg)
}

// ====================================================================
// AsyncResult Tests
// ====================================================================

func TestAsyncResult_Done(t *testing.T) {
	ctx := context.Background()
	result := newAsyncResult(ctx)

	// Should not be complete initially
	assert.False(t, result.IsComplete())

	// Complete the result
	go func() {
		time.Sleep(10 * time.Millisecond)
		result.complete(nil)
	}()

	// Wait for done
	select {
	case <-result.Done():
		assert.True(t, result.IsComplete())
		assert.NoError(t, result.Err())
	case <-time.After(time.Second):
		t.Fatal("Timed out waiting for result")
	}
}

func TestAsyncResult_Wait(t *testing.T) {
	ctx := context.Background()
	result := newAsyncResult(ctx)

	expectedErr := errors.New("test error")

	go func() {
		time.Sleep(10 * time.Millisecond)
		result.complete(expectedErr)
	}()

	err := result.Wait()
	assert.ErrorIs(t, err, expectedErr)
	assert.True(t, result.IsComplete())
}

func TestAsyncResult_WaitWithTimeout_Success(t *testing.T) {
	ctx := context.Background()
	result := newAsyncResult(ctx)

	go func() {
		time.Sleep(10 * time.Millisecond)
		result.complete(nil)
	}()

	err := result.WaitWithTimeout(time.Second)
	assert.NoError(t, err)
	assert.True(t, result.IsComplete())
}

func TestAsyncResult_WaitWithTimeout_Timeout(t *testing.T) {
	ctx := context.Background()
	result := newAsyncResult(ctx)

	// Don't complete the result

	err := result.WaitWithTimeout(50 * time.Millisecond)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.False(t, result.IsComplete())
}

func TestAsyncResult_Cancel(t *testing.T) {
	ctx := context.Background()
	result := newAsyncResult(ctx)

	// Verify context is initially active
	assert.NoError(t, result.Context().Err())

	// Cancel the result
	result.Cancel()

	// Verify context is cancelled
	assert.ErrorIs(t, result.Context().Err(), context.Canceled)
}

func TestAsyncResult_IsComplete(t *testing.T) {
	ctx := context.Background()
	result := newAsyncResult(ctx)

	// Initially not complete
	assert.False(t, result.IsComplete())

	// Complete it
	result.complete(nil)

	// Now complete
	assert.True(t, result.IsComplete())
}

func TestAsyncResult_DoubleComplete(t *testing.T) {
	ctx := context.Background()
	result := newAsyncResult(ctx)

	// Complete the result
	result.complete(nil)

	// Calling complete again should not panic
	assert.NotPanics(t, func() {
		result.complete(errors.New("second error"))
	})

	// Original error (nil) should be preserved
	assert.NoError(t, result.Err())
}

func TestAsyncResult_Err_BeforeComplete(t *testing.T) {
	ctx := context.Background()
	result := newAsyncResult(ctx)

	// Error should be nil before completion
	assert.Nil(t, result.Err())
}

// ====================================================================
// SagaManager Async Tests
// ====================================================================

func TestSagaManager_StartAsync(t *testing.T) {
	memAdapter := memory.NewAdapter()
	subAdapter := newMockSubscriptionAdapter()
	eventStoreWithSub := &mockEventStoreWithSubscription{
		MemoryAdapter: memAdapter,
		subAdapter:    subAdapter,
	}
	eventStore := New(eventStoreWithSub)
	sagaStore := newMockSagaStore()
	cmdBus := NewCommandBus()

	manager := NewSagaManager(eventStore,
		WithSagaStore(sagaStore),
		WithCommandBus(cmdBus),
	)

	manager.RegisterSimple("TestSaga", newTestSaga, "OrderCreated")

	ctx := context.Background()
	result := manager.StartAsync(ctx)

	// Wait a moment for manager to start
	time.Sleep(50 * time.Millisecond)

	// Verify it's running asynchronously
	assert.False(t, result.IsComplete())
	assert.True(t, manager.IsRunning())

	// Send an event
	subAdapter.SendEvent(adapters.StoredEvent{
		ID:             "evt-1",
		StreamID:       "order-123",
		Type:           "OrderCreated",
		Data:           []byte(`{}`),
		GlobalPosition: 1,
	})

	// Wait for event to be processed
	time.Sleep(100 * time.Millisecond)

	// Verify saga was created
	state, err := sagaStore.FindByCorrelationID(ctx, "order-123")
	assert.NoError(t, err)
	assert.NotNil(t, state)

	// Cancel and wait for shutdown
	result.Cancel()
	err = result.WaitWithTimeout(time.Second)
	assert.ErrorIs(t, err, context.Canceled)
	assert.True(t, result.IsComplete())

	// Give the manager time to stop
	time.Sleep(50 * time.Millisecond)
	assert.False(t, manager.IsRunning())
}

func TestSagaManager_StartAsync_AlreadyRunning(t *testing.T) {
	memAdapter := memory.NewAdapter()
	subAdapter := newMockSubscriptionAdapter()
	eventStoreWithSub := &mockEventStoreWithSubscription{
		MemoryAdapter: memAdapter,
		subAdapter:    subAdapter,
	}
	eventStore := New(eventStoreWithSub)
	sagaStore := newMockSagaStore()
	cmdBus := NewCommandBus()

	manager := NewSagaManager(eventStore,
		WithSagaStore(sagaStore),
		WithCommandBus(cmdBus),
	)

	manager.RegisterSimple("TestSaga", newTestSaga, "OrderCreated")

	ctx := context.Background()
	result1 := manager.StartAsync(ctx)

	// Wait for it to start
	time.Sleep(50 * time.Millisecond)
	assert.True(t, manager.IsRunning())

	// Try to start again
	result2 := manager.StartAsync(ctx)

	// Second start should fail immediately
	err := result2.WaitWithTimeout(time.Second)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already running")

	// Cleanup
	result1.Cancel()
	_ = result1.WaitWithTimeout(time.Second)
}

func TestSagaManager_StartSaga_Success(t *testing.T) {
	memAdapter := memory.NewAdapter()
	eventStore := New(memAdapter)
	sagaStore := newMockSagaStore()
	cmdBus := NewCommandBus()

	manager := NewSagaManager(eventStore,
		WithSagaStore(sagaStore),
		WithCommandBus(cmdBus),
	)

	manager.RegisterSimple("TestSaga", newTestSaga, "OrderCreated")

	ctx := context.Background()
	triggerEvent := StoredEvent{
		ID:       "evt-1",
		StreamID: "order-123",
		Type:     "OrderCreated",
		Data:     []byte(`{}`),
	}

	err := manager.StartSaga(ctx, "TestSaga", triggerEvent)
	assert.NoError(t, err)

	// Verify saga was created
	state, err := sagaStore.FindByCorrelationID(ctx, "order-123")
	assert.NoError(t, err)
	assert.NotNil(t, state)
	assert.Equal(t, "TestSaga", state.Type)
}

func TestSagaManager_StartSaga_UnregisteredType(t *testing.T) {
	memAdapter := memory.NewAdapter()
	eventStore := New(memAdapter)
	sagaStore := newMockSagaStore()
	cmdBus := NewCommandBus()

	manager := NewSagaManager(eventStore,
		WithSagaStore(sagaStore),
		WithCommandBus(cmdBus),
	)

	ctx := context.Background()
	triggerEvent := StoredEvent{
		ID:       "evt-1",
		StreamID: "order-123",
		Type:     "OrderCreated",
		Data:     []byte(`{}`),
	}

	err := manager.StartSaga(ctx, "UnknownSaga", triggerEvent)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not registered")
}

func TestSagaManager_StartSaga_InvalidStartingEvent(t *testing.T) {
	memAdapter := memory.NewAdapter()
	eventStore := New(memAdapter)
	sagaStore := newMockSagaStore()
	cmdBus := NewCommandBus()

	manager := NewSagaManager(eventStore,
		WithSagaStore(sagaStore),
		WithCommandBus(cmdBus),
	)

	// Only OrderCreated can start the saga
	manager.RegisterSimple("TestSaga", newTestSaga, "OrderCreated")

	ctx := context.Background()
	triggerEvent := StoredEvent{
		ID:       "evt-1",
		StreamID: "order-123",
		Type:     "ItemAdded", // Not a starting event
		Data:     []byte(`{}`),
	}

	err := manager.StartSaga(ctx, "TestSaga", triggerEvent)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not a starting event")
}

func TestSagaManager_StartSagaAsync_Success(t *testing.T) {
	memAdapter := memory.NewAdapter()
	eventStore := New(memAdapter)
	sagaStore := newMockSagaStore()
	cmdBus := NewCommandBus()

	manager := NewSagaManager(eventStore,
		WithSagaStore(sagaStore),
		WithCommandBus(cmdBus),
	)

	manager.RegisterSimple("TestSaga", newTestSaga, "OrderCreated")

	ctx := context.Background()
	triggerEvent := StoredEvent{
		ID:       "evt-1",
		StreamID: "order-456",
		Type:     "OrderCreated",
		Data:     []byte(`{}`),
	}

	result := manager.StartSagaAsync(ctx, "TestSaga", triggerEvent)

	// Wait for completion
	err := result.WaitWithTimeout(time.Second)
	assert.NoError(t, err)
	assert.True(t, result.IsComplete())

	// Verify saga was created
	state, err := sagaStore.FindByCorrelationID(ctx, "order-456")
	assert.NoError(t, err)
	assert.NotNil(t, state)
}

func TestSagaManager_StartSagaAsync_Error(t *testing.T) {
	memAdapter := memory.NewAdapter()
	eventStore := New(memAdapter)
	sagaStore := newMockSagaStore()
	cmdBus := NewCommandBus()

	manager := NewSagaManager(eventStore,
		WithSagaStore(sagaStore),
		WithCommandBus(cmdBus),
	)

	// Don't register the saga type

	ctx := context.Background()
	triggerEvent := StoredEvent{
		ID:       "evt-1",
		StreamID: "order-123",
		Type:     "OrderCreated",
		Data:     []byte(`{}`),
	}

	result := manager.StartSagaAsync(ctx, "UnknownSaga", triggerEvent)

	// Wait for completion
	err := result.WaitWithTimeout(time.Second)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not registered")
	assert.True(t, result.IsComplete())
}

func TestSagaManager_StartAsync_MultipleSagas(t *testing.T) {
	memAdapter := memory.NewAdapter()
	subAdapter := newMockSubscriptionAdapter()
	eventStoreWithSub := &mockEventStoreWithSubscription{
		MemoryAdapter: memAdapter,
		subAdapter:    subAdapter,
	}
	eventStore := New(eventStoreWithSub)
	sagaStore := newMockSagaStore()
	cmdBus := NewCommandBus()

	manager := NewSagaManager(eventStore,
		WithSagaStore(sagaStore),
		WithCommandBus(cmdBus),
	)

	manager.RegisterSimple("TestSaga", newTestSaga, "OrderCreated")

	ctx := context.Background()
	result := manager.StartAsync(ctx)

	// Wait a moment for manager to start
	time.Sleep(50 * time.Millisecond)

	// Send multiple events
	for i := 0; i < 5; i++ {
		subAdapter.SendEvent(adapters.StoredEvent{
			ID:             fmt.Sprintf("evt-%d", i+1),
			StreamID:       fmt.Sprintf("order-%d", i),
			Type:           "OrderCreated",
			Data:           []byte(`{}`),
			GlobalPosition: uint64(i + 1),
		})
	}

	// Wait for processing
	time.Sleep(200 * time.Millisecond)

	// Verify all sagas were created
	for i := 0; i < 5; i++ {
		correlationID := fmt.Sprintf("order-%d", i)
		state, err := sagaStore.FindByCorrelationID(ctx, correlationID)
		assert.NoError(t, err, "Failed to find saga for %s", correlationID)
		assert.NotNil(t, state)
	}

	// Cleanup
	result.Cancel()
	_ = result.WaitWithTimeout(time.Second)
}
