# Part 5: CQRS and the Command Bus

*This is Part 5 of an 8-part series on Event Sourcing and CQRS with Go. In this post, we'll explore CQRS—Command Query Responsibility Segregation—and implement a command bus with go-mink.*

---

## What is CQRS?

**CQRS** (Command Query Responsibility Segregation) is an architectural pattern that separates read and write operations into different models.

### Traditional Architecture

In traditional applications, the same model handles both reads and writes:

```
┌──────────────────────────────────────────┐
│              Application                  │
├──────────────────────────────────────────┤
│                                          │
│    ┌────────────────────────────────┐    │
│    │         Domain Model            │    │
│    │   (Same model for R/W)          │    │
│    └────────────────────────────────┘    │
│                    │                      │
│                    ▼                      │
│    ┌────────────────────────────────┐    │
│    │           Database              │    │
│    └────────────────────────────────┘    │
│                                          │
└──────────────────────────────────────────┘
```

### CQRS Architecture

CQRS splits this into two distinct paths:

```
┌─────────────────────────────────────────────────────────────┐
│                        Application                           │
├─────────────────────────────────────────────────────────────┤
│                                                              │
│   Commands (Write)              Queries (Read)               │
│         │                             │                      │
│         ▼                             ▼                      │
│   ┌───────────┐               ┌─────────────┐                │
│   │ Command   │               │    Query    │                │
│   │ Handlers  │               │  Handlers   │                │
│   └─────┬─────┘               └──────┬──────┘                │
│         │                            │                       │
│         ▼                            ▼                       │
│   ┌───────────┐               ┌─────────────┐                │
│   │ Write     │    ───────►   │    Read     │                │
│   │ Model     │   (Projections)│   Model    │                │
│   └─────┬─────┘               └──────┬──────┘                │
│         │                            │                       │
│         ▼                            ▼                       │
│   ┌───────────┐               ┌─────────────┐                │
│   │  Event    │               │   Read DB   │                │
│   │  Store    │               │ (Optimized) │                │
│   └───────────┘               └─────────────┘                │
│                                                              │
└─────────────────────────────────────────────────────────────┘
```

### Why Separate Reads and Writes?

1. **Different optimization needs**: Writes need consistency; reads need speed
2. **Different scaling requirements**: Reads are often 100x more frequent
3. **Different models**: Write model enforces rules; read model serves views
4. **Independent evolution**: Change read model without affecting writes

---

## Commands vs Queries

### Commands

Commands are *intentions* to change state. They:

- Are imperative ("PlaceOrder", "CancelSubscription")
- May fail (validation, business rules)
- Change state (produce events)
- Don't return data (except success/failure)

```go
type PlaceOrderCommand struct {
    CustomerID string
    Items      []OrderItem
}
```

### Queries

Queries retrieve data. They:

- Are interrogative ("GetOrderDetails", "ListCustomerOrders")
- Don't fail (except for not found)
- Don't change state
- Return data

```go
type GetOrderQuery struct {
    OrderID string
}
// Returns OrderDetailsDTO
```

### The Golden Rule

**Commands**: "Do this" → Returns success/failure
**Queries**: "Give me this" → Returns data

Never mix them. A command should not return rich data. A query should not change state.

---

## The Command Interface

go-mink defines commands with this interface:

```go
type Command interface {
    CommandType() string  // Unique type identifier
    Validate() error      // Self-validation
}
```

### Basic Command

```go
type CreateOrderCommand struct {
    CustomerID string
    Items      []OrderItem
}

func (c CreateOrderCommand) CommandType() string {
    return "CreateOrder"
}

func (c CreateOrderCommand) Validate() error {
    if c.CustomerID == "" {
        return errors.New("customer ID is required")
    }
    if len(c.Items) == 0 {
        return errors.New("at least one item is required")
    }
    return nil
}
```

### Aggregate Command

For commands that target a specific aggregate:

```go
type AggregateCommand interface {
    Command
    AggregateID() string  // Which aggregate to modify
}

type AddItemCommand struct {
    OrderID  string
    SKU      string
    Quantity int
    Price    float64
}

func (c AddItemCommand) CommandType() string   { return "AddItem" }
func (c AddItemCommand) AggregateID() string   { return c.OrderID }
func (c AddItemCommand) Validate() error {
    if c.Quantity <= 0 {
        return errors.New("quantity must be positive")
    }
    return nil
}
```

### Idempotent Command

For commands that support deduplication:

```go
type IdempotentCommand interface {
    Command
    IdempotencyKey() string  // Unique key for deduplication
}

type ProcessPaymentCommand struct {
    OrderID       string
    Amount        float64
    TransactionID string  // External payment ID
}

func (c ProcessPaymentCommand) CommandType() string     { return "ProcessPayment" }
func (c ProcessPaymentCommand) IdempotencyKey() string  { return c.TransactionID }
func (c ProcessPaymentCommand) Validate() error {
    if c.Amount <= 0 {
        return errors.New("amount must be positive")
    }
    return nil
}
```

### Command with Metadata

Use `CommandBase` for built-in metadata support:

```go
type CancelOrderCommand struct {
    mink.CommandBase  // Provides CommandID, CorrelationID, etc.
    OrderID string
    Reason  string
}

func (c CancelOrderCommand) CommandType() string { return "CancelOrder" }
func (c CancelOrderCommand) Validate() error {
    if c.Reason == "" {
        return errors.New("cancellation reason is required")
    }
    return nil
}

// Usage
cmd := CancelOrderCommand{
    OrderID: "order-123",
    Reason:  "Customer requested",
}.WithCorrelationID(requestID).WithMetadata("source", "customer-portal")
```

---

## Command Handlers

Handlers execute commands and produce results:

```go
type CommandHandler interface {
    CommandType() string
    Handle(ctx context.Context, cmd Command) (CommandResult, error)
}
```

### Handler Result

```go
type CommandResult struct {
    Success     bool
    AggregateID string
    Version     int64
    Data        interface{}  // Optional additional data
    Error       error
}

// Helpers
mink.NewSuccessResult(aggregateID, version)
mink.NewErrorResult(err)
```

### Function Handler

The simplest approach:

```go
handler := mink.NewHandlerFunc("CreateOrder",
    func(ctx context.Context, cmd mink.Command) (mink.CommandResult, error) {
        createCmd := cmd.(CreateOrderCommand)

        order := NewOrder(uuid.New().String())
        if err := order.Create(createCmd.CustomerID, createCmd.Items); err != nil {
            return mink.NewErrorResult(err), err
        }

        if err := store.SaveAggregate(ctx, order); err != nil {
            return mink.NewErrorResult(err), err
        }

        return mink.NewSuccessResult(order.AggregateID(), order.Version()), nil
    })
```

### Generic Handler

Type-safe without casting:

```go
handler := mink.NewGenericHandler(
    func(ctx context.Context, cmd CreateOrderCommand) (mink.CommandResult, error) {
        // cmd is already the correct type
        order := NewOrder(uuid.New().String())
        if err := order.Create(cmd.CustomerID, cmd.Items); err != nil {
            return mink.NewErrorResult(err), err
        }

        if err := store.SaveAggregate(ctx, order); err != nil {
            return mink.NewErrorResult(err), err
        }

        return mink.NewSuccessResult(order.AggregateID(), order.Version()), nil
    })
```

### Aggregate Handler

The most powerful approach—handles the full aggregate lifecycle:

```go
handler := mink.NewAggregateHandler(mink.AggregateHandlerConfig[AddItemCommand, *Order]{
    Store: store,

    // Factory creates new or empty aggregates
    Factory: func(id string) *Order {
        return NewOrder(id)
    },

    // Executor runs business logic
    Executor: func(ctx context.Context, order *Order, cmd AddItemCommand) error {
        return order.AddItem(cmd.SKU, cmd.Quantity, cmd.Price)
    },

    // Optional: Generate IDs for new aggregates
    NewIDFunc: func() string {
        return uuid.New().String()
    },
})
```

What `AggregateHandler` does automatically:
1. Extracts aggregate ID from command
2. Creates aggregate via factory
3. Loads existing state from store
4. Executes your business logic
5. Saves aggregate with optimistic concurrency
6. Returns result with version

---

## The Command Bus

The command bus dispatches commands to handlers:

```go
// Create handler registry
registry := mink.NewHandlerRegistry()
registry.Register(createOrderHandler)
registry.Register(addItemHandler)
registry.Register(cancelOrderHandler)

// Create command bus
bus := mink.NewCommandBus(
    mink.WithHandlerRegistry(registry),
)

// Dispatch a command
result, err := bus.Dispatch(ctx, CreateOrderCommand{
    CustomerID: "cust-123",
    Items:      items,
})

if err != nil {
    log.Printf("Command failed: %v", err)
    return
}

log.Printf("Order created: %s (version %d)",
    result.AggregateID, result.Version)
```

### Handler Registration

```go
registry := mink.NewHandlerRegistry()

// Register individual handlers
registry.Register(createOrderHandler)
registry.Register(addItemHandler)

// Register function handlers
registry.RegisterFunc("CancelOrder", cancelOrderFunc)

// Check if handler exists
if registry.Has("CreateOrder") {
    // ...
}
```

### Bus Options

```go
bus := mink.NewCommandBus(
    mink.WithHandlerRegistry(registry),
    mink.WithMiddleware(
        mink.ValidationMiddleware(),
        mink.LoggingMiddleware(logger),
    ),
)
```

---

## Async Dispatch

For fire-and-forget scenarios:

```go
// Returns a channel for the result
resultCh := bus.DispatchAsync(ctx, command)

// Do other work...

// Check result when needed
result := <-resultCh
if result.Error != nil {
    log.Printf("Async command failed: %v", result.Error)
}
```

### Batch Dispatch

For multiple commands:

```go
commands := []mink.Command{
    CreateOrderCommand{...},
    AddItemCommand{...},
    ProcessPaymentCommand{...},
}

results := bus.DispatchAll(ctx, commands...)

for i, result := range results {
    if result.Error != nil {
        log.Printf("Command %d failed: %v", i, result.Error)
    }
}
```

---

## Complete CQRS Example

Let's build a complete order management system:

### Events

```go
type OrderCreated struct {
    OrderID    string    `json:"orderId"`
    CustomerID string    `json:"customerId"`
    CreatedAt  time.Time `json:"createdAt"`
}

type ItemAdded struct {
    SKU      string  `json:"sku"`
    Quantity int     `json:"quantity"`
    Price    float64 `json:"price"`
}

type OrderConfirmed struct {
    ConfirmedAt time.Time `json:"confirmedAt"`
}

type OrderCancelled struct {
    Reason      string    `json:"reason"`
    CancelledAt time.Time `json:"cancelledAt"`
}
```

### Aggregate

```go
type Order struct {
    mink.AggregateBase
    CustomerID  string
    Items       []OrderItem
    Status      string
    Total       float64
}

func NewOrder(id string) *Order {
    return &Order{
        AggregateBase: mink.NewAggregateBase(id, "Order"),
        Items:         []OrderItem{},
        Status:        "draft",
    }
}

func (o *Order) ApplyEvent(event interface{}) error {
    switch e := event.(type) {
    case OrderCreated:
        o.CustomerID = e.CustomerID
        o.Status = "created"
    case ItemAdded:
        o.Items = append(o.Items, OrderItem{
            SKU: e.SKU, Quantity: e.Quantity, Price: e.Price,
        })
        o.Total += e.Price * float64(e.Quantity)
    case OrderConfirmed:
        o.Status = "confirmed"
    case OrderCancelled:
        o.Status = "cancelled"
    }
    o.IncrementVersion()
    return nil
}

func (o *Order) Create(customerID string) error {
    if o.Version() > 0 {
        return errors.New("order already exists")
    }
    o.Apply(OrderCreated{
        OrderID:    o.AggregateID(),
        CustomerID: customerID,
        CreatedAt:  time.Now(),
    })
    return nil
}

func (o *Order) AddItem(sku string, qty int, price float64) error {
    if o.Status != "created" {
        return fmt.Errorf("cannot add items to %s order", o.Status)
    }
    o.Apply(ItemAdded{SKU: sku, Quantity: qty, Price: price})
    return nil
}

func (o *Order) Confirm() error {
    if o.Status != "created" {
        return fmt.Errorf("cannot confirm %s order", o.Status)
    }
    if len(o.Items) == 0 {
        return errors.New("cannot confirm empty order")
    }
    o.Apply(OrderConfirmed{ConfirmedAt: time.Now()})
    return nil
}

func (o *Order) Cancel(reason string) error {
    if o.Status == "cancelled" {
        return errors.New("order already cancelled")
    }
    o.Apply(OrderCancelled{Reason: reason, CancelledAt: time.Now()})
    return nil
}
```

### Commands

```go
type CreateOrderCommand struct {
    CustomerID string
}

func (c CreateOrderCommand) CommandType() string { return "CreateOrder" }
func (c CreateOrderCommand) Validate() error {
    if c.CustomerID == "" {
        return errors.New("customer ID required")
    }
    return nil
}

type AddItemCommand struct {
    OrderID  string
    SKU      string
    Quantity int
    Price    float64
}

func (c AddItemCommand) CommandType() string { return "AddItem" }
func (c AddItemCommand) AggregateID() string { return c.OrderID }
func (c AddItemCommand) Validate() error {
    if c.Quantity <= 0 {
        return errors.New("quantity must be positive")
    }
    return nil
}

type ConfirmOrderCommand struct {
    OrderID string
}

func (c ConfirmOrderCommand) CommandType() string { return "ConfirmOrder" }
func (c ConfirmOrderCommand) AggregateID() string { return c.OrderID }
func (c ConfirmOrderCommand) Validate() error     { return nil }

type CancelOrderCommand struct {
    OrderID string
    Reason  string
}

func (c CancelOrderCommand) CommandType() string { return "CancelOrder" }
func (c CancelOrderCommand) AggregateID() string { return c.OrderID }
func (c CancelOrderCommand) Validate() error {
    if c.Reason == "" {
        return errors.New("reason required")
    }
    return nil
}
```

### Handlers

```go
func setupHandlers(store *mink.EventStore) *mink.HandlerRegistry {
    registry := mink.NewHandlerRegistry()

    // Create order (generates new ID)
    registry.Register(mink.NewGenericHandler(
        func(ctx context.Context, cmd CreateOrderCommand) (mink.CommandResult, error) {
            order := NewOrder(uuid.New().String())
            if err := order.Create(cmd.CustomerID); err != nil {
                return mink.NewErrorResult(err), err
            }
            if err := store.SaveAggregate(ctx, order); err != nil {
                return mink.NewErrorResult(err), err
            }
            return mink.NewSuccessResult(order.AggregateID(), order.Version()), nil
        }))

    // Add item (uses aggregate handler)
    registry.Register(mink.NewAggregateHandler(mink.AggregateHandlerConfig[AddItemCommand, *Order]{
        Store:   store,
        Factory: NewOrder,
        Executor: func(ctx context.Context, order *Order, cmd AddItemCommand) error {
            return order.AddItem(cmd.SKU, cmd.Quantity, cmd.Price)
        },
    }))

    // Confirm order
    registry.Register(mink.NewAggregateHandler(mink.AggregateHandlerConfig[ConfirmOrderCommand, *Order]{
        Store:   store,
        Factory: NewOrder,
        Executor: func(ctx context.Context, order *Order, cmd ConfirmOrderCommand) error {
            return order.Confirm()
        },
    }))

    // Cancel order
    registry.Register(mink.NewAggregateHandler(mink.AggregateHandlerConfig[CancelOrderCommand, *Order]{
        Store:   store,
        Factory: NewOrder,
        Executor: func(ctx context.Context, order *Order, cmd CancelOrderCommand) error {
            return order.Cancel(cmd.Reason)
        },
    }))

    return registry
}
```

### Putting It Together

```go
func main() {
    ctx := context.Background()

    // Setup
    store := mink.New(memory.NewAdapter())
    store.RegisterEvents(OrderCreated{}, ItemAdded{}, OrderConfirmed{}, OrderCancelled{})

    registry := setupHandlers(store)

    bus := mink.NewCommandBus(
        mink.WithHandlerRegistry(registry),
        mink.WithMiddleware(
            mink.ValidationMiddleware(),
            mink.RecoveryMiddleware(),
        ),
    )

    // Create order
    result, err := bus.Dispatch(ctx, CreateOrderCommand{CustomerID: "cust-123"})
    if err != nil {
        log.Fatal(err)
    }
    orderID := result.AggregateID
    fmt.Printf("Created order: %s\n", orderID)

    // Add items
    bus.Dispatch(ctx, AddItemCommand{OrderID: orderID, SKU: "LAPTOP", Quantity: 1, Price: 999.99})
    bus.Dispatch(ctx, AddItemCommand{OrderID: orderID, SKU: "MOUSE", Quantity: 2, Price: 29.99})

    // Confirm
    result, err = bus.Dispatch(ctx, ConfirmOrderCommand{OrderID: orderID})
    if err != nil {
        log.Fatal(err)
    }
    fmt.Printf("Order confirmed at version %d\n", result.Version)

    // Load and display
    order := NewOrder(orderID)
    store.LoadAggregate(ctx, order)
    fmt.Printf("Order %s: %d items, $%.2f, status: %s\n",
        order.AggregateID(), len(order.Items), order.Total, order.Status)
}
```

Output:

```
Created order: 550e8400-e29b-41d4-a716-446655440000
Order confirmed at version 4
Order 550e8400-e29b-41d4-a716-446655440000: 2 items, $1059.97, status: confirmed
```

---

## Validation Patterns

### Field Validation

```go
func (c CreateOrderCommand) Validate() error {
    var errs []string

    if c.CustomerID == "" {
        errs = append(errs, "customerID is required")
    }

    if len(c.Items) == 0 {
        errs = append(errs, "at least one item is required")
    }

    for i, item := range c.Items {
        if item.Quantity <= 0 {
            errs = append(errs, fmt.Sprintf("item[%d]: quantity must be positive", i))
        }
    }

    if len(errs) > 0 {
        return fmt.Errorf("validation failed: %s", strings.Join(errs, "; "))
    }

    return nil
}
```

### Using Validation Errors

```go
func (c AddItemCommand) Validate() error {
    if c.Quantity <= 0 {
        return mink.NewValidationError("AddItem", "quantity", "must be positive")
    }
    if c.Price < 0 {
        return mink.NewValidationError("AddItem", "price", "cannot be negative")
    }
    return nil
}

// In handler
result, err := bus.Dispatch(ctx, cmd)
if err != nil {
    var valErr *mink.ValidationError
    if errors.As(err, &valErr) {
        fmt.Printf("Validation error on %s.%s: %s\n",
            valErr.CommandType, valErr.Field, valErr.Message)
    }
}
```

---

## What's Next?

In this post, you learned:

- What CQRS is and why it matters
- The difference between commands and queries
- How to define commands with validation
- Different handler types: function, generic, and aggregate
- How to set up and use the command bus
- A complete CQRS example

In **Part 6**, we'll explore the **middleware system**—adding cross-cutting concerns like logging, metrics, idempotency, and error recovery to your command pipeline.

---

## Key Takeaways

1. **Commands are intentions**: They express what you want to happen
2. **Queries are questions**: They don't change state
3. **Handlers execute commands**: Keep them focused and simple
4. **Aggregate handlers reduce boilerplate**: Automatic load/save lifecycle
5. **Validation belongs in commands**: Self-validating commands are cleaner

---

*Previous: [← Part 4: The Event Store Deep Dive](04-event-store-deep-dive.md)*

*Next: [Part 6: Middleware and Cross-Cutting Concerns →](06-middleware-and-cross-cutting-concerns.md)*
