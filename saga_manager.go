package mink

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/AshkanYarmoradi/go-mink/adapters"
)

// SagaManagerOption configures a SagaManager.
type SagaManagerOption func(*SagaManager)

// WithSagaStore sets the saga store.
func WithSagaStore(store SagaStore) SagaManagerOption {
	return func(m *SagaManager) {
		m.store = store
	}
}

// WithSagaLogger sets the logger.
func WithSagaLogger(logger Logger) SagaManagerOption {
	return func(m *SagaManager) {
		m.logger = logger
	}
}

// WithSagaSerializer sets the serializer for saga data.
func WithSagaSerializer(serializer Serializer) SagaManagerOption {
	return func(m *SagaManager) {
		m.serializer = serializer
	}
}

// WithCommandBus sets the command bus for dispatching commands.
func WithCommandBus(bus *CommandBus) SagaManagerOption {
	return func(m *SagaManager) {
		m.commandBus = bus
	}
}

// WithSagaPollInterval sets the polling interval for event subscription.
func WithSagaPollInterval(d time.Duration) SagaManagerOption {
	return func(m *SagaManager) {
		m.pollInterval = d
	}
}

// WithSagaRetryAttempts sets the number of retry attempts for failed commands.
func WithSagaRetryAttempts(attempts int) SagaManagerOption {
	return func(m *SagaManager) {
		m.retryAttempts = attempts
	}
}

// WithSagaRetryDelay sets the delay between retry attempts.
func WithSagaRetryDelay(d time.Duration) SagaManagerOption {
	return func(m *SagaManager) {
		m.retryDelay = d
	}
}

// SagaManager orchestrates saga lifecycle and event processing.
// It subscribes to events, routes them to appropriate sagas,
// and dispatches resulting commands.
type SagaManager struct {
	eventStore *EventStore
	commandBus *CommandBus
	store      SagaStore
	serializer Serializer
	logger     Logger

	// Registry maps saga types to their factories
	registry map[string]SagaFactory

	// correlations defines how events map to sagas
	correlations map[string][]SagaCorrelation

	// eventHandlers maps event types to saga types that handle them
	eventHandlers map[string][]string

	// Configuration
	pollInterval  time.Duration
	retryAttempts int
	retryDelay    time.Duration

	// State
	mu       sync.RWMutex
	position uint64
	running  bool
	cancel   context.CancelFunc
}

// NewSagaManager creates a new SagaManager.
func NewSagaManager(eventStore *EventStore, opts ...SagaManagerOption) *SagaManager {
	m := &SagaManager{
		eventStore:    eventStore,
		registry:      make(map[string]SagaFactory),
		correlations:  make(map[string][]SagaCorrelation),
		eventHandlers: make(map[string][]string),
		logger:        &noopLogger{},
		serializer:    NewJSONSerializer(),
		pollInterval:  100 * time.Millisecond,
		retryAttempts: 3,
		retryDelay:    time.Second,
	}

	for _, opt := range opts {
		opt(m)
	}

	return m
}

// Register registers a saga type with its factory and correlation configuration.
func (m *SagaManager) Register(sagaType string, factory SagaFactory, correlation SagaCorrelation) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.registry[sagaType] = factory
	m.correlations[sagaType] = append(m.correlations[sagaType], correlation)

	// Create a temporary saga to get handled events
	saga := factory("")
	for _, eventType := range saga.HandledEvents() {
		m.eventHandlers[eventType] = append(m.eventHandlers[eventType], sagaType)
	}

	m.logger.Info("Registered saga", "type", sagaType, "events", saga.HandledEvents())
}

// RegisterSimple registers a saga with a simple correlation based on event stream ID.
func (m *SagaManager) RegisterSimple(sagaType string, factory SagaFactory, startingEvents ...string) {
	correlation := SagaCorrelation{
		SagaType:       sagaType,
		StartingEvents: startingEvents,
		CorrelationIDFunc: func(event StoredEvent) string {
			return event.StreamID
		},
	}
	m.Register(sagaType, factory, correlation)
}

// Start begins processing events and routing them to sagas.
// This method blocks until the context is cancelled.
func (m *SagaManager) Start(ctx context.Context) error {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return errors.New("mink: saga manager already running")
	}
	if m.store == nil {
		m.mu.Unlock()
		return errors.New("mink: saga store is required")
	}
	if m.commandBus == nil {
		m.mu.Unlock()
		return errors.New("mink: command bus is required")
	}

	ctx, m.cancel = context.WithCancel(ctx)
	m.running = true
	m.mu.Unlock()

	defer func() {
		m.mu.Lock()
		m.running = false
		m.mu.Unlock()
	}()

	m.logger.Info("Saga manager started", "position", m.position)

	// Subscribe to all events from the last processed position
	adapter := m.eventStore.Adapter()
	subscriber, ok := adapter.(adapters.SubscriptionAdapter)
	if !ok {
		return ErrSubscriptionNotSupported
	}

	eventCh, err := subscriber.SubscribeAll(ctx, m.position)
	if err != nil {
		return fmt.Errorf("mink: failed to subscribe: %w", err)
	}

	for {
		select {
		case <-ctx.Done():
			m.logger.Info("Saga manager stopped")
			return ctx.Err()

		case event, ok := <-eventCh:
			if !ok {
				m.logger.Info("Event channel closed")
				return nil
			}

			if err := m.processEvent(ctx, m.adaptEvent(event)); err != nil {
				m.logger.Error("Failed to process event", "error", err, "eventType", event.Type)
				// Continue processing - don't stop on individual event failures
			}

			m.mu.Lock()
			m.position = event.GlobalPosition + 1
			m.mu.Unlock()
		}
	}
}

// adaptEvent converts adapters.StoredEvent to mink.StoredEvent
func (m *SagaManager) adaptEvent(e adapters.StoredEvent) StoredEvent {
	return StoredEvent{
		ID:             e.ID,
		StreamID:       e.StreamID,
		Type:           e.Type,
		Data:           e.Data,
		Metadata:       Metadata(e.Metadata),
		Version:        e.Version,
		GlobalPosition: e.GlobalPosition,
		Timestamp:      e.Timestamp,
	}
}

// Stop gracefully stops the saga manager.
func (m *SagaManager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.cancel != nil {
		m.cancel()
	}
}

// IsRunning returns true if the saga manager is running.
func (m *SagaManager) IsRunning() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.running
}

// Position returns the current event position.
func (m *SagaManager) Position() uint64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.position
}

// SetPosition sets the starting position for event processing.
func (m *SagaManager) SetPosition(pos uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.position = pos
}

// processEvent routes an event to appropriate sagas.
func (m *SagaManager) processEvent(ctx context.Context, event StoredEvent) error {
	m.mu.RLock()
	sagaTypes := m.eventHandlers[event.Type]
	m.mu.RUnlock()

	if len(sagaTypes) == 0 {
		// No saga handles this event type
		return nil
	}

	for _, sagaType := range sagaTypes {
		if err := m.processSagaEvent(ctx, sagaType, event); err != nil {
			m.logger.Error("Failed to process saga event",
				"sagaType", sagaType,
				"eventType", event.Type,
				"error", err)
			// Continue processing other saga types
		}
	}

	return nil
}

// processSagaEvent processes an event for a specific saga type.
func (m *SagaManager) processSagaEvent(ctx context.Context, sagaType string, event StoredEvent) error {
	m.mu.RLock()
	correlations := m.correlations[sagaType]
	factory := m.registry[sagaType]
	m.mu.RUnlock()

	if factory == nil {
		return fmt.Errorf("mink: saga factory not found for type %q", sagaType)
	}

	// Find or create saga based on correlation
	var saga Saga
	var state *SagaState
	var isNew bool

	for _, correlation := range correlations {
		correlationID := correlation.CorrelationIDFunc(event)
		if correlationID == "" {
			continue
		}

		// Try to find existing saga
		var err error
		state, err = m.store.FindByCorrelationID(ctx, correlationID)
		if err == nil {
			// Found existing saga - hydrate it
			saga = factory(state.ID)
			if err := m.hydrateSaga(saga, state); err != nil {
				return fmt.Errorf("mink: failed to hydrate saga: %w", err)
			}
			break
		}

		if !errors.Is(err, ErrSagaNotFound) {
			return fmt.Errorf("mink: failed to find saga: %w", err)
		}

		// Check if this event can start a new saga
		if isStartingEvent(correlation.StartingEvents, event.Type) {
			// Create new saga
			sagaID := fmt.Sprintf("%s-%s", sagaType, correlationID)
			saga = factory(sagaID)
			saga.SetCorrelationID(correlationID)
			saga.SetStatus(SagaStatusStarted)
			saga.SetStartedAt(time.Now())
			isNew = true
			m.logger.Info("Creating new saga",
				"sagaType", sagaType,
				"sagaID", sagaID,
				"correlationID", correlationID,
				"triggerEvent", event.Type)
			break
		}
	}

	if saga == nil {
		// No saga found and event doesn't start one
		return nil
	}

	// Skip if saga is in terminal state
	if !isNew && saga.Status().IsTerminal() {
		m.logger.Debug("Skipping terminal saga",
			"sagaID", saga.SagaID(),
			"status", saga.Status())
		return nil
	}

	// Handle the event
	saga.SetStatus(SagaStatusRunning)
	commands, err := saga.HandleEvent(ctx, event)
	if err != nil {
		return m.handleSagaFailure(ctx, saga, err)
	}

	// Execute resulting commands
	for _, cmd := range commands {
		if err := m.dispatchCommand(ctx, saga, cmd); err != nil {
			return m.handleSagaFailure(ctx, saga, err)
		}
	}

	// Check if saga completed
	if saga.IsComplete() {
		saga.SetStatus(SagaStatusCompleted)
		now := time.Now()
		saga.SetCompletedAt(&now)
		m.logger.Info("Saga completed",
			"sagaID", saga.SagaID(),
			"sagaType", saga.SagaType())
	}

	// Persist saga state
	saga.IncrementVersion()
	return m.saveSaga(ctx, saga)
}

// handleSagaFailure handles a saga failure by triggering compensation.
func (m *SagaManager) handleSagaFailure(ctx context.Context, saga Saga, originalErr error) error {
	m.logger.Error("Saga failed, starting compensation",
		"sagaID", saga.SagaID(),
		"sagaType", saga.SagaType(),
		"error", originalErr)

	saga.SetStatus(SagaStatusCompensating)

	// Get compensation commands
	compensateCommands, err := saga.Compensate(ctx, saga.CurrentStep(), originalErr)
	if err != nil {
		m.logger.Error("Failed to get compensation commands",
			"sagaID", saga.SagaID(),
			"error", err)
		saga.SetStatus(SagaStatusFailed)
		return m.saveSaga(ctx, saga)
	}

	// Execute compensation commands, tracking any failures
	var compensationFailed bool
	for _, cmd := range compensateCommands {
		if err := m.dispatchCommand(ctx, saga, cmd); err != nil {
			m.logger.Error("Compensation command failed",
				"sagaID", saga.SagaID(),
				"command", cmd.CommandType(),
				"error", err)
			compensationFailed = true
			// Continue with other compensation commands to attempt full rollback
		}
	}

	// Set final status based on whether all compensations succeeded
	if compensationFailed {
		saga.SetStatus(SagaStatusCompensationFailed)
		m.logger.Warn("Saga compensation partially failed",
			"sagaID", saga.SagaID(),
			"sagaType", saga.SagaType())
	} else {
		saga.SetStatus(SagaStatusCompensated)
	}

	now := time.Now()
	saga.SetCompletedAt(&now)

	return m.saveSaga(ctx, saga)
}

// dispatchCommand dispatches a command with retry logic using exponential backoff.
func (m *SagaManager) dispatchCommand(ctx context.Context, saga Saga, cmd Command) error {
	var lastErr error

	for attempt := 0; attempt < m.retryAttempts; attempt++ {
		if attempt > 0 {
			// Exponential backoff: retryDelay, 2*retryDelay, 4*retryDelay, ...
			delay := m.retryDelay * time.Duration(1<<uint(attempt-1))
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
		}

		result, err := m.commandBus.Dispatch(ctx, cmd)
		if err == nil && result.Success {
			m.logger.Debug("Command dispatched",
				"sagaID", saga.SagaID(),
				"command", cmd.CommandType(),
				"attempt", attempt+1)
			return nil
		}

		if err != nil {
			lastErr = err
		} else if result.Error != nil {
			lastErr = result.Error
		}

		m.logger.Warn("Command dispatch failed, retrying",
			"sagaID", saga.SagaID(),
			"command", cmd.CommandType(),
			"attempt", attempt+1,
			"error", lastErr)
	}

	return fmt.Errorf("mink: command %q failed after %d attempts: %w",
		cmd.CommandType(), m.retryAttempts, lastErr)
}

// saveSaga persists the saga state.
func (m *SagaManager) saveSaga(ctx context.Context, saga Saga) error {
	state := &SagaState{
		ID:            saga.SagaID(),
		Type:          saga.SagaType(),
		CorrelationID: saga.CorrelationID(),
		Status:        saga.Status(),
		CurrentStep:   saga.CurrentStep(),
		Data:          saga.Data(),
		StartedAt:     saga.StartedAt(),
		UpdatedAt:     time.Now(),
		CompletedAt:   saga.CompletedAt(),
		Version:       saga.Version(),
	}

	return m.store.Save(ctx, state)
}

// hydrateSaga restores a saga's state from persisted data.
func (m *SagaManager) hydrateSaga(saga Saga, state *SagaState) error {
	saga.SetStatus(state.Status)
	saga.SetCurrentStep(state.CurrentStep)
	saga.SetCorrelationID(state.CorrelationID)
	saga.SetStartedAt(state.StartedAt)
	saga.SetCompletedAt(state.CompletedAt)
	saga.SetVersion(state.Version)
	saga.SetData(state.Data)

	return nil
}

// isStartingEvent checks if an event type can start a new saga.
func isStartingEvent(startingEvents []string, eventType string) bool {
	for _, e := range startingEvents {
		if e == eventType {
			return true
		}
	}
	return false
}

// ProcessEvent manually processes a single event (for testing or manual replay).
func (m *SagaManager) ProcessEvent(ctx context.Context, event StoredEvent) error {
	return m.processEvent(ctx, event)
}

// GetSaga retrieves a saga by its ID.
func (m *SagaManager) GetSaga(ctx context.Context, sagaID string) (*SagaState, error) {
	return m.store.Load(ctx, sagaID)
}

// FindSagaByCorrelationID finds a saga by its correlation ID.
func (m *SagaManager) FindSagaByCorrelationID(ctx context.Context, correlationID string) (*SagaState, error) {
	return m.store.FindByCorrelationID(ctx, correlationID)
}

// SagaStateToJSON converts saga state to JSON for persistence.
func SagaStateToJSON(state *SagaState) ([]byte, error) {
	return json.Marshal(state)
}

// SagaStateFromJSON parses saga state from JSON.
func SagaStateFromJSON(data []byte) (*SagaState, error) {
	state := &SagaState{}
	if err := json.Unmarshal(data, state); err != nil {
		return nil, err
	}
	return state, nil
}
