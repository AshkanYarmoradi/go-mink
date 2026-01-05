// Example: Protocol Buffers Serializer
//
// This example demonstrates how to use Protocol Buffers serialization
// for highly efficient, strongly-typed event storage.
//
// Protocol Buffers provides:
// - Smaller binary size than JSON and MessagePack
// - Strong typing with schema enforcement
// - Forward/backward compatibility with field numbering
// - Fast serialization/deserialization
//
// Run with: go run .
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/AshkanYarmoradi/go-mink"
	"github.com/AshkanYarmoradi/go-mink/adapters"
	"github.com/AshkanYarmoradi/go-mink/adapters/memory"
	"github.com/AshkanYarmoradi/go-mink/serializer/protobuf"
	"google.golang.org/protobuf/types/known/timestamppb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// =============================================================================
// Main
// =============================================================================

func main() {
	fmt.Println("=== Protocol Buffers Serializer Example ===")
	fmt.Println()

	// Demonstrate basic serialization
	demonstrateBasicSerialization()

	// Demonstrate with event store
	demonstrateEventStore()

	// Demonstrate size comparison
	demonstrateSizeComparison()

	// Demonstrate projection building
	demonstrateProjection()

	fmt.Println("✅ All examples completed successfully!")
}

// =============================================================================
// Basic Serialization
// =============================================================================

func demonstrateBasicSerialization() {
	fmt.Println("1️⃣ Basic Serialization")
	fmt.Println("─────────────────────────────────────────")

	// Create a protobuf serializer
	s := protobuf.NewSerializer()

	// Register event types using well-known protobuf types
	// In production, you'd use your own .proto defined types
	s.MustRegister("OrderID", &wrapperspb.StringValue{})
	s.MustRegister("ItemCount", &wrapperspb.Int32Value{})
	s.MustRegister("TotalAmount", &wrapperspb.DoubleValue{})
	s.MustRegister("IsShipped", &wrapperspb.BoolValue{})
	s.MustRegister("Timestamp", &timestamppb.Timestamp{})

	// Serialize different event types
	events := []struct {
		name  string
		typ   string
		event interface{}
	}{
		{"Order ID", "OrderID", wrapperspb.String("ORD-2024-001")},
		{"Item Count", "ItemCount", wrapperspb.Int32(5)},
		{"Total Amount", "TotalAmount", wrapperspb.Double(149.99)},
		{"Is Shipped", "IsShipped", wrapperspb.Bool(true)},
		{"Timestamp", "Timestamp", timestamppb.Now()},
	}

	for _, e := range events {
		data, err := s.Serialize(e.event)
		if err != nil {
			fmt.Printf("   ❌ Failed to serialize %s: %v\n", e.name, err)
			continue
		}
		fmt.Printf("   ✓ %s: %d bytes\n", e.name, len(data))

		// Deserialize back
		result, err := s.Deserialize(data, e.typ)
		if err != nil {
			fmt.Printf("     ❌ Failed to deserialize: %v\n", err)
			continue
		}
		fmt.Printf("     → Deserialized: %v\n", result)
	}

	fmt.Println()
}

// =============================================================================
// Event Store Integration
// =============================================================================

func demonstrateEventStore() {
	fmt.Println("2️⃣ Event Store Integration (Direct Adapter Usage)")
	fmt.Println("─────────────────────────────────────────")

	ctx := context.Background()

	// Create protobuf serializer
	pbSerializer := protobuf.NewSerializer()
	pbSerializer.MustRegister("CustomerID", &wrapperspb.StringValue{})
	pbSerializer.MustRegister("ItemAdded", &wrapperspb.Int32Value{})
	pbSerializer.MustRegister("OrderTotal", &wrapperspb.DoubleValue{})
	pbSerializer.MustRegister("Shipped", &wrapperspb.BoolValue{})

	// Create memory adapter directly for low-level control
	adapter := memory.NewAdapter()

	streamID := "order-12345"

	// Append events to the stream using EventRecord (pre-serialized)
	fmt.Printf("   📝 Appending events to stream '%s'...\n", streamID)

	events := []adapters.EventRecord{
		{Type: "CustomerID", Data: mustSerialize(pbSerializer, wrapperspb.String("CUST-001"))},
		{Type: "ItemAdded", Data: mustSerialize(pbSerializer, wrapperspb.Int32(3))},
		{Type: "ItemAdded", Data: mustSerialize(pbSerializer, wrapperspb.Int32(2))},
		{Type: "OrderTotal", Data: mustSerialize(pbSerializer, wrapperspb.Double(259.97))},
		{Type: "Shipped", Data: mustSerialize(pbSerializer, wrapperspb.Bool(true))},
	}

	_, err := adapter.Append(ctx, streamID, events, mink.NoStream)
	if err != nil {
		fmt.Printf("   ❌ Failed to append events: %v\n", err)
		return
	}
	fmt.Printf("   ✓ Appended %d events\n", len(events))

	// Load events back
	fmt.Println("   📖 Loading events from stream...")
	storedEvents, err := adapter.Load(ctx, streamID, 0)
	if err != nil {
		fmt.Printf("   ❌ Failed to load events: %v\n", err)
		return
	}

	for _, e := range storedEvents {
		result, err := pbSerializer.Deserialize(e.Data, e.Type)
		if err != nil {
			fmt.Printf("   ❌ Event %s: failed to deserialize: %v\n", e.Type, err)
			continue
		}
		fmt.Printf("   ✓ Event %d: %s = %v\n", e.Version, e.Type, formatValue(result))
	}

	fmt.Println()
}

// =============================================================================
// Size Comparison
// =============================================================================

func demonstrateSizeComparison() {
	fmt.Println("3️⃣ Size Comparison: Protobuf vs JSON")
	fmt.Println("─────────────────────────────────────────")

	jsonSerializer := mink.NewJSONSerializer()
	pbSerializer := protobuf.NewSerializer()

	// Compare sizes for various data types
	comparisons := []struct {
		name      string
		jsonEvent interface{}
		pbEvent   interface{}
	}{
		{
			name:      "Small string",
			jsonEvent: map[string]string{"value": "hello"},
			pbEvent:   wrapperspb.String("hello"),
		},
		{
			name:      "Large string",
			jsonEvent: map[string]string{"value": string(make([]byte, 1000))},
			pbEvent:   wrapperspb.String(string(make([]byte, 1000))),
		},
		{
			name:      "Integer",
			jsonEvent: map[string]int{"value": 42},
			pbEvent:   wrapperspb.Int32(42),
		},
		{
			name:      "Large integer",
			jsonEvent: map[string]int64{"value": 9223372036854775807},
			pbEvent:   wrapperspb.Int64(9223372036854775807),
		},
		{
			name:      "Double",
			jsonEvent: map[string]float64{"value": 3.14159265358979},
			pbEvent:   wrapperspb.Double(3.14159265358979),
		},
		{
			name:      "Boolean",
			jsonEvent: map[string]bool{"value": true},
			pbEvent:   wrapperspb.Bool(true),
		},
		{
			name:      "Binary (100 bytes)",
			jsonEvent: map[string][]byte{"value": make([]byte, 100)},
			pbEvent:   wrapperspb.Bytes(make([]byte, 100)),
		},
	}

	fmt.Printf("   %-20s %10s %10s %10s\n", "Data Type", "JSON", "Protobuf", "Savings")
	fmt.Printf("   %-20s %10s %10s %10s\n", "─────────", "────", "────────", "───────")

	var totalJSON, totalPB int
	for _, c := range comparisons {
		jsonBytes, _ := jsonSerializer.Serialize(c.jsonEvent)
		pbBytes, _ := pbSerializer.Serialize(c.pbEvent)

		totalJSON += len(jsonBytes)
		totalPB += len(pbBytes)

		savings := float64(len(jsonBytes)-len(pbBytes)) / float64(len(jsonBytes)) * 100
		fmt.Printf("   %-20s %10d %10d %9.1f%%\n", c.name, len(jsonBytes), len(pbBytes), savings)
	}

	totalSavings := float64(totalJSON-totalPB) / float64(totalJSON) * 100
	fmt.Printf("   %-20s %10s %10s %10s\n", "─────────", "────", "────────", "───────")
	fmt.Printf("   %-20s %10d %10d %9.1f%%\n", "TOTAL", totalJSON, totalPB, totalSavings)

	fmt.Println()
}

// =============================================================================
// Projection Building
// =============================================================================

func demonstrateProjection() {
	fmt.Println("4️⃣ Building Projections from Protobuf Events")
	fmt.Println("─────────────────────────────────────────")

	// Simulate an order summary projection
	type OrderSummary struct {
		OrderID    string
		CustomerID string
		ItemCount  int32
		TotalItems int32
		IsComplete bool
	}

	pbSerializer := protobuf.NewSerializer()
	pbSerializer.MustRegister("CustomerID", &wrapperspb.StringValue{})
	pbSerializer.MustRegister("ItemAdded", &wrapperspb.Int32Value{})
	pbSerializer.MustRegister("OrderComplete", &wrapperspb.BoolValue{})

	// Simulated events from the store
	events := []struct {
		Type string
		Data []byte
	}{
		{"CustomerID", mustSerialize(pbSerializer, wrapperspb.String("CUST-ABC"))},
		{"ItemAdded", mustSerialize(pbSerializer, wrapperspb.Int32(3))},
		{"ItemAdded", mustSerialize(pbSerializer, wrapperspb.Int32(2))},
		{"ItemAdded", mustSerialize(pbSerializer, wrapperspb.Int32(5))},
		{"OrderComplete", mustSerialize(pbSerializer, wrapperspb.Bool(true))},
	}

	// Build projection
	summary := OrderSummary{OrderID: "ORD-001"}

	fmt.Println("   📊 Processing events to build projection...")
	for i, e := range events {
		result, err := pbSerializer.Deserialize(e.Data, e.Type)
		if err != nil {
			fmt.Printf("   ❌ Event %d: failed to deserialize: %v\n", i+1, err)
			continue
		}

		switch e.Type {
		case "CustomerID":
			sv := result.(wrapperspb.StringValue)
			summary.CustomerID = sv.Value
			fmt.Printf("   → Event %d: Set CustomerID = %s\n", i+1, sv.Value)
		case "ItemAdded":
			iv := result.(wrapperspb.Int32Value)
			summary.ItemCount++
			summary.TotalItems += iv.Value
			fmt.Printf("   → Event %d: Added %d items (total batches: %d, total items: %d)\n",
				i+1, iv.Value, summary.ItemCount, summary.TotalItems)
		case "OrderComplete":
			bv := result.(wrapperspb.BoolValue)
			summary.IsComplete = bv.Value
			fmt.Printf("   → Event %d: Order complete = %v\n", i+1, bv.Value)
		}
	}

	fmt.Println()
	fmt.Println("   📋 Final Projection State:")
	fmt.Printf("      Order ID:    %s\n", summary.OrderID)
	fmt.Printf("      Customer ID: %s\n", summary.CustomerID)
	fmt.Printf("      Item Batches: %d\n", summary.ItemCount)
	fmt.Printf("      Total Items: %d\n", summary.TotalItems)
	fmt.Printf("      Complete:    %v\n", summary.IsComplete)

	fmt.Println()
}

// =============================================================================
// Helper Functions
// =============================================================================

func mustSerialize(s *protobuf.Serializer, event interface{}) []byte {
	data, err := s.Serialize(event)
	if err != nil {
		panic(err)
	}
	return data
}

func formatValue(v interface{}) string {
	switch val := v.(type) {
	case wrapperspb.StringValue:
		return fmt.Sprintf("%q", val.Value)
	case wrapperspb.Int32Value:
		return fmt.Sprintf("%d", val.Value)
	case wrapperspb.Int64Value:
		return fmt.Sprintf("%d", val.Value)
	case wrapperspb.DoubleValue:
		return fmt.Sprintf("%.2f", val.Value)
	case wrapperspb.BoolValue:
		return fmt.Sprintf("%v", val.Value)
	case wrapperspb.BytesValue:
		return fmt.Sprintf("[%d bytes]", len(val.Value))
	case timestamppb.Timestamp:
		return val.AsTime().Format(time.RFC3339)
	default:
		return fmt.Sprintf("%v", v)
	}
}
