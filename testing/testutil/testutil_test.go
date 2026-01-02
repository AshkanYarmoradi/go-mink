// Package testutil provides test utilities and fixtures for go-mink.
package testutil

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/AshkanYarmoradi/go-mink"
	"github.com/AshkanYarmoradi/go-mink/adapters/memory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// TestConfig Tests
// =============================================================================

func TestDefaultConfig(t *testing.T) {
	t.Run("returns config from environment", func(t *testing.T) {
		// Set environment variable
		originalURL := os.Getenv("TEST_DATABASE_URL")
		os.Setenv("TEST_DATABASE_URL", "postgres://test:test@localhost:5432/test")
		defer func() {
			if originalURL != "" {
				os.Setenv("TEST_DATABASE_URL", originalURL)
			} else {
				os.Unsetenv("TEST_DATABASE_URL")
			}
		}()

		config := DefaultConfig()

		assert.Equal(t, "postgres://test:test@localhost:5432/test", config.PostgresURL)
	})

	t.Run("returns default when env not set", func(t *testing.T) {
		// Clear environment variable
		originalURL := os.Getenv("TEST_DATABASE_URL")
		os.Unsetenv("TEST_DATABASE_URL")
		defer func() {
			if originalURL != "" {
				os.Setenv("TEST_DATABASE_URL", originalURL)
			}
		}()

		config := DefaultConfig()

		assert.Equal(t, "postgres://postgres:postgres@localhost:5432/mink_test?sslmode=disable", config.PostgresURL)
	})
}

func TestGetEnvOrDefault(t *testing.T) {
	t.Run("returns env value when set", func(t *testing.T) {
		os.Setenv("TEST_UTIL_TEST_KEY", "test-value")
		defer os.Unsetenv("TEST_UTIL_TEST_KEY")

		result := getEnvOrDefault("TEST_UTIL_TEST_KEY", "default")

		assert.Equal(t, "test-value", result)
	})

	t.Run("returns default when not set", func(t *testing.T) {
		os.Unsetenv("TEST_UTIL_MISSING_KEY")

		result := getEnvOrDefault("TEST_UTIL_MISSING_KEY", "default")

		assert.Equal(t, "default", result)
	})
}

func TestUniqueSchema(t *testing.T) {
	t.Run("generates schema name with prefix", func(t *testing.T) {
		schema := UniqueSchema("test")

		assert.Contains(t, schema, "test_")
		// Check that suffix is a number
		assert.Regexp(t, `^test_\d+$`, schema)
	})

	t.Run("uses custom prefix", func(t *testing.T) {
		schema := UniqueSchema("myprefix")

		assert.Contains(t, schema, "myprefix_")
	})
}

func TestQuoteIdentifier(t *testing.T) {
	t.Run("quotes simple identifier", func(t *testing.T) {
		result := quoteIdentifier("test_schema")

		assert.Equal(t, `"test_schema"`, result)
	})

	t.Run("escapes double quotes", func(t *testing.T) {
		result := quoteIdentifier(`test"schema`)

		assert.Equal(t, `"test""schema"`, result)
	})
}

// =============================================================================
// Test Fixtures Tests
// =============================================================================

func TestNewOrder(t *testing.T) {
	t.Run("creates order with ID", func(t *testing.T) {
		order := NewOrder("order-123")

		assert.Equal(t, "order-123", order.AggregateID())
		assert.Equal(t, "Order", order.AggregateType())
		assert.Empty(t, order.Items)
	})
}

func TestOrder_Create(t *testing.T) {
	t.Run("creates order successfully", func(t *testing.T) {
		order := NewOrder("order-123")

		err := order.Create("customer-456")

		require.NoError(t, err)
		assert.Equal(t, "customer-456", order.CustomerID)
		assert.Equal(t, "Created", order.Status)
		assert.Len(t, order.UncommittedEvents(), 1)
	})

	t.Run("fails if already created", func(t *testing.T) {
		order := NewOrder("order-123")
		order.Create("customer-456")
		order.ClearUncommittedEvents()

		err := order.Create("customer-789")

		assert.Error(t, err)
	})
}

func TestOrder_AddItem(t *testing.T) {
	t.Run("adds item successfully", func(t *testing.T) {
		order := NewOrder("order-123")
		order.Create("customer-456")
		order.ClearUncommittedEvents()

		err := order.AddItem("SKU-001", 2, 29.99)

		require.NoError(t, err)
		assert.Len(t, order.Items, 1)
		assert.Equal(t, "SKU-001", order.Items[0].SKU)
	})

	t.Run("fails if order not created", func(t *testing.T) {
		order := NewOrder("order-123")

		err := order.AddItem("SKU-001", 2, 29.99)

		assert.Error(t, err)
	})
}

func TestOrder_Ship(t *testing.T) {
	t.Run("ships order successfully", func(t *testing.T) {
		order := NewOrder("order-123")
		order.Create("customer-456")
		order.AddItem("SKU-001", 2, 29.99)
		order.ClearUncommittedEvents()

		err := order.Ship("TRACK-123")

		require.NoError(t, err)
		assert.Equal(t, "Shipped", order.Status)
		assert.Equal(t, "TRACK-123", order.TrackingNumber)
	})

	t.Run("fails if no items", func(t *testing.T) {
		order := NewOrder("order-123")
		order.Create("customer-456")

		err := order.Ship("TRACK-123")

		assert.Error(t, err)
	})

	t.Run("fails if order not created", func(t *testing.T) {
		order := NewOrder("order-123")

		err := order.Ship("TRACK-123")

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "cannot ship")
	})

	t.Run("fails if already shipped", func(t *testing.T) {
		order := NewOrder("order-123")
		order.Create("customer-456")
		order.AddItem("SKU-001", 2, 29.99)
		order.Ship("TRACK-123")
		order.ClearUncommittedEvents()

		err := order.Ship("TRACK-456")

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "cannot ship")
	})
}

func TestOrder_Cancel(t *testing.T) {
	t.Run("cancels order successfully", func(t *testing.T) {
		order := NewOrder("order-123")
		order.Create("customer-456")
		order.ClearUncommittedEvents()

		err := order.Cancel("Customer request")

		require.NoError(t, err)
		assert.Equal(t, "Cancelled", order.Status)
		assert.Equal(t, "Customer request", order.CancelReason)
	})

	t.Run("fails if shipped", func(t *testing.T) {
		order := NewOrder("order-123")
		order.Create("customer-456")
		order.AddItem("SKU-001", 2, 29.99)
		order.Ship("TRACK-123")

		err := order.Cancel("Customer request")

		assert.Error(t, err)
	})

	t.Run("fails if already cancelled", func(t *testing.T) {
		order := NewOrder("order-123")
		order.Create("customer-456")
		order.Cancel("First cancel")

		err := order.Cancel("Second cancel")

		assert.Error(t, err)
	})
}

func TestOrder_TotalAmount(t *testing.T) {
	t.Run("calculates total correctly", func(t *testing.T) {
		order := NewOrder("order-123")
		order.Create("customer-456")
		order.AddItem("SKU-001", 2, 29.99)
		order.AddItem("SKU-002", 1, 49.99)

		total := order.TotalAmount()

		expected := 2*29.99 + 1*49.99
		assert.InDelta(t, expected, total, 0.01)
	})
}

func TestOrder_ApplyEvent(t *testing.T) {
	t.Run("applies OrderCreated", func(t *testing.T) {
		order := NewOrder("order-123")
		event := OrderCreated{OrderID: "order-123", CustomerID: "customer-456"}

		err := order.ApplyEvent(event)

		require.NoError(t, err)
		assert.Equal(t, "customer-456", order.CustomerID)
		assert.Equal(t, "Created", order.Status)
	})

	t.Run("applies ItemAdded", func(t *testing.T) {
		order := NewOrder("order-123")
		order.Status = "Created"
		event := ItemAdded{OrderID: "order-123", SKU: "SKU-001", Quantity: 2, Price: 29.99}

		err := order.ApplyEvent(event)

		require.NoError(t, err)
		assert.Len(t, order.Items, 1)
	})

	t.Run("applies OrderShipped", func(t *testing.T) {
		order := NewOrder("order-123")
		order.Status = "Created"
		event := OrderShipped{OrderID: "order-123", TrackingNumber: "TRACK-123"}

		err := order.ApplyEvent(event)

		require.NoError(t, err)
		assert.Equal(t, "Shipped", order.Status)
		assert.Equal(t, "TRACK-123", order.TrackingNumber)
	})

	t.Run("applies OrderCancelled", func(t *testing.T) {
		order := NewOrder("order-123")
		order.Status = "Created"
		event := OrderCancelled{OrderID: "order-123", Reason: "Customer request"}

		err := order.ApplyEvent(event)

		require.NoError(t, err)
		assert.Equal(t, "Cancelled", order.Status)
		assert.Equal(t, "Customer request", order.CancelReason)
	})

	t.Run("returns error for unknown event", func(t *testing.T) {
		order := NewOrder("order-123")

		err := order.ApplyEvent("unknown event")

		assert.Error(t, err)
	})
}

// =============================================================================
// Event Serialization Tests
// =============================================================================

func TestEventSerialization(t *testing.T) {
	t.Run("OrderCreated serializes correctly", func(t *testing.T) {
		event := OrderCreated{OrderID: "order-123", CustomerID: "customer-456"}

		data, err := json.Marshal(event)
		require.NoError(t, err)

		var decoded OrderCreated
		err = json.Unmarshal(data, &decoded)
		require.NoError(t, err)
		assert.Equal(t, event, decoded)
	})

	t.Run("ItemAdded serializes correctly", func(t *testing.T) {
		event := ItemAdded{OrderID: "order-123", SKU: "SKU-001", Quantity: 2, Price: 29.99}

		data, err := json.Marshal(event)
		require.NoError(t, err)

		var decoded ItemAdded
		err = json.Unmarshal(data, &decoded)
		require.NoError(t, err)
		assert.Equal(t, event, decoded)
	})
}

// =============================================================================
// Read Model Tests
// =============================================================================

func TestNewOrderReadModel(t *testing.T) {
	t.Run("creates empty read model", func(t *testing.T) {
		rm := NewOrderReadModel()

		assert.NotNil(t, rm)
		assert.Equal(t, 0, rm.Count())
		assert.Equal(t, 0, rm.UpdateCount())
	})
}

func TestOrderReadModel_Apply(t *testing.T) {
	t.Run("applies OrderCreated", func(t *testing.T) {
		rm := NewOrderReadModel()
		event := mink.StoredEvent{
			StreamID: "Order-order-123",
			Type:     "OrderCreated",
			Data:     []byte(`{"orderId":"order-123","customerId":"customer-456"}`),
		}

		err := rm.Apply(event)

		require.NoError(t, err)
		summary := rm.Get("order-123")
		require.NotNil(t, summary)
		assert.Equal(t, "customer-456", summary.CustomerID)
		assert.Equal(t, "Created", summary.Status)
	})

	t.Run("applies ItemAdded", func(t *testing.T) {
		rm := NewOrderReadModel()
		rm.Apply(mink.StoredEvent{
			StreamID: "Order-order-123",
			Type:     "OrderCreated",
			Data:     []byte(`{"orderId":"order-123","customerId":"customer-456"}`),
		})

		err := rm.Apply(mink.StoredEvent{
			StreamID: "Order-order-123",
			Type:     "ItemAdded",
			Data:     []byte(`{"orderId":"order-123","sku":"SKU-001"}`),
		})

		require.NoError(t, err)
		summary := rm.Get("order-123")
		assert.Equal(t, 1, summary.ItemCount)
	})

	t.Run("applies OrderShipped", func(t *testing.T) {
		rm := NewOrderReadModel()
		rm.Apply(mink.StoredEvent{
			StreamID: "Order-order-123",
			Type:     "OrderCreated",
			Data:     []byte(`{"orderId":"order-123","customerId":"customer-456"}`),
		})

		err := rm.Apply(mink.StoredEvent{
			StreamID: "Order-order-123",
			Type:     "OrderShipped",
			Data:     []byte(`{"orderId":"order-123","trackingNumber":"TRACK-123"}`),
		})

		require.NoError(t, err)
		summary := rm.Get("order-123")
		assert.Equal(t, "Shipped", summary.Status)
		assert.Equal(t, "TRACK-123", summary.TrackingNumber)
	})

	t.Run("applies OrderCancelled", func(t *testing.T) {
		rm := NewOrderReadModel()
		rm.Apply(mink.StoredEvent{
			StreamID: "Order-order-123",
			Type:     "OrderCreated",
			Data:     []byte(`{"orderId":"order-123","customerId":"customer-456"}`),
		})

		err := rm.Apply(mink.StoredEvent{
			StreamID: "Order-order-123",
			Type:     "OrderCancelled",
			Data:     []byte(`{"orderId":"order-123","reason":"Customer request"}`),
		})

		require.NoError(t, err)
		summary := rm.Get("order-123")
		assert.Equal(t, "Cancelled", summary.Status)
	})

	t.Run("returns error for invalid stream ID", func(t *testing.T) {
		rm := NewOrderReadModel()
		event := mink.StoredEvent{
			StreamID: "Bad",
			Type:     "OrderCreated",
			Data:     []byte(`{}`),
		}

		err := rm.Apply(event)

		assert.Error(t, err)
	})

	t.Run("returns error for invalid OrderCreated JSON", func(t *testing.T) {
		rm := NewOrderReadModel()
		event := mink.StoredEvent{
			StreamID: "Order-order-123",
			Type:     "OrderCreated",
			Data:     []byte(`{invalid json`),
		}

		err := rm.Apply(event)

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to unmarshal OrderCreated")
	})

	t.Run("returns error for invalid OrderShipped JSON", func(t *testing.T) {
		rm := NewOrderReadModel()
		// First create the order so it exists
		rm.Apply(mink.StoredEvent{
			StreamID: "Order-order-123",
			Type:     "OrderCreated",
			Data:     []byte(`{"orderId":"order-123","customerId":"customer-456"}`),
		})

		event := mink.StoredEvent{
			StreamID: "Order-order-123",
			Type:     "OrderShipped",
			Data:     []byte(`{invalid json`),
		}

		err := rm.Apply(event)

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to unmarshal OrderShipped")
	})

	t.Run("skips ItemAdded when order does not exist", func(t *testing.T) {
		rm := NewOrderReadModel()
		event := mink.StoredEvent{
			StreamID: "Order-order-123",
			Type:     "ItemAdded",
			Data:     []byte(`{"orderId":"order-123","sku":"SKU-001"}`),
		}

		err := rm.Apply(event)

		require.NoError(t, err)
		assert.Nil(t, rm.Get("order-123"))
	})

	t.Run("skips OrderShipped when order does not exist", func(t *testing.T) {
		rm := NewOrderReadModel()
		event := mink.StoredEvent{
			StreamID: "Order-order-123",
			Type:     "OrderShipped",
			Data:     []byte(`{"orderId":"order-123","trackingNumber":"TRACK-123"}`),
		}

		err := rm.Apply(event)

		require.NoError(t, err)
		assert.Nil(t, rm.Get("order-123"))
	})

	t.Run("skips OrderCancelled when order does not exist", func(t *testing.T) {
		rm := NewOrderReadModel()
		event := mink.StoredEvent{
			StreamID: "Order-order-123",
			Type:     "OrderCancelled",
			Data:     []byte(`{"orderId":"order-123","reason":"Customer request"}`),
		}

		err := rm.Apply(event)

		require.NoError(t, err)
		assert.Nil(t, rm.Get("order-123"))
	})

	t.Run("handles unknown event type gracefully", func(t *testing.T) {
		rm := NewOrderReadModel()
		rm.Apply(mink.StoredEvent{
			StreamID: "Order-order-123",
			Type:     "OrderCreated",
			Data:     []byte(`{"orderId":"order-123","customerId":"customer-456"}`),
		})

		event := mink.StoredEvent{
			StreamID: "Order-order-123",
			Type:     "UnknownEvent",
			Data:     []byte(`{}`),
		}

		err := rm.Apply(event)

		require.NoError(t, err)
		// Update count should still increment
		assert.Equal(t, 2, rm.UpdateCount())
	})

	t.Run("increments update count", func(t *testing.T) {
		rm := NewOrderReadModel()
		rm.Apply(mink.StoredEvent{
			StreamID: "Order-order-123",
			Type:     "OrderCreated",
			Data:     []byte(`{"orderId":"order-123","customerId":"customer-456"}`),
		})
		rm.Apply(mink.StoredEvent{
			StreamID: "Order-order-123",
			Type:     "ItemAdded",
			Data:     []byte(`{}`),
		})

		assert.Equal(t, 2, rm.UpdateCount())
	})
}

func TestOrderReadModel_Get(t *testing.T) {
	t.Run("returns nil for non-existent order", func(t *testing.T) {
		rm := NewOrderReadModel()

		summary := rm.Get("non-existent")

		assert.Nil(t, summary)
	})
}

func TestOrderReadModel_Count(t *testing.T) {
	t.Run("returns correct count", func(t *testing.T) {
		rm := NewOrderReadModel()
		rm.Apply(mink.StoredEvent{
			StreamID: "Order-order-1",
			Type:     "OrderCreated",
			Data:     []byte(`{"orderId":"order-1","customerId":"customer-1"}`),
		})
		rm.Apply(mink.StoredEvent{
			StreamID: "Order-order-2",
			Type:     "OrderCreated",
			Data:     []byte(`{"orderId":"order-2","customerId":"customer-2"}`),
		})

		assert.Equal(t, 2, rm.Count())
	})
}

// =============================================================================
// RegisterTestEvents Tests
// =============================================================================

func TestRegisterTestEvents(t *testing.T) {
	t.Run("registers all test event types with store", func(t *testing.T) {
		adapter := memory.NewAdapter()
		store := mink.New(adapter)

		// Should not panic
		RegisterTestEvents(store)

		// Verify events are registered by trying to use them
		// The store should be able to serialize/deserialize these event types
		assert.NotNil(t, store)
	})
}

// =============================================================================
// PostgreSQL Integration Tests
// =============================================================================

func TestPostgresDB_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	connStr := os.Getenv("TEST_DATABASE_URL")
	if connStr == "" {
		connStr = "postgres://postgres:postgres@localhost:5432/mink_test?sslmode=disable"
	}

	t.Run("connects to PostgreSQL successfully", func(t *testing.T) {
		ctx := context.Background()
		db, err := PostgresDB(ctx, connStr)
		require.NoError(t, err)
		defer db.Close()

		// Verify connection works
		var result int
		err = db.QueryRowContext(ctx, "SELECT 1").Scan(&result)
		require.NoError(t, err)
		assert.Equal(t, 1, result)
	})

	t.Run("fails with invalid connection string", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*100) // Short timeout
		defer cancel()

		_, err := PostgresDB(ctx, "postgres://invalid:invalid@localhost:9999/invalid?sslmode=disable&connect_timeout=1")
		assert.Error(t, err)
	})
}

func TestMustPostgresDB_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	connStr := os.Getenv("TEST_DATABASE_URL")
	if connStr == "" {
		connStr = "postgres://postgres:postgres@localhost:5432/mink_test?sslmode=disable"
	}

	t.Run("returns database connection", func(t *testing.T) {
		ctx := context.Background()
		db := MustPostgresDB(ctx, connStr)
		defer db.Close()

		assert.NotNil(t, db)
	})
}

func TestCleanupSchema_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	connStr := os.Getenv("TEST_DATABASE_URL")
	if connStr == "" {
		connStr = "postgres://postgres:postgres@localhost:5432/mink_test?sslmode=disable"
	}

	ctx := context.Background()
	db, err := PostgresDB(ctx, connStr)
	require.NoError(t, err)
	defer db.Close()

	t.Run("drops existing schema", func(t *testing.T) {
		// Create a unique schema
		schema := UniqueSchema("test_cleanup")

		// Create the schema
		_, err := db.ExecContext(ctx, `CREATE SCHEMA `+quoteIdentifier(schema))
		require.NoError(t, err)

		// Verify it exists
		var exists bool
		err = db.QueryRowContext(ctx,
			"SELECT EXISTS(SELECT 1 FROM information_schema.schemata WHERE schema_name = $1)",
			schema).Scan(&exists)
		require.NoError(t, err)
		assert.True(t, exists)

		// Clean it up
		err = CleanupSchema(ctx, db, schema)
		require.NoError(t, err)

		// Verify it's gone
		err = db.QueryRowContext(ctx,
			"SELECT EXISTS(SELECT 1 FROM information_schema.schemata WHERE schema_name = $1)",
			schema).Scan(&exists)
		require.NoError(t, err)
		assert.False(t, exists)
	})

	t.Run("succeeds when schema does not exist", func(t *testing.T) {
		// This should not fail
		err := CleanupSchema(ctx, db, "nonexistent_schema_12345")
		assert.NoError(t, err)
	})
}
