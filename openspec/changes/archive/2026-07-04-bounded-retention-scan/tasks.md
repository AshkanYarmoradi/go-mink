## 1. Required — Checkpointed retention scan (`retention-policies`)

- [x] 1.1 Split `RetentionPolicy.matches` into `matchesStatic(se)` (category/prefix/type/tenant) + `ageEligible(se, now)` (the `MaxAge` check); the now-unused private `matches` was removed and `run()` uses the two split matchers directly
- [x] 1.2 Add `WithRetentionCheckpoint(store CheckpointStore, name string) RetentionManagerOption` (nil store / empty name ⇒ no-op); store it on `RetentionManager` (nil ⇒ full-scan, unchanged)
- [x] 1.3 In `run()`: when configured, read `startPos` via `GetCheckpoint(ctx, name)` and start the scan there; on read error return a wrapped error
- [x] 1.4 Track the safe-resume frontier `F`: advance `F` through the maximal contiguous prefix of non-**pending** events from `startPos`; freeze at the first pending event (`matchesStatic ∧ MaxAge>0 ∧ age<MaxAge`); keep scanning to HEAD and acting on all matches
- [x] 1.5 On `Apply` only (never `DryRun`), if `F > startPos`, persist via `SetCheckpoint(ctx, name, F)`; a write error is a non-fatal `report.Errors` entry
- [x] 1.6 Doc comments on all new/changed public APIs: cost characteristic, DryRun-never-persists, reserved-name convention, and the policy-set validity rule (D7)
- [x] 1.7 Table-driven tests (`memory.NewCheckpointStore()`): resume skips the handled prefix + second run acts only on the newly-aged tail (`TestRetention_CheckpointResumesFromHandledPrefix`); frontier freezes at first pending while acting past it (`...FreezesAtPendingEvent`); DryRun persists nothing (`...DryRunPersistsNothing`); **unconfigured manager is unchanged** (`...UnconfiguredScanIsUnchanged`). Correctness for non-monotonic (old-after-young) ordering is established by the design argument (D2) rather than a direct test — the memory test adapter stamps `time.Now()` at append with no backdating hook, so a high-position/old-timestamp event can't be constructed deterministically

## 2. Good-to-have — Bounded per-run scan (`retention-policies`)

- [x] 2.1 Add `WithRetentionMaxScan(n int) RetentionManagerOption` (n ≤ 0 ⇒ unbounded); stop a run once `Scanned` reaches `n`; a truncated run sets `RetentionReport.Truncated`
- [x] 2.2 Compose with the frontier: a capped run persists `F`-so-far and the next run continues; cap set **without** a checkpoint ⇒ loud non-fatal `ErrRetentionMaxScanNeedsCheckpoint` in `report.Errors` + scan unbounded (never silently cap-and-forget)
- [x] 2.3 Tests: cap bounds a single run and the checkpoint resumes the remainder across runs (`TestRetention_MaxScanBoundsAndResumes`); cap-without-checkpoint is reported and runs unbounded (`...MaxScanWithoutCheckpointIsLoud`)

## 3. Docs, changelog & validation

- [x] 3.1 `CHANGELOG` `[Unreleased]`: augmented the RetentionManager entry with the opt-in checkpoint + bounded scan (default unchanged)
- [x] 3.2 Noted the resumable/bounded scan on the retention line in `website/docs/roadmap.md`
- [x] 3.3 `gofmt` + `go vet ./...` clean; `go test ./...` green (root package incl. governance-retention e2e + all adapters); `openspec validate bounded-retention-scan --strict` passes. (golangci-lint typecheck noise is environmental only — a go1.26 stdlib vs go1.25 build skew hitting vendored aws/kafka/pgx deps, not this change's files)
