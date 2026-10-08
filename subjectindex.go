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

// dedupeStrings returns in with duplicates removed, preserving order.
func dedupeStrings(in []string) []string {
	if len(in) < 2 {
		return in
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// BackfillSubjectIndex scans the whole event store and records each event's subjects
// (derived by tagger, plus any already tagged on the event) into writer. This is the
// migration step that lets a subject index cover events written BEFORE subject tagging
// was enabled — making historical subjects fully resolvable, and therefore fully
// erasable, instead of permanently Partial.
//
// The tagger sees each event exactly as the live append path shows it: a
// field-encrypted event is DECRYPTED first, through the store's own decrypt path
// (DecryptStoredEvent, with WithDecryptionErrorHandler honored), with the encryption
// envelope stripped from the metadata it receives. Running a tagger over ciphertext
// would silently under-index every subject whose id lives in an encrypted field. An
// event that cannot be decrypted — its key is revoked (crypto-shredded), the handler
// swallowed the failure and left the ciphertext in place, or the store has no
// encryption configured for an encrypted event — is NOT passed to the tagger: it is
// indexed from its existing "$subjects" tags only, counted, and a single warning with
// the count is logged through the store's logger at the end.
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
// PII-free. Scanned == Indexed + Untagged.
type BackfillReport struct {
	// Scanned is the number of events examined.
	Scanned int

	// Indexed is the number of events for which at least one subject was recorded in
	// the index (derived by the tagger and/or already present in the event's tags).
	Indexed int

	// Untagged is the number of events for which NEITHER the tagger NOR the event's
	// existing "$subjects" tags produced a subject. Such events are invisible to an
	// index-backed resolve: if any of them concerns a data subject, that subject's
	// footprint resolved through this index is incomplete. WithAuthoritativeIndex is only
	// safe over this index when Untagged == 0, or when the untagged events are known to
	// carry no PII.
	Untagged int

	// Undecryptable is the number of field-encrypted events that could not be decrypted
	// (revoked key, a handler that swallowed the failure, or no encryption config), so
	// the tagger never saw their encrypted fields; they were indexed from their existing
	// tags only (and count as Untagged when they had none). The store's logger receives
	// one warning with this count.
	Undecryptable int
}

// BackfillSubjectIndexWithReport is BackfillSubjectIndex returning a BackfillReport
// instead of a bare scanned count. Read Untagged before asserting WithAuthoritativeIndex
// over the index: an index-backed resolve never sees untagged events, so a non-zero
// Untagged means the index — and any footprint or erasure certificate derived from it —
// cannot be proven complete. On an error the report covers the events processed so far.
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

	defer func() {
		if rep.Undecryptable > 0 {
			store.logger.Warn("mink: subject index backfill could not decrypt some events; indexed them from their existing subject tags only",
				"undecryptable", rep.Undecryptable, "scanned", rep.Scanned)
		}
	}()
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
		for _, se := range batch {
			// Honor cancellation mid-batch, not only between batches.
			if err := ctx.Err(); err != nil {
				return rep, err
			}
			rep.Scanned++
			// Combine tagger-derived subjects with any already tagged on the event, then
			// de-duplicate — when tagging was already enabled the two overlap, and
			// duplicate (subject, stream) pairs are redundant index writes. Build a fresh
			// slice rather than appending onto the tagger's return: the tagger's
			// slice-ownership is unspecified, so appending could mutate a buffer it reuses.
			var derived []string
			if data, md, ok := backfillPlaintext(ctx, store, se); ok {
				derived = tagger(se.Type, data, md)
			} else {
				rep.Undecryptable++
			}
			existing := GetSubjectTags(se.Metadata)
			subjects := make([]string, 0, len(derived)+len(existing))
			subjects = append(subjects, derived...)
			subjects = append(subjects, existing...)
			subjects = dedupeStrings(subjects)
			if len(subjects) == 0 {
				rep.Untagged++
				continue
			}
			if err := writer.IndexSubjects(ctx, se.StreamID, subjects); err != nil {
				return rep, fmt.Errorf("mink: backfill index stream %q: %w", se.StreamID, err)
			}
			rep.Indexed++
		}
		position = batch[len(batch)-1].GlobalPosition
	}
	return rep, nil
}

// backfillPlaintext returns an event's data and metadata as a SubjectTagger expects
// them: decrypted, with the encryption envelope stripped — what the live append path
// hands the tagger before encrypting. A plaintext event passes through untouched. ok
// is false when the event is field-encrypted but cannot be decrypted: the store has
// no encryption configured, the decrypt path failed, or its WithDecryptionErrorHandler
// swallowed a revoked-key failure and left the ciphertext in place (DecryptStoredEvent
// then keeps the envelope, which is how that no-op is detected).
func backfillPlaintext(ctx context.Context, store *EventStore, se StoredEvent) ([]byte, Metadata, bool) {
	if !IsEncrypted(se.Metadata) {
		return se.Data, se.Metadata, true
	}
	if store.encryption == nil {
		return nil, Metadata{}, false
	}
	dec, err := store.DecryptStoredEvent(ctx, se)
	if err != nil || IsEncrypted(dec.Metadata) {
		return nil, Metadata{}, false
	}
	return dec.Data, dec.Metadata, true
}
