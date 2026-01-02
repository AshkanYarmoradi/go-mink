// Package msgpack provides a MessagePack serializer implementation for mink.
//
// MessagePack is a binary serialization format that produces smaller payloads
// than JSON while maintaining similar flexibility. It's particularly useful
// for high-throughput event sourcing applications.
//
// Basic usage:
//
//	serializer := msgpack.NewSerializer()
//	serializer.Register("OrderCreated", OrderCreated{})
//
//	data, err := serializer.Serialize(OrderCreated{OrderID: "123"})
//	event, err := serializer.Deserialize(data, "OrderCreated")
package msgpack

import (
	"fmt"
	"reflect"
	"sync"

	"github.com/vmihailenco/msgpack/v5"
)

// Serializer is a MessagePack implementation of mink.Serializer.
// It provides efficient binary serialization with type registry support.
type Serializer struct {
	mu       sync.RWMutex
	registry map[string]reflect.Type
}

// NewSerializer creates a new MessagePack Serializer with an empty registry.
func NewSerializer() *Serializer {
	return &Serializer{
		registry: make(map[string]reflect.Type),
	}
}

// SerializerOption configures a Serializer.
type SerializerOption func(*Serializer)

// WithRegistry sets the initial type registry.
func WithRegistry(registry map[string]reflect.Type) SerializerOption {
	return func(s *Serializer) {
		for k, v := range registry {
			s.registry[k] = v
		}
	}
}

// NewSerializerWithOptions creates a new Serializer with the given options.
func NewSerializerWithOptions(opts ...SerializerOption) *Serializer {
	s := NewSerializer()
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Register adds a mapping from eventType to the Go type of the example.
// The example should be a value (not a pointer) of the event type.
func (s *Serializer) Register(eventType string, example interface{}) {
	s.mu.Lock()
	defer s.mu.Unlock()

	t := reflect.TypeOf(example)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	s.registry[eventType] = t
}

// RegisterAll registers multiple events using their struct names as type names.
// Each example should be a value (not a pointer) of the event type.
func (s *Serializer) RegisterAll(examples ...interface{}) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, example := range examples {
		t := reflect.TypeOf(example)
		if t.Kind() == reflect.Ptr {
			t = t.Elem()
		}
		s.registry[t.Name()] = t
	}
}

// Lookup returns the Go type for the given event type name.
// Returns nil and false if the type is not registered.
func (s *Serializer) Lookup(eventType string) (reflect.Type, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	t, ok := s.registry[eventType]
	return t, ok
}

// RegisteredTypes returns a slice of all registered event type names.
func (s *Serializer) RegisteredTypes() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	types := make([]string, 0, len(s.registry))
	for t := range s.registry {
		types = append(types, t)
	}
	return types
}

// Count returns the number of registered event types.
func (s *Serializer) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.registry)
}

// Serialize converts an event to MessagePack bytes.
func (s *Serializer) Serialize(event interface{}) ([]byte, error) {
	if event == nil {
		return nil, &SerializationError{
			EventType: "nil",
			Operation: "serialize",
			Err:       fmt.Errorf("event cannot be nil"),
		}
	}

	data, err := msgpack.Marshal(event)
	if err != nil {
		eventType := reflect.TypeOf(event).Name()
		return nil, &SerializationError{
			EventType: eventType,
			Operation: "serialize",
			Err:       err,
		}
	}

	return data, nil
}

// Deserialize converts MessagePack bytes back to an event.
// If the event type is registered, returns a value of that type.
// Otherwise, returns a map[string]interface{}.
func (s *Serializer) Deserialize(data []byte, eventType string) (interface{}, error) {
	if len(data) == 0 {
		return nil, &SerializationError{
			EventType: eventType,
			Operation: "deserialize",
			Err:       fmt.Errorf("data cannot be empty"),
		}
	}

	// Try to find registered type
	t, ok := s.Lookup(eventType)
	if !ok {
		// Fall back to map if type not registered
		var result map[string]interface{}
		if err := msgpack.Unmarshal(data, &result); err != nil {
			return nil, &SerializationError{
				EventType: eventType,
				Operation: "deserialize",
				Err:       err,
			}
		}
		return result, nil
	}

	// Create new instance of registered type
	ptr := reflect.New(t)
	if err := msgpack.Unmarshal(data, ptr.Interface()); err != nil {
		return nil, &SerializationError{
			EventType: eventType,
			Operation: "deserialize",
			Err:       err,
		}
	}

	// Return the value (not pointer)
	return ptr.Elem().Interface(), nil
}

// SerializationError represents a serialization or deserialization error.
type SerializationError struct {
	EventType string
	Operation string // "serialize" or "deserialize"
	Err       error
}

// Error implements the error interface.
func (e *SerializationError) Error() string {
	return fmt.Sprintf("mink/msgpack: failed to %s event %s: %v", e.Operation, e.EventType, e.Err)
}

// Unwrap returns the underlying error.
func (e *SerializationError) Unwrap() error {
	return e.Err
}
