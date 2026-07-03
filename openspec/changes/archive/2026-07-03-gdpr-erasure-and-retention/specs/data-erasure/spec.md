## ADDED Requirements

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
