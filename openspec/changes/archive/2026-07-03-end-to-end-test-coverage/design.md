## Context

The 2026-07-03 coverage survey classified every feature area by unit / integration / true-E2E coverage. Findings:

- **Strong**: unit tests everywhere; PostgreSQL adapter integration (event store, subscriptions incl. the gapless watermark, saga store, outbox store, read-model store, snapshots, audit store, idempotency); env-gated Kafka publisher and KMS/Vault round-trip tests; a reusable provider contract kit (`encryption/providertest`).
- **Missing (the gaps this change closes)**:
  1. No test wires two real external systems together — the `OutboxProcessor` is only ever driven by a `mockPublisher`.
  2. The GDPR orchestrators (`DataExporter`/`DataEraser`/`RetentionManager`/`Verify`/redaction/anonymization) are memory-only; only the subject *index* and *snapshot eraser* touch PG.
  3. Encryption E2E is memory-only (ciphertext-at-rest in PG never asserted; KMS/Vault never chained through the store or `DataEraser`).
  4. The live/async projection engine has no PG integration (only *rebuild* does).
  5. SNS publisher has no integration test at all (mock only).

Existing harness to build on: `testing/containers` (`StartPostgres`, `NewIntegrationTest`, `NewFullStackTest`, per-test schema isolation, self-skip on `-short`/unreachable), the `testing/{bdd,assertions,projections,sagas}` helpers, `encryption/providertest`, and `examples/full-ecommerce` (a full aggregate+command+projection+saga app on PG that is *not* run as a test).

## Goals / Non-Goals

**Goals**
- One infrastructure-backed end-to-end suite per feature area, verifying the feature through the full stack against real PostgreSQL/Kafka (opt-in SNS/KMS/Vault).
- A shared, low-friction harness so each suite is a few lines of wiring, and every suite self-skips cleanly when its infra is absent.
- Prove the house invariants end-to-end: append-only (history never rewritten), crypto-shred blast radius, zero-overhead-when-unused, at-least-once outbox delivery.

**Non-Goals**
- No source/behavior changes; no new mandatory dependencies; no testcontainers-go in the default build; no change to the 90% coverage threshold.

## Decisions

### D1 — Reuse the "connect to already-running infra + self-skip" model; do not provision containers in-process
`testing/containers` deliberately connects to infra started out-of-band (`make infra-up` / CI services) and skips when unreachable, keeping the default build dependency-light. `StartKafka` follows the same contract as `StartPostgres`: read `TEST_KAFKA_BROKERS`, skip the test when unset/unreachable. SNS uses a LocalStack endpoint via env (`TEST_SNS_ENDPOINT`); KMS/Vault reuse their existing `AWS_*` / `VAULT_*` gates. This keeps `-short` unit runs green everywhere and matches the established pattern reviewers already know.

### D2 — One E2E suite per capability, `e2e_<area>_test.go`, in `package mink_test`
Consistent with the existing `e2e_*_test.go` files. Each suite constructs the shared fixture, exercises the real full flow, and asserts observable end-to-end outcomes (not internal state). Failure-mode scenarios (crash-recovery, compensation, shredded-redaction) are first-class subtests, not afterthoughts.

### D3 — Cross-infra is the headline; assert the delivery boundary, not the mock
The outbox suites assert the payload actually arrives at the external system: consume it back from Kafka (`TEST_KAFKA_BROKERS`), receive the webhook on an `httptest.Server`, or read the SNS message from LocalStack — then assert `MarkCompleted`. This is the first coverage of store → outbox → processor → real broker and is the highest-value target.

### D4 — Provider-parameterized encryption/erasure suites
The encryption-at-rest and GDPR suites take the `encryption.Provider` as a parameter and default to `encryption/local`. The same suite body runs against KMS/Vault when their env gates are set, so revocation-through-`DataEraser` is verified against a real KMS/Vault without duplicating the flow. This reuses `encryption/providertest` assertions where possible.

### D5 — Assert append-only against real storage
GDPR/retention suites assert, after erasure/redaction/retention, that the raw PG `events` rows are unchanged in count and position (crypto-shred works by key revocation + read-model redaction, never by mutating the log). This turns the core invariant into an executable check against real storage, which the memory-only tests cannot fully guarantee.

### D6 — Gating & CI
Suites are gated by `testing.Short()` + the relevant env var(s), so `make test-unit` never runs them and `make test` (infra up) does. A `make test-e2e` convenience target runs the infra-gated suites explicitly. LocalStack (SNS) and, optionally, Kafka are ensured in `docker-compose.test.yml`/CI; cloud-provider (KMS/Vault) suites stay opt-in and are not required in CI.

## Risks / Trade-offs

- **Flakiness from real async delivery.** Mitigation: poll with `require.Eventually` and bounded timeouts; consume from a fresh topic/group per test; isolate PG per-schema (already supported). No `time.Sleep`-based assertions.
- **CI runtime.** Mitigation: E2E suites run only in the infra-gated job (already present for integration); they are excluded from the fast `-short` matrix.
- **Test-induced coupling to infra versions.** Mitigation: assert behavior, not broker internals; pin service versions in `docker-compose.test.yml` (already done for PG/Kafka).
- **Surfacing latent product bugs.** Expected and desired — but any fix lands as a *separate* change (Non-Goal), so this change stays test-only and reviewable.

## Backward Compatibility

Purely additive and test-only. New `testing/containers` helpers are additive; no existing helper signature changes. No runtime code is touched, so the append-only, zero-overhead, and public-API contracts are unaffected. The default (`-short`) build and coverage gate are unchanged. PRs target `develop`.
