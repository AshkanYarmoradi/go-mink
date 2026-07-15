package mink

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go-mink.dev/adapters/memory"
)

// supervisedProjection records every applied event position and fails (poison) on a
// configured position until healed, so a test can drive a fault, observe restarts, then
// let the worker recover and assert it resumed from its checkpoint (never from 0).
type supervisedProjection struct {
	AsyncProjectionBase
	mu       sync.Mutex
	applied  []uint64
	failPos  uint64 // fail while event.GlobalPosition == failPos and not healed; 0 = never fail
	healed   bool
	faultErr error
}

func newSupervisedProjection(name string, failPos uint64, faultErr error, handled ...string) *supervisedProjection {
	return &supervisedProjection{
		AsyncProjectionBase: NewAsyncProjectionBase(name, handled...),
		failPos:             failPos,
		faultErr:            faultErr,
	}
}

func (p *supervisedProjection) Apply(_ context.Context, event StoredEvent) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failPos != 0 && event.GlobalPosition == p.failPos && !p.healed {
		return p.faultErr
	}
	p.applied = append(p.applied, event.GlobalPosition)
	return nil
}

func (p *supervisedProjection) heal() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healed = true
}

func (p *supervisedProjection) appliedPositions() []uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]uint64, len(p.applied))
	copy(out, p.applied)
	return out
}

// stateRecorder is a thread-safe WithProjectionStateObserver sink.
type stateRecorder struct {
	mu          sync.Mutex
	transitions []recordedTransition
}

type recordedTransition struct {
	name     string
	oldState ProjectionState
	newState ProjectionState
	errMsg   string
}

func newStateRecorder() *stateRecorder { return &stateRecorder{} }

func (r *stateRecorder) record(name string, oldState, newState ProjectionState, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	r.transitions = append(r.transitions, recordedTransition{name, oldState, newState, msg})
}

func (r *stateRecorder) countInto(name string, state ProjectionState) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, t := range r.transitions {
		if t.name == name && t.newState == state {
			n++
		}
	}
	return n
}

// sawFaultError reports whether any transition into Faulted for name carried an error.
func (r *stateRecorder) sawFaultError(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, t := range r.transitions {
		if t.name == name && t.newState == ProjectionStateFaulted && t.errMsg != "" {
			return true
		}
	}
	return false
}

// orderedNewStates returns the destination state of each transition for name, in order.
func (r *stateRecorder) orderedNewStates(name string) []ProjectionState {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []ProjectionState
	for _, t := range r.transitions {
		if t.name == name {
			out = append(out, t.newState)
		}
	}
	return out
}

// containsSubsequence reports whether want appears as an ordered (not necessarily
// contiguous) subsequence of states.
func containsSubsequence(states, want []ProjectionState) bool {
	i := 0
	for _, s := range states {
		if i < len(want) && s == want[i] {
			i++
		}
	}
	return i == len(want)
}

// newObservedEngine wires an engine with an in-memory store (ProjectionTestEvent
// registered), a fresh checkpoint store, and the given state observer.
func newObservedEngine(rec *stateRecorder) (*ProjectionEngine, *EventStore) {
	adapter := memory.NewAdapter()
	store := New(adapter)
	store.RegisterEvents(&ProjectionTestEvent{})
	engine := NewProjectionEngine(store,
		WithCheckpointStore(newTestCheckpointStore()),
		WithProjectionStateObserver(rec.record),
	)
	return engine, store
}

// appendTestEvents appends n single-event streams, yielding global positions 1..n.
func appendTestEvents(t *testing.T, store *EventStore, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		require.NoError(t, store.Append(context.Background(),
			"Order-sup-"+string(rune('a'+i)),
			[]interface{}{&ProjectionTestEvent{OrderID: "sup"}}))
	}
}

func supervisionOpts() AsyncOptions {
	opts := fastAsyncOpts()
	opts.BatchSize = 1 // one event per checkpoint, so a fault lands on a known position
	// Fault fast: the first poison error exhausts the budget.
	opts.RetryPolicy = ExponentialBackoffRetry(1, time.Millisecond, 5*time.Millisecond)
	opts.MaxRetries = 1
	return opts
}

// --- Restart resumes from checkpoint, never from 0 ---

func TestProjectionEngine_Supervision_RestartResumesFromCheckpoint(t *testing.T) {
	rec := newStateRecorder()
	engine, store := newObservedEngine(rec)
	appendTestEvents(t, store, 3) // positions 1, 2, 3

	// Fails on position 2 until healed. StartFromBeginning is true, but a restart must
	// still resume from the checkpoint (1), never reprocessing position 1.
	projection := newSupervisedProjection("ResumeFromCheckpoint", 2, assert.AnError, "ProjectionTestEvent")
	opts := supervisionOpts()
	opts.RestartPolicy = RestartForever(2*time.Millisecond, 10*time.Millisecond)
	require.NoError(t, engine.RegisterAsync(projection, opts))

	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, engine.Start(runCtx))
	defer func() { _ = engine.Stop(context.Background()) }()

	// Wait until it has faulted and been restarted at least once.
	require.Eventually(t, func() bool {
		return rec.countInto("ResumeFromCheckpoint", ProjectionStateRestarting) >= 1
	}, 2*time.Second, 5*time.Millisecond, "worker should fault and enter Restarting")

	// Heal: the next restart resumes from checkpoint 1 and finishes positions 2 and 3.
	projection.heal()

	require.Eventually(t, func() bool {
		return len(projection.appliedPositions()) == 3
	}, 2*time.Second, 5*time.Millisecond, "worker should resume and apply all three events after healing")

	// Never reprocessed from 0: position 1 was applied exactly once despite the restarts.
	positions := projection.appliedPositions()
	assert.Equal(t, []uint64{1, 2, 3}, positions, "must resume from the checkpoint, not replay from 0")
}

// --- Bounded restart policy eventually stays Faulted ---

func TestProjectionEngine_Supervision_BoundedRestartStaysFaulted(t *testing.T) {
	rec := newStateRecorder()
	engine, store := newObservedEngine(rec)
	appendTestEvents(t, store, 1) // position 1

	// Never heals: every run faults.
	projection := newSupervisedProjection("BoundedRestart", 1, assert.AnError, "ProjectionTestEvent")
	opts := supervisionOpts()
	opts.RestartPolicy = ExponentialBackoffRestart(3, 2*time.Millisecond, 10*time.Millisecond)
	require.NoError(t, engine.RegisterAsync(projection, opts))

	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, engine.Start(runCtx))
	defer func() { _ = engine.Stop(context.Background()) }()

	// Exactly 3 restarts, then terminal Faulted.
	require.Eventually(t, func() bool {
		return rec.countInto("BoundedRestart", ProjectionStateRestarting) == 3
	}, 2*time.Second, 5*time.Millisecond, "a bounded policy should restart exactly maxRestarts times")

	// It settles into Faulted and does not restart again.
	require.Eventually(t, func() bool {
		status, err := engine.GetStatus("BoundedRestart")
		return err == nil && status.State == ProjectionStateFaulted
	}, 2*time.Second, 5*time.Millisecond, "after exhausting restarts the worker stays Faulted")

	// Give it a moment to prove no further restart fires.
	time.Sleep(60 * time.Millisecond)
	assert.Equal(t, 3, rec.countInto("BoundedRestart", ProjectionStateRestarting), "must not restart beyond the bound")
	status, err := engine.GetStatus("BoundedRestart")
	require.NoError(t, err)
	assert.Equal(t, ProjectionStateFaulted, status.State)
}

// --- RestartForever keeps restarting ---

func TestProjectionEngine_Supervision_RestartForeverKeepsRestarting(t *testing.T) {
	rec := newStateRecorder()
	engine, store := newObservedEngine(rec)
	appendTestEvents(t, store, 1)

	projection := newSupervisedProjection("RestartForever", 1, assert.AnError, "ProjectionTestEvent")
	opts := supervisionOpts()
	opts.RestartPolicy = RestartForever(2*time.Millisecond, 10*time.Millisecond)
	require.NoError(t, engine.RegisterAsync(projection, opts))

	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, engine.Start(runCtx))
	defer func() { _ = engine.Stop(context.Background()) }()

	// An unlimited policy keeps restarting well past any finite bound.
	require.Eventually(t, func() bool {
		return rec.countInto("RestartForever", ProjectionStateRestarting) >= 6
	}, 3*time.Second, 5*time.Millisecond, "RestartForever should keep restarting with backoff")
}

// --- Manual Restart primitive ---

func TestProjectionEngine_Restart(t *testing.T) {
	t.Run("relaunches a Faulted worker from its checkpoint", func(t *testing.T) {
		rec := newStateRecorder()
		engine, store := newObservedEngine(rec)
		appendTestEvents(t, store, 1)

		// No RestartPolicy: the fault is terminal until a manual Restart.
		projection := newSupervisedProjection("ManualRestart", 1, assert.AnError, "ProjectionTestEvent")
		require.NoError(t, engine.RegisterAsync(projection, supervisionOpts()))

		runCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		require.NoError(t, engine.Start(runCtx))
		defer func() { _ = engine.Stop(context.Background()) }()

		require.Eventually(t, func() bool {
			status, err := engine.GetStatus("ManualRestart")
			return err == nil && status.State == ProjectionStateFaulted
		}, 2*time.Second, 5*time.Millisecond, "worker should fault terminally with no RestartPolicy")

		// Fix the underlying condition, then manually restart.
		projection.heal()
		require.NoError(t, engine.Restart(runCtx, "ManualRestart"))

		require.Eventually(t, func() bool {
			return len(projection.appliedPositions()) == 1
		}, 2*time.Second, 5*time.Millisecond, "a manual Restart should relaunch and resume the worker")
		assert.Equal(t, []uint64{1}, projection.appliedPositions())
	})

	t.Run("is an idempotent no-op on a healthy worker", func(t *testing.T) {
		engine, store, _ := newTestEngineWithStore()
		appendTestEvents(t, store, 1)

		projection := newTestAsyncProjection("HealthyRestart", "ProjectionTestEvent")
		require.NoError(t, engine.RegisterAsync(projection, fastAsyncOpts()))

		runCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		require.NoError(t, engine.Start(runCtx))
		defer func() { _ = engine.Stop(context.Background()) }()

		require.Eventually(t, func() bool {
			status, err := engine.GetStatus("HealthyRestart")
			return err == nil && status.State == ProjectionStateRunning
		}, 2*time.Second, 5*time.Millisecond)

		// Restart on a Running worker is a no-op returning nil.
		require.NoError(t, engine.Restart(runCtx, "HealthyRestart"))
		status, err := engine.GetStatus("HealthyRestart")
		require.NoError(t, err)
		assert.Equal(t, ProjectionStateRunning, status.State)
	})

	t.Run("errors on an unknown projection", func(t *testing.T) {
		engine, _, _ := newTestEngineWithStore()
		err := engine.Restart(context.Background(), "does-not-exist")
		assert.ErrorIs(t, err, ErrProjectionNotFound)
	})

	t.Run("concurrent Restart relaunches at most one worker", func(t *testing.T) {
		engine, store, _ := newTestEngineWithStore()
		appendTestEvents(t, store, 1)

		projection := newSupervisedProjection("ConcurrentRestart", 1, assert.AnError, "ProjectionTestEvent")
		require.NoError(t, engine.RegisterAsync(projection, supervisionOpts()))

		runCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		require.NoError(t, engine.Start(runCtx))
		defer func() { _ = engine.Stop(context.Background()) }()

		require.Eventually(t, func() bool {
			status, err := engine.GetStatus("ConcurrentRestart")
			return err == nil && status.State == ProjectionStateFaulted
		}, 2*time.Second, 5*time.Millisecond)

		// Heal, then fire a burst of concurrent Restarts. The per-worker guard must ensure
		// exactly one supervisor relaunches, so the event applies exactly once.
		projection.heal()
		var wg sync.WaitGroup
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_ = engine.Restart(runCtx, "ConcurrentRestart")
			}()
		}
		wg.Wait()

		require.Eventually(t, func() bool {
			return len(projection.appliedPositions()) == 1
		}, 2*time.Second, 5*time.Millisecond, "the worker should recover")
		// Give any erroneous second supervisor a chance to double-apply before asserting.
		time.Sleep(60 * time.Millisecond)
		assert.Equal(t, []uint64{1}, projection.appliedPositions(), "at most one supervisor may run, so no duplicate apply")
	})
}

// --- State observer ---

func TestProjectionEngine_Supervision_ObserverSeesFaultAndRecovery(t *testing.T) {
	rec := newStateRecorder()
	engine, store := newObservedEngine(rec)
	appendTestEvents(t, store, 1)

	projection := newSupervisedProjection("ObservedRecovery", 1, errors.New("simulated fault"), "ProjectionTestEvent")
	opts := supervisionOpts()
	opts.RestartPolicy = RestartForever(2*time.Millisecond, 10*time.Millisecond)
	require.NoError(t, engine.RegisterAsync(projection, opts))

	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, engine.Start(runCtx))
	defer func() { _ = engine.Stop(context.Background()) }()

	require.Eventually(t, func() bool {
		return rec.countInto("ObservedRecovery", ProjectionStateRestarting) >= 1
	}, 2*time.Second, 5*time.Millisecond, "observer should see a Restarting transition")

	projection.heal()
	require.Eventually(t, func() bool {
		return len(projection.appliedPositions()) == 1
	}, 2*time.Second, 5*time.Millisecond, "worker should recover after healing")

	// The fault transition carried the fault error.
	assert.True(t, rec.sawFaultError("ObservedRecovery"), "the Faulted transition must carry the fault error")

	// The observer saw the Faulted → Restarting → (CatchingUp/Running) recovery arc.
	states := rec.orderedNewStates("ObservedRecovery")
	assert.True(t,
		containsSubsequence(states, []ProjectionState{
			ProjectionStateFaulted, ProjectionStateRestarting, ProjectionStateRunning,
		}),
		"observer should see Faulted → Restarting → Running, got %v", states)
}

func TestProjectionEngine_Supervision_ObserverCanQueryEngineWithoutDeadlock(t *testing.T) {
	adapter := memory.NewAdapter()
	store := New(adapter)
	store.RegisterEvents(&ProjectionTestEvent{})

	var engine *ProjectionEngine
	var queried atomic.Bool
	observer := func(name string, _, _ ProjectionState, _ error) {
		// Re-enter the engine from within the observer; must not deadlock because the
		// transition is published after the worker's state lock is released.
		if _, err := engine.GetStatus(name); err == nil {
			queried.Store(true)
		}
	}
	engine = NewProjectionEngine(store,
		WithCheckpointStore(newTestCheckpointStore()),
		WithProjectionStateObserver(observer),
	)

	require.NoError(t, engine.RegisterAsync(newTestAsyncProjection("ObserverQuery", "ProjectionTestEvent"), fastAsyncOpts()))

	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, engine.Start(runCtx))
	defer func() { _ = engine.Stop(context.Background()) }()

	require.Eventually(t, queried.Load, 2*time.Second, 5*time.Millisecond,
		"observer must be able to call GetStatus without deadlock")
}

// --- A persistent checkpoint-read fault is restartable and self-heals ---

func TestProjectionEngine_Supervision_CheckpointReadFaultIsRestartable(t *testing.T) {
	adapter := memory.NewAdapter()
	store := New(adapter)
	store.RegisterEvents(&ProjectionTestEvent{})
	checkpoint := newTestCheckpointStore()
	checkpoint.getErr = errors.New("checkpoint store unavailable") // persistent read failure
	rec := newStateRecorder()
	engine := NewProjectionEngine(store,
		WithCheckpointStore(checkpoint),
		WithProjectionStateObserver(rec.record),
	)

	require.NoError(t, store.Append(context.Background(), "Order-cpf-1",
		[]interface{}{&ProjectionTestEvent{OrderID: "cpf"}}))

	// StartFromBeginning is false, so the worker must read the checkpoint on boot — which
	// fails. It must fault (never default its position to 0) and, being a fault, be
	// restartable.
	projection := newSupervisedProjection("CheckpointFault", 0, nil, "ProjectionTestEvent")
	opts := DefaultAsyncOptions()
	opts.PollInterval = 20 * time.Millisecond
	opts.StartFromBeginning = false
	opts.RestartPolicy = RestartForever(2*time.Millisecond, 10*time.Millisecond)
	require.NoError(t, engine.RegisterAsync(projection, opts))

	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, engine.Start(runCtx))
	defer func() { _ = engine.Stop(context.Background()) }()

	// It faults on the checkpoint read and enters the restart cycle.
	require.Eventually(t, func() bool {
		return rec.countInto("CheckpointFault", ProjectionStateRestarting) >= 1
	}, 3*time.Second, 10*time.Millisecond, "a persistent checkpoint-read failure should fault and be restarted")

	// It never defaulted the position to 0, so nothing was processed during the outage.
	assert.Empty(t, projection.appliedPositions(), "must not process from 0 while the checkpoint is unreadable")

	// Heal the checkpoint store: the next restart reads it and the worker self-recovers.
	checkpoint.mu.Lock()
	checkpoint.getErr = nil
	checkpoint.mu.Unlock()

	require.Eventually(t, func() bool {
		return len(projection.appliedPositions()) >= 1
	}, 3*time.Second, 10*time.Millisecond, "a checkpoint-store outage that heals should self-recover")
}

// --- Composition with Rebuild ---

func TestProjectionEngine_Supervision_ComposesWithRebuild(t *testing.T) {
	engine, store, _ := newTestEngineWithStore()
	ctx := context.Background()
	for i := 0; i < 20; i++ {
		require.NoError(t, store.Append(ctx, "Order-sr-"+string(rune('A'+i)),
			[]interface{}{&ProjectionTestEvent{OrderID: "sr"}}))
	}

	// raceProneProjection (projection_test.go) has an unguarded counter, so -race flags any
	// concurrent Apply between a supervised worker and a Rebuild.
	proj := &raceProneProjection{AsyncProjectionBase: NewAsyncProjectionBase("SupervisedRebuild", "ProjectionTestEvent")}
	opts := fastAsyncOpts()
	opts.RestartPolicy = RestartForever(2*time.Millisecond, 10*time.Millisecond)
	require.NoError(t, engine.RegisterAsync(proj, opts))

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	require.NoError(t, engine.Start(runCtx))

	time.Sleep(40 * time.Millisecond) // let the worker get busy
	// A Rebuild holds processingMu; the supervised loop honors it, so Apply never runs
	// concurrently (would be caught by -race on proj.count).
	require.NoError(t, engine.Rebuild(ctx, "SupervisedRebuild"))

	_ = engine.Stop(context.Background()) // joins workers before we read count
	assert.Greater(t, proj.count, 0)
}

// --- Graceful stop while mid-backoff ---

func TestProjectionEngine_Supervision_StopJoinsWorkerMidBackoff(t *testing.T) {
	rec := newStateRecorder()
	engine, store := newObservedEngine(rec)
	appendTestEvents(t, store, 1)

	projection := newSupervisedProjection("StopMidBackoff", 1, assert.AnError, "ProjectionTestEvent")
	opts := supervisionOpts()
	// A long backoff parks the worker in the restart window, so Stop must interrupt it.
	opts.RestartPolicy = RestartForever(10*time.Second, 10*time.Second)
	require.NoError(t, engine.RegisterAsync(projection, opts))

	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, engine.Start(runCtx))

	// Wait until the worker is parked in its (10s) restart backoff.
	require.Eventually(t, func() bool {
		return rec.countInto("StopMidBackoff", ProjectionStateRestarting) >= 1
	}, 2*time.Second, 5*time.Millisecond, "worker should enter the restart backoff window")

	// Stop must return promptly — the backoff wait unblocks on the stop signal rather than
	// blocking for the full 10s.
	start := time.Now()
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer stopCancel()
	require.NoError(t, engine.Stop(stopCtx))
	assert.Less(t, time.Since(start), 3*time.Second, "Stop should interrupt the restart backoff, not wait it out")

	status, err := engine.GetStatus("StopMidBackoff")
	require.NoError(t, err)
	assert.Equal(t, ProjectionStateStopped, status.State)
}

// --- Engine reuse: Start after Stop must relaunch workers ---

func TestProjectionEngine_StartStopStart_Reuse(t *testing.T) {
	engine, store, _ := newTestEngineWithStore()
	require.NoError(t, store.Append(context.Background(), "Order-reuse-1",
		[]interface{}{&ProjectionTestEvent{OrderID: "reuse"}}))

	projection := newTestAsyncProjection("ReuseProj", "ProjectionTestEvent")
	require.NoError(t, engine.RegisterAsync(projection, fastAsyncOpts()))

	// The engine must be reusable: each Start after a Stop must relaunch the worker (reach
	// Running). Reaching Running proves the supervisor goroutine was launched — it pins that
	// the per-worker supervising guard is fully reset by the time Stop's join returns,
	// otherwise a back-to-back Stop→Start could lose the CAS and silently never relaunch.
	for cycle := 0; cycle < 5; cycle++ {
		require.NoError(t, engine.Start(context.Background()))
		require.Eventually(t, func() bool {
			status, err := engine.GetStatus("ReuseProj")
			return err == nil && status.State == ProjectionStateRunning
		}, 2*time.Second, 5*time.Millisecond, "worker should relaunch and reach Running on cycle %d", cycle)
		require.NoError(t, engine.Stop(context.Background()))
	}

	// And the reused worker is functional, not merely Running: a final run (left going long
	// enough to tick a batch) processes the event.
	require.NoError(t, engine.Start(context.Background()))
	defer func() { _ = engine.Stop(context.Background()) }()
	require.Eventually(t, func() bool {
		return len(projection.Events()) >= 1
	}, 2*time.Second, 5*time.Millisecond, "the reused worker should process events")
}
