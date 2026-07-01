package mink

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"go-mink.dev/adapters"
	"go-mink.dev/encryption"
)

// DataEraser performs GDPR right-to-erasure (Article 17) by crypto-shredding the
// encryption keys behind a data subject's events. It is the erasure counterpart to
// DataExporter and shares the same subject-targeting model (Streams / Filter).
//
// Erasure revokes the master key(s) under which the subject's PII was encrypted —
// rendering those fields permanently unrecoverable — then optionally appends an
// append-only marker event recording that the erasure occurred. It never deletes
// or mutates historical event rows.
//
// IMPORTANT: crypto-shredding erases ALL data encrypted under a revoked key.
// Subject-scoped erasure therefore requires per-subject keys; with per-tenant keys
// (WithTenantKeyResolver) revoking a key erases the whole tenant. ErasureResult
// reports exactly which keys were revoked so the caller can verify the blast radius.
type DataEraser struct {
	store        *EventStore
	batchSize    int
	logger       Logger
	markerStream string
	resolver     *SubjectResolver
	redactors    []SubjectRedactable
	rebuilders   []ReadModelRebuilder
	certSink     CertificateSink
	hooks        []ErasureHook
}

// DataEraserOption configures a DataEraser.
type DataEraserOption func(*DataEraser)

// WithEraseBatchSize sets the number of events loaded per batch during scan-based
// key discovery. Default is 1000.
func WithEraseBatchSize(size int) DataEraserOption {
	return func(e *DataEraser) {
		if size > 0 {
			e.batchSize = size
		}
	}
}

// WithEraseLogger sets the logger for the data eraser.
func WithEraseLogger(l Logger) DataEraserOption {
	return func(e *DataEraser) {
		if l != nil {
			e.logger = l
		}
	}
}

// WithErasureMarker makes Erase append an append-only ErasureMarker event to the
// given stream after a successful erasure, recording that the subject was erased
// (subject reference, timestamp, and key count — never the erased PII fields).
// Register ErasureMarker with the store's serializer to load markers back. Leave
// unset to skip.
func WithErasureMarker(streamID string) DataEraserOption {
	return func(e *DataEraser) {
		e.markerStream = streamID
	}
}

// WithEraseSubjectResolver configures a SubjectResolver so a SubjectID-only
// ErasureRequest (no Streams/Filter/KeyIDs) is automatically resolved to the
// subject's complete footprint. A partial footprint propagates to
// ErasureResult.Partial — never a silent partial.
func WithEraseSubjectResolver(r *SubjectResolver) DataEraserOption {
	return func(e *DataEraser) {
		e.resolver = r
	}
}

// WithReadModelRedactor registers in-place read-model redactors. After shredding
// the subject's keys, Erase calls each redactor's RedactSubject so PII does not
// survive on the read side. Preferred over a rebuild when available.
func WithReadModelRedactor(redactors ...SubjectRedactable) DataEraserOption {
	return func(e *DataEraser) {
		e.redactors = append(e.redactors, redactors...)
	}
}

// WithReadModelRebuilder registers projection rebuilders. After shredding, Erase
// triggers each one so crypto-shredded events re-materialize as redacted. Use this
// when a read model has no in-place SubjectRedactable hook.
func WithReadModelRebuilder(rebuilders ...ReadModelRebuilder) DataEraserOption {
	return func(e *DataEraser) {
		e.rebuilders = append(e.rebuilders, rebuilders...)
	}
}

// WithCertificateSink makes Erase verify the erasure and emit a PII-free
// ErasureCertificate to the sink (e.g. an AuditStore) for Article 17
// accountability. Sink failure is non-fatal (recorded in ErasureResult.Errors).
func WithCertificateSink(sink CertificateSink) DataEraserOption {
	return func(e *DataEraser) {
		e.certSink = sink
	}
}

// WithErasureHook registers side-effect hooks run during Erase to clean PII the
// application owns outside the event store (blob storage, exports, caches, search
// indexes). Failures are reported on ErasureResult, not fatal.
func WithErasureHook(hooks ...ErasureHook) DataEraserOption {
	return func(e *DataEraser) {
		e.hooks = append(e.hooks, hooks...)
	}
}

// NewDataEraser creates a new DataEraser for the given event store.
func NewDataEraser(store *EventStore, opts ...DataEraserOption) *DataEraser {
	e := &DataEraser{
		store:     store,
		batchSize: 1000,
		logger:    &noopLogger{},
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// ErasureRequest describes which data subject to erase. It mirrors ExportRequest.
type ErasureRequest struct {
	// SubjectID identifies the data subject (required). It is a reference/identifier
	// used for the audit marker, not the erased personal data itself.
	SubjectID string

	// Streams lists specific stream IDs whose events identify the keys to revoke.
	Streams []string

	// Filter selects which events identify the keys to revoke. When Streams is empty
	// and Filter is set, the eraser scans all events (requires the adapter to
	// implement SubscriptionAdapter).
	Filter ExportFilter

	// KeyIDs explicitly names master keys to revoke. They are revoked in addition to
	// any keys discovered from matched events; when set with no Streams/Filter, the
	// keys are revoked directly without scanning.
	KeyIDs []string

	// FromTime / ToTime bound the events considered during key discovery.
	FromTime *time.Time
	ToTime   *time.Time
}

// ErasureResult reports the outcome of an erasure.
type ErasureResult struct {
	// SubjectID is the data subject identifier from the request.
	SubjectID string

	// KeysRevoked lists the master keys that were successfully revoked
	// (sorted, de-duplicated).
	KeysRevoked []string

	// Streams lists the streams that contained matched events (sorted).
	Streams []string

	// EventsScanned is the number of events examined during key discovery.
	EventsScanned int

	// MarkerWritten reports whether an erasure-marker event was appended.
	MarkerWritten bool

	// Partial is true when the erasure was driven by a subject footprint that could
	// not be proven complete (e.g. legacy untagged events). The caller MUST treat
	// the erasure as incomplete and remediate (e.g. backfill subject tags).
	Partial bool

	// RedactedReadModels lists read models redacted in place or rebuilt to redacted.
	RedactedReadModels []string

	// ResidualReadModels lists read models that could not be redacted and may still
	// hold the subject's PII (residual risk; see Errors for the cause).
	ResidualReadModels []string

	// SideEffects lists external-PII erasure hooks that ran successfully.
	SideEffects []string

	// ErasedAt is when the erasure was performed.
	ErasedAt time.Time

	// Errors holds non-fatal per-key / marker failures (partial-failure contract).
	Errors []error
}

// ErasureMarker is the default payload appended when WithErasureMarker is set. It
// records that an erasure occurred WITHOUT carrying any erased PII.
type ErasureMarker struct {
	SubjectID   string    `json:"subjectId"`
	ErasedAt    time.Time `json:"erasedAt"`
	KeysRevoked int       `json:"keysRevoked"`
}

// Erase crypto-shreds the data subject identified by req and returns a result. It
// is idempotent: re-erasing an already-erased subject revokes already-revoked keys
// as a no-op and succeeds. Partial failures (a key that fails to revoke, a marker
// that fails to append) are reported in ErasureResult.Errors, not returned fatally.
// A provider that does not support revocation, and a store without field
// encryption, are returned as typed (fatal) errors.
func (e *DataEraser) Erase(ctx context.Context, req ErasureRequest) (*ErasureResult, error) {
	if req.SubjectID == "" {
		return nil, ErrErasureSubjectRequired
	}

	cfg := e.store.EncryptionConfig()
	if cfg == nil {
		return nil, NewErasureError(req.SubjectID, ErrErasureNotConfigured)
	}

	result := &ErasureResult{SubjectID: req.SubjectID, ErasedAt: time.Now()}
	keySet := make(map[string]struct{}, len(req.KeyIDs))
	streamSet := make(map[string]struct{})

	// A SubjectID-only request resolves to the subject's complete footprint (keys +
	// streams). A partial footprint propagates to the result — never silently.
	resolved := false
	if e.resolver != nil && len(req.Streams) == 0 && req.Filter == nil && len(req.KeyIDs) == 0 {
		fp, err := e.resolver.Resolve(ctx, req.SubjectID)
		if err != nil {
			return nil, NewErasureError(req.SubjectID, err)
		}
		for _, k := range fp.KeyIDs {
			keySet[k] = struct{}{}
		}
		for _, s := range fp.Streams {
			streamSet[s] = struct{}{}
		}
		result.EventsScanned = fp.EventCount
		result.Partial = fp.Partial
		resolved = true
	}
	if !resolved {
		if err := e.validateRequest(req); err != nil {
			return nil, err
		}
	}

	// Discover the keys to revoke: explicit KeyIDs plus keys found on matched events.
	for _, k := range req.KeyIDs {
		if k != "" {
			keySet[k] = struct{}{}
		}
	}
	if len(req.Streams) > 0 || req.Filter != nil {
		if err := e.discoverKeys(ctx, req, func(se StoredEvent) {
			result.EventsScanned++
			streamSet[se.StreamID] = struct{}{}
			if keyID := GetEncryptionKeyID(se.Metadata); keyID != "" {
				keySet[keyID] = struct{}{}
			}
		}); err != nil {
			return nil, err
		}
	}

	// Revoke each distinct key (idempotent). An unsupported provider is fatal —
	// nothing can be crypto-shredded.
	for keyID := range keySet {
		if err := cfg.RevokeKey(keyID); err != nil {
			if errors.Is(err, encryption.ErrRevocationUnsupported) {
				return nil, NewErasureError(req.SubjectID, encryption.ErrRevocationUnsupported)
			}
			result.Errors = append(result.Errors, fmt.Errorf("revoke key %q: %w", keyID, err))
			continue
		}
		result.KeysRevoked = append(result.KeysRevoked, keyID)
	}
	sort.Strings(result.KeysRevoked)
	result.Streams = sortedSet(streamSet)

	// Propagate erasure to read models so PII does not survive on the read side.
	e.redactReadModels(ctx, req.SubjectID, result)

	// Run side-effect hooks (PII the app owns outside the event store).
	e.runErasureHooks(ctx, req.SubjectID, result)

	// 3. Optional append-only marker (non-fatal on failure).
	if e.markerStream != "" {
		if err := e.appendMarker(ctx, req.SubjectID, result); err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("append erasure marker: %w", err))
		} else {
			result.MarkerWritten = true
		}
	}

	// 4. Optional verification certificate (non-fatal on failure).
	if e.certSink != nil {
		e.emitCertificate(ctx, req.SubjectID, result)
	}

	return result, nil
}

func (e *DataEraser) validateRequest(req ErasureRequest) error {
	if req.SubjectID == "" {
		return ErrErasureSubjectRequired
	}
	if len(req.Streams) == 0 && req.Filter == nil && len(req.KeyIDs) == 0 {
		return ErrNoErasureSources
	}
	return nil
}

// discoverKeys walks the subject's matched events (by stream or scan) and calls fn
// for each, mirroring DataExporter's enumeration so the two share one subject model.
func (e *DataEraser) discoverKeys(ctx context.Context, req ErasureRequest, fn func(StoredEvent)) error {
	if len(req.Streams) > 0 {
		for _, streamID := range req.Streams {
			if err := ctx.Err(); err != nil {
				return err
			}
			stored, err := e.store.LoadRaw(ctx, streamID, 0)
			if err != nil {
				if errors.Is(err, ErrStreamNotFound) {
					e.logger.Warn("stream not found during erasure, skipping",
						"streamID", streamID, "subjectID", req.SubjectID)
					continue
				}
				return NewErasureError(req.SubjectID, fmt.Errorf("failed to load stream %q: %w", streamID, err))
			}
			for _, se := range stored {
				if e.matches(se, req) {
					fn(se)
				}
			}
		}
		return nil
	}

	// Scan-based discovery.
	if _, ok := e.store.Adapter().(adapters.SubscriptionAdapter); !ok {
		return NewErasureError(req.SubjectID, ErrErasureScanNotSupported)
	}
	var position uint64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		batch, err := e.store.LoadEventsFromPosition(ctx, position, e.batchSize)
		if err != nil {
			return NewErasureError(req.SubjectID,
				fmt.Errorf("failed to scan events from position %d: %w", position, err))
		}
		if len(batch) == 0 {
			break
		}
		for _, se := range batch {
			if e.matches(se, req) {
				fn(se)
			}
		}
		position = batch[len(batch)-1].GlobalPosition
	}
	return nil
}

func (e *DataEraser) matches(se StoredEvent, req ErasureRequest) bool {
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

func (e *DataEraser) appendMarker(ctx context.Context, subjectID string, result *ErasureResult) error {
	marker := ErasureMarker{
		SubjectID:   subjectID,
		ErasedAt:    result.ErasedAt,
		KeysRevoked: len(result.KeysRevoked),
	}
	return e.store.Append(ctx, e.markerStream, []interface{}{marker})
}

// redactReadModels runs the configured in-place redactors then rebuilders,
// recording redacted and residual-PII read models on the result. Failures are
// non-fatal (partial-failure contract).
func (e *DataEraser) redactReadModels(ctx context.Context, subjectID string, result *ErasureResult) {
	for _, r := range e.redactors {
		if err := r.RedactSubject(ctx, subjectID); err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("redact read model %q: %w", r.ReadModelName(), err))
			result.ResidualReadModels = append(result.ResidualReadModels, r.ReadModelName())
			continue
		}
		result.RedactedReadModels = append(result.RedactedReadModels, r.ReadModelName())
	}
	for _, b := range e.rebuilders {
		if err := b.Rebuild(ctx); err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("rebuild read model %q: %w", b.Name, err))
			result.ResidualReadModels = append(result.ResidualReadModels, b.Name)
			continue
		}
		result.RedactedReadModels = append(result.RedactedReadModels, b.Name)
	}
}

// runErasureHooks runs the configured external-PII hooks, recording successes on
// the result. Failures are non-fatal (partial-failure contract).
func (e *DataEraser) runErasureHooks(ctx context.Context, subjectID string, result *ErasureResult) {
	if len(e.hooks) == 0 {
		return
	}
	ec := ErasureContext{SubjectID: subjectID, Streams: result.Streams, KeysRevoked: result.KeysRevoked}
	for _, h := range e.hooks {
		if err := h.Run(ctx, ec); err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("erasure hook %q: %w", h.Name, err))
			continue
		}
		result.SideEffects = append(result.SideEffects, h.Name)
	}
}

func sortedSet(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
