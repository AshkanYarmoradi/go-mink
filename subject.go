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

// eventTagsSubject reports whether an event's metadata tags the given subject (persisted
// "$subjects" tags only; see EventStore.attributeSubjects for the rule the footprint
// consumers use, which also derives untagged events through the tagger).
func eventTagsSubject(m Metadata, subjectID string) bool {
	for _, s := range GetSubjectTags(m) {
		if s == subjectID {
			return true
		}
	}
	return false
}

// SubjectFilter returns an ExportFilter matching events whose PERSISTED "$subjects" tags
// name subjectID. It bridges subject tagging into the export/erasure scan model.
//
// It reads tags only — it has no store or context to run a SubjectTagger — so an
// untagged event the tagger could derive the subject from does not match. The
// SubjectResolver, DataEraser and the exporter's own default filters (a resolved
// footprint, or explicit Streams with no Filter) attribute such events through the
// store's tagger instead; pass SubjectFilter explicitly only when persisted tags are the
// intended scope.
func SubjectFilter(subjectID string) ExportFilter {
	return func(e StoredEvent) bool {
		return eventTagsSubject(e.Metadata, subjectID)
	}
}

// subjectAttribution is the outcome of attributing one stored event to its data
// subject(s) (EventStore.attributeSubjects).
type subjectAttribution struct {
	// subjects the event is attributed to: its persisted "$subjects" tags, or — for an
	// untagged event — what the tagger derived from its plaintext. Empty when the event
	// could not be attributed to anyone.
	subjects []string
	// derived is true when subjects came from the tagger rather than persisted tags.
	derived bool
	// undecryptable is true when the event is untagged and field-encrypted but could not
	// be decrypted, so it was never handed to the tagger (subjects is then empty).
	undecryptable bool
}

// attributed reports whether the event could be attributed to at least one subject.
func (a subjectAttribution) attributed() bool { return len(a.subjects) > 0 }

// to reports whether the event is attributed to subjectID.
func (a subjectAttribution) to(subjectID string) bool { return containsString(a.subjects, subjectID) }

// toOther reports whether the event is attributed to at least one subject other than
// subjectID (possibly in addition to subjectID).
func (a subjectAttribution) toOther(subjectID string) bool {
	for _, s := range a.subjects {
		if s != subjectID {
			return true
		}
	}
	return false
}

// attributeSubjects attributes one stored event to its data subject(s). It is the ONE
// rule every footprint consumer uses — SubjectResolver (index and scan paths,
// shared-stream detection), DataEraser (key discovery, the shared-key guard,
// verification and the certificate), DataExporter's default filters and
// BackfillSubjectIndex — so an event cannot be indexed, exported, shredded or verified
// under different rules.
//
// Persisted "$subjects" tags win: an event that carries them is attributed to exactly
// those subjects and the tagger is not consulted, so attribution costs nothing extra
// when every event is tagged. An UNTAGGED event is attributed by running tagger over
// it exactly as the append path would have: a field-encrypted event is decrypted first
// through the store's own decrypt path (DecryptStoredEvent, WithDecryptionErrorHandler
// honored) and the tagger receives its plaintext with the encryption envelope stripped
// (the stored payload as written — upcasters are not applied). An encrypted event that
// cannot be decrypted — its key is revoked, the handler swallowed the failure and left
// the ciphertext in place, or the store has no encryption configured — is NEVER handed
// to the tagger: running a tagger over ciphertext, or over a payload missing its
// encrypted fields, would silently mis-attribute. Such an event is reported
// undecryptable and unattributed; so is one the tagger derives nothing for. Note the
// consequence: a derived-only (untagged) encrypted event can no longer be attributed
// once its key is revoked, which is why Erase verifies the events it matched BEFORE
// revoking (see erasureMatches).
//
// A nil tagger means the store's own SubjectTagger (WithSubjectTagger), so attribution
// matches the append-time rule with no extra configuration; with no tagger at all,
// untagged events are simply unattributed (persisted tags only, as before).
func (s *EventStore) attributeSubjects(ctx context.Context, tagger SubjectTagger, se StoredEvent) subjectAttribution {
	if tags := GetSubjectTags(se.Metadata); len(tags) > 0 {
		return subjectAttribution{subjects: tags}
	}
	if tagger == nil {
		tagger = s.subjectTagger
	}
	if tagger == nil {
		return subjectAttribution{}
	}
	data, md, ok := s.taggerPlaintext(ctx, se)
	if !ok {
		return subjectAttribution{undecryptable: true}
	}
	derived := cleanSubjects(tagger(se.Type, data, md))
	if len(derived) == 0 {
		return subjectAttribution{}
	}
	return subjectAttribution{subjects: derived, derived: true}
}

// cleanSubjects returns a FRESH copy of a tagger's output with empty ids dropped and
// duplicates removed, order-preserving. A fresh slice is built rather than the tagger's
// own: the tagger's slice-ownership is unspecified, so retaining or appending onto it
// could alias a buffer it reuses.
func cleanSubjects(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// taggerPlaintext returns an event's data and metadata as a SubjectTagger expects them:
// decrypted, with the encryption envelope stripped — what the live append path hands
// the tagger before encrypting. A plaintext event passes through untouched. ok is
// false when the event is field-encrypted but cannot be decrypted: the store has no
// encryption configured, the decrypt path failed, or its WithDecryptionErrorHandler
// swallowed a revoked-key failure and left the ciphertext in place (DecryptStoredEvent
// then keeps the envelope, which is how that no-op is detected).
func (s *EventStore) taggerPlaintext(ctx context.Context, se StoredEvent) ([]byte, Metadata, bool) {
	if !IsEncrypted(se.Metadata) {
		return se.Data, se.Metadata, true
	}
	if s.encryption == nil {
		return nil, Metadata{}, false
	}
	dec, err := s.DecryptStoredEvent(ctx, se)
	if err != nil || IsEncrypted(dec.Metadata) {
		return nil, Metadata{}, false
	}
	return dec.Data, dec.Metadata, true
}

// streamSharedWithOthers reports whether any event in the stream is attributed (by
// attributeSubjects: persisted tag, or derived by tagger) to a subject other than
// subjectID. Unattributable events do not count: they make a footprint Partial, not a
// stream shared.
func (s *EventStore) streamSharedWithOthers(ctx context.Context, tagger SubjectTagger, stored []StoredEvent, subjectID string) bool {
	for i := range stored {
		if s.attributeSubjects(ctx, tagger, stored[i]).toOther(subjectID) {
			return true
		}
	}
	return false
}

// SubjectFootprint describes the complete extent of a data subject's events. It
// drives complete-by-default export and erasure and doubles as an erasure preview.
//
// An event belongs to the footprint when it is ATTRIBUTED to the subject: it carries the
// subject's persisted "$subjects" tag, or — when it carries no tag at all — the
// SubjectTagger (the store's, or WithResolverTagger's) derives the subject from its
// plaintext, exactly as the append path would have. One rule decides this for the
// resolver, DataEraser, DataExporter and BackfillSubjectIndex alike, so a derived-only
// historical event is indexed, exported, shredded and verified consistently.
type SubjectFootprint struct {
	SubjectID         string
	Streams           []string       // sorted, de-duplicated
	StreamEventCounts map[string]int // attributed events per stream
	EventCount        int            // total attributed events

	// SharedStreams lists the footprint streams (sorted, a subset of Streams) in which
	// the resolver observed an event attributed (tagged, or tagger-derived) to a subject
	// OTHER than this one — streams shared with co-tenants (an order with buyer and
	// seller, a conversation). Rows that sibling stores key by such a stream id (outbox
	// messages, audit / idempotency rows, sagas correlated on it) cannot be attributed to
	// this subject alone, so the built-in SubjectErasable implementations purge and count
	// only the EXCLUSIVE streams (see ExclusiveStreams and SubjectFootprintIDs) and report
	// the shared ones as skipped. Unattributable events do not make a stream shared; they
	// make the footprint Partial.
	SharedStreams []string

	// KeyIDs lists the distinct master key ids (sorted) of the attributed events that
	// carry a COMPLETE field-encryption envelope (HasEncryptionEnvelope) — the keys an
	// erasure revokes. An attributed event with a bare "$encryption_key_id" but no
	// envelope is plaintext as far as crypto-shredding is concerned: its key is not listed
	// here and the event counts as CleartextEvents.
	KeyIDs []string

	// CleartextEvents is the number of attributed events that carry NO complete
	// field-encryption envelope (!HasEncryptionEnvelope). Crypto-shredding cannot reach
	// them (there is no key to revoke), so their payload stays readable after an erasure;
	// see ErasureResult.CleartextEvents.
	CleartextEvents int

	// Partial is true when completeness cannot be proven — e.g. the store contains
	// events that carry no subject tag and that the tagger could not attribute to anyone
	// (no tagger configured, the tagger derived nothing, or the event is encrypted and
	// undecryptable), any of which could belong to the subject. Callers MUST treat a
	// partial footprint as incomplete (never a silent partial).
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
	tagger        SubjectTagger // nil → the store's own SubjectTagger
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
// An index-backed resolve never observes the events outside the indexed streams, so this
// assertion is the ONLY thing standing between a legacy (pre-tagging) event the backfill
// could not attribute and a footprint — and hence an erasure certificate — that claims
// completeness. Use it only when ALL hold:
//
//   - the index was populated by BackfillSubjectIndexWithReport and the report shows
//     Untagged == 0 (every scanned event was attributed to at least one subject, by its
//     persisted tags or by the tagger), or the untagged events are known to carry no PII;
//     an Undecryptable count means untagged encrypted events the tagger could not see,
//     each of them also counted as Untagged;
//   - the resolver attributes untagged events with the SAME tagger the backfill used —
//     the store's own SubjectTagger by default, or the one passed to WithResolverTagger —
//     since an event the backfill indexed under a derived subject is found in the footprint
//     only if the resolver derives it the same way; and
//   - no write has bypassed the index writer since (WithSubjectIndexWriter on every store
//     instance that appends, or a transactionally-consistent index such as the PostgreSQL
//     adapter's drift-free StreamsBySubject — which indexes PERSISTED tags only, so a
//     derived-only historical event is reachable through it only via the scan or a
//     backfilled index).
//
// Otherwise prefer the scan-backed resolver, which proves completeness by observing the
// unattributable events itself.
func WithAuthoritativeIndex() SubjectResolverOption {
	return func(r *SubjectResolver) {
		r.authoritative = true
	}
}

// WithResolverTagger sets the SubjectTagger the resolver uses to attribute events that
// carry no persisted "$subjects" tag (events written before tagging was enabled, or by a
// store without a tagger). By default the resolver uses the store's own SubjectTagger
// (WithSubjectTagger), so attribution matches the append-time rule with no extra
// configuration; pass a tagger here when resolving through a store that has none, or to
// use the tagger a BackfillSubjectIndex run was made with. The tagger runs only for
// untagged events (zero overhead when every event is tagged) and sees each one exactly as
// the append path would have — decrypted, envelope stripped; an undecryptable encrypted
// event is never handed to it and stays unattributed. A DataEraser or DataExporter
// configured with this resolver attributes with the same tagger. A nil tagger is ignored.
func WithResolverTagger(tagger SubjectTagger) SubjectResolverOption {
	return func(r *SubjectResolver) {
		if tagger != nil {
			r.tagger = tagger
		}
	}
}

// attribute attributes se with the resolver's tagger (EventStore.attributeSubjects).
func (r *SubjectResolver) attribute(ctx context.Context, se StoredEvent) subjectAttribution {
	return r.store.attributeSubjects(ctx, r.tagger, se)
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
//
// An event is the subject's when attributeSubjects attributes it to the subject: by its
// persisted "$subjects" tag or, for an untagged event, by the tagger (the store's, or
// WithResolverTagger's) run over its plaintext. An untagged event the tagger cannot
// attribute to anyone makes the footprint Partial on the scan path; on the index path
// Partial is decided by WithAuthoritativeIndex alone (see there for what that assertion
// requires).
func (r *SubjectResolver) Resolve(ctx context.Context, subjectID string) (*SubjectFootprint, error) {
	return r.resolve(ctx, subjectID, nil)
}

// resolve is Resolve recording, when matched is non-nil, every event it attributes to
// the subject — so DataEraser can verify exactly the events it is about to shred, whose
// derived attribution revocation may destroy (see erasureMatches).
func (r *SubjectResolver) resolve(ctx context.Context, subjectID string, matched erasureMatches) (*SubjectFootprint, error) {
	if subjectID == "" {
		return nil, ErrSubjectIDRequired
	}
	b := newFootprintBuilder(subjectID, matched)

	// Index-backed fast path. An index is used ONLY when explicitly injected
	// (WithResolverIndex) — never auto-detected from the adapter — so a store gaining a
	// SubjectIndexAdapter never silently switches resolution away from the scan (which
	// can prove completeness by observing unattributable events) to an index (which
	// cannot). Pass store.Adapter() to WithResolverIndex to opt into a drift-free adapter
	// index.
	if r.index != nil {
		return r.resolveByIndex(ctx, subjectID, b)
	}

	// Scan fallback.
	return r.resolveByScan(ctx, subjectID, b)
}

// resolveByIndex is the index-backed half of resolve: it loads every stream the index
// lists for the subject and attributes each of their events once (collectIndexedStream).
// An index only proves completeness when the caller asserts it is authoritative;
// otherwise it may have drifted behind best-effort append-time writes, so the footprint
// cannot be proven complete.
func (r *SubjectResolver) resolveByIndex(ctx context.Context, subjectID string, b *footprintBuilder) (*SubjectFootprint, error) {
	streams, err := r.index.StreamsBySubject(ctx, subjectID)
	if err != nil {
		return nil, fmt.Errorf("mink: subject index for %q: %w", subjectID, err)
	}
	for _, streamID := range streams {
		stored, found, err := r.loadFootprintStream(ctx, subjectID, streamID)
		if err != nil {
			return nil, err
		}
		if !found {
			continue
		}
		r.collectIndexedStream(ctx, subjectID, streamID, stored, b)
	}
	fp := b.finalize()
	fp.Partial = !r.authoritative
	return fp, nil
}

// collectIndexedStream handles one indexed stream, which is in hand whole: it attributes
// each event once, recording the subject's own and noting whether a co-tenant shares
// the stream. The stream is marked shared only when it is also the subject's — the
// index may list a stream none of whose events attribute to the subject any more.
func (r *SubjectResolver) collectIndexedStream(ctx context.Context, subjectID, streamID string, stored []StoredEvent, b *footprintBuilder) {
	shared := false
	for _, se := range stored {
		at := r.attribute(ctx, se)
		if at.to(subjectID) {
			b.record(se)
		}
		if at.toOther(subjectID) {
			shared = true
		}
	}
	if _, mine := b.streamSet[streamID]; mine && shared {
		b.sharedSet[streamID] = struct{}{}
	}
}

// resolveByScan is the scan-based half of resolve: it walks the whole store (which
// needs a SubscriptionAdapter), attributing every event (attributeBatch), and then
// settles which footprint streams are shared with other subjects.
func (r *SubjectResolver) resolveByScan(ctx context.Context, subjectID string, b *footprintBuilder) (*SubjectFootprint, error) {
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
		r.attributeBatch(ctx, subjectID, batch, b)
		position = batch[len(batch)-1].GlobalPosition
	}
	// A co-tenant's event can precede the subject's first event in a shared stream, so
	// sharing cannot be settled during the single pass without remembering every
	// attributed stream in the store. Re-read the footprint streams instead: O(the
	// subject's events).
	if err := r.detectSharedStreams(ctx, subjectID, b); err != nil {
		return nil, err
	}
	return b.finalize(), nil
}

// attributeBatch attributes one scan batch: the subject's events are recorded; an event
// neither a tag nor the tagger attributes to anyone could be the subject's, so it makes
// the footprint Partial (it cannot be proven complete).
func (r *SubjectResolver) attributeBatch(ctx context.Context, subjectID string, batch []StoredEvent, b *footprintBuilder) {
	for _, se := range batch {
		at := r.attribute(ctx, se)
		switch {
		case at.to(subjectID):
			b.record(se)
		case !at.attributed():
			b.fp.Partial = true
		}
	}
}

// detectSharedStreams loads each footprint stream and records in b.sharedSet the ones
// in which another subject is attributed (see SubjectFootprint.SharedStreams).
func (r *SubjectResolver) detectSharedStreams(ctx context.Context, subjectID string, b *footprintBuilder) error {
	for streamID := range b.streamSet {
		if err := ctx.Err(); err != nil {
			return err
		}
		stored, found, err := r.loadFootprintStream(ctx, subjectID, streamID)
		if err != nil {
			return err
		}
		if !found {
			continue
		}
		if r.store.streamSharedWithOthers(ctx, r.tagger, stored, subjectID) {
			b.sharedSet[streamID] = struct{}{}
		}
	}
	return nil
}

// loadFootprintStream loads one of the subject's streams for resolution. A stream that
// no longer exists is not an error: it is reported as found == false so the caller
// skips it; any other load failure is wrapped with the stream and subject it concerned.
func (r *SubjectResolver) loadFootprintStream(ctx context.Context, subjectID, streamID string) (stored []StoredEvent, found bool, err error) {
	stored, err = r.store.LoadRaw(ctx, streamID, 0)
	if err != nil {
		if errors.Is(err, ErrStreamNotFound) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("mink: load stream %q for subject %q: %w", streamID, subjectID, err)
	}
	return stored, true, nil
}

// footprintBuilder accumulates a SubjectFootprint while resolve walks the subject's
// events (by index or by scan): the footprint under construction plus the stream, shared
// stream and key sets that finalize sorts into it, and the erasureMatches memo (nil for
// a plain Resolve) that records every event attributed to the subject.
type footprintBuilder struct {
	fp        *SubjectFootprint
	streamSet map[string]struct{}
	sharedSet map[string]struct{}
	keySet    map[string]struct{}
	matched   erasureMatches
}

// newFootprintBuilder starts an empty footprint for subjectID.
func newFootprintBuilder(subjectID string, matched erasureMatches) *footprintBuilder {
	return &footprintBuilder{
		fp:        &SubjectFootprint{SubjectID: subjectID, StreamEventCounts: map[string]int{}},
		streamSet: map[string]struct{}{},
		sharedSet: map[string]struct{}{},
		keySet:    map[string]struct{}{},
		matched:   matched,
	}
}

// record adds one event attributed to the subject to the footprint and the memo.
func (b *footprintBuilder) record(se StoredEvent) {
	b.matched.add(se)
	b.fp.EventCount++
	b.fp.StreamEventCounts[se.StreamID]++
	b.streamSet[se.StreamID] = struct{}{}
	// One predicate for "would revoking this event's key erase it?": only a complete
	// envelope names a key worth revoking; anything less is cleartext to an erasure.
	if HasEncryptionEnvelope(se.Metadata) {
		b.keySet[GetEncryptionKeyID(se.Metadata)] = struct{}{}
	} else {
		b.fp.CleartextEvents++
	}
}

// finalize sorts the accumulated sets into the footprint and returns it.
func (b *footprintBuilder) finalize() *SubjectFootprint {
	b.fp.Streams = sortedSet(b.streamSet)
	b.fp.SharedStreams = sortedSet(b.sharedSet)
	b.fp.KeyIDs = sortedSet(b.keySet)
	return b.fp
}
