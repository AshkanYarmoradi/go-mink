## 1. Required — E2E test harness (`e2e-test-harness`)

- [x] 1.1 Add `StartKafka(t *testing.T) *KafkaContainer` to `testing/containers` (reads `TEST_KAFKA_BROKERS`, self-skips when unset/unreachable/`-short`); expose broker address + isolated-topic create/consume helpers
- [x] 1.2 Add a shared PostgreSQL-backed E2E fixture (`e2ePG`/`newE2EPGBase` in `package mink_test`, kept out of `testing/containers` so that package stays dependency-light) that builds a real `EventStore` on an isolated schema and exposes the adapter (which is also the CheckpointStore/SubscriptionAdapter/SnapshotAdapter) + raw `*sql.DB`/schema for direct assertions; drop schema on cleanup; self-skip without `TEST_DATABASE_URL`
- [x] 1.3 Kafka is already in `docker-compose.test.yml`; added a `make test-e2e` target that runs the infra-gated `TestE2E_*` suites
- [x] 1.4 Documented the E2E suite in `CONTRIBUTING.md`: required env vars per suite and the self-skip behavior
- [x] 1.5 Harness self-tests: `StartKafka` skip contract + `BrokerList` parsing (`testing/containers/kafka_test.go`); the fixture's isolated schema create/drop is exercised by every suite

## 2. Required — Outbox delivery E2E (`outbox-delivery-e2e`)

- [x] 2.1 `e2e_outbox_delivery_test.go`: `NewEventStoreWithOutbox.Append` on PG → real `OutboxProcessor` → `outbox/webhook` publisher (httptest); assert delivery + completion + `X-Outbox-event-type` header
- [x] 2.2 Same flow with the `outbox/kafka` publisher; consume the message back from `TEST_KAFKA_BROKERS` and assert payload/header + completion
- [x] 2.3 Failure path: publisher returns 5xx → exhausts retries → dead-letter; assert never completed and the `events` row count is unchanged (at-least-once + append-only)
- [ ] 2.4 (good-to-have) SNS via LocalStack (`TEST_SNS_ENDPOINT`), self-skipping when unset

## 3. Required — Projection pipeline E2E (`projection-pipeline-e2e`)

- [x] 3.1 `e2e_projection_pipeline_test.go`: dispatch a command through the bus (recovery + correlation + validation + idempotency middleware) → handler appends to PG → async `ProjectionEngine` off a real PG subscription → assert read model updated + checkpoint advanced
- [x] 3.2 Idempotency: dispatch the same command twice → events appended once
- [x] 3.3 Crash-recovery: process to position N, stop, append more, restart a fresh worker → resumes from checkpoint (applies only post-N events, no replay-from-0)
- [x] 3.4 Poison event: force an apply failure → `OnPoisonEvent` receives the event; worker skips it and advances to later events without spinning

## 4. Required — Encryption-at-rest E2E (`encryption-at-rest-e2e`)

- [x] 4.1 `e2e_encryption_at_rest_test.go` (`encryption/local`): save an encrypted event to PG, read raw `data` via SQL and assert the field is ciphertext (non-encrypted field stays plaintext), then `Load` decrypts transparently
- [x] 4.2 Zero-overhead control: same flow without encryption stores/loads unchanged, no encryption metadata added
- [ ] 4.3 (good-to-have) Run the same suite against KMS (`AWS_*`+`MINK_KMS_TEST_KEY_ID`) and Vault (`VAULT_*`+`MINK_VAULT_TEST_KEY`), env-gated; revoke through `DataEraser`/`Revocable` → `Load` cannot decrypt

## 5. Required — GDPR lifecycle E2E (`gdpr-lifecycle-e2e`)

- [x] 5.1 `e2e_gdpr_lifecycle_test.go`: populate PG with subject-tagged, per-subject-encrypted events + a subject index + a snapshot; run `DataEraser.Erase` with a read-model redactor + the snapshot sibling eraser; assert key revoked, `Load` shredded (email unrecoverable), read model redacted, snapshot purged, marker appended, and `Verify` finds no recoverable PII — with the raw `events` row count unchanged except the marker
- [x] 5.2 Append-only + idempotency: re-run `Erase` → no-op, no duplicate marker; assert raw `events` row count identical before/after the re-run
- [x] 5.3 Shared-key guard: a key shared with another subject is refused (`ErrSharedKeyRevocation`), the key is not revoked, and both subjects stay recoverable
- [x] 5.4 `DataExporter.Export` over a real PG stream with a live-key subject (plaintext) and a shredded subject (`Redacted=true`, `Data=nil`, `RedactedCount=1`)
- [ ] 5.5 (good-to-have) `mink gdpr discover`/`verify` over the populated PG footprint; assert read-only output + that the CLI performs no revocation

## 6. Good-to-have — Saga → outbox → publisher E2E (`saga-outbox-e2e`)

- [x] 6.1 `e2e_saga_outbox_test.go`: `SagaManager` (StartAsync) reacts to a PG-stored event → its command handler appends a follow-up event through the outbox → real webhook publisher delivers it → saga reaches `Completed`
- [x] 6.2 Compensation: a failing command drives the saga to `Compensated` (the compensating command is dispatched); no appended event rewritten
- [x] 6.3 A context-cancellation during dispatch does NOT compensate (no compensating command dispatched; saga not `Compensated`)

## 7. Good-to-have — Governance & retention E2E (`governance-retention-e2e`)

- [x] 7.1 `e2e_governance_retention_test.go`: `RetentionManager` over PG with a Shred policy + dry-run (dry-run changes nothing, real run revokes the key) and a RedactFields policy invoking its Apply hook per matched event; raw `events` row count never changes
- [x] 7.2 `DataEraser.Verify` over PG events is asserted in `TestE2E_GDPR_EraseOnPostgres` (5.1: `ResidualRecoverable` empty after shred)
- [x] 7.3 `Anonymizer` determinism + one-way + scope separation

## 8. Good-to-have — Serialization & versioning E2E (`serialization-versioning-e2e`)

- [x] 8.1 `e2e_serialization_versioning_test.go`: store a v1 event in PG, register an `UpcasterChain`, assert `Load` upcasts to v2, the stored row stays v1 (history not rewritten), and new appends stamp the latest `$schema_version`
- [x] 8.2 Schema-version gap → `ErrSchemaVersionGap` surfaced on `Load` (not malformed data)
- [x] 8.3 msgpack event round-trips through an event store and reloads as its concrete type (in-memory adapter). **Finding:** the PostgreSQL adapter's JSONB `data` column rejects non-JSON (msgpack/protobuf) bodies — a real constraint, now documented by `TestE2E_Serialization_MsgpackRejectedByPGJSONB`; a binary-serializer-on-PG path (BYTEA column or early rejection) is a separate change. Protobuf is not added (same JSONB constraint; the msgpack case already establishes the round-trip + the PG limit).

## 9. Cross-cutting

- [x] 9.1 All suites are gated by `testing.Short()` + their env var(s) and self-skip cleanly; the `-short` unit matrix stays green everywhere
- [x] 9.2 No source/runtime changes landed; the only non-test addition is the `StartKafka` helper under `testing/` (Non-Goal preserved)
- [x] 9.3 `make test` / `make test-e2e` (infra up) run the required suites; cloud-provider (KMS/Vault/SNS) suites remain opt-in and are not required in CI; PRs target `develop`
