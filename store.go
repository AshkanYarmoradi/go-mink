package mink

import (
	"bytes"
	"context"
	"fmt"
	"sync"
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

	// Aggregate replay-safety (replay-type-safety); zero overhead when unused.
	strictReplay bool     // WithStrictReplay: fail LoadAggregate on an unregistered type
	autoRegister bool     // WithAutoRegisterOnAppend: register concrete types at append time
	replayWarned sync.Map // dedups the default WARN to once per (stream|type|version)

	// eventTypeRegistrar caches the serializer's optional EventTypeRegistrar view,
	// resolved once at construction, so the per-event replay-type check on the hot
	// LoadAggregate path is a single field read + map lookup rather than an interface
	// type assertion per event. nil when the serializer does not support introspection.
	eventTypeRegistrar EventTypeRegistrar

	// serializerErr records a fatal serializer/adapter incompatibility detected at
	// construction (a binary serializer paired with a JSON/JSONB-backed adapter). It is
	// nil for every valid pairing. When set, the write paths (Append/SaveAggregate) return
	// it instead of letting the binary payload fail as a cryptic driver error on INSERT.
	serializerErr error
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
//
// If a binary serializer (one whose BinaryFormat reports true, such as
// serializer/msgpack or serializer/protobuf) is paired with an adapter that
// stores event data as JSON text (one whose RequiresJSONData reports true, such
// as the PostgreSQL JSONB adapter), the pairing can never succeed — the binary
// payload is rejected by the JSON column on every write. New does not panic on
// this: it records the incompatibility so the first Append/SaveAggregate returns
// a clear ErrBinarySerializerUnsupported instead of a cryptic driver error. Use
// the default JSON serializer with JSON-backed adapters.
func New(adapter adapters.EventStoreAdapter, opts ...Option) *EventStore {
	es := &EventStore{
		adapter:    adapter,
		serializer: NewJSONSerializer(),
		logger:     &noopLogger{},
	}

	for _, opt := range opts {
		opt(es)
	}

	// Cache the serializer's optional introspection view once (the serializer is fixed
	// after options are applied), so replay-safety checks avoid a per-event type assertion.
	if r, ok := es.serializer.(EventTypeRegistrar); ok {
		es.eventTypeRegistrar = r
	}

	// Detect a fatal serializer/adapter incompatibility (a binary serializer with a
	// JSON/JSONB-backed adapter). Record it rather than panicking — a library constructor
	// must not panic on a recoverable configuration error, and New's signature cannot
	// return one without a breaking change. The write paths surface it as a clear
	// ErrBinarySerializerUnsupported on first use, instead of a cryptic driver error.
	es.serializerErr = checkSerializerAdapterCompatible(es.serializer, adapter)

	return es
}

// checkSerializerAdapterCompatible rejects a binary serializer paired with an
// adapter that requires JSON-encoded event data. Both signals are opt-in: an
// adapter that does not implement adapters.JSONDataAdapter (or returns false)
// imposes no constraint, and a serializer that does not report a binary format
// is assumed to emit JSON text. It therefore returns nil for every historical
// pairing (JSON serializer with any adapter; any serializer with the in-memory
// adapter).
func checkSerializerAdapterCompatible(s Serializer, adapter adapters.EventStoreAdapter) error {
	jsonAdapter, ok := adapter.(adapters.JSONDataAdapter)
	if !ok || !jsonAdapter.RequiresJSONData() {
		return nil
	}
	if !producesBinary(s) {
		return nil
	}
	return fmt.Errorf(
		"%w: this serializer emits binary (non-JSON) event data that the adapter's JSON/JSONB data column cannot store; use the default JSON serializer, or an event-store adapter with a BYTEA data column",
		ErrBinarySerializerUnsupported,
	)
}

// producesBinary reports whether s serializes to a binary (non-JSON-text) format via the
// optional BinaryFormatReporter interface (a serializer that does not implement it is assumed
// to emit JSON text). Decorators are responsible for forwarding BinaryFormat to their wrapped
// serializer — UpcastingSerializer does — so wrapping a binary serializer does not defeat the
// check without an unbounded interface-chain walk here.
func producesBinary(s Serializer) bool {
	r, ok := s.(BinaryFormatReporter)
	return ok && r.BinaryFormat()
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

// RegisterAggregateEvents registers a set of event types in one call. It is an alias of
// RegisterEvents named for the common, important case: register every event type an
// aggregate applies so LoadAggregate never silently drops one. Pre-flight it in a test with
// RegisteredEventTypes.
func (s *EventStore) RegisterAggregateEvents(events ...interface{}) {
	s.RegisterEvents(events...)
}

// RegisteredEventTypes returns the event type names registered with the serializer, or nil
// if the serializer does not support introspection. Use it to assert, in a test, that every
// event type your aggregates apply is registered — a pre-flight against silent replay drops.
func (s *EventStore) RegisteredEventTypes() []string {
	if s.eventTypeRegistrar != nil {
		return s.eventTypeRegistrar.RegisteredEventTypes()
	}
	return nil
}

// WithStrictReplay makes LoadAggregate return an *UnregisteredEventTypeError when it
// encounters an event whose type is not registered (which would otherwise deserialize to the
// map fallback and be silently dropped from the rebuilt aggregate state). The default is
// lenient — such an event is logged (WARN, once per stream/type/version) and skipped,
// preserving existing v1.x behavior; use this to fail fast in dev/CI. Zero overhead unset.
func WithStrictReplay() Option {
	return func(es *EventStore) { es.strictReplay = true }
}

// WithAutoRegisterOnAppend registers each event's concrete type with the serializer the
// first time it is appended/saved in this process, so a save-then-load round-trip in the same
// process does not require a separate RegisterEvents call. In-process only: a cold process
// that only loads historical events still needs explicit registration. Off by default to keep
// registration explicit and discoverable.
func WithAutoRegisterOnAppend() Option {
	return func(es *EventStore) { es.autoRegister = true }
}

// isUnresolvedReplayType reports whether a stored event deserialized to the untyped map
// fallback (its Type is not registered), so an aggregate's concrete-type ApplyEvent switch
// cannot apply it — the silent-drop condition WithStrictReplay guards against.
func (s *EventStore) isUnresolvedReplayType(stored StoredEvent, data interface{}) bool {
	if s.eventTypeRegistrar != nil {
		return !s.eventTypeRegistrar.IsRegistered(stored.Type)
	}
	_, isMap := data.(map[string]interface{})
	return isMap
}

// warnUnresolvedReplayType logs one WARN per (stream, type, version) for an unregistered
// replay event, so the otherwise-silent state loss is observable without flooding the log.
func (s *EventStore) warnUnresolvedReplayType(streamID, eventType string, version int64) {
	key := fmt.Sprintf("%s|%s|%d", streamID, eventType, version)
	if _, loaded := s.replayWarned.LoadOrStore(key, struct{}{}); loaded {
		return
	}
	s.logger.Warn("mink: unregistered event type on aggregate replay — event skipped, aggregate state may be incomplete (register it via RegisterEvents/RegisterAggregateEvents, or use WithStrictReplay to fail fast)",
		"stream", streamID, "eventType", eventType, "version", version)
}

// autoRegisterEvents registers the concrete types of events being appended when
// WithAutoRegisterOnAppend is set (JSON serializer only; no-op otherwise).
func (s *EventStore) autoRegisterEvents(events []interface{}) {
	if !s.autoRegister {
		return
	}
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
	if s.serializerErr != nil {
		return s.serializerErr
	}
	if streamID == "" {
		return ErrEmptyStreamID
	}

	if len(events) == 0 {
		return ErrNoEvents
	}

	// Optionally register these events' concrete types (WithAutoRegisterOnAppend) so a
	// save-then-load round-trip in this process resolves them to concrete types.
	s.autoRegisterEvents(events)

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
	if s.serializerErr != nil {
		return s.serializerErr
	}
	if agg == nil {
		return ErrNilAggregate
	}

	events := agg.UncommittedEvents()
	if len(events) == 0 {
		return nil // Nothing to save
	}

	// Optionally register these events' concrete types (WithAutoRegisterOnAppend) so a
	// subsequent LoadAggregate in this process resolves them rather than dropping them.
	s.autoRegisterEvents(events)

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

		// An unregistered event type deserializes to the map fallback, which the
		// aggregate's concrete-type ApplyEvent switch cannot match — silently dropping it
		// from the rebuilt state. Surface it (WARN by default, once per stream/type/version)
		// or fail fast under WithStrictReplay.
		if s.isUnresolvedReplayType(minkStored, event.Data) {
			if s.strictReplay {
				return &UnregisteredEventTypeError{StreamID: streamID, EventType: minkStored.Type, Version: minkStored.Version}
			}
			s.warnUnresolvedReplayType(streamID, minkStored.Type, minkStored.Version)
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
	// If decryption actually transformed the data, clear the encryption markers on a COPY
	// of the metadata so the returned event is internally consistent: plaintext Data must
	// not still be flagged $encrypted_fields, or re-processing it (ProcessStoredEvent /
	// DecryptStoredEvent) would try to decrypt plaintext and fail. When decryptFields is a
	// crypto-shred no-op (handler swallowed the error, ciphertext left in place), the bytes
	// are unchanged and the markers are intentionally kept so the event still reports as
	// encrypted/unrecoverable.
	if !bytes.Equal(dec, stored.Data) {
		stored.Metadata = stripEncryptionMetadata(stored.Metadata)
	}
	stored.Data = dec
	return stored, nil
}

// decryptStoredEvents returns events with each field-encrypted event's Data decrypted, via
// DecryptStoredEvent. It is the shared batch primitive the pull-based delivery surfaces —
// async projections, projection rebuild, and catch-up subscriptions — route through, so
// decryption is applied identically everywhere and a new pull surface inherits it by calling
// one method. Zero overhead when encryption is unconfigured. A hard, unhandled decryption
// error aborts the batch and is returned (the caller must not advance its cursor/checkpoint).
func (s *EventStore) decryptStoredEvents(ctx context.Context, events []StoredEvent) ([]StoredEvent, error) {
	if s.encryption == nil {
		return events, nil
	}
	for i := range events {
		dec, err := s.DecryptStoredEvent(ctx, events[i])
		if err != nil {
			return nil, fmt.Errorf("decrypt event at position %d: %w", events[i].GlobalPosition, err)
		}
		events[i] = dec
	}
	return events, nil
}

// EncryptStoredEvent is the encode counterpart to DecryptStoredEvent: it seals a
// stored event's configured fields under the current FieldEncryptionConfig and
// stamps the current envelope in its metadata, exactly as the append path
// (prepareEventData) would — the configured SubjectTagger runs first so the wrapping
// key is resolved identically. It is a passthrough (input returned unchanged) when
// no encryption is configured, the event type has no configured fields, or the event
// is already encrypted. Zero overhead when unused.
func (s *EventStore) EncryptStoredEvent(ctx context.Context, stored StoredEvent) (StoredEvent, error) {
	if s.encryption == nil || IsEncrypted(stored.Metadata) || !s.encryption.HasEncryptedFields(stored.Type) {
		return stored, nil
	}
	md := stored.Metadata
	// Tag the subject(s) before encryption so resolveKeyID picks the same per-subject
	// key the live append path would (subject-scoped field keys).
	if s.subjectTagger != nil {
		if subjects := s.subjectTagger(stored.Type, stored.Data, md); len(subjects) > 0 {
			md = setSubjectTags(md, subjects)
		}
	}
	encData, encMeta, err := s.encryption.encryptFields(ctx, stored.StreamID, stored.Type, stored.Data, md)
	if err != nil {
		return StoredEvent{}, err
	}
	stored.Data = encData
	stored.Metadata = encMeta
	return stored, nil
}

// ReEncryptStreamInPlace brings a stream's existing events under the current
// field-encryption scheme in place: it seals each not-yet-encrypted event's
// configured fields under the current key and rewrites the same row, preserving the
// event's id, type, stream, version, global position, and timestamp (only the
// at-rest encoding of data/metadata changes — history is not rewritten). It is the
// opt-in, operator-triggered historical backfill for a consumer that enabled
// encryption after it already had data, making that PII crypto-shreddable; contrast
// with ReEncryptStream, which copies to a NEW stream (for rotation).
//
// It is idempotent — an event already encrypted, or of a type with no configured
// fields, is skipped — so a re-run or a resume after a mid-stream failure is safe.
// Returns the number of events re-encrypted and the distinct wrapping key ids used.
// Requires an adapter implementing adapters.EventRewriteAdapter (else
// ErrRewriteNotSupported); a no-op with zero overhead when no encryption is
// configured.
func (s *EventStore) ReEncryptStreamInPlace(ctx context.Context, streamID string) (int, []string, error) {
	rw, ok := s.adapter.(adapters.EventRewriteAdapter)
	if !ok {
		return 0, nil, ErrRewriteNotSupported
	}
	if s.encryption == nil {
		return 0, nil, nil
	}
	events, err := s.LoadRaw(ctx, streamID, 0)
	if err != nil {
		return 0, nil, err
	}
	keySet := make(map[string]struct{})
	n := 0
	for _, ev := range events {
		if IsEncrypted(ev.Metadata) || !s.encryption.HasEncryptedFields(ev.Type) {
			continue // already current, or nothing to encrypt (idempotent)
		}
		enc, err := s.EncryptStoredEvent(ctx, ev)
		if err != nil {
			return n, sortedSet(keySet), err // partial progress; resumable
		}
		if err := rw.RewriteEventData(ctx, streamID, ev.Version, enc.Data, convertMetadataToAdapter(enc.Metadata)); err != nil {
			return n, sortedSet(keySet), err
		}
		if k := GetEncryptionKeyID(enc.Metadata); k != "" {
			keySet[k] = struct{}{}
		}
		n++
	}
	return n, sortedSet(keySet), nil
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

// FeedFilter selects events for a filtered load-from-position read by indexed axis
// (event type, stream id, or category). It is an input DTO with no conversion step,
// so it is aliased to the adapter type rather than duplicated. See adapters.FeedFilter.
type FeedFilter = adapters.FeedFilter

// LoadEventsFromPositionFiltered loads events from a global position that match an
// indexed FeedFilter (event type, stream id, and/or category), pushing the predicate
// down to storage instead of scanning the whole feed. It returns
// ErrFilteredFeedNotSupported if the adapter does not implement FilteredFeedAdapter.
// Results keep the LoadEventsFromPosition contract: ascending global position,
// exclusive of fromPosition, bounded by limit, and (on the PostgreSQL adapter)
// subject to the same gapless safe watermark. An empty filter is equivalent to
// LoadEventsFromPosition.
//
// This is an introspection / ops / migration primitive for reading the raw event log
// by indexed axis (audit browsers, backfill scanners, diagnostics) — NOT an
// application read path. For queries over unindexed data (payload fields, a tenant in
// metadata) or for application reads, project a read model instead.
func (s *EventStore) LoadEventsFromPositionFiltered(ctx context.Context, fromPosition uint64, limit int, filter FeedFilter) ([]StoredEvent, error) {
	// Check if adapter supports filtered feed reads
	filterAdapter, ok := s.adapter.(adapters.FilteredFeedAdapter)
	if !ok {
		return nil, ErrFilteredFeedNotSupported
	}

	events, err := filterAdapter.LoadFromPositionFiltered(ctx, fromPosition, limit, filter)
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
