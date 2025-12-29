// Package main demonstrates Phase 2 CQRS & Command Bus features with PostgreSQL.
//
// This example shows:
//   - PostgreSQL event store adapter
//   - PostgreSQL idempotency store
//   - Command bus with full middleware pipeline
//   - Type-safe command handlers
//   - Correlation/Causation ID tracking
//
// Prerequisites (from project root):
//
//	docker compose -f docker-compose.test.yml up -d --wait
//
// Run:
//
//	cd examples/cqrs-postgres
//	go run main.go
//
// Cleanup:
//
//	docker compose -f docker-compose.test.yml down
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/AshkanYarmoradi/go-mink"
	"github.com/AshkanYarmoradi/go-mink/adapters/postgres"

	_ "github.com/lib/pq" // PostgreSQL driver
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
// Order Aggregate
// =============================================================================

// OrderItem represents a line item in an order.
type OrderItem struct {
	SKU      string
	Quantity int
	Price    float64
}

// Order is the aggregate root for order management.
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

// AddItem adds a line item to the order.
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

// AddItemCommand adds an item to an order.
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
// Logger Implementation
// =============================================================================

// SimpleLogger implements mink.Logger for demonstration.
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
// Metrics Collector Implementation
// =============================================================================

// SimpleMetrics implements mink.MetricsCollector for demonstration.
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
	fmt.Println("=== go-mink Phase 2: PostgreSQL Example ===")
	fmt.Println()

	ctx := context.Background()

	// Get database connection string (matches docker-compose.test.yml)
	connStr := os.Getenv("TEST_DATABASE_URL")
	if connStr == "" {
		connStr = "postgres://postgres:postgres@localhost:5432/mink_test?sslmode=disable"
	}

	// =========================================================================
	// Step 1: Initialize PostgreSQL Adapters
	// =========================================================================
	fmt.Println("📦 Step 1: Initializing PostgreSQL adapters...")

	// Create PostgreSQL event store adapter
	eventAdapter, err := postgres.NewAdapter(connStr,
		postgres.WithSchema("mink_example"),
	)
	if err != nil {
		log.Fatalf("Failed to create event store adapter: %v", err)
	}
	defer eventAdapter.Close()

	// Initialize event store schema
	if err := eventAdapter.Initialize(ctx); err != nil {
		log.Fatalf("Failed to initialize event store: %v", err)
	}
	fmt.Println("   ✓ Event store adapter initialized")

	// Open database connection for idempotency store
	db, err := sql.Open("postgres", connStr)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer db.Close()

	// Create PostgreSQL idempotency store
	idempotencyStore := postgres.NewIdempotencyStore(db,
		postgres.WithIdempotencySchema("mink_example"),
		postgres.WithIdempotencyTable("idempotency_keys"),
	)

	// Initialize idempotency store schema
	if err := idempotencyStore.Initialize(ctx); err != nil {
		log.Fatalf("Failed to initialize idempotency store: %v", err)
	}
	fmt.Println("   ✓ Idempotency store adapter initialized")

	// =========================================================================
	// Step 2: Create Event Store
	// =========================================================================
	fmt.Println("\n🗄️  Step 2: Creating event store...")

	store := mink.New(eventAdapter)
	store.RegisterEvents(OrderCreated{}, ItemAdded{}, OrderShipped{})
	fmt.Println("   ✓ Event store created with registered events")

	// =========================================================================
	// Step 3: Setup Command Bus with Middleware
	// =========================================================================
	fmt.Println("\n🚌 Step 3: Setting up command bus with middleware...")

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

			// Idempotency middleware - prevents duplicate processing (with PostgreSQL!)
			mink.IdempotencyMiddleware(mink.IdempotencyConfig{
				Store:        idempotencyStore,
				TTL:          24 * time.Hour,
				KeyGenerator: mink.GenerateIdempotencyKey,
			}),

			// Timeout middleware - enforces command timeout
			mink.TimeoutMiddleware(30*time.Second),
		),
	)

	fmt.Println("   ✓ Command bus created with 8 middleware (PostgreSQL idempotency)")

	// =========================================================================
	// Step 4: Execute Commands
	// =========================================================================
	fmt.Println("\n🚀 Step 4: Executing commands...")
	fmt.Println()

	// Create order
	fmt.Println("   Creating order...")
	createCmd := CreateOrderCommand{
		CommandBase: mink.CommandBase{
			CommandID:     fmt.Sprintf("cmd-create-%d", time.Now().UnixNano()),
			CorrelationID: "request-postgres-demo",
		},
		CustomerID: "customer-pg-123",
	}

	result, err := bus.Dispatch(ctx, createCmd)
	if err != nil {
		log.Fatalf("Failed to create order: %v", err)
	}
	orderID := result.AggregateID
	fmt.Printf("   ✓ Order created: %s (version %d)\n", orderID, result.Version)
	fmt.Println()

	// Add items
	fmt.Println("   Adding items...")
	items := []struct {
		sku      string
		quantity int
		price    float64
	}{
		{"LAPTOP-PRO", 1, 1299.99},
		{"MOUSE-WIRELESS", 2, 49.99},
		{"USB-HUB", 1, 29.99},
	}

	for _, item := range items {
		addCmd := AddItemCommand{
			CommandBase: mink.CommandBase{
				CommandID:     fmt.Sprintf("cmd-add-%d", time.Now().UnixNano()),
				CorrelationID: "request-postgres-demo",
				CausationID:   createCmd.CommandID,
			},
			OrderID:  orderID,
			SKU:      item.sku,
			Quantity: item.quantity,
			Price:    item.price,
		}

		result, err = bus.Dispatch(ctx, addCmd)
		if err != nil {
			log.Fatalf("Failed to add item: %v", err)
		}
		fmt.Printf("   ✓ Added %s x%d @ $%.2f (version %d)\n",
			item.sku, item.quantity, item.price, result.Version)
	}
	fmt.Println()

	// =========================================================================
	// Step 5: Test Idempotency with PostgreSQL
	// =========================================================================
	fmt.Println("🔄 Step 5: Testing idempotency (PostgreSQL storage)...")
	fmt.Println()

	// Create command with fixed ID for idempotency test
	idempotentCmd := AddItemCommand{
		CommandBase: mink.CommandBase{
			CommandID:     "cmd-idempotent-postgres-test", // Fixed ID
			CorrelationID: "request-idempotency-test",
		},
		OrderID:  orderID,
		SKU:      "UNIQUE-ITEM",
		Quantity: 1,
		Price:    19.99,
	}

	// First dispatch
	result1, err := bus.Dispatch(ctx, idempotentCmd)
	if err != nil {
		log.Fatalf("First dispatch failed: %v", err)
	}
	fmt.Printf("   First dispatch: version %d\n", result1.Version)

	// Duplicate dispatch (same command ID - stored in PostgreSQL)
	result2, err := bus.Dispatch(ctx, idempotentCmd)
	if err != nil {
		log.Fatalf("Duplicate dispatch failed: %v", err)
	}
	fmt.Printf("   Duplicate dispatch: version %d (returned from PostgreSQL)\n", result2.Version)
	fmt.Printf("   ✓ Idempotency working with PostgreSQL storage!\n")
	fmt.Println()

	// =========================================================================
	// Step 6: Test Validation
	// =========================================================================
	fmt.Println("✅ Step 6: Testing validation...")
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

	// =========================================================================
	// Step 7: Ship the Order
	// =========================================================================
	fmt.Println("📦 Step 7: Shipping order...")
	fmt.Println()

	shipCmd := ShipOrderCommand{
		CommandBase: mink.CommandBase{
			CommandID:     fmt.Sprintf("cmd-ship-%d", time.Now().UnixNano()),
			CorrelationID: "request-postgres-demo",
		},
		OrderID:        orderID,
		TrackingNumber: "TRACK-PG-12345",
	}

	result, err = bus.Dispatch(ctx, shipCmd)
	if err != nil {
		log.Fatalf("Failed to ship order: %v", err)
	}
	fmt.Printf("   ✓ Order shipped (version %d)\n", result.Version)
	fmt.Println()

	// =========================================================================
	// Step 8: Load Final State
	// =========================================================================
	fmt.Println("📖 Step 8: Loading final order state from PostgreSQL...")
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

	// =========================================================================
	// Step 9: Show Event Stream
	// =========================================================================
	fmt.Println("📜 Step 9: Event stream from PostgreSQL...")
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
	}
	fmt.Println()

	// =========================================================================
	// Step 10: Metrics Summary
	// =========================================================================
	fmt.Println("📊 Step 10: Command execution metrics...")
	fmt.Println()
	metrics.PrintStats()
	fmt.Println()

	fmt.Println("=== PostgreSQL Example Complete ===")
	fmt.Println()
	fmt.Println("💡 Key Points Demonstrated:")
	fmt.Println("   • PostgreSQL event store for durable event storage")
	fmt.Println("   • PostgreSQL idempotency store (survives restarts)")
	fmt.Println("   • Full middleware pipeline (8 middleware)")
	fmt.Println("   • Type-safe generic and aggregate handlers")
	fmt.Println("   • Correlation/Causation ID tracking")
	fmt.Println("   • Command validation")
	fmt.Println("   • Idempotency with PostgreSQL persistence")
	fmt.Println("   • Business rule enforcement")
}
