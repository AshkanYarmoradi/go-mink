# key-revocation Specification

## Purpose
TBD - created by archiving change gdpr-erasure-and-retention. Update Purpose after archive.
## Requirements
### Requirement: Optional Revocable provider interface
The `encryption` package SHALL define an optional `Revocable` interface — `RevokeKey(keyID string) error` and `IsRevoked(keyID string) (bool, error)` — that providers MAY implement to perform crypto-shredding portably. The signature SHALL match the existing `local` provider's `RevokeKey` (no `context.Context`) for consistency; providers whose backend needs a context use a background context internally. Support SHALL be detected at runtime via a type assertion, exposed through `encryption.Revoke(p, keyID)` / `encryption.IsRevoked(p, keyID)` helpers, so `encryption.Provider` is NOT changed (no breaking modification).

#### Scenario: A Revocable provider can shred
- **WHEN** a configured provider implements `Revocable` and `RevokeKey(keyID)` is called
- **THEN** the key is marked revoked and subsequent `DecryptDataKey`/`Decrypt` for that key return `ErrKeyRevoked`

#### Scenario: A non-Revocable provider stays valid
- **WHEN** a provider does not implement `Revocable`
- **THEN** encryption/decryption keep working, and `encryption.Revoke` / any erasure that needs revocation returns `ErrRevocationUnsupported` rather than panicking

### Requirement: Revocation is permanent and idempotent
`RevokeKey` SHALL be idempotent — revoking an already-revoked key SHALL succeed without error — and SHALL render the key's data permanently unrecoverable.

#### Scenario: Re-revoking is a no-op success
- **WHEN** `RevokeKey` is called twice for the same key
- **THEN** both calls succeed and the key remains revoked

#### Scenario: Revoked data cannot be decrypted
- **WHEN** data encrypted under a revoked key is loaded
- **THEN** decryption fails with `ErrKeyRevoked` and the field is surfaced as redacted, not as raw ciphertext

### Requirement: Built-in providers implement Revocable
The local, AWS KMS, and HashiCorp Vault providers SHALL implement `Revocable`. The KMS and Vault providers SHALL gain revocation through an OPTIONAL client sub-interface (`KMSRevocationClient` with `ScheduleKeyDeletion`/`DescribeKey`; `VaultRevocationClient` with `DeleteKey`/`KeyExists`) so the base `KMSClient`/`VaultClient` adapters are NOT changed; when the injected client lacks it, `RevokeKey`/`IsRevoked` return `ErrRevocationUnsupported`. Each backend's durability semantics SHALL be documented (local: zero + delete key material; KMS: `ScheduleKeyDeletion`, immediately unusable, destroyed after the pending window; Vault Transit: `DeleteKey`).

#### Scenario: KMS revocation uses native deletion
- **WHEN** `RevokeKey` is called on the KMS provider whose client supports revocation
- **THEN** it invokes `ScheduleKeyDeletion` and `IsRevoked` (via `DescribeKey`) reflects the pending-deletion/disabled state

#### Scenario: A provider whose client lacks revocation
- **WHEN** `RevokeKey` is called on a KMS/Vault provider whose injected client does not implement the revocation sub-interface
- **THEN** it returns `ErrRevocationUnsupported` without side effects

### Requirement: IsRevoked status query
`IsRevoked(keyID)` SHALL report whether a key is currently revoked, for verification and idempotency checks.

#### Scenario: Status reflects revocation
- **WHEN** a key is revoked and `IsRevoked` is queried
- **THEN** it returns true

### Requirement: Revocation state distinguishes soft from permanent
The `encryption` package SHALL define `RevocationState` (`NotRevoked`, `SoftRevoked`,
`Revoked`) and an OPTIONAL `StatefulRevocable` interface —
`RevocationState(keyID string) (RevocationState, error)` — exposed via
`encryption.GetRevocationState(p, keyID)`, which SHALL fall back to `IsRevoked`
(mapping to `Revoked`/`NotRevoked`) for providers that do not implement it. This lets
callers (notably `Verify`) tell a still-recoverable soft-revoked key from a permanently
shredded one without changing `Revocable`/`IsRevoked`.

#### Scenario: State reflects soft vs hard revocation
- **WHEN** a key is soft-revoked within its grace window and `GetRevocationState` is queried
- **THEN** it returns `SoftRevoked`; after a hard `RevokeKey` (or grace-window expiry) it returns `Revoked`

### Requirement: Soft-revoke becomes a real shred after the grace window
A `RecoverableRevocable` provider SHALL, once a soft-revoked key's grace window has
elapsed, render the key material permanently unrecoverable (crypto-shredded) — not
merely gate decryption. The local provider SHALL promote an expired soft-revocation to
a hard revoke on next access, zeroing and deleting the key bytes, so the "permanent
after the window" guarantee holds against a memory dump, not just the provider API.

#### Scenario: Expired soft-revoke clears key material
- **WHEN** a key is soft-revoked and the grace window elapses, and the key is next accessed or its state queried
- **THEN** the key material is zeroed and removed, `UnrevokeKey` fails, and `RevocationState` returns `Revoked`

### Requirement: Soft-revoke helpers signal unsupported providers
The package SHALL provide `encryption.SoftRevoke(p, keyID, window)` and
`encryption.Unrevoke(p, keyID)` helpers that return `ErrRevocationUnsupported` when the
provider does not implement `RecoverableRevocable` (e.g. the current KMS/Vault
providers), so a caller relying on a grace window cannot silently fall through to an
immediate hard revoke.

#### Scenario: SoftRevoke on a non-recoverable provider
- **WHEN** `encryption.SoftRevoke` is called on a KMS or Vault provider
- **THEN** it returns `ErrRevocationUnsupported` without side effects

### Requirement: Provider contract proves revoke prevents decryption
The shared `providertest` suite SHALL include `AssertRevokeMakesDecryptFail`, wired into
every provider's tests, which encrypts data, revokes the key, and asserts that a
subsequent `Decrypt`/`DecryptDataKey` fails — enforcing the core crypto-shred property
for local, KMS, and Vault, not only local.

#### Scenario: Decryption fails after revoke, for every provider
- **WHEN** `AssertRevokeMakesDecryptFail` runs against a revocation-capable provider
- **THEN** post-revoke decryption returns an error rather than plaintext

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

