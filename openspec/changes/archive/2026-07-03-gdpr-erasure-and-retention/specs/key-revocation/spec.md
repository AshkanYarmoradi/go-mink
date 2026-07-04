## ADDED Requirements

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
