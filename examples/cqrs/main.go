// Package main demonstrates the CQRS and Command Bus features of go-mink (Phase 2).
//
// This example shows:
// - Command Bus with middleware pipeline
// - Generic and Aggregate command handlers
// - Validation middleware
// - Idempotency for duplicate command detection
// - Correlation and Causation ID tracking
// - Retry middleware for transient failures
// - Logging and Metrics middleware
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

// =============================================================================
// Domain Events
// =============================================================================

// OrderCreated is emitted when a new order is created.
type OrderCreated struct {
	OrderID    string `json:"orderId"`
	CustomerID string `json:"customerId"`
}

// ItemAdded is emitted when an item is added to an order.
type ItemAdded struct {
	OrderID  string  `json:"orderId"`
	SKU      string  `json:"sku"`
	Quantity int     `json:"quantity"`
	Price    float64 `json:"price"`
}

// OrderShipped is emitted when an order is shipped.
type OrderShipped struct {
	OrderID        string `json:"orderId"`
	TrackingNumber string `json:"trackingNumber"`
}

// =============================================================================
// Commands
// =============================================================================

// CreateOrderCommand creates a new order.
type CreateOrderCommand struct {
	mink.CommandBase
	CustomerID string `json:"customerId"`
}

func (c CreateOrderCommand) CommandType() string { return "CreateOrder" }

func (c CreateOrderCommand) Validate() error {
	if c.CustomerID == "" {
		return mink.NewValidationError("CreateOrder", "CustomerID", "customer ID is required")
	}
	return nil
}

// AddItemCommand adds an item to an existing order.
type AddItemCommand struct {
	mink.CommandBase
	OrderID  string  `json:"orderId"`
	SKU      string  `json:"sku"`
	Quantity int     `json:"quantity"`
	Price    float64 `json:"price"`
}

func (c AddItemCommand) CommandType() string   { return "AddItem" }
func (c AddItemCommand) AggregateID() string   { return c.OrderID }
func (c AddItemCommand) AggregateType() string { return "Order" }

func (c AddItemCommand) Validate() error {
	errs := mink.NewMultiValidationError("AddItem")

	if c.OrderID == "" {
		errs.AddField("OrderID", "order ID is required")
	}
	if c.SKU == "" {
		errs.AddField("SKU", "SKU is required")
	}
	if c.Quantity <= 0 {
		errs.AddField("Quantity", "quantity must be positive")
	}
	if c.Price < 0 {
		errs.AddField("Price", "price cannot be negative")
	}

	if errs.HasErrors() {
		return errs
	}
	return nil
}

// ShipOrderCommand ships an order.
type ShipOrderCommand struct {
	mink.CommandBase
	OrderID        string `json:"orderId"`
	TrackingNumber string `json:"trackingNumber"`
}

func (c ShipOrderCommand) CommandType() string   { return "ShipOrder" }
func (c ShipOrderCommand) AggregateID() string   { return c.OrderID }
func (c ShipOrderCommand) AggregateType() string { return "Order" }

func (c ShipOrderCommand) Validate() error {
	errs := mink.NewMultiValidationError("ShipOrder")

	if c.OrderID == "" {
		errs.AddField("OrderID", "order ID is required")
	}
	if c.TrackingNumber == "" {
		errs.AddField("TrackingNumber", "tracking number is required")
	}

	if errs.HasErrors() {
		return errs
	}
	return nil
}

// =============================================================================
// Order Aggregate
// =============================================================================

// OrderItem represents an item in an order.
type OrderItem struct {
	SKU      string
	Quantity int
	Price    float64
}

// Order is our aggregate root.
type Order struct {
	mink.AggregateBase

	CustomerID     string
	Items          []OrderItem
	Status         string
	TrackingNumber string
}

// NewOrder creates a new Order aggregate.
func NewOrder(id string) *Order {
	return &Order{
		AggregateBase: mink.NewAggregateBase(id, "Order"),
		Items:         make([]OrderItem, 0),
	}
}

// Create initializes a new order for a customer.
func (o *Order) Create(customerID string) error {
	if o.Status != "" {
		return errors.New("order already exists")
	}

	o.Apply(OrderCreated{
		OrderID:    o.AggregateID(),
		CustomerID: customerID,
	})
	o.CustomerID = customerID
	o.Status = "Created"
	return nil
}

// AddItem adds an item to the order.
func (o *Order) AddItem(sku string, quantity int, price float64) error {
	if o.Status == "" {
		return errors.New("order not created")
	}
	if o.Status == "Shipped" {
		return errors.New("cannot add items to shipped order")
	}

	o.Apply(ItemAdded{
		OrderID:  o.AggregateID(),
		SKU:      sku,
		Quantity: quantity,
		Price:    price,
	})
	o.Items = append(o.Items, OrderItem{
		SKU:      sku,
		Quantity: quantity,
		Price:    price,
	})
	return nil
}

// Ship marks the order as shipped.
func (o *Order) Ship(trackingNumber string) error {
	if o.Status == "" {
		return errors.New("order not created")
	}
	if o.Status == "Shipped" {
		return errors.New("order already shipped")
	}
	if len(o.Items) == 0 {
		return errors.New("cannot ship empty order")
	}

	o.Apply(OrderShipped{
		OrderID:        o.AggregateID(),
		TrackingNumber: trackingNumber,
	})
	o.Status = "Shipped"
	o.TrackingNumber = trackingNumber
	return nil
}

// TotalAmount calculates the total order amount.
func (o *Order) TotalAmount() float64 {
	total := 0.0
	for _, item := range o.Items {
		total += float64(item.Quantity) * item.Price
	}
	return total
}

// ApplyEvent applies a historical event to rebuild state.
func (o *Order) ApplyEvent(event interface{}) error {
	switch e := event.(type) {
	case OrderCreated:
		o.CustomerID = e.CustomerID
		o.Status = "Created"
	case ItemAdded:
		o.Items = append(o.Items, OrderItem{
			SKU:      e.SKU,
			Quantity: e.Quantity,
			Price:    e.Price,
		})
	case OrderShipped:
		o.Status = "Shipped"
		o.TrackingNumber = e.TrackingNumber
	default:
		return fmt.Errorf("unknown event type: %T", event)
	}
	o.IncrementVersion()
	return nil
}

// =============================================================================
// Custom Logger (implements mink.Logger)
// =============================================================================

type SimpleLogger struct{}

func (l *SimpleLogger) Debug(msg string, args ...interface{}) {
	fmt.Printf("   [DEBUG] %s %v\n", msg, args)
}

func (l *SimpleLogger) Info(msg string, args ...interface{}) {
	fmt.Printf("   [INFO]  %s %v\n", msg, args)
}

func (l *SimpleLogger) Warn(msg string, args ...interface{}) {
	fmt.Printf("   [WARN]  %s %v\n", msg, args)
}

func (l *SimpleLogger) Error(msg string, args ...interface{}) {
	fmt.Printf("   [ERROR] %s %v\n", msg, args)
}

// =============================================================================
// Custom Metrics Collector (implements mink.MetricsCollector)
// =============================================================================

type SimpleMetrics struct {
	commandCounts   map[string]int
	commandDuration map[string]time.Duration
}

func NewSimpleMetrics() *SimpleMetrics {
	return &SimpleMetrics{
		commandCounts:   make(map[string]int),
		commandDuration: make(map[string]time.Duration),
	}
}

func (m *SimpleMetrics) RecordCommand(cmdType string, duration time.Duration, success bool, err error) {
	m.commandCounts[cmdType]++
	m.commandDuration[cmdType] += duration
	status := "success"
	if !success {
		status = "failure"
	}
	key := fmt.Sprintf("%s:%s", cmdType, status)
	m.commandCounts[key]++
}

func (m *SimpleMetrics) PrintStats() {
	fmt.Println("   Command Statistics:")
	for cmdType, count := range m.commandCounts {
		fmt.Printf("      %s: %d\n", cmdType, count)
	}
}

// =============================================================================
// Main
// =============================================================================

func main() {
	ctx := context.Background()

	// Create event store with in-memory adapter
	adapter := memory.NewAdapter()
	store := mink.New(adapter)
	store.RegisterEvents(OrderCreated{}, ItemAdded{}, OrderShipped{})

	// Create idempotency store (in-memory for demo)
	idempotencyStore := memory.NewIdempotencyStore()

	// Create logger and metrics
	logger := &SimpleLogger{}
	metrics := NewSimpleMetrics()

	// Create handler registry
	registry := mink.NewHandlerRegistry()

	// Register CreateOrder handler (generic handler for creating new orders)
	mink.RegisterGenericHandler(registry, func(ctx context.Context, cmd CreateOrderCommand) (mink.CommandResult, error) {
		// Generate a new order ID (in production, use UUID)
		orderID := fmt.Sprintf("order-%d", time.Now().UnixNano())

		order := NewOrder(orderID)
		if err := order.Create(cmd.CustomerID); err != nil {
			return mink.NewErrorResult(err), err
		}

		if err := store.SaveAggregate(ctx, order); err != nil {
			return mink.NewErrorResult(err), err
		}

		return mink.NewSuccessResult(orderID, order.Version()), nil
	})

	// Register AddItem handler (aggregate handler)
	addItemHandler := mink.NewAggregateHandler(mink.AggregateHandlerConfig[AddItemCommand, *Order]{
		Factory: func(id string) *Order { return NewOrder(id) },
		Store:   store,
		Executor: func(ctx context.Context, order *Order, cmd AddItemCommand) error {
			return order.AddItem(cmd.SKU, cmd.Quantity, cmd.Price)
		},
	})
	registry.Register(addItemHandler)

	// Register ShipOrder handler (aggregate handler)
	shipHandler := mink.NewAggregateHandler(mink.AggregateHandlerConfig[ShipOrderCommand, *Order]{
		Factory: func(id string) *Order { return NewOrder(id) },
		Store:   store,
		Executor: func(ctx context.Context, order *Order, cmd ShipOrderCommand) error {
			return order.Ship(cmd.TrackingNumber)
		},
	})
	registry.Register(shipHandler)

	// Create command bus with middleware pipeline
	bus := mink.NewCommandBus(
		mink.WithHandlerRegistry(registry),
		mink.WithMiddleware(
			// Recovery middleware - catches panics
			mink.RecoveryMiddleware(),

			// Logging middleware - logs command execution
			mink.NewLoggingMiddleware(logger).Middleware(),

			// Metrics middleware - records command metrics
			mink.MetricsMiddleware(metrics),

			// Correlation ID middleware - tracks request flow
			mink.CorrelationIDMiddleware(nil), // Uses default UUID generator

			// Causation ID middleware - tracks command chains
			mink.CausationIDMiddleware(),

			// Validation middleware - validates commands
			mink.ValidationMiddleware(),

			// Idempotency middleware - prevents duplicate processing
			mink.IdempotencyMiddleware(mink.IdempotencyConfig{
				Store:        idempotencyStore,
				TTL:          24 * time.Hour,
				KeyGenerator: mink.GenerateIdempotencyKey,
			}),

			// Timeout middleware - enforces command timeout
			mink.TimeoutMiddleware(5*time.Second),
		),
	)

	fmt.Println("=== go-mink CQRS & Command Bus Example (Phase 2) ===")
	fmt.Println()

	// --- 1. Create an Order ---
	fmt.Println("1. Creating a new order with CreateOrderCommand...")
	fmt.Println()

	createCmd := CreateOrderCommand{
		CommandBase: mink.CommandBase{
			CommandID:     "cmd-001",
			CorrelationID: "request-123",
		},
		CustomerID: "customer-456",
	}

	result, err := bus.Dispatch(ctx, createCmd)
	if err != nil {
		log.Fatalf("Failed to create order: %v", err)
	}

	orderID := result.AggregateID
	fmt.Printf("   ✓ Order created: %s (version %d)\n", orderID, result.Version)
	fmt.Println()

	// --- 2. Add Items (with aggregate handler) ---
	fmt.Println("2. Adding items with AddItemCommand...")
	fmt.Println()

	addCmd1 := AddItemCommand{
		CommandBase: mink.CommandBase{
			CommandID:     "cmd-002",
			CorrelationID: "request-123", // Same correlation as create
			CausationID:   "cmd-001",     // Caused by create command
		},
		OrderID:  orderID,
		SKU:      "WIDGET-A",
		Quantity: 2,
		Price:    29.99,
	}

	result, err = bus.Dispatch(ctx, addCmd1)
	if err != nil {
		log.Fatalf("Failed to add item: %v", err)
	}
	fmt.Printf("   ✓ Item added (version %d)\n", result.Version)

	addCmd2 := AddItemCommand{
		CommandBase: mink.CommandBase{
			CommandID:     "cmd-003",
			CorrelationID: "request-123",
			CausationID:   "cmd-002",
		},
		OrderID:  orderID,
		SKU:      "GADGET-B",
		Quantity: 1,
		Price:    149.99,
	}

	result, err = bus.Dispatch(ctx, addCmd2)
	if err != nil {
		log.Fatalf("Failed to add item: %v", err)
	}
	fmt.Printf("   ✓ Item added (version %d)\n", result.Version)
	fmt.Println()

	// --- 3. Demonstrate Validation ---
	fmt.Println("3. Testing validation middleware (invalid command)...")
	fmt.Println()

	invalidCmd := AddItemCommand{
		CommandBase: mink.CommandBase{CommandID: "cmd-invalid"},
		OrderID:     orderID,
		SKU:         "", // Invalid: empty SKU
		Quantity:    -1, // Invalid: negative quantity
		Price:       10.00,
	}

	_, err = bus.Dispatch(ctx, invalidCmd)
	if err != nil {
		var validationErr *mink.MultiValidationError
		if errors.As(err, &validationErr) {
			fmt.Printf("   ✓ Validation failed as expected:\n")
			for _, e := range validationErr.Errors {
				fmt.Printf("      - %s: %s\n", e.Field, e.Message)
			}
		}
	}
	fmt.Println()

	// --- 4. Demonstrate Idempotency ---
	fmt.Println("4. Testing idempotency (duplicate command)...")
	fmt.Println()

	// First dispatch
	idempotentCmd := AddItemCommand{
		CommandBase: mink.CommandBase{
			CommandID:     "cmd-idempotent-001",
			CorrelationID: "request-456",
		},
		OrderID:  orderID,
		SKU:      "UNIQUE-ITEM",
		Quantity: 1,
		Price:    19.99,
	}

	result1, err := bus.Dispatch(ctx, idempotentCmd)
	if err != nil {
		log.Fatalf("First dispatch failed: %v", err)
	}
	fmt.Printf("   First dispatch: version %d\n", result1.Version)

	// Duplicate dispatch (same command ID)
	result2, err := bus.Dispatch(ctx, idempotentCmd)
	if err != nil {
		log.Fatalf("Duplicate dispatch failed: %v", err)
	}
	fmt.Printf("   Duplicate dispatch: version %d (returned cached result)\n", result2.Version)
	fmt.Printf("   ✓ Idempotency working: same result returned for duplicate command\n")
	fmt.Println()

	// --- 5. Ship the Order ---
	fmt.Println("5. Shipping the order with ShipOrderCommand...")
	fmt.Println()

	shipCmd := ShipOrderCommand{
		CommandBase: mink.CommandBase{
			CommandID:     "cmd-004",
			CorrelationID: "request-123",
			CausationID:   "cmd-003",
		},
		OrderID:        orderID,
		TrackingNumber: "TRACK-12345",
	}

	result, err = bus.Dispatch(ctx, shipCmd)
	if err != nil {
		log.Fatalf("Failed to ship order: %v", err)
	}
	fmt.Printf("   ✓ Order shipped (version %d)\n", result.Version)
	fmt.Println()

	// --- 6. Load Final State ---
	fmt.Println("6. Loading final order state...")
	fmt.Println()

	finalOrder := NewOrder(orderID)
	if err := store.LoadAggregate(ctx, finalOrder); err != nil {
		log.Fatalf("Failed to load order: %v", err)
	}

	fmt.Printf("   Order ID: %s\n", finalOrder.AggregateID())
	fmt.Printf("   Customer: %s\n", finalOrder.CustomerID)
	fmt.Printf("   Status: %s\n", finalOrder.Status)
	fmt.Printf("   Tracking: %s\n", finalOrder.TrackingNumber)
	fmt.Printf("   Items:\n")
	for _, item := range finalOrder.Items {
		fmt.Printf("      - %s x%d @ $%.2f\n", item.SKU, item.Quantity, item.Price)
	}
	fmt.Printf("   Total: $%.2f\n", finalOrder.TotalAmount())
	fmt.Printf("   Version: %d\n", finalOrder.Version())
	fmt.Println()

	// --- 7. Show Event Stream with Metadata ---
	fmt.Println("7. Event stream with correlation/causation metadata...")
	fmt.Println()

	events, err := store.LoadRaw(ctx, "Order-"+orderID, 0)
	if err != nil {
		log.Fatalf("Failed to load events: %v", err)
	}

	for _, event := range events {
		fmt.Printf("   [v%d] %s\n", event.Version, event.Type)
		if event.Metadata.CorrelationID != "" {
			fmt.Printf("         CorrelationID: %s\n", event.Metadata.CorrelationID)
		}
		if event.Metadata.CausationID != "" {
			fmt.Printf("         CausationID: %s\n", event.Metadata.CausationID)
		}
	}
	fmt.Println()

	// --- 8. Show Metrics ---
	fmt.Println("8. Command execution metrics...")
	fmt.Println()
	metrics.PrintStats()
	fmt.Println()

	fmt.Println("=== Example Complete ===")
}
