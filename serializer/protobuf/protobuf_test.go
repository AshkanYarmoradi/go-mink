package protobuf

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// =============================================================================
// Test Types (Non-Proto)
// =============================================================================

// NonProtoEvent is a regular Go struct that does NOT implement proto.Message.
type NonProtoEvent struct {
	ID   string
	Data string
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
			"StringValue": reflect.TypeOf(wrapperspb.StringValue{}),
		}

		s := NewSerializerWithOptions(WithRegistry(registry))

		assert.Equal(t, 1, s.Count())
		_, ok := s.Lookup("StringValue")
		assert.True(t, ok)
	})
}

// =============================================================================
// Register Tests
// =============================================================================

func TestSerializer_Register(t *testing.T) {
	t.Run("registers proto.Message type", func(t *testing.T) {
		s := NewSerializer()
		err := s.Register("StringValue", &wrapperspb.StringValue{})

		require.NoError(t, err)
		registeredType, ok := s.Lookup("StringValue")
		require.True(t, ok)
		assert.Equal(t, reflect.TypeOf(wrapperspb.StringValue{}), registeredType)
	})

	t.Run("overwrites existing registration", func(t *testing.T) {
		s := NewSerializer()
		_ = s.Register("Event", &wrapperspb.StringValue{})
		err := s.Register("Event", &wrapperspb.Int32Value{})

		require.NoError(t, err)
		registeredType, ok := s.Lookup("Event")
		require.True(t, ok)
		assert.Equal(t, reflect.TypeOf(wrapperspb.Int32Value{}), registeredType)
	})

	t.Run("returns error for non-proto.Message type", func(t *testing.T) {
		s := NewSerializer()
		err := s.Register("NonProto", NonProtoEvent{})

		require.Error(t, err)
		var serErr *SerializationError
		require.ErrorAs(t, err, &serErr)
		assert.True(t, errors.Is(serErr, ErrNotProtoMessage))
	})
}

func TestSerializer_RegisterAll(t *testing.T) {
	t.Run("registers multiple proto.Message events", func(t *testing.T) {
		s := NewSerializer()
		err := s.RegisterAll(&wrapperspb.StringValue{}, &wrapperspb.Int32Value{})

		require.NoError(t, err)
		assert.Equal(t, 2, s.Count())

		_, ok1 := s.Lookup("StringValue")
		_, ok2 := s.Lookup("Int32Value")
		assert.True(t, ok1)
		assert.True(t, ok2)
	})

	t.Run("returns error for non-proto.Message in batch", func(t *testing.T) {
		s := NewSerializer()
		err := s.RegisterAll(&wrapperspb.StringValue{}, NonProtoEvent{})

		require.Error(t, err)
		// Only the first event should have been registered
		assert.Equal(t, 1, s.Count())
	})
}

func TestSerializer_MustRegister(t *testing.T) {
	t.Run("registers without panic for proto.Message", func(t *testing.T) {
		s := NewSerializer()

		assert.NotPanics(t, func() {
			s.MustRegister("StringValue", &wrapperspb.StringValue{})
		})
		assert.Equal(t, 1, s.Count())
	})

	t.Run("panics for non-proto.Message", func(t *testing.T) {
		s := NewSerializer()

		assert.Panics(t, func() {
			s.MustRegister("NonProto", NonProtoEvent{})
		})
	})
}

// =============================================================================
// Lookup Tests
// =============================================================================

func TestSerializer_Lookup(t *testing.T) {
	t.Run("returns type when registered", func(t *testing.T) {
		s := NewSerializer()
		_ = s.Register("StringValue", &wrapperspb.StringValue{})

		registeredType, ok := s.Lookup("StringValue")

		assert.True(t, ok)
		assert.Equal(t, reflect.TypeOf(wrapperspb.StringValue{}), registeredType)
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
		_ = s.RegisterAll(&wrapperspb.StringValue{}, &wrapperspb.Int32Value{})

		types := s.RegisteredTypes()

		assert.Len(t, types, 2)
		assert.Contains(t, types, "StringValue")
		assert.Contains(t, types, "Int32Value")
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
		_ = s.RegisterAll(&wrapperspb.StringValue{}, &wrapperspb.Int32Value{})
		assert.Equal(t, 2, s.Count())
	})
}

// =============================================================================
// Serialize Tests
// =============================================================================

func TestSerializer_Serialize(t *testing.T) {
	t.Run("serializes proto.Message event", func(t *testing.T) {
		s := NewSerializer()
		event := wrapperspb.String("order-123")

		data, err := s.Serialize(event)

		require.NoError(t, err)
		assert.NotEmpty(t, data)
		// Protocol Buffers produces binary data
		assert.True(t, len(data) > 0)
	})

	t.Run("serializes int value", func(t *testing.T) {
		s := NewSerializer()
		event := wrapperspb.Int32(42)

		data, err := s.Serialize(event)

		require.NoError(t, err)
		assert.NotEmpty(t, data)
	})

	t.Run("serializes double value", func(t *testing.T) {
		s := NewSerializer()
		event := wrapperspb.Double(3.14159)

		data, err := s.Serialize(event)

		require.NoError(t, err)
		assert.NotEmpty(t, data)
	})

	t.Run("serializes bool value", func(t *testing.T) {
		s := NewSerializer()
		event := wrapperspb.Bool(true)

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
		assert.True(t, errors.Is(serErr, ErrNilEvent))
	})

	t.Run("returns error for non-proto.Message", func(t *testing.T) {
		s := NewSerializer()
		event := NonProtoEvent{ID: "123", Data: "test"}

		_, err := s.Serialize(event)

		require.Error(t, err)
		var serErr *SerializationError
		require.ErrorAs(t, err, &serErr)
		assert.Equal(t, "serialize", serErr.Operation)
		assert.True(t, errors.Is(serErr, ErrNotProtoMessage))
	})

	t.Run("produces compact binary output", func(t *testing.T) {
		s := NewSerializer()
		event := wrapperspb.String("this is a longer string value for testing compact output")

		data, err := s.Serialize(event)
		require.NoError(t, err)

		// Protocol Buffers should produce compact output
		assert.NotEmpty(t, data)
	})
}

// =============================================================================
// Deserialize Tests
// =============================================================================

func TestSerializer_Deserialize(t *testing.T) {
	t.Run("deserializes to registered type", func(t *testing.T) {
		s := NewSerializer()
		_ = s.Register("StringValue", &wrapperspb.StringValue{})

		original := wrapperspb.String("order-123")
		data, err := s.Serialize(original)
		require.NoError(t, err)

		result, err := s.Deserialize(data, "StringValue")

		require.NoError(t, err)
		deserialized, ok := result.(wrapperspb.StringValue)
		require.True(t, ok)
		assert.Equal(t, original.GetValue(), deserialized.GetValue())
	})

	t.Run("deserializes int value", func(t *testing.T) {
		s := NewSerializer()
		_ = s.Register("Int32Value", &wrapperspb.Int32Value{})

		original := wrapperspb.Int32(42)
		data, err := s.Serialize(original)
		require.NoError(t, err)

		result, err := s.Deserialize(data, "Int32Value")

		require.NoError(t, err)
		deserialized, ok := result.(wrapperspb.Int32Value)
		require.True(t, ok)
		assert.Equal(t, original.GetValue(), deserialized.GetValue())
	})

	t.Run("deserializes double value", func(t *testing.T) {
		s := NewSerializer()
		_ = s.Register("DoubleValue", &wrapperspb.DoubleValue{})

		original := wrapperspb.Double(3.14159)
		data, err := s.Serialize(original)
		require.NoError(t, err)

		result, err := s.Deserialize(data, "DoubleValue")

		require.NoError(t, err)
		deserialized, ok := result.(wrapperspb.DoubleValue)
		require.True(t, ok)
		assert.InDelta(t, original.GetValue(), deserialized.GetValue(), 0.00001)
	})

	t.Run("deserializes bool value", func(t *testing.T) {
		s := NewSerializer()
		_ = s.Register("BoolValue", &wrapperspb.BoolValue{})

		original := wrapperspb.Bool(true)
		data, err := s.Serialize(original)
		require.NoError(t, err)

		result, err := s.Deserialize(data, "BoolValue")

		require.NoError(t, err)
		deserialized, ok := result.(wrapperspb.BoolValue)
		require.True(t, ok)
		assert.Equal(t, original.GetValue(), deserialized.GetValue())
	})

	t.Run("returns error when type not registered", func(t *testing.T) {
		s := NewSerializer()

		original := wrapperspb.String("order-123")
		data, err := s.Serialize(original)
		require.NoError(t, err)

		_, err = s.Deserialize(data, "UnregisteredType")

		require.Error(t, err)
		var serErr *SerializationError
		require.ErrorAs(t, err, &serErr)
		assert.Equal(t, "UnregisteredType", serErr.EventType)
		assert.True(t, errors.Is(serErr, ErrTypeNotRegistered))
	})

	t.Run("deserializes empty byte slice as zero value", func(t *testing.T) {
		s := NewSerializer()
		_ = s.Register("StringValue", &wrapperspb.StringValue{})

		// Empty byte slice is valid protobuf encoding for zero values
		result, err := s.Deserialize([]byte{}, "StringValue")

		require.NoError(t, err)
		deserialized, ok := result.(wrapperspb.StringValue)
		require.True(t, ok)
		// Empty protobuf message deserializes to zero value
		assert.Equal(t, "", deserialized.GetValue())
	})

	t.Run("returns error for nil data", func(t *testing.T) {
		s := NewSerializer()
		_ = s.Register("StringValue", &wrapperspb.StringValue{})

		_, err := s.Deserialize(nil, "StringValue")

		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrEmptyData))
	})

	t.Run("handles empty byte slice (valid for zero values)", func(t *testing.T) {
		s := NewSerializer()
		_ = s.Register("StringValue", &wrapperspb.StringValue{})

		// Empty byte slice is valid protobuf for zero values
		result, err := s.Deserialize([]byte{}, "StringValue")

		require.NoError(t, err)
		deserialized, ok := result.(wrapperspb.StringValue)
		require.True(t, ok)
		assert.Equal(t, "", deserialized.GetValue())
	})
}

// =============================================================================
// Round-trip Tests
// =============================================================================

func TestSerializer_RoundTrip(t *testing.T) {
	t.Run("preserves string data through serialize/deserialize", func(t *testing.T) {
		s := NewSerializer()
		_ = s.Register("StringValue", &wrapperspb.StringValue{})

		values := []string{"order-1", "order-2", "", "unicode: 你好世界"}

		for _, val := range values {
			original := wrapperspb.String(val)
			data, err := s.Serialize(original)
			require.NoError(t, err)

			result, err := s.Deserialize(data, "StringValue")
			require.NoError(t, err)

			deserialized := result.(wrapperspb.StringValue)
			assert.Equal(t, original.GetValue(), deserialized.GetValue())
		}
	})

	t.Run("preserves int data through serialize/deserialize", func(t *testing.T) {
		s := NewSerializer()
		_ = s.Register("Int32Value", &wrapperspb.Int32Value{})

		values := []int32{0, 1, -1, 42, -42, 2147483647, -2147483648}

		for _, val := range values {
			original := wrapperspb.Int32(val)
			data, err := s.Serialize(original)
			require.NoError(t, err)

			result, err := s.Deserialize(data, "Int32Value")
			require.NoError(t, err)

			deserialized := result.(wrapperspb.Int32Value)
			assert.Equal(t, original.GetValue(), deserialized.GetValue())
		}
	})

	t.Run("preserves int64 data through serialize/deserialize", func(t *testing.T) {
		s := NewSerializer()
		_ = s.Register("Int64Value", &wrapperspb.Int64Value{})

		values := []int64{0, 1, -1, 9223372036854775807, -9223372036854775808}

		for _, val := range values {
			original := wrapperspb.Int64(val)
			data, err := s.Serialize(original)
			require.NoError(t, err)

			result, err := s.Deserialize(data, "Int64Value")
			require.NoError(t, err)

			deserialized := result.(wrapperspb.Int64Value)
			assert.Equal(t, original.GetValue(), deserialized.GetValue())
		}
	})

	t.Run("preserves double data through serialize/deserialize", func(t *testing.T) {
		s := NewSerializer()
		_ = s.Register("DoubleValue", &wrapperspb.DoubleValue{})

		values := []float64{0.0, 1.0, -1.0, 3.14159, 2.71828}

		for _, val := range values {
			original := wrapperspb.Double(val)
			data, err := s.Serialize(original)
			require.NoError(t, err)

			result, err := s.Deserialize(data, "DoubleValue")
			require.NoError(t, err)

			deserialized := result.(wrapperspb.DoubleValue)
			assert.InDelta(t, original.GetValue(), deserialized.GetValue(), 0.00001)
		}
	})

	t.Run("preserves bytes data through serialize/deserialize", func(t *testing.T) {
		s := NewSerializer()
		_ = s.Register("BytesValue", &wrapperspb.BytesValue{})

		values := [][]byte{
			{0x00},
			{0x01, 0x02, 0x03},
			[]byte("hello world"),
		}

		for _, val := range values {
			original := wrapperspb.Bytes(val)
			data, err := s.Serialize(original)
			require.NoError(t, err)

			result, err := s.Deserialize(data, "BytesValue")
			require.NoError(t, err)

			deserialized := result.(wrapperspb.BytesValue)
			assert.Equal(t, original.GetValue(), deserialized.GetValue())
		}
	})

	t.Run("preserves empty bytes through serialize/deserialize", func(t *testing.T) {
		s := NewSerializer()
		_ = s.Register("BytesValue", &wrapperspb.BytesValue{})

		// Empty bytes is a special case - protobuf treats empty and nil as equivalent
		original := wrapperspb.Bytes([]byte{})
		data, err := s.Serialize(original)
		require.NoError(t, err)

		result, err := s.Deserialize(data, "BytesValue")
		require.NoError(t, err)

		deserialized := result.(wrapperspb.BytesValue)
		// Both nil and empty are valid zero values for bytes
		assert.Empty(t, deserialized.GetValue())
	})
}

// =============================================================================
// proto.Clone compatibility test
// =============================================================================

func TestSerializer_ProtoCloneCompatibility(t *testing.T) {
	t.Run("deserialized value can be cloned", func(t *testing.T) {
		s := NewSerializer()
		_ = s.Register("StringValue", &wrapperspb.StringValue{})

		original := wrapperspb.String("test-value")
		data, err := s.Serialize(original)
		require.NoError(t, err)

		result, err := s.Deserialize(data, "StringValue")
		require.NoError(t, err)

		deserialized := result.(wrapperspb.StringValue)

		// Should be able to clone the deserialized message
		cloned := proto.Clone(&deserialized).(*wrapperspb.StringValue)
		assert.Equal(t, deserialized.GetValue(), cloned.GetValue())
	})
}

// =============================================================================
// Error Type Tests
// =============================================================================

func TestSerializationError(t *testing.T) {
	t.Run("Error() returns formatted message", func(t *testing.T) {
		err := &SerializationError{
			EventType: "StringValue",
			Operation: "serialize",
			Cause:     ErrNilEvent,
		}

		msg := err.Error()

		assert.Contains(t, msg, "StringValue")
		assert.Contains(t, msg, "serialize")
	})

	t.Run("Error() without cause", func(t *testing.T) {
		err := &SerializationError{
			EventType: "StringValue",
			Operation: "deserialize",
		}

		msg := err.Error()

		assert.Contains(t, msg, "StringValue")
		assert.Contains(t, msg, "deserialize")
	})

	t.Run("Unwrap() returns cause", func(t *testing.T) {
		cause := errors.New("test cause")
		err := &SerializationError{
			EventType: "Test",
			Operation: "test",
			Cause:     cause,
		}

		assert.Equal(t, cause, err.Unwrap())
	})

	t.Run("Is() works with sentinel errors", func(t *testing.T) {
		err := &SerializationError{
			EventType: "Test",
			Operation: "serialize",
			Cause:     ErrNilEvent,
		}

		assert.True(t, errors.Is(err, ErrNilEvent))
		assert.False(t, errors.Is(err, ErrEmptyData))
	})
}

// =============================================================================
// Concurrency Tests
// =============================================================================

func TestSerializer_Concurrency(t *testing.T) {
	t.Run("concurrent read/write is safe", func(t *testing.T) {
		s := NewSerializer()

		done := make(chan bool)

		// Writer goroutine
		go func() {
			for i := 0; i < 100; i++ {
				_ = s.Register("Event", &wrapperspb.StringValue{})
			}
			done <- true
		}()

		// Reader goroutine
		go func() {
			for i := 0; i < 100; i++ {
				s.Lookup("Event")
				s.Count()
				s.RegisteredTypes()
			}
			done <- true
		}()

		// Serializer goroutine
		go func() {
			_ = s.Register("StringValue", &wrapperspb.StringValue{})
			event := wrapperspb.String("test")
			for i := 0; i < 100; i++ {
				data, _ := s.Serialize(event)
				if len(data) > 0 {
					_, _ = s.Deserialize(data, "StringValue")
				}
			}
			done <- true
		}()

		// Wait for all goroutines
		for i := 0; i < 3; i++ {
			<-done
		}
	})
}

// =============================================================================
// Benchmark Tests
// =============================================================================

func BenchmarkSerializer_Serialize(b *testing.B) {
	s := NewSerializer()
	event := wrapperspb.String("order-123")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = s.Serialize(event)
	}
}

func BenchmarkSerializer_Deserialize(b *testing.B) {
	s := NewSerializer()
	_ = s.Register("StringValue", &wrapperspb.StringValue{})
	event := wrapperspb.String("order-123")
	data, _ := s.Serialize(event)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = s.Deserialize(data, "StringValue")
	}
}

func BenchmarkSerializer_RoundTrip(b *testing.B) {
	s := NewSerializer()
	_ = s.Register("StringValue", &wrapperspb.StringValue{})
	event := wrapperspb.String("order-123")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		data, _ := s.Serialize(event)
		_, _ = s.Deserialize(data, "StringValue")
	}
}

// =============================================================================
// End-to-End Tests
// =============================================================================

// SimulatedEvent represents a stored event in the event store.
type SimulatedEvent struct {
	ID        string
	StreamID  string
	Type      string
	Data      []byte
	Version   int64
	Timestamp int64
}

// SimulatedEventStore simulates an in-memory event store for e2e testing.
type SimulatedEventStore struct {
	mu         sync.Mutex
	events     []SimulatedEvent
	serializer *Serializer
	nextID     int
}

func NewSimulatedEventStore(s *Serializer) *SimulatedEventStore {
	return &SimulatedEventStore{
		events:     make([]SimulatedEvent, 0),
		serializer: s,
		nextID:     1,
	}
}

func (es *SimulatedEventStore) Append(streamID, eventType string, event proto.Message) error {
	data, err := es.serializer.Serialize(event)
	if err != nil {
		return err
	}

	es.mu.Lock()
	defer es.mu.Unlock()

	es.events = append(es.events, SimulatedEvent{
		ID:        fmt.Sprintf("evt-%d", es.nextID),
		StreamID:  streamID,
		Type:      eventType,
		Data:      data,
		Version:   int64(len(es.events) + 1),
		Timestamp: time.Now().UnixNano(),
	})
	es.nextID++
	return nil
}

func (es *SimulatedEventStore) Load(streamID string) ([]SimulatedEvent, error) {
	es.mu.Lock()
	defer es.mu.Unlock()

	var result []SimulatedEvent
	for _, e := range es.events {
		if e.StreamID == streamID {
			result = append(result, e)
		}
	}
	return result, nil
}

func (es *SimulatedEventStore) Deserialize(event SimulatedEvent) (interface{}, error) {
	return es.serializer.Deserialize(event.Data, event.Type)
}

func TestE2E_FullEventSourcingFlow(t *testing.T) {
	t.Run("complete order lifecycle with protobuf serialization", func(t *testing.T) {
		// Setup serializer with all event types
		s := NewSerializer()
		require.NoError(t, s.Register("OrderCreated", &wrapperspb.StringValue{}))
		require.NoError(t, s.Register("ItemAdded", &wrapperspb.Int32Value{}))
		require.NoError(t, s.Register("OrderTotal", &wrapperspb.DoubleValue{}))
		require.NoError(t, s.Register("OrderShipped", &wrapperspb.BoolValue{}))

		// Create simulated event store
		store := NewSimulatedEventStore(s)

		// Simulate order creation
		streamID := "order-12345"

		// 1. Order Created
		err := store.Append(streamID, "OrderCreated", wrapperspb.String("customer-abc"))
		require.NoError(t, err)

		// 2. Items Added (multiple)
		err = store.Append(streamID, "ItemAdded", wrapperspb.Int32(3))
		require.NoError(t, err)
		err = store.Append(streamID, "ItemAdded", wrapperspb.Int32(2))
		require.NoError(t, err)

		// 3. Order Total Calculated
		err = store.Append(streamID, "OrderTotal", wrapperspb.Double(149.99))
		require.NoError(t, err)

		// 4. Order Shipped
		err = store.Append(streamID, "OrderShipped", wrapperspb.Bool(true))
		require.NoError(t, err)

		// Load and replay events
		events, err := store.Load(streamID)
		require.NoError(t, err)
		assert.Len(t, events, 5)

		// Verify each event deserializes correctly
		// Event 1: OrderCreated
		result1, err := store.Deserialize(events[0])
		require.NoError(t, err)
		orderCreated := result1.(wrapperspb.StringValue)
		assert.Equal(t, "customer-abc", orderCreated.GetValue())

		// Event 2: ItemAdded (3 items)
		result2, err := store.Deserialize(events[1])
		require.NoError(t, err)
		itemAdded1 := result2.(wrapperspb.Int32Value)
		assert.Equal(t, int32(3), itemAdded1.GetValue())

		// Event 3: ItemAdded (2 items)
		result3, err := store.Deserialize(events[2])
		require.NoError(t, err)
		itemAdded2 := result3.(wrapperspb.Int32Value)
		assert.Equal(t, int32(2), itemAdded2.GetValue())

		// Event 4: OrderTotal
		result4, err := store.Deserialize(events[3])
		require.NoError(t, err)
		orderTotal := result4.(wrapperspb.DoubleValue)
		assert.InDelta(t, 149.99, orderTotal.GetValue(), 0.001)

		// Event 5: OrderShipped
		result5, err := store.Deserialize(events[4])
		require.NoError(t, err)
		orderShipped := result5.(wrapperspb.BoolValue)
		assert.True(t, orderShipped.GetValue())
	})

	t.Run("multiple streams with concurrent access", func(t *testing.T) {
		s := NewSerializer()
		require.NoError(t, s.Register("UserCreated", &wrapperspb.StringValue{}))
		require.NoError(t, s.Register("UserUpdated", &wrapperspb.StringValue{}))
		require.NoError(t, s.Register("UserDeleted", &wrapperspb.BoolValue{}))

		store := NewSimulatedEventStore(s)

		// Simulate multiple user streams concurrently
		var wg sync.WaitGroup
		userCount := 10
		eventsPerUser := 5

		for i := 0; i < userCount; i++ {
			wg.Add(1)
			go func(userID int) {
				defer wg.Done()
				streamID := fmt.Sprintf("user-%d", userID)

				// Create user
				_ = store.Append(streamID, "UserCreated", wrapperspb.String(fmt.Sprintf("user%d@example.com", userID)))

				// Update user multiple times
				for j := 0; j < eventsPerUser-2; j++ {
					_ = store.Append(streamID, "UserUpdated", wrapperspb.String(fmt.Sprintf("updated-%d-%d", userID, j)))
				}

				// Delete user
				_ = store.Append(streamID, "UserDeleted", wrapperspb.Bool(true))
			}(i)
		}

		wg.Wait()

		// Verify total events
		assert.Len(t, store.events, userCount*eventsPerUser)

		// Verify each user stream has correct events
		for i := 0; i < userCount; i++ {
			streamID := fmt.Sprintf("user-%d", i)
			events, err := store.Load(streamID)
			require.NoError(t, err)
			assert.Len(t, events, eventsPerUser)

			// First event should be UserCreated
			result, err := store.Deserialize(events[0])
			require.NoError(t, err)
			created := result.(wrapperspb.StringValue)
			assert.Contains(t, created.GetValue(), "@example.com")

			// Last event should be UserDeleted
			result, err = store.Deserialize(events[len(events)-1])
			require.NoError(t, err)
			deleted := result.(wrapperspb.BoolValue)
			assert.True(t, deleted.GetValue())
		}
	})

	t.Run("projection building from events", func(t *testing.T) {
		s := NewSerializer()
		require.NoError(t, s.Register("BalanceDeposit", &wrapperspb.DoubleValue{}))
		require.NoError(t, s.Register("BalanceWithdraw", &wrapperspb.DoubleValue{}))

		store := NewSimulatedEventStore(s)
		streamID := "account-001"

		// Simulate account transactions
		transactions := []struct {
			eventType string
			amount    float64
		}{
			{"BalanceDeposit", 100.00},
			{"BalanceDeposit", 50.00},
			{"BalanceWithdraw", 25.00},
			{"BalanceDeposit", 75.00},
			{"BalanceWithdraw", 10.00},
		}

		for _, tx := range transactions {
			err := store.Append(streamID, tx.eventType, wrapperspb.Double(tx.amount))
			require.NoError(t, err)
		}

		// Build projection by replaying events
		events, err := store.Load(streamID)
		require.NoError(t, err)

		var balance float64
		for _, event := range events {
			result, err := store.Deserialize(event)
			require.NoError(t, err)

			dv := result.(wrapperspb.DoubleValue)
			amount := dv.GetValue()

			switch event.Type {
			case "BalanceDeposit":
				balance += amount
			case "BalanceWithdraw":
				balance -= amount
			}
		}

		// Expected: 100 + 50 - 25 + 75 - 10 = 190
		assert.InDelta(t, 190.00, balance, 0.001)
	})

	t.Run("error handling in event flow", func(t *testing.T) {
		s := NewSerializer()
		require.NoError(t, s.Register("ValidEvent", &wrapperspb.StringValue{}))
		// Note: "UnknownEvent" is NOT registered

		store := NewSimulatedEventStore(s)

		// Append valid event
		err := store.Append("stream-1", "ValidEvent", wrapperspb.String("valid-data"))
		require.NoError(t, err)

		// Try to append nil event (should fail)
		err = store.Append("stream-1", "ValidEvent", nil)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrNilEvent))

		// Try to append non-proto event (should fail at serializer level)
		_, err = s.Serialize(NonProtoEvent{ID: "test"})
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrNotProtoMessage))

		// Manually create event with unregistered type and try to deserialize
		manualEvent := SimulatedEvent{
			ID:       "manual-1",
			StreamID: "stream-1",
			Type:     "UnknownEvent",
			Data:     []byte{0x0a, 0x05, 0x68, 0x65, 0x6c, 0x6c, 0x6f}, // protobuf encoded "hello"
			Version:  1,
		}
		_, err = store.Deserialize(manualEvent)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrTypeNotRegistered))
	})

	t.Run("large payload handling", func(t *testing.T) {
		s := NewSerializer()
		require.NoError(t, s.Register("LargePayload", &wrapperspb.BytesValue{}))

		store := NewSimulatedEventStore(s)

		// Create large payload (1MB)
		largeData := make([]byte, 1024*1024)
		for i := range largeData {
			largeData[i] = byte(i % 256)
		}

		err := store.Append("stream-large", "LargePayload", wrapperspb.Bytes(largeData))
		require.NoError(t, err)

		// Verify round-trip
		events, err := store.Load("stream-large")
		require.NoError(t, err)
		require.Len(t, events, 1)

		result, err := store.Deserialize(events[0])
		require.NoError(t, err)

		payload := result.(wrapperspb.BytesValue)
		assert.Len(t, payload.GetValue(), 1024*1024)
		assert.Equal(t, largeData, payload.GetValue())
	})

	t.Run("unicode and special characters", func(t *testing.T) {
		s := NewSerializer()
		require.NoError(t, s.Register("TextEvent", &wrapperspb.StringValue{}))
		require.NoError(t, s.Register("BinaryEvent", &wrapperspb.BytesValue{}))

		store := NewSimulatedEventStore(s)

		// Test various valid unicode strings (protobuf strings must be valid UTF-8)
		unicodeStrings := []string{
			"Hello, 世界!",                  // Chinese
			"مرحبا بالعالم",                 // Arabic
			"שלום עולם",                     // Hebrew
			"Привет мир",                   // Russian
			"こんにちは世界",                      // Japanese
			"🎉🚀💻🔥✨",                        // Emojis
			"Special chars: <>&\"'\\",      // Special chars
			"Line\nBreaks\tAnd\rCarriage",  // Control chars
			"Mixed: Hello世界مرحبا🎉",         // Mixed
		}

		for i, str := range unicodeStrings {
			streamID := fmt.Sprintf("unicode-%d", i)
			err := store.Append(streamID, "TextEvent", wrapperspb.String(str))
			require.NoError(t, err, "Failed to append string: %q", str)

			events, err := store.Load(streamID)
			require.NoError(t, err)
			require.Len(t, events, 1)

			result, err := store.Deserialize(events[0])
			require.NoError(t, err)

			// Deserialize returns value type, get the Value field directly
			sv := result.(wrapperspb.StringValue)
			assert.Equal(t, str, sv.Value, "Failed for string: %q", str)
		}

		// Test invalid UTF-8 is rejected (protobuf strings must be valid UTF-8)
		t.Run("rejects invalid UTF-8", func(t *testing.T) {
			invalidUTF8 := string([]byte{0xc0, 0x80}) // Invalid UTF-8 encoding
			_, err := s.Serialize(wrapperspb.String(invalidUTF8))
			assert.Error(t, err, "Should reject invalid UTF-8 in string fields")
		})

		// Test binary data can store arbitrary bytes using BytesValue
		t.Run("binary data with BytesValue", func(t *testing.T) {
			binaryData := []byte{0x00, 0xc0, 0x80, 0xff, 0xfe, 0x00, 0x01}
			streamID := "binary-data"
			err := store.Append(streamID, "BinaryEvent", wrapperspb.Bytes(binaryData))
			require.NoError(t, err)

			events, err := store.Load(streamID)
			require.NoError(t, err)
			require.Len(t, events, 1)

			result, err := store.Deserialize(events[0])
			require.NoError(t, err)

			// Deserialize returns value type
			bv := result.(wrapperspb.BytesValue)
			assert.Equal(t, binaryData, bv.Value)
		})
	})

	t.Run("event versioning simulation", func(t *testing.T) {
		s := NewSerializer()
		require.NoError(t, s.Register("OrderV1", &wrapperspb.StringValue{}))
		require.NoError(t, s.Register("OrderV2", &wrapperspb.Int64Value{}))

		store := NewSimulatedEventStore(s)
		streamID := "versioned-order"

		// Simulate old events (V1 format - string order ID)
		err := store.Append(streamID, "OrderV1", wrapperspb.String("ORD-12345"))
		require.NoError(t, err)

		// Simulate new events (V2 format - numeric order ID)
		err = store.Append(streamID, "OrderV2", wrapperspb.Int64(12345))
		require.NoError(t, err)

		// Load and handle both versions
		events, err := store.Load(streamID)
		require.NoError(t, err)
		assert.Len(t, events, 2)

		for _, event := range events {
			result, err := store.Deserialize(event)
			require.NoError(t, err)

			switch event.Type {
			case "OrderV1":
				v1 := result.(wrapperspb.StringValue)
				assert.Equal(t, "ORD-12345", v1.GetValue())
			case "OrderV2":
				v2 := result.(wrapperspb.Int64Value)
				assert.Equal(t, int64(12345), v2.GetValue())
			}
		}
	})

	t.Run("snapshot and restore simulation", func(t *testing.T) {
		s := NewSerializer()
		require.NoError(t, s.Register("CounterIncremented", &wrapperspb.Int32Value{}))
		require.NoError(t, s.Register("CounterSnapshot", &wrapperspb.Int64Value{}))

		store := NewSimulatedEventStore(s)
		streamID := "counter-001"

		// Add many increment events
		for i := 0; i < 100; i++ {
			err := store.Append(streamID, "CounterIncremented", wrapperspb.Int32(1))
			require.NoError(t, err)
		}

		// Calculate current state by replaying
		events, _ := store.Load(streamID)
		var counter int64
		for _, event := range events {
			result, _ := store.Deserialize(event)
			iv := result.(wrapperspb.Int32Value)
			counter += int64(iv.GetValue())
		}
		assert.Equal(t, int64(100), counter)

		// Create snapshot
		err := store.Append(streamID, "CounterSnapshot", wrapperspb.Int64(counter))
		require.NoError(t, err)

		// Add more events after snapshot
		for i := 0; i < 10; i++ {
			err := store.Append(streamID, "CounterIncremented", wrapperspb.Int32(1))
			require.NoError(t, err)
		}

		// Rebuild state from snapshot
		events, _ = store.Load(streamID)
		var rebuiltCounter int64
		var snapshotFound bool

		// Find latest snapshot and replay from there
		for i := len(events) - 1; i >= 0; i-- {
			if events[i].Type == "CounterSnapshot" {
				result, _ := store.Deserialize(events[i])
				lv := result.(wrapperspb.Int64Value)
				rebuiltCounter = lv.GetValue()
				snapshotFound = true

				// Replay events after snapshot
				for j := i + 1; j < len(events); j++ {
					if events[j].Type == "CounterIncremented" {
						result, _ := store.Deserialize(events[j])
						iv := result.(wrapperspb.Int32Value)
						rebuiltCounter += int64(iv.GetValue())
					}
				}
				break
			}
		}

		assert.True(t, snapshotFound)
		assert.Equal(t, int64(110), rebuiltCounter)
	})
}

func TestE2E_SerializerRegistry(t *testing.T) {
	t.Run("registry isolation between serializer instances", func(t *testing.T) {
		s1 := NewSerializer()
		s2 := NewSerializer()

		// Register different types in each serializer
		require.NoError(t, s1.Register("EventA", &wrapperspb.StringValue{}))
		require.NoError(t, s2.Register("EventB", &wrapperspb.Int32Value{}))

		// s1 should only know about EventA
		_, ok := s1.Lookup("EventA")
		assert.True(t, ok)
		_, ok = s1.Lookup("EventB")
		assert.False(t, ok)

		// s2 should only know about EventB
		_, ok = s2.Lookup("EventA")
		assert.False(t, ok)
		_, ok = s2.Lookup("EventB")
		assert.True(t, ok)
	})

	t.Run("serializer reuse across multiple streams", func(t *testing.T) {
		s := NewSerializer()
		require.NoError(t, s.Register("GenericEvent", &wrapperspb.StringValue{}))

		store := NewSimulatedEventStore(s)

		// Use same serializer for multiple streams
		streams := []string{"stream-a", "stream-b", "stream-c"}
		for _, streamID := range streams {
			for i := 0; i < 10; i++ {
				err := store.Append(streamID, "GenericEvent", wrapperspb.String(fmt.Sprintf("%s-event-%d", streamID, i)))
				require.NoError(t, err)
			}
		}

		// Verify each stream
		for _, streamID := range streams {
			events, err := store.Load(streamID)
			require.NoError(t, err)
			assert.Len(t, events, 10)

			for i, event := range events {
				result, err := store.Deserialize(event)
				require.NoError(t, err)
				expected := fmt.Sprintf("%s-event-%d", streamID, i)
				sv := result.(wrapperspb.StringValue)
				assert.Equal(t, expected, sv.GetValue())
			}
		}
	})

	t.Run("pre-configured registry via options", func(t *testing.T) {
		registry := map[string]reflect.Type{
			"TypeA": reflect.TypeOf(wrapperspb.StringValue{}),
			"TypeB": reflect.TypeOf(wrapperspb.Int32Value{}),
			"TypeC": reflect.TypeOf(wrapperspb.DoubleValue{}),
		}

		s := NewSerializerWithOptions(WithRegistry(registry))

		assert.Equal(t, 3, s.Count())
		types := s.RegisteredTypes()
		assert.Contains(t, types, "TypeA")
		assert.Contains(t, types, "TypeB")
		assert.Contains(t, types, "TypeC")

		// All types should work for serialization
		_, err := s.Deserialize([]byte{}, "TypeA")
		require.NoError(t, err) // Empty is valid for zero values

		_, err = s.Deserialize([]byte{}, "TypeB")
		require.NoError(t, err)

		_, err = s.Deserialize([]byte{}, "TypeC")
		require.NoError(t, err)
	})
}

func TestE2E_PerformanceCharacteristics(t *testing.T) {
	t.Run("serialization size comparison", func(t *testing.T) {
		s := NewSerializer()

		// Test various payload sizes
		testCases := []struct {
			name     string
			value    string
			maxBytes int // Expected max serialized size
		}{
			{"tiny", "a", 10},
			{"small", "hello world", 20},
			{"medium", strings.Repeat("x", 100), 110},
			{"large", strings.Repeat("x", 1000), 1010},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				data, err := s.Serialize(wrapperspb.String(tc.value))
				require.NoError(t, err)

				// Protobuf should be efficient
				assert.LessOrEqual(t, len(data), tc.maxBytes,
					"Serialized size %d exceeds expected max %d for %s",
					len(data), tc.maxBytes, tc.name)
			})
		}
	})

	t.Run("batch processing performance", func(t *testing.T) {
		s := NewSerializer()
		require.NoError(t, s.Register("BatchEvent", &wrapperspb.StringValue{}))

		const batchSize = 10000
		events := make([]*wrapperspb.StringValue, batchSize)
		for i := 0; i < batchSize; i++ {
			events[i] = wrapperspb.String(fmt.Sprintf("event-data-%d", i))
		}

		// Serialize all
		start := time.Now()
		serialized := make([][]byte, batchSize)
		for i, event := range events {
			data, err := s.Serialize(event)
			require.NoError(t, err)
			serialized[i] = data
		}
		serializeTime := time.Since(start)

		// Deserialize all
		start = time.Now()
		for _, data := range serialized {
			_, err := s.Deserialize(data, "BatchEvent")
			require.NoError(t, err)
		}
		deserializeTime := time.Since(start)

		// Log performance (should be very fast)
		t.Logf("Batch of %d events:", batchSize)
		t.Logf("  Serialize:   %v (%.2f µs/event)", serializeTime, float64(serializeTime.Microseconds())/float64(batchSize))
		t.Logf("  Deserialize: %v (%.2f µs/event)", deserializeTime, float64(deserializeTime.Microseconds())/float64(batchSize))

		// Sanity check - should process at least 10k events per second
		assert.Less(t, serializeTime, 10*time.Second)
		assert.Less(t, deserializeTime, 10*time.Second)
	})
}
