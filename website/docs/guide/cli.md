---
title: CLI
sidebar_position: 10
---

# Mink CLI Tool

The `mink` CLI provides essential tooling for development and operations with event-sourced applications.

---

## Installation

Install the CLI using Go:

```bash
go install github.com/AshkanYarmoradi/go-mink/cmd/mink@latest
```

Or build from source:

```bash
git clone https://github.com/AshkanYarmoradi/go-mink.git
cd go-mink
go build -o mink ./cmd/mink
```

Verify installation:

```bash
mink version
```

---

## Overview

```bash
$ mink --help

Mink - Event Sourcing toolkit for Go

Mink is an Event Sourcing and CQRS toolkit for Go applications.
It provides a complete solution for building event-driven systems.

Quick Start:

  mink init           Initialize a new project
  mink generate       Generate code scaffolding
  mink migrate up     Run database migrations
  mink diagnose       Check your setup

Usage:
  mink [command]

Available Commands:
  init        Initialize a new mink project
  generate    Generate code scaffolding
  migrate     Database migrations
  projection  Manage projections
  stream      Inspect and manage event streams
  diagnose    Run diagnostic checks
  schema      Schema management
  version     Show version information
  help        Help about any command

Flags:
      --no-color   Disable colored output
  -h, --help       Help for mink
```

---

## Commands

### `mink init`

Initialize a new mink project with configuration and directory structure.

```bash
# Interactive mode (default)
$ mink init

Welcome to Mink!

? Project name: minkshop
? Go module path: github.com/mycompany/minkshop
? Database driver:
  > postgres
    memory

Created mink.yaml
Created migrations directory

Next Steps:
  1. Set DATABASE_URL environment variable
  2. Run 'mink migrate up' to create schema
  3. Generate your first aggregate with 'mink generate aggregate'

# Non-interactive mode
$ mink init --name=myapp --module=github.com/me/myapp --driver=postgres --non-interactive

# Initialize in a subdirectory
$ mink init my-project
```

**Flags:**

| Flag | Description |
|------|-------------|
| `--name` | Project name |
| `--module` | Go module path (auto-detected from go.mod) |
| `--driver` | Database driver: `postgres` or `memory` |
| `--non-interactive` | Skip interactive prompts |

**Generated `mink.yaml`:**

```yaml
version: "1"
project:
  name: minkshop
  module: github.com/mycompany/minkshop

database:
  driver: postgres
  url: ${DATABASE_URL}
  migrations_dir: migrations

eventstore:
  table_name: mink_events
  schema: mink

generation:
  aggregate_package: internal/domain
  event_package: internal/events
  projection_package: internal/projections
  command_package: internal/commands
```

---

### `mink generate`

Generate boilerplate code for aggregates, events, projections, and commands.

**Aliases:** `gen`, `g`

#### Generate Aggregate

```bash
# Interactive mode
$ mink generate aggregate Order
? Events (comma-separated): Created,ItemAdded,Shipped

Created internal/domain/order.go
Created internal/events/order_events.go
Created internal/domain/order_test.go

# With flags
$ mink generate aggregate Order --events Created,ItemAdded,Shipped --non-interactive
```

**Generated aggregate code:**

```go
// internal/domain/order.go
package domain

import (
    "errors"
    "github.com/AshkanYarmoradi/go-mink"
)

type Order struct {
    mink.AggregateBase
    // Add your aggregate state here
}

func NewOrder(id string) *Order {
    agg := &Order{}
    agg.SetID(id)
    agg.SetType("Order")
    return agg
}

func (a *Order) ApplyEvent(event interface{}) error {
    switch e := event.(type) {
    case Created:
        return a.applyCreated(e)
    case *Created:
        return a.applyCreated(*e)
    case ItemAdded:
        return a.applyItemAdded(e)
    // ... other events
    default:
        return errors.New("unknown event type")
    }
}

func (a *Order) applyCreated(e Created) error {
    // TODO: Apply the event to aggregate state
    return nil
}

// ... other apply methods
```

#### Generate Event

```bash
$ mink generate event PaymentReceived --aggregate Order

Created internal/events/paymentreceived.go
```

**Flags:**

| Flag | Short | Description |
|------|-------|-------------|
| `--aggregate` | `-a` | Aggregate this event belongs to |
| `--non-interactive` | | Skip prompts |

#### Generate Projection

```bash
$ mink generate projection OrderSummary --events OrderCreated,ItemAdded,OrderShipped

Created internal/projections/ordersummary.go
Created internal/projections/ordersummary_test.go
```

**Generated projection:**

```go
// internal/projections/ordersummary.go
package projections

import (
    "context"
    "encoding/json"
    "github.com/AshkanYarmoradi/go-mink"
)

type OrderSummaryProjection struct {
    // Add dependencies here
}

func NewOrderSummaryProjection() *OrderSummaryProjection {
    return &OrderSummaryProjection{}
}

func (p *OrderSummaryProjection) Name() string {
    return "OrderSummary"
}

func (p *OrderSummaryProjection) HandledEvents() []string {
    return []string{
        "OrderCreated",
        "ItemAdded",
        "OrderShipped",
    }
}

func (p *OrderSummaryProjection) Apply(ctx context.Context, event mink.StoredEvent) error {
    switch event.Type {
    case "OrderCreated":
        return p.handleOrderCreated(ctx, event)
    case "ItemAdded":
        return p.handleItemAdded(ctx, event)
    case "OrderShipped":
        return p.handleOrderShipped(ctx, event)
    }
    return nil
}
```

#### Generate Command

```bash
$ mink generate command CreateOrder --aggregate Order

Created internal/commands/createorder.go
```

**Generated command:**

```go
// internal/commands/createorder.go
package commands

import (
    "context"
    "errors"
    "github.com/AshkanYarmoradi/go-mink"
)

type CreateOrder struct {
    OrderID string
    // Add command fields here
}

func (c CreateOrder) AggregateID() string {
    return c.OrderID
}

func (c CreateOrder) CommandType() string {
    return "CreateOrder"
}

func (c CreateOrder) Validate() error {
    if c.OrderID == "" {
        return errors.New("order_id is required")
    }
    return nil
}

type CreateOrderHandler struct {
    store *mink.EventStore
}

func NewCreateOrderHandler(store *mink.EventStore) *CreateOrderHandler {
    return &CreateOrderHandler{store: store}
}

func (h *CreateOrderHandler) Handle(ctx context.Context, cmd CreateOrder) error {
    // TODO: Implement command handling
    return nil
}
```

---

### `mink migrate`

Database schema migration management.

#### Create Migration

```bash
$ mink migrate create add_customer_index

Created migrations/20260107120000_add_customer_index.sql
Created migrations/20260107120000_add_customer_index.down.sql
```

**With SQL content:**

```bash
$ mink migrate create add_index --sql "CREATE INDEX idx_customer ON orders(customer_id);"
```

#### Run Migrations

```bash
$ mink migrate up

Running migrations...

Applying migrations:
  20260107100000_initial_schema.sql
  20260107110000_add_projections.sql

All migrations applied successfully.

# Apply specific number of migrations
$ mink migrate up --steps 1
```

#### Rollback Migrations

```bash
$ mink migrate down

Rolling back 1 migration...
  Rolled back 20260107110000_add_projections.sql

# Rollback multiple
$ mink migrate down --steps 2
```

#### Check Status

```bash
$ mink migrate status

Migration Status

┌────────────────────────────────────────┬─────────┬─────────────────────┐
│ Migration                              │ Status  │ Applied At          │
├────────────────────────────────────────┼─────────┼─────────────────────┤
│ 20260107100000_initial_schema.sql      │ Applied │ 2026-01-07 10:00:00 │
│ 20260107110000_add_projections.sql     │ Applied │ 2026-01-07 11:00:00 │
│ 20260107120000_add_customer_index.sql  │ Pending │ -                   │
└────────────────────────────────────────┴─────────┴─────────────────────┘

2 applied, 1 pending
```

---

### `mink projection`

Manage event projections.

**Aliases:** `proj`

#### List Projections

```bash
$ mink projection list

Projections

┌──────────────────┬──────────┬───────────┬─────────────────────┐
│ Name             │ Position │ Status    │ Last Updated        │
├──────────────────┼──────────┼───────────┼─────────────────────┤
│ OrderSummary     │ 15,234   │ Active    │ 2026-01-07 09:30:00 │
│ CustomerStats    │ 14,890   │ Paused    │ 2026-01-06 16:45:00 │
└──────────────────┴──────────┴───────────┴─────────────────────┘
```

#### View Projection Status

```bash
$ mink projection status OrderSummary

Projection: OrderSummary

  Name:        OrderSummary
  Position:    15,234 / 15,246
  Status:      Active
  Last Update: 2026-01-07T09:30:00Z

  Progress:
  [████████████████████████████████████████] 99.9%
```

#### Rebuild Projection

```bash
$ mink projection rebuild OrderSummary

? Rebuild projection 'OrderSummary'? This will reset the checkpoint and replay all events.
> Yes

Rebuilding projection 'OrderSummary'...

Checkpoint reset to 0
Projection will rebuild on next run

# Skip confirmation
$ mink projection rebuild OrderSummary --yes
```

#### Pause/Resume Projections

```bash
# Pause
$ mink projection pause CustomerStats
Paused projection 'CustomerStats'

# Resume
$ mink projection resume CustomerStats
Resumed projection 'CustomerStats'
```

---

### `mink stream`

Inspect and manage event streams.

#### List Streams

```bash
$ mink stream list

Event Streams

┌────────────────┬─────────┬───────────────┬─────────────────────┐
│ Stream ID      │ Events  │ Last Event    │ Updated             │
├────────────────┼─────────┼───────────────┼─────────────────────┤
│ Order-abc123   │ 15      │ OrderShipped  │ 2026-01-07 09:30:00 │
│ Order-def456   │ 8       │ ItemAdded     │ 2026-01-06 16:45:00 │
│ Cart-xyz789    │ 3       │ ItemRemoved   │ 2026-01-07 08:15:00 │
└────────────────┴─────────┴───────────────┴─────────────────────┘

# Filter by prefix
$ mink stream list --prefix Order-

# Limit results
$ mink stream list --limit 10
```

#### View Stream Events

```bash
$ mink stream events Order-abc123

Events in Order-abc123

┌─────┬────────────────┬─────────────────────┬──────────────────────────┐
│ Ver │ Type           │ Timestamp           │ Data                     │
├─────┼────────────────┼─────────────────────┼──────────────────────────┤
│ 1   │ OrderCreated   │ 2026-01-05 10:00:00 │ {"customerId":"cust-1"}  │
│ 2   │ ItemAdded      │ 2026-01-05 10:01:00 │ {"sku":"WIDGET-01"}      │
│ 3   │ ItemAdded      │ 2026-01-05 10:02:00 │ {"sku":"GADGET-02"}      │
└─────┴────────────────┴─────────────────────┴──────────────────────────┘

# Limit events
$ mink stream events Order-abc123 --max-events 10

# Start from version
$ mink stream events Order-abc123 --from 5
```

#### Export Stream

```bash
$ mink stream export Order-abc123 --output order_backup.json

Exported 15 events to order_backup.json
```

#### Stream Statistics

```bash
$ mink stream stats

Event Store Statistics

Total Streams:     12,456
Total Events:      1,234,567
Events Today:      5,432
Average Events/Stream: 99

Top Event Types:
  OrderCreated:    45,234 (12.3%)
  ItemAdded:       123,456 (33.5%)
  OrderShipped:    34,567 (9.4%)
```

---

### `mink diagnose`

Run diagnostic checks on your mink setup.

**Aliases:** `diag`, `doctor`

```bash
$ mink diagnose

Mink

Running Diagnostics

  Checking Go Version... OK
    go1.22.0
  Checking Configuration... OK
    Project: minkshop, Driver: postgres
  Checking Database Connection... OK
    Connected to PostgreSQL 16.1
  Checking Event Store Schema... OK
    All tables present (events, streams, snapshots, checkpoints)
  Checking Projections... OK
    2 projections healthy
  Checking System Resources... OK
    Memory: 45.2 MB used, 128.0 MB total

──────────────────────────────────────────────────────

All checks passed! Your mink setup is healthy.
```

**When issues are detected:**

```bash
$ mink diagnose

  Checking Database Connection... WARNING
    DATABASE_URL not set

Recommendations:
  -> Set DATABASE_URL environment variable
```

---

### `mink schema`

Schema management commands.

#### Generate Schema

```bash
# Output to file
$ mink schema generate --output schema.sql

Generated schema to schema.sql

# Output to stdout
$ mink schema print
```

**Generated schema:**

```sql
-- Mink Event Store Schema
-- Generated for PostgreSQL

CREATE SCHEMA IF NOT EXISTS mink;

CREATE TABLE IF NOT EXISTS mink.events (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    stream_id VARCHAR(255) NOT NULL,
    version BIGINT NOT NULL,
    type VARCHAR(255) NOT NULL,
    data JSONB NOT NULL,
    metadata JSONB DEFAULT '{}',
    global_position BIGSERIAL,
    timestamp TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE(stream_id, version)
);

CREATE INDEX idx_events_stream_id ON mink.events(stream_id);
CREATE INDEX idx_events_type ON mink.events(type);
CREATE INDEX idx_events_global_position ON mink.events(global_position);
CREATE INDEX idx_events_timestamp ON mink.events(timestamp);

-- ... additional tables for streams, snapshots, checkpoints
```

---

### `mink version`

Display version information with a beautiful animated display.

```bash
$ mink version

Mink

┌───────────┬─────────────────────────────┐
│ Version   │ v1.0.0                      │
│ Commit    │ abc123def                   │
│ Built     │ 2026-01-07                  │
│ Go        │ go1.22.0                    │
│ OS/Arch   │ darwin/arm64                │
└───────────┴─────────────────────────────┘
```

---

## Configuration

### Configuration File

The CLI looks for `mink.yaml` in the current directory or parent directories.

```yaml
version: "1"

project:
  name: minkshop
  module: github.com/mycompany/minkshop

database:
  driver: postgres           # postgres or memory
  url: ${DATABASE_URL}       # Environment variable expansion
  migrations_dir: migrations

eventstore:
  table_name: mink_events
  schema: mink

generation:
  aggregate_package: internal/domain
  event_package: internal/events
  projection_package: internal/projections
  command_package: internal/commands
```

### Environment Variables

| Variable | Description |
|----------|-------------|
| `DATABASE_URL` | PostgreSQL connection string |
| `MINK_CONFIG` | Path to config file (default: `./mink.yaml`) |

**Example DATABASE_URL:**
```bash
export DATABASE_URL="postgres://user:password@localhost:5432/mydb?sslmode=disable"
```

---

## Testing

The CLI tool has comprehensive test coverage (84.9%):

| Category | Tests | Description |
|----------|-------|-------------|
| Unit Tests | ~200 | Core logic, helpers, validation |
| Integration Tests | 67 | PostgreSQL operations |
| E2E Tests | 4 | Complete workflows |

### Running Tests

```bash
# Unit tests only
go test -short ./cli/...

# All tests (requires PostgreSQL)
docker-compose -f docker-compose.test.yml up -d
go test ./cli/...

# With coverage
go test -cover ./cli/...
```

---

## Tips & Best Practices

### Use Non-Interactive Mode for CI/CD

```bash
mink init --name=myapp --driver=postgres --non-interactive
mink generate aggregate Order --events Created,Shipped --non-interactive
mink migrate up
```

### Generate with Go Generate

Add to your Go files:

```go
//go:generate mink generate aggregate Order --events Created,Shipped --non-interactive
//go:generate mink generate projection OrderSummary --events Created,Shipped --non-interactive

package domain
```

Then run:

```bash
go generate ./...
```

---

## Troubleshooting

### "DATABASE_URL not set"

```bash
export DATABASE_URL="postgres://user:pass@localhost:5432/mydb?sslmode=disable"
```

### "mink.yaml not found"

Run `mink init` to create the configuration file, or check you're in the correct directory.

### "Permission denied" on migrations

Ensure your database user has CREATE TABLE permissions:

```sql
GRANT ALL PRIVILEGES ON DATABASE mydb TO myuser;
GRANT ALL PRIVILEGES ON SCHEMA mink TO myuser;
```

---

Next: [Roadmap →](/docs/roadmap)
