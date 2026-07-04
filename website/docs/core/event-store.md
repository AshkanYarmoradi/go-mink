---
title: Event Store
sidebar_position: 4
---

# Event Store Design

<span class="badge badge--success">Implemented</span>

---

## Event Structure

```go
// EventData represents an event to be stored
type EventData struct {
    // Event type identifier (e.g., "OrderCreated")
    Type string

    // Serialized event payload
    Data []byte

    // Optional metadata (correlation ID, causation ID, user ID)
    Metadata Metadata
}

// StoredEvent represents a persisted event
type StoredEvent struct {
    // Global unique event ID
    ID string

    // Stream this event belongs to
    StreamID StreamID

    // Event type
    Type string

    // Serialized payload
    Data []byte

    // Event metadata
    Metadata Metadata

    // Position within stream (1-based)
    Version int64

    // Global position across all streams
    GlobalPosition uint64

    // When the event was stored
    Timestamp time.Time
}

// StreamID uniquely identifies an event stream
type StreamID struct {
    // Category (e.g., "Order", "Customer")
    Category string

    // Instance ID (e.g., "order-123")
    ID string
}

func (s StreamID) String() string {
    return fmt.Sprintf("%s-%s", s.Category, s.ID)
}
```

## Core Operations

### Appending Events

```go
// AppendToStream adds events with optimistic concurrency
func (s *EventStore) AppendToStream(
    ctx context.Context,
    streamID StreamID,
    expectedVersion int64,
    events []EventData,
) error

// Usage
err := store.AppendToStream(ctx,
    StreamID{Category: "Order", ID: "123"},
    2,  // Expected version (for optimistic concurrency)
    []EventData{
        {Type: "ItemAdded", Data: itemAddedJSON},
    },
)

// Special version constants
const (
    AnyVersion      int64 = -1  // No concurrency check
    NoStream        int64 = 0   // Stream must not exist
    StreamExists    int64 = -2  // Stream must exist
)
```

### Loading Events

```go
// LoadStream retrieves all events from a stream
func (s *EventStore) LoadStream(
    ctx context.Context,
    streamID StreamID,
) ([]StoredEvent, error)

// LoadStreamFrom retrieves events from a specific version
func (s *EventStore) LoadStreamFrom(
    ctx context.Context,
    streamID StreamID,
    fromVersion int64,
) ([]StoredEvent, error)

// LoadStreamRange retrieves a range of events
func (s *EventStore) LoadStreamRange(
    ctx context.Context,
    streamID StreamID,
    fromVersion int64,
    count int,
) ([]StoredEvent, error)
```

### Subscriptions

```go
// SubscribeToStream subscribes to a single stream
func (s *EventStore) SubscribeToStream(
    ctx context.Context,
    streamID StreamID,
    fromVersion int64,
) (<-chan StoredEvent, error)

// SubscribeToAll subscribes to all events (for projections)
func (s *EventStore) SubscribeToAll(
    ctx context.Context,
    fromPosition uint64,
) (<-chan StoredEvent, error)

// SubscribeToCategory subscribes to all streams in a category
func (s *EventStore) SubscribeToCategory(
    ctx context.Context,
    category string,
    fromPosition uint64,
) (<-chan StoredEvent, error)
```

### Filtered feed reads

`LoadEventsFromPositionFiltered` is a filtered variant of the load-from-position read
for **introspection** — audit browsers, migration/backfill scanners, and diagnostics
that read the raw event log by an *indexed* axis instead of loading the whole feed and
filtering in application code.

```go
// LoadEventsFromPositionFiltered reads events after a global position that match an
// indexed FeedFilter, pushing the predicate down to storage.
func (s *EventStore) LoadEventsFromPositionFiltered(
    ctx context.Context,
    fromPosition uint64,
    limit int,
    filter FeedFilter, // alias of adapters.FeedFilter
) ([]StoredEvent, error)

// FeedFilter selects by indexed columns only. Axes AND-compose; a multi-valued axis
// is an OR/IN set. An empty filter behaves exactly like the unfiltered read.
type FeedFilter struct {
    EventTypes []string // event_type IN (...)   — idx_events_type
    StreamIDs  []string // stream_id  IN (...)   — idx_events_stream
    Category   string   // stream_id  LIKE '<Category>-%'
}
```

Results keep the ordering, `limit`, exclusivity, and gapless safe-watermark
guarantees of `LoadEventsFromPosition`. Filtering is **indexed-only by design**:
querying the feed by unindexed data (a tenant in `metadata`, a payload field) belongs
in a purpose-built read model, not here — which keeps this a bounded introspection
primitive rather than an ad-hoc query engine over the log. Adapters that cannot serve
it return `ErrFilteredFeedNotSupported`; the in-memory and PostgreSQL adapters both
implement it.

## PostgreSQL Schema

```sql
-- Event streams table
CREATE TABLE streams (
    id              BIGSERIAL PRIMARY KEY,
    stream_id       VARCHAR(500) NOT NULL UNIQUE,
    category        VARCHAR(250) NOT NULL,
    version         BIGINT NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_streams_category ON streams(category);

-- Events table
CREATE TABLE events (
    global_position BIGSERIAL PRIMARY KEY,
    stream_id       VARCHAR(500) NOT NULL,
    version         BIGINT NOT NULL,
    event_id        UUID NOT NULL DEFAULT gen_random_uuid(),
    event_type      VARCHAR(500) NOT NULL,
    data            JSONB NOT NULL,
    metadata        JSONB,
    timestamp       TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    UNIQUE(stream_id, version)
);

CREATE INDEX idx_events_stream ON events(stream_id, version);
CREATE INDEX idx_events_type ON events(event_type);
CREATE INDEX idx_events_timestamp ON events(timestamp);

-- Optimistic concurrency is enforced by the UNIQUE(stream_id, version)
-- constraint above together with the adapter's append logic; the adapter does
-- not create a stored procedure.
```

## Serialization

```go
// EventSerializer handles event payload serialization
type EventSerializer interface {
    Serialize(event interface{}) ([]byte, error)
    Deserialize(data []byte, eventType string) (interface{}, error)
}

// JSONSerializer is the default implementation
type JSONSerializer struct {
    registry map[string]reflect.Type
}

func (s *JSONSerializer) RegisterEvent(eventType string, example interface{}) {
    s.registry[eventType] = reflect.TypeOf(example)
}

// Usage
serializer := go-mink.NewJSONSerializer()
serializer.RegisterEvent("OrderCreated", OrderCreated{})
serializer.RegisterEvent("ItemAdded", ItemAdded{})

store := go-mink.NewEventStore(adapter, go-mink.WithSerializer(serializer))
```

:::warning Binary serializers require a byte-oriented store
The default `JSONSerializer` emits JSON text, which the PostgreSQL adapter's
`JSONB` `data` column stores directly. The binary serializers
(`serializer/msgpack`, `serializer/protobuf`) emit non-JSON bytes, so pairing one
with the PostgreSQL (JSONB) adapter makes the first `Append`/`SaveAggregate`
return `mink.ErrBinarySerializerUnsupported` (detected at construction, surfaced
before any write). Use a binary serializer only with the in-memory adapter or a
`BYTEA`-backed store.
:::

## Metadata

```go
// Metadata contains event context
type Metadata struct {
    // Correlation ID for distributed tracing
    CorrelationID string `json:"correlationId,omitempty"`

    // Causation ID links to causing event/command
    CausationID string `json:"causationId,omitempty"`

    // User who triggered this event
    UserID string `json:"userId,omitempty"`

    // Tenant ID for multi-tenancy
    TenantID string `json:"tenantId,omitempty"`

    // Custom key-value pairs
    Custom map[string]string `json:"custom,omitempty"`
}

// MetadataFromContext extracts metadata from context
func MetadataFromContext(ctx context.Context) Metadata {
    return Metadata{
        CorrelationID: trace.SpanFromContext(ctx).SpanContext().TraceID().String(),
        UserID:        auth.UserFromContext(ctx),
        TenantID:      tenant.FromContext(ctx),
    }
}
```

## Snapshots

```go
// Snapshot stores aggregate state for fast loading
type Snapshot struct {
    StreamID  StreamID
    Version   int64
    Data      []byte
    Timestamp time.Time
}

// SnapshotStore manages aggregate snapshots
type SnapshotStore interface {
    Save(ctx context.Context, snapshot Snapshot) error
    Load(ctx context.Context, streamID StreamID) (*Snapshot, error)
    Delete(ctx context.Context, streamID StreamID) error
}

// SnapshotPolicy determines when to create snapshots
type SnapshotPolicy interface {
    ShouldSnapshot(aggregate Aggregate) bool
}

// Every N events
type EveryNEventsPolicy struct {
    N int
}

func (p EveryNEventsPolicy) ShouldSnapshot(agg Aggregate) bool {
    return agg.Version()%int64(p.N) == 0
}
```

---

Next: [Read Models →](read-models)
