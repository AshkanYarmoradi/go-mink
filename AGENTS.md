# Go-Mink Developer Instructions

> Instructions for AI agents and developers working on the go-mink codebase.

## Quick Start for New Contributors

```bash
# Clone and setup
git clone https://github.com/AshkanYarmoradi/go-mink.git
cd go-mink
go mod download

# Run tests
go test -short ./...          # Unit tests only
go test ./...                 # All tests (needs PostgreSQL)

# Start PostgreSQL for integration tests
docker run -d --name mink-pg -e POSTGRES_PASSWORD=mink -p 5432:5432 postgres:16
export TEST_DATABASE_URL="postgres://postgres:mink@localhost:5432/postgres?sslmode=disable"
```

## What is go-mink?

**go-mink** is an Event Sourcing and CQRS library for Go - think "MartenDB for Go".

**Core Idea**: Instead of storing current state, store all changes as events. Rebuild state by replaying events.

```go
// Traditional: UPDATE orders SET status = 'shipped' WHERE id = 123
// Event Sourcing: Append(OrderShipped{OrderID: "123", ShippedAt: time.Now()})
```

## Architecture Overview

### Core Components

```
┌─────────────────────────────────────────────────────────────────┐
│                        APPLICATION LAYER                        │
│  Commands (Write) │ Queries (Read) │ Subscriptions (Real-time)  │
├─────────────────────────────────────────────────────────────────┤
│                         go-mink CORE                            │
│  ┌──────────────────────────────────────────────────────────┐  │
│  │ Command Bus │ Event Store │ Projection Engine │ Sagas    │  │
│  └──────────────────────────────────────────────────────────┘  │
├─────────────────────────────────────────────────────────────────┤
│                        ADAPTER LAYER                            │
│  PostgreSQL │ MongoDB │ Redis │ Memory (testing)                │
└─────────────────────────────────────────────────────────────────┘
```

### Package Structure (Target)

```
github.com/AshkanYarmoradi/go-mink/
├── mink.go                    # Public API entry point
├── event.go                   # Event types: EventData, StoredEvent, Metadata
├── aggregate.go               # Aggregate interface and AggregateBase
├── command.go                 # Command interface and CommandBus
├── projection.go              # Projection interfaces (Inline, Async, Live)
├── store.go                   # EventStore implementation
├── saga.go                    # Saga/Process Manager
├── errors.go                  # Sentinel errors and typed errors
│
├── adapters/
│   ├── adapter.go             # Adapter interfaces
│   ├── postgres/
│   │   ├── postgres.go        # PostgreSQL event store
│   │   ├── readmodel.go       # PostgreSQL read models with auto-migration
│   │   ├── idempotency.go     # PostgreSQL idempotency store
│   │   ├── subscription.go    # PostgreSQL subscriptions
│   │   ├── snapshot.go        # PostgreSQL snapshots (planned)
│   │   └── outbox.go          # PostgreSQL outbox (Phase 5)
│   ├── mongodb/
│   ├── redis/
│   └── memory/                # In-memory for testing
│
├── projection/
│   ├── engine.go              # Projection engine
│   ├── inline.go              # Same-transaction projections
│   ├── async.go               # Background projections
│   ├── live.go                # Real-time projections
│   └── rebuild.go             # Projection rebuilder
│
├── middleware/
│   ├── logging.go
│   ├── metrics/               # Prometheus metrics (v0.4.0+)
│   ├── tracing/               # OpenTelemetry tracing (v0.4.0+)
│   ├── retry.go
│   ├── idempotency.go
│   └── validation.go
│
├── serializer/
│   └── msgpack/               # MessagePack serializer (v0.4.0+)
│
├── encryption/
│   ├── provider.go            # EncryptionProvider interface
│   ├── kms/                   # AWS KMS implementation
│   ├── vault/                 # HashiCorp Vault implementation
│   └── local/                 # Local encryption (testing only)
│
├── gdpr/
│   ├── manager.go             # GDPRManager
│   ├── export.go              # Data export
│   └── retention.go           # Data retention policies
│
├── upcasting/
│   ├── upcaster.go            # Upcaster interface
│   ├── chain.go               # UpcasterChain
│   └── registry.go            # SchemaRegistry
│
├── cli/
│   └── mink/
│       ├── main.go
│       ├── init.go
│       ├── generate.go
│       ├── migrate.go
│       ├── projection.go
│       └── stream.go
│
├── testing/
│   ├── bdd/                   # BDD test fixtures (Given-When-Then)
│   ├── assertions/            # Event assertions and diffing
│   ├── projections/           # Projection testing utilities
│   ├── sagas/                 # Saga testing utilities
│   ├── containers/            # PostgreSQL test containers
│   └── testutil/              # Mock adapters and helpers
│
└── examples/
    ├── basic/
    ├── ecommerce/
    └── multi-tenant/
```

---

## Development Phases

### Completed Phases

**Phase 1 (v0.1.0)**: Foundation - COMPLETE
- Core interfaces: `Event`, `EventData`, `StoredEvent`, `Metadata`
- `Aggregate` interface and `AggregateBase` implementation
- `EventStore` with `Append`, `Load`, `SaveAggregate`, `LoadAggregate`
- PostgreSQL adapter with optimistic concurrency
- In-memory adapter for testing
- JSON serializer with event registry

**Phase 2 (v0.2.0)**: CQRS & Commands - COMPLETE
- `Command` interface with validation
- `CommandHandler` generic interface
- `CommandBus` with middleware pipeline
- `IdempotencyStore` interface and PostgreSQL implementation
- Middleware: Logging, Validation, Recovery, Idempotency, Correlation, Causation, Tenant

**Phase 3 (v0.3.0)**: Read Models - COMPLETE
- `Projection` interface hierarchy (Inline, Async, Live)
- `ProjectionEngine` with worker pool
- Checkpoint management and rebuilding
- `ReadModelRepository` generic interface with query builder
- PostgreSQL read model repository with auto-migration
- Event subscriptions (`SubscribeAll`, `SubscribeStream`, `SubscribeCategory`)

**Phase 4 (v0.4.0)**: Developer Experience - COMPLETE
- Testing utilities: `testing/bdd`, `testing/assertions`, `testing/projections`, `testing/sagas`, `testing/containers`
- Observability: `middleware/metrics` (Prometheus), `middleware/tracing` (OpenTelemetry)
- MessagePack serializer: `serializer/msgpack`
- CLI tool: `cli/commands` with 84.9% test coverage
  - Commands: init, generate, migrate, projection, stream, diagnose, schema
  - 200+ unit tests, 67 integration tests, 4 E2E tests
  - Full PostgreSQL integration testing

### Phase 5 (v0.5.0): Security & Advanced Patterns - IN PROGRESS

**Goal**: Production-ready features for enterprise use

**Completed Tasks**:
- ✅ Saga / Process Manager implementation
  - `Saga` interface and `SagaBase` implementation
  - `SagaManager` for orchestration
  - `SagaStore` with PostgreSQL and Memory implementations
  - `testing/sagas` package for testing
- ✅ Outbox Pattern for reliable messaging
  - `OutboxStore` interface with PostgreSQL and Memory implementations
  - `OutboxAppender` for atomic event+outbox writes (PostgreSQL)
  - `EventStoreWithOutbox` wrapper with automatic routing
  - `OutboxProcessor` background worker with polling, retry, dead-letter
  - Built-in publishers: Webhook (`outbox/webhook`), Kafka (`outbox/kafka`), SNS (`outbox/sns`)
  - Prometheus metrics integration (`OutboxMetrics` in `middleware/metrics`)

**Remaining Tasks**:
1. Event versioning & upcasting
2. Field-level encryption (AWS KMS, HashiCorp Vault)
3. GDPR compliance (crypto-shredding, data export)

### Phase 6-7: See roadmap.md for details

---

## Coding Standards

### Go Conventions

```go
// Package documentation
// Package mink provides event sourcing and CQRS primitives for Go applications.
package mink

// Interface names - single method uses -er suffix
type Serializer interface {
    Serialize(v interface{}) ([]byte, error)
}

// Struct constructors use New prefix
func NewEventStore(adapter EventStoreAdapter, opts ...Option) *EventStore

// Options pattern for configuration
type Option func(*EventStore)

func WithSerializer(s Serializer) Option {
    return func(es *EventStore) {
        es.serializer = s
    }
}

// Error handling - use sentinel errors and typed errors
var ErrConcurrencyConflict = errors.New("mink: concurrency conflict")

type ConcurrencyError struct {
    StreamID        string
    ExpectedVersion int64
    ActualVersion   int64
}

func (e *ConcurrencyError) Error() string {
    return fmt.Sprintf("mink: concurrency conflict on stream %s: expected %d, got %d",
        e.StreamID, e.ExpectedVersion, e.ActualVersion)
}

func (e *ConcurrencyError) Is(target error) bool {
    return target == ErrConcurrencyConflict
}
```

### Interface Design Principles

```go
// 1. Small, focused interfaces
type EventStoreAdapter interface {
    Append(ctx context.Context, streamID string, events []EventData, expectedVersion int64) ([]StoredEvent, error)
    Load(ctx context.Context, streamID string, fromVersion int64) ([]StoredEvent, error)
}

// 2. Separate read and write concerns
type EventReader interface {
    Load(ctx context.Context, streamID string, fromVersion int64) ([]StoredEvent, error)
}

type EventWriter interface {
    Append(ctx context.Context, streamID string, events []EventData, expectedVersion int64) ([]StoredEvent, error)
}

// EventStoreAdapter composes both
type EventStoreAdapter interface {
    EventReader
    EventWriter
    Subscriber
}

// 3. Context-first parameters
func (s *EventStore) Append(ctx context.Context, streamID string, ...) error

// 4. Return errors, not panic
func (s *EventStore) Load(ctx context.Context, streamID string) ([]StoredEvent, error)
```

### Testing Requirements

```go
// 1. Table-driven tests
func TestEventStore_Append(t *testing.T) {
    tests := []struct {
        name            string
        streamID        string
        events          []EventData
        expectedVersion int64
        wantErr         error
    }{
        {
            name:            "append to new stream",
            streamID:        "order-123",
            events:          []EventData{{Type: "OrderCreated", Data: []byte("{}")}},
            expectedVersion: mink.NoStream,
            wantErr:         nil,
        },
        {
            name:            "concurrency conflict",
            streamID:        "order-123",
            events:          []EventData{{Type: "ItemAdded", Data: []byte("{}")}},
            expectedVersion: 0, // Stream already has version 1
            wantErr:         mink.ErrConcurrencyConflict,
        },
    }
    
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            // Test implementation
        })
    }
}

// 2. Use testify assertions
import "github.com/stretchr/testify/assert"
import "github.com/stretchr/testify/require"

assert.Equal(t, expected, actual)
require.NoError(t, err)

// 3. BDD-style for aggregate tests (testing/bdd package)
func TestOrder_CannotAddItemAfterShipping(t *testing.T) {
    order := NewOrder("order-123")

    bdd.Given(t, order,
        OrderCreated{OrderID: "order-123"},
        OrderShipped{OrderID: "order-123"},
    ).
        When(func() error {
            return order.AddItem("WIDGET", 1, 29.99)
        }).
        ThenError(ErrOrderAlreadyShipped)
}
```

### Documentation Standards

```go
// Every exported type, function, and method needs documentation
// Use complete sentences starting with the name

// EventStore manages event persistence and aggregate lifecycle.
// It provides methods for appending events, loading streams,
// and managing aggregate state.
type EventStore struct {
    // ...
}

// Append stores events to the specified stream with optimistic concurrency control.
// If expectedVersion does not match the current stream version, ErrConcurrencyConflict is returned.
//
// Special version constants:
//   - AnyVersion (-1): Skip version check
//   - NoStream (0): Stream must not exist
//   - StreamExists (-2): Stream must exist
func (s *EventStore) Append(ctx context.Context, streamID string, events []EventData, expectedVersion int64) error {
    // ...
}
```

---

## Implementation Guidelines

### Event Store Implementation

```go
// event.go
package mink

import "time"

// EventData represents an event to be stored.
type EventData struct {
    Type     string
    Data     []byte
    Metadata Metadata
}

// StoredEvent represents a persisted event.
type StoredEvent struct {
    ID             string
    StreamID       string
    Type           string
    Data           []byte
    Metadata       Metadata
    Version        int64
    GlobalPosition uint64
    Timestamp      time.Time
}

// Metadata contains event context for tracing and multi-tenancy.
type Metadata struct {
    CorrelationID string            `json:"correlationId,omitempty"`
    CausationID   string            `json:"causationId,omitempty"`
    UserID        string            `json:"userId,omitempty"`
    TenantID      string            `json:"tenantId,omitempty"`
    Custom        map[string]string `json:"custom,omitempty"`
}

// Version constants for optimistic concurrency.
const (
    AnyVersion   int64 = -1 // No concurrency check
    NoStream     int64 = 0  // Stream must not exist
    StreamExists int64 = -2 // Stream must exist
)
```

### Aggregate Implementation

```go
// aggregate.go
package mink

// Aggregate defines the interface for event-sourced aggregates.
type Aggregate interface {
    AggregateID() string
    AggregateType() string
    Version() int64
    ApplyEvent(event interface{}) error
    UncommittedEvents() []interface{}
    ClearUncommittedEvents()
}

// AggregateBase provides a default implementation of Aggregate.
type AggregateBase struct {
    id                string
    aggregateType     string
    version           int64
    uncommittedEvents []interface{}
}

func (a *AggregateBase) AggregateID() string   { return a.id }
func (a *AggregateBase) AggregateType() string { return a.aggregateType }
func (a *AggregateBase) Version() int64        { return a.version }

func (a *AggregateBase) SetID(id string)           { a.id = id }
func (a *AggregateBase) SetType(t string)          { a.aggregateType = t }
func (a *AggregateBase) SetVersion(v int64)        { a.version = v }
func (a *AggregateBase) IncrementVersion()         { a.version++ }

func (a *AggregateBase) UncommittedEvents() []interface{} {
    return a.uncommittedEvents
}

func (a *AggregateBase) ClearUncommittedEvents() {
    a.uncommittedEvents = nil
}

// Apply records an event to be committed.
// The aggregate should also update its state based on the event.
func (a *AggregateBase) Apply(event interface{}) {
    a.uncommittedEvents = append(a.uncommittedEvents, event)
}
```

### PostgreSQL Adapter

```go
// adapters/postgres/eventstore.go
package postgres

import (
    "context"
    "database/sql"
    
    "github.com/AshkanYarmoradi/go-mink"
)

type PostgresAdapter struct {
    db     *sql.DB
    schema string
}

func NewAdapter(connStr string, opts ...Option) (*PostgresAdapter, error) {
    db, err := sql.Open("pgx", connStr)
    if err != nil {
        return nil, fmt.Errorf("mink/postgres: failed to connect: %w", err)
    }
    
    adapter := &PostgresAdapter{
        db:     db,
        schema: "mink", // Default schema
    }
    
    for _, opt := range opts {
        opt(adapter)
    }
    
    return adapter, nil
}

func (a *PostgresAdapter) Append(ctx context.Context, streamID string, 
    events []mink.EventData, expectedVersion int64) ([]mink.StoredEvent, error) {
    
    // Use transaction for atomicity
    tx, err := a.db.BeginTx(ctx, nil)
    if err != nil {
        return nil, fmt.Errorf("mink/postgres: begin tx: %w", err)
    }
    defer tx.Rollback()
    
    // Call PostgreSQL function for optimistic concurrency
    // See event-store.md for SQL schema
    
    if err := tx.Commit(); err != nil {
        return nil, fmt.Errorf("mink/postgres: commit: %w", err)
    }
    
    return stored, nil
}
```

---

## Testing Approach

### Unit Tests (Aggregates)

```go
// order_test.go
func TestOrder_Create(t *testing.T) {
    order := NewOrder("order-123")
    
    err := order.Create("customer-456")
    
    require.NoError(t, err)
    assert.Equal(t, "customer-456", order.CustomerID)
    assert.Equal(t, "Created", order.Status)
    
    events := order.UncommittedEvents()
    require.Len(t, events, 1)
    
    created, ok := events[0].(OrderCreated)
    require.True(t, ok)
    assert.Equal(t, "order-123", created.OrderID)
    assert.Equal(t, "customer-456", created.CustomerID)
}

func TestOrder_AddItem_WhenShipped_ReturnsError(t *testing.T) {
    order := NewOrder("order-123")
    order.ApplyEvent(OrderCreated{OrderID: "order-123"})
    order.ApplyEvent(OrderShipped{OrderID: "order-123"})
    order.ClearUncommittedEvents()
    
    err := order.AddItem("SKU-001", 1, 29.99)
    
    assert.ErrorIs(t, err, ErrOrderAlreadyShipped)
    assert.Empty(t, order.UncommittedEvents())
}
```

### Integration Tests (Adapters)

```go
// adapters/postgres/eventstore_test.go
func TestPostgresAdapter_Append(t *testing.T) {
    if testing.Short() {
        t.Skip("Skipping integration test")
    }
    
    connStr := os.Getenv("TEST_DATABASE_URL")
    if connStr == "" {
        t.Skip("TEST_DATABASE_URL not set")
    }
    
    adapter, err := postgres.NewAdapter(connStr)
    require.NoError(t, err)
    defer adapter.Close()
    
    // Initialize schema
    require.NoError(t, adapter.Initialize(context.Background()))
    
    // Test append
    events := []mink.EventData{
        {Type: "TestEvent", Data: []byte(`{"key":"value"}`)},
    }
    
    stored, err := adapter.Append(context.Background(), "test-stream", events, mink.NoStream)
    require.NoError(t, err)
    assert.Len(t, stored, 1)
    assert.Equal(t, int64(1), stored[0].Version)
}
```

### BDD Tests (Behavior)

```go
import "github.com/AshkanYarmoradi/go-mink/testing/bdd"

func TestOrderFulfillment(t *testing.T) {
    t.Run("successful order flow", func(t *testing.T) {
        order := NewOrder("order-123")

        bdd.Given(t, order).
            When(func() error {
                return order.Create("cust-456")
            }).
            Then(OrderCreated{OrderID: "order-123", CustomerID: "cust-456"})
    })

    t.Run("cannot add items after shipping", func(t *testing.T) {
        order := NewOrder("order-123")

        bdd.Given(t, order,
            OrderCreated{OrderID: "order-123"},
            OrderShipped{OrderID: "order-123"},
        ).
            When(func() error {
                return order.AddItem("WIDGET", 1, 29.99)
            }).
            ThenError(ErrOrderAlreadyShipped)
    })
}
```

---

## Pull Request Guidelines

### PR Title Format
```
[component] Brief description

Examples:
[eventstore] Add PostgreSQL adapter with optimistic concurrency
[projection] Implement async projection engine
[cli] Add mink generate aggregate command
```

### PR Description Template
```markdown
## Summary
Brief description of changes.

## Changes
- Change 1
- Change 2

## Testing
- [ ] Unit tests added/updated
- [ ] Integration tests added/updated
- [ ] Manual testing performed

## Documentation
- [ ] Code comments added
- [ ] README updated (if applicable)
- [ ] API docs updated

## Checklist
- [ ] Code follows project style guidelines
- [ ] All tests pass
- [ ] No breaking changes (or documented if intentional)
```

### Commit Message Format
```
component: brief description

Longer explanation if needed.

Closes #123
```

---

## Common Patterns

### Implementing a New Adapter

```go
// 1. Create adapter package
// adapters/mydb/eventstore.go

package mydb

import "github.com/AshkanYarmoradi/go-mink"

// Ensure interface compliance at compile time
var _ mink.EventStoreAdapter = (*MyDBAdapter)(nil)

type MyDBAdapter struct {
    client *mydb.Client
}

func NewAdapter(connStr string) (*MyDBAdapter, error) {
    // Initialize connection
}

func (a *MyDBAdapter) Append(ctx context.Context, streamID string,
    events []mink.EventData, expectedVersion int64) ([]mink.StoredEvent, error) {
    // Implement with optimistic concurrency
}

func (a *MyDBAdapter) Load(ctx context.Context, streamID string,
    fromVersion int64) ([]mink.StoredEvent, error) {
    // Load events
}

// ... implement other interface methods
```

### Implementing a Projection

```go
type OrderSummaryProjection struct {
    repo mink.Repository[OrderSummary]
}

func (p *OrderSummaryProjection) Name() string {
    return "OrderSummary"
}

func (p *OrderSummaryProjection) HandledEvents() []string {
    return []string{"OrderCreated", "ItemAdded", "OrderShipped"}
}

func (p *OrderSummaryProjection) Apply(ctx context.Context, tx mink.Transaction, event mink.StoredEvent) error {
    switch event.Type {
    case "OrderCreated":
        var e OrderCreated
        if err := json.Unmarshal(event.Data, &e); err != nil {
            return err
        }
        return tx.Insert(ctx, &OrderSummary{
            ID:         e.OrderID,
            CustomerID: e.CustomerID,
            Status:     "Created",
        })
        
    case "ItemAdded":
        var e ItemAdded
        if err := json.Unmarshal(event.Data, &e); err != nil {
            return err
        }
        return tx.Update(ctx, e.OrderID, func(s *OrderSummary) {
            s.ItemCount++
            s.TotalAmount += e.Price * float64(e.Quantity)
        })
        
    case "OrderShipped":
        return tx.Update(ctx, event.StreamID, func(s *OrderSummary) {
            s.Status = "Shipped"
        })
    }
    return nil
}
```

---

## Resources

### Documentation Files
- [Introduction](docs/introduction.md) - Problem statement and goals
- [Architecture](docs/architecture.md) - System design
- [Event Store](docs/event-store.md) - Event storage design with PostgreSQL schema
- [Read Models](docs/read-models.md) - Projection system
- [Adapters](docs/adapters.md) - Database adapter interfaces
- [CLI](docs/cli.md) - Command-line tooling
- [API Design](docs/api-design.md) - Public API reference
- [Advanced Patterns](docs/advanced-patterns.md) - Commands, Sagas, Outbox
- [Security](docs/security.md) - Encryption, GDPR, Versioning
- [Testing](docs/testing.md) - BDD fixtures and test utilities
- [Roadmap](docs/roadmap.md) - Development phases

### External References
- [MartenDB](https://martendb.io/) - .NET inspiration
- [Event Sourcing by Martin Fowler](https://martinfowler.com/eaaDev/EventSourcing.html)
- [CQRS by Martin Fowler](https://martinfowler.com/bliki/CQRS.html)
- [PostgreSQL JSONB](https://www.postgresql.org/docs/current/datatype-json.html)

---

## Quick Reference

### Version Constants
```go
AnyVersion   = -1  // Skip version check
NoStream     = 0   // Stream must not exist  
StreamExists = -2  // Stream must exist
```

### Error Handling
```go
errors.Is(err, mink.ErrConcurrencyConflict)
errors.Is(err, mink.ErrStreamNotFound)
errors.Is(err, mink.ErrEventNotFound)
```

### Common Imports
```go
import (
    "github.com/AshkanYarmoradi/go-mink"
    "github.com/AshkanYarmoradi/go-mink/adapters/postgres"
    "github.com/AshkanYarmoradi/go-mink/adapters/memory"

    // Testing utilities (v0.4.0+)
    "github.com/AshkanYarmoradi/go-mink/testing/bdd"
    "github.com/AshkanYarmoradi/go-mink/testing/assertions"
    "github.com/AshkanYarmoradi/go-mink/testing/projections"
    "github.com/AshkanYarmoradi/go-mink/testing/containers"

    // Observability (v0.4.0+)
    "github.com/AshkanYarmoradi/go-mink/middleware/metrics"
    "github.com/AshkanYarmoradi/go-mink/middleware/tracing"
)
```

---

**Remember**: The goal is to make Event Sourcing accessible to every Go developer. Keep APIs simple, provide excellent error messages, and document everything thoroughly.
