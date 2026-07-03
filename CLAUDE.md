# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Summary

go-mink is an Event Sourcing and CQRS library for Go (inspired by MartenDB for .NET). Instead of storing current state, it stores all changes as immutable events and rebuilds state by replaying them.

**Module path**: `go-mink.dev`

> **Deeper contributor guide**: `AGENTS.md` (repo root) is the full developer handbook — implementation guidelines with code examples, testing approach, and PR/commit conventions; `.github/copilot-instructions.md` mirrors the essentials. This file is the quick-orientation layer; reach for `AGENTS.md` when you need depth.

## Key Commands

```bash
# Preferred: use Makefile targets (infrastructure managed via docker-compose.test.yml)
make build                              # go build -v ./...
make test-unit                          # Unit tests only (no infra, CGO_ENABLED=0, works everywhere)
make test-unit-race                     # Unit tests with race detector (requires gcc/clang)
make test                               # All tests (auto-starts PostgreSQL + Kafka via docker-compose)
make lint                               # golangci-lint run ./... (pinned v2.12.2, auto-installed into ./bin; no .golangci.yml, uses defaults)
make fmt                                # go fmt ./...
make test-coverage                      # Coverage report (excludes examples/ and testing/)

# Infrastructure management
make infra-up                           # Start test PostgreSQL + Kafka (docker-compose.test.yml)
make infra-down                         # Stop test infrastructure
make clean                              # Stop infra + remove coverage artifacts + clear caches

# Running a single test (infra must be up for integration tests)
TEST_DATABASE_URL="postgres://postgres:postgres@localhost:5432/mink_test?sslmode=disable" \
  TEST_KAFKA_BROKERS="localhost:9092" \
  go test -v -run TestName ./path/to/pkg/...

# Integration tests skip themselves when:
#   - testing.Short() is true (via -short flag)
#   - TEST_DATABASE_URL env var is not set (for Postgres tests)
#   - TEST_KAFKA_BROKERS env var is not set (for Kafka tests)

# Benchmarks
make benchmark                  # Full benchmark suite + 1M-event scale tests (memory, no infra)
make benchmark-quick            # Go benchmarks only (fast, no infra)
make benchmark-adapters         # Memory adapter benchmarks (no infra)
make benchmark-adapters-pg      # PostgreSQL adapter benchmarks (requires infra)
```

**CI enforces 90% code coverage** (excludes `examples/` and `testing/`). Go version: go.mod targets 1.25. CI runs coverage tests on the go.mod version (1.25, ubuntu), lints on Go 1.25, and builds + runs `-short` unit tests on Go 1.25 & 1.26 across Linux, macOS, Windows. Scale tests enabled in CI via `MINK_SCALE_TESTS=1`; SonarQube Cloud analysis runs after the test job. The lint job also fails on unformatted code (`go fmt ./...`).

**Branching & Releases**: Feature branches target `develop`. Each merge to `develop` auto-creates an RC pre-release (`v1.0.4-rc.1`, `v1.0.4-rc.2`, ...). Merging `develop` into `main` auto-creates the next stable release (`v1.0.4`). CI (test/lint/build) runs on both `main` and `develop`. Documentation deploys to GitHub Pages on push to `main` when `website/**` changes (via `.github/workflows/docs.yml`).

## Architecture

```
Commands --> Command Bus (middleware pipeline) --> Handler --> Aggregate --> Events --> Event Store
                                                                                          |
                                                                                +---------+---------+
                                                                                |                   |
                                                                      Projection Engine       Saga Manager
                                                                                |            (compensation)
                                                                          Read Models               |
                                                                                            Outbox Processor
                                                                                                    |
                                                                                     External Systems (Kafka, Webhooks, SNS)
```

**Data flow**: Commands are dispatched through the bus with middleware (validation, idempotency, correlation, recovery). Handlers load aggregates from the event store, execute domain logic, and persist uncommitted events. The projection engine subscribes to stored events and updates read models. Sagas orchestrate long-running workflows with compensation. The outbox processor reliably publishes events to external systems (Kafka, webhooks, SNS) with retry and dead-letter support.

## Key File Locations

All core types live in the root `mink` package. Key entry points: `store.go` (EventStore), `bus.go` (CommandBus), `projection_engine.go` (ProjectionEngine), `saga_manager.go` (SagaManager), `outbox_processor.go` (OutboxProcessor), `errors.go` (all sentinel/typed errors), `versioning.go` (Upcaster, UpcasterChain, schema version helpers), `versioning_errors.go` (versioning sentinel/typed errors), `upcasting_serializer.go` (UpcastingSerializer decorator), `schema_registry.go` (SchemaRegistry, compatibility checking), `encryption.go` (FieldEncryptionConfig, encrypt/decrypt logic, metadata helpers), `encryption_errors.go` (encryption error aliases).

Data-governance / GDPR files (also root `mink` package — see the [Data Governance & GDPR](#data-governance--gdpr-articles-151720) section for how they fit together): `export.go`/`export_errors.go` (DataExporter, Art. 15/20), `eraser.go`/`eraser_errors.go` (DataEraser, Art. 17), `subject.go`+`subjectindex.go`+`subjecterasure.go`+`subjecterasers.go` (subject tagging/resolution/index + sibling-store erasers), `redaction.go` (read-model redaction), `sideeffects.go` (external-PII erasure hooks), `retention.go` (RetentionManager/Policy), `anonymize.go` (Anonymizer), `verify.go` (erasure verification + ErasureCertificate), `keylifecycle.go` (ReEncryptStream, key rotation), `audit.go` (AuditMiddleware/AuditStore).

Adapters:
- `adapters/adapter.go` - All adapter interfaces and shared types (EventStoreAdapter, SubscriptionAdapter, SagaStore, OutboxStore, OutboxAppender, HealthChecker, Migrator, plus CLI adapters: StreamQueryAdapter, DiagnosticAdapter, SchemaProvider)
- `adapters/postgres/` - PostgreSQL adapter (production)
- `adapters/memory/` - In-memory adapter (testing)

Other packages:
- `encryption/` - Provider interface (+ optional `Revocable`/`RecoverableRevocable`/`StatefulRevocable` for crypto-shredding), types, sentinel/typed errors
- `encryption/local/` - AES-256-GCM provider (testing/development)
- `encryption/kms/` - AWS KMS provider (production)
- `encryption/vault/` - HashiCorp Vault Transit provider (production)
- `outbox/{webhook,kafka,sns}/` - Outbox publishers
- `middleware/{metrics,tracing}/` - Prometheus metrics, OpenTelemetry tracing
- `serializer/{msgpack,protobuf}/` - Alternative serializers
- `testing/{bdd,assertions,projections,sagas,containers}/` - Test utilities
- `cli/commands/` - CLI tool `mink` (init, generate, migrate, projection, stream, diagnose, schema, gdpr, version); `cli/{ui,config,styles}/` - TUI (bubbletea/huh), config, styling
- `examples/` - Runnable example projects (basic, cqrs, cqrs-postgres, projections, sagas, versioning, encryption, export, audit, metrics, tracing, msgpack, protobuf, bdd-testing, full-ecommerce)

Documentation website:
- `website/` - Docusaurus 3 documentation site (Node.js 18+, TypeScript)
- `website/docs/` - All documentation content as MDX files
- `website/src/` - React components (landing page, custom theme)
- `website/static/` - Static assets (images, SVGs)
- `website/docusaurus.config.ts` - Site configuration, redirects, navbar, footer
- `website/sidebars.ts` - Sidebar navigation structure (docs, tutorial, blogSeries)

```bash
# Website development
cd website
npm install                    # Install dependencies
npm start                      # Dev server at localhost:3000
npm run build                  # Production build to website/build/
npm run typecheck              # TypeScript type checking
```

## Core Interfaces

```go
// Adapters implement this (adapters/adapter.go)
type EventStoreAdapter interface {
    Append(ctx context.Context, streamID string, events []EventRecord, expectedVersion int64) ([]StoredEvent, error)
    Load(ctx context.Context, streamID string, fromVersion int64) ([]StoredEvent, error)
    GetStreamInfo(ctx context.Context, streamID string) (*StreamInfo, error)
    GetLastPosition(ctx context.Context) (uint64, error)
    Initialize(ctx context.Context) error
    Close() error
}

// Optional interfaces adapters can implement:
// - SubscriptionAdapter: real-time event streaming (SubscribeAll, SubscribeStream, SubscribeCategory)
// - SnapshotAdapter: aggregate snapshots
// - TransactionalAdapter: atomic operations via BeginTx
// - CheckpointAdapter: projection checkpoint management
// - IdempotencyStore: command deduplication
// - SagaStore: saga persistence (Save, Load, FindByCorrelationID, FindByType, Delete)
// - OutboxStore: outbox message persistence (Schedule, FetchPending, MarkCompleted, etc.)
// - OutboxAppender: atomic event+outbox writes (AppendWithOutbox)
// - StreamQueryAdapter, DiagnosticAdapter, SchemaProvider: CLI tool support

// Domain models embed AggregateBase and implement ApplyEvent
type Aggregate interface {
    AggregateID() string
    AggregateType() string
    Version() int64
    ApplyEvent(event interface{}) error
    UncommittedEvents() []interface{}
    ClearUncommittedEvents()
}
```

## Error Handling Pattern

Sentinel errors are defined in two places with aliasing:
- `adapters/adapter.go` defines storage-level errors (ErrConcurrencyConflict, ErrStreamNotFound, ErrEmptyStreamID, etc.)
- `errors.go` aliases adapter errors and adds domain-level errors (ErrHandlerNotFound, ErrValidationFailed, ErrProjectionFailed, etc.)

Typed errors implement `Is()` for `errors.Is()` compatibility:
```go
// Sentinel: errors.Is(err, mink.ErrConcurrencyConflict)
// Typed: err.(*mink.ConcurrencyError).StreamID for details
// Also: StreamNotFoundError, SerializationError, HandlerNotFoundError, PanicError, ProjectionError
// Versioning: ErrUpcastFailed, ErrSchemaVersionGap, ErrIncompatibleSchema, ErrSchemaNotFound
// Typed: UpcastError, SchemaVersionGapError, IncompatibleSchemaError
// Encryption: ErrEncryptionFailed, ErrDecryptionFailed, ErrKeyNotFound, ErrKeyRevoked, ErrProviderClosed
// Typed: EncryptionError, KeyNotFoundError, KeyRevokedError (all with Is(), Unwrap())
// Export (export_errors.go): ErrExportFailed, ErrExportPartialFootprint, ErrExportScanNotSupported, ErrNoExportSources, ErrSubjectIDRequired; Typed: ExportError
// Erasure (eraser_errors.go): ErrErasureFailed, ErrErasureNotConfigured, ErrErasureScanNotSupported, ErrErasureSubjectRequired, ErrNoErasureSources, ErrSharedKeyRevocation; Typed: ErasureError, SharedKeyError
```

## Version Constants

```go
AnyVersion   = -1  // Skip version check
NoStream     = 0   // Stream must not exist
StreamExists = -2  // Stream must exist
```

## Code Style Rules

1. **Context first**: `func Foo(ctx context.Context, ...)`
2. **Error last**: `func Foo(...) (Result, error)`
3. **Options pattern**: `func New(opts ...Option)` with `type Option func(*T)`
4. **Sentinel errors**: `var ErrXxx = errors.New("mink: ...")` prefixed with "mink:"
5. **Typed errors**: Implement `Is()` and `Unwrap()` for `errors.Is()` compatibility
6. **Compile-time interface checks**: `var _ mink.EventStoreAdapter = (*MyAdapter)(nil)`
7. **Naming**: Interfaces with single method use `-er` suffix (`Serializer`, `Subscriber`). Constructors use `NewXxx()`. Test functions: `TestXxx_Method_Scenario`.

## Testing Patterns

Uses `github.com/stretchr/testify` (`assert` and `require`) for assertions.

```go
// Table-driven tests
func TestFoo(t *testing.T) {
    tests := []struct {
        name    string
        input   Input
        want    Output
        wantErr error
    }{...}
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {...})
    }
}

// BDD for aggregates (testing/bdd package)
bdd.Given(t, aggregate, previousEvents...).
    When(func() error { return aggregate.DoSomething() }).
    Then(expectedEvents...)
// or: .ThenError(expectedErr)

// Integration tests skip pattern
if testing.Short() { t.Skip("Skipping integration test") }
connStr := os.Getenv("TEST_DATABASE_URL")
if connStr == "" { t.Skip("TEST_DATABASE_URL not set") }

// Test containers
container := containers.StartPostgres(t)
db := container.MustDB(ctx)
```

## Event Versioning & Upcasting

Schema version is stored in `Metadata.Custom["$schema_version"]` — no DB column changes needed. Absent key defaults to version 1 (backward compatible with all existing events). See `versioning.go` for the `Upcaster` interface and `UpcasterChain`.

Key design decisions:
- Upcasters operate on raw `[]byte` — serializer-agnostic, `ToVersion()` must equal `FromVersion() + 1`
- `EventStore.upcasters` is `nil` by default — all code paths short-circuit with zero overhead
- Events are automatically upcasted during `Load`/`LoadAggregate` and stamped with latest version during `Append`/`SaveAggregate`
- Decrypt before upcast ordering: encrypted fields decrypted before upcasters run

## Field-Level Encryption

Encryption metadata stored in `Metadata.Custom` with `$`-prefixed keys (`$encrypted_fields`, `$encryption_key_id`, `$encrypted_dek`, `$encryption_algorithm`). No DB schema changes needed. See `encryption.go` for `FieldEncryptionConfig` and `encryption/provider.go` for the `Provider` interface.

Key design decisions:
- Envelope encryption: 1 provider call per event (GenerateDataKey), local AES-256-GCM per field
- `FieldEncryptionConfig` is `nil` by default — all code paths short-circuit with zero overhead
- Providers: `encryption/local` (AES-256-GCM, testing), `encryption/kms` (AWS KMS), `encryption/vault` (Vault Transit)
- DEK plaintext zeroed after use via `ClearBytes()`
- Master-key selection precedence: `Metadata.TenantID` → first `$subjects` tag (recorded by `WithSubjectTagger`) → `defaultKeyID`. `WithTenantKeyResolver` / `WithSubjectKeyResolver` (aliases, last-applied wins) pick the key. Each event stamps the key id it was wrapped under, so decryption and key rotation need no migration.
- GDPR via crypto-shredding: revoke a key (via `encryption.Revocable`) to make data encrypted under it permanently unrecoverable. **Blast radius = everything under that key** — per-subject keys make a single subject shreddable; per-tenant keys shred the whole tenant. See Data Governance below.

## Data Governance & GDPR (Articles 15/17/20)

A cohesive subsystem in the root `mink` package (the current focus of `develop` — see `openspec/changes/`). Every piece is opt-in and zero-overhead when unconfigured. The unifying idea: **crypto-shredding** — a subject's data is erased by revoking the encryption key that wraps it, *never* by mutating the append-only event log.

- **Export (Art. 15/20)** — `DataExporter` (`export.go`): `Export`/`ExportStream` collect a subject's events (stream-based or scan-based via `SubscriptionAdapter`); crypto-shredded events return `Redacted=true` with `nil` Data. Filters: `FilterByTenantID`/`FilterByUserID`/`FilterByStreamPrefix`/`FilterByMetadata`/`FilterByEventTypes`/`CombineFilters`.
- **Erasure (Art. 17)** — `DataEraser` (`eraser.go`): one call revokes keys, redacts read models, runs external-PII hooks, appends an optional marker event, and emits a verified `ErasureCertificate`. Idempotent; reports partial failures. Mirrors `DataExporter`'s subject-targeting model.
- **Subject targeting** — `SubjectTagger` + `WithSubjectTagger` (`subject.go`) records subject ids in `Metadata.Custom["$subjects"]` at append time (one shared hook covers Append/SaveAggregate/outbox); `SubjectResolver` resolves a subject's cross-stream footprint (`Partial` flag — completeness is never silently partial). `subjectindex.go` (`MemorySubjectIndex`, `SubjectIndexWriter`, PostgreSQL `mink_subject_index` + drift-free `StreamsBySubject` over JSONB) makes resolution O(subject's events); `BackfillSubjectIndex` migrates historical events. Indexes are **explicit-only** (`WithResolverIndex`), never auto-detected.
- **Sibling stores** — `SubjectErasable` seam (`subjecterasure.go`) + built-in erasers (`subjecterasers.go`) reach PII *derived* from events that crypto-shredding misses (plaintext audit trail, saga state, snapshots, outbox, idempotency). Register via `DataEraser.WithSubjectStore`; must target read-side stores only.
- **Read-model redaction** — `redaction.go`: `SubjectRedactable` (cheap in-place) preferred, else `ReadModelRebuilder` (rebuild over the now-shredded stream).
- **External PII** — `ErasureHook` (`sideeffects.go`): erase app-owned PII outside the store (blobs, caches, search indexes); failures reported, never fatal.
- **Retention** — `RetentionManager`/`RetentionPolicy` (`retention.go`): schedulable sweep with actions Shred / RedactFields / Anonymize, dry-run, and a report.
- **Anonymization** — `Anonymizer` (`anonymize.go`): deterministic, one-way pseudonymization (behind `ActionAnonymize`).
- **Verification** — `DataEraser.Verify` (`verify.go`): confirms no recoverable PII remains across events + read models; produces `VerificationReport` / PII-free `ErasureCertificate`.
- **Key lifecycle** — `ReEncryptStream` (`keylifecycle.go`): append-only re-encryption *by copy* after key rotation/compromise (erases nothing until the source is retired and old keys revoked).
- **Audit** — `AuditMiddleware`/`AuditStore` (`audit.go`): append-only `mink_audit` trail of every command (who/what/when/outcome); fail-open by default.
- **CLI** — `mink gdpr` (`cli/commands/gdpr.go`): `discover` / `verify` / `erase` / `retain`, read-only over the diagnostic adapter. The CLI does **not** hold the app's encryption keys, so actual revocation runs from the app via `DataEraser`/`RetentionManager`.

Guardrails: `WithSharedKeyGuard()` refuses to revoke a key shared across subjects unless `AllowSharedKeyRevocation()` (returns `SharedKeyError`); `WithStrictAccountability()` makes a lost marker/certificate fatal. Subject-scoped keys (Field-Level Encryption above) are the mechanism that keeps an erasure's blast radius to one subject.

## OpenSpec Workflow

Larger changes are spec-driven via OpenSpec. `openspec/config.yaml` holds project context and authoring rules (schema `spec-driven`); each change lives under `openspec/changes/<name>/` as `proposal.md` + `design.md` + `tasks.md` + delta `specs/`, and synced/archived capability specs under `openspec/specs/`. The recent GDPR erasure/retention/hardening work was authored this way. Use the `openspec-*` / `opsx:*` skills (propose, apply-change, sync-specs, archive-change, explore) to drive one. Repo-specific rules: proposals must include a **Non-Goals** subsection and split **What Changes** into Required vs Good-to-have; spec requirements are Go-API-shaped (interfaces, option funcs, typed errors), use SHALL/MUST, and each has a `#### Scenario` with WHEN/THEN including failure modes; preserve the append-only + zero-overhead-when-unused invariants.

## Current Version

v1.0.0 is the first stable release; all core event-sourcing features are complete. Active development on `develop` (RC pre-releases `v1.0.x-rc.N`) is building out the **v1.1.0 "Data Governance"** milestone — the GDPR export/erasure/retention subsystem above. See `CHANGELOG.md` `[Unreleased]` for exactly what has landed.

## PostgreSQL Schema

The adapter auto-creates a **two-table** core schema (`streams` + `events`),
plus `snapshots` and `checkpoints`. Table names default to `streams`/`events`
but are configurable. The `events` table's primary key is `global_position`
(`BIGSERIAL`), which also provides the total global ordering; `(stream_id,
version)` is the unique optimistic-concurrency constraint. See
`adapters/postgres/postgres.go` (`eventStoreSchemaStatements`) for the source of
truth. Outbox, idempotency, and saga tables are created separately by their
sub-stores' `Initialize`.

```sql
CREATE TABLE streams (
    id              BIGSERIAL PRIMARY KEY,
    stream_id       VARCHAR(500) NOT NULL UNIQUE,
    category        VARCHAR(250) NOT NULL,
    version         BIGINT NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE events (
    global_position BIGSERIAL PRIMARY KEY,
    stream_id       VARCHAR(500) NOT NULL,
    version         BIGINT NOT NULL,
    event_id        UUID NOT NULL DEFAULT gen_random_uuid(),
    event_type      VARCHAR(500) NOT NULL,
    data            JSONB NOT NULL,
    metadata        JSONB,
    timestamp       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(stream_id, version)
);
```

## Commit Conventions

```
component: brief description

Longer explanation if needed.

Closes #123
```

PR titles: `[component] Brief description` (e.g., `[eventstore] Add PostgreSQL adapter with optimistic concurrency`).

## Common Contributor Tasks

**Adding a new adapter**: Create package in `adapters/mydb/`, implement `EventStoreAdapter` (and optional interfaces like `SubscriptionAdapter`, `SagaStore`, etc.), add compile-time check `var _ adapters.EventStoreAdapter = (*MyAdapter)(nil)`, write integration tests with skip pattern, add benchmark suite using `adapters/common.go` shared helpers.

**Adding a new feature**: Write tests first. Implement in the root `mink` package or appropriate sub-package. Ensure 90%+ coverage. Update CHANGELOG.md `[Unreleased]`. For anything sizeable, draft an OpenSpec change first (see OpenSpec Workflow). Follow the house invariants: never rewrite history (append-only), zero overhead when the feature is unconfigured (nil-check short-circuit), and avoid DB-schema changes by storing metadata in `Metadata.Custom` with `$`-prefixed keys.

## Don't Do

- Don't mutate events after storage
- Don't use `panic()` for recoverable errors
- Don't skip context propagation
- Don't use global state
- Don't break public API without version bump
- Don't add dependencies without discussion
