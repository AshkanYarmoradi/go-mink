package mink

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test event types
type OrderCreated struct {
	OrderID    string `json:"orderId"`
	CustomerID string `json:"customerId"`
}

type ItemAdded struct {
	OrderID  string  `json:"orderId"`
	SKU      string  `json:"sku"`
	Quantity int     `json:"quantity"`
	Price    float64 `json:"price"`
}

type OrderShipped struct {
	OrderID   string    `json:"orderId"`
	ShippedAt time.Time `json:"shippedAt"`
}

func TestEventRegistry(t *testing.T) {
	t.Run("Register adds type", func(t *testing.T) {
		r := NewEventRegistry()
		r.Register("OrderCreated", OrderCreated{})

		_, ok := r.Lookup("OrderCreated")
		assert.True(t, ok)
	})

	t.Run("Register handles pointer", func(t *testing.T) {
		r := NewEventRegistry()
		r.Register("OrderCreated", &OrderCreated{})

		t_, ok := r.Lookup("OrderCreated")
		assert.True(t, ok)
		assert.Equal(t, "OrderCreated", t_.Name())
	})

	t.Run("RegisterAll uses struct names", func(t *testing.T) {
		r := NewEventRegistry()
		r.RegisterAll(OrderCreated{}, ItemAdded{}, OrderShipped{})

		assert.Equal(t, 3, r.Count())

		_, ok1 := r.Lookup("OrderCreated")
		_, ok2 := r.Lookup("ItemAdded")
		_, ok3 := r.Lookup("OrderShipped")

		assert.True(t, ok1)
		assert.True(t, ok2)
		assert.True(t, ok3)
	})

	t.Run("RegisterAll handles pointers", func(t *testing.T) {
		r := NewEventRegistry()
		r.RegisterAll(&OrderCreated{}, &ItemAdded{})

		assert.Equal(t, 2, r.Count())

		t_, ok := r.Lookup("OrderCreated")
		assert.True(t, ok)
		assert.Equal(t, "OrderCreated", t_.Name())
	})

	t.Run("Lookup returns false for unregistered type", func(t *testing.T) {
		r := NewEventRegistry()

		_, ok := r.Lookup("UnknownEvent")
		assert.False(t, ok)
	})

	t.Run("RegisteredTypes returns all types", func(t *testing.T) {
		r := NewEventRegistry()
		r.RegisterAll(OrderCreated{}, ItemAdded{})

		types := r.RegisteredTypes()
		assert.Len(t, types, 2)
		assert.Contains(t, types, "OrderCreated")
		assert.Contains(t, types, "ItemAdded")
	})

	t.Run("Count returns correct count", func(t *testing.T) {
		r := NewEventRegistry()
		assert.Equal(t, 0, r.Count())

		r.Register("OrderCreated", OrderCreated{})
		assert.Equal(t, 1, r.Count())

		r.Register("ItemAdded", ItemAdded{})
		assert.Equal(t, 2, r.Count())
	})
}

func TestJSONSerializer(t *testing.T) {
	t.Run("NewJSONSerializer creates with empty registry", func(t *testing.T) {
		s := NewJSONSerializer()

		assert.NotNil(t, s.Registry())
		assert.Equal(t, 0, s.Registry().Count())
	})

	t.Run("NewJSONSerializerWithRegistry uses provided registry", func(t *testing.T) {
		r := NewEventRegistry()
		r.Register("Test", OrderCreated{})

		s := NewJSONSerializerWithRegistry(r)

		assert.Equal(t, 1, s.Registry().Count())
	})

	t.Run("NewJSONSerializerWithRegistry handles nil registry", func(t *testing.T) {
		s := NewJSONSerializerWithRegistry(nil)

		assert.NotNil(t, s.Registry())
	})

	t.Run("Register adds to registry", func(t *testing.T) {
		s := NewJSONSerializer()
		s.Register("OrderCreated", OrderCreated{})

		_, ok := s.Registry().Lookup("OrderCreated")
		assert.True(t, ok)
	})

	t.Run("RegisterAll adds multiple types", func(t *testing.T) {
		s := NewJSONSerializer()
		s.RegisterAll(OrderCreated{}, ItemAdded{})

		assert.Equal(t, 2, s.Registry().Count())
	})

	t.Run("Serialize creates valid JSON", func(t *testing.T) {
		s := NewJSONSerializer()

		event := OrderCreated{
			OrderID:    "order-123",
			CustomerID: "customer-456",
		}

		data, err := s.Serialize(event)
		require.NoError(t, err)
		assert.JSONEq(t, `{"orderId":"order-123","customerId":"customer-456"}`, string(data))
	})

	t.Run("Serialize returns error for nil", func(t *testing.T) {
		s := NewJSONSerializer()

		_, err := s.Serialize(nil)
		assert.Error(t, err)
		assert.True(t, errors.Is(err, ErrSerializationFailed))
	})

	t.Run("Serialize returns error for invalid type", func(t *testing.T) {
		s := NewJSONSerializer()

		// channels cannot be serialized to JSON
		ch := make(chan int)

		_, err := s.Serialize(ch)
		assert.Error(t, err)
		assert.True(t, errors.Is(err, ErrSerializationFailed))
	})

	t.Run("Deserialize to registered type", func(t *testing.T) {
		s := NewJSONSerializer()
		s.Register("OrderCreated", OrderCreated{})

		data := []byte(`{"orderId":"order-123","customerId":"customer-456"}`)

		result, err := s.Deserialize(data, "OrderCreated")
		require.NoError(t, err)

		event, ok := result.(OrderCreated)
		require.True(t, ok)
		assert.Equal(t, "order-123", event.OrderID)
		assert.Equal(t, "customer-456", event.CustomerID)
	})

	t.Run("Deserialize to map for unregistered type", func(t *testing.T) {
		s := NewJSONSerializer()

		data := []byte(`{"orderId":"order-123","customerId":"customer-456"}`)

		result, err := s.Deserialize(data, "UnknownEvent")
		require.NoError(t, err)

		m, ok := result.(map[string]interface{})
		require.True(t, ok)
		assert.Equal(t, "order-123", m["orderId"])
		assert.Equal(t, "customer-456", m["customerId"])
	})

	t.Run("Deserialize returns error for empty data", func(t *testing.T) {
		s := NewJSONSerializer()

		_, err := s.Deserialize([]byte{}, "OrderCreated")
		assert.Error(t, err)
		assert.True(t, errors.Is(err, ErrSerializationFailed))
	})

	t.Run("Deserialize returns error for invalid JSON", func(t *testing.T) {
		s := NewJSONSerializer()
		s.Register("OrderCreated", OrderCreated{})

		_, err := s.Deserialize([]byte(`{invalid`), "OrderCreated")
		assert.Error(t, err)
		assert.True(t, errors.Is(err, ErrSerializationFailed))
	})

	t.Run("Deserialize returns error for invalid JSON to map", func(t *testing.T) {
		s := NewJSONSerializer()
		// No registration - will fall back to map

		_, err := s.Deserialize([]byte(`{invalid`), "UnknownEvent")
		assert.Error(t, err)
		assert.True(t, errors.Is(err, ErrSerializationFailed))
	})

	t.Run("roundtrip preserves data", func(t *testing.T) {
		s := NewJSONSerializer()
		s.Register("OrderShipped", OrderShipped{})

		original := OrderShipped{
			OrderID:   "order-123",
			ShippedAt: time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC),
		}

		data, err := s.Serialize(original)
		require.NoError(t, err)

		result, err := s.Deserialize(data, "OrderShipped")
		require.NoError(t, err)

		event, ok := result.(OrderShipped)
		require.True(t, ok)
		assert.Equal(t, original.OrderID, event.OrderID)
		assert.Equal(t, original.ShippedAt.UTC(), event.ShippedAt.UTC())
	})
}

func TestGetEventType(t *testing.T) {
	t.Run("returns struct name", func(t *testing.T) {
		assert.Equal(t, "OrderCreated", GetEventType(OrderCreated{}))
		assert.Equal(t, "ItemAdded", GetEventType(ItemAdded{}))
	})

	t.Run("handles pointer", func(t *testing.T) {
		assert.Equal(t, "OrderCreated", GetEventType(&OrderCreated{}))
	})

	t.Run("returns empty for nil", func(t *testing.T) {
		assert.Equal(t, "", GetEventType(nil))
	})
}

func TestSerializeEvent(t *testing.T) {
	t.Run("creates EventData", func(t *testing.T) {
		s := NewJSONSerializer()
		event := OrderCreated{OrderID: "order-123", CustomerID: "customer-456"}
		metadata := Metadata{}.WithUserID("user-789")

		eventData, err := SerializeEvent(s, event, metadata)
		require.NoError(t, err)

		assert.Equal(t, "OrderCreated", eventData.Type)
		assert.JSONEq(t, `{"orderId":"order-123","customerId":"customer-456"}`, string(eventData.Data))
		assert.Equal(t, "user-789", eventData.Metadata.UserID)
	})

	t.Run("returns error for nil event", func(t *testing.T) {
		s := NewJSONSerializer()

		_, err := SerializeEvent(s, nil, Metadata{})
		assert.Error(t, err)
	})

	t.Run("returns error when serializer fails", func(t *testing.T) {
		s := &testFailingSerializer{serializeErr: errors.New("serialize failed")}
		event := OrderCreated{OrderID: "order-123", CustomerID: "customer-456"}

		_, err := SerializeEvent(s, event, Metadata{})
		assert.Error(t, err)
	})
}

type testFailingSerializer struct {
	serializeErr   error
	deserializeErr error
}

func (s *testFailingSerializer) Serialize(v interface{}) ([]byte, error) {
	if s.serializeErr != nil {
		return nil, s.serializeErr
	}
	return []byte("{}"), nil
}

func (s *testFailingSerializer) Deserialize(data []byte, eventType string) (interface{}, error) {
	if s.deserializeErr != nil {
		return nil, s.deserializeErr
	}
	return map[string]interface{}{}, nil
}

func (s *testFailingSerializer) Register(eventType string, example interface{}) {}

func (s *testFailingSerializer) RegisterAll(examples ...interface{}) {}

func TestDeserializeEvent(t *testing.T) {
	t.Run("creates Event from StoredEvent", func(t *testing.T) {
		s := NewJSONSerializer()
		s.Register("OrderCreated", OrderCreated{})

		now := time.Now()
		stored := StoredEvent{
			ID:             "evt-123",
			StreamID:       "Order-456",
			Type:           "OrderCreated",
			Data:           []byte(`{"orderId":"order-456","customerId":"customer-789"}`),
			Metadata:       Metadata{}.WithUserID("user-123"),
			Version:        1,
			GlobalPosition: 100,
			Timestamp:      now,
		}

		event, err := DeserializeEvent(s, stored)
		require.NoError(t, err)

		assert.Equal(t, "evt-123", event.ID)
		assert.Equal(t, "Order-456", event.StreamID)
		assert.Equal(t, "OrderCreated", event.Type)
		assert.Equal(t, int64(1), event.Version)
		assert.Equal(t, uint64(100), event.GlobalPosition)
		assert.Equal(t, now, event.Timestamp)
		assert.Equal(t, "user-123", event.Metadata.UserID)

		orderCreated, ok := event.Data.(OrderCreated)
		require.True(t, ok)
		assert.Equal(t, "order-456", orderCreated.OrderID)
		assert.Equal(t, "customer-789", orderCreated.CustomerID)
	})

	t.Run("returns error for invalid data", func(t *testing.T) {
		s := NewJSONSerializer()

		stored := StoredEvent{
			Type: "OrderCreated",
			Data: []byte{},
		}

		_, err := DeserializeEvent(s, stored)
		assert.Error(t, err)
	})
}

// Test concurrent access to registry
func TestEventRegistry_Concurrent(t *testing.T) {
	r := NewEventRegistry()

	done := make(chan bool)

	// Writer goroutine
	go func() {
		for i := 0; i < 100; i++ {
			r.Register("OrderCreated", OrderCreated{})
			r.RegisterAll(ItemAdded{}, OrderShipped{})
		}
		done <- true
	}()

	// Reader goroutines
	for i := 0; i < 5; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				r.Lookup("OrderCreated")
				r.RegisteredTypes()
				r.Count()
			}
			done <- true
		}()
	}

	// Wait for all goroutines
	for i := 0; i < 6; i++ {
		<-done
	}
}
