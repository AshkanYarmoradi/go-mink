---
layout: default
title: "Part 5: CQRS and the Command Bus"
parent: Blog
nav_order: 5
permalink: /blog/05-cqrs
---

# Part 5: CQRS and the Command Bus
{: .no_toc }

Separating reads from writes with Command Query Responsibility Segregation.
{: .fs-6 .fw-300 }

## Table of contents
{: .no_toc .text-delta }

1. TOC
{:toc}

---

*This is Part 5 of an 8-part series on Event Sourcing and CQRS with Go.*

---

## What is CQRS?

**CQRS** (Command Query Responsibility Segregation) separates read and write operations into different models:

```
   Commands (Write)              Queries (Read)
         │                             │
         ▼                             ▼
   ┌───────────┐               ┌─────────────┐
   │ Command   │               │    Query    │
   │ Handlers  │               │  Handlers   │
   └─────┬─────┘               └──────┬──────┘
         │                            │
         ▼                            ▼
   ┌───────────┐               ┌─────────────┐
   │  Event    │   ───────►    │    Read     │
   │  Store    │  (Projections)│   Model     │
   └───────────┘               └─────────────┘
```

### Why Separate?

1. **Different optimization needs**: Writes need consistency; reads need speed
2. **Different scaling requirements**: Reads are often 100x more frequent
3. **Independent evolution**: Change read model without affecting writes

---

## Commands vs Queries

### Commands

Commands are *intentions* to change state:

```go
type PlaceOrderCommand struct {
    CustomerID string
    Items      []OrderItem
}

func (c PlaceOrderCommand) CommandType() string { return "PlaceOrder" }
func (c PlaceOrderCommand) Validate() error {
    if c.CustomerID == "" {
        return errors.New("customer ID required")
    }
    return nil
}
```

### The Golden Rule

**Commands**: "Do this" → Returns success/failure
**Queries**: "Give me this" → Returns data

---

## The Command Interface

```go
type Command interface {
    CommandType() string  // Unique type identifier
    Validate() error      // Self-validation
}
```

### Aggregate Command

```go
type AggregateCommand interface {
    Command
    AggregateID() string  // Which aggregate to modify
}

type AddItemCommand struct {
    OrderID  string
    SKU      string
    Quantity int
}

func (c AddItemCommand) CommandType() string { return "AddItem" }
func (c AddItemCommand) AggregateID() string { return c.OrderID }
```

---

## Command Handlers

### Generic Handler

```go
handler := mink.NewGenericHandler(
    func(ctx context.Context, cmd CreateOrderCommand) (mink.CommandResult, error) {
        order := NewOrder(uuid.New().String())
        if err := order.Create(cmd.CustomerID); err != nil {
            return mink.NewErrorResult(err), err
        }
        if err := store.SaveAggregate(ctx, order); err != nil {
            return mink.NewErrorResult(err), err
        }
        return mink.NewSuccessResult(order.AggregateID(), order.Version()), nil
    })
```

### Aggregate Handler

Handles the full aggregate lifecycle automatically:

```go
handler := mink.NewAggregateHandler(mink.AggregateHandlerConfig[AddItemCommand, *Order]{
    Store:   store,
    Factory: NewOrder,
    Executor: func(ctx context.Context, order *Order, cmd AddItemCommand) error {
        return order.AddItem(cmd.SKU, cmd.Quantity, cmd.Price)
    },
})
```

---

## The Command Bus

```go
registry := mink.NewHandlerRegistry()
registry.Register(createOrderHandler)
registry.Register(addItemHandler)

bus := mink.NewCommandBus(
    mink.WithHandlerRegistry(registry),
    mink.WithMiddleware(
        mink.ValidationMiddleware(),
        mink.RecoveryMiddleware(),
    ),
)

result, err := bus.Dispatch(ctx, CreateOrderCommand{CustomerID: "cust-123"})
```

---

## Key Takeaways

{: .highlight }
> 1. **Commands are intentions**: They express what you want to happen
> 2. **Queries are questions**: They don't change state
> 3. **Handlers execute commands**: Keep them focused and simple
> 4. **Aggregate handlers reduce boilerplate**: Automatic load/save lifecycle
> 5. **Validation belongs in commands**: Self-validating commands are cleaner

---

[← Part 4: Event Store](/blog/04-event-store){: .btn .fs-5 .mb-4 .mb-md-0 .mr-2 }
[Part 6: Middleware →](/blog/06-middleware){: .btn .btn-primary .fs-5 .mb-4 .mb-md-0 }
