## ADDED Requirements

### Requirement: Read-only unregistered-type audit
The package SHALL provide a read-only helper that scans a stream — and an all-streams variant — and returns the distinct event types present in storage that are NOT currently registered. The audit SHALL NOT append, mutate, or delete any event (append-only invariant preserved) and SHALL be usable as a pre-deploy / migration safety check.

#### Scenario: Audit reports an unregistered type
- **WHEN** a stream contains an event of a type that is not registered and the audit runs over that stream
- **THEN** the result includes that event type (with a count and/or example stream), and the event log is unchanged

#### Scenario: Fully-registered storage audits clean
- **WHEN** every event type in the scanned scope is registered
- **THEN** the audit returns an empty result

### Requirement: CLI surface for the audit
The `mink` CLI SHALL expose a verb (e.g. `mink stream types` / `mink doctor`) that runs the audit and prints unregistered event types, consistent with the existing `mink stream` / `mink projection` command style. It SHALL exit non-zero when unregistered types are found so it can gate CI/deploys.

#### Scenario: CLI gates on unregistered types
- **WHEN** the audit CLI is run against storage containing an unregistered event type
- **THEN** it prints the offending type(s) and exits with a non-zero status; against clean storage it prints nothing of concern and exits zero
