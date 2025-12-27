package mink

import (
	"fmt"
	"strings"
	"time"
)

// Version constants for optimistic concurrency control.
const (
	// AnyVersion skips version checking, allowing append regardless of current version.
	AnyVersion int64 = -1

	// NoStream indicates the stream must not exist (for creating new streams).
	NoStream int64 = 0

	// StreamExists indicates the stream must exist (for appending to existing streams).
	StreamExists int64 = -2
)

// StreamID uniquely identifies an event stream.
// It consists of a category (aggregate type) and an instance ID.
type StreamID struct {
	// Category represents the aggregate type (e.g., "Order", "Customer").
	Category string

	// ID is the unique identifier within the category (e.g., "order-123").
	ID string
}

// NewStreamID creates a new StreamID from category and ID.
func NewStreamID(category, id string) StreamID {
	return StreamID{Category: category, ID: id}
}

// ParseStreamID parses a stream ID string in the format "Category-ID".
// Returns an error if the format is invalid.
func ParseStreamID(s string) (StreamID, error) {
	parts := strings.SplitN(s, "-", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return StreamID{}, fmt.Errorf("mink: invalid stream ID format %q, expected 'Category-ID'", s)
	}
	return StreamID{Category: parts[0], ID: parts[1]}, nil
}

// String returns the stream ID as "Category-ID".
func (s StreamID) String() string {
	return fmt.Sprintf("%s-%s", s.Category, s.ID)
}

// IsZero reports whether the StreamID is empty.
func (s StreamID) IsZero() bool {
	return s.Category == "" && s.ID == ""
}

// Validate checks if the StreamID is valid.
func (s StreamID) Validate() error {
	if s.Category == "" {
		return fmt.Errorf("mink: stream category is required")
	}
	if s.ID == "" {
		return fmt.Errorf("mink: stream ID is required")
	}
	return nil
}

// Metadata contains contextual information about an event.
// It supports distributed tracing, multi-tenancy, and custom key-value pairs.
type Metadata struct {
	// CorrelationID links related events across services for distributed tracing.
	CorrelationID string `json:"correlationId,omitempty"`

	// CausationID identifies the event or command that caused this event.
	CausationID string `json:"causationId,omitempty"`

	// UserID identifies the user who triggered this event.
	UserID string `json:"userId,omitempty"`

	// TenantID identifies the tenant for multi-tenant applications.
	TenantID string `json:"tenantId,omitempty"`

	// Custom contains arbitrary key-value pairs for application-specific metadata.
	Custom map[string]string `json:"custom,omitempty"`
}

// WithCorrelationID returns a copy of Metadata with the correlation ID set.
func (m Metadata) WithCorrelationID(id string) Metadata {
	m.CorrelationID = id
	return m
}

// WithCausationID returns a copy of Metadata with the causation ID set.
func (m Metadata) WithCausationID(id string) Metadata {
	m.CausationID = id
	return m
}

// WithUserID returns a copy of Metadata with the user ID set.
func (m Metadata) WithUserID(id string) Metadata {
	m.UserID = id
	return m
}

// WithTenantID returns a copy of Metadata with the tenant ID set.
func (m Metadata) WithTenantID(id string) Metadata {
	m.TenantID = id
	return m
}

// WithCustom returns a copy of Metadata with a custom key-value pair added.
func (m Metadata) WithCustom(key, value string) Metadata {
	if m.Custom == nil {
		m.Custom = make(map[string]string)
	}
	newCustom := make(map[string]string, len(m.Custom)+1)
	for k, v := range m.Custom {
		newCustom[k] = v
	}
	newCustom[key] = value
	m.Custom = newCustom
	return m
}

// IsEmpty reports whether the Metadata has no values set.
func (m Metadata) IsEmpty() bool {
	return m.CorrelationID == "" &&
		m.CausationID == "" &&
		m.UserID == "" &&
		m.TenantID == "" &&
		len(m.Custom) == 0
}

// EventData represents an event to be stored.
// It contains the event type, serialized payload, and optional metadata.
type EventData struct {
	// Type is the event type identifier (e.g., "OrderCreated").
	Type string

	// Data is the serialized event payload.
	Data []byte

	// Metadata contains optional contextual information.
	Metadata Metadata
}

// NewEventData creates a new EventData with the given type and data.
func NewEventData(eventType string, data []byte) EventData {
	return EventData{
		Type: eventType,
		Data: data,
	}
}

// WithMetadata returns a copy of EventData with the metadata set.
func (e EventData) WithMetadata(m Metadata) EventData {
	e.Metadata = m
	return e
}

// Validate checks if the EventData is valid.
func (e EventData) Validate() error {
	if e.Type == "" {
		return fmt.Errorf("mink: event type is required")
	}
	if len(e.Data) == 0 {
		return fmt.Errorf("mink: event data is required")
	}
	return nil
}

// StoredEvent represents a persisted event with all storage metadata.
type StoredEvent struct {
	// ID is the globally unique event identifier.
	ID string

	// StreamID identifies the stream this event belongs to.
	StreamID string

	// Type is the event type identifier.
	Type string

	// Data is the serialized event payload.
	Data []byte

	// Metadata contains contextual information.
	Metadata Metadata

	// Version is the position within the stream (1-based).
	Version int64

	// GlobalPosition is the position across all streams.
	GlobalPosition uint64

	// Timestamp is when the event was stored.
	Timestamp time.Time
}

// StreamInfo contains metadata about an event stream.
type StreamInfo struct {
	// StreamID is the stream identifier.
	StreamID string

	// Category is the stream category (aggregate type).
	Category string

	// Version is the current stream version.
	Version int64

	// EventCount is the number of events in the stream.
	EventCount int64

	// CreatedAt is when the stream was created.
	CreatedAt time.Time

	// UpdatedAt is when the stream was last modified.
	UpdatedAt time.Time
}

// Event represents a deserialized event with its data as a Go type.
// This is the high-level representation used by applications.
type Event struct {
	// ID is the globally unique event identifier.
	ID string

	// StreamID identifies the stream this event belongs to.
	StreamID string

	// Type is the event type identifier.
	Type string

	// Data is the deserialized event payload.
	Data interface{}

	// Metadata contains contextual information.
	Metadata Metadata

	// Version is the position within the stream (1-based).
	Version int64

	// GlobalPosition is the position across all streams.
	GlobalPosition uint64

	// Timestamp is when the event was stored.
	Timestamp time.Time
}

// EventFromStored creates an Event from a StoredEvent with deserialized data.
func EventFromStored(stored StoredEvent, data interface{}) Event {
	return Event{
		ID:             stored.ID,
		StreamID:       stored.StreamID,
		Type:           stored.Type,
		Data:           data,
		Metadata:       stored.Metadata,
		Version:        stored.Version,
		GlobalPosition: stored.GlobalPosition,
		Timestamp:      stored.Timestamp,
	}
}
