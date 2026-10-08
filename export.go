package mink

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"go-mink.dev/adapters"
)

// ExportFilter determines whether a stored event should be included in a data export.
type ExportFilter func(event StoredEvent) bool

// ExportHandler is called for each exported event during streaming export.
// Return a non-nil error to stop the export.
type ExportHandler func(ctx context.Context, event ExportedEvent) error

// DataExporter handles GDPR data export (right to access / right to data portability).
// It collects events belonging to a data subject from the event store, decrypts
// encrypted fields, and returns them in a portable format.
//
// When encrypted fields cannot be decrypted (e.g., key revoked via crypto-shredding),
// those events are included with Redacted=true and nil Data.
//
// There are two enumeration strategies:
//   - Stream-based: provide explicit stream IDs in ExportRequest.Streams.
//   - Scan-based: provide an ExportFilter and the exporter scans all events.
//     Requires the adapter to implement SubscriptionAdapter.
type DataExporter struct {
	store     *EventStore
	batchSize int
	logger    Logger
	resolver  *SubjectResolver
}

// DataExporterOption configures a DataExporter.
type DataExporterOption func(*DataExporter)

// WithExportBatchSize sets the number of events loaded per batch during scan-based export.
// Default is 1000.
func WithExportBatchSize(size int) DataExporterOption {
	return func(e *DataExporter) {
		if size > 0 {
			e.batchSize = size
		}
	}
}

// WithExportLogger sets the logger for the data exporter.
func WithExportLogger(l Logger) DataExporterOption {
	return func(e *DataExporter) {
		if l != nil {
			e.logger = l
		}
	}
}

// WithExportSubjectResolver configures a SubjectResolver so a SubjectID-only
// ExportRequest (no Streams/Filter) is automatically resolved to the subject's
// complete footprint — for both Export and ExportStream. A partial footprint is never
// silently exported: Export surfaces it as ExportResult.Partial, while ExportStream (which
// has no result object) fails with ErrExportPartialFootprint.
func WithExportSubjectResolver(r *SubjectResolver) DataExporterOption {
	return func(e *DataExporter) {
		e.resolver = r
	}
}

// NewDataExporter creates a new DataExporter for the given event store.
func NewDataExporter(store *EventStore, opts ...DataExporterOption) *DataExporter {
	e := &DataExporter{
		store:     store,
		batchSize: 1000,
		logger:    &noopLogger{},
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// ExportRequest describes what data to export for a data subject.
type ExportRequest struct {
	// SubjectID identifies the data subject (required).
	SubjectID string

	// Streams lists specific stream IDs to export.
	// When provided, only these streams are loaded (efficient, no full scan).
	//
	// A stream may hold events about several data subjects (a shared "order" stream,
	// say). When Streams is given and Filter is nil, the exporter therefore applies
	// SubjectOrUntaggedFilter(SubjectID) by default: events tagged for other subjects
	// (via WithSubjectTagger, $subjects) are dropped, while events carrying no subject
	// tags — which cannot be attributed to anyone — are still exported. Set Filter
	// explicitly to override that default (e.g. SubjectFilter to also drop untagged
	// events, or a custom predicate).
	Streams []string

	// Filter selects which events to include.
	// When Streams is empty, the exporter scans all events and applies this filter
	// (requires the adapter to implement SubscriptionAdapter).
	// When Streams is provided, the filter is applied within each stream; when it is
	// nil, SubjectOrUntaggedFilter(SubjectID) is applied instead (see Streams).
	Filter ExportFilter

	// FromTime limits export to events stored at or after this time.
	FromTime *time.Time

	// ToTime limits export to events stored at or before this time.
	ToTime *time.Time
}

// ExportResult contains all exported data for a data subject.
type ExportResult struct {
	// SubjectID is the data subject identifier from the request.
	SubjectID string

	// Events contains all exported events, ordered by stream then version.
	Events []ExportedEvent

	// Streams lists all unique stream IDs that contained matching events.
	Streams []string

	// TotalEvents is the total number of events included (including redacted).
	TotalEvents int

	// RedactedCount is the number of events whose PII could not be decrypted
	// (e.g., due to crypto-shredding / key revocation).
	RedactedCount int

	// ExportedAt is the timestamp when the export was generated.
	ExportedAt time.Time

	// Partial is true when the export was driven by a subject footprint that could
	// not be proven complete (e.g. legacy untagged events); the caller MUST treat
	// the export as potentially incomplete.
	Partial bool
}

// ExportedEvent represents a single event in the data export.
type ExportedEvent struct {
	// StreamID identifies the stream this event belongs to.
	StreamID string

	// EventType is the event type identifier.
	EventType string

	// Data is the deserialized event payload.
	// When Redacted is true, Data is nil.
	Data interface{}

	// RawData is the serialized event payload after decryption and upcasting
	// (plaintext JSON bytes). When Redacted is true, RawData instead contains the
	// original (still-encrypted) on-disk bytes.
	RawData []byte

	// Metadata contains non-PII contextual information about the event.
	Metadata ExportedMetadata

	// Version is the position within the stream (1-based).
	Version int64

	// GlobalPosition is the position across all streams.
	GlobalPosition uint64

	// Timestamp is when the event was stored.
	Timestamp time.Time

	// Redacted indicates the event's encrypted fields could not be decrypted
	// (e.g., because the encryption key was revoked via crypto-shredding).
	Redacted bool
}

// ExportedMetadata contains non-PII metadata included in the export.
type ExportedMetadata struct {
	CorrelationID string
	CausationID   string
	TenantID      string
	SchemaVersion int
}

// Export collects all matching events for a data subject and returns them.
// Use ExportStream for large exports that should not be held in memory.
func (e *DataExporter) Export(ctx context.Context, req ExportRequest) (*ExportResult, error) {
	if req.SubjectID == "" {
		return nil, ErrSubjectIDRequired
	}

	// A SubjectID-only request resolves to the subject's complete footprint.
	resolved, partial := false, false
	if e.resolver != nil && len(req.Streams) == 0 && req.Filter == nil {
		fp, err := e.resolver.Resolve(ctx, req.SubjectID)
		if err != nil {
			return nil, NewExportError(req.SubjectID, err)
		}
		req.Streams = fp.Streams
		// A resolved stream is included because it holds at least one event tagged for the
		// subject, but a shared stream may also hold OTHER subjects' events. Constrain the
		// export to this subject's events so a shared stream never leaks a co-tenant's data.
		req.Filter = SubjectFilter(req.SubjectID)
		partial = fp.Partial
		resolved = true
	}
	if !resolved {
		if err := e.validateRequest(req); err != nil {
			return nil, err
		}
	}

	result := &ExportResult{
		SubjectID:  req.SubjectID,
		ExportedAt: time.Now(),
		Partial:    partial,
	}

	// A resolved subject with no streams has no data to export (avoid scanning all).
	if resolved && len(req.Streams) == 0 {
		result.Streams = []string{}
		return result, nil
	}

	streamSet := make(map[string]struct{})

	handler := func(_ context.Context, event ExportedEvent) error {
		result.Events = append(result.Events, event)
		streamSet[event.StreamID] = struct{}{}
		result.TotalEvents++
		if event.Redacted {
			result.RedactedCount++
		}
		return nil
	}

	if err := e.processEvents(ctx, req, handler); err != nil {
		return nil, err
	}

	result.Streams = make([]string, 0, len(streamSet))
	for s := range streamSet {
		result.Streams = append(result.Streams, s)
	}

	return result, nil
}

// ExportStream calls handler for each matching event, without holding all events in memory.
// This is suitable for large exports. Events are yielded in stream order for stream-based
// export, or global position order for scan-based export.
// Return a non-nil error from the handler to stop the export early.
// When a SubjectID-only request auto-resolves to a partial footprint, ExportStream returns
// ErrExportPartialFootprint rather than streaming incomplete data — use Export for the flag.
func (e *DataExporter) ExportStream(ctx context.Context, req ExportRequest, handler ExportHandler) error {
	if req.SubjectID == "" {
		return ErrSubjectIDRequired
	}
	if handler == nil {
		return NewExportError(req.SubjectID, fmt.Errorf("handler is required"))
	}

	// A SubjectID-only request resolves to the subject's footprint, mirroring Export.
	// ExportStream has no result object to carry a Partial flag, so rather than silently
	// streaming an incomplete footprint it fails with ErrExportPartialFootprint — callers
	// who need the flag (not an error) should use Export and inspect ExportResult.Partial.
	if e.resolver != nil && len(req.Streams) == 0 && req.Filter == nil {
		fp, err := e.resolver.Resolve(ctx, req.SubjectID)
		if err != nil {
			return NewExportError(req.SubjectID, err)
		}
		if fp.Partial {
			return NewExportError(req.SubjectID, ErrExportPartialFootprint)
		}
		req.Streams = fp.Streams
		if len(req.Streams) == 0 {
			return nil // resolved to no streams — nothing to export
		}
		// Constrain to this subject's events: a shared stream may also hold other subjects'
		// events, and streaming the whole stream would leak them (see Export).
		req.Filter = SubjectFilter(req.SubjectID)
	} else if err := e.validateRequest(req); err != nil {
		return err
	}

	return e.processEvents(ctx, req, handler)
}

func (e *DataExporter) validateRequest(req ExportRequest) error {
	if req.SubjectID == "" {
		return ErrSubjectIDRequired
	}
	if len(req.Streams) == 0 && req.Filter == nil {
		return ErrNoExportSources
	}
	return nil
}

func (e *DataExporter) processEvents(ctx context.Context, req ExportRequest, handler ExportHandler) error {
	// One revocation-status cache per export: IsRevoked is a remote call for KMS/Vault
	// providers, and an export is the "many events, few keys" shape, so memoizing by key id
	// turns N per-event round-trips into one lookup per distinct key. Confined to this export.
	revokedCache := make(map[string]bool)
	if len(req.Streams) > 0 {
		// Explicit streams with no filter: SubjectID must scope the export, not merely
		// label it. A shared stream may hold other subjects' events, and exporting the
		// whole stream would hand one subject another's data. Drop events tagged for
		// other subjects; keep untagged ones (unattributable, exported as before).
		if req.Filter == nil {
			req.Filter = SubjectOrUntaggedFilter(req.SubjectID)
		}
		return e.exportFromStreams(ctx, req, handler, revokedCache)
	}
	return e.exportFromScan(ctx, req, handler, revokedCache)
}

// exportFromStreams loads events from specific streams.
func (e *DataExporter) exportFromStreams(ctx context.Context, req ExportRequest, handler ExportHandler, revokedCache map[string]bool) error {
	for _, streamID := range req.Streams {
		if err := ctx.Err(); err != nil {
			return err
		}

		stored, err := e.store.LoadRaw(ctx, streamID, 0)
		if err != nil {
			if errors.Is(err, ErrStreamNotFound) {
				e.logger.Warn("stream not found during export, skipping",
					"streamID", streamID, "subjectID", req.SubjectID)
				continue
			}
			return NewExportError(req.SubjectID, fmt.Errorf("failed to load stream %q: %w", streamID, err))
		}

		for _, se := range stored {
			if err := ctx.Err(); err != nil {
				return err
			}

			if !e.matchesRequest(se, req) {
				continue
			}

			exported, err := e.processStoredEvent(ctx, se, revokedCache)
			if err != nil {
				return err
			}

			if err := handler(ctx, exported); err != nil {
				return err
			}
		}
	}
	return nil
}

// exportFromScan scans all events in global position order and applies the filter.
func (e *DataExporter) exportFromScan(ctx context.Context, req ExportRequest, handler ExportHandler, revokedCache map[string]bool) error {
	if _, ok := e.store.Adapter().(adapters.SubscriptionAdapter); !ok {
		return ErrExportScanNotSupported
	}

	e.logger.Info("starting scan-based export", "subjectID", req.SubjectID, "batchSize", e.batchSize)

	var position uint64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		batch, err := e.store.LoadEventsFromPosition(ctx, position, e.batchSize)
		if err != nil {
			return NewExportError(req.SubjectID,
				fmt.Errorf("failed to scan events from position %d: %w", position, err))
		}

		if len(batch) == 0 {
			break
		}

		for _, se := range batch {
			if err := ctx.Err(); err != nil {
				return err
			}

			if !e.matchesRequest(se, req) {
				continue
			}

			exported, err := e.processStoredEvent(ctx, se, revokedCache)
			if err != nil {
				return err
			}

			if err := handler(ctx, exported); err != nil {
				return err
			}
		}

		position = batch[len(batch)-1].GlobalPosition
	}

	return nil
}

// matchesRequest checks whether a stored event matches the export request criteria.
func (e *DataExporter) matchesRequest(se StoredEvent, req ExportRequest) bool {
	if req.FromTime != nil && se.Timestamp.Before(*req.FromTime) {
		return false
	}
	if req.ToTime != nil && se.Timestamp.After(*req.ToTime) {
		return false
	}
	if req.Filter != nil && !req.Filter(se) {
		return false
	}
	return true
}

// processStoredEvent converts a StoredEvent into an ExportedEvent.
// It handles decryption, upcasting, and deserialization via the event store's pipeline.
// If the encryption key has been revoked (crypto-shredding), the event is marked as redacted.
func (e *DataExporter) processStoredEvent(ctx context.Context, se StoredEvent, revokedCache map[string]bool) (ExportedEvent, error) {
	exported := ExportedEvent{
		StreamID:       se.StreamID,
		EventType:      se.Type,
		RawData:        se.Data,
		Metadata:       newExportedMetadata(se.Metadata),
		Version:        se.Version,
		GlobalPosition: se.GlobalPosition,
		Timestamp:      se.Timestamp,
	}

	// Independently redact crypto-shredded events. A WithDecryptionErrorHandler that
	// returns nil (the recommended shred setup) makes ProcessStoredEvent report success
	// with still-encrypted data, which would otherwise leak ciphertext into the export.
	// If the event is encrypted under a revoked key, redact regardless of the handler.
	// Revocation status is memoized by key id (revokedCache) so a KMS/Vault provider is
	// queried at most once per distinct key across the whole export, not once per event.
	if enc := e.store.EncryptionConfig(); enc != nil && IsEncrypted(se.Metadata) {
		keyID := GetEncryptionKeyID(se.Metadata)
		revoked, cached := revokedCache[keyID]
		if !cached {
			if r, rerr := enc.IsRevoked(keyID); rerr == nil {
				revoked = r
				revokedCache[keyID] = r
			}
			// On an IsRevoked error, leave uncached and treat as not-revoked (fall through to
			// the ProcessStoredEvent path below, which handles ErrKeyRevoked/ErrKeyNotFound).
		}
		if revoked {
			e.logger.Info("event redacted: encrypted under a revoked key",
				"streamID", se.StreamID, "eventType", se.Type, "position", se.GlobalPosition)
			exported.Redacted = true
			exported.Data = nil
			// RawData keeps the (unrecoverable) ciphertext, consistent with the
			// ErrKeyRevoked/ErrKeyNotFound redaction branches below.
			return exported, nil
		}
	}

	event, err := e.store.ProcessStoredEvent(ctx, se)
	if err != nil {
		if errors.Is(err, ErrKeyRevoked) || errors.Is(err, ErrKeyNotFound) {
			e.logger.Info("event redacted due to key revocation",
				"streamID", se.StreamID, "eventType", se.Type,
				"position", se.GlobalPosition)
			exported.Redacted = true
			exported.Data = nil
			return exported, nil
		}

		if errors.Is(err, ErrDecryptionFailed) {
			e.logger.Warn("event redacted due to decryption failure",
				"streamID", se.StreamID, "eventType", se.Type,
				"position", se.GlobalPosition, "error", err)
			exported.Redacted = true
			exported.Data = nil
			return exported, nil
		}

		return ExportedEvent{}, NewExportError("",
			fmt.Errorf("failed to process event at position %d in stream %q: %w",
				se.GlobalPosition, se.StreamID, err))
	}

	exported.Data = event.Data
	// Re-serialize the decrypted/upcasted payload to plaintext JSON (RawData's
	// documented contract) rather than the on-disk, possibly-encrypted bytes.
	// json.Marshal handles both typed events and the generic map produced for
	// unregistered event types (SerializeEvent cannot serialize a bare map). On
	// the rare marshal failure, clear RawData and log — never leave ciphertext.
	if raw, err := json.Marshal(event.Data); err == nil {
		exported.RawData = raw
	} else {
		exported.RawData = nil
		e.logger.Warn("export: failed to re-serialize decrypted payload to JSON",
			"streamID", se.StreamID, "eventType", se.Type,
			"position", se.GlobalPosition, "error", err)
	}
	return exported, nil
}

func newExportedMetadata(m Metadata) ExportedMetadata {
	return ExportedMetadata{
		CorrelationID: m.CorrelationID,
		CausationID:   m.CausationID,
		TenantID:      m.TenantID,
		SchemaVersion: GetSchemaVersion(m),
	}
}

// Built-in export filters.
//
// An empty selector is never a wildcard in a GDPR export: FilterByTenantID(""),
// FilterByUserID(""), FilterByMetadata(k, ""), FilterByStreamPrefix(""),
// FilterByStreamCategory(""), FilterByStreams() and CombineFilters() all match NOTHING.
// Matching "events whose tenant is empty" would otherwise select most of the store (every
// event appended without that metadata) and hand it to a single data subject.

// matchNothing is the fail-closed filter every empty selector collapses to.
func matchNothing(StoredEvent) bool { return false }

// FilterByTenantID returns a filter that matches events with the given tenant ID.
// An empty tenantID matches nothing (it is not a wildcard for "events with no tenant").
func FilterByTenantID(tenantID string) ExportFilter {
	if tenantID == "" {
		return matchNothing
	}
	return func(event StoredEvent) bool {
		return event.Metadata.TenantID == tenantID
	}
}

// FilterByUserID returns a filter that matches events with the given user ID. Note that
// Metadata.UserID is ACTOR-scoped (who issued the command), not a data-subject footprint
// — use SubjectFilter for that. An empty userID matches nothing.
func FilterByUserID(userID string) ExportFilter {
	if userID == "" {
		return matchNothing
	}
	return func(event StoredEvent) bool {
		return event.Metadata.UserID == userID
	}
}

// FilterByStreamPrefix returns a filter that matches events from streams whose ID starts
// with the given prefix. It is a plain prefix match: "user-1" also matches "user-10" and
// "user-123". To select one aggregate end the prefix with the id separator ("user-1-"),
// or use FilterByStreams for exact ids / FilterByStreamCategory for a whole category.
// An empty prefix matches nothing.
func FilterByStreamPrefix(prefix string) ExportFilter {
	if prefix == "" {
		return matchNothing
	}
	return func(event StoredEvent) bool {
		return strings.HasPrefix(event.StreamID, prefix)
	}
}

// FilterByStreams returns a filter that matches events from exactly the given stream IDs
// (no prefix semantics). With no ids it matches nothing.
func FilterByStreams(ids ...string) ExportFilter {
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if id != "" {
			set[id] = struct{}{}
		}
	}
	if len(set) == 0 {
		return matchNothing
	}
	return func(event StoredEvent) bool {
		_, ok := set[event.StreamID]
		return ok
	}
}

// FilterByStreamCategory returns a filter that matches events whose stream category —
// the text before the first '-' in the stream ID (the whole ID when it has none) — equals
// category. Unlike FilterByStreamPrefix("user-") it cannot be confused by ids that merely
// start with the same letters. An empty category matches nothing.
func FilterByStreamCategory(category string) ExportFilter {
	if category == "" {
		return matchNothing
	}
	return func(event StoredEvent) bool {
		return streamCategory(event.StreamID) == category
	}
}

// FilterByMetadata returns a filter that matches events with a specific custom metadata
// key-value pair. An empty key or value matches nothing (it is not a wildcard for
// "events lacking that key").
func FilterByMetadata(key, value string) ExportFilter {
	if key == "" || value == "" {
		return matchNothing
	}
	return func(event StoredEvent) bool {
		if event.Metadata.Custom == nil {
			return false
		}
		return event.Metadata.Custom[key] == value
	}
}

// FilterByEventTypes returns a filter that matches events of any of the given types.
// With no types it matches nothing.
func FilterByEventTypes(types ...string) ExportFilter {
	typeSet := make(map[string]struct{}, len(types))
	for _, t := range types {
		typeSet[t] = struct{}{}
	}
	return func(event StoredEvent) bool {
		_, ok := typeSet[event.Type]
		return ok
	}
}

// SubjectOrUntaggedFilter returns a filter that admits events tagged for subjectID (see
// WithSubjectTagger / SubjectFilter) and events that carry no subject tags at all —
// which cannot be attributed to anyone and are exported as they always were — while
// dropping events tagged exclusively for OTHER subjects. It is the rule Export and
// ExportStream apply by default to a request with explicit Streams and no Filter, so a
// stream shared between subjects never leaks a co-subject's events. Prefer SubjectFilter
// when every event is tagged and untagged ones must be excluded too. An empty subjectID
// matches nothing.
func SubjectOrUntaggedFilter(subjectID string) ExportFilter {
	if subjectID == "" {
		return matchNothing
	}
	return func(event StoredEvent) bool {
		tags := GetSubjectTags(event.Metadata)
		return len(tags) == 0 || containsString(tags, subjectID)
	}
}

// CombineFilters returns a filter that matches events passing ALL provided filters (AND
// logic). With no filters it matches NOTHING — an empty conjunction is not a wildcard in
// a GDPR export — and a nil filter in the list fails closed (matches nothing) rather than
// being skipped.
func CombineFilters(filters ...ExportFilter) ExportFilter {
	if len(filters) == 0 {
		return matchNothing
	}
	return func(event StoredEvent) bool {
		for _, f := range filters {
			if f == nil || !f(event) {
				return false
			}
		}
		return true
	}
}
