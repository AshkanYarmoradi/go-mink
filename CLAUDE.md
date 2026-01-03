# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Summary

go-mink is an Event Sourcing and CQRS library for Go (like MartenDB for .NET).

## Key Commands

```bash
# Run tests
go test -short ./...                    # Unit tests only
go test ./...                           # All tests (needs PostgreSQL)
go test -v -run TestName ./pkg/...      # Run a single test

# Format and lint
go fmt ./...
golangci-lint run

# Start test database
docker run -d --name mink-pg -e POSTGRES_PASSWORD=mink -p 5432:5432 postgres:16
export TEST_DATABASE_URL="postgres://postgres:mink@localhost:5432/postgres?sslmode=disable"
```

## Architecture

```
Commands → Command Bus → Aggregate → Events → Event Store
                                                    ↓
                                           Projection Engine
                                                    ↓
                                             Read Models → Queries
```

## Key File Locations

- Core types: `event.go`, `aggregate.go`, `command.go`, `projection.go`
- Event store: `store.go`
- Command bus: `bus.go`, `handler.go`, `middleware.go`
- Adapters: `adapters/postgres/postgres.go`, `adapters/memory/memory.go`
- Idempotency: `idempotency.go`, `adapters/*/idempotency.go`
- Subscriptions: `subscription.go`, `adapters/postgres/subscription.go`
- Errors: `errors.go`, `adapters/adapter.go` (sentinel errors)
- Testing utilities: `testing/bdd/`, `testing/assertions/`, `testing/projections/`, `testing/sagas/`, `testing/containers/`
- Observability: `middleware/metrics/`, `middleware/tracing/`
- Serializers: `serializer/msgpack/`

## Core Interfaces to Know

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

// Optional subscription support (adapters/adapter.go)
type SubscriptionAdapter interface {
    LoadFromPosition(ctx context.Context, fromPosition uint64, limit int) ([]StoredEvent, error)
    SubscribeAll(ctx context.Context, fromPosition uint64, opts ...SubscriptionOptions) (<-chan StoredEvent, error)
    SubscribeStream(ctx context.Context, streamID string, fromVersion int64, opts ...SubscriptionOptions) (<-chan StoredEvent, error)
    SubscribeCategory(ctx context.Context, category string, fromPosition uint64, opts ...SubscriptionOptions) (<-chan StoredEvent, error)
}

// Domain models implement this
type Aggregate interface {
    AggregateID() string
    AggregateType() string
    Version() int64
    ApplyEvent(event interface{}) error
    UncommittedEvents() []interface{}
    ClearUncommittedEvents()
}
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
3. **Options pattern**: `func New(opts ...Option)`
4. **Sentinel errors**: `var ErrNotFound = errors.New("mink: not found")`
5. **Typed errors**: Implement `Is()` for `errors.Is()` compatibility

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

// Event assertions (testing/assertions package)
assertions.AssertEventTypes(t, events, "OrderCreated", "ItemAdded")
assertions.AssertEventsEqual(t, expected, actual)

// Test containers (testing/containers package)
container := containers.StartPostgres(t)
db := container.MustDB(ctx)
```

## Current Development Phase

**Phase 5 (v0.5.0)**: Security & Advanced Patterns - NEXT
- Saga / Process Manager implementation
- Outbox pattern
- Event versioning & upcasting
- Field-level encryption
- GDPR compliance (crypto-shredding)

**Completed phases**:
- Phase 1 (v0.1.0): Event types, Aggregate, EventStore, PostgreSQL/Memory adapters
- Phase 2 (v0.2.0): Command Bus, handlers, middleware (Validation, Recovery, Idempotency, Correlation)
- Phase 3 (v0.3.0): Projections (`InlineProjection`, `AsyncProjection`, `LiveProjection`), `ProjectionEngine`, subscriptions
- Phase 4 (v0.4.0): Testing utilities (`testing/bdd`, `testing/assertions`, `testing/projections`, `testing/sagas`, `testing/containers`), observability (`middleware/metrics`, `middleware/tracing`), MessagePack serializer

## Don't Do

- Don't mutate events after storage
- Don't use `panic()` for recoverable errors
- Don't skip context propagation
- Don't use global state
- Don't break public API without version bump

## Documentation

See `docs/` folder and `AGENTS.md` for comprehensive details.
