// Package main demonstrates event versioning and upcasting in go-mink.
//
// This example shows:
// - Storing events with an initial schema (v1)
// - Defining upcasters for schema evolution (v1 → v2 → v3)
// - Transparently loading old events as the latest schema version
// - Schema version stamping on newly appended events
// - Using the SchemaRegistry for compatibility checking
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"go-mink.dev"
	"go-mink.dev/adapters/memory"
)

// ---------------------------------------------------------------------------
// Event definitions — all schema versions share the same Go struct name so
// the event type stored in the database ("ProductAdded") stays consistent.
// Older events are transformed to the latest shape by upcasters at read time.
// ---------------------------------------------------------------------------

// ProductAdded is the latest (v3) schema for the "product added" event.
// v1 had only Name and Price.
// v2 added Currency (default "USD").
// v3 added TaxRate  (default 0.0).
type ProductAdded struct {
	ProductID string  `json:"product_id"`
	Name      string  `json:"name"`
	Price     float64 `json:"price"`
	Currency  string  `json:"currency"` // added in v2
	TaxRate   float64 `json:"tax_rate"` // added in v3
}

// ---------------------------------------------------------------------------
// Aggregate
// ---------------------------------------------------------------------------

// Product is an aggregate that tracks catalog products.
type Product struct {
	mink.AggregateBase

	Name     string
	Price    float64
	Currency string
	TaxRate  float64
}

// NewProduct creates a new Product aggregate.
func NewProduct(id string) *Product {
	return &Product{
		AggregateBase: mink.NewAggregateBase(id, "Product"),
	}
}

// Add records a new product in the catalog.
func (p *Product) Add(name string, price float64, currency string, taxRate float64) {
	p.Apply(ProductAdded{
		ProductID: p.AggregateID(),
		Name:      name,
		Price:     price,
		Currency:  currency,
		TaxRate:   taxRate,
	})
	p.Name = name
	p.Price = price
	p.Currency = currency
	p.TaxRate = taxRate
}

// ApplyEvent replays a historical event to rebuild state.
func (p *Product) ApplyEvent(event interface{}) error {
	switch e := event.(type) {
	case ProductAdded:
		p.Name = e.Name
		p.Price = e.Price
		p.Currency = e.Currency
		p.TaxRate = e.TaxRate
	default:
		return fmt.Errorf("unknown event type: %T", event)
	}
	p.IncrementVersion()
	return nil
}

// ---------------------------------------------------------------------------
// Upcasters — pure data transformations on raw JSON bytes
// ---------------------------------------------------------------------------

// productAddedV1ToV2 adds the Currency field (default "USD").
type productAddedV1ToV2 struct{}

func (u productAddedV1ToV2) EventType() string { return "ProductAdded" }
func (u productAddedV1ToV2) FromVersion() int  { return 1 }
func (u productAddedV1ToV2) ToVersion() int    { return 2 }

func (u productAddedV1ToV2) Upcast(data []byte, metadata mink.Metadata) ([]byte, error) {
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	m["currency"] = "USD" // default for all pre-v2 events
	return json.Marshal(m)
}

// productAddedV2ToV3 adds the TaxRate field (default 0.0).
type productAddedV2ToV3 struct{}

func (u productAddedV2ToV3) EventType() string { return "ProductAdded" }
func (u productAddedV2ToV3) FromVersion() int  { return 2 }
func (u productAddedV2ToV3) ToVersion() int    { return 3 }

func (u productAddedV2ToV3) Upcast(data []byte, metadata mink.Metadata) ([]byte, error) {
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	m["tax_rate"] = 0.0
	return json.Marshal(m)
}

func main() {
	ctx := context.Background()

	fmt.Println("=== go-mink Event Versioning Example ===")
	fmt.Println()

	// ------------------------------------------------------------------
	// Step 1: Store a v1 event (no upcasters, no schema version stamp)
	// ------------------------------------------------------------------
	fmt.Println("1. Storing a v1 ProductAdded event (old schema)...")

	adapter := memory.NewAdapter()
	storeV1 := mink.New(adapter)
	storeV1.RegisterEvents(ProductAdded{})

	// Simulate storing a v1 event — only Name and Price, no Currency or TaxRate.
	v1Event := ProductAdded{
		ProductID: "prod-001",
		Name:      "Wireless Mouse",
		Price:     29.99,
		// Currency and TaxRate are zero-valued, representing the old schema.
	}
	err := storeV1.Append(ctx, "Product-prod-001", []interface{}{v1Event})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("   Stored: {Name: Wireless Mouse, Price: 29.99}")
	fmt.Println("   (no Currency or TaxRate — this is a v1 event)")
	fmt.Println()

	// ------------------------------------------------------------------
	// Step 2: Set up upcasters and create a new store
	// ------------------------------------------------------------------
	fmt.Println("2. Registering upcasters (v1 → v2 → v3)...")

	chain := mink.NewUpcasterChain()
	if err := chain.Register(productAddedV1ToV2{}); err != nil {
		log.Fatal(err)
	}
	if err := chain.Register(productAddedV2ToV3{}); err != nil {
		log.Fatal(err)
	}
	if err := chain.Validate(); err != nil {
		log.Fatal(err)
	}

	fmt.Printf("   Registered upcasters for: %v\n", chain.RegisteredEventTypes())
	fmt.Printf("   Latest version for ProductAdded: v%d\n", chain.LatestVersion("ProductAdded"))
	fmt.Println()

	// Create a new store pointing at the same adapter, now with upcasters
	storeV3 := mink.New(adapter, mink.WithUpcasters(chain))
	storeV3.RegisterEvents(ProductAdded{})

	// ------------------------------------------------------------------
	// Step 3: Load the old event — it gets upcasted transparently
	// ------------------------------------------------------------------
	fmt.Println("3. Loading the old event through the new store...")

	events, err := storeV3.Load(ctx, "Product-prod-001")
	if err != nil {
		log.Fatal(err)
	}

	loaded := events[0].Data.(ProductAdded)
	fmt.Printf("   Loaded event (after upcasting):\n")
	fmt.Printf("      Name:     %s\n", loaded.Name)
	fmt.Printf("      Price:    $%.2f\n", loaded.Price)
	fmt.Printf("      Currency: %s  (added by v1→v2 upcaster)\n", loaded.Currency)
	fmt.Printf("      TaxRate:  %.1f%% (added by v2→v3 upcaster)\n", loaded.TaxRate*100)
	fmt.Println()

	// ------------------------------------------------------------------
	// Step 4: Rebuild aggregate from old events
	// ------------------------------------------------------------------
	fmt.Println("4. Loading aggregate (rebuilds state from upcasted events)...")

	product := NewProduct("prod-001")
	if err := storeV3.LoadAggregate(ctx, product); err != nil {
		log.Fatal(err)
	}

	fmt.Printf("   Product state:\n")
	fmt.Printf("      Name:     %s\n", product.Name)
	fmt.Printf("      Price:    $%.2f\n", product.Price)
	fmt.Printf("      Currency: %s\n", product.Currency)
	fmt.Printf("      TaxRate:  %.1f%%\n", product.TaxRate*100)
	fmt.Printf("      Version:  %d\n", product.Version())
	fmt.Println()

	// ------------------------------------------------------------------
	// Step 5: Save a new v3 event — schema version is stamped automatically
	// ------------------------------------------------------------------
	fmt.Println("5. Saving a new product (v3 schema, version stamped)...")

	product2 := NewProduct("prod-002")
	product2.Add("Mechanical Keyboard", 149.99, "EUR", 0.19)

	if err := storeV3.SaveAggregate(ctx, product2); err != nil {
		log.Fatal(err)
	}

	// Verify the schema version was stamped in metadata
	rawEvents, err := storeV3.LoadRaw(ctx, "Product-prod-002", 0)
	if err != nil {
		log.Fatal(err)
	}
	schemaVersion := mink.GetSchemaVersion(rawEvents[0].Metadata)
	fmt.Printf("   Stored event type: %s\n", rawEvents[0].Type)
	fmt.Printf("   Schema version in metadata: v%d\n", schemaVersion)
	fmt.Println()

	// ------------------------------------------------------------------
	// Step 6: SchemaRegistry — track and compare schemas
	// ------------------------------------------------------------------
	fmt.Println("6. Using SchemaRegistry for compatibility checking...")

	registry := mink.NewSchemaRegistry()

	_ = registry.Register("ProductAdded", mink.SchemaDefinition{
		Version: 1,
		Fields: []mink.FieldDefinition{
			{Name: "product_id", Type: "string", Required: true},
			{Name: "name", Type: "string", Required: true},
			{Name: "price", Type: "float64", Required: true},
		},
	})
	_ = registry.Register("ProductAdded", mink.SchemaDefinition{
		Version: 2,
		Fields: []mink.FieldDefinition{
			{Name: "product_id", Type: "string", Required: true},
			{Name: "name", Type: "string", Required: true},
			{Name: "price", Type: "float64", Required: true},
			{Name: "currency", Type: "string", Required: false},
		},
	})
	_ = registry.Register("ProductAdded", mink.SchemaDefinition{
		Version: 3,
		Fields: []mink.FieldDefinition{
			{Name: "product_id", Type: "string", Required: true},
			{Name: "name", Type: "string", Required: true},
			{Name: "price", Type: "float64", Required: true},
			{Name: "currency", Type: "string", Required: false},
			{Name: "tax_rate", Type: "float64", Required: false},
		},
	})

	latestVer, _ := registry.GetLatestVersion("ProductAdded")
	fmt.Printf("   Latest registered schema version: v%d\n", latestVer)

	compat12, _ := registry.CheckCompatibility("ProductAdded", 1, 2)
	compat23, _ := registry.CheckCompatibility("ProductAdded", 2, 3)
	fmt.Printf("   v1 → v2: %s (Currency field added)\n", compat12)
	fmt.Printf("   v2 → v3: %s (TaxRate field added)\n", compat23)
	fmt.Println()

	fmt.Println("=== Example Complete ===")
}
