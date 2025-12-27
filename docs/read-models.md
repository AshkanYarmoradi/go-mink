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

## Projection Strategies

### 1. Inline Projections

Updated in the same transaction as events - **strongly consistent**.

```go
type InlineProjection interface {
    Projection
    
    // Called within event store transaction
    ApplyInTransaction(ctx context.Context, tx Transaction, event StoredEvent) error
}

// Example: Order Summary projection
type OrderSummaryProjection struct {
    repo ReadModelRepository
}

func (p *OrderSummaryProjection) Name() string {
    return "OrderSummary"
}

func (p *OrderSummaryProjection) HandledEvents() []string {
    return []string{"OrderCreated", "ItemAdded", "OrderShipped"}
}

func (p *OrderSummaryProjection) ApplyInTransaction(
    ctx context.Context, 
    tx Transaction, 
    event StoredEvent,
) error {
    switch event.Type {
    case "OrderCreated":
        var e OrderCreated
        json.Unmarshal(event.Data, &e)
        return tx.Insert(ctx, &OrderSummary{
            ID:         e.OrderID,
            CustomerID: e.CustomerID,
            Status:     "Created",
            ItemCount:  0,
            CreatedAt:  event.Timestamp,
        })
        
    case "ItemAdded":
        var e ItemAdded
        json.Unmarshal(event.Data, &e)
        return tx.Update(ctx, event.StreamID.ID, func(m *OrderSummary) {
            m.ItemCount++
            m.TotalAmount += e.Price * float64(e.Quantity)
        })
        
    case "OrderShipped":
        return tx.Update(ctx, event.StreamID.ID, func(m *OrderSummary) {
            m.Status = "Shipped"
            m.ShippedAt = &event.Timestamp
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
    
    // Async projections can batch events
    ApplyBatch(ctx context.Context, events []StoredEvent) error
    
    // Checkpoint management
    GetCheckpoint() uint64
    SetCheckpoint(position uint64) error
}

// Example: Analytics projection
type OrderAnalyticsProjection struct {
    db         *sql.DB
    checkpoint uint64
}

func (p *OrderAnalyticsProjection) ApplyBatch(
    ctx context.Context, 
    events []StoredEvent,
) error {
    tx, _ := p.db.BeginTx(ctx, nil)
    defer tx.Rollback()
    
    for _, event := range events {
        switch event.Type {
        case "OrderCreated":
            tx.Exec(`
                INSERT INTO daily_order_stats (date, order_count, revenue)
                VALUES ($1, 1, 0)
                ON CONFLICT (date) DO UPDATE 
                SET order_count = daily_order_stats.order_count + 1
            `, event.Timestamp.Truncate(24*time.Hour))
            
        case "OrderCompleted":
            var e OrderCompleted
            json.Unmarshal(event.Data, &e)
            tx.Exec(`
                UPDATE daily_order_stats 
                SET revenue = revenue + $1
                WHERE date = $2
            `, e.TotalAmount, event.Timestamp.Truncate(24*time.Hour))
        }
    }
    
    // Save checkpoint
    tx.Exec(`
        UPDATE projection_checkpoints 
        SET position = $1 
        WHERE name = $2
    `, events[len(events)-1].GlobalPosition, p.Name())
    
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
    
    // No persistence, in-memory only
    IsTransient() bool
}

// Example: Real-time dashboard
type DashboardProjection struct {
    broadcast chan<- DashboardUpdate
}

func (p *DashboardProjection) OnEvent(ctx context.Context, event StoredEvent) {
    switch event.Type {
    case "OrderCreated":
        p.broadcast <- DashboardUpdate{
            Type:    "new_order",
            OrderID: event.StreamID.ID,
        }
    case "OrderShipped":
        p.broadcast <- DashboardUpdate{
            Type:    "order_shipped",
            OrderID: event.StreamID.ID,
        }
    }
}
```

## Projection Registration

```go
// Register projections with the engine
engine := go-mink.NewProjectionEngine(store)

// Inline - same transaction
engine.RegisterInline(&OrderSummaryProjection{})

// Async - background processing
engine.RegisterAsync(&OrderAnalyticsProjection{}, go-mink.AsyncOptions{
    BatchSize:     100,
    BatchTimeout:  time.Second,
    Workers:       4,
    RetryPolicy:   go-mink.ExponentialBackoff(3),
})

// Live - real-time
engine.RegisterLive(&DashboardProjection{})

// Start projection engine
engine.Start(ctx)
```

## Read Model Repository

```go
// Generic read model repository
type ReadModelRepository[T any] interface {
    // Get by ID
    Get(ctx context.Context, id string) (*T, error)
    
    // Get multiple by IDs
    GetMany(ctx context.Context, ids []string) ([]*T, error)
    
    // Query with filters
    Query(ctx context.Context, query Query) ([]*T, error)
    
    // Insert new read model
    Insert(ctx context.Context, model *T) error
    
    // Update existing
    Update(ctx context.Context, id string, fn func(*T)) error
    
    // Delete
    Delete(ctx context.Context, id string) error
}

// Query builder
type Query struct {
    Filters  []Filter
    OrderBy  []OrderBy
    Limit    int
    Offset   int
}

// Usage
type OrderSummaryRepo = ReadModelRepository[OrderSummary]

repo := postgres.NewReadModelRepository[OrderSummary](db, "order_summaries")

// Query orders
orders, _ := repo.Query(ctx, go-mink.Query{
    Filters: []go-mink.Filter{
        {Field: "status", Op: "=", Value: "Pending"},
        {Field: "total_amount", Op: ">", Value: 100},
    },
    OrderBy: []go-mink.OrderBy{ {Field: "created_at", Desc: true} },
    Limit:   10,
})
```

## Projection Rebuilding

```go
// Rebuild projection from scratch
rebuilder := go-mink.NewProjectionRebuilder(store)

// Rebuild single projection
err := rebuilder.Rebuild(ctx, "OrderSummary", go-mink.RebuildOptions{
    BatchSize: 1000,
    Parallel:  true,
    OnProgress: func(processed, total uint64) {
        fmt.Printf("Progress: %d/%d\n", processed, total)
    },
})

// Rebuild all projections
err := rebuilder.RebuildAll(ctx)
```

## Schema Generation

```go
// Auto-generate read model schemas
type OrderSummary struct {
    ID          string    `go-mink:"pk"`
    CustomerID  string    `go-mink:"index"`
    Status      string    `go-mink:"index"`
    ItemCount   int
    TotalAmount float64
    CreatedAt   time.Time `go-mink:"index"`
    ShippedAt   *time.Time
}

// Generate migration
migration := go-mink.GenerateSchema[OrderSummary]("order_summaries")
// CREATE TABLE order_summaries (
//     id VARCHAR(255) PRIMARY KEY,
//     customer_id VARCHAR(255),
//     status VARCHAR(255),
//     item_count INT,
//     total_amount DECIMAL,
//     created_at TIMESTAMPTZ,
//     shipped_at TIMESTAMPTZ
// );
// CREATE INDEX idx_order_summaries_customer_id ON order_summaries(customer_id);
// CREATE INDEX idx_order_summaries_status ON order_summaries(status);
// CREATE INDEX idx_order_summaries_created_at ON order_summaries(created_at);
```

---

Next: [Adapters →](05-adapters.md)
