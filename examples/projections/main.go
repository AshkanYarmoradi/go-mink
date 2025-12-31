// Package main demonstrates the projection and read model features of go-mink.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	mink "github.com/AshkanYarmoradi/go-mink"
	"github.com/AshkanYarmoradi/go-mink/adapters/memory"
)

// --- Events ---

// OrderCreated is emitted when a new order is created.
type OrderCreated struct {
	OrderID    string  `json:"orderId"`
	CustomerID string  `json:"customerId"`
	Amount     float64 `json:"amount"`
	CreatedAt  time.Time `json:"createdAt"`
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
	OrderID   string    `json:"orderId"`
	ShippedAt time.Time `json:"shippedAt"`
}

// --- Read Model ---

// OrderSummary is a read model representing an order summary.
type OrderSummary struct {
	OrderID     string    `json:"orderId"`
	CustomerID  string    `json:"customerId"`
	Status      string    `json:"status"`
	ItemCount   int       `json:"itemCount"`
	TotalAmount float64   `json:"totalAmount"`
	CreatedAt   time.Time `json:"createdAt"`
	ShippedAt   *time.Time `json:"shippedAt,omitempty"`
}

// --- Inline Projection ---

// OrderSummaryProjection maintains an up-to-date summary of all orders.
type OrderSummaryProjection struct {
	mink.ProjectionBase
	repo *mink.InMemoryRepository[OrderSummary]
}

// NewOrderSummaryProjection creates a new OrderSummaryProjection.
func NewOrderSummaryProjection(repo *mink.InMemoryRepository[OrderSummary]) *OrderSummaryProjection {
	return &OrderSummaryProjection{
		ProjectionBase: mink.NewProjectionBase("OrderSummary", "OrderCreated", "ItemAdded", "OrderShipped"),
		repo:           repo,
	}
}

// Apply handles events and updates the read model.
func (p *OrderSummaryProjection) Apply(ctx context.Context, event mink.StoredEvent) error {
	switch event.Type {
	case "OrderCreated":
		var e OrderCreated
		if err := json.Unmarshal(event.Data, &e); err != nil {
			return err
		}
		return p.repo.Insert(ctx, &OrderSummary{
			OrderID:    e.OrderID,
			CustomerID: e.CustomerID,
			Status:     "Created",
			CreatedAt:  e.CreatedAt,
		})

	case "ItemAdded":
		var e ItemAdded
		if err := json.Unmarshal(event.Data, &e); err != nil {
			return err
		}
		return p.repo.Update(ctx, e.OrderID, func(s *OrderSummary) {
			s.ItemCount += e.Quantity
			s.TotalAmount += e.Price * float64(e.Quantity)
		})

	case "OrderShipped":
		var e OrderShipped
		if err := json.Unmarshal(event.Data, &e); err != nil {
			return err
		}
		return p.repo.Update(ctx, e.OrderID, func(s *OrderSummary) {
			s.Status = "Shipped"
			s.ShippedAt = &e.ShippedAt
		})
	}

	return nil
}

// --- Live Projection for Real-time Dashboard ---

// OrderDashboardProjection provides real-time order updates for a dashboard.
type OrderDashboardProjection struct {
	mink.LiveProjectionBase
	updates chan string
}

// NewOrderDashboardProjection creates a new live projection for order updates.
func NewOrderDashboardProjection() *OrderDashboardProjection {
	return &OrderDashboardProjection{
		LiveProjectionBase: mink.NewLiveProjectionBase("OrderDashboard", true, "OrderCreated", "OrderShipped"),
		updates:            make(chan string, 100),
	}
}

// OnEvent is called for each event in real-time.
func (p *OrderDashboardProjection) OnEvent(ctx context.Context, event mink.StoredEvent) {
	var message string
	switch event.Type {
	case "OrderCreated":
		var e OrderCreated
		if err := json.Unmarshal(event.Data, &e); err != nil {
			return
		}
		message = fmt.Sprintf("📦 New order %s from customer %s (%.2f)", e.OrderID, e.CustomerID, e.Amount)

	case "OrderShipped":
		var e OrderShipped
		if err := json.Unmarshal(event.Data, &e); err != nil {
			return
		}
		message = fmt.Sprintf("🚚 Order %s shipped at %s", e.OrderID, e.ShippedAt.Format(time.RFC822))
	}

	if message != "" {
		select {
		case p.updates <- message:
		default:
			// Channel full, skip
		}
	}
}

// Updates returns a channel of dashboard update messages.
func (p *OrderDashboardProjection) Updates() <-chan string {
	return p.updates
}

func main() {
	ctx := context.Background()

	// Create event store with memory adapter
	adapter := memory.NewAdapter()
	store := mink.New(adapter)

	if err := store.Initialize(ctx); err != nil {
		log.Fatalf("Failed to initialize store: %v", err)
	}
	defer store.Close()

	// Create read model repository
	orderRepo := mink.NewInMemoryRepository(func(o *OrderSummary) string {
		return o.OrderID
	})

	// Create checkpoint store
	checkpointStore := memory.NewCheckpointStore()

	// Create projection engine
	engine := mink.NewProjectionEngine(store,
		mink.WithCheckpointStore(checkpointStore),
	)

	// Register inline projection (synchronous updates)
	summaryProjection := NewOrderSummaryProjection(orderRepo)
	if err := engine.RegisterInline(summaryProjection); err != nil {
		log.Fatalf("Failed to register inline projection: %v", err)
	}

	// Register live projection (real-time updates)
	dashboardProjection := NewOrderDashboardProjection()
	if err := engine.RegisterLive(dashboardProjection); err != nil {
		log.Fatalf("Failed to register live projection: %v", err)
	}

	// Start the projection engine
	if err := engine.Start(ctx); err != nil {
		log.Fatalf("Failed to start projection engine: %v", err)
	}
	defer engine.Stop(ctx)

	// Start a goroutine to print dashboard updates
	go func() {
		for update := range dashboardProjection.Updates() {
			fmt.Println("[Dashboard]", update)
		}
	}()

	fmt.Println("=== Projections Demo ===")
	fmt.Println()

	// Simulate some business operations
	now := time.Now()
	var err error

	// Create Order 1
	fmt.Println("Creating Order 1...")
	order1Event := OrderCreated{
		OrderID:    "order-001",
		CustomerID: "customer-123",
		Amount:     99.99,
		CreatedAt:  now,
	}

	err = store.Append(ctx, "Order-001", []interface{}{order1Event}, mink.ExpectVersion(mink.NoStream))
	if err != nil {
		log.Fatalf("Failed to append events: %v", err)
	}

	// Load the events back to process projections
	events1, _ := store.LoadRaw(ctx, "Order-001", 0)
	engine.ProcessInlineProjections(ctx, events1)
	engine.NotifyLiveProjections(ctx, events1)

	// Add items to Order 1
	fmt.Println("Adding items to Order 1...")
	item1 := ItemAdded{OrderID: "order-001", SKU: "WIDGET-A", Quantity: 2, Price: 29.99}
	item2 := ItemAdded{OrderID: "order-001", SKU: "GADGET-B", Quantity: 1, Price: 49.99}

	err = store.Append(ctx, "Order-001", []interface{}{item1, item2}, mink.ExpectVersion(1))
	if err != nil {
		log.Fatalf("Failed to append items: %v", err)
	}

	events2, _ := store.LoadRaw(ctx, "Order-001", 1)
	engine.ProcessInlineProjections(ctx, events2)

	// Create Order 2
	fmt.Println("Creating Order 2...")
	order2Event := OrderCreated{
		OrderID:    "order-002",
		CustomerID: "customer-456",
		Amount:     149.99,
		CreatedAt:  now,
	}

	err = store.Append(ctx, "Order-002", []interface{}{order2Event}, mink.ExpectVersion(mink.NoStream))
	if err != nil {
		log.Fatalf("Failed to create order 2: %v", err)
	}

	events3, _ := store.LoadRaw(ctx, "Order-002", 0)
	engine.ProcessInlineProjections(ctx, events3)
	engine.NotifyLiveProjections(ctx, events3)

	// Ship Order 1
	fmt.Println("Shipping Order 1...")
	shippedEvent := OrderShipped{
		OrderID:   "order-001",
		ShippedAt: time.Now(),
	}

	err = store.Append(ctx, "Order-001", []interface{}{shippedEvent}, mink.ExpectVersion(3))
	if err != nil {
		log.Fatalf("Failed to ship order: %v", err)
	}

	events4, _ := store.LoadRaw(ctx, "Order-001", 3)
	engine.ProcessInlineProjections(ctx, events4)
	engine.NotifyLiveProjections(ctx, events4)

	// Give live projection time to process
	time.Sleep(100 * time.Millisecond)

	fmt.Println()
	fmt.Println("=== Query Read Model ===")
	fmt.Println()

	// Query the read model
	order1, err := orderRepo.Get(ctx, "order-001")
	if err != nil {
		log.Printf("Failed to get order: %v", err)
	} else {
		fmt.Printf("Order 1: %+v\n", order1)
	}

	order2, err := orderRepo.Get(ctx, "order-002")
	if err != nil {
		log.Printf("Failed to get order: %v", err)
	} else {
		fmt.Printf("Order 2: %+v\n", order2)
	}

	// Use the query builder
	fmt.Println()
	fmt.Println("=== Query Builder Demo ===")
	fmt.Println()

	// Find all orders
	allOrders, _ := orderRepo.Find(ctx, mink.Query{})
	fmt.Printf("Total orders: %d\n", len(allOrders))

	// Count orders
	count, _ := orderRepo.Count(ctx, mink.Query{})
	fmt.Printf("Order count: %d\n", count)

	// Check projection status
	fmt.Println()
	fmt.Println("=== Projection Status ===")
	fmt.Println()

	statuses := engine.GetAllStatuses()
	for _, status := range statuses {
		fmt.Printf("Projection: %s, State: %s\n", status.Name, status.State)
	}

	fmt.Println()
	fmt.Println("Demo completed!")
}
