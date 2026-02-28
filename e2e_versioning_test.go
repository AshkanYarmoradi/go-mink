package mink_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/AshkanYarmoradi/go-mink"
	"github.com/AshkanYarmoradi/go-mink/adapters"
	"github.com/AshkanYarmoradi/go-mink/adapters/memory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// E2E Test: Event Versioning & Upcasting
// =============================================================================

// --- Test Domain Events (versioned) ---
// In a real application all schema versions share the same Go struct name,
// since only the latest version exists in the codebase at any time.
// In tests we need both old and new schemas simultaneously, so we use
// separate struct names but a single logical event type name.

// ProductAdded is the latest event schema (v3).
// This is the struct the application code uses.
type ProductAdded struct {
	ProductID string  `json:"product_id"`
	Name      string  `json:"name"`
	Price     float64 `json:"price"`
	Currency  string  `json:"currency"`
	Tax       float64 `json:"tax"`
}

// productAddedEventType is the logical event type name — matches GetEventType(ProductAdded{}).
const productAddedEventType = "ProductAdded"

// --- Test Aggregate ---

type productCatalog struct {
	mink.AggregateBase
	Products []catalogProduct
}

type catalogProduct struct {
	ProductID string
	Name      string
	Price     float64
	Currency  string
	Tax       float64
}

func newProductCatalog(id string) *productCatalog {
	return &productCatalog{
		AggregateBase: mink.NewAggregateBase(id, "ProductCatalog"),
	}
}

func (c *productCatalog) ApplyEvent(event interface{}) error {
	switch e := event.(type) {
	case ProductAdded:
		c.Products = append(c.Products, catalogProduct(e))
	default:
		return fmt.Errorf("unknown event type: %T", event)
	}
	c.IncrementVersion()
	return nil
}

// --- Test Upcasters ---

// paV1ToV2 adds a default Currency field.
type paV1ToV2 struct{}

func (u *paV1ToV2) EventType() string { return productAddedEventType }
func (u *paV1ToV2) FromVersion() int  { return 1 }
func (u *paV1ToV2) ToVersion() int    { return 2 }
func (u *paV1ToV2) Upcast(data []byte, metadata mink.Metadata) ([]byte, error) {
	var obj map[string]interface{}
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil, err
	}
	if metadata.TenantID == "eu-store" {
		obj["currency"] = "EUR"
	} else {
		obj["currency"] = "USD"
	}
	return json.Marshal(obj)
}

// paV2ToV3 adds a default Tax field.
type paV2ToV3 struct{}

func (u *paV2ToV3) EventType() string { return productAddedEventType }
func (u *paV2ToV3) FromVersion() int  { return 2 }
func (u *paV2ToV3) ToVersion() int    { return 3 }
func (u *paV2ToV3) Upcast(data []byte, metadata mink.Metadata) ([]byte, error) {
	var obj map[string]interface{}
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil, err
	}
	obj["tax"] = 0.0
	return json.Marshal(obj)
}

// --- Test Helpers ---

// storeV1Events stores events with the v1 schema (no currency, no tax) directly
// through the adapter, using the consistent event type name "ProductAdded".
// This simulates events stored before schema evolution was introduced.
func storeV1Events(ctx context.Context, t *testing.T, adapter adapters.EventStoreAdapter, streamID string, events []map[string]interface{}, metadata mink.Metadata, expectedVersion int64) {
	t.Helper()
	records := make([]adapters.EventRecord, len(events))
	for i, e := range events {
		data, err := json.Marshal(e)
		require.NoError(t, err)
		records[i] = adapters.EventRecord{
			Type: productAddedEventType,
			Data: data,
			Metadata: adapters.Metadata{
				CorrelationID: metadata.CorrelationID,
				CausationID:   metadata.CausationID,
				UserID:        metadata.UserID,
				TenantID:      metadata.TenantID,
				Custom:        metadata.Custom,
			},
		}
	}
	_, err := adapter.Append(ctx, streamID, records, expectedVersion)
	require.NoError(t, err)
}

// newV3Store creates a store with upcasters that deserializes into the latest ProductAdded struct.
func newV3Store(adapter adapters.EventStoreAdapter) *mink.EventStore {
	chain := mink.NewUpcasterChain()
	_ = chain.Register(&paV1ToV2{})
	_ = chain.Register(&paV2ToV3{})

	store := mink.New(adapter, mink.WithUpcasters(chain))
	store.RegisterEvents(ProductAdded{})
	return store
}

// =============================================================================
// Test: Full round-trip with memory adapter
// =============================================================================

func TestE2E_Versioning_FullRoundTrip(t *testing.T) {
	ctx := context.Background()
	adapter := memory.NewAdapter()

	// Step 1: Store v1 events (no currency, no tax) via adapter.
	// These simulate events stored before schema evolution was introduced.
	storeV1Events(ctx, t, adapter, "ProductCatalog-catalog-1", []map[string]interface{}{
		{"product_id": "prod-1", "name": "Widget", "price": 9.99},
		{"product_id": "prod-2", "name": "Gadget", "price": 19.99},
	}, mink.Metadata{}, mink.NoStream)

	// Step 2: Create a store with upcasters and the latest event type.
	storeV3 := newV3Store(adapter)

	// Step 3: Load events — they should be transparently upcasted v1 → v2 → v3.
	events, err := storeV3.Load(ctx, "ProductCatalog-catalog-1")
	require.NoError(t, err)
	require.Len(t, events, 2)

	// Verify first event was upcasted
	e1, ok := events[0].Data.(ProductAdded)
	require.True(t, ok, "expected ProductAdded, got %T", events[0].Data)
	assert.Equal(t, "prod-1", e1.ProductID)
	assert.Equal(t, "Widget", e1.Name)
	assert.Equal(t, 9.99, e1.Price)
	assert.Equal(t, "USD", e1.Currency) // Added by v1→v2 upcaster
	assert.Equal(t, 0.0, e1.Tax)        // Added by v2→v3 upcaster

	// Verify second event
	e2, ok := events[1].Data.(ProductAdded)
	require.True(t, ok)
	assert.Equal(t, "prod-2", e2.ProductID)
	assert.Equal(t, "USD", e2.Currency)
}

// =============================================================================
// Test: Upcaster uses metadata context (tenant-specific defaults)
// =============================================================================

func TestE2E_Versioning_MetadataContext(t *testing.T) {
	ctx := context.Background()
	adapter := memory.NewAdapter()

	// Store v1 events with different tenant metadata
	storeV1Events(ctx, t, adapter, "ProductCatalog-eu-1",
		[]map[string]interface{}{{"product_id": "eu-prod", "name": "Euro Widget", "price": 8.99}},
		mink.Metadata{TenantID: "eu-store"}, mink.NoStream)

	storeV1Events(ctx, t, adapter, "ProductCatalog-us-1",
		[]map[string]interface{}{{"product_id": "us-prod", "name": "Dollar Widget", "price": 9.99}},
		mink.Metadata{TenantID: "us-store"}, mink.NoStream)

	// Create store with upcasters — the v1→v2 upcaster reads TenantID
	storeV3 := newV3Store(adapter)

	// Load EU stream — should get EUR currency
	euEvents, err := storeV3.Load(ctx, "ProductCatalog-eu-1")
	require.NoError(t, err)
	require.Len(t, euEvents, 1)
	assert.Equal(t, "EUR", euEvents[0].Data.(ProductAdded).Currency, "EU tenant should get EUR")

	// Load US stream — should get USD currency
	usEvents, err := storeV3.Load(ctx, "ProductCatalog-us-1")
	require.NoError(t, err)
	require.Len(t, usEvents, 1)
	assert.Equal(t, "USD", usEvents[0].Data.(ProductAdded).Currency, "US tenant should get USD")
}

// =============================================================================
// Test: Schema version stamped on new events
// =============================================================================

func TestE2E_Versioning_SchemaVersionStamped(t *testing.T) {
	ctx := context.Background()
	adapter := memory.NewAdapter()

	storeV3 := newV3Store(adapter)

	// Append v3 event — should stamp schema version 3 in metadata
	err := storeV3.Append(ctx, "ProductCatalog-stamped-1",
		[]interface{}{ProductAdded{ProductID: "prod-new", Name: "New", Price: 5.0, Currency: "USD", Tax: 1.0}},
	)
	require.NoError(t, err)

	// Load raw to inspect metadata
	raw, err := storeV3.LoadRaw(ctx, "ProductCatalog-stamped-1", 0)
	require.NoError(t, err)
	require.Len(t, raw, 1)

	schemaVersion := mink.GetSchemaVersion(raw[0].Metadata)
	assert.Equal(t, 3, schemaVersion, "new events should be stamped with latest schema version")
}

// =============================================================================
// Test: No upcasters — zero overhead, identical to current behavior
// =============================================================================

func TestE2E_Versioning_NoUpcasters_ZeroOverhead(t *testing.T) {
	ctx := context.Background()
	adapter := memory.NewAdapter()

	// Store without upcasters — standard behavior
	store := mink.New(adapter)
	store.RegisterEvents(ProductAdded{})

	err := store.Append(ctx, "ProductCatalog-noop-1",
		[]interface{}{ProductAdded{ProductID: "prod-1", Name: "Widget", Price: 9.99, Currency: "USD", Tax: 0}},
	)
	require.NoError(t, err)

	events, err := store.Load(ctx, "ProductCatalog-noop-1")
	require.NoError(t, err)
	require.Len(t, events, 1)

	e, ok := events[0].Data.(ProductAdded)
	require.True(t, ok, "expected ProductAdded, got %T", events[0].Data)
	assert.Equal(t, "prod-1", e.ProductID)
	assert.Equal(t, 9.99, e.Price)

	// No schema version stamped (no upcasters configured)
	raw, err := store.LoadRaw(ctx, "ProductCatalog-noop-1", 0)
	require.NoError(t, err)
	assert.Equal(t, mink.DefaultSchemaVersion, mink.GetSchemaVersion(raw[0].Metadata),
		"events without upcasters should default to version 1")
}

// =============================================================================
// Test: Mixed-version events in same stream
// =============================================================================

func TestE2E_Versioning_MixedVersions(t *testing.T) {
	ctx := context.Background()
	adapter := memory.NewAdapter()

	// Step 1: Store a v1 event (before upcasters were added)
	storeV1Events(ctx, t, adapter, "ProductCatalog-mixed-1",
		[]map[string]interface{}{{"product_id": "old-prod", "name": "Legacy", "price": 5.0}},
		mink.Metadata{}, mink.NoStream)

	// Step 2: Create store with upcasters and append a v3 event to same stream
	storeV3 := newV3Store(adapter)
	err := storeV3.Append(ctx, "ProductCatalog-mixed-1",
		[]interface{}{ProductAdded{ProductID: "new-prod", Name: "Modern", Price: 15.0, Currency: "GBP", Tax: 3.0}},
		mink.ExpectVersion(1),
	)
	require.NoError(t, err)

	// Step 3: Load — stream has mixed versions:
	//   event 1: stored as v1 (no $schema_version) → upcasted to v3
	//   event 2: stored as v3 ($schema_version=3) → no upcasting needed
	events, err := storeV3.Load(ctx, "ProductCatalog-mixed-1")
	require.NoError(t, err)
	require.Len(t, events, 2)

	// Event 1: old v1, upcasted to v3
	e1 := events[0].Data.(ProductAdded)
	assert.Equal(t, "old-prod", e1.ProductID)
	assert.Equal(t, "USD", e1.Currency, "v1 event should be upcasted with default currency")
	assert.Equal(t, 0.0, e1.Tax, "v1 event should be upcasted with zero tax")

	// Event 2: native v3, unchanged
	e2 := events[1].Data.(ProductAdded)
	assert.Equal(t, "new-prod", e2.ProductID)
	assert.Equal(t, "GBP", e2.Currency, "v3 event currency should be preserved")
	assert.Equal(t, 3.0, e2.Tax, "v3 event tax should be preserved")
}

// =============================================================================
// Test: LoadAggregate with upcasting rebuilds correct state
// =============================================================================

func TestE2E_Versioning_LoadAggregate_Rebuild(t *testing.T) {
	ctx := context.Background()
	adapter := memory.NewAdapter()

	// Store v1 events without upcasters
	storeV1Events(ctx, t, adapter, "ProductCatalog-rebuild-1", []map[string]interface{}{
		{"product_id": "prod-a", "name": "Alpha", "price": 10.0},
		{"product_id": "prod-b", "name": "Beta", "price": 20.0},
	}, mink.Metadata{}, mink.NoStream)

	// Create store with upcasters
	storeV3 := newV3Store(adapter)

	// Load aggregate — events should be upcasted before ApplyEvent
	catalog := newProductCatalog("rebuild-1")
	err := storeV3.LoadAggregate(ctx, catalog)
	require.NoError(t, err)

	// Verify aggregate state was rebuilt from upcasted events
	require.Len(t, catalog.Products, 2)
	assert.Equal(t, "prod-a", catalog.Products[0].ProductID)
	assert.Equal(t, "USD", catalog.Products[0].Currency, "upcasted currency")
	assert.Equal(t, 0.0, catalog.Products[0].Tax, "upcasted tax")
	assert.Equal(t, "prod-b", catalog.Products[1].ProductID)
	assert.Equal(t, "USD", catalog.Products[1].Currency)
}

// =============================================================================
// Test: RegisterUpcasters convenience method
// =============================================================================

func TestE2E_Versioning_RegisterUpcasters(t *testing.T) {
	ctx := context.Background()
	adapter := memory.NewAdapter()

	// Store v1 events
	storeV1Events(ctx, t, adapter, "ProductCatalog-convenience-1",
		[]map[string]interface{}{{"product_id": "prod-c", "name": "Charlie", "price": 30.0}},
		mink.Metadata{}, mink.AnyVersion)

	// Use RegisterUpcasters convenience method (auto-creates chain)
	store := mink.New(adapter)
	store.RegisterEvents(ProductAdded{})
	err := store.RegisterUpcasters(&paV1ToV2{}, &paV2ToV3{})
	require.NoError(t, err)

	events, err := store.Load(ctx, "ProductCatalog-convenience-1")
	require.NoError(t, err)
	require.Len(t, events, 1)

	e := events[0].Data.(ProductAdded)
	assert.Equal(t, "prod-c", e.ProductID)
	assert.Equal(t, "USD", e.Currency)
	assert.Equal(t, 0.0, e.Tax)
}

// =============================================================================
// Test: UpcastingSerializer standalone (without EventStore)
// =============================================================================

func TestE2E_Versioning_UpcastingSerializer_Standalone(t *testing.T) {
	inner := mink.NewJSONSerializer()
	inner.Register(productAddedEventType, ProductAdded{})

	chain := mink.NewUpcasterChain()
	require.NoError(t, chain.Register(&paV1ToV2{}))
	require.NoError(t, chain.Register(&paV2ToV3{}))

	s := mink.NewUpcastingSerializer(inner, chain)

	// Deserialize v1 data through the upcasting serializer (version 1 → 3)
	v1Data, _ := json.Marshal(map[string]interface{}{"product_id": "p2", "name": "Old", "price": 3.0})
	result, err := s.DeserializeWithVersion(v1Data, productAddedEventType, 1, mink.Metadata{})
	require.NoError(t, err)

	event := result.(ProductAdded)
	assert.Equal(t, "p2", event.ProductID)
	assert.Equal(t, "USD", event.Currency)
	assert.Equal(t, 0.0, event.Tax)

	// Deserialize v3 data at version 3 — no upcasting should occur
	v3Data, _ := json.Marshal(ProductAdded{ProductID: "p1", Name: "Test", Price: 5.0, Currency: "GBP", Tax: 1.0})
	result2, err := s.DeserializeWithVersion(v3Data, productAddedEventType, 3, mink.Metadata{})
	require.NoError(t, err)

	event2 := result2.(ProductAdded)
	assert.Equal(t, "p1", event2.ProductID)
	assert.Equal(t, "GBP", event2.Currency, "v3 data at version 3 should not be upcasted")
	assert.Equal(t, 1.0, event2.Tax)
}

// =============================================================================
// Test: SchemaRegistry compatibility checks
// =============================================================================

func TestE2E_Versioning_SchemaRegistry(t *testing.T) {
	registry := mink.NewSchemaRegistry()

	require.NoError(t, registry.Register(productAddedEventType, mink.SchemaDefinition{
		Version: 1,
		Fields: []mink.FieldDefinition{
			{Name: "product_id", Type: "string", Required: true},
			{Name: "name", Type: "string", Required: true},
			{Name: "price", Type: "float64", Required: true},
		},
	}))

	require.NoError(t, registry.Register(productAddedEventType, mink.SchemaDefinition{
		Version: 2,
		Fields: []mink.FieldDefinition{
			{Name: "product_id", Type: "string", Required: true},
			{Name: "name", Type: "string", Required: true},
			{Name: "price", Type: "float64", Required: true},
			{Name: "currency", Type: "string", Required: false},
		},
	}))

	require.NoError(t, registry.Register(productAddedEventType, mink.SchemaDefinition{
		Version: 3,
		Fields: []mink.FieldDefinition{
			{Name: "product_id", Type: "string", Required: true},
			{Name: "name", Type: "string", Required: true},
			{Name: "price", Type: "float64", Required: true},
			{Name: "currency", Type: "string", Required: false},
			{Name: "tax", Type: "float64", Required: false},
		},
	}))

	compat12, err := registry.CheckCompatibility(productAddedEventType, 1, 2)
	require.NoError(t, err)
	assert.Equal(t, mink.SchemaBackwardCompatible, compat12, "v1→v2 should be backward compatible")

	compat23, err := registry.CheckCompatibility(productAddedEventType, 2, 3)
	require.NoError(t, err)
	assert.Equal(t, mink.SchemaBackwardCompatible, compat23, "v2→v3 should be backward compatible")

	latest, err := registry.GetLatestVersion(productAddedEventType)
	require.NoError(t, err)
	assert.Equal(t, 3, latest)

	types := registry.RegisteredEventTypes()
	assert.Equal(t, []string{productAddedEventType}, types)
}

// =============================================================================
// Test: Upcast error during Load propagates correctly
// =============================================================================

// failingUpcaster always returns an error.
type failingUpcaster struct{}

func (u *failingUpcaster) EventType() string { return productAddedEventType }
func (u *failingUpcaster) FromVersion() int  { return 1 }
func (u *failingUpcaster) ToVersion() int    { return 2 }
func (u *failingUpcaster) Upcast(data []byte, metadata mink.Metadata) ([]byte, error) {
	return nil, fmt.Errorf("upcast deliberately failed")
}

func TestE2E_Versioning_UpcastError_Load(t *testing.T) {
	ctx := context.Background()
	adapter := memory.NewAdapter()

	// Store a v1 event
	storeV1Events(ctx, t, adapter, "ProductCatalog-err-1",
		[]map[string]interface{}{{"product_id": "p1", "name": "Widget", "price": 9.99}},
		mink.Metadata{}, mink.NoStream)

	// Create store with a failing upcaster
	chain := mink.NewUpcasterChain()
	require.NoError(t, chain.Register(&failingUpcaster{}))
	store := mink.New(adapter, mink.WithUpcasters(chain))
	store.RegisterEvents(ProductAdded{})

	// Load should propagate the upcast error
	_, err := store.Load(ctx, "ProductCatalog-err-1")
	require.Error(t, err)
	assert.ErrorContains(t, err, "upcast deliberately failed")
}

func TestE2E_Versioning_UpcastError_LoadAggregate(t *testing.T) {
	ctx := context.Background()
	adapter := memory.NewAdapter()

	// Store a v1 event
	storeV1Events(ctx, t, adapter, "ProductCatalog-err-agg-1",
		[]map[string]interface{}{{"product_id": "p1", "name": "Widget", "price": 9.99}},
		mink.Metadata{}, mink.NoStream)

	// Create store with a failing upcaster
	chain := mink.NewUpcasterChain()
	require.NoError(t, chain.Register(&failingUpcaster{}))
	store := mink.New(adapter, mink.WithUpcasters(chain))
	store.RegisterEvents(ProductAdded{})

	// LoadAggregate should propagate the upcast error
	catalog := newProductCatalog("err-agg-1")
	err := store.LoadAggregate(ctx, catalog)
	require.Error(t, err)
	assert.ErrorContains(t, err, "upcast deliberately failed")
}

// =============================================================================
// Test: RegisterUpcasters error path
// =============================================================================

func TestE2E_Versioning_RegisterUpcasters_Error(t *testing.T) {
	store := mink.New(memory.NewAdapter())

	// Register valid upcaster, then attempt a duplicate
	err := store.RegisterUpcasters(&paV1ToV2{})
	require.NoError(t, err)

	// Second registration with same version range should fail
	err = store.RegisterUpcasters(&paV1ToV2{})
	require.Error(t, err)
	assert.ErrorContains(t, err, "duplicate")
}

// =============================================================================
// Test: SaveAggregate stamps schema version
// =============================================================================

func TestE2E_Versioning_SaveAggregate_StampsVersion(t *testing.T) {
	ctx := context.Background()
	adapter := memory.NewAdapter()

	chain := mink.NewUpcasterChain()
	require.NoError(t, chain.Register(&paV1ToV2{}))
	require.NoError(t, chain.Register(&paV2ToV3{}))

	store := mink.New(adapter, mink.WithUpcasters(chain))
	store.RegisterEvents(ProductAdded{})

	// Create and save aggregate
	catalog := newProductCatalog("save-stamp-1")
	catalog.Apply(ProductAdded{ProductID: "p1", Name: "Test", Price: 5.0, Currency: "USD", Tax: 0})
	catalog.Products = append(catalog.Products, catalogProduct{ProductID: "p1", Name: "Test", Price: 5.0, Currency: "USD"})

	err := store.SaveAggregate(ctx, catalog)
	require.NoError(t, err)

	// Verify schema version was stamped
	raw, err := store.LoadRaw(ctx, "ProductCatalog-save-stamp-1", 0)
	require.NoError(t, err)
	require.Len(t, raw, 1)
	assert.Equal(t, 3, mink.GetSchemaVersion(raw[0].Metadata))
}

// =============================================================================
// Test: CheckCompatibility error for unknown event type
// =============================================================================

func TestE2E_Versioning_SchemaRegistry_CheckCompatibility_Error(t *testing.T) {
	registry := mink.NewSchemaRegistry()

	// Unknown event type
	_, err := registry.CheckCompatibility("Unknown", 1, 2)
	require.Error(t, err)
}

// =============================================================================
// Test: Deserialization error after successful upcast
// =============================================================================

// corruptingUpcaster produces invalid JSON so deserialization fails.
type corruptingUpcaster struct{}

func (u *corruptingUpcaster) EventType() string { return productAddedEventType }
func (u *corruptingUpcaster) FromVersion() int  { return 1 }
func (u *corruptingUpcaster) ToVersion() int    { return 2 }
func (u *corruptingUpcaster) Upcast(_ []byte, _ mink.Metadata) ([]byte, error) {
	return []byte(`{invalid json`), nil // Upcast succeeds but produces bad data
}

func TestE2E_Versioning_DeserializeError_AfterUpcast(t *testing.T) {
	ctx := context.Background()
	adapter := memory.NewAdapter()

	storeV1Events(ctx, t, adapter, "ProductCatalog-deserr-1",
		[]map[string]interface{}{{"product_id": "p1", "name": "Widget", "price": 9.99}},
		mink.Metadata{}, mink.NoStream)

	chain := mink.NewUpcasterChain()
	require.NoError(t, chain.Register(&corruptingUpcaster{}))
	store := mink.New(adapter, mink.WithUpcasters(chain))
	store.RegisterEvents(ProductAdded{})

	_, err := store.Load(ctx, "ProductCatalog-deserr-1")
	require.Error(t, err)
	assert.ErrorContains(t, err, "deserialize")
}
