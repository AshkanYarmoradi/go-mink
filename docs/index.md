---
layout: default
title: Home
nav_order: 1
description: "go-mink - A Comprehensive Event Sourcing & CQRS Toolkit for Go"
permalink: /
---

<div class="hero-section" markdown="0">
<div class="hero-glow"></div>
</div>

# go-mink
{: .fs-9 .gradient-text }

**Event Sourcing & CQRS for Go** — Built for developers who demand simplicity without sacrificing power.
{: .fs-6 .fw-300 .hero-subtitle }

[Get Started →](/docs/introduction){: .btn .btn-primary .fs-5 .mb-4 .mb-md-0 .mr-2 }
[GitHub](https://github.com/AshkanYarmoradi/go-mink){: .btn .btn-outline .fs-5 .mb-4 .mb-md-0 }

---

<div class="stats-bar" markdown="1">

🚀 **Production Ready** · 🔌 **Pluggable** · 🛡️ **Type Safe** · ⚡ **High Performance**

</div>

---

## The Problem We Solve

Building event-sourced applications in Go means wrestling with **boilerplate code**, **manual projections**, and **scattered patterns**. Most teams end up reinventing the wheel—or worse, avoiding event sourcing entirely.

**go-mink changes that.**

> "Make Event Sourcing in Go as simple as using a traditional ORM, while maintaining the flexibility that Go developers expect."

---

## ✨ Features

<div class="code-example" markdown="1">

| | Feature | Status | What You Get |
|:--|:--------|:-------|:-------------|
| 🎯 | **Event Store** | ✅ v0.1.0 | Append-only storage with optimistic concurrency |
| 🔌 | **Adapters** | ✅ v0.1.0 | PostgreSQL & In-Memory adapters |
| 🧱 | **Aggregates** | ✅ v0.1.0 | Base implementation with event application |
| 📋 | **Command Bus** | ✅ v0.2.0 | Full CQRS with middleware, validation & idempotency |
| 🔐 | **Idempotency** | ✅ v0.2.0 | Prevent duplicate command processing |
| 🔗 | **Correlation/Causation** | ✅ v0.2.0 | Distributed tracing support |
| 📖 | **Projections** | ✅ v0.3.0 | Inline, async, and live read models |
| 📡 | **Subscriptions** | ✅ v0.3.0 | Catch-up and polling event subscriptions |
| 🧪 | **Testing Utilities** | ✅ v0.4.0 | BDD fixtures, assertions, test containers |
| 📊 | **Observability** | ✅ v0.4.0 | Prometheus metrics & OpenTelemetry tracing |
| 🛠️ | **CLI** | ✅ v0.5.0 | Generate code, run migrations, diagnose (84.9% coverage) |
| � | **Sagas** | ✅ v0.5.0 | Coordinate long-running business processes |
| 🔐 | **Security** | ✅ v0.5.0 | Field-level encryption & GDPR compliance |
| 📤 | **Outbox** | ✅ v0.5.0 | Reliable event publishing |

</div>

---

## 🚀 Quick Start

```go
package main

import (
    "context"
    
    "github.com/AshkanYarmoradi/go-mink"
    "github.com/AshkanYarmoradi/go-mink/adapters/postgres"
)

func main() {
    ctx := context.Background()
    
    // 🔌 Connect to your database
    store, _ := mink.NewEventStore(
        postgres.NewAdapter("postgres://localhost/orders"),
    )
    
    // 📦 Create an aggregate
    order := NewOrder("order-123")
    order.Create("customer-456")
    order.AddItem("SKU-001", 2, 29.99)
    
    // 💾 Save events automatically
    store.SaveAggregate(ctx, order)
    
    // 📖 Projections update in real-time
    store.RegisterProjection(&OrderSummaryProjection{})
}
```

**That's it.** No ceremony. No configuration hell. Just clean, idiomatic Go.

---

## 📦 Installation

```bash
# Install the library
go get github.com/AshkanYarmoradi/go-mink

# Install PostgreSQL adapter
go get github.com/AshkanYarmoradi/go-mink/adapters/postgres

# Or use in-memory adapter for testing
go get github.com/AshkanYarmoradi/go-mink/adapters/memory
```

---

## 📚 Documentation

<div class="code-example" markdown="1">

### Getting Started
{: .no_toc }

| Guide | Description |
|:------|:------------|
| [📘 Introduction](/docs/introduction) | Why go-mink exists and what problems it solves |
| [🏗️ Architecture](/docs/architecture) | Core concepts and system design |
| [💾 Event Store](/docs/event-store) | How events are stored and retrieved |
| [📖 Read Models](/docs/read-models) | Building and maintaining projections |

### Advanced Topics
{: .no_toc }

| Guide | Description |
|:------|:------------|
| [🔌 Adapters](/docs/adapters) | Database adapters and custom implementations |
| [⚡ Commands & Sagas](/docs/advanced-patterns) | CQRS command bus and process managers |
| [🔐 Security](/docs/security) | Encryption, GDPR, and event versioning |
| [🧪 Testing](/docs/testing) | BDD fixtures and debugging tools |

### Reference
{: .no_toc }

| Guide | Description |
|:------|:------------|
| [🛠️ CLI Reference](/docs/cli) | All command-line tools |
| [📖 API Reference](/docs/api-design) | Complete API documentation |
| [🗺️ Roadmap](/docs/roadmap) | What's coming next |

### Learn Event Sourcing

{: .no_toc }

 

| Guide | Description |

|:------|:------------|

| [📝 Blog Series](/blog) | 8-part series on Event Sourcing & CQRS with go-mink |



</div>

---

## 🌟 Why Choose go-mink?

<div class="code-example comparison-box" markdown="1">

| Aspect | Traditional Approach | With go-mink |
|:-------|:---------------------|:-------------|
| **Event Storage** | Custom implementation | Built-in, optimized |
| **Projections** | Manual, error-prone | Automatic, reliable |
| **Database Support** | Locked to one DB | Swap adapters anytime |
| **Testing** | Complex setup | BDD fixtures included |
| **Learning Curve** | Steep | Gentle, familiar API |

</div>

---

## 🤝 Community & Support

<div class="code-example" markdown="1">

| | |
|:--|:--|
| 💬 | [GitHub Discussions](https://github.com/AshkanYarmoradi/go-mink/discussions) — Ask questions, share ideas |
| 🐛 | [Issue Tracker](https://github.com/AshkanYarmoradi/go-mink/issues) — Report bugs, request features |
| 🤝 | [Contributing Guide](https://github.com/AshkanYarmoradi/go-mink/blob/main/CONTRIBUTING.md) — Help us improve |
| ⭐ | [Star on GitHub](https://github.com/AshkanYarmoradi/go-mink) — Show your support |

</div>

---

## 📄 License

go-mink is open source under the [Apache 2.0 License](https://github.com/AshkanYarmoradi/go-mink/blob/main/LICENSE).

---

<p align="center" style="margin-top: 3rem;">
  <strong style="font-size: 1.25rem;">go-mink</strong><br/>
  <span style="color: #94a3b8;">Event Sourcing for Go, Done Right. 🦫</span>
</p>
