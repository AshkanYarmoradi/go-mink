# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Summary

go-mink is an Event Sourcing and CQRS library for Go (inspired by MartenDB for .NET). Instead of storing current state, it stores all changes as immutable events and rebuilds state by replaying them.

## Key Commands

```bash
# Preferred: use Makefile targets (infrastructure managed via docker-compose.test.yml)
make test-unit                          # Unit tests only (no infra needed, uses -short -race)
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
```

**CI enforces 90% code coverage** (excludes `examples/` and `testing/`). Go version compatibility: go.mod targets 1.25, CI tests Go 1.22–1.26 on Linux, macOS, Windows.

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

All core types live in the root `mink` package. Key entry points: `store.go` (EventStore), `bus.go` (CommandBus), `projection_engine.go` (ProjectionEngine), `saga_manager.go` (SagaManager), `outbox_processor.go` (OutboxProcessor), `errors.go` (all sentinel/typed errors).

Adapters:
- `adapters/adapter.go` - All adapter interfaces and shared types (EventStoreAdapter, SubscriptionAdapter, SagaStore, OutboxStore, OutboxAppender, HealthChecker, Migrator, plus CLI adapters: StreamQueryAdapter, DiagnosticAdapter, SchemaProvider)
- `adapters/postgres/` - PostgreSQL adapter (production)
- `adapters/memory/` - In-memory adapter (testing)

Other packages:
- `outbox/{webhook,kafka,sns}/` - Outbox publishers
- `middleware/{metrics,tracing}/` - Prometheus metrics, OpenTelemetry tracing
- `serializer/{msgpack,protobuf}/` - Alternative serializers
- `testing/{bdd,assertions,projections,sagas,containers}/` - Test utilities
- `cli/commands/` - CLI tool (init, generate, migrate, projection, stream, diagnose, schema)
- `examples/` - Example projects

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

## Current Development Phase

**Phase 5 (v0.5.0)**: Security & Advanced Patterns - IN PROGRESS
- Completed: Saga / Process Manager, CLI tool, Outbox pattern (stores, processor, publishers, metrics)
- Remaining: Event versioning & upcasting, field-level encryption, GDPR compliance

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

## Don't Do

- Don't mutate events after storage
- Don't use `panic()` for recoverable errors
- Don't skip context propagation
- Don't use global state
- Don't break public API without version bump
- Don't add dependencies without discussion
