---
layout: default
title: Read Models
nav_order: 5
permalink: /docs/read-models
---

# Read Models & Projections
{: .no_toc }

## Table of contents
{: .no_toc .text-delta }

1. TOC
{:toc}

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

## Read Model Repository

Generic repository for read model storage:

```go
// Interface definition
type ReadModelRepository[T any] interface {
    Insert(ctx context.Context, model *T) error
    Get(ctx context.Context, id string) (*T, error)
    Update(ctx context.Context, id string, fn func(*T)) error
    Delete(ctx context.Context, id string) error
    Query(ctx context.Context, query Query) ([]*T, error)
    FindOne(ctx context.Context, query Query) (*T, error)
    Count(ctx context.Context, query Query) (int, error)
    Exists(ctx context.Context, id string) (bool, error)
    GetAll(ctx context.Context) ([]*T, error)
    Clear(ctx context.Context) error
}

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
import "github.com/AshkanYarmoradi/go-mink/adapters/postgres"

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
| `mink:"col,nullable"` | Allow NULL values |
| `mink:"col,default=value"` | Default value (see security note below) |
| `mink:"col,type=VARCHAR(100)"` | Override SQL type (see security note below) |

> **Security Note**: The `default=` and `type=` values are validated against common SQL injection patterns but are interpolated into DDL statements. Only use static, hardcoded values in your source code. Never construct these tag values from user input or external sources.

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
| `map`, `struct` | `JSONB` |

> **Note on unsigned integers**: Go's unsigned integer types (`uint`, `uint32`, `uint64`) are mapped to PostgreSQL's signed integer types. Values exceeding the signed integer maximum may cause overflow. Consider using explicit `type=NUMERIC` for large unsigned values.

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
// Build a query
query := mink.NewQuery().
    Where("Status", mink.Eq, "Pending").
    And("TotalAmount", mink.Gt, 100.0).
    OrderByDesc("CreatedAt").
    WithPagination(10, 0)  // limit 10, offset 0

// Execute query
orders, err := repo.Query(ctx, query)

// Find single result
order, err := repo.FindOne(ctx, query)

// Count matching records
count, err := repo.Count(ctx, query)
```

### Filter Operators

```go
// Available filter operators
mink.Eq       // Equal
mink.NotEq    // Not equal
mink.Gt       // Greater than
mink.Gte      // Greater than or equal
mink.Lt       // Less than
mink.Lte      // Less than or equal
mink.In       // In list
mink.Contains // Contains substring
```

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
