# gdpr-lifecycle-e2e Specification

## Purpose
TBD - created by archiving change end-to-end-test-coverage. Update Purpose after archive.
## Requirements
### Requirement: Full subject erasure is verified end-to-end on PostgreSQL
There SHALL be an end-to-end test that populates a PostgreSQL store with a subject's encrypted, subject-tagged events plus real PostgreSQL sibling stores (read models, audit, saga, outbox, idempotency), runs `DataEraser.Erase`, and asserts the complete Article 17 outcome: the subject's key is revoked, `Load` of the subject's events returns redacted data, the PG read models are redacted, every registered sibling PG store is purged of the subject's PII, an erasure marker event is appended, and `DataEraser.Verify` yields a PII-free `ErasureCertificate`. Crucially, the raw `events` rows MUST be unchanged in count and global position — erasure is by key revocation + redaction, never by rewriting the append-only log.

#### Scenario: Erase revokes, redacts, purges siblings, marks, and certifies
- **WHEN** `DataEraser.Erase` runs for a subject whose data spans PostgreSQL events, read models, and sibling stores
- **THEN** the key is revoked, `Load` returns the subject's events as redacted, PG read models and all registered sibling PG stores no longer contain the subject's PII, a marker is appended, and `Verify` returns a PII-free `ErasureCertificate`

#### Scenario: Append-only invariant holds and Erase is idempotent
- **WHEN** the same `Erase` is run a second time for an already-erased subject
- **THEN** it succeeds as a no-op (no duplicate marker, already-revoked keys are fine) and the raw PG `events` row count and global positions are identical before and after both runs

#### Scenario: Shared-key erasure is refused unless explicitly allowed
- **WHEN** a subject's key also protects another subject and the shared-key guard is enabled without `AllowSharedKeyRevocation`
- **THEN** `Erase` reports a `SharedKeyError` / partial result and does not shred the shared key, so the other subject's data remains recoverable

### Requirement: Subject data export is verified end-to-end on PostgreSQL
There SHALL be an end-to-end test that runs `DataExporter.Export`/`ExportStream` over a real PostgreSQL stream (both stream-based and scan-based via the real `SubscriptionAdapter`) for a mix of plaintext, encrypted, and crypto-shredded events, asserting the Article 15/20 output includes the subject's events with the export filters applied and that crypto-shredded events come back with `Redacted=true` and `nil` Data.

#### Scenario: Export returns the subject footprint with shredded events redacted
- **WHEN** `DataExporter.ExportStream` scans a PostgreSQL log containing the subject's plaintext, encrypted, and revoked-key events
- **THEN** the export contains the subject's events (filters applied), decrypted where the key is live, and each revoked-key event is `Redacted=true` with `Data=nil` (no ciphertext leak)

### Requirement: CLI GDPR discovery/verification over a populated store (good-to-have)
There SHALL be an end-to-end test that runs `mink gdpr discover` and `mink gdpr verify` against a populated PostgreSQL footprint via the diagnostic adapter, asserting the read-only output reflects the subject's streams, and that the CLI does not (and cannot) perform revocation (it holds no encryption keys) — the actual erasure runs from the application via `DataEraser`.

#### Scenario: CLI reports the footprint but cannot revoke
- **WHEN** `mink gdpr discover`/`verify` runs against a populated PG store
- **THEN** it reports the subject's streams/verification read-only, and any erasure it surfaces is explicitly deferred to the application (no key revocation performed by the CLI)

