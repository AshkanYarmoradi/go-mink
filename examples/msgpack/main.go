// Example: MessagePack Serializer
//
// This example demonstrates how to use MessagePack serialization
// for more efficient event storage compared to JSON.
//
// Run with: go run .
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/AshkanYarmoradi/go-mink"
	"github.com/AshkanYarmoradi/go-mink/adapters/memory"
	"github.com/AshkanYarmoradi/go-mink/serializer/msgpack"
)

// =============================================================================
// Domain Events
// =============================================================================

type ProductCreated struct {
	ProductID   string    `json:"productId" msgpack:"productId"`
	Name        string    `json:"name" msgpack:"name"`
	Description string    `json:"description" msgpack:"description"`
	Price       float64   `json:"price" msgpack:"price"`
	Category    string    `json:"category" msgpack:"category"`
	Tags        []string  `json:"tags" msgpack:"tags"`
	CreatedAt   time.Time `json:"createdAt" msgpack:"createdAt"`
}

type ProductPriceChanged struct {
	ProductID string    `json:"productId" msgpack:"productId"`
	OldPrice  float64   `json:"oldPrice" msgpack:"oldPrice"`
	NewPrice  float64   `json:"newPrice" msgpack:"newPrice"`
	ChangedAt time.Time `json:"changedAt" msgpack:"changedAt"`
}

type ProductInventoryUpdated struct {
	ProductID   string    `json:"productId" msgpack:"productId"`
	WarehouseID string    `json:"warehouseId" msgpack:"warehouseId"`
	OldQuantity int       `json:"oldQuantity" msgpack:"oldQuantity"`
	NewQuantity int       `json:"newQuantity" msgpack:"newQuantity"`
	Reason      string    `json:"reason" msgpack:"reason"`
	UpdatedAt   time.Time `json:"updatedAt" msgpack:"updatedAt"`
}

type LargeEvent struct {
	ID         string            `json:"id" msgpack:"id"`
	Data       []byte            `json:"data" msgpack:"data"`
	Attributes map[string]string `json:"attributes" msgpack:"attributes"`
	Numbers    []int64           `json:"numbers" msgpack:"numbers"`
	Nested     NestedData        `json:"nested" msgpack:"nested"`
}

type NestedData struct {
	Field1 string   `json:"field1" msgpack:"field1"`
	Field2 int      `json:"field2" msgpack:"field2"`
	Field3 []string `json:"field3" msgpack:"field3"`
}

// =============================================================================
// Main
// =============================================================================

func main() {
	fmt.Println("=== MessagePack Serializer Example ===")
	fmt.Println()

	// Create serializers
	jsonSerializer := mink.NewJSONSerializer()
	msgpackSerializer := msgpack.NewSerializer()

	// Register event types with msgpack serializer for deserialization
	msgpackSerializer.Register("ProductCreated", ProductCreated{})
	msgpackSerializer.Register("ProductPriceChanged", ProductPriceChanged{})
	msgpackSerializer.Register("ProductInventoryUpdated", ProductInventoryUpdated{})
	msgpackSerializer.Register("LargeEvent", LargeEvent{})

	fmt.Println("📦 Comparing serialization formats...")
	fmt.Println()

	// Test with a typical event
	productEvent := ProductCreated{
		ProductID:   "PROD-12345",
		Name:        "Premium Widget Pro Max",
		Description: "A high-quality widget with advanced features for professional use",
		Price:       299.99,
		Category:    "Electronics",
		Tags:        []string{"premium", "professional", "advanced", "widget"},
		CreatedAt:   time.Now(),
	}

	// Serialize with both
	jsonBytes, _ := jsonSerializer.Serialize(productEvent)
	msgpackBytes, _ := msgpackSerializer.Serialize(productEvent)

	fmt.Println("1️⃣ ProductCreated Event:")
	fmt.Printf("   JSON size:     %d bytes\n", len(jsonBytes))
	fmt.Printf("   MsgPack size:  %d bytes\n", len(msgpackBytes))
	fmt.Printf("   Savings:       %.1f%%\n", (1-float64(len(msgpackBytes))/float64(len(jsonBytes)))*100)
	fmt.Println()

	// Test with a larger event
	largeEvent := LargeEvent{
		ID:   "LARGE-001",
		Data: make([]byte, 1000),
		Attributes: map[string]string{
			"key1": "value1",
			"key2": "value2",
			"key3": "value3",
			"key4": "value4",
			"key5": "value5",
		},
		Numbers: make([]int64, 100),
		Nested: NestedData{
			Field1: "Nested string value",
			Field2: 42,
			Field3: []string{"a", "b", "c", "d", "e"},
		},
	}
	for i := range largeEvent.Numbers {
		largeEvent.Numbers[i] = int64(i * 1000)
	}

	jsonLarge, _ := jsonSerializer.Serialize(largeEvent)
	msgpackLarge, _ := msgpackSerializer.Serialize(largeEvent)

	fmt.Println("2️⃣ Large Event with Arrays/Maps:")
	fmt.Printf("   JSON size:     %d bytes\n", len(jsonLarge))
	fmt.Printf("   MsgPack size:  %d bytes\n", len(msgpackLarge))
	fmt.Printf("   Savings:       %.1f%%\n", (1-float64(len(msgpackLarge))/float64(len(jsonLarge)))*100)
	fmt.Println()

	// Test deserialization
	fmt.Println("3️⃣ Deserialization Test:")

	deserializedJSON, _ := jsonSerializer.Deserialize(jsonBytes, "ProductCreated")
	deserializedMsgPack, _ := msgpackSerializer.Deserialize(msgpackBytes, "ProductCreated")

	// JSON returns map[string]interface{} since type is not registered
	if jsonMap, ok := deserializedJSON.(map[string]interface{}); ok {
		fmt.Printf("   JSON -> ProductID: %s\n", jsonMap["productId"])
	}

	// MsgPack returns ProductCreated since type is registered
	if msgpackProduct, ok := deserializedMsgPack.(ProductCreated); ok {
		fmt.Printf("   MsgPack -> ProductID: %s\n", msgpackProduct.ProductID)
	}
	fmt.Println()

	// Benchmark serialization speed
	fmt.Println("4️⃣ Serialization Speed Benchmark (10,000 iterations):")

	iterations := 10000

	// JSON benchmark
	start := time.Now()
	for i := 0; i < iterations; i++ {
		jsonSerializer.Serialize(productEvent)
	}
	jsonDuration := time.Since(start)

	// MsgPack benchmark
	start = time.Now()
	for i := 0; i < iterations; i++ {
		msgpackSerializer.Serialize(productEvent)
	}
	msgpackDuration := time.Since(start)

	fmt.Printf("   JSON:     %v (%.2f ops/sec)\n", jsonDuration, float64(iterations)/jsonDuration.Seconds())
	fmt.Printf("   MsgPack:  %v (%.2f ops/sec)\n", msgpackDuration, float64(iterations)/msgpackDuration.Seconds())
	if msgpackDuration < jsonDuration {
		fmt.Printf("   MsgPack is %.1fx faster\n", float64(jsonDuration)/float64(msgpackDuration))
	}
	fmt.Println()

	// Use with EventStore
	fmt.Println("---")
	fmt.Println()
	fmt.Println("💾 Using MessagePack with EventStore:")
	fmt.Println()

	adapter := memory.NewAdapter()
	store := mink.New(adapter, mink.WithSerializer(msgpackSerializer))
	ctx := context.Background()

	// Store some events
	events := []interface{}{
		ProductCreated{
			ProductID:   "PROD-001",
			Name:        "Widget A",
			Description: "A basic widget",
			Price:       49.99,
			Category:    "Widgets",
			Tags:        []string{"basic", "widget"},
			CreatedAt:   time.Now(),
		},
		ProductPriceChanged{
			ProductID: "PROD-001",
			OldPrice:  49.99,
			NewPrice:  39.99,
			ChangedAt: time.Now(),
		},
		ProductInventoryUpdated{
			ProductID:   "PROD-001",
			WarehouseID: "WH-01",
			OldQuantity: 100,
			NewQuantity: 85,
			Reason:      "Sale",
			UpdatedAt:   time.Now(),
		},
	}

	err := store.Append(ctx, "product-PROD-001", events, mink.ExpectVersion(mink.NoStream))
	if err != nil {
		fmt.Printf("   ❌ Error: %v\n", err)
		return
	}

	fmt.Printf("   ✅ Stored %d events with MessagePack serialization\n", len(events))

	// Load and verify
	loadedEvents, _ := store.Load(ctx, "product-PROD-001")
	fmt.Printf("   ✅ Loaded %d events\n", len(loadedEvents))

	for i, event := range loadedEvents {
		fmt.Printf("      %d. %s (version %d)\n", i+1, event.Type, event.Version)
	}

	fmt.Println()
	fmt.Println("💡 When to use MessagePack:")
	fmt.Println("   - High-throughput systems with many events")
	fmt.Println("   - When storage costs are a concern")
	fmt.Println("   - Internal services where human readability isn't needed")
	fmt.Println("   - Real-time event streaming scenarios")
	fmt.Println()
	fmt.Println("💡 When to use JSON:")
	fmt.Println("   - When debugging/inspecting events in the database")
	fmt.Println("   - API responses where interoperability matters")
	fmt.Println("   - Systems where event data needs to be human-readable")
	fmt.Println()

	fmt.Println("=== Example Complete ===")
}
