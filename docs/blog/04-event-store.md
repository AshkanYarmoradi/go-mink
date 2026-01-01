---
layout: default
title: "Part 4: The Event Store Deep Dive"
parent: Blog
nav_order: 4
permalink: /blog/04-event-store
---

# Part 4: The Event Store Deep Dive
{: .no_toc }

Understanding how events are persisted, ordered, and queried.
{: .fs-6 .fw-300 }

## Table of contents
{: .no_toc .text-delta }

1. TOC
{:toc}

---

*This is Part 4 of an 8-part series on Event Sourcing and CQRS with Go. In this post, we'll explore the Event Store in depth.*

---

## The Event Store: Heart of the System

The event store is the foundation of any event-sourced system. It's responsible for:

- **Persisting events** durably and atomically
- **Ordering events** both within streams and globally
- **Providing replay** capabilities for rebuilding state
- **Ensuring consistency** through optimistic concurrency
- **Enabling subscriptions** for real-time updates

Unlike traditional databases that optimize for reads and updates, event stores optimize for **append** and **sequential read**.

---

## Event Store Architecture

go-mink's event store has a layered architecture:

```
┌─────────────────────────────────────────────────────┐
│                   Application Code                   │
│         store.Append(), store.Load(), etc.          │
└────────────────────────┬────────────────────────────┘
                         │
┌────────────────────────▼────────────────────────────┐
│                     EventStore                       │
│   • Event serialization/deserialization              │
│   • Type registration                                │
│   • Aggregate loading/saving                         │
└────────────────────────┬────────────────────────────┘
                         │
┌────────────────────────▼────────────────────────────┐
│                 EventStoreAdapter                    │
│   • Append()  - Write events                         │
│   • Load()    - Read stream events                   │
│   • Initialize() - Setup storage                     │
└────────────────────────┬────────────────────────────┘
                         │
         ┌───────────────┼───────────────┐
         ▼               ▼               ▼
    ┌─────────┐    ┌──────────┐    ┌──────────┐
    │ Memory  │    │ Postgres │    │ MongoDB  │
    │ Adapter │    │ Adapter  │    │ (future) │
    └─────────┘    └──────────┘    └──────────┘
```

---

## Three Event Representations

### 1. Application Events (Your Domain)

```go
type OrderPlaced struct {
    OrderID    string  `json:"orderId"`
    CustomerID string  `json:"customerId"`
    Total      float64 `json:"total"`
}
```

### 2. EventData (Write Model)

```go
type EventData struct {
    Type     string   // "OrderPlaced"
    Data     []byte   // JSON payload
    Metadata Metadata // CorrelationID, UserID, etc.
}
```

### 3. StoredEvent (Storage Model)

```go
type StoredEvent struct {
    ID             string    // Unique event ID (UUID)
    StreamID       string    // "Order-order-123"
    Type           string    // "OrderPlaced"
    Data           []byte    // JSON payload
    Metadata       Metadata  // Context data
    Version        int64     // Position in stream (1, 2, 3...)
    GlobalPosition uint64    // Position across all streams
    Timestamp      time.Time // When stored
}
```

---

## Stream IDs and Categories

Stream IDs follow the pattern: `{Category}-{ID}`

```go
"Order-order-123"     // Order aggregate with ID order-123
"Customer-cust-456"   // Customer aggregate with ID cust-456
"Cart-cart-789"       // Cart aggregate with ID cart-789
```

The category (prefix) enables:
- **Filtering**: Load all events for a category
- **Subscriptions**: Subscribe to all Order events
- **Projections**: Build read models per category

---

## Appending Events

### Basic Append

```go
events := []interface{}{
    OrderPlaced{OrderID: "order-1", CustomerID: "cust-1", Total: 99.99},
    OrderConfirmed{OrderID: "order-1"},
}

stored, err := store.Append(ctx, "Order-order-1", events)
```

### Version Expectations

| Constant | Value | Meaning |
|----------|-------|---------|
| `AnyVersion` | -1 | Don't check version |
| `NoStream` | 0 | Stream must NOT exist |
| `StreamExists` | -2 | Stream MUST exist |
| Positive int | N | Stream must be at version N |

```go
// Creating a new stream
_, err := store.Append(ctx, "Order-new", events,
    mink.ExpectVersion(mink.NoStream))
```

---

## Metadata System

```go
type Metadata struct {
    CorrelationID string            // Links related events
    CausationID   string            // What caused this event
    UserID        string            // Who triggered it
    TenantID      string            // Multi-tenant support
    Custom        map[string]string // Arbitrary key-value pairs
}
```

### Using Metadata

```go
metadata := mink.Metadata{
    CorrelationID: requestID,
    CausationID:   commandID,
    UserID:        currentUser,
}

store.Append(ctx, streamID, events,
    mink.WithAppendMetadata(metadata))
```

---

## Adapters

### Memory Adapter (Testing)

```go
adapter := memory.NewAdapter()
store := mink.New(adapter)
```

### PostgreSQL Adapter (Production)

```go
connStr := "postgres://user:pass@localhost:5432/events?sslmode=disable"
adapter, err := postgres.NewAdapter(connStr)
adapter.Initialize(ctx)
store := mink.New(adapter)
```

---

## Key Takeaways

{: .highlight }
> 1. **Events transform through the system**: Domain → Storage → Application
> 2. **Stream IDs have structure**: `Category-ID` enables filtering
> 3. **Version expectations prevent conflicts**: Use appropriately
> 4. **Metadata enables observability**: Correlation, causation, and custom data
> 5. **Adapters are swappable**: Same code, different storage

---

[← Part 3: Aggregates](/blog/03-aggregates){: .btn .fs-5 .mb-4 .mb-md-0 .mr-2 }
[Part 5: CQRS →](/blog/05-cqrs){: .btn .btn-primary .fs-5 .mb-4 .mb-md-0 }
