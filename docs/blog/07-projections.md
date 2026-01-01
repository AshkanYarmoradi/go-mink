---
layout: default
title: "Part 7: Projections and Read Models"
parent: Blog
nav_order: 7
permalink: /blog/07-projections
---

# Part 7: Projections and Read Models
{: .no_toc }

Building optimized query views from your event stream.
{: .fs-6 .fw-300 }

## Table of contents
{: .no_toc .text-delta }

1. TOC
{:toc}

---

*This is Part 7 of an 8-part series on Event Sourcing and CQRS with Go.*

---

## The Read Side Problem

Event sourcing stores events, not state. But users need fast queries. We build **read models**—denormalized views optimized for specific queries.

```
Events in Event Store
        │
        ▼
  ┌─────────────┐
  │  Projection │ ──────► Read Model (SQL, Redis, Elastic)
  └─────────────┘
```

---

## Projection Types

| Type | Consistency | Performance | Use Case |
|------|------------|-------------|----------|
| **Inline** | Strong | Lower throughput | Counters, caches |
| **Async** | Eventual | High throughput | Read databases |
| **Live** | Real-time | Non-blocking | Notifications |

---

## Inline Projections

Run synchronously during event append:

```go
type OrderCountProjection struct {
    mink.ProjectionBase
    count int64
}

func (p *OrderCountProjection) Apply(ctx context.Context, event mink.StoredEvent) error {
    if event.Type == "OrderCreated" {
        p.count++
    }
    return nil
}

engine.RegisterInline(NewOrderCountProjection())
```

---

## Async Projections

Run in background workers with batching:

```go
type OrderListProjection struct {
    mink.ProjectionBase
    db *sql.DB
}

func (p *OrderListProjection) Apply(ctx context.Context, event mink.StoredEvent) error {
    switch event.Type {
    case "OrderCreated":
        var data OrderCreated
        json.Unmarshal(event.Data, &data)
        p.db.Exec(`INSERT INTO order_list ...`, data.OrderID, data.CustomerID)
    }
    return nil
}

engine.RegisterAsync(projection, mink.AsyncOptions{
    BatchSize:    100,
    PollInterval: 100 * time.Millisecond,
})
```

---

## Live Projections

Real-time for transient use cases:

```go
type OrderNotificationProjection struct {
    mink.ProjectionBase
    broadcast chan<- OrderUpdate
}

func (p *OrderNotificationProjection) OnEvent(ctx context.Context, event mink.StoredEvent) {
    p.broadcast <- OrderUpdate{OrderID: extractID(event.StreamID)}
}

func (p *OrderNotificationProjection) IsTransient() bool {
    return true
}
```

---

## The Projection Engine

```go
engine := mink.NewProjectionEngine(store,
    mink.WithCheckpointStore(checkpointStore),
)

engine.RegisterInline(counterProjection)
engine.RegisterAsync(readModelProjection, asyncOptions)
engine.RegisterLive(notificationProjection)

engine.Start(ctx)
defer engine.Stop(ctx)
```

---

## Best Practices

### 1. Idempotent Projections

```go
// Use UPSERT instead of INSERT
p.db.Exec(`INSERT INTO order_list (order_id, status)
    VALUES ($1, $2)
    ON CONFLICT (order_id) DO UPDATE SET status = $2`,
    orderID, status)
```

### 2. Handle Unknown Events

```go
switch event.Type {
case "OrderCreated":
    // Handle
default:
    return nil  // Ignore, don't fail
}
```

### 3. Monitor Lag

```go
status := engine.GetStatus("OrderList")
if status.Lag > 10000 {
    alerting.Warn("Projection lag: %d", status.Lag)
}
```

---

## Key Takeaways

{: .highlight }
> 1. **Projections build read models**: Transform events into queryable data
> 2. **Three types for different needs**: Inline, Async, Live
> 3. **Checkpoints enable restart**: Know where you left off
> 4. **Idempotency is crucial**: Events may be replayed
> 5. **Multiple read models are normal**: Different queries, different models

---

[← Part 6: Middleware](/blog/06-middleware){: .btn .fs-5 .mb-4 .mb-md-0 .mr-2 }
[Part 8: Production →](/blog/08-production){: .btn .btn-primary .fs-5 .mb-4 .mb-md-0 }
