## ADDED Requirements

### Requirement: Kafka test helper (env-gated, self-skipping)
The `testing/containers` package SHALL provide a `StartKafka(t *testing.T) *KafkaContainer` helper that MUST connect to an already-running broker addressed by the `TEST_KAFKA_BROKERS` environment variable and MUST skip the calling test (never fail it) when the variable is unset or the broker is unreachable — mirroring the existing `StartPostgres` contract. The returned handle SHALL expose the broker address and a helper to create/consume an isolated topic so a suite can assert delivery without cross-test interference.

#### Scenario: Kafka available
- **WHEN** `TEST_KAFKA_BROKERS` is set to a reachable broker and a test calls `StartKafka(t)`
- **THEN** the helper returns a usable `KafkaContainer` (broker address + topic helpers) and the test proceeds

#### Scenario: Kafka absent — self-skip, never fail
- **WHEN** `TEST_KAFKA_BROKERS` is unset (or points at an unreachable broker), or the test runs under `-short`
- **THEN** `StartKafka(t)` calls `t.Skip(...)` so unit-only environments stay green and the default build needs no broker

### Requirement: Shared PostgreSQL-backed end-to-end fixture
The `testing/containers` package SHALL provide a shared end-to-end fixture that constructs a real `mink.EventStore` over the PostgreSQL adapter on a per-test isolated schema and optionally wires a projection engine, saga manager, and/or outbox processor, so each E2E suite is a few lines of setup. The fixture MUST drop its schema on cleanup and MUST self-skip under `-short` or when `TEST_DATABASE_URL` is unset, reusing `StartPostgres`/`NewIntegrationTest`.

#### Scenario: Fixture wires a real store on an isolated schema
- **WHEN** an E2E suite requests the fixture with `TEST_DATABASE_URL` set
- **THEN** it receives an initialized `EventStore` on a fresh schema (plus any requested projection engine / saga manager / outbox processor), and the schema is dropped on `t.Cleanup`

#### Scenario: Fixture self-skips without infra
- **WHEN** the fixture is requested under `-short` or with `TEST_DATABASE_URL` unset
- **THEN** the calling test is skipped rather than failed, so the fast unit matrix is unaffected
