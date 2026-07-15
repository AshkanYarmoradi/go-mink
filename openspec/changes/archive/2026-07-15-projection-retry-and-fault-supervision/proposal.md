## Why

A downstream consumer (a production event-sourced service) lost an async projection to a
**single transient database error** (`connection reset by peer`). The projection went to
`Faulted`, its worker goroutine exited, and the read model silently stopped advancing —
until a full process restart hours later. No alert fired, because nothing pushes on fault;
the only signal was a `GetStatus` nobody was polling. Root-cause analysis found **three
independent library-level gaps** in go-mink's async projection engine, which compound into
"silent permanent death":

1. **The retry-count convention is inconsistent — a footgun.** The worker's
   `shouldRetry` (`projection_engine.go`) treats a **non-positive** budget as *retry
   forever* on the `MaxRetries` path (`if w.options.MaxRetries > 0 { … }; return true`),
   and `AsyncOptions.MaxRetries` is documented that way. But the built-in
   `exponentialBackoffRetry.ShouldRetry(attempt, err)` is `err != nil && attempt <
   r.maxRetries` — so `ExponentialBackoffRetry(0, …)` means **never retry** (attempt `1 <
   0` is false), the *opposite* convention. A caller who writes `ExponentialBackoffRetry(0,
   base, max)` intending "retry forever with backoff" silently gets "give up on the first
   error." That zeroes the retry budget, so the very first transient blip exhausts it.

2. **No transient-vs-poison classification.** The engine counts **every** error against
   one budget. A transient infrastructure error (connection reset, failover, deadline) is
   indistinguishable from a genuine *poison event* whose `Apply()` deterministically fails.
   So a DB failover that outlasts a finite budget (three quick backoff attempts inside a
   few seconds, versus a 30 s failover) exhausts the *poison* budget over an event that was
   never poison — and with no `OnPoisonEvent` skip handler set, the worker faults. There is
   no hook to say "this error is retryable infrastructure — retry it independently of the
   poison budget."

3. **No self-healing of a Faulted worker.** On budget exhaustion with no `OnPoisonEvent`
   skip, the run loop logs "giving up after exhausting retries", calls `setError` (state →
   `Faulted`), and `return`s — the goroutine exits **permanently** (`defer e.wg.Done()`).
   Nothing restarts it; recovery needs a full process restart or a manual `Rebuild`. There
   is a `ProjectionStatus` surface and an `e.metrics.RecordError` hook, but **no restart
   supervisor and no push callback on fault** — so the fault is invisible until someone
   looks.

Each gap is real on its own; together they turn a recoverable, seconds-long infrastructure
blip into an unbounded, unalerted outage of a read model. This change closes all three
while preserving go-mink's invariants — append-only event store, zero overhead when the new
knobs are unconfigured, no forced DB-schema change, options-shaped configuration, and typed
sentinel errors with `Is`/`Unwrap`.

## What Changes

### Required

- **Consistent retry-count convention** (`projection-retry-policy`). Make
  `ExponentialBackoffRetry` honor the **same** "non-positive budget = retry indefinitely"
  convention that `AsyncOptions.MaxRetries` already documents, so the two paths agree and
  the footgun disappears. Add an explicit, self-documenting `RetryForever(baseDelay,
  maxDelay)` policy so callers never have to lean on the `0` sentinel to express "retry
  forever," and keep `NoRetry()` as the one canonical "never retry." This is a **deliberate
  behavior change** for the narrow set of callers that pass `maxRetries <= 0` to
  `ExponentialBackoffRetry` (the default policy uses `3`, so the default is unaffected) —
  see `design.md` for the blast-radius analysis and the migration note.

- **Transient-vs-poison error classification** (`projection-error-classification`). Add an
  optional `AsyncOptions.ErrorClassifier func(error) ErrorClass` so an error classified
  `ErrorClassTransient` is retried **independently of the poison budget** (it never trips
  `OnPoisonEvent` or `Faulted`), while an `ErrorClassPoison` error consumes the budget
  exactly as today and still reaches `OnPoisonEvent` after `MaxRetries`. Ship a
  `DefaultErrorClassifier` that honors `interface{ Temporary() bool }`, an exported
  `interface{ Retryable() bool }`, and the new exported `ErrTransient` sentinel
  (`errors.Is`). Unset classifier ⇒ every error is poison ⇒ **byte-for-byte current
  behavior, zero overhead**.

### Good-to-have

- **Faulted-worker supervision & push-based fault alerting** (`projection-supervision`).
  Add an optional per-projection `AsyncOptions.RestartPolicy` (with
  `ExponentialBackoffRestart` / `RestartForever`) that **restarts a Faulted worker with
  backoff, resuming strictly from its checkpoint** instead of exiting permanently; a manual
  `ProjectionEngine.Restart(ctx, name)` operator primitive (symmetric with
  `Pause`/`Resume`/`Rebuild`); and an engine-level
  `WithProjectionStateObserver(func(name string, old, new ProjectionState, err error))`
  push callback so a transition into `Faulted` (and back out) fires an alert instead of
  waiting to be polled. All opt-in: no policy ⇒ a fault stays terminal exactly as today; no
  observer ⇒ no callback, zero overhead.

### Non-Goals

- **No change to the default retry budget or the `RetryPolicy` interface shape.**
  `DefaultAsyncOptions()` keeps `ExponentialBackoffRetry(3, …)` and `MaxRetries: 3`. The
  `RetryPolicy` interface (`ShouldRetry`/`Delay`) is unchanged; only the built-in
  implementation's `maxRetries <= 0` semantics change, plus additive constructors.
- **No automatic error classification by default.** With no `ErrorClassifier`, every error
  remains poison — go-mink does not guess which of a consumer's errors are transient. The
  shipped `DefaultErrorClassifier` is opt-in, and does **not** auto-classify
  `context.DeadlineExceeded`/batch-timeout as transient (left to the caller / an Open
  Question), to avoid masking a genuinely hung poison event.
- **No restart-from-zero, ever.** A supervised restart resumes from the persisted
  checkpoint; `StartFromBeginning` governs only the *first* boot when no checkpoint exists.
  Reprocessing history is exclusively the job of the explicit `Rebuild`.
- **No new mandatory config, no `ProjectionMetrics` interface change, and no DB-schema
  change.** State-transition alerting rides the new additive `WithProjectionStateObserver`
  callback rather than extending the `ProjectionMetrics` interface (which would break every
  existing implementer). Checkpoints use the existing `CheckpointStore`; no new columns.
- **No change to inline or live projections.** Inline projections are synchronous with the
  append and live projections are transient/best-effort by contract; retry classification
  and worker supervision apply to the **async** worker only.
- **Not a circuit breaker or dead-letter queue.** Those remain on the roadmap; this change
  is the minimal, composable retry/supervision surface, not a general resilience framework.

## Capabilities

### Added Capabilities

- `projection-retry-policy` (Required) — the retry-budget convention and the explicit
  unlimited-retry policy.
- `projection-error-classification` (Required) — transient-vs-poison classification and its
  interaction with the retry budget and backoff.
- `projection-supervision` (Good-to-have) — Faulted-worker restart with checkpoint-safe
  resume, the manual `Restart` primitive, and the push-based state observer.

The existing `projection-engine` capability (checkpoint-load safety, poison-event identity,
transparent decryption, live-drop visibility) is unchanged and preserved; these are new,
composable behaviors layered onto the same async worker.

## Impact

- **`projection_engine.go`**: `exponentialBackoffRetry.ShouldRetry` `<=0` semantics; new
  `RetryForever`, `ErrorClass`/`DefaultErrorClassifier`, `RestartPolicy` +
  `ExponentialBackoffRestart`/`RestartForever`; new `AsyncOptions.ErrorClassifier` and
  `AsyncOptions.RestartPolicy` fields; the run loop splits poison-budget accounting from
  backoff accounting and wraps the worker body in a supervising loop
  (`superviseAsyncWorker` → `runAsyncWorkerOnce`); `setState`/`setError` fire the engine
  observer outside the state lock; new `ProjectionEngine.Restart`; new
  `WithProjectionStateObserver` engine option.
- **`projection.go`**: additive `ProjectionStateRestarting` state constant.
- **`errors.go`**: new `ErrTransient` sentinel (and, for supervision, `ErrProjectionNotFaulted`).
- **Tests**: table-driven suites per capability on the in-memory checkpoint store + a
  fault-injecting projection (retry-count convention incl. `<=0`; classifier routing;
  restart-from-checkpoint incl. `StartFromBeginning` ignored on restart; observer fires on
  fault/recovery; composition with `Rebuild`).
- **Docs**: doc comments on every new public API; `CHANGELOG` `[Unreleased]` (with the Gap-1
  behavior-change + migration note under `Changed`); the Reliability line in
  `website/docs/roadmap.md`.
- **Compatibility**: additive and opt-in except the deliberate Gap-1 convention fix; default
  options and any caller passing a positive `maxRetries` are unaffected.
