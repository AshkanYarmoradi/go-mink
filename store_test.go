package mink

import (
	"context"
	"errors"
	"testing"

	"github.com/AshkanYarmoradi/go-mink/adapters/memory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test event types for EventStore tests
type StoreOrderCreated struct {
	OrderID    string `json:"orderId"`
	CustomerID string `json:"customerId"`
}

type StoreItemAdded struct {
	OrderID  string  `json:"orderId"`
	SKU      string  `json:"sku"`
	Quantity int     `json:"quantity"`
	Price    float64 `json:"price"`
}

type StoreOrderShipped struct {
	OrderID string `json:"orderId"`
}

// Test aggregate for EventStore tests
type StoreTestOrder struct {
	AggregateBase
	CustomerID string
	Items      []struct {
		SKU      string
		Quantity int
		Price    float64
	}
	Status string
}

func NewStoreTestOrder(id string) *StoreTestOrder {
	return &StoreTestOrder{
		AggregateBase: NewAggregateBase(id, "Order"),
	}
}

func (o *StoreTestOrder) Create(customerID string) {
	o.Apply(StoreOrderCreated{OrderID: o.AggregateID(), CustomerID: customerID})
	o.CustomerID = customerID
	o.Status = "Created"
}

func (o *StoreTestOrder) AddItem(sku string, qty int, price float64) {
	o.Apply(StoreItemAdded{OrderID: o.AggregateID(), SKU: sku, Quantity: qty, Price: price})
	o.Items = append(o.Items, struct {
		SKU      string
		Quantity int
		Price    float64
	}{sku, qty, price})
}

func (o *StoreTestOrder) Ship() {
	o.Apply(StoreOrderShipped{OrderID: o.AggregateID()})
	o.Status = "Shipped"
}

func (o *StoreTestOrder) ApplyEvent(event interface{}) error {
	switch e := event.(type) {
	case StoreOrderCreated:
		o.CustomerID = e.CustomerID
		o.Status = "Created"
	case StoreItemAdded:
		o.Items = append(o.Items, struct {
			SKU      string
			Quantity int
			Price    float64
		}{e.SKU, e.Quantity, e.Price})
	case StoreOrderShipped:
		o.Status = "Shipped"
	}
	o.IncrementVersion()
	return nil
}

func TestEventStore_New(t *testing.T) {
	t.Run("creates with default serializer", func(t *testing.T) {
		adapter := memory.NewAdapter()
		store := New(adapter)

		assert.NotNil(t, store.Serializer())
		assert.Equal(t, adapter, store.Adapter())
	})

	t.Run("creates with custom serializer", func(t *testing.T) {
		adapter := memory.NewAdapter()
		serializer := NewJSONSerializer()
		serializer.Register("Test", struct{}{})

		store := New(adapter, WithSerializer(serializer))

		assert.Equal(t, serializer, store.Serializer())
	})

	t.Run("creates with custom logger", func(t *testing.T) {
		adapter := memory.NewAdapter()
		store := New(adapter, WithLogger(&noopLogger{}))

		assert.NotNil(t, store)
	})
}

func TestEventStore_RegisterEvents(t *testing.T) {
	t.Run("registers event types", func(t *testing.T) {
		adapter := memory.NewAdapter()
		store := New(adapter)

		store.RegisterEvents(StoreOrderCreated{}, StoreItemAdded{}, StoreOrderShipped{})

		serializer := store.Serializer().(*JSONSerializer)
		_, ok := serializer.Registry().Lookup("StoreOrderCreated")
		assert.True(t, ok)
	})
}

func TestEventStore_Append(t *testing.T) {
	t.Run("append events to new stream", func(t *testing.T) {
		adapter := memory.NewAdapter()
		store := New(adapter)

		events := []interface{}{
			StoreOrderCreated{OrderID: "123", CustomerID: "456"},
		}

		err := store.Append(context.Background(), "Order-123", events)

		require.NoError(t, err)
		assert.Equal(t, 1, adapter.EventCount())
	})

	t.Run("append multiple events", func(t *testing.T) {
		adapter := memory.NewAdapter()
		store := New(adapter)

		events := []interface{}{
			StoreOrderCreated{OrderID: "123", CustomerID: "456"},
			StoreItemAdded{OrderID: "123", SKU: "SKU-001", Quantity: 2, Price: 29.99},
			StoreItemAdded{OrderID: "123", SKU: "SKU-002", Quantity: 1, Price: 49.99},
		}

		err := store.Append(context.Background(), "Order-123", events)

		require.NoError(t, err)
		assert.Equal(t, 3, adapter.EventCount())
	})

	t.Run("append with expected version", func(t *testing.T) {
		adapter := memory.NewAdapter()
		store := New(adapter)

		// Create stream
		events1 := []interface{}{StoreOrderCreated{OrderID: "123", CustomerID: "456"}}
		err := store.Append(context.Background(), "Order-123", events1, ExpectVersion(NoStream))
		require.NoError(t, err)

		// Append more with version check
		events2 := []interface{}{StoreItemAdded{OrderID: "123", SKU: "SKU-001"}}
		err = store.Append(context.Background(), "Order-123", events2, ExpectVersion(1))
		require.NoError(t, err)
	})

	t.Run("append with metadata", func(t *testing.T) {
		adapter := memory.NewAdapter()
		store := New(adapter)

		metadata := Metadata{}.WithUserID("user-123").WithCorrelationID("corr-456")
		events := []interface{}{StoreOrderCreated{OrderID: "123", CustomerID: "456"}}

		err := store.Append(context.Background(), "Order-123", events, WithAppendMetadata(metadata))

		require.NoError(t, err)

		// Load and verify metadata
		stored, err := adapter.Load(context.Background(), "Order-123", 0)
		require.NoError(t, err)
		assert.Equal(t, "user-123", stored[0].Metadata.UserID)
		assert.Equal(t, "corr-456", stored[0].Metadata.CorrelationID)
	})

	t.Run("empty stream ID", func(t *testing.T) {
		adapter := memory.NewAdapter()
		store := New(adapter)

		err := store.Append(context.Background(), "", []interface{}{})

		assert.True(t, errors.Is(err, ErrEmptyStreamID))
	})

	t.Run("no events", func(t *testing.T) {
		adapter := memory.NewAdapter()
		store := New(adapter)

		err := store.Append(context.Background(), "Order-123", []interface{}{})

		assert.True(t, errors.Is(err, ErrNoEvents))
	})

	t.Run("concurrency conflict", func(t *testing.T) {
		adapter := memory.NewAdapter()
		store := New(adapter)

		// Create stream
		events := []interface{}{StoreOrderCreated{OrderID: "123", CustomerID: "456"}}
		err := store.Append(context.Background(), "Order-123", events, ExpectVersion(NoStream))
		require.NoError(t, err)

		// Try to create again
		err = store.Append(context.Background(), "Order-123", events, ExpectVersion(NoStream))
		assert.True(t, errors.Is(err, ErrConcurrencyConflict))
	})
}

func TestEventStore_Load(t *testing.T) {
	t.Run("load empty stream", func(t *testing.T) {
		adapter := memory.NewAdapter()
		store := New(adapter)
		store.RegisterEvents(StoreOrderCreated{})

		events, err := store.Load(context.Background(), "Order-nonexistent")

		require.NoError(t, err)
		assert.Empty(t, events)
	})

	t.Run("load all events", func(t *testing.T) {
		adapter := memory.NewAdapter()
		store := New(adapter)
		store.RegisterEvents(StoreOrderCreated{}, StoreItemAdded{})

		// Append events
		err := store.Append(context.Background(), "Order-123", []interface{}{
			StoreOrderCreated{OrderID: "123", CustomerID: "456"},
			StoreItemAdded{OrderID: "123", SKU: "SKU-001", Quantity: 2, Price: 29.99},
		})
		require.NoError(t, err)

		events, err := store.Load(context.Background(), "Order-123")

		require.NoError(t, err)
		require.Len(t, events, 2)
		assert.Equal(t, "StoreOrderCreated", events[0].Type)
		assert.Equal(t, "StoreItemAdded", events[1].Type)

		// Verify deserialized data
		orderCreated, ok := events[0].Data.(StoreOrderCreated)
		require.True(t, ok)
		assert.Equal(t, "123", orderCreated.OrderID)
		assert.Equal(t, "456", orderCreated.CustomerID)
	})

	t.Run("load from version", func(t *testing.T) {
		adapter := memory.NewAdapter()
		store := New(adapter)
		store.RegisterEvents(StoreOrderCreated{}, StoreItemAdded{})

		err := store.Append(context.Background(), "Order-123", []interface{}{
			StoreOrderCreated{OrderID: "123", CustomerID: "456"},
			StoreItemAdded{OrderID: "123", SKU: "SKU-001"},
			StoreItemAdded{OrderID: "123", SKU: "SKU-002"},
		})
		require.NoError(t, err)

		events, err := store.LoadFrom(context.Background(), "Order-123", 1)

		require.NoError(t, err)
		assert.Len(t, events, 2)
		assert.Equal(t, int64(2), events[0].Version)
	})

	t.Run("load raw events", func(t *testing.T) {
		adapter := memory.NewAdapter()
		store := New(adapter)

		err := store.Append(context.Background(), "Order-123", []interface{}{
			StoreOrderCreated{OrderID: "123", CustomerID: "456"},
		})
		require.NoError(t, err)

		events, err := store.LoadRaw(context.Background(), "Order-123", 0)

		require.NoError(t, err)
		require.Len(t, events, 1)
		assert.Equal(t, "StoreOrderCreated", events[0].Type)
		assert.NotEmpty(t, events[0].Data) // Raw JSON bytes
	})
}

func TestEventStore_SaveAggregate(t *testing.T) {
	t.Run("save new aggregate", func(t *testing.T) {
		adapter := memory.NewAdapter()
		store := New(adapter)

		order := NewStoreTestOrder("123")
		order.Create("customer-456")
		order.AddItem("SKU-001", 2, 29.99)

		err := store.SaveAggregate(context.Background(), order)

		require.NoError(t, err)
		assert.Equal(t, 2, adapter.EventCount())
		assert.Empty(t, order.UncommittedEvents()) // Should be cleared
	})

	t.Run("save aggregate with optimistic concurrency", func(t *testing.T) {
		adapter := memory.NewAdapter()
		store := New(adapter)

		// Create and save initial state
		order1 := NewStoreTestOrder("123")
		order1.Create("customer-456")
		err := store.SaveAggregate(context.Background(), order1)
		require.NoError(t, err)

		// Load, modify, and save
		order2 := NewStoreTestOrder("123")
		err = store.LoadAggregate(context.Background(), order2)
		require.NoError(t, err)

		order2.AddItem("SKU-001", 1, 29.99)
		err = store.SaveAggregate(context.Background(), order2)
		require.NoError(t, err)

		assert.Equal(t, 2, adapter.EventCount())
	})

	t.Run("save nil aggregate", func(t *testing.T) {
		adapter := memory.NewAdapter()
		store := New(adapter)

		err := store.SaveAggregate(context.Background(), nil)

		assert.True(t, errors.Is(err, ErrNilAggregate))
	})

	t.Run("save aggregate with no events", func(t *testing.T) {
		adapter := memory.NewAdapter()
		store := New(adapter)

		order := NewStoreTestOrder("123")

		err := store.SaveAggregate(context.Background(), order)

		assert.NoError(t, err)
		assert.Equal(t, 0, adapter.EventCount())
	})
}

func TestEventStore_LoadAggregate(t *testing.T) {
	t.Run("load aggregate rebuilds state", func(t *testing.T) {
		adapter := memory.NewAdapter()
		store := New(adapter)
		store.RegisterEvents(StoreOrderCreated{}, StoreItemAdded{}, StoreOrderShipped{})

		// Create and save aggregate
		original := NewStoreTestOrder("123")
		original.Create("customer-456")
		original.AddItem("SKU-001", 2, 29.99)
		original.AddItem("SKU-002", 1, 49.99)
		original.Ship()
		err := store.SaveAggregate(context.Background(), original)
		require.NoError(t, err)

		// Load into new instance
		loaded := NewStoreTestOrder("123")
		err = store.LoadAggregate(context.Background(), loaded)

		require.NoError(t, err)
		assert.Equal(t, "customer-456", loaded.CustomerID)
		assert.Equal(t, "Shipped", loaded.Status)
		assert.Len(t, loaded.Items, 2)
		assert.Equal(t, int64(4), loaded.Version())
	})

	t.Run("load non-existent aggregate", func(t *testing.T) {
		adapter := memory.NewAdapter()
		store := New(adapter)
		store.RegisterEvents(StoreOrderCreated{})

		order := NewStoreTestOrder("nonexistent")
		err := store.LoadAggregate(context.Background(), order)

		require.NoError(t, err)
		assert.Equal(t, int64(0), order.Version())
		assert.Equal(t, "", order.CustomerID)
	})

	t.Run("load nil aggregate", func(t *testing.T) {
		adapter := memory.NewAdapter()
		store := New(adapter)

		err := store.LoadAggregate(context.Background(), nil)

		assert.True(t, errors.Is(err, ErrNilAggregate))
	})
}

func TestEventStore_GetStreamInfo(t *testing.T) {
	t.Run("get stream info", func(t *testing.T) {
		adapter := memory.NewAdapter()
		store := New(adapter)

		err := store.Append(context.Background(), "Order-123", []interface{}{
			StoreOrderCreated{OrderID: "123", CustomerID: "456"},
			StoreItemAdded{OrderID: "123", SKU: "SKU-001"},
		})
		require.NoError(t, err)

		info, err := store.GetStreamInfo(context.Background(), "Order-123")

		require.NoError(t, err)
		assert.Equal(t, "Order-123", info.StreamID)
		assert.Equal(t, "Order", info.Category)
		assert.Equal(t, int64(2), info.Version)
	})

	t.Run("stream not found", func(t *testing.T) {
		adapter := memory.NewAdapter()
		store := New(adapter)

		_, err := store.GetStreamInfo(context.Background(), "Order-nonexistent")

		assert.True(t, errors.Is(err, ErrStreamNotFound))
	})
}

func TestEventStore_GetLastPosition(t *testing.T) {
	t.Run("empty store returns 0", func(t *testing.T) {
		adapter := memory.NewAdapter()
		store := New(adapter)

		pos, err := store.GetLastPosition(context.Background())

		require.NoError(t, err)
		assert.Equal(t, uint64(0), pos)
	})

	t.Run("returns last position", func(t *testing.T) {
		adapter := memory.NewAdapter()
		store := New(adapter)

		err := store.Append(context.Background(), "Order-1", []interface{}{StoreOrderCreated{}})
		require.NoError(t, err)
		err = store.Append(context.Background(), "Order-2", []interface{}{StoreOrderCreated{}})
		require.NoError(t, err)

		pos, err := store.GetLastPosition(context.Background())

		require.NoError(t, err)
		assert.Equal(t, uint64(2), pos)
	})
}

func TestEventStore_Initialize(t *testing.T) {
	t.Run("initialize adapter", func(t *testing.T) {
		adapter := memory.NewAdapter()
		store := New(adapter)

		err := store.Initialize(context.Background())

		assert.NoError(t, err)
	})
}

func TestEventStore_Close(t *testing.T) {
	t.Run("close releases resources", func(t *testing.T) {
		adapter := memory.NewAdapter()
		store := New(adapter)

		err := store.Close()

		assert.NoError(t, err)
	})
}

// Benchmark tests
func BenchmarkEventStore_Append(b *testing.B) {
	adapter := memory.NewAdapter()
	store := New(adapter)
	ctx := context.Background()

	event := StoreOrderCreated{OrderID: "123", CustomerID: "456"}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		streamID := "Order-" + string(rune(i))
		_ = store.Append(ctx, streamID, []interface{}{event}, ExpectVersion(AnyVersion))
	}
}

func BenchmarkEventStore_Load(b *testing.B) {
	adapter := memory.NewAdapter()
	store := New(adapter)
	store.RegisterEvents(StoreOrderCreated{}, StoreItemAdded{})
	ctx := context.Background()

	// Setup: create stream with events
	events := []interface{}{
		StoreOrderCreated{OrderID: "123", CustomerID: "456"},
		StoreItemAdded{OrderID: "123", SKU: "SKU-001"},
		StoreItemAdded{OrderID: "123", SKU: "SKU-002"},
		StoreItemAdded{OrderID: "123", SKU: "SKU-003"},
		StoreItemAdded{OrderID: "123", SKU: "SKU-004"},
	}
	_ = store.Append(ctx, "Order-bench", events)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = store.Load(ctx, "Order-bench")
	}
}

func BenchmarkEventStore_SaveAggregate(b *testing.B) {
	adapter := memory.NewAdapter()
	store := New(adapter)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		order := NewStoreTestOrder("order-" + string(rune(i)))
		order.Create("customer-456")
		order.AddItem("SKU-001", 2, 29.99)
		_ = store.SaveAggregate(ctx, order)
	}
}

func BenchmarkEventStore_LoadAggregate(b *testing.B) {
	adapter := memory.NewAdapter()
	store := New(adapter)
	store.RegisterEvents(StoreOrderCreated{}, StoreItemAdded{})
	ctx := context.Background()

	// Setup: create and save aggregate
	order := NewStoreTestOrder("bench-order")
	order.Create("customer-456")
	for i := 0; i < 10; i++ {
		order.AddItem("SKU-001", 1, 10.0)
	}
	_ = store.SaveAggregate(ctx, order)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		loaded := NewStoreTestOrder("bench-order")
		_ = store.LoadAggregate(ctx, loaded)
	}
}

// Test to verify that adapters.EventRecord is properly used
func TestEventStore_UsesAdapterRecords(t *testing.T) {
	adapter := memory.NewAdapter()
	store := New(adapter)
	ctx := context.Background()

	events := []interface{}{
		StoreOrderCreated{OrderID: "123", CustomerID: "456"},
	}

	err := store.Append(ctx, "Order-123", events)
	require.NoError(t, err)

	// Verify the adapter received the event
	assert.Equal(t, 1, adapter.EventCount())

	// Load directly from adapter to verify
	stored, err := adapter.Load(ctx, "Order-123", 0)
	require.NoError(t, err)
	require.Len(t, stored, 1)

	assert.Equal(t, "StoreOrderCreated", stored[0].Type)
	assert.NotEmpty(t, stored[0].Data)
}
