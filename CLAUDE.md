# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Summary

go-mink is an Event Sourcing and CQRS library for Go (inspired by MartenDB for .NET). Instead of storing current state, it stores all changes as immutable events and rebuilds state by replaying them.

## Key Commands

```bash
# Preferred: use Makefile targets (infrastructure managed via docker-compose.test.yml)
make test-unit                          # Unit tests only (no infra needed, uses -short -race)
make test                               # All tests (auto-starts PostgreSQL via docker-compose)
make lint                               # golangci-lint run ./...
make fmt                                # go fmt ./...
make test-coverage                      # Coverage report (excludes examples/ and testing/)

# Infrastructure management
make infra-up                           # Start test PostgreSQL (docker-compose.test.yml)
make infra-down                         # Stop test infrastructure
make clean                              # Stop infra + remove coverage artifacts + clear caches

# Running a single test (infra must be up for integration tests)
TEST_DATABASE_URL="postgres://postgres:postgres@localhost:5432/mink_test?sslmode=disable" \
  go test -v -run TestName ./path/to/pkg/...

# Integration tests skip themselves when:
#   - testing.Short() is true (via -short flag)
#   - TEST_DATABASE_URL env var is not set
```

## Architecture

```
Commands --> Command Bus (middleware pipeline) --> Handler --> Aggregate --> Events --> Event Store
                                                                                          |
                                                                              Projection Engine
                                                                                          |
                                                                                    Read Models
                                                                          |
                                                                     Saga Manager
                                                                    (compensation)
```

**Data flow**: Commands are dispatched through the bus with middleware (validation, idempotency, correlation, recovery). Handlers load aggregates from the event store, execute domain logic, and persist uncommitted events. The projection engine subscribes to stored events and updates read models. Sagas orchestrate long-running workflows with compensation.

## Key File Locations

Root package (all core types are in the root `mink` package):
- `event.go` - Event types, StreamID, Metadata
- `aggregate.go` - Aggregate interface and AggregateBase
- `store.go` - EventStore (main entry point: Append, Load, SaveAggregate, LoadAggregate)
- `command.go` - Command interface, CommandBase, AggregateCommand, IdempotentCommand
- `bus.go` - CommandBus with middleware pipeline
- `handler.go` - CommandHandler and handler registry
- `middleware.go` - Middleware implementations (Validation, Recovery, Idempotency, Correlation, Causation, Tenant)
- `idempotency.go` - IdempotencyConfig and middleware setup
- `projection.go` - Projection interfaces (Inline, Async, Live), ProjectionBase helpers
- `projection_engine.go` - ProjectionEngine with worker pool, checkpoints, resilience
- `rebuilder.go` - ProjectionRebuilder for rebuilding from scratch
- `subscription.go` - Event subscription management
- `saga.go` - Saga interface, SagaBase, SagaCorrelation
- `saga_manager.go` - SagaManager orchestration with compensation and retry
- `repository.go` - Repository pattern for aggregates
- `serializer.go` - Serializer interface and JSON implementation
- `errors.go` - All sentinel errors and typed errors

Adapters:
- `adapters/adapter.go` - All adapter interfaces and shared types (EventStoreAdapter, SubscriptionAdapter, SagaStore, etc.)
- `adapters/postgres/` - PostgreSQL adapter (postgres.go, subscription.go, idempotency.go, saga_store.go, readmodel.go)
- `adapters/memory/` - In-memory adapter for testing (memory.go, checkpoint.go, idempotency.go, saga_store.go)

Other packages:
- `middleware/metrics/` - Prometheus metrics middleware
- `middleware/tracing/` - OpenTelemetry tracing middleware
- `serializer/msgpack/` - MessagePack serializer
- `serializer/protobuf/` - Protocol Buffers serializer
- `testing/bdd/` - BDD-style aggregate testing (Given/When/Then)
- `testing/assertions/` - Event assertion helpers
- `testing/projections/` - Projection testing utilities
- `testing/sagas/` - Saga testing utilities
- `testing/containers/` - PostgreSQL Docker test containers
- `cli/commands/` - CLI tool (init, generate, migrate, projection, stream, diagnose, schema)
- `examples/` - 13 example projects

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
- Completed: Saga / Process Manager, CLI tool
- Remaining: Outbox pattern, event versioning & upcasting, field-level encryption, GDPR compliance

## Don't Do

- Don't mutate events after storage
- Don't use `panic()` for recoverable errors
- Don't skip context propagation
- Don't use global state
- Don't break public API without version bump
