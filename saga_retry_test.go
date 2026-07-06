package mink

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go-mink.dev/adapters"
	"go-mink.dev/adapters/memory"
)

// ====================================================================
// Re-drive test harness
// ====================================================================

// retryTestSaga drives commands keyed by event type so that multi-step re-drive
// scenarios can be expressed purely by the sequence of events delivered:
//   - "StepA" is an intermediate step: it dispatches "CmdA" and does NOT complete.
//   - "StepB" is the final step: it dispatches "CmdB" and then completes.
//
// The command outcomes (success/failure) are controlled by the command bus, not the
// saga, so a step can be made to fail and then fixed for the re-drive.
type retryTestSaga struct {
	SagaBase
	data          map[string]interface{}
	compensateErr error // when set, Compensate errors → saga settles Failed (not Compensated)
	complete      bool
}

func retrySagaFactory(compensateErr error) SagaFactory {
	return func(id string) Saga {
		return &retryTestSaga{
			SagaBase:      NewSagaBase(id, "RetrySaga"),
			compensateErr: compensateErr,
		}
	}
}

func (s *retryTestSaga) HandledEvents() []string { return []string{"StepA", "StepB"} }

func (s *retryTestSaga) HandleEvent(_ context.Context, event StoredEvent) ([]Command, error) {
	switch event.Type {
	case "StepA":
		return []Command{&testCommand{commandType: "CmdA", valid: true}}, nil
	case "StepB":
		s.complete = true
		return []Command{&testCommand{commandType: "CmdB", valid: true}}, nil
	}
	return nil, nil
}

func (s *retryTestSaga) Compensate(_ context.Context, _ int, _ error) ([]Command, error) {
	if s.compensateErr != nil {
		return nil, s.compensateErr
	}
	return nil, nil // no compensation commands → Compensated
}

func (s *retryTestSaga) IsComplete() bool                 { return s.complete }
func (s *retryTestSaga) Data() map[string]interface{}     { return s.data }
func (s *retryTestSaga) SetData(d map[string]interface{}) { s.data = d }

// retryHarness wires a SagaManager over a saga store with a command bus whose CmdA
// always succeeds and whose CmdB fails while cmdBFails is set. Both commands count
// their invocations so tests can assert exactly which steps were (re-)dispatched.
type retryHarness struct {
	manager   *SagaManager
	store     SagaStore
	cmdBFails atomic.Bool
	cmdACalls int32
	cmdBCalls int32
}

func newRetryHarness(t *testing.T, store SagaStore, startingEvent string, compensateErr error, opts ...SagaManagerOption) *retryHarness {
	t.Helper()
	h := &retryHarness{store: store}

	bus := NewCommandBus()
	bus.Register(&mockCommandHandler{cmdType: "CmdA", handleFunc: func(_ context.Context, _ Command) (CommandResult, error) {
		atomic.AddInt32(&h.cmdACalls, 1)
		return NewSuccessResult("", 0), nil
	}})
	bus.Register(&mockCommandHandler{cmdType: "CmdB", handleFunc: func(_ context.Context, _ Command) (CommandResult, error) {
		atomic.AddInt32(&h.cmdBCalls, 1)
		if h.cmdBFails.Load() {
			return NewErrorResult(errors.New("CmdB transient failure")), errors.New("CmdB transient failure")
		}
		return NewSuccessResult("", 0), nil
	}})

	base := []SagaManagerOption{
		WithSagaStore(store),
		WithCommandBus(bus),
		WithSagaRetryAttempts(1),
		WithSagaRetryDelay(time.Millisecond),
	}
	base = append(base, opts...)

	h.manager = NewSagaManager(New(memory.NewAdapter()), base...)
	h.manager.RegisterSimple("RetrySaga", retrySagaFactory(compensateErr), startingEvent)
	return h
}

func retryEvent(id, streamID, typ string, pos uint64) StoredEvent {
	return StoredEvent{ID: id, StreamID: streamID, Type: typ, Data: []byte(`{}`), GlobalPosition: pos}
}

// armableConflictStore wraps a SagaStore and returns a concurrency conflict on the
// next Save once armed, to simulate another writer committing between a re-drive's
// load and its save.
type armableConflictStore struct {
	SagaStore
	arm atomic.Bool
}

func (s *armableConflictStore) Save(ctx context.Context, state *SagaState) error {
	if s.arm.CompareAndSwap(true, false) {
		return adapters.ErrConcurrencyConflict
	}
	return s.SagaStore.Save(ctx, state)
}

// ====================================================================
// RetrySaga — core (Required)
// ====================================================================

func TestRetrySaga_ReDrivesFailedToCompleted(t *testing.T) {
	ctx := context.Background()
	// compensateErr forces the initial failure to settle as Failed (not Compensated).
	h := newRetryHarness(t, memory.NewSagaStore(), "StepB", errors.New("no compensation"))
	sagaID := "RetrySaga-order-1"

	// Initial delivery fails (CmdB down) and the saga settles Failed.
	h.cmdBFails.Store(true)
	require.NoError(t, h.manager.ProcessEvent(ctx, retryEvent("e1", "order-1", "StepB", 1)))

	failed, err := h.store.Load(ctx, sagaID)
	require.NoError(t, err)
	require.Equal(t, SagaStatusFailed, failed.Status, "precondition: saga should have settled Failed")

	// The transient cause is fixed; re-drive should carry the saga to Completed.
	h.cmdBFails.Store(false)
	require.NoError(t, h.manager.RetrySaga(ctx, sagaID))

	completed, err := h.store.Load(ctx, sagaID)
	require.NoError(t, err)
	assert.Equal(t, SagaStatusCompleted, completed.Status)
	assert.Empty(t, completed.FailureReason)
}

func TestRetrySaga_IsIdempotentAcrossSteps(t *testing.T) {
	ctx := context.Background()
	// Saga starts on StepA (an intermediate step), so StepB routes to the existing saga.
	h := newRetryHarness(t, memory.NewSagaStore(), "StepA", nil)
	sagaID := "RetrySaga-order-1"

	// Step A succeeds: CmdA dispatched once, saga stays Running.
	require.NoError(t, h.manager.ProcessEvent(ctx, retryEvent("evA", "order-1", "StepA", 1)))
	require.Equal(t, int32(1), atomic.LoadInt32(&h.cmdACalls))

	// Step B fails (CmdB down): saga settles Compensated. Its event is not recorded
	// as processed (it failed), while StepA's key remains.
	h.cmdBFails.Store(true)
	require.NoError(t, h.manager.ProcessEvent(ctx, retryEvent("evB", "order-1", "StepB", 2)))
	settled, err := h.store.Load(ctx, sagaID)
	require.NoError(t, err)
	require.Equal(t, SagaStatusCompensated, settled.Status)
	require.Contains(t, settled.ProcessedEvents, "evA:1", "StepA must remain recorded as processed")

	// Fix CmdB and re-drive: only StepB is re-delivered.
	h.cmdBFails.Store(false)
	require.NoError(t, h.manager.RetrySaga(ctx, sagaID))

	completed, err := h.store.Load(ctx, sagaID)
	require.NoError(t, err)
	assert.Equal(t, SagaStatusCompleted, completed.Status)

	// CmdA (step 0..N-1) is NOT re-dispatched by the re-drive — still exactly one call.
	assert.Equal(t, int32(1), atomic.LoadInt32(&h.cmdACalls), "already-succeeded step must not be re-dispatched")
	// StepA's idempotency key survives the re-drive; StepB is now recorded too.
	assert.Contains(t, completed.ProcessedEvents, "evA:1")
	assert.Contains(t, completed.ProcessedEvents, "evB:2")
}

func TestRetrySaga_RejectsNonRetryableStatuses(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name   string
		status SagaStatus
	}{
		{"completed", SagaStatusCompleted},
		{"started", SagaStatusStarted},
		{"running", SagaStatusRunning},
		{"compensating", SagaStatusCompensating},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newRetryHarness(t, memory.NewSagaStore(), "StepB", nil)
			sagaID := "RetrySaga-" + tt.name
			require.NoError(t, h.store.Save(ctx, &SagaState{
				ID: sagaID, Type: "RetrySaga", CorrelationID: tt.name,
				Status:    tt.status,
				StartedAt: time.Now(), UpdatedAt: time.Now(),
				Data: map[string]interface{}{reservedLastEventKey: retryEvent("e1", tt.name, "StepB", 1)},
			}))

			err := h.manager.RetrySaga(ctx, sagaID)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrSagaNotRetryable)

			var typed *SagaNotRetryableError
			require.ErrorAs(t, err, &typed)
			assert.Equal(t, tt.status, typed.Status)

			// The saga is untouched and no command was dispatched.
			after, loadErr := h.store.Load(ctx, sagaID)
			require.NoError(t, loadErr)
			assert.Equal(t, tt.status, after.Status)
			assert.Zero(t, atomic.LoadInt32(&h.cmdBCalls))
		})
	}
}

func TestRetrySaga_RejectsMissingTriggerEvent(t *testing.T) {
	ctx := context.Background()
	h := newRetryHarness(t, memory.NewSagaStore(), "StepB", nil)
	sagaID := "RetrySaga-legacy"

	// A retryable-status saga with no captured trigger event (e.g. created before
	// this capability existed): Data has no reserved key.
	require.NoError(t, h.store.Save(ctx, &SagaState{
		ID: sagaID, Type: "RetrySaga", CorrelationID: "legacy",
		Status:    SagaStatusFailed,
		StartedAt: time.Now(), UpdatedAt: time.Now(),
	}))

	err := h.manager.RetrySaga(ctx, sagaID)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSagaNotRetryable)

	var typed *SagaNotRetryableError
	require.ErrorAs(t, err, &typed)
	assert.Contains(t, typed.Reason, "trigger event", "reason should explain the missing trigger event")
}

func TestRetrySaga_MissingSagaReturnsNotFound(t *testing.T) {
	ctx := context.Background()
	h := newRetryHarness(t, memory.NewSagaStore(), "StepB", nil)

	err := h.manager.RetrySaga(ctx, "RetrySaga-does-not-exist")
	assert.ErrorIs(t, err, ErrSagaNotFound)
}

func TestRetrySaga_SurfacesConcurrencyConflict(t *testing.T) {
	ctx := context.Background()
	store := &armableConflictStore{SagaStore: memory.NewSagaStore()}
	h := newRetryHarness(t, store, "StepB", errors.New("no compensation"))
	sagaID := "RetrySaga-order-1"

	// Drive to Failed with the conflict store unarmed (setup saves succeed).
	h.cmdBFails.Store(true)
	require.NoError(t, h.manager.ProcessEvent(ctx, retryEvent("e1", "order-1", "StepB", 1)))
	require.False(t, store.arm.Load())

	// Arm a conflict on the re-drive's save; the command itself would succeed now.
	h.cmdBFails.Store(false)
	store.arm.Store(true)

	err := h.manager.RetrySaga(ctx, sagaID)
	assert.ErrorIs(t, err, ErrConcurrencyConflict, "a concurrent change must surface, not be swallowed")

	// The saga was not double-driven: it remains Failed in the store.
	after, loadErr := h.store.Load(ctx, sagaID)
	require.NoError(t, loadErr)
	assert.Equal(t, SagaStatusFailed, after.Status)
}

func TestRetrySaga_SerialisesWithEventLoop(t *testing.T) {
	ctx := context.Background()
	h := newRetryHarness(t, memory.NewSagaStore(), "StepB", errors.New("no compensation"))
	sagaID := "RetrySaga-order-1"

	h.cmdBFails.Store(true)
	require.NoError(t, h.manager.ProcessEvent(ctx, retryEvent("e1", "order-1", "StepB", 1)))
	h.cmdBFails.Store(false)

	// Hold the per-saga lock the event loop uses, then start a re-drive: it must
	// block until the lock is released, proving the two cannot interleave.
	lock := h.manager.getSagaLock(sagaID)
	lock.Lock()

	done := make(chan error, 1)
	go func() { done <- h.manager.RetrySaga(ctx, sagaID) }()

	select {
	case <-done:
		lock.Unlock()
		t.Fatal("RetrySaga must not proceed while the per-saga lock is held")
	case <-time.After(50 * time.Millisecond):
		// Still blocked on the lock — expected.
	}

	lock.Unlock()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("RetrySaga did not proceed after the lock was released")
	}

	completed, err := h.store.Load(ctx, sagaID)
	require.NoError(t, err)
	assert.Equal(t, SagaStatusCompleted, completed.Status)
}

func TestRetrySaga_IsObservable(t *testing.T) {
	t.Run("observer sees the attempt", func(t *testing.T) {
		ctx := context.Background()
		var mu sync.Mutex
		var seen []RetryEvent
		observer := func(ev RetryEvent) {
			mu.Lock()
			seen = append(seen, ev)
			mu.Unlock()
		}
		h := newRetryHarness(t, memory.NewSagaStore(), "StepB", errors.New("no compensation"),
			WithSagaRetryObserver(observer))
		sagaID := "RetrySaga-order-1"

		h.cmdBFails.Store(true)
		require.NoError(t, h.manager.ProcessEvent(ctx, retryEvent("e1", "order-1", "StepB", 1)))
		h.cmdBFails.Store(false)
		require.NoError(t, h.manager.RetrySaga(ctx, sagaID))

		mu.Lock()
		defer mu.Unlock()
		require.Len(t, seen, 1)
		assert.Equal(t, sagaID, seen[0].SagaID)
		assert.Equal(t, "RetrySaga", seen[0].SagaType)
		assert.Equal(t, SagaStatusFailed, seen[0].FromStatus)
		assert.NoError(t, seen[0].Err)
		assert.False(t, seen[0].At.IsZero())
	})

	t.Run("re-drive is recorded even without an observer", func(t *testing.T) {
		ctx := context.Background()
		h := newRetryHarness(t, memory.NewSagaStore(), "StepB", errors.New("no compensation"))
		sagaID := "RetrySaga-order-1"

		h.cmdBFails.Store(true)
		require.NoError(t, h.manager.ProcessEvent(ctx, retryEvent("e1", "order-1", "StepB", 1)))
		h.cmdBFails.Store(false)
		require.NoError(t, h.manager.RetrySaga(ctx, sagaID))

		completed, err := h.store.Load(ctx, sagaID)
		require.NoError(t, err)
		require.NotEmpty(t, completed.Steps, "a re-drive SagaStep must be recorded even with no observer")
		var found bool
		for _, step := range completed.Steps {
			if step.Name == "operator re-drive (from failed)" {
				found = true
			}
		}
		assert.True(t, found, "expected a re-drive step noting the from-status, got %+v", completed.Steps)
	})

	t.Run("observer sees a conflicted attempt", func(t *testing.T) {
		ctx := context.Background()
		var mu sync.Mutex
		var seen []RetryEvent
		store := &armableConflictStore{SagaStore: memory.NewSagaStore()}
		h := newRetryHarness(t, store, "StepB", errors.New("no compensation"),
			WithSagaRetryObserver(func(ev RetryEvent) {
				mu.Lock()
				seen = append(seen, ev)
				mu.Unlock()
			}))
		sagaID := "RetrySaga-order-1"

		h.cmdBFails.Store(true)
		require.NoError(t, h.manager.ProcessEvent(ctx, retryEvent("e1", "order-1", "StepB", 1)))
		h.cmdBFails.Store(false)
		store.arm.Store(true)

		err := h.manager.RetrySaga(ctx, sagaID)
		require.ErrorIs(t, err, ErrConcurrencyConflict)

		mu.Lock()
		defer mu.Unlock()
		require.Len(t, seen, 1)
		assert.ErrorIs(t, seen[0].Err, ErrConcurrencyConflict)
		assert.Equal(t, SagaStatusFailed, seen[0].FromStatus)
	})
}

// ====================================================================
// ResumeStalled + batch (Good-to-have)
// ====================================================================

func TestResumeStalled_ResumesStaleRunningSaga(t *testing.T) {
	ctx := context.Background()
	h := newRetryHarness(t, newMockSagaStore(), "StepB", nil, WithSagaTimeout(time.Minute))
	sagaID := "RetrySaga-order-1"

	// A Running saga last updated well beyond the timeout, with a captured event.
	stale := time.Now().Add(-2 * time.Minute)
	require.NoError(t, h.store.Save(ctx, &SagaState{
		ID: sagaID, Type: "RetrySaga", CorrelationID: "order-1",
		Status:    SagaStatusRunning,
		StartedAt: stale, UpdatedAt: stale,
		Data: map[string]interface{}{reservedLastEventKey: retryEvent("e1", "order-1", "StepB", 1)},
	}))

	require.NoError(t, h.manager.ResumeStalled(ctx, sagaID))

	completed, err := h.store.Load(ctx, sagaID)
	require.NoError(t, err)
	assert.Equal(t, SagaStatusCompleted, completed.Status)
}

func TestResumeStalled_RejectsRecentlyUpdatedRunningSaga(t *testing.T) {
	ctx := context.Background()
	h := newRetryHarness(t, newMockSagaStore(), "StepB", nil, WithSagaTimeout(time.Minute))
	sagaID := "RetrySaga-order-1"

	// A Running saga updated just now — must not be resumed (could race a live worker).
	require.NoError(t, h.store.Save(ctx, &SagaState{
		ID: sagaID, Type: "RetrySaga", CorrelationID: "order-1",
		Status:    SagaStatusRunning,
		StartedAt: time.Now(), UpdatedAt: time.Now(),
		Data: map[string]interface{}{reservedLastEventKey: retryEvent("e1", "order-1", "StepB", 1)},
	}))

	err := h.manager.ResumeStalled(ctx, sagaID)
	require.ErrorIs(t, err, ErrSagaNotRetryable)

	after, loadErr := h.store.Load(ctx, sagaID)
	require.NoError(t, loadErr)
	assert.Equal(t, SagaStatusRunning, after.Status)
	assert.Zero(t, atomic.LoadInt32(&h.cmdBCalls), "a fresh running saga must not be driven")
}

func TestResumeStalled_RejectsNonRunningSaga(t *testing.T) {
	ctx := context.Background()
	h := newRetryHarness(t, newMockSagaStore(), "StepB", nil, WithSagaTimeout(time.Minute))
	sagaID := "RetrySaga-order-1"

	require.NoError(t, h.store.Save(ctx, &SagaState{
		ID: sagaID, Type: "RetrySaga", CorrelationID: "order-1",
		Status:    SagaStatusFailed, // settled, not Running
		StartedAt: time.Now().Add(-2 * time.Minute), UpdatedAt: time.Now().Add(-2 * time.Minute),
		Data: map[string]interface{}{reservedLastEventKey: retryEvent("e1", "order-1", "StepB", 1)},
	}))

	err := h.manager.ResumeStalled(ctx, sagaID)
	assert.ErrorIs(t, err, ErrSagaNotRetryable)
}

func TestRetrySagasByType_ReportsPerSagaOutcomes(t *testing.T) {
	ctx := context.Background()
	store := memory.NewSagaStore()
	h := newRetryHarness(t, store, "StepB", errors.New("no compensation"))

	// Three Failed sagas whose captured event is StepB. CmdB is down, so saga "bad"
	// will fail again; "good1"/"good2" complete once CmdB recovers... but here we keep
	// CmdB down so every re-drive fails again — then flip for a mixed batch below.
	seed := func(id string) {
		require.NoError(t, store.Save(ctx, &SagaState{
			ID: "RetrySaga-" + id, Type: "RetrySaga", CorrelationID: id,
			Status:    SagaStatusFailed,
			StartedAt: time.Now(), UpdatedAt: time.Now(),
			Data: map[string]interface{}{reservedLastEventKey: retryEvent("e-"+id, id, "StepB", 1)},
		}))
	}
	seed("good1")
	seed("good2")

	h.cmdBFails.Store(false) // both should recover to Completed
	report, err := h.manager.RetrySagasByType(ctx, "RetrySaga", SagaStatusFailed)
	require.NoError(t, err)

	require.Len(t, report.Results, 2)
	assert.Equal(t, 2, report.Count(RetrySucceeded))
	assert.Equal(t, 0, report.Count(RetryFailedAgain))
	for _, res := range report.Results {
		assert.NoError(t, res.Err)
	}
}

func TestRetrySagasByType_ContinuesPastFailure(t *testing.T) {
	ctx := context.Background()
	store := &sagaLoadFailStore{SagaStore: memory.NewSagaStore()}
	h := newRetryHarness(t, store, "StepB", errors.New("no compensation"))

	seed := func(id string) {
		require.NoError(t, store.Save(ctx, &SagaState{
			ID: "RetrySaga-" + id, Type: "RetrySaga", CorrelationID: id,
			Status:    SagaStatusFailed,
			StartedAt: time.Now(), UpdatedAt: time.Now(),
			Data: map[string]interface{}{reservedLastEventKey: retryEvent("e-"+id, id, "StepB", 1)},
		}))
	}
	seed("a")
	seed("b")
	h.cmdBFails.Store(false)

	// Make the pre-check Load fail for saga "a" only; the batch must still attempt "b".
	store.failLoadFor = "RetrySaga-a"

	report, err := h.manager.RetrySagasByType(ctx, "RetrySaga", SagaStatusFailed)
	require.NoError(t, err)
	require.Len(t, report.Results, 2, "one saga failing must not abort the batch")

	var aFailed, bSucceeded bool
	for _, res := range report.Results {
		if res.SagaID == "RetrySaga-a" {
			aFailed = res.Outcome == RetryFailedAgain && res.Err != nil
		}
		if res.SagaID == "RetrySaga-b" {
			bSucceeded = res.Outcome == RetrySucceeded
		}
	}
	assert.True(t, aFailed, "saga a should be reported failed with its error")
	assert.True(t, bSucceeded, "saga b should still be attempted and succeed")
}

func TestRetrySagasByType_ReportsSkippedAlongsideSucceeded(t *testing.T) {
	ctx := context.Background()
	store := memory.NewSagaStore()
	h := newRetryHarness(t, store, "StepB", errors.New("no compensation"))

	// "ok" is a normal Failed saga with a captured trigger event.
	require.NoError(t, store.Save(ctx, &SagaState{
		ID: "RetrySaga-ok", Type: "RetrySaga", CorrelationID: "ok",
		Status: SagaStatusFailed, StartedAt: time.Now(), UpdatedAt: time.Now(),
		Data: map[string]interface{}{reservedLastEventKey: retryEvent("e-ok", "ok", "StepB", 1)},
	}))
	// "noevent" is Failed (so FindByType returns it) but has no captured trigger
	// event, so its re-drive is skipped — the batch must record that, not abort.
	require.NoError(t, store.Save(ctx, &SagaState{
		ID: "RetrySaga-noevent", Type: "RetrySaga", CorrelationID: "noevent",
		Status: SagaStatusFailed, StartedAt: time.Now(), UpdatedAt: time.Now(),
	}))

	h.cmdBFails.Store(false)
	report, err := h.manager.RetrySagasByType(ctx, "RetrySaga", SagaStatusFailed)
	require.NoError(t, err)
	require.Len(t, report.Results, 2)
	assert.Equal(t, 1, report.Count(RetrySucceeded))
	assert.Equal(t, 1, report.Count(RetrySkipped))

	for _, res := range report.Results {
		if res.SagaID == "RetrySaga-noevent" {
			assert.ErrorIs(t, res.Err, ErrSagaNotRetryable)
		}
	}
}

func TestRetrySagasByType_PropagatesFindError(t *testing.T) {
	ctx := context.Background()
	store := newMockSagaStore()
	store.findByTypeError = errors.New("find boom")
	h := newRetryHarness(t, store, "StepB", nil)

	_, err := h.manager.RetrySagasByType(ctx, "RetrySaga", SagaStatusFailed)
	assert.ErrorIs(t, err, store.findByTypeError)
}

// sagaLoadFailStore fails Load for one specific saga id, to exercise a per-saga
// error inside a batch without aborting it.
type sagaLoadFailStore struct {
	SagaStore
	failLoadFor string
}

func (s *sagaLoadFailStore) Load(ctx context.Context, sagaID string) (*SagaState, error) {
	if s.failLoadFor != "" && sagaID == s.failLoadFor {
		return nil, errors.New("injected load failure")
	}
	return s.SagaStore.Load(ctx, sagaID)
}

// ====================================================================
// IsRetryable predicate
// ====================================================================

func TestSagaStatus_IsRetryable(t *testing.T) {
	tests := []struct {
		status    SagaStatus
		retryable bool
	}{
		{SagaStatusStarted, false},
		{SagaStatusRunning, false},
		{SagaStatusCompleted, false},
		{SagaStatusFailed, true},
		{SagaStatusCompensating, false},
		{SagaStatusCompensated, true},
		{SagaStatusCompensationFailed, true},
	}
	for _, tt := range tests {
		t.Run(tt.status.String(), func(t *testing.T) {
			assert.Equal(t, tt.retryable, tt.status.IsRetryable())
			assert.Equal(t, tt.retryable, (&SagaState{Status: tt.status}).IsRetryable())
		})
	}
}

// ====================================================================
// Helpers: last-event decode, outcome labels, classification
// ====================================================================

func TestDecodeLastEvent(t *testing.T) {
	ev := retryEvent("e1", "s1", "StepB", 7)

	t.Run("StoredEvent value (in-memory store round-trip)", func(t *testing.T) {
		got, ok := decodeLastEvent(ev)
		require.True(t, ok)
		assert.Equal(t, ev, got)
	})

	t.Run("pointer to StoredEvent", func(t *testing.T) {
		got, ok := decodeLastEvent(&ev)
		require.True(t, ok)
		assert.Equal(t, ev, got)
	})

	t.Run("nil *StoredEvent is unusable", func(t *testing.T) {
		_, ok := decodeLastEvent((*StoredEvent)(nil))
		assert.False(t, ok)
	})

	t.Run("map round-trip (JSON-backed store e.g. PostgreSQL)", func(t *testing.T) {
		// Simulate how a JSONB column persists and reloads Data: the event becomes a
		// generic map. decodeLastEvent must faithfully reconstruct the StoredEvent,
		// including the []byte payload (base64 across the JSON round-trip).
		b, err := json.Marshal(ev)
		require.NoError(t, err)
		var asMap map[string]interface{}
		require.NoError(t, json.Unmarshal(b, &asMap))

		got, ok := decodeLastEvent(asMap)
		require.True(t, ok)
		assert.Equal(t, ev.ID, got.ID)
		assert.Equal(t, ev.Type, got.Type)
		assert.Equal(t, ev.StreamID, got.StreamID)
		assert.Equal(t, ev.GlobalPosition, got.GlobalPosition)
		assert.Equal(t, ev.Data, got.Data, "payload bytes must survive the JSON round-trip")
	})

	t.Run("empty map decodes to an unusable event", func(t *testing.T) {
		_, ok := decodeLastEvent(map[string]interface{}{})
		assert.False(t, ok)
	})

	t.Run("unmarshalable value is unusable", func(t *testing.T) {
		_, ok := decodeLastEvent(make(chan int)) // channels cannot be JSON-marshaled
		assert.False(t, ok)
	})
}

func TestStampLastEvent_SkipsZeroEvent(t *testing.T) {
	m := NewSagaManager(New(memory.NewAdapter()))
	saga := &retryTestSaga{SagaBase: NewSagaBase("s1", "RetrySaga")}

	// A zero-value event carries nothing worth capturing and must be skipped.
	m.stampLastEvent(saga, StoredEvent{})
	assert.NotContains(t, saga.Data(), reservedLastEventKey)

	// A real event is captured under the reserved key.
	ev := retryEvent("e1", "s1", "StepB", 1)
	m.stampLastEvent(saga, ev)
	require.Contains(t, saga.Data(), reservedLastEventKey)
	assert.Equal(t, ev, saga.Data()[reservedLastEventKey])
}

func TestRetryOutcome_String(t *testing.T) {
	assert.Equal(t, "succeeded", RetrySucceeded.String())
	assert.Equal(t, "failed_again", RetryFailedAgain.String())
	assert.Equal(t, "conflicted", RetryConflicted.String())
	assert.Equal(t, "skipped", RetrySkipped.String())
	assert.Equal(t, "unknown", RetryOutcome(99).String())
}

func TestClassifyRetried(t *testing.T) {
	ctx := context.Background()
	store := newMockSagaStore()
	m := NewSagaManager(New(memory.NewAdapter()), WithSagaStore(store))

	require.NoError(t, store.Save(ctx, &SagaState{ID: "s-done", Type: "T", Status: SagaStatusCompleted, StartedAt: time.Now()}))
	assert.Equal(t, RetrySucceeded, m.classifyRetried(ctx, "s-done"))

	require.NoError(t, store.Save(ctx, &SagaState{ID: "s-fail", Type: "T", Status: SagaStatusFailed, StartedAt: time.Now()}))
	assert.Equal(t, RetryFailedAgain, m.classifyRetried(ctx, "s-fail"))

	// If the post-drive status can't be read, the re-drive returned no error, so it
	// is treated as applied rather than inventing a failure.
	store.loadError = errors.New("boom")
	assert.Equal(t, RetrySucceeded, m.classifyRetried(ctx, "s-done"))
}

func TestResumeStalled_RequiresConfiguredTimeout(t *testing.T) {
	ctx := context.Background()
	// No WithSagaTimeout configured — ResumeStalled cannot judge staleness.
	h := newRetryHarness(t, newMockSagaStore(), "StepB", nil)
	sagaID := "RetrySaga-order-1"

	old := time.Now().Add(-time.Hour)
	require.NoError(t, h.store.Save(ctx, &SagaState{
		ID: sagaID, Type: "RetrySaga", CorrelationID: "order-1",
		Status:    SagaStatusRunning,
		StartedAt: old, UpdatedAt: old,
		Data: map[string]interface{}{reservedLastEventKey: retryEvent("e1", "order-1", "StepB", 1)},
	}))

	err := h.manager.ResumeStalled(ctx, sagaID)
	require.ErrorIs(t, err, ErrSagaNotRetryable)
	var typed *SagaNotRetryableError
	require.ErrorAs(t, err, &typed)
	assert.Contains(t, typed.Reason, "timeout")
}
