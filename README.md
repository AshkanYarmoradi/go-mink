# go-mink 🦫

**A Comprehensive Event Sourcing & CQRS Toolkit for Go**

[![Go Reference](https://pkg.go.dev/badge/github.com/AshkanYarmoradi/go-mink.svg)](https://pkg.go.dev/github.com/AshkanYarmoradi/go-mink)
[![Go Report Card](https://goreportcard.com/badge/github.com/AshkanYarmoradi/go-mink)](https://goreportcard.com/report/github.com/AshkanYarmoradi/go-mink)
[![Build Status](https://github.com/AshkanYarmoradi/go-mink/actions/workflows/test.yml/badge.svg)](https://github.com/AshkanYarmoradi/go-mink/actions/workflows/test.yml)
[![Coverage](https://img.shields.io/badge/coverage-90%25+-brightgreen)](https://github.com/AshkanYarmoradi/go-mink)
[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)
[![Go Version](https://img.shields.io/badge/Go-1.24+-00ADD8?style=flat&logo=go)](https://go.dev/)

---

## 🚀 Current Status: v0.1.0 (Phase 1 Complete)

Phase 1 (Foundation) is complete with:
- ✅ Event Store with optimistic concurrency
- ✅ PostgreSQL adapter (production-ready)
- ✅ In-Memory adapter (for testing)
- ✅ Aggregate base implementation
- ✅ JSON serialization with type registry
- ✅ 90%+ test coverage

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
| 📋 **Command Bus** | 🔜 v0.2.0 | Full CQRS with command handlers and middleware |
| 📖 **Projections** | 🔜 v0.3.0 | Automatic projection management |
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
    store := mink.NewEventStore(adapter)
    
    // Create and populate an aggregate
    order := NewOrder("order-123")
    order.Create("customer-456")
    order.AddItem("SKU-001", 2, 29.99)
    
    // Save aggregate (events are persisted)
    store.SaveAggregate(ctx, order)
    
    // Load aggregate (events are replayed)
    loaded := NewOrder("order-123")
    store.LoadAggregate(ctx, loaded)
    // loaded.Status == "Created"
    // len(loaded.Items) == 1
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
| [Introduction](docs/01-introduction.md) | Problem statement and goals |
| [Architecture](docs/02-architecture.md) | System design and components |
| [Event Store](docs/03-event-store.md) | Event storage design |
| [Read Models](docs/04-read-models.md) | Projection system |
| [Adapters](docs/05-adapters.md) | Database adapter system |
| [CLI](docs/06-cli.md) | Command-line tooling |
| [API Design](docs/07-api-design.md) | Public API reference |
| [Roadmap](docs/08-roadmap.md) | Development phases |
| [Advanced Patterns](docs/09-advanced-patterns.md) | Commands, Sagas, Outbox |
| [Security](docs/10-security.md) | Encryption, GDPR, Versioning |
| [Testing](docs/11-testing.md) | BDD fixtures and test utilities |

## License

Apache License 2.0 - See [LICENSE](LICENSE) for details.

---

**go-mink** - Event Sourcing for Go, Done Right.
