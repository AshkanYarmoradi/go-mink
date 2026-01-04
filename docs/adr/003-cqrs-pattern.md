---
layout: default
title: "ADR-003: CQRS Pattern Implementation"
parent: Architecture Decision Records
nav_order: 3
---

# ADR-003: CQRS Pattern Implementation

| Status | Date | Deciders |
|--------|------|----------|
| Accepted | 2024-02-01 | Core Team |

## Context

Event sourcing naturally leads to a separation between how we write data (append events) and how we read data (query projections). This separation aligns with the Command Query Responsibility Segregation (CQRS) pattern.

Key challenges we face:

1. **Query Performance**: Replaying events for every query is expensive
2. **Query Flexibility**: Different read use cases need different data shapes
3. **Scalability**: Read and write workloads have different characteristics
4. **Consistency**: Need to balance consistency with performance

We need to decide how strictly to implement CQRS and what abstractions to provide.

## Decision

We will implement **CQRS** with clear separation between command and query sides:

### Command Side (Write Model)

```
┌─────────────────────────────────────────────────────────────┐
│                      COMMAND SIDE                           │
│                                                             │
│  Command ──▶ Command Bus ──▶ Handler ──▶ Aggregate ──▶ Events
│                  │                                          │
│                  ▼                                          │
│            Middleware                                       │
│         (Validation, Auth)                                  │
└─────────────────────────────────────────────────────────────┘
```

**Characteristics**:
- Optimized for consistency
- Domain logic in aggregates
- Commands validated before execution
- Events stored atomically

### Query Side (Read Model)

```
┌─────────────────────────────────────────────────────────────┐
│                      QUERY SIDE                             │
│                                                             │
│  Events ──▶ Projection Engine ──▶ Read Models ──▶ Queries   │
│                                                             │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐         │
│  │   Inline    │  │    Async    │  │    Live     │         │
│  │ (Immediate) │  │ (Background)│  │ (Real-time) │         │
│  └─────────────┘  └─────────────┘  └─────────────┘         │
└─────────────────────────────────────────────────────────────┘
```

**Characteristics**:
- Optimized for query performance
- Multiple views of same data
- Denormalized for specific use cases
- Eventually consistent (configurable)

### Implementation

```go
// Command Side
type CreateOrderCommand struct {
    CustomerID string
    Items      []OrderItem
}

func (h *OrderHandler) Handle(ctx context.Context, cmd CreateOrderCommand) (CommandResult, error) {
    order := NewOrder(uuid.New().String())
    if err := order.Create(cmd.CustomerID, cmd.Items); err != nil {
        return CommandResult{}, err
    }
    return h.store.SaveAggregate(ctx, order)
}

// Query Side
type OrderSummaryView struct {
    OrderID     string
    CustomerID  string
    TotalAmount float64
    Status      string
}

func (p *OrderSummaryProjection) Apply(ctx context.Context, event StoredEvent) error {
    switch event.Type {
    case "OrderCreated":
        // Create read model
    case "OrderShipped":
        // Update read model
    }
    return nil
}

// Query
orders, _ := orderRepo.Query(ctx, NewQuery().
    Where("CustomerID", Eq, customerId).
    Where("Status", Eq, "Pending"))
```

## Consequences

### Positive

1. **Performance Optimization**: Each side optimized for its purpose
2. **Scalability**: Can scale read and write independently
3. **Flexibility**: Multiple read models for different use cases
4. **Simplicity**: Clear boundaries between concerns
5. **Testing**: Can test command and query sides independently

### Negative

1. **Eventual Consistency**: Queries may return stale data
2. **Complexity**: More moving parts than simple CRUD
3. **Duplication**: Data exists in events and projections
4. **Synchronization**: Must keep projections up to date

### Neutral

1. **Learning Curve**: Teams need to understand CQRS concepts
2. **Tooling**: Need projection management tools

## Design Decisions

### 1. Commands Return Results, Not Entities

Commands return a result indicating success/failure, not the updated entity:

```go
type CommandResult struct {
    AggregateID string
    Version     int64
    Error       error
}
```

**Rationale**: Returning entities would bypass the query side and couple commands to specific read models.

### 2. Queries Use Dedicated Repositories

Queries go through read model repositories, not the event store:

```go
// Good: Query read model
orders, _ := orderRepo.Query(ctx, query)

// Avoid: Loading aggregate for queries
order := NewOrder(id)
store.LoadAggregate(ctx, order) // Only for commands
```

### 3. Projection Consistency Levels

We support three consistency levels via projection types:

| Type | Consistency | Use Case |
|------|-------------|----------|
| Inline | Strong | Financial transactions |
| Async | Eventual | Reports, analytics |
| Live | Real-time | Dashboards, notifications |

## Alternatives Considered

### Alternative 1: No CQRS (Event Sourcing Only)

**Description**: Use event sourcing without separate read models.

**Rejected because**:
- Query performance degrades with event count
- Forces single data model for all use cases
- Doesn't leverage event sourcing benefits fully

### Alternative 2: Full CQRS with Separate Databases

**Description**: Completely separate databases for read and write.

**Rejected because**:
- Adds infrastructure complexity
- Overkill for many use cases
- Can be added later if needed

### Alternative 3: Synchronous Projections Only

**Description**: All projections updated in same transaction.

**Rejected because**:
- Reduces write throughput
- Limits scalability
- Some projections don't need strong consistency

## References

- [CQRS by Martin Fowler](https://martinfowler.com/bliki/CQRS.html)
- [CQRS Documents by Greg Young](https://cqrs.files.wordpress.com/2010/11/cqrs_documents.pdf)
- [Microsoft CQRS Pattern](https://docs.microsoft.com/en-us/azure/architecture/patterns/cqrs)
