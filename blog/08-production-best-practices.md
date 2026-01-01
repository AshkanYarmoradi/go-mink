# Part 8: Production Best Practices

*This is Part 8 of an 8-part series on Event Sourcing and CQRS with Go. In this final post, we'll cover everything you need to run event-sourced systems in production.*

---

## From Development to Production

Event sourcing in development feels magical. Events flow, aggregates rebuild, projections sync. But production brings challenges:

- Long-lived aggregates with thousands of events
- High throughput requirements
- Operational visibility
- Schema evolution
- Disaster recovery

Let's address each systematically.

---

## Snapshots: Taming Long Event Streams

### The Problem

An aggregate with 10,000 events takes time to replay. Every load means replaying the entire history:

```go
order := NewOrder("order-123")
store.LoadAggregate(ctx, order)  // Replays 10,000 events... slow!
```

### The Solution: Snapshots

A **snapshot** captures the current state at a point in time. Instead of replaying from the beginning, you:

1. Load the latest snapshot
2. Replay only events *after* the snapshot

```
Events:     [1] [2] [3] [4] [5] [6] [7] [8] [9] [10]
                         ^
                    Snapshot at v5

Load: Snapshot(v5) + Events [6, 7, 8, 9, 10]
```

### Implementing Snapshots

```go
type Snapshot struct {
    AggregateID   string
    AggregateType string
    Version       int64
    State         []byte  // Serialized aggregate state
    CreatedAt     time.Time
}

type SnapshotStore interface {
    Save(ctx context.Context, snapshot Snapshot) error
    Load(ctx context.Context, aggregateID, aggregateType string) (*Snapshot, error)
}
```

### Snapshot-Aware Loading

```go
func LoadWithSnapshot(ctx context.Context, store *mink.EventStore, snapshots SnapshotStore, agg mink.Aggregate) error {
    // Try to load snapshot
    snapshot, err := snapshots.Load(ctx, agg.AggregateID(), agg.AggregateType())
    if err == nil && snapshot != nil {
        // Restore state from snapshot
        if err := json.Unmarshal(snapshot.State, agg); err != nil {
            return err
        }
        // Load only events after snapshot
        events, err := store.LoadRaw(ctx,
            fmt.Sprintf("%s-%s", agg.AggregateType(), agg.AggregateID()),
            snapshot.Version)
        if err != nil {
            return err
        }
        for _, event := range events {
            agg.ApplyEvent(deserialize(event))
        }
        return nil
    }

    // No snapshot, load all events
    return store.LoadAggregate(ctx, agg)
}
```

### When to Create Snapshots

**Option 1: After N events**

```go
func (h *OrderHandler) Handle(ctx context.Context, cmd Command) (Result, error) {
    // ... handle command ...

    if err := store.SaveAggregate(ctx, order); err != nil {
        return nil, err
    }

    // Snapshot every 100 events
    if order.Version() % 100 == 0 {
        snapshot := Snapshot{
            AggregateID:   order.AggregateID(),
            AggregateType: order.AggregateType(),
            Version:       order.Version(),
            State:         serialize(order),
            CreatedAt:     time.Now(),
        }
        snapshotStore.Save(ctx, snapshot)
    }

    return result, nil
}
```

**Option 2: Background job**

```go
func SnapshotJob(ctx context.Context) {
    ticker := time.NewTicker(1 * time.Hour)
    for range ticker.C {
        aggregates := findAggregatesNeedingSnapshot()
        for _, aggID := range aggregates {
            createSnapshot(ctx, aggID)
        }
    }
}
```

### Snapshot Best Practices

1. **Don't snapshot too often**: Storage cost vs. load time tradeoff
2. **Keep old snapshots**: Useful for debugging, can delete after retention period
3. **Version your snapshot format**: Schema may change
4. **Test snapshot restore**: Part of your test suite

---

## Optimistic Concurrency Strategies

### Retry with Reload

The most common pattern:

```go
func ExecuteWithRetry(ctx context.Context, aggregateID string, action func(agg *Order) error) error {
    maxRetries := 3

    for attempt := 0; attempt < maxRetries; attempt++ {
        order := NewOrder(aggregateID)
        if err := store.LoadAggregate(ctx, order); err != nil {
            return err
        }

        if err := action(order); err != nil {
            return err  // Business error, don't retry
        }

        err := store.SaveAggregate(ctx, order)
        if err == nil {
            return nil  // Success
        }

        if !errors.Is(err, mink.ErrConcurrencyConflict) {
            return err  // Different error, don't retry
        }

        // Concurrency conflict, retry
        log.Printf("Concurrency conflict, retry %d/%d", attempt+1, maxRetries)
        time.Sleep(time.Duration(attempt*10) * time.Millisecond)
    }

    return fmt.Errorf("failed after %d retries", maxRetries)
}

// Usage
err := ExecuteWithRetry(ctx, "order-123", func(order *Order) error {
    return order.AddItem("SKU-001", 1, 29.99)
})
```

### Conflict Resolution

For some cases, you can merge changes:

```go
func AddItemWithMerge(ctx context.Context, orderID string, item OrderItem) error {
    for attempt := 0; attempt < 3; attempt++ {
        order := NewOrder(orderID)
        store.LoadAggregate(ctx, order)

        // Check if item already added (idempotent)
        if order.HasItem(item.SKU) {
            return nil  // Already done
        }

        order.AddItem(item.SKU, item.Quantity, item.Price)

        if err := store.SaveAggregate(ctx, order); err == nil {
            return nil
        }
        // Retry...
    }
    return ErrConflictNotResolved
}
```

---

## Event Schema Evolution

Events are immutable, but your code evolves. How do you handle schema changes?

### Strategy 1: Upcasting

Transform old events to new format during load:

```go
// Old event format
type OrderCreatedV1 struct {
    OrderID string `json:"orderId"`
    Customer string `json:"customer"`  // Just a name
}

// New event format
type OrderCreatedV2 struct {
    OrderID    string `json:"orderId"`
    CustomerID string `json:"customerId"`
    CustomerName string `json:"customerName"`
}

// Upcaster transforms V1 to V2
func UpcastOrderCreated(data []byte, version int) (interface{}, error) {
    if version == 1 {
        var v1 OrderCreatedV1
        json.Unmarshal(data, &v1)

        return OrderCreatedV2{
            OrderID:      v1.OrderID,
            CustomerID:   "", // Unknown in V1
            CustomerName: v1.Customer,
        }, nil
    }

    var v2 OrderCreatedV2
    json.Unmarshal(data, &v2)
    return v2, nil
}
```

### Strategy 2: Weak Schema

Use flexible deserialization:

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

### Strategy 3: New Event Types

For significant changes, create new event types:

```go
// Instead of changing OrderCreated...
type OrderCreatedV2 struct {
    OrderID      string
    CustomerID   string
    CustomerName string
    Source       string  // New field
}

func (o *Order) ApplyEvent(event interface{}) error {
    switch e := event.(type) {
    case OrderCreated:      // Handle V1
        o.handleOrderCreatedV1(e)
    case OrderCreatedV2:    // Handle V2
        o.handleOrderCreatedV2(e)
    }
    return nil
}
```

### Best Practices for Schema Evolution

1. **Never remove fields from events**: Add, don't remove
2. **Make new fields optional**: Old events won't have them
3. **Version your events**: Store version in metadata
4. **Test with old events**: Include historical events in tests
5. **Document changes**: Maintain a schema changelog

---

## Monitoring and Observability

### Key Metrics

**Event Store:**
- Events appended per second
- Append latency (p50, p95, p99)
- Stream sizes (event count distribution)
- Storage growth rate

**Command Bus:**
- Commands dispatched per second
- Command success/failure rate
- Command latency by type
- Handler execution time

**Projections:**
- Projection lag (events behind)
- Processing rate (events/second)
- Error rate
- Checkpoint positions

### Implementing Metrics

```go
type Metrics struct {
    eventsAppended   prometheus.Counter
    appendLatency    prometheus.Histogram
    commandsExecuted *prometheus.CounterVec
    commandLatency   *prometheus.HistogramVec
    projectionLag    *prometheus.GaugeVec
}

func NewMetrics() *Metrics {
    return &Metrics{
        eventsAppended: prometheus.NewCounter(prometheus.CounterOpts{
            Name: "mink_events_appended_total",
            Help: "Total events appended to the store",
        }),
        appendLatency: prometheus.NewHistogram(prometheus.HistogramOpts{
            Name:    "mink_append_duration_seconds",
            Help:    "Event append latency",
            Buckets: prometheus.DefBuckets,
        }),
        commandsExecuted: prometheus.NewCounterVec(prometheus.CounterOpts{
            Name: "mink_commands_total",
            Help: "Total commands executed",
        }, []string{"type", "status"}),
        commandLatency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
            Name:    "mink_command_duration_seconds",
            Help:    "Command execution latency",
            Buckets: prometheus.DefBuckets,
        }, []string{"type"}),
        projectionLag: prometheus.NewGaugeVec(prometheus.GaugeOpts{
            Name: "mink_projection_lag_events",
            Help: "Number of events projection is behind",
        }, []string{"projection"}),
    }
}
```

### Health Checks

```go
func HealthCheck(engine *mink.ProjectionEngine) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        health := struct {
            Status      string            `json:"status"`
            Projections map[string]string `json:"projections"`
        }{
            Status:      "healthy",
            Projections: make(map[string]string),
        }

        for _, name := range engine.ProjectionNames() {
            status := engine.GetStatus(name)
            health.Projections[name] = string(status.State)

            if status.State == mink.ProjectionStateFaulted {
                health.Status = "unhealthy"
            }
            if status.Lag > 10000 {
                health.Status = "degraded"
            }
        }

        if health.Status != "healthy" {
            w.WriteHeader(http.StatusServiceUnavailable)
        }

        json.NewEncoder(w).Encode(health)
    }
}
```

### Structured Logging

```go
logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

// Log commands
logger.Info("command executed",
    "command_type", cmd.CommandType(),
    "aggregate_id", result.AggregateID,
    "version", result.Version,
    "duration_ms", duration.Milliseconds(),
    "correlation_id", correlationID,
)

// Log events
logger.Info("events appended",
    "stream_id", streamID,
    "event_count", len(events),
    "new_version", newVersion,
)

// Log projection progress
logger.Info("projection checkpoint",
    "projection", projectionName,
    "position", position,
    "lag", lag,
)
```

---

## Error Handling Patterns

### Idempotent Error Recovery

```go
func ProcessWithIdempotency(ctx context.Context, commandID string, cmd Command) error {
    // Check if already processed
    result, err := idempotencyStore.Get(ctx, commandID)
    if err == nil {
        log.Printf("Command %s already processed, returning cached result", commandID)
        return nil
    }

    // Process
    result, err = bus.Dispatch(ctx, cmd)

    // Store result (even errors for idempotency)
    idempotencyStore.Set(ctx, commandID, result, err, 24*time.Hour)

    return err
}
```

### Dead Letter Queue

For events that fail processing:

```go
type DeadLetterEntry struct {
    Event     mink.StoredEvent
    Error     string
    Attempts  int
    FirstFail time.Time
    LastFail  time.Time
}

func (p *ResilientProjection) Apply(ctx context.Context, event mink.StoredEvent) error {
    err := p.innerApply(ctx, event)
    if err != nil {
        if p.shouldDeadLetter(err) {
            p.deadLetterQueue.Push(DeadLetterEntry{
                Event:    event,
                Error:    err.Error(),
                Attempts: 1,
            })
            return nil  // Don't block projection
        }
        return err  // Retry
    }
    return nil
}

func (p *ResilientProjection) shouldDeadLetter(err error) bool {
    // Permanent failures go to dead letter
    return errors.Is(err, ErrInvalidEventData) ||
           errors.Is(err, ErrBusinessRuleViolation)
}
```

---

## Testing Strategies

### Unit Testing Aggregates

```go
func TestOrder_AddItem_ToShippedOrder_Fails(t *testing.T) {
    order := NewOrder("test-order")
    order.Create("customer-1")
    order.Ship("tracking-123")

    err := order.AddItem("SKU-1", 1, 10.00)

    if err == nil {
        t.Error("Expected error when adding item to shipped order")
    }
    if !errors.Is(err, ErrOrderAlreadyShipped) {
        t.Errorf("Expected ErrOrderAlreadyShipped, got %v", err)
    }
}
```

### BDD-Style Testing

```go
func TestOrder_Confirmation_Flow(t *testing.T) {
    mink.Given(t, NewOrder("order-1"),
        OrderCreated{OrderID: "order-1", CustomerID: "cust-1"},
        ItemAdded{SKU: "SKU-1", Quantity: 2, Price: 25.00},
    ).
    When(func(order *Order) error {
        return order.Confirm()
    }).
    Then(
        OrderConfirmed{Total: 50.00},
    )
}
```

### Integration Testing

```go
func TestOrderWorkflow_Integration(t *testing.T) {
    if testing.Short() {
        t.Skip("Skipping integration test")
    }

    // Setup real store
    adapter, _ := postgres.NewAdapter(os.Getenv("TEST_DATABASE_URL"))
    defer adapter.Close()
    adapter.Initialize(context.Background())

    store := mink.New(adapter)
    store.RegisterEvents(OrderCreated{}, ItemAdded{}, OrderConfirmed{})

    // Run workflow
    ctx := context.Background()
    order := NewOrder("test-order")
    order.Create("customer-1")
    order.AddItem("SKU-1", 1, 99.99)

    err := store.SaveAggregate(ctx, order)
    require.NoError(t, err)

    // Verify
    loaded := NewOrder("test-order")
    err = store.LoadAggregate(ctx, loaded)
    require.NoError(t, err)
    assert.Equal(t, int64(2), loaded.Version())
    assert.Equal(t, 99.99, loaded.Total())
}
```

### Testing Projections

```go
func TestOrderListProjection(t *testing.T) {
    projection := NewOrderListProjection()

    events := []mink.StoredEvent{
        {Type: "OrderCreated", StreamID: "Order-order-1",
         Data: []byte(`{"orderId":"order-1","customerId":"cust-1"}`)},
        {Type: "OrderConfirmed", StreamID: "Order-order-1", Data: []byte(`{}`)},
    }

    for _, event := range events {
        err := projection.Apply(context.Background(), event)
        require.NoError(t, err)
    }

    order := projection.GetOrder("order-1")
    assert.Equal(t, "confirmed", order.Status)
}
```

---

## Deployment Strategies

### Rolling Deployments

Event sourcing is naturally compatible with rolling deployments:

1. New code can read old events (backward compatible)
2. Projections catch up automatically
3. No schema migrations for events

### Blue-Green Deployments

```
┌─────────────────────────────────────────────────────────┐
│                    Event Store                          │
│                    (Shared)                             │
└─────────────────────────────────────────────────────────┘
           ▲                           ▲
           │                           │
    ┌──────┴──────┐             ┌──────┴──────┐
    │    Blue     │             │    Green    │
    │  (Current)  │             │   (New)     │
    └─────────────┘             └─────────────┘
           │                           │
           ▼                           ▼
    ┌─────────────┐             ┌─────────────┐
    │  Read DB    │             │  Read DB    │
    │   (Blue)    │             │  (Green)    │
    └─────────────┘             └─────────────┘
```

Both versions:
- Share the event store
- Have separate read models
- Projections run independently

Switch traffic when green catches up.

### Database Migrations

**For the event store**: Never migrate events. They're immutable.

**For read models**:
1. Deploy new projection with new schema
2. Rebuild from events
3. Switch traffic
4. Drop old read model

---

## Disaster Recovery

### Backup Strategy

**Events**: Regular PostgreSQL backups
**Snapshots**: Can be regenerated from events
**Read models**: Can be rebuilt from events

The event store is your source of truth. Everything else is derived.

### Recovery Procedures

**Lost read model:**
```bash
# Reset checkpoint
psql -c "DELETE FROM checkpoints WHERE projection_name = 'OrderList'"

# Clear read model
psql -c "TRUNCATE order_list"

# Restart projection - it rebuilds automatically
```

**Corrupted events** (rare, catastrophic):
```bash
# Restore from backup
pg_restore -d events events_backup.dump

# Rebuild all projections
./mink-admin rebuild-all-projections
```

**Point-in-time recovery:**

Because events are timestamped, you can:
1. Load events up to a timestamp
2. Replay to see state at that moment
3. Debug or audit historical state

---

## Performance Tuning

### PostgreSQL Indexes

```sql
-- Essential indexes
CREATE INDEX idx_events_stream ON events(stream_id, version);
CREATE INDEX idx_events_global ON events(global_position);

-- For category subscriptions
CREATE INDEX idx_events_category ON events(split_part(stream_id, '-', 1));

-- For time-based queries
CREATE INDEX idx_events_timestamp ON events(timestamp);
```

### Connection Pooling

```go
adapter, _ := postgres.NewAdapter(connStr,
    postgres.WithMaxConnections(50),
    postgres.WithMinConnections(10),
    postgres.WithMaxIdleTime(5 * time.Minute),
)
```

### Batch Processing

```go
// For projections
engine.RegisterAsync(projection, mink.AsyncOptions{
    BatchSize:    500,    // Process 500 events at once
    BatchTimeout: 5 * time.Second,
})

// For event loading
events, _ := store.LoadEventsFromPosition(ctx, position, 1000)
```

### Caching

```go
type CachedAggregateLoader struct {
    store *mink.EventStore
    cache *lru.Cache
}

func (l *CachedAggregateLoader) Load(ctx context.Context, agg mink.Aggregate) error {
    key := fmt.Sprintf("%s-%s", agg.AggregateType(), agg.AggregateID())

    if cached, ok := l.cache.Get(key); ok {
        // Copy state from cache
        copyState(cached, agg)

        // Load only new events
        events, _ := l.store.LoadRaw(ctx, key, agg.Version())
        for _, e := range events {
            agg.ApplyEvent(deserialize(e))
        }
        return nil
    }

    // Full load
    err := l.store.LoadAggregate(ctx, agg)
    if err == nil {
        l.cache.Add(key, clone(agg))
    }
    return err
}
```

---

## Checklist: Going to Production

### Before Launch

- [ ] PostgreSQL adapter configured with connection pooling
- [ ] All events registered with the store
- [ ] Snapshots implemented for high-traffic aggregates
- [ ] Projections checkpointing to persistent store
- [ ] Dead letter queue for failed events
- [ ] Metrics exposed (Prometheus, DataDog, etc.)
- [ ] Health check endpoints
- [ ] Structured logging configured
- [ ] Backup strategy in place

### Monitoring

- [ ] Dashboard for command/event rates
- [ ] Alerts for projection lag > threshold
- [ ] Alerts for error rate > threshold
- [ ] Alerts for latency p99 > threshold
- [ ] Storage growth trending

### Runbooks

- [ ] Projection rebuild procedure
- [ ] Dead letter queue processing
- [ ] Snapshot recreation
- [ ] Point-in-time recovery
- [ ] Performance troubleshooting

---

## Conclusion

Congratulations! You've completed this 8-part series on Event Sourcing and CQRS with go-mink.

### What You've Learned

1. **Event Sourcing Fundamentals**: Storing events instead of state
2. **Getting Started**: Setting up go-mink and the event store
3. **Aggregates**: Encapsulating business logic and state
4. **Event Store Deep Dive**: Streams, versioning, and metadata
5. **CQRS**: Separating reads from writes with the command bus
6. **Middleware**: Cross-cutting concerns and pipelines
7. **Projections**: Building read models for queries
8. **Production**: Snapshots, monitoring, and operations

### Key Principles

1. **Events are facts**: Immutable records of what happened
2. **State is derived**: Always rebuildable from events
3. **Aggregates guard consistency**: Single unit of change
4. **Commands express intent**: Validated before execution
5. **Projections serve queries**: Optimized for specific needs
6. **Observability is essential**: Know what your system is doing

### Next Steps

- Build a small project with go-mink
- Experiment with different projection types
- Practice schema evolution
- Set up monitoring and alerting
- Join the go-mink community

Thank you for reading. Happy event sourcing!

---

*Previous: [← Part 7: Projections and Read Models](07-projections-and-read-models.md)*

*Back to: [Series Index](README.md)*
