---
title: Read Models
sidebar_position: 5
---

# Read Models & Projections

---

## Overview

Projections transform events into optimized read models. go-mink supports three projection strategies:

```
Events Stream                    Read Models
┌─────────────┐
│ OrderCreated│──┐              ┌─────────────────┐
├─────────────┤  │  Inline      │ OrderSummary    │
│ ItemAdded   │──┼─────────────►│ (same tx)       │
├─────────────┤  │              └─────────────────┘
│ ItemAdded   │──┤
├─────────────┤  │              ┌─────────────────┐
│ OrderShipped│──┼──────────────│ ShippingReport  │
└─────────────┘  │  Async       │ (background)    │
                 │              └─────────────────┘
                 │
                 │              ┌─────────────────┐
                 └──────────────│ LiveDashboard   │
                    Live        │ (real-time)     │
                                └─────────────────┘
```

## Projection Interfaces

### Base Projection Interface

All projections implement the base `Projection` interface:

```go
type Projection interface {
    // Name returns a unique identifier for this projection
    Name() string

    // HandledEvents returns the list of event types this projection handles
    HandledEvents() []string

    // Apply processes a single event
    Apply(ctx context.Context, event StoredEvent) error
}
```

### 1. Inline Projections

Updated synchronously when events are appended - **strongly consistent**.

```go
type InlineProjection interface {
    Projection
    // Inline projections are processed in the same execution context
}

// Example: Order Summary projection using ProjectionBase
type OrderSummaryProjection struct {
    mink.ProjectionBase  // Embeds name and handled events
    repo *mink.InMemoryRepository[OrderSummary]
}

func NewOrderSummaryProjection(repo *mink.InMemoryRepository[OrderSummary]) *OrderSummaryProjection {
    return &OrderSummaryProjection{
        ProjectionBase: mink.NewProjectionBase("OrderSummary",
            "OrderCreated", "ItemAdded", "OrderShipped"),
        repo: repo,
    }
}

func (p *OrderSummaryProjection) Apply(ctx context.Context, event mink.StoredEvent) error {
    switch event.Type {
    case "OrderCreated":
        var e OrderCreated
        if err := json.Unmarshal(event.Data, &e); err != nil {
            return err
        }
        return p.repo.Insert(ctx, &OrderSummary{
            OrderID:    e.OrderID,
            CustomerID: e.CustomerID,
            Status:     "Created",
            CreatedAt:  e.CreatedAt,
        })

    case "ItemAdded":
        var e ItemAdded
        if err := json.Unmarshal(event.Data, &e); err != nil {
            return err
        }
        return p.repo.Update(ctx, e.OrderID, func(s *OrderSummary) {
            s.ItemCount += e.Quantity
            s.TotalAmount += e.Price * float64(e.Quantity)
        })

    case "OrderShipped":
        var e OrderShipped
        if err := json.Unmarshal(event.Data, &e); err != nil {
            return err
        }
        return p.repo.Update(ctx, e.OrderID, func(s *OrderSummary) {
            s.Status = "Shipped"
            s.ShippedAt = &e.ShippedAt
        })
    }
    return nil
}
```

### 2. Async Projections

Processed in the background - **eventually consistent** but more scalable.

```go
type AsyncProjection interface {
    Projection

    // Batch processing for efficiency
    ApplyBatch(ctx context.Context, events []StoredEvent) error

    // Batch configuration
    BatchSize() int
}

// Example using AsyncProjectionBase
type AnalyticsProjection struct {
    mink.AsyncProjectionBase
    db *sql.DB
}

func NewAnalyticsProjection(db *sql.DB) *AnalyticsProjection {
    return &AnalyticsProjection{
        AsyncProjectionBase: mink.NewAsyncProjectionBase(
            "Analytics",
            100, // batch size
            "OrderCreated", "OrderCompleted",
        ),
        db: db,
    }
}

func (p *AnalyticsProjection) ApplyBatch(ctx context.Context, events []mink.StoredEvent) error {
    tx, _ := p.db.BeginTx(ctx, nil)
    defer tx.Rollback()

    for _, event := range events {
        switch event.Type {
        case "OrderCreated":
            tx.Exec(`
                INSERT INTO daily_stats (date, order_count)
                VALUES ($1, 1)
                ON CONFLICT (date) DO UPDATE
                SET order_count = daily_stats.order_count + 1
            `, event.Timestamp.Truncate(24*time.Hour))
        }
    }

    return tx.Commit()
}
```

### 3. Live Projections

Real-time subscriptions for dashboards and notifications.

```go
type LiveProjection interface {
    Projection

    // Called for each event in real-time
    OnEvent(ctx context.Context, event StoredEvent)
}

// Example using LiveProjectionBase
type DashboardProjection struct {
    mink.LiveProjectionBase
}

func NewDashboardProjection() *DashboardProjection {
    return &DashboardProjection{
        LiveProjectionBase: mink.NewLiveProjectionBase(
            "Dashboard",
            "OrderCreated", "OrderShipped",
        ),
    }
}

func (p *DashboardProjection) OnEvent(ctx context.Context, event mink.StoredEvent) {
    p.Send(fmt.Sprintf("Event %s on stream %s", event.Type, event.StreamID))
}

// Consume updates
func (p *DashboardProjection) Updates() <-chan string {
    return p.LiveProjectionBase.Updates()
}
```

## Projection Engine

The `ProjectionEngine` orchestrates all projection types:

```go
// Create checkpoint store for async projections
checkpointStore := memory.NewCheckpointStore()

// Create projection engine
engine := mink.NewProjectionEngine(store,
    mink.WithCheckpointStore(checkpointStore),
)

// Register inline projection (synchronous)
summaryProjection := NewOrderSummaryProjection(repo)
if err := engine.RegisterInline(summaryProjection); err != nil {
    log.Fatal(err)
}

// Register async projection (background)
analyticsProjection := NewAnalyticsProjection(db)
if err := engine.RegisterAsync(analyticsProjection, mink.AsyncOptions{
    BatchSize:    100,
    Interval:     time.Second,
    Workers:      4,
    RetryPolicy:  mink.NewExponentialBackoffRetry(100*time.Millisecond, 5*time.Second, 3),
}); err != nil {
    log.Fatal(err)
}

// Register live projection (real-time)
dashboardProjection := NewDashboardProjection()
if err := engine.RegisterLive(dashboardProjection); err != nil {
    log.Fatal(err)
}

// Start the engine
if err := engine.Start(ctx); err != nil {
    log.Fatal(err)
}
defer engine.Stop(ctx)

// Process events through projections
events, _ := store.LoadRaw(ctx, streamID, 0)
engine.ProcessInlineProjections(ctx, events)
engine.NotifyLiveProjections(ctx, events)
```

### Poison-event handling

By default an async projection that keeps failing on the same event exhausts its
retry budget and stops in the `Faulted` state, blocking all later events. Set
`AsyncOptions.OnPoisonEvent` to skip (dead-letter) the offending event and keep
the projection moving. Return `nil` to advance past the event; return an error to
stop the worker.

```go
engine.RegisterAsync(analyticsProjection, mink.AsyncOptions{
    BatchSize:   100,
    Workers:     4,
    MaxRetries:  3,
    OnPoisonEvent: func(ctx context.Context, event mink.StoredEvent, cause error) error {
        // Record it for later inspection, then skip so the projection continues.
        log.Printf("dead-lettering poison event %s@%d: %v",
            event.Type, event.GlobalPosition, cause)
        deadLetter.Save(ctx, event, cause)
        return nil // returning a non-nil error would stop the worker instead
    },
})
```

### Projection Status

Monitor projection health:

```go
// Get single projection status
status, err := engine.GetStatus("OrderSummary")
fmt.Printf("State: %s, Position: %d, Lag: %d\n",
    status.State, status.Position, status.Lag)

// Get all projection statuses
statuses := engine.GetAllStatuses()
for name, status := range statuses {
    fmt.Printf("%s: %s (error: %v)\n", name, status.State, status.LastError)
}
```

### Pausing, Resuming, and Rebuilding

Async and live projections can be paused and resumed at runtime without
stopping the whole engine — useful for maintenance or for taking a consistent
snapshot before a rebuild. `Rebuild` resets a single async projection's
checkpoint and replays the event log from the beginning, then resumes from the
rebuilt position. All three return `mink.ErrProjectionNotFound` for an unknown
name.

```go
// Temporarily stop an async/live projection (it stays registered and alive).
if err := engine.Pause("Analytics"); err != nil {
    log.Fatal(err)
}

// ... do maintenance, deploy a new read-model schema, etc. ...

// Resume processing from where it left off.
if err := engine.Resume("Analytics"); err != nil {
    log.Fatal(err)
}

// Replay the whole log into one projection. For a consistent rebuild the
// projection should be quiescent first (engine stopped, or projection paused).
if err := engine.Rebuild(ctx, "Analytics"); err != nil {
    log.Fatal(err)
}
```

## Read Model Repository

Generic repository for read model storage:

```go
// Interface definition
type ReadModelRepository[T any] interface {
    Get(ctx context.Context, id string) (*T, error)
    GetMany(ctx context.Context, ids []string) ([]*T, error)
    Find(ctx context.Context, query Query) ([]*T, error)
    FindOne(ctx context.Context, query Query) (*T, error)
    Count(ctx context.Context, query Query) (int64, error)
    Insert(ctx context.Context, model *T) error
    Update(ctx context.Context, id string, fn func(*T)) error
    Upsert(ctx context.Context, model *T) error
    Delete(ctx context.Context, id string) error
    DeleteMany(ctx context.Context, query Query) (int64, error)
    Clear(ctx context.Context) error
}

// Both the in-memory and PostgreSQL repositories also provide Exists(ctx, id)
// and GetAll(ctx) helpers beyond the interface above.

// In-memory implementation (great for testing)
repo := mink.NewInMemoryRepository[OrderSummary](func(o *OrderSummary) string {
    return o.OrderID  // ID extractor function
})

// CRUD operations
repo.Insert(ctx, &OrderSummary{OrderID: "order-1", Status: "Created"})

summary, err := repo.Get(ctx, "order-1")

repo.Update(ctx, "order-1", func(s *OrderSummary) {
    s.Status = "Shipped"
})

repo.Delete(ctx, "order-1")
```

### PostgreSQL Repository

For production use, go-mink provides a PostgreSQL-backed repository with **automatic schema migration**:

```go
import "go-mink.dev/adapters/postgres"

// Define your read model with mink struct tags
type OrderSummary struct {
    OrderID     string    `mink:"order_id,pk"`        // Primary key
    CustomerID  string    `mink:"customer_id,index"`  // Creates an index
    Status      string    `mink:"status"`
    ItemCount   int       `mink:"item_count"`
    TotalAmount float64   `mink:"total_amount"`
    CreatedAt   time.Time `mink:"created_at"`
    UpdatedAt   time.Time `mink:"updated_at"`
}

// Create repository with auto-migration
repo, err := postgres.NewPostgresRepository[OrderSummary](db,
    postgres.WithReadModelSchema("projections"),
    postgres.WithTableName("order_summaries"),
)
if err != nil {
    log.Fatal(err)
}

// Use exactly like in-memory repository
repo.Insert(ctx, &OrderSummary{
    OrderID:    "order-1",
    CustomerID: "cust-123",
    Status:     "pending",
})

// Queries work with full SQL support
query := mink.NewQuery().
    Where("status", mink.FilterOpEq, "pending").
    And("total_amount", mink.FilterOpGt, 100.0).
    OrderByDesc("created_at").
    WithLimit(10)

orders, err := repo.Find(ctx, query.Build())
```

#### Supported Struct Tags

| Tag | Description |
|-----|-------------|
| `mink:"column_name"` | Sets the column name (default: snake_case of field) |
| `mink:"-"` | Skip this field |
| `mink:"col,pk"` | Primary key |
| `mink:"col,index"` | Create index on column |
| `mink:"col,unique"` | Unique constraint (also creates index) |
| `mink:"col,nullable"` | Allow NULL values (see nullability note below) |
| `mink:"col,default=value"` | Default value (see security note below) |
| `mink:"col,type=VARCHAR(100)"` | Override SQL type (see security note below) |

> **Security Note**: The `default=` and `type=` values are validated against common SQL injection patterns but are interpolated into DDL statements. Only use static, hardcoded values in your source code. Never construct these tag values from user input or external sources.

> **Nullability Note**: `nullable` governs both the schema and reads. The column is emitted without `NOT NULL`, and a stored `NULL` in a nullable **non-pointer scalar** field (`string`, the `int`/`uint` kinds, `float32/64`, `bool`, `time.Time`) is read back as that field's Go zero value (`""`, `0`, `false`, zero time) — one `NULL` cell never aborts a `Find`/`Get`. Writes are unaffected: persisting a zero-value scalar stores that zero value, **not** `NULL`. To persist and read back a value distinguishable from the zero value, use a **pointer** field (`*string`, …), which reads `NULL` as `nil`. A `NULL` in a column that is *not* tagged `nullable` (e.g. an external write) surfaces a typed `*mink.NullColumnError` (matches `errors.Is(err, mink.ErrNullColumn)`) naming the column and field, instead of the driver's opaque `converting NULL to <type>` message. A non-`NULL` value that does not fit a nullable numeric field (e.g. a value beyond `int8`, or a negative value read into an unsigned field — reachable only via an out-of-band write) surfaces a typed `*mink.ColumnValueRangeError` (`errors.Is(err, mink.ErrColumnValueRange)`) rather than silently truncating; widen the field type to resolve it.

#### Go Type to SQL Mapping

| Go Type | PostgreSQL Type |
|---------|-----------------|
| `string` | `TEXT` |
| `int`, `int32` | `INTEGER` |
| `int64` | `BIGINT` |
| `float32` | `REAL` |
| `float64` | `DOUBLE PRECISION` |
| `bool` | `BOOLEAN` |
| `time.Time` | `TIMESTAMPTZ` |
| `[]byte` | `BYTEA` |
| `[]T` (slices) | `JSONB` |
| `map`, `struct` | `JSONB` |

> **Note on JSONB types**: While Go slices (other than `[]byte`), maps, and structs are mapped to `JSONB`, the current implementation stores them using Go's native database/sql handling. For complex JSONB data, use `[]byte` with manual JSON marshaling/unmarshaling, or implement custom `sql.Scanner` and `driver.Valuer` interfaces on your types.

> **Note on unsigned integers**: Go's unsigned integer types are mapped to PostgreSQL's signed integer types: `uint` and `uint32` are stored as `INTEGER` (max `2,147,483,647`), and `uint64` is stored as `BIGINT` (max `9,223,372,036,854,775,807`). Values greater than these limits will overflow or be rejected by PostgreSQL. If you need to store larger unsigned values, use an explicit tag such as `mink:"type=NUMERIC"` (or another appropriate type).

#### Transaction Support

Use transactions for consistent updates across multiple read models:

```go
tx, err := db.BeginTx(ctx, nil)
if err != nil {
    return err
}
defer tx.Rollback()

txRepo := repo.WithTx(tx)

// All operations in same transaction
txRepo.Insert(ctx, &OrderSummary{...})
txRepo.Update(ctx, "order-2", func(o *OrderSummary) {
    o.ItemCount++
})

return tx.Commit()
```

#### Schema Migration

The repository automatically:
- Creates the schema if it doesn't exist
- Creates the table with proper column types
- Adds indexes for `index` and `unique` tagged columns
- Adds missing columns when your struct evolves (non-breaking schema changes)

```go
// Disable auto-migration if you manage schema externally
repo, err := postgres.NewPostgresRepository[OrderSummary](db,
    postgres.WithReadModelSchema("projections"),
    postgres.WithAutoMigrate(false),
)

// Or run migration manually
err = repo.Migrate(ctx)
```

### Query Builder

Fluent query construction:

```go
// Build a query. NewQuery returns a *Query builder; pass query.Build() to the repo.
query := mink.NewQuery().
    Where("status", mink.FilterOpEq, "pending").
    And("total_amount", mink.FilterOpGt, 100.0).
    OrderByDesc("created_at").
    WithPagination(1, 10) // page 1, page size 10

// Execute query
orders, err := repo.Find(ctx, query.Build())

// Find single result
order, err := repo.FindOne(ctx, query.Build())

// Count matching records
count, err := repo.Count(ctx, query.Build())
```

### Filter Operators

```go
// Available filter operators (mink.FilterOp constants)
mink.FilterOpEq        // =  (equal)
mink.FilterOpNe        // != (not equal)
mink.FilterOpGt        // >  (greater than)
mink.FilterOpGte       // >= (greater than or equal)
mink.FilterOpLt        // <  (less than)
mink.FilterOpLte       // <= (less than or equal)
mink.FilterOpIn        // IN     (value is one of a list/slice)
mink.FilterOpNotIn     // NOT IN (value is not in a list/slice)
mink.FilterOpLike      // LIKE   (SQL pattern; caller supplies % / _ wildcards)
mink.FilterOpContains  // substring match on text, containment (@>) on JSONB
mink.FilterOpBetween   // BETWEEN (inclusive range; value is a 2-element slice)
mink.FilterOpIsNull    // IS NULL
mink.FilterOpIsNotNull // IS NOT NULL
```

Examples:

```go
// IN: status is one of the listed values (accepts []string, []int, etc.)
mink.NewQuery().Where("status", mink.FilterOpIn, []string{"pending", "shipped"})

// CONTAINS: substring match on a text column ('%' and '_' are escaped, not wildcards)
mink.NewQuery().Where("name", mink.FilterOpContains, "smith")

// CONTAINS: element/containment match on a JSONB column
mink.NewQuery().Where("tags", mink.FilterOpContains, "premium")

// BETWEEN: inclusive range
mink.NewQuery().Where("total_amount", mink.FilterOpBetween, []float64{50, 100})

// IS NULL / IS NOT NULL: the value is ignored
mink.NewQuery().Where("shipped_at", mink.FilterOpIsNull, nil)
```

> **Backend support:** the PostgreSQL repository implements every operator
> above. The in-memory repository (`mink.NewInMemoryRepository`) is a lightweight
> testing helper that does **not** apply filters — use the PostgreSQL repository
> (or another database-backed implementation) for real querying.

### Case-insensitive matching

All string operators (`FilterOpEq`, `FilterOpLike`, `FilterOpContains`) are
**case-sensitive**. There is intentionally no `ILIKE` / case-insensitive
operator (see the design note below). When you need case-insensitive search,
make the *column* case-insensitive rather than reaching for a special operator.

**Option 1 — `citext` column (transparent, no query changes).** Declare the
column as PostgreSQL's case-insensitive text type. `FilterOpEq`,
`FilterOpLike`, and `FilterOpContains` then match case-insensitively on that
column automatically.

```go
type Customer struct {
    ID   string `mink:"id,pk"`
    Name string `mink:"name,type=citext"` // case-insensitive column
}
```

```sql
-- run once per database, before the table is created
CREATE EXTENSION IF NOT EXISTS citext;
```

```go
// matches "Smith", "SMITH", "smith"
mink.NewQuery().Where("name", mink.FilterOpContains, "smith")
```

Because auto-migration emits `name citext`, the extension must already exist —
create it first, or use `WithAutoMigrate(false)` and manage the DDL yourself.

**Option 2 — lowercase shadow column (portable, index-friendly).** Keep a
normalized copy and query it lowercased. Works on any backend and can be
indexed for fast search.

```go
type Customer struct {
    ID        string `mink:"id,pk"`
    Name      string `mink:"name"`
    NameLower string `mink:"name_lower,index"` // set to strings.ToLower(Name) in the projection
}

mink.NewQuery().Where("name_lower", mink.FilterOpContains, strings.ToLower(q))
```

**Option 3 — raw SQL for a one-off.** A read model is a plain table, so use the
`*sql.DB` you already have together with `repo.TableName()`:

```go
rows, err := db.QueryContext(ctx,
    `SELECT * FROM `+repo.TableName()+` WHERE name ILIKE $1`, "%"+q+"%")
// note: you scan the rows yourself; the repository's typed scanner is internal
```

:::note Design note: why there is no `FilterOpILike`
`FilterOp` is the **backend-neutral** query vocabulary shared by the
`ReadModelRepository[T]` interface and *every* adapter. `ILIKE` is a
PostgreSQL-specific keyword. Other engines support case-insensitive matching
too, but express it at different layers — MongoDB via a `$regex` `i` flag or a
collation, MySQL/SQL Server/SQLite often via a case-insensitive collation *by
default* — so there is no shared *operator* to expose, and case-insensitivity is
fundamentally a property of the **data and its collation**, not of the query
operator.

Adding `FilterOpILike` to the shared enum would (1) bake a vendor keyword into a
cross-backend contract, (2) oblige every current and future adapter to emulate
it faithfully or silently diverge — the exact "silently ignored operator" class
of bug that motivated implementing `FilterOpContains` — and (3) invite a
combinatorial explosion of case-insensitive variants (`IContains`, `IEq`,
`IStartsWith`, …). The operator set is kept small and orthogonal, and
case-folding is pushed to the schema (`citext`, a lowercased column, or a
case-insensitive collation) where it belongs. If a portable case-insensitive
match is ever added, it would be defined by its *semantics* — each adapter
implementing it natively (PostgreSQL `ILIKE`, others `LOWER(col) LIKE LOWER(?)`)
with cross-adapter tests — never as the raw `ILIKE` keyword.
:::

## Subscription System

Subscribe to events for projections:

```go
// Event filters
typeFilter := mink.NewEventTypeFilter("OrderCreated", "OrderShipped")
categoryFilter := mink.NewCategoryFilter("Order")
compositeFilter := mink.NewCompositeFilter(typeFilter, categoryFilter)

// Subscription options
opts := mink.SubscriptionOptions{
    FromPosition: 0,                    // Start position
    Filter:       compositeFilter,      // Event filter
    BufferSize:   100,                  // Channel buffer
}

// Create subscription (requires SubscriptionAdapter)
sub, err := mink.NewCatchupSubscription(adapter, opts)
if err != nil {
    log.Fatal(err)
}

// Start receiving events
eventCh, err := sub.Subscribe(ctx)
if err != nil {
    log.Fatal(err)
}

for event := range eventCh {
    fmt.Printf("Received: %s at position %d\n", event.Type, event.GlobalPosition)
}
```

## Projection Rebuilding

Rebuild projections from the event log:

```go
// Create rebuilder
rebuilder := mink.NewProjectionRebuilder(store, checkpointStore)

// Create progress callback
progress := &mink.RebuildProgress{
    OnProgress: func(processed, total uint64) {
        pct := float64(processed) / float64(total) * 100
        fmt.Printf("Progress: %.1f%% (%d/%d)\n", pct, processed, total)
    },
    OnComplete: func() {
        fmt.Println("Rebuild complete!")
    },
    OnError: func(err error) {
        fmt.Printf("Error: %v\n", err)
    },
}

// Rebuild single projection
err := rebuilder.Rebuild(ctx, summaryProjection, mink.RebuildOptions{
    BatchSize: 1000,
    Progress:  progress,
})

// Rebuild all projections
err := rebuilder.RebuildAll(ctx, []mink.Projection{
    summaryProjection,
    analyticsProjection,
}, mink.RebuildOptions{BatchSize: 1000})
```

### Parallel Rebuilding

Rebuild multiple projections concurrently:

```go
parallelRebuilder := mink.NewParallelRebuilder(store, checkpointStore, 4) // 4 workers

err := parallelRebuilder.RebuildAll(ctx, []mink.Projection{
    summaryProjection,
    analyticsProjection,
    reportProjection,
}, mink.RebuildOptions{
    BatchSize: 1000,
})
```

### Clearable Projections

Projections that can be cleared before rebuild:

```go
type Clearable interface {
    Clear(ctx context.Context) error
}

// Implement on your projection
func (p *OrderSummaryProjection) Clear(ctx context.Context) error {
    return p.repo.Clear(ctx)
}

// Rebuilder automatically clears if projection implements Clearable
```

## Retry Policy

Configure retry behavior for async projections:

```go
// Exponential backoff with jitter
retryPolicy := mink.NewExponentialBackoffRetry(
    100*time.Millisecond,  // Initial delay
    5*time.Second,         // Max delay
    3,                     // Max attempts
)

engine.RegisterAsync(projection, mink.AsyncOptions{
    RetryPolicy: retryPolicy,
})
```

## Checkpoint Storage

Checkpoints track projection progress:

```go
// In-memory checkpoint store (for testing)
checkpointStore := memory.NewCheckpointStore()

// Get/Set checkpoints
pos, err := checkpointStore.GetCheckpoint(ctx, "OrderSummary")
err = checkpointStore.SetCheckpoint(ctx, "OrderSummary", 100)

// Get checkpoint with timestamp
pos, timestamp, err := checkpointStore.GetCheckpointWithTimestamp(ctx, "OrderSummary")

// List all checkpoints
checkpoints, err := checkpointStore.GetAllCheckpoints(ctx)
```

## Complete Example

See the [projections example](https://github.com/AshkanYarmoradi/go-mink/tree/main/examples/projections) for a complete working demonstration.

---

Next: [Adapters →](adapters)
