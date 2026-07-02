## ADDED Requirements

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
