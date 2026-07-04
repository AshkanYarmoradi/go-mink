## ADDED Requirements

### Requirement: Checkpointed (resumable) retention scan
`RetentionManager` SHALL support an optional `WithRetentionCheckpoint(store CheckpointStore, name string)` that makes a sweep resumable. When configured, `Apply` SHALL start its scan from the position persisted under `name` (via `GetCheckpoint`) instead of position 0, and — on `Apply` only, never `DryRun` — SHALL persist a **safe-resume frontier** (via `SetCheckpoint`): the highest global position below which no event can newly match a policy on a future run. An event is *pending* (and therefore stops the frontier) when it statically matches a policy (category / stream prefix / event type / tenant) but is younger than that policy's `MaxAge`. The frontier SHALL advance only through the maximal contiguous prefix of non-pending events from the resume position; a run SHALL still act on every match it scans. When the option is not configured, `Apply` SHALL behave exactly as before (scan from 0, persist nothing). The persisted frontier is valid only for the policy set's static matchers: changing a policy's `MaxAge` is safe, but broadening a static matcher to cover already-scanned older events requires resetting the checkpoint.

#### Scenario: A later run resumes and re-scans only the newly-aged tail
- **WHEN** a checkpointed `Apply` has run once over a store and a second `Apply` runs later after more events have aged past `MaxAge`
- **THEN** the second run starts from the persisted frontier, skips the already-handled prefix, and shreds the events that have newly aged since the first run

#### Scenario: The frontier freezes at the first pending event
- **WHEN** a run scans past events older than `MaxAge` and then reaches an event that statically matches a policy but is younger than `MaxAge`
- **THEN** the persisted frontier is the position just before that pending event (so it is re-scanned on the next run), even though the run continues scanning and acting to HEAD

#### Scenario: Dry-run persists no checkpoint
- **WHEN** `DryRun` runs with a checkpoint configured
- **THEN** no `SetCheckpoint` is written and the resume position is unchanged

#### Scenario: Unconfigured scan is unchanged
- **WHEN** `Apply` runs with no checkpoint configured
- **THEN** it scans the whole store from position 0 and persists nothing, identical to prior behavior

#### Scenario: Checkpoint read/write failure is surfaced, not silent
- **WHEN** the checkpoint store fails to read the resume position
- **THEN** `Apply` returns a wrapped error; **AND WHEN** it fails to persist the frontier after acting, the failure is recorded in `RetentionReport.Errors` (non-fatal — the next run redoes the range harmlessly)

### Requirement: Bounded per-run retention scan
`RetentionManager` SHALL support an optional `WithRetentionMaxScan(n int)` that stops a single sweep after `n` events have been scanned, so any one run — including the first run after enabling retention on a large store — has a bounded cost and the remainder is resumed on the next run. The cap SHALL compose with the checkpoint frontier (a capped run persists the frontier reached so far). Because resuming requires a persisted position, a cap configured without `WithRetentionCheckpoint` SHALL be reported as a non-fatal misconfiguration in `RetentionReport.Errors` and the scan SHALL run unbounded rather than silently cap-and-forget.

#### Scenario: A cap bounds one run and the checkpoint resumes the rest
- **WHEN** `WithRetentionMaxScan(n)` and a checkpoint are configured and the store has more than `n` events to scan
- **THEN** the run scans at most `n` events, acts on the matches among them, persists the frontier reached, and a subsequent run continues from there

#### Scenario: A cap without a checkpoint is loud and runs unbounded
- **WHEN** `WithRetentionMaxScan(n)` is configured but `WithRetentionCheckpoint` is not
- **THEN** the scan runs unbounded and `RetentionReport.Errors` records the misconfiguration (never a silent partial sweep)
