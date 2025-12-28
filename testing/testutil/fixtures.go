// Package testutil provides test utilities and fixtures for go-mink.
package testutil

import (
	"fmt"
	"sync"

	"github.com/AshkanYarmoradi/go-mink"
)

// =============================================================================
// Domain Events for Testing
// =============================================================================

// OrderCreated event for testing.
type OrderCreated struct {
	OrderID    string `json:"orderId"`
	CustomerID string `json:"customerId"`
}

// ItemAdded event for testing.
type ItemAdded struct {
	OrderID  string  `json:"orderId"`
	SKU      string  `json:"sku"`
	Quantity int     `json:"quantity"`
	Price    float64 `json:"price"`
}

// OrderShipped event for testing.
type OrderShipped struct {
	OrderID        string `json:"orderId"`
	TrackingNumber string `json:"trackingNumber"`
}

// OrderCancelled event for testing.
type OrderCancelled struct {
	OrderID string `json:"orderId"`
	Reason  string `json:"reason"`
}

// =============================================================================
// Test Aggregate
// =============================================================================

// OrderItem represents an item in an order.
type OrderItem struct {
	SKU      string
	Quantity int
	Price    float64
}

// Order is a test aggregate for E2E tests.
type Order struct {
	mink.AggregateBase

	CustomerID     string
	Items          []OrderItem
	Status         string
	TrackingNumber string
	CancelReason   string
}

// NewOrder creates a new Order aggregate for testing.
func NewOrder(id string) *Order {
	return &Order{
		AggregateBase: mink.NewAggregateBase(id, "Order"),
		Items:         make([]OrderItem, 0),
	}
}

// Create initializes the order.
func (o *Order) Create(customerID string) error {
	if o.Status != "" {
		return fmt.Errorf("order already exists")
	}
	o.Apply(OrderCreated{OrderID: o.AggregateID(), CustomerID: customerID})
	o.CustomerID = customerID
	o.Status = "Created"
	return nil
}

// AddItem adds an item to the order.
func (o *Order) AddItem(sku string, qty int, price float64) error {
	if o.Status != "Created" {
		return fmt.Errorf("cannot add items: order status is %s", o.Status)
	}
	o.Apply(ItemAdded{OrderID: o.AggregateID(), SKU: sku, Quantity: qty, Price: price})
	o.Items = append(o.Items, OrderItem{SKU: sku, Quantity: qty, Price: price})
	return nil
}

// Ship marks the order as shipped.
func (o *Order) Ship(trackingNumber string) error {
	if o.Status != "Created" {
		return fmt.Errorf("cannot ship: order status is %s", o.Status)
	}
	if len(o.Items) == 0 {
		return fmt.Errorf("cannot ship empty order")
	}
	o.Apply(OrderShipped{OrderID: o.AggregateID(), TrackingNumber: trackingNumber})
	o.Status = "Shipped"
	o.TrackingNumber = trackingNumber
	return nil
}

// Cancel cancels the order.
func (o *Order) Cancel(reason string) error {
	if o.Status == "Shipped" {
		return fmt.Errorf("cannot cancel shipped order")
	}
	if o.Status == "Cancelled" {
		return fmt.Errorf("order already cancelled")
	}
	o.Apply(OrderCancelled{OrderID: o.AggregateID(), Reason: reason})
	o.Status = "Cancelled"
	o.CancelReason = reason
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

// ApplyEvent applies historical events to rebuild state.
func (o *Order) ApplyEvent(event interface{}) error {
	switch e := event.(type) {
	case OrderCreated:
		o.CustomerID = e.CustomerID
		o.Status = "Created"
	case ItemAdded:
		o.Items = append(o.Items, OrderItem{SKU: e.SKU, Quantity: e.Quantity, Price: e.Price})
	case OrderShipped:
		o.Status = "Shipped"
		o.TrackingNumber = e.TrackingNumber
	case OrderCancelled:
		o.Status = "Cancelled"
		o.CancelReason = e.Reason
	default:
		return fmt.Errorf("unknown event type: %T", event)
	}
	o.IncrementVersion()
	return nil
}

// =============================================================================
// Simple Read Model for Testing
// =============================================================================

// OrderSummary is a simple read model for orders.
type OrderSummary struct {
	OrderID        string
	CustomerID     string
	ItemCount      int
	TotalAmount    float64
	Status         string
	TrackingNumber string
}

// OrderReadModel maintains a collection of order summaries.
type OrderReadModel struct {
	mu      sync.RWMutex
	orders  map[string]*OrderSummary
	updates int
}

// NewOrderReadModel creates a new read model.
func NewOrderReadModel() *OrderReadModel {
	return &OrderReadModel{
		orders: make(map[string]*OrderSummary),
	}
}

// Apply processes an event and updates the read model.
func (rm *OrderReadModel) Apply(event mink.StoredEvent) error {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	// Extract order ID from stream ID (format: "Order-{id}")
	orderID := event.StreamID[6:] // Remove "Order-" prefix

	switch event.Type {
	case "OrderCreated":
		rm.orders[orderID] = &OrderSummary{
			OrderID: orderID,
			Status:  "Created",
		}
	case "ItemAdded":
		if summary, ok := rm.orders[orderID]; ok {
			summary.ItemCount++
			summary.TotalAmount += 10.0 // Simplified for testing
		}
	case "OrderShipped":
		if summary, ok := rm.orders[orderID]; ok {
			summary.Status = "Shipped"
		}
	case "OrderCancelled":
		if summary, ok := rm.orders[orderID]; ok {
			summary.Status = "Cancelled"
		}
	}
	rm.updates++
	return nil
}

// Get returns an order summary by ID.
func (rm *OrderReadModel) Get(orderID string) *OrderSummary {
	rm.mu.RLock()
	defer rm.mu.RUnlock()
	return rm.orders[orderID]
}

// Count returns the number of orders in the read model.
func (rm *OrderReadModel) Count() int {
	rm.mu.RLock()
	defer rm.mu.RUnlock()
	return len(rm.orders)
}

// UpdateCount returns the number of updates processed.
func (rm *OrderReadModel) UpdateCount() int {
	rm.mu.RLock()
	defer rm.mu.RUnlock()
	return rm.updates
}

// RegisterTestEvents registers test event types with the store.
func RegisterTestEvents(store *mink.EventStore) {
	store.RegisterEvents(OrderCreated{}, ItemAdded{}, OrderShipped{}, OrderCancelled{})
}
