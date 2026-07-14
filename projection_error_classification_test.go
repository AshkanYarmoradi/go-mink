package mink

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// temporaryErr implements the net.Error-style interface{ Temporary() bool }.
type temporaryErr struct{ temp bool }

func (e temporaryErr) Error() string   { return "temporary error" }
func (e temporaryErr) Temporary() bool { return e.temp }

// retryableErr implements the exported Retryable interface.
type retryableErr struct{ retry bool }

func (e retryableErr) Error() string   { return "retryable error" }
func (e retryableErr) Retryable() bool { return e.retry }

// scriptedProjection returns a scripted sequence of results across successive Apply calls
// (the worker re-applies the same failing event each cycle), then succeeds. It lets a test
// interleave transient and poison errors deterministically.
type scriptedProjection struct {
	AsyncProjectionBase
	mu        sync.Mutex
	behaviors []error // per-call result; a nil entry (or running past the end) succeeds
	idx       int
	applied   atomic.Int32
}

func newScriptedProjection(name string, behaviors []error, handled ...string) *scriptedProjection {
	return &scriptedProjection{
		AsyncProjectionBase: NewAsyncProjectionBase(name, handled...),
		behaviors:           behaviors,
	}
}

func (p *scriptedProjection) Apply(_ context.Context, _ StoredEvent) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.idx < len(p.behaviors) {
		err := p.behaviors[p.idx]
		p.idx++
		if err != nil {
			return err
		}
	}
	p.applied.Add(1)
	return nil
}

func (p *scriptedProjection) appliedCount() int { return int(p.applied.Load()) }

// --- DefaultErrorClassifier truth table ---

func TestDefaultErrorClassifier(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want ErrorClass
	}{
		{"nil is poison", nil, ErrorClassPoison},
		{"plain error is poison", errors.New("boom"), ErrorClassPoison},
		{"ErrTransient sentinel is transient", ErrTransient, ErrorClassTransient},
		{"wrapped ErrTransient is transient", fmt.Errorf("db down: %w", ErrTransient), ErrorClassTransient},
		{"Temporary()==true is transient", temporaryErr{temp: true}, ErrorClassTransient},
		{"Temporary()==false is poison", temporaryErr{temp: false}, ErrorClassPoison},
		{"wrapped Temporary()==true is transient", fmt.Errorf("wrap: %w", temporaryErr{temp: true}), ErrorClassTransient},
		{"Retryable()==true is transient", retryableErr{retry: true}, ErrorClassTransient},
		{"Retryable()==false is poison", retryableErr{retry: false}, ErrorClassPoison},
		{"wrapped Retryable()==true is transient", fmt.Errorf("wrap: %w", retryableErr{retry: true}), ErrorClassTransient},
		// The important guard: context.DeadlineExceeded reports Temporary()==true, but must
		// NOT be auto-classified transient (a hung poison event would retry forever).
		{"context.DeadlineExceeded is poison", context.DeadlineExceeded, ErrorClassPoison},
		{"wrapped context.DeadlineExceeded is poison", fmt.Errorf("batch timed out: %w", context.DeadlineExceeded), ErrorClassPoison},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, DefaultErrorClassifier(tt.err))
		})
	}
}

// --- Engine-level routing ---

func TestProjectionEngine_TransientError_RetriesPastBudgetWithoutFaulting(t *testing.T) {
	engine, store, _ := newTestEngineWithStore()
	metrics := newTestProjectionMetrics()
	engine.metrics = metrics
	ctx := context.Background()
	require.NoError(t, store.Append(ctx, "Order-tr-1", []interface{}{&ProjectionTestEvent{OrderID: "tr1"}}))

	projection := newRetryProbeProjection("TransientProbe", "ProjectionTestEvent")
	projection.setErr(fmt.Errorf("connection reset: %w", ErrTransient))

	var poisonCalls atomic.Int32
	opts := fastAsyncOpts()
	opts.ErrorClassifier = DefaultErrorClassifier
	opts.MaxRetries = 2 // tiny poison budget the transient error must NOT exhaust
	opts.RetryPolicy = ExponentialBackoffRetry(2, 2*time.Millisecond, 5*time.Millisecond)
	opts.OnPoisonEvent = func(_ context.Context, _ StoredEvent, _ error) error {
		poisonCalls.Add(1)
		return nil
	}
	require.NoError(t, engine.RegisterAsync(projection, opts))

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	require.NoError(t, engine.Start(runCtx))
	defer func() { _ = engine.Stop(context.Background()) }()

	// It keeps retrying well past MaxRetries (=2).
	require.Eventually(t, func() bool {
		return projection.calls() >= 5
	}, 2*time.Second, 5*time.Millisecond, "a transient error should be retried past the poison budget")

	// Never faulted, never handed to OnPoisonEvent — the poison budget is untouched.
	status, err := engine.GetStatus("TransientProbe")
	require.NoError(t, err)
	assert.NotEqual(t, ProjectionStateFaulted, status.State, "a transient error must not fault the worker")
	assert.Equal(t, int32(0), poisonCalls.Load(), "a transient error must never reach OnPoisonEvent")

	// Observability still fires: RecordError is called for the transient errors.
	metrics.mu.Lock()
	errorsRecorded := metrics.errorsRecorded
	metrics.mu.Unlock()
	assert.Positive(t, errorsRecorded, "RecordError must still fire for transient errors")
}

func TestProjectionEngine_PoisonError_ExhaustsBudget(t *testing.T) {
	ctx := context.Background()

	t.Run("skips via OnPoisonEvent when handler returns nil", func(t *testing.T) {
		engine, store, _ := newTestEngineWithStore()
		require.NoError(t, store.Append(ctx, "Order-po-1", []interface{}{&ProjectionTestEvent{OrderID: "po1"}}))

		projection := newRetryProbeProjection("PoisonSkipProbe", "ProjectionTestEvent")
		projection.setErr(errors.New("deterministic apply failure")) // poison: matches no transient signal

		var poisonCalls atomic.Int32
		opts := fastAsyncOpts()
		opts.ErrorClassifier = DefaultErrorClassifier
		opts.RetryPolicy = ExponentialBackoffRetry(2, 2*time.Millisecond, 5*time.Millisecond)
		opts.OnPoisonEvent = func(_ context.Context, _ StoredEvent, _ error) error {
			poisonCalls.Add(1)
			return nil // skip
		}
		require.NoError(t, engine.RegisterAsync(projection, opts))

		runCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		require.NoError(t, engine.Start(runCtx))
		defer func() { _ = engine.Stop(context.Background()) }()

		require.Eventually(t, func() bool {
			return poisonCalls.Load() >= 1
		}, 2*time.Second, 5*time.Millisecond, "a poison error must exhaust the budget and reach OnPoisonEvent")
	})

	t.Run("faults when no handler is set", func(t *testing.T) {
		engine, store, _ := newTestEngineWithStore()
		require.NoError(t, store.Append(ctx, "Order-po-2", []interface{}{&ProjectionTestEvent{OrderID: "po2"}}))

		projection := newRetryProbeProjection("PoisonFaultProbe", "ProjectionTestEvent")
		projection.setErr(errors.New("deterministic apply failure"))

		opts := fastAsyncOpts()
		opts.ErrorClassifier = DefaultErrorClassifier
		opts.RetryPolicy = ExponentialBackoffRetry(2, 2*time.Millisecond, 5*time.Millisecond)
		require.NoError(t, engine.RegisterAsync(projection, opts))

		runCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		require.NoError(t, engine.Start(runCtx))
		defer func() { _ = engine.Stop(context.Background()) }()

		require.Eventually(t, func() bool {
			status, err := engine.GetStatus("PoisonFaultProbe")
			return err == nil && status.State == ProjectionStateFaulted
		}, 2*time.Second, 5*time.Millisecond, "a poison error with no handler must fault the worker")
	})
}

func TestProjectionEngine_InterleavedTransientAndPoison_OnlyCountsPoison(t *testing.T) {
	engine, store, _ := newTestEngineWithStore()
	ctx := context.Background()
	require.NoError(t, store.Append(ctx, "Order-mix-1", []interface{}{&ProjectionTestEvent{OrderID: "mix1"}}))

	transient := fmt.Errorf("blip: %w", ErrTransient)
	poison := errors.New("deterministic failure")
	// Three transient errors (exceeding MaxRetries=2) then one poison, then success. If the
	// transient errors wrongly counted against the budget, the worker would have faulted or
	// skipped before ever reaching success.
	behaviors := []error{transient, transient, transient, poison}
	projection := newScriptedProjection("InterleavedProbe", behaviors, "ProjectionTestEvent")

	var poisonCalls atomic.Int32
	opts := fastAsyncOpts()
	opts.ErrorClassifier = DefaultErrorClassifier
	opts.MaxRetries = 2
	opts.RetryPolicy = ExponentialBackoffRetry(2, 2*time.Millisecond, 5*time.Millisecond)
	opts.OnPoisonEvent = func(_ context.Context, _ StoredEvent, _ error) error {
		poisonCalls.Add(1)
		return nil
	}
	require.NoError(t, engine.RegisterAsync(projection, opts))

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	require.NoError(t, engine.Start(runCtx))
	defer func() { _ = engine.Stop(context.Background()) }()

	// The event is eventually applied — only the single poison error advanced the budget
	// (to 1 < 2), so the worker never gave up.
	require.Eventually(t, func() bool {
		return projection.appliedCount() >= 1
	}, 2*time.Second, 5*time.Millisecond, "only poison errors should count toward the budget; the event must eventually apply")

	status, err := engine.GetStatus("InterleavedProbe")
	require.NoError(t, err)
	assert.NotEqual(t, ProjectionStateFaulted, status.State)
	assert.Equal(t, int32(0), poisonCalls.Load(), "the budget (2) was never exhausted by the single poison error")
}

func TestProjectionEngine_NilClassifier_ReproducesCurrentBehavior(t *testing.T) {
	engine, store, _ := newTestEngineWithStore()
	ctx := context.Background()
	require.NoError(t, store.Append(ctx, "Order-nc-1", []interface{}{&ProjectionTestEvent{OrderID: "nc1"}}))

	projection := newRetryProbeProjection("NilClassifierProbe", "ProjectionTestEvent")
	// Even an error that WOULD classify transient must be treated as poison with no classifier.
	projection.setErr(fmt.Errorf("would-be transient: %w", ErrTransient))

	var poisonCalls atomic.Int32
	opts := fastAsyncOpts()
	opts.ErrorClassifier = nil // the default
	opts.RetryPolicy = ExponentialBackoffRetry(2, 2*time.Millisecond, 5*time.Millisecond)
	opts.OnPoisonEvent = func(_ context.Context, _ StoredEvent, _ error) error {
		poisonCalls.Add(1)
		return nil
	}
	require.NoError(t, engine.RegisterAsync(projection, opts))

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	require.NoError(t, engine.Start(runCtx))
	defer func() { _ = engine.Stop(context.Background()) }()

	// With no classifier every error is poison, so the budget still exhausts and the event
	// reaches OnPoisonEvent — identical to the pre-classification behavior.
	require.Eventually(t, func() bool {
		return poisonCalls.Load() >= 1
	}, 2*time.Second, 5*time.Millisecond, "with no classifier every error must count as poison")
}
