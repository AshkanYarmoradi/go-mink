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
	serializeTests := []struct {
		name  string
		event proto.Message
	}{
		{"proto.Message event", wrapperspb.String("order-123")},
		{"int value", wrapperspb.Int32(42)},
		{"double value", wrapperspb.Double(3.14159)},
		{"bool value", wrapperspb.Bool(true)},
	}

	for _, tc := range serializeTests {
		t.Run("serializes "+tc.name, func(t *testing.T) {
			s := NewSerializer()
			data, err := s.Serialize(tc.event)
			require.NoError(t, err)
			assert.NotEmpty(t, data)
		})
	}

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

// deserializeTestCase defines a test case for deserialize operations
type deserializeTestCase struct {
	typeName string
	original proto.Message
	getValue func(interface{}) interface{}
}

func TestSerializer_Deserialize(t *testing.T) {
	basicDeserializeTests := []deserializeTestCase{
		{"StringValue", wrapperspb.String("order-123"), func(v interface{}) interface{} {
			switch typed := v.(type) {
			case wrapperspb.StringValue:
				return typed.GetValue()
			case *wrapperspb.StringValue:
				return typed.GetValue()
			}
			return nil
		}},
		{"Int32Value", wrapperspb.Int32(42), func(v interface{}) interface{} {
			switch typed := v.(type) {
			case wrapperspb.Int32Value:
				return typed.GetValue()
			case *wrapperspb.Int32Value:
				return typed.GetValue()
			}
			return nil
		}},
		{"BoolValue", wrapperspb.Bool(true), func(v interface{}) interface{} {
			switch typed := v.(type) {
			case wrapperspb.BoolValue:
				return typed.GetValue()
			case *wrapperspb.BoolValue:
				return typed.GetValue()
			}
			return nil
		}},
	}

	for _, tc := range basicDeserializeTests {
		t.Run("deserializes to registered type "+tc.typeName, func(t *testing.T) {
			s := NewSerializer()
			require.NoError(t, s.Register(tc.typeName, tc.original))

			data, err := s.Serialize(tc.original)
			require.NoError(t, err)

			result, err := s.Deserialize(data, tc.typeName)
			require.NoError(t, err)
			assert.Equal(t, tc.getValue(tc.original), tc.getValue(result))
		})
	}

	t.Run("deserializes double value with delta comparison", func(t *testing.T) {
		s := NewSerializer()
		require.NoError(t, s.Register("DoubleValue", &wrapperspb.DoubleValue{}))

		original := wrapperspb.Double(3.14159)
		data, err := s.Serialize(original)
		require.NoError(t, err)

		result, err := s.Deserialize(data, "DoubleValue")
		require.NoError(t, err)
		assert.InDelta(t, original.GetValue(), extractDoubleValue(result), 0.00001)
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
		// Empty protobuf message deserializes to zero value
		assert.Equal(t, "", extractStringValue(result))
	})

	t.Run("returns error for nil data", func(t *testing.T) {
		s := NewSerializer()
		_ = s.Register("StringValue", &wrapperspb.StringValue{})

		_, err := s.Deserialize(nil, "StringValue")

		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrEmptyData))
	})
}

// =============================================================================
// Round-trip Tests
// =============================================================================

// Value extractors that handle both pointer and value types from Deserialize
func extractStringValue(v interface{}) string {
	switch typed := v.(type) {
	case *wrapperspb.StringValue:
		return typed.GetValue()
	case wrapperspb.StringValue:
		return typed.GetValue()
	}
	return ""
}

func extractInt32Value(v interface{}) int32 {
	switch typed := v.(type) {
	case *wrapperspb.Int32Value:
		return typed.GetValue()
	case wrapperspb.Int32Value:
		return typed.GetValue()
	}
	return 0
}

func extractInt64Value(v interface{}) int64 {
	switch typed := v.(type) {
	case *wrapperspb.Int64Value:
		return typed.GetValue()
	case wrapperspb.Int64Value:
		return typed.GetValue()
	}
	return 0
}

func extractDoubleValue(v interface{}) float64 {
	switch typed := v.(type) {
	case *wrapperspb.DoubleValue:
		return typed.GetValue()
	case wrapperspb.DoubleValue:
		return typed.GetValue()
	}
	return 0
}

func extractBytesValue(v interface{}) []byte {
	switch typed := v.(type) {
	case *wrapperspb.BytesValue:
		return typed.GetValue()
	case wrapperspb.BytesValue:
		return typed.GetValue()
	}
	return nil
}

// roundTripTest performs a serialize/deserialize round trip and verifies the result
func roundTripTest[T any](t *testing.T, s *Serializer, typeName string, original proto.Message, getValue func(interface{}) T, compare func(*testing.T, T, T)) {
	t.Helper()
	data, err := s.Serialize(original)
	require.NoError(t, err)

	result, err := s.Deserialize(data, typeName)
	require.NoError(t, err)

	compare(t, getValue(original), getValue(result))
}

func TestSerializer_RoundTrip(t *testing.T) {
	t.Run("preserves string data through serialize/deserialize", func(t *testing.T) {
		s := setupSerializer(t, "StringValue")
		values := []string{"order-1", "order-2", "", "unicode: 你好世界"}

		for _, val := range values {
			roundTripTest(t, s, "StringValue", wrapperspb.String(val), extractStringValue,
				func(t *testing.T, expected, actual string) { assert.Equal(t, expected, actual) })
		}
	})

	t.Run("preserves int data through serialize/deserialize", func(t *testing.T) {
		s := setupSerializer(t, "Int32Value")
		values := []int32{0, 1, -1, 42, -42, 2147483647, -2147483648}

		for _, val := range values {
			roundTripTest(t, s, "Int32Value", wrapperspb.Int32(val), extractInt32Value,
				func(t *testing.T, expected, actual int32) { assert.Equal(t, expected, actual) })
		}
	})

	t.Run("preserves int64 data through serialize/deserialize", func(t *testing.T) {
		s := setupSerializer(t, "Int64Value")
		values := []int64{0, 1, -1, 9223372036854775807, -9223372036854775808}

		for _, val := range values {
			roundTripTest(t, s, "Int64Value", wrapperspb.Int64(val), extractInt64Value,
				func(t *testing.T, expected, actual int64) { assert.Equal(t, expected, actual) })
		}
	})

	t.Run("preserves double data through serialize/deserialize", func(t *testing.T) {
		s := setupSerializer(t, "DoubleValue")
		values := []float64{0.0, 1.0, -1.0, 3.14159, 2.71828}

		for _, val := range values {
			roundTripTest(t, s, "DoubleValue", wrapperspb.Double(val), extractDoubleValue,
				func(t *testing.T, expected, actual float64) { assert.InDelta(t, expected, actual, 0.00001) })
		}
	})

	t.Run("preserves bytes data through serialize/deserialize", func(t *testing.T) {
		s := setupSerializer(t, "BytesValue")
		values := [][]byte{{0x00}, {0x01, 0x02, 0x03}, []byte("hello world")}

		for _, val := range values {
			roundTripTest(t, s, "BytesValue", wrapperspb.Bytes(val), extractBytesValue,
				func(t *testing.T, expected, actual []byte) { assert.Equal(t, expected, actual) })
		}
	})

	t.Run("preserves empty bytes through serialize/deserialize", func(t *testing.T) {
		s := setupSerializer(t, "BytesValue")
		// Empty bytes is a special case - protobuf treats empty and nil as equivalent
		data, err := s.Serialize(wrapperspb.Bytes([]byte{}))
		require.NoError(t, err)

		result, err := s.Deserialize(data, "BytesValue")
		require.NoError(t, err)
		assert.Empty(t, extractBytesValue(result))
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

		// Get proto.Message for cloning - use reflect to get addressable pointer
		var msg proto.Message
		rv := reflect.ValueOf(result)
		if rv.Kind() == reflect.Ptr {
			msg = result.(proto.Message)
		} else {
			// Create a new pointer and set its value
			ptr := reflect.New(rv.Type())
			ptr.Elem().Set(rv)
			msg = ptr.Interface().(proto.Message)
		}

		// Should be able to clone the deserialized message
		cloned := proto.Clone(msg).(*wrapperspb.StringValue)
		assert.Equal(t, extractStringValue(result), cloned.GetValue())
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

// eventTypeRegistry defines standard event type registrations for tests
var eventTypeRegistry = map[string]proto.Message{
	"StringValue":         &wrapperspb.StringValue{},
	"Int32Value":          &wrapperspb.Int32Value{},
	"Int64Value":          &wrapperspb.Int64Value{},
	"DoubleValue":         &wrapperspb.DoubleValue{},
	"BoolValue":           &wrapperspb.BoolValue{},
	"BytesValue":          &wrapperspb.BytesValue{},
	"OrderCreated":        &wrapperspb.StringValue{},
	"ItemAdded":           &wrapperspb.Int32Value{},
	"OrderTotal":          &wrapperspb.DoubleValue{},
	"OrderShipped":        &wrapperspb.BoolValue{},
	"UserCreated":         &wrapperspb.StringValue{},
	"UserUpdated":         &wrapperspb.StringValue{},
	"UserDeleted":         &wrapperspb.BoolValue{},
	"BalanceDeposit":      &wrapperspb.DoubleValue{},
	"BalanceWithdraw":     &wrapperspb.DoubleValue{},
	"ValidEvent":          &wrapperspb.StringValue{},
	"LargePayload":        &wrapperspb.BytesValue{},
	"TextEvent":           &wrapperspb.StringValue{},
	"BinaryEvent":         &wrapperspb.BytesValue{},
	"OrderV1":             &wrapperspb.StringValue{},
	"OrderV2":             &wrapperspb.Int64Value{},
	"CounterIncremented":  &wrapperspb.Int32Value{},
	"CounterSnapshot":     &wrapperspb.Int64Value{},
	"GenericEvent":        &wrapperspb.StringValue{},
	"BatchEvent":          &wrapperspb.StringValue{},
}

// setupSerializer creates a serializer and registers the specified event types
func setupSerializer(t *testing.T, eventTypes ...string) *Serializer {
	t.Helper()
	s := NewSerializer()
	for _, eventType := range eventTypes {
		proto, ok := eventTypeRegistry[eventType]
		require.True(t, ok, "Unknown event type: %s", eventType)
		require.NoError(t, s.Register(eventType, proto))
	}
	return s
}

// setupStore creates a serializer with event types and returns both the serializer and store
func setupStore(t *testing.T, eventTypes ...string) (*Serializer, *SimulatedEventStore) {
	t.Helper()
	s := setupSerializer(t, eventTypes...)
	return s, NewSimulatedEventStore(s)
}

// mustDeserializeAs deserializes an event and asserts the type, returning pointer for method access
func mustDeserializeAs[T any](t *testing.T, store *SimulatedEventStore, event SimulatedEvent) *T {
	t.Helper()
	result, err := store.Deserialize(event)
	require.NoError(t, err)
	typed, ok := result.(T)
	require.True(t, ok, "Expected type %T, got %T", new(T), result)
	return &typed
}

func TestE2E_FullEventSourcingFlow(t *testing.T) {
	t.Run("complete order lifecycle with protobuf serialization", func(t *testing.T) {
		_, store := setupStore(t, "OrderCreated", "ItemAdded", "OrderTotal", "OrderShipped")
		streamID := "order-12345"

		// Simulate order creation and lifecycle
		require.NoError(t, store.Append(streamID, "OrderCreated", wrapperspb.String("customer-abc")))
		require.NoError(t, store.Append(streamID, "ItemAdded", wrapperspb.Int32(3)))
		require.NoError(t, store.Append(streamID, "ItemAdded", wrapperspb.Int32(2)))
		require.NoError(t, store.Append(streamID, "OrderTotal", wrapperspb.Double(149.99)))
		require.NoError(t, store.Append(streamID, "OrderShipped", wrapperspb.Bool(true)))

		// Load and replay events
		events, err := store.Load(streamID)
		require.NoError(t, err)
		assert.Len(t, events, 5)

		// Verify each event deserializes correctly
		assert.Equal(t, "customer-abc", mustDeserializeAs[wrapperspb.StringValue](t, store, events[0]).GetValue())
		assert.Equal(t, int32(3), mustDeserializeAs[wrapperspb.Int32Value](t, store, events[1]).GetValue())
		assert.Equal(t, int32(2), mustDeserializeAs[wrapperspb.Int32Value](t, store, events[2]).GetValue())
		assert.InDelta(t, 149.99, mustDeserializeAs[wrapperspb.DoubleValue](t, store, events[3]).GetValue(), 0.001)
		assert.True(t, mustDeserializeAs[wrapperspb.BoolValue](t, store, events[4]).GetValue())
	})

	t.Run("multiple streams with concurrent access", func(t *testing.T) {
		_, store := setupStore(t, "UserCreated", "UserUpdated", "UserDeleted")

		// Simulate multiple user streams concurrently
		var wg sync.WaitGroup
		userCount, eventsPerUser := 10, 5

		for i := 0; i < userCount; i++ {
			wg.Add(1)
			go func(userID int) {
				defer wg.Done()
				streamID := fmt.Sprintf("user-%d", userID)
				_ = store.Append(streamID, "UserCreated", wrapperspb.String(fmt.Sprintf("user%d@example.com", userID)))
				for j := 0; j < eventsPerUser-2; j++ {
					_ = store.Append(streamID, "UserUpdated", wrapperspb.String(fmt.Sprintf("updated-%d-%d", userID, j)))
				}
				_ = store.Append(streamID, "UserDeleted", wrapperspb.Bool(true))
			}(i)
		}
		wg.Wait()

		// Verify total events and each user stream
		assert.Len(t, store.events, userCount*eventsPerUser)
		for i := 0; i < userCount; i++ {
			events, err := store.Load(fmt.Sprintf("user-%d", i))
			require.NoError(t, err)
			assert.Len(t, events, eventsPerUser)
			assert.Contains(t, mustDeserializeAs[wrapperspb.StringValue](t, store, events[0]).GetValue(), "@example.com")
			assert.True(t, mustDeserializeAs[wrapperspb.BoolValue](t, store, events[len(events)-1]).GetValue())
		}
	})

	t.Run("projection building from events", func(t *testing.T) {
		_, store := setupStore(t, "BalanceDeposit", "BalanceWithdraw")
		streamID := "account-001"

		// Simulate account transactions
		transactions := []struct {
			eventType string
			amount    float64
		}{
			{"BalanceDeposit", 100.00}, {"BalanceDeposit", 50.00}, {"BalanceWithdraw", 25.00},
			{"BalanceDeposit", 75.00}, {"BalanceWithdraw", 10.00},
		}
		for _, tx := range transactions {
			require.NoError(t, store.Append(streamID, tx.eventType, wrapperspb.Double(tx.amount)))
		}

		// Build projection by replaying events
		events, err := store.Load(streamID)
		require.NoError(t, err)

		var balance float64
		for _, event := range events {
			amount := mustDeserializeAs[wrapperspb.DoubleValue](t, store, event).GetValue()
			if event.Type == "BalanceDeposit" {
				balance += amount
			} else {
				balance -= amount
			}
		}
		assert.InDelta(t, 190.00, balance, 0.001) // 100 + 50 - 25 + 75 - 10 = 190
	})

	t.Run("error handling in event flow", func(t *testing.T) {
		s, store := setupStore(t, "ValidEvent")

		require.NoError(t, store.Append("stream-1", "ValidEvent", wrapperspb.String("valid-data")))
		require.True(t, errors.Is(store.Append("stream-1", "ValidEvent", nil), ErrNilEvent))

		_, err := s.Serialize(NonProtoEvent{ID: "test"})
		require.True(t, errors.Is(err, ErrNotProtoMessage))

		// Manually create event with unregistered type
		manualEvent := SimulatedEvent{Type: "UnknownEvent", Data: []byte{0x0a, 0x05, 0x68, 0x65, 0x6c, 0x6c, 0x6f}}
		_, err = store.Deserialize(manualEvent)
		require.True(t, errors.Is(err, ErrTypeNotRegistered))
	})

	t.Run("large payload handling", func(t *testing.T) {
		_, store := setupStore(t, "LargePayload")

		largeData := make([]byte, 1024*1024) // 1MB
		for i := range largeData {
			largeData[i] = byte(i % 256)
		}

		require.NoError(t, store.Append("stream-large", "LargePayload", wrapperspb.Bytes(largeData)))
		events, err := store.Load("stream-large")
		require.NoError(t, err)
		require.Len(t, events, 1)

		payload := mustDeserializeAs[wrapperspb.BytesValue](t, store, events[0])
		assert.Equal(t, largeData, payload.GetValue())
	})

	t.Run("unicode and special characters", func(t *testing.T) {
		s, store := setupStore(t, "TextEvent", "BinaryEvent")

		unicodeStrings := []string{
			"Hello, 世界!", "مرحبا بالعالم", "שלום עולם", "Привет мир", "こんにちは世界",
			"🎉🚀💻🔥✨", "Special chars: <>&\"'\\", "Line\nBreaks\tAnd\rCarriage", "Mixed: Hello世界مرحبا🎉",
		}

		for i, str := range unicodeStrings {
			streamID := fmt.Sprintf("unicode-%d", i)
			require.NoError(t, store.Append(streamID, "TextEvent", wrapperspb.String(str)))
			events, _ := store.Load(streamID)
			assert.Equal(t, str, mustDeserializeAs[wrapperspb.StringValue](t, store, events[0]).Value)
		}

		t.Run("rejects invalid UTF-8", func(t *testing.T) {
			_, err := s.Serialize(wrapperspb.String(string([]byte{0xc0, 0x80})))
			assert.Error(t, err)
		})

		t.Run("binary data with BytesValue", func(t *testing.T) {
			binaryData := []byte{0x00, 0xc0, 0x80, 0xff, 0xfe, 0x00, 0x01}
			require.NoError(t, store.Append("binary-data", "BinaryEvent", wrapperspb.Bytes(binaryData)))
			events, _ := store.Load("binary-data")
			assert.Equal(t, binaryData, mustDeserializeAs[wrapperspb.BytesValue](t, store, events[0]).Value)
		})
	})

	t.Run("event versioning simulation", func(t *testing.T) {
		_, store := setupStore(t, "OrderV1", "OrderV2")
		streamID := "versioned-order"

		require.NoError(t, store.Append(streamID, "OrderV1", wrapperspb.String("ORD-12345")))
		require.NoError(t, store.Append(streamID, "OrderV2", wrapperspb.Int64(12345)))

		events, _ := store.Load(streamID)
		assert.Len(t, events, 2)
		assert.Equal(t, "ORD-12345", mustDeserializeAs[wrapperspb.StringValue](t, store, events[0]).GetValue())
		assert.Equal(t, int64(12345), mustDeserializeAs[wrapperspb.Int64Value](t, store, events[1]).GetValue())
	})

	t.Run("snapshot and restore simulation", func(t *testing.T) {
		_, store := setupStore(t, "CounterIncremented", "CounterSnapshot")
		streamID := "counter-001"

		// Add increment events
		for i := 0; i < 100; i++ {
			require.NoError(t, store.Append(streamID, "CounterIncremented", wrapperspb.Int32(1)))
		}

		// Calculate and create snapshot
		events, _ := store.Load(streamID)
		var counter int64
		for _, event := range events {
			counter += int64(mustDeserializeAs[wrapperspb.Int32Value](t, store, event).GetValue())
		}
		assert.Equal(t, int64(100), counter)
		require.NoError(t, store.Append(streamID, "CounterSnapshot", wrapperspb.Int64(counter)))

		// Add more events after snapshot
		for i := 0; i < 10; i++ {
			require.NoError(t, store.Append(streamID, "CounterIncremented", wrapperspb.Int32(1)))
		}

		// Rebuild from snapshot
		events, _ = store.Load(streamID)
		var rebuiltCounter int64
		for i := len(events) - 1; i >= 0; i-- {
			if events[i].Type == "CounterSnapshot" {
				rebuiltCounter = mustDeserializeAs[wrapperspb.Int64Value](t, store, events[i]).GetValue()
				for j := i + 1; j < len(events); j++ {
					if events[j].Type == "CounterIncremented" {
						rebuiltCounter += int64(mustDeserializeAs[wrapperspb.Int32Value](t, store, events[j]).GetValue())
					}
				}
				break
			}
		}
		assert.Equal(t, int64(110), rebuiltCounter)
	})
}

func TestE2E_SerializerRegistry(t *testing.T) {
	t.Run("registry isolation between serializer instances", func(t *testing.T) {
		s1 := setupSerializer(t, "StringValue")
		s2 := NewSerializer()
		require.NoError(t, s2.Register("EventB", &wrapperspb.Int32Value{}))

		_, ok := s1.Lookup("StringValue")
		assert.True(t, ok)
		_, ok = s1.Lookup("EventB")
		assert.False(t, ok)

		_, ok = s2.Lookup("StringValue")
		assert.False(t, ok)
		_, ok = s2.Lookup("EventB")
		assert.True(t, ok)
	})

	t.Run("serializer reuse across multiple streams", func(t *testing.T) {
		_, store := setupStore(t, "GenericEvent")
		streams := []string{"stream-a", "stream-b", "stream-c"}

		for _, streamID := range streams {
			for i := 0; i < 10; i++ {
				require.NoError(t, store.Append(streamID, "GenericEvent", wrapperspb.String(fmt.Sprintf("%s-event-%d", streamID, i))))
			}
		}

		for _, streamID := range streams {
			events, _ := store.Load(streamID)
			assert.Len(t, events, 10)
			for i, event := range events {
				assert.Equal(t, fmt.Sprintf("%s-event-%d", streamID, i), mustDeserializeAs[wrapperspb.StringValue](t, store, event).GetValue())
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
		for _, typeName := range []string{"TypeA", "TypeB", "TypeC"} {
			_, err := s.Deserialize([]byte{}, typeName)
			require.NoError(t, err)
		}
	})
}

func TestE2E_PerformanceCharacteristics(t *testing.T) {
	t.Run("serialization size comparison", func(t *testing.T) {
		s := NewSerializer()
		testCases := []struct {
			name     string
			value    string
			maxBytes int
		}{
			{"tiny", "a", 10}, {"small", "hello world", 20},
			{"medium", strings.Repeat("x", 100), 110}, {"large", strings.Repeat("x", 1000), 1010},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				data, err := s.Serialize(wrapperspb.String(tc.value))
				require.NoError(t, err)
				assert.LessOrEqual(t, len(data), tc.maxBytes)
			})
		}
	})

	t.Run("batch processing performance", func(t *testing.T) {
		s := setupSerializer(t, "BatchEvent")
		const batchSize = 10000

		events := make([]*wrapperspb.StringValue, batchSize)
		for i := 0; i < batchSize; i++ {
			events[i] = wrapperspb.String(fmt.Sprintf("event-data-%d", i))
		}

		start := time.Now()
		serialized := make([][]byte, batchSize)
		for i, event := range events {
			serialized[i], _ = s.Serialize(event)
		}
		serializeTime := time.Since(start)

		start = time.Now()
		for _, data := range serialized {
			_, _ = s.Deserialize(data, "BatchEvent")
		}
		deserializeTime := time.Since(start)

		t.Logf("Batch of %d events:", batchSize)
		t.Logf("  Serialize:   %v (%.2f µs/event)", serializeTime, float64(serializeTime.Microseconds())/float64(batchSize))
		t.Logf("  Deserialize: %v (%.2f µs/event)", deserializeTime, float64(deserializeTime.Microseconds())/float64(batchSize))

		assert.Less(t, serializeTime, 10*time.Second)
		assert.Less(t, deserializeTime, 10*time.Second)
	})
}
