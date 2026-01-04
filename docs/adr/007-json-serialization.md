---
layout: default
title: "ADR-007: JSON as Default Serialization"
parent: Architecture Decision Records
nav_order: 7
---

# ADR-007: JSON as Default Serialization

| Status | Date | Deciders |
|--------|------|----------|
| Accepted | 2024-01-15 | Core Team |

## Context

Events must be serialized for storage. The serialization format affects:

1. **Readability**: Can humans read the data?
2. **Schema Evolution**: How do we handle changes?
3. **Performance**: Serialization/deserialization speed
4. **Size**: Storage and network costs
5. **Tooling**: Database querying, debugging

We need to choose a default format while allowing alternatives.

## Decision

We will use **JSON** as the default serialization format with **JSONB** storage in PostgreSQL.

### Implementation

```go
type JSONSerializer struct {
    registry *EventRegistry
}

func (s *JSONSerializer) Serialize(event interface{}) ([]byte, string, error) {
    eventType := s.registry.TypeName(event)
    if eventType == "" {
        return nil, "", ErrUnregisteredEvent
    }
    
    data, err := json.Marshal(event)
    if err != nil {
        return nil, "", fmt.Errorf("failed to serialize event: %w", err)
    }
    
    return data, eventType, nil
}

func (s *JSONSerializer) Deserialize(data []byte, eventType string) (interface{}, error) {
    event := s.registry.NewInstance(eventType)
    if event == nil {
        return nil, fmt.Errorf("unknown event type: %s", eventType)
    }
    
    if err := json.Unmarshal(data, event); err != nil {
        return nil, fmt.Errorf("failed to deserialize event: %w", err)
    }
    
    return event, nil
}
```

### Event Registry

```go
type EventRegistry struct {
    types map[string]reflect.Type
    names map[reflect.Type]string
}

func (r *EventRegistry) Register(eventType string, event interface{}) {
    t := reflect.TypeOf(event)
    if t.Kind() == reflect.Ptr {
        t = t.Elem()
    }
    r.types[eventType] = t
    r.names[t] = eventType
}

// Usage
registry := NewEventRegistry()
registry.Register("OrderCreated", OrderCreated{})
registry.Register("ItemAdded", ItemAdded{})
```

### PostgreSQL JSONB Benefits

```sql
-- Query events by content
SELECT * FROM mink_events 
WHERE data->>'customerId' = 'cust-123';

-- Index on JSONB field
CREATE INDEX idx_events_customer 
ON mink_events ((data->>'customerId'));

-- Partial index for specific event types
CREATE INDEX idx_order_events 
ON mink_events (stream_id) 
WHERE type LIKE 'Order%';
```

### Alternative Serializers

We provide a `Serializer` interface for alternatives:

```go
type Serializer interface {
    Serialize(event interface{}) ([]byte, string, error)
    Deserialize(data []byte, eventType string) (interface{}, error)
}

// MessagePack for performance
store := mink.New(adapter, mink.WithSerializer(msgpack.NewSerializer(registry)))

// Custom serializer
store := mink.New(adapter, mink.WithSerializer(mySerializer))
```

## Consequences

### Positive

1. **Human Readable**: Easy to debug and inspect
2. **Universal**: Every language supports JSON
3. **Flexible Schema**: Add fields without breaking readers
4. **Database Queries**: JSONB enables SQL queries on event data
5. **No Code Generation**: Works with standard Go structs
6. **Tooling**: Works with jq, JSON viewers, etc.

### Negative

1. **Size**: Larger than binary formats (~2-3x)
2. **Performance**: Slower than Protocol Buffers or MessagePack
3. **No Schema Enforcement**: Easy to make mistakes
4. **Type Information**: Limited type fidelity (no int64 distinction)

### Neutral

1. **Compression**: Can use database compression to reduce size
2. **Alternatives Available**: MessagePack, Protobuf can be used if needed

## Schema Evolution

JSON handles schema evolution well:

### Adding Fields (Safe)

```go
// v1
type OrderCreated struct {
    OrderID    string
    CustomerID string
}

// v2 - added field
type OrderCreated struct {
    OrderID    string
    CustomerID string
    Priority   string // New field, defaults to zero value
}
```

### Removing Fields (Safe)

```go
// Old events with removed field are still readable
// Unknown fields are ignored during deserialization
```

### Renaming Fields (Requires Migration)

```go
// Use JSON tags for backward compatibility
type OrderCreated struct {
    OrderID    string `json:"orderId"`
    CustomerID string `json:"customerId"`
    // Renamed from "customer_id" to "customerId"
}
```

### Changing Types (Requires Upcasting)

```go
// v1: Price as float
// v2: Price as Money struct
// Requires upcaster to transform old events
```

## Performance Comparison

Benchmarks on typical event (OrderCreated with 3 items):

| Format | Serialize | Deserialize | Size |
|--------|-----------|-------------|------|
| JSON | 450ns | 650ns | 380 bytes |
| MessagePack | 180ns | 220ns | 195 bytes |
| Protocol Buffers | 120ns | 150ns | 145 bytes |

For most applications, JSON performance is acceptable. Use alternatives when:
- Processing >10,000 events/second
- Storage costs are critical
- Network bandwidth is constrained

## Alternatives Considered

### Alternative 1: Protocol Buffers

**Pros**:
- Small size
- Fast serialization
- Schema enforcement
- Code generation

**Rejected as default because**:
- Requires code generation
- Less readable
- Harder to query in database
- Schema changes require regeneration

**Available as option for high-performance needs.**

### Alternative 2: MessagePack

**Pros**:
- Binary JSON (smaller)
- Faster than JSON
- No code generation

**Rejected as default because**:
- Not human readable
- Less tooling support
- Similar flexibility to JSON with less benefit

**Available as option via `serializer/msgpack`.**

### Alternative 3: Avro

**Pros**:
- Schema registry
- Compact
- Good evolution support

**Rejected because**:
- Requires schema registry
- More complex setup
- Less common in Go ecosystem

### Alternative 4: BSON

**Pros**:
- Rich type support
- MongoDB compatible

**Rejected because**:
- MongoDB-specific
- Larger than JSON in many cases
- Less tooling

## References

- [PostgreSQL JSONB](https://www.postgresql.org/docs/current/datatype-json.html)
- [JSON Schema Evolution](https://json-schema.org/understanding-json-schema/reference/schema.html)
- [Avro Schema Evolution](https://avro.apache.org/docs/current/spec.html#Schema+Resolution)
- [Protocol Buffers](https://developers.google.com/protocol-buffers)
