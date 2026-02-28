package mink

// Compile-time interface check.
var _ Serializer = (*UpcastingSerializer)(nil)

// UpcastingSerializer is a decorator that wraps any Serializer and applies
// upcasting during deserialization. Serialization passes through unchanged.
type UpcastingSerializer struct {
	inner Serializer
	chain *UpcasterChain
}

// NewUpcastingSerializer creates a new UpcastingSerializer wrapping the given serializer.
func NewUpcastingSerializer(inner Serializer, chain *UpcasterChain) *UpcastingSerializer {
	return &UpcastingSerializer{
		inner: inner,
		chain: chain,
	}
}

// Serialize converts an event to bytes. Pass-through to the inner serializer.
func (s *UpcastingSerializer) Serialize(event interface{}) ([]byte, error) {
	return s.inner.Serialize(event)
}

// Deserialize converts bytes back to an event.
// If upcasters are registered for the event type, the data is upcasted from
// DefaultSchemaVersion before deserialization.
func (s *UpcastingSerializer) Deserialize(data []byte, eventType string) (interface{}, error) {
	return s.DeserializeWithVersion(data, eventType, DefaultSchemaVersion, Metadata{})
}

// DeserializeWithVersion converts bytes back to an event, upcasting from the
// specified schema version. Metadata is provided as read-only context to upcasters.
func (s *UpcastingSerializer) DeserializeWithVersion(data []byte, eventType string, schemaVersion int, metadata Metadata) (interface{}, error) {
	if s.chain != nil && s.chain.HasUpcasters(eventType) {
		upcasted, _, err := s.chain.Upcast(eventType, schemaVersion, data, metadata)
		if err != nil {
			return nil, err
		}
		data = upcasted
	}
	return s.inner.Deserialize(data, eventType)
}

// Inner returns the wrapped serializer.
func (s *UpcastingSerializer) Inner() Serializer {
	return s.inner
}

// Chain returns the upcaster chain.
func (s *UpcastingSerializer) Chain() *UpcasterChain {
	return s.chain
}
