// Package main demonstrates a complete e-commerce order fulfillment system using go-mink.
//
// This example showcases all major features of the go-mink library:
//   - Event Sourcing with PostgreSQL persistence
//   - CQRS with Command Bus and middleware
//   - Projections for read models
//   - Saga pattern for long-running business processes
//   - Optimistic concurrency control
//   - Full lifecycle management
//
// Prerequisites (from project root):
//
//	docker compose -f docker-compose.test.yml up -d --wait
//
// Run:
//
//	cd examples/full-ecommerce
//	go run main.go
//
// Cleanup:
//
//	docker compose -f docker-compose.test.yml down
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/AshkanYarmoradi/go-mink"
	"github.com/AshkanYarmoradi/go-mink/adapters/postgres"

	_ "github.com/lib/pq"
)

// =============================================================================
// DOMAIN EVENTS
// =============================================================================

// Order Events
type OrderCreated struct {
	OrderID    string    `json:"orderId"`
	CustomerID string    `json:"customerId"`
	CreatedAt  time.Time `json:"createdAt"`
}

type ItemAdded struct {
	OrderID  string  `json:"orderId"`
	SKU      string  `json:"sku"`
	Name     string  `json:"name"`
	Quantity int     `json:"quantity"`
	Price    float64 `json:"price"`
}

type OrderSubmitted struct {
	OrderID     string    `json:"orderId"`
	TotalAmount float64   `json:"totalAmount"`
	SubmittedAt time.Time `json:"submittedAt"`
}

type OrderShipped struct {
	OrderID        string    `json:"orderId"`
	TrackingNumber string    `json:"trackingNumber"`
	ShippedAt      time.Time `json:"shippedAt"`
}

type OrderCompleted struct {
	OrderID     string    `json:"orderId"`
	CompletedAt time.Time `json:"completedAt"`
}

type OrderCancelled struct {
	OrderID     string    `json:"orderId"`
	Reason      string    `json:"reason"`
	CancelledAt time.Time `json:"cancelledAt"`
}

// Payment Events
type PaymentProcessed struct {
	OrderID       string    `json:"orderId"`
	TransactionID string    `json:"transactionId"`
	Amount        float64   `json:"amount"`
	ProcessedAt   time.Time `json:"processedAt"`
}

type PaymentFailed struct {
	OrderID  string    `json:"orderId"`
	Reason   string    `json:"reason"`
	FailedAt time.Time `json:"failedAt"`
}

type PaymentRefunded struct {
	OrderID    string    `json:"orderId"`
	Amount     float64   `json:"amount"`
	RefundedAt time.Time `json:"refundedAt"`
}

// Inventory Events
type InventoryReserved struct {
	OrderID       string   `json:"orderId"`
	Items         []string `json:"items"`
	ReservationID string   `json:"reservationId"`
}

type InventoryReservationFailed struct {
	OrderID string `json:"orderId"`
	Reason  string `json:"reason"`
}

type InventoryReleased struct {
	OrderID       string `json:"orderId"`
	ReservationID string `json:"reservationId"`
}

// =============================================================================
// ORDER AGGREGATE
// =============================================================================

type OrderItem struct {
	SKU      string
	Name     string
	Quantity int
	Price    float64
}

type Order struct {
	mink.AggregateBase
	CustomerID     string
	Items          []OrderItem
	Status         string
	TotalAmount    float64
	TrackingNumber string
}

func NewOrder(id string) *Order {
	return &Order{
		AggregateBase: mink.NewAggregateBase(id, "Order"),
		Items:         make([]OrderItem, 0),
		Status:        "",
	}
}

// ApplyEvent applies a domain event to update aggregate state.
func (o *Order) ApplyEvent(event interface{}) error {
	switch e := event.(type) {
	case OrderCreated:
		o.CustomerID = e.CustomerID
		o.Status = "Created"
	case ItemAdded:
		o.Items = append(o.Items, OrderItem{
			SKU:      e.SKU,
			Name:     e.Name,
			Quantity: e.Quantity,
			Price:    e.Price,
		})
	case OrderSubmitted:
		o.Status = "Submitted"
		o.TotalAmount = e.TotalAmount
	case OrderShipped:
		o.Status = "Shipped"
		o.TrackingNumber = e.TrackingNumber
	case OrderCompleted:
		o.Status = "Completed"
	case OrderCancelled:
		o.Status = "Cancelled"
	}
	return nil
}

// Command methods
func (o *Order) Create(customerID string) error {
	if o.Status != "" {
		return errors.New("order already exists")
	}
	o.Apply(OrderCreated{
		OrderID:    o.AggregateID(),
		CustomerID: customerID,
		CreatedAt:  time.Now(),
	})
	o.CustomerID = customerID
	o.Status = "Created"
	return nil
}

func (o *Order) AddItem(sku, name string, quantity int, price float64) error {
	if o.Status == "" {
		return errors.New("order not created")
	}
	if o.Status != "Created" {
		return errors.New("cannot add items: order already submitted")
	}
	o.Apply(ItemAdded{
		OrderID:  o.AggregateID(),
		SKU:      sku,
		Name:     name,
		Quantity: quantity,
		Price:    price,
	})
	o.Items = append(o.Items, OrderItem{SKU: sku, Name: name, Quantity: quantity, Price: price})
	return nil
}

func (o *Order) Submit() error {
	if o.Status != "Created" {
		return errors.New("can only submit created orders")
	}
	if len(o.Items) == 0 {
		return errors.New("cannot submit empty order")
	}
	total := 0.0
	for _, item := range o.Items {
		total += float64(item.Quantity) * item.Price
	}
	o.Apply(OrderSubmitted{
		OrderID:     o.AggregateID(),
		TotalAmount: total,
		SubmittedAt: time.Now(),
	})
	o.Status = "Submitted"
	o.TotalAmount = total
	return nil
}

func (o *Order) Ship(trackingNumber string) error {
	if o.Status != "Submitted" {
		return errors.New("can only ship submitted orders")
	}
	o.Apply(OrderShipped{
		OrderID:        o.AggregateID(),
		TrackingNumber: trackingNumber,
		ShippedAt:      time.Now(),
	})
	o.Status = "Shipped"
	o.TrackingNumber = trackingNumber
	return nil
}

func (o *Order) Complete() error {
	if o.Status != "Shipped" {
		return errors.New("can only complete shipped orders")
	}
	o.Apply(OrderCompleted{
		OrderID:     o.AggregateID(),
		CompletedAt: time.Now(),
	})
	o.Status = "Completed"
	return nil
}

func (o *Order) Cancel(reason string) error {
	if o.Status == "Completed" {
		return errors.New("cannot cancel completed order")
	}
	if o.Status == "Shipped" {
		return errors.New("cannot cancel shipped order")
	}
	o.Apply(OrderCancelled{
		OrderID:     o.AggregateID(),
		Reason:      reason,
		CancelledAt: time.Now(),
	})
	o.Status = "Cancelled"
	return nil
}

// =============================================================================
// COMMANDS
// =============================================================================

type CreateOrderCommand struct {
	mink.CommandBase
	OrderID    string `json:"orderId"`
	CustomerID string `json:"customerId"`
}

func (c *CreateOrderCommand) CommandType() string { return "CreateOrder" }
func (c *CreateOrderCommand) AggregateID() string { return c.OrderID }
func (c *CreateOrderCommand) Validate() error {
	if c.OrderID == "" {
		return errors.New("orderID is required")
	}
	if c.CustomerID == "" {
		return errors.New("customerID is required")
	}
	return nil
}

type AddItemCommand struct {
	mink.CommandBase
	OrderID  string  `json:"orderId"`
	SKU      string  `json:"sku"`
	Name     string  `json:"name"`
	Quantity int     `json:"quantity"`
	Price    float64 `json:"price"`
}

func (c *AddItemCommand) CommandType() string { return "AddItem" }
func (c *AddItemCommand) AggregateID() string { return c.OrderID }
func (c *AddItemCommand) Validate() error {
	if c.OrderID == "" {
		return errors.New("orderID is required")
	}
	if c.SKU == "" {
		return errors.New("SKU is required")
	}
	if c.Quantity <= 0 {
		return errors.New("quantity must be positive")
	}
	return nil
}

type SubmitOrderCommand struct {
	mink.CommandBase
	OrderID string `json:"orderId"`
}

func (c *SubmitOrderCommand) CommandType() string { return "SubmitOrder" }
func (c *SubmitOrderCommand) AggregateID() string { return c.OrderID }
func (c *SubmitOrderCommand) Validate() error {
	if c.OrderID == "" {
		return errors.New("orderID is required")
	}
	return nil
}

type ShipOrderCommand struct {
	mink.CommandBase
	OrderID        string `json:"orderId"`
	TrackingNumber string `json:"trackingNumber"`
}

func (c *ShipOrderCommand) CommandType() string { return "ShipOrder" }
func (c *ShipOrderCommand) AggregateID() string { return c.OrderID }
func (c *ShipOrderCommand) Validate() error {
	if c.OrderID == "" {
		return errors.New("orderID is required")
	}
	return nil
}

type CompleteOrderCommand struct {
	mink.CommandBase
	OrderID string `json:"orderId"`
}

func (c *CompleteOrderCommand) CommandType() string { return "CompleteOrder" }
func (c *CompleteOrderCommand) AggregateID() string { return c.OrderID }
func (c *CompleteOrderCommand) Validate() error     { return nil }

type CancelOrderCommand struct {
	mink.CommandBase
	OrderID string `json:"orderId"`
	Reason  string `json:"reason"`
}

func (c *CancelOrderCommand) CommandType() string { return "CancelOrder" }
func (c *CancelOrderCommand) AggregateID() string { return c.OrderID }
func (c *CancelOrderCommand) Validate() error     { return nil }

// =============================================================================
// COMMAND HANDLERS (using GenericHandler)
// =============================================================================

func NewCreateOrderHandler(store *mink.EventStore) mink.CommandHandler {
	return mink.NewGenericHandler(func(ctx context.Context, cmd *CreateOrderCommand) (mink.CommandResult, error) {
		order := NewOrder(cmd.OrderID)
		// For create - DON'T load existing state, just create new
		if err := order.Create(cmd.CustomerID); err != nil {
			return mink.NewErrorResult(err), err
		}
		// Use SaveAggregate which respects version for optimistic concurrency
		if err := store.SaveAggregate(ctx, order); err != nil {
			return mink.NewErrorResult(err), err
		}
		return mink.NewSuccessResult(order.AggregateID(), order.Version()), nil
	})
}

func NewAddItemHandler(store *mink.EventStore) mink.CommandHandler {
	return mink.NewGenericHandler(func(ctx context.Context, cmd *AddItemCommand) (mink.CommandResult, error) {
		order := NewOrder(cmd.OrderID)
		// Load existing state first
		if err := store.LoadAggregate(ctx, order); err != nil {
			// If not found, the order doesn't exist
			return mink.NewErrorResult(err), err
		}
		if err := order.AddItem(cmd.SKU, cmd.Name, cmd.Quantity, cmd.Price); err != nil {
			return mink.NewErrorResult(err), err
		}
		if err := store.SaveAggregate(ctx, order); err != nil {
			return mink.NewErrorResult(err), err
		}
		return mink.NewSuccessResult(order.AggregateID(), order.Version()), nil
	})
}

func NewSubmitOrderHandler(store *mink.EventStore) mink.CommandHandler {
	return mink.NewGenericHandler(func(ctx context.Context, cmd *SubmitOrderCommand) (mink.CommandResult, error) {
		order := NewOrder(cmd.OrderID)
		if err := store.LoadAggregate(ctx, order); err != nil {
			return mink.NewErrorResult(err), err
		}
		if err := order.Submit(); err != nil {
			return mink.NewErrorResult(err), err
		}
		if err := store.SaveAggregate(ctx, order); err != nil {
			return mink.NewErrorResult(err), err
		}
		return mink.NewSuccessResult(order.AggregateID(), order.Version()), nil
	})
}

func NewShipOrderHandler(store *mink.EventStore) mink.CommandHandler {
	return mink.NewGenericHandler(func(ctx context.Context, cmd *ShipOrderCommand) (mink.CommandResult, error) {
		order := NewOrder(cmd.OrderID)
		if err := store.LoadAggregate(ctx, order); err != nil {
			return mink.NewErrorResult(err), err
		}
		if err := order.Ship(cmd.TrackingNumber); err != nil {
			return mink.NewErrorResult(err), err
		}
		if err := store.SaveAggregate(ctx, order); err != nil {
			return mink.NewErrorResult(err), err
		}
		return mink.NewSuccessResult(order.AggregateID(), order.Version()), nil
	})
}

func NewCompleteOrderHandler(store *mink.EventStore) mink.CommandHandler {
	return mink.NewGenericHandler(func(ctx context.Context, cmd *CompleteOrderCommand) (mink.CommandResult, error) {
		order := NewOrder(cmd.OrderID)
		if err := store.LoadAggregate(ctx, order); err != nil {
			return mink.NewErrorResult(err), err
		}
		if err := order.Complete(); err != nil {
			return mink.NewErrorResult(err), err
		}
		if err := store.SaveAggregate(ctx, order); err != nil {
			return mink.NewErrorResult(err), err
		}
		return mink.NewSuccessResult(order.AggregateID(), order.Version()), nil
	})
}

func NewCancelOrderHandler(store *mink.EventStore) mink.CommandHandler {
	return mink.NewGenericHandler(func(ctx context.Context, cmd *CancelOrderCommand) (mink.CommandResult, error) {
		order := NewOrder(cmd.OrderID)
		if err := store.LoadAggregate(ctx, order); err != nil {
			return mink.NewErrorResult(err), err
		}
		if err := order.Cancel(cmd.Reason); err != nil {
			return mink.NewErrorResult(err), err
		}
		if err := store.SaveAggregate(ctx, order); err != nil {
			return mink.NewErrorResult(err), err
		}
		return mink.NewSuccessResult(order.AggregateID(), order.Version()), nil
	})
}

// =============================================================================
// READ MODEL (Projection)
// =============================================================================

type OrderSummary struct {
	ID             string    `json:"id"`
	CustomerID     string    `json:"customerId"`
	ItemCount      int       `json:"itemCount"`
	TotalAmount    float64   `json:"totalAmount"`
	Status         string    `json:"status"`
	TrackingNumber string    `json:"trackingNumber,omitempty"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

type OrderSummaryProjection struct {
	orders map[string]*OrderSummary
}

func NewOrderSummaryProjection() *OrderSummaryProjection {
	return &OrderSummaryProjection{
		orders: make(map[string]*OrderSummary),
	}
}

func (p *OrderSummaryProjection) Name() string {
	return "OrderSummary"
}

func (p *OrderSummaryProjection) HandledEvents() []string {
	return []string{
		"OrderCreated", "ItemAdded", "OrderSubmitted",
		"OrderShipped", "OrderCompleted", "OrderCancelled",
	}
}

func (p *OrderSummaryProjection) Apply(ctx context.Context, event mink.StoredEvent) error {
	switch event.Type {
	case "OrderCreated":
		var e OrderCreated
		if err := json.Unmarshal(event.Data, &e); err != nil {
			return err
		}
		p.orders[e.OrderID] = &OrderSummary{
			ID:         e.OrderID,
			CustomerID: e.CustomerID,
			Status:     "Created",
			CreatedAt:  e.CreatedAt,
			UpdatedAt:  e.CreatedAt,
		}

	case "ItemAdded":
		var e ItemAdded
		if err := json.Unmarshal(event.Data, &e); err != nil {
			return err
		}
		if order, ok := p.orders[e.OrderID]; ok {
			order.ItemCount += e.Quantity
			order.UpdatedAt = event.Timestamp
		}

	case "OrderSubmitted":
		var e OrderSubmitted
		if err := json.Unmarshal(event.Data, &e); err != nil {
			return err
		}
		if order, ok := p.orders[e.OrderID]; ok {
			order.Status = "Submitted"
			order.TotalAmount = e.TotalAmount
			order.UpdatedAt = e.SubmittedAt
		}

	case "OrderShipped":
		var e OrderShipped
		if err := json.Unmarshal(event.Data, &e); err != nil {
			return err
		}
		if order, ok := p.orders[e.OrderID]; ok {
			order.Status = "Shipped"
			order.TrackingNumber = e.TrackingNumber
			order.UpdatedAt = e.ShippedAt
		}

	case "OrderCompleted":
		var e OrderCompleted
		if err := json.Unmarshal(event.Data, &e); err != nil {
			return err
		}
		if order, ok := p.orders[e.OrderID]; ok {
			order.Status = "Completed"
			order.UpdatedAt = e.CompletedAt
		}

	case "OrderCancelled":
		var e OrderCancelled
		if err := json.Unmarshal(event.Data, &e); err != nil {
			return err
		}
		if order, ok := p.orders[e.OrderID]; ok {
			order.Status = "Cancelled"
			order.UpdatedAt = e.CancelledAt
		}
	}
	return nil
}

func (p *OrderSummaryProjection) GetOrder(id string) *OrderSummary {
	return p.orders[id]
}

func (p *OrderSummaryProjection) GetAllOrders() []*OrderSummary {
	result := make([]*OrderSummary, 0, len(p.orders))
	for _, order := range p.orders {
		result = append(result, order)
	}
	return result
}

// =============================================================================
// ORDER FULFILLMENT SAGA
// =============================================================================

type OrderFulfillmentSaga struct {
	mink.SagaBase
	OrderID       string
	CustomerID    string
	TotalAmount   float64
	TransactionID string
	ReservationID string
}

func NewOrderFulfillmentSaga(id string) mink.Saga {
	saga := &OrderFulfillmentSaga{
		SagaBase: mink.NewSagaBase(id, "OrderFulfillment"),
	}
	return saga
}

func (s *OrderFulfillmentSaga) HandledEvents() []string {
	return []string{
		"OrderSubmitted",
		"PaymentProcessed", "PaymentFailed",
		"InventoryReserved", "InventoryReservationFailed",
		"OrderShipped",
	}
}

func (s *OrderFulfillmentSaga) HandleEvent(ctx context.Context, event mink.StoredEvent) ([]mink.Command, error) {
	switch event.Type {
	case "OrderSubmitted":
		return s.handleOrderSubmitted(event)
	case "PaymentProcessed":
		return s.handlePaymentProcessed(event)
	case "PaymentFailed":
		return s.handlePaymentFailed(event)
	case "InventoryReserved":
		return s.handleInventoryReserved(event)
	case "InventoryReservationFailed":
		return s.handleInventoryReservationFailed(event)
	case "OrderShipped":
		return s.handleOrderShipped(event)
	}
	return nil, nil
}

func (s *OrderFulfillmentSaga) handleOrderSubmitted(event mink.StoredEvent) ([]mink.Command, error) {
	var data OrderSubmitted
	if err := json.Unmarshal(event.Data, &data); err != nil {
		return nil, err
	}

	s.OrderID = data.OrderID
	s.TotalAmount = data.TotalAmount
	s.SetCorrelationID(data.OrderID)
	s.SetStatus(mink.SagaStatusRunning)
	s.SetCurrentStep(1)

	fmt.Printf("📋 [Saga] Order %s submitted (Total: $%.2f), initiating payment...\n",
		s.OrderID, s.TotalAmount)

	// In a real system, this would dispatch a command to the payment service
	// For this example, we simulate by returning no commands
	return nil, nil
}

func (s *OrderFulfillmentSaga) handlePaymentProcessed(event mink.StoredEvent) ([]mink.Command, error) {
	var data PaymentProcessed
	if err := json.Unmarshal(event.Data, &data); err != nil {
		return nil, err
	}

	s.TransactionID = data.TransactionID
	s.SetCurrentStep(2)

	fmt.Printf("💳 [Saga] Payment processed for order %s (Transaction: %s)\n",
		s.OrderID, s.TransactionID)

	// Next: Reserve inventory
	return nil, nil
}

func (s *OrderFulfillmentSaga) handlePaymentFailed(event mink.StoredEvent) ([]mink.Command, error) {
	var data PaymentFailed
	if err := json.Unmarshal(event.Data, &data); err != nil {
		return nil, err
	}

	s.SetStatus(mink.SagaStatusFailed)

	fmt.Printf("❌ [Saga] Payment failed for order %s: %s\n", s.OrderID, data.Reason)

	// Cancel the order
	return []mink.Command{
		&CancelOrderCommand{OrderID: s.OrderID, Reason: "Payment failed: " + data.Reason},
	}, nil
}

func (s *OrderFulfillmentSaga) handleInventoryReserved(event mink.StoredEvent) ([]mink.Command, error) {
	var data InventoryReserved
	if err := json.Unmarshal(event.Data, &data); err != nil {
		return nil, err
	}

	s.ReservationID = data.ReservationID
	s.SetCurrentStep(3)

	fmt.Printf("📦 [Saga] Inventory reserved for order %s (Reservation: %s)\n",
		s.OrderID, s.ReservationID)

	// Generate tracking number and ship
	trackingNumber := fmt.Sprintf("TRACK-%s-%d", s.OrderID, time.Now().Unix())
	return []mink.Command{
		&ShipOrderCommand{OrderID: s.OrderID, TrackingNumber: trackingNumber},
	}, nil
}

func (s *OrderFulfillmentSaga) handleInventoryReservationFailed(event mink.StoredEvent) ([]mink.Command, error) {
	var data InventoryReservationFailed
	if err := json.Unmarshal(event.Data, &data); err != nil {
		return nil, err
	}

	s.SetStatus(mink.SagaStatusCompensating)

	fmt.Printf("⚠️ [Saga] Inventory reservation failed for order %s: %s\n",
		s.OrderID, data.Reason)

	// Need to refund and cancel
	return []mink.Command{
		&CancelOrderCommand{OrderID: s.OrderID, Reason: "Inventory unavailable: " + data.Reason},
	}, nil
}

func (s *OrderFulfillmentSaga) handleOrderShipped(event mink.StoredEvent) ([]mink.Command, error) {
	var data OrderShipped
	if err := json.Unmarshal(event.Data, &data); err != nil {
		return nil, err
	}

	s.Complete()

	fmt.Printf("✅ [Saga] Order %s shipped with tracking %s - Saga Complete!\n",
		s.OrderID, data.TrackingNumber)

	return []mink.Command{
		&CompleteOrderCommand{OrderID: s.OrderID},
	}, nil
}

func (s *OrderFulfillmentSaga) Compensate(ctx context.Context, failedStep int, failureReason error) ([]mink.Command, error) {
	var commands []mink.Command

	// Compensate in reverse order
	if failedStep >= 2 && s.TransactionID != "" {
		fmt.Printf("💰 [Saga] Refunding payment %s for order %s\n", s.TransactionID, s.OrderID)
		// In real system, add refund command
	}

	if failedStep >= 3 && s.ReservationID != "" {
		fmt.Printf("📤 [Saga] Releasing inventory reservation %s\n", s.ReservationID)
		// In real system, add release inventory command
	}

	commands = append(commands, &CancelOrderCommand{
		OrderID: s.OrderID,
		Reason:  failureReason.Error(),
	})

	s.MarkCompensated()
	return commands, nil
}

func (s *OrderFulfillmentSaga) Data() map[string]interface{} {
	return map[string]interface{}{
		"orderId":       s.OrderID,
		"customerId":    s.CustomerID,
		"totalAmount":   s.TotalAmount,
		"transactionId": s.TransactionID,
		"reservationId": s.ReservationID,
	}
}

func (s *OrderFulfillmentSaga) SetData(data map[string]interface{}) {
	if v, ok := data["orderId"].(string); ok {
		s.OrderID = v
	}
	if v, ok := data["customerId"].(string); ok {
		s.CustomerID = v
	}
	if v, ok := data["totalAmount"].(float64); ok {
		s.TotalAmount = v
	}
	if v, ok := data["transactionId"].(string); ok {
		s.TransactionID = v
	}
	if v, ok := data["reservationId"].(string); ok {
		s.ReservationID = v
	}
}

func (s *OrderFulfillmentSaga) IsComplete() bool {
	return s.Status() == mink.SagaStatusCompleted ||
		s.Status() == mink.SagaStatusCompensated ||
		s.Status() == mink.SagaStatusFailed
}

// =============================================================================
// SIMPLE LOGGER
// =============================================================================

type simpleLogger struct{}

func (l *simpleLogger) Debug(msg string, keysAndValues ...interface{}) {}
func (l *simpleLogger) Info(msg string, keysAndValues ...interface{})  {}
func (l *simpleLogger) Warn(msg string, keysAndValues ...interface{})  {}
func (l *simpleLogger) Error(msg string, keysAndValues ...interface{}) {}

// =============================================================================
// MAIN - PUTTING IT ALL TOGETHER
// =============================================================================

func main() {
	fmt.Println("╔══════════════════════════════════════════════════════════════════╗")
	fmt.Println("║     go-mink Full E-Commerce Example with PostgreSQL              ║")
	fmt.Println("╚══════════════════════════════════════════════════════════════════╝")
	fmt.Println()

	ctx := context.Background()

	// Database connection
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://postgres:postgres@localhost:5432/mink_test?sslmode=disable"
	}

	fmt.Println("📡 Connecting to PostgreSQL...")
	db, err := sql.Open("postgres", dbURL)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer db.Close()

	// Initialize PostgreSQL adapter
	fmt.Println("🔧 Initializing PostgreSQL adapters...")
	eventAdapter, err := postgres.NewAdapter(dbURL)
	if err != nil {
		log.Fatalf("Failed to create event adapter: %v", err)
	}
	defer eventAdapter.Close()

	if err := eventAdapter.Initialize(ctx); err != nil {
		log.Fatalf("Failed to initialize event store: %v", err)
	}

	// Initialize Saga Store (uses the database connection)
	sagaStore := postgres.NewSagaStore(db)
	if err := sagaStore.Initialize(ctx); err != nil {
		log.Fatalf("Failed to initialize saga store: %v", err)
	}

	// Initialize Idempotency Store
	idempotencyStore := postgres.NewIdempotencyStore(db)
	if err := idempotencyStore.Initialize(ctx); err != nil {
		log.Fatalf("Failed to initialize idempotency store: %v", err)
	}

	// Create Event Store with JSON serializer
	serializer := mink.NewJSONSerializer()
	serializer.Register("OrderCreated", OrderCreated{})
	serializer.Register("ItemAdded", ItemAdded{})
	serializer.Register("OrderSubmitted", OrderSubmitted{})
	serializer.Register("OrderShipped", OrderShipped{})
	serializer.Register("OrderCompleted", OrderCompleted{})
	serializer.Register("OrderCancelled", OrderCancelled{})
	serializer.Register("PaymentProcessed", PaymentProcessed{})
	serializer.Register("PaymentFailed", PaymentFailed{})
	serializer.Register("PaymentRefunded", PaymentRefunded{})
	serializer.Register("InventoryReserved", InventoryReserved{})
	serializer.Register("InventoryReservationFailed", InventoryReservationFailed{})
	serializer.Register("InventoryReleased", InventoryReleased{})

	eventStore := mink.New(eventAdapter, mink.WithSerializer(serializer))

	// Create a simple logger for output
	logger := &simpleLogger{}

	// Create Command Bus with middleware
	commandBus := mink.NewCommandBus(
		mink.WithMiddleware(
			mink.RecoveryMiddleware(),
			mink.NewLoggingMiddleware(logger).Middleware(),
			mink.ValidationMiddleware(),
			mink.CorrelationIDMiddleware(nil),
			mink.CausationIDMiddleware(),
			mink.IdempotencyMiddleware(mink.IdempotencyConfig{
				Store:        idempotencyStore,
				TTL:          24 * time.Hour,
				KeyGenerator: mink.GenerateIdempotencyKey,
			}),
		),
	)

	// Register Command Handlers
	commandBus.Register(NewCreateOrderHandler(eventStore))
	commandBus.Register(NewAddItemHandler(eventStore))
	commandBus.Register(NewSubmitOrderHandler(eventStore))
	commandBus.Register(NewShipOrderHandler(eventStore))
	commandBus.Register(NewCompleteOrderHandler(eventStore))
	commandBus.Register(NewCancelOrderHandler(eventStore))

	// Create Projection
	projection := NewOrderSummaryProjection()

	fmt.Println("✅ All systems initialized!")
	fmt.Println()

	// =========================================================================
	// SCENARIO 1: Happy Path - Complete Order Flow
	// =========================================================================
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("📦 SCENARIO 1: Happy Path - Complete Order Fulfillment")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println()

	orderID := fmt.Sprintf("ORD-%d", time.Now().UnixNano())
	customerID := "CUST-12345"

	// Step 1: Create Order
	fmt.Println("1️⃣  Creating new order...")
	createCmd := &CreateOrderCommand{
		CommandBase: mink.CommandBase{CorrelationID: orderID},
		OrderID:     orderID,
		CustomerID:  customerID,
	}
	if _, err := commandBus.Dispatch(ctx, createCmd); err != nil {
		log.Printf("Failed to create order: %v", err)
	}

	// Step 2: Add Items
	fmt.Println("2️⃣  Adding items to order...")
	items := []struct {
		SKU      string
		Name     string
		Quantity int
		Price    float64
	}{
		{"LAPTOP-001", "Gaming Laptop", 1, 1299.99},
		{"MOUSE-001", "Wireless Mouse", 2, 49.99},
		{"KEYBOARD-001", "Mechanical Keyboard", 1, 149.99},
	}

	for _, item := range items {
		addCmd := &AddItemCommand{
			CommandBase: mink.CommandBase{CorrelationID: orderID},
			OrderID:     orderID,
			SKU:         item.SKU,
			Name:        item.Name,
			Quantity:    item.Quantity,
			Price:       item.Price,
		}
		if _, err := commandBus.Dispatch(ctx, addCmd); err != nil {
			log.Printf("Failed to add item: %v", err)
		}
		fmt.Printf("   ✓ Added: %s (x%d) - $%.2f\n", item.Name, item.Quantity, item.Price)
	}

	// Step 3: Submit Order
	fmt.Println("3️⃣  Submitting order...")
	submitCmd := &SubmitOrderCommand{
		CommandBase: mink.CommandBase{CorrelationID: orderID},
		OrderID:     orderID,
	}
	if _, err := commandBus.Dispatch(ctx, submitCmd); err != nil {
		log.Printf("Failed to submit order: %v", err)
	}

	// Step 4: Ship Order (simulating saga completing)
	fmt.Println("4️⃣  Shipping order...")
	shipCmd := &ShipOrderCommand{
		CommandBase:    mink.CommandBase{CorrelationID: orderID},
		OrderID:        orderID,
		TrackingNumber: fmt.Sprintf("TRACK-%d", time.Now().Unix()),
	}
	if _, err := commandBus.Dispatch(ctx, shipCmd); err != nil {
		log.Printf("Failed to ship order: %v", err)
	}

	// Step 5: Complete Order
	fmt.Println("5️⃣  Completing order...")
	completeCmd := &CompleteOrderCommand{
		CommandBase: mink.CommandBase{CorrelationID: orderID},
		OrderID:     orderID,
	}
	if _, err := commandBus.Dispatch(ctx, completeCmd); err != nil {
		log.Printf("Failed to complete order: %v", err)
	}

	// Load and display final state
	fmt.Println()
	fmt.Println("📊 Final Order State (from Event Store):")
	finalOrder := NewOrder(orderID)
	if err := eventStore.LoadAggregate(ctx, finalOrder); err != nil {
		log.Printf("Failed to load order: %v", err)
	} else {
		fmt.Printf("   Order ID: %s\n", finalOrder.AggregateID())
		fmt.Printf("   Customer: %s\n", finalOrder.CustomerID)
		fmt.Printf("   Status: %s\n", finalOrder.Status)
		fmt.Printf("   Items: %d\n", len(finalOrder.Items))
		fmt.Printf("   Total: $%.2f\n", finalOrder.TotalAmount)
		fmt.Printf("   Tracking: %s\n", finalOrder.TrackingNumber)
		fmt.Printf("   Version: %d\n", finalOrder.Version())
	}

	// Apply events to projection
	fmt.Println()
	fmt.Println("📈 Rebuilding Read Model (Projection):")
	streamID := fmt.Sprintf("Order-%s", orderID)
	rawEvents, err := eventStore.LoadRaw(ctx, streamID, 0)
	if err != nil {
		log.Printf("Failed to load events: %v", err)
	} else {
		for _, event := range rawEvents {
			projection.Apply(ctx, event)
		}
		if summary := projection.GetOrder(orderID); summary != nil {
			fmt.Printf("   Order: %s | Customer: %s | Items: %d | Total: $%.2f | Status: %s\n",
				summary.ID, summary.CustomerID, summary.ItemCount, summary.TotalAmount, summary.Status)
		}
	}

	fmt.Println()

	// =========================================================================
	// SCENARIO 2: Saga Persistence
	// =========================================================================
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("🔄 SCENARIO 2: Saga State Persistence")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println()

	sagaID := fmt.Sprintf("saga-%d", time.Now().UnixNano())
	sagaOrderID := fmt.Sprintf("ORD-SAGA-%d", time.Now().UnixNano())

	// Create and save saga state
	fmt.Println("1️⃣  Creating saga instance...")
	sagaState := &mink.SagaState{
		ID:            sagaID,
		Type:          "OrderFulfillment",
		CorrelationID: sagaOrderID,
		Status:        mink.SagaStatusStarted,
		CurrentStep:   0,
		StartedAt:     time.Now(),
		Version:       0,
		Data: map[string]interface{}{
			"orderId":     sagaOrderID,
			"customerId":  "CUST-SAGA-001",
			"totalAmount": 1549.96,
		},
	}

	if err := sagaStore.Save(ctx, sagaState); err != nil {
		log.Printf("Failed to save saga: %v", err)
	} else {
		fmt.Printf("   ✓ Saga saved: %s (Version: %d)\n", sagaID, sagaState.Version)
	}

	// Simulate saga progress
	fmt.Println("2️⃣  Simulating saga progress (Payment → Inventory → Ship)...")

	// Step 1: Payment processed
	now := time.Now()
	sagaState.Status = mink.SagaStatusRunning
	sagaState.CurrentStep = 1
	sagaState.Steps = []mink.SagaStep{
		{
			Name:        "ProcessPayment",
			Index:       0,
			Status:      mink.SagaStepCompleted,
			Command:     "ProcessPaymentCommand",
			CompletedAt: &now,
		},
	}
	sagaState.Data["transactionId"] = "TXN-123456"
	if err := sagaStore.Save(ctx, sagaState); err != nil {
		log.Printf("Failed to update saga: %v", err)
	}
	fmt.Printf("   ✓ Step 1 complete: Payment (Version: %d)\n", sagaState.Version)

	// Step 2: Inventory reserved
	now = time.Now()
	sagaState.CurrentStep = 2
	sagaState.Steps = append(sagaState.Steps, mink.SagaStep{
		Name:        "ReserveInventory",
		Index:       1,
		Status:      mink.SagaStepCompleted,
		Command:     "ReserveInventoryCommand",
		CompletedAt: &now,
	})
	sagaState.Data["reservationId"] = "RES-789012"
	if err := sagaStore.Save(ctx, sagaState); err != nil {
		log.Printf("Failed to update saga: %v", err)
	}
	fmt.Printf("   ✓ Step 2 complete: Inventory (Version: %d)\n", sagaState.Version)

	// Step 3: Order shipped - Complete saga
	now = time.Now()
	sagaState.CurrentStep = 3
	sagaState.Status = mink.SagaStatusCompleted
	sagaState.CompletedAt = &now
	sagaState.Steps = append(sagaState.Steps, mink.SagaStep{
		Name:        "ShipOrder",
		Index:       2,
		Status:      mink.SagaStepCompleted,
		Command:     "ShipOrderCommand",
		CompletedAt: &now,
	})
	sagaState.Data["trackingNumber"] = "TRACK-SAGA-001"
	if err := sagaStore.Save(ctx, sagaState); err != nil {
		log.Printf("Failed to update saga: %v", err)
	}
	fmt.Printf("   ✓ Step 3 complete: Shipped (Version: %d)\n", sagaState.Version)

	// Reload and display saga state
	fmt.Println()
	fmt.Println("📊 Reloading Saga from PostgreSQL:")
	loadedSaga, err := sagaStore.Load(ctx, sagaID)
	if err != nil {
		log.Printf("Failed to load saga: %v", err)
	} else {
		fmt.Printf("   Saga ID: %s\n", loadedSaga.ID)
		fmt.Printf("   Type: %s\n", loadedSaga.Type)
		fmt.Printf("   Correlation ID: %s\n", loadedSaga.CorrelationID)
		fmt.Printf("   Status: %s\n", loadedSaga.Status)
		fmt.Printf("   Current Step: %d\n", loadedSaga.CurrentStep)
		fmt.Printf("   Version: %d\n", loadedSaga.Version)
		fmt.Printf("   Steps Completed: %d\n", len(loadedSaga.Steps))
		for _, step := range loadedSaga.Steps {
			fmt.Printf("      - %s: %s\n", step.Name, step.Status)
		}
	}

	// Find by correlation ID
	fmt.Println()
	fmt.Println("🔍 Finding saga by correlation ID:")
	foundSaga, err := sagaStore.FindByCorrelationID(ctx, sagaOrderID)
	if err != nil {
		log.Printf("Failed to find saga: %v", err)
	} else {
		fmt.Printf("   Found: %s (Status: %s)\n", foundSaga.ID, foundSaga.Status)
	}

	// Count by status
	fmt.Println()
	fmt.Println("📈 Saga Statistics:")
	counts, err := sagaStore.CountByStatus(ctx)
	if err != nil {
		log.Printf("Failed to count sagas: %v", err)
	} else {
		for status, count := range counts {
			if count > 0 {
				fmt.Printf("   %s: %d\n", status, count)
			}
		}
	}

	fmt.Println()

	// =========================================================================
	// SCENARIO 3: Idempotency Check
	// =========================================================================
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("🔒 SCENARIO 3: Idempotency - Duplicate Command Prevention")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println()

	idempotentOrderID := fmt.Sprintf("ORD-IDEMP-%d", time.Now().UnixNano())
	requestID := fmt.Sprintf("REQ-%d", time.Now().UnixNano())

	// First attempt - use CommandID for idempotency
	fmt.Println("1️⃣  First command dispatch...")
	cmd1 := &CreateOrderCommand{
		CommandBase: mink.CommandBase{CommandID: requestID},
		OrderID:     idempotentOrderID,
		CustomerID:  "CUST-IDEMP",
	}
	if _, err := commandBus.Dispatch(ctx, cmd1); err != nil {
		fmt.Printf("   ❌ Error: %v\n", err)
	} else {
		fmt.Println("   ✓ Order created successfully")
	}

	// Duplicate attempt (same CommandID = same idempotency key)
	fmt.Println("2️⃣  Duplicate command dispatch (same command ID)...")
	cmd2 := &CreateOrderCommand{
		CommandBase: mink.CommandBase{CommandID: requestID},
		OrderID:     idempotentOrderID,
		CustomerID:  "CUST-IDEMP",
	}
	if _, err := commandBus.Dispatch(ctx, cmd2); err != nil {
		fmt.Printf("   ℹ️  Skipped (idempotent): %v\n", err)
	} else {
		fmt.Println("   ✓ Command processed (was deduplicated)")
	}

	fmt.Println()

	// =========================================================================
	// SCENARIO 4: Optimistic Concurrency
	// =========================================================================
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("⚡ SCENARIO 4: Optimistic Concurrency Control")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println()

	concurrentOrderID := fmt.Sprintf("ORD-CONC-%d", time.Now().UnixNano())

	// Create order
	fmt.Println("1️⃣  Creating order...")
	concCmd := &CreateOrderCommand{OrderID: concurrentOrderID, CustomerID: "CUST-CONC"}
	if _, err := commandBus.Dispatch(ctx, concCmd); err != nil {
		log.Printf("Failed: %v", err)
	}

	// Simulate concurrent modifications
	fmt.Println("2️⃣  Simulating concurrent access...")

	// Load order twice (simulating two concurrent processes)
	order1 := NewOrder(concurrentOrderID)
	order2 := NewOrder(concurrentOrderID)
	eventStore.LoadAggregate(ctx, order1)
	eventStore.LoadAggregate(ctx, order2)

	fmt.Printf("   Order1 loaded at version: %d\n", order1.Version())
	fmt.Printf("   Order2 loaded at version: %d\n", order2.Version())

	// First modification succeeds
	fmt.Println("3️⃣  First concurrent modification...")
	order1.AddItem("ITEM-1", "First Item", 1, 10.00)
	if err := eventStore.SaveAggregate(ctx, order1); err != nil {
		fmt.Printf("   ❌ Order1 failed: %v\n", err)
	} else {
		fmt.Printf("   ✓ Order1 saved (new version: %d)\n", order1.Version())
	}

	// Second modification fails (version conflict)
	fmt.Println("4️⃣  Second concurrent modification (should fail)...")
	order2.AddItem("ITEM-2", "Second Item", 1, 20.00)
	if err := eventStore.SaveAggregate(ctx, order2); err != nil {
		if errors.Is(err, mink.ErrConcurrencyConflict) {
			fmt.Printf("   ⚠️  Expected conflict: %v\n", err)
		} else {
			fmt.Printf("   ❌ Unexpected error: %v\n", err)
		}
	} else {
		fmt.Println("   ✓ Order2 saved (unexpected)")
	}

	// Retry with fresh load
	fmt.Println("5️⃣  Retrying with fresh load...")
	order2Retry := NewOrder(concurrentOrderID)
	eventStore.LoadAggregate(ctx, order2Retry)
	order2Retry.AddItem("ITEM-2", "Second Item", 1, 20.00)
	if err := eventStore.SaveAggregate(ctx, order2Retry); err != nil {
		fmt.Printf("   ❌ Retry failed: %v\n", err)
	} else {
		fmt.Printf("   ✓ Retry succeeded (version: %d)\n", order2Retry.Version())
	}

	fmt.Println()

	// =========================================================================
	// SUMMARY
	// =========================================================================
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("✅ ALL SCENARIOS COMPLETED SUCCESSFULLY!")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println()
	fmt.Println("This example demonstrated:")
	fmt.Println("  • Event Sourcing with PostgreSQL persistence")
	fmt.Println("  • CQRS with Command Bus and middleware pipeline")
	fmt.Println("  • Projections for building read models")
	fmt.Println("  • Saga pattern for process orchestration")
	fmt.Println("  • Idempotency for exactly-once processing")
	fmt.Println("  • Optimistic concurrency control")
	fmt.Println()
	fmt.Println("For more examples, see the /examples directory.")
	fmt.Println()
}
