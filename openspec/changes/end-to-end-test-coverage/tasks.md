## 1. Required — E2E test harness (`e2e-test-harness`)

- [ ] 1.1 Add `StartKafka(t *testing.T) *KafkaContainer` to `testing/containers` (reads `TEST_KAFKA_BROKERS`, self-skips when unset/unreachable/`-short`); expose broker address + isolated-topic create/consume helpers
- [ ] 1.2 Add a shared PostgreSQL-backed E2E fixture (extend `NewFullStackTest` / add `E2EStore`) that builds a real `EventStore` on an isolated schema and optionally wires a `ProjectionEngine`, `SagaManager`, and `OutboxProcessor`; drop schema on cleanup; self-skip without `TEST_DATABASE_URL`
- [ ] 1.3 Ensure `docker-compose.test.yml` provides Kafka (already present) and confirm CI exports `TEST_KAFKA_BROKERS`; add a `make test-e2e` target that runs the infra-gated suites
- [ ] 1.4 Document the E2E suite in `AGENTS.md`/`CONTRIBUTING.md`: required env vars per suite and the self-skip behavior
- [ ] 1.5 Harness self-tests: `StartKafka` skips cleanly when unset; the fixture creates+drops an isolated schema and never leaks it

## 2. Required — Outbox delivery E2E (`outbox-delivery-e2e`)

- [ ] 2.1 `e2e_outbox_delivery_test.go`: `AppendWithOutbox` on PG → real `OutboxProcessor` → `outbox/webhook` publisher (httptest); assert delivery + `MarkCompleted`
- [ ] 2.2 Same flow with the `outbox/kafka` publisher; consume the message back from `TEST_KAFKA_BROKERS` and assert payload/headers + completion
- [ ] 2.3 Failure path: publisher returns error → retries → dead-letter; assert the `events` row is never mutated/removed (at-least-once + append-only)
- [ ] 2.4 (good-to-have) SNS via LocalStack (`TEST_SNS_ENDPOINT`), self-skipping when unset

## 3. Required — Projection pipeline E2E (`projection-pipeline-e2e`)

- [ ] 3.1 `e2e_projection_pipeline_test.go`: dispatch a command through the bus (validation + idempotency + correlation middleware) → handler appends to PG → async/live `ProjectionEngine` off a real PG subscription → assert read model updated + checkpoint advanced
- [ ] 3.2 Idempotency: dispatch the same command twice → events appended once, projection applied once
- [ ] 3.3 Crash-recovery: process to position N, restart the async worker → resumes from checkpoint (no replay-from-0)
- [ ] 3.4 Poison event: force an apply failure → `OnPoisonEvent` receives the applied-batch event; worker advances/faults per policy without spinning

## 4. Required — Encryption-at-rest E2E (`encryption-at-rest-e2e`)

- [ ] 4.1 `e2e_encryption_at_rest_test.go` (provider-parameterized, default `encryption/local`): save encrypted aggregate to PG, read raw `data` via SQL and assert ciphertext (no plaintext), then `LoadAggregate` decrypts
- [ ] 4.2 Zero-overhead control: same flow without encryption stores/loads unchanged, no encryption metadata added
- [ ] 4.3 (good-to-have) Run the same suite against KMS (`AWS_*`+`MINK_KMS_TEST_KEY_ID`) and Vault (`VAULT_*`+`MINK_VAULT_TEST_KEY`), env-gated; revoke through `DataEraser`/`Revocable` → `Load` cannot decrypt

## 5. Required — GDPR lifecycle E2E (`gdpr-lifecycle-e2e`)

- [ ] 5.1 `e2e_gdpr_lifecycle_test.go`: populate PG with subject-tagged encrypted events + PG read models + PG sibling stores (audit/saga/outbox/idempotency); run `DataEraser.Erase`; assert key revoked, `Load` redacted, read models redacted, sibling stores purged, marker appended, `Verify` → PII-free `ErasureCertificate`
- [ ] 5.2 Append-only + idempotency: re-run `Erase` → no-op, no duplicate marker; assert raw `events` row count + global positions identical before/after
- [ ] 5.3 Shared-key guard: a key shared with another subject is refused without `AllowSharedKeyRevocation` (`SharedKeyError`/partial), other subject stays recoverable
- [ ] 5.4 `DataExporter.Export`/`ExportStream` over a real PG stream (stream + scan) with mixed plaintext/encrypted/shredded events; assert filters applied and shredded events `Redacted=true`, `Data=nil`
- [ ] 5.5 (good-to-have) `mink gdpr discover`/`verify` over the populated PG footprint; assert read-only output + that the CLI performs no revocation

## 6. Good-to-have — Saga → outbox → publisher E2E (`saga-outbox-e2e`)

- [ ] 6.1 `e2e_saga_outbox_test.go`: `SagaManager` reacting to PG-stored events → emitted commands persisted + enqueued → real publisher delivers; assert saga completes and messages delivered
- [ ] 6.2 Compensation: force a downstream failure → compensation runs, saga recorded compensated/failed; no appended event rewritten
- [ ] 6.3 Graceful shutdown mid-dispatch (context cancel) leaves the saga `Running` for restart (no compensation on a dead context)

## 7. Good-to-have — Governance & retention E2E (`governance-retention-e2e`)

- [ ] 7.1 `e2e_governance_test.go`: `RetentionManager` over PG with Shred/RedactFields/Anonymize policies + dry-run; assert report + read-side action, raw `events` never rewritten
- [ ] 7.2 `DataEraser.Verify` over PG events + read models → detects residual PII / certifies clean
- [ ] 7.3 `Anonymizer` determinism + irreversibility over PG-sourced values

## 8. Good-to-have — Serialization & versioning E2E (`serialization-versioning-e2e`)

- [ ] 8.1 `e2e_versioning_pg_test.go`: store v1 events in PG, register an `UpcasterChain`, assert `Load`/`LoadAggregate` upcasts, stored rows stay v1, new appends stamp latest `$schema_version`
- [ ] 8.2 Schema-version gap → typed upcast/schema error surfaced (not malformed data)
- [ ] 8.3 msgpack + protobuf event bodies round-trip through PG storage and reload as the correct concrete type; schema-registry compatibility over stored streams

## 9. Cross-cutting

- [ ] 9.1 All suites are gated by `testing.Short()` + their env var(s) and self-skip cleanly; the `-short` unit matrix stays green everywhere
- [ ] 9.2 No source/runtime changes land in this change; any defect an E2E test surfaces is filed and fixed as a separate change (Non-Goal)
- [ ] 9.3 `make test` (infra up) runs the required suites; cloud-provider (KMS/Vault/SNS) suites remain opt-in and are not required in CI; PRs target `develop`
