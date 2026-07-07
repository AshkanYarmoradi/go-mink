## Why

`RetentionManager.Apply` walks the store from **global position 0 to HEAD on every
sweep** (`retention.go` → `run()` loops `LoadEventsFromPosition(ctx, position, batch)`
with no lower bound and no persisted resume point). Because an event-sourced store is
append-only and never shrinks, each scheduled run is a **full-store table scan whose
cost grows linearly with total history** — even though a periodic sweep only ever needs
to act on the *newly-aged tail* since the last run. Everything below the aged frontier
was already handled (crypto-shredded) on a prior run; re-scanning it every night is pure
waste, and the waste compounds for the life of the product.

This was surfaced in a production operations-panel review as efficiency finding **EF2**.
It is **low urgency** — retention is opt-in and off by default (a consumer gates the sweep
on `RETENTION_MAX_AGE_DAYS`) — but it is exactly the kind of cost that only bites once
someone enables it on a large store, so it is worth closing before then.

go-mink already has the primitive to fix this cleanly: the `CheckpointStore` interface
(`GetCheckpoint`/`SetCheckpoint`, implemented by the Postgres adapter, used by the
projection engine to resume). Retention should resume the same way projections do.

## What Changes

### Required

- **Checkpointed (resumable) retention scan** — a new option
  `WithRetentionCheckpoint(store CheckpointStore, name string)` on `RetentionManager`
  (mirroring the projection engine's `WithCheckpointStore`). When configured, `Apply`:
  - resumes the scan from the persisted position instead of 0;
  - after a successful `Apply` (never `DryRun`), persists a **safe-resume frontier** —
    the highest global position below which no event can *newly* match on a future run;
  - so steady-state per-run cost is bounded by the events **within the retention
    window**, independent of total store history.
  When the option is **absent, behavior is byte-for-byte identical to today** (scan from
  0, persist nothing) — no change for existing consumers.

### Good-to-have

- **Bounded per-run scan** — an optional `WithRetentionMaxScan(n int)` cap (mirroring the
  an audit handler's bounded-scan cap) that stops a single sweep after `n` events and
  resumes on the next run via the checkpoint. This bounds *any single run* — most
  importantly the **first run after enabling retention on an already-large store**, which
  the checkpoint alone does not (the first run must still reach the aged tail once). It is
  only meaningful together with `WithRetentionCheckpoint` (it needs somewhere to resume
  from); set without a checkpoint it is a loud, non-fatal misconfiguration and the scan
  runs unbounded.

### Non-Goals

- **No early-termination at the first not-yet-aged event.** That would assume event
  timestamps are monotonic with global position — a guarantee go-mink does not make
  (backdating / clock skew is possible). The frontier design below is correct *without*
  that assumption; a monotonic fast-path is left as a future, explicitly-opt-in optimization.
- **No change to what a run acts on.** The checkpoint governs only where the *next* run
  resumes; within a run every match is still acted on exactly as today.
- **No default behavior change and no new mandatory schema.** Resume reuses the existing
  `CheckpointStore`; unconfigured managers are unchanged. Preserves go-mink's append-only,
  zero-overhead-when-unused invariants.

## Capabilities

### Modified Capabilities

- `retention-policies`: adds an optional resumable-scan checkpoint and an optional
  per-run scan cap to `RetentionManager`. The existing policy model, sweep, dry-run,
  report, and append-only guarantees are unchanged; the default (no checkpoint) scan is
  preserved.

## Impact

- **`retention.go`**: new `WithRetentionCheckpoint` / `WithRetentionMaxScan` options; the
  `run()` scan resumes from and persists a frontier; `RetentionPolicy.matches` is split
  into a static matcher (`matchesStatic`) + an age check (`ageEligible`) so the frontier
  can tell an event that is *permanently handled* from one that is *pending* (statically
  matches a policy but is not yet old enough). Additive; the public `matches` semantics
  and all existing exports are preserved.
- **Tests** (`retention_test.go`): table-driven coverage for resume-skips-handled-prefix,
  frontier-freezes-at-first-pending, dry-run-persists-nothing, unconfigured-is-unchanged,
  cap-bounds-and-resumes, cap-without-checkpoint-is-loud.
- **Docs**: doc comments on the new APIs; a note on the retention scan's cost and the
  checkpoint-vs-policy-set validity rule; `website/docs/roadmap.md` / `CHANGELOG`.
- **Downstream (out of scope here; separate follow-up after a go-mink release)**: a consuming application passes an already-available checkpoint store + a stable name into the retention manager in its DI wiring; still gated on its retention-window config,
  still off by default.
- **Compatibility**: fully additive and opt-in — zero overhead and zero behavior change
  when the new options are not used.
