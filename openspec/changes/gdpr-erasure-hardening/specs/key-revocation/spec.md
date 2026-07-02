## ADDED Requirements

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
