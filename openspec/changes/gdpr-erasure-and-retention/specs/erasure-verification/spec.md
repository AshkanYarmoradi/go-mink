## ADDED Requirements

### Requirement: Verify a subject is erased
The eraser SHALL provide `Verify(ctx, subject) (*VerificationReport, error)` that reads back the subject's events and confirms every PII field is redacted (no decryptable PII remains), reporting any residual cleartext or still-decryptable fields.

#### Scenario: Verification passes after erasure
- **WHEN** `Verify` runs for a subject whose key has been revoked
- **THEN** it reports all encrypted PII as redacted with no decryptable residue

#### Scenario: Verification flags residual cleartext
- **WHEN** a subject has PII written before encryption was enabled (legacy cleartext)
- **THEN** `Verify` flags those fields as residual so they can be remediated (e.g. via a retention redaction policy)

### Requirement: Erasure certificate via the audit store
On a verified erasure the eraser SHALL be able to emit an erasure certificate (subject reference, timestamp, actor, keys revoked, verification outcome) recorded via the existing `AuditStore`, WITHOUT recording the erased PII.

#### Scenario: Certificate is recorded without PII
- **WHEN** an erasure is verified and certification is enabled
- **THEN** an audit entry captures the erasure proof and contains no personal data
