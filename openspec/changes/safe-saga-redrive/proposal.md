## Why

A saga that ends in a **failed / compensated** state today is a dead end: there is no
first-class way for an operator to re-drive it once the underlying cause is fixed. The
`SagaManager` exposes `GetSaga`, `FindSagasByType`, and `FindSagaByCorrelationID` for
*inspection*, and a `WithSagaTimeout` sweep that *auto-compensates* stalled `Running`
sagas — but **no `RetrySaga` / re-drive entry point** for intentional, operator-initiated
recovery. The only options today are to hand-mutate the saga row (dangerous) or to
re-emit a synthetic trigger event (fragile, and it races the running manager).

This was surfaced in the huisscan "Operations Panel" review as finding **§6.3**: the panel
can now *inspect* a stuck `UserRegistration` / `OrganizationRegistration` saga (via
`FindSagasByType` + `GetSaga`), but the operator's natural next action — "the transient
cause is fixed, **re-drive this saga**" (e.g. recover a *user-without-workspace* whose
`CreateWorkspaceCommand` failed) — has no safe API to call. huisscan deliberately shipped
inspection-only and deferred retry precisely because a naive retry can double-dispatch
non-idempotent commands or race the manager.

go-mink already has every primitive a *safe* re-drive needs: optimistic-concurrency on
`SagaStore.Save` (the `Version` field), per-saga locking inside the manager, terminal-state
detection (`SagaState.IsTerminal()`), and idempotency via `ProcessedEvents`. What is
missing is a single method that composes them into a correct, auditable re-drive. This
change adds it.

## What Changes

### Required

- **`RetrySaga(ctx, sagaID string) error`** on `SagaManager` — an operator-initiated,
  idempotent re-drive of a **non-`Completed`** saga. It:
  - loads the saga fresh and **rejects a `Completed` saga** with a typed error
    (`ErrSagaNotRetryable`) — a successfully-completed saga is never re-run;
  - acquires the **same per-saga lock** the event loop uses, so it cannot race an
    in-flight event for that saga;
  - re-delivers the saga's **last trigger event** through the existing processing path,
    after clearing that event from `ProcessedEvents` so the idempotency guard does not
    skip it — while every *earlier* processed event stays recorded, so already-succeeded
    steps are **not** re-executed;
  - persists via `Save` under **optimistic concurrency** (a concurrent change ⇒ typed
    `ErrConcurrencyConflict`, surfaced not swallowed);
  - drives the saga to `Completed` on success, or back through the normal
    failure/compensation path on a fresh failure — i.e. a retry has exactly the same
    semantics as the original delivery, just re-triggered.
- **Retry is auditable**: `RetrySaga` records the attempt (operator, saga id, from-status,
  outcome) via the saga's `Steps` history and an optional retry hook
  (`WithSagaRetryObserver`), so a re-drive is never a silent state change.
- **Re-drivable last event is captured without a schema change**: the manager stashes the
  raw last trigger event in `SagaState.Data` under a reserved key (`Data` is already a
  JSON `map[string]interface{}` — no new column), so `RetrySaga` can replay it without the
  event store. Zero overhead and no behavior change when `RetrySaga` is never called.

### Good-to-have

- **`ResumeStalled(ctx, sagaID string) error`** — re-drive a `Running` saga that is stuck
  (its worker died mid-dispatch) **now**, instead of waiting for the `WithSagaTimeout`
  sweep to compensate it. Same guards as `RetrySaga`; rejects any non-`Running` saga.
- **`RetrySagasByType(ctx, sagaType string, statuses ...SagaStatus) (RetryReport, error)`**
  — a batch convenience that `FindByType` + `RetrySaga` each match, returning a per-saga
  outcome report. Bounded and sequential (respects each per-saga lock); purely a
  composition of the single-saga primitive.

### Non-Goals

- **No re-drive of a `Completed` saga.** Completion is terminal and final; re-running it
  would re-execute already-succeeded, typically non-idempotent commands. `RetrySaga`
  rejects it.
- **No "resume compensation from the middle."** An already-`Compensating` saga is left for
  the manager/sweep; `RetrySaga` re-drives the *forward* path of a settled
  (`Failed` / `Compensated` / `CompensationFailed`) saga, it does not restart a partial
  rollback (which would re-dispatch non-idempotent compensation commands — the exact
  hazard the existing sweep already avoids).
- **No relaxation of the saga-author idempotency contract.** Re-drive re-delivers an event;
  saga command handlers MUST already be idempotent (as they must be for the store's
  at-least-once delivery today). This change documents and depends on that contract, it
  does not replace it.
- **No new mandatory `SagaStore` method and no schema change.** The re-drivable last event
  rides in the existing `Data` JSON; retry composes existing `Load`/`Save`. Preserves
  go-mink's append-only, zero-overhead-when-unused, no-forced-schema-change invariants.

## Capabilities

### Modified Capabilities

- `saga-orchestration`: adds an operator-initiated, idempotent, concurrency-safe re-drive
  (`RetrySaga`, plus optional `ResumeStalled` / batch retry) and a retry-observability
  hook. The existing event-processing loop, timeout sweep, compensation semantics,
  optimistic-concurrency, and terminal-state rules are unchanged; the re-drivable
  last-event capture is additive and free when unused.

## Impact

- **`saga_manager.go`**: new public `RetrySaga` (Required) + `ResumeStalled` /
  `RetrySagasByType` (Good-to-have); a private `redrive(ctx, saga, event)` that both the
  event loop reuse and retry share; capture of the raw last event into `SagaState.Data`
  under a reserved key in the existing save path; `WithSagaRetryObserver` option.
- **`saga.go` / `adapters/adapter.go`**: no interface change to `SagaStore`; a new typed
  error `ErrSagaNotRetryable` alongside the existing `ErrSagaNotFound` /
  `ErrConcurrencyConflict`. A small `SagaState` helper (`IsRetryable()`) mirroring
  `IsTerminal()`.
- **Tests** (`saga_manager_test.go` / a new `saga_retry_test.go`, table-driven, on the
  in-memory saga store): retry re-drives a `Failed` saga to `Completed`; retry is
  **idempotent** (already-processed earlier steps are not re-dispatched); retry of a
  `Completed` saga returns `ErrSagaNotRetryable`; a concurrent change surfaces
  `ErrConcurrencyConflict`; retry respects the per-saga lock; `ResumeStalled` rejects a
  non-`Running` saga; the retry observer sees the attempt.
- **Docs**: doc comments on all new APIs (retryable-status matrix, idempotency contract,
  reserved `Data` key); `saga-orchestration` spec; `website/docs/roadmap.md` /
  `CHANGELOG`.
- **Downstream (out of scope here; separate follow-up after a go-mink release)**: huisscan
  wires `RetrySaga` behind `POST /api/ops/sagas/:id/retry` (`ops:sync:run`, audited) on the
  existing ops saga-inspection panel (§6.3), turning inspection-only into inspect-and-recover.
- **Compatibility**: fully additive and opt-in — the last-event capture is a single map
  write in the existing save path (no schema, no new required config), and no existing
  behavior changes unless `RetrySaga` / `ResumeStalled` is called.
