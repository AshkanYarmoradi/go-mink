# Part 3: Building Your First Aggregate

*This is Part 3 of an 8-part series on Event Sourcing and CQRS with Go. In this post, we'll learn about Aggregates—the heart of domain-driven design and event sourcing.*

---

## The Problem with Raw Events

In Part 2, we manually rebuilt state by loading events and switching on their types:

```go
func rebuildTask(events []mink.Event) *Task {
    task := &Task{}
    for _, event := range events {
        switch e := event.Data.(type) {
        case TaskCreated:
            task.ID = e.TaskID
            task.Title = e.Title
            // ... more fields
        case TaskCompleted:
            task.IsCompleted = true
            // ... more fields
        }
    }
    return task
}
```

This works, but has problems:

1. **Scattered logic**: State rebuilding is separate from business operations
2. **No encapsulation**: Anyone can create events without validation
3. **No invariants**: Business rules aren't enforced
4. **Duplication**: The switch statement appears everywhere

We need something better: **Aggregates**.

---

## What is an Aggregate?

An **Aggregate** is a cluster of domain objects that can be treated as a single unit. In event sourcing, an aggregate:

- **Encapsulates state** and the rules that govern it
- **Produces events** when its state changes
- **Rebuilds from events** during loading
- **Enforces invariants** (business rules that must always be true)

Think of an aggregate as the *guardian* of a consistency boundary. All modifications go through the aggregate, and it ensures the rules are followed.

---

## The Aggregate Interface

go-mink defines the aggregate contract:

```go
type Aggregate interface {
    AggregateID() string              // Unique identifier
    AggregateType() string            // Category name (e.g., "Order", "Task")
    Version() int64                   // Current version (event count)
    ApplyEvent(event interface{}) error  // Rebuild state from one event
    UncommittedEvents() []interface{} // Events not yet saved
    ClearUncommittedEvents()          // Called after successful save
}
```

You *could* implement all these methods yourself, but go-mink provides `AggregateBase` to handle the boilerplate.

---

## Your First Aggregate: Shopping Cart

Let's build a shopping cart aggregate step by step.

### Step 1: Define the Events

```go
package cart

import "time"

// CartCreated is raised when a new cart is created
type CartCreated struct {
    CartID     string `json:"cartId"`
    CustomerID string `json:"customerId"`
}

// ItemAdded is raised when an item is added to the cart
type ItemAdded struct {
    SKU      string  `json:"sku"`
    Name     string  `json:"name"`
    Quantity int     `json:"quantity"`
    Price    float64 `json:"price"`
}

// ItemRemoved is raised when an item is removed from the cart
type ItemRemoved struct {
    SKU string `json:"sku"`
}

// CartCheckedOut is raised when the cart is converted to an order
type CartCheckedOut struct {
    OrderID    string    `json:"orderId"`
    CheckedOut time.Time `json:"checkedOutAt"`
}
```

### Step 2: Define the Aggregate State

```go
package cart

import "github.com/AshkanYarmoradi/go-mink"

// CartItem represents an item in the cart
type CartItem struct {
    SKU      string
    Name     string
    Quantity int
    Price    float64
}

// Cart is the shopping cart aggregate
type Cart struct {
    mink.AggregateBase          // Embed the base implementation

    CustomerID   string
    Items        map[string]*CartItem  // SKU -> Item
    IsCheckedOut bool
    OrderID      string
}

// NewCart creates a new cart aggregate
func NewCart(id string) *Cart {
    return &Cart{
        AggregateBase: mink.NewAggregateBase(id, "Cart"),
        Items:         make(map[string]*CartItem),
    }
}
```

Key points:
- Embed `mink.AggregateBase` to get the common functionality
- Use `mink.NewAggregateBase(id, type)` to initialize it
- The type name `"Cart"` becomes part of the stream ID

### Step 3: Implement ApplyEvent

The `ApplyEvent` method rebuilds state from a single event. It's called:
- During loading (replay)
- Immediately after recording a new event

```go
// ApplyEvent updates state based on a single event
func (c *Cart) ApplyEvent(event interface{}) error {
    switch e := event.(type) {
    case CartCreated:
        c.CustomerID = e.CustomerID

    case ItemAdded:
        if item, exists := c.Items[e.SKU]; exists {
            item.Quantity += e.Quantity
        } else {
            c.Items[e.SKU] = &CartItem{
                SKU:      e.SKU,
                Name:     e.Name,
                Quantity: e.Quantity,
                Price:    e.Price,
            }
        }

    case ItemRemoved:
        delete(c.Items, e.SKU)

    case CartCheckedOut:
        c.IsCheckedOut = true
        c.OrderID = e.OrderID
    }

    c.IncrementVersion()
    return nil
}
```

**Critical rule**: `ApplyEvent` must be:
- **Deterministic**: Same event always produces same state change
- **Side-effect free**: No I/O, no external calls
- **Idempotent**: Can be called multiple times safely

### Step 4: Implement Business Methods

Now the interesting part—business operations that produce events:

```go
import (
    "errors"
    "fmt"
    "time"
)

var (
    ErrCartAlreadyExists = errors.New("cart already exists")
    ErrCartEmpty         = errors.New("cart is empty")
    ErrCartCheckedOut    = errors.New("cart is already checked out")
    ErrItemNotFound      = errors.New("item not found in cart")
)

// Create initializes a new cart
func (c *Cart) Create(customerID string) error {
    // Invariant: Can only create once
    if c.Version() > 0 {
        return ErrCartAlreadyExists
    }

    // Record the event
    c.Apply(CartCreated{
        CartID:     c.AggregateID(),
        CustomerID: customerID,
    })

    return nil
}

// AddItem adds an item to the cart
func (c *Cart) AddItem(sku, name string, quantity int, price float64) error {
    // Invariant: Cannot modify checked-out cart
    if c.IsCheckedOut {
        return ErrCartCheckedOut
    }

    // Validation
    if quantity <= 0 {
        return fmt.Errorf("quantity must be positive, got %d", quantity)
    }
    if price < 0 {
        return fmt.Errorf("price cannot be negative, got %.2f", price)
    }

    // Record the event
    c.Apply(ItemAdded{
        SKU:      sku,
        Name:     name,
        Quantity: quantity,
        Price:    price,
    })

    return nil
}

// RemoveItem removes an item from the cart
func (c *Cart) RemoveItem(sku string) error {
    if c.IsCheckedOut {
        return ErrCartCheckedOut
    }

    // Invariant: Item must exist
    if _, exists := c.Items[sku]; !exists {
        return ErrItemNotFound
    }

    c.Apply(ItemRemoved{SKU: sku})

    return nil
}

// Checkout converts the cart to an order
func (c *Cart) Checkout(orderID string) error {
    if c.IsCheckedOut {
        return ErrCartCheckedOut
    }

    // Invariant: Cannot checkout empty cart
    if len(c.Items) == 0 {
        return ErrCartEmpty
    }

    c.Apply(CartCheckedOut{
        OrderID:    orderID,
        CheckedOut: time.Now(),
    })

    return nil
}

// Total calculates the cart total
func (c *Cart) Total() float64 {
    var total float64
    for _, item := range c.Items {
        total += item.Price * float64(item.Quantity)
    }
    return total
}
```

Notice the pattern in each method:
1. **Check invariants** — Is this operation allowed?
2. **Validate input** — Is the data correct?
3. **Apply event** — Record what happened
4. **Return** — Success or error

The `Apply()` method (from `AggregateBase`) does two things:
- Adds the event to uncommitted events
- Calls `ApplyEvent()` to update state immediately

---

## Using the Aggregate

### Creating and Saving

```go
ctx := context.Background()

// Create a new cart
cart := cart.NewCart("cart-001")
if err := cart.Create("customer-123"); err != nil {
    log.Fatal(err)
}

// Add some items
cart.AddItem("LAPTOP-01", "MacBook Pro", 1, 2499.00)
cart.AddItem("MOUSE-01", "Magic Mouse", 2, 99.00)

// Save to the store
if err := store.SaveAggregate(ctx, cart); err != nil {
    log.Fatal(err)
}

fmt.Printf("Cart saved with %d events, version %d\n",
    len(cart.UncommittedEvents()), cart.Version())
```

### Loading and Modifying

```go
// Create an empty cart with the same ID
cart := cart.NewCart("cart-001")

// Load replays all events to rebuild state
if err := store.LoadAggregate(ctx, cart); err != nil {
    log.Fatal(err)
}

fmt.Printf("Loaded cart with %d items, version %d\n",
    len(cart.Items), cart.Version())

// Modify
cart.RemoveItem("MOUSE-01")
cart.Checkout("order-456")

// Save the new events
if err := store.SaveAggregate(ctx, cart); err != nil {
    log.Fatal(err)
}
```

---

## The Apply Pattern

A key pattern in event-sourced aggregates is the separation between:

1. **Business methods** — Validate, enforce invariants, call `Apply()`
2. **ApplyEvent** — Update state based on events

```
┌────────────────────────────────────────────────────────┐
│                   Business Method                       │
│  1. Validate input                                      │
│  2. Check invariants                                    │
│  3. Call Apply(event) ──────────────┐                  │
│  4. Return success/error            │                  │
└─────────────────────────────────────│──────────────────┘
                                      │
                                      ▼
┌────────────────────────────────────────────────────────┐
│                     Apply()                             │
│  1. Add event to uncommittedEvents                      │
│  2. Call ApplyEvent(event) ─────────┐                  │
└─────────────────────────────────────│──────────────────┘
                                      │
                                      ▼
┌────────────────────────────────────────────────────────┐
│                   ApplyEvent()                          │
│  1. Switch on event type                                │
│  2. Update internal state                               │
│  3. Increment version                                   │
└────────────────────────────────────────────────────────┘
```

Why this separation?

- `ApplyEvent` is also called during **loading** to replay history
- Business methods are only called for **new** operations
- Invariants are checked in business methods, not `ApplyEvent`

---

## Handling Concurrency

When you save an aggregate, go-mink uses the version for optimistic concurrency:

```go
cart := cart.NewCart("cart-001")
store.LoadAggregate(ctx, cart)  // Version is now 3

cart.AddItem("GADGET-01", "Widget", 1, 50.00)

// Meanwhile, another process also modified the cart...

err := store.SaveAggregate(ctx, cart)
if errors.Is(err, mink.ErrConcurrencyConflict) {
    // Reload and retry
    cart = cart.NewCart("cart-001")
    store.LoadAggregate(ctx, cart)
    // Try the operation again
}
```

`SaveAggregate` expects the stream version to match the aggregate's version when loaded. If someone else appended events in between, you get a conflict.

---

## Common Patterns

### Factory Functions

Always use factory functions to create aggregates:

```go
// Good: Factory function
func NewCart(id string) *Cart {
    return &Cart{
        AggregateBase: mink.NewAggregateBase(id, "Cart"),
        Items:         make(map[string]*CartItem),
    }
}

// Bad: Direct construction
cart := &Cart{ID: "cart-001"}  // Missing required initialization
```

### Private State, Public Events

State fields should be private; only events are public:

```go
type Cart struct {
    mink.AggregateBase
    customerID   string                // private
    items        map[string]*CartItem  // private
    isCheckedOut bool                  // private
}

// Accessor methods if needed
func (c *Cart) CustomerID() string { return c.customerID }
func (c *Cart) ItemCount() int     { return len(c.items) }
func (c *Cart) IsCheckedOut() bool { return c.isCheckedOut }
```

This prevents external code from bypassing business logic.

### Aggregate-Specific Errors

Define clear domain errors:

```go
var (
    ErrCartEmpty       = errors.New("cart is empty")
    ErrCartCheckedOut  = errors.New("cart is already checked out")
    ErrItemNotFound    = errors.New("item not found")
    ErrInsufficientQty = errors.New("insufficient quantity")
)
```

These are more meaningful than generic errors and help with error handling.

---

## Testing Aggregates

Aggregates are pure logic—perfect for testing:

```go
func TestCart_AddItem(t *testing.T) {
    cart := NewCart("test-cart")
    cart.Create("customer-1")

    err := cart.AddItem("SKU-1", "Widget", 2, 10.00)

    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if len(cart.Items) != 1 {
        t.Errorf("expected 1 item, got %d", len(cart.Items))
    }
    if cart.Items["SKU-1"].Quantity != 2 {
        t.Errorf("expected quantity 2, got %d", cart.Items["SKU-1"].Quantity)
    }
}

func TestCart_CannotCheckoutEmpty(t *testing.T) {
    cart := NewCart("test-cart")
    cart.Create("customer-1")

    err := cart.Checkout("order-1")

    if !errors.Is(err, ErrCartEmpty) {
        t.Errorf("expected ErrCartEmpty, got %v", err)
    }
}
```

### BDD-Style Testing with go-mink

go-mink provides helpers for behavior-driven testing:

```go
func TestCart_Checkout(t *testing.T) {
    mink.Given(t, NewCart("cart-1"),
        CartCreated{CartID: "cart-1", CustomerID: "cust-1"},
        ItemAdded{SKU: "SKU-1", Name: "Widget", Quantity: 1, Price: 10.00},
    ).
    When(func(c *Cart) error {
        return c.Checkout("order-1")
    }).
    Then(
        CartCheckedOut{OrderID: "order-1"},
    )
}
```

This reads naturally:
- **Given** this history of events...
- **When** we perform this action...
- **Then** expect these events to be produced

---

## Complete Example

Here's a complete, runnable example:

```go
package main

import (
    "context"
    "errors"
    "fmt"
    "log"
    "time"

    "github.com/AshkanYarmoradi/go-mink"
    "github.com/AshkanYarmoradi/go-mink/adapters/memory"
)

// Events
type CartCreated struct {
    CartID     string `json:"cartId"`
    CustomerID string `json:"customerId"`
}

type ItemAdded struct {
    SKU      string  `json:"sku"`
    Quantity int     `json:"quantity"`
    Price    float64 `json:"price"`
}

type CartCheckedOut struct {
    OrderID    string    `json:"orderId"`
    CheckedOut time.Time `json:"checkedOutAt"`
}

// Aggregate
type Cart struct {
    mink.AggregateBase
    CustomerID   string
    Items        map[string]int
    IsCheckedOut bool
}

func NewCart(id string) *Cart {
    return &Cart{
        AggregateBase: mink.NewAggregateBase(id, "Cart"),
        Items:         make(map[string]int),
    }
}

func (c *Cart) ApplyEvent(event interface{}) error {
    switch e := event.(type) {
    case CartCreated:
        c.CustomerID = e.CustomerID
    case ItemAdded:
        c.Items[e.SKU] += e.Quantity
    case CartCheckedOut:
        c.IsCheckedOut = true
    }
    c.IncrementVersion()
    return nil
}

func (c *Cart) Create(customerID string) error {
    if c.Version() > 0 {
        return errors.New("cart already exists")
    }
    c.Apply(CartCreated{CartID: c.AggregateID(), CustomerID: customerID})
    return nil
}

func (c *Cart) AddItem(sku string, qty int, price float64) error {
    if c.IsCheckedOut {
        return errors.New("cart is checked out")
    }
    c.Apply(ItemAdded{SKU: sku, Quantity: qty, Price: price})
    return nil
}

func (c *Cart) Checkout(orderID string) error {
    if c.IsCheckedOut {
        return errors.New("already checked out")
    }
    if len(c.Items) == 0 {
        return errors.New("cart is empty")
    }
    c.Apply(CartCheckedOut{OrderID: orderID, CheckedOut: time.Now()})
    return nil
}

func main() {
    ctx := context.Background()

    // Setup
    store := mink.New(memory.NewAdapter())
    store.RegisterEvents(CartCreated{}, ItemAdded{}, CartCheckedOut{})

    // Create and populate cart
    cart := NewCart("cart-001")
    cart.Create("customer-123")
    cart.AddItem("LAPTOP", 1, 999.99)
    cart.AddItem("MOUSE", 2, 29.99)

    if err := store.SaveAggregate(ctx, cart); err != nil {
        log.Fatal(err)
    }
    fmt.Printf("Saved cart at version %d\n", cart.Version())

    // Load cart fresh
    loaded := NewCart("cart-001")
    if err := store.LoadAggregate(ctx, loaded); err != nil {
        log.Fatal(err)
    }
    fmt.Printf("Loaded cart: %d items, customer %s\n",
        len(loaded.Items), loaded.CustomerID)

    // Checkout
    loaded.Checkout("order-789")
    if err := store.SaveAggregate(ctx, loaded); err != nil {
        log.Fatal(err)
    }
    fmt.Printf("Cart checked out, final version %d\n", loaded.Version())
}
```

---

## What's Next?

In this post, you learned:

- What aggregates are and why they matter
- How to implement the `Aggregate` interface using `AggregateBase`
- The separation between business methods and `ApplyEvent`
- How to enforce invariants and validation
- Testing patterns for aggregates

In **Part 4**, we'll dive deeper into the **Event Store**, exploring advanced features like stream metadata, subscriptions, and handling large event streams.

---

## Key Takeaways

1. **Aggregates encapsulate logic**: State + behavior + invariants
2. **Business methods produce events**: Validate, then Apply()
3. **ApplyEvent rebuilds state**: Pure, deterministic, no I/O
4. **Version enables concurrency**: Optimistic locking built-in
5. **Test business logic directly**: Aggregates are pure and testable

---

*Previous: [← Part 2: Getting Started with go-mink](02-getting-started-with-go-mink.md)*

*Next: [Part 4: The Event Store Deep Dive →](04-event-store-deep-dive.md)*
