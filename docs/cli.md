---
layout: default
title: CLI
nav_order: 7
permalink: /docs/cli
---

# CLI Tool
{: .no_toc }

## Table of contents
{: .no_toc .text-delta }

1. TOC
{:toc}

---

## Overview

The `go-mink` CLI provides essential tooling for development and operations.

```bash
$ go-mink --help

go-mink - Event Sourcing Toolkit for Go

Usage:
  go-mink [command]

Available Commands:
  init        Initialize a new go-mink project
  generate    Generate code (events, projections, aggregates)
  migrate     Database schema migrations
  projection  Manage projections
  stream      Inspect and manage event streams
  diagnose    Health checks and diagnostics
  version     Print version information

Flags:
  -c, --config string   Config file (default "./go-mink.yaml")
  -v, --verbose         Verbose output
  -h, --help            Help for go-mink

Use "go-mink [command] --help" for more information about a command.
```

## Commands

### `go-mink init`

Initialize a new go-mink project.

```bash
$ go-mink init

? Project name: myapp
? Event store adapter: PostgreSQL
? Read model adapter: PostgreSQL
? Include example code? Yes

Creating project structure...
✓ Created go-mink.yaml
✓ Created internal/events/
✓ Created internal/aggregates/
✓ Created internal/projections/
✓ Created internal/readmodels/
✓ Created cmd/migrate/main.go

Next steps:
  1. Update go-mink.yaml with your database connection
  2. Run 'go-mink migrate up' to create tables
  3. Define your first aggregate with 'go-mink generate aggregate'
```

Generated `go-mink.yaml`:

```yaml
version: "1"
name: myapp

eventstore:
  adapter: postgres
  connection: ${DATABASE_URL}
  schema: events
  
readmodels:
  adapter: postgres
  connection: ${DATABASE_URL}
  schema: readmodels

snapshots:
  adapter: postgres
  connection: ${DATABASE_URL}
  interval: 100  # Snapshot every 100 events

projections:
  checkpoint_interval: 100
  batch_size: 500
  
serialization:
  format: json  # json, protobuf, msgpack
  
logging:
  level: info
```

### `go-mink generate`

Generate boilerplate code.

```bash
# Generate aggregate
$ go-mink generate aggregate Order
? Events for Order aggregate: OrderCreated, ItemAdded, ItemRemoved, OrderShipped

✓ Created internal/aggregates/order.go
✓ Created internal/events/order_events.go
✓ Created internal/aggregates/order_test.go

# Generate projection  
$ go-mink generate projection OrderSummary --events OrderCreated,ItemAdded,OrderShipped
? Projection type: Inline (same transaction)
? Read model fields: ID, CustomerID, Status, ItemCount, TotalAmount, CreatedAt

✓ Created internal/projections/order_summary.go
✓ Created internal/readmodels/order_summary.go

# Generate event
$ go-mink generate event PaymentReceived --aggregate Order
? Event fields: OrderID (string), Amount (float64), PaymentMethod (string)

✓ Updated internal/events/order_events.go
```

Generated aggregate code:

```go
// internal/aggregates/order.go
package aggregates

import (
    "github.com/AshkanYarmoradi/go-mink"
    "myapp/internal/events"
)

type Order struct {
    go-mink.AggregateBase
    
    // State
    CustomerID string
    Items      []OrderItem
    Status     string
    Total      float64
}

func NewOrder(id string) *Order {
    o := &Order{}
    o.SetID(id)
    return o
}

// Commands
func (o *Order) Create(customerID string) error {
    if o.Version() > 0 {
        return errors.New("order already exists")
    }
    
    o.Apply(events.OrderCreated{
        OrderID:    o.ID(),
        CustomerID: customerID,
        CreatedAt:  time.Now(),
    })
    return nil
}

func (o *Order) AddItem(sku string, quantity int, price float64) error {
    if o.Status == "Shipped" {
        return errors.New("cannot modify shipped order")
    }
    
    o.Apply(events.ItemAdded{
        OrderID:  o.ID(),
        SKU:      sku,
        Quantity: quantity,
        Price:    price,
    })
    return nil
}

// Event handlers
func (o *Order) ApplyEvent(event go-mink.Event) error {
    switch e := event.(type) {
    case events.OrderCreated:
        o.CustomerID = e.CustomerID
        o.Status = "Created"
        
    case events.ItemAdded:
        o.Items = append(o.Items, OrderItem{
            SKU:      e.SKU,
            Quantity: e.Quantity,
            Price:    e.Price,
        })
        o.Total += e.Price * float64(e.Quantity)
        
    case events.OrderShipped:
        o.Status = "Shipped"
    }
    return nil
}
```

### `go-mink migrate`

Database schema management.

```bash
# Create migration
$ go-mink migrate create add_customer_index

✓ Created migrations/20251227120000_add_customer_index.sql

# Run migrations
$ go-mink migrate up

Applying migrations...
✓ 20251227100000_initial_schema.sql
✓ 20251227110000_add_projections.sql  
✓ 20251227120000_add_customer_index.sql

All migrations applied successfully.

# Rollback
$ go-mink migrate down --steps 1

Rolling back 1 migration...
✓ Rolled back 20251227120000_add_customer_index.sql

# Status
$ go-mink migrate status

Migration Status:
┌────────────────────────────────────────┬─────────┬─────────────────────┐
│ Migration                              │ Status  │ Applied At          │
├────────────────────────────────────────┼─────────┼─────────────────────┤
│ 20251227100000_initial_schema.sql      │ Applied │ 2025-12-27 10:00:00 │
│ 20251227110000_add_projections.sql     │ Applied │ 2025-12-27 11:00:00 │
│ 20251227120000_add_customer_index.sql  │ Pending │ -                   │
└────────────────────────────────────────┴─────────┴─────────────────────┘
```

### `go-mink projection`

Manage projections.

```bash
# List projections
$ go-mink projection list

Projections:
┌──────────────────┬──────────┬────────────┬─────────────┬──────────────┐
│ Name             │ Type     │ Status     │ Position    │ Lag          │
├──────────────────┼──────────┼────────────┼─────────────┼──────────────┤
│ OrderSummary     │ Inline   │ Active     │ N/A         │ N/A          │
│ OrderAnalytics   │ Async    │ Running    │ 15,234      │ 12 events    │
│ CustomerStats    │ Async    │ Paused     │ 14,890      │ 356 events   │
└──────────────────┴──────────┴────────────┴─────────────┴──────────────┘

# Rebuild projection
$ go-mink projection rebuild OrderAnalytics

Rebuilding OrderAnalytics projection...
Progress: [████████████████████████████████████████] 100% (15,246/15,246)

✓ Rebuild complete in 45.2s

# Pause/Resume
$ go-mink projection pause CustomerStats
$ go-mink projection resume CustomerStats

# Reset checkpoint
$ go-mink projection reset OrderAnalytics --position 10000
```

### `go-mink stream`

Inspect event streams.

```bash
# List streams
$ go-mink stream list --category Order

Streams (category: Order):
┌────────────────┬─────────┬─────────────────────┬──────────────────────┐
│ Stream ID      │ Version │ Created             │ Last Event           │
├────────────────┼─────────┼─────────────────────┼──────────────────────┤
│ Order-abc123   │ 15      │ 2025-12-20 10:00:00 │ 2025-12-27 09:30:00  │
│ Order-def456   │ 8       │ 2025-12-21 14:30:00 │ 2025-12-26 16:45:00  │
│ Order-ghi789   │ 3       │ 2025-12-27 08:00:00 │ 2025-12-27 08:15:00  │
└────────────────┴─────────┴─────────────────────┴──────────────────────┘

# View stream events
$ go-mink stream events Order-abc123

Events in Order-abc123:
┌─────┬────────────────┬─────────────────────┬──────────────────────────┐
│ Ver │ Type           │ Timestamp           │ Data (preview)           │
├─────┼────────────────┼─────────────────────┼──────────────────────────┤
│ 1   │ OrderCreated   │ 2025-12-20 10:00:00 │ {"customerId":"cust-1"}  │
│ 2   │ ItemAdded      │ 2025-12-20 10:01:00 │ {"sku":"WIDGET-01",...}  │
│ 3   │ ItemAdded      │ 2025-12-20 10:02:00 │ {"sku":"GADGET-02",...}  │
│ ... │ ...            │ ...                 │ ...                      │
└─────┴────────────────┴─────────────────────┴──────────────────────────┘

# Export stream
$ go-mink stream export Order-abc123 --format json > order_abc123.json

# Replay events (dry-run projections)
$ go-mink stream replay Order-abc123 --projection OrderSummary --dry-run
```

### `go-mink diagnose`

Health checks and diagnostics.

```bash
$ go-mink diagnose

Running diagnostics...

Database Connectivity:
  ✓ Event store (PostgreSQL) - Connected (latency: 2ms)
  ✓ Read models (PostgreSQL) - Connected (latency: 3ms)
  ✓ Snapshots (Redis) - Connected (latency: 1ms)

Schema Validation:
  ✓ Events table - OK
  ✓ Streams table - OK
  ✓ Checkpoints table - OK
  ⚠ Missing index on events.timestamp (recommended for time-based queries)

Projection Health:
  ✓ OrderSummary - Healthy
  ✓ OrderAnalytics - Healthy (lag: 12 events)
  ⚠ CustomerStats - High lag (356 events behind)

Event Store Statistics:
  Total streams: 12,456
  Total events: 1,234,567
  Events/day (avg): 5,432
  Largest stream: Order-abc123 (2,345 events)

Recommendations:
  1. Consider adding index: CREATE INDEX idx_events_timestamp ON events(timestamp)
  2. Investigate CustomerStats projection lag
  3. Consider snapshotting Order-abc123 (>100 events)
```

## Integration with Go Generate

```go
//go:generate go-mink generate aggregate Order
//go:generate go-mink generate projection OrderSummary

package myapp
```

## Configuration Environments

```bash
# Use different configs per environment
$ go-mink --config go-mink.production.yaml migrate up

# Or via environment variable
$ go-mink_CONFIG=go-mink.staging.yaml go-mink projection list
```

---

Next: [API Design →](07-api-design.md)
