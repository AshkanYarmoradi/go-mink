# projection-pipeline-e2e Specification

## Purpose
TBD - created by archiving change end-to-end-test-coverage. Update Purpose after archive.
## Requirements
### Requirement: Command-to-read-model pipeline is verified end-to-end on PostgreSQL
There SHALL be an end-to-end test that dispatches a command through the `CommandBus` (with the real middleware pipeline — validation, idempotency, correlation) to a handler that persists events to a PostgreSQL store, drives the `ProjectionEngine` (async and live) off a real PostgreSQL subscription, and asserts the read model is updated and the projection checkpoint advances. This closes the gap where the live/async engine has PG coverage only for *rebuild*.

#### Scenario: Dispatched command flows through to the read model
- **WHEN** a command is dispatched through the bus, its handler appends events to PostgreSQL, and an async/live projection is subscribed
- **THEN** the projection applies the events (decrypted where applicable), the read model reflects them, and the checkpoint advances to the last processed global position

#### Scenario: Idempotent command is not double-applied
- **WHEN** the same command (same idempotency key) is dispatched twice through the bus on PostgreSQL
- **THEN** the events are appended once and the projection reflects a single application (idempotency store enforced end-to-end)

### Requirement: Async projection recovers from a checkpoint restart on PostgreSQL
There SHALL be an end-to-end test that stops/restarts an async projection worker against PostgreSQL and asserts it resumes from its persisted checkpoint rather than replaying from position 0, and that a poison event is surfaced to `OnPoisonEvent` (from the applied batch) without stalling the worker.

#### Scenario: Worker resumes from persisted checkpoint after restart
- **WHEN** an async projection has processed up to position N on PostgreSQL and its worker is restarted
- **THEN** it resumes from N (loaded from the checkpoint store), applies only events after N, and does not replay history

#### Scenario: Poison event is reported, not silently dropped
- **WHEN** a projection's apply fails for a specific event during the PG-backed run
- **THEN** the poison event (from the applied/filtered batch) is delivered to `OnPoisonEvent` and the worker advances past it or faults per policy — it does not spin or silently skip

