// Example: Saga (Process Manager) Pattern
//
// This example demonstrates how to use the saga testing utilities
// to build and test long-running business processes.
//
// Run with: go run .
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/AshkanYarmoradi/go-mink"
	"github.com/AshkanYarmoradi/go-mink/adapters/memory"
)

// =============================================================================
// Domain Events
// =============================================================================

type OrderPlaced struct {
	OrderID    string    `json:"orderId"`
	CustomerID string    `json:"customerId"`
	Amount     float64   `json:"amount"`
	Items      []string  `json:"items"`
	PlacedAt   time.Time `json:"placedAt"`
}

type PaymentProcessed struct {
	OrderID       string    `json:"orderId"`
	TransactionID string    `json:"transactionId"`
	Amount        float64   `json:"amount"`
	ProcessedAt   time.Time `json:"processedAt"`
}

type PaymentFailed struct {
	OrderID string `json:"orderId"`
	Reason  string `json:"reason"`
}

type InventoryReserved struct {
	OrderID     string   `json:"orderId"`
	Items       []string `json:"items"`
	WarehouseID string   `json:"warehouseId"`
}

type InventoryReservationFailed struct {
	OrderID string   `json:"orderId"`
	Items   []string `json:"items"`
	Reason  string   `json:"reason"`
}

type ShipmentCreated struct {
	OrderID        string    `json:"orderId"`
	TrackingNumber string    `json:"trackingNumber"`
	CreatedAt      time.Time `json:"createdAt"`
}

// =============================================================================
// Commands
// =============================================================================

type ProcessPaymentCommand struct {
	mink.CommandBase
	OrderID string  `json:"orderId"`
	Amount  float64 `json:"amount"`
}

func (c *ProcessPaymentCommand) CommandType() string { return "ProcessPayment" }
func (c *ProcessPaymentCommand) Validate() error     { return nil }

type ReserveInventoryCommand struct {
	mink.CommandBase
	OrderID string   `json:"orderId"`
	Items   []string `json:"items"`
}

func (c *ReserveInventoryCommand) CommandType() string { return "ReserveInventory" }
func (c *ReserveInventoryCommand) Validate() error     { return nil }

type CreateShipmentCommand struct {
	mink.CommandBase
	OrderID string `json:"orderId"`
}

func (c *CreateShipmentCommand) CommandType() string { return "CreateShipment" }
func (c *CreateShipmentCommand) Validate() error     { return nil }

type CompleteOrderCommand struct {
	mink.CommandBase
	OrderID string `json:"orderId"`
}

func (c *CompleteOrderCommand) CommandType() string { return "CompleteOrder" }
func (c *CompleteOrderCommand) Validate() error     { return nil }

type CancelOrderCommand struct {
	mink.CommandBase
	OrderID string `json:"orderId"`
	Reason  string `json:"reason"`
}

func (c *CancelOrderCommand) CommandType() string { return "CancelOrder" }
func (c *CancelOrderCommand) Validate() error     { return nil }

type RefundPaymentCommand struct {
	mink.CommandBase
	OrderID string `json:"orderId"`
}

func (c *RefundPaymentCommand) CommandType() string { return "RefundPayment" }
func (c *RefundPaymentCommand) Validate() error     { return nil }

// =============================================================================
// Order Fulfillment Saga
// =============================================================================

// SagaState represents the possible states of the saga
type SagaState string

const (
	StateStarted           SagaState = "Started"
	StatePaymentPending    SagaState = "PaymentPending"
	StatePaymentReceived   SagaState = "PaymentReceived"
	StateInventoryPending  SagaState = "InventoryPending"
	StateInventoryReserved SagaState = "InventoryReserved"
	StateShipmentPending   SagaState = "ShipmentPending"
	StateShipmentCreated   SagaState = "ShipmentCreated"
	StateCompleted         SagaState = "Completed"
	StateCancelled         SagaState = "Cancelled"
	StateCompensating      SagaState = "Compensating"
)

// OrderFulfillmentSaga coordinates the order fulfillment process
type OrderFulfillmentSaga struct {
	OrderID    string
	CustomerID string
	Amount     float64
	Items      []string
	State      SagaState

	// Track what's been done for compensation
	PaymentProcessed  bool
	InventoryReserved bool
	ShipmentCreated   bool
}

func NewOrderFulfillmentSaga() *OrderFulfillmentSaga {
	return &OrderFulfillmentSaga{
		State: StateStarted,
	}
}

func (s *OrderFulfillmentSaga) Name() string {
	return "OrderFulfillment"
}

func (s *OrderFulfillmentSaga) HandleEvent(ctx context.Context, event mink.StoredEvent) ([]mink.Command, error) {
	switch event.Type {
	case "OrderPlaced":
		return s.handleOrderPlaced(event)
	case "PaymentProcessed":
		return s.handlePaymentProcessed(event)
	case "PaymentFailed":
		return s.handlePaymentFailed(event)
	case "InventoryReserved":
		return s.handleInventoryReserved(event)
	case "InventoryReservationFailed":
		return s.handleInventoryReservationFailed(event)
	case "ShipmentCreated":
		return s.handleShipmentCreated(event)
	}
	return nil, nil
}

func (s *OrderFulfillmentSaga) handleOrderPlaced(event mink.StoredEvent) ([]mink.Command, error) {
	var data OrderPlaced
	if err := json.Unmarshal(event.Data, &data); err != nil {
		return nil, err
	}

	s.OrderID = data.OrderID
	s.CustomerID = data.CustomerID
	s.Amount = data.Amount
	s.Items = data.Items
	s.State = StatePaymentPending

	fmt.Printf("📦 Saga: Order %s placed, processing payment of $%.2f\n", s.OrderID, s.Amount)

	return []mink.Command{
		&ProcessPaymentCommand{OrderID: s.OrderID, Amount: s.Amount},
	}, nil
}

func (s *OrderFulfillmentSaga) handlePaymentProcessed(event mink.StoredEvent) ([]mink.Command, error) {
	s.PaymentProcessed = true
	s.State = StateInventoryPending

	fmt.Printf("💳 Saga: Payment received for order %s, reserving inventory\n", s.OrderID)

	return []mink.Command{
		&ReserveInventoryCommand{OrderID: s.OrderID, Items: s.Items},
	}, nil
}

func (s *OrderFulfillmentSaga) handlePaymentFailed(event mink.StoredEvent) ([]mink.Command, error) {
	var data PaymentFailed
	if err := json.Unmarshal(event.Data, &data); err != nil {
		return nil, err
	}

	s.State = StateCancelled
	fmt.Printf("❌ Saga: Payment failed for order %s: %s\n", s.OrderID, data.Reason)

	return []mink.Command{
		&CancelOrderCommand{OrderID: s.OrderID, Reason: "Payment failed: " + data.Reason},
	}, nil
}

func (s *OrderFulfillmentSaga) handleInventoryReserved(event mink.StoredEvent) ([]mink.Command, error) {
	s.InventoryReserved = true
	s.State = StateShipmentPending

	fmt.Printf("📦 Saga: Inventory reserved for order %s, creating shipment\n", s.OrderID)

	return []mink.Command{
		&CreateShipmentCommand{OrderID: s.OrderID},
	}, nil
}

func (s *OrderFulfillmentSaga) handleInventoryReservationFailed(event mink.StoredEvent) ([]mink.Command, error) {
	s.State = StateCompensating

	fmt.Printf("⚠️ Saga: Inventory reservation failed, compensating...\n")

	// Need to refund the payment
	return []mink.Command{
		&RefundPaymentCommand{OrderID: s.OrderID},
		&CancelOrderCommand{OrderID: s.OrderID, Reason: "Inventory not available"},
	}, nil
}

func (s *OrderFulfillmentSaga) handleShipmentCreated(event mink.StoredEvent) ([]mink.Command, error) {
	s.ShipmentCreated = true
	s.State = StateCompleted

	fmt.Printf("✅ Saga: Shipment created, order %s completed!\n", s.OrderID)

	return []mink.Command{
		&CompleteOrderCommand{OrderID: s.OrderID},
	}, nil
}

func (s *OrderFulfillmentSaga) IsComplete() bool {
	return s.State == StateCompleted || s.State == StateCancelled
}

func (s *OrderFulfillmentSaga) SagaState() interface{} {
	return s
}

// =============================================================================
// Simulation
// =============================================================================

func main() {
	fmt.Println("=== Order Fulfillment Saga Example ===")
	fmt.Println()

	// Setup
	adapter := memory.NewAdapter()
	store := mink.New(adapter)
	ctx := context.Background()

	// Create saga instance
	saga := NewOrderFulfillmentSaga()

	// Simulate the happy path
	fmt.Println("📋 Simulating successful order fulfillment...")
	fmt.Println()

	events := []mink.StoredEvent{
		makeEvent("OrderPlaced", OrderPlaced{
			OrderID:    "ORD-001",
			CustomerID: "CUST-123",
			Amount:     149.99,
			Items:      []string{"SKU-A", "SKU-B"},
			PlacedAt:   time.Now(),
		}),
		makeEvent("PaymentProcessed", PaymentProcessed{
			OrderID:       "ORD-001",
			TransactionID: "TXN-456",
			Amount:        149.99,
			ProcessedAt:   time.Now(),
		}),
		makeEvent("InventoryReserved", InventoryReserved{
			OrderID:     "ORD-001",
			Items:       []string{"SKU-A", "SKU-B"},
			WarehouseID: "WH-01",
		}),
		makeEvent("ShipmentCreated", ShipmentCreated{
			OrderID:        "ORD-001",
			TrackingNumber: "TRACK-789",
			CreatedAt:      time.Now(),
		}),
	}

	// Process events through saga
	var allCommands []mink.Command
	for _, event := range events {
		cmds, err := saga.HandleEvent(ctx, event)
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			continue
		}
		allCommands = append(allCommands, cmds...)
	}

	fmt.Println()
	fmt.Println("📊 Saga State:")
	fmt.Printf("   - Order ID: %s\n", saga.OrderID)
	fmt.Printf("   - State: %s\n", saga.State)
	fmt.Printf("   - Payment Processed: %v\n", saga.PaymentProcessed)
	fmt.Printf("   - Inventory Reserved: %v\n", saga.InventoryReserved)
	fmt.Printf("   - Shipment Created: %v\n", saga.ShipmentCreated)
	fmt.Printf("   - Is Complete: %v\n", saga.IsComplete())

	fmt.Println()
	fmt.Println("📤 Commands Generated:")
	for _, cmd := range allCommands {
		fmt.Printf("   - %s\n", cmd.CommandType())
	}

	// Demonstrate compensation (failure scenario)
	fmt.Println()
	fmt.Println("---")
	fmt.Println()
	fmt.Println("📋 Simulating failed order (inventory not available)...")
	fmt.Println()

	failedSaga := NewOrderFulfillmentSaga()
	failureEvents := []mink.StoredEvent{
		makeEvent("OrderPlaced", OrderPlaced{
			OrderID:    "ORD-002",
			CustomerID: "CUST-456",
			Amount:     299.99,
			Items:      []string{"SKU-RARE"},
			PlacedAt:   time.Now(),
		}),
		makeEvent("PaymentProcessed", PaymentProcessed{
			OrderID:       "ORD-002",
			TransactionID: "TXN-789",
			Amount:        299.99,
			ProcessedAt:   time.Now(),
		}),
		makeEvent("InventoryReservationFailed", InventoryReservationFailed{
			OrderID: "ORD-002",
			Items:   []string{"SKU-RARE"},
			Reason:  "Item out of stock",
		}),
	}

	var compensationCommands []mink.Command
	for _, event := range failureEvents {
		cmds, _ := failedSaga.HandleEvent(ctx, event)
		compensationCommands = append(compensationCommands, cmds...)
	}

	fmt.Println()
	fmt.Println("📊 Failed Saga State:")
	fmt.Printf("   - Order ID: %s\n", failedSaga.OrderID)
	fmt.Printf("   - State: %s\n", failedSaga.State)
	fmt.Printf("   - Is Complete: %v\n", failedSaga.IsComplete())

	fmt.Println()
	fmt.Println("📤 Compensation Commands:")
	for _, cmd := range compensationCommands {
		fmt.Printf("   - %s\n", cmd.CommandType())
	}

	// Show event store usage
	fmt.Println()
	fmt.Println("---")
	fmt.Println()
	fmt.Println("💾 Storing saga events...")

	sagaEvents := make([]interface{}, len(events))
	for i, event := range events {
		sagaEvents[i] = event
	}
	err := store.Append(ctx, "saga-order-001", sagaEvents, mink.ExpectVersion(mink.NoStream))
	if err != nil {
		fmt.Printf("Error storing events: %v\n", err)
	}

	storedEvents, _ := store.Load(ctx, "saga-order-001")
	fmt.Printf("✅ Stored %d events in event store\n", len(storedEvents))

	fmt.Println()
	fmt.Println("=== Example Complete ===")
}

func makeEvent(eventType string, data interface{}) mink.StoredEvent {
	jsonData, _ := json.Marshal(data)
	return mink.StoredEvent{
		ID:        fmt.Sprintf("evt-%s-%d", eventType, time.Now().UnixNano()),
		StreamID:  "saga-stream",
		Type:      eventType,
		Data:      jsonData,
		Version:   1,
		Timestamp: time.Now(),
	}
}
