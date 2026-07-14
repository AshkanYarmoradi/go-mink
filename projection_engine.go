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

	// stateObserver, if set, is invoked (outside any worker lock) on every async/live
	// projection state transition — see WithProjectionStateObserver. nil ⇒ no callback.
	stateObserver func(name string, oldState, newState ProjectionState, err error)

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
	// lifecycleMu serializes the stopping-transition against the guarded wg.Add in
	// launchSupervisor, so a manual Restart can never add to the wait group after Stop
	// has begun joining workers (which would race/panic the wait group).
	lifecycleMu sync.Mutex
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

// WithProjectionStateObserver registers a callback invoked on every async/live projection
// state transition (oldState → newState), carrying the fault error when a worker enters
// ProjectionStateFaulted (nil otherwise). Use it to push an alert on Faulted / Restarting
// and on recovery instead of polling GetStatus. The callback runs outside the worker's
// state lock, so it may safely call back into the engine (e.g. GetStatus) without deadlock
// or reentrancy; it should not block for long, as it runs on the worker's goroutine. nil
// (the default) means no callback and zero overhead. This does not change or extend the
// ProjectionMetrics interface.
func WithProjectionStateObserver(fn func(name string, oldState, newState ProjectionState, err error)) ProjectionEngineOption {
	return func(e *ProjectionEngine) {
		e.stateObserver = fn
	}
}

// notifyStateObserver invokes the registered state observer, if any, for a transition.
// It is always called after the caller has released the worker's state lock, so the
// observer can re-enter the engine safely. A nil observer is a zero-overhead no-op.
func (e *ProjectionEngine) notifyStateObserver(name string, oldState, newState ProjectionState, err error) {
	if e.stateObserver != nil {
		e.stateObserver(name, oldState, newState, err)
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

	// BatchTimeout bounds how long loading and processing a single batch may take.
	// When > 0 the batch runs under a context with this deadline. 0 disables it.
	// Default: 1 second
	BatchTimeout time.Duration

	// PollInterval is how often to poll for new events when idle.
	// Default: 100ms
	PollInterval time.Duration

	// RetryPolicy defines how to handle errors. When set, its ShouldRetry method governs
	// the retry budget and MaxRetries is ignored.
	RetryPolicy RetryPolicy

	// MaxRetries bounds retries for a failing (poison) event before the engine gives up
	// on it, and is used only when RetryPolicy is nil. A positive value gives up after
	// that many attempts; a non-positive value (0 or negative) retries indefinitely — the
	// same convention the built-in ExponentialBackoffRetry/RetryForever policies follow.
	// Default: 3
	MaxRetries int

	// ErrorClassifier optionally classifies a processing error as transient or poison. An
	// error classified ErrorClassTransient is retried with backoff but does not consume
	// the poison budget (MaxRetries / RetryPolicy), so it never reaches OnPoisonEvent and
	// never faults the worker — infrastructure blips self-heal instead of exhausting the
	// budget. An ErrorClassPoison error is accounted exactly as before. nil (the default)
	// classifies every error as poison, making behavior and overhead identical to the
	// prior release. See DefaultErrorClassifier for a batteries-included implementation.
	ErrorClassifier func(error) ErrorClass

	// OnPoisonEvent, if set, is invoked when an event keeps failing after the
	// retry budget is exhausted. Returning nil tells the engine to skip the event
	// (advance past it) and continue; returning an error stops the worker. When
	// nil, the worker stops in the Faulted state after the retries are exhausted.
	OnPoisonEvent func(ctx context.Context, event StoredEvent, err error) error

	// RestartPolicy optionally restarts a Faulted worker with backoff, resuming strictly
	// from its persisted checkpoint (never reprocessing from position 0). nil (the
	// default) leaves a fault terminal — the worker goroutine exits, exactly as before —
	// so this is opt-in and zero-overhead when unset. Reprocessing history remains the
	// exclusive job of Rebuild.
	RestartPolicy RestartPolicy

	// StartFromBeginning starts processing from the beginning of the event stream on the
	// worker's first boot when no checkpoint exists. It never applies to a supervised or
	// manual restart, which always resume from the checkpoint.
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

// LiveOptions configures live projection behavior.
type LiveOptions struct {
	// BufferSize is the size of the event channel buffer.
	// Default: 1000
	BufferSize int
}

// DefaultLiveOptions returns the default live projection options.
func DefaultLiveOptions() LiveOptions {
	return LiveOptions{
		BufferSize: 1000,
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

// ExponentialBackoffRetry creates a RetryPolicy with exponential backoff. A positive
// maxRetries caps the attempts (retry while attempt < maxRetries); a non-positive
// maxRetries (0 or negative) means retry indefinitely — the same "non-positive = retry
// forever" convention AsyncOptions.MaxRetries uses, and the basis of RetryForever. To
// never retry, use NoRetry rather than a zero count. baseDelay is the first backoff and
// maxDelay caps it.
func ExponentialBackoffRetry(maxRetries int, baseDelay, maxDelay time.Duration) RetryPolicy {
	return &exponentialBackoffRetry{
		maxRetries: maxRetries,
		baseDelay:  baseDelay,
		maxDelay:   maxDelay,
	}
}

// RetryForever returns a RetryPolicy that retries on every non-nil error, without limit,
// using exponential backoff between attempts capped at maxDelay. It is the
// self-documenting spelling of ExponentialBackoffRetry(0, baseDelay, maxDelay) — prefer
// it when the intent is "retry forever with backoff." Use NoRetry to never retry.
func RetryForever(baseDelay, maxDelay time.Duration) RetryPolicy {
	return ExponentialBackoffRetry(0, baseDelay, maxDelay)
}

// ShouldRetry reports whether another attempt should be made. It returns false on a nil
// error. For a non-nil error it retries indefinitely when maxRetries <= 0 (the "retry
// forever" convention shared with AsyncOptions.MaxRetries and RetryForever), and
// otherwise retries while attempt < maxRetries.
func (r *exponentialBackoffRetry) ShouldRetry(attempt int, err error) bool {
	if err == nil {
		return false
	}
	if r.maxRetries <= 0 {
		return true
	}
	return attempt < r.maxRetries
}

func (r *exponentialBackoffRetry) Delay(attempt int) time.Duration {
	return expBackoff(attempt, r.baseDelay, r.maxDelay)
}

// expBackoff computes an exponential backoff delay of baseDelay * 2^shift, clamped to
// [baseDelay, maxDelay]. A negative shift clamps to 0; a shift large enough to overflow
// returns maxDelay. Shared by the retry and restart policies so both back off identically.
func expBackoff(shift int, baseDelay, maxDelay time.Duration) time.Duration {
	// Clamp shift to a valid range to avoid integer overflow.
	if shift < 0 {
		shift = 0
	}
	// Cap the shift amount to prevent overflow (max 62 for int64 duration).
	if shift > 62 {
		return maxDelay
	}
	delay := baseDelay * (1 << uint(shift)) // #nosec G115 - shift is clamped to 0-62
	if delay <= 0 || delay > maxDelay {
		// delay <= 0 catches a base*2^shift multiplication overflow.
		return maxDelay
	}
	return delay
}

// noRetry is a retry policy that never retries.
type noRetry struct{}

// NoRetry returns a retry policy that never retries. It is the single canonical way to
// stop on the first error; prefer it over ExponentialBackoffRetry(0, …), which now means
// "retry forever."
func NoRetry() RetryPolicy {
	return &noRetry{}
}

func (r *noRetry) ShouldRetry(attempt int, err error) bool {
	return false
}

func (r *noRetry) Delay(attempt int) time.Duration {
	return 0
}

// ErrorClass classifies a projection processing error for retry accounting. Its zero
// value is ErrorClassPoison, so an unclassified error behaves exactly as before this
// type existed. See AsyncOptions.ErrorClassifier and DefaultErrorClassifier.
type ErrorClass int

const (
	// ErrorClassPoison is the zero value: the error counts against the poison retry
	// budget (AsyncOptions.MaxRetries / RetryPolicy) and, once the budget is exhausted,
	// reaches OnPoisonEvent or faults the worker — the classic poison-event path.
	ErrorClassPoison ErrorClass = iota

	// ErrorClassTransient marks a retryable infrastructure error: it is retried with
	// backoff but does NOT consume the poison budget, so it never trips OnPoisonEvent and
	// never faults the worker. Use it for errors that a later attempt can succeed on
	// (dropped connections, failovers), letting the worker self-heal when infra returns.
	ErrorClassTransient
)

// DefaultErrorClassifier classifies err as ErrorClassTransient when err, or any error in
// its Unwrap chain, (a) satisfies errors.Is(err, ErrTransient), (b) implements
// Retryable() bool returning true, or (c) implements interface{ Temporary() bool }
// returning true (the net.Error idiom); otherwise it returns ErrorClassPoison. It
// deliberately does NOT treat context.DeadlineExceeded as transient (even though that
// error reports Temporary() == true), so a genuinely hung poison event is not retried
// forever behind a batch timeout; a caller who wants deadlines treated as transient must
// classify them explicitly. Exported so a custom classifier can wrap or compose it, e.g.
// to add driver-specific transient codes.
func DefaultErrorClassifier(err error) ErrorClass {
	if err == nil {
		return ErrorClassPoison
	}
	// An explicit transient marker always wins.
	if errors.Is(err, ErrTransient) {
		return ErrorClassTransient
	}
	// A context deadline is NOT auto-classified transient: context.DeadlineExceeded
	// satisfies Temporary() == true, but treating a batch timeout as transient would
	// retry a genuinely hung poison event forever. Guard it before the behavioral-
	// interface checks below.
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrorClassPoison
	}
	for e := err; e != nil; e = errors.Unwrap(e) {
		if r, ok := e.(Retryable); ok && r.Retryable() {
			return ErrorClassTransient
		}
		if t, ok := e.(interface{ Temporary() bool }); ok && t.Temporary() {
			return ErrorClassTransient
		}
	}
	return ErrorClassPoison
}

// RestartPolicy decides whether and how long to wait before restarting a Faulted async
// projection worker. The engine's supervising loop consults it after a worker exits into
// Faulted; see AsyncOptions.RestartPolicy. A restart always resumes from the worker's
// persisted checkpoint (never from position 0).
type RestartPolicy interface {
	// ShouldRestart reports whether a worker that has now faulted restartCount times
	// (1 for the first restart) should be restarted again. lastErr is the fault cause.
	ShouldRestart(restartCount int, lastErr error) bool

	// Delay returns how long to wait before the given restart attempt (restartCount
	// starts at 1). It should grow with restartCount and be capped, to avoid hot-looping.
	Delay(restartCount int) time.Duration
}

// exponentialBackoffRestart implements RestartPolicy with exponential backoff.
type exponentialBackoffRestart struct {
	maxRestarts int
	baseDelay   time.Duration
	maxDelay    time.Duration
}

// ExponentialBackoffRestart returns a RestartPolicy that restarts a Faulted worker with
// exponential backoff. A positive maxRestarts caps the number of restarts (after which
// the worker stays Faulted); a non-positive maxRestarts (0 or negative) restarts
// indefinitely — the same "non-positive = unlimited" convention as ExponentialBackoffRetry
// and RetryForever. baseDelay is the first backoff and maxDelay caps it.
func ExponentialBackoffRestart(maxRestarts int, baseDelay, maxDelay time.Duration) RestartPolicy {
	return &exponentialBackoffRestart{
		maxRestarts: maxRestarts,
		baseDelay:   baseDelay,
		maxDelay:    maxDelay,
	}
}

// RestartForever returns a RestartPolicy that restarts a Faulted worker without limit,
// with exponential backoff capped at maxDelay. It is the self-documenting spelling of
// ExponentialBackoffRestart(0, baseDelay, maxDelay).
func RestartForever(baseDelay, maxDelay time.Duration) RestartPolicy {
	return ExponentialBackoffRestart(0, baseDelay, maxDelay)
}

func (r *exponentialBackoffRestart) ShouldRestart(restartCount int, _ error) bool {
	if r.maxRestarts <= 0 {
		return true
	}
	return restartCount <= r.maxRestarts
}

func (r *exponentialBackoffRestart) Delay(restartCount int) time.Duration {
	// restartCount starts at 1, so the first restart waits baseDelay (shift 0).
	return expBackoff(restartCount-1, r.baseDelay, r.maxDelay)
}

// validateProjection checks if a projection is valid for registration.
// Returns an error if the projection is nil or has an empty name.
func validateProjection(projection Projection) error {
	if projection == nil {
		return ErrNilProjection
	}
	if projection.Name() == "" {
		return ErrEmptyProjectionName
	}
	return nil
}

// RegisterInline registers an inline projection.
// Inline projections are processed synchronously with event appends.
func (e *ProjectionEngine) RegisterInline(projection InlineProjection) error {
	if err := validateProjection(projection); err != nil {
		return err
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
	if err := validateProjection(projection); err != nil {
		return err
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

// RegisterLive registers a live projection with optional configuration.
// Live projections receive events in real-time.
func (e *ProjectionEngine) RegisterLive(projection LiveProjection, opts ...LiveOptions) error {
	if err := validateProjection(projection); err != nil {
		return err
	}

	options := DefaultLiveOptions()
	if len(opts) > 0 {
		options = opts[0]
	}
	if options.BufferSize <= 0 {
		options.BufferSize = 1000
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
		eventCh:    make(chan StoredEvent, options.BufferSize),
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
		worker.closeOnce.Do(func() {
			close(worker.stopCh)
		})
		delete(e.asyncProjections, name)
		e.asyncMu.Unlock()
		e.logger.Info("Unregistered async projection", "name", name)
		return nil
	}
	e.asyncMu.Unlock()

	// Try live projections
	e.liveMu.Lock()
	if worker, exists := e.liveProjections[name]; exists {
		worker.closeOnce.Do(func() {
			close(worker.stopCh)
		})
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

	// Start async projection workers under a supervising loop. resumeFirst is false on
	// first boot so StartFromBeginning is honored; a later restart forces a checkpoint
	// resume.
	e.asyncMu.RLock()
	for _, worker := range e.asyncProjections {
		e.launchSupervisor(ctx, worker, false)
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

	// Serialize the stopping transition with launchSupervisor's guarded wg.Add so a
	// concurrent Restart cannot add to the wait group after we begin joining.
	e.lifecycleMu.Lock()
	e.stopping.Store(true)
	close(e.stopCh)
	e.lifecycleMu.Unlock()

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

// Pause pauses a registered async or live projection. A paused async worker stops
// processing new events (entering ProjectionStatePaused) but stays alive; a paused
// live projection stops receiving notifications. Returns ErrProjectionNotFound if
// no projection with the given name is registered.
func (e *ProjectionEngine) Pause(name string) error {
	e.asyncMu.RLock()
	if w, ok := e.asyncProjections[name]; ok {
		e.asyncMu.RUnlock()
		w.paused.Store(true)
		w.setState(ProjectionStatePaused)
		e.logger.Info("Paused projection", "name", name)
		return nil
	}
	e.asyncMu.RUnlock()

	e.liveMu.RLock()
	if w, ok := e.liveProjections[name]; ok {
		e.liveMu.RUnlock()
		w.setState(ProjectionStatePaused)
		e.logger.Info("Paused projection", "name", name)
		return nil
	}
	e.liveMu.RUnlock()

	return fmt.Errorf("%w: %s", ErrProjectionNotFound, name)
}

// Resume resumes a previously paused async or live projection.
// Returns ErrProjectionNotFound if no projection with the given name is registered.
func (e *ProjectionEngine) Resume(name string) error {
	e.asyncMu.RLock()
	if w, ok := e.asyncProjections[name]; ok {
		e.asyncMu.RUnlock()
		w.paused.Store(false)
		w.setState(ProjectionStateRunning)
		e.logger.Info("Resumed projection", "name", name)
		return nil
	}
	e.asyncMu.RUnlock()

	e.liveMu.RLock()
	if w, ok := e.liveProjections[name]; ok {
		e.liveMu.RUnlock()
		w.setState(ProjectionStateRunning)
		e.logger.Info("Resumed projection", "name", name)
		return nil
	}
	e.liveMu.RUnlock()

	return fmt.Errorf("%w: %s", ErrProjectionNotFound, name)
}

// Rebuild rebuilds a registered async projection from scratch. The worker is
// paused and marked ProjectionStateRebuilding for the duration; on completion it
// resumes from the rebuilt checkpoint. For a consistent rebuild the projection
// should be quiescent (the engine stopped, or the projection already paused).
// Returns ErrProjectionNotFound if the async projection is not registered.
func (e *ProjectionEngine) Rebuild(ctx context.Context, name string, opts ...RebuildOptions) error {
	e.asyncMu.RLock()
	worker, ok := e.asyncProjections[name]
	e.asyncMu.RUnlock()
	if !ok {
		return fmt.Errorf("%w: %s", ErrProjectionNotFound, name)
	}

	wasPaused := worker.paused.Swap(true)
	worker.setState(ProjectionStateRebuilding)
	defer func() {
		worker.paused.Store(wasPaused)
		if wasPaused {
			worker.setState(ProjectionStatePaused)
		} else {
			worker.setState(ProjectionStateRunning)
		}
	}()

	// Quiesce the worker: pausing only takes effect at the worker's next tick, so
	// acquire its processing mutex to wait for any in-flight batch to finish and
	// to hold exclusive access for the duration of the rebuild. This guarantees
	// the rebuilder and the worker never call Apply on the same projection
	// concurrently and never race on the checkpoint.
	worker.processingMu.Lock()
	defer worker.processingMu.Unlock()

	rebuilder := NewProjectionRebuilder(e.store, e.checkpointStore,
		WithRebuilderLogger(e.logger), WithRebuilderMetrics(e.metrics))
	if err := rebuilder.RebuildAsync(ctx, worker.projection, opts...); err != nil {
		return err
	}

	// Resume the worker from the rebuilt checkpoint position.
	var pos uint64
	if e.checkpointStore != nil {
		if p, err := e.checkpointStore.GetCheckpoint(ctx, name); err == nil {
			pos = p
		}
	}
	worker.stateMu.Lock()
	worker.lastPosition = pos
	worker.stateMu.Unlock()
	return nil
}

// Restart relaunches a Faulted async projection worker, resuming strictly from its
// persisted checkpoint (never reprocessing from position 0, regardless of
// StartFromBeginning). It is the operator counterpart to a configured RestartPolicy and
// is symmetric with Pause/Resume/Rebuild. It is idempotent: a no-op returning nil when the
// named worker is not Faulted (or when the engine is not running), and it returns
// ErrProjectionNotFound for an unknown name. Concurrent Restart calls — and a race with an
// automatic policy-driven restart — relaunch at most one worker goroutine.
func (e *ProjectionEngine) Restart(ctx context.Context, name string) error {
	e.asyncMu.RLock()
	worker, ok := e.asyncProjections[name]
	e.asyncMu.RUnlock()
	if !ok {
		return fmt.Errorf("%w: %s", ErrProjectionNotFound, name)
	}

	// Only a running, non-stopping engine can supervise a restart. Outside that window
	// Restart is an idempotent no-op (Start launches workers; Stop joins them).
	if !e.running.Load() || e.stopping.Load() {
		return nil
	}

	// Only a Faulted worker needs a restart; otherwise no-op (idempotent on a healthy,
	// paused, or rebuilding worker).
	worker.stateMu.RLock()
	faulted := worker.state == ProjectionStateFaulted
	worker.stateMu.RUnlock()
	if !faulted {
		return nil
	}

	// launchSupervisor CASes the per-worker guard, so concurrent Restarts (or a race with
	// an in-flight supervisor) launch at most one goroutine. resumeFirst is true so the
	// relaunched worker resumes from its checkpoint even if StartFromBeginning is set.
	e.launchSupervisor(ctx, worker, true)
	return nil
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
		// Skip events no inline projection handles — no need to decrypt (or loop) them.
		if !anyInlineHandles(projections, event.Type) {
			continue
		}
		// Decrypt field-encrypted events so inline projections see the same plaintext as the
		// async path (and as Load). No-op when encryption is unconfigured / the event is not
		// encrypted. A hard, unhandled decryption error fails this append's inline-projection
		// step — deliberately, and consistently with an inline projection Apply error below:
		// inline projections are synchronous with the write, so silently skipping an event
		// would leave the read model inconsistent. A crypto-shred handler that swallows the
		// error is a no-op and does not fail.
		decrypted, err := e.store.DecryptStoredEvent(ctx, event)
		if err != nil {
			return fmt.Errorf("inline projection decrypt event at position %d: %w", event.GlobalPosition, err)
		}
		event = decrypted
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
	// Snapshot the live workers under the lock, then release it before any
	// blocking channel sends. Holding liveMu during a blocking send would stall
	// Register/Unregister of every live projection behind one slow consumer.
	e.liveMu.RLock()
	workers := make([]*liveProjectionWorker, 0, len(e.liveProjections))
	for _, worker := range e.liveProjections {
		workers = append(workers, worker)
	}
	e.liveMu.RUnlock()

	// Field encryption is transparent on read: decrypt each event once before fan-out so live
	// projections see the same plaintext as Load/async/inline. Only events at least one live
	// worker handles are decrypted (others are passed through untouched and skipped in
	// delivery). Best-effort — live delivery has no error channel, so an event that fails to
	// decrypt is logged (with the projections that lose it) and skipped; those projections can
	// be rebuilt to backfill, and async projections still receive it via the retrying
	// checkpoint path. Guarded on encryption being configured, so unencrypted stores incur
	// zero overhead.
	if e.store.EncryptionConfig() != nil {
		out := make([]StoredEvent, 0, len(events))
		for _, ev := range events {
			handledBy := liveWorkersHandling(workers, ev.Type)
			if len(handledBy) == 0 {
				out = append(out, ev) // no live worker handles it — skip decrypt, will be filtered in delivery
				continue
			}
			dec, err := e.store.DecryptStoredEvent(ctx, ev)
			if err != nil {
				e.logger.Error("Live projection: dropping event that failed to decrypt (no live catch-up; rebuild to backfill)",
					"position", ev.GlobalPosition, "eventType", ev.Type,
					"affectedProjections", handledBy, "error", err)
				continue
			}
			out = append(out, dec)
		}
		events = out
	}

	for _, worker := range workers {
		worker.stateMu.RLock()
		state := worker.state
		eventCh := worker.eventCh
		worker.stateMu.RUnlock()

		if state != ProjectionStateRunning || eventCh == nil {
			// A live projection has no catch-up, so events arriving while it is not
			// Running are lost. Surface the drop (never silent) so the loss is visible
			// and operators can rebuild/restart to backfill.
			dropped := 0
			for _, event := range events {
				if shouldHandleEvent(worker.projection, event.Type) {
					dropped++
				}
			}
			if dropped > 0 {
				e.logger.Warn("Live projection not running; dropping events with no catch-up",
					"projection", worker.projection.Name(),
					"state", state,
					"droppedEvents", dropped)
			}
			continue
		}

	deliver:
		for _, event := range events {
			if !shouldHandleEvent(worker.projection, event.Type) {
				continue
			}

			// Block until the event is delivered, the worker stops, or the
			// context is cancelled. Watching the worker's stopCh prevents an
			// unregistered worker (whose buffer is full and no longer drained)
			// from wedging the sender indefinitely.
			select {
			case eventCh <- event:
			case <-worker.stopCh:
				break deliver
			case <-ctx.Done():
				e.logger.Warn("Context cancelled while delivering live projection event, dropping event",
					"projection", worker.projection.Name(),
					"eventType", event.Type,
					"error", ctx.Err())
				return
			}
		}
	}
}

// shouldHandleEvent checks if a projection should handle the given event type.
func shouldHandleEvent(projection Projection, eventType string) bool {
	return ShouldHandleEventType(projection.HandledEvents(), eventType)
}

// anyInlineHandles reports whether any of the inline projections handles eventType. Used to
// skip decrypting (and looping over) an event that no inline projection cares about.
func anyInlineHandles(projections []InlineProjection, eventType string) bool {
	for _, p := range projections {
		if shouldHandleEvent(p, eventType) {
			return true
		}
	}
	return false
}

// liveWorkersHandling returns the names of the live workers that handle eventType. It drives
// both the decrypt-only-what-is-handled optimization and the attribution of a live event that
// is dropped because it failed to decrypt.
func liveWorkersHandling(workers []*liveProjectionWorker, eventType string) []string {
	var names []string
	for _, w := range workers {
		if shouldHandleEvent(w.projection, eventType) {
			names = append(names, w.projection.Name())
		}
	}
	return names
}

// asyncProjectionWorker manages an async projection's background processing.
type asyncProjectionWorker struct {
	projection AsyncProjection
	options    AsyncOptions
	engine     *ProjectionEngine

	stopCh    chan struct{}
	closeOnce sync.Once
	state     ProjectionState
	stateMu   sync.RWMutex
	paused    atomic.Bool
	// supervising is true while a supervising goroutine is alive for this worker. It
	// guarantees at most one supervisor per worker across Start and manual Restart, and a
	// race between them (see launchSupervisor).
	supervising atomic.Bool
	// processingMu serializes a worker's batch processing with a concurrent
	// Rebuild, so the rebuilder never calls Apply on the projection while the
	// worker is mid-batch (and vice versa).
	processingMu    sync.Mutex
	lastPosition    uint64
	eventsProcessed uint64
	lastProcessedAt time.Time
	lastError       error
	// failedEvent records the event that caused the most recent processing error,
	// so the poison-event handler can identify and skip it after retries exhaust.
	failedEvent *StoredEvent
}

func (w *asyncProjectionWorker) getStatus() *ProjectionStatus {
	w.stateMu.RLock()
	defer w.stateMu.RUnlock()

	status := &ProjectionStatus{
		Name:            w.projection.Name(),
		State:           w.state,
		LastPosition:    w.lastPosition,
		EventsProcessed: atomic.LoadUint64(&w.eventsProcessed),
		LastProcessedAt: w.lastProcessedAt,
	}

	if w.lastError != nil {
		status.Error = w.lastError.Error()
	}

	return status
}

func (w *asyncProjectionWorker) setState(state ProjectionState) {
	w.stateMu.Lock()
	old := w.state
	w.state = state
	w.stateMu.Unlock()
	if old != state {
		w.engine.notifyStateObserver(w.projection.Name(), old, state, nil)
	}
}

func (w *asyncProjectionWorker) setError(err error) {
	w.stateMu.Lock()
	old := w.state
	w.lastError = err
	if err != nil {
		w.state = ProjectionStateFaulted
	}
	newState := w.state
	w.stateMu.Unlock()
	if old != newState {
		w.engine.notifyStateObserver(w.projection.Name(), old, newState, err)
	}
}

// setTransientError records a transient (retryable) processing error for observability
// without faulting the worker: unlike setError it does not change the projection state,
// so a transient outage is retried without ever showing as Faulted or firing a state
// transition. Only reached when an ErrorClassifier classifies the error as transient.
func (w *asyncProjectionWorker) setTransientError(err error) {
	w.stateMu.Lock()
	w.lastError = err
	w.stateMu.Unlock()
}

func (w *asyncProjectionWorker) clearError() {
	w.stateMu.Lock()
	old := w.state
	w.lastError = nil
	w.state = ProjectionStateRunning
	w.stateMu.Unlock()
	if old != ProjectionStateRunning {
		w.engine.notifyStateObserver(w.projection.Name(), old, ProjectionStateRunning, nil)
	}
}

// getLastError returns the worker's last recorded error under the state lock. Used by the
// supervising loop to pass the fault cause to RestartPolicy.ShouldRestart.
func (w *asyncProjectionWorker) getLastError() error {
	w.stateMu.RLock()
	defer w.stateMu.RUnlock()
	return w.lastError
}

func (w *asyncProjectionWorker) setFailedEvent(event *StoredEvent) {
	w.stateMu.Lock()
	w.failedEvent = event
	w.stateMu.Unlock()
}

func (w *asyncProjectionWorker) clearFailedEvent() {
	w.stateMu.Lock()
	w.failedEvent = nil
	w.stateMu.Unlock()
}

func (w *asyncProjectionWorker) getFailedEvent() *StoredEvent {
	w.stateMu.RLock()
	defer w.stateMu.RUnlock()
	return w.failedEvent
}

// shouldRetry reports whether the worker should keep retrying after the given
// number of consecutive errors. It honors a configured RetryPolicy, falling back
// to MaxRetries when no policy is set. A non-positive budget means retry forever.
func (w *asyncProjectionWorker) shouldRetry(consecutiveErrors int, err error) bool {
	if w.options.RetryPolicy != nil {
		return w.options.RetryPolicy.ShouldRetry(consecutiveErrors, err)
	}
	if w.options.MaxRetries > 0 {
		return consecutiveErrors < w.options.MaxRetries
	}
	return true
}

// handlePoisonEvent is invoked when the retry budget for a failing event is
// exhausted. If an OnPoisonEvent handler is configured and chooses to skip
// (returns nil), the worker advances past the failing event and returns true so
// the caller can continue. Otherwise it returns false and the worker stops.
func (e *ProjectionEngine) handlePoisonEvent(ctx context.Context, worker *asyncProjectionWorker, cause error) bool {
	failed := worker.getFailedEvent()
	if worker.options.OnPoisonEvent == nil || failed == nil {
		return false
	}
	if err := worker.options.OnPoisonEvent(ctx, *failed, cause); err != nil {
		e.logger.Error("Poison-event handler requested stop",
			"projection", worker.projection.Name(),
			"global_position", failed.GlobalPosition,
			"error", err,
		)
		return false
	}
	// Skip the poison event: advance the position past it and persist a checkpoint.
	e.logger.Warn("Skipping poison event after exhausting retries",
		"projection", worker.projection.Name(),
		"event_type", failed.Type,
		"stream_id", failed.StreamID,
		"global_position", failed.GlobalPosition,
		"cause", cause,
	)
	worker.stateMu.Lock()
	worker.lastPosition = failed.GlobalPosition
	worker.failedEvent = nil
	worker.stateMu.Unlock()
	if e.checkpointStore != nil {
		if err := e.checkpointStore.SetCheckpoint(ctx, worker.projection.Name(), failed.GlobalPosition); err != nil {
			e.logger.Error("Failed to checkpoint after skipping poison event",
				"projection", worker.projection.Name(), "error", err)
		}
	}
	return true
}

// checkpointReadRetries / checkpointReadBackoff bound the transient-retry of the initial
// checkpoint read at async-worker startup, so a momentary checkpoint-store blip does not
// permanently fault the projection while still never silently restarting from position 0.
const (
	checkpointReadRetries = 3
	checkpointReadBackoff = 50 * time.Millisecond
)

// getCheckpointWithRetry reads the projection's checkpoint, retrying a transient error up to
// checkpointReadRetries times with a short linear backoff. A context cancellation/deadline
// (shutdown) returns immediately without retrying. Returns the last error if every attempt
// fails, so the caller can distinguish a persistent failure (fault) from a missing checkpoint.
func (e *ProjectionEngine) getCheckpointWithRetry(ctx context.Context, name string) (uint64, error) {
	var lastErr error
	for attempt := 1; attempt <= checkpointReadRetries; attempt++ {
		pos, err := e.checkpointStore.GetCheckpoint(ctx, name)
		if err == nil {
			return pos, nil
		}
		lastErr = err
		if isShutdownError(err) {
			return 0, err
		}
		if attempt < checkpointReadRetries {
			e.logger.Warn("Checkpoint read failed; retrying",
				"projection", name, "attempt", attempt, "error", err)
			select {
			case <-ctx.Done():
				return 0, ctx.Err()
			case <-time.After(checkpointReadBackoff * time.Duration(attempt)):
			}
		}
	}
	return 0, lastErr
}

// exitReason reports why a single run of runAsyncWorkerOnce returned, so the supervising
// loop can distinguish a clean stop from a fault eligible for restart.
type exitReason int

const (
	// exitStopped: the worker stopped cleanly (engine stop, worker stop, or context
	// cancellation). The supervisor must not restart it.
	exitStopped exitReason = iota
	// exitFaulted: the worker entered Faulted (persistent checkpoint-read failure or
	// poison-budget exhaustion with no skip). The supervisor may restart it if a
	// RestartPolicy permits.
	exitFaulted
)

// launchSupervisor starts a supervising goroutine for the worker under the wait group,
// unless one is already alive (the per-worker supervising guard) or the engine is
// stopping. It returns true when it launched one. resumeFirst forces the first run to
// resume from the checkpoint (used by manual Restart); Start passes false so the first
// boot honors StartFromBeginning. Centralizing the guarded wg.Add here keeps Stop's join
// race-free.
func (e *ProjectionEngine) launchSupervisor(ctx context.Context, worker *asyncProjectionWorker, resumeFirst bool) bool {
	if !worker.supervising.CompareAndSwap(false, true) {
		return false // a supervisor is already alive for this worker
	}
	e.lifecycleMu.Lock()
	if e.stopping.Load() {
		e.lifecycleMu.Unlock()
		worker.supervising.Store(false)
		return false
	}
	e.wg.Add(1)
	e.lifecycleMu.Unlock()

	go func() {
		defer worker.supervising.Store(false)
		e.superviseAsyncWorker(ctx, worker, resumeFirst)
	}()
	return true
}

// superviseAsyncWorker runs an async worker under a supervising loop that owns the single
// wg.Done for this launch. It runs the worker once; on a clean stop, or on a fault with no
// RestartPolicy, it returns (today's terminal behavior). With a RestartPolicy that permits
// restart, it marks the worker ProjectionStateRestarting, waits the policy's interruptible
// backoff, and re-runs it — always resuming from the checkpoint, so a restart never
// reprocesses from position 0.
func (e *ProjectionEngine) superviseAsyncWorker(ctx context.Context, worker *asyncProjectionWorker, resumeFirst bool) {
	defer e.wg.Done()

	restarts := 0
	for {
		reason := e.runAsyncWorkerOnce(ctx, worker, resumeFirst || restarts > 0)
		if reason != exitFaulted {
			return // clean stop
		}
		policy := worker.options.RestartPolicy
		if policy == nil {
			return // no supervisor configured → fault is terminal, exactly as before
		}
		restarts++
		if !policy.ShouldRestart(restarts, worker.getLastError()) {
			return // policy gave up → remain Faulted (terminal)
		}
		worker.setState(ProjectionStateRestarting)

		// Wait the restart backoff, still responding to stop/cancel signals so Stop can
		// join a worker that is waiting to restart.
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
		case <-time.After(policy.Delay(restarts)):
		}
	}
}

// runAsyncWorkerOnce runs a single lifetime of the async worker: catch up from the
// checkpoint, then poll and process batches until it stops (exitStopped) or faults
// (exitFaulted). resumeFromCheckpoint forces the checkpoint read even when
// StartFromBeginning is set — true on every restart, so a restart never reprocesses from
// position 0 (StartFromBeginning governs only the first boot, when no checkpoint exists).
func (e *ProjectionEngine) runAsyncWorkerOnce(ctx context.Context, worker *asyncProjectionWorker, resumeFromCheckpoint bool) exitReason {
	worker.setState(ProjectionStateCatchingUp)

	// Get initial checkpoint. On a restart we always read it (resumeFromCheckpoint);
	// StartFromBeginning is honored only on the first boot.
	var startPosition uint64
	if (resumeFromCheckpoint || !worker.options.StartFromBeginning) && e.checkpointStore != nil {
		pos, err := e.getCheckpointWithRetry(ctx, worker.projection.Name())
		if err != nil {
			// A load error is NOT "no checkpoint" — adapters return (0, nil) for a missing
			// checkpoint. Defaulting to 0 here would replay the entire history into a
			// non-idempotent projection. The read is retried on a transient error first
			// (above); only a shutdown or a persistent error reaches here.
			if isShutdownError(err) {
				// Engine stopping during startup — stop cleanly rather than fault.
				worker.setState(ProjectionStateStopped)
				return exitStopped
			}
			// A persistent checkpoint-read failure faults (never defaults to 0). It is a
			// fault like any other, so a configured RestartPolicy will retry it — a
			// checkpoint-store outage that later heals self-recovers.
			e.logger.Error("Failed to get checkpoint after retries; faulting worker instead of restarting from 0",
				"projection", worker.projection.Name(), "error", err)
			worker.setError(fmt.Errorf("load checkpoint: %w", err))
			return exitFaulted
		}
		startPosition = pos
	}
	worker.stateMu.Lock()
	worker.lastPosition = startPosition
	worker.stateMu.Unlock()

	worker.setState(ProjectionStateRunning)

	ticker := time.NewTicker(worker.options.PollInterval)
	defer ticker.Stop()

	// poisonErrors drives the retry budget (shouldRetry / OnPoisonEvent / fault) and counts
	// only ErrorClassPoison errors. backoffErrors counts every error (any class) and drives
	// only the backoff delay, so a transient outage still backs off. With no ErrorClassifier
	// every error is poison, so the two counters stay equal and the arithmetic is identical
	// to the pre-classification behavior.
	var poisonErrors int
	var backoffErrors int
	var firstErrorAt time.Time

	for {
		select {
		case <-e.stopCh:
			worker.setState(ProjectionStateStopped)
			return exitStopped
		case <-worker.stopCh:
			worker.setState(ProjectionStateStopped)
			return exitStopped
		case <-ctx.Done():
			worker.setState(ProjectionStateStopped)
			return exitStopped
		case <-ticker.C:
			// Skip processing while paused, but keep the worker alive.
			if worker.paused.Load() {
				worker.setState(ProjectionStatePaused)
				continue
			}
			// Serialize with Rebuild so the rebuilder never applies concurrently.
			worker.processingMu.Lock()
			err := e.processAsyncBatch(ctx, worker)
			worker.processingMu.Unlock()
			if err != nil {
				if errors.Is(err, context.Canceled) {
					worker.setState(ProjectionStateStopped)
					return exitStopped
				}

				backoffErrors++
				if backoffErrors == 1 {
					firstErrorAt = time.Now()
				}

				// Classify the error. Poison (the default, and the only class when no
				// classifier is set) consumes the retry budget; transient does not.
				isPoison := true
				if worker.options.ErrorClassifier != nil {
					isPoison = worker.options.ErrorClassifier(err) != ErrorClassTransient
				}
				if isPoison {
					poisonErrors++
				}

				// Log only at power-of-2 counts (1, 2, 4, 8, 16...) to reduce noise. Driven
				// by total consecutive errors so a transient outage is still visible.
				if backoffErrors&(backoffErrors-1) == 0 {
					e.logger.Error("Async projection error",
						"projection", worker.projection.Name(),
						"error", err,
						"consecutive_errors", backoffErrors,
					)
				}

				// Surface the error: a poison error faults the worker (as before); a
				// transient error is recorded for visibility but never faults.
				if isPoison {
					worker.setError(err)
				} else {
					worker.setTransientError(err)
				}
				e.metrics.RecordError(worker.projection.Name(), err)

				// Only a poison error consumes the budget and can trip OnPoisonEvent/fault.
				if isPoison && !worker.shouldRetry(poisonErrors, err) {
					if e.handlePoisonEvent(ctx, worker, err) {
						poisonErrors = 0
						backoffErrors = 0
						worker.clearError()
						continue
					}
					e.logger.Error("Async projection giving up after exhausting retries",
						"projection", worker.projection.Name(),
						"consecutive_errors", poisonErrors,
						"error", err,
					)
					worker.setError(err) // remain Faulted
					return exitFaulted
				}

				// Compute backoff delay from the total consecutive-error count.
				var delay time.Duration
				if worker.options.RetryPolicy != nil {
					delay = worker.options.RetryPolicy.Delay(backoffErrors - 1)
				} else {
					// Built-in fallback: exponential backoff, 100ms base, 30s cap
					shift := backoffErrors - 1
					if shift > 18 {
						shift = 18
					}
					delay = 100 * time.Millisecond * time.Duration(1<<uint(shift))
					if delay > 30*time.Second {
						delay = 30 * time.Second
					}
				}

				// Wait with backoff, still responding to stop/cancel signals
				select {
				case <-e.stopCh:
					worker.setState(ProjectionStateStopped)
					return exitStopped
				case <-worker.stopCh:
					worker.setState(ProjectionStateStopped)
					return exitStopped
				case <-ctx.Done():
					worker.setState(ProjectionStateStopped)
					return exitStopped
				case <-time.After(delay):
				}
			} else if backoffErrors > 0 {
				// Recovered from errors
				outageDuration := time.Since(firstErrorAt)
				e.logger.Info("Async projection recovered",
					"projection", worker.projection.Name(),
					"consecutive_errors", backoffErrors,
					"outage_duration", outageDuration,
				)
				poisonErrors = 0
				backoffErrors = 0
				worker.clearError()
			}
		}
	}
}

// processAsyncBatch processes a batch of events for an async projection.
func (e *ProjectionEngine) processAsyncBatch(ctx context.Context, worker *asyncProjectionWorker) (retErr error) {
	// Recover from panics in projection Apply/ApplyBatch handlers to prevent
	// a panicking projection from killing the async worker goroutine.
	var currentEvent *StoredEvent
	var batchRange string
	defer func() {
		if r := recover(); r != nil {
			switch {
			case currentEvent != nil:
				e.logger.Error("Async projection panicked",
					"projection", worker.projection.Name(),
					"event_type", currentEvent.Type,
					"stream_id", currentEvent.StreamID,
					"global_position", currentEvent.GlobalPosition,
					"panic", r,
				)
				retErr = fmt.Errorf("panic processing event %s (stream %s) at position %d: %v",
					currentEvent.Type, currentEvent.StreamID, currentEvent.GlobalPosition, r)
			case batchRange != "":
				e.logger.Error("Async projection panicked",
					"projection", worker.projection.Name(),
					"batch", batchRange,
					"panic", r,
				)
				retErr = fmt.Errorf("panic processing batch %s in projection %s: %v",
					batchRange, worker.projection.Name(), r)
			default:
				e.logger.Error("Async projection panicked",
					"projection", worker.projection.Name(),
					"panic", r,
				)
				retErr = fmt.Errorf("panic in projection %s: %v", worker.projection.Name(), r)
			}
		}
	}()

	// Apply an optional per-batch processing deadline so a single batch cannot
	// block the worker indefinitely.
	if worker.options.BatchTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, worker.options.BatchTimeout)
		defer cancel()
	}

	// Load events from current position
	// Note: This is a simplified implementation. In production, you'd want
	// to use the SubscriptionAdapter for better performance.
	events, err := e.loadEventsFromPosition(ctx, worker.lastPosition, worker.options.BatchSize)
	if err != nil {
		return fmt.Errorf("failed to load events: %w", err)
	}

	if len(events) == 0 {
		// No new events at this position; back off briefly to avoid tight polling loops.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
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
		// Update checkpoint and position even if no events were handled
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

	// Try batch processing first. Record the batch's position range for panic
	// diagnostics instead of mis-attributing a batch failure to its first event.
	start := time.Now()
	batchRange = fmt.Sprintf("%d-%d", filteredEvents[0].GlobalPosition, filteredEvents[len(filteredEvents)-1].GlobalPosition)
	err = worker.projection.ApplyBatch(ctx, filteredEvents)
	if errors.Is(err, ErrNotImplemented) {
		batchRange = ""
		// Fall back to sequential processing
		for i := range filteredEvents {
			event := filteredEvents[i]
			currentEvent = &filteredEvents[i]
			eventStart := time.Now()
			if err := worker.projection.Apply(ctx, event); err != nil {
				e.metrics.RecordEventProcessed(worker.projection.Name(), event.Type, time.Since(eventStart), false)
				worker.setFailedEvent(&filteredEvents[i])
				return fmt.Errorf("failed to apply event: %w", err)
			}
			e.metrics.RecordEventProcessed(worker.projection.Name(), event.Type, time.Since(eventStart), true)
			atomic.AddUint64(&worker.eventsProcessed, 1)
		}
	} else if err != nil {
		e.metrics.RecordBatchProcessed(worker.projection.Name(), len(filteredEvents), time.Since(start), false)
		// ApplyBatch is opaque about which event failed, so on a poison-skip we report
		// the last APPLIED (filtered) event — never an event the projection does not
		// handle — as the poison event. Any unhandled trailing events after it are
		// harmlessly re-loaded and re-filtered on the next cycle.
		worker.setFailedEvent(&filteredEvents[len(filteredEvents)-1])
		return err
	} else {
		e.metrics.RecordBatchProcessed(worker.projection.Name(), len(filteredEvents), time.Since(start), true)
		atomic.AddUint64(&worker.eventsProcessed, uint64(len(filteredEvents)))
	}
	worker.clearFailedEvent()

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
// Returns ErrSubscriptionNotSupported if the adapter does not implement SubscriptionAdapter.
func (e *ProjectionEngine) loadEventsFromPosition(ctx context.Context, fromPosition uint64, limit int) ([]StoredEvent, error) {
	events, err := e.store.LoadEventsFromPosition(ctx, fromPosition, limit)
	if err != nil {
		return nil, err
	}
	// Field encryption is transparent on read: decrypt each event's Data before it reaches a
	// projection, matching Load/LoadAggregate/DataExporter, via the shared batch primitive so
	// every pull surface decrypts identically. A hard (unhandled) decryption error surfaces
	// here — the cycle does not advance the checkpoint, so it retries rather than skipping
	// silently. No-op when encryption is unconfigured.
	return e.store.decryptStoredEvents(ctx, events)
}

// liveProjectionWorker manages a live projection's real-time processing.
type liveProjectionWorker struct {
	projection LiveProjection
	engine     *ProjectionEngine

	stopCh    chan struct{}
	closeOnce sync.Once
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
	old := w.state
	w.state = state
	w.stateMu.Unlock()
	if old != state {
		w.engine.notifyStateObserver(w.projection.Name(), old, state, nil)
	}
}

// runLiveWorker runs the background worker for a live projection.
func (e *ProjectionEngine) runLiveWorker(ctx context.Context, worker *liveProjectionWorker) {
	defer e.wg.Done()

	// Mark as running - eventCh is already created at registration
	worker.setState(ProjectionStateRunning)

	for {
		select {
		case <-e.stopCh:
			e.drainLiveWorker(ctx, worker)
			worker.setState(ProjectionStateStopped)
			return
		case <-worker.stopCh:
			e.drainLiveWorker(ctx, worker)
			worker.setState(ProjectionStateStopped)
			return
		case <-ctx.Done():
			worker.setState(ProjectionStateStopped)
			return
		case event := <-worker.eventCh:
			e.deliverLiveEvent(ctx, worker, event)
		}
	}
}

// deliverLiveEvent calls a live projection's OnEvent, recovering from panics.
func (e *ProjectionEngine) deliverLiveEvent(ctx context.Context, worker *liveProjectionWorker, event StoredEvent) {
	defer func() {
		if r := recover(); r != nil {
			e.logger.Error("Live projection panicked", "projection", worker.projection.Name(), "panic", r)
		}
	}()
	worker.projection.OnEvent(ctx, event)
}

// drainLiveWorker best-effort delivers any events still buffered in the worker's
// channel at graceful stop, so they are not silently dropped.
func (e *ProjectionEngine) drainLiveWorker(ctx context.Context, worker *liveProjectionWorker) {
	for {
		select {
		case event := <-worker.eventCh:
			e.deliverLiveEvent(ctx, worker, event)
		default:
			return
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
