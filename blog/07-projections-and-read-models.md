# Part 7: Projections and Read Models

*This is Part 7 of an 8-part series on Event Sourcing and CQRS with Go. In this post, we'll learn how to build read models using projections—the "Q" in CQRS.*

---

## The Read Side Problem

Event sourcing stores events, not state. But users need fast queries:

- "Show me all orders for customer X"
- "What's the total revenue this month?"
- "List the top 10 products by sales"

You *could* replay events for every query, but that's slow and impractical. Instead, we build **read models**—denormalized views optimized for specific queries.

---

## What Are Projections?

A **projection** transforms events into a read model. It:

1. **Subscribes** to events (all, by stream, or by category)
2. **Processes** each event
3. **Updates** the read model (database table, cache, search index)

```
Events in Event Store
        │
        ▼
  ┌─────────────┐
  │  Projection │ ──────► Read Model (SQL table, Redis, Elastic, etc.)
  └─────────────┘
```

### Example: Order List Projection

Events:
- `OrderCreated { orderId, customerId, createdAt }`
- `OrderConfirmed { orderId, confirmedAt }`
- `OrderShipped { orderId, shippedAt, trackingNumber }`

Read Model (SQL table):
```sql
CREATE TABLE order_list (
    order_id VARCHAR PRIMARY KEY,
    customer_id VARCHAR,
    status VARCHAR,
    created_at TIMESTAMP,
    confirmed_at TIMESTAMP,
    shipped_at TIMESTAMP,
    tracking_number VARCHAR
);
```

The projection listens to events and maintains this table:

```go
func (p *OrderListProjection) Apply(event mink.StoredEvent) error {
    switch e := event.Type; e {
    case "OrderCreated":
        var data OrderCreated
        json.Unmarshal(event.Data, &data)
        p.db.Exec(`INSERT INTO order_list (order_id, customer_id, status, created_at)
                   VALUES ($1, $2, 'created', $3)`,
            data.OrderID, data.CustomerID, data.CreatedAt)

    case "OrderConfirmed":
        p.db.Exec(`UPDATE order_list SET status = 'confirmed', confirmed_at = $2
                   WHERE order_id = $1`, event.StreamID, time.Now())

    case "OrderShipped":
        var data OrderShipped
        json.Unmarshal(event.Data, &data)
        p.db.Exec(`UPDATE order_list SET status = 'shipped', shipped_at = $2, tracking_number = $3
                   WHERE order_id = $1`, data.OrderID, data.ShippedAt, data.TrackingNumber)
    }
    return nil
}
```

---

## Projection Types in go-mink

go-mink supports three projection types with different consistency and performance characteristics:

| Type | Consistency | Performance | Use Case |
|------|------------|-------------|----------|
| **Inline** | Strong | Lower write throughput | Counters, caches, critical data |
| **Async** | Eventual | High throughput | Read databases, search indexes |
| **Live** | Real-time | Non-blocking | Notifications, dashboards |

---

## The Projection Interface

All projections share a common base:

```go
type Projection interface {
    Name() string              // Unique identifier
    HandledEvents() []string   // Which events to process (empty = all)
}
```

### ProjectionBase

Use `ProjectionBase` for common functionality:

```go
type OrderStatsProjection struct {
    mink.ProjectionBase
    // ... your fields
}

func NewOrderStatsProjection() *OrderStatsProjection {
    return &OrderStatsProjection{
        ProjectionBase: mink.NewProjectionBase(
            "OrderStats",                              // Name
            "OrderCreated", "OrderConfirmed", "OrderCancelled", // Events to handle
        ),
    }
}
```

If `HandledEvents()` returns an empty slice, the projection receives all events.

---

## Inline Projections

**Inline projections** run synchronously during event append. They're processed in the same transaction as the events.

### When to Use

- Critical data that must be consistent
- Counters and aggregations needed immediately
- Cache updates that can't have lag

### Interface

```go
type InlineProjection interface {
    Projection
    Apply(ctx context.Context, event StoredEvent) error
}
```

### Example: Order Counter

```go
type OrderCountProjection struct {
    mink.ProjectionBase
    mu    sync.Mutex
    count int64
}

func NewOrderCountProjection() *OrderCountProjection {
    return &OrderCountProjection{
        ProjectionBase: mink.NewProjectionBase("OrderCount", "OrderCreated"),
    }
}

func (p *OrderCountProjection) Apply(ctx context.Context, event mink.StoredEvent) error {
    if event.Type != "OrderCreated" {
        return nil
    }

    p.mu.Lock()
    p.count++
    p.mu.Unlock()

    return nil
}

func (p *OrderCountProjection) GetCount() int64 {
    p.mu.Lock()
    defer p.mu.Unlock()
    return p.count
}
```

### Registration

```go
engine := mink.NewProjectionEngine(store)

countProjection := NewOrderCountProjection()
engine.RegisterInline(countProjection)
```

### Trade-offs

| Pros | Cons |
|------|------|
| Strong consistency | Slower writes |
| No lag | Blocks event append |
| Simple (same transaction) | Must be fast |

---

## Async Projections

**Async projections** run in background workers. They process events from a position marker, supporting batch operations.

### When to Use

- Read databases (SQL, NoSQL)
- Search indexes (Elasticsearch)
- External systems
- Heavy processing

### Interface

```go
type AsyncProjection interface {
    Projection
    Apply(ctx context.Context, event StoredEvent) error
    ApplyBatch(ctx context.Context, events []StoredEvent) error
}
```

### Example: Order List Read Model

```go
type OrderListProjection struct {
    mink.ProjectionBase
    db *sql.DB
}

func NewOrderListProjection(db *sql.DB) *OrderListProjection {
    return &OrderListProjection{
        ProjectionBase: mink.NewProjectionBase(
            "OrderList",
            "OrderCreated", "OrderConfirmed", "OrderShipped", "OrderCancelled",
        ),
        db: db,
    }
}

func (p *OrderListProjection) Apply(ctx context.Context, event mink.StoredEvent) error {
    switch event.Type {
    case "OrderCreated":
        var data struct {
            OrderID    string    `json:"orderId"`
            CustomerID string    `json:"customerId"`
            CreatedAt  time.Time `json:"createdAt"`
        }
        if err := json.Unmarshal(event.Data, &data); err != nil {
            return err
        }

        _, err := p.db.ExecContext(ctx, `
            INSERT INTO order_list (order_id, customer_id, status, created_at)
            VALUES ($1, $2, 'created', $3)
            ON CONFLICT (order_id) DO UPDATE SET
                customer_id = $2, status = 'created', created_at = $3`,
            data.OrderID, data.CustomerID, data.CreatedAt)
        return err

    case "OrderConfirmed":
        _, err := p.db.ExecContext(ctx, `
            UPDATE order_list SET status = 'confirmed', updated_at = NOW()
            WHERE order_id = $1`,
            extractOrderID(event.StreamID))
        return err

    case "OrderShipped":
        var data struct {
            TrackingNumber string `json:"trackingNumber"`
        }
        json.Unmarshal(event.Data, &data)

        _, err := p.db.ExecContext(ctx, `
            UPDATE order_list
            SET status = 'shipped', tracking_number = $2, updated_at = NOW()
            WHERE order_id = $1`,
            extractOrderID(event.StreamID), data.TrackingNumber)
        return err

    case "OrderCancelled":
        _, err := p.db.ExecContext(ctx, `
            UPDATE order_list SET status = 'cancelled', updated_at = NOW()
            WHERE order_id = $1`,
            extractOrderID(event.StreamID))
        return err
    }

    return nil
}

func (p *OrderListProjection) ApplyBatch(ctx context.Context, events []mink.StoredEvent) error {
    tx, err := p.db.BeginTx(ctx, nil)
    if err != nil {
        return err
    }

    for _, event := range events {
        if err := p.applyInTx(ctx, tx, event); err != nil {
            tx.Rollback()
            return err
        }
    }

    return tx.Commit()
}

func extractOrderID(streamID string) string {
    // "Order-order-123" -> "order-123"
    parts := strings.SplitN(streamID, "-", 2)
    if len(parts) == 2 {
        return parts[1]
    }
    return streamID
}
```

### Registration with Options

```go
engine := mink.NewProjectionEngine(store,
    mink.WithCheckpointStore(checkpointStore),
)

engine.RegisterAsync(orderListProjection, mink.AsyncOptions{
    BatchSize:         100,              // Process 100 events at a time
    BatchTimeout:      time.Second,      // Or after 1 second
    PollInterval:      100 * time.Millisecond, // Check for new events
    MaxRetries:        3,                // Retry on failure
    StartFromBeginning: false,           // Start from checkpoint
})
```

### Trade-offs

| Pros | Cons |
|------|------|
| High throughput | Eventual consistency |
| Batch processing | Lag during high load |
| Doesn't block writes | More complex |
| Independent scaling | Needs checkpointing |

---

## Live Projections

**Live projections** receive events in real-time for transient use cases. They don't persist state.

### When to Use

- WebSocket notifications
- Real-time dashboards
- Metrics streaming
- Debugging/monitoring

### Interface

```go
type LiveProjection interface {
    Projection
    OnEvent(ctx context.Context, event StoredEvent)
    IsTransient() bool  // Always returns true
}
```

### Example: Order Notifications

```go
type OrderNotificationProjection struct {
    mink.ProjectionBase
    broadcast chan<- OrderUpdate
}

type OrderUpdate struct {
    OrderID   string
    Status    string
    Timestamp time.Time
}

func NewOrderNotificationProjection(broadcast chan<- OrderUpdate) *OrderNotificationProjection {
    return &OrderNotificationProjection{
        ProjectionBase: mink.NewProjectionBase(
            "OrderNotifications",
            "OrderCreated", "OrderConfirmed", "OrderShipped",
        ),
        broadcast: broadcast,
    }
}

func (p *OrderNotificationProjection) OnEvent(ctx context.Context, event mink.StoredEvent) {
    update := OrderUpdate{
        OrderID:   extractOrderID(event.StreamID),
        Timestamp: event.Timestamp,
    }

    switch event.Type {
    case "OrderCreated":
        update.Status = "created"
    case "OrderConfirmed":
        update.Status = "confirmed"
    case "OrderShipped":
        update.Status = "shipped"
    default:
        return
    }

    select {
    case p.broadcast <- update:
    case <-ctx.Done():
    default:
        // Channel full, skip (non-blocking)
    }
}

func (p *OrderNotificationProjection) IsTransient() bool {
    return true
}
```

### Registration

```go
notificationCh := make(chan OrderUpdate, 100)

engine.RegisterLive(NewOrderNotificationProjection(notificationCh))

// Consume updates
go func() {
    for update := range notificationCh {
        // Send to WebSocket clients
        websocketHub.Broadcast(update)
    }
}()
```

---

## The Projection Engine

The `ProjectionEngine` orchestrates all projections:

```go
engine := mink.NewProjectionEngine(store,
    // Checkpoint store for async projections
    mink.WithCheckpointStore(checkpointStore),

    // Optional: metrics collector
    mink.WithProjectionMetrics(metricsCollector),

    // Optional: structured logger
    mink.WithProjectionLogger(logger),
)

// Register projections
engine.RegisterInline(counterProjection)
engine.RegisterAsync(readModelProjection, asyncOptions)
engine.RegisterLive(notificationProjection)

// Start processing
if err := engine.Start(ctx); err != nil {
    log.Fatal(err)
}

// ... application runs ...

// Shutdown gracefully
engine.Stop(ctx)
```

### Checkpoint Store

Async projections need to remember their position:

```go
type CheckpointStore interface {
    GetCheckpoint(ctx context.Context, projectionName string) (uint64, error)
    SaveCheckpoint(ctx context.Context, projectionName string, position uint64) error
}
```

go-mink provides implementations:

```go
// In-memory (for testing)
checkpointStore := memory.NewCheckpointStore()

// PostgreSQL (for production)
checkpointStore := postgres.NewCheckpointStore(db)
```

### Projection Status

Monitor projection health:

```go
status := engine.GetStatus("OrderList")

fmt.Printf("Projection: %s\n", status.Name)
fmt.Printf("State: %s\n", status.State)
fmt.Printf("Position: %d\n", status.LastPosition)
fmt.Printf("Events Processed: %d\n", status.EventsProcessed)
fmt.Printf("Lag: %d events\n", status.Lag)
fmt.Printf("Last Error: %v\n", status.LastError)
```

### Projection States

```go
const (
    ProjectionStateStopped     = "stopped"
    ProjectionStateRunning     = "running"
    ProjectionStatePaused      = "paused"
    ProjectionStateFaulted     = "faulted"
    ProjectionStateRebuilding  = "rebuilding"
    ProjectionStateCatchingUp  = "catching_up"
)
```

---

## Rebuilding Projections

Sometimes you need to rebuild a projection from scratch:

- Schema change in read model
- Bug fix in projection logic
- New projection added

### Rebuild Process

```go
// Stop the projection
engine.Pause(ctx, "OrderList")

// Clear existing read model
db.Exec("TRUNCATE order_list")

// Reset checkpoint to beginning
checkpointStore.SaveCheckpoint(ctx, "OrderList", 0)

// Restart
engine.Resume(ctx, "OrderList")
```

### Dedicated Rebuild Method

```go
// Rebuilds from position 0
err := engine.Rebuild(ctx, "OrderList")
if err != nil {
    log.Printf("Rebuild failed: %v", err)
}
```

---

## Multiple Read Models

Different queries need different models:

### Order List (for listing)

```sql
CREATE TABLE order_list (
    order_id VARCHAR PRIMARY KEY,
    customer_id VARCHAR,
    status VARCHAR,
    total DECIMAL,
    created_at TIMESTAMP,
    INDEX idx_customer (customer_id),
    INDEX idx_status (status),
    INDEX idx_created (created_at DESC)
);
```

### Order Details (for single order view)

```sql
CREATE TABLE order_details (
    order_id VARCHAR PRIMARY KEY,
    customer_id VARCHAR,
    customer_name VARCHAR,
    customer_email VARCHAR,
    items JSONB,
    status VARCHAR,
    status_history JSONB,
    shipping_address JSONB,
    billing_address JSONB,
    payment_info JSONB,
    timeline JSONB
);
```

### Customer Orders (for customer profile)

```sql
CREATE TABLE customer_orders (
    customer_id VARCHAR,
    order_id VARCHAR,
    status VARCHAR,
    total DECIMAL,
    created_at TIMESTAMP,
    PRIMARY KEY (customer_id, order_id),
    INDEX idx_customer_created (customer_id, created_at DESC)
);
```

### Order Stats (for analytics)

```sql
CREATE TABLE order_stats_daily (
    date DATE PRIMARY KEY,
    total_orders INT,
    total_revenue DECIMAL,
    orders_by_status JSONB,
    avg_order_value DECIMAL
);
```

Each read model has its own projection, optimized for specific queries.

---

## Best Practices

### 1. Idempotent Projections

Events may be replayed. Handle duplicates:

```go
func (p *OrderListProjection) Apply(ctx context.Context, event mink.StoredEvent) error {
    // Use UPSERT instead of INSERT
    _, err := p.db.ExecContext(ctx, `
        INSERT INTO order_list (order_id, customer_id, status)
        VALUES ($1, $2, $3)
        ON CONFLICT (order_id) DO UPDATE SET
            customer_id = EXCLUDED.customer_id,
            status = EXCLUDED.status`,
        orderID, customerID, status)
    return err
}
```

### 2. Handle Unknown Events

Projections should ignore events they don't understand:

```go
func (p *OrderListProjection) Apply(ctx context.Context, event mink.StoredEvent) error {
    switch event.Type {
    case "OrderCreated":
        // Handle
    case "OrderConfirmed":
        // Handle
    default:
        // Ignore unknown events - don't fail!
        return nil
    }
}
```

### 3. Transactional Updates

For async projections, use transactions:

```go
func (p *OrderListProjection) ApplyBatch(ctx context.Context, events []mink.StoredEvent) error {
    tx, err := p.db.BeginTx(ctx, nil)
    if err != nil {
        return err
    }
    defer tx.Rollback()  // Rollback if not committed

    for _, event := range events {
        if err := p.applyInTx(ctx, tx, event); err != nil {
            return err
        }
    }

    return tx.Commit()
}
```

### 4. Monitor Lag

Alert when projections fall behind:

```go
status := engine.GetStatus("OrderList")
if status.Lag > 10000 {
    alerting.Warn("OrderList projection lag: %d events", status.Lag)
}
```

### 5. Graceful Degradation

If a projection fails, the system should continue:

```go
// Query read model with fallback
orders, err := orderListRepo.GetByCustomer(ctx, customerID)
if err != nil {
    // Fallback: slower but always works
    events, _ := store.Load(ctx, fmt.Sprintf("Customer-%s", customerID))
    orders = rebuildOrdersFromEvents(events)
}
```

---

## Complete Example

```go
package main

import (
    "context"
    "database/sql"
    "log"
    "time"

    "github.com/AshkanYarmoradi/go-mink"
    "github.com/AshkanYarmoradi/go-mink/adapters/memory"
)

// Events
type OrderCreated struct {
    OrderID    string `json:"orderId"`
    CustomerID string `json:"customerId"`
}

type OrderConfirmed struct{}

// Inline: Order counter
type OrderCountProjection struct {
    mink.ProjectionBase
    count int64
}

func NewOrderCountProjection() *OrderCountProjection {
    return &OrderCountProjection{
        ProjectionBase: mink.NewProjectionBase("OrderCount", "OrderCreated"),
    }
}

func (p *OrderCountProjection) Apply(ctx context.Context, event mink.StoredEvent) error {
    p.count++
    log.Printf("Order count: %d", p.count)
    return nil
}

// Async: Order list
type OrderListProjection struct {
    mink.ProjectionBase
    orders map[string]string // orderID -> status
}

func NewOrderListProjection() *OrderListProjection {
    return &OrderListProjection{
        ProjectionBase: mink.NewProjectionBase("OrderList", "OrderCreated", "OrderConfirmed"),
        orders:         make(map[string]string),
    }
}

func (p *OrderListProjection) Apply(ctx context.Context, event mink.StoredEvent) error {
    orderID := extractOrderID(event.StreamID)

    switch event.Type {
    case "OrderCreated":
        p.orders[orderID] = "created"
    case "OrderConfirmed":
        p.orders[orderID] = "confirmed"
    }

    log.Printf("Order %s: %s", orderID, p.orders[orderID])
    return nil
}

func (p *OrderListProjection) ApplyBatch(ctx context.Context, events []mink.StoredEvent) error {
    for _, event := range events {
        if err := p.Apply(ctx, event); err != nil {
            return err
        }
    }
    return nil
}

func extractOrderID(streamID string) string {
    // Simple extraction - in real code use proper parsing
    return streamID[6:] // Remove "Order-" prefix
}

func main() {
    ctx := context.Background()

    // Setup
    adapter := memory.NewAdapter()
    store := mink.New(adapter)
    store.RegisterEvents(OrderCreated{}, OrderConfirmed{})

    // Projection engine
    engine := mink.NewProjectionEngine(store,
        mink.WithCheckpointStore(memory.NewCheckpointStore()),
    )

    // Register projections
    engine.RegisterInline(NewOrderCountProjection())
    engine.RegisterAsync(NewOrderListProjection(), mink.AsyncOptions{
        BatchSize:    10,
        PollInterval: 100 * time.Millisecond,
    })

    // Start engine
    if err := engine.Start(ctx); err != nil {
        log.Fatal(err)
    }
    defer engine.Stop(ctx)

    // Create some orders
    for i := 1; i <= 5; i++ {
        orderID := fmt.Sprintf("order-%d", i)
        streamID := fmt.Sprintf("Order-%s", orderID)

        store.Append(ctx, streamID, []interface{}{
            OrderCreated{OrderID: orderID, CustomerID: "cust-1"},
        })

        if i%2 == 0 {
            store.Append(ctx, streamID, []interface{}{
                OrderConfirmed{},
            })
        }
    }

    // Wait for async projections to catch up
    time.Sleep(500 * time.Millisecond)

    // Check status
    status := engine.GetStatus("OrderList")
    log.Printf("OrderList: %s, processed %d events", status.State, status.EventsProcessed)
}
```

---

## What's Next?

In this post, you learned:

- Why read models are essential for CQRS
- Three projection types: inline, async, and live
- How to build and register projections
- The projection engine and checkpointing
- Rebuilding projections
- Best practices for production

In **Part 8**, we'll cover **Production Best Practices**—snapshots, scaling, monitoring, and operational concerns.

---

## Key Takeaways

1. **Projections build read models**: Transform events into queryable data
2. **Three types for different needs**: Inline (consistent), Async (performant), Live (real-time)
3. **Checkpoints enable restart**: Know where you left off
4. **Idempotency is crucial**: Events may be replayed
5. **Multiple read models are normal**: Different queries, different models

---

*Previous: [← Part 6: Middleware and Cross-Cutting Concerns](06-middleware-and-cross-cutting-concerns.md)*

*Next: [Part 8: Production Best Practices →](08-production-best-practices.md)*
