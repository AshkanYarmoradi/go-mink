# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Summary

go-mink is an Event Sourcing and CQRS library for Go (inspired by MartenDB for .NET). Instead of storing current state, it stores all changes as immutable events and rebuilds state by replaying them.

**Module path**: `go-mink.dev`

## Key Commands

```bash
# Preferred: use Makefile targets (infrastructure managed via docker-compose.test.yml)
make build                              # go build -v ./...
make test-unit                          # Unit tests only (no infra, CGO_ENABLED=0, works everywhere)
make test-unit-race                     # Unit tests with race detector (requires gcc/clang)
make test                               # All tests (auto-starts PostgreSQL + Kafka via docker-compose)
make lint                               # golangci-lint run ./... (uses defaults, no .golangci.yml)
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

**CI enforces 90% code coverage** (excludes `examples/` and `testing/`). Go version: go.mod targets 1.25. CI runs tests with coverage on Go 1.24 (ubuntu), lints on Go 1.25, builds on Go 1.22–1.26 across Linux, macOS, Windows. Scale tests enabled in CI via `MINK_SCALE_TESTS=1`.

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

All core types live in the root `mink` package. Key entry points: `store.go` (EventStore), `bus.go` (CommandBus), `projection_engine.go` (ProjectionEngine), `saga_manager.go` (SagaManager), `outbox_processor.go` (OutboxProcessor), `export.go` (DataExporter, GDPR data export), `export_errors.go` (export sentinel/typed errors), `errors.go` (all sentinel/typed errors), `versioning.go` (Upcaster, UpcasterChain, schema version helpers), `versioning_errors.go` (versioning sentinel/typed errors), `upcasting_serializer.go` (UpcastingSerializer decorator), `schema_registry.go` (SchemaRegistry, compatibility checking), `encryption.go` (FieldEncryptionConfig, encrypt/decrypt logic, metadata helpers), `encryption_errors.go` (encryption error aliases).

Adapters:
- `adapters/adapter.go` - All adapter interfaces and shared types (EventStoreAdapter, SubscriptionAdapter, SagaStore, OutboxStore, OutboxAppender, HealthChecker, Migrator, plus CLI adapters: StreamQueryAdapter, DiagnosticAdapter, SchemaProvider)
- `adapters/postgres/` - PostgreSQL adapter (production)
- `adapters/memory/` - In-memory adapter (testing)

Other packages:
- `encryption/` - Provider interface, types, sentinel/typed errors
- `encryption/local/` - AES-256-GCM provider (testing/development)
- `encryption/kms/` - AWS KMS provider (production)
- `encryption/vault/` - HashiCorp Vault Transit provider (production)
- `outbox/{webhook,kafka,sns}/` - Outbox publishers
- `middleware/{metrics,tracing}/` - Prometheus metrics, OpenTelemetry tracing
- `serializer/{msgpack,protobuf}/` - Alternative serializers
- `testing/{bdd,assertions,projections,sagas,containers}/` - Test utilities
- `cli/commands/` - CLI tool (init, generate, migrate, projection, stream, diagnose, schema)
- `examples/` - Example projects (basic, versioning, projections, sagas, cqrs, metrics, tracing, encryption, export, etc.)

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
- GDPR via crypto-shredding: revoke a key to make tenant data permanently unrecoverable

## Current Version: v1.0.0 (Stable)

First stable release. All core features are complete.

## PostgreSQL Schema

Events table (auto-created by adapter):
```sql
CREATE TABLE mink_events (
    id UUID PRIMARY KEY,
    stream_id VARCHAR(255) NOT NULL,
    version BIGINT NOT NULL,
    type VARCHAR(255) NOT NULL,
    data JSONB NOT NULL,
    metadata JSONB DEFAULT '{}',
    global_position BIGSERIAL,
    timestamp TIMESTAMPTZ DEFAULT NOW(),
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

**Adding a new feature**: Write tests first. Implement in the root `mink` package or appropriate sub-package. Ensure 90%+ coverage. Update CHANGELOG.md.

## Don't Do

- Don't mutate events after storage
- Don't use `panic()` for recoverable errors
- Don't skip context propagation
- Don't use global state
- Don't break public API without version bump
- Don't add dependencies without discussion
