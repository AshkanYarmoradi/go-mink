# Architecture

## High-Level Overview

```
┌─────────────────────────────────────────────────────────────────────────┐
│                              Application                                 │
├─────────────────────────────────────────────────────────────────────────┤
│                                                                         │
│  ┌─────────────┐    ┌─────────────┐    ┌─────────────┐                 │
│  │  Commands   │    │   Queries   │    │  Subscript  │                 │
│  │  (Write)    │    │   (Read)    │    │   ions      │                 │
│  └──────┬──────┘    └──────┬──────┘    └──────┬──────┘                 │
│         │                  │                  │                         │
├─────────▼──────────────────▼──────────────────▼─────────────────────────┤
│                           go-mink CORE                                     │
│  ┌─────────────────────────────────────────────────────────────────┐   │
│  │                      Event Store Engine                          │   │
│  │  ┌───────────┐  ┌───────────┐  ┌───────────┐  ┌───────────┐    │   │
│  │  │ Aggregate │  │   Event   │  │ Snapshot  │  │  Outbox   │    │   │
│  │  │  Manager  │  │  Streams  │  │  Manager  │  │  Manager  │    │   │
│  │  └───────────┘  └───────────┘  └───────────┘  └───────────┘    │   │
│  └─────────────────────────────────────────────────────────────────┘   │
│                                                                         │
│  ┌─────────────────────────────────────────────────────────────────┐   │
│  │                     Projection Engine                            │   │
│  │  ┌───────────┐  ┌───────────┐  ┌───────────┐  ┌───────────┐    │   │
│  │  │  Inline   │  │   Async   │  │   Live    │  │  Rebuild  │    │   │
│  │  │Projection │  │Projection │  │Projection │  │  Manager  │    │   │
│  │  └───────────┘  └───────────┘  └───────────┘  └───────────┘    │   │
│  └─────────────────────────────────────────────────────────────────┘   │
│                                                                         │
├─────────────────────────────────────────────────────────────────────────┤
│                        ADAPTER LAYER                                    │
│  ┌────────────┐  ┌────────────┐  ┌────────────┐  ┌────────────┐       │
│  │ PostgreSQL │  │  MongoDB   │  │   Redis    │  │   Memory   │       │
│  │  Adapter   │  │  Adapter   │  │  Adapter   │  │  Adapter   │       │
│  └────────────┘  └────────────┘  └────────────┘  └────────────┘       │
└─────────────────────────────────────────────────────────────────────────┘
```

## Core Components

### 1. Event Store Engine

The heart of go-mink - manages event persistence and retrieval.

```go
// Core interface - adapters implement this
type EventStoreAdapter interface {
    // Append events to a stream
    Append(ctx context.Context, streamID StreamID, events []EventData, 
           expectedVersion int64) error
    
    // Load events from a stream
    Load(ctx context.Context, streamID StreamID, 
         fromVersion int64) ([]StoredEvent, error)
    
    // Subscribe to all events (for projections)
    SubscribeAll(ctx context.Context, fromPosition uint64) 
                 (<-chan StoredEvent, error)
    
    // Get global position
    GetLastPosition(ctx context.Context) (uint64, error)
}
```

### 2. Projection Engine

Transforms events into read models automatically.

```go
// Projections define how events become read models
type Projection interface {
    // Which events this projection handles
    HandledEvents() []string
    
    // Transform event into read model update
    Apply(ctx context.Context, event StoredEvent) error
    
    // Projection identity
    Name() string
}
```

### 3. Aggregate Manager

Implements the Aggregate Root pattern for domain modeling.

```go
// Aggregates encapsulate business logic
type Aggregate interface {
    // Unique identifier
    AggregateID() string
    
    // Apply event to update state
    ApplyEvent(event Event) error
    
    // Get uncommitted events
    UncommittedEvents() []Event
    
    // Current version
    Version() int64
}
```

## Data Flow

### Write Path (Commands)

```
Command → Aggregate → Events → Event Store → Outbox → Projections
    │         │          │          │           │          │
    ▼         ▼          ▼          ▼           ▼          ▼
 Validate  Business   Generate  Persist    Publish    Update
  Input    Logic      Events    Atomic    External   Read Models
```

### Read Path (Queries)

```
Query → Read Model Repository → Database → Response
    │            │                  │          │
    ▼            ▼                  ▼          ▼
 Validate    Select           Optimized    Return
  Input     Adapter           Query        DTO
```

## Adapter Architecture

```go
// Adapters are registered at startup
type AdapterRegistry struct {
    eventStore  EventStoreAdapter
    readModels  ReadModelAdapter
    outbox      OutboxAdapter
    snapshots   SnapshotAdapter
}

// Configuration allows mixing adapters
config := go-mink.Config{
    EventStore: postgres.NewAdapter(pgConn),    // Events in PostgreSQL
    ReadModels: mongodb.NewAdapter(mongoConn),  // Read models in MongoDB
    Snapshots:  redis.NewAdapter(redisConn),    // Snapshots in Redis
}
```

## Package Structure

```
github.com/AshkanYarmoradi/go-mink/
├── go-mink.go                 # Public API entry point
├── event.go                # Event types and interfaces
├── aggregate.go            # Aggregate base implementation
├── projection.go           # Projection interfaces
├── store.go                # Event store implementation
│
├── adapters/               # Storage adapters
│   ├── adapter.go          # Adapter interfaces
│   ├── postgres/           # PostgreSQL implementation
│   ├── mongodb/            # MongoDB implementation
│   ├── redis/              # Redis implementation
│   └── memory/             # In-memory (testing)
│
├── projection/             # Projection engine
│   ├── engine.go           # Core projection runner
│   ├── inline.go           # Same-transaction projections
│   ├── async.go            # Background projections
│   └── rebuild.go          # Projection rebuilder
│
├── middleware/             # Cross-cutting concerns
│   ├── logging.go
│   ├── metrics.go
│   ├── tracing.go
│   └── retry.go
│
├── cli/                    # CLI tool
│   └── go-mink/
│       └── main.go
│
└── testing/                # Test utilities
    ├── inmemory.go
    └── assertions.go
```

## Thread Safety

All go-mink components are designed for concurrent use:

| Component | Concurrency Model |
|-----------|-------------------|
| Event Store | Optimistic locking per stream |
| Projections | Worker pool with ordering guarantees |
| Aggregates | Single-writer per aggregate |
| Adapters | Connection pooling |

---

Next: [Event Store →](03-event-store.md)
