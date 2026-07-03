package mink

import (
	"context"
	"fmt"
	"sync/atomic"

	"go-mink.dev/adapters"
)

// EventStore is the main entry point for event sourcing operations.
// It provides methods for appending events, loading aggregates, and managing streams.
type EventStore struct {
	adapter    adapters.EventStoreAdapter
	serializer Serializer
	logger     Logger
	// upcasters holds an optional *UpcasterChain. It is an atomic pointer so that
	// RegisterUpcasters can be called concurrently with Load/Append without a data
	// race; nil (the default) means zero overhead.
	upcasters     atomic.Pointer[UpcasterChain]
	encryption    *FieldEncryptionConfig // nil by default — zero overhead when unused
	maxEventSize  int                    // 0 = unlimited
	subjectTagger SubjectTagger          // nil by default — zero overhead when unused
	subjectIndex  SubjectIndexWriter     // nil by default — zero overhead when unused
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

// WithUpcasters configures the event store with an upcaster chain for
// transparent schema evolution. When set, events are automatically upcasted
// to the latest schema version during loading and stamped with the latest
// version during appending.
func WithUpcasters(chain *UpcasterChain) Option {
	return func(es *EventStore) {
		es.upcasters.Store(chain)
	}
}

// WithMaxEventSize sets the maximum serialized size, in bytes, of a single
// event's data. Appending an event whose serialized (and, if configured,
// encrypted) data exceeds this returns ErrEventTooLarge. The default (0) imposes
// no limit; set it to bound memory/IO from oversized or malicious payloads.
func WithMaxEventSize(maxBytes int) Option {
	return func(es *EventStore) {
		es.maxEventSize = maxBytes
	}
}

// WithFieldEncryption configures the event store with field-level encryption.
// When set, configured fields are automatically encrypted during appending and
// decrypted during loading. Uses envelope encryption for performance.
func WithFieldEncryption(config *FieldEncryptionConfig) Option {
	return func(es *EventStore) {
		es.encryption = config
	}
}

// WithSubjectTagger configures a tagger that records, at append time, which data
// subject(s) each event concerns (in Metadata.Custom). It enables a SubjectResolver
// to later enumerate a subject's complete cross-stream footprint for GDPR export
// and erasure. Zero overhead when unset.
func WithSubjectTagger(tagger SubjectTagger) Option {
	return func(es *EventStore) {
		es.subjectTagger = tagger
	}
}

// WithSubjectIndexWriter records, at append time, which subject(s) each stream touches
// into the given index (derived from the events' subject tags — pair with
// WithSubjectTagger). Writes are best-effort: an index-write failure is logged, never
// failing the append. Combine with a SubjectResolver using WithResolverIndex to make
// discovery and erasure O(a subject's events) instead of a full scan. Zero overhead when
// unset.
func WithSubjectIndexWriter(w SubjectIndexWriter) Option {
	return func(es *EventStore) {
		es.subjectIndex = w
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

// EncryptionConfig returns the field-encryption configuration, or nil if the
// store was not configured with WithFieldEncryption. The erasure machinery
// (DataEraser) uses it to crypto-shred a subject's keys.
func (s *EventStore) EncryptionConfig() *FieldEncryptionConfig {
	return s.encryption
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

	// Validate the expected version: it must be one of the sentinels
	// (AnyVersion=-1, StreamExists=-2, NoStream=0) or a non-negative version.
	if config.expectedVersion < StreamExists {
		return ErrInvalidVersion
	}

	// Convert events to EventRecords
	records := make([]adapters.EventRecord, len(events))
	var subjectSet map[string]struct{} // nil (zero overhead) unless a subject index is wired
	if s.subjectIndex != nil {
		subjectSet = make(map[string]struct{})
	}
	for i, event := range events {
		eventData, err := SerializeEvent(s.serializer, event, config.metadata)
		if err != nil {
			return fmt.Errorf("mink: failed to serialize event %d: %w", i, err)
		}

		if err := s.prepareEventData(ctx, streamID, &eventData); err != nil {
			return fmt.Errorf("mink: failed to prepare event %d: %w", i, err)
		}
		s.collectSubjects(eventData.Metadata, subjectSet)

		records[i] = adapters.EventRecord{
			Type:     eventData.Type,
			Data:     eventData.Data,
			Metadata: convertMetadataToAdapter(eventData.Metadata),
		}
	}

	if _, err := s.adapter.Append(ctx, streamID, records, config.expectedVersion); err != nil {
		return err
	}
	s.writeSubjectIndex(ctx, streamID, subjectSet)
	return nil
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
		event, err := s.deserializeWithUpcast(ctx, minkStored)
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
// The aggregate's current version (agg.Version()) is used as the expected stream
// version for optimistic concurrency control.
//
// Invariant: creating uncommitted events (via AggregateBase.Apply) must not change
// Version(). Version is only advanced by ApplyEvent while replaying stored events
// during LoadAggregate. If a command mutator also increments the version before
// save, the expected version will no longer match the stored version and the save
// will fail with ErrConcurrencyConflict.
//
// After a successful save, if the aggregate implements VersionSetter, the version
// is updated to reflect the new stream version (and OriginalVersion is advanced),
// allowing subsequent modifications without reloading.
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
	var subjectSet map[string]struct{} // nil (zero overhead) unless a subject index is wired
	if s.subjectIndex != nil {
		subjectSet = make(map[string]struct{})
	}
	for i, event := range events {
		eventData, err := SerializeEvent(s.serializer, event, Metadata{})
		if err != nil {
			return fmt.Errorf("mink: failed to serialize aggregate event %d: %w", i, err)
		}

		if err := s.prepareEventData(ctx, streamID, &eventData); err != nil {
			return fmt.Errorf("mink: failed to prepare aggregate event %d: %w", i, err)
		}
		s.collectSubjects(eventData.Metadata, subjectSet)

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
	s.writeSubjectIndex(ctx, streamID, subjectSet)

	// Update aggregate version after successful save if it implements VersionSetter.
	// New version = old version + number of events saved.
	newVersion := expectedVersion + int64(len(events))
	if setter, ok := agg.(VersionSetter); ok {
		setter.SetVersion(newVersion)
	}
	if ov, ok := agg.(originalVersionSetter); ok {
		ov.setOriginalVersion(newVersion)
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

	// Apply each event to rebuild state
	for i, stored := range storedEvents {
		minkStored := convertStoredEventFromAdapter(stored)
		event, err := s.deserializeWithUpcast(ctx, minkStored)
		if err != nil {
			return fmt.Errorf("mink: failed to deserialize event %d: %w", i, err)
		}

		if err := agg.ApplyEvent(event.Data); err != nil {
			return fmt.Errorf("mink: failed to apply event %d: %w", i, err)
		}
	}

	// Set the aggregate version if it implements VersionSetter.
	// This is crucial for optimistic concurrency control in SaveAggregate.
	loadedVersion := int64(len(storedEvents))
	if setter, ok := agg.(VersionSetter); ok {
		setter.SetVersion(loadedVersion)
	}
	if ov, ok := agg.(originalVersionSetter); ok {
		ov.setOriginalVersion(loadedVersion)
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

// ProcessStoredEvent applies decryption, upcasting, and deserialization to a stored event.
// This is used by components that work with raw StoredEvents (e.g., DataExporter)
// and need the full processing pipeline.
func (s *EventStore) ProcessStoredEvent(ctx context.Context, stored StoredEvent) (Event, error) {
	return s.deserializeWithUpcast(ctx, stored)
}

// DecryptStoredEvent returns stored with its field-encrypted Data decrypted, reusing the
// same decrypt + WithDecryptionErrorHandler path as the deserializing read paths
// (Load/LoadAggregate/DataExporter). It is the StoredEvent-preserving counterpart to
// ProcessStoredEvent (which returns a deserialized Event): the value stays a StoredEvent
// and only its Data changes. Upcasting is intentionally NOT applied here — raw fields are
// preserved for Type-based consumers (projections, subscriptions).
//
// It is the single primitive every raw-StoredEvent delivery surface (the projection
// engine, event subscriptions, live catch-up) routes through, so decryption transparency
// is defined in exactly one place. Passthrough with zero overhead when no encryption is
// configured or the event is not encrypted. A crypto-shredded subject is handled exactly
// as elsewhere: decryptFields consults WithDecryptionErrorHandler; a handler that returns
// nil yields the event with its fields left as stored and no error. A hard, unhandled
// decryption error is returned for the caller to decide (retry / poison / fail).
func (s *EventStore) DecryptStoredEvent(ctx context.Context, stored StoredEvent) (StoredEvent, error) {
	if s.encryption == nil || !IsEncrypted(stored.Metadata) {
		return stored, nil
	}
	dec, err := s.encryption.decryptFields(ctx, stored.StreamID, stored.Type, stored.Data, stored.Metadata)
	if err != nil {
		return StoredEvent{}, err
	}
	stored.Data = dec
	return stored, nil
}

// deserializeWithUpcast deserializes a stored event, applying decryption and upcasting if configured.
// Decryption happens before upcasting so that upcasters receive plaintext.
// If no encryption or upcasters are registered, it falls back to the standard
// DeserializeEvent path with zero overhead.
func (s *EventStore) deserializeWithUpcast(ctx context.Context, stored StoredEvent) (Event, error) {
	upcasters := s.upcasters.Load()
	needsDecryption := s.encryption != nil && IsEncrypted(stored.Metadata)
	needsUpcast := upcasters != nil && upcasters.HasUpcasters(stored.Type)

	if !needsDecryption && !needsUpcast {
		return DeserializeEvent(s.serializer, stored)
	}

	eventData := stored.Data

	// Decrypt before upcasting
	if needsDecryption {
		decData, err := s.encryption.decryptFields(ctx, stored.StreamID, stored.Type, stored.Data, stored.Metadata)
		if err != nil {
			return Event{}, err
		}
		eventData = decData
	}

	// Upcast after decryption
	if needsUpcast {
		schemaVersion := GetSchemaVersion(stored.Metadata)
		upcasted, _, err := upcasters.Upcast(stored.Type, schemaVersion, eventData, stored.Metadata)
		if err != nil {
			return Event{}, err
		}
		eventData = upcasted
	}

	data, err := s.serializer.Deserialize(eventData, stored.Type)
	if err != nil {
		return Event{}, err
	}

	return EventFromStored(stored, data), nil
}

// collectSubjects adds an event's subject tags to set for subject-index writing.
// No-op when no index writer is configured (zero overhead) or when set is nil
// (defensive — callers allocate set exactly when subjectIndex is configured).
func (s *EventStore) collectSubjects(md Metadata, set map[string]struct{}) {
	if s.subjectIndex == nil || set == nil {
		return
	}
	for _, sub := range GetSubjectTags(md) {
		set[sub] = struct{}{}
	}
}

// writeSubjectIndex records the collected subjects for streamID into the configured
// index. Best-effort: a failure is logged, never returned — the append already
// succeeded, and the index can be reconciled with BackfillSubjectIndex.
func (s *EventStore) writeSubjectIndex(ctx context.Context, streamID string, set map[string]struct{}) {
	if s.subjectIndex == nil || len(set) == 0 {
		return
	}
	subs := make([]string, 0, len(set))
	for k := range set {
		subs = append(subs, k)
	}
	if err := s.subjectIndex.IndexSubjects(ctx, streamID, subs); err != nil {
		s.logger.Warn("mink: subject index write failed", "streamID", streamID, "error", err)
	}
}

// prepareEventData stamps the schema version and encrypts fields as needed.
// This is the shared logic used by Append, SaveAggregate, and the outbox wrapper.
// streamID is bound into the field-encryption AAD so ciphertext cannot be
// relocated to a different stream.
func (s *EventStore) prepareEventData(ctx context.Context, streamID string, eventData *EventData) error {
	if upcasters := s.upcasters.Load(); upcasters != nil {
		// Stamp the latest schema version, but respect a version the caller has
		// already set explicitly (e.g. when re-appending an event carried from
		// elsewhere) rather than clobbering it.
		if !hasSchemaVersion(eventData.Metadata) {
			eventData.Metadata = SetSchemaVersion(eventData.Metadata, upcasters.LatestVersion(eventData.Type))
		}
	}

	// Tag the data subject(s) before encryption so the tag stays queryable in
	// plaintext metadata (zero overhead when no tagger is configured).
	if s.subjectTagger != nil {
		if subjects := s.subjectTagger(eventData.Type, eventData.Data, eventData.Metadata); len(subjects) > 0 {
			eventData.Metadata = setSubjectTags(eventData.Metadata, subjects)
		}
	}

	if s.encryption != nil && s.encryption.HasEncryptedFields(eventData.Type) {
		encData, encMeta, err := s.encryption.encryptFields(ctx, streamID, eventData.Type, eventData.Data, eventData.Metadata)
		if err != nil {
			return err
		}
		eventData.Data = encData
		eventData.Metadata = encMeta
	}

	// Enforce the optional max event size on the final (serialized, encrypted) data.
	if s.maxEventSize > 0 && len(eventData.Data) > s.maxEventSize {
		return fmt.Errorf("%w: event type %q is %d bytes (limit %d)",
			ErrEventTooLarge, eventData.Type, len(eventData.Data), s.maxEventSize)
	}

	return nil
}

// RegisterUpcasters is a convenience method that registers upcasters with the event store.
// If no UpcasterChain has been configured, a new one is created automatically.
// It is safe to call concurrently with Load/Append (the upcaster pointer is atomic
// and UpcasterChain.Register is internally synchronized).
func (s *EventStore) RegisterUpcasters(upcasters ...Upcaster) error {
	chain := s.upcasters.Load()
	if chain == nil {
		chain = NewUpcasterChain()
		if !s.upcasters.CompareAndSwap(nil, chain) {
			// Another goroutine installed a chain first; use that one.
			chain = s.upcasters.Load()
		}
	}
	for _, u := range upcasters {
		if err := chain.Register(u); err != nil {
			return err
		}
	}
	return nil
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
