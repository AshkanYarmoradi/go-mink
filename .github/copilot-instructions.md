# Copilot Instructions for go-mink

## Project Context

go-mink is an Event Sourcing and CQRS library for Go. Think of it as "MartenDB for Go" - making event sourcing as easy as using a traditional ORM.

## Key Concepts

### Event Sourcing
- Store all changes as immutable events, not current state
- Rebuild state by replaying events
- Events are facts that happened - never delete or modify them

### CQRS
- Separate write model (aggregates) from read model (projections)
- Commands change state, queries read state
- Optimize each side independently

## Architecture Quick Reference

```
Commands → Command Bus → Aggregate → Events → Event Store
                                                    ↓
                                           Projection Engine
                                                    ↓
                                             Read Models → Queries
```

## Core Interfaces

### EventStoreAdapter (adapters must implement)
```go
type EventStoreAdapter interface {
    Append(ctx context.Context, streamID string, events []EventData, expectedVersion int64) ([]StoredEvent, error)
    Load(ctx context.Context, streamID string, fromVersion int64) ([]StoredEvent, error)
    Subscribe(ctx context.Context, fromPosition uint64) (<-chan StoredEvent, error)
}
```

### Aggregate (domain models implement)
```go
type Aggregate interface {
    AggregateID() string
    AggregateType() string
    Version() int64
    ApplyEvent(event interface{}) error
    UncommittedEvents() []interface{}
    ClearUncommittedEvents()
}
```

### Projection (read models implement)
```go
type Projection interface {
    Name() string
    HandledEvents() []string
    Apply(ctx context.Context, event StoredEvent) error
}
```

## Development Priorities

### Completed Phases
- **Phase 1 (v0.1.0)**: Core Foundation - Event Store, Aggregates, PostgreSQL/Memory adapters
- **Phase 2 (v0.2.0)**: CQRS Commands - Command Bus, middleware, idempotency
- **Phase 3 (v0.3.0)**: Projections - Inline/async/live projections, subscriptions
- **Phase 4 (v0.4.0)**: Developer Experience - Testing utilities, observability, MessagePack

### Phase 5 (Next): Security & Advanced Patterns
1. Saga / Process Manager implementation
2. Outbox pattern for reliable messaging
3. Event versioning & upcasting
4. Field-level encryption (AWS KMS, HashiCorp Vault)
5. GDPR compliance (crypto-shredding)

## Coding Standards

### Go Idioms
- Use `context.Context` as first parameter
- Return `error` as last return value
- Use `Option` function pattern for configuration
- Prefer `errors.Is()` over type assertions
- Use sentinel errors: `var ErrNotFound = errors.New("mink: not found")`

### Naming Conventions
- Interface single method: use `-er` suffix (`Serializer`, `Subscriber`)
- Constructors: `NewXxx()` prefix
- Test files: `xxx_test.go`
- Test functions: `TestXxx_Method_Scenario`

### Error Handling
```go
// Define sentinel errors
var ErrConcurrencyConflict = errors.New("mink: concurrency conflict")

// Use typed errors for details
type ConcurrencyError struct {
    StreamID        string
    ExpectedVersion int64
    ActualVersion   int64
}

// Implement Is() for errors.Is() compatibility
func (e *ConcurrencyError) Is(target error) bool {
    return target == ErrConcurrencyConflict
}
```

### Testing Patterns
```go
// Table-driven tests
func TestEventStore_Append(t *testing.T) {
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
```

## Package Structure

```
mink/
├── mink.go              # Public API
├── event.go             # Event types
├── aggregate.go         # Aggregate interface
├── store.go             # EventStore
├── adapters/
│   ├── postgres/        # PostgreSQL (primary)
│   └── memory/          # Testing
├── middleware/
│   ├── metrics/         # Prometheus metrics (v0.4.0+)
│   └── tracing/         # OpenTelemetry tracing (v0.4.0+)
├── serializer/
│   └── msgpack/         # MessagePack serializer (v0.4.0+)
└── testing/
    ├── bdd/             # BDD test fixtures
    ├── assertions/      # Event assertions
    ├── projections/     # Projection testing
    ├── sagas/           # Saga testing
    ├── containers/      # PostgreSQL test containers
    └── testutil/        # Mock adapters
```

## PostgreSQL Schema

Events table uses JSONB for flexible event data:
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

## Common Tasks

### Adding a new adapter
1. Create package in `adapters/mydb/`
2. Implement `EventStoreAdapter` interface
3. Add `_test.go` with integration tests
4. Document in `docs/adapters.md`

### Adding a new feature
1. Check roadmap in `docs/roadmap.md` for phase alignment
2. Write tests first (TDD approach)
3. Implement feature
4. Add documentation
5. Update CHANGELOG.md

### Writing tests
- Unit tests: No external dependencies, fast
- Integration tests: Use `testing.Short()` skip
- Use testify: `assert` and `require`
- Use table-driven tests for multiple cases

## Don't Do

- Don't mutate events after storage
- Don't use `panic()` for recoverable errors
- Don't skip context propagation
- Don't use global state
- Don't break public API without version bump
- Don't add dependencies without discussion

## Reference Documentation

See `/docs` folder for detailed documentation:
- `introduction.md` - Project goals
- `architecture.md` - System design
- `event-store.md` - Event storage
- `advanced-patterns.md` - Commands, Sagas
- `testing.md` - Test utilities
- `roadmap.md` - Development phases

## For Full Developer Instructions

See [AGENTS.md](../AGENTS.md) in the repository root for comprehensive development instructions including:
- Quick start setup
- Complete package structure
- Implementation guidelines with code examples
- Testing approach (unit, integration, BDD)
- PR guidelines and commit conventions
