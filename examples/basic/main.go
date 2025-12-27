// Package main demonstrates the basic usage of go-mink for event sourcing.
//
// This example shows:
// - Defining events
// - Creating an aggregate
// - Saving and loading aggregates
// - Working with the event store directly
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/AshkanYarmoradi/go-mink"
	"github.com/AshkanYarmoradi/go-mink/adapters/memory"
)

// Domain Events
// Events represent things that happened in our domain.
// They are immutable facts that are stored in the event store.

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

// OrderItem represents an item in an order.
type OrderItem struct {
	SKU      string
	Quantity int
	Price    float64
}

// Order is our aggregate root.
// It encapsulates the business logic for an order and maintains consistency.
type Order struct {
	mink.AggregateBase

	CustomerID     string
	Items          []OrderItem
	Status         string
	TrackingNumber string
}

// NewOrder creates a new Order aggregate with the given ID.
func NewOrder(id string) *Order {
	return &Order{
		AggregateBase: mink.NewAggregateBase(id, "Order"),
		Items:         make([]OrderItem, 0),
	}
}

// Create initializes a new order for a customer.
func (o *Order) Create(customerID string) error {
	if o.Status != "" {
		return fmt.Errorf("order already exists")
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
		return fmt.Errorf("order not created")
	}
	if o.Status == "Shipped" {
		return fmt.Errorf("cannot add items to shipped order")
	}
	if quantity <= 0 {
		return fmt.Errorf("quantity must be positive")
	}
	if price < 0 {
		return fmt.Errorf("price cannot be negative")
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

// Ship marks the order as shipped with a tracking number.
func (o *Order) Ship(trackingNumber string) error {
	if o.Status == "" {
		return fmt.Errorf("order not created")
	}
	if o.Status == "Shipped" {
		return fmt.Errorf("order already shipped")
	}
	if len(o.Items) == 0 {
		return fmt.Errorf("cannot ship empty order")
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
// This method is called when loading an aggregate from the event store.
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

func main() {
	ctx := context.Background()

	// Create an event store with the in-memory adapter
	// For production, use postgres.NewAdapter(ctx, connStr)
	adapter := memory.NewAdapter()
	store := mink.New(adapter)

	// Register event types so they can be serialized/deserialized
	store.RegisterEvents(OrderCreated{}, ItemAdded{}, OrderShipped{})

	fmt.Println("=== go-mink Event Sourcing Example ===")
	fmt.Println()

	// --- Create and Save an Order ---
	fmt.Println("1. Creating a new order...")

	order := NewOrder("order-001")
	if err := order.Create("customer-123"); err != nil {
		log.Fatal(err)
	}
	if err := order.AddItem("WIDGET-A", 2, 29.99); err != nil {
		log.Fatal(err)
	}
	if err := order.AddItem("GADGET-B", 1, 149.99); err != nil {
		log.Fatal(err)
	}

	fmt.Printf("   Order ID: %s\n", order.AggregateID())
	fmt.Printf("   Customer: %s\n", order.CustomerID)
	fmt.Printf("   Items: %d\n", len(order.Items))
	fmt.Printf("   Total: $%.2f\n", order.TotalAmount())
	fmt.Printf("   Uncommitted events: %d\n", len(order.UncommittedEvents()))
	fmt.Println()

	// Save the aggregate (this persists the uncommitted events)
	if err := store.SaveAggregate(ctx, order); err != nil {
		log.Fatal(err)
	}
	fmt.Println("   ✓ Order saved to event store")
	fmt.Printf("   Uncommitted events after save: %d\n", len(order.UncommittedEvents()))
	fmt.Println()

	// --- Load the Order (in a new instance) ---
	fmt.Println("2. Loading order from event store...")

	loadedOrder := NewOrder("order-001")
	if err := store.LoadAggregate(ctx, loadedOrder); err != nil {
		log.Fatal(err)
	}

	fmt.Printf("   Order ID: %s\n", loadedOrder.AggregateID())
	fmt.Printf("   Customer: %s\n", loadedOrder.CustomerID)
	fmt.Printf("   Status: %s\n", loadedOrder.Status)
	fmt.Printf("   Items: %d\n", len(loadedOrder.Items))
	fmt.Printf("   Total: $%.2f\n", loadedOrder.TotalAmount())
	fmt.Printf("   Version: %d\n", loadedOrder.Version())
	fmt.Println()

	// --- Add More Items and Ship ---
	fmt.Println("3. Adding another item and shipping...")

	if err := loadedOrder.AddItem("CABLE-C", 3, 9.99); err != nil {
		log.Fatal(err)
	}
	if err := loadedOrder.Ship("TRACK-12345"); err != nil {
		log.Fatal(err)
	}

	if err := store.SaveAggregate(ctx, loadedOrder); err != nil {
		log.Fatal(err)
	}
	fmt.Println("   ✓ Order updated and saved")
	fmt.Println()

	// --- Load Raw Events ---
	fmt.Println("4. Loading raw events from stream...")

	events, err := store.LoadRaw(ctx, "Order-order-001", 0)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("   Found %d events:\n", len(events))
	for i, event := range events {
		fmt.Printf("   [%d] v%d %s at position %d\n",
			i+1, event.Version, event.Type, event.GlobalPosition)
	}
	fmt.Println()

	// --- Get Stream Info ---
	fmt.Println("5. Getting stream information...")

	info, err := store.GetStreamInfo(ctx, "Order-order-001")
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("   Stream ID: %s\n", info.StreamID)
	fmt.Printf("   Category: %s\n", info.Category)
	fmt.Printf("   Current Version: %d\n", info.Version)
	fmt.Printf("   Event Count: %d\n", info.EventCount)
	fmt.Printf("   Created: %s\n", info.CreatedAt.Format("2006-01-02 15:04:05"))
	fmt.Println()

	// --- Final State ---
	fmt.Println("6. Final order state...")

	finalOrder := NewOrder("order-001")
	if err := store.LoadAggregate(ctx, finalOrder); err != nil {
		log.Fatal(err)
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

	fmt.Println("=== Example Complete ===")
}
