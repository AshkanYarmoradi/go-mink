# Design — projection retry & fault supervision

## Context

All three gaps live in the **async** projection worker in `projection_engine.go`. The
relevant current code (go-mink `develop`; line numbers approximate):

- **`shouldRetry`** decides whether to keep retrying after N consecutive errors:

  ```go
  func (w *asyncProjectionWorker) shouldRetry(consecutiveErrors int, err error) bool {
      if w.options.RetryPolicy != nil {
          return w.options.RetryPolicy.ShouldRetry(consecutiveErrors, err)
      }
      if w.options.MaxRetries > 0 {
          return consecutiveErrors < w.options.MaxRetries
      }
      return true // non-positive MaxRetries = retry forever — ONLY on the nil-policy path
  }
  ```

- **`exponentialBackoffRetry.ShouldRetry`** — the built-in policy the default uses:

  ```go
  func (r *exponentialBackoffRetry) ShouldRetry(attempt int, err error) bool {
      if err == nil { return false }
      return attempt < r.maxRetries   // ExponentialBackoffRetry(0,…) ⇒ attempt(≥1) < 0 ⇒ never
  }
  ```

  `shouldRetry` is called with `consecutiveErrors`, which starts at `1` after the first
  failure. So `ExponentialBackoffRetry(3,…)` retries after failures 1 and 2 and gives up at
  3 (3 attempts / 2 retries). `DefaultAsyncOptions()` sets `RetryPolicy:
  ExponentialBackoffRetry(3,…)` **and** `MaxRetries: 3`; because `RetryPolicy != nil`,
  `MaxRetries` is ignored (as its doc comment says). This is fine but underlines that the
  *policy* path is the one that governs in practice — and it is the one carrying the
  opposite `<=0` convention.

- **The run loop** (`runAsyncWorker`) increments a single `consecutiveErrors` that drives
  **both** the poison budget (`shouldRetry`) and the backoff delay, then on exhaustion:

  ```go
  if !worker.shouldRetry(consecutiveErrors, err) {
      if e.handlePoisonEvent(ctx, worker, err) { consecutiveErrors = 0; worker.clearError(); continue }
      e.logger.Error("Async projection giving up after exhausting retries", …)
      worker.setError(err) // remain Faulted
      return               // goroutine exits permanently (defer e.wg.Done())
  }
  ```

- **`setError`** sets `state = ProjectionStateFaulted`; **`setState`** just writes
  `w.state` under `w.stateMu`. Neither pushes a notification. The engine already has a
  `getCheckpointWithRetry` precedent (bounded transient retry of the *checkpoint read* at
  startup) and a package-level `isShutdownError(err)` (`context.Canceled ||
  context.DeadlineExceeded`) — both reusable.

The design reuses these seams and adds no new dependency.

## Goals / Non-Goals

**Goals.** (1) One coherent retry-count convention across every `RetryPolicy` and
`MaxRetries`. (2) A way to retry transient infrastructure errors without ever spending the
poison budget. (3) Optional self-healing of a Faulted worker from its checkpoint, plus a
push signal on fault. (4) Every new knob zero-overhead and behavior-neutral when unset; the
one behavior change (Gap 1) narrowly scoped and documented.

**Non-Goals.** Restart-from-zero (never — that is `Rebuild`); automatic classification with
no classifier; extending or breaking the `RetryPolicy`/`ProjectionMetrics` interfaces; any
inline/live-projection change; a general circuit-breaker/DLQ framework; any DB-schema
change.

## Decisions

### D1 — Retry-count convention: `maxRetries <= 0` means "retry indefinitely" (Gap 1, Required)

Make the built-in policy obey the convention `AsyncOptions.MaxRetries` already documents:

```go
func (r *exponentialBackoffRetry) ShouldRetry(attempt int, err error) bool {
    if err == nil {
        return false
    }
    if r.maxRetries <= 0 { // non-positive budget = retry indefinitely (matches MaxRetries)
        return true
    }
    return attempt < r.maxRetries
}
```

And add an explicit, self-documenting policy so no one has to encode intent in the `0`
sentinel:

```go
// RetryForever returns a RetryPolicy that retries on every non-nil error, without limit,
// with exponential backoff between attempts capped at maxDelay. It is the readable spelling
// of ExponentialBackoffRetry(0, baseDelay, maxDelay).
func RetryForever(baseDelay, maxDelay time.Duration) RetryPolicy {
    return ExponentialBackoffRetry(0, baseDelay, maxDelay)
}
```

`NoRetry()` stays the one canonical "never retry." `ExponentialBackoffRetry` and the
`RetryPolicy` interface keep their signatures. The private `shouldRetry` is unchanged — once
the policy path honors `<=0`, both branches agree, so `ExponentialBackoffRetry(0,…)`,
`RetryForever(…)`, and a nil policy with `MaxRetries <= 0` all mean the same thing.

**Alternatives considered.**
- *Reject `maxRetries <= 0` at construction* (validate/panic/error). Rejected: an
  event-sourcing library must not panic on a recoverable misconfiguration (the same
  principle that made `mink.New` *record* the binary-serializer/JSONB mismatch instead of
  panicking), and `ExponentialBackoffRetry` returns `RetryPolicy` with **no** error, so
  rejecting would force either a panic or a breaking signature change to `(RetryPolicy,
  error)`. Neither is acceptable, and "reject 0" also throws away the genuinely useful
  "retry forever" meaning.
- *Only add `RetryForever`, leave `ExponentialBackoffRetry(0,…)` = never.* Rejected: it
  leaves the trap armed — the two `<=0` conventions still disagree, and the incident-causing
  call still silently means "give up on first error." The convergence is the fix; the named
  policy is the ergonomic sugar on top.
- *Change the `RetryPolicy` interface* (e.g. add `IsUnlimited()`), rejected as gratuitous
  surface; the count convention carries the meaning fine.

**Back-compat / behavior-change analysis (the one deliberate break).**
| Caller | Today | After | Impact |
|---|---|---|---|
| `DefaultAsyncOptions()` (`ExponentialBackoffRetry(3,…)`) | give up at 3 | give up at 3 | **none** |
| `ExponentialBackoffRetry(n>0,…)` | give up at n | give up at n | **none** |
| nil policy, `MaxRetries > 0` | give up at n | give up at n | **none** |
| nil policy, `MaxRetries <= 0` | retry forever | retry forever | **none** (already this) |
| `ExponentialBackoffRetry(0,…)` / negative | **never retry** | **retry forever** | **CHANGED** |

Blast radius is exactly the last row: callers who explicitly pass a non-positive count to
`ExponentialBackoffRetry`. That call today means "specify backoff delays, then never use
them" — almost certainly a latent bug (anyone wanting never-retry uses `NoRetry()`), and it
is the shape that caused the incident. The only theoretical regression is a caller relying
on `ExponentialBackoffRetry(0,…)` as a synonym for `NoRetry()`; the migration note tells
them to switch to `NoRetry()`. Shipped as a `Changed` (not `Fixed`) entry with that note,
because it *is* a semantic change even though it converges two conventions.

### D2 — Transient-vs-poison classification via an opt-in `ErrorClassifier` (Gap 2, Required)

Add a classifier that the worker consults per error; a **transient** classification retries
with backoff but does **not** spend the poison budget.

```go
// ErrorClass classifies a projection Apply/ApplyBatch error for retry accounting.
type ErrorClass int

const (
    // ErrorClassPoison is the zero value: the error counts against the poison retry budget
    // (MaxRetries / RetryPolicy) and, on exhaustion, reaches OnPoisonEvent — today's behavior.
    ErrorClassPoison ErrorClass = iota
    // ErrorClassTransient marks a retryable infrastructure error: retried with backoff,
    // independently of the poison budget, so it never trips OnPoisonEvent or Faulted.
    ErrorClassTransient
)

// ErrTransient marks an error as a transient/retryable infrastructure failure. A projection's
// Apply may wrap an infra error with it (fmt.Errorf("...: %w", mink.ErrTransient)) to request
// budget-independent retry; DefaultErrorClassifier honors it via errors.Is.
var ErrTransient = errors.New("mink: transient error")

// Retryable is honored by DefaultErrorClassifier: an error implementing it and returning
// true classifies as ErrorClassTransient.
type Retryable interface{ Retryable() bool }

// DefaultErrorClassifier classifies err as ErrorClassTransient when err, or any error in its
// Unwrap chain, (a) satisfies errors.Is(err, ErrTransient), (b) implements Retryable() == true,
// or (c) implements interface{ Temporary() bool } == true (the net.Error idiom). Otherwise it
// returns ErrorClassPoison. Exported so callers can wrap or extend it.
func DefaultErrorClassifier(err error) ErrorClass
```

Wire it as an `AsyncOptions` **field** (not a `WithX`), because `AsyncOptions` is a struct
and its sibling resilience knobs — `RetryPolicy`, `MaxRetries`, `OnPoisonEvent` — are all
struct fields; matching the local shape beats importing the engine-level `WithX` idiom here:

```go
type AsyncOptions struct {
    // ...existing fields...

    // ErrorClassifier optionally classifies a processing error as transient or poison.
    // A transient error is retried with backoff but does not consume the poison budget, so
    // it never reaches OnPoisonEvent or faults the worker. nil (default) classifies every
    // error as poison — identical to prior behavior, zero overhead.
    ErrorClassifier func(error) ErrorClass
}
```

**Interaction with the budget and backoff.** Split the single counter into two:

- `poisonErrors` — incremented only for `ErrorClassPoison`; drives `shouldRetry` /
  `OnPoisonEvent` / fault (exactly today's `consecutiveErrors` role).
- `backoffErrors` — incremented for **any** error; drives only the backoff delay, so a
  transient outage still backs off and caps at `maxDelay`.

Sketch:

```go
class := ErrorClassPoison
if worker.options.ErrorClassifier != nil {
    class = worker.options.ErrorClassifier(err)
}
backoffErrors++
if class == ErrorClassPoison {
    poisonErrors++
    if !worker.shouldRetry(poisonErrors, err) {
        // OnPoisonEvent skip, else Faulted — unchanged
    }
}
// transient: fall through to the interruptible backoff wait; never fault
// success: reset both counters (existing "recovered" path)
```

With no classifier, `class` is always poison ⇒ `poisonErrors == backoffErrors` ⇒ the loop is
arithmetically identical to today. Transient errors are retried **unbounded** by default
(they self-heal when infra returns; escalation is D3's observer/supervision, not a fault),
and the existing power-of-2 log throttle and `RecordError` metric still fire on every error
regardless of class.

**Alternatives considered.**
- *Honor `interface{ Temporary() bool }` / a `Retryable` interface only (no classifier
  option).* Rejected as the sole mechanism because a consumer's transient errors (e.g. a
  pgx `connection reset by peer`, SQLSTATE `08xxx`) frequently satisfy **neither**
  interface; a caller-supplied `ErrorClassifier` is the escape hatch. So we ship **both**:
  the interfaces + sentinel as the batteries-included `DefaultErrorClassifier`, and the
  function option as the fully general hook.
- *An `ErrTransient` sentinel only.* Rejected alone: it requires the projection author to
  wrap every infra error, which they cannot always do (the error originates in the store's
  load path, not their `Apply`). Kept as one input to `DefaultErrorClassifier`.
- *A separate finite `MaxTransientRetries`/`TransientRetryPolicy`.* Deferred (Open
  Question). Unbounded transient retry with observability is the safer default: a bounded
  transient budget just reintroduces "fault on a long-but-recoverable outage," which is the
  bug. If wanted, it composes later as an additive field.

### D3 — Faulted-worker supervision + state observer (Gap 3, Good-to-have)

**D3a — restart with checkpoint-safe resume.** Refactor the worker body into a
supervising outer loop. The current `runAsyncWorker` body becomes `runAsyncWorkerOnce(ctx,
worker) exitReason` (returning `exitStopped` or `exitFaulted`); a new
`superviseAsyncWorker` wraps it and is what `Start`/`Restart` launch under the wait group:

```go
type RestartPolicy interface {
    ShouldRestart(restartCount int, lastErr error) bool
    Delay(restartCount int) time.Duration
}

// maxRestarts <= 0 = unlimited (consistent with the D1 retry convention).
func ExponentialBackoffRestart(maxRestarts int, baseDelay, maxDelay time.Duration) RestartPolicy
func RestartForever(baseDelay, maxDelay time.Duration) RestartPolicy

type AsyncOptions struct {
    // ...existing fields...

    // RestartPolicy optionally restarts a Faulted worker with backoff, resuming from its
    // checkpoint. nil (default) leaves a fault terminal — the worker goroutine exits, exactly
    // as today.
    RestartPolicy RestartPolicy
}

func (e *ProjectionEngine) superviseAsyncWorker(ctx context.Context, worker *asyncProjectionWorker) {
    defer e.wg.Done()
    restarts := 0
    for {
        reason := e.runAsyncWorkerOnce(ctx, worker, restarts > 0) // resumeFromCheckpoint on restart
        if reason != exitFaulted || worker.options.RestartPolicy == nil {
            return // clean stop, or no supervisor → terminal (today's behavior)
        }
        restarts++
        if !worker.options.RestartPolicy.ShouldRestart(restarts, worker.lastError) {
            return // policy gave up → remain Faulted (terminal)
        }
        worker.setState(ProjectionStateRestarting)
        select {
        case <-e.stopCh:      worker.setState(ProjectionStateStopped); return
        case <-worker.stopCh: worker.setState(ProjectionStateStopped); return
        case <-ctx.Done():    worker.setState(ProjectionStateStopped); return
        case <-time.After(worker.options.RestartPolicy.Delay(restarts)):
        }
    }
}
```

**Checkpoint safety (the core invariant).** `runAsyncWorkerOnce` already reads the
checkpoint at entry via `getCheckpointWithRetry` unless `StartFromBeginning`. The added
`resumeFromCheckpoint` argument forces the checkpoint read on **every restart**, so
`StartFromBeginning` is honored only on the *first* boot (when there may be no checkpoint) —
a restart therefore **never** reprocesses from 0. A persistent checkpoint-read fault is
still surfaced as a fault (never defaulted to 0, preserving the existing
`projection-engine` guarantee) and is itself restartable, so a checkpoint-store outage that
heals self-recovers. At-least-once redelivery of the last un-checkpointed batch on restart
is the same contract async projections already carry (handlers are idempotent).

**A new additive state** makes the backoff window observable:

```go
// ProjectionStateRestarting indicates a Faulted worker is waiting to be restarted by its
// RestartPolicy. Additive; existing states are unchanged.
ProjectionStateRestarting ProjectionState = "restarting"
```

**Manual operator recovery**, symmetric with `Pause`/`Resume`/`Rebuild`:

```go
// Restart relaunches a Faulted async worker from its checkpoint. It is idempotent: a no-op
// (nil) if the worker is not Faulted, and ErrProjectionNotFound for an unknown name. Guarded
// so concurrent calls relaunch at most one goroutine.
func (e *ProjectionEngine) Restart(ctx context.Context, name string) error
```

**Composition with `Rebuild`.** The supervised loop honors `paused` and `processingMu`
exactly as the normal tick does, so a concurrent `Rebuild` (which swaps `paused`, holds
`processingMu`, sets `Rebuilding`, then resumes from the rebuilt checkpoint) composes with
no special-casing: a restart cannot apply concurrently with a rebuild, and after a rebuild
the worker resumes from the rebuilt checkpoint. `Rebuild` remains the only path that resets
position; supervision only ever *resumes*.

**D3b — push-based state observer.** An engine-level callback (state alerting is naturally
one cross-projection sink) fired on every transition:

```go
// WithProjectionStateObserver registers a callback invoked on every async/live projection
// state transition (old → new, with the fault error when entering Faulted). Use it to alert
// on Faulted / Restarting and on recovery. nil (default) ⇒ no callback, zero overhead.
func WithProjectionStateObserver(
    fn func(name string, old, new ProjectionState, err error),
) ProjectionEngineOption
```

`setState`/`setError` capture `old → new` **under** `stateMu`, release the lock, **then**
invoke the observer — so an observer that calls back into `GetStatus` cannot deadlock or
re-enter under the lock.

**Alternatives considered.**
- *Extend `ProjectionMetrics` with `RecordStateChange`.* Rejected: it breaks every existing
  `ProjectionMetrics` implementer (incl. the metrics middleware and `noopProjectionMetrics`)
  — an interface change. The additive observer callback delivers push alerting without a
  break; the metrics middleware can adopt the observer in a follow-up.
- *Per-projection `OnFault func(name, err)` in `AsyncOptions`.* Rejected as the primary:
  narrower (fault-only, per-projection wiring) than a single engine-level observer that also
  reports `Restarting` and recovery. `RestartPolicy` stays per-projection (it is
  per-projection behavior); the observer stays engine-level (it is one alerting sink).
- *Engine-level `WithProjectionSupervisor(RestartPolicy)` default for all async
  projections.* A reasonable convenience; noted as a possible additive follow-up.
  Per-`AsyncOptions.RestartPolicy` is the minimal surface and lands next to the other
  resilience knobs.

## Risks / Trade-offs

- **Gap-1 behavior change.** Mitigated by the blast-radius table (only explicit
  non-positive `ExponentialBackoffRetry`), a `Changed` CHANGELOG entry, a migration note
  (use `NoRetry()` for never-retry), and a test pinning the new `<=0` semantics.
- **Unbounded transient retry can mask a persistent "transient-looking" failure.** Mitigated
  by unchanged `RecordError` metrics, the power-of-2 error logs, and D3b's observer/`Faulted`
  visibility; the Open Question tracks whether an optional transient cap is wanted.
- **Misclassification.** A poison event wrapped (wrongly) as transient would retry forever
  instead of hitting `OnPoisonEvent`. Mitigated by the poison-default (zero value), a tight
  `DefaultErrorClassifier` that only trusts explicit signals, and docs steering the
  classifier at infrastructure errors, not `Apply` logic errors.
- **Restart churn / thundering herd.** A worker that faults immediately on restart could
  hot-loop; mitigated by mandatory backoff in `RestartPolicy.Delay` and an optional finite
  `maxRestarts`. `RestartForever` still backs off and caps at `maxDelay`.
- **Supervisor vs. shutdown races.** The backoff wait and every once-loop selects on
  `e.stopCh` / `worker.stopCh` / `ctx.Done()`, and `superviseAsyncWorker` owns the single
  `wg.Done()`, so `Stop()` still joins cleanly.

## Migration Plan

1. Land D1–D3 additively; only the D1 `<=0` semantics change existing behavior.
2. **Migration note (Gap 1):** callers who passed `ExponentialBackoffRetry(0,…)` (or a
   negative count) expecting *never retry* must switch to `NoRetry()`; callers who wanted
   *retry forever* can drop the workaround and use `RetryForever(base, max)` (or keep the now
   correctly-behaving `ExponentialBackoffRetry(0,…)`). Everyone else is unaffected.
3. Adopt classification opt-in: set `AsyncOptions.ErrorClassifier = mink.DefaultErrorClassifier`
   (or a custom one matching your driver's transient codes).
4. Adopt supervision opt-in: set `AsyncOptions.RestartPolicy = mink.ExponentialBackoffRestart(0,
   time.Second, time.Minute)` and register `WithProjectionStateObserver(...)` to alert.
5. No data migration, no schema change, no checkpoint format change.

## Open Questions

1. **Optional transient cap.** Should there be a `MaxTransientRetries` /
   `TransientRetryPolicy` so a never-healing "transient" error eventually faults (and is then
   picked up by `RestartPolicy`)? Default proposed: unbounded transient retry + observer.
2. **`context.DeadlineExceeded` classification.** Should `DefaultErrorClassifier` treat a
   `BatchTimeout`-derived deadline as transient? Proposed **no** by default (a genuinely hung
   poison event would then retry forever); callers who want it classify explicitly.
3. **Engine-level supervisor default.** Ship `WithProjectionSupervisor(RestartPolicy)` as a
   convenience applying to all async projections, or keep restart strictly per-`AsyncOptions`?
   Proposed: per-`AsyncOptions` now; engine default as a later additive follow-up.
4. **Metrics adoption.** Route the new state transitions into `middleware/metrics` via the
   observer in this change, or as a separate follow-up? Proposed: follow-up (keeps this change
   free of a metrics-package edit).
