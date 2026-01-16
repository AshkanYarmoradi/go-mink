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

// maxProcessedEventsToTrack is the maximum number of processed events to retain
// for idempotency tracking. This prevents unbounded growth of saga data while
// still providing sufficient history for retry scenarios.
const maxProcessedEventsToTrack = 100

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
//
// # Concurrency and Idempotency
//
// SagaManager provides several mechanisms to ensure correct saga processing
// under concurrent access:
//
//  1. Per-Saga Locking: Each saga ID has an associated mutex that serializes
//     access. This prevents race conditions when the same event is delivered
//     from multiple sources (e.g., pg_notify + polling) or when multiple
//     events for the same saga arrive simultaneously.
//
//  2. Fresh State Loading: Before processing each event, the saga state is
//     loaded fresh from the store. This ensures terminal status checks and
//     idempotency checks see the latest state.
//
//  3. Event Idempotency: Processed events are tracked in the SagaState.ProcessedEvents
//     field (not in the saga's Data map) to prevent duplicate processing on retries.
//     This is handled transparently by the SagaManager - saga implementations don't
//     need to preserve these internal tracking fields.
//
//  4. Optimistic Concurrency: The saga store uses version-based optimistic
//     concurrency control. On conflict, the event is retried with fresh state.
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

	// sagaLocks provides per-saga ID serialization to prevent concurrent
	// modification of the same saga instance. This ensures that when multiple
	// event sources (e.g., pg_notify + polling) deliver the same event, only
	// one goroutine processes it at a time for a given saga.
	//
	// Note on memory growth: Locks are stored indefinitely and not cleaned up.
	// Each unique saga ID consumes a small per-saga overhead (sync.Mutex plus
	// map entry). For most applications with bounded saga lifecycles, this is
	// negligible.
	//
	// For applications that may create millions of unique saga IDs, you should
	// plan an explicit cleanup/rotation strategy at the application level. Two
	// common patterns are:
	//   - Periodically recreating the SagaManager in long-running workers so
	//     that the internal lock map is reset on a controlled schedule.
	//   - Sharding work across multiple SagaManager instances (for different
	//     saga types or tenants) so that the number of distinct saga IDs per
	//     process remains bounded.
	//
	// In addition, consider exporting and monitoring metrics (for example via
	// the middleware/metrics package) such as memory usage and the number of
	// active saga instances per process. Sudden or unbounded growth in these
	// metrics is a signal to tighten your rotation policy or sharding model.
	sagaLocks sync.Map // map[string]*sync.Mutex

	// processedEvents tracks which events have been processed by each saga.
	// This is used for idempotency tracking and is stored in SagaState.ProcessedEvents
	// when persisted. The key is saga ID, the value is a slice of event keys.
	//
	// Note on memory growth: Like sagaLocks, entries in processedEvents are stored
	// indefinitely and are not automatically cleaned up. Each unique saga ID holds
	// a slice of processed event keys, which can consume more memory per saga than
	// a single mutex pointer.
	//
	// The maxProcessedEventsToTrack constant caps how many processed events are
	// retained per saga instance, but the number of distinct saga IDs in this map
	// can still grow over time in long-running processes.
	//
	// For applications with many unique saga IDs or long-lived workers, consider
	// an explicit cleanup/rotation strategy at the application level (e.g.,
	// periodically recreating the SagaManager, or sharding across instances).
	processedEvents sync.Map // map[string][]string
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

// getSagaLock returns the mutex for a specific saga ID, creating one if needed.
// This ensures serialized access to each saga, preventing race conditions when
// multiple event sources deliver the same event or concurrent events for the same saga.
func (m *SagaManager) getSagaLock(sagaID string) *sync.Mutex {
	lock, _ := m.sagaLocks.LoadOrStore(sagaID, &sync.Mutex{})
	return lock.(*sync.Mutex)
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
// It implements optimistic concurrency control with retry on conflicts
// and per-saga locking to serialize access to the same saga instance.
func (m *SagaManager) processSagaEvent(ctx context.Context, sagaType string, event StoredEvent) error {
	m.mu.RLock()
	correlations := m.correlations[sagaType]
	factory := m.registry[sagaType]
	m.mu.RUnlock()

	if factory == nil {
		return fmt.Errorf("mink: saga factory not found for type %q", sagaType)
	}

	// Determine the saga ID from the correlation.
	// NOTE: resolveSagaID performs a store lookup to find existing saga state.
	// This lookup is done BEFORE acquiring the per-saga lock to determine which
	// lock to acquire. The returned state is NOT used for processing - we always
	// reload fresh state after acquiring the lock to avoid race conditions.
	sagaID, correlationID, _, isStarting := m.resolveSagaID(ctx, sagaType, event, correlations)
	if sagaID == "" && !isStarting {
		// No existing saga found and event doesn't start one
		return nil
	}

	// If we have a saga ID (existing saga), acquire a per-saga lock
	// For new sagas, we use the potential saga ID to prevent duplicate creation
	lockID := sagaID
	if lockID == "" {
		// For new sagas, use the correlation-based ID that would be created
		lockID = fmt.Sprintf("%s-%s", sagaType, correlationID)
	}

	// Acquire per-saga lock to serialize access
	sagaLock := m.getSagaLock(lockID)
	sagaLock.Lock()
	defer sagaLock.Unlock()

	// Retry loop for handling concurrency conflicts
	var lastErr error
	for attempt := 0; attempt < m.retryAttempts; attempt++ {
		if attempt > 0 {
			// Use exponential backoff for retry delay, consistent with dispatchCommand
			delay := m.retryDelay * time.Duration(1<<uint(attempt-1))
			m.logger.Debug("Retrying saga event processing after concurrency conflict",
				"sagaType", sagaType,
				"event", event.Type,
				"attempt", attempt,
				"delay", delay)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
		}

		// IMPORTANT: For existing sagas, we must ALWAYS reload the state after acquiring
		// the lock to ensure we have the latest version. The pre-loaded state from
		// resolveSagaID may be stale if another goroutine modified the saga between
		// when we loaded it and when we acquired the lock.
		//
		// For NEW sagas (isStarting=true), we can use nil since there's no existing
		// state to reload - the saga will be created fresh.
		//
		// Previously, we used the pre-loaded resolvedState on the first attempt as an
		// optimization, but this caused race conditions where stale ProcessedEvents
		// were restored, leading to duplicate event processing.
		err := m.attemptProcessSagaEvent(ctx, sagaType, event, correlations, factory, nil)
		if err == nil {
			return nil
		}

		if errors.Is(err, ErrConcurrencyConflict) || errors.Is(err, adapters.ErrConcurrencyConflict) {
			lastErr = err
			m.logger.Debug("Concurrency conflict detected, will retry",
				"sagaType", sagaType,
				"event", event.Type,
				"attempt", attempt,
				"maxAttempts", m.retryAttempts)
			continue
		}

		// Non-retryable error
		return err
	}

	return fmt.Errorf("mink: saga event processing failed after %d retries: %w", m.retryAttempts, lastErr)
}

// resolveSagaID determines the saga ID for an event based on correlations.
// It returns the saga ID and loaded state if an existing saga is found, or empty values if not.
// It also returns whether this event could start a new saga.
// The returned state can be passed to attemptProcessSagaEvent to avoid duplicate store queries.
func (m *SagaManager) resolveSagaID(ctx context.Context, sagaType string, event StoredEvent, correlations []SagaCorrelation) (sagaID, correlationID string, state *SagaState, isStarting bool) {
	for _, correlation := range correlations {
		corrID := correlation.CorrelationIDFunc(event)
		if corrID == "" {
			continue
		}

		// Try to find existing saga
		loadedState, err := m.store.FindByCorrelationID(ctx, corrID)
		if err == nil {
			return loadedState.ID, corrID, loadedState, false
		}

		// Check if this event can start a new saga
		if isStartingEvent(correlation.StartingEvents, event.Type) {
			return "", corrID, nil, true
		}
	}

	return "", "", nil, false
}

// attemptProcessSagaEvent performs a single attempt to process an event for a saga.
// This method is called under a per-saga lock, so we are guaranteed to have exclusive
// access to this saga instance.
//
// The preloadedState parameter allows passing a state that was already loaded by
// resolveSagaID to avoid duplicate store queries on the first attempt. Pass nil
// to force a fresh load from the store (used on retry attempts).
func (m *SagaManager) attemptProcessSagaEvent(
	ctx context.Context,
	sagaType string,
	event StoredEvent,
	correlations []SagaCorrelation,
	factory SagaFactory,
	preloadedState *SagaState,
) error {
	// Find or create saga based on correlation
	var saga Saga
	var state *SagaState
	var isNew bool

	for _, correlation := range correlations {
		correlationID := correlation.CorrelationIDFunc(event)
		if correlationID == "" {
			continue
		}

		// Use preloaded state if available (first attempt), otherwise load fresh.
		//
		// Notes on behavior:
		//   - First attempt: preloadedState is provided for a specific correlation ID.
		//     We only reuse it when its CorrelationID matches the current correlation.
		//     For other correlations in this loop, we intentionally fall back to
		//     loading state from the store.
		//   - Retries: preloadedState is set to nil before calling this function, so
		//     this branch is skipped and we always load the latest state from the
		//     store. This ensures we do not rely on potentially stale preloaded
		//     state when re-processing an event.
		//   - If FindByCorrelationID returns ErrSagaNotFound, state remains nil and
		//     we rely on the isStartingEvent check below to decide whether a new
		//     saga should be created for this correlation ID.
		if preloadedState != nil && preloadedState.CorrelationID == correlationID {
			state = preloadedState
			m.logger.Debug("Using preloaded state",
				"correlationID", correlationID,
				"version", state.Version,
				"status", state.Status)
		} else {
			// Load fresh state from store - critical for retries to see latest state
			var err error
			state, err = m.store.FindByCorrelationID(ctx, correlationID)
			if err != nil && !errors.Is(err, ErrSagaNotFound) {
				return fmt.Errorf("mink: failed to find saga: %w", err)
			}
			if state != nil {
				m.logger.Debug("Loaded saga state from store",
					"correlationID", correlationID,
					"sagaID", state.ID,
					"version", state.Version,
					"status", state.Status)
			}
		}

		if state != nil {
			// Found existing saga - hydrate it with state
			saga = factory(state.ID)
			if err := m.hydrateSaga(saga, state); err != nil {
				return fmt.Errorf("mink: failed to hydrate saga: %w", err)
			}
			break
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
		m.logger.Info("Skipping terminal saga - already completed",
			"sagaID", saga.SagaID(),
			"status", saga.Status(),
			"version", saga.Version(),
			"eventType", event.Type,
			"eventID", event.ID)
		return nil
	}

	// Check if this event was already processed (idempotency check for retries)
	if m.eventAlreadyProcessed(saga, event) {
		m.logger.Info("Event already processed by saga, skipping",
			"sagaID", saga.SagaID(),
			"eventType", event.Type,
			"eventID", event.ID,
			"eventPosition", event.GlobalPosition)
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

	// Persist saga state (includes ProcessedEvents)
	// IMPORTANT: Do NOT call saga.IncrementVersion() here - the saga store
	// implementation is responsible for atomically incrementing and validating
	// the version using optimistic concurrency control.
	//
	// We pass the current event to saveSaga so it can be included in the
	// ProcessedEvents that are persisted. The in-memory cache is only updated
	// AFTER the save succeeds to avoid false-positive idempotency checks on retry.
	if err := m.saveSaga(ctx, saga, &event); err != nil {
		return err
	}
	return nil
}

// eventAlreadyProcessed checks if the saga has already processed this event.
// This provides idempotency during retry scenarios.
func (m *SagaManager) eventAlreadyProcessed(saga Saga, event StoredEvent) bool {
	sagaID := saga.SagaID()
	eventKey := fmt.Sprintf("%s:%d", event.ID, event.GlobalPosition)

	eventsRaw, ok := m.processedEvents.Load(sagaID)
	if !ok {
		return false
	}

	events, ok := eventsRaw.([]string)
	if !ok {
		return false
	}

	for _, pe := range events {
		if pe == eventKey {
			return true
		}
	}

	return false
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
		return m.saveSaga(ctx, saga, nil)
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

	return m.saveSaga(ctx, saga, nil)
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
// The currentEvent parameter is optional - if non-nil, the event will be added to
// ProcessedEvents when saving, and the in-memory cache will be updated only after
// a successful save to avoid false-positive idempotency checks on retry.
// Pass nil when saving saga state without processing a new event (e.g., compensation).
func (m *SagaManager) saveSaga(ctx context.Context, saga Saga, currentEvent *StoredEvent) error {
	sagaID := saga.SagaID()

	// Get existing processed events for this saga
	var processedEvents []string
	if eventsRaw, ok := m.processedEvents.Load(sagaID); ok {
		if events, ok := eventsRaw.([]string); ok {
			processedEvents = append(processedEvents, events...)
		}
	}

	// Add the current event to the list if provided (for persistence)
	if currentEvent != nil {
		eventKey := fmt.Sprintf("%s:%d", currentEvent.ID, currentEvent.GlobalPosition)
		processedEvents = append(processedEvents, eventKey)
	}

	// Keep only the most recent events to avoid unbounded growth
	if len(processedEvents) > maxProcessedEventsToTrack {
		processedEvents = processedEvents[len(processedEvents)-maxProcessedEventsToTrack:]
	}

	state := &SagaState{
		ID:              saga.SagaID(),
		Type:            saga.SagaType(),
		CorrelationID:   saga.CorrelationID(),
		Status:          saga.Status(),
		CurrentStep:     saga.CurrentStep(),
		Data:            saga.Data(),
		ProcessedEvents: processedEvents,
		StartedAt:       saga.StartedAt(),
		UpdatedAt:       time.Now(),
		CompletedAt:     saga.CompletedAt(),
		Version:         saga.Version(),
	}

	err := m.store.Save(ctx, state)
	if err != nil {
		return err
	}

	// Update saga's version with the new version from the database
	// This is important for any subsequent operations on the same saga instance
	saga.SetVersion(state.Version)

	// Only update the in-memory cache AFTER successful save
	// This ensures retry attempts won't incorrectly skip the event
	m.processedEvents.Store(sagaID, processedEvents)

	return nil
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

	// Restore processed events for idempotency tracking
	if len(state.ProcessedEvents) > 0 {
		m.processedEvents.Store(saga.SagaID(), state.ProcessedEvents)
	}

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

// AsyncResult represents the result of an asynchronous operation.
// It provides methods to wait for completion and check the result.
type AsyncResult struct {
	done      chan struct{}
	err       error
	errMu     sync.RWMutex
	ctx       context.Context
	cancel    context.CancelFunc
	closeOnce sync.Once
}

// newAsyncResult creates a new AsyncResult.
func newAsyncResult(ctx context.Context) *AsyncResult {
	asyncCtx, cancel := context.WithCancel(ctx)
	return &AsyncResult{
		done:   make(chan struct{}),
		ctx:    asyncCtx,
		cancel: cancel,
	}
}

// complete marks the async operation as complete with an optional error.
func (r *AsyncResult) complete(err error) {
	r.closeOnce.Do(func() {
		r.errMu.Lock()
		r.err = err
		r.errMu.Unlock()
		close(r.done)
		r.cancel()
	})
}

// Done returns a channel that is closed when the operation completes.
// Use this in select statements for non-blocking wait patterns.
//
// Example:
//
//	select {
//	case <-result.Done():
//	    if err := result.Err(); err != nil {
//	        log.Printf("Failed: %v", err)
//	    }
//	case <-time.After(5 * time.Second):
//	    result.Cancel()
//	    log.Println("Timed out")
//	}
func (r *AsyncResult) Done() <-chan struct{} {
	return r.done
}

// Wait blocks until the operation completes and returns any error.
// This is a convenience method equivalent to <-result.Done(); return result.Err()
func (r *AsyncResult) Wait() error {
	<-r.done
	return r.Err()
}

// WaitWithTimeout blocks until the operation completes or the timeout expires.
// Returns context.DeadlineExceeded if the timeout is reached before completion.
func (r *AsyncResult) WaitWithTimeout(timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	select {
	case <-r.done:
		return r.Err()
	case <-ctx.Done():
		return context.DeadlineExceeded
	}
}

// Err returns the error from the completed operation, or nil if not yet complete or successful.
func (r *AsyncResult) Err() error {
	r.errMu.RLock()
	defer r.errMu.RUnlock()
	return r.err
}

// IsComplete returns true if the operation has completed (successfully or with an error).
func (r *AsyncResult) IsComplete() bool {
	select {
	case <-r.done:
		return true
	default:
		return false
	}
}

// Cancel cancels the async operation's context.
// This can be used to stop a long-running operation early.
func (r *AsyncResult) Cancel() {
	r.cancel()
}

// Context returns the context for this async operation.
func (r *AsyncResult) Context() context.Context {
	return r.ctx
}

// StartAsync begins processing events and routing them to sagas in a background goroutine.
// It returns immediately with an AsyncResult that can be used to:
//   - Wait for the saga manager to stop: result.Wait()
//   - Wait with timeout: result.WaitWithTimeout(5 * time.Second)
//   - Check if stopped: result.IsComplete()
//   - Cancel the manager: result.Cancel()
//   - Get the error: result.Err()
//
// The saga manager will continue processing until:
//   - The provided context is cancelled
//   - result.Cancel() is called
//   - An unrecoverable error occurs
//
// Example:
//
//	result := manager.StartAsync(ctx)
//
//	// Do other work while saga manager runs in background...
//
//	// Later, when shutting down:
//	result.Cancel()
//	if err := result.WaitWithTimeout(10 * time.Second); err != nil {
//	    log.Printf("Saga manager shutdown: %v", err)
//	}
func (m *SagaManager) StartAsync(ctx context.Context) *AsyncResult {
	result := newAsyncResult(ctx)

	go func() {
		err := m.Start(result.ctx)
		result.complete(err)
	}()

	return result
}

// StartSagaAsync manually triggers a new saga instance asynchronously.
// The saga will be started and the first event will be processed in a background goroutine.
// Returns an AsyncResult that can be used to wait for the initial processing to complete.
//
// This is useful when you want to start a saga based on an external trigger
// rather than waiting for an event to flow through the event store subscription.
//
// Example:
//
//	result := manager.StartSagaAsync(ctx, "OrderFulfillment", initialEvent)
//	if err := result.WaitWithTimeout(5 * time.Second); err != nil {
//	    log.Printf("Failed to start saga: %v", err)
//	}
func (m *SagaManager) StartSagaAsync(ctx context.Context, sagaType string, triggerEvent StoredEvent) *AsyncResult {
	result := newAsyncResult(ctx)

	go func() {
		err := m.StartSaga(ctx, sagaType, triggerEvent)
		result.complete(err)
	}()

	return result
}

// StartSaga manually triggers a new saga instance synchronously.
// The saga will be created and the trigger event will be processed immediately.
//
// This is useful when you want to start a saga based on an external trigger
// rather than waiting for an event to flow through the event store subscription.
func (m *SagaManager) StartSaga(ctx context.Context, sagaType string, triggerEvent StoredEvent) error {
	m.mu.RLock()
	factory := m.registry[sagaType]
	correlations := m.correlations[sagaType]
	m.mu.RUnlock()

	if factory == nil {
		return fmt.Errorf("mink: saga type %q not registered", sagaType)
	}

	if len(correlations) == 0 {
		return fmt.Errorf("mink: no correlations defined for saga type %q", sagaType)
	}

	// Check if this is a valid starting event for this saga type
	var validCorrelation *SagaCorrelation
	for i := range correlations {
		if isStartingEvent(correlations[i].StartingEvents, triggerEvent.Type) {
			validCorrelation = &correlations[i]
			break
		}
	}

	if validCorrelation == nil {
		return fmt.Errorf("mink: event type %q is not a starting event for saga %q", triggerEvent.Type, sagaType)
	}

	// Process the event through the saga
	return m.processSagaEvent(ctx, sagaType, triggerEvent)
}
