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
go install go-mink.dev/cmd/mink@latest
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
  events      Query the global event feed by type, stream, or category
  diagnose    Run diagnostic checks
  schema      Schema management
  gdpr        GDPR data-governance operations
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
  schema: mink
  migrations_dir: migrations

event_store:
  table_name: events
  snapshot_table_name: snapshots
  outbox_table_name: mink_outbox

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
    "go-mink.dev"
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
    "go-mink.dev"
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
    "go-mink.dev"
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

### `mink events`

Query the **global** event feed by indexed axis — event type, stream id, or stream
category — starting after a global position. Where `mink stream events` walks a *single*
stream by version, `mink events` scans *across all* streams by global position.

This is an **introspection / ops / migration** tool over the raw log (audit browsers,
backfill scanners, diagnostics) — not an application read path. For unindexed criteria (a
tenant in metadata, a payload field) or application queries, project a read model instead.

```bash
# Every OrderPlaced across all streams, from the start
$ mink events --type OrderPlaced

Event Feed

┌──────────┬──────────────┬───────────────┬─────────────────────┐
│ Position │ Stream       │ Type          │ Time                │
├──────────┼──────────────┼───────────────┼─────────────────────┤
│ 42       │ Order-abc123 │ OrderPlaced   │ 2026-01-05 10:00:00 │
│ 87       │ Order-def456 │ OrderPlaced   │ 2026-01-06 16:45:00 │
└──────────┴──────────────┴───────────────┴─────────────────────┘

Showing 2 event(s)

# Either type — repeatable or comma-separated (an OR set)
$ mink events --type OrderPlaced --type OrderShipped
$ mink events --type OrderPlaced,OrderShipped

# Exact stream set
$ mink events --stream Order-abc123 --stream Order-def456

# A whole category (Order-*), after position 1000, capped at 100
$ mink events --category Order --from 1000 --limit 100

# Machine-readable output for scripts / CSV pipelines (jq-friendly)
$ mink events --type OrderPlaced --json
```

**Flags**

| Flag | Short | Description |
| --- | --- | --- |
| `--type` | `-t` | Event type(s); repeatable or comma-separated (`event_type IN …`) |
| `--stream` | `-s` | Exact stream id(s); repeatable or comma-separated (`stream_id IN …`) |
| `--category` | `-c` | Stream category (`stream_id LIKE '<category>-%'`) |
| `--from` | `-f` | Start **after** this global position (exclusive; default `0`) |
| `--limit` | `-n` | Maximum events to return (default `50`; `0` = adapter cap) |
| `--json` |  | Emit a JSON array instead of a table |

Axes **AND**-compose; a multi-valued axis is an **OR** set; an empty filter walks the whole
feed (like an unfiltered load-from-position). Results are ordered by ascending global
position, exclusive of `--from`, and bounded by `--limit`.

:::note Indexed-only by design
The filter covers only columns the event store indexes (`event_type`, `stream_id` /
category). There is deliberately **no** metadata/tenant or payload axis — those belong in a
read-model projection, keeping this a bounded introspection primitive rather than an ad-hoc
query engine over the event log. Field-encrypted `data` is shown as stored (the CLI holds no
keys), so use `--json` and decrypt downstream if needed.
:::

---

### `mink diagnose`

Run diagnostic checks on your mink setup.

**Aliases:** `diag`, `doctor`

```bash
$ mink diagnose

Mink

Running Diagnostics

  Checking Go Version... OK
    go1.25.0
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
-- Mink Event Store Schema (PostgreSQL)
-- Generated for: minkshop

CREATE SCHEMA IF NOT EXISTS "mink";

-- Includes streams, events, snapshots, checkpoints, migrations,
-- outbox, and idempotency tables using schema-qualified names.
```

---

### `mink gdpr`

Subject-centric **GDPR data-governance** operations over the event store. These verbs are
**read-only**: the CLI operates through the diagnostic adapter and deliberately does **not**
hold your application's encryption keys, so it produces auditable *plans and reports* —
actual key revocation runs from your application via `mink.NewDataEraser(...).Erase` and
`mink.NewRetentionManager(...).Apply`. See [GDPR & Data Governance](/docs/security).

Requires a `mink.yaml` (it uses the configured store — `postgres` or `memory`).

#### Discover a subject's footprint

Resolve every stream and event tagged for a data subject, plus the distinct encryption
keys protecting them. This is the read-only erasure preview.

```bash
$ mink gdpr discover user-123

🗄️  Subject footprint: user-123

  Streams:         2
  Tagged events:   7
  Encryption keys: 1

┌────────────────┬───────────────┐
│ Stream         │ Tagged events │
├────────────────┼───────────────┤
│ Order-o1       │ 3             │
│ User-user-123  │ 4             │
└────────────────┴───────────────┘

🔑 Encryption keys
  • tenant-A
```

If tagging was not applied uniformly (legacy untagged events exist), the footprint is
reported as **PARTIAL** — treat it as incomplete.

#### Verify erasure readiness

Classify a subject's tagged events by encryption posture: encrypted (crypto-shreddable)
vs. **residual cleartext** (written before encryption was enabled — must be handled on the
read side).

```bash
$ mink gdpr verify user-123

🔒 Erasure readiness: user-123

  Tagged events:          7
  Encrypted (shreddable): 7
  Cleartext (residual):   0
  Encryption keys:        1

✓ All tagged events are encrypted — the subject is fully crypto-shreddable
```

#### Print an erasure plan

Resolve the footprint and list the encryption keys that must be revoked. The CLI does not
revoke them (it holds no keys) — run the erasure from your app or your KMS/Vault console.

```bash
$ mink gdpr erase user-123

🗄️  Subject footprint: user-123
  ...

🔑 Erasure plan — revoke these keys
  → tenant-A

ℹ Execute via mink.NewDataEraser(store, ...).Erase — the CLI does not hold your keys
```

#### Preview a retention policy (dry-run)

Report how many events a retention policy would crypto-shred, without changing anything.

```bash
$ mink gdpr retain --category Customer --max-age 8760h

📊 Retention preview (dry-run)

  Scanned:  1240 events
  Matched:  38 events

⚠ 38 event(s) would be crypto-shredded — run RetentionManager.Apply with your
  encryption provider to enforce
```

**Flags for `retain`** (at least one matcher is required):

| Flag | Description |
|------|-------------|
| `--prefix` | Match stream-id prefix |
| `--category` | Match stream category (text before the first `-`) |
| `--tenant` | Match metadata tenant id |
| `--event-type` | Match event type |
| `--max-age` | Match events older than this age (e.g. `8760h`) |

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
│ Go        │ go1.25.0                    │
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
  schema: mink
  migrations_dir: migrations

event_store:
  table_name: events
  snapshot_table_name: snapshots
  outbox_table_name: mink_outbox

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
