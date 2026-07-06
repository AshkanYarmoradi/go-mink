# Design — safe saga re-drive

## Problem restated

A settled-but-unsuccessful saga (`Failed`, `Compensated`, or `CompensationFailed`) has no
safe forward-recovery path. Operators need: *"the transient cause is fixed — re-drive this
saga from where it stopped, without re-doing what already succeeded and without racing the
running manager."* The hazards a naive retry hits:

- **H1 — double dispatch.** Re-running a saga that already dispatched commands for steps
  0..N would re-emit those commands. Command handlers are idempotent under the store's
  at-least-once delivery, but re-drive must still not *gratuitously* replay succeeded
  steps.
- **H2 — race with the manager.** The event loop may pick up an event for the same saga
  concurrently; an unsynchronized retry corrupts state or loses a version race.
- **H3 — terminal `Completed` mutation.** Re-running a completed saga re-does final,
  typically non-idempotent, commands.
- **H4 — compensation re-entry.** Re-running compensation on an `Compensating` saga
  re-dispatches non-idempotent compensation commands (the existing timeout sweep already
  refuses to touch `Compensating` for exactly this reason).

go-mink already neutralises H1–H4 with existing primitives; the design just composes them.

## Saga status matrix (what `RetrySaga` accepts)

| Status                | `RetrySaga`            | Rationale |
|-----------------------|------------------------|-----------|
| `Started` / `Running` | reject (`ResumeStalled` handles a *stalled* `Running`) | in-flight; the loop/sweep owns it |
| `Completed`           | **reject** `ErrSagaNotRetryable` | terminal success — never re-run (H3) |
| `Failed`              | **accept**             | failed forward, no compensation done — re-drive forward |
| `Compensating`        | reject                 | rollback in progress — owned by loop/sweep (H4) |
| `Compensated`         | **accept**             | rolled back to a clean state — safe to re-drive forward |
| `CompensationFailed`  | **accept**             | stuck after partial rollback — operator re-drive is the only recovery |

`SagaState.IsRetryable()` encodes this (mirrors the existing `IsTerminal()`).
`ResumeStalled` accepts **only** `Running` (and, as a guard, requires the saga's
`UpdatedAt` to be older than a caller-supplied/`WithSagaTimeout` threshold so it can't
fight a live worker).

## Mechanism: re-deliver the last trigger event

A saga advances only by **processing an event** (`Saga.HandleEvent(ctx, event) → []Command`).
There is no "redo step N" primitive, so re-drive must re-deliver an event. The event that
caused the stop is the saga's **last trigger event**.

**Decision D1 — capture the last event in the manager-owned slot of `SagaState.Data`
(schema-free, opt-in).** `SagaState.Data` is already a persisted JSON
`map[string]interface{}`, so the captured event rides it under a reserved key
`__mink_last_event` — **no new column, no new `SagaStore` method**. Two refinements make
this correct and cheap:

- **Manager-owned, not author-owned.** The reserved key is *not* written through the saga's
  `Data()`/`SetData()`: the manager stamps it into the persisted `state.Data` in `saveSaga`
  (into a copy of the saga's map) and strips it in `hydrateSaga` before calling `SetData`,
  holding it on a `SagaBase`-owned slot in between. This is essential because a saga's
  `Data()`/`SetData()` is the *author's* projection — an idiomatic projection-style saga
  (e.g. the repo's `OrderFulfillmentSaga`) rebuilds `Data` from typed fields and drops
  unknown keys, which would silently lose a key written into `Data()`, while a
  map-preserving saga would instead *leak* the reserved key into author code. Owning it end
  to end fixes both.
- **Opt-in via `WithSagaRetryCapture`.** Capture is off by default: nothing is stamped and
  there is zero overhead unless enabled, honoring the library's
  zero-overhead-when-unconfigured invariant. `RetrySaga` on a saga with no captured event
  returns `ErrSagaNotRetryable` with a reason pointing at the option.

*Alternative considered:* re-fetch the event from the event store by position. Rejected as
the default because it couples the manager to a per-saga event query it does not otherwise
need; the `Data` capture is self-contained. (A store-refetch fallback can be a later opt-in
for consumers who do not want the event echoed in the saga row.)

**Decision D2 — reset idempotency for *only* the retried event.** On re-drive, remove the
last event's key from `ProcessedEvents` before re-delivery, leaving every earlier key in
place. Effect: `HandleEvent` runs again for the failed event (re-emitting the command for
the current step), while the manager's `eventAlreadyProcessed` guard still short-circuits
the already-succeeded earlier events — so H1 is bounded to *exactly the step that failed*.
(Saga authors' handlers remain the ultimate idempotency backstop, as they already must be.)

**Decision D3 — status reset before re-drive.** Set the saga to `Running` and clear
`FailureReason` before re-delivery, so the normal loop logic applies. If the re-driven
event succeeds and `saga.IsComplete()`, it becomes `Completed`; if it fails again, it goes
through the *same* `handleSagaFailure` compensation path as a first-time failure. Retry is
therefore semantically identical to a fresh delivery of that event — no special-case state
machine.

## Concurrency & durability (reusing what exists)

- **D4 — per-saga lock.** `RetrySaga` acquires `m.getSagaLock(sagaID)` (the same lock the
  event loop takes) for the load→re-drive→save critical section, so it serialises against
  any concurrent event for that saga (H2).
- **D5 — optimistic concurrency.** The final `Save` carries the loaded `Version`; a
  concurrent change ⇒ `ErrConcurrencyConflict`, returned to the caller (not silently
  retried in a loop — an operator action should fail loudly and be re-issued). This reuses
  the store's existing OCC exactly as the event loop does.
- **D6 — fresh reload under the lock.** State is (re)loaded *inside* the lock, never from a
  stale pre-read, matching the loop's `processSagaEvent` discipline.

## Observability

- **D7 — auditable retry.** `RetrySaga` appends a `SagaStep` marking the re-drive (from
  status → outcome) and invokes an optional `WithSagaRetryObserver(func(RetryEvent))` hook
  (saga id, type, from-status, at, error). huisscan routes this to its audit log; the
  default (no observer) still records the `Steps` entry, so a re-drive is never invisible.

## Edge cases

- **No captured last event** (e.g. a saga created before this change, or one that never
  processed a starting event): `RetrySaga` returns `ErrSagaNotRetryable` with a clear
  reason rather than guessing. `ResumeStalled` has the same requirement. (A future opt-in
  store-refetch fallback would close this for legacy sagas.)
- **Saga not found**: `ErrSagaNotFound` (existing).
- **Concurrent double-retry**: the per-saga lock + OCC make the second lose with
  `ErrConcurrencyConflict`; no double-drive.
- **Retry that fails again**: lands in the normal failure/compensation path; a further
  retry is still permitted (idempotent by D2), so recovery is repeatable once the cause is
  actually fixed.

## Why this is the minimal safe surface

Every guard maps to an existing primitive: terminal check (`IsTerminal`), lock
(`getSagaLock`), OCC (`Version`/`Save`), idempotency (`ProcessedEvents`), compensation
(existing `handleSagaFailure`). The only genuinely new state is the reserved `Data` key —
deliberately chosen over a schema change to honour go-mink's invariants. Batch retry and
`ResumeStalled` are thin compositions of the single-saga primitive, hence Good-to-have.
