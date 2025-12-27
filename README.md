# go-mink 🦫

**A Comprehensive Event Sourcing & CQRS Toolkit for Go**

[![Go Version](https://img.shields.io/badge/Go-1.21+-00ADD8?style=flat&logo=go)](https://go.dev/)
[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)

---

## What is go-mink?

go-mink is a batteries-included Event Sourcing and CQRS (Command Query Responsibility Segregation) library for Go. Inspired by [MartenDB](https://martendb.io/) for .NET, go-mink brings the same developer-friendly experience to the Go ecosystem.

> **Why "go-mink"?** Just as Marten (the animal) inspired the .NET library name, we chose go-mink - another member of the Mustelidae family - for our Go counterpart.

## Vision

**"Make Event Sourcing in Go as simple as using a traditional ORM"**

go-mink aims to eliminate the boilerplate code typically required when implementing Event Sourcing in Go, while providing a pluggable architecture that allows teams to choose their preferred storage backends.

## Key Features

| Feature | Description |
|---------|-------------|
| 🎯 **Event Store** | Append-only event storage with optimistic concurrency |
| 📖 **Read Models** | Automatic projection management with multiple strategies |
| 🔌 **Pluggable Adapters** | PostgreSQL, MongoDB, Redis, and more |
| 🛠️ **CLI Tool** | Code generation, migrations, and diagnostics |
| 🏢 **Multi-tenancy** | Built-in tenant isolation strategies |
| ⚡ **High Performance** | Leveraging Go's concurrency primitives |
| 🧪 **Testing Utilities** | In-memory adapters and test helpers |

## Quick Example

```go
package main

import (
    "github.com/AshkanYarmoradi/go-mink"
    "github.com/AshkanYarmoradi/go-mink/adapters/postgres"
)

func main() {
    // Initialize with PostgreSQL
    store, _ := go-mink.NewEventStore(
        postgres.NewAdapter("postgres://localhost/mydb"),
    )

    // Append events
    store.Append(ctx, "order-123", []go-mink.Event{
        OrderCreated{OrderID: "123", CustomerID: "456"},
        ItemAdded{SKU: "WIDGET-01", Quantity: 2},
    })

    // Build read models automatically
    store.RegisterProjection(&OrderSummaryProjection{})
}
```

## Documentation

| Document | Description |
|----------|-------------|
| [Introduction](docs/01-introduction.md) | Problem statement and goals |
| [Architecture](docs/02-architecture.md) | System design and components |
| [Event Store](docs/03-event-store.md) | Event storage design |
| [Read Models](docs/04-read-models.md) | Projection system |
| [Adapters](docs/05-adapters.md) | Database adapter system |
| [CLI](docs/06-cli.md) | Command-line tooling |
| [API Design](docs/07-api-design.md) | Public API reference |
| [Roadmap](docs/08-roadmap.md) | Development phases |

## License

Apache License 2.0 - See [LICENSE](LICENSE) for details.

---

**go-mink** - Event Sourcing for Go, Done Right.
