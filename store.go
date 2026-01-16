package mink

import (
	"context"
	"fmt"

	"github.com/AshkanYarmoradi/go-mink/adapters"
)

// EventStore is the main entry point for event sourcing operations.
// It provides methods for appending events, loading aggregates, and managing streams.
type EventStore struct {
	adapter    adapters.EventStoreAdapter
	serializer Serializer
	logger     Logger
}

// Logger defines the logging interface for the event store.
type Logger interface {
	Debug(msg string, args ...interface{})
	Info(msg string, args ...interface{})
	Warn(msg string, args ...interface{})
	Error(msg string, args ...interface{})
}

// noopLogger is a no-op logger implementation.
type noopLogger struct{}

func (l *noopLogger) Debug(msg string, args ...interface{}) {}
func (l *noopLogger) Info(msg string, args ...interface{})  {}
func (l *noopLogger) Warn(msg string, args ...interface{})  {}
func (l *noopLogger) Error(msg string, args ...interface{}) {}

// Option configures an EventStore.
type Option func(*EventStore)

// WithSerializer sets a custom serializer.
func WithSerializer(s Serializer) Option {
	return func(es *EventStore) {
		es.serializer = s
	}
}

// WithLogger sets a custom logger.
func WithLogger(l Logger) Option {
	return func(es *EventStore) {
		es.logger = l
	}
}

// New creates a new EventStore with the given adapter and options.
func New(adapter adapters.EventStoreAdapter, opts ...Option) *EventStore {
	es := &EventStore{
		adapter:    adapter,
		serializer: NewJSONSerializer(),
		logger:     &noopLogger{},
	}

	for _, opt := range opts {
		opt(es)
	}

	return es
}

// Serializer returns the event store's serializer.
func (s *EventStore) Serializer() Serializer {
	return s.serializer
}

// Adapter returns the underlying adapter.
func (s *EventStore) Adapter() adapters.EventStoreAdapter {
	return s.adapter
}

// RegisterEvents registers event types with the serializer.
// This is required for deserializing events back to their original types.
func (s *EventStore) RegisterEvents(events ...interface{}) {
	if js, ok := s.serializer.(*JSONSerializer); ok {
		js.RegisterAll(events...)
	}
}

// AppendOption configures an append operation.
type AppendOption func(*appendConfig)

type appendConfig struct {
	metadata        Metadata
	expectedVersion int64
}

// ExpectVersion sets the expected stream version for optimistic concurrency.
func ExpectVersion(v int64) AppendOption {
	return func(c *appendConfig) {
		c.expectedVersion = v
	}
}

// WithMetadata sets metadata for all events in the append operation.
func WithAppendMetadata(m Metadata) AppendOption {
	return func(c *appendConfig) {
		c.metadata = m
	}
}

// Append stores events to the specified stream.
// Events can be Go structs which will be serialized using the configured serializer.
func (s *EventStore) Append(ctx context.Context, streamID string, events []interface{}, opts ...AppendOption) error {
	if streamID == "" {
		return ErrEmptyStreamID
	}

	if len(events) == 0 {
		return ErrNoEvents
	}

	config := &appendConfig{
		expectedVersion: AnyVersion,
	}

	for _, opt := range opts {
		opt(config)
	}

	// Convert events to EventRecords
	records := make([]adapters.EventRecord, len(events))
	for i, event := range events {
		eventData, err := SerializeEvent(s.serializer, event, config.metadata)
		if err != nil {
			return fmt.Errorf("mink: failed to serialize event %d: %w", i, err)
		}

		records[i] = adapters.EventRecord{
			Type:     eventData.Type,
			Data:     eventData.Data,
			Metadata: convertMetadataToAdapter(eventData.Metadata),
		}
	}

	_, err := s.adapter.Append(ctx, streamID, records, config.expectedVersion)
	return err
}

// Load retrieves all events from a stream.
func (s *EventStore) Load(ctx context.Context, streamID string) ([]Event, error) {
	return s.LoadFrom(ctx, streamID, 0)
}

// LoadFrom retrieves events from a stream starting from the specified version.
func (s *EventStore) LoadFrom(ctx context.Context, streamID string, fromVersion int64) ([]Event, error) {
	if streamID == "" {
		return nil, ErrEmptyStreamID
	}

	storedEvents, err := s.adapter.Load(ctx, streamID, fromVersion)
	if err != nil {
		return nil, err
	}

	events := make([]Event, len(storedEvents))
	for i, stored := range storedEvents {
		minkStored := convertStoredEventFromAdapter(stored)
		event, err := DeserializeEvent(s.serializer, minkStored)
		if err != nil {
			return nil, fmt.Errorf("mink: failed to deserialize event %d: %w", i, err)
		}
		events[i] = event
	}

	return events, nil
}

// LoadRaw retrieves raw (non-deserialized) events from a stream.
func (s *EventStore) LoadRaw(ctx context.Context, streamID string, fromVersion int64) ([]StoredEvent, error) {
	if streamID == "" {
		return nil, ErrEmptyStreamID
	}

	storedEvents, err := s.adapter.Load(ctx, streamID, fromVersion)
	if err != nil {
		return nil, err
	}

	result := make([]StoredEvent, len(storedEvents))
	for i, stored := range storedEvents {
		result[i] = convertStoredEventFromAdapter(stored)
	}
	return result, nil
}

// SaveAggregate persists uncommitted events from an aggregate.
// The aggregate's version is used for optimistic concurrency control.
//
// After a successful save, if the aggregate implements VersionSetter,
// the version will be updated to reflect the new stream version.
// This allows for subsequent modifications without reloading.
func (s *EventStore) SaveAggregate(ctx context.Context, agg Aggregate) error {
	if agg == nil {
		return ErrNilAggregate
	}

	events := agg.UncommittedEvents()
	if len(events) == 0 {
		return nil // Nothing to save
	}

	streamID := fmt.Sprintf("%s-%s", agg.AggregateType(), agg.AggregateID())

	// Convert events to EventRecords
	records := make([]adapters.EventRecord, len(events))
	for i, event := range events {
		eventData, err := SerializeEvent(s.serializer, event, Metadata{})
		if err != nil {
			return fmt.Errorf("mink: failed to serialize aggregate event %d: %w", i, err)
		}

		records[i] = adapters.EventRecord{
			Type:     eventData.Type,
			Data:     eventData.Data,
			Metadata: convertMetadataToAdapter(eventData.Metadata),
		}
	}

	// Use aggregate version for optimistic concurrency
	expectedVersion := agg.Version()

	_, err := s.adapter.Append(ctx, streamID, records, expectedVersion)
	if err != nil {
		return err
	}

	// Update aggregate version after successful save if it implements VersionSetter.
	// New version = old version + number of events saved.
	if setter, ok := agg.(VersionSetter); ok {
		setter.SetVersion(expectedVersion + int64(len(events)))
	}

	// Clear uncommitted events after successful save
	agg.ClearUncommittedEvents()

	return nil
}

// LoadAggregate loads an aggregate's state by replaying its events.
// The aggregate should be a new instance with its ID and type already set.
//
// If the aggregate implements VersionSetter, the version will be set to the
// number of events loaded. This is required for proper optimistic concurrency
// control when saving the aggregate later.
//
// Note: AggregateBase implements VersionSetter, so aggregates embedding
// AggregateBase will automatically have their version set correctly.
func (s *EventStore) LoadAggregate(ctx context.Context, agg Aggregate) error {
	if agg == nil {
		return ErrNilAggregate
	}

	streamID := fmt.Sprintf("%s-%s", agg.AggregateType(), agg.AggregateID())

	storedEvents, err := s.adapter.Load(ctx, streamID, 0)
	if err != nil {
		return err
	}

	// Track the version from loaded events
	var lastVersion int64

	// Apply each event to rebuild state
	for i, stored := range storedEvents {
		// Deserialize event data
		data, err := s.serializer.Deserialize(stored.Data, stored.Type)
		if err != nil {
			return fmt.Errorf("mink: failed to deserialize event %d: %w", i, err)
		}

		// Apply event to aggregate
		if err := agg.ApplyEvent(data); err != nil {
			return fmt.Errorf("mink: failed to apply event %d: %w", i, err)
		}

		// Track version from stored event
		lastVersion = stored.Version
	}

	// Set the aggregate's version to the last loaded event version.
	// Aggregates that implement VersionSetter (such as those embedding AggregateBase)
	// will have their version automatically set during load, which is used for
	// optimistic concurrency control in SaveAggregate.
	if setter, ok := agg.(VersionSetter); ok && len(storedEvents) > 0 {
		setter.SetVersion(lastVersion)
	}

	// Set the aggregate version if it implements VersionSetter.
	// This is crucial for optimistic concurrency control in SaveAggregate.
	if setter, ok := agg.(VersionSetter); ok {
		setter.SetVersion(int64(len(storedEvents)))
	}

	return nil
}

// GetStreamInfo returns metadata about a stream.
func (s *EventStore) GetStreamInfo(ctx context.Context, streamID string) (*StreamInfo, error) {
	if streamID == "" {
		return nil, ErrEmptyStreamID
	}

	info, err := s.adapter.GetStreamInfo(ctx, streamID)
	if err != nil {
		return nil, err
	}

	return &StreamInfo{
		StreamID:   info.StreamID,
		Category:   info.Category,
		Version:    info.Version,
		EventCount: info.EventCount,
		CreatedAt:  info.CreatedAt,
		UpdatedAt:  info.UpdatedAt,
	}, nil
}

// GetLastPosition returns the global position of the last stored event.
func (s *EventStore) GetLastPosition(ctx context.Context) (uint64, error) {
	return s.adapter.GetLastPosition(ctx)
}

// Initialize sets up the required storage schema.
func (s *EventStore) Initialize(ctx context.Context) error {
	return s.adapter.Initialize(ctx)
}

// Close releases resources held by the event store.
func (s *EventStore) Close() error {
	return s.adapter.Close()
}

// Conversion helper functions

func convertMetadataToAdapter(m Metadata) adapters.Metadata {
	return adapters.Metadata{
		CorrelationID: m.CorrelationID,
		CausationID:   m.CausationID,
		UserID:        m.UserID,
		TenantID:      m.TenantID,
		Custom:        m.Custom,
	}
}

func convertMetadataFromAdapter(m adapters.Metadata) Metadata {
	return Metadata{
		CorrelationID: m.CorrelationID,
		CausationID:   m.CausationID,
		UserID:        m.UserID,
		TenantID:      m.TenantID,
		Custom:        m.Custom,
	}
}

func convertStoredEventFromAdapter(s adapters.StoredEvent) StoredEvent {
	return StoredEvent{
		ID:             s.ID,
		StreamID:       s.StreamID,
		Type:           s.Type,
		Data:           s.Data,
		Metadata:       convertMetadataFromAdapter(s.Metadata),
		Version:        s.Version,
		GlobalPosition: s.GlobalPosition,
		Timestamp:      s.Timestamp,
	}
}

// LoadEventsFromPosition loads events starting from a global position.
// Returns ErrSubscriptionNotSupported if the adapter does not implement SubscriptionAdapter.
// This is a helper method used by ProjectionEngine and ProjectionRebuilder.
func (s *EventStore) LoadEventsFromPosition(ctx context.Context, fromPosition uint64, limit int) ([]StoredEvent, error) {
	// Check if adapter supports subscription
	subAdapter, ok := s.adapter.(adapters.SubscriptionAdapter)
	if !ok {
		return nil, ErrSubscriptionNotSupported
	}

	events, err := subAdapter.LoadFromPosition(ctx, fromPosition, limit)
	if err != nil {
		return nil, err
	}

	// Convert adapters.StoredEvent to mink.StoredEvent
	result := make([]StoredEvent, len(events))
	for i, e := range events {
		result[i] = convertStoredEventFromAdapter(e)
	}
	return result, nil
}
