package mink

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"go-mink.dev/adapters"
)

// SubjectTagsKey is the reserved Metadata.Custom key under which subject tags are
// recorded (a JSON array of subject ids). It is exported so adapters can implement a
// drift-free SubjectIndexAdapter by querying the events' own tags (e.g. a PostgreSQL
// JSONB query on metadata->'custom'->>'$subjects').
const SubjectTagsKey = "$subjects"

// subjectTagsKey is the unexported alias kept for internal readability.
const subjectTagsKey = SubjectTagsKey

// SubjectTagger derives the data-subject identifier(s) a freshly-appended event
// concerns, from its serialized data and metadata. The returned ids are recorded
// in Metadata.Custom so the subject's complete footprint can later be resolved for
// GDPR export/erasure. Returning nil tags nothing (zero overhead). Configure via
// WithSubjectTagger.
//
// data is the serialized event payload (the bytes being appended); applied at the
// single shared prepare-event hook, so it covers Append, SaveAggregate, and the
// outbox uniformly. Most taggers derive the subject from md (UserID/TenantID) or a
// known field within data.
type SubjectTagger func(eventType string, data []byte, md Metadata) []string

// setSubjectTags records subjects in Metadata.Custom (JSON array), merging with
// any already present, de-duplicated and order-preserving.
func setSubjectTags(m Metadata, subjects []string) Metadata {
	set := make(map[string]struct{})
	merged := make([]string, 0, len(subjects))
	for _, s := range append(GetSubjectTags(m), subjects...) {
		if s == "" {
			continue
		}
		if _, ok := set[s]; ok {
			continue
		}
		set[s] = struct{}{}
		merged = append(merged, s)
	}
	if len(merged) == 0 {
		return m
	}
	b, err := json.Marshal(merged)
	if err != nil {
		return m
	}
	return m.WithCustom(subjectTagsKey, string(b))
}

// replaceSubjectTags records the tagger-derived subjects in Metadata.Custom,
// REPLACING any caller-supplied tags rather than merging with them (the default
// append-time policy when a SubjectTagger is configured; see WithCallerSubjectTags
// for the opt-in merge). An empty subjects list leaves the event untagged — a
// caller must not be able to attribute an event to a subject the tagger did not
// derive. Copy-on-write: the caller's map is never mutated.
func replaceSubjectTags(m Metadata, subjects []string) Metadata {
	m = withoutCustomKeys(m, subjectTagsKey)
	if len(subjects) == 0 {
		return m
	}
	return setSubjectTags(m, subjects)
}

// GetSubjectTags returns the data-subject ids recorded on an event's metadata, or
// nil if none.
func GetSubjectTags(m Metadata) []string {
	if m.Custom == nil {
		return nil
	}
	v, ok := m.Custom[subjectTagsKey]
	if !ok {
		return nil
	}
	var subjects []string
	if err := json.Unmarshal([]byte(v), &subjects); err != nil {
		return nil
	}
	return subjects
}

// eventTagsSubject reports whether an event's metadata tags the given subject.
func eventTagsSubject(m Metadata, subjectID string) bool {
	for _, s := range GetSubjectTags(m) {
		if s == subjectID {
			return true
		}
	}
	return false
}

// SubjectFilter returns an ExportFilter matching events tagged with subjectID. It
// bridges subject tagging into the export/erasure scan model.
func SubjectFilter(subjectID string) ExportFilter {
	return func(e StoredEvent) bool {
		return eventTagsSubject(e.Metadata, subjectID)
	}
}

// SubjectFootprint describes the complete extent of a data subject's events. It
// drives complete-by-default export and erasure and doubles as an erasure preview.
type SubjectFootprint struct {
	SubjectID         string
	Streams           []string       // sorted, de-duplicated
	StreamEventCounts map[string]int // tagged events per stream
	EventCount        int            // total tagged events

	// SharedStreams lists the footprint streams (sorted, a subset of Streams) in which
	// the resolver observed an event tagged for a subject OTHER than this one — streams
	// shared with co-tenants (an order with buyer and seller, a conversation). Rows that
	// sibling stores key by such a stream id (outbox messages, audit / idempotency rows,
	// sagas correlated on it) cannot be attributed to this subject alone, so the built-in
	// SubjectErasable implementations purge and count only the EXCLUSIVE streams (see
	// ExclusiveStreams and SubjectFootprintIDs) and report the shared ones as skipped.
	// Untagged events do not make a stream shared; they make the footprint Partial.
	SharedStreams []string

	// KeyIDs lists the distinct master key ids (sorted) of the tagged events that carry a
	// COMPLETE field-encryption envelope (HasEncryptionEnvelope) — the keys an erasure
	// revokes. A tagged event with a bare "$encryption_key_id" but no envelope is
	// plaintext as far as crypto-shredding is concerned: its key is not listed here and
	// the event counts as CleartextEvents.
	KeyIDs []string

	// CleartextEvents is the number of tagged events that carry NO complete
	// field-encryption envelope (!HasEncryptionEnvelope). Crypto-shredding cannot reach
	// them (there is no key to revoke), so their payload stays readable after an erasure;
	// see ErasureResult.CleartextEvents.
	CleartextEvents int

	// Partial is true when completeness cannot be proven — e.g. the store contains
	// untagged (legacy) events that could belong to the subject. Callers MUST treat
	// a partial footprint as incomplete (never a silent partial).
	Partial bool
}

// ExclusiveStreams returns the footprint streams that are NOT shared with another
// subject (Streams minus SharedStreams), in Streams order. It is the stream set the
// built-in sibling-store erasers purge and count by; a nil footprint yields nil.
func (fp *SubjectFootprint) ExclusiveStreams() []string {
	if fp == nil || len(fp.Streams) == 0 {
		return nil
	}
	if len(fp.SharedStreams) == 0 {
		return fp.Streams
	}
	shared := make(map[string]struct{}, len(fp.SharedStreams))
	for _, s := range fp.SharedStreams {
		shared[s] = struct{}{}
	}
	out := make([]string, 0, len(fp.Streams))
	for _, s := range fp.Streams {
		if _, ok := shared[s]; !ok {
			out = append(out, s)
		}
	}
	return out
}

// SubjectIndexAdapter is an OPTIONAL extension that resolves a subject's streams from
// an index, avoiding a full scan. The event-store adapter MAY implement it, or an index
// can be injected into the resolver with WithResolverIndex; the resolver falls back to a
// scan otherwise.
type SubjectIndexAdapter interface {
	StreamsBySubject(ctx context.Context, subjectID string) ([]string, error)
}

// SubjectIndexWriter is the OPTIONAL write side of a subject index: it records which
// streams touch a subject so SubjectIndexAdapter can later resolve them without a scan.
// Wire one into the store with WithSubjectIndexWriter (populated at append time) and/or
// populate history with BackfillSubjectIndex. A type that implements both interfaces is
// a complete, keep-in-sync subject index (see MemorySubjectIndex).
type SubjectIndexWriter interface {
	// IndexSubjects records that streamID contains events for each of subjectIDs.
	// It MUST be idempotent (indexing the same (subject, stream) twice is a no-op).
	IndexSubjects(ctx context.Context, streamID string, subjectIDs []string) error
}

// SubjectResolver resolves a subject id to its complete footprint across all
// streams, using a subject index when available or a scan otherwise.
type SubjectResolver struct {
	store         *EventStore
	batchSize     int
	index         SubjectIndexAdapter
	authoritative bool
}

// SubjectResolverOption configures a SubjectResolver.
type SubjectResolverOption func(*SubjectResolver)

// WithResolverBatchSize sets the scan batch size (default 1000).
func WithResolverBatchSize(size int) SubjectResolverOption {
	return func(r *SubjectResolver) {
		if size > 0 {
			r.batchSize = size
		}
	}
}

// WithResolverIndex injects a subject index (read side) the resolver prefers over both
// the adapter's own index and a full scan — turning resolution into O(subject's events).
//
// By itself the index is treated as POSSIBLY INCOMPLETE: append-time index writes are
// best-effort (a failed write is logged, not fatal), so an index can silently drift
// behind the event log. A resolve that used the index therefore reports Partial=true
// unless you ALSO pass WithAuthoritativeIndex to assert the index is complete. This
// prevents an out-of-sync index from producing a falsely-complete footprint that would
// make Erase miss streams while certifying success.
func WithResolverIndex(idx SubjectIndexAdapter) SubjectResolverOption {
	return func(r *SubjectResolver) {
		if idx != nil {
			r.index = idx
		}
	}
}

// WithAuthoritativeIndex asserts that the injected index (WithResolverIndex) is complete
// — every stream touching a subject is recorded — so an index-backed resolve may report
// a non-partial footprint. Without it, an index-backed resolve is honestly marked Partial.
//
// An index-backed resolve never observes untagged events, so this assertion is the ONLY
// thing standing between a legacy (pre-tagging) event the backfill could not attribute
// and a footprint — and hence an erasure certificate — that claims completeness. Use it
// only when BOTH hold:
//
//   - the index was populated by BackfillSubjectIndexWithReport and the report shows
//     Untagged == 0 (every scanned event was attributed to at least one subject), or the
//     untagged events are known to carry no PII; an Undecryptable count means the tagger
//     never saw those events' encrypted fields, so their subjects were taken from existing
//     tags only; and
//   - no write has bypassed the index writer since (WithSubjectIndexWriter on every store
//     instance that appends, or a transactionally-consistent index such as the PostgreSQL
//     adapter's drift-free StreamsBySubject).
//
// Otherwise prefer the scan-backed resolver, which proves completeness by observing the
// untagged events itself.
func WithAuthoritativeIndex() SubjectResolverOption {
	return func(r *SubjectResolver) {
		r.authoritative = true
	}
}

// NewSubjectResolver creates a resolver for the given store.
func NewSubjectResolver(store *EventStore, opts ...SubjectResolverOption) *SubjectResolver {
	r := &SubjectResolver{store: store, batchSize: 1000}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Resolve returns the subject's footprint. It is read-only and therefore doubles
// as an erasure preview.
func (r *SubjectResolver) Resolve(ctx context.Context, subjectID string) (*SubjectFootprint, error) {
	if subjectID == "" {
		return nil, ErrSubjectIDRequired
	}

	fp := &SubjectFootprint{SubjectID: subjectID, StreamEventCounts: map[string]int{}}
	streamSet := map[string]struct{}{}
	sharedSet := map[string]struct{}{}
	keySet := map[string]struct{}{}

	// Index-backed fast path. An index is used ONLY when explicitly injected
	// (WithResolverIndex) — never auto-detected from the adapter — so a store gaining a
	// SubjectIndexAdapter never silently switches resolution away from the scan (which
	// can prove completeness by observing untagged events) to an index (which cannot).
	// Pass store.Adapter() to WithResolverIndex to opt into a drift-free adapter index.
	if idx := r.index; idx != nil {
		streams, err := idx.StreamsBySubject(ctx, subjectID)
		if err != nil {
			return nil, fmt.Errorf("mink: subject index for %q: %w", subjectID, err)
		}
		for _, streamID := range streams {
			stored, err := r.store.LoadRaw(ctx, streamID, 0)
			if err != nil {
				if errors.Is(err, ErrStreamNotFound) {
					continue
				}
				return nil, fmt.Errorf("mink: load stream %q for subject %q: %w", streamID, subjectID, err)
			}
			for _, se := range stored {
				if eventTagsSubject(se.Metadata, subjectID) {
					r.record(fp, streamSet, keySet, se)
				}
			}
			// The whole stream is in hand: note whether a co-tenant shares it.
			if _, mine := streamSet[streamID]; mine && streamSharedWithOthers(stored, subjectID) {
				sharedSet[streamID] = struct{}{}
			}
		}
		r.finalize(fp, streamSet, sharedSet, keySet)
		// An index only proves completeness when the caller asserts it is authoritative;
		// otherwise it may have drifted behind best-effort append-time writes, so the
		// footprint cannot be proven complete.
		fp.Partial = !r.authoritative
		return fp, nil
	}

	// Scan fallback.
	if _, ok := r.store.Adapter().(adapters.SubscriptionAdapter); !ok {
		return nil, ErrExportScanNotSupported
	}
	var position uint64
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		batch, err := r.store.LoadEventsFromPosition(ctx, position, r.batchSize)
		if err != nil {
			return nil, fmt.Errorf("mink: subject scan from %d: %w", position, err)
		}
		if len(batch) == 0 {
			break
		}
		for _, se := range batch {
			switch {
			case eventTagsSubject(se.Metadata, subjectID):
				r.record(fp, streamSet, keySet, se)
			case len(GetSubjectTags(se.Metadata)) == 0:
				// An untagged event: tagging was not universally applied, so the
				// footprint cannot be proven complete.
				fp.Partial = true
			}
		}
		position = batch[len(batch)-1].GlobalPosition
	}
	// A co-tenant's event can precede the subject's first event in a shared stream, so
	// sharing cannot be settled during the single pass without remembering every tagged
	// stream in the store. Re-read the footprint streams instead: O(the subject's events).
	if err := r.detectSharedStreams(ctx, subjectID, streamSet, sharedSet); err != nil {
		return nil, err
	}
	r.finalize(fp, streamSet, sharedSet, keySet)
	return fp, nil
}

// detectSharedStreams loads each footprint stream and records in sharedSet the ones in
// which another subject is tagged (see SubjectFootprint.SharedStreams).
func (r *SubjectResolver) detectSharedStreams(ctx context.Context, subjectID string, streamSet, sharedSet map[string]struct{}) error {
	for streamID := range streamSet {
		if err := ctx.Err(); err != nil {
			return err
		}
		stored, err := r.store.LoadRaw(ctx, streamID, 0)
		if err != nil {
			if errors.Is(err, ErrStreamNotFound) {
				continue
			}
			return fmt.Errorf("mink: load stream %q for subject %q: %w", streamID, subjectID, err)
		}
		if streamSharedWithOthers(stored, subjectID) {
			sharedSet[streamID] = struct{}{}
		}
	}
	return nil
}

// streamSharedWithOthers reports whether any event in the stream is tagged for a subject
// other than subjectID. Untagged events do not count: they make a footprint Partial, not
// a stream shared.
func streamSharedWithOthers(stored []StoredEvent, subjectID string) bool {
	for i := range stored {
		for _, s := range GetSubjectTags(stored[i].Metadata) {
			if s != subjectID {
				return true
			}
		}
	}
	return false
}

func (r *SubjectResolver) record(fp *SubjectFootprint, streamSet, keySet map[string]struct{}, se StoredEvent) {
	fp.EventCount++
	fp.StreamEventCounts[se.StreamID]++
	streamSet[se.StreamID] = struct{}{}
	// One predicate for "would revoking this event's key erase it?": only a complete
	// envelope names a key worth revoking; anything less is cleartext to an erasure.
	if HasEncryptionEnvelope(se.Metadata) {
		keySet[GetEncryptionKeyID(se.Metadata)] = struct{}{}
	} else {
		fp.CleartextEvents++
	}
}

func (r *SubjectResolver) finalize(fp *SubjectFootprint, streamSet, sharedSet, keySet map[string]struct{}) {
	fp.Streams = sortedSet(streamSet)
	fp.SharedStreams = sortedSet(sharedSet)
	fp.KeyIDs = sortedSet(keySet)
}
