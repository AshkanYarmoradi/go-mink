# erasure-verification Specification

## Purpose
TBD - created by archiving change gdpr-erasure-and-retention. Update Purpose after archive.
## Requirements
### Requirement: Verify a subject is erased
The eraser SHALL provide `Verify(ctx, subject) (*VerificationReport, error)` that reads back the subject's events and confirms every PII field is redacted (no decryptable PII remains), reporting any residual cleartext or still-decryptable fields.

#### Scenario: Verification passes after erasure
- **WHEN** `Verify` runs for a subject whose key has been revoked
- **THEN** it reports all encrypted PII as redacted with no decryptable residue

#### Scenario: Verification flags residual cleartext
- **WHEN** a subject has PII written before encryption was enabled (legacy cleartext)
- **THEN** `Verify` flags those fields as residual so they can be remediated (e.g. via a retention redaction policy)

### Requirement: Erasure certificate via the audit store
On a verified erasure the eraser SHALL be able to emit an erasure certificate (subject reference, timestamp, actor, keys revoked, verification outcome) recorded via the existing `AuditStore`, WITHOUT recording the erased PII.

#### Scenario: Certificate is recorded without PII
- **WHEN** an erasure is verified and certification is enabled
- **THEN** an audit entry captures the erasure proof and contains no personal data

### Requirement: Verify must not certify a recoverable key
`Verify` SHALL use `encryption.GetRevocationState` and count an encrypted event as
erased ONLY when its key is permanently `Revoked`. A `SoftRevoked` (still-restorable,
in-grace-window) key SHALL be surfaced in a new `VerificationReport.ResidualRecoverable`
set and SHALL force `Verified = false`, so an `ErasureCertificate` can never assert
final erasure while a subject's data is still recoverable via `UnrevokeKey`.

#### Scenario: Soft-revoked subject is not verified
- **WHEN** a subject's key is soft-revoked (within its grace window) and `Verify` runs
- **THEN** the events appear in `ResidualRecoverable`, `Verified` is false, and any emitted certificate is not marked verified

#### Scenario: Hard-revoked subject verifies
- **WHEN** every one of a subject's encrypted-event keys is permanently revoked
- **THEN** `Verified` is true and `ResidualRecoverable` is empty

