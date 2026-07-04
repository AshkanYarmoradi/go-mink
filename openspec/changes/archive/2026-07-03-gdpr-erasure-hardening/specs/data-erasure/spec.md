## ADDED Requirements

### Requirement: Blast-radius guard for shared keys
`DataEraser` SHALL offer an opt-in `WithSharedKeyGuard()` that, before performing the
irreversible key revocation, detects any key in the revoke set that also protects events
tagged for a subject other than the target (or untagged events) — the per-tenant-key
case — and SHALL fail with `ErrSharedKeyRevocation` (a `*SharedKeyError` listing the
shared keys) unless `AllowSharedKeyRevocation()` is also set. The guard SHALL be zero
overhead when not configured.

#### Scenario: A shared tenant key is blocked
- **WHEN** `WithSharedKeyGuard()` is set and erasing subject A would revoke a key that also protects subject B's events
- **THEN** `Erase` returns `ErrSharedKeyRevocation` and revokes nothing

#### Scenario: Explicit override proceeds
- **WHEN** both `WithSharedKeyGuard()` and `AllowSharedKeyRevocation()` are set
- **THEN** `Erase` proceeds and `ErasureResult.KeysRevoked` reports the full (tenant-wide) blast radius

### Requirement: Accountability records are durable in strict mode
`DataEraser` SHALL offer `WithStrictAccountability()` under which a failure to append the
erasure marker or emit the certificate (after the idempotent key revocation) returns a
non-nil error, so a caller cannot mistake a lost receipt for a completed, provable
erasure. When a marker stream is configured, `ErasureCertificate.Verified` SHALL be gated
on `MarkerWritten`. The default (non-strict) behavior stays best-effort with soft errors.

#### Scenario: Strict mode fails on lost certificate
- **WHEN** `WithStrictAccountability()` is set, keys are revoked, but the certificate sink errors
- **THEN** `Erase` returns a non-nil error (the caller re-runs `Erase`; revocation is idempotent) rather than reporting success

### Requirement: Erase mitigates the discovery-to-revoke race
`Erase` SHALL, after revoking, re-resolve the subject once and revoke any newly-appeared
keys; if the key set is still growing on the second pass it SHALL set
`ErasureResult.Partial = true` with a warning error. The requirement to quiesce a
subject's writes for a race-free erasure SHALL be documented on `Erase`/`ErasureRequest`.

#### Scenario: Append after discovery under a new key is caught
- **WHEN** an event for the subject is appended under a not-yet-discovered key between discovery and revoke
- **THEN** the re-resolution revokes it, or (if it keeps growing) `Erase` reports `Partial = true`
