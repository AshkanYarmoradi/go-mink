# outbox-delivery-e2e Specification

## Purpose
TBD - created by archiving change end-to-end-test-coverage. Update Purpose after archive.
## Requirements
### Requirement: Store-to-broker outbox delivery is verified end-to-end
There SHALL be an end-to-end test that appends an event together with an outbox message via `EventStore.AppendWithOutbox` to a real PostgreSQL store, runs the real `OutboxProcessor` configured with a real publisher, and asserts the message is delivered to the external system and then marked completed in the outbox store. This MUST cover the `outbox/webhook` publisher (against an `httptest.Server`) and the `outbox/kafka` publisher (against `TEST_KAFKA_BROKERS`), replacing the mock-publisher-only coverage that exists today. Delivery MUST be at-least-once: the event is never lost.

#### Scenario: Appended event is delivered to the broker and marked completed
- **WHEN** an event is appended with `AppendWithOutbox` on PostgreSQL and the `OutboxProcessor` runs with the real Kafka (or webhook) publisher
- **THEN** the payload is received at the broker/endpoint (consumed back from Kafka, or received on the httptest server) with the expected type/headers, and the outbox row transitions to completed

#### Scenario: Transient publisher failure retries, then dead-letters, without losing the event
- **WHEN** the publisher returns an error (endpoint returns non-2xx / broker send fails) for a message
- **THEN** the processor retries per its policy and, on exhausting retries, moves the message to the dead-letter path — the event row in `events` is never mutated or removed (at-least-once preserved, append-only intact)

### Requirement: SNS delivery is verified end-to-end (good-to-have, LocalStack-gated)
There SHALL be an end-to-end test that runs the `OutboxProcessor` with the `outbox/sns` publisher against a LocalStack SNS endpoint (`TEST_SNS_ENDPOINT`), asserting the message is published with its message-group id and marked completed. It MUST self-skip when the endpoint is not configured.

#### Scenario: SNS publish via LocalStack
- **WHEN** `TEST_SNS_ENDPOINT` is set and the processor runs with the SNS publisher
- **THEN** the message is published to the topic (observed via LocalStack) and marked completed; **WHEN** the endpoint is unset the test skips

