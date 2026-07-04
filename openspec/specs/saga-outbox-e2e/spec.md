# saga-outbox-e2e Specification

## Purpose
TBD - created by archiving change end-to-end-test-coverage. Update Purpose after archive.
## Requirements
### Requirement: Saga-to-publisher orchestration is verified end-to-end (good-to-have)
There SHALL be an end-to-end test in which a `SagaManager` reacts to events stored in PostgreSQL, emits commands that a handler persists to the store and enqueues as outbox messages, and the real `OutboxProcessor` delivers them via a real publisher (Kafka or webhook). The suite MUST cover both the happy-path completion and a forced-failure path that drives compensation, asserting compensation runs and the saga state reflects it — without mutating the append-only event log.

#### Scenario: Saga completes and its emitted commands are delivered
- **WHEN** a saga-triggering event is appended to PostgreSQL and the saga manager, command handler, and outbox processor run
- **THEN** the saga advances through its steps, each emitted command's outbox message is delivered to the broker, and the saga reaches completion

#### Scenario: A failing step drives compensation end-to-end
- **WHEN** a downstream command fails during the PG-backed saga run (a business failure, not a context cancellation)
- **THEN** the saga runs its compensation steps, the saga is recorded as compensated/failed per policy, and no already-appended event is rewritten or removed

#### Scenario: Graceful shutdown mid-dispatch does not compensate
- **WHEN** the manager is stopped (context cancelled) while dispatching a saga's commands
- **THEN** the saga is left `Running` for restart rather than driving compensation on a dead context

