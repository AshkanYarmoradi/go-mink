package mink

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// ProjectionEngine manages the lifecycle of projections.
// It handles registration, starting, stopping, and monitoring projections.
type ProjectionEngine struct {
	store           *EventStore
	checkpointStore CheckpointStore
	metrics         ProjectionMetrics
	logger          Logger

	// Inline projections (synchronous)
	inlineProjections []InlineProjection
	inlineMu          sync.RWMutex

	// Async projections (background workers)
	asyncProjections map[string]*asyncProjectionWorker
	asyncMu          sync.RWMutex

	// Live projections (real-time)
	liveProjections map[string]*liveProjectionWorker
	liveMu          sync.RWMutex

	// Engine state
	running  atomic.Bool
	stopping atomic.Bool
	wg       sync.WaitGroup
	stopCh   chan struct{}
}

// ProjectionEngineOption configures a ProjectionEngine.
type ProjectionEngineOption func(*ProjectionEngine)

// WithCheckpointStore sets the checkpoint store for the engine.
func WithCheckpointStore(store CheckpointStore) ProjectionEngineOption {
	return func(e *ProjectionEngine) {
		e.checkpointStore = store
	}
}

// WithProjectionMetrics sets the metrics collector for the engine.
func WithProjectionMetrics(metrics ProjectionMetrics) ProjectionEngineOption {
	return func(e *ProjectionEngine) {
		e.metrics = metrics
	}
}

// WithProjectionLogger sets the logger for the engine.
func WithProjectionLogger(logger Logger) ProjectionEngineOption {
	return func(e *ProjectionEngine) {
		e.logger = logger
	}
}

// NewProjectionEngine creates a new ProjectionEngine.
func NewProjectionEngine(store *EventStore, opts ...ProjectionEngineOption) *ProjectionEngine {
	e := &ProjectionEngine{
		store:            store,
		metrics:          &noopProjectionMetrics{},
		logger:           &noopLogger{},
		asyncProjections: make(map[string]*asyncProjectionWorker),
		liveProjections:  make(map[string]*liveProjectionWorker),
		stopCh:           make(chan struct{}),
	}

	for _, opt := range opts {
		opt(e)
	}

	return e
}

// AsyncOptions configures async projection behavior.
type AsyncOptions struct {
	// BatchSize is the maximum number of events to process in a batch.
	// Default: 100
	BatchSize int

	// BatchTimeout is the maximum time to wait for a full batch.
	// Default: 1 second
	BatchTimeout time.Duration

	// PollInterval is how often to poll for new events when idle.
	// Default: 100ms
	PollInterval time.Duration

	// RetryPolicy defines how to handle errors.
	RetryPolicy RetryPolicy

	// MaxRetries is the maximum number of retries for failed events.
	// Default: 3
	MaxRetries int

	// StartFromBeginning starts processing from the beginning of the event stream.
	// If false, starts from the last checkpoint.
	// Default: false
	StartFromBeginning bool
}

// DefaultAsyncOptions returns the default async projection options.
func DefaultAsyncOptions() AsyncOptions {
	return AsyncOptions{
		BatchSize:          100,
		BatchTimeout:       time.Second,
		PollInterval:       100 * time.Millisecond,
		RetryPolicy:        ExponentialBackoffRetry(3, 100*time.Millisecond, 10*time.Second),
		MaxRetries:         3,
		StartFromBeginning: false,
	}
}

// RetryPolicy defines how to handle retries for failed operations.
type RetryPolicy interface {
	// ShouldRetry returns true if the operation should be retried.
	ShouldRetry(attempt int, err error) bool

	// Delay returns the duration to wait before the next retry.
	Delay(attempt int) time.Duration
}

// exponentialBackoffRetry implements RetryPolicy with exponential backoff.
type exponentialBackoffRetry struct {
	maxRetries int
	baseDelay  time.Duration
	maxDelay   time.Duration
}

// ExponentialBackoffRetry creates a new retry policy with exponential backoff.
func ExponentialBackoffRetry(maxRetries int, baseDelay, maxDelay time.Duration) RetryPolicy {
	return &exponentialBackoffRetry{
		maxRetries: maxRetries,
		baseDelay:  baseDelay,
		maxDelay:   maxDelay,
	}
}

func (r *exponentialBackoffRetry) ShouldRetry(attempt int, err error) bool {
	if err == nil {
		return false
	}
	return attempt < r.maxRetries
}

func (r *exponentialBackoffRetry) Delay(attempt int) time.Duration {
	delay := r.baseDelay * (1 << uint(attempt))
	if delay > r.maxDelay {
		delay = r.maxDelay
	}
	return delay
}

// noRetry is a retry policy that never retries.
type noRetry struct{}

// NoRetry returns a retry policy that never retries.
func NoRetry() RetryPolicy {
	return &noRetry{}
}

func (r *noRetry) ShouldRetry(attempt int, err error) bool {
	return false
}

func (r *noRetry) Delay(attempt int) time.Duration {
	return 0
}

// RegisterInline registers an inline projection.
// Inline projections are processed synchronously with event appends.
func (e *ProjectionEngine) RegisterInline(projection InlineProjection) error {
	if projection == nil {
		return ErrNilProjection
	}
	if projection.Name() == "" {
		return ErrEmptyProjectionName
	}

	e.inlineMu.Lock()
	defer e.inlineMu.Unlock()

	// Check for duplicates
	for _, p := range e.inlineProjections {
		if p.Name() == projection.Name() {
			return fmt.Errorf("%w: %s", ErrProjectionAlreadyRegistered, projection.Name())
		}
	}

	e.inlineProjections = append(e.inlineProjections, projection)
	e.logger.Info("Registered inline projection", "name", projection.Name())
	return nil
}

// RegisterAsync registers an async projection with the given options.
// Async projections are processed in background workers.
func (e *ProjectionEngine) RegisterAsync(projection AsyncProjection, opts ...AsyncOptions) error {
	if projection == nil {
		return ErrNilProjection
	}
	if projection.Name() == "" {
		return ErrEmptyProjectionName
	}

	options := DefaultAsyncOptions()
	if len(opts) > 0 {
		options = opts[0]
	}

	e.asyncMu.Lock()
	defer e.asyncMu.Unlock()

	// Check for duplicates
	if _, exists := e.asyncProjections[projection.Name()]; exists {
		return fmt.Errorf("%w: %s", ErrProjectionAlreadyRegistered, projection.Name())
	}

	worker := &asyncProjectionWorker{
		projection:      projection,
		options:         options,
		engine:          e,
		stopCh:          make(chan struct{}),
		state:           ProjectionStateStopped,
		eventsProcessed: 0,
	}

	e.asyncProjections[projection.Name()] = worker
	e.logger.Info("Registered async projection", "name", projection.Name())
	return nil
}

// RegisterLive registers a live projection.
// Live projections receive events in real-time.
func (e *ProjectionEngine) RegisterLive(projection LiveProjection) error {
	if projection == nil {
		return ErrNilProjection
	}
	if projection.Name() == "" {
		return ErrEmptyProjectionName
	}

	e.liveMu.Lock()
	defer e.liveMu.Unlock()

	// Check for duplicates
	if _, exists := e.liveProjections[projection.Name()]; exists {
		return fmt.Errorf("%w: %s", ErrProjectionAlreadyRegistered, projection.Name())
	}

	worker := &liveProjectionWorker{
		projection: projection,
		engine:     e,
		stopCh:     make(chan struct{}),
		state:      ProjectionStateStopped,
	}

	e.liveProjections[projection.Name()] = worker
	e.logger.Info("Registered live projection", "name", projection.Name())
	return nil
}

// Unregister removes a projection by name.
func (e *ProjectionEngine) Unregister(name string) error {
	if name == "" {
		return ErrEmptyProjectionName
	}

	// Try inline projections
	e.inlineMu.Lock()
	for i, p := range e.inlineProjections {
		if p.Name() == name {
			e.inlineProjections = append(e.inlineProjections[:i], e.inlineProjections[i+1:]...)
			e.inlineMu.Unlock()
			e.logger.Info("Unregistered inline projection", "name", name)
			return nil
		}
	}
	e.inlineMu.Unlock()

	// Try async projections
	e.asyncMu.Lock()
	if worker, exists := e.asyncProjections[name]; exists {
		if worker.state == ProjectionStateRunning {
			close(worker.stopCh)
		}
		delete(e.asyncProjections, name)
		e.asyncMu.Unlock()
		e.logger.Info("Unregistered async projection", "name", name)
		return nil
	}
	e.asyncMu.Unlock()

	// Try live projections
	e.liveMu.Lock()
	if worker, exists := e.liveProjections[name]; exists {
		if worker.state == ProjectionStateRunning {
			close(worker.stopCh)
		}
		delete(e.liveProjections, name)
		e.liveMu.Unlock()
		e.logger.Info("Unregistered live projection", "name", name)
		return nil
	}
	e.liveMu.Unlock()

	return fmt.Errorf("%w: %s", ErrProjectionNotFound, name)
}

// Start starts the projection engine and all registered projections.
func (e *ProjectionEngine) Start(ctx context.Context) error {
	if e.running.Load() {
		return ErrProjectionEngineAlreadyRunning
	}

	if e.checkpointStore == nil {
		return ErrNoCheckpointStore
	}

	e.running.Store(true)
	e.stopping.Store(false)
	e.stopCh = make(chan struct{})

	// Start async projection workers
	e.asyncMu.RLock()
	for _, worker := range e.asyncProjections {
		e.wg.Add(1)
		go e.runAsyncWorker(ctx, worker)
	}
	e.asyncMu.RUnlock()

	// Start live projection workers
	e.liveMu.RLock()
	for _, worker := range e.liveProjections {
		e.wg.Add(1)
		go e.runLiveWorker(ctx, worker)
	}
	e.liveMu.RUnlock()

	e.logger.Info("Projection engine started")
	return nil
}

// Stop gracefully stops the projection engine.
func (e *ProjectionEngine) Stop(ctx context.Context) error {
	if !e.running.Load() {
		return nil
	}

	e.stopping.Store(true)
	close(e.stopCh)

	// Wait for all workers to stop with context timeout
	done := make(chan struct{})
	go func() {
		e.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		e.running.Store(false)
		e.logger.Info("Projection engine stopped")
		return nil
	case <-ctx.Done():
		e.running.Store(false)
		return ctx.Err()
	}
}

// IsRunning returns true if the engine is running.
func (e *ProjectionEngine) IsRunning() bool {
	return e.running.Load()
}

// GetStatus returns the status of a projection by name.
func (e *ProjectionEngine) GetStatus(name string) (*ProjectionStatus, error) {
	// Check async projections
	e.asyncMu.RLock()
	if worker, exists := e.asyncProjections[name]; exists {
		e.asyncMu.RUnlock()
		return worker.getStatus(), nil
	}
	e.asyncMu.RUnlock()

	// Check live projections
	e.liveMu.RLock()
	if worker, exists := e.liveProjections[name]; exists {
		e.liveMu.RUnlock()
		return worker.getStatus(), nil
	}
	e.liveMu.RUnlock()

	// Check inline projections
	e.inlineMu.RLock()
	for _, p := range e.inlineProjections {
		if p.Name() == name {
			e.inlineMu.RUnlock()
			return &ProjectionStatus{
				Name:  name,
				State: ProjectionStateRunning,
			}, nil
		}
	}
	e.inlineMu.RUnlock()

	return nil, fmt.Errorf("%w: %s", ErrProjectionNotFound, name)
}

// GetAllStatuses returns the status of all registered projections.
func (e *ProjectionEngine) GetAllStatuses() []*ProjectionStatus {
	var statuses []*ProjectionStatus

	// Async projections
	e.asyncMu.RLock()
	for _, worker := range e.asyncProjections {
		statuses = append(statuses, worker.getStatus())
	}
	e.asyncMu.RUnlock()

	// Live projections
	e.liveMu.RLock()
	for _, worker := range e.liveProjections {
		statuses = append(statuses, worker.getStatus())
	}
	e.liveMu.RUnlock()

	// Inline projections
	e.inlineMu.RLock()
	for _, p := range e.inlineProjections {
		statuses = append(statuses, &ProjectionStatus{
			Name:  p.Name(),
			State: ProjectionStateRunning,
		})
	}
	e.inlineMu.RUnlock()

	return statuses
}

// ProcessInlineProjections processes all inline projections for the given events.
// This is called by the event store after appending events.
func (e *ProjectionEngine) ProcessInlineProjections(ctx context.Context, events []StoredEvent) error {
	e.inlineMu.RLock()
	projections := make([]InlineProjection, len(e.inlineProjections))
	copy(projections, e.inlineProjections)
	e.inlineMu.RUnlock()

	for _, event := range events {
		for _, projection := range projections {
			if !shouldHandleEvent(projection, event.Type) {
				continue
			}

			start := time.Now()
			err := projection.Apply(ctx, event)
			duration := time.Since(start)

			if err != nil {
				e.metrics.RecordEventProcessed(projection.Name(), event.Type, duration, false)
				e.metrics.RecordError(projection.Name(), err)
				return fmt.Errorf("inline projection %s failed: %w", projection.Name(), err)
			}

			e.metrics.RecordEventProcessed(projection.Name(), event.Type, duration, true)
		}
	}

	return nil
}

// NotifyLiveProjections notifies all live projections of new events.
func (e *ProjectionEngine) NotifyLiveProjections(ctx context.Context, events []StoredEvent) {
	e.liveMu.RLock()
	defer e.liveMu.RUnlock()

	for _, worker := range e.liveProjections {
		worker.stateMu.RLock()
		state := worker.state
		eventCh := worker.eventCh
		worker.stateMu.RUnlock()

		if state != ProjectionStateRunning {
			continue
		}

		if eventCh == nil {
			continue
		}

		for _, event := range events {
			if !shouldHandleEvent(worker.projection, event.Type) {
				continue
			}

			// Non-blocking send to avoid slowing down writes
			select {
			case eventCh <- event:
			default:
				e.logger.Warn("Live projection event channel full, dropping event",
					"projection", worker.projection.Name(),
					"eventType", event.Type)
			}
		}
	}
}

// shouldHandleEvent checks if a projection should handle the given event type.
func shouldHandleEvent(projection Projection, eventType string) bool {
	handledEvents := projection.HandledEvents()
	if len(handledEvents) == 0 {
		return true // Empty list means handle all events
	}
	for _, et := range handledEvents {
		if et == eventType {
			return true
		}
	}
	return false
}

// asyncProjectionWorker manages an async projection's background processing.
type asyncProjectionWorker struct {
	projection AsyncProjection
	options    AsyncOptions
	engine     *ProjectionEngine

	stopCh          chan struct{}
	state           ProjectionState
	stateMu         sync.RWMutex
	lastPosition    uint64
	eventsProcessed uint64
	lastProcessedAt time.Time
	lastError       error
}

func (w *asyncProjectionWorker) getStatus() *ProjectionStatus {
	w.stateMu.RLock()
	defer w.stateMu.RUnlock()

	status := &ProjectionStatus{
		Name:            w.projection.Name(),
		State:           w.state,
		LastPosition:    w.lastPosition,
		EventsProcessed: w.eventsProcessed,
		LastProcessedAt: w.lastProcessedAt,
	}

	if w.lastError != nil {
		status.Error = w.lastError.Error()
	}

	return status
}

func (w *asyncProjectionWorker) setState(state ProjectionState) {
	w.stateMu.Lock()
	w.state = state
	w.stateMu.Unlock()
}

func (w *asyncProjectionWorker) setError(err error) {
	w.stateMu.Lock()
	w.lastError = err
	if err != nil {
		w.state = ProjectionStateFaulted
	}
	w.stateMu.Unlock()
}

// runAsyncWorker runs the background worker for an async projection.
func (e *ProjectionEngine) runAsyncWorker(ctx context.Context, worker *asyncProjectionWorker) {
	defer e.wg.Done()

	worker.setState(ProjectionStateCatchingUp)

	// Get initial checkpoint
	var startPosition uint64
	if !worker.options.StartFromBeginning && e.checkpointStore != nil {
		pos, err := e.checkpointStore.GetCheckpoint(ctx, worker.projection.Name())
		if err != nil {
			e.logger.Error("Failed to get checkpoint", "projection", worker.projection.Name(), "error", err)
		} else {
			startPosition = pos
		}
	}
	worker.lastPosition = startPosition

	worker.setState(ProjectionStateRunning)

	ticker := time.NewTicker(worker.options.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-e.stopCh:
			worker.setState(ProjectionStateStopped)
			return
		case <-worker.stopCh:
			worker.setState(ProjectionStateStopped)
			return
		case <-ctx.Done():
			worker.setState(ProjectionStateStopped)
			return
		case <-ticker.C:
			if err := e.processAsyncBatch(ctx, worker); err != nil {
				if errors.Is(err, context.Canceled) {
					worker.setState(ProjectionStateStopped)
					return
				}
				e.logger.Error("Async projection error", "projection", worker.projection.Name(), "error", err)
				worker.setError(err)
				e.metrics.RecordError(worker.projection.Name(), err)
			}
		}
	}
}

// processAsyncBatch processes a batch of events for an async projection.
func (e *ProjectionEngine) processAsyncBatch(ctx context.Context, worker *asyncProjectionWorker) error {
	// Load events from current position
	// Note: This is a simplified implementation. In production, you'd want
	// to use the SubscriptionAdapter for better performance.
	events, err := e.loadEventsFromPosition(ctx, worker.lastPosition, worker.options.BatchSize)
	if err != nil {
		return fmt.Errorf("failed to load events: %w", err)
	}

	if len(events) == 0 {
		return nil
	}

	// Filter events that this projection handles
	var filteredEvents []StoredEvent
	for _, event := range events {
		if shouldHandleEvent(worker.projection, event.Type) {
			filteredEvents = append(filteredEvents, event)
		}
	}

	if len(filteredEvents) == 0 {
		// Update position even if no events were handled
		worker.lastPosition = events[len(events)-1].GlobalPosition
		return nil
	}

	// Try batch processing first
	start := time.Now()
	err = worker.projection.ApplyBatch(ctx, filteredEvents)
	if errors.Is(err, ErrNotImplemented) {
		// Fall back to sequential processing
		for _, event := range filteredEvents {
			eventStart := time.Now()
			if err := worker.projection.Apply(ctx, event); err != nil {
				e.metrics.RecordEventProcessed(worker.projection.Name(), event.Type, time.Since(eventStart), false)
				return fmt.Errorf("failed to apply event: %w", err)
			}
			e.metrics.RecordEventProcessed(worker.projection.Name(), event.Type, time.Since(eventStart), true)
			atomic.AddUint64(&worker.eventsProcessed, 1)
		}
	} else if err != nil {
		e.metrics.RecordBatchProcessed(worker.projection.Name(), len(filteredEvents), time.Since(start), false)
		return err
	} else {
		e.metrics.RecordBatchProcessed(worker.projection.Name(), len(filteredEvents), time.Since(start), true)
		atomic.AddUint64(&worker.eventsProcessed, uint64(len(filteredEvents)))
	}

	// Update checkpoint
	newPosition := events[len(events)-1].GlobalPosition
	if e.checkpointStore != nil {
		if err := e.checkpointStore.SetCheckpoint(ctx, worker.projection.Name(), newPosition); err != nil {
			e.logger.Error("Failed to save checkpoint", "projection", worker.projection.Name(), "error", err)
		} else {
			e.metrics.RecordCheckpoint(worker.projection.Name(), newPosition)
		}
	}

	worker.stateMu.Lock()
	worker.lastPosition = newPosition
	worker.lastProcessedAt = time.Now()
	worker.stateMu.Unlock()

	return nil
}

// loadEventsFromPosition loads events starting from the given global position.
func (e *ProjectionEngine) loadEventsFromPosition(ctx context.Context, fromPosition uint64, limit int) ([]StoredEvent, error) {
	// This is a simplified implementation that loads events across all streams.
	// In production, you'd want to use a more efficient query or subscription.
	adapter := e.store.Adapter()

	// Check if adapter supports subscription
	if subAdapter, ok := adapter.(SubscriptionAdapter); ok {
		return e.loadEventsViaSubscription(ctx, subAdapter, fromPosition, limit)
	}

	// Fallback: This is inefficient but works for simple cases
	// In a real implementation, you'd add a method to EventStoreAdapter for this
	return nil, nil
}

// loadEventsViaSubscription loads events using the subscription adapter.
func (e *ProjectionEngine) loadEventsViaSubscription(ctx context.Context, adapter SubscriptionAdapter, fromPosition uint64, limit int) ([]StoredEvent, error) {
	events, err := adapter.LoadFromPosition(ctx, fromPosition, limit)
	if err != nil {
		return nil, err
	}
	return events, nil
}

// liveProjectionWorker manages a live projection's real-time processing.
type liveProjectionWorker struct {
	projection LiveProjection
	engine     *ProjectionEngine

	stopCh    chan struct{}
	eventCh   chan StoredEvent
	state     ProjectionState
	stateMu   sync.RWMutex
	lastError error
}

func (w *liveProjectionWorker) getStatus() *ProjectionStatus {
	w.stateMu.RLock()
	defer w.stateMu.RUnlock()

	status := &ProjectionStatus{
		Name:  w.projection.Name(),
		State: w.state,
	}

	if w.lastError != nil {
		status.Error = w.lastError.Error()
	}

	return status
}

func (w *liveProjectionWorker) setState(state ProjectionState) {
	w.stateMu.Lock()
	w.state = state
	w.stateMu.Unlock()
}

// runLiveWorker runs the background worker for a live projection.
func (e *ProjectionEngine) runLiveWorker(ctx context.Context, worker *liveProjectionWorker) {
	defer e.wg.Done()

	eventCh := make(chan StoredEvent, 1000)

	worker.stateMu.Lock()
	worker.eventCh = eventCh
	worker.state = ProjectionStateRunning
	worker.stateMu.Unlock()

	for {
		select {
		case <-e.stopCh:
			worker.setState(ProjectionStateStopped)
			return
		case <-worker.stopCh:
			worker.setState(ProjectionStateStopped)
			return
		case <-ctx.Done():
			worker.setState(ProjectionStateStopped)
			return
		case event := <-eventCh:
			func() {
				defer func() {
					if r := recover(); r != nil {
						e.logger.Error("Live projection panicked", "projection", worker.projection.Name(), "panic", r)
					}
				}()
				worker.projection.OnEvent(ctx, event)
			}()
		}
	}
}

// SubscriptionAdapter provides methods for subscribing to event streams.
// This interface extends the basic EventStoreAdapter for subscription capabilities.
type SubscriptionAdapter interface {
	// LoadFromPosition loads events starting from a global position.
	LoadFromPosition(ctx context.Context, fromPosition uint64, limit int) ([]StoredEvent, error)

	// SubscribeAll subscribes to all events across all streams.
	SubscribeAll(ctx context.Context, fromPosition uint64) (<-chan StoredEvent, error)

	// SubscribeStream subscribes to events from a specific stream.
	SubscribeStream(ctx context.Context, streamID string, fromVersion int64) (<-chan StoredEvent, error)

	// SubscribeCategory subscribes to all events from streams in a category.
	SubscribeCategory(ctx context.Context, category string, fromPosition uint64) (<-chan StoredEvent, error)
}
