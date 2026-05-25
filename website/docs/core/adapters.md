---
title: Adapters
sidebar_position: 6
---

# Adapter System

<span class="badge badge--success">v1.0.0</span>

---

## Design Philosophy

go-mink's adapter system allows mixing different storage backends:

```
┌─────────────────────────────────────────────────────────────────┐
│                        Your Application                          │
├─────────────────────────────────────────────────────────────────┤
│                         go-mink Core                                │
│                                                                  │
│  EventStore    Projections    Snapshots    Outbox               │
│      │              │             │           │                  │
├──────▼──────────────▼─────────────▼───────────▼─────────────────┤
│                    Adapter Interfaces                            │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌──────────┐        │
│  │PostgreSQL│  │ MongoDB  │  │  Redis   │  │  Memory  │        │
│  └──────────┘  └──────────┘  └──────────┘  └──────────┘        │
│                                                                  │
│  Events ────────► PostgreSQL (ACID, JSON)                       │
│  Read Models ───► MongoDB (flexible queries)                    │
│  Snapshots ─────► Redis (fast access)                           │
│  Cache ─────────► Redis (ephemeral)                             │
│                                                                  │
└─────────────────────────────────────────────────────────────────┘
```

## Adapter Interfaces

### Event Store Adapter (Implemented)

```go
// EventStoreAdapter is the interface that database adapters must implement.
// It provides the low-level operations for persisting and retrieving events.
type EventStoreAdapter interface {
    // Append stores events to the specified stream with optimistic concurrency control.
    // expectedVersion specifies the expected current version of the stream:
    //   - AnyVersion (-1): Skip version check
    //   - NoStream (0): Stream must not exist
    //   - StreamExists (-2): Stream must exist
    //   - Any positive number: Stream must be at this exact version
    Append(ctx context.Context, streamID string, events []EventRecord, expectedVersion int64) ([]StoredEvent, error)

    // Load retrieves all events from a stream starting from the specified version.
    Load(ctx context.Context, streamID string, fromVersion int64) ([]StoredEvent, error)

    // GetStreamInfo returns metadata about a stream.
    GetStreamInfo(ctx context.Context, streamID string) (*StreamInfo, error)

    // GetLastPosition returns the global position of the last stored event.
    GetLastPosition(ctx context.Context) (uint64, error)

    // Initialize sets up the required database schema.
    Initialize(ctx context.Context) error

    // Close releases any resources held by the adapter.
    Close() error
}
```

### Subscription Adapter (Implemented)

```go
// SubscriptionAdapter provides event subscription capabilities.
type SubscriptionAdapter interface {
    // LoadFromPosition loads events starting from a global position.
    // This is used by projection engines to catch up on historical events.
    LoadFromPosition(ctx context.Context, fromPosition uint64, limit int) ([]StoredEvent, error)

    // SubscribeAll subscribes to all events across all streams.
    // Optional SubscriptionOptions can be provided to configure buffer size, poll interval, etc.
    SubscribeAll(ctx context.Context, fromPosition uint64, opts ...SubscriptionOptions) (<-chan StoredEvent, error)

    // SubscribeStream subscribes to events from a specific stream.
    // Optional SubscriptionOptions can be provided to configure behavior.
    SubscribeStream(ctx context.Context, streamID string, fromVersion int64, opts ...SubscriptionOptions) (<-chan StoredEvent, error)

    // SubscribeCategory subscribes to all events from streams in a category.
    // Optional SubscriptionOptions can be provided to configure behavior.
    SubscribeCategory(ctx context.Context, category string, fromPosition uint64, opts ...SubscriptionOptions) (<-chan StoredEvent, error)
}

// SubscriptionOptions configures subscription behavior.
type SubscriptionOptions struct {
    BufferSize   int           // Channel buffer size (default: 100)
    PollInterval time.Duration // Polling interval for polling-based subscriptions
    OnError      func(error)   // Error callback for non-fatal errors
}
```

### Read Model Adapter (Future)

```go
// ReadModelAdapter provides generic document storage
type ReadModelAdapter interface {
    // CRUD operations
    Get(ctx context.Context, collection, id string) ([]byte, error)
    Set(ctx context.Context, collection, id string, data []byte) error
    Delete(ctx context.Context, collection, id string) error

    // Bulk operations
    GetMany(ctx context.Context, collection string, ids []string) ([][]byte, error)
    SetMany(ctx context.Context, collection string, docs map[string][]byte) error

    // Queries
    Query(ctx context.Context, collection string, query QuerySpec) ([][]byte, error)
    Count(ctx context.Context, collection string, query QuerySpec) (int64, error)

    // Schema management
    CreateCollection(ctx context.Context, collection string, schema Schema) error
    CreateIndex(ctx context.Context, collection string, index IndexSpec) error

    // Transactions (optional)
    BeginTx(ctx context.Context) (Transaction, error)
}
```

### Snapshot Adapter

```go
// SnapshotAdapter stores aggregate snapshots
type SnapshotAdapter interface {
    Save(ctx context.Context, streamID string, version int64, data []byte) error
    Load(ctx context.Context, streamID string) (*SnapshotRecord, error)
    Delete(ctx context.Context, streamID string) error
}
```

### Outbox Adapter

```go
// OutboxAdapter for reliable event publishing
type OutboxAdapter interface {
    // Store outbox entry (in same tx as events)
    Store(ctx context.Context, tx Transaction, entries []OutboxEntry) error

    // Fetch unpublished entries
    FetchPending(ctx context.Context, limit int) ([]OutboxEntry, error)

    // Mark as published
    MarkPublished(ctx context.Context, ids []string) error

    // Cleanup old entries
    Cleanup(ctx context.Context, olderThan time.Duration) error
}
```

## PostgreSQL Adapter

```go
package postgres

import (
    "database/sql"
    "go-mink.dev"
)

type PostgresAdapter struct {
    db     *sql.DB
    schema string
}

func NewAdapter(connStr string, opts ...Option) (*PostgresAdapter, error) {
    db, err := sql.Open("pgx", connStr)
    if err != nil {
        return nil, err
    }

    adapter := &PostgresAdapter{
        db:     db,
        schema: "go-mink",
    }

    for _, opt := range opts {
        opt(adapter)
    }

    return adapter, nil
}

// Options
func WithSchema(schema string) Option {
    return func(a *PostgresAdapter) { a.schema = schema }
}

func WithMaxConnections(n int) Option {
    return func(a *PostgresAdapter) { a.db.SetMaxOpenConns(n) }
}

// Initialize creates required tables
func (a *PostgresAdapter) Initialize(ctx context.Context) error {
    // Create schema
    _, err := a.db.ExecContext(ctx, fmt.Sprintf(
        `CREATE SCHEMA IF NOT EXISTS %s`, a.schema,
    ))
    if err != nil {
        return err
    }

    // Create tables (streams, events, checkpoints, outbox)
    return a.runMigrations(ctx)
}
```

## MongoDB Adapter

```go
package main

import (
    "context"

    mink "go-mink.dev"
    "go-mink.dev/adapters/mongodb"
    "go.mongodb.org/mongo-driver/v2/mongo/readconcern"
    "go.mongodb.org/mongo-driver/v2/mongo/readpref"
    "go.mongodb.org/mongo-driver/v2/mongo/writeconcern"
)

func main() {
    ctx := context.Background()

    adapter, err := mongodb.NewAdapter(
        "mongodb://localhost:27017/mink?replicaSet=rs0",
        mongodb.WithDatabase("mink"),
        mongodb.WithTransactionMode(mongodb.TransactionModeRequired),
        mongodb.WithSubscriptionMode(mongodb.SubscriptionModeAuto),
        mongodb.WithWriteConcern(writeconcern.Majority()),
        mongodb.WithReadConcern(readconcern.Majority()),
        mongodb.WithReadPreference(readpref.Primary()),
    )
    if err != nil {
        panic(err)
    }
    defer adapter.Close()

    if err := adapter.Initialize(ctx); err != nil {
        panic(err)
    }

    store := mink.New(adapter)
    _ = store
}
```

The MongoDB adapter uses the official v2 driver (`go.mongodb.org/mongo-driver/v2/...`) and stores serialized event payloads as BSON binary so JSON, MessagePack, Protocol Buffers, or custom serializers remain storage-independent. Event metadata is stored as a BSON document for filtering and diagnostics.

MongoDB supports the same core adapter interfaces as PostgreSQL: event append/load, hybrid change-stream subscriptions with polling fallback, snapshots, checkpoints, stream/projection CLI queries, diagnostics, migration metadata, schema generation, idempotency, saga storage, outbox storage, generic read-model repositories, and adapter-backed projection transactions.

### Transaction Modes

| Mode | Behavior |
|------|----------|
| `TransactionModeAuto` | Probe transactions during initialization; use them when available, otherwise use standalone best-effort writes |
| `TransactionModeRequired` | Fail initialization when transactions are unavailable |
| `TransactionModeDisabled` | Always use standalone best-effort writes |

Use `TransactionModeRequired` in production with a replica set or sharded cluster. Standalone MongoDB mode is intended for local development; optimistic stream concurrency is preserved, but cross-collection atomicity is not guaranteed.

`EventStoreWithOutbox` writes event+outbox records atomically only when MongoDB transactions are active. Without transactions the adapter returns `adapters.ErrOutboxAtomicityUnsupported` before writing, and the wrapper falls back to its existing non-atomic scheduling path.

### Subscription Modes

| Mode | Behavior |
|------|----------|
| `SubscriptionModeAuto` | Uses change streams as a low-latency wake-up signal when available, then falls back to polling |
| `SubscriptionModePolling` | Uses ordered polling only |
| `SubscriptionModeChangeStream` | Requires change streams and returns an error if they cannot start |

Ordered delivery still comes from `LoadFromPosition`/`Load`; change streams only wake the subscription loop sooner. For long-running workers, set `SubscriptionOptions.ResumeTokenKey` so MongoDB stores the latest change-stream resume token in `mink_resume_tokens` and resumes future watches with `resumeAfter`.

```go
events, err := adapter.SubscribeAll(ctx, 0, adapters.SubscriptionOptions{
    ResumeTokenKey: "orders-projection",
})
```

### Production Notes

For production MongoDB deployments, prefer:

- `TransactionModeRequired`
- `writeconcern.Majority()`
- `readconcern.Majority()`
- `readpref.Primary()`

`mink schema generate` intentionally does not auto-shard MongoDB collections. The core event collection has unique indexes on `{ stream_id, version }` and `{ global_position }`; MongoDB requires unique indexes on sharded collections to include the shard key as the index key or prefix. Shard read-model collections by each model's access pattern instead of copying the event-store layout.

## Redis Adapter

```go
package redis

import (
    "github.com/redis/go-redis/v9"
    "go-mink.dev"
)

type RedisAdapter struct {
    client *redis.Client
    prefix string
}

func NewAdapter(addr string, opts ...Option) *RedisAdapter {
    client := redis.NewClient(&redis.Options{Addr: addr})

    return &RedisAdapter{
        client: client,
        prefix: "go-mink:",
    }
}

// Optimized for snapshots - fast key-value access
func (a *RedisAdapter) Save(ctx context.Context, streamID string,
    version int64, data []byte) error {

    key := fmt.Sprintf("%ssnapshot:%s", a.prefix, streamID)

    value, _ := json.Marshal(SnapshotRecord{
        Version: version,
        Data:    data,
        SavedAt: time.Now(),
    })

    return a.client.Set(ctx, key, value, 0).Err()
}

func (a *RedisAdapter) Load(ctx context.Context,
    streamID string) (*SnapshotRecord, error) {

    key := fmt.Sprintf("%ssnapshot:%s", a.prefix, streamID)

    data, err := a.client.Get(ctx, key).Bytes()
    if err == redis.Nil {
        return nil, nil
    }
    if err != nil {
        return nil, err
    }

    var record SnapshotRecord
    json.Unmarshal(data, &record)
    return &record, nil
}
```

## Memory Adapter (Testing)

```go
package memory

// InMemoryAdapter for unit tests
type InMemoryAdapter struct {
    mu      sync.RWMutex
    streams map[string][]StoredEvent
    global  []StoredEvent
}

func NewAdapter() *InMemoryAdapter {
    return &InMemoryAdapter{
        streams: make(map[string][]StoredEvent),
    }
}

// Perfect for unit tests - no external dependencies
func (a *InMemoryAdapter) Append(ctx context.Context, streamID string,
    events []EventRecord, expectedVersion int64) ([]StoredEvent, error) {

    a.mu.Lock()
    defer a.mu.Unlock()

    stream := a.streams[streamID]
    currentVersion := int64(len(stream))

    if expectedVersion >= 0 && currentVersion != expectedVersion {
        return nil, go-mink.ErrConcurrencyConflict
    }

    var stored []StoredEvent
    for _, e := range events {
        currentVersion++
        se := StoredEvent{
            ID:             uuid.NewString(),
            StreamID:       streamID,
            Type:           e.Type,
            Data:           e.Data,
            Version:        currentVersion,
            GlobalPosition: uint64(len(a.global) + 1),
            Timestamp:      time.Now(),
        }
        stored = append(stored, se)
        a.global = append(a.global, se)
    }

    a.streams[streamID] = append(stream, stored...)
    return stored, nil
}
```

## Custom Adapter Template

```go
package myadapter

import "go-mink.dev"

// Implement your own adapter
type MyCustomAdapter struct {
    // Your storage client
}

// Ensure interface compliance at compile time
var _ go-mink.EventStoreAdapter = (*MyCustomAdapter)(nil)

func NewAdapter( /* your config */ ) *MyCustomAdapter {
    return &MyCustomAdapter{}
}

// Implement all EventStoreAdapter methods...
func (a *MyCustomAdapter) Append(ctx context.Context, streamID string,
    events []go-mink.EventRecord, expectedVersion int64) ([]go-mink.StoredEvent, error) {
    // Your implementation
}

// Register with go-mink
func init() {
    go-mink.RegisterAdapter("myadapter", func(config map[string]interface{}) (go-mink.EventStoreAdapter, error) {
        // Create adapter from config
        return NewAdapter(), nil
    })
}
```

## Configuration

```go
// Mix and match adapters via configuration
store, _ := go-mink.New(go-mink.Config{
    // Events in PostgreSQL for ACID guarantees
    EventStore: go-mink.AdapterConfig{
        Type: "postgres",
        Connection: "postgres://localhost/mydb",
        Options: map[string]interface{}{
            "schema": "events",
            "maxConnections": 25,
        },
    },

    // Read models in MongoDB for flexible queries
    ReadModels: go-mink.AdapterConfig{
        Type: "mongodb",
        Connection: "mongodb://localhost:27017",
        Options: map[string]interface{}{
            "database": "readmodels",
        },
    },

    // Snapshots in Redis for speed
    Snapshots: go-mink.AdapterConfig{
        Type: "redis",
        Connection: "redis://localhost:6379",
    },
})
```

---

Next: [API Design →](/docs/advanced/api-design)
