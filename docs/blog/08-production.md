---
layout: default
title: "Part 8: Production Best Practices"
parent: Blog
nav_order: 8
permalink: /blog/08-production
---

# Part 8: Production Best Practices
{: .no_toc }

Everything you need to run event-sourced systems in production.
{: .fs-6 .fw-300 }

## Table of contents
{: .no_toc .text-delta }

1. TOC
{:toc}

---

*This is Part 8 of an 8-part series on Event Sourcing and CQRS with Go.*

---

## Snapshots

For aggregates with thousands of events, snapshots prevent slow replay:

```
Events:     [1] [2] [3] [4] [5] [6] [7] [8] [9] [10]
                         ^
                    Snapshot at v5

Load: Snapshot(v5) + Events [6, 7, 8, 9, 10]
```

### When to Snapshot

```go
// Every N events
if order.Version() % 100 == 0 {
    snapshotStore.Save(ctx, Snapshot{
        AggregateID: order.AggregateID(),
        Version:     order.Version(),
        State:       serialize(order),
    })
}
```

---

## Optimistic Concurrency

### Retry with Reload

```go
func ExecuteWithRetry(ctx context.Context, id string, action func(*Order) error) error {
    for attempt := 0; attempt < 3; attempt++ {
        order := NewOrder(id)
        store.LoadAggregate(ctx, order)

        if err := action(order); err != nil {
            return err  // Business error
        }

        err := store.SaveAggregate(ctx, order)
        if err == nil {
            return nil
        }
        if !errors.Is(err, mink.ErrConcurrencyConflict) {
            return err
        }
        // Retry...
    }
    return ErrMaxRetries
}
```

---

## Schema Evolution

### Strategy: Weak Schema

```go
func (o *Order) ApplyEvent(event interface{}) error {
    switch e := event.(type) {
    case OrderCreated:
        o.CustomerID = e.CustomerID
        // Handle optional new fields gracefully
        if e.CustomerName != "" {
            o.CustomerName = e.CustomerName
        }
    }
    return nil
}
```

### Best Practices

1. Never remove fields from events
2. Make new fields optional
3. Version your events in metadata
4. Test with old events

---

## Monitoring

### Key Metrics

- Events appended per second
- Command success/failure rate
- Projection lag
- Append latency (p50, p95, p99)

### Health Checks

```go
func HealthCheck(engine *mink.ProjectionEngine) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        for _, name := range engine.ProjectionNames() {
            status := engine.GetStatus(name)
            if status.State == mink.ProjectionStateFaulted {
                w.WriteHeader(http.StatusServiceUnavailable)
            }
        }
    }
}
```

---

## Disaster Recovery

### The Event Store is Truth

- **Events**: Regular PostgreSQL backups
- **Snapshots**: Can be regenerated
- **Read models**: Can be rebuilt

### Recovery

```bash
# Lost read model? Reset checkpoint and rebuild
psql -c "DELETE FROM checkpoints WHERE projection_name = 'OrderList'"
psql -c "TRUNCATE order_list"
# Restart - projection rebuilds automatically
```

---

## Production Checklist

### Before Launch

- [ ] PostgreSQL adapter with connection pooling
- [ ] All events registered
- [ ] Snapshots for high-traffic aggregates
- [ ] Checkpoint store configured
- [ ] Metrics exposed
- [ ] Health check endpoints
- [ ] Backup strategy

### Monitoring

- [ ] Dashboard for command/event rates
- [ ] Alerts for projection lag
- [ ] Alerts for error rates
- [ ] Storage growth trending

---

## Conclusion

Congratulations! You've completed this 8-part series on Event Sourcing and CQRS with go-mink.

### What You've Learned

1. **Event Sourcing Fundamentals**: Storing events instead of state
2. **Getting Started**: Setting up go-mink
3. **Aggregates**: Encapsulating business logic
4. **Event Store**: Streams, versioning, metadata
5. **CQRS**: Separating reads from writes
6. **Middleware**: Cross-cutting concerns
7. **Projections**: Building read models
8. **Production**: Snapshots, monitoring, operations

### Key Principles

{: .highlight }
> 1. **Events are facts**: Immutable records of what happened
> 2. **State is derived**: Always rebuildable from events
> 3. **Aggregates guard consistency**: Single unit of change
> 4. **Commands express intent**: Validated before execution
> 5. **Projections serve queries**: Optimized for specific needs
> 6. **Observability is essential**: Know what your system is doing

---

Thank you for reading. Happy event sourcing!

---

[← Part 7: Projections](/blog/07-projections){: .btn .fs-5 .mb-4 .mb-md-0 .mr-2 }
[Back to Blog](/blog){: .btn .btn-primary .fs-5 .mb-4 .mb-md-0 }
