---
layout: default
title: "ADR-005: Three Projection Types"
parent: Architecture Decision Records
nav_order: 5
---

# ADR-005: Three Projection Types

| Status | Date | Deciders |
|--------|------|----------|
| Accepted | 2024-03-01 | Core Team |

## Context

Projections transform event streams into read models. Different use cases have different consistency and performance requirements:

1. **Financial Systems**: Need strong consistency (balance must be accurate)
2. **Reports/Analytics**: Can tolerate delay (eventual consistency is fine)
3. **Dashboards**: Need real-time updates (live notifications)

A one-size-fits-all projection approach forces unnecessary trade-offs.

## Decision

We will implement **three distinct projection types**, each optimized for different consistency requirements:

### 1. Inline Projections (Synchronous)

Updated in the same transaction as the event append.

```go
type InlineProjection interface {
    Projection
    // Apply is called within the event store transaction
    ApplyInline(ctx context.Context, tx Transaction, event StoredEvent) error
}
```

**Characteristics**:
- Strong consistency
- Blocks event append until projection updated
- Lower write throughput
- Use for critical read models

**Use Cases**:
- Account balances
- Inventory counts
- Unique constraint checks

```go
engine.RegisterInline(&AccountBalanceProjection{})
```

### 2. Async Projections (Background)

Updated by background workers after events are committed.

```go
type AsyncProjection interface {
    Projection
    // Apply is called by background worker
    Apply(ctx context.Context, event StoredEvent) error
}

type AsyncOptions struct {
    BatchSize   int           // Events per batch
    Interval    time.Duration // Polling interval
    Workers     int           // Parallel workers
    RetryPolicy RetryPolicy   // Retry on failure
}
```

**Characteristics**:
- Eventual consistency
- High write throughput
- Parallel processing
- Automatic retry on failure

**Use Cases**:
- Search indexes
- Reports
- Analytics
- Notifications

```go
engine.RegisterAsync(&SearchIndexProjection{}, AsyncOptions{
    BatchSize:   100,
    Interval:    time.Second,
    Workers:     4,
    RetryPolicy: ExponentialBackoff(100*time.Millisecond, 5*time.Second, 3),
})
```

### 3. Live Projections (Real-time)

Receive events in real-time for transient use cases.

```go
type LiveProjection interface {
    Projection
    // OnEvent is called immediately when event is committed
    OnEvent(ctx context.Context, event StoredEvent)
    // IsTransient indicates if state should survive restarts
    IsTransient() bool
}
```

**Characteristics**:
- Real-time delivery
- No persistence (transient)
- WebSocket/SSE integration
- Memory-only state

**Use Cases**:
- Live dashboards
- Real-time notifications
- WebSocket updates
- Monitoring

```go
engine.RegisterLive(&DashboardProjection{})

// Access live updates
dashboard.Updates() // Returns channel of updates
```

### Comparison

| Aspect | Inline | Async | Live |
|--------|--------|-------|------|
| Consistency | Strong | Eventual | Real-time |
| Persistence | Yes | Yes | No (transient) |
| Write Impact | High | None | None |
| Recovery | Automatic | Checkpoint-based | Rebuild |
| Use Case | Critical data | Reports | Dashboards |

## Consequences

### Positive

1. **Right Tool for Job**: Choose consistency level per use case
2. **Performance Optimization**: Async projections don't slow writes
3. **Real-time Capability**: Live projections enable dashboards
4. **Clear Semantics**: Explicit consistency expectations
5. **Scalability**: Async projections can scale independently

### Negative

1. **Complexity**: Three types to understand and manage
2. **Consistency Confusion**: Developers must choose correctly
3. **Testing Variety**: Need to test each projection type

### Neutral

1. **Migration**: Can change projection types if needs change
2. **Monitoring**: Each type needs different monitoring

## Implementation Details

### Projection Engine

```go
type ProjectionEngine struct {
    eventStore      *EventStore
    checkpointStore CheckpointStore
    
    inlineProjections []InlineProjection
    asyncProjections  map[string]*asyncRunner
    liveProjections   []LiveProjection
    
    subscriptions     []Subscription
}

func (e *ProjectionEngine) Start(ctx context.Context) error {
    // Start async workers
    for name, runner := range e.asyncProjections {
        go runner.Run(ctx)
    }
    
    // Start live subscriptions
    for _, proj := range e.liveProjections {
        sub, _ := e.eventStore.SubscribeAll(ctx, 0)
        go e.runLiveProjection(ctx, proj, sub)
    }
    
    return nil
}

// Called by EventStore during Append
func (e *ProjectionEngine) ProcessInline(ctx context.Context, tx Transaction, events []StoredEvent) error {
    for _, proj := range e.inlineProjections {
        for _, event := range events {
            if proj.HandlesEvent(event.Type) {
                if err := proj.ApplyInline(ctx, tx, event); err != nil {
                    return err
                }
            }
        }
    }
    return nil
}
```

### Checkpoint Management

Async projections track their position:

```go
type Checkpoint struct {
    ProjectionName string
    Position       uint64
    UpdatedAt      time.Time
}

type CheckpointStore interface {
    Get(ctx context.Context, name string) (Checkpoint, error)
    Save(ctx context.Context, checkpoint Checkpoint) error
}
```

### Rebuild Support

All persistent projections support rebuilding:

```go
rebuilder := NewProjectionRebuilder(store, checkpointStore)

// Rebuild from scratch
rebuilder.Rebuild(ctx, projection, RebuildOptions{
    BatchSize:    1000,
    Parallelism:  4,
    FromPosition: 0,
})
```

## Alternatives Considered

### Alternative 1: Single Projection Type

**Description**: One projection interface for all use cases.

**Rejected because**:
- Forces compromise between consistency and performance
- No real-time capability
- Overly complex configuration

### Alternative 2: Configurable Consistency per Event

**Description**: Configure consistency at event level.

**Rejected because**:
- Too granular
- Hard to reason about
- Complicates projection logic

### Alternative 3: External Projection Service

**Description**: Run projections in separate service.

**Rejected because**:
- Adds deployment complexity
- Network latency for inline projections
- Can be added later if needed

## References

- [Marten Projections](https://martendb.io/events/projections/)
- [EventStoreDB Projections](https://developers.eventstore.com/server/v21.10/projections.html)
- [Axon Query Update Emitter](https://docs.axoniq.io/reference-guide/axon-framework/queries/query-dispatchers)
