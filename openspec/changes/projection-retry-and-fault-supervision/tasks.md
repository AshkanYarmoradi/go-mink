# Tasks — projection-retry-and-fault-supervision

Branch from `develop`; PR targets `develop`. `gofmt` + `golangci-lint` clean; keep unit
tests infra-free (`make test-unit`). Group Required before Good-to-have; ordered by
dependency (retry convention → classification → supervision → cross-cutting).

## 1. Required — Consistent retry-count convention (`projection-retry-policy`)

- [x] 1.1 Change `exponentialBackoffRetry.ShouldRetry(attempt, err)` so `maxRetries <= 0`
  returns `true` on a non-nil error (retry indefinitely), matching the `AsyncOptions.MaxRetries`
  "non-positive = forever" convention; keep `err == nil → false` and `attempt < maxRetries`
  for positive counts. Update the method doc comment to state the `<=0` semantics.
- [x] 1.2 Add `func RetryForever(baseDelay, maxDelay time.Duration) RetryPolicy` (a
  legible alias for `ExponentialBackoffRetry(0, baseDelay, maxDelay)`); doc-comment it as the
  canonical "retry forever with backoff," and reaffirm `NoRetry()` as the canonical "never."
- [x] 1.3 Update the `ExponentialBackoffRetry` and `AsyncOptions.MaxRetries` doc comments so
  both explicitly document one convention: positive = that many attempts; non-positive =
  unlimited; and that `RetryPolicy` takes precedence over `MaxRetries` when set.
- [x] 1.4 Table-driven tests (`projection_retry_policy_test.go`, in-memory checkpoint store,
  a fault-injecting projection): `ExponentialBackoffRetry(0/-1,…)` retries past where it used
  to give up (regression pin for the footgun); `ExponentialBackoffRetry(3,…)` still gives up
  at 3; nil policy with `MaxRetries` 0 / negative / positive; `RetryForever` never gives up;
  `NoRetry` never retries; the default options are unchanged.

## 2. Required — Transient-vs-poison classification (`projection-error-classification`)

- [x] 2.1 Add `type ErrorClass int` with `ErrorClassPoison` (iota / zero) and
  `ErrorClassTransient`; the exported `Retryable` interface (`Retryable() bool`); and the
  `ErrTransient` sentinel in `errors.go` (doc-commented for `errors.Is` wrapping).
- [x] 2.2 Add `func DefaultErrorClassifier(err error) ErrorClass` returning
  `ErrorClassTransient` when the Unwrap chain satisfies `errors.Is(err, ErrTransient)`, or
  implements `Retryable() == true`, or implements `interface{ Temporary() bool } == true`;
  else `ErrorClassPoison`. Doc-comment the exact contract (and that it does **not** auto-classify
  `context.DeadlineExceeded`).
- [x] 2.3 Add the `AsyncOptions.ErrorClassifier func(error) ErrorClass` field (doc: nil ⇒ every
  error poison ⇒ current behavior, zero overhead); leave `DefaultAsyncOptions()` with it nil.
- [x] 2.4 In the run loop, split poison-budget accounting from backoff accounting: classify the
  error, increment a `poisonErrors` counter only for `ErrorClassPoison` (drives `shouldRetry` /
  `OnPoisonEvent` / fault), and a `backoffErrors` counter for any error (drives the backoff delay
  only); a transient error backs off and retries without ever consuming the poison budget; a
  successful batch resets both. Preserve the existing power-of-2 log throttle and
  `metrics.RecordError` on every error regardless of class.
- [x] 2.5 Confirm the nil-classifier path is arithmetically identical to today (poison counter ==
  the old `consecutiveErrors`), so unconfigured behavior and overhead are unchanged.
- [x] 2.6 Table-driven tests (`projection_error_classification_test.go`): a transient error
  (via `ErrTransient` / `Temporary()` / `Retryable()`) retries past `MaxRetries` and never calls
  `OnPoisonEvent`/faults; a poison error still exhausts `MaxRetries` → `OnPoisonEvent` (skip) or
  `Faulted` (no handler); an interleaved transient+poison stream only counts poison toward the
  budget; nil classifier reproduces current behavior exactly; `DefaultErrorClassifier` truth table.
- [x] 2.7 Doc comments on `ErrorClass`, `ErrorClassTransient/Poison`, `Retryable`, `ErrTransient`,
  `DefaultErrorClassifier`, and `AsyncOptions.ErrorClassifier`, including the budget/backoff
  interaction and the poison default.

## 3. Good-to-have — Faulted-worker supervision & state observer (`projection-supervision`)

- [x] 3.1 Add `type RestartPolicy interface { ShouldRestart(restartCount int, lastErr error) bool;
  Delay(restartCount int) time.Duration }` with `ExponentialBackoffRestart(maxRestarts, base, max)`
  (`maxRestarts <= 0` = unlimited, consistent with 1.1) and `RestartForever(base, max)`.
- [x] 3.2 Add the additive `ProjectionStateRestarting` constant to `projection.go`.
- [x] 3.3 Refactor `runAsyncWorker` into `runAsyncWorkerOnce(ctx, worker, resumeFromCheckpoint bool)
  exitReason` (`exitStopped` / `exitFaulted`) and a `superviseAsyncWorker` wrapper that owns
  `defer e.wg.Done()`; `Start` launches `superviseAsyncWorker`. On `exitFaulted` + a configured
  `RestartPolicy` + `ShouldRestart`, set `Restarting`, wait the interruptible backoff (select on
  `e.stopCh`/`worker.stopCh`/`ctx.Done()`), then re-enter with `resumeFromCheckpoint = true`.
- [x] 3.4 Add `AsyncOptions.RestartPolicy RestartPolicy` (doc: nil ⇒ fault is terminal, today's
  behavior).
- [x] 3.5 Guarantee checkpoint-safe resume: on restart force the checkpoint read regardless of
  `StartFromBeginning` (which applies only to first boot); a persistent checkpoint-read fault still
  surfaces as a fault (never defaults to 0) and is itself restartable. Test that a restart resumes
  from the checkpoint and never reprocesses from 0, including with `StartFromBeginning: true`.
- [x] 3.6 Add `func (e *ProjectionEngine) Restart(ctx, name string) error` — relaunch a Faulted
  worker from its checkpoint (`wg.Add(1); go superviseAsyncWorker`), idempotent (no-op nil if not
  Faulted; concurrency-guarded to launch at most one goroutine), `ErrProjectionNotFound` for an
  unknown name. Add `ErrProjectionNotFaulted` if a strict variant is preferred (documented).
- [x] 3.7 Add `func WithProjectionStateObserver(fn func(name string, old, new ProjectionState,
  err error)) ProjectionEngineOption`; fire it from `setState`/`setError` after capturing old→new
  and **releasing** `stateMu` (no deadlock/reentrancy); nil ⇒ no callback, zero overhead.
- [x] 3.8 Verify composition with `Rebuild`: the supervised loop honors `paused`/`processingMu` so
  a restart never applies concurrently with a rebuild and resumes from the rebuilt checkpoint.
- [x] 3.9 Table-driven tests (`projection_supervision_test.go`): a Faulted worker restarts and
  resumes from its checkpoint; `ExponentialBackoffRestart(n)` gives up after n and stays Faulted;
  `RestartForever` keeps restarting with backoff; `Restart` is idempotent and errors on unknown
  name; the observer sees `Running→Faulted→Restarting→Running` (and the fault error); a concurrent
  `Rebuild` composes; `Stop()` joins cleanly while a worker is mid-backoff.
- [x] 3.10 Doc comments on `RestartPolicy`, `ExponentialBackoffRestart`, `RestartForever`,
  `AsyncOptions.RestartPolicy`, `ProjectionEngine.Restart`, `WithProjectionStateObserver`, and
  `ProjectionStateRestarting`, including the checkpoint-safe-resume and `Rebuild`-composition
  guarantees.

## 4. Cross-cutting — docs, changelog, roadmap & validation

- [x] 4.1 `CHANGELOG` `[Unreleased]`: under **Changed**, the Gap-1 retry-convention behavior change
  (`ExponentialBackoffRetry(<=0,…)` now retries forever) with the migration note (use `NoRetry()`
  for never; `RetryForever` for forever); under **Added**, the `ErrorClassifier` /
  `DefaultErrorClassifier` / `ErrTransient` classification and the `RestartPolicy` /
  `Restart` / `WithProjectionStateObserver` supervision — each noted additive/opt-in, zero-overhead
  when unset, no schema change.
- [x] 4.2 `website/docs/roadmap.md`: tick/annotate the Future → **Reliability** items
  (graceful degradation / DLQ) that this partially delivers, and mention projection supervision +
  transient classification on the projection-engine line.
- [x] 4.3 `gofmt` + `go vet ./...` clean; `make test-unit` green (no infra);
  `openspec validate projection-retry-and-fault-supervision --strict` passes.
