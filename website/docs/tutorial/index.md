---
title: "Tutorial: Building an E-Commerce App"
sidebar_position: 13
---

# Tutorial: Building an E-Commerce App with go-mink

<span class="badge badge--success">Hands-On Tutorial</span>

Build a complete e-commerce system using Event Sourcing and CQRS patterns.

---

## What You'll Build

In this 6-part tutorial, you'll build **MinkShop** — a real-world e-commerce application featuring:

- **Product Catalog** — Manage products with inventory tracking
- **Shopping Cart** — Add/remove items with real-time updates
- **Order Processing** — Complete checkout flow with payment handling
- **Customer Management** — User accounts with order history
- **Read Models** — Optimized queries for product listings and dashboards

By the end, you'll have a production-ready foundation demonstrating:

```
┌─────────────────────────────────────────────────────────────────┐
│                       MinkShop Architecture                      │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│   Commands              Queries              Real-time           │
│   ┌──────────┐        ┌──────────┐        ┌──────────┐          │
│   │AddToCart │        │GetCart   │        │Dashboard │          │
│   │Checkout  │        │ListOrders│        │Updates   │          │
│   │Ship      │        │Search    │        │           │          │
│   └────┬─────┘        └────┬─────┘        └────┬─────┘          │
│        │                   │                   │                 │
│        ▼                   ▼                   ▼                 │
│   ┌─────────────────────────────────────────────────┐           │
│   │               Command Bus + Middleware           │           │
│   └─────────────────────────────────────────────────┘           │
│        │                   │                   │                 │
│        ▼                   ▼                   ▼                 │
│   ┌─────────┐        ┌─────────────────────────────┐           │
│   │Aggregate│───────▶│     Event Store (PostgreSQL) │           │
│   │ Roots   │        └─────────────────────────────┘           │
│   └─────────┘                     │                             │
│                                   ▼                             │
│                    ┌─────────────────────────────┐              │
│                    │     Projection Engine       │              │
│                    ├─────────────────────────────┤              │
│                    │ CartView │ OrderHistory │ Dashboard │      │
│                    └─────────────────────────────┘              │
└─────────────────────────────────────────────────────────────────┘
```

---

## Tutorial Parts

| Part | Title | Duration | What You'll Learn |
|:-----|:------|:---------|:------------------|
| 1 | [Project Setup](/docs/tutorial/setup) | 20 min | Install go-mink, configure PostgreSQL, project structure |
| 2 | [Domain Modeling](/docs/tutorial/domain-modeling) | 45 min | Define events, build aggregates, implement business rules |
| 3 | [Commands & CQRS](/docs/tutorial/commands-cqrs) | 40 min | Command bus, handlers, middleware pipeline |
| 4 | [Projections & Queries](/docs/tutorial/projections) | 45 min | Build read models, queries, real-time updates |
| 5 | [Testing](/docs/tutorial/testing) | 35 min | BDD tests, assertions, integration testing |
| 6 | [Production Ready](/docs/tutorial/production) | 40 min | Metrics, tracing, deployment, best practices |

**Total Time**: ~4 hours

---

## Prerequisites

Before starting, ensure you have:

- **Go 1.21+** installed ([download](https://go.dev/dl/))
- **Docker** for PostgreSQL ([download](https://www.docker.com/products/docker-desktop))
- **Basic Go knowledge** — structs, interfaces, error handling
- **A code editor** — VS Code with Go extension recommended

### Verify Your Setup

```bash
# Check Go version
go version
# Expected: go version go1.21.x or higher

# Check Docker
docker --version
# Expected: Docker version 24.x or higher
```

---

## What is Event Sourcing?

If you're new to Event Sourcing, here's a quick overview:

**Traditional Approach**: Store current state, overwrite on updates
```sql
UPDATE products SET quantity = 8 WHERE id = 'WIDGET';
-- Previous quantity is lost forever
```

**Event Sourcing**: Store what happened, derive current state
```
ProductCreated { id: "WIDGET", quantity: 10 }
InventoryReserved { productId: "WIDGET", quantity: 2 }
-- Current quantity = 10 - 2 = 8
-- Full history preserved!
```

### Benefits for E-Commerce

1. **Complete Audit Trail** — Every cart change, order update is recorded
2. **Time Travel** — "What did this order look like yesterday?"
3. **Event-Driven** — Inventory, payments, notifications react to events
4. **Debug Production** — Replay exact sequence that caused issues
5. **Analytics** — Build any report from historical events

---

## Project Structure

Here's what we'll build:

```
minkshop/
├── cmd/
│   └── server/
│       └── main.go           # Application entry point
│
├── internal/
│   ├── domain/               # Core business logic
│   │   ├── product/          # Product aggregate
│   │   │   ├── aggregate.go
│   │   │   ├── events.go
│   │   │   └── commands.go
│   │   ├── cart/             # Shopping cart aggregate
│   │   │   ├── aggregate.go
│   │   │   ├── events.go
│   │   │   └── commands.go
│   │   └── order/            # Order aggregate
│   │       ├── aggregate.go
│   │       ├── events.go
│   │       └── commands.go
│   │
│   ├── projections/          # Read models
│   │   ├── product_catalog.go
│   │   ├── cart_view.go
│   │   └── order_history.go
│   │
│   ├── handlers/             # Command handlers
│   │   ├── product_handler.go
│   │   ├── cart_handler.go
│   │   └── order_handler.go
│   │
│   └── api/                  # HTTP handlers
│       └── routes.go
│
├── tests/                    # Test files
│   ├── product_test.go
│   ├── cart_test.go
│   └── order_test.go
│
├── docker-compose.yml        # PostgreSQL setup
└── go.mod
```

---

## Quick Start

Can't wait to get started? Here's the fastest path:

### Option A: Using the CLI (Recommended)

```bash
# 1. Install the CLI
go install github.com/AshkanYarmoradi/go-mink/cmd/mink@latest

# 2. Create project
mkdir minkshop && cd minkshop
go mod init minkshop

# 3. Initialize with CLI
mink init --name=minkshop --driver=postgres

# 4. Generate aggregates
mink generate aggregate Product --events Created,StockAdded,StockReserved
mink generate aggregate Cart --events Created,ItemAdded,ItemRemoved,Cleared
mink generate aggregate Order --events Created,Paid,Shipped,Delivered,Cancelled

# 5. Start PostgreSQL
docker run -d --name minkshop-db \
  -e POSTGRES_PASSWORD=secret \
  -p 5432:5432 \
  postgres:16

# 6. Set connection and migrate
export DATABASE_URL="postgres://postgres:secret@localhost:5432/postgres?sslmode=disable"
mink migrate up

# 7. Verify setup
mink diagnose
```

### Option B: Manual Setup

```bash
# 1. Create project
mkdir minkshop && cd minkshop
go mod init minkshop

# 2. Install go-mink
go get github.com/AshkanYarmoradi/go-mink

# 3. Start PostgreSQL
docker run -d --name minkshop-db \
  -e POSTGRES_PASSWORD=secret \
  -p 5432:5432 \
  postgres:16

# 4. Continue to Part 1...
```

:::tip
The CLI generates boilerplate code while teaching you the fundamentals. The manual approach in this tutorial helps you understand what's happening under the hood.
:::

---

## E-Commerce Domain Overview

Our e-commerce system has three main aggregates:

### Product
Manages inventory and product information.

```go
// Events
ProductCreated { ID, Name, Price, InitialStock }
StockAdded { ProductID, Quantity }
StockReserved { ProductID, Quantity, OrderID }
StockReleased { ProductID, Quantity, OrderID }
```

### Cart
Handles shopping cart operations per customer.

```go
// Events
CartCreated { CartID, CustomerID }
ItemAddedToCart { CartID, ProductID, Quantity, Price }
ItemRemovedFromCart { CartID, ProductID }
CartCleared { CartID }
```

### Order
Processes checkout and order lifecycle.

```go
// Events
OrderCreated { OrderID, CustomerID, Items, Total }
OrderPaid { OrderID, PaymentID, Amount }
OrderShipped { OrderID, TrackingNumber }
OrderDelivered { OrderID }
OrderCancelled { OrderID, Reason }
```

---

## Getting Help

Stuck? Here's where to get help:

- **GitHub Issues**: [Report bugs or ask questions](https://github.com/AshkanYarmoradi/go-mink/issues)
- **Discussions**: [Community Q&A](https://github.com/AshkanYarmoradi/go-mink/discussions)
- **Examples**: Check the `/examples` folder in the repository

---

## Let's Get Started!

Ready to build? Head to **[Part 1: Project Setup](/docs/tutorial/setup)** to create your project and configure the event store.

**Next**: [Part 1: Project Setup -->](/docs/tutorial/setup)

---

:::note
This tutorial is designed to be followed in order. Each part builds on the previous one, introducing new concepts progressively.
:::
