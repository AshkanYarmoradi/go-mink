# saga-orchestration Specification

## Purpose
TBD - created by archiving change fix-review-correctness-bugs. Update Purpose after archive.
## Requirements
### Requirement: Saga retry attempts are at least one
`WithSagaRetryAttempts(n)` SHALL clamp the configured attempts to a minimum of 1, so a
saga event is always attempted at least once. A configured value of 0 SHALL NOT cause
the processing loop to skip the event, drop it silently, or advance the position past an
unprocessed event.

#### Scenario: Zero retry attempts still processes the event
- **WHEN** the saga manager is configured with `WithSagaRetryAttempts(0)` and an event arrives
- **THEN** the event is dispatched at least once and the position advances only after a real attempt, not over a silently-dropped event

### Requirement: Context cancellation during dispatch does not trigger compensation
The saga manager SHALL NOT treat a command-dispatch failure of `context.Canceled` or
`context.DeadlineExceeded` (e.g. during a graceful `Stop()`) as a business failure. It
SHALL leave the saga `Running` and SHALL NOT run compensation, so the saga is not
persisted as `CompensationFailed` and a restart can resume it.

#### Scenario: Graceful shutdown mid-dispatch leaves the saga runnable
- **WHEN** the context is cancelled while dispatching a saga command
- **THEN** the saga stays `Running`, no compensation is dispatched, and it is not persisted as `CompensationFailed`

#### Scenario: A real step failure still compensates
- **WHEN** a command dispatch fails with a non-context business error
- **THEN** the saga runs its compensation as before

### Requirement: Operator-initiated safe saga re-drive
`SagaManager` SHALL provide `RetrySaga(ctx context.Context, sagaID string) error` that
re-drives a settled-but-unsuccessful saga once its underlying cause is fixed. It SHALL
accept a saga whose status is `Failed`, `Compensated`, or `CompensationFailed`
(`SagaState.IsRetryable()` SHALL report exactly these), and SHALL reject a `Completed`
saga — and any in-flight `Running`/`Started`/`Compensating` saga — with the typed error
`ErrSagaNotRetryable`. A missing saga SHALL return the existing `ErrSagaNotFound`. Re-drive
SHALL re-deliver the saga's last trigger event through the normal processing path so a
retry has identical semantics to a fresh delivery: on success the saga becomes `Completed`;
on a fresh failure it follows the same compensation path as a first-time failure. Re-drive
SHALL be **idempotent with respect to already-succeeded steps** — only the failed event is
re-processed; earlier events recorded in `ProcessedEvents` SHALL NOT be re-dispatched. This
requirement preserves the append-only event store, forces no schema change, and adds zero
overhead when `RetrySaga` is never called.

#### Scenario: A failed saga is re-driven to completion
- **WHEN** a saga has stopped in `Failed` (its command dispatch failed) and the transient cause is fixed
- **THEN** `RetrySaga` re-delivers the failing event, the command now succeeds, and the saga reaches `Completed`

#### Scenario: Re-drive does not re-run already-succeeded steps
- **WHEN** a multi-step saga succeeded on steps 0..N-1 and failed on step N, and `RetrySaga` runs
- **THEN** only the step-N event is re-processed and the commands for steps 0..N-1 are NOT re-dispatched (their event keys remain in `ProcessedEvents`)

#### Scenario: A completed saga is not retryable
- **WHEN** `RetrySaga` is called for a saga in status `Completed`
- **THEN** it returns `ErrSagaNotRetryable`, the saga is unchanged, and no command is dispatched

#### Scenario: An in-flight saga is not retryable
- **WHEN** `RetrySaga` is called for a saga in status `Running`, `Started`, or `Compensating`
- **THEN** it returns `ErrSagaNotRetryable` and does not touch the saga (the event loop / timeout sweep owns it)

#### Scenario: A saga with no captured trigger event is rejected, not guessed
- **WHEN** `RetrySaga` is called for a retryable-status saga that has no recorded last trigger event (e.g. created before this capability)
- **THEN** it returns `ErrSagaNotRetryable` with a clear reason rather than fabricating an event

### Requirement: Re-drive is concurrency-safe against the running manager
`RetrySaga` SHALL perform its load → re-drive → save under the **same per-saga lock** the
event-processing loop uses, and SHALL persist via `SagaStore.Save` under **optimistic
concurrency** using the loaded `Version`. A concurrent modification of the same saga SHALL
surface as the existing `ErrConcurrencyConflict` and SHALL NOT be silently swallowed or
retried in an internal loop. The saga state used for re-drive SHALL be (re)loaded inside
the lock, never from a stale pre-read.

#### Scenario: A concurrent change makes the retry lose loudly
- **WHEN** the same saga is modified concurrently between `RetrySaga`'s load and its save
- **THEN** `RetrySaga` returns `ErrConcurrencyConflict` and does not double-drive the saga

#### Scenario: Retry serialises against an in-flight event
- **WHEN** the event loop is processing an event for saga S and `RetrySaga(S)` is called
- **THEN** the two do not interleave — the per-saga lock serialises them, and the second to run observes the first's committed state

### Requirement: Re-drive is observable and auditable
A re-drive SHALL be recorded so it is never a silent state change: `RetrySaga` SHALL append
a `SagaStep` to the saga's history marking the re-drive (from-status and outcome), and
`SagaManager` SHALL support an optional `WithSagaRetryObserver(func(RetryEvent))` hook that
is invoked for each retry attempt with the saga id, saga type, from-status, timestamp, and
error (nil on success). When no observer is configured, the `SagaStep` record SHALL still
be written.

#### Scenario: The retry observer sees each attempt
- **WHEN** a `WithSagaRetryObserver` is configured and `RetrySaga` runs (succeeding or failing)
- **THEN** the observer is invoked once with the saga id, type, from-status, timestamp, and the attempt's error (nil on success)

#### Scenario: Retry is recorded even without an observer
- **WHEN** `RetrySaga` runs with no observer configured
- **THEN** a `SagaStep` marking the re-drive is appended to the saga's `Steps` history

### Requirement: Resume a stalled running saga on demand
`SagaManager` SHALL provide `ResumeStalled(ctx context.Context, sagaID string) error` that
re-drives a `Running` saga whose worker died mid-dispatch, **now**, instead of waiting for
the `WithSagaTimeout` sweep to compensate it. It SHALL accept **only** a `Running` saga and
SHALL reject any other status with `ErrSagaNotRetryable`. To avoid fighting a live worker,
it SHALL require the saga's `UpdatedAt` to be older than the configured saga timeout (or a
caller-supplied threshold); otherwise it SHALL reject the resume. It SHALL reuse the same
per-saga lock, optimistic concurrency, idempotency, and observability guarantees as
`RetrySaga`.

#### Scenario: A stalled running saga is resumed
- **WHEN** a saga has been `Running` with no update for longer than the saga timeout and `ResumeStalled` is called
- **THEN** its last event is re-delivered under the per-saga lock and it advances (to `Completed` or through compensation), identically to `RetrySaga`

#### Scenario: A recently-updated running saga is not resumed
- **WHEN** `ResumeStalled` is called for a `Running` saga updated more recently than the timeout threshold
- **THEN** it returns `ErrSagaNotRetryable` and does not touch the saga, so it cannot race a live worker

### Requirement: Batch re-drive by type
`SagaManager` SHALL provide `RetrySagasByType(ctx context.Context, sagaType string, statuses ...SagaStatus) (RetryReport, error)`
that finds sagas of the given type in the given (retryable) statuses via the existing
`FindByType` and applies `RetrySaga` to each, sequentially, returning a `RetryReport` with a
per-saga outcome (succeeded / failed-again / conflicted / skipped-not-retryable). It SHALL
be a pure composition of the single-saga primitive: each saga is re-driven under its own
per-saga lock, and a failure of one SHALL NOT abort the batch.

#### Scenario: A batch re-drive reports per-saga outcomes and does not abort on one failure
- **WHEN** `RetrySagasByType` runs over several sagas and one of them conflicts or fails again
- **THEN** the returned `RetryReport` records each saga's individual outcome and the remaining sagas are still attempted

