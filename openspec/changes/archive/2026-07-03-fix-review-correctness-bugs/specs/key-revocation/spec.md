## ADDED Requirements

### Requirement: A reversible disabled key is not treated as revoked
The KMS provider SHALL NOT count `KeyStateDisabled` as revoked. `IsRevoked` SHALL return
true only when key material is permanently unrecoverable (pending deletion or absent).
`RevokeKey` SHALL schedule key deletion (or return an error if it cannot), so a
subsequently re-enabled key can never resurrect data that erasure/verification certified
as permanently destroyed.

#### Scenario: Disabled key does not certify erasure
- **WHEN** a KMS CMK is merely `Disabled` (re-enable-able) and `RevokeKey`/`IsRevoked` are called
- **THEN** `RevokeKey` schedules deletion or returns an error, and `IsRevoked` returns false — the key is not reported as permanently erased

#### Scenario: Pending-deletion key is erased
- **WHEN** a key is scheduled for deletion (or its material is absent)
- **THEN** `IsRevoked` returns true and erasure verification may certify it
