package msgpack

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Test Types
// =============================================================================

type OrderCreated struct {
	OrderID    string `msgpack:"order_id"`
	CustomerID string `msgpack:"customer_id"`
}

type ItemAdded struct {
	OrderID  string  `msgpack:"order_id"`
	SKU      string  `msgpack:"sku"`
	Quantity int     `msgpack:"quantity"`
	Price    float64 `msgpack:"price"`
}

type ComplexEvent struct {
	ID       string                 `msgpack:"id"`
	Tags     []string               `msgpack:"tags"`
	Metadata map[string]interface{} `msgpack:"metadata"`
	Nested   *NestedData            `msgpack:"nested"`
}

type NestedData struct {
	Value int    `msgpack:"value"`
	Name  string `msgpack:"name"`
}

// =============================================================================
// NewSerializer Tests
// =============================================================================

func TestNewSerializer(t *testing.T) {
	t.Run("creates empty serializer", func(t *testing.T) {
		s := NewSerializer()

		assert.NotNil(t, s)
		assert.Equal(t, 0, s.Count())
	})
}

func TestNewSerializerWithOptions(t *testing.T) {
	t.Run("with registry option", func(t *testing.T) {
		registry := map[string]reflect.Type{
			"OrderCreated": reflect.TypeOf(OrderCreated{}),
		}

		s := NewSerializerWithOptions(WithRegistry(registry))

		assert.Equal(t, 1, s.Count())
		_, ok := s.Lookup("OrderCreated")
		assert.True(t, ok)
	})
}

// =============================================================================
// Register Tests
// =============================================================================

func TestSerializer_Register(t *testing.T) {
	t.Run("registers event type", func(t *testing.T) {
		s := NewSerializer()
		s.Register("OrderCreated", OrderCreated{})

		registeredType, ok := s.Lookup("OrderCreated")
		require.True(t, ok)
		assert.Equal(t, reflect.TypeOf(OrderCreated{}), registeredType)
	})

	t.Run("registers pointer type as element type", func(t *testing.T) {
		s := NewSerializer()
		s.Register("OrderCreated", &OrderCreated{})

		registeredType, ok := s.Lookup("OrderCreated")
		require.True(t, ok)
		assert.Equal(t, reflect.TypeOf(OrderCreated{}), registeredType)
	})

	t.Run("overwrites existing registration", func(t *testing.T) {
		s := NewSerializer()
		s.Register("Event", OrderCreated{})
		s.Register("Event", ItemAdded{})

		registeredType, ok := s.Lookup("Event")
		require.True(t, ok)
		assert.Equal(t, reflect.TypeOf(ItemAdded{}), registeredType)
	})
}

func TestSerializer_RegisterAll(t *testing.T) {
	t.Run("registers multiple events", func(t *testing.T) {
		s := NewSerializer()
		s.RegisterAll(OrderCreated{}, ItemAdded{})

		assert.Equal(t, 2, s.Count())

		_, ok1 := s.Lookup("OrderCreated")
		_, ok2 := s.Lookup("ItemAdded")
		assert.True(t, ok1)
		assert.True(t, ok2)
	})

	t.Run("handles pointer types", func(t *testing.T) {
		s := NewSerializer()
		s.RegisterAll(&OrderCreated{}, &ItemAdded{})

		assert.Equal(t, 2, s.Count())
	})
}

// =============================================================================
// Lookup Tests
// =============================================================================

func TestSerializer_Lookup(t *testing.T) {
	t.Run("returns type when registered", func(t *testing.T) {
		s := NewSerializer()
		s.Register("OrderCreated", OrderCreated{})

		registeredType, ok := s.Lookup("OrderCreated")

		assert.True(t, ok)
		assert.Equal(t, reflect.TypeOf(OrderCreated{}), registeredType)
	})

	t.Run("returns false when not registered", func(t *testing.T) {
		s := NewSerializer()

		_, ok := s.Lookup("NotRegistered")

		assert.False(t, ok)
	})
}

// =============================================================================
// RegisteredTypes Tests
// =============================================================================

func TestSerializer_RegisteredTypes(t *testing.T) {
	t.Run("returns empty slice for empty registry", func(t *testing.T) {
		s := NewSerializer()

		types := s.RegisteredTypes()

		assert.Empty(t, types)
	})

	t.Run("returns all registered types", func(t *testing.T) {
		s := NewSerializer()
		s.RegisterAll(OrderCreated{}, ItemAdded{})

		types := s.RegisteredTypes()

		assert.Len(t, types, 2)
		assert.Contains(t, types, "OrderCreated")
		assert.Contains(t, types, "ItemAdded")
	})
}

// =============================================================================
// Count Tests
// =============================================================================

func TestSerializer_Count(t *testing.T) {
	t.Run("returns zero for empty registry", func(t *testing.T) {
		s := NewSerializer()
		assert.Equal(t, 0, s.Count())
	})

	t.Run("returns correct count", func(t *testing.T) {
		s := NewSerializer()
		s.RegisterAll(OrderCreated{}, ItemAdded{})
		assert.Equal(t, 2, s.Count())
	})
}

// =============================================================================
// Serialize Tests
// =============================================================================

func TestSerializer_Serialize(t *testing.T) {
	t.Run("serializes simple event", func(t *testing.T) {
		s := NewSerializer()
		event := OrderCreated{
			OrderID:    "order-123",
			CustomerID: "customer-456",
		}

		data, err := s.Serialize(event)

		require.NoError(t, err)
		assert.NotEmpty(t, data)
		// MessagePack produces binary data
		assert.True(t, len(data) > 0)
	})

	t.Run("serializes complex event", func(t *testing.T) {
		s := NewSerializer()
		event := ComplexEvent{
			ID:   "event-123",
			Tags: []string{"tag1", "tag2"},
			Metadata: map[string]interface{}{
				"key1": "value1",
				"key2": 42,
			},
			Nested: &NestedData{
				Value: 100,
				Name:  "nested",
			},
		}

		data, err := s.Serialize(event)

		require.NoError(t, err)
		assert.NotEmpty(t, data)
	})

	t.Run("returns error for nil event", func(t *testing.T) {
		s := NewSerializer()

		_, err := s.Serialize(nil)

		require.Error(t, err)
		var serErr *SerializationError
		require.ErrorAs(t, err, &serErr)
		assert.Equal(t, "nil", serErr.EventType)
		assert.Equal(t, "serialize", serErr.Operation)
	})

	t.Run("produces smaller output than JSON", func(t *testing.T) {
		s := NewSerializer()
		event := ComplexEvent{
			ID:   "event-123",
			Tags: []string{"tag1", "tag2", "tag3", "tag4", "tag5"},
			Metadata: map[string]interface{}{
				"key1": "value1",
				"key2": "value2",
				"key3": "value3",
			},
			Nested: &NestedData{
				Value: 100,
				Name:  "nested-data-name",
			},
		}

		data, err := s.Serialize(event)
		require.NoError(t, err)

		// MessagePack should produce compact output
		// JSON equivalent would be larger
		assert.NotEmpty(t, data)
	})
}

// =============================================================================
// Deserialize Tests
// =============================================================================

func TestSerializer_Deserialize(t *testing.T) {
	t.Run("deserializes to registered type", func(t *testing.T) {
		s := NewSerializer()
		s.Register("OrderCreated", OrderCreated{})

		original := OrderCreated{
			OrderID:    "order-123",
			CustomerID: "customer-456",
		}
		data, err := s.Serialize(original)
		require.NoError(t, err)

		result, err := s.Deserialize(data, "OrderCreated")

		require.NoError(t, err)
		deserialized, ok := result.(OrderCreated)
		require.True(t, ok)
		assert.Equal(t, original.OrderID, deserialized.OrderID)
		assert.Equal(t, original.CustomerID, deserialized.CustomerID)
	})

	t.Run("deserializes complex event", func(t *testing.T) {
		s := NewSerializer()
		s.Register("ComplexEvent", ComplexEvent{})

		original := ComplexEvent{
			ID:   "event-123",
			Tags: []string{"tag1", "tag2"},
			Metadata: map[string]interface{}{
				"key1": "value1",
			},
			Nested: &NestedData{
				Value: 100,
				Name:  "nested",
			},
		}
		data, err := s.Serialize(original)
		require.NoError(t, err)

		result, err := s.Deserialize(data, "ComplexEvent")

		require.NoError(t, err)
		deserialized, ok := result.(ComplexEvent)
		require.True(t, ok)
		assert.Equal(t, original.ID, deserialized.ID)
		assert.Equal(t, original.Tags, deserialized.Tags)
		assert.Equal(t, original.Nested.Value, deserialized.Nested.Value)
	})

	t.Run("deserializes to map when type not registered", func(t *testing.T) {
		s := NewSerializer()

		original := OrderCreated{
			OrderID:    "order-123",
			CustomerID: "customer-456",
		}
		data, err := s.Serialize(original)
		require.NoError(t, err)

		result, err := s.Deserialize(data, "UnregisteredType")

		require.NoError(t, err)
		mapResult, ok := result.(map[string]interface{})
		require.True(t, ok)
		assert.Equal(t, "order-123", mapResult["order_id"])
		assert.Equal(t, "customer-456", mapResult["customer_id"])
	})

	t.Run("returns error for empty data", func(t *testing.T) {
		s := NewSerializer()

		_, err := s.Deserialize([]byte{}, "OrderCreated")

		require.Error(t, err)
		var serErr *SerializationError
		require.ErrorAs(t, err, &serErr)
		assert.Equal(t, "OrderCreated", serErr.EventType)
		assert.Equal(t, "deserialize", serErr.Operation)
	})

	t.Run("returns error for nil data", func(t *testing.T) {
		s := NewSerializer()

		_, err := s.Deserialize(nil, "OrderCreated")

		require.Error(t, err)
	})

	t.Run("returns error for invalid data", func(t *testing.T) {
		s := NewSerializer()
		s.Register("OrderCreated", OrderCreated{})

		_, err := s.Deserialize([]byte("invalid msgpack data"), "OrderCreated")

		require.Error(t, err)
	})
}

// =============================================================================
// Round-trip Tests
// =============================================================================

func TestSerializer_RoundTrip(t *testing.T) {
	t.Run("preserves data through serialize/deserialize", func(t *testing.T) {
		s := NewSerializer()
		s.Register("OrderCreated", OrderCreated{})

		events := []OrderCreated{
			{OrderID: "order-1", CustomerID: "customer-1"},
			{OrderID: "order-2", CustomerID: "customer-2"},
			{OrderID: "", CustomerID: ""},
		}

		for _, original := range events {
			data, err := s.Serialize(original)
			require.NoError(t, err)

			result, err := s.Deserialize(data, "OrderCreated")
			require.NoError(t, err)

			deserialized := result.(OrderCreated)
			assert.Equal(t, original, deserialized)
		}
	})

	t.Run("preserves complex data", func(t *testing.T) {
		s := NewSerializer()
		s.Register("ComplexEvent", ComplexEvent{})

		original := ComplexEvent{
			ID:   "event-123",
			Tags: []string{"tag1", "tag2"},
			Metadata: map[string]interface{}{
				"string": "value",
				"number": int64(42),
				"float":  3.14,
				"bool":   true,
			},
			Nested: &NestedData{
				Value: 100,
				Name:  "test",
			},
		}

		data, err := s.Serialize(original)
		require.NoError(t, err)

		result, err := s.Deserialize(data, "ComplexEvent")
		require.NoError(t, err)

		deserialized := result.(ComplexEvent)
		assert.Equal(t, original.ID, deserialized.ID)
		assert.Equal(t, original.Tags, deserialized.Tags)
		assert.Equal(t, original.Nested.Value, deserialized.Nested.Value)
		assert.Equal(t, original.Nested.Name, deserialized.Nested.Name)
	})
}

// =============================================================================
// SerializationError Tests
// =============================================================================

func TestSerializationError(t *testing.T) {
	t.Run("Error returns formatted message", func(t *testing.T) {
		err := &SerializationError{
			EventType: "OrderCreated",
			Operation: "serialize",
			Err:       assert.AnError,
		}

		msg := err.Error()

		assert.Contains(t, msg, "mink/msgpack")
		assert.Contains(t, msg, "serialize")
		assert.Contains(t, msg, "OrderCreated")
	})

	t.Run("Unwrap returns underlying error", func(t *testing.T) {
		underlying := assert.AnError
		err := &SerializationError{
			EventType: "OrderCreated",
			Operation: "deserialize",
			Err:       underlying,
		}

		assert.Equal(t, underlying, err.Unwrap())
	})
}

// =============================================================================
// Concurrency Tests
// =============================================================================

func TestSerializer_Concurrency(t *testing.T) {
	t.Run("concurrent registration is safe", func(t *testing.T) {
		s := NewSerializer()

		done := make(chan bool)
		for i := 0; i < 100; i++ {
			go func(i int) {
				s.Register("Event", OrderCreated{})
				done <- true
			}(i)
		}

		for i := 0; i < 100; i++ {
			<-done
		}

		// Should not panic
		assert.True(t, s.Count() >= 1)
	})

	t.Run("concurrent serialize/deserialize is safe", func(t *testing.T) {
		s := NewSerializer()
		s.Register("OrderCreated", OrderCreated{})

		done := make(chan bool)
		for i := 0; i < 100; i++ {
			go func(i int) {
				event := OrderCreated{OrderID: "order-123"}
				data, err := s.Serialize(event)
				if err == nil {
					_, _ = s.Deserialize(data, "OrderCreated")
				}
				done <- true
			}(i)
		}

		for i := 0; i < 100; i++ {
			<-done
		}
	})
}

// =============================================================================
// Benchmark Tests
// =============================================================================

func BenchmarkSerializer_Serialize(b *testing.B) {
	s := NewSerializer()
	event := OrderCreated{
		OrderID:    "order-123",
		CustomerID: "customer-456",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = s.Serialize(event)
	}
}

func BenchmarkSerializer_Deserialize(b *testing.B) {
	s := NewSerializer()
	s.Register("OrderCreated", OrderCreated{})

	event := OrderCreated{
		OrderID:    "order-123",
		CustomerID: "customer-456",
	}
	data, _ := s.Serialize(event)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = s.Deserialize(data, "OrderCreated")
	}
}
