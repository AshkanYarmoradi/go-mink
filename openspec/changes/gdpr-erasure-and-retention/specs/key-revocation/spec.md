## ADDED Requirements

### Requirement: Optional Revocable provider interface
The `encryption` package SHALL define an optional `Revocable` interface — `RevokeDataKey(ctx, keyID) error` and `IsRevoked(ctx, keyID) (bool, error)` — that providers MAY implement to perform crypto-shredding portably. The core encryption path SHALL detect it at runtime (type assertion), so providers that do not implement it remain valid and `encryption.Provider` is NOT changed (no breaking modification).

#### Scenario: A Revocable provider can shred
- **WHEN** a configured provider implements `Revocable` and `RevokeDataKey(keyID)` is called
- **THEN** the key is marked revoked and subsequent `DecryptDataKey`/`Decrypt` for that key return `ErrKeyRevoked`

#### Scenario: A non-Revocable provider stays valid
- **WHEN** a provider does not implement `Revocable`
- **THEN** encryption/decryption keep working, and any erasure that needs revocation returns a typed "revocation unsupported" error rather than panicking

### Requirement: Revocation is permanent and idempotent
`RevokeDataKey` SHALL be idempotent — revoking an already-revoked key SHALL succeed without error — and SHALL render the key's data permanently unrecoverable.

#### Scenario: Re-revoking is a no-op success
- **WHEN** `RevokeDataKey` is called twice for the same key
- **THEN** both calls succeed and the key remains revoked

#### Scenario: Revoked data cannot be decrypted
- **WHEN** data encrypted under a revoked key is loaded
- **THEN** decryption fails with `ErrKeyRevoked` and the field is surfaced as redacted, not as raw ciphertext

### Requirement: Built-in providers implement Revocable
The local, AWS KMS, and HashiCorp Vault providers SHALL implement `Revocable` using each backend's native mechanism (local: delete/tombstone key material; KMS: schedule key deletion or disable; Vault Transit: soft-delete / `min_decryption_version`) and SHALL document each one's durability semantics.

#### Scenario: KMS revocation uses native deletion
- **WHEN** `RevokeDataKey` is called on the KMS provider
- **THEN** it invokes the KMS key deletion/disable operation and `IsRevoked` reflects the revoked state

### Requirement: IsRevoked status query
`IsRevoked(ctx, keyID)` SHALL report whether a key is currently revoked, for verification and idempotency checks.

#### Scenario: Status reflects revocation
- **WHEN** a key is revoked and `IsRevoked` is queried
- **THEN** it returns true
