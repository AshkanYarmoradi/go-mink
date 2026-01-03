// Example: Distributed Tracing with OpenTelemetry
//
// This example demonstrates how to instrument your event store
// with OpenTelemetry for distributed tracing.
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
	minktracing "github.com/AshkanYarmoradi/go-mink/middleware/tracing"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.21.0"
	"go.opentelemetry.io/otel/trace"
)

// =============================================================================
// Domain Events
// =============================================================================

type OrderCreated struct {
	OrderID    string    `json:"orderId"`
	CustomerID string    `json:"customerId"`
	Total      float64   `json:"total"`
	CreatedAt  time.Time `json:"createdAt"`
}

type OrderItemAdded struct {
	OrderID  string  `json:"orderId"`
	SKU      string  `json:"sku"`
	Quantity int     `json:"quantity"`
	Price    float64 `json:"price"`
}

type OrderSubmitted struct {
	OrderID     string    `json:"orderId"`
	SubmittedAt time.Time `json:"submittedAt"`
}

// =============================================================================
// Main
// =============================================================================

func main() {
	fmt.Println("=== Distributed Tracing Example ===")
	fmt.Println()

	// Initialize OpenTelemetry with stdout exporter
	tp, err := initTracer()
	if err != nil {
		fmt.Printf("Failed to initialize tracer: %v\n", err)
		return
	}
	defer tp.Shutdown(context.Background())

	// Create memory adapter and event store
	adapter := memory.NewAdapter()
	store := mink.New(adapter)

	// Create tracing middleware tracer
	tracingTracer := minktracing.NewTracer(
		minktracing.WithTracerProvider(tp),
		minktracing.WithServiceName("order-service"),
	)
	_ = tracingTracer // Used for demonstration

	fmt.Println("🔍 OpenTelemetry tracing initialized")
	fmt.Println("   - Traces will be output to stdout")
	fmt.Println()

	// Get a tracer
	tracer := otel.Tracer("order-service")

	// Simulate an order creation flow with tracing
	fmt.Println("📦 Creating order with distributed tracing...")
	fmt.Println()

	// Start the root span
	ctx, rootSpan := tracer.Start(context.Background(), "CreateOrderWorkflow",
		trace.WithAttributes(
			attribute.String("order.id", "ORD-12345"),
			attribute.String("customer.id", "CUST-001"),
		),
	)

	// Step 1: Create order
	err = createOrder(ctx, store, tracer, "ORD-12345", "CUST-001")
	if err != nil {
		fmt.Printf("❌ Failed to create order: %v\n", err)
		rootSpan.End()
		return
	}
	fmt.Println("   ✅ Order created")

	// Step 2: Add items
	items := []struct {
		SKU      string
		Quantity int
		Price    float64
	}{
		{"WIDGET-001", 2, 29.99},
		{"GADGET-002", 1, 149.99},
		{"THING-003", 3, 9.99},
	}

	for _, item := range items {
		err = addOrderItem(ctx, store, tracer, "ORD-12345", item.SKU, item.Quantity, item.Price)
		if err != nil {
			fmt.Printf("❌ Failed to add item: %v\n", err)
			rootSpan.End()
			return
		}
		fmt.Printf("   ✅ Added item %s (qty: %d)\n", item.SKU, item.Quantity)
	}

	// Step 3: Submit order
	err = submitOrder(ctx, store, tracer, "ORD-12345")
	if err != nil {
		fmt.Printf("❌ Failed to submit order: %v\n", err)
		rootSpan.End()
		return
	}
	fmt.Println("   ✅ Order submitted")

	// End the root span
	rootSpan.End()

	fmt.Println()
	fmt.Println("---")
	fmt.Println()

	// Load and display order events
	fmt.Println("📜 Order Event History:")
	events, _ := store.Load(context.Background(), "order-ORD-12345")
	for i, event := range events {
		fmt.Printf("   %d. %s (version %d)\n", i+1, event.Type, event.Version)
	}

	fmt.Println()
	fmt.Println("---")
	fmt.Println()
	fmt.Println("💡 Trace Information:")
	fmt.Println("   - Each operation creates a span with timing data")
	fmt.Println("   - Spans are linked via trace ID for end-to-end visibility")
	fmt.Println("   - Attributes capture context (order ID, customer ID, etc.)")
	fmt.Println("   - Export to Jaeger, Zipkin, or Datadog for visualization")
	fmt.Println()

	fmt.Println("=== Example Complete ===")
}

// initTracer initializes an OpenTelemetry tracer with stdout exporter
func initTracer() (*sdktrace.TracerProvider, error) {
	// Create stdout exporter
	exporter, err := stdouttrace.New(stdouttrace.WithPrettyPrint())
	if err != nil {
		return nil, fmt.Errorf("creating stdout exporter: %w", err)
	}

	// Create resource with service info
	res, err := resource.New(context.Background(),
		resource.WithAttributes(
			semconv.ServiceName("order-service"),
			semconv.ServiceVersion("1.0.0"),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("creating resource: %w", err)
	}

	// Create tracer provider
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)

	// Set as global tracer provider
	otel.SetTracerProvider(tp)

	return tp, nil
}

// createOrder creates a new order with tracing
func createOrder(ctx context.Context, store *mink.EventStore, tracer trace.Tracer, orderID, customerID string) error {
	ctx, span := tracer.Start(ctx, "EventStore.CreateOrder",
		trace.WithAttributes(
			attribute.String("order.id", orderID),
			attribute.String("customer.id", customerID),
		),
	)
	defer span.End()

	event := OrderCreated{
		OrderID:    orderID,
		CustomerID: customerID,
		Total:      0,
		CreatedAt:  time.Now(),
	}

	events := []interface{}{event}
	err := store.Append(ctx, "order-"+orderID, events, mink.ExpectVersion(mink.NoStream))
	if err != nil {
		span.RecordError(err)
		return err
	}

	span.SetAttributes(attribute.Int("events.appended", 1))
	return nil
}

// addOrderItem adds an item to an order with tracing
func addOrderItem(ctx context.Context, store *mink.EventStore, tracer trace.Tracer, orderID, sku string, quantity int, price float64) error {
	ctx, span := tracer.Start(ctx, "EventStore.AddOrderItem",
		trace.WithAttributes(
			attribute.String("order.id", orderID),
			attribute.String("item.sku", sku),
			attribute.Int("item.quantity", quantity),
			attribute.Float64("item.price", price),
		),
	)
	defer span.End()

	event := OrderItemAdded{
		OrderID:  orderID,
		SKU:      sku,
		Quantity: quantity,
		Price:    price,
	}

	events := []interface{}{event}
	err := store.Append(ctx, "order-"+orderID, events)
	if err != nil {
		span.RecordError(err)
		return err
	}

	span.SetAttributes(attribute.Int("events.appended", 1))
	return nil
}

// submitOrder submits an order with tracing
func submitOrder(ctx context.Context, store *mink.EventStore, tracer trace.Tracer, orderID string) error {
	ctx, span := tracer.Start(ctx, "EventStore.SubmitOrder",
		trace.WithAttributes(
			attribute.String("order.id", orderID),
		),
	)
	defer span.End()

	event := OrderSubmitted{
		OrderID:     orderID,
		SubmittedAt: time.Now(),
	}

	events := []interface{}{event}
	err := store.Append(ctx, "order-"+orderID, events)
	if err != nil {
		span.RecordError(err)
		return err
	}

	// Load all events to calculate total for the span
	allEvents, _ := store.Load(ctx, "order-"+orderID)
	span.SetAttributes(
		attribute.Int("events.appended", 1),
		attribute.Int("events.total", len(allEvents)),
	)

	return nil
}

// Helper to marshal events to JSON
func toJSON(v interface{}) []byte {
	data, _ := json.Marshal(v)
	return data
}
