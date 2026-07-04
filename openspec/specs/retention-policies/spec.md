# retention-policies Specification

## Purpose
TBD - created by archiving change gdpr-erasure-and-retention. Update Purpose after archive.
## Requirements
### Requirement: Configurable retention policy
The `mink` package SHALL provide a `RetentionPolicy` describing a rule: a matcher (any of category, stream prefix, event type, tenant id, or age via `MaxAge`) and an action (`Shred` crypto-shredding, `RedactFields`, or `Anonymize`). Multiple policies SHALL be composable.

#### Scenario: Define an age-based shred policy
- **WHEN** a policy is defined to shred events older than `MaxAge` for a category
- **THEN** events in that category whose age exceeds `MaxAge` are selected for shredding

#### Scenario: Policies compose
- **WHEN** several policies are configured
- **THEN** each matched event has the action of every policy that matches it applied

### Requirement: RetentionManager sweep
A `RetentionManager` SHALL apply the configured policies over the event store on demand via `Apply(ctx)`, SHALL be safe to run on a schedule, and SHALL return a `RetentionReport` (matched, acted-on, skipped, errors).

#### Scenario: Apply enforces matching policies
- **WHEN** `Apply` runs with a shred policy and matching events exist
- **THEN** the matching subjects' keys are revoked and the report counts them

### Requirement: Dry-run mode
The manager SHALL support a dry-run that reports what *would* be acted on without making any change, so policies can be validated before enforcement.

#### Scenario: Dry-run changes nothing
- **WHEN** `Apply` runs in dry-run mode
- **THEN** the report lists matched events but no key is revoked and no field is redacted

### Requirement: Retention never rewrites history
Retention actions SHALL be limited to crypto-shredding, field redaction, and anonymization, which preserve the append-only log; they SHALL NOT delete or mutate historical event rows.

#### Scenario: Append-only is preserved
- **WHEN** a retention action runs
- **THEN** no event row is deleted or rewritten — the effect is achieved via key revocation / redaction metadata only

