---
layout: default
title: "ADR-006: Optimistic Concurrency Control"
parent: Architecture Decision Records
nav_order: 6
---

# ADR-006: Optimistic Concurrency Control

| Status | Date | Deciders |
|--------|------|----------|
| Accepted | 2024-01-15 | Core Team |

## Context

Event sourcing requires protecting against concurrent modifications to the same aggregate. Consider this scenario:

```
Time    User A                      User B
────────────────────────────────────────────────
T1      Load Order (v1)             
T2                                  Load Order (v1)
T3      Add Item → v2               
T4                                  Add Item → v2 (CONFLICT!)
```

Both users loaded version 1 and try to create version 2. Without protection, one user's changes would be lost.

We need to decide how to handle concurrent access:
1. **Pessimistic Locking**: Lock aggregate during edit
2. **Optimistic Concurrency**: Detect conflicts at write time
3. **Last Write Wins**: No protection (not acceptable)

## Decision

We will use **Optimistic Concurrency Control (OCC)** with version-based conflict detection.

### Mechanism

Each event append includes an expected version:

```go
func (s *EventStore) Append(ctx context.Context, streamID string, events []EventData, expectedVersion int64) error
```

The append only succeeds if the current stream version matches the expected version.

### Version Constants

```go
const (
    AnyVersion   int64 = -1  // Skip version check (use carefully!)
    NoStream     int64 = 0   // Stream must not exist (create)
    StreamExists int64 = -2  // Stream must exist (update)
)
```

### PostgreSQL Implementation

```sql
-- Atomic version check and insert
WITH current_version AS (
    SELECT COALESCE(MAX(version), 0) as v 
    FROM mink_events 
    WHERE stream_id = $1
)
INSERT INTO mink_events (stream_id, version, type, data, metadata)
SELECT $1, cv.v + generate_series(1, $2), 
       unnest($3::text[]), 
       unnest($4::jsonb[]),
       unnest($5::jsonb[])
FROM current_version cv
WHERE cv.v = $6  -- Expected version check
RETURNING *;
```

### Error Handling

```go
var ErrConcurrencyConflict = errors.New("mink: concurrency conflict")

type ConcurrencyError struct {
    StreamID        string
    ExpectedVersion int64
    ActualVersion   int64
}

func (e *ConcurrencyError) Error() string {
    return fmt.Sprintf("concurrency conflict on stream %s: expected %d, got %d",
        e.StreamID, e.ExpectedVersion, e.ActualVersion)
}

func (e *ConcurrencyError) Is(target error) bool {
    return target == ErrConcurrencyConflict
}
```

### Usage in Aggregates

```go
func (s *EventStore) SaveAggregate(ctx context.Context, agg Aggregate) error {
    events := agg.UncommittedEvents()
    if len(events) == 0 {
        return nil
    }
    
    // Use aggregate version for expected version
    expectedVersion := agg.Version()
    
    eventData := make([]EventData, len(events))
    for i, e := range events {
        eventData[i] = s.serialize(e)
    }
    
    _, err := s.Append(ctx, agg.StreamID(), eventData, expectedVersion)
    if err != nil {
        return err
    }
    
    agg.ClearUncommittedEvents()
    return nil
}
```

### Conflict Resolution Strategies

When a conflict occurs, applications can:

1. **Retry with Reload**:
```go
func SaveWithRetry(ctx context.Context, store *EventStore, agg Aggregate, action func() error) error {
    for attempts := 0; attempts < 3; attempts++ {
        if err := action(); err != nil {
            return err
        }
        
        err := store.SaveAggregate(ctx, agg)
        if err == nil {
            return nil
        }
        
        if !errors.Is(err, mink.ErrConcurrencyConflict) {
            return err
        }
        
        // Reload and retry
        agg.ClearUncommittedEvents()
        if err := store.LoadAggregate(ctx, agg); err != nil {
            return err
        }
    }
    return ErrMaxRetriesExceeded
}
```

2. **Merge Changes** (domain-specific):
```go
func MergeOrderItems(original, concurrent []Item) []Item {
    // Domain-specific merge logic
}
```

3. **User Resolution**:
```go
if errors.Is(err, mink.ErrConcurrencyConflict) {
    return NewConflictResponse(agg.Version(), actualVersion)
}
```

## Consequences

### Positive

1. **No Deadlocks**: No locks held during user think time
2. **High Throughput**: Multiple readers don't block each other
3. **Simple Model**: Just version numbers, no lock management
4. **Scalable**: Works across multiple application instances
5. **Natural Fit**: Version numbers already exist in event streams

### Negative

1. **Retry Logic**: Applications must handle conflicts
2. **Write Failures**: High-contention scenarios may see many conflicts
3. **Complexity**: Conflict resolution can be domain-specific

### Neutral

1. **Conflict Visibility**: Developers must think about concurrency
2. **Testing**: Need to test conflict scenarios

## When to Use AnyVersion

`AnyVersion` (-1) skips the version check. Use sparingly:

```go
// Acceptable: Append-only log with no business logic
store.Append(ctx, "audit-log", events, mink.AnyVersion)

// Dangerous: Business aggregate without version check
store.Append(ctx, "order-123", events, mink.AnyVersion) // DON'T DO THIS
```

## Alternatives Considered

### Alternative 1: Pessimistic Locking

**Description**: Lock the aggregate before editing.

**Pros**:
- No conflicts possible
- Simpler application logic

**Rejected because**:
- Deadlock risk
- Reduced throughput
- Doesn't scale across instances
- Holds locks during user think time

### Alternative 2: Event Sequence Numbers

**Description**: Use global sequence instead of per-stream version.

**Rejected because**:
- Single point of contention
- Doesn't prevent per-stream conflicts
- Complicates partitioning

### Alternative 3: Timestamp-Based

**Description**: Use timestamps for conflict detection.

**Rejected because**:
- Clock skew issues
- Less precise than versions
- Doesn't guarantee ordering

### Alternative 4: CRDTs

**Description**: Use conflict-free replicated data types.

**Rejected because**:
- Limited to specific data structures
- Doesn't fit all domain models
- Adds significant complexity

## References

- [Optimistic Concurrency Control](https://en.wikipedia.org/wiki/Optimistic_concurrency_control)
- [EventStoreDB Expected Version](https://developers.eventstore.com/clients/grpc/appending-events.html#handling-concurrency)
- [Marten Concurrency](https://martendb.io/documents/concurrency.html)
