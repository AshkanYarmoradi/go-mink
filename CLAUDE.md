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

## Core Interfaces to Know

```go
// Adapters implement this
type EventStoreAdapter interface {
    Append(ctx context.Context, streamID string, events []EventData, expectedVersion int64) ([]StoredEvent, error)
    Load(ctx context.Context, streamID string, fromVersion int64) ([]StoredEvent, error)
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

// BDD for aggregates
Given(t, aggregate, previousEvents...).
    When(command).
    Then(expectedEvents...)
```

## Current Development Phase

**Phase 3 (v0.3.0)**: Projections - IN PROGRESS
- Projection interfaces: `InlineProjection`, `AsyncProjection`, `LiveProjection`
- `ProjectionEngine` with worker pool
- Checkpoint management and rebuilding
- Event subscriptions (`SubscribeAll`, `SubscribeStream`, `SubscribeCategory`)

**Completed phases**:
- Phase 1: Event types, Aggregate, EventStore, PostgreSQL/Memory adapters
- Phase 2: Command Bus, handlers, middleware (Validation, Recovery, Idempotency, Correlation)

## Don't Do

- Don't mutate events after storage
- Don't use `panic()` for recoverable errors
- Don't skip context propagation
- Don't use global state
- Don't break public API without version bump

## Documentation

See `docs/` folder and `AGENTS.md` for comprehensive details.
