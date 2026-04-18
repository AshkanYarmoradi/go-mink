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
│  PostgreSQL │ Memory (testing)                                   │
└─────────────────────────────────────────────────────────────────┘
```

### Package Structure

```
go-mink.dev/
├── *.go                       # Core types in root mink package:
│                              #   store.go (EventStore), bus.go (CommandBus),
│                              #   projection_engine.go (ProjectionEngine),
│                              #   saga_manager.go (SagaManager), outbox_processor.go,
│                              #   export.go (DataExporter), export_errors.go,
│                              #   errors.go, versioning.go, encryption.go, etc.
│
├── adapters/
│   ├── adapter.go             # All adapter interfaces and shared types
│   ├── postgres/              # PostgreSQL adapter (production)
│   └── memory/                # In-memory adapter (testing)
│
├── encryption/
│   ├── provider.go            # Provider interface, types, errors
│   ├── local/                 # AES-256-GCM (testing/development)
│   ├── kms/                   # AWS KMS (production)
│   └── vault/                 # HashiCorp Vault Transit (production)
│
├── outbox/
│   ├── webhook/               # Webhook publisher
│   ├── kafka/                 # Kafka publisher
│   └── sns/                   # SNS publisher
│
├── middleware/
│   ├── metrics/               # Prometheus metrics
│   └── tracing/               # OpenTelemetry tracing
│
├── serializer/
│   ├── msgpack/               # MessagePack serializer
│   └── protobuf/              # Protocol Buffers serializer
│
├── cli/commands/              # CLI tool (init, generate, migrate, projection,
│                              #   stream, diagnose, schema)
│
├── testing/
│   ├── bdd/                   # BDD test fixtures (Given-When-Then)
│   ├── assertions/            # Event assertions and diffing
│   ├── projections/           # Projection testing utilities
│   ├── sagas/                 # Saga testing utilities
│   └── containers/            # PostgreSQL test containers
│
└── examples/                  # Example projects (basic, versioning, projections,
                               #   sagas, cqrs, metrics, tracing, encryption, export, etc.)
```

---

## Implemented Features (v1.0.0)

**Core**: Event Store with optimistic concurrency, PostgreSQL and in-memory adapters, JSON serializer with event registry, Aggregate interface and AggregateBase.

**CQRS**: Command interface with validation, CommandBus with middleware pipeline, IdempotencyStore (PostgreSQL and in-memory), Middleware: Logging, Validation, Recovery, Idempotency, Correlation, Causation, Tenant.

**Read Models**: Projection hierarchy (Inline, Async, Live), ProjectionEngine with worker pool, checkpoint management and rebuilding, ReadModelRepository with query builder, event subscriptions.

**Developer Experience**: Testing utilities (`testing/bdd`, `testing/assertions`, `testing/projections`, `testing/sagas`, `testing/containers`), Observability (`middleware/metrics`, `middleware/tracing`), MessagePack serializer, CLI tool (`cli/commands`).

**Advanced Patterns**: Saga / Process Manager with compensation, Outbox pattern (stores, processor, Webhook/Kafka/SNS publishers), Event versioning & upcasting with schema registry, Field-level encryption (Local/AWS KMS/Vault Transit providers), GDPR crypto-shredding.

See `docs/roadmap.md` for future development plans.

---

## Branching & Release Model

```
feature/xxx ──► develop (RC pre-releases) ──► main (stable releases)
```

- **Feature branches** are created from `develop` and merged back via PR.
- Each merge to `develop` auto-creates an RC release (`v1.0.4-rc.1`, `v1.0.4-rc.2`, ...).
- Merging `develop` into `main` auto-creates the next stable release (`v1.0.4`).
- CI (tests, lint, build) runs on both `main` and `develop` branches.
- RC versions are GitHub pre-releases — `go get module@latest` still returns stable.

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
// 1. Small, focused core interface — adapters implement only what they need
type EventStoreAdapter interface {
    Append(ctx context.Context, streamID string, events []EventRecord, expectedVersion int64) ([]StoredEvent, error)
    Load(ctx context.Context, streamID string, fromVersion int64) ([]StoredEvent, error)
    GetStreamInfo(ctx context.Context, streamID string) (*StreamInfo, error)
    GetLastPosition(ctx context.Context) (uint64, error)
    Initialize(ctx context.Context) error
    Close() error
}

// 2. Optional interfaces via composition (adapters opt in as needed)
// SubscriptionAdapter, SnapshotAdapter, TransactionalAdapter,
// CheckpointAdapter, IdempotencyStore, SagaStore, OutboxStore,
// OutboxAppender, StreamQueryAdapter, DiagnosticAdapter, SchemaProvider

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
        events          []EventRecord
        expectedVersion int64
        wantErr         error
    }{
        {
            name:            "append to new stream",
            streamID:        "order-123",
            events:          []EventRecord{{ID: uuid.NewString(), Type: "OrderCreated", Data: []byte("{}")}},
            expectedVersion: mink.NoStream,
            wantErr:         nil,
        },
        {
            name:            "concurrency conflict",
            streamID:        "order-123",
            events:          []EventRecord{{ID: uuid.NewString(), Type: "ItemAdded", Data: []byte("{}")}},
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
func (s *EventStore) Append(ctx context.Context, streamID string, events []interface{}, opts ...AppendOption) error {
    // ...
}
```

---

## Implementation Guidelines

### Core Types

```go
// adapters/adapter.go — types used by adapter implementations
package adapters

import "time"

// EventRecord represents an event to be stored (input to Append).
type EventRecord struct {
    ID       string
    Type     string
    Data     []byte
    Metadata Metadata
}

// StoredEvent represents a persisted event (returned from Append/Load).
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

// Version constants for optimistic concurrency (defined in root mink package).
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
// adapters/postgres/ — uses pgx/v5 connection pool
package postgres

import (
    "context"
    "go-mink.dev/adapters"
)

// Compile-time interface checks
var _ adapters.EventStoreAdapter = (*Adapter)(nil)

func NewAdapter(connStr string, opts ...Option) (*Adapter, error) {
    // Initializes pgx connection pool
}

func (a *Adapter) Append(ctx context.Context, streamID string,
    events []adapters.EventRecord, expectedVersion int64) ([]adapters.StoredEvent, error) {
    // Optimistic concurrency via UNIQUE(stream_id, version) constraint
}

// Also implements: SubscriptionAdapter, TransactionalAdapter, CheckpointAdapter,
// IdempotencyStore, SagaStore, OutboxStore, OutboxAppender, SnapshotAdapter,
// StreamQueryAdapter, DiagnosticAdapter, SchemaProvider, MigrationAdapter
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
    events := []adapters.EventRecord{
        {ID: uuid.NewString(), Type: "TestEvent", Data: []byte(`{"key":"value"}`)},
    }
    
    stored, err := adapter.Append(context.Background(), "test-stream", events, mink.NoStream)
    require.NoError(t, err)
    assert.Len(t, stored, 1)
    assert.Equal(t, int64(1), stored[0].Version)
}
```

### BDD Tests (Behavior)

```go
import "go-mink.dev/testing/bdd"

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

import (
    "context"
    "go-mink.dev/adapters"
)

// Ensure interface compliance at compile time
var _ adapters.EventStoreAdapter = (*MyDBAdapter)(nil)

type MyDBAdapter struct {
    client *mydb.Client
}

func NewAdapter(connStr string) (*MyDBAdapter, error) {
    // Initialize connection
}

func (a *MyDBAdapter) Append(ctx context.Context, streamID string,
    events []adapters.EventRecord, expectedVersion int64) ([]adapters.StoredEvent, error) {
    // Implement with optimistic concurrency
}

func (a *MyDBAdapter) Load(ctx context.Context, streamID string,
    fromVersion int64) ([]adapters.StoredEvent, error) {
    // Load events
}

// ... implement remaining EventStoreAdapter methods (GetStreamInfo, GetLastPosition, Initialize, Close)
// Optionally implement SubscriptionAdapter, SagaStore, OutboxStore, etc.
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
    "go-mink.dev"
    "go-mink.dev/adapters/postgres"
    "go-mink.dev/adapters/memory"

    // Testing utilities
    "go-mink.dev/testing/bdd"
    "go-mink.dev/testing/assertions"
    "go-mink.dev/testing/projections"
    "go-mink.dev/testing/containers"

    // Observability
    "go-mink.dev/middleware/metrics"
    "go-mink.dev/middleware/tracing"
)
```

---

**Remember**: The goal is to make Event Sourcing accessible to every Go developer. Keep APIs simple, provide excellent error messages, and document everything thoroughly.
