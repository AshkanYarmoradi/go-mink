## ADDED Requirements

### Requirement: Crypto-shredded events rebuild read models as redacted
When a projection is rebuilt over events whose key has been revoked, go-mink SHALL deliver those events in redacted form (via the existing decryption-error/redaction handling) rather than failing, so a projection that simply rebuilds converges to a PII-free read model for an erased subject instead of crashing or carrying stale plaintext.

#### Scenario: Rebuild after erasure yields redacted rows
- **WHEN** a subject is crypto-shredded and an affected projection is rebuilt from the event stream
- **THEN** the projection receives redacted payloads for the shredded events and writes read-model rows with the PII fields blank/redacted

#### Scenario: Shredded event does not break the projection
- **WHEN** a projection encounters a `ErrKeyRevoked` event during rebuild
- **THEN** it is handed the redaction sentinel and continues, rather than aborting the rebuild

### Requirement: Erasure propagates to read models
`DataEraser.Erase` SHALL drive read-model redaction for the erased subject — by rebuilding the impacted projections from the now-redacted events and/or invoking a redaction hook — so PII does not survive in read models after erasure. The `ErasureResult` SHALL report which read models/projections were redacted. This is required because crypto-shredding only makes the *events* unreadable; projections have already copied PII into separate read-model tables that the application actually serves.

#### Scenario: Read models hold no PII after erase
- **WHEN** `Erase` completes for a subject
- **THEN** querying the affected read models for that subject returns redacted/absent PII, and `ErasureResult` lists the redacted projections

#### Scenario: Event-only erasure is reported as incomplete
- **WHEN** erasure can shred events but cannot redact a read model (no rebuild path and no hook)
- **THEN** the result flags that read model as residual-PII rather than reporting full erasure

### Requirement: Optional SubjectRedactable projection hook
A projection MAY implement an optional `SubjectRedactable { RedactSubject(ctx, subjectID) error }` interface to redact its rows for a subject in place. When present, `DataEraser` SHALL prefer it over a full rebuild; when absent, the eraser falls back to rebuilding the projection. The hook is opt-in with zero overhead when not implemented.

#### Scenario: In-place redaction avoids a full rebuild
- **WHEN** an affected projection implements `SubjectRedactable`
- **THEN** the eraser calls `RedactSubject` and the projection redacts only that subject's rows, without replaying the whole stream

#### Scenario: Hook failure is reported, not fatal
- **WHEN** a `RedactSubject` hook returns an error during erasure
- **THEN** the failure is captured in `ErasureResult` and the other redactions/revocations still proceed

### Requirement: Verification covers read models
Erasure verification (`Verify`) SHALL check read models in addition to events for residual PII, so verification reflects what the application actually serves, not only the event log.

#### Scenario: Verify catches read-model residue
- **WHEN** events are shredded but a read model still holds plaintext PII for the subject
- **THEN** `Verify` reports the subject as not fully erased and identifies the offending read model
