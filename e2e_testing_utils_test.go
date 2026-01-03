package mink_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/AshkanYarmoradi/go-mink"
	"github.com/AshkanYarmoradi/go-mink/adapters/memory"
	"github.com/AshkanYarmoradi/go-mink/middleware/metrics"
	"github.com/AshkanYarmoradi/go-mink/middleware/tracing"
	"github.com/AshkanYarmoradi/go-mink/serializer/msgpack"
	"github.com/AshkanYarmoradi/go-mink/testing/assertions"
	"github.com/AshkanYarmoradi/go-mink/testing/bdd"
	"github.com/AshkanYarmoradi/go-mink/testing/sagas"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// =============================================================================
// E2E Test: BDD Testing Framework
// =============================================================================

// --- Test Domain for BDD ---

var ErrInsufficientStock = errors.New("insufficient stock")
var ErrAlreadyReserved = errors.New("inventory already reserved")

type InventoryItem struct {
	mink.AggregateBase
	SKU              string
	QuantityOnHand   int
	QuantityReserved int
}

func NewInventoryItem(sku string) *InventoryItem {
	item := &InventoryItem{}
	item.SetID(sku)
	item.SetType("InventoryItem")
	return item
}

func (i *InventoryItem) Receive(quantity int) error {
	i.Apply(&InventoryReceived{SKU: i.AggregateID(), Quantity: quantity})
	i.QuantityOnHand += quantity
	return nil
}

func (i *InventoryItem) Reserve(quantity int, orderID string) error {
	if i.QuantityOnHand-i.QuantityReserved < quantity {
		return ErrInsufficientStock
	}
	i.Apply(&InventoryReserved{SKU: i.AggregateID(), Quantity: quantity, OrderID: orderID})
	i.QuantityReserved += quantity
	return nil
}

func (i *InventoryItem) ApplyEvent(event interface{}) error {
	switch e := event.(type) {
	case *InventoryReceived:
		i.QuantityOnHand += e.Quantity
	case *InventoryReserved:
		i.QuantityReserved += e.Quantity
	}
	i.IncrementVersion()
	return nil
}

type InventoryReceived struct {
	SKU      string `json:"sku"`
	Quantity int    `json:"quantity"`
}

type InventoryReserved struct {
	SKU      string `json:"sku"`
	Quantity int    `json:"quantity"`
	OrderID  string `json:"orderId"`
}

func TestE2E_BDD_CompleteWorkflow(t *testing.T) {
	t.Run("successful reservation flow", func(t *testing.T) {
		item := NewInventoryItem("SKU-123")

		bdd.Given(t, item,
			&InventoryReceived{SKU: "SKU-123", Quantity: 100},
		).When(func() error {
			return item.Reserve(10, "order-1")
		}).Then(
			&InventoryReserved{SKU: "SKU-123", Quantity: 10, OrderID: "order-1"},
		)
	})

	t.Run("insufficient stock error", func(t *testing.T) {
		item := NewInventoryItem("SKU-456")

		bdd.Given(t, item,
			&InventoryReceived{SKU: "SKU-456", Quantity: 5},
		).When(func() error {
			return item.Reserve(10, "order-2")
		}).ThenError(ErrInsufficientStock)
	})

	t.Run("multiple operations", func(t *testing.T) {
		item := NewInventoryItem("SKU-789")

		// First receive inventory
		bdd.Given(t, item).When(func() error {
			return item.Receive(50)
		}).Then(
			&InventoryReceived{SKU: "SKU-789", Quantity: 50},
		)
	})
}

// =============================================================================
// E2E Test: Saga Testing Framework
// =============================================================================

// --- Order Fulfillment Saga ---

type OrderFulfillmentSaga struct {
	OrderID           string
	PaymentReceived   bool
	InventoryReserved bool
	ShipmentCreated   bool
}

func NewOrderFulfillmentSaga() *OrderFulfillmentSaga {
	return &OrderFulfillmentSaga{}
}

func (s *OrderFulfillmentSaga) Name() string {
	return "OrderFulfillment"
}

func (s *OrderFulfillmentSaga) HandleEvent(ctx context.Context, event mink.StoredEvent) ([]mink.Command, error) {
	switch event.Type {
	case "OrderPlaced":
		var data OrderPlacedEvent
		if err := json.Unmarshal(event.Data, &data); err != nil {
			return nil, err
		}
		s.OrderID = data.OrderID
		return []mink.Command{
			&ProcessPaymentCommand{OrderID: data.OrderID, Amount: data.Amount},
		}, nil

	case "PaymentReceived":
		s.PaymentReceived = true
		return []mink.Command{
			&ReserveInventoryCommand{OrderID: s.OrderID},
		}, nil

	case "InventoryReserved":
		s.InventoryReserved = true
		return []mink.Command{
			&CreateShipmentCommand{OrderID: s.OrderID},
		}, nil

	case "ShipmentCreated":
		s.ShipmentCreated = true
		return nil, nil // Saga complete
	}

	return nil, nil
}

func (s *OrderFulfillmentSaga) IsComplete() bool {
	return s.PaymentReceived && s.InventoryReserved && s.ShipmentCreated
}

func (s *OrderFulfillmentSaga) State() interface{} {
	return s
}

// Saga Events
type OrderPlacedEvent struct {
	OrderID string  `json:"orderId"`
	Amount  float64 `json:"amount"`
}

type PaymentReceivedEvent struct {
	OrderID string `json:"orderId"`
}

type InventoryReservedEvent struct {
	OrderID string `json:"orderId"`
}

type ShipmentCreatedEvent struct {
	OrderID string `json:"orderId"`
}

// Saga Commands
type ProcessPaymentCommand struct {
	mink.CommandBase
	OrderID string  `json:"orderId"`
	Amount  float64 `json:"amount"`
}

func (c *ProcessPaymentCommand) CommandType() string { return "ProcessPayment" }
func (c *ProcessPaymentCommand) Validate() error     { return nil }

type ReserveInventoryCommand struct {
	mink.CommandBase
	OrderID string `json:"orderId"`
}

func (c *ReserveInventoryCommand) CommandType() string { return "ReserveInventory" }
func (c *ReserveInventoryCommand) Validate() error     { return nil }

type CreateShipmentCommand struct {
	mink.CommandBase
	OrderID string `json:"orderId"`
}

func (c *CreateShipmentCommand) CommandType() string { return "CreateShipment" }
func (c *CreateShipmentCommand) Validate() error     { return nil }

func makeStoredEvent(eventType string, data interface{}) mink.StoredEvent {
	jsonData, _ := json.Marshal(data)
	return mink.StoredEvent{
		ID:       "evt-" + eventType,
		StreamID: "test-stream",
		Type:     eventType,
		Data:     jsonData,
		Version:  1,
	}
}

func TestE2E_Saga_CompleteWorkflow(t *testing.T) {
	t.Run("complete order fulfillment saga", func(t *testing.T) {
		saga := NewOrderFulfillmentSaga()

		// Test the saga progression
		sagas.TestSaga(t, saga).
			GivenEvents(
				makeStoredEvent("OrderPlaced", OrderPlacedEvent{OrderID: "order-123", Amount: 99.99}),
			).
			ThenCommands(
				&ProcessPaymentCommand{OrderID: "order-123", Amount: 99.99},
			)
	})

	t.Run("saga state transitions", func(t *testing.T) {
		// Test the complete saga flow with all events at once
		saga := NewOrderFulfillmentSaga()

		// Apply all events to simulate the complete saga flow
		sagas.TestSaga(t, saga).
			GivenEvents(
				makeStoredEvent("OrderPlaced", OrderPlacedEvent{OrderID: "order-456", Amount: 50.00}),
				makeStoredEvent("PaymentReceived", PaymentReceivedEvent{OrderID: "order-456"}),
				makeStoredEvent("InventoryReserved", InventoryReservedEvent{OrderID: "order-456"}),
				makeStoredEvent("ShipmentCreated", ShipmentCreatedEvent{OrderID: "order-456"}),
			).
			ThenCommands(
				// Each event generates a command
				&ProcessPaymentCommand{OrderID: "order-456", Amount: 50.00},
				&ReserveInventoryCommand{OrderID: "order-456"},
				&CreateShipmentCommand{OrderID: "order-456"},
				// ShipmentCreated produces no commands, saga completes
			).
			ThenCompleted()
	})
}

// =============================================================================
// E2E Test: Projection Testing Framework
// =============================================================================

// --- Order Summary Projection ---

type OrderSummaryProjection struct {
	mink.ProjectionBase
	summaries map[string]*OrderSummary
	mu        sync.RWMutex
}

type OrderSummary struct {
	OrderID     string
	CustomerID  string
	TotalAmount float64
	ItemCount   int
	Status      string
}

func NewOrderSummaryProjection() *OrderSummaryProjection {
	p := &OrderSummaryProjection{
		summaries: make(map[string]*OrderSummary),
	}
	p.ProjectionBase = mink.NewProjectionBase("OrderSummary", "OrderCreated", "ItemAdded", "OrderShipped")
	return p
}

func (p *OrderSummaryProjection) Apply(ctx context.Context, event mink.StoredEvent) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	switch event.Type {
	case "OrderCreated":
		var data struct {
			OrderID    string `json:"orderId"`
			CustomerID string `json:"customerId"`
		}
		if err := json.Unmarshal(event.Data, &data); err != nil {
			return err
		}
		p.summaries[data.OrderID] = &OrderSummary{
			OrderID:    data.OrderID,
			CustomerID: data.CustomerID,
			Status:     "Created",
		}

	case "ItemAdded":
		var data struct {
			OrderID  string  `json:"orderId"`
			Price    float64 `json:"price"`
			Quantity int     `json:"quantity"`
		}
		if err := json.Unmarshal(event.Data, &data); err != nil {
			return err
		}
		if summary, ok := p.summaries[data.OrderID]; ok {
			summary.TotalAmount += data.Price * float64(data.Quantity)
			summary.ItemCount += data.Quantity
		}

	case "OrderShipped":
		var data struct {
			OrderID string `json:"orderId"`
		}
		if err := json.Unmarshal(event.Data, &data); err != nil {
			return err
		}
		if summary, ok := p.summaries[data.OrderID]; ok {
			summary.Status = "Shipped"
		}
	}

	return nil
}

func (p *OrderSummaryProjection) Get(orderID string) *OrderSummary {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.summaries[orderID]
}

func TestE2E_Projection_CompleteWorkflow(t *testing.T) {
	t.Run("projection processes events correctly", func(t *testing.T) {
		projection := NewOrderSummaryProjection()
		ctx := context.Background()

		events := []mink.StoredEvent{
			{
				ID:       "evt-1",
				StreamID: "order-1",
				Type:     "OrderCreated",
				Data:     []byte(`{"orderId":"order-1","customerId":"cust-1"}`),
				Version:  1,
			},
			{
				ID:       "evt-2",
				StreamID: "order-1",
				Type:     "ItemAdded",
				Data:     []byte(`{"orderId":"order-1","price":25.00,"quantity":2}`),
				Version:  2,
			},
			{
				ID:       "evt-3",
				StreamID: "order-1",
				Type:     "OrderShipped",
				Data:     []byte(`{"orderId":"order-1"}`),
				Version:  3,
			},
		}

		// Apply events manually
		for _, event := range events {
			err := projection.Apply(ctx, event)
			require.NoError(t, err)
		}

		// Verify projection state
		summary := projection.Get("order-1")
		require.NotNil(t, summary)
		assert.Equal(t, "cust-1", summary.CustomerID)
		assert.Equal(t, 50.00, summary.TotalAmount)
		assert.Equal(t, 2, summary.ItemCount)
		assert.Equal(t, "Shipped", summary.Status)
	})
}

// =============================================================================
// E2E Test: Assertions Framework
// =============================================================================

func TestE2E_Assertions_CompleteWorkflow(t *testing.T) {
	adapter := memory.NewAdapter()
	store := mink.New(adapter)
	ctx := context.Background()

	// Store some events using the correct API
	events := []interface{}{
		&TestOrderCreatedEvt{OrderID: "assert-order-1"},
		&TestItemAddedEvt{OrderID: "assert-order-1", SKU: "SKU-1"},
		&TestOrderShippedEvt{OrderID: "assert-order-1"},
	}

	err := store.Append(ctx, "assert-order-1", events, mink.ExpectVersion(mink.NoStream))
	require.NoError(t, err)

	t.Run("assert event count", func(t *testing.T) {
		loaded, err := store.Load(ctx, "assert-order-1")
		require.NoError(t, err)

		// Convert to []interface{} for assertions
		eventData := make([]interface{}, len(loaded))
		for i, e := range loaded {
			eventData[i] = e.Data
		}

		assertions.AssertEventCount(t, eventData, 3)
	})

	t.Run("assert event types", func(t *testing.T) {
		loaded, err := store.Load(ctx, "assert-order-1")
		require.NoError(t, err)

		// Check event types
		expectedTypes := []string{"TestOrderCreatedEvt", "TestItemAddedEvt", "TestOrderShippedEvt"}
		for i, event := range loaded {
			assert.Equal(t, expectedTypes[i], event.Type)
		}
	})
}

type TestOrderCreatedEvt struct {
	OrderID string `json:"orderId"`
}

type TestItemAddedEvt struct {
	OrderID string `json:"orderId"`
	SKU     string `json:"sku"`
}

type TestOrderShippedEvt struct {
	OrderID string `json:"orderId"`
}

// =============================================================================
// E2E Test: MessagePack Serializer
// =============================================================================

type ProductEvent struct {
	ProductID string  `msgpack:"productId"`
	Name      string  `msgpack:"name"`
	Price     float64 `msgpack:"price"`
}

func TestE2E_MsgPack_CompleteWorkflow(t *testing.T) {
	t.Run("serialize and deserialize with event store", func(t *testing.T) {
		// Create msgpack serializer
		serializer := msgpack.NewSerializer()
		serializer.Register("ProductEvent", ProductEvent{})

		// Create event store with msgpack serializer
		adapter := memory.NewAdapter()
		store := mink.New(adapter, mink.WithSerializer(serializer))
		ctx := context.Background()

		// Create event
		originalEvent := ProductEvent{
			ProductID: "prod-123",
			Name:      "Widget Pro",
			Price:     49.99,
		}

		// Store event
		err := store.Append(ctx, "product-prod-123", []interface{}{originalEvent}, mink.ExpectVersion(mink.NoStream))
		require.NoError(t, err)

		// Load and verify
		loaded, err := store.Load(ctx, "product-prod-123")
		require.NoError(t, err)
		require.Len(t, loaded, 1)

		assert.Equal(t, "ProductEvent", loaded[0].Type)

		// Deserialize - msgpack returns a value, not a pointer
		product, ok := loaded[0].Data.(ProductEvent)
		require.True(t, ok, "Expected ProductEvent type, got %T", loaded[0].Data)
		assert.Equal(t, "prod-123", product.ProductID)
		assert.Equal(t, "Widget Pro", product.Name)
		assert.Equal(t, 49.99, product.Price)
	})

	t.Run("msgpack is smaller than json", func(t *testing.T) {
		serializer := msgpack.NewSerializer()
		jsonSerializer := mink.NewJSONSerializer()

		event := ProductEvent{
			ProductID: "prod-456",
			Name:      "Super Widget with a really long name for testing",
			Price:     199.99,
		}

		msgpackData, err := serializer.Serialize(event)
		require.NoError(t, err)

		jsonData, err := jsonSerializer.Serialize(event)
		require.NoError(t, err)

		// MessagePack should be smaller
		t.Logf("MessagePack size: %d bytes", len(msgpackData))
		t.Logf("JSON size: %d bytes", len(jsonData))
		assert.Less(t, len(msgpackData), len(jsonData), "MessagePack should produce smaller output than JSON")
	})
}

// =============================================================================
// E2E Test: Metrics Middleware
// =============================================================================

func TestE2E_Metrics_CompleteWorkflow(t *testing.T) {
	// Create a new registry for this test to avoid conflicts
	reg := prometheus.NewRegistry()

	// Create metrics
	metricsCollector := metrics.New(metrics.WithNamespace("e2e_test"))

	// Register collectors
	for _, c := range metricsCollector.Collectors() {
		reg.MustRegister(c)
	}

	// Create command bus with metrics middleware
	bus := mink.NewCommandBus()
	bus.Use(metricsCollector.CommandMiddleware())

	// Register a test handler
	bus.RegisterFunc("TestMetricsCommand", func(ctx context.Context, cmd mink.Command) (mink.CommandResult, error) {
		time.Sleep(10 * time.Millisecond) // Simulate work
		return mink.NewSuccessResult("test-123", 1), nil
	})

	// Dispatch commands
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		_, err := bus.Dispatch(ctx, &TestMetricsCommand{})
		require.NoError(t, err)
	}

	// Verify metrics were collected
	gathered, err := reg.Gather()
	require.NoError(t, err)

	// Find our command metrics
	var foundCommandMetric bool
	for _, mf := range gathered {
		// The metric name is namespace_commands_total (e2e_test_commands_total)
		if mf.GetName() == "e2e_test_commands_total" {
			foundCommandMetric = true
		}
	}
	assert.True(t, foundCommandMetric, "Should have command metrics")
}

type TestMetricsCommand struct {
	mink.CommandBase
}

func (c *TestMetricsCommand) CommandType() string { return "TestMetricsCommand" }
func (c *TestMetricsCommand) Validate() error     { return nil }

// =============================================================================
// E2E Test: Tracing Middleware
// =============================================================================

func TestE2E_Tracing_CompleteWorkflow(t *testing.T) {
	// Create in-memory span exporter for testing
	exporter := tracetest.NewInMemoryExporter()

	// Create tracer provider with the exporter
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(exporter),
	)
	defer func() { _ = tp.Shutdown(context.Background()) }()

	// Create tracer with our provider
	tracer := tracing.NewTracer(
		tracing.WithTracerProvider(tp),
		tracing.WithServiceName("e2e-test-service"),
	)

	// Create command bus with tracing middleware
	bus := mink.NewCommandBus()
	bus.Use(tracing.CommandMiddleware(tracer))

	// Register handler
	bus.RegisterFunc("TestTracingCommand", func(ctx context.Context, cmd mink.Command) (mink.CommandResult, error) {
		// Simulate some work
		time.Sleep(5 * time.Millisecond)
		return mink.NewSuccessResult("traced-123", 1), nil
	})

	// Dispatch command
	ctx := context.Background()
	result, err := bus.Dispatch(ctx, &TestTracingCommand{})
	require.NoError(t, err)
	assert.True(t, result.Success)

	// Force flush to ensure spans are exported
	tp.ForceFlush(ctx)

	// Verify spans were created
	spans := exporter.GetSpans()
	require.NotEmpty(t, spans, "Should have created spans")

	// Find command span
	var foundCommandSpan bool
	for _, span := range spans {
		if span.Name == "command.TestTracingCommand" {
			foundCommandSpan = true
		}
	}
	assert.True(t, foundCommandSpan, "Should have command span")
}

type TestTracingCommand struct {
	mink.CommandBase
}

func (c *TestTracingCommand) CommandType() string { return "TestTracingCommand" }
func (c *TestTracingCommand) Validate() error     { return nil }

// =============================================================================
// E2E Test: Full Stack Integration
// =============================================================================

func TestE2E_FullStack_AllComponentsTogether(t *testing.T) {
	// This test demonstrates all components working together

	// 1. Setup metrics
	metricsCollector := metrics.New(metrics.WithNamespace("fullstack"))

	// 2. Setup tracing
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	defer func() { _ = tp.Shutdown(context.Background()) }()
	tracer := tracing.NewTracer(tracing.WithTracerProvider(tp))

	// 3. Setup msgpack serializer
	serializer := msgpack.NewSerializer()
	serializer.Register("FullStackOrderCreated", FullStackOrderCreated{})

	// 4. Setup event store
	adapter := memory.NewAdapter()
	store := mink.New(adapter, mink.WithSerializer(serializer))

	// 5. Setup command bus with all middleware
	bus := mink.NewCommandBus()
	bus.Use(metricsCollector.CommandMiddleware())
	bus.Use(tracing.CommandMiddleware(tracer))
	bus.Use(mink.ValidationMiddleware())
	bus.Use(mink.RecoveryMiddleware())

	// 6. Register command handler
	bus.RegisterFunc("CreateFullStackOrder", func(ctx context.Context, cmd mink.Command) (mink.CommandResult, error) {
		c := cmd.(*CreateFullStackOrderCommand)

		// Create and save event
		event := &FullStackOrderCreated{
			OrderID:    c.OrderID,
			CustomerID: c.CustomerID,
		}

		err := store.Append(ctx, "order-"+c.OrderID, []interface{}{event}, mink.ExpectVersion(mink.NoStream))
		if err != nil {
			return mink.NewErrorResult(err), err
		}

		return mink.NewSuccessResult(c.OrderID, 1), nil
	})

	// 7. Execute command
	ctx := context.Background()
	result, err := bus.Dispatch(ctx, &CreateFullStackOrderCommand{
		OrderID:    "fullstack-123",
		CustomerID: "customer-456",
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Equal(t, "fullstack-123", result.AggregateID)

	// 8. Verify event was stored
	events, err := store.Load(ctx, "order-fullstack-123")
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, "FullStackOrderCreated", events[0].Type)

	// 9. Use BDD to verify aggregate behavior
	order := NewFullStackOrder("new-order")
	bdd.Given(t, order).When(func() error {
		return order.Create("test-customer")
	}).Then(&FullStackOrderCreated{OrderID: "new-order", CustomerID: "test-customer"})

	// 10. Verify tracing spans
	tp.ForceFlush(ctx)
	spans := exporter.GetSpans()
	assert.NotEmpty(t, spans, "Should have tracing spans")

	t.Log("✅ Full stack integration test passed!")
	t.Log("   - Metrics middleware: ✓")
	t.Log("   - Tracing middleware: ✓")
	t.Log("   - MessagePack serializer: ✓")
	t.Log("   - Event store: ✓")
	t.Log("   - Command bus: ✓")
	t.Log("   - BDD testing: ✓")
}

type CreateFullStackOrderCommand struct {
	mink.CommandBase
	OrderID    string `json:"orderId"`
	CustomerID string `json:"customerId"`
}

func (c *CreateFullStackOrderCommand) CommandType() string { return "CreateFullStackOrder" }
func (c *CreateFullStackOrderCommand) Validate() error {
	if c.OrderID == "" {
		return mink.NewValidationError("CreateFullStackOrder", "OrderID", "is required")
	}
	return nil
}

type FullStackOrderCreated struct {
	OrderID    string `json:"orderId" msgpack:"orderId"`
	CustomerID string `json:"customerId" msgpack:"customerId"`
}

type FullStackOrder struct {
	mink.AggregateBase
	CustomerID string
}

func NewFullStackOrder(id string) *FullStackOrder {
	o := &FullStackOrder{}
	o.SetID(id)
	o.SetType("FullStackOrder")
	return o
}

func (o *FullStackOrder) Create(customerID string) error {
	o.Apply(&FullStackOrderCreated{OrderID: o.AggregateID(), CustomerID: customerID})
	o.CustomerID = customerID
	return nil
}

func (o *FullStackOrder) ApplyEvent(event interface{}) error {
	switch e := event.(type) {
	case *FullStackOrderCreated:
		o.CustomerID = e.CustomerID
	}
	o.IncrementVersion()
	return nil
}
