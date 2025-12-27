package mink

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
)

// Serializer handles event payload serialization and deserialization.
type Serializer interface {
	// Serialize converts an event to bytes.
	Serialize(event interface{}) ([]byte, error)

	// Deserialize converts bytes back to an event.
	// The eventType is used to determine the target type.
	Deserialize(data []byte, eventType string) (interface{}, error)
}

// EventRegistry maps event type names to Go types.
// It is used by the JSONSerializer to deserialize events to the correct type.
type EventRegistry struct {
	mu    sync.RWMutex
	types map[string]reflect.Type
}

// NewEventRegistry creates a new empty EventRegistry.
func NewEventRegistry() *EventRegistry {
	return &EventRegistry{
		types: make(map[string]reflect.Type),
	}
}

// Register adds a mapping from eventType to the Go type of the example.
// The example should be a value (not a pointer) of the event type.
func (r *EventRegistry) Register(eventType string, example interface{}) {
	r.mu.Lock()
	defer r.mu.Unlock()

	t := reflect.TypeOf(example)
	// If a pointer was passed, get the element type
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	r.types[eventType] = t
}

// RegisterAll registers multiple events using their struct names as type names.
// Each example should be a value (not a pointer) of the event type.
func (r *EventRegistry) RegisterAll(examples ...interface{}) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, example := range examples {
		t := reflect.TypeOf(example)
		if t.Kind() == reflect.Ptr {
			t = t.Elem()
		}
		r.types[t.Name()] = t
	}
}

// Lookup returns the Go type for the given event type name.
// Returns nil and false if the type is not registered.
func (r *EventRegistry) Lookup(eventType string) (reflect.Type, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	t, ok := r.types[eventType]
	return t, ok
}

// RegisteredTypes returns a slice of all registered event type names.
func (r *EventRegistry) RegisteredTypes() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	types := make([]string, 0, len(r.types))
	for t := range r.types {
		types = append(types, t)
	}
	return types
}

// Count returns the number of registered event types.
func (r *EventRegistry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.types)
}

// JSONSerializer is the default Serializer implementation using JSON encoding.
type JSONSerializer struct {
	registry *EventRegistry
}

// NewJSONSerializer creates a new JSONSerializer with an empty registry.
func NewJSONSerializer() *JSONSerializer {
	return &JSONSerializer{
		registry: NewEventRegistry(),
	}
}

// NewJSONSerializerWithRegistry creates a new JSONSerializer with the given registry.
func NewJSONSerializerWithRegistry(registry *EventRegistry) *JSONSerializer {
	if registry == nil {
		registry = NewEventRegistry()
	}
	return &JSONSerializer{
		registry: registry,
	}
}

// Register adds an event type to the serializer's registry.
func (s *JSONSerializer) Register(eventType string, example interface{}) {
	s.registry.Register(eventType, example)
}

// RegisterAll registers multiple events using their struct names as type names.
func (s *JSONSerializer) RegisterAll(examples ...interface{}) {
	s.registry.RegisterAll(examples...)
}

// Registry returns the underlying EventRegistry.
func (s *JSONSerializer) Registry() *EventRegistry {
	return s.registry
}

// Serialize converts an event to JSON bytes.
func (s *JSONSerializer) Serialize(event interface{}) ([]byte, error) {
	if event == nil {
		return nil, NewSerializationError("nil", "serialize", fmt.Errorf("event cannot be nil"))
	}

	data, err := json.Marshal(event)
	if err != nil {
		eventType := reflect.TypeOf(event).Name()
		return nil, NewSerializationError(eventType, "serialize", err)
	}

	return data, nil
}

// Deserialize converts JSON bytes back to an event.
// If the event type is registered, returns a value of that type.
// Otherwise, returns a map[string]interface{}.
func (s *JSONSerializer) Deserialize(data []byte, eventType string) (interface{}, error) {
	if len(data) == 0 {
		return nil, NewSerializationError(eventType, "deserialize", fmt.Errorf("data cannot be empty"))
	}

	// Try to find registered type
	t, ok := s.registry.Lookup(eventType)
	if !ok {
		// Fall back to map if type not registered
		var result map[string]interface{}
		if err := json.Unmarshal(data, &result); err != nil {
			return nil, NewSerializationError(eventType, "deserialize", err)
		}
		return result, nil
	}

	// Create new instance of registered type
	ptr := reflect.New(t)
	if err := json.Unmarshal(data, ptr.Interface()); err != nil {
		return nil, NewSerializationError(eventType, "deserialize", err)
	}

	// Return the value (not pointer)
	return ptr.Elem().Interface(), nil
}

// GetEventType returns the event type name for the given event.
// It uses the struct name as the type name.
func GetEventType(event interface{}) string {
	if event == nil {
		return ""
	}

	t := reflect.TypeOf(event)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return t.Name()
}

// SerializeEvent is a convenience function that serializes an event and returns EventData.
func SerializeEvent(serializer Serializer, event interface{}, metadata Metadata) (EventData, error) {
	eventType := GetEventType(event)
	if eventType == "" {
		return EventData{}, NewSerializationError("", "serialize", fmt.Errorf("cannot determine event type"))
	}

	data, err := serializer.Serialize(event)
	if err != nil {
		return EventData{}, err
	}

	return EventData{
		Type:     eventType,
		Data:     data,
		Metadata: metadata,
	}, nil
}

// DeserializeEvent is a convenience function that deserializes a StoredEvent to an Event.
func DeserializeEvent(serializer Serializer, stored StoredEvent) (Event, error) {
	data, err := serializer.Deserialize(stored.Data, stored.Type)
	if err != nil {
		return Event{}, err
	}

	return EventFromStored(stored, data), nil
}
