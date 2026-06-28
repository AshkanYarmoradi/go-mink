## ADDED Requirements

### Requirement: Pseudonymization action
The package SHALL provide an anonymization action that replaces configured PII fields with stable pseudonyms (deterministic tokenization keyed per field/tenant), usable as a retention or erasure action, preserving referential and analytic utility while removing direct identifiers.

#### Scenario: Stable pseudonym replaces PII
- **WHEN** the anonymize action runs on a field
- **THEN** the field value is replaced by a stable pseudonym such that equal inputs map to equal pseudonyms and the original value is not recoverable

### Requirement: Anonymization is selectable per policy
Retention policies and the eraser SHALL allow `Anonymize` as an alternative to `Shred`, so callers can choose pseudonymization where analytic continuity matters and full crypto-shredding where it does not.

#### Scenario: Choose anonymize over shred
- **WHEN** a retention policy specifies `Anonymize` for a field
- **THEN** matching events have that field pseudonymized rather than crypto-shredded
