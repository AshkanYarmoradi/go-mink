package mink

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Test aggregate implementation
type TestOrder struct {
	AggregateBase
	CustomerID string
	Items      []OrderItem
	Status     string
}

type OrderItem struct {
	SKU      string
	Quantity int
	Price    float64
}

// Test events
type TestOrderCreated struct {
	OrderID    string
	CustomerID string
}

type TestItemAdded struct {
	SKU      string
	Quantity int
	Price    float64
}

type TestOrderSubmitted struct {
	OrderID string
}

func NewTestOrder(id string) *TestOrder {
	return &TestOrder{
		AggregateBase: NewAggregateBase(id, "Order"),
		Items:         make([]OrderItem, 0),
	}
}

func (o *TestOrder) Create(customerID string) {
	o.Apply(TestOrderCreated{
		OrderID:    o.AggregateID(),
		CustomerID: customerID,
	})
	o.CustomerID = customerID
	o.Status = "Created"
}

func (o *TestOrder) AddItem(sku string, quantity int, price float64) {
	o.Apply(TestItemAdded{
		SKU:      sku,
		Quantity: quantity,
		Price:    price,
	})
	o.Items = append(o.Items, OrderItem{
		SKU:      sku,
		Quantity: quantity,
		Price:    price,
	})
}

func (o *TestOrder) Submit() {
	o.Apply(TestOrderSubmitted{OrderID: o.AggregateID()})
	o.Status = "Submitted"
}

func (o *TestOrder) ApplyEvent(event interface{}) error {
	switch e := event.(type) {
	case TestOrderCreated:
		o.CustomerID = e.CustomerID
		o.Status = "Created"
	case TestItemAdded:
		o.Items = append(o.Items, OrderItem(e))
	case TestOrderSubmitted:
		o.Status = "Submitted"
	}
	o.IncrementVersion()
	return nil
}

func TestAggregateBase(t *testing.T) {
	t.Run("NewAggregateBase creates with ID and type", func(t *testing.T) {
		base := NewAggregateBase("order-123", "Order")

		assert.Equal(t, "order-123", base.AggregateID())
		assert.Equal(t, "Order", base.AggregateType())
		assert.Equal(t, int64(0), base.Version())
		assert.Empty(t, base.UncommittedEvents())
	})

	t.Run("SetID and SetType", func(t *testing.T) {
		base := AggregateBase{}
		base.SetID("order-456")
		base.SetType("Order")

		assert.Equal(t, "order-456", base.AggregateID())
		assert.Equal(t, "Order", base.AggregateType())
	})

	t.Run("Version management", func(t *testing.T) {
		base := NewAggregateBase("order-123", "Order")

		assert.Equal(t, int64(0), base.Version())

		base.SetVersion(5)
		assert.Equal(t, int64(5), base.Version())

		base.IncrementVersion()
		assert.Equal(t, int64(6), base.Version())
	})

	t.Run("Apply adds to uncommitted events", func(t *testing.T) {
		base := NewAggregateBase("order-123", "Order")

		event1 := TestOrderCreated{OrderID: "order-123", CustomerID: "cust-456"}
		event2 := TestItemAdded{SKU: "SKU-001", Quantity: 2, Price: 29.99}

		base.Apply(event1)
		base.Apply(event2)

		events := base.UncommittedEvents()
		assert.Len(t, events, 2)
		assert.Equal(t, event1, events[0])
		assert.Equal(t, event2, events[1])
	})

	t.Run("ClearUncommittedEvents removes all events", func(t *testing.T) {
		base := NewAggregateBase("order-123", "Order")
		base.Apply(TestOrderCreated{OrderID: "order-123"})

		assert.True(t, base.HasUncommittedEvents())

		base.ClearUncommittedEvents()

		assert.False(t, base.HasUncommittedEvents())
		assert.Empty(t, base.UncommittedEvents())
	})

	t.Run("StreamID returns correct format", func(t *testing.T) {
		base := NewAggregateBase("123", "Order")

		streamID := base.StreamID()
		assert.Equal(t, "Order", streamID.Category)
		assert.Equal(t, "123", streamID.ID)
		assert.Equal(t, "Order-123", streamID.String())
	})
}

func TestTestOrder_Integration(t *testing.T) {
	t.Run("create order", func(t *testing.T) {
		order := NewTestOrder("order-123")
		order.Create("customer-456")

		assert.Equal(t, "order-123", order.AggregateID())
		assert.Equal(t, "Order", order.AggregateType())
		assert.Equal(t, "customer-456", order.CustomerID)
		assert.Equal(t, "Created", order.Status)
		assert.Len(t, order.UncommittedEvents(), 1)
	})

	t.Run("add items", func(t *testing.T) {
		order := NewTestOrder("order-123")
		order.Create("customer-456")
		order.AddItem("SKU-001", 2, 29.99)
		order.AddItem("SKU-002", 1, 49.99)

		assert.Len(t, order.Items, 2)
		assert.Equal(t, "SKU-001", order.Items[0].SKU)
		assert.Equal(t, 2, order.Items[0].Quantity)
		assert.Len(t, order.UncommittedEvents(), 3)
	})

	t.Run("submit order", func(t *testing.T) {
		order := NewTestOrder("order-123")
		order.Create("customer-456")
		order.AddItem("SKU-001", 1, 29.99)
		order.Submit()

		assert.Equal(t, "Submitted", order.Status)
		assert.Len(t, order.UncommittedEvents(), 3)
	})

	t.Run("apply events rebuilds state", func(t *testing.T) {
		// Simulate loading from store
		order := NewTestOrder("order-123")

		// Apply stored events
		_ = order.ApplyEvent(TestOrderCreated{OrderID: "order-123", CustomerID: "customer-456"})
		_ = order.ApplyEvent(TestItemAdded{SKU: "SKU-001", Quantity: 2, Price: 29.99})
		_ = order.ApplyEvent(TestItemAdded{SKU: "SKU-002", Quantity: 1, Price: 49.99})
		_ = order.ApplyEvent(TestOrderSubmitted{OrderID: "order-123"})

		assert.Equal(t, "customer-456", order.CustomerID)
		assert.Equal(t, "Submitted", order.Status)
		assert.Len(t, order.Items, 2)
		assert.Equal(t, int64(4), order.Version())
		// No uncommitted events when loading
		assert.Empty(t, order.UncommittedEvents())
	})
}

func TestAggregate_Interface(t *testing.T) {
	t.Run("TestOrder implements Aggregate", func(t *testing.T) {
		var _ Aggregate = (*TestOrder)(nil)
	})

	t.Run("AggregateBase implements core methods", func(t *testing.T) {
		order := NewTestOrder("order-123")

		// Test all interface methods
		assert.Equal(t, "order-123", order.AggregateID())
		assert.Equal(t, "Order", order.AggregateType())
		assert.Equal(t, int64(0), order.Version())
		assert.Empty(t, order.UncommittedEvents())

		order.Create("customer-456")
		assert.NotEmpty(t, order.UncommittedEvents())

		order.ClearUncommittedEvents()
		assert.Empty(t, order.UncommittedEvents())
	})
}
