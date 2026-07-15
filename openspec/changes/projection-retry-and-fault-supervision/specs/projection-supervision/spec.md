## ADDED Requirements

### Requirement: A Faulted worker can be restarted with backoff, resuming from its checkpoint

The async projection worker SHALL support an optional `AsyncOptions.RestartPolicy`
(interface `RestartPolicy { ShouldRestart(restartCount int, lastErr error) bool;
Delay(restartCount int) time.Duration }`, with constructors
`ExponentialBackoffRestart(maxRestarts, baseDelay, maxDelay)` where `maxRestarts <= 0` means
unlimited, and `RestartForever(baseDelay, maxDelay)`). When a worker exits into `Faulted`
and a `RestartPolicy` is configured and permits restart, the engine SHALL wait the policy's
backoff (interruptible by engine stop, worker stop, or context cancellation) and relaunch
the worker instead of leaving its goroutine exited. A restart MUST resume from the worker's
persisted checkpoint and MUST NOT reprocess from position 0: `StartFromBeginning` SHALL be
honored only on the first boot (when no checkpoint exists), never on a restart. When no
`RestartPolicy` is configured, a fault SHALL remain terminal exactly as today (the goroutine
exits). The engine's graceful `Stop` SHALL still join all workers, including one waiting in
restart backoff.

#### Scenario: A transient-caused fault self-heals

- **WHEN** a worker faults (e.g. after a poison-budget exhaustion caused by an outage) and `RestartPolicy: RestartForever(...)` is set
- **THEN** the engine waits the backoff and relaunches the worker, which resumes from its last checkpoint and continues once the underlying condition clears — no process restart required

#### Scenario: A restart never reprocesses from zero

- **WHEN** a worker with a checkpoint at position N is restarted, even if `StartFromBeginning: true` was set for its first boot
- **THEN** it resumes from the checkpoint (position N), not from 0 — reprocessing history is exclusively the job of `Rebuild`

#### Scenario: A bounded restart policy eventually stays Faulted

- **WHEN** `RestartPolicy: ExponentialBackoffRestart(3, base, max)` is set and the worker faults on every attempt
- **THEN** the engine restarts it up to 3 times with growing backoff and then leaves it `Faulted` (terminal), rather than hot-looping forever

#### Scenario: No restart policy keeps today's terminal fault

- **WHEN** a worker faults and `AsyncOptions.RestartPolicy` is nil (the default)
- **THEN** the worker stays `Faulted` and its goroutine exits, exactly as before — zero behavior change and zero overhead when unconfigured

#### Scenario: A persistent checkpoint-read failure is faulted, not restarted from zero

- **WHEN** a restart re-reads the checkpoint and the checkpoint store is persistently failing
- **THEN** the worker faults again (never defaulting its position to 0), and — being a fault — is itself eligible for a further backed-off restart, so a checkpoint-store outage that later heals self-recovers

#### Scenario: Stop joins a worker waiting to restart

- **WHEN** the engine is stopped while a worker is in its restart backoff window
- **THEN** the backoff wait unblocks on the stop signal, the worker transitions to `Stopped`, and `Stop` returns without hanging

### Requirement: A manual Restart primitive recovers a Faulted worker

The engine SHALL expose `func (e *ProjectionEngine) Restart(ctx context.Context, name string)
error` that relaunches a `Faulted` async worker from its checkpoint, symmetric with
`Pause`/`Resume`/`Rebuild`. It SHALL be idempotent — a no-op returning nil when the named
worker is not `Faulted` — and SHALL return `ErrProjectionNotFound` for an unknown name.
Concurrent `Restart` calls MUST relaunch at most one worker goroutine.

#### Scenario: Restart relaunches a Faulted worker from its checkpoint

- **WHEN** `Restart(ctx, name)` is called on a `Faulted` worker
- **THEN** the worker is relaunched, resumes from its checkpoint, and transitions back toward `Running` (never reprocessing from 0)

#### Scenario: Restart is idempotent on a healthy worker

- **WHEN** `Restart(ctx, name)` is called on a worker that is `Running`, `Paused`, or `Rebuilding`
- **THEN** it is a no-op returning nil, and no second goroutine is launched

#### Scenario: Restart on an unknown projection errors

- **WHEN** `Restart(ctx, name)` names a projection that is not registered
- **THEN** it returns `ErrProjectionNotFound`

#### Scenario: Restart composes with Rebuild

- **WHEN** a `Rebuild` is in progress (worker paused, holding its processing lock) and a restart occurs
- **THEN** the restarted worker does not apply concurrently with the rebuild and resumes from the rebuilt checkpoint after the rebuild completes

### Requirement: State transitions are observable via a push callback

The engine SHALL support `func WithProjectionStateObserver(fn func(name string, old, new
ProjectionState, err error)) ProjectionEngineOption`, invoked on every async/live projection
state transition, carrying the fault error when entering `Faulted`. The observer MUST be
invoked outside the worker's state lock so it can safely call back into the engine (e.g.
`GetStatus`) without deadlock or reentrancy. When no observer is registered there SHALL be no
callback and zero overhead. A new additive `ProjectionStateRestarting` state SHALL mark the
window between a fault and a policy-driven restart. This callback SHALL NOT change or extend
the `ProjectionMetrics` interface.

#### Scenario: Entering Faulted fires an alertable callback

- **WHEN** a worker transitions to `Faulted` and an observer is registered
- **THEN** the observer is called with `(name, old, ProjectionStateFaulted, err)` where `err` is the fault cause — so operators are pushed the fault instead of polling `GetStatus`

#### Scenario: Restart and recovery are observable

- **WHEN** a worker faults and is then restarted and recovers
- **THEN** the observer sees the `Faulted → Restarting → CatchingUp/Running` transitions, distinguishing a self-healed recovery from a permanent fault

#### Scenario: The observer can safely query engine state

- **WHEN** the registered observer calls `GetStatus(name)` from within the callback
- **THEN** it does not deadlock, because the transition is published after the state lock is released

#### Scenario: No observer is zero-overhead

- **WHEN** no `WithProjectionStateObserver` is configured (the default)
- **THEN** state transitions perform no callback work and allocate nothing extra, and the `ProjectionMetrics` interface is unchanged so existing metrics implementers keep compiling
