// Package mink provides event sourcing and CQRS primitives for Go applications.
//
// go-mink is an Event Sourcing library for Go that makes it easy to build
// applications using event sourcing patterns. It provides a simple API for
// storing events, loading aggregates, and projecting read models.
//
// # Quick Start
//
// Create an event store with the in-memory adapter for development:
//
//	import (
//	    "github.com/AshkanYarmoradi/go-mink"
//	    "github.com/AshkanYarmoradi/go-mink/adapters/memory"
//	)
//
//	store := mink.New(memory.NewAdapter())
//
// For production, use the PostgreSQL adapter:
//
//	import (
//	    "github.com/AshkanYarmoradi/go-mink"
//	    "github.com/AshkanYarmoradi/go-mink/adapters/postgres"
//	)
//
//	adapter, err := postgres.NewAdapter(ctx, connStr)
//	if err != nil {
//	    log.Fatal(err)
//	}
//	store := mink.New(adapter)
//
// # Defining Events
//
// Events are simple structs that represent something that happened in your domain:
//
//	type OrderCreated struct {
//	    OrderID    string `json:"orderId"`
//	    CustomerID string `json:"customerId"`
//	}
//
//	type ItemAdded struct {
//	    OrderID  string  `json:"orderId"`
//	    SKU      string  `json:"sku"`
//	    Quantity int     `json:"quantity"`
//	    Price    float64 `json:"price"`
//	}
//
// Register events with the store so they can be serialized and deserialized:
//
//	store.RegisterEvents(OrderCreated{}, ItemAdded{})
//
// # Defining Aggregates
//
// Aggregates are domain objects that encapsulate business logic and generate events:
//
//	type Order struct {
//	    mink.AggregateBase
//	    CustomerID string
//	    Items      []OrderItem
//	    Status     string
//	}
//
//	func NewOrder(id string) *Order {
//	    return &Order{
//	        AggregateBase: mink.NewAggregateBase(id, "Order"),
//	    }
//	}
//
//	func (o *Order) Create(customerID string) {
//	    o.Apply(OrderCreated{OrderID: o.AggregateID(), CustomerID: customerID})
//	    o.CustomerID = customerID
//	    o.Status = "Created"
//	}
//
//	func (o *Order) ApplyEvent(event interface{}) error {
//	    switch e := event.(type) {
//	    case OrderCreated:
//	        o.CustomerID = e.CustomerID
//	        o.Status = "Created"
//	    case ItemAdded:
//	        o.Items = append(o.Items, OrderItem{SKU: e.SKU, Quantity: e.Quantity, Price: e.Price})
//	    }
//	    o.IncrementVersion()
//	    return nil
//	}
//
// # Saving and Loading Aggregates
//
// Save aggregates to persist their uncommitted events:
//
//	order := NewOrder("order-123")
//	order.Create("customer-456")
//	order.AddItem("SKU-001", 2, 29.99)
//
//	err := store.SaveAggregate(ctx, order)
//
// Load aggregates to rebuild state from events:
//
//	loaded := NewOrder("order-123")
//	err := store.LoadAggregate(ctx, loaded)
//	// loaded.Status == "Created"
//	// loaded.Items contains the added item
//
// # Low-Level Event Operations
//
// Append events directly to a stream:
//
//	events := []interface{}{
//	    OrderCreated{OrderID: "123", CustomerID: "456"},
//	    ItemAdded{OrderID: "123", SKU: "SKU-001", Quantity: 2, Price: 29.99},
//	}
//	err := store.Append(ctx, "Order-123", events)
//
// Load events from a stream:
//
//	events, err := store.Load(ctx, "Order-123")
//
// # Optimistic Concurrency
//
// Use expected versions to prevent concurrent modifications:
//
//	// Create new stream (must not exist)
//	err := store.Append(ctx, "Order-123", events, mink.ExpectVersion(mink.NoStream))
//
//	// Append to existing stream at specific version
//	err := store.Append(ctx, "Order-123", events, mink.ExpectVersion(1))
//
// Version constants:
//   - AnyVersion (-1): Skip version check
//   - NoStream (0): Stream must not exist
//   - StreamExists (-2): Stream must exist
//
// # Metadata
//
// Add metadata to events for tracing and multi-tenancy:
//
//	metadata := mink.Metadata{}.
//	    WithUserID("user-123").
//	    WithCorrelationID("corr-456").
//	    WithTenantID("tenant-789")
//
//	err := store.Append(ctx, "Order-123", events, mink.WithAppendMetadata(metadata))
//
// # Commands and CQRS (v0.2.0)
//
// Define commands to encapsulate user intentions:
//
//	type CreateOrder struct {
//	    mink.CommandBase
//	    CustomerID string `json:"customerId"`
//	}
//
//	func (c CreateOrder) CommandType() string { return "CreateOrder" }
//	func (c CreateOrder) Validate() error {
//	    if c.CustomerID == "" {
//	        return mink.NewValidationError("CustomerID", "required")
//	    }
//	    return nil
//	}
//
// Create a command bus with middleware:
//
//	bus := mink.NewCommandBus()
//	bus.Use(mink.ValidationMiddleware())
//	bus.Use(mink.RecoveryMiddleware(func(err error) { log.Error(err) }))
//	bus.Use(mink.LoggingMiddleware(logger, nil))
//
// Register command handlers:
//
//	bus.Register("CreateOrder", func(ctx context.Context, cmd mink.Command) (mink.CommandResult, error) {
//	    c := cmd.(CreateOrder)
//	    order := NewOrder(uuid.New().String())
//	    order.Create(c.CustomerID)
//	    if err := store.SaveAggregate(ctx, order); err != nil {
//	        return mink.NewErrorResult(err), err
//	    }
//	    return mink.NewSuccessResult(order.AggregateID(), order.Version()), nil
//	})
//
// Dispatch commands:
//
//	result, err := bus.Dispatch(ctx, CreateOrder{CustomerID: "cust-123"})
//
// # Idempotency
//
// Prevent duplicate command processing with idempotency:
//
//	idempotencyStore := memory.NewIdempotencyStore()
//	config := mink.DefaultIdempotencyConfig(idempotencyStore)
//	bus.Use(mink.IdempotencyMiddleware(config))
//
// Make commands idempotent by implementing IdempotentCommand:
//
//	func (c CreateOrder) IdempotencyKey() string { return c.RequestID }
package mink

// Version returns the library version string.
func Version() string {
	return "0.2.0"
}

// BuildStreamID creates a stream ID from an aggregate type and ID.
// This follows the convention: "{Type}-{ID}"
func BuildStreamID(aggregateType, aggregateID string) string {
	return aggregateType + "-" + aggregateID
}
