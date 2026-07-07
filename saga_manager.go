package mink

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"go-mink.dev/adapters"
)

// maxProcessedEventsToTrack is the maximum number of processed events to retain
// for idempotency tracking. This prevents unbounded growth of saga data while
// still providing sufficient history for retry scenarios.
const maxProcessedEventsToTrack = 100

// reservedLastEventKey is the reserved key under which the manager persists the raw
// last trigger event, so an operator can re-drive a settled saga (see
// SagaManager.RetrySaga) without re-reading the event store. It rides the existing
// persisted SagaState.Data JSON — no schema change — but is written and read only by
// the manager: it is stamped into the persisted state in saveSaga and stripped in
// hydrateSaga, so it NEVER appears in the saga's own Data()/SetData() and works
// regardless of how a saga projects its Data. It is populated only when
// WithSagaRetryCapture is enabled, so it adds zero overhead when re-drive is unused.
//
// The "__mink_" prefix is reserved for the library; saga authors MUST NOT read or
// write Data keys with this prefix.
const reservedLastEventKey = "__mink_last_event"

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

// WithSagaPollInterval sets the cadence of the background timeout sweep that
// detects abandoned sagas (see WithSagaTimeout). It does not affect the primary
// event subscription, which is push-based.
func WithSagaPollInterval(d time.Duration) SagaManagerOption {
	return func(m *SagaManager) {
		m.pollInterval = d
	}
}

// WithSagaTimeout enables timeout handling for long-running sagas. When set to a
// positive duration, the manager periodically sweeps for Running sagas whose last
// update is older than the timeout and drives them into compensation. The default
// (0) disables the sweep. The sweep cadence is controlled by WithSagaSweepInterval.
func WithSagaTimeout(d time.Duration) SagaManagerOption {
	return func(m *SagaManager) {
		m.sagaTimeout = d
	}
}

// WithSagaSweepInterval sets how often the abandoned-saga timeout sweep runs (see
// WithSagaTimeout). Timeout detection tolerates coarse cadence, so this should be
// well above the per-event poll interval to avoid constant store queries. When
// unset, the sweep falls back to the poll interval, floored at 1 second.
func WithSagaSweepInterval(d time.Duration) SagaManagerOption {
	return func(m *SagaManager) {
		m.sagaSweepInterval = d
	}
}

// WithSagaRetryAttempts sets the number of retry attempts for failed commands.
// The value is clamped to a minimum of 1: a saga event is always attempted at
// least once. Passing 0 (or a negative value) would otherwise make the retry
// loops skip their body entirely, silently dropping the event while advancing
// the manager's position past it.
func WithSagaRetryAttempts(attempts int) SagaManagerOption {
	return func(m *SagaManager) {
		if attempts < 1 {
			attempts = 1
		}
		m.retryAttempts = attempts
	}
}

// WithSagaRetryDelay sets the delay between retry attempts.
func WithSagaRetryDelay(d time.Duration) SagaManagerOption {
	return func(m *SagaManager) {
		m.retryDelay = d
	}
}

// WithSagaRetryCapture enables capturing each saga's last trigger event so a
// settled saga can later be re-driven by RetrySaga / ResumeStalled. It is opt-in
// and OFF by default: when unset the manager captures nothing and adds zero
// overhead (honoring the library's zero-overhead-when-unconfigured invariant), and
// RetrySaga on a saga with no captured event returns ErrSagaNotRetryable. Enable it
// on managers whose sagas you may need to recover operationally.
//
// The captured event is stored in the manager-owned reserved slot of the saga's
// persisted state (SagaState.Data under reservedLastEventKey). The manager stamps
// and reads it itself and strips it before the saga's own SetData ever sees it, so
// it never leaks into saga-author code and it works regardless of how a saga
// implements Data()/SetData() — including projection-style sagas that rebuild Data
// from typed fields (which would otherwise silently drop a key written into Data).
func WithSagaRetryCapture() SagaManagerOption {
	return func(m *SagaManager) {
		m.captureLastEvent = true
	}
}

// WithSagaRetryObserver registers a hook invoked once per operator-initiated
// re-drive attempt (RetrySaga / ResumeStalled / RetrySagasByType), with the saga
// id, saga type, the status it was re-driven from, the status it reached, the
// attempt time, and the attempt's error. It makes a re-drive auditable — route it
// to an audit log or metrics. It is additive and opt-in: with no observer
// configured a re-drive is still recorded as a SagaStep on the saga's history, so
// it is never a silent state change.
//
// The observer is called synchronously on the caller's goroutine while the
// per-saga lock is NOT held; keep it fast and non-blocking, and do not call back
// into the SagaManager from it.
func WithSagaRetryObserver(fn func(RetryEvent)) SagaManagerOption {
	return func(m *SagaManager) {
		m.retryObserver = fn
	}
}

// RetryEvent describes a single operator-initiated saga re-drive attempt reported
// to a WithSagaRetryObserver.
//
// Err reports an OPERATIONAL failure of the attempt — the re-drive could not be
// applied — e.g. ErrConcurrencyConflict, a store error, or a failure to decode the
// captured event. It is nil when the re-drive ran to a persisted outcome. A re-drive
// that RAN but whose saga failed again (its command re-failed, driving compensation)
// is NOT reported via Err — the whole saga machinery treats compensation as handled;
// inspect ResultStatus (or the saga's persisted status) to see the business outcome.
type RetryEvent struct {
	// SagaID is the id of the re-driven saga.
	SagaID string

	// SagaType is the saga's type.
	SagaType string

	// FromStatus is the status the saga was in when the re-drive was initiated.
	FromStatus SagaStatus

	// ResultStatus is the status the saga reached after the re-drive (meaningful
	// when Err is nil): SagaStatusCompleted on a clean recovery, or a settled
	// unsuccessful status (Failed / Compensated / CompensationFailed) if it failed
	// again. Zero-valued when the attempt did not run to a persisted outcome.
	ResultStatus SagaStatus

	// At is when the attempt was made.
	At time.Time

	// Err is the attempt's operational error, or nil (see the type doc).
	Err error
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
	pollInterval      time.Duration
	retryAttempts     int
	retryDelay        time.Duration
	sagaTimeout       time.Duration
	sagaSweepInterval time.Duration

	// retryObserver, when set via WithSagaRetryObserver, is invoked once per
	// operator-initiated re-drive attempt (RetrySaga / ResumeStalled). It is nil by
	// default, so re-drive adds no observability overhead unless configured.
	retryObserver func(RetryEvent)

	// captureLastEvent, enabled via WithSagaRetryCapture, makes the manager record
	// each saga's last trigger event so RetrySaga / ResumeStalled can re-deliver it.
	// It is false by default: when unset the manager captures nothing and adds zero
	// overhead, honoring the library's zero-overhead-when-unconfigured invariant.
	captureLastEvent bool

	// State
	mu       sync.RWMutex
	position uint64
	running  bool
	cancel   context.CancelFunc
	doneCh   chan struct{}

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
	// This is used for idempotency tracking. The authoritative copy of this
	// information is stored in the database as SagaState.ProcessedEvents when
	// a saga is persisted; this sync.Map is an in-memory cache of that
	// persisted state, keyed by saga ID with a value of a slice of event keys.
	//
	// During saga hydration (see hydrateSaga), the cache for a given saga ID is
	// repopulated from the corresponding SagaState.ProcessedEvents loaded from
	// the store. This means the cache can be safely cleared (for example,
	// by restarting the process or recreating the SagaManager); it will be
	// rebuilt from the database on demand and idempotency guarantees are
	// preserved.
	//
	// Note on memory growth: Like sagaLocks, entries in processedEvents are
	// stored in memory for the lifetime of the SagaManager instance and are
	// not automatically cleaned up. Each unique saga ID holds a slice of
	// processed event keys, which can consume more memory per saga than a
	// single mutex pointer.
	//
	// The maxProcessedEventsToTrack constant caps how many processed events are
	// retained per saga instance, but the number of distinct saga IDs in this
	// map can still grow over time in long-running processes.
	//
	// For applications with many unique saga IDs or long-lived workers,
	// consider an explicit cleanup/rotation strategy at the application level
	// (e.g., periodically recreating the SagaManager so the cache is reset, or
	// sharding work across multiple SagaManager instances).
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
	m.doneCh = make(chan struct{})
	doneCh := m.doneCh
	m.mu.Unlock()

	defer func() {
		m.mu.Lock()
		m.running = false
		m.mu.Unlock()
		close(doneCh) // signal Stop() that the loop has exited
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

	// Optionally run a periodic sweep for abandoned/timed-out sagas, on a coarse
	// cadence (decoupled from the per-event poll interval) so it does not hammer
	// the saga store.
	var sweepCh <-chan time.Time
	if m.sagaTimeout > 0 {
		sweepEvery := m.sagaSweepInterval
		if sweepEvery <= 0 {
			sweepEvery = m.pollInterval
			if sweepEvery < time.Second {
				sweepEvery = time.Second
			}
		}
		ticker := time.NewTicker(sweepEvery)
		defer ticker.Stop()
		sweepCh = ticker.C
	}

	for {
		select {
		case <-ctx.Done():
			m.logger.Info("Saga manager stopped")
			return ctx.Err()

		case <-sweepCh:
			m.sweepTimeouts(ctx)

		case event, ok := <-eventCh:
			if !ok {
				m.logger.Info("Event channel closed")
				return nil
			}

			if err := m.processEvent(ctx, m.adaptEvent(event)); err != nil {
				m.logger.Error("Failed to process event", "error", err, "eventType", event.Type)
				// Continue processing - don't stop on individual event failures
			}

			// If the manager is shutting down (ctx cancelled, possibly mid-dispatch — see
			// isShutdownError), do NOT advance the cursor past this event: leave it on the
			// event so a restart from the persisted position re-delivers it and the
			// shutdown-interrupted saga resumes. Re-delivery is idempotent (per-saga
			// processed-event tracking), so a fully-processed event is safely skipped on replay.
			if ctx.Err() != nil {
				return ctx.Err()
			}

			m.mu.Lock()
			m.position = event.GlobalPosition + 1
			m.mu.Unlock()
		}
	}
}

// sweepTimeouts finds Running sagas that have not been updated within the
// configured timeout and drives them into compensation. It is a no-op when no
// timeout is configured.
//
// Only Running sagas are swept: an already-Compensating saga is intentionally
// left alone, because restarting its compensation from scratch would re-dispatch
// already-executed (and typically non-idempotent) compensation commands.
func (m *SagaManager) sweepTimeouts(ctx context.Context) {
	if m.sagaTimeout <= 0 {
		return
	}

	m.mu.RLock()
	sagaTypes := make([]string, 0, len(m.registry))
	for t := range m.registry {
		sagaTypes = append(sagaTypes, t)
	}
	m.mu.RUnlock()

	cutoff := time.Now().Add(-m.sagaTimeout)
	for _, sagaType := range sagaTypes {
		states, err := m.store.FindByType(ctx, sagaType, SagaStatusRunning)
		if err != nil {
			m.logger.Error("Saga timeout sweep failed to list sagas", "sagaType", sagaType, "error", err)
			continue
		}
		for _, state := range states {
			if state.UpdatedAt.After(cutoff) {
				continue // still fresh
			}
			m.timeoutSaga(ctx, sagaType, state)
		}
	}
}

// timeoutSaga compensates a single timed-out saga under its per-saga lock.
func (m *SagaManager) timeoutSaga(ctx context.Context, sagaType string, state *SagaState) {
	m.mu.RLock()
	factory, ok := m.registry[sagaType]
	m.mu.RUnlock()
	if !ok {
		return
	}

	lock := m.getSagaLock(state.ID)
	lock.Lock()
	defer lock.Unlock()

	// Reload under the lock to avoid acting on stale state.
	fresh, err := m.store.Load(ctx, state.ID)
	if err != nil || fresh == nil || fresh.Status.IsTerminal() {
		return
	}

	saga := factory(fresh.ID)
	if err := m.hydrateSaga(saga, fresh); err != nil {
		m.logger.Error("Failed to hydrate timed-out saga", "sagaID", fresh.ID, "error", err)
		return
	}

	m.logger.Warn("Saga timed out, compensating",
		"sagaID", saga.SagaID(), "sagaType", sagaType, "age", time.Since(fresh.UpdatedAt))
	if err := m.handleSagaFailure(ctx, saga, ErrSagaTimedOut); err != nil {
		m.logger.Error("Failed to compensate timed-out saga", "sagaID", saga.SagaID(), "error", err)
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

// Stop gracefully stops the saga manager and waits for the processing loop to
// exit, so no event is being processed once Stop returns.
func (m *SagaManager) Stop() {
	m.mu.Lock()
	cancel := m.cancel
	doneCh := m.doneCh
	m.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if doneCh != nil {
		<-doneCh // wait for the Start loop to finish
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

// isShutdownError reports whether err is a context cancellation or deadline —
// i.e. the manager is stopping — as opposed to a genuine saga-step failure. Such
// an error must not drive compensation or advance the manager's position.
func isShutdownError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
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
		// This results in two store lookups for existing sagas:
		//   1. resolveSagaID - to determine which lock to acquire (saga ID needed)
		//   2. attemptProcessSagaEvent - to get fresh state after lock acquisition
		//
		// The first lookup cannot be eliminated because we need the saga ID to determine
		// the lock key. For new sagas (isStarting=true), only one lookup occurs since
		// the saga doesn't exist in the store yet.
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
		m.logger.Debug("Skipping terminal saga - already completed",
			"sagaID", saga.SagaID(),
			"status", saga.Status(),
			"version", saga.Version(),
			"eventType", event.Type,
			"eventID", event.ID)
		return nil
	}

	// Check if this event was already processed (idempotency check for retries)
	if m.eventAlreadyProcessed(saga, event) {
		m.logger.Debug("Event already processed by saga, skipping",
			"sagaID", saga.SagaID(),
			"eventType", event.Type,
			"eventID", event.ID,
			"eventPosition", event.GlobalPosition)
		return nil
	}

	// Drive the saga forward with this event. RetrySaga re-drives a settled saga
	// through the very same path, so the two share identical semantics.
	return m.redrive(ctx, saga, event)
}

// redrive delivers a single event to a saga and drives the resulting work:
// HandleEvent -> dispatch its commands -> mark complete / compensate -> persist.
// It is the shared per-saga drive used both by the live event loop
// (attemptProcessSagaEvent) and by operator re-drive (RetrySaga / ResumeStalled),
// so a re-drive behaves exactly like a fresh delivery of the event — on success the
// saga reaches Completed; on a fresh failure it follows the same compensation path
// as a first-time failure.
//
// The caller is responsible for the terminal-state and idempotency checks and, for
// re-drive, for holding the per-saga lock and resetting the retried event's
// idempotency key beforehand.
func (m *SagaManager) redrive(ctx context.Context, saga Saga, event StoredEvent) error {
	// Capture the trigger event (only when WithSagaRetryCapture is enabled) so a
	// settled saga can later be re-driven without the event store. Recorded on the
	// manager-owned carrier slot before any dispatch, so it is captured even if the
	// saga fails and follows the compensation save path — and persisted by saveSaga
	// independently of the saga's own Data()/SetData().
	m.captureTriggerEvent(saga, event)

	// Handle the event
	saga.SetStatus(SagaStatusRunning)
	commands, err := saga.HandleEvent(ctx, event)
	if err != nil {
		if isShutdownError(err) {
			return err
		}
		return m.handleSagaFailure(ctx, saga, err)
	}

	// Execute resulting commands. Advance the current step after each successful
	// command so CurrentStep reflects completed progress; on failure, the failed
	// step number passed to Compensate is the count of steps that succeeded.
	for _, cmd := range commands {
		if err := m.dispatchCommand(ctx, saga, cmd); err != nil {
			// A context cancellation/deadline (e.g. a graceful Stop() mid-dispatch) is
			// not a business failure: leave the saga Running so a restart resumes it,
			// instead of driving compensation on an already-cancelled context (which
			// would fail every compensation command and persist CompensationFailed).
			if isShutdownError(err) {
				return err
			}
			return m.handleSagaFailure(ctx, saga, err)
		}
		saga.SetCurrentStep(saga.CurrentStep() + 1)
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
	return m.saveSaga(ctx, saga, &event)
}

// lastEventCarrier is the manager-owned capability (satisfied by any saga embedding
// SagaBase, via its unexported promoted methods) for stashing the last trigger event
// used by RetrySaga. It is deliberately NOT the saga's Data map: keeping the captured
// event here means the manager can persist and recover it itself (see saveSaga /
// hydrateSaga) without depending on how the saga round-trips Data()/SetData(), and
// without ever exposing the reserved key to saga-author code.
type lastEventCarrier interface {
	lastTriggerEvent() (StoredEvent, bool)
	setLastTriggerEvent(StoredEvent)
}

// captureTriggerEvent records the trigger event on the saga's manager-owned carrier
// slot, so a settled saga can be re-driven later without re-reading the event store.
// It is a no-op unless WithSagaRetryCapture is enabled (zero overhead when unused),
// for a zero-value event (nothing meaningful to capture), or for a saga that does
// not embed SagaBase (no carrier).
func (m *SagaManager) captureTriggerEvent(saga Saga, event StoredEvent) {
	if !m.captureLastEvent {
		return
	}
	if event.ID == "" && event.Type == "" {
		return
	}
	if c, ok := saga.(lastEventCarrier); ok {
		c.setLastTriggerEvent(event)
	}
}

// cloneSagaData shallow-copies a saga Data map into a new map (never nil), with room
// for one extra key. Used to stamp the reserved capture key into the persisted state
// without mutating the saga's own Data map.
func cloneSagaData(data map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(data)+1)
	for k, v := range data {
		out[k] = v
	}
	return out
}

// cloneSagaDataWithout shallow-copies data omitting key. Used on hydrate to hand the
// saga its Data without the reserved capture key. Returns nil when the result is
// empty and the input was the only carrier, matching Data()'s nil-for-empty idiom.
func cloneSagaDataWithout(data map[string]interface{}, key string) map[string]interface{} {
	if len(data) <= 1 {
		return nil
	}
	out := make(map[string]interface{}, len(data)-1)
	for k, v := range data {
		if k != key {
			out[k] = v
		}
	}
	return out
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
		} else {
			// Unsuccessful result with no error attached.
			lastErr = ErrCommandUnsuccessful
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

// stepRecordingSaga is the optional capability — satisfied by any saga embedding
// SagaBase — that lets the manager persist and restore the saga's SagaStep history.
// It is intentionally NOT part of the Saga interface, so it stays backward
// compatible: a saga type that implements neither method simply has no persisted
// step history (and a re-drive of it is not recorded as a step, though it is still
// reported to any WithSagaRetryObserver).
type stepRecordingSaga interface {
	Steps() []SagaStep
	SetSteps([]SagaStep)
	RecordStep(SagaStep)
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
			// Pre-allocate capacity for existing events plus potential currentEvent
			processedEvents = make([]string, 0, len(events)+1)
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

	// Build the persisted Data. When retry-capture is on and the saga carries a last
	// trigger event, stamp it under the reserved key into a COPY of the saga's Data —
	// never mutating the saga's own map, and independent of how the saga implements
	// Data()/SetData() — so re-drive works even for projection-style sagas and the
	// reserved key never leaks back into saga-author code (hydrateSaga strips it).
	persistData := saga.Data()
	if m.captureLastEvent {
		if c, ok := saga.(lastEventCarrier); ok {
			if ev, ok := c.lastTriggerEvent(); ok {
				persistData = cloneSagaData(saga.Data())
				persistData[reservedLastEventKey] = ev
			}
		}
	}

	state := &SagaState{
		ID:              saga.SagaID(),
		Type:            saga.SagaType(),
		CorrelationID:   saga.CorrelationID(),
		Status:          saga.Status(),
		CurrentStep:     saga.CurrentStep(),
		Data:            persistData,
		ProcessedEvents: processedEvents,
		StartedAt:       saga.StartedAt(),
		UpdatedAt:       time.Now(),
		CompletedAt:     saga.CompletedAt(),
		Version:         saga.Version(),
	}

	// Persist the saga's step history when the saga supports it (SagaBase does).
	// Used to record operator re-drives; empty for sagas that never record a step.
	if sr, ok := saga.(stepRecordingSaga); ok {
		state.Steps = sr.Steps()
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

	// Once a saga reaches a terminal state it will never be processed again, so
	// evict its per-saga lock and idempotency cache to bound memory growth.
	if saga.Status().IsTerminal() {
		m.processedEvents.Delete(sagaID)
		m.sagaLocks.Delete(sagaID)
	}

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

	// Recover the manager-owned captured trigger event (if present) onto the saga's
	// carrier slot, and hand the saga a Data map WITHOUT the reserved key — so the
	// library-internal capture never appears in the saga's own Data()/SetData().
	data := state.Data
	if raw, ok := data[reservedLastEventKey]; ok {
		if c, ok := saga.(lastEventCarrier); ok {
			if ev, ok := decodeLastEvent(raw); ok {
				c.setLastTriggerEvent(ev)
			}
		}
		data = cloneSagaDataWithout(data, reservedLastEventKey)
	}
	saga.SetData(data)

	// Restore the step history when the saga supports it (SagaBase does).
	if sr, ok := saga.(stepRecordingSaga); ok {
		sr.SetSteps(state.Steps)
	}

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

// FindSagasByType returns saga states of the given type, optionally filtered by
// status. This is useful for recovery tooling and dashboards (e.g. listing
// Running or CompensationFailed sagas that need operator attention).
func (m *SagaManager) FindSagasByType(ctx context.Context, sagaType string, statuses ...SagaStatus) ([]*SagaState, error) {
	return m.store.FindByType(ctx, sagaType, statuses...)
}

// RetrySaga re-drives a settled-but-unsuccessful saga once its underlying cause is
// fixed — the operator-initiated recovery counterpart to the automatic timeout
// sweep. It re-delivers the saga's last trigger event through the normal processing
// path, so a retry has identical semantics to a fresh delivery: on success the saga
// reaches Completed; on a fresh failure it follows the same compensation path as a
// first-time failure.
//
// # Which statuses are retryable
//
// RetrySaga accepts exactly the settled-but-unsuccessful statuses
// (SagaState.IsRetryable):
//
//	Failed              re-drive the forward path (no compensation ran)
//	Compensated         rolled back to a clean state — safe to re-drive forward
//	CompensationFailed  stuck after a partial rollback — re-drive is the recovery
//
// It rejects, with a typed *SagaNotRetryableError (matching
// errors.Is(err, ErrSagaNotRetryable)):
//
//	Completed                       terminal success — never re-run
//	Started / Running / Compensating in-flight — the event loop / timeout sweep owns it
//
// A missing saga returns the existing ErrSagaNotFound. To re-drive a stalled
// Running saga whose worker died mid-dispatch, use ResumeStalled instead.
//
// # Idempotency
//
// Re-drive re-delivers a single event: only that event's idempotency key is
// cleared before re-delivery, so already-succeeded earlier steps (recorded in
// ProcessedEvents) are NOT re-dispatched. Beyond that, re-drive relies on the same
// contract as the store's at-least-once delivery — saga command handlers MUST be
// idempotent. A retry that fails again lands in the normal failure/compensation
// path and may be retried again once the cause is truly fixed.
//
// # Concurrency
//
// The load -> re-drive -> save runs under the same per-saga lock the event loop
// uses (so it cannot race an in-flight event for that saga), and the save uses
// optimistic concurrency on the loaded version. A concurrent modification surfaces
// as ErrConcurrencyConflict and is NOT silently swallowed or retried internally —
// an operator action fails loudly and can be re-issued.
//
// # Mechanism
//
// The last trigger event is recovered from the manager-owned reserved slot of the
// saga's stored state, captured during normal processing when WithSagaRetryCapture
// is enabled (see reservedLastEventKey) — no event-store re-read and no schema
// change. Capture is opt-in and zero-overhead when off; a retryable-status saga with
// no captured event (capture disabled, or the saga last ran before it was enabled)
// is rejected with a clear reason rather than guessing.
//
// # Observed outcome
//
// The returned error reports an OPERATIONAL failure (ErrConcurrencyConflict, a store
// error, or an undecodable captured event). A re-drive that RAN but whose saga
// failed again (driving compensation) returns nil — inspect the saga's status, or a
// WithSagaRetryObserver's RetryEvent.ResultStatus, for the business outcome
// (RetrySagasByType classifies this for you). The re-drive is auditable: it is
// recorded as a SagaStep on the saga's history and reported to any observer.
func (m *SagaManager) RetrySaga(ctx context.Context, sagaID string) error {
	_, err := m.retrySaga(ctx, sagaID)
	return err
}

// retrySaga is RetrySaga returning the saga's resulting status (meaningful when the
// error is nil) so the batch path can classify the outcome without an extra load.
func (m *SagaManager) retrySaga(ctx context.Context, sagaID string) (SagaStatus, error) {
	// Cheap pre-check outside the per-saga lock: reject an obviously non-retryable
	// or missing saga without taking the lock. The authoritative check is repeated
	// under the lock (against a fresh reload) inside reDriveUnderLock.
	pre, err := m.store.Load(ctx, sagaID)
	if err != nil {
		return 0, err // ErrSagaNotFound (typed) surfaces unchanged
	}
	if pre == nil {
		return 0, &SagaNotFoundError{SagaID: sagaID}
	}
	if !pre.Status.IsRetryable() {
		return 0, &SagaNotRetryableError{
			SagaID: sagaID, Status: pre.Status,
			Reason: "status is not retryable (expected Failed, Compensated, or CompensationFailed)",
		}
	}

	return m.reDriveUnderLock(ctx, sagaID, func(state *SagaState) error {
		if !state.Status.IsRetryable() {
			return &SagaNotRetryableError{
				SagaID: sagaID, Status: state.Status,
				Reason: "status is not retryable (expected Failed, Compensated, or CompensationFailed)",
			}
		}
		return nil
	})
}

// ResumeStalled re-drives a Running saga whose worker died mid-dispatch, now,
// instead of waiting for the WithSagaTimeout sweep to compensate it. It accepts
// only a Running saga (any other status is rejected with *SagaNotRetryableError /
// ErrSagaNotRetryable) and, to avoid fighting a live worker, requires the saga's
// UpdatedAt to be older than the configured saga timeout (WithSagaTimeout); a saga
// updated more recently, or a manager with no timeout configured, is refused.
//
// It reuses RetrySaga's machinery exactly: the same per-saga lock, optimistic
// concurrency, single-event idempotency reset, SagaStep record, and
// WithSagaRetryObserver reporting.
func (m *SagaManager) ResumeStalled(ctx context.Context, sagaID string) error {
	pre, err := m.store.Load(ctx, sagaID)
	if err != nil {
		return err
	}
	if pre == nil {
		return &SagaNotFoundError{SagaID: sagaID}
	}
	if pre.Status != SagaStatusRunning {
		return &SagaNotRetryableError{
			SagaID: sagaID, Status: pre.Status,
			Reason: "only a Running saga can be resumed",
		}
	}

	_, err = m.reDriveUnderLock(ctx, sagaID, func(state *SagaState) error {
		if state.Status != SagaStatusRunning {
			return &SagaNotRetryableError{
				SagaID: sagaID, Status: state.Status,
				Reason: "only a Running saga can be resumed",
			}
		}
		if m.sagaTimeout <= 0 {
			return &SagaNotRetryableError{
				SagaID: sagaID, Status: state.Status,
				Reason: "ResumeStalled requires a configured saga timeout (WithSagaTimeout) to judge staleness",
			}
		}
		if time.Since(state.UpdatedAt) < m.sagaTimeout {
			return &SagaNotRetryableError{
				SagaID: sagaID, Status: state.Status,
				Reason: "saga was updated more recently than the timeout; it is not stalled",
			}
		}
		return nil
	})
	return err
}

// RetryOutcome classifies the result of re-driving one saga within a batch
// (RetrySagasByType).
type RetryOutcome int

const (
	// RetrySucceeded means the re-drive was applied and the saga reached Completed.
	RetrySucceeded RetryOutcome = iota

	// RetryFailedAgain means the re-drive ran but the saga did not complete — it hit
	// a fresh failure and followed the compensation path again, or the attempt
	// returned a non-conflict operational error (see SagaRetryResult.Err).
	RetryFailedAgain

	// RetryConflicted means an optimistic-concurrency conflict prevented the
	// re-drive; the saga is unchanged and the operation can be re-issued.
	RetryConflicted

	// RetrySkipped means the saga was not re-driven because it was not retryable
	// (e.g. its status changed under the lock, or it has no captured trigger event).
	RetrySkipped
)

// String returns a lower-case label for the outcome.
func (o RetryOutcome) String() string {
	switch o {
	case RetrySucceeded:
		return "succeeded"
	case RetryFailedAgain:
		return "failed_again"
	case RetryConflicted:
		return "conflicted"
	case RetrySkipped:
		return "skipped"
	default:
		return "unknown"
	}
}

// SagaRetryResult is the outcome of re-driving a single saga in a batch.
type SagaRetryResult struct {
	// SagaID is the saga this result is for.
	SagaID string

	// Outcome classifies what happened.
	Outcome RetryOutcome

	// Err is the attempt's error, when any (nil for a clean RetrySucceeded).
	Err error
}

// RetryReport is the aggregate result of RetrySagasByType, one entry per saga.
type RetryReport struct {
	// Results holds the per-saga outcomes, in the order the sagas were processed.
	Results []SagaRetryResult
}

// Count returns how many sagas ended with the given outcome.
func (r RetryReport) Count(o RetryOutcome) int {
	n := 0
	for _, res := range r.Results {
		if res.Outcome == o {
			n++
		}
	}
	return n
}

// RetrySagasByType finds sagas of the given type in the given statuses (via
// FindByType) and applies RetrySaga to each, sequentially, returning a per-saga
// RetryReport. It is a pure composition of the single-saga primitive: each saga is
// re-driven under its own per-saga lock, and a failure of one does NOT abort the
// batch — every matched saga is attempted and its individual outcome recorded.
//
// Pass the retryable statuses to target (e.g. SagaStatusFailed,
// SagaStatusCompensationFailed); a matched saga that is not retryable when its turn
// comes is reported as RetrySkipped rather than failing the batch. The returned
// error is non-nil only if the initial FindByType lookup fails.
func (m *SagaManager) RetrySagasByType(ctx context.Context, sagaType string, statuses ...SagaStatus) (RetryReport, error) {
	sagas, err := m.store.FindByType(ctx, sagaType, statuses...)
	if err != nil {
		return RetryReport{}, err
	}

	report := RetryReport{Results: make([]SagaRetryResult, 0, len(sagas))}
	for _, s := range sagas {
		res := SagaRetryResult{SagaID: s.ID}
		// retrySaga returns the resulting status, so a re-failure is classified without
		// an extra store load (the drive already reloaded and re-persisted the saga).
		status, retryErr := m.retrySaga(ctx, s.ID)
		switch {
		case retryErr == nil:
			if status == SagaStatusCompleted {
				res.Outcome = RetrySucceeded
			} else {
				res.Outcome = RetryFailedAgain
			}
		case errors.Is(retryErr, ErrConcurrencyConflict):
			res.Outcome, res.Err = RetryConflicted, retryErr
		case errors.Is(retryErr, ErrSagaNotRetryable):
			res.Outcome, res.Err = RetrySkipped, retryErr
		default:
			res.Outcome, res.Err = RetryFailedAgain, retryErr
		}
		report.Results = append(report.Results, res)
	}
	return report, nil
}

// reDriveUnderLock is the shared re-drive machinery behind RetrySaga and
// ResumeStalled. It runs the load -> re-drive -> save critical section under the
// saga's per-saga lock (see driveLocked), then — outside the lock — reports the
// attempt to any WithSagaRetryObserver and returns the saga's resulting status
// (meaningful when the returned error is nil).
func (m *SagaManager) reDriveUnderLock(ctx context.Context, sagaID string, accept func(*SagaState) error) (SagaStatus, error) {
	saga, fromStatus, driveErr, setupErr := m.driveLocked(ctx, sagaID, accept)
	if setupErr != nil {
		// The re-drive never ran (rejected on status/missing event/etc.); no attempt
		// to report and no resulting status.
		return 0, setupErr
	}

	resultStatus := saga.Status()

	// Notify outside the lock (released by driveLocked's defer) so a slow observer
	// cannot block other work for this saga.
	m.notifyRetry(RetryEvent{
		SagaID:       sagaID,
		SagaType:     saga.SagaType(),
		FromStatus:   fromStatus,
		ResultStatus: resultStatus,
		At:           time.Now(),
		Err:          driveErr,
	})

	return resultStatus, driveErr
}

// driveLocked performs the re-drive critical section under the per-saga lock: fresh
// reload (never a stale pre-read), accept re-validation, captured-event recovery +
// single-event idempotency reset, hydrate, SagaStep record, and redrive. It returns
// the hydrated saga, the status it was re-driven from, the re-drive's own error
// (driveErr — nil, ErrConcurrencyConflict, or a shutdown error), and a setupErr for
// a rejection that never ran (bad status, missing/undecodable event, unknown type).
// On a non-nil setupErr the saga is nil.
func (m *SagaManager) driveLocked(ctx context.Context, sagaID string, accept func(*SagaState) error) (saga Saga, fromStatus SagaStatus, driveErr, setupErr error) {
	lock := m.getSagaLock(sagaID)
	lock.Lock()
	defer lock.Unlock()

	// Fresh (re)load under the lock — never act on a stale pre-read.
	state, err := m.store.Load(ctx, sagaID)
	if err != nil {
		return nil, 0, nil, err
	}
	if state == nil {
		return nil, 0, nil, &SagaNotFoundError{SagaID: sagaID}
	}

	// Re-validate under the lock: status may have changed since the pre-check.
	if err := accept(state); err != nil {
		return nil, 0, nil, err
	}

	// Recover the captured trigger event to re-deliver.
	raw, ok := state.Data[reservedLastEventKey]
	if !ok {
		return nil, 0, nil, &SagaNotRetryableError{
			SagaID: sagaID, Status: state.Status,
			Reason: "no captured trigger event to re-drive (enable WithSagaRetryCapture, or the saga last ran before it was enabled)",
		}
	}
	event, ok := decodeLastEvent(raw)
	if !ok {
		return nil, 0, nil, &SagaNotRetryableError{
			SagaID: sagaID, Status: state.Status,
			Reason: "captured trigger event could not be decoded",
		}
	}

	fromStatus = state.Status

	// Reset idempotency for ONLY the retried event; keep every earlier key so
	// already-succeeded steps are not re-dispatched on re-delivery.
	eventKey := fmt.Sprintf("%s:%d", event.ID, event.GlobalPosition)
	state.ProcessedEvents = removeProcessedEventKey(state.ProcessedEvents, eventKey)

	// Reset the settled status so the normal drive logic applies on re-delivery.
	state.Status = SagaStatusRunning
	state.FailureReason = ""
	state.CompletedAt = nil

	// Rebuild and hydrate the saga instance from the adjusted state.
	m.mu.RLock()
	factory := m.registry[state.Type]
	m.mu.RUnlock()
	if factory == nil {
		return nil, 0, nil, fmt.Errorf("mink: saga factory not found for type %q", state.Type)
	}
	saga = factory(state.ID)
	if err := m.hydrateSaga(saga, state); err != nil {
		return nil, 0, nil, fmt.Errorf("mink: failed to hydrate saga for re-drive: %w", err)
	}

	// hydrateSaga repopulates the idempotency cache only when ProcessedEvents is
	// non-empty; when the reset left it empty, set it explicitly here to clear any
	// stale entry (notably the just-removed retried key) that would otherwise let
	// saveSaga re-append or eventAlreadyProcessed short-circuit incorrectly.
	if len(state.ProcessedEvents) == 0 {
		m.processedEvents.Store(sagaID, state.ProcessedEvents)
	}

	// Record the re-drive on the saga's history so it is auditable even with no
	// observer configured; the saga's resulting status after the drive is the outcome.
	m.recordRetryStep(saga, fromStatus)

	// Re-deliver through the normal drive path — identical semantics to a fresh
	// delivery, persisted under optimistic concurrency (ErrConcurrencyConflict on a
	// concurrent change; not retried internally).
	driveErr = m.redrive(ctx, saga, event)
	return saga, fromStatus, driveErr, nil
}

// recordRetryStep appends a SagaStep marking an operator re-drive to the saga's
// history (persisted by the subsequent save), so a re-drive is auditable even with
// no WithSagaRetryObserver configured. The step records the re-drive action itself —
// which completes here — noting the status the saga was re-driven from; the saga's
// resulting status after the drive (and any observer's RetryEvent.ResultStatus) is
// the business outcome. It is a no-op for a saga that does not embed SagaBase.
func (m *SagaManager) recordRetryStep(saga Saga, fromStatus SagaStatus) {
	sr, ok := saga.(stepRecordingSaga)
	if !ok {
		return
	}
	now := time.Now()
	sr.RecordStep(SagaStep{
		Name:        fmt.Sprintf("operator re-drive (from %s)", fromStatus),
		Index:       saga.CurrentStep(),
		Status:      SagaStepCompleted,
		CompletedAt: &now,
	})
}

// notifyRetry reports a re-drive attempt to the configured observer, if any.
func (m *SagaManager) notifyRetry(ev RetryEvent) {
	if m.retryObserver != nil {
		m.retryObserver(ev)
	}
}

// decodeLastEvent recovers a StoredEvent captured under reservedLastEventKey. The
// in-memory saga store round-trips the value as a StoredEvent; a JSON-backed store
// (e.g. PostgreSQL) round-trips it as a map[string]interface{}, so fall back to a
// JSON re-decode. It returns false when the value is missing or unusable.
func decodeLastEvent(raw interface{}) (StoredEvent, bool) {
	switch v := raw.(type) {
	case StoredEvent:
		return v, true
	case *StoredEvent:
		if v == nil {
			return StoredEvent{}, false
		}
		return *v, true
	default:
		b, err := json.Marshal(raw)
		if err != nil {
			return StoredEvent{}, false
		}
		var ev StoredEvent
		if err := json.Unmarshal(b, &ev); err != nil {
			return StoredEvent{}, false
		}
		// A usable event must at least identify itself (idempotency keys on its ID).
		if ev.ID == "" && ev.Type == "" {
			return StoredEvent{}, false
		}
		return ev, true
	}
}

// removeProcessedEventKey returns events with every occurrence of key removed,
// preserving order. Used by re-drive to clear only the retried event's idempotency
// key while leaving already-succeeded earlier keys intact. The input slice is not
// mutated.
func removeProcessedEventKey(events []string, key string) []string {
	if len(events) == 0 {
		return events
	}
	out := make([]string, 0, len(events))
	for _, e := range events {
		if e != key {
			out = append(out, e)
		}
	}
	return out
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
