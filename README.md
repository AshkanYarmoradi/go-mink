# go-mink 🦫

**A Comprehensive Event Sourcing & CQRS Toolkit for Go**

[![Go Reference](https://pkg.go.dev/badge/github.com/AshkanYarmoradi/go-mink.svg)](https://pkg.go.dev/github.com/AshkanYarmoradi/go-mink)
[![Go Report Card](https://goreportcard.com/badge/github.com/AshkanYarmoradi/go-mink)](https://goreportcard.com/report/github.com/AshkanYarmoradi/go-mink)
[![Build Status](https://github.com/AshkanYarmoradi/go-mink/actions/workflows/test.yml/badge.svg)](https://github.com/AshkanYarmoradi/go-mink/actions/workflows/test.yml)
[![codecov](https://codecov.io/gh/AshkanYarmoradi/go-mink/graph/badge.svg?token=ZCB3IDSI2Q)](https://codecov.io/gh/AshkanYarmoradi/go-mink)
[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)
[![Go Version](https://img.shields.io/badge/Go-1.22+-00ADD8?style=flat&logo=go)](https://go.dev/)

---

## 🚀 Current Status: v0.3.0 (Phase 3 Complete)

Phase 3 (Projections & Read Models) is complete with:
- ✅ Projection Engine (inline, async, live projections)
- ✅ Read Model Repository with fluent query builder
- ✅ Event subscriptions (catch-up, polling, filtered)
- ✅ Checkpoint management for async projections
- ✅ Projection rebuilding (single and parallel)
- ✅ Retry policies with exponential backoff
- ✅ 95%+ test coverage

**Previous phases included:**
- ✅ Event Store with optimistic concurrency (v0.1.0)
- ✅ PostgreSQL & In-Memory adapters (v0.1.0)
- ✅ Command Bus with middleware pipeline (v0.2.0)
- ✅ Idempotency, Validation, Correlation tracking (v0.2.0)

---

## What is go-mink?

go-mink is a batteries-included Event Sourcing and CQRS (Command Query Responsibility Segregation) library for Go. Inspired by [MartenDB](https://martendb.io/) for .NET, go-mink brings the same developer-friendly experience to the Go ecosystem.

> **Why "go-mink"?** Just as Marten (the animal) inspired the .NET library name, we chose go-mink - another member of the Mustelidae family - for our Go counterpart.

## Vision

**"Make Event Sourcing in Go as simple as using a traditional ORM"**

go-mink aims to eliminate the boilerplate code typically required when implementing Event Sourcing in Go, while providing a pluggable architecture that allows teams to choose their preferred storage backends.

## Key Features

| Feature | Status | Description |
|---------|--------|-------------|
| 🎯 **Event Store** | ✅ v0.1.0 | Append-only event storage with optimistic concurrency |
| 🔌 **PostgreSQL Adapter** | ✅ v0.1.0 | Production-ready PostgreSQL support |
| 🧪 **Memory Adapter** | ✅ v0.1.0 | In-memory adapter for testing |
| 🧱 **Aggregates** | ✅ v0.1.0 | Base implementation with event application |
| 📋 **Command Bus** | ✅ v0.2.0 | Full CQRS with command handlers and middleware |
| 🔐 **Idempotency** | ✅ v0.2.0 | Prevent duplicate command processing |
| 🔗 **Correlation/Causation** | ✅ v0.2.0 | Distributed tracing support |
| 📖 **Projections** | ✅ v0.3.0 | Inline, async, and live projection engine |
| 📊 **Read Models** | ✅ v0.3.0 | Generic repository with query builder |
| 📡 **Subscriptions** | ✅ v0.3.0 | Catch-up and polling event subscriptions |
| 🛠️ **CLI Tool** | 🔜 v0.4.0 | Code generation, migrations, and diagnostics |
| 🔐 **Security** | 🔜 v0.5.0 | Field-level encryption and GDPR compliance |
| 🔄 **Sagas** | 🔜 v0.5.0 | Process manager for long-running workflows |
| 📤 **Outbox Pattern** | 🔜 v0.5.0 | Reliable event publishing to external systems |

## Quick Example

```go
package main

import (
    "context"
    
    "github.com/AshkanYarmoradi/go-mink"
    "github.com/AshkanYarmoradi/go-mink/adapters/postgres"
)

func main() {
    ctx := context.Background()
    
    // Initialize PostgreSQL adapter
    adapter, _ := postgres.NewAdapter("postgres://localhost/mydb")
    defer adapter.Close()
    
    // Create event store
    store := mink.New(adapter)
    
    // Create and populate an aggregate
    order := NewOrder("order-123")
    order.Create("customer-456")
    order.AddItem("SKU-001", 2, 29.99)
    
    // Save aggregate (events are persisted)
    store.SaveAggregate(ctx, order)
    
    // Load aggregate (events are replayed)
    loaded := NewOrder("order-123")
    store.LoadAggregate(ctx, loaded)
}
```

## CQRS with Command Bus (v0.2.0)

```go
package main

import (
    "context"
    
    "github.com/AshkanYarmoradi/go-mink"
    "github.com/AshkanYarmoradi/go-mink/adapters/memory"
)

// Define a command
type CreateOrder struct {
    mink.CommandBase
    CustomerID string `json:"customerId"`
}

func (c CreateOrder) CommandType() string { return "CreateOrder" }
func (c CreateOrder) Validate() error {
    if c.CustomerID == "" {
        return mink.NewValidationError("CreateOrder", "CustomerID", "required")
    }
    return nil
}

func main() {
    ctx := context.Background()
    
    // Create command bus with middleware
    bus := mink.NewCommandBus()
    bus.Use(mink.ValidationMiddleware())
    bus.Use(mink.RecoveryMiddleware())
    bus.Use(mink.CorrelationIDMiddleware(nil))
    
    // Add idempotency (prevents duplicate processing)
    idempotencyStore := memory.NewIdempotencyStore()
    bus.Use(mink.IdempotencyMiddleware(mink.DefaultIdempotencyConfig(idempotencyStore)))
    
    // Register command handler
    bus.RegisterFunc("CreateOrder", func(ctx context.Context, cmd mink.Command) (mink.CommandResult, error) {
        c := cmd.(CreateOrder)
        // Process command...
        return mink.NewSuccessResult("order-123", 1), nil
    })
    
    // Dispatch command
    result, err := bus.Dispatch(ctx, CreateOrder{CustomerID: "cust-456"})
    if err != nil {
        panic(err)
    }
    
    fmt.Printf("Created order: %s (version %d)\n", result.AggregateID, result.Version)
}
```

## Projections & Read Models (v0.3.0)

```go
package main

import (
    "context"
    "time"

    "github.com/AshkanYarmoradi/go-mink"
    "github.com/AshkanYarmoradi/go-mink/adapters/memory"
)

// Define a read model
type OrderSummary struct {
    OrderID    string
    CustomerID string
    Status     string
    ItemCount  int
    Total      float64
}

// Define a projection
type OrderSummaryProjection struct {
    mink.ProjectionBase
    repo *mink.InMemoryRepository[OrderSummary]
}

func (p *OrderSummaryProjection) Apply(ctx context.Context, event mink.StoredEvent) error {
    // Transform events into read model updates
    // ...
    return nil
}

func main() {
    ctx := context.Background()

    // Create projection engine
    checkpointStore := memory.NewCheckpointStore()
    engine := mink.NewProjectionEngine(store,
        mink.WithCheckpointStore(checkpointStore),
    )

    // Register projections
    repo := mink.NewInMemoryRepository[OrderSummary](func(o *OrderSummary) string {
        return o.OrderID
    })
    engine.RegisterInline(&OrderSummaryProjection{repo: repo})

    // Start engine
    engine.Start(ctx)
    defer engine.Stop(ctx)

    // Query read models with fluent API
    orders, _ := repo.Query(ctx, mink.NewQuery().
        Where("Status", mink.Eq, "Pending").
        OrderByDesc("Total").
        WithLimit(10))

    // Rebuild projections when needed
    rebuilder := mink.NewProjectionRebuilder(store, checkpointStore)
    rebuilder.RebuildInline(ctx, projection, mink.RebuildOptions{BatchSize: 1000})
}
```

## Installation

```bash
go get github.com/AshkanYarmoradi/go-mink
go get github.com/AshkanYarmoradi/go-mink/adapters/postgres
```

## Documentation

| Document | Description |
|----------|-------------|
| [Introduction](docs/introduction.md) | Problem statement and goals |
| [Architecture](docs/architecture.md) | System design and components |
| [Event Store](docs/event-store.md) | Event storage design |
| [Read Models](docs/read-models.md) | Projection system |
| [Adapters](docs/adapters.md) | Database adapter system |
| [CLI](docs/cli.md) | Command-line tooling |
| [API Design](docs/api-design.md) | Public API reference |
| [Roadmap](docs/roadmap.md) | Development phases |
| [Advanced Patterns](docs/advanced-patterns.md) | Commands, Sagas, Outbox |
| [Security](docs/security.md) | Encryption, GDPR, Versioning |
| [Testing](docs/testing.md) | BDD fixtures and test utilities |

## License

Apache License 2.0 - See [LICENSE](LICENSE) for details.

---

**go-mink** - Event Sourcing for Go, Done Right.
