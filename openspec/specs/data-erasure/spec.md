# data-erasure Specification

## Purpose
TBD - created by archiving change gdpr-erasure-and-retention. Update Purpose after archive.
## Requirements
### Requirement: DataEraser orchestrator
The `mink` package SHALL provide a `DataEraser`, constructed via `NewDataEraser(store, opts...)` (mirroring `NewDataExporter`), that performs GDPR Article 17 erasure for a subject in one call. `Erase(ctx, ErasureRequest) (*ErasureResult, error)` SHALL crypto-shred the subject by revoking the relevant key(s) via a `Revocable` provider, optionally append an erasure-marker event, and return a structured result.

#### Scenario: Erase a subject by key
- **WHEN** `Erase` is called for a subject whose data is encrypted under a known key id
- **THEN** the key is revoked, the subject's encrypted fields become unrecoverable, and the result reports the revoked key(s) and affected streams

#### Scenario: Erasure without a revocable provider
- **WHEN** `Erase` is called but the configured provider does not implement `Revocable`
- **THEN** it returns a typed "erasure unsupported" error and makes no partial state change

### Requirement: Consistent subject targeting with export
`ErasureRequest` SHALL identify the subject by key id(s) and/or the same primitives as `DataExporter` (explicit `Streams`, a `Filter` over events, and `SubjectID`), so erasure and export share one subject model.

#### Scenario: Target by filter
- **WHEN** `Erase` is given a `Filter` (e.g. by tenant id) rather than explicit streams
- **THEN** the eraser resolves the matching key(s) and revokes them

### Requirement: Optional erasure-marker event
The eraser SHALL support optionally appending an append-only erasure-marker event (configurable event type and stream) recording that a subject was erased, when, and by whom — so the log retains tamper-evident proof of erasure. When marker emission is enabled, the event MUST contain no erased PII.

#### Scenario: Marker is appended when configured
- **WHEN** erasure is configured to append a marker and a subject is erased
- **THEN** a marker event with no PII is appended to the designated stream

### Requirement: Erasure is idempotent and reports partial failures
`Erase` SHALL be idempotent (re-erasing an already-erased subject succeeds as a no-op) and SHALL return an `ErasureResult` capturing keys revoked, streams/events affected, and per-item errors rather than aborting on the first failure.

#### Scenario: Re-erase is a no-op success
- **WHEN** a subject that was already erased is erased again
- **THEN** the call succeeds and the result indicates nothing new to revoke

#### Scenario: Partial failure is reported, not fatal
- **WHEN** one of several keys fails to revoke
- **THEN** the others still revoke and the failure is captured in `ErasureResult.Errors`

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

### Requirement: Reconciliation honours the shared-key guard
`reconcileAfterRevoke` SHALL apply the same shared-key detection as the initial revoke
path to any newly-appeared ("newcomer") key when `WithSharedKeyGuard()` is set and
`AllowSharedKeyRevocation()` is not. A newcomer key that also protects another subject's
events SHALL NOT be revoked silently; the erasure SHALL surface a `SharedKeyError` and
set `ErasureResult.Partial`.

#### Scenario: A shared tenant key appearing during reconcile is not silently shredded
- **WHEN** a per-tenant key first appears as a newcomer during reconciliation and `WithSharedKeyGuard()` is set without `AllowSharedKeyRevocation()`
- **THEN** it is not revoked; `Erase` reports a `SharedKeyError` and `Partial=true` instead of shredding co-tenant subjects

#### Scenario: Explicit override still allows reconcile revocation
- **WHEN** both `WithSharedKeyGuard()` and `AllowSharedKeyRevocation()` are set
- **THEN** the newcomer key is revoked and reported in `ErasureResult.KeysRevoked`

### Requirement: Failed() accounts for skipped subject stores
`ErasureResult.Failed()` SHALL report failure when a registered subject store did not
erase the subject (e.g. its optional purger interface was not implemented and the
outcome was `Skipped`). Such an outcome SHALL make `Failed()` true or set `Partial` and
append an error, so a caller checking `Failed()`/`err` cannot mistake residual PII for a
completed erasure.

#### Scenario: A skipped sibling store is not reported as success
- **WHEN** a registered subject store returns a `Skipped` outcome because its purger interface is unimplemented
- **THEN** `ErasureResult.Failed()` returns true (or `Partial` is set with an error), not a clean success

### Requirement: Erasure marker is not duplicated on idempotent re-run
`appendMarker` SHALL NOT append a duplicate erasure marker when `Erase` is re-run for an
already-erased subject. A single logical erasure SHALL yield at most one marker per
subject in the marker stream.

#### Scenario: Re-running Erase does not double-append a marker
- **WHEN** `Erase` is re-invoked for a subject that already has an erasure marker
- **THEN** no second marker is appended and the accountability stream keeps exactly one marker for the erasure

