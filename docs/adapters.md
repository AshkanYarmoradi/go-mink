---
layout: default
title: Adapters
nav_order: 6
permalink: /docs/adapters
---

# Adapter System
{: .no_toc }

## Table of contents
{: .no_toc .text-delta }

1. TOC
{:toc}

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

### Event Store Adapter

```go
// EventStoreAdapter is implemented by each database driver
type EventStoreAdapter interface {
    // Core operations
    Append(ctx context.Context, streamID string, events []EventRecord, 
           expectedVersion int64) ([]StoredEvent, error)
    Load(ctx context.Context, streamID string, fromVersion int64) ([]StoredEvent, error)
    
    // Subscriptions
    SubscribeAll(ctx context.Context, fromPosition uint64) (<-chan StoredEvent, error)
    SubscribeCategory(ctx context.Context, category string, fromPosition uint64) (<-chan StoredEvent, error)
    
    // Metadata
    GetStreamInfo(ctx context.Context, streamID string) (*StreamInfo, error)
    GetLastPosition(ctx context.Context) (uint64, error)
    
    // Lifecycle
    Initialize(ctx context.Context) error
    Close() error
}
```

### Read Model Adapter

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
    "github.com/AshkanYarmoradi/go-mink"
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
package mongodb

import (
    "go.mongodb.org/mongo-driver/mongo"
    "github.com/AshkanYarmoradi/go-mink"
)

type MongoAdapter struct {
    client   *mongo.Client
    database string
}

func NewAdapter(uri, database string) (*MongoAdapter, error) {
    client, err := mongo.Connect(context.Background(), 
        options.Client().ApplyURI(uri))
    if err != nil {
        return nil, err
    }
    
    return &MongoAdapter{
        client:   client,
        database: database,
    }, nil
}

// MongoDB-specific: Flexible document queries
func (a *MongoAdapter) Query(ctx context.Context, collection string, 
    query QuerySpec) ([][]byte, error) {
    
    coll := a.client.Database(a.database).Collection(collection)
    
    filter := buildMongoFilter(query.Filters)
    opts := options.Find().
        SetSort(buildMongoSort(query.OrderBy)).
        SetLimit(int64(query.Limit)).
        SetSkip(int64(query.Offset))
    
    cursor, err := coll.Find(ctx, filter, opts)
    if err != nil {
        return nil, err
    }
    defer cursor.Close(ctx)
    
    var results [][]byte
    for cursor.Next(ctx) {
        results = append(results, cursor.Current)
    }
    
    return results, cursor.Err()
}
```

## Redis Adapter

```go
package redis

import (
    "github.com/redis/go-redis/v9"
    "github.com/AshkanYarmoradi/go-mink"
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

import "github.com/AshkanYarmoradi/go-mink"

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

Next: [CLI →](cli)
