---
layout: default
title: "Part 1: Introduction to Event Sourcing"
parent: Blog
nav_order: 1
permalink: /blog/01-introduction
---

# Part 1: Introduction to Event Sourcing
{: .no_toc }

A Different Way to Think About Data
{: .fs-6 .fw-300 }

## Table of contents
{: .no_toc .text-delta }

1. TOC
{:toc}

---

*This is Part 1 of an 8-part series on Event Sourcing and CQRS with Go. By the end of this series, you'll understand these powerful patterns and how to implement them using go-mink.*

---

## The Problem with Traditional Data Storage

Imagine you're building an e-commerce system. A customer places an order, adds items, changes the shipping address, applies a discount code, and finally checks out. In a traditional system, you might have an `orders` table that looks like this:

```sql
SELECT * FROM orders WHERE id = 'order-123';

| id        | customer_id | status    | total  | shipping_address     | discount_code |
|-----------|-------------|-----------|--------|----------------------|---------------|
| order-123 | cust-456    | completed | 127.50 | 123 New Street, NYC  | SAVE20        |
```

This gives you the current state. But what if you need to answer:

- What was the original shipping address before the customer changed it?
- When was the discount code applied?
- Did the customer remove any items before checkout?
- What was the order total before the discount?

**You can't.** That information is gone forever.

Traditional databases store *state*. When you update a record, the previous state is overwritten. This is called **destructive updates**, and it's been the dominant paradigm for decades.

But there's another way.

---

## What is Event Sourcing?

**Event Sourcing** is an architectural pattern where you store the *history of changes* rather than the current state. Instead of updating records in place, you append immutable events to a log.

For our order example, instead of one row showing the final state, you'd have a sequence of events:

```
1. OrderCreated      { orderId: "order-123", customerId: "cust-456" }
2. ItemAdded         { sku: "WIDGET-001", quantity: 2, price: 49.99 }
3. ItemAdded         { sku: "GADGET-002", quantity: 1, price: 79.99 }
4. ShippingUpdated   { address: "123 New Street, NYC" }
5. DiscountApplied   { code: "SAVE20", percentage: 20 }
6. OrderCompleted    { total: 127.50 }
```

To get the current state, you *replay* these events from the beginning. The state is derived, not stored.

### The Key Insight

Events are **facts**. They describe something that happened in the past. You can't change the past—you can only record new events that supersede previous ones.

- You don't "update" an address. You record `ShippingUpdated`.
- You don't "delete" an item. You record `ItemRemoved`.
- You don't "change" a status. You record `OrderCancelled` or `OrderCompleted`.

---

## Why Event Sourcing?

### 1. Complete Audit Trail

Every change is recorded with a timestamp. You have a perfect audit log built into your data model. This is invaluable for:

- Debugging production issues ("What happened to this order?")
- Compliance requirements (financial regulations, GDPR)
- Customer support ("When did this change occur?")

### 2. Time Travel

You can reconstruct the state at any point in time:

```go
// What did this order look like yesterday at 3 PM?
events := store.LoadUntil(ctx, "order-123", yesterday3PM)
order := replayEvents(events)
```

This enables powerful features like "undo" functionality, historical reporting, and debugging time-sensitive bugs.

### 3. Event-Driven Architecture

Events are a natural integration point. Other services can subscribe to events and react:

- Inventory service listens for `ItemAdded` to reserve stock
- Email service listens for `OrderCompleted` to send confirmation
- Analytics service listens to everything for reporting

### 4. No Data Loss

Traditional systems lose information through updates. Event sourcing preserves everything. You might discover years later that you need historical data—with event sourcing, you have it.

### 5. Debugging and Testing

When something goes wrong, you can:

- Replay the exact sequence of events that led to the bug
- Reproduce the issue locally with production event data
- Understand exactly what happened and when

---

## The Mental Model Shift

Traditional thinking: **"What is the current state of this entity?"**

Event sourcing thinking: **"What happened to this entity?"**

This shift is fundamental. You stop thinking in terms of CRUD operations (Create, Read, Update, Delete) and start thinking in terms of domain events—things that happen in your business.

Consider these traditional operations and their event-sourced equivalents:

| Traditional | Event Sourced |
|-------------|---------------|
| `UPDATE orders SET status = 'shipped'` | `OrderShipped { orderId, trackingNumber, carrier }` |
| `DELETE FROM cart_items WHERE id = 5` | `ItemRemovedFromCart { cartId, sku, reason }` |
| `INSERT INTO payments VALUES (...)` | `PaymentReceived { orderId, amount, method, transactionId }` |

Notice how the events capture *why* something happened, not just *what* changed.

---

## Core Concepts

### Events

An event is an immutable record of something that happened. Events have:

- **Type**: What kind of event (e.g., `OrderCreated`, `ItemAdded`)
- **Data**: The payload describing what happened
- **Metadata**: Contextual information (who, when, correlation IDs)
- **Timestamp**: When the event was recorded

```go
// Event data structure
type OrderCreated struct {
    OrderID    string `json:"orderId"`
    CustomerID string `json:"customerId"`
    CreatedAt  time.Time `json:"createdAt"`
}
```

### Streams

Events are organized into **streams**. A stream represents the history of a single entity (aggregate). Each stream has:

- **Stream ID**: Unique identifier (e.g., `Order-order-123`)
- **Sequence of events**: Ordered by version number
- **Current version**: The count of events in the stream

### Event Store

The **event store** is the database optimized for event sourcing. It provides:

- Append-only writes (no updates or deletes)
- Ordered reads (replay events in sequence)
- Optimistic concurrency (prevent conflicting writes)
- Global ordering (for projections and subscriptions)

---

## A Simple Example

Let's trace through a complete example. We have a bank account that supports deposits and withdrawals.

### Define the Events

```go
type AccountOpened struct {
    AccountID string `json:"accountId"`
    OwnerName string `json:"ownerName"`
}

type MoneyDeposited struct {
    Amount float64 `json:"amount"`
}

type MoneyWithdrawn struct {
    Amount float64 `json:"amount"`
}
```

### Record Events

```go
// Open account
events = append(events, AccountOpened{AccountID: "acc-1", OwnerName: "Alice"})

// Deposit $100
events = append(events, MoneyDeposited{Amount: 100})

// Withdraw $30
events = append(events, MoneyWithdrawn{Amount: 30})

// Deposit $50
events = append(events, MoneyDeposited{Amount: 50})
```

### Replay to Get Current State

```go
type Account struct {
    ID      string
    Owner   string
    Balance float64
}

func replayEvents(events []interface{}) *Account {
    account := &Account{}

    for _, event := range events {
        switch e := event.(type) {
        case AccountOpened:
            account.ID = e.AccountID
            account.Owner = e.OwnerName
            account.Balance = 0
        case MoneyDeposited:
            account.Balance += e.Amount
        case MoneyWithdrawn:
            account.Balance -= e.Amount
        }
    }

    return account
}

// Result: Account{ID: "acc-1", Owner: "Alice", Balance: 120}
```

The current balance (120) is never stored directly—it's calculated by replaying all events.

---

## Common Concerns and Misconceptions

### "Won't replaying events be slow?"

For most use cases, no. Modern systems can replay thousands of events in milliseconds. For very long-lived entities, you can use **snapshots**—periodic saves of the current state that allow you to start replay from a recent point rather than the beginning.

### "What about storage costs?"

Events are typically small (a few hundred bytes). Even millions of events are manageable. The tradeoff is worth it for the benefits. And storage is cheap compared to lost business data.

### "Isn't this more complex?"

There's a learning curve, but event sourcing often *simplifies* complex domains. Instead of figuring out how to update tangled state, you just record what happened. The complexity shifts from "how do I update this" to "what events represent my domain."

### "Can I query events like a regular database?"

Not directly. Event stores are optimized for append and sequential read. For queries, you build **projections**—read models derived from events. This is where CQRS comes in, which we'll cover in Part 5.

---

## When to Use Event Sourcing

Event sourcing excels when you have:

- **Complex business domains** with intricate rules and workflows
- **Audit requirements** where you need to track all changes
- **Integration needs** where multiple services react to domain events
- **Temporal queries** like "show me the state at point X in time"
- **High-value transactions** where losing data is unacceptable

It may be overkill for:

- Simple CRUD applications with no audit needs
- Throwaway prototypes
- Static reference data that rarely changes

---

## What's Next?

In this post, we covered the foundations of event sourcing:

- Why traditional databases lose valuable information
- How event sourcing preserves the complete history
- The mental model shift from state to events
- Core concepts: events, streams, and event stores

In **Part 2**, we'll get hands-on with **go-mink**, a powerful event sourcing library for Go. You'll set up your first event store and start recording events.

---

## Key Takeaways

{: .highlight }
> 1. **Events are facts**: Immutable records of things that happened
> 2. **State is derived**: Calculated by replaying events, not stored directly
> 3. **History is preserved**: Every change is recorded forever
> 4. **Think in events**: "What happened?" instead of "What is the current state?"
> 5. **Events enable integration**: Other systems can subscribe and react

---

[Next: Part 2: Getting Started with go-mink →](/blog/02-getting-started){: .btn .btn-primary .fs-5 .mb-4 .mb-md-0 }
