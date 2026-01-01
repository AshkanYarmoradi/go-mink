# Part 4: The Event Store Deep Dive

*This is Part 4 of an 8-part series on Event Sourcing and CQRS with Go. In this post, we'll explore the Event Store in depth—understanding how events are persisted, ordered, and queried.*

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

This separation allows you to:
- Use in-memory storage for tests
- Use PostgreSQL for production
- Swap adapters without changing application code

---

## Three Event Representations

Understanding the three event types is crucial:

### 1. Application Events (Your Domain)

These are the Go structs you define:

```go
type OrderPlaced struct {
    OrderID    string  `json:"orderId"`
    CustomerID string  `json:"customerId"`
    Total      float64 `json:"total"`
}
```

### 2. EventData (Write Model)

When you append events, they're wrapped as `EventData`:

```go
type EventData struct {
    Type     string   // "OrderPlaced"
    Data     []byte   // JSON: {"orderId":"...","customerId":"..."}
    Metadata Metadata // CorrelationID, UserID, etc.
}
```

### 3. StoredEvent (Storage Model)

After persistence, events become `StoredEvent`:

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

### 4. Event (Application Read Model)

When you load events, they're returned as `Event`:

```go
type Event struct {
    ID             string
    StreamID       string
    Type           string
    Data           interface{} // Deserialized to actual Go type
    Metadata       Metadata
    Version        int64
    GlobalPosition uint64
    Timestamp      time.Time
}
```

The difference: `StoredEvent.Data` is bytes; `Event.Data` is the deserialized struct.

---

## Stream IDs and Categories

### Stream ID Format

Stream IDs follow the pattern: `{Category}-{ID}`

```go
"Order-order-123"     // Order aggregate with ID order-123
"Customer-cust-456"   // Customer aggregate with ID cust-456
"Cart-cart-789"       // Cart aggregate with ID cart-789
```

### Categories

The category (prefix) enables:

- **Filtering**: Load all events for a category
- **Subscriptions**: Subscribe to all Order events
- **Projections**: Build read models per category

```go
// StreamID helper
type StreamID struct {
    Category string  // "Order"
    ID       string  // "order-123"
}

func (s StreamID) String() string {
    return s.Category + "-" + s.ID
}

// Usage
streamID := mink.NewStreamID("Order", "order-123")
// streamID.String() == "Order-order-123"
```

---

## Appending Events

### Basic Append

```go
events := []interface{}{
    OrderPlaced{OrderID: "order-1", CustomerID: "cust-1", Total: 99.99},
    OrderConfirmed{OrderID: "order-1"},
}

stored, err := store.Append(ctx, "Order-order-1", events)
if err != nil {
    return err
}

// stored contains metadata assigned by the store
for _, e := range stored {
    fmt.Printf("Stored: %s, version %d, position %d\n",
        e.Type, e.Version, e.GlobalPosition)
}
```

### Append with Options

```go
// With expected version (optimistic concurrency)
stored, err := store.Append(ctx, streamID, events,
    mink.ExpectVersion(5))

// With metadata
stored, err := store.Append(ctx, streamID, events,
    mink.WithAppendMetadata(mink.Metadata{
        CorrelationID: "req-12345",
        UserID:        "user-alice",
    }))

// Combined
stored, err := store.Append(ctx, streamID, events,
    mink.ExpectVersion(5),
    mink.WithAppendMetadata(metadata))
```

### Version Expectations

| Constant | Value | Meaning |
|----------|-------|---------|
| `AnyVersion` | -1 | Don't check version (dangerous) |
| `NoStream` | 0 | Stream must NOT exist (create new) |
| `StreamExists` | -2 | Stream MUST exist (update existing) |
| Positive int | N | Stream must be at exactly version N |

```go
// Creating a new stream
_, err := store.Append(ctx, "Order-new", events,
    mink.ExpectVersion(mink.NoStream))

// Updating existing stream at version 3
_, err := store.Append(ctx, "Order-existing", events,
    mink.ExpectVersion(3))

// Fail if stream doesn't exist
_, err := store.Append(ctx, "Order-maybe", events,
    mink.ExpectVersion(mink.StreamExists))
```

---

## Loading Events

### Load Entire Stream

```go
events, err := store.Load(ctx, "Order-order-123")
if err != nil {
    if errors.Is(err, mink.ErrStreamNotFound) {
        // Handle missing stream
    }
    return err
}

for _, event := range events {
    fmt.Printf("[v%d] %s: %+v\n",
        event.Version, event.Type, event.Data)
}
```

### Load Raw Events

For when you don't need deserialization:

```go
// LoadRaw returns StoredEvent (bytes, not deserialized)
stored, err := store.LoadRaw(ctx, "Order-order-123", 0)

for _, event := range stored {
    fmt.Printf("[v%d] %s: %s\n",
        event.Version, event.Type, string(event.Data))
}
```

### Load from Version

For partial replay:

```go
// Load only events after version 5
stored, err := store.LoadRaw(ctx, "Order-order-123", 5)
// Returns events at versions 6, 7, 8, ...
```

### Load for Aggregates

The high-level way:

```go
order := NewOrder("order-123")
err := store.LoadAggregate(ctx, order)
if err != nil {
    if errors.Is(err, mink.ErrStreamNotFound) {
        // Order doesn't exist
    }
    return err
}
// order.Version() reflects loaded state
```

---

## Stream Information

### Get Stream Metadata

```go
info, err := store.GetStreamInfo(ctx, "Order-order-123")
if err != nil {
    return err
}

fmt.Printf("Stream: %s\n", info.StreamID)
fmt.Printf("Version: %d events\n", info.Version)
fmt.Printf("Created: %s\n", info.CreatedAt)
fmt.Printf("Updated: %s\n", info.LastEventAt)
```

### Global Position

For projections, you need to know the global position:

```go
lastPosition, err := store.GetLastPosition(ctx)
// lastPosition is the highest global position across all streams
```

### Load from Position

For catching up projections:

```go
// Load 100 events starting from position 5000
events, err := store.LoadEventsFromPosition(ctx, 5000, 100)

for _, event := range events {
    fmt.Printf("Stream: %s, Position: %d, Type: %s\n",
        event.StreamID, event.GlobalPosition, event.Type)
}
```

---

## Serialization

### Default JSON Serializer

go-mink uses JSON by default:

```go
type OrderPlaced struct {
    OrderID    string    `json:"orderId"`
    CustomerID string    `json:"customerId"`
    PlacedAt   time.Time `json:"placedAt"`
}
```

Events are stored as:

```json
{
  "orderId": "order-123",
  "customerId": "cust-456",
  "placedAt": "2024-01-15T10:30:00Z"
}
```

### Event Registration

For deserialization to work, register your events:

```go
store.RegisterEvents(
    OrderPlaced{},
    OrderConfirmed{},
    OrderShipped{},
    OrderCancelled{},
)
```

This maps type names to Go types:
- `"OrderPlaced"` → `OrderPlaced{}`
- `"OrderConfirmed"` → `OrderConfirmed{}`

### Custom Serializers

For different formats (Protobuf, MessagePack):

```go
type Serializer interface {
    Serialize(event interface{}) ([]byte, error)
    Deserialize(data []byte, eventType string) (interface{}, error)
}

// Use custom serializer
store := mink.New(adapter, mink.WithSerializer(mySerializer))
```

---

## Metadata System

### Built-in Metadata Fields

```go
type Metadata struct {
    CorrelationID string            // Links related events
    CausationID   string            // What caused this event
    UserID        string            // Who triggered it
    TenantID      string            // Multi-tenant support
    Custom        map[string]string // Arbitrary key-value pairs
}
```

### Correlation and Causation

**Correlation ID**: Links all events in a business transaction

```
Request comes in with CorrelationID: "req-123"
  └── OrderPlaced       (CorrelationID: "req-123")
  └── PaymentProcessed  (CorrelationID: "req-123")
  └── EmailSent         (CorrelationID: "req-123")
```

**Causation ID**: Chain of causality

```
OrderPlaced (ID: "evt-1", CausationID: "cmd-create")
  └── causes PaymentProcessed (ID: "evt-2", CausationID: "evt-1")
      └── causes EmailSent (ID: "evt-3", CausationID: "evt-2")
```

### Using Metadata

```go
metadata := mink.Metadata{
    CorrelationID: requestID,
    CausationID:   commandID,
    UserID:        currentUser,
    Custom: map[string]string{
        "source":    "web-api",
        "ip":        clientIP,
        "userAgent": userAgent,
    },
}

store.Append(ctx, streamID, events,
    mink.WithAppendMetadata(metadata))
```

---

## Adapters

### Memory Adapter

Perfect for testing:

```go
import "github.com/AshkanYarmoradi/go-mink/adapters/memory"

adapter := memory.NewAdapter()
store := mink.New(adapter)

// No initialization needed
// Data is lost when program exits
```

### PostgreSQL Adapter

For production:

```go
import "github.com/AshkanYarmoradi/go-mink/adapters/postgres"

connStr := "postgres://user:pass@localhost:5432/events?sslmode=disable"

adapter, err := postgres.NewAdapter(connStr)
if err != nil {
    log.Fatal(err)
}
defer adapter.Close()

// Create tables (run once or on startup)
if err := adapter.Initialize(ctx); err != nil {
    log.Fatal(err)
}

store := mink.New(adapter)
```

### PostgreSQL Schema

The adapter creates this table:

```sql
CREATE TABLE IF NOT EXISTS events (
    id             UUID PRIMARY KEY,
    stream_id      VARCHAR(255) NOT NULL,
    type           VARCHAR(255) NOT NULL,
    data           JSONB NOT NULL,
    metadata       JSONB,
    version        BIGINT NOT NULL,
    global_position BIGSERIAL,
    timestamp      TIMESTAMPTZ DEFAULT NOW(),

    UNIQUE(stream_id, version)
);

CREATE INDEX idx_events_stream ON events(stream_id, version);
CREATE INDEX idx_events_global ON events(global_position);
CREATE INDEX idx_events_type ON events(type);
```

### Adapter Interface

To implement a custom adapter:

```go
type EventStoreAdapter interface {
    // Core operations
    Append(ctx context.Context, streamID string, events []EventRecord, expectedVersion int64) ([]StoredEvent, error)
    Load(ctx context.Context, streamID string, fromVersion int64) ([]StoredEvent, error)

    // Metadata
    GetStreamInfo(ctx context.Context, streamID string) (*StreamInfo, error)
    GetLastPosition(ctx context.Context) (uint64, error)

    // Lifecycle
    Initialize(ctx context.Context) error
    Close() error
}
```

---

## Error Handling

### Sentinel Errors

```go
var (
    ErrStreamNotFound         = errors.New("mink: stream not found")
    ErrConcurrencyConflict    = errors.New("mink: concurrency conflict")
    ErrEventTypeNotRegistered = errors.New("mink: event type not registered")
    ErrEmptyStreamID          = errors.New("mink: empty stream ID")
    ErrNoEvents               = errors.New("mink: no events to append")
)
```

### Using errors.Is

```go
stored, err := store.Append(ctx, streamID, events, mink.ExpectVersion(5))
if err != nil {
    if errors.Is(err, mink.ErrConcurrencyConflict) {
        // Reload and retry
        return handleConcurrencyConflict(ctx, streamID, events)
    }
    return err
}
```

### Detailed Error Types

```go
if err != nil {
    var concErr *mink.ConcurrencyError
    if errors.As(err, &concErr) {
        fmt.Printf("Conflict on stream %s: expected %d, got %d\n",
            concErr.StreamID,
            concErr.ExpectedVersion,
            concErr.ActualVersion)
    }
}
```

---

## Subscriptions

### Loading from Position

For batch processing:

```go
position := uint64(0)  // Start from beginning

for {
    events, err := store.LoadEventsFromPosition(ctx, position, 100)
    if err != nil {
        return err
    }

    if len(events) == 0 {
        break  // Caught up
    }

    for _, event := range events {
        processEvent(event)
        position = event.GlobalPosition + 1
    }
}
```

### Real-time Subscriptions

For live updates (PostgreSQL adapter):

```go
// Subscribe to all events
eventCh, err := adapter.SubscribeAll(ctx, lastPosition)
if err != nil {
    return err
}

for event := range eventCh {
    fmt.Printf("New event: %s in %s\n", event.Type, event.StreamID)
    processEvent(event)
}
```

### Subscribe to Stream

```go
// Subscribe to a specific stream
eventCh, err := adapter.SubscribeStream(ctx, "Order-order-123", 0)
```

### Subscribe to Category

```go
// Subscribe to all Order events
eventCh, err := adapter.SubscribeCategory(ctx, "Order", 0)
```

---

## Performance Considerations

### Batching Appends

Append multiple events atomically:

```go
// Good: Single append with multiple events
events := []interface{}{
    OrderPlaced{...},
    PaymentAuthorized{...},
    InventoryReserved{...},
}
store.Append(ctx, streamID, events)

// Avoid: Multiple appends (more round trips)
store.Append(ctx, streamID, []interface{}{OrderPlaced{...}})
store.Append(ctx, streamID, []interface{}{PaymentAuthorized{...}})
store.Append(ctx, streamID, []interface{}{InventoryReserved{...}})
```

### Load Optimization

For long streams, load only what you need:

```go
// If you only need recent events
stored, _ := store.LoadRaw(ctx, streamID, lastKnownVersion)

// If you need snapshots (covered in Part 8)
// Load snapshot + events after snapshot
```

### Connection Pooling

The PostgreSQL adapter uses connection pooling:

```go
adapter, _ := postgres.NewAdapter(connStr,
    postgres.WithMaxConnections(20),
    postgres.WithMinConnections(5))
```

---

## What's Next?

In this post, you learned:

- The three event representations: domain, storage, and application
- How stream IDs and categories work
- Appending events with versioning and metadata
- Loading events and stream information
- The adapter architecture and PostgreSQL specifics
- Subscription patterns for projections

In **Part 5**, we'll introduce **CQRS and the Command Bus**—separating reads from writes and dispatching commands through a middleware pipeline.

---

## Key Takeaways

1. **Events transform through the system**: Domain → Storage → Application
2. **Stream IDs have structure**: `Category-ID` enables filtering
3. **Version expectations prevent conflicts**: Use appropriately
4. **Metadata enables observability**: Correlation, causation, and custom data
5. **Adapters are swappable**: Same code, different storage

---

*Previous: [← Part 3: Building Your First Aggregate](03-building-your-first-aggregate.md)*

*Next: [Part 5: CQRS and the Command Bus →](05-cqrs-and-command-bus.md)*
