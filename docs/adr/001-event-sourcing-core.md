---
layout: default
title: "ADR-001: Event Sourcing as Core Pattern"
parent: Architecture Decision Records
nav_order: 1
---

# ADR-001: Event Sourcing as Core Pattern

| Status | Date | Deciders |
|--------|------|----------|
| Accepted | 2024-01-15 | Core Team |

## Context

We are building a library for Go developers who need to implement complex domain logic with full audit trails, temporal queries, and the ability to replay state. Traditional CRUD-based persistence has several limitations:

1. **Lost History**: Updates overwrite previous state, losing the history of changes
2. **Audit Complexity**: Implementing audit logs requires additional tables and triggers
3. **Debugging Difficulty**: Hard to understand how an entity reached its current state
4. **Integration Challenges**: Difficult to notify other systems of changes reliably
5. **Temporal Queries**: Cannot easily query "what was the state at time X?"

The Go ecosystem lacks a comprehensive, production-ready event sourcing library comparable to what exists in .NET (EventStoreDB, Marten) or Java (Axon Framework).

## Decision

We will implement **Event Sourcing** as the core architectural pattern for go-mink:

1. **All state changes are captured as immutable events**
2. **Current state is derived by replaying events**
3. **Events are the source of truth, not current state**
4. **Events are append-only and never modified or deleted**

### Core Concepts

```go
// Events are immutable facts
type OrderPlaced struct {
    OrderID    string
    CustomerID string
    Items      []OrderItem
    PlacedAt   time.Time
}

// State is rebuilt from events
func (o *Order) ApplyEvent(event interface{}) error {
    switch e := event.(type) {
    case OrderPlaced:
        o.id = e.OrderID
        o.customerID = e.CustomerID
        o.status = StatusPending
        o.placedAt = e.PlacedAt
    }
    return nil
}
```

### Stream Organization

Events are organized into streams, typically one per aggregate instance:

```
order-123:
  1. OrderPlaced
  2. ItemAdded
  3. ItemAdded
  4. OrderShipped

order-456:
  1. OrderPlaced
  2. OrderCancelled
```

## Consequences

### Positive

1. **Complete Audit Trail**: Every change is recorded with timestamp and metadata
2. **Temporal Queries**: Can reconstruct state at any point in time
3. **Event Replay**: Can rebuild read models or fix bugs by replaying events
4. **Debugging**: Clear history of how state evolved
5. **Integration**: Events can be published to other systems
6. **Testing**: Easy to set up test scenarios with predefined events
7. **Compliance**: Natural fit for regulatory requirements (GDPR, SOX, etc.)

### Negative

1. **Learning Curve**: Developers need to learn event sourcing concepts
2. **Storage Growth**: Event stores grow continuously (mitigated by snapshots)
3. **Eventual Consistency**: Read models may lag behind writes
4. **Schema Evolution**: Event versioning requires careful planning
5. **Query Complexity**: Cannot directly query current state without projections

### Neutral

1. **Different Mental Model**: Requires thinking in terms of events, not state
2. **Tooling Requirements**: Need specialized tools for event store management

## Alternatives Considered

### Alternative 1: Traditional ORM/CRUD

**Description**: Use a standard ORM like GORM with CRUD operations.

**Rejected because**:
- Loses change history
- Requires separate audit log implementation
- Cannot replay or rebuild state
- Difficult to integrate with event-driven architectures

### Alternative 2: Change Data Capture (CDC)

**Description**: Use database CDC to capture changes after the fact.

**Rejected because**:
- Events are derived, not intentional
- Loses domain semantics (only see column changes, not business events)
- Adds infrastructure complexity
- Still requires traditional data model

### Alternative 3: Event Sourcing + State Snapshots Only

**Description**: Store only snapshots with event log for recent changes.

**Rejected because**:
- Loses ability to rebuild from scratch
- Complicates event replay
- Reduces auditability

## References

- [Event Sourcing by Martin Fowler](https://martinfowler.com/eaaDev/EventSourcing.html)
- [Event Store Documentation](https://www.eventstore.com/docs)
- [Marten Documentation](https://martendb.io/)
- [Domain-Driven Design by Eric Evans](https://www.domainlanguage.com/ddd/)
