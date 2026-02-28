---
layout: default
title: Event Versioning
nav_order: 10
permalink: /docs/versioning
---

# Event Versioning & Upcasting
{: .no_toc }

## Table of contents
{: .no_toc .text-delta }

1. TOC
{:toc}

---

## Overview

Event schemas inevitably change as your domain evolves. A `ProductAdded` event might start with just `Name` and `Price`, but later gain `Currency` and `TaxRate` fields. Since events are immutable and stored forever, you need a strategy for reading old events with new code.

go-mink solves this with **upcasting** — transparent, on-the-fly transformation of old event data to the latest schema version during deserialization. Old events in the database are never modified; instead, upcasters transform the raw bytes before they reach your application code.

### Key Design Decisions

| Decision | Rationale |
|----------|-----------|
| Schema version in `Metadata.Custom["$schema_version"]` | No database migration needed — works with existing events table |
| Absent version defaults to 1 | Backward compatible with all pre-versioning events |
| Upcasters operate on raw `[]byte` | Serializer-agnostic (JSON, MessagePack, Protobuf all store `[]byte`) |
| No `context.Context` in upcasters | Pure data transformation — no I/O, deterministic, testable |
| Single-event output (1:1) | Splitting would break stream version invariants |
| `nil` upcaster chain by default | Zero overhead when versioning is not used |

---

## Quick Start

```go
package main

import (
    "context"
    "encoding/json"

    "github.com/AshkanYarmoradi/go-mink"
    "github.com/AshkanYarmoradi/go-mink/adapters/memory"
)

// ProductAdded is the latest (v3) event schema.
// v1: Name, Price
// v2: + Currency (default "USD")
// v3: + TaxRate  (default 0.0)
type ProductAdded struct {
    ProductID string  `json:"product_id"`
    Name      string  `json:"name"`
    Price     float64 `json:"price"`
    Currency  string  `json:"currency"` // v2
    TaxRate   float64 `json:"tax_rate"` // v3
}

// Upcaster: v1 → v2 (add Currency)
type productAddedV1ToV2 struct{}

func (u productAddedV1ToV2) EventType() string { return "ProductAdded" }
func (u productAddedV1ToV2) FromVersion() int  { return 1 }
func (u productAddedV1ToV2) ToVersion() int    { return 2 }
func (u productAddedV1ToV2) Upcast(data []byte, _ mink.Metadata) ([]byte, error) {
    var m map[string]interface{}
    if err := json.Unmarshal(data, &m); err != nil {
        return nil, err
    }
    m["currency"] = "USD"
    return json.Marshal(m)
}

// Upcaster: v2 → v3 (add TaxRate)
type productAddedV2ToV3 struct{}

func (u productAddedV2ToV3) EventType() string { return "ProductAdded" }
func (u productAddedV2ToV3) FromVersion() int  { return 2 }
func (u productAddedV2ToV3) ToVersion() int    { return 3 }
func (u productAddedV2ToV3) Upcast(data []byte, _ mink.Metadata) ([]byte, error) {
    var m map[string]interface{}
    if err := json.Unmarshal(data, &m); err != nil {
        return nil, err
    }
    m["tax_rate"] = 0.0
    return json.Marshal(m)
}

func main() {
    ctx := context.Background()

    // Build upcaster chain
    chain := mink.NewUpcasterChain()
    chain.Register(productAddedV1ToV2{})
    chain.Register(productAddedV2ToV3{})
    chain.Validate() // check for gaps

    // Create store with upcasters
    adapter := memory.NewAdapter()
    store := mink.New(adapter, mink.WithUpcasters(chain))
    store.RegisterEvents(ProductAdded{})

    // Load old events — transparently upcasted to v3
    events, _ := store.Load(ctx, "Product-prod-001")
    product := events[0].Data.(ProductAdded)
    // product.Currency == "USD"  (filled by v1→v2 upcaster)
    // product.TaxRate  == 0.0    (filled by v2→v3 upcaster)
}
```

See the full runnable example at [`examples/versioning/main.go`](https://github.com/AshkanYarmoradi/go-mink/blob/main/examples/versioning/main.go).

---

## Upcaster Interface

Each upcaster handles exactly one version transition (e.g., v1 → v2). Upcasters receive raw bytes and metadata, and return transformed bytes.

```go
type Upcaster interface {
    // EventType returns the event type this upcaster handles (e.g., "OrderCreated").
    EventType() string

    // FromVersion returns the source schema version.
    FromVersion() int

    // ToVersion returns the target schema version. Must equal FromVersion() + 1.
    ToVersion() int

    // Upcast transforms event data from FromVersion to ToVersion.
    // Metadata is provided as read-only context (e.g., for tenant-specific defaults).
    Upcast(data []byte, metadata Metadata) ([]byte, error)
}
```

### Rules

- `ToVersion()` must equal `FromVersion() + 1` — each upcaster handles exactly one step.
- `FromVersion()` must be >= 1.
- Upcasters should be **pure functions** — no side effects, no I/O, deterministic output.
- The `metadata` parameter is read-only context. Use it for tenant-aware defaults, not for mutation.

### Metadata-Aware Upcasting

The metadata parameter enables tenant-specific or context-aware defaults:

```go
func (u *orderCreatedV1ToV2) Upcast(data []byte, metadata mink.Metadata) ([]byte, error) {
    var m map[string]interface{}
    json.Unmarshal(data, &m)

    // Set default currency based on tenant
    switch metadata.TenantID {
    case "eu-tenant":
        m["currency"] = "EUR"
    case "jp-tenant":
        m["currency"] = "JPY"
    default:
        m["currency"] = "USD"
    }

    return json.Marshal(m)
}
```

---

## UpcasterChain

The `UpcasterChain` is a thread-safe registry that manages and applies upcasters in sequence.

```go
chain := mink.NewUpcasterChain()

// Register upcasters (order doesn't matter — sorted automatically)
chain.Register(orderCreatedV2ToV3{})
chain.Register(orderCreatedV1ToV2{})

// Validate the chain has no gaps
if err := chain.Validate(); err != nil {
    log.Fatal(err) // e.g., "mink: schema version gap for OrderCreated: ..."
}

// Query chain state
chain.HasUpcasters("OrderCreated")      // true
chain.LatestVersion("OrderCreated")     // 3
chain.RegisteredEventTypes()            // ["OrderCreated"]

// Manually upcast (usually done automatically by EventStore)
data, finalVersion, err := chain.Upcast("OrderCreated", 1, rawBytes, metadata)
```

### Validation

`Validate()` checks that registered upcasters form a contiguous chain for each event type. For example, if you have v1→v2 and v3→v4 upcasters but no v2→v3, `Validate()` returns an `ErrSchemaVersionGap` error.

---

## EventStore Integration

### Configuration

```go
// Option 1: Configure at construction
store := mink.New(adapter, mink.WithUpcasters(chain))

// Option 2: Register after construction
store.RegisterUpcasters(myUpcasterV1ToV2, myUpcasterV2ToV3)
// Creates a chain automatically if none exists
```

### Automatic Behavior

Once configured, the EventStore handles versioning transparently:

| Operation | Behavior |
|-----------|----------|
| `Load()` / `LoadFrom()` | Events are upcasted from their stored version to the latest version |
| `LoadAggregate()` | Events are upcasted before being applied to the aggregate |
| `Append()` | New events are stamped with `$schema_version` = latest version |
| `SaveAggregate()` | Same stamping as `Append()` |
| `LoadRaw()` | Returns raw events **without** upcasting (for inspection/debugging) |

### Zero Overhead

When no upcasters are configured (`upcasters` field is `nil`), all code paths short-circuit immediately. There is no performance penalty for code that doesn't use versioning.

---

## Schema Version Storage

The schema version is stored in `Metadata.Custom["$schema_version"]` using the existing metadata infrastructure:

```go
// Read schema version from event metadata (defaults to 1 if absent)
version := mink.GetSchemaVersion(event.Metadata)

// Set schema version
metadata = mink.SetSchemaVersion(metadata, 3)
```

The `$` prefix marks this as a system-managed key, preventing collisions with user metadata. This approach requires **zero changes** to database schemas, adapter interfaces, or the PostgreSQL events table.

---

## UpcastingSerializer

The `UpcastingSerializer` is a decorator that wraps any `Serializer` and applies upcasting during deserialization. It implements the `Serializer` interface, so it can be used anywhere a `Serializer` is expected.

```go
inner := mink.NewJSONSerializer()
inner.Register("OrderCreated", OrderCreated{})

chain := mink.NewUpcasterChain()
chain.Register(orderCreatedV1ToV2{})

serializer := mink.NewUpcastingSerializer(inner, chain)

// Serialize — passes through to inner serializer (no transformation)
data, err := serializer.Serialize(event)

// Deserialize — applies upcasting from DefaultSchemaVersion (1)
result, err := serializer.Deserialize(data, "OrderCreated")

// DeserializeWithVersion — explicit version control
result, err := serializer.DeserializeWithVersion(data, "OrderCreated", 2, metadata)

// Access inner components
serializer.Inner() // returns the wrapped serializer
serializer.Chain() // returns the upcaster chain
```

The `UpcastingSerializer` is useful when you need upcasting outside of the EventStore — for example, in custom projection handlers or message consumers.

---

## SchemaRegistry

The `SchemaRegistry` is an optional in-memory registry that tracks event schema definitions and provides compatibility analysis. It is independent of the upcasting system and can be used for documentation, validation, or tooling.

```go
registry := mink.NewSchemaRegistry()

// Register schema versions
registry.Register("OrderCreated", mink.SchemaDefinition{
    Version: 1,
    Fields: []mink.FieldDefinition{
        {Name: "order_id", Type: "string", Required: true},
        {Name: "amount", Type: "float64", Required: true},
    },
})

registry.Register("OrderCreated", mink.SchemaDefinition{
    Version: 2,
    Fields: []mink.FieldDefinition{
        {Name: "order_id", Type: "string", Required: true},
        {Name: "amount", Type: "float64", Required: true},
        {Name: "currency", Type: "string", Required: false}, // new field
    },
})

// Query schemas
schema, err := registry.GetSchema("OrderCreated", 1)
latestVersion, err := registry.GetLatestVersion("OrderCreated")
eventTypes := registry.RegisteredEventTypes()

// Check compatibility between versions
compat, err := registry.CheckCompatibility("OrderCreated", 1, 2)
// Returns one of:
//   SchemaFullyCompatible    — no fields added, removed, or changed
//   SchemaBackwardCompatible — fields added (new code can read old data)
//   SchemaForwardCompatible  — fields removed (old code can read new data)
//   SchemaBreaking           — field types changed, or fields both added and removed
```

### SchemaDefinition

```go
type SchemaDefinition struct {
    Version      int                // Schema version number (>= 1)
    Fields       []FieldDefinition  // Field metadata
    JSONSchema   json.RawMessage    // Optional JSON Schema document
    RegisteredAt time.Time          // Auto-set if zero
}

type FieldDefinition struct {
    Name     string // Field name
    Type     string // Field type (e.g., "string", "int", "float64")
    Required bool   // Whether the field must be present
}
```

---

## Error Handling

### Sentinel Errors

```go
var (
    ErrUpcastFailed       = errors.New("mink: upcast failed")
    ErrSchemaVersionGap   = errors.New("mink: schema version gap")
    ErrIncompatibleSchema = errors.New("mink: incompatible schema")
    ErrSchemaNotFound     = errors.New("mink: schema not found")
)

// Usage
if errors.Is(err, mink.ErrUpcastFailed) {
    // Handle upcasting failure
}
```

### Typed Errors

All typed errors implement `Is()` and `Unwrap()` for `errors.Is()` and `errors.As()` compatibility:

```go
// UpcastError — detailed upcasting failure
var upcastErr *mink.UpcastError
if errors.As(err, &upcastErr) {
    fmt.Printf("Failed to upcast %s from v%d to v%d: %v\n",
        upcastErr.EventType,
        upcastErr.FromVersion,
        upcastErr.ToVersion,
        upcastErr.Unwrap())
}

// SchemaVersionGapError — gap in upcaster chain
var gapErr *mink.SchemaVersionGapError
if errors.As(err, &gapErr) {
    fmt.Printf("Missing upcaster for %s: expected v%d, found v%d\n",
        gapErr.EventType,
        gapErr.MissingVersion,
        gapErr.ExpectedVersion)
}

// IncompatibleSchemaError — schema compatibility issue
var incompatErr *mink.IncompatibleSchemaError
if errors.As(err, &incompatErr) {
    fmt.Printf("Incompatible schema for %s v%d→v%d: %s (%s)\n",
        incompatErr.EventType,
        incompatErr.OldVersion,
        incompatErr.NewVersion,
        incompatErr.Compatibility,
        incompatErr.Reason)
}
```

---

## Best Practices

### Event Type Naming

Use a single, consistent event type name across all schema versions. The stored event type (determined by Go struct name via `GetEventType()`) must remain stable:

```go
// GOOD: Same struct name, fields evolve
type ProductAdded struct {
    Name     string  `json:"name"`
    Price    float64 `json:"price"`
    Currency string  `json:"currency"` // added in v2
}

// BAD: Different struct names create different stored types
type ProductAddedV1 struct { ... }  // stores as "ProductAddedV1"
type ProductAddedV2 struct { ... }  // stores as "ProductAddedV2"
```

### Upcaster Design

1. **Keep upcasters simple** — each should handle exactly one field addition/change.
2. **Use sensible defaults** — old events should get reasonable values for new fields.
3. **Test upcasters independently** — they are pure functions, easy to unit test.
4. **Validate chains at startup** — call `chain.Validate()` during initialization.
5. **Use metadata for context** — tenant-specific or region-specific defaults via `Metadata`.

### Migration Strategy

1. **Add new fields to your event struct** with sensible zero values.
2. **Write an upcaster** for the version transition.
3. **Register the upcaster** with the chain.
4. **Deploy** — old events are transparently upcasted on read.
5. **No database migration needed** — schema version flows through existing metadata.

---

## Files

| File | Description |
|------|-------------|
| `versioning.go` | `Upcaster` interface, `UpcasterChain`, `GetSchemaVersion`/`SetSchemaVersion` helpers |
| `versioning_errors.go` | Sentinel errors and typed errors (`UpcastError`, `SchemaVersionGapError`, `IncompatibleSchemaError`) |
| `upcasting_serializer.go` | `UpcastingSerializer` decorator wrapping any `Serializer` |
| `schema_registry.go` | `SchemaRegistry`, `SchemaDefinition`, `SchemaCompatibility`, compatibility checking |
| `store.go` | `WithUpcasters` option, `RegisterUpcasters`, automatic upcasting in `Load`/`LoadAggregate`/`Append`/`SaveAggregate` |
| `serializer.go` | `SerializeEventWithVersion` helper function |
| `examples/versioning/main.go` | Full runnable example with v1→v2→v3 upcasting |

---

Next: [Security →](security)
