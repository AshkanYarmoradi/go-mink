package mink

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/AshkanYarmoradi/go-mink/adapters"
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
	Streams []string

	// Filter selects which events to include.
	// When Streams is empty, the exporter scans all events and applies this filter
	// (requires the adapter to implement SubscriptionAdapter).
	// When Streams is provided, the filter is applied within each stream.
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

	// RawData is the serialized event payload after decryption (JSON bytes).
	// When Redacted is true, RawData contains the original (encrypted) bytes.
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
	if err := e.validateRequest(req); err != nil {
		return nil, err
	}

	result := &ExportResult{
		SubjectID:  req.SubjectID,
		ExportedAt: time.Now(),
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
func (e *DataExporter) ExportStream(ctx context.Context, req ExportRequest, handler ExportHandler) error {
	if err := e.validateRequest(req); err != nil {
		return err
	}
	if handler == nil {
		return NewExportError(req.SubjectID, fmt.Errorf("handler is required"))
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
	if len(req.Streams) > 0 {
		return e.exportFromStreams(ctx, req, handler)
	}
	return e.exportFromScan(ctx, req, handler)
}

// exportFromStreams loads events from specific streams.
func (e *DataExporter) exportFromStreams(ctx context.Context, req ExportRequest, handler ExportHandler) error {
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

			exported, err := e.processStoredEvent(ctx, se)
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
func (e *DataExporter) exportFromScan(ctx context.Context, req ExportRequest, handler ExportHandler) error {
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

			exported, err := e.processStoredEvent(ctx, se)
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
func (e *DataExporter) processStoredEvent(ctx context.Context, se StoredEvent) (ExportedEvent, error) {
	exported := ExportedEvent{
		StreamID:       se.StreamID,
		EventType:      se.Type,
		RawData:        se.Data,
		Metadata:       newExportedMetadata(se.Metadata),
		Version:        se.Version,
		GlobalPosition: se.GlobalPosition,
		Timestamp:      se.Timestamp,
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

// FilterByTenantID returns a filter that matches events with the given tenant ID.
func FilterByTenantID(tenantID string) ExportFilter {
	return func(event StoredEvent) bool {
		return event.Metadata.TenantID == tenantID
	}
}

// FilterByUserID returns a filter that matches events with the given user ID.
func FilterByUserID(userID string) ExportFilter {
	return func(event StoredEvent) bool {
		return event.Metadata.UserID == userID
	}
}

// FilterByStreamPrefix returns a filter that matches events from streams
// whose ID starts with the given prefix.
func FilterByStreamPrefix(prefix string) ExportFilter {
	return func(event StoredEvent) bool {
		return strings.HasPrefix(event.StreamID, prefix)
	}
}

// FilterByMetadata returns a filter that matches events with a specific
// custom metadata key-value pair.
func FilterByMetadata(key, value string) ExportFilter {
	return func(event StoredEvent) bool {
		if event.Metadata.Custom == nil {
			return false
		}
		return event.Metadata.Custom[key] == value
	}
}

// FilterByEventTypes returns a filter that matches events of any of the given types.
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

// CombineFilters returns a filter that matches events passing ALL provided filters (AND logic).
func CombineFilters(filters ...ExportFilter) ExportFilter {
	return func(event StoredEvent) bool {
		for _, f := range filters {
			if !f(event) {
				return false
			}
		}
		return true
	}
}
