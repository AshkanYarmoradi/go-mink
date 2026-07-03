# subject-erasure Specification

## Purpose
TBD - created by archiving change gdpr-erasure-hardening. Update Purpose after archive.
## Requirements
### Requirement: SubjectErasable seam for non-event stores
The `mink` package SHALL define an optional `SubjectErasable` interface —
`EraseSubject(ctx, subjectID string, footprint *SubjectFootprint) (SubjectErasureOutcome, error)`
and `ErasableName() string` — so stores that hold PII *derived* from events (audit
trail, saga state, snapshots, external sinks) can be erased alongside crypto-shredding.
`DataEraser.WithSubjectStore(...)` SHALL register them, and `Erase` SHALL invoke each
after key revocation and read-model redaction, recording the results in
`ErasureResult.SubjectStores` and treating a per-store failure as non-fatal (reported,
not returned) — symmetric with `WithErasureHook`. This preserves the append-only event
log: `SubjectErasable` targets read-side stores only.

#### Scenario: Registered subject stores are erased
- **WHEN** `Erase` runs for a subject with one or more `SubjectErasable` stores registered
- **THEN** each store's `EraseSubject` is called with the resolved footprint and its outcome (erased count / skipped / error) appears in `ErasureResult.SubjectStores`

#### Scenario: A failing subject store is non-fatal
- **WHEN** one registered `SubjectErasable` returns an error
- **THEN** `Erase` still revokes keys and completes, records the error in that store's outcome and in `ErasureResult.Errors`, and does not abort the other stores

### Requirement: Built-in erasers for audit, saga, and snapshots
The library SHALL ship `SubjectErasable` implementations for its own derived stores:
`NewAuditSubjectEraser` (via optional `adapters.SubjectAuditPurger.DeleteAuditBySubject`,
deleting rows where `actor == subjectID OR aggregate_id == subjectID`),
`NewSagaSubjectEraser` (via optional `adapters.SubjectSagaPurger.DeleteSagasBySubject`,
deleting rows where `correlation_id == subjectID`), and `NewSnapshotSubjectEraser`
(deleting the snapshot of each `footprint.Streams` via the existing `DeleteSnapshot`).
The audit/saga sub-interfaces SHALL be optional and implemented on both the memory and
PostgreSQL stores; a store that does not implement its purger SHALL cause the eraser to
report a skip, not panic.

#### Scenario: Audit rows for the subject are purged
- **WHEN** `NewAuditSubjectEraser(store).EraseSubject(ctx, "user-1", fp)` runs
- **THEN** audit entries with `actor=="user-1"` or `aggregate_id=="user-1"` are deleted and the count is returned, while other subjects' entries remain

#### Scenario: Snapshot eraser uses the footprint streams
- **WHEN** the snapshot eraser runs for a subject whose footprint spans streams S1, S2
- **THEN** the snapshots for S1 and S2 are deleted, leaving unrelated snapshots intact

