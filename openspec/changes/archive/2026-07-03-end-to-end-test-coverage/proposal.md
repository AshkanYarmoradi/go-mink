## Why

go-mink ships a large feature surface — event store, command bus + middleware, inline/async/live projections, projection rebuild, sagas + compensation, the outbox pattern with webhook/Kafka/SNS publishers, event versioning/upcasting, a schema registry, field-level envelope encryption with local/KMS/Vault providers, crypto-shredding + key lifecycle, and the whole GDPR data-governance subsystem (export, erasure, subject discovery, sibling-store erasers, read-model redaction, retention, anonymization, verification, audit). A full-repo coverage survey (2026-07-03) found the **unit** and **adapter-level integration** coverage strong, but the **end-to-end** coverage has structural gaps that hide exactly the class of bug this project is most exposed to:

- **No test wires more than one real external system together.** Zero test files import both `adapters/postgres` and any `outbox/{kafka,webhook,sns}` publisher. The `OutboxProcessor` is only ever driven with a `mockPublisher`, so the store → outbox → processor → real broker delivery chain — the reliability backbone — is unverified.
- **The GDPR/data-governance orchestrators are memory-only.** `DataExporter`, `DataEraser`, `RetentionManager`, `Verify`, redaction, and anonymization are never run against a real PostgreSQL event log **plus** real sibling PG tables (audit/saga/outbox/idempotency). The append-only + crypto-shred + blast-radius invariants are asserted only in-memory.
- **Field encryption E2E is memory-only.** Ciphertext-at-rest is never inspected in a PG `data` column, and KMS/Vault are round-trip-only — never chained through the event store or `DataEraser`.
- **The live/async projection engine has no PostgreSQL integration.** Only *rebuild* touches PG. Real-time subscribe → apply → checkpoint → crash-recover on PG (including the poison-event and checkpoint-load-failure guards) is untested against real infra.
- The repo's existing `e2e_*_test.go` files are, despite their name, **in-process memory tests** that run under `-short`.

This change establishes a first-class, infrastructure-backed **end-to-end test suite** covering **every feature**, not just the current PR, and the shared harness it needs. The goal is to verify each feature the way a real application uses it — through the full stack against real PostgreSQL/Kafka (and, opt-in, SNS/KMS/Vault) — so the append-only and zero-overhead invariants are proven end-to-end.

## What Changes

### Required

- **E2E test harness** (`e2e-test-harness`): extend `testing/containers` with a `StartKafka(t)` helper (env-gated, self-skipping like `StartPostgres`) and a shared PostgreSQL-backed end-to-end fixture that wires an `EventStore` + real adapter (schema-isolated per test) with optional projection engine / saga manager / outbox processor, plus documented env-gated skip conventions (`TEST_DATABASE_URL`, `TEST_KAFKA_BROKERS`, cloud creds). Everything MUST self-skip in `-short` / unit-only environments so the default build stays green.
- **Outbox delivery E2E** (`outbox-delivery-e2e`): a test that appends via `AppendWithOutbox` to PostgreSQL, runs the real `OutboxProcessor` with a real publisher, and asserts the message is delivered to the external system and marked completed — for the **webhook** (httptest) and **Kafka** publishers.
- **Projection pipeline E2E** (`projection-pipeline-e2e`): dispatch through the command bus with real middleware to a PostgreSQL store, drive the `ProjectionEngine` off a real PG subscription (async + live), and assert read-model update, checkpoint advance, and crash-recovery replay.
- **Encryption-at-rest E2E** (`encryption-at-rest-e2e`): persist field-encrypted events to PostgreSQL, assert the stored `data` is ciphertext, then `LoadAggregate` decrypts — provider-parameterized so the local provider runs by default.
- **GDPR lifecycle E2E** (`gdpr-lifecycle-e2e`): the full `DataEraser.Erase` flow on PostgreSQL (revoke key → `Load` returns redacted → PG read models redacted → sibling PG stores purged → marker appended → `Verify` yields a PII-free `ErasureCertificate`), and `DataExporter` (Article 15/20) over a real PG stream including crypto-shredded redaction.

### Good-to-have

- **Saga → outbox → publisher E2E** (`saga-outbox-e2e`): a `SagaManager` reacting to PG-stored events, emitting commands that persist and enqueue outbox messages delivered by a real publisher, with a forced-failure path exercising compensation end-to-end.
- **Governance & retention E2E** (`governance-retention-e2e`): `RetentionManager` sweep (Shred / RedactFields / Anonymize) + dry-run, erasure `Verify`, `Anonymizer`, and read-model redaction over a real PG event log — asserting history is never rewritten.
- **Serialization & versioning E2E** (`serialization-versioning-e2e`): upcasting-on-`Load` from PG, schema-registry compatibility over stored streams, and msgpack/protobuf event bodies round-tripped through PG storage.
- **Cloud-provider E2E** (env-gated within the capabilities above): SNS delivery via LocalStack (`outbox-delivery-e2e`); KMS/Vault encryption through the event store and revocation through `DataEraser` (`encryption-at-rest-e2e`); CLI `mink gdpr discover/verify` over a populated PG footprint (`gdpr-lifecycle-e2e`).

### Non-Goals

- **No production/source behavior changes.** This change adds tests + test-only harness helpers under `testing/`. It MUST NOT modify the append-only event store, adapters, or any feature's runtime behavior. If an E2E test surfaces a real defect, that fix is a separate change.
- **No new mandatory dependencies in the default build.** `StartKafka`/LocalStack/KMS/Vault paths are env-gated and self-skip when the service or credentials are absent, exactly like the existing integration tests. testcontainers-go is NOT adopted here (kept out of the default build).
- **Not a replacement for unit tests.** The existing table-driven unit tests stay; this adds the missing end-to-end layer on top, it does not relocate coverage.
- **No change to CI's coverage gate mechanics.** These tests run under the existing infra-gated targets; wiring them into `make test` / CI is in scope, changing the 90% threshold is not.

## Capabilities

### New Capabilities

- `e2e-test-harness` *(required)*: `testing/containers` gains a `StartKafka` helper and a shared PostgreSQL-backed end-to-end fixture; all E2E entry points self-skip in `-short`/unit-only environments.
- `outbox-delivery-e2e` *(required)*: store → `AppendWithOutbox` → `OutboxProcessor` → real publisher delivery is verified against real infra for webhook and Kafka (SNS good-to-have, LocalStack-gated).
- `projection-pipeline-e2e` *(required)*: command bus → PostgreSQL store → async/live `ProjectionEngine` → read model, including checkpoint advance and crash-recovery, is verified against real PG.
- `encryption-at-rest-e2e` *(required)*: field-encrypted events are proven ciphertext-at-rest in PostgreSQL and decrypted transparently on load (KMS/Vault good-to-have, env-gated).
- `gdpr-lifecycle-e2e` *(required)*: `DataEraser` and `DataExporter` are verified end-to-end on PostgreSQL with real sibling stores, marker, and certificate.
- `saga-outbox-e2e` *(good-to-have)*: saga → command → PG store → outbox → publisher, happy path and compensation.
- `governance-retention-e2e` *(good-to-have)*: retention sweep, verification, anonymization, and redaction over a real PG event log.
- `serialization-versioning-e2e` *(good-to-have)*: upcasting, schema-registry compatibility, and alt-serializer round-trips through PG storage.

### Modified Capabilities

<!-- None. This change is purely additive test coverage + test-only harness
     helpers; no existing capability's behavior is redefined. The referenced
     product capabilities (event-store, outbox-publishing, projection-engine,
     data-erasure, data-export, etc.) are exercised, not modified. -->

## Impact

- **`testing/containers`**: new `StartKafka(t) *KafkaContainer` (env-gated), a shared `E2EStore`/`FullStackTest` fixture that constructs an `EventStore` + real adapter on an isolated schema and optionally a projection engine / saga manager / outbox processor, and helper teardown. Additive; existing `StartPostgres`/`NewIntegrationTest` unchanged.
- **New E2E test files** (build-tag/`-short`-gated, `TEST_DATABASE_URL`/`TEST_KAFKA_BROKERS`-gated): one focused suite per capability, e.g. `e2e_outbox_delivery_test.go`, `e2e_projection_pipeline_test.go`, `e2e_encryption_at_rest_test.go`, `e2e_gdpr_lifecycle_test.go`, plus the good-to-have suites. Named and placed consistently with the existing `e2e_*_test.go` convention.
- **`docker-compose.test.yml` / Makefile**: ensure Kafka (already present) and, for good-to-have SNS, a LocalStack service are available to CI; add/confirm a `make test-e2e` path that runs the infra-gated suites. No change to the coverage threshold.
- **Docs**: a short "Running the end-to-end test suite" section (required env vars, which services each suite needs, how each self-skips) in `AGENTS.md`/`CONTRIBUTING.md`.
- **Compatibility**: test-only and additive; zero impact on the library's runtime, the append-only invariant, or the default (`-short`) build. PRs target `develop`.
