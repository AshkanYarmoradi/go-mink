# Event Sourcing and CQRS with Go: A Complete Guide

Welcome to this comprehensive 8-part blog series on **Event Sourcing** and **CQRS** (Command Query Responsibility Segregation) using **go-mink**, a powerful event sourcing library for Go.

---

## About This Series

This series takes you from the fundamentals of event sourcing to production-ready implementation patterns. Whether you're new to event sourcing or looking to deepen your understanding, you'll find practical, hands-on content with real code examples.

### What You'll Learn

- **Core Concepts**: Events, aggregates, streams, and the event store
- **CQRS Architecture**: Separating reads and writes for scalability
- **Practical Implementation**: Building real features with go-mink
- **Production Patterns**: Snapshots, monitoring, error handling, and more

---

## The Series

### Part 1: Introduction to Event Sourcing
*[Read Part 1 →](01-introduction-to-event-sourcing.md)*

Understand why event sourcing matters and how it differs from traditional data storage. Learn the fundamental concepts: events as facts, state as derived data, and the mental model shift from "what is" to "what happened."

**Topics covered:**
- The problem with destructive updates
- Events as immutable facts
- Streams and event stores
- Benefits: audit trails, time travel, debugging

---

### Part 2: Getting Started with go-mink
*[Read Part 2 →](02-getting-started-with-go-mink.md)*

Set up your first event store and start recording events. Learn how to define events, register them, and use optimistic concurrency control.

**Topics covered:**
- Installation and setup
- Defining and registering events
- Appending and loading events
- Optimistic concurrency with versions
- Working with metadata

---

### Part 3: Building Your First Aggregate
*[Read Part 3 →](03-building-your-first-aggregate.md)*

Master aggregates—the heart of domain-driven design and event sourcing. Learn to encapsulate state, enforce business rules, and produce events.

**Topics covered:**
- The Aggregate interface
- Implementing ApplyEvent
- Business methods that produce events
- Testing aggregates
- The Apply pattern

---

### Part 4: The Event Store Deep Dive
*[Read Part 4 →](04-event-store-deep-dive.md)*

Explore the event store in depth: how events are represented, stored, and queried. Understand the adapter architecture and PostgreSQL specifics.

**Topics covered:**
- Three event representations
- Stream IDs and categories
- Serialization and deserialization
- Adapters: Memory and PostgreSQL
- Subscriptions for projections

---

### Part 5: CQRS and the Command Bus
*[Read Part 5 →](05-cqrs-and-command-bus.md)*

Implement CQRS with go-mink's command bus. Learn to define commands, create handlers, and dispatch through a middleware pipeline.

**Topics covered:**
- Commands vs queries
- The Command interface
- Handler types: function, generic, aggregate
- The command bus
- Validation patterns

---

### Part 6: Middleware and Cross-Cutting Concerns
*[Read Part 6 →](06-middleware-and-cross-cutting-concerns.md)*

Add logging, metrics, validation, retries, and idempotency to your command pipeline. Learn to write custom middleware.

**Topics covered:**
- Middleware architecture
- Built-in middleware: validation, recovery, logging, metrics
- Correlation and causation tracking
- Idempotency for at-least-once delivery
- Writing custom middleware

---

### Part 7: Projections and Read Models
*[Read Part 7 →](07-projections-and-read-models.md)*

Build optimized read models using projections. Understand inline, async, and live projections for different consistency needs.

**Topics covered:**
- Why read models matter
- Three projection types
- The projection engine
- Checkpointing and recovery
- Rebuilding projections

---

### Part 8: Production Best Practices
*[Read Part 8 →](08-production-best-practices.md)*

Everything you need to run event-sourced systems in production: snapshots, monitoring, schema evolution, and disaster recovery.

**Topics covered:**
- Snapshots for long-lived aggregates
- Optimistic concurrency strategies
- Event schema evolution
- Monitoring and observability
- Error handling patterns
- Testing strategies
- Deployment and disaster recovery

---

## Quick Start

```go
package main

import (
    "context"
    "fmt"

    "github.com/AshkanYarmoradi/go-mink"
    "github.com/AshkanYarmoradi/go-mink/adapters/memory"
)

// Define an event
type OrderCreated struct {
    OrderID    string `json:"orderId"`
    CustomerID string `json:"customerId"`
}

func main() {
    ctx := context.Background()

    // Create event store
    store := mink.New(memory.NewAdapter())
    store.RegisterEvents(OrderCreated{})

    // Append an event
    store.Append(ctx, "Order-order-1", []interface{}{
        OrderCreated{OrderID: "order-1", CustomerID: "cust-1"},
    })

    // Load events
    events, _ := store.Load(ctx, "Order-order-1")
    for _, e := range events {
        fmt.Printf("%s: %+v\n", e.Type, e.Data)
    }
}
```

---

## About go-mink

**go-mink** is an Event Sourcing and CQRS library for Go, inspired by MartenDB for .NET. It provides:

- **Event Store** with pluggable adapters (PostgreSQL, Memory)
- **Aggregate support** for domain modeling
- **Command Bus** with middleware pipeline
- **Projection Engine** for read models
- **Production-ready patterns** for real-world applications

### Installation

```bash
go get github.com/AshkanYarmoradi/go-mink
```

### Documentation

- [GitHub Repository](https://github.com/AshkanYarmoradi/go-mink)
- [API Documentation](https://pkg.go.dev/github.com/AshkanYarmoradi/go-mink)

---

## Prerequisites

To get the most from this series, you should have:

- **Go experience**: Comfortable with Go syntax, interfaces, and error handling
- **Basic database knowledge**: Understanding of SQL and transactions
- **Curiosity**: Ready to think differently about data

No prior event sourcing experience is required.

---

## How to Use This Series

1. **Read sequentially**: Each part builds on the previous
2. **Run the examples**: Code is meant to be executed
3. **Experiment**: Modify examples to understand better
4. **Build something**: Apply concepts to a real project

---

## Feedback

Found an issue or have suggestions? Please open an issue on the [go-mink repository](https://github.com/AshkanYarmoradi/go-mink/issues).

---

Happy event sourcing!
