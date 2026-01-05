// Package protobuf provides a Protocol Buffers serializer for mink events.
//
// Protocol Buffers is a language-neutral, platform-neutral extensible mechanism
// for serializing structured data developed by Google. It offers smaller payloads
// and faster serialization compared to JSON.
//
// Usage:
//
//	// Create a new serializer
//	s := protobuf.NewSerializer()
//
//	// Register event types (must implement proto.Message)
//	s.Register("OrderCreated", &pb.OrderCreated{})
//	s.Register("ItemAdded", &pb.ItemAdded{})
//
//	// Serialize an event
//	data, err := s.Serialize(event)
//
//	// Deserialize an event
//	result, err := s.Deserialize(data, "OrderCreated")
//
// Note: Only types that implement proto.Message can be serialized with this
// serializer. For non-protobuf types, use the JSON or MessagePack serializers.
package protobuf

import (
	"errors"
	"fmt"
	"reflect"
	"sync"

	"google.golang.org/protobuf/proto"
)

// =============================================================================
// Errors
// =============================================================================

var (
	// ErrNilEvent indicates an attempt to serialize a nil event.
	ErrNilEvent = errors.New("mink/protobuf: cannot serialize nil event")

	// ErrEmptyData indicates an attempt to deserialize empty data.
	ErrEmptyData = errors.New("mink/protobuf: cannot deserialize empty data")

	// ErrNotProtoMessage indicates the event does not implement proto.Message.
	ErrNotProtoMessage = errors.New("mink/protobuf: event must implement proto.Message")

	// ErrTypeNotRegistered indicates the event type is not registered.
	ErrTypeNotRegistered = errors.New("mink/protobuf: event type not registered")
)

// SerializationError provides detailed error information for serialization failures.
type SerializationError struct {
	// EventType is the name of the event type that failed.
	EventType string

	// Operation is either "serialize" or "deserialize".
	Operation string

	// Cause is the underlying error.
	Cause error
}

// Error returns the error message.
func (e *SerializationError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("mink/protobuf: failed to %s %s: %v", e.Operation, e.EventType, e.Cause)
	}
	return fmt.Sprintf("mink/protobuf: failed to %s %s", e.Operation, e.EventType)
}

// Unwrap returns the underlying error.
func (e *SerializationError) Unwrap() error {
	return e.Cause
}

// Is checks if the target error matches.
func (e *SerializationError) Is(target error) bool {
	switch target {
	case ErrNilEvent, ErrEmptyData, ErrNotProtoMessage, ErrTypeNotRegistered:
		return errors.Is(e.Cause, target)
	}
	return false
}

// =============================================================================
// Serializer Options
// =============================================================================

// SerializerOption configures the Serializer.
type SerializerOption func(*Serializer)

// WithRegistry initializes the serializer with a pre-configured registry.
// The map should contain event type names as keys and proto.Message types as values.
func WithRegistry(registry map[string]reflect.Type) SerializerOption {
	return func(s *Serializer) {
		for name, typ := range registry {
			s.registry[name] = typ
		}
	}
}

// =============================================================================
// Serializer
// =============================================================================

// Serializer implements the mink.Serializer interface using Protocol Buffers.
// It maintains a registry of event types for deserialization.
type Serializer struct {
	mu       sync.RWMutex
	registry map[string]reflect.Type
}

// NewSerializer creates a new Protocol Buffers serializer with an empty registry.
func NewSerializer() *Serializer {
	return &Serializer{
		registry: make(map[string]reflect.Type),
	}
}

// NewSerializerWithOptions creates a new serializer with the specified options.
func NewSerializerWithOptions(opts ...SerializerOption) *Serializer {
	s := &Serializer{
		registry: make(map[string]reflect.Type),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Register adds an event type to the registry.
// The event must implement proto.Message.
// If a type with the same name already exists, it will be overwritten.
func (s *Serializer) Register(eventType string, event interface{}) error {
	if _, ok := event.(proto.Message); !ok {
		// Check if pointer implements proto.Message
		v := reflect.ValueOf(event)
		if v.Kind() != reflect.Ptr {
			ptrType := reflect.PtrTo(reflect.TypeOf(event))
			if !ptrType.Implements(reflect.TypeOf((*proto.Message)(nil)).Elem()) {
				return &SerializationError{
					EventType: eventType,
					Operation: "register",
					Cause:     ErrNotProtoMessage,
				}
			}
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	typ := reflect.TypeOf(event)
	if typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
	}
	s.registry[eventType] = typ
	return nil
}

// RegisterAll registers multiple event types by inferring their names from type names.
// All events must implement proto.Message.
func (s *Serializer) RegisterAll(events ...interface{}) error {
	for _, event := range events {
		typ := reflect.TypeOf(event)
		if typ.Kind() == reflect.Ptr {
			typ = typ.Elem()
		}
		eventType := typ.Name()
		if err := s.Register(eventType, event); err != nil {
			return err
		}
	}
	return nil
}

// MustRegister registers an event type and panics on error.
func (s *Serializer) MustRegister(eventType string, event interface{}) {
	if err := s.Register(eventType, event); err != nil {
		panic(err)
	}
}

// MustRegisterAll registers multiple event types and panics on error.
func (s *Serializer) MustRegisterAll(events ...interface{}) {
	if err := s.RegisterAll(events...); err != nil {
		panic(err)
	}
}

// Lookup returns the registered type for the given event type name.
func (s *Serializer) Lookup(eventType string) (reflect.Type, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	typ, ok := s.registry[eventType]
	return typ, ok
}

// RegisteredTypes returns a slice of all registered event type names.
func (s *Serializer) RegisteredTypes() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	types := make([]string, 0, len(s.registry))
	for name := range s.registry {
		types = append(types, name)
	}
	return types
}

// Count returns the number of registered event types.
func (s *Serializer) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return len(s.registry)
}

// Serialize converts an event to Protocol Buffers binary format.
// The event must implement proto.Message.
func (s *Serializer) Serialize(event interface{}) ([]byte, error) {
	if event == nil {
		return nil, &SerializationError{
			EventType: "nil",
			Operation: "serialize",
			Cause:     ErrNilEvent,
		}
	}

	msg, ok := event.(proto.Message)
	if !ok {
		// Try to get proto.Message from pointer
		v := reflect.ValueOf(event)
		if v.Kind() == reflect.Ptr && !v.IsNil() {
			if pm, ok := v.Interface().(proto.Message); ok {
				msg = pm
			} else {
				return nil, &SerializationError{
					EventType: reflect.TypeOf(event).String(),
					Operation: "serialize",
					Cause:     ErrNotProtoMessage,
				}
			}
		} else {
			return nil, &SerializationError{
				EventType: reflect.TypeOf(event).String(),
				Operation: "serialize",
				Cause:     ErrNotProtoMessage,
			}
		}
	}

	data, err := proto.Marshal(msg)
	if err != nil {
		return nil, &SerializationError{
			EventType: reflect.TypeOf(event).String(),
			Operation: "serialize",
			Cause:     err,
		}
	}

	return data, nil
}

// Deserialize converts Protocol Buffers binary data back to an event.
// The eventType must be registered for typed deserialization.
// If the type is not registered, returns ErrTypeNotRegistered.
//
// Note: Protocol Buffers can produce empty byte slices for messages with all
// default/zero values. This is valid and will be deserialized correctly.
func (s *Serializer) Deserialize(data []byte, eventType string) (interface{}, error) {
	if data == nil {
		return nil, &SerializationError{
			EventType: eventType,
			Operation: "deserialize",
			Cause:     ErrEmptyData,
		}
	}

	s.mu.RLock()
	typ, ok := s.registry[eventType]
	s.mu.RUnlock()

	if !ok {
		return nil, &SerializationError{
			EventType: eventType,
			Operation: "deserialize",
			Cause:     ErrTypeNotRegistered,
		}
	}

	// Create a new instance of the registered type
	v := reflect.New(typ)
	msg, ok := v.Interface().(proto.Message)
	if !ok {
		return nil, &SerializationError{
			EventType: eventType,
			Operation: "deserialize",
			Cause:     ErrNotProtoMessage,
		}
	}

	if err := proto.Unmarshal(data, msg); err != nil {
		return nil, &SerializationError{
			EventType: eventType,
			Operation: "deserialize",
			Cause:     err,
		}
	}

	// Return the value, not the pointer (consistent with other serializers)
	return v.Elem().Interface(), nil
}

// =============================================================================
// Interface Compliance
// =============================================================================

// Ensure Serializer implements the mink.Serializer interface at compile time.
// This is commented out to avoid circular dependencies.
// var _ mink.Serializer = (*Serializer)(nil)
