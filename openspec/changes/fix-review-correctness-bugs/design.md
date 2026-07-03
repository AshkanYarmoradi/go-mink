# Design

All fixes are **additive and behavior-preserving for correct callers**: new typed
errors, an option clamp, a `--force` flag, a safe-watermark cursor, and copy/ordering
corrections. No required interface changes, no DB-schema changes, no event mutation.
Where a fix has more than one reasonable shape, the decision and its trade-off are
recorded below; the trivial one-line fixes are specified directly in the delta specs.

## 1. Read-model DELETE/scan guard — `read-model-store`

Problem: `buildWhereClause` (`adapters/postgres/readmodel.go`) `continue`s over any
filter whose `fieldToColumn` returns `""`, and `Query.Where` does no validation. A
non-empty `query.Filters` that resolves to zero conditions yields `whereClause == ""`,
so `deleteManyWithExecutor` runs `DELETE FROM "schema"."table"` (whole-table wipe) and
`Find`/`Count` scan everything.

Decision: distinguish "caller asked for no filter" from "caller's filters all
resolved to nothing." `buildWhereClause` returns a new sentinel `ErrUnknownFilterField`
(typed `*UnknownFilterFieldError` carrying the offending field name) when
`len(query.Filters) > 0` but `len(conditions) == 0`. `DeleteMany`, `Find`, and `Count`
propagate it. An intentional match-all stays expressible via an explicitly empty
filter set (`len(query.Filters) == 0`). This is defense-in-depth at the exact layer
that builds the SQL, so every caller (memory adapter mirrors the check) is covered
without validating field names at `Query.Where` time.

## 2. Gapless position-based delivery — `event-subscriptions`

### Scope (broadened after review)
The original finding named `subscription.go`, but the **same** "advance a cursor to the
last-seen `global_position`, then poll `global_position > cursor`" pattern drives every
position-based delivery path: PostgreSQL `SubscribeAll`/`SubscribeCategory` **and** async
projections (`ProjectionEngine.loadEventsFromPosition`, which also **checkpoints** the
advanced position, so a skip is permanent) **and** the saga manager's event loop. So the
fix must live at the **shared load-from-position layer**, not in `subscription.go` alone,
and all three consumers inherit it.

Production context: the live consumer (huisscan) runs async projections + a saga with
concurrent, unserialized writers, so it is exposed today (silently missed read-model
updates and un-fired saga steps under load). Interim mitigations while this ships: the
**outbox is gap-free** (it claims rows with `FOR UPDATE SKIP LOCKED`, no position
dependency) — route must-not-miss side effects through it; and a projection
**rebuild-from-0** recovers already-missed read-model events.

### Problem
`global_position` (`BIGSERIAL`) is assigned at `INSERT` but a row becomes visible only at
`COMMIT`, so commit order can differ from position order. Crucially, under MVCC the poller
**cannot see** an in-flight transaction's row at all — it sees position 6 (committed) but
not the still-open position 5, advances its cursor to 6, and when 5 commits it can never
satisfy `> 6` again. The poller must therefore *detect* that a lower position may still
appear and hold back, rather than trust visible contiguity.

### Decision (no DB-schema change)
Deliver only up to a **safe high-watermark** = the largest position guaranteed to have no
still-in-flight lower position. Two viable PG-native mechanisms (to be chosen during
implementation, both **without** a schema column):

- **Transaction-snapshot watermark** — at poll time capture `pg_snapshot`/`xid8`
  boundaries (`pg_snapshot_xmin(pg_current_snapshot())`) and the sequence's `last_value`;
  the watermark is the highest position below any xid still in the in-progress set. Uses
  the hidden `xmin` system column — no schema change.
- **Gap-detection with a staleness timeout** (Marten's async-daemon approach) — process
  the contiguous committed prefix; on a gap, wait a grace period; if it fills, deliver in
  order; if it persists past the timeout and no in-flight transaction could fill it, treat
  it as a rolled-back (permanent) gap and advance past it.

Enforce the watermark at the **adapter's load-from-position query** (return nothing above
the safe watermark). Each consumer then naturally advances its cursor/checkpoint only to
the last *returned* (safe) event; held-back positions are returned on a later poll once
stable — so subscriptions, projections, and sagas are fixed with no consumer-side change.
This trades a small, bounded delivery latency (≈ one poll interval / the grace window) for
the no-skip guarantee. **At-least-once is preserved** (duplicates on restart still
possible); only the *at-most-once loss* is removed. The in-memory adapter has no
transactions and is already fixed by the atomic snapshot+register in §2.1 above.

### In-memory snapshot/register race
Problem: `SubscribeAll` copies history under `a.mu`, releases it, then registers the
subscriber under a *different* mutex (`a.subscribersMu`). An `Append` in the gap
notifies the not-yet-registered subscriber and is also past the history cutoff — lost.

Decision: make the historical snapshot and subscriber registration a single critical
section under `a.mu`: register the subscriber (and capture the current position) while
holding the lock used by `Append`/`notifySubscribers`, then drain history from that
exact position. No new lock ordering is introduced (registration moves under the
already-held `a.mu`); the separate `subscribersMu` is either folded in or acquired in a
fixed order to avoid inversion.

### Category LIKE escaping (Good-to-have)
`loadCategoryEvents` builds `category + "-%"` unescaped. Reuse the existing
`escapeLikePattern` helper (`readmodel.go`) on the category prefix so `%`/`_` in a
category name are literals, and keep the trailing `-%` wildcard.

## 3. Saga reliability — `saga-orchestration`

- `WithSagaRetryAttempts(n)` clamps to `max(1, n)` (mirrors how `bus.go` guards
  retries). Zero can no longer make the `for attempt := 0; attempt < retryAttempts`
  loop skip its body — which today drops the event, returns a nil-wrapped error
  (`%!w(<nil>)`), and still advances the position.
- `attemptProcessSagaEvent` classifies the dispatch (and `HandleEvent`) error: if
  `errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)`, it
  returns without calling `handleSagaFailure`, leaving the saga `Running` so a restart
  resumes it. Only genuine business/step failures drive `Compensate`. This prevents
  graceful `Stop()` from persisting sagas as `CompensationFailed` with compensations
  that "ran" on an already-cancelled context. (The manager's swallow-and-continue
  position handling on non-cancellation errors is unchanged — a deliberate resilience
  choice covered by existing tests.)

## 4. Projection recovery & edge cases — `projection-engine`

- **Checkpoint load must not default to 0.** `runAsyncWorker` currently logs a
  `GetCheckpoint` error and proceeds with `startPosition == 0`. Since both adapters
  return `(0, nil)` for a *missing* checkpoint, a non-nil error is genuinely transient
  (DB down / adapter closed). The worker SHALL fail to start (surfaced via the engine's
  error path / worker state) instead of replaying history from 0 into a non-idempotent
  projection.
- **Live projections (Good-to-have).** `NotifyLiveProjections` skips workers not in
  `Running`. Either buffer/catch-up from the last known position when a worker becomes
  `Running`, or make the "live projections have no catch-up" contract explicit and
  observable (a dropped-event counter + documented guarantee) so silent loss becomes a
  visible, intentional choice rather than a surprise.
- **Poison event identity (Good-to-have).** In the batch path, `ApplyBatch` runs over
  `filteredEvents` but the poison event is taken from the unfiltered `events[len-1]`.
  Record the poison event from `filteredEvents` (the slice actually applied). The
  position-advance-past-the-batch behavior stays (it is a documented trade-off); only
  the *identity* handed to `OnPoisonEvent` is corrected.

## 5. Outbox delivery correctness — `outbox-publishing`

`sendMessage` (`outbox/webhook`) currently fails only on `>= 500` and `>= 400`, so
`3xx` (e.g. `300`/`304`, which `net/http` does not auto-follow for a `POST`) falls
through to success and the outbox marks the message delivered. Success becomes
`StatusCode >= 200 && StatusCode < 300`; everything else returns an error so the outbox
retries / dead-letters. `2xx`-that-isn't-`200` (201/202/204) stays a success.

## 6. GDPR correctness — `data-erasure`, `data-export`, `key-revocation`

- **Reconcile guard (`data-erasure`).** `reconcileAfterRevoke` revokes newly-appeared
  "newcomer" keys via `cfg.RevokeKey` with no shared-key check, while the initial path
  runs `detectSharedKeys` under `WithSharedKeyGuard`. Route reconcile revocations
  through the same guard: a shared/tenant key appearing during the window returns
  `SharedKeyError` (and sets `Partial`) unless `AllowSharedKeyRevocation()` is set,
  instead of silently shredding co-tenant subjects.
- **`Failed()` blind spot (`data-erasure`).** `ErasureResult.Failed()` ignores subject
  stores recorded `Skipped` (their optional purger interface was not implemented). A
  registered store that could not erase MUST make `Failed()` true (or set `Partial` and
  append an error), so a caller checking `Failed()`/`err` cannot mistake residual PII
  for a clean erasure.
- **Marker idempotency (`data-erasure`, Good-to-have).** `appendMarker` appends
  unconditionally. Re-running `Erase` (documented as idempotent) double-appends. Guard
  on an existing marker for the subject (or make the marker append conditional on this
  run having performed a not-yet-recorded revocation).
- **Export redaction (`data-export`).** With `WithDecryptionErrorHandler` returning nil
  (the recommended crypto-shred setup), `decryptFields` returns still-encrypted bytes +
  nil error, so `processStoredEvent` takes the success branch and emits `Redacted=false`
  with ciphertext in `Data`/`RawData`. Export SHALL independently detect an event whose
  encryption metadata (`$encrypted_fields`) is present and whose fields remain
  encrypted, and mark it `Redacted=true` with `nil` `Data`/`RawData` regardless of the
  handler's decision — closing the untested with-handler path.
- **Revocation permanence (`key-revocation`).** The KMS `revoked()` helper counts
  `KeyStateDisabled` as revoked, so `RevokeKey` returns nil (no `ScheduleKeyDeletion`)
  and `IsRevoked` returns true for a reversible key. `Disabled` MUST NOT count as
  revoked: `RevokeKey` schedules deletion (or returns an error if it cannot), and
  `IsRevoked`/erasure certification treat only `PendingDeletion`/absent key material as
  erased. Re-enabling a "revoked" key can no longer resurrect certified-erased data.

## 7. Adapter data integrity — `event-store`

The in-memory `appendLocked` stores the caller's `event.Data` slice and
`Metadata.Custom` map by reference. Deep-copy both on append (reuse the existing
`copyBytes` helper for `Data`; clone the `Custom` map), mirroring the snapshot/outbox
deep-copy discipline and the PostgreSQL adapter's freshly-scanned bytes. This removes a
data-corruption + `-race` hazard when a caller reuses a serialization buffer.

## 8. Compatibility summary

New surface is additive: `ErrUnknownFilterField`/`UnknownFilterFieldError`
(`read-model-store`), a `--force` flag (`cli-tooling`), and internal watermark/copy
logic. `WithSagaRetryAttempts` clamping only changes the already-broken `0` case.
Export/KMS/eraser changes make previously-wrong outputs correct without changing the
happy path. Every new behavior is either always-on correctness (never a regression for
correct callers) or gated behind existing config, so the zero-overhead-when-unconfigured
invariant holds.
