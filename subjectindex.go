package mink

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"go-mink.dev/adapters"
)

// SubjectIndexPurger is the OPTIONAL purge side of a subject index: it removes every
// (subject, stream) entry recorded for a subject once that subject has been erased, so
// the index itself stops naming the subject's streams. Wire one into a DataEraser with
// WithSubjectIndexPurge. MemorySubjectIndex and the PostgreSQL SubjectIndex implement it.
type SubjectIndexPurger interface {
	// DeleteSubject removes every entry recorded for subjectID. It MUST be idempotent
	// (purging an unknown subject is a no-op, not an error).
	DeleteSubject(ctx context.Context, subjectID string) error
}

// MemorySubjectIndex is a thread-safe in-memory subject index implementing
// SubjectIndexAdapter (read), SubjectIndexWriter (write) and SubjectIndexPurger
// (purge). It maps each subject to the set of streams that contain its events, so a
// resolver can find a subject's footprint without scanning the whole store. Populate
// it at append time (WithSubjectIndexWriter) and/or via BackfillSubjectIndex; it is
// NOT persistent — use a durable index (e.g. a table-backed one) in production.
type MemorySubjectIndex struct {
	mu      sync.RWMutex
	streams map[string]map[string]struct{} // subjectID → set of streamIDs
}

var (
	_ SubjectIndexAdapter = (*MemorySubjectIndex)(nil)
	_ SubjectIndexWriter  = (*MemorySubjectIndex)(nil)
	_ SubjectIndexPurger  = (*MemorySubjectIndex)(nil)
)

// NewMemorySubjectIndex creates an empty in-memory subject index.
func NewMemorySubjectIndex() *MemorySubjectIndex {
	return &MemorySubjectIndex{streams: map[string]map[string]struct{}{}}
}

// IndexSubjects records that streamID contains events for each subject. Idempotent.
func (m *MemorySubjectIndex) IndexSubjects(ctx context.Context, streamID string, subjectIDs []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if streamID == "" || len(subjectIDs) == 0 {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range subjectIDs {
		if s == "" {
			continue
		}
		set, ok := m.streams[s]
		if !ok {
			set = map[string]struct{}{}
			m.streams[s] = set
		}
		set[streamID] = struct{}{}
	}
	return nil
}

// StreamsBySubject returns the sorted streams recorded for subjectID (empty if none).
func (m *MemorySubjectIndex) StreamsBySubject(ctx context.Context, subjectID string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	set := m.streams[subjectID]
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out, nil
}

// DeleteSubject removes every entry recorded for subjectID. Idempotent: an unknown
// (or empty) subject is a no-op. Implements SubjectIndexPurger.
func (m *MemorySubjectIndex) DeleteSubject(ctx context.Context, subjectID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if subjectID == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.streams, subjectID)
	return nil
}

// BackfillSubjectIndex scans the whole event store and records each event's subjects
// into writer: the event's persisted "$subjects" tags when it has any, otherwise the
// subjects tagger derives from it. This is the migration step that lets a subject index
// cover events written BEFORE subject tagging was enabled — making historical subjects
// fully resolvable, and therefore fully erasable, instead of permanently Partial.
//
// Attribution is the ONE rule shared with SubjectResolver, DataEraser and DataExporter
// (see WithResolverTagger): a tagged event is indexed from its tags alone (the tagger is
// not re-run over it — to re-attribute history under a changed tagger, re-tag it, e.g.
// with ReEncryptStream, which re-applies the tag policy), and an untagged event is
// attributed by the tagger exactly as the live append path would have. The tagger sees
// such an event as the append path showed it: a field-encrypted event is DECRYPTED first,
// through the store's own decrypt path (DecryptStoredEvent, with
// WithDecryptionErrorHandler honored), with the encryption envelope stripped from the
// metadata it receives. Running a tagger over ciphertext would silently under-index every
// subject whose id lives in an encrypted field. An untagged event that cannot be
// decrypted — its key is revoked (crypto-shredded), the handler swallowed the failure and
// left the ciphertext in place, or the store has no encryption configured for an
// encrypted event — is NOT passed to the tagger: it stays unattributed (Untagged), is
// counted (Undecryptable), and a single warning with the count is logged through the
// store's logger at the end.
//
// Because the index records exactly what a resolver using the same tagger attributes, an
// index-backed resolve finds every event the backfill indexed — provided the resolver
// attributes with the SAME tagger (the store's own by default, or WithResolverTagger).
//
// Requires a scannable adapter (SubscriptionAdapter) — returns
// ErrSubscriptionNotSupported otherwise. Returns the number of events scanned
// (BackfillReport.Scanned). Safe to re-run (IndexSubjects is idempotent).
//
// It is a thin wrapper over BackfillSubjectIndexWithReport, which additionally reports
// how many events could NOT be attributed to any subject — the figure that decides
// whether WithAuthoritativeIndex may be asserted over the resulting index.
func BackfillSubjectIndex(ctx context.Context, store *EventStore, tagger SubjectTagger, writer SubjectIndexWriter, batchSize int) (int, error) {
	rep, err := BackfillSubjectIndexWithReport(ctx, store, tagger, writer, batchSize)
	return rep.Scanned, err
}

// BackfillReport summarizes a BackfillSubjectIndexWithReport run. All counts are
// PII-free. Scanned == Indexed + Untagged; Undecryptable <= Untagged.
type BackfillReport struct {
	// Scanned is the number of events examined.
	Scanned int

	// Indexed is the number of events for which at least one subject was recorded in
	// the index (from the event's persisted tags, or derived by the tagger when it had
	// none).
	Indexed int

	// Untagged is the number of events that could not be attributed to ANY subject even
	// by the tagger: they carry no "$subjects" tag and the tagger derived nothing (or
	// never saw them, see Undecryptable). Such events are invisible to an index-backed
	// resolve: if any of them concerns a data subject, that subject's footprint resolved
	// through this index is incomplete. WithAuthoritativeIndex is only safe over this
	// index when Untagged == 0, or when the untagged events are known to carry no PII.
	Untagged int

	// Undecryptable is the number of UNTAGGED field-encrypted events that could not be
	// decrypted (revoked key, a handler that swallowed the failure, or no encryption
	// config), so the tagger never saw them and they could not be attributed; each is also
	// counted in Untagged. Tagged encrypted events are attributed from their tags and are
	// not decrypted, so they never count here. The store's logger receives one warning
	// with this count.
	Undecryptable int
}

// BackfillSubjectIndexWithReport is BackfillSubjectIndex returning a BackfillReport
// instead of a bare scanned count. Read Untagged before asserting WithAuthoritativeIndex
// over the index: an index-backed resolve never sees events outside the indexed streams,
// so a non-zero Untagged means the index — and any footprint or erasure certificate
// derived from it — cannot be proven complete. On an error the report covers the events
// processed so far.
func BackfillSubjectIndexWithReport(ctx context.Context, store *EventStore, tagger SubjectTagger, writer SubjectIndexWriter, batchSize int) (BackfillReport, error) {
	var rep BackfillReport
	if tagger == nil || writer == nil {
		return rep, fmt.Errorf("mink: BackfillSubjectIndex requires a tagger and a writer")
	}
	if batchSize <= 0 {
		batchSize = 1000
	}
	if _, ok := store.Adapter().(adapters.SubscriptionAdapter); !ok {
		// The backfill scans every event, which needs a SubscriptionAdapter. Report that
		// missing capability directly — not the export sentinel, whose "provide explicit
		// stream IDs" guidance is meaningless here (backfill takes no stream IDs).
		return rep, ErrSubscriptionNotSupported
	}

	defer warnUndecryptable(store.logger, &rep)
	run := &subjectBackfill{store: store, tagger: tagger, writer: writer, rep: &rep}
	var position uint64
	for {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		batch, err := store.LoadEventsFromPosition(ctx, position, batchSize)
		if err != nil {
			return rep, fmt.Errorf("mink: backfill scan from %d: %w", position, err)
		}
		if len(batch) == 0 {
			break
		}
		if err := run.indexBatch(ctx, batch); err != nil {
			return rep, err
		}
		position = batch[len(batch)-1].GlobalPosition
	}
	return rep, nil
}

// warnUndecryptable logs, once, how many untagged field-encrypted events a backfill
// could not decrypt (and so could not attribute to any subject). It is deferred by
// BackfillSubjectIndexWithReport so the warning covers the run however it ended.
func warnUndecryptable(logger Logger, rep *BackfillReport) {
	if rep.Undecryptable > 0 {
		logger.Warn("mink: subject index backfill could not decrypt some untagged events; they could not be attributed to any subject and were counted as untagged",
			"undecryptable", rep.Undecryptable, "scanned", rep.Scanned)
	}
}

// subjectBackfill carries one BackfillSubjectIndexWithReport run's collaborators and
// its in-progress report between the per-batch and per-event steps.
type subjectBackfill struct {
	store  *EventStore
	tagger SubjectTagger
	writer SubjectIndexWriter
	rep    *BackfillReport
}

// indexBatch indexes every event in batch in order, honoring cancellation mid-batch
// (not only between batches) and stopping at the first index-write failure.
func (b *subjectBackfill) indexBatch(ctx context.Context, batch []StoredEvent) error {
	for _, se := range batch {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := b.indexEvent(ctx, se); err != nil {
			return err
		}
	}
	return nil
}

// indexEvent counts one scanned event and records its subjects (subjectsOf) in the
// index, or counts it as Untagged when neither its existing tags nor the tagger name
// any subject.
func (b *subjectBackfill) indexEvent(ctx context.Context, se StoredEvent) error {
	b.rep.Scanned++
	subjects := b.subjectsOf(ctx, se)
	if len(subjects) == 0 {
		b.rep.Untagged++
		return nil
	}
	if err := b.writer.IndexSubjects(ctx, se.StreamID, subjects); err != nil {
		return fmt.Errorf("mink: backfill index stream %q: %w", se.StreamID, err)
	}
	b.rep.Indexed++
	return nil
}

// subjectsOf returns the subjects of one event under the shared attribution rule
// (EventStore.attributeSubjects, with the backfill's tagger): its persisted tags, or —
// for an untagged event — what the tagger derives from its plaintext. An untagged event
// that cannot be decrypted never reaches the tagger; it is counted as Undecryptable and
// yields no subject. Using the very helper the resolver, eraser and exporter attribute
// with is what keeps the index from naming streams a resolve would not confirm, or
// missing ones it would.
func (b *subjectBackfill) subjectsOf(ctx context.Context, se StoredEvent) []string {
	at := b.store.attributeSubjects(ctx, b.tagger, se)
	if at.undecryptable {
		b.rep.Undecryptable++
	}
	return at.subjects
}
