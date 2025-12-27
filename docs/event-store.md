---
layout: default
title: Event Store
nav_order: 4
permalink: /docs/event-store
---

# Event Store Design
{: .no_toc }

{: .label .label-green }
Implemented

## Table of contents
{: .no_toc .text-delta }

1. TOC
{:toc}

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

## PostgreSQL Schema

```sql
-- Event streams table
CREATE TABLE go-mink_streams (
    id              BIGSERIAL PRIMARY KEY,
    stream_id       VARCHAR(500) NOT NULL UNIQUE,
    category        VARCHAR(250) NOT NULL,
    version         BIGINT NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_streams_category ON go-mink_streams(category);

-- Events table
CREATE TABLE go-mink_events (
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

CREATE INDEX idx_events_stream ON go-mink_events(stream_id, version);
CREATE INDEX idx_events_type ON go-mink_events(event_type);
CREATE INDEX idx_events_timestamp ON go-mink_events(timestamp);

-- Optimistic concurrency function
CREATE OR REPLACE FUNCTION go-mink_append_events(
    p_stream_id VARCHAR(500),
    p_category VARCHAR(250),
    p_expected_version BIGINT,
    p_events JSONB
) RETURNS TABLE(global_position BIGINT, version BIGINT) AS $$
DECLARE
    v_current_version BIGINT;
    v_event JSONB;
    v_new_version BIGINT;
BEGIN
    -- Lock stream row
    SELECT version INTO v_current_version
    FROM go-mink_streams
    WHERE stream_id = p_stream_id
    FOR UPDATE;
    
    -- Handle new stream
    IF v_current_version IS NULL THEN
        IF p_expected_version NOT IN (-1, 0) THEN
            RAISE EXCEPTION 'CONCURRENCY_ERROR: Stream does not exist';
        END IF;
        
        INSERT INTO go-mink_streams (stream_id, category, version)
        VALUES (p_stream_id, p_category, 0);
        v_current_version := 0;
    ELSE
        -- Check expected version
        IF p_expected_version >= 0 AND v_current_version != p_expected_version THEN
            RAISE EXCEPTION 'CONCURRENCY_ERROR: Expected %, actual %', 
                p_expected_version, v_current_version;
        END IF;
    END IF;
    
    -- Insert events
    v_new_version := v_current_version;
    FOR v_event IN SELECT * FROM jsonb_array_elements(p_events)
    LOOP
        v_new_version := v_new_version + 1;
        
        INSERT INTO go-mink_events (stream_id, version, event_type, data, metadata)
        VALUES (
            p_stream_id,
            v_new_version,
            v_event->>'type',
            v_event->'data',
            v_event->'metadata'
        )
        RETURNING go-mink_events.global_position, go-mink_events.version
        INTO global_position, version;
        
        RETURN NEXT;
    END LOOP;
    
    -- Update stream version
    UPDATE go-mink_streams 
    SET version = v_new_version, updated_at = NOW()
    WHERE stream_id = p_stream_id;
END;
$$ LANGUAGE plpgsql;
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
