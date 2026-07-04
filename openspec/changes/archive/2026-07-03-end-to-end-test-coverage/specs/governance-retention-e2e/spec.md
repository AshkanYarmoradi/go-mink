## ADDED Requirements

### Requirement: Retention sweep is verified end-to-end on PostgreSQL (good-to-have)
There SHALL be an end-to-end test that runs `RetentionManager` over a real PostgreSQL event log with policies exercising each action (Shred, RedactFields, Anonymize) plus a dry-run, asserting the report enumerates the affected events and that the configured action is applied to the read side while the append-only `events` rows are never rewritten or deleted.

#### Scenario: Dry-run reports without mutating, then apply performs the action
- **WHEN** a retention policy runs in dry-run mode over PostgreSQL and then for real
- **THEN** the dry-run produces a report and changes nothing; the real run applies the action (shred/redact/anonymize) to the read side, and in both cases the raw PG `events` row count and positions are unchanged

### Requirement: Erasure verification and anonymization are verified on PostgreSQL (good-to-have)
There SHALL be an end-to-end test that runs `DataEraser.Verify` and the `Anonymizer` over a real PostgreSQL event log plus PG read models, asserting `Verify` detects any residual recoverable PII and that anonymization is deterministic and one-way (the same input maps to the same pseudonym, and the original value is not recoverable from it).

#### Scenario: Verify confirms no recoverable PII after a real erasure
- **WHEN** `Verify` runs against PostgreSQL events and read models after a subject was erased
- **THEN** it reports no recoverable PII and produces a PII-free `VerificationReport`/certificate; if PII remains it is reported as residual

#### Scenario: Anonymization is deterministic and irreversible
- **WHEN** the `Anonymizer` pseudonymizes a subject value twice
- **THEN** both runs yield the identical pseudonym and the original value cannot be derived from the pseudonym
