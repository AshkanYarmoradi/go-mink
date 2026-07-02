package mink

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"go-mink.dev/adapters"
)

// MemorySubjectIndex is a thread-safe in-memory subject index implementing both
// SubjectIndexAdapter (read) and SubjectIndexWriter (write). It maps each subject to the
// set of streams that contain its events, so a resolver can find a subject's footprint
// without scanning the whole store. Populate it at append time (WithSubjectIndexWriter)
// and/or via BackfillSubjectIndex; it is NOT persistent — use a durable index (e.g. a
// table-backed one) in production.
type MemorySubjectIndex struct {
	mu      sync.RWMutex
	streams map[string]map[string]struct{} // subjectID → set of streamIDs
}

var (
	_ SubjectIndexAdapter = (*MemorySubjectIndex)(nil)
	_ SubjectIndexWriter  = (*MemorySubjectIndex)(nil)
)

// NewMemorySubjectIndex creates an empty in-memory subject index.
func NewMemorySubjectIndex() *MemorySubjectIndex {
	return &MemorySubjectIndex{streams: map[string]map[string]struct{}{}}
}

// IndexSubjects records that streamID contains events for each subject. Idempotent.
func (m *MemorySubjectIndex) IndexSubjects(_ context.Context, streamID string, subjectIDs []string) error {
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
func (m *MemorySubjectIndex) StreamsBySubject(_ context.Context, subjectID string) ([]string, error) {
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
// erasable, instead of permanently Partial. It reads RAW events (no decryption) and
// requires a scannable adapter (SubscriptionAdapter) — returns ErrSubscriptionNotSupported
// otherwise. Returns the number of events scanned. Safe to re-run (IndexSubjects is
// idempotent).
func BackfillSubjectIndex(ctx context.Context, store *EventStore, tagger SubjectTagger, writer SubjectIndexWriter, batchSize int) (int, error) {
	if tagger == nil || writer == nil {
		return 0, fmt.Errorf("mink: BackfillSubjectIndex requires a tagger and a writer")
	}
	if batchSize <= 0 {
		batchSize = 1000
	}
	if _, ok := store.Adapter().(adapters.SubscriptionAdapter); !ok {
		// The backfill scans every event, which needs a SubscriptionAdapter. Report that
		// missing capability directly — not the export sentinel, whose "provide explicit
		// stream IDs" guidance is meaningless here (backfill takes no stream IDs).
		return 0, ErrSubscriptionNotSupported
	}

	var scanned int
	var position uint64
	for {
		if err := ctx.Err(); err != nil {
			return scanned, err
		}
		batch, err := store.LoadEventsFromPosition(ctx, position, batchSize)
		if err != nil {
			return scanned, fmt.Errorf("mink: backfill scan from %d: %w", position, err)
		}
		if len(batch) == 0 {
			break
		}
		for _, se := range batch {
			scanned++
			// Combine tagger-derived subjects with any already tagged on the event, then
			// de-duplicate — when tagging was already enabled the two overlap, and
			// duplicate (subject, stream) pairs are redundant index writes.
			subjects := dedupeStrings(append(tagger(se.Type, se.Data, se.Metadata), GetSubjectTags(se.Metadata)...))
			if len(subjects) == 0 {
				continue
			}
			if err := writer.IndexSubjects(ctx, se.StreamID, subjects); err != nil {
				return scanned, fmt.Errorf("mink: backfill index stream %q: %w", se.StreamID, err)
			}
		}
		position = batch[len(batch)-1].GlobalPosition
	}
	return scanned, nil
}
