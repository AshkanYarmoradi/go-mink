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

### GDPR Erasure Sub-Interfaces (Optional)

For the GDPR **right to erasure**, adapters may implement small, *optional* sub-interfaces
so `DataEraser` can reach a subject's data. Support is detected by type assertion — a store
that does not implement its purger is reported as `Skipped`, never an error. The in-memory
and PostgreSQL stores implement all of these.

```go
// Delete a data subject's rows from a sibling store (matched on the noted column).
type SubjectAuditPurger interface {        // audit trail — actor OR aggregate_id == subject
    DeleteAuditBySubject(ctx context.Context, subjectID string) (int64, error)
}
type SubjectSagaPurger interface {         // saga state — correlation_id == subject
    DeleteSagasBySubject(ctx context.Context, subjectID string) (int64, error)
}
type SubjectOutboxPurger interface {       // outbox rows — aggregate_id == subject
    DeleteOutboxBySubject(ctx context.Context, subjectID string) (int64, error)
}
type SubjectIdempotencyPurger interface {  // idempotency records — aggregate_id == subject
    DeleteIdempotencyBySubject(ctx context.Context, subjectID string) (int64, error)
}

// Footprint-aware counterparts (preferred when present). The rows the library writes are
// NOT keyed by the bare subject id — the outbox's AggregateID is the producing STREAM id
// ("User-u1", never "u1"); the audit/idempotency AggregateID is the raw aggregate id
// ("u1") — so mink's erasers pass mink.SubjectFootprintIDs(subject, fp): the subject id
// plus every EXCLUSIVE footprint stream (streams shared with other subjects are skipped —
// never purged nor counted — and reported as SharedStreamsSkipped), and the aggregate id
// after the first '-' of each exclusive stream ONLY when the eraser is configured with
// mink.WithDerivedAggregateIDs (none by default). The saga purger/counter receives the
// same set as candidate correlation ids; counters receive the identical set during
// verification. Contract: an empty/all-empty id slice returns (0, nil) without touching
// the store; ids are de-duplicated and matched exactly; counters return the number of
// matching rows.
type SubjectOutboxFootprintPurger interface {
    DeleteOutboxByAggregateIDs(ctx context.Context, aggregateIDs []string) (int64, error)
}
type SubjectAuditFootprintPurger interface {
    DeleteAuditByAggregateIDs(ctx context.Context, aggregateIDs []string) (int64, error)
}
type SubjectIdempotencyFootprintPurger interface {
    DeleteIdempotencyByAggregateIDs(ctx context.Context, aggregateIDs []string) (int64, error)
}
type SubjectSagaFootprintPurger interface {
    DeleteSagasByCorrelationIDs(ctx context.Context, correlationIDs []string) (int64, error)
}

// Residual counters let DataEraser.Verify and the erasure certificate prove a store
// clean (or disclose it as unchecked) instead of certifying from the event log alone.
type SubjectOutboxCounter interface {
    CountOutboxByAggregateIDs(ctx context.Context, aggregateIDs []string) (int64, error)
}
type SubjectAuditCounter interface {        // actor == subject OR aggregate_id in ids
    CountAuditBySubject(ctx context.Context, subjectID string, aggregateIDs []string) (int64, error)
}
type SubjectIdempotencyCounter interface {
    CountIdempotencyByAggregateIDs(ctx context.Context, aggregateIDs []string) (int64, error)
}
type SubjectSagaCounter interface {
    CountSagasByCorrelationIDs(ctx context.Context, correlationIDs []string) (int64, error)
}

// Type-scoped saga lookup. FindByCorrelationID is unscoped by saga type, so two saga
// types sharing a correlation id could hydrate from each other's row; SagaManager
// prefers this method when the store offers it (returns an error satisfying
// errors.Is(err, ErrSagaNotFound) when absent).
type SagaCorrelationTypeFinder interface {
    FindByCorrelationIDAndType(ctx context.Context, correlationID, sagaType string) (*SagaState, error)
}
```

Wrap a store as a `mink.SubjectErasable` with `mink.NewAuditSubjectEraser`,
`NewSagaSubjectEraser`, `NewOutboxSubjectEraser`, `NewIdempotencySubjectEraser`, or
`NewSnapshotSubjectEraser`, then register it on the eraser via `WithSubjectStore`. The
built-in erasers use the footprint purger when the store implements it (reporting
`FootprintAware`), fall back to the id-equality purger otherwise, and expose the counters
through `mink.SubjectResidualCounter`. See the
[GDPR guide](/docs/security#sibling-stores--audit-saga-snapshots-outbox-idempotency).

:::note PostgreSQL implementation
Id lists are bound as a single `= ANY($n::text[])` array parameter, so commas, braces,
quotes, backslashes and the word `NULL` in an id match literally. `ListStreams` likewise
treats its prefix as literal text (`%`, `_` and `\` are backslash-escaped and matched with
`LIKE` under PostgreSQL's default escape character — the same mechanism as the read-model
`CONTAINS` filter and category subscriptions, so it also works with
`standard_conforming_strings=off`). `StreamsBySubject` tolerates a malformed `$subjects`
tag on an *unrelated* row by falling back to a Go-side scan that skips it; a malformed row
whose text may name the requested subject, on a stream no well-formed row resolved, makes
the call fail with `*postgres.SubjectTagMalformedError` (`errors.Is` →
`postgres.ErrSubjectTagMalformed`; carries the subject id and row/stream counts only)
rather than returning a silently partial footprint. Results are sorted bytewise in Go on
both paths.
:::

### Subject Index (Optional)

To resolve *which streams touch a subject* without scanning the whole store, an adapter (or
a standalone index) may implement:

```go
type SubjectIndexAdapter interface { // read side
    StreamsBySubject(ctx context.Context, subjectID string) ([]string, error)
}
type SubjectIndexWriter interface {  // write side (idempotent)
    IndexSubjects(ctx context.Context, streamID string, subjectIDs []string) error
}
type SubjectIndexPurger interface {  // optional: lets DataEraser.WithSubjectIndexPurge drop
    DeleteSubject(ctx context.Context, subjectID string) error // an erased subject's entries
}
```

The PostgreSQL event-store adapter implements a **drift-free** `SubjectIndexAdapter` by
querying the events' own `$subjects` tags in JSONB — no separate table to fall out of sync.
`mink.MemorySubjectIndex` and `postgres.SubjectIndex` implement all three; inject either
into a resolver with `mink.WithResolverIndex`. See the
[subject index section](/docs/security#subject-index--backfill).

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

:::note Serializer compatibility
The adapter stores event `data` in a `JSONB` column, so it requires a serializer
that emits JSON text. It advertises this via `adapters.JSONDataAdapter`
(`RequiresJSONData() bool`), and `mink.New` detects a binary serializer
(`serializer/msgpack`, `serializer/protobuf`) up front and returns
`mink.ErrBinarySerializerUnsupported` from the first `Append`/`SaveAggregate`
(before any write), rather than the cryptic driver error the `INSERT` would
otherwise raise. Use the default JSON serializer with this adapter.
:::

## MongoDB Adapter

```go
package mongodb

import (
    "go.mongodb.org/mongo-driver/mongo"
    "go-mink.dev"
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

:::note Detached copies
The sketch above is simplified. The shipped in-memory adapter stores private copies of
what it is given and hands out **detached copies** of what it returns: events from `Load`,
`LoadFromPosition(Filtered)`, `GetStreamEvents` and subscriptions carry cloned `Data` and
`Metadata.Custom`, the `StoredEvent`s returned by `Append` carry the caller's own buffers
rather than the log's, `RewriteEventData` copies its input, and the idempotency store copies
`Response` on `Store`/`Get`. Mutating a returned event or record can therefore never alter
stored history or undo a redaction — the same isolation a database adapter gives you.
:::

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
