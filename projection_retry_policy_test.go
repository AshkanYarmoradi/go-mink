package mink

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// retryProbeProjection counts every Apply invocation (including failed ones) and returns
// a configurable error, so a test can observe how many times the worker retried a failing
// event. Unlike testAsyncProjection.Events(), the Apply counter advances even when Apply
// returns an error.
type retryProbeProjection struct {
	AsyncProjectionBase
	applyCalls atomic.Int32
	mu         sync.Mutex
	err        error
}

func newRetryProbeProjection(name string, handled ...string) *retryProbeProjection {
	return &retryProbeProjection{AsyncProjectionBase: NewAsyncProjectionBase(name, handled...)}
}

func (p *retryProbeProjection) Apply(_ context.Context, _ StoredEvent) error {
	p.applyCalls.Add(1)
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

func (p *retryProbeProjection) setErr(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.err = err
}

func (p *retryProbeProjection) calls() int { return int(p.applyCalls.Load()) }

// --- Policy-level unit tests: the one convention (non-positive budget = retry forever) ---

func TestExponentialBackoffRetry_NonPositiveBudgetRetriesForever(t *testing.T) {
	tests := []struct {
		name       string
		maxRetries int
		attempt    int
		err        error
		want       bool
	}{
		// Regression pin: the footgun was ExponentialBackoffRetry(0, …) meaning "never
		// retry" (attempt >= 0 was always false). It now means "retry forever".
		{"zero budget retries at attempt 1", 0, 1, assert.AnError, true},
		{"zero budget retries at attempt 100", 0, 100, assert.AnError, true},
		{"negative budget retries at attempt 1", -1, 1, assert.AnError, true},
		{"negative budget retries at large attempt", -5, 1000, assert.AnError, true},
		{"non-positive budget still stops on nil error", 0, 1, nil, false},
		// A positive budget is unchanged: retry while attempt < maxRetries.
		{"positive budget retries below max", 3, 2, assert.AnError, true},
		{"positive budget gives up at max", 3, 3, assert.AnError, false},
		{"positive budget stops on nil error", 3, 1, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			policy := ExponentialBackoffRetry(tt.maxRetries, time.Millisecond, time.Second)
			assert.Equal(t, tt.want, policy.ShouldRetry(tt.attempt, tt.err))
		})
	}
}

func TestRetryForever(t *testing.T) {
	policy := RetryForever(10*time.Millisecond, time.Second)

	t.Run("never gives up on a non-nil error", func(t *testing.T) {
		for _, attempt := range []int{0, 1, 2, 50, 10000} {
			assert.True(t, policy.ShouldRetry(attempt, assert.AnError),
				"RetryForever must retry at attempt %d", attempt)
		}
	})

	t.Run("stops on a nil error", func(t *testing.T) {
		assert.False(t, policy.ShouldRetry(1, nil))
	})

	t.Run("backs off exponentially and caps at maxDelay", func(t *testing.T) {
		assert.Equal(t, 10*time.Millisecond, policy.Delay(0))
		assert.Equal(t, 20*time.Millisecond, policy.Delay(1))
		assert.Equal(t, time.Second, policy.Delay(100))
	})

	t.Run("is equivalent to ExponentialBackoffRetry(0, …)", func(t *testing.T) {
		equiv := ExponentialBackoffRetry(0, 10*time.Millisecond, time.Second)
		for _, attempt := range []int{1, 5, 42} {
			assert.Equal(t, equiv.ShouldRetry(attempt, assert.AnError), policy.ShouldRetry(attempt, assert.AnError))
		}
	})
}

func TestNoRetry_IsTheCanonicalNever(t *testing.T) {
	policy := NoRetry()
	for _, attempt := range []int{0, 1, 5} {
		assert.False(t, policy.ShouldRetry(attempt, assert.AnError),
			"NoRetry must never retry (attempt %d)", attempt)
	}
	assert.Equal(t, time.Duration(0), policy.Delay(3))
}

// --- Worker nil-policy path: MaxRetries carries the same convention ---

func TestAsyncWorker_ShouldRetry_NilPolicyMaxRetriesConvention(t *testing.T) {
	tests := []struct {
		name              string
		maxRetries        int
		consecutiveErrors int
		want              bool
	}{
		{"positive budget retries below max", 3, 2, true},
		{"positive budget gives up at max", 3, 3, false},
		{"zero budget retries forever", 0, 100, true},
		{"negative budget retries forever", -1, 100, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// A bare worker exercises the nil-RetryPolicy fallback path directly.
			w := &asyncProjectionWorker{options: AsyncOptions{MaxRetries: tt.maxRetries}}
			assert.Equal(t, tt.want, w.shouldRetry(tt.consecutiveErrors, assert.AnError))
		})
	}
}

func TestDefaultAsyncOptions_RetryUnchanged(t *testing.T) {
	opts := DefaultAsyncOptions()
	require.NotNil(t, opts.RetryPolicy)
	assert.Equal(t, 3, opts.MaxRetries)
	// The default policy still gives up at the 3rd consecutive error, exactly as before.
	assert.True(t, opts.RetryPolicy.ShouldRetry(1, assert.AnError))
	assert.True(t, opts.RetryPolicy.ShouldRetry(2, assert.AnError))
	assert.False(t, opts.RetryPolicy.ShouldRetry(3, assert.AnError))
}

// --- Engine-level regression pin for the footgun ---

func TestProjectionEngine_ExponentialBackoffRetryZero_RetriesForever(t *testing.T) {
	engine, store, _ := newTestEngineWithStore()
	appendTestEvents(t, store, 1)

	projection := newRetryProbeProjection("RetryForeverProbe", "ProjectionTestEvent")
	projection.setErr(assert.AnError) // persistent failure

	opts, poisonCalls := poisonCountingOpts()
	// Before the fix this meant "never retry" → give up on the first error. It must now keep retrying.
	opts.RetryPolicy = ExponentialBackoffRetry(0, 2*time.Millisecond, 5*time.Millisecond)
	require.NoError(t, engine.RegisterAsync(projection, opts))
	startEngine(t, engine)

	// The worker must retry the failing event many times rather than giving up at once.
	require.Eventually(t, func() bool {
		return projection.calls() >= 3
	}, 2*time.Second, 5*time.Millisecond, "ExponentialBackoffRetry(0, …) must retry forever, not give up on the first error")

	// Retry-forever never exhausts the budget, so the poison handler is never reached.
	assert.Equal(t, int32(0), poisonCalls.Load(), "an unlimited budget must never invoke OnPoisonEvent")
}
