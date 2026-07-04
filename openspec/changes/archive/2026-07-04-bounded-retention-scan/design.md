## Context

`RetentionManager.run()` scans the whole store on every `Apply`:

```go
var position uint64
for {
    batch, _ := m.store.LoadEventsFromPosition(ctx, position, m.batchSize) // exclusive of `position`
    if len(batch) == 0 { break }
    for _, se := range batch { /* match every policy, act on shred/redact/anonymize */ }
    position = batch[len(batch)-1].GlobalPosition
}
```

Cost is O(total history) per run. A scheduled sweep, however, only needs to act on events
that have **aged past a policy's `MaxAge` since the previous run** — a small moving tail.
Everything older than the aged frontier was already crypto-shredded on an earlier run
(and re-revoking an already-revoked key is a documented no-op — the local provider returns
`nil` for an already-revoked key). So the fix is to **not re-scan the fully-handled prefix**.

## Goals / Non-Goals

**Goals**
- Bound steady-state per-run cost to the events within the retention window, not total history.
- Preserve correctness *without* assuming timestamps are monotonic with global position.
- Reuse existing machinery (`CheckpointStore`); stay additive, opt-in, zero-overhead-when-unused.

**Non-Goals**
- Changing default behavior. No option set ⇒ identical to today (scan from 0, persist nothing).
- Early-stop at the first not-yet-aged event (needs a monotonicity guarantee go-mink doesn't make).
- New DB schema or a bespoke persistence store for retention state.

## Decisions

### D1: Resume via the existing `CheckpointStore`, passed explicitly
`WithRetentionCheckpoint(store CheckpointStore, name string)` mirrors the projection
engine's `WithCheckpointStore(store)` exactly. The Postgres adapter already implements
`CheckpointStore`; consumers (e.g. huisscan) already surface it as
`EventStoreResult.CheckpointStore`, so wiring is ~2 lines and needs no schema change. The
store is passed in rather than type-asserted off `m.store.Adapter()` so it is explicit,
testable with `memory.NewCheckpointStore()`, and lets a caller point retention at a
different checkpoint backend than its projections if it wants.
- *Alternative:* a new `RetentionCheckpointStore` type — rejected (duplicates a proven interface).
- *Alternative:* auto-derive the store from the adapter — rejected (hidden dependency, harder to test, couples retention to adapter capability detection).

The checkpoint `name` is caller-chosen and lives in the same keyspace as projection
checkpoints, so it MUST NOT collide with a projection name (e.g. use a reserved sentinel
like `"__mink_retention__"`). Documented on the option.

### D2: The safe-resume **frontier** — the crux, and why it's correct without monotonicity
After a run we persist a position `F` and next run resumes at `F` (exclusive). `F` must
satisfy one invariant:

> **No event at global position ≤ F can ever *newly* require action on a future run.**

The only time-dependent matcher is `MaxAge`; every other matcher (category, stream prefix,
event type, tenant) is static. So an event can *newly* match in the future **iff** it
statically matches some policy but is not yet old enough for that policy — call this
event **pending**:

```
pending(e) := ∃ policy p .  p.matchesStatic(e)  ∧  p.MaxAge > 0  ∧  age(e) < p.MaxAge
```

An event that is **not** pending is permanently settled: either it already matched (and was
acted on — idempotently, so re-acting is harmless) or it matches no policy at all and never
will. Therefore we advance `F` through the **maximal contiguous prefix of non-pending
events** from the resume point, and **freeze `F` at the first pending event** encountered.
We keep scanning to HEAD and acting on everything, but `F` does not advance past that first
pending event.

This is correct **regardless of whether timestamps are monotonic with position**:
- Age-matching is monotonic in *wall-clock time* — once `age(e) ≥ MaxAge`, every later run
  (larger `now`) still sees it as aged. So a non-pending event stays non-pending forever.
- `F` only advances through non-pending events, so every event ≤ `F` is settled forever.
  Skipping `[0, F]` next run can never drop an erasure.
- A pending event at position `p` freezes `F` at `< p`, so it (and everything after it,
  which we do **not** try to reason about) is re-scanned next run until it too settles.

Because we do not assume position order tracks timestamp order, an "old" event appearing
after a "young" one is fine: the young one freezes `F`; the old one is still acted on this
run and simply gets re-scanned (and idempotently re-acted) next run until the young one ages.

### D3: The checkpoint governs *resume*, not *action*
Within a run, every matching event is acted on exactly as today. The checkpoint changes
only the scan's start position and adds one persisted number at the end. The re-scanned
"young window" `[F, HEAD]` (events newer than `MaxAge`, plus any post-freeze stragglers)
is bounded by *ingestion-rate × MaxAge*, not by total history — this is the win — and
re-acting on any already-shredded event in it is a documented no-op.

### D4: Persist on `Apply` only, and only when `F` advanced
`DryRun` never writes a checkpoint (it must change nothing). `Apply` writes `SetCheckpoint`
only when `F > startPos`, avoiding no-op writes when a run found nothing newly settled. A
`SetCheckpoint` failure is a non-fatal `report.Errors` entry (the sweep still did its work;
it just didn't advance the resume point — next run redoes the range harmlessly).

### D5: Default unchanged; unconfigured is exactly today
No `WithRetentionCheckpoint` ⇒ `startPos = 0`, no frontier persistence, full scan — the
current behavior, byte-for-byte. Existing consumers and tests are unaffected.

### D6: Optional per-run cap composes with the frontier (good-to-have)
`WithRetentionMaxScan(n)` stops a run after `n` scanned events. It composes cleanly: `F` is
still "highest contiguous non-pending prefix seen so far," so stopping early just means `F`
advances less this run and the next run continues from it. Its purpose is bounding the
**first** run after enabling retention on a large store (the checkpoint alone can't — the
first run has no prior frontier and must reach the aged tail once). Because "resume next
run" requires a persisted position, the cap is only meaningful with a checkpoint; set
without one, `run()` records a loud non-fatal `report.Errors` entry and scans unbounded
(never silently caps-and-forgets, which would re-shred the same oldest `n` forever and
never reach the tail).

### D7: Checkpoint validity is tied to the policy set's *static* matchers
A persisted frontier is only valid for the policy set that produced it. Reasoning:
- **Changing `MaxAge` is safe.** Lengthening it matches fewer/older events — everything
  below `F` was already shredded. Shortening it makes *younger* events eligible, but those
  are **above** `F` (nearer HEAD) and get scanned normally. Either way nothing below `F` is
  wrongly skipped. (huisscan only ever changes `RETENTION_MAX_AGE_DAYS`, so it is always safe.)
- **Broadening a static matcher is NOT safe.** Adding a policy (or widening
  category/prefix/type/tenant) that now targets *old* events sitting below `F` would skip
  them, because `F` advanced past them when no policy matched. Such a change requires
  resetting the checkpoint (`DeleteCheckpoint`, or a fresh `name`).
This is the same rule as projection checkpoints (change the projection logic ⇒ rebuild).
Documented on the option and in the spec.

## Risks / Trade-offs

- **Young-window re-scan each run.** `[F, HEAD]` is re-scanned every sweep. Bounded by the
  retention window, not history, and re-actions are no-ops — acceptable. Eliminating it
  needs D2's rejected monotonicity fast-path.
- **First run still full** (without the cap). Enabling on a large store pays one full scan
  to establish `F`. Mitigated by `WithRetentionMaxScan` for that case.
- **Checkpoint-name collision** with a projection would corrupt both. Mitigated by
  documenting a reserved-name convention; the name is caller-supplied and explicit.
- **Stale checkpoint after a broadened policy** (D7) would under-erase. Mitigated by
  documentation + the reset primitive; the common case (age-only change) is safe by construction.

## Migration

Additive and opt-in — no migration. Existing `NewRetentionManager(store, policies)` calls
are unchanged. A consumer opts in by adding `WithRetentionCheckpoint(cpStore, name)` (and
optionally `WithRetentionMaxScan(n)`); the first opted-in run behaves like today and every
run after resumes from the frontier.
