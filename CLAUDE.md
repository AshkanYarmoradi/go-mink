# CLAUDE.md - Instructions for Claude AI

This file provides guidance for Claude when working on the go-mink codebase.

## Project Summary

go-mink is an Event Sourcing and CQRS library for Go (like MartenDB for .NET).

## Key Commands

```bash
# Run tests
go test -short ./...          # Unit tests only
go test ./...                 # All tests (needs PostgreSQL)

# Format code
go fmt ./...

# Lint
golangci-lint run

# Start test database
docker run -d --name mink-pg -e POSTGRES_PASSWORD=mink -p 5432:5432 postgres:16
```

## Architecture

```
Commands → Command Bus → Aggregate → Events → Event Store
                                                    ↓
                                           Projection Engine
                                                    ↓
                                             Read Models → Queries
```

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

**Phase 1 (v0.1.0)**: Core Foundation
- Event types: `EventData`, `StoredEvent`, `Metadata`
- `Aggregate` interface and `AggregateBase`
- `EventStore` with `Append`, `Load`, `SaveAggregate`, `LoadAggregate`
- PostgreSQL adapter with optimistic concurrency
- In-memory adapter for testing

## Don't Do

- Don't mutate events after storage
- Don't use `panic()` for recoverable errors
- Don't skip context propagation
- Don't use global state
- Don't break public API without version bump

## Documentation

See `docs/` folder and `AGENTS.md` for comprehensive details.
