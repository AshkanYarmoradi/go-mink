package mink

// gdpr_refactor_test.go pins the contracts of the unexported helpers extracted from
// DataEraser.discoverKeys, DataEraser.buildCertificate,
// RetentionManager.detectSharedShredKeys and BackfillSubjectIndexWithReport when
// their cognitive complexity was reduced. The end-to-end behavior of those four
// functions is covered by the existing eraser, verify, retention and subject-index
// tests; these tests lock each helper's single responsibility (and its exact
// PII-free wording) in isolation.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go-mink.dev/adapters"
	"go-mink.dev/adapters/memory"
)

// refactorEnvelopedMeta returns metadata carrying a COMPLETE field-encryption
// envelope under keyID (HasEncryptionEnvelope == true).
func refactorEnvelopedMeta(keyID string) Metadata {
	return Metadata{Custom: map[string]string{
		encryptedFieldsKey: `["email"]`,
		encryptionKeyIDKey: keyID,
		encryptedDEKKey:    "wrapped-dek",
	}}
}

// =============================================================================
// eraser.go: discoverKeys helpers
// =============================================================================

func TestDataEraser_CollectStreamKeys_SubjectScoping(t *testing.T) {
	const subject = "user-1"
	tagged := func(v int64, subjects ...string) StoredEvent {
		return StoredEvent{StreamID: "s", Version: v, Metadata: setSubjectTags(Metadata{}, subjects)}
	}
	plain := func(v int64) StoredEvent { return StoredEvent{StreamID: "s", Version: v} }
	rejectVersion := func(v int64) ExportFilter {
		return func(se StoredEvent) bool { return se.Version != v }
	}

	tests := []struct {
		name         string
		stored       []StoredEvent
		req          ErasureRequest
		wantTally    streamKeyTally
		wantVersions []int64
	}{
		{
			name:         "legacy stream hands every matched event to fn",
			stored:       []StoredEvent{plain(1), plain(2)},
			req:          ErasureRequest{SubjectID: subject},
			wantTally:    streamKeyTally{tagged: false, matched: true},
			wantVersions: []int64{1, 2},
		},
		{
			name:         "tagged stream keeps only the subject's events",
			stored:       []StoredEvent{tagged(1, subject), tagged(2, "other"), tagged(3, "other", subject)},
			req:          ErasureRequest{SubjectID: subject},
			wantTally:    streamKeyTally{tagged: true, matched: true},
			wantVersions: []int64{1, 3},
		},
		{
			name:         "untagged events in a tagged stream are counted, not handed over",
			stored:       []StoredEvent{tagged(1, subject), plain(2), plain(3)},
			req:          ErasureRequest{SubjectID: subject},
			wantTally:    streamKeyTally{tagged: true, matched: true, untagged: 2},
			wantVersions: []int64{1},
		},
		{
			name:      "only another subject's events: nothing matched, nothing untagged",
			stored:    []StoredEvent{tagged(1, "other")},
			req:       ErasureRequest{SubjectID: subject},
			wantTally: streamKeyTally{tagged: true},
		},
		{
			name:         "request filter applies before subject scoping",
			stored:       []StoredEvent{plain(1), plain(2)},
			req:          ErasureRequest{SubjectID: subject, Filter: rejectVersion(1)},
			wantTally:    streamKeyTally{tagged: false, matched: true},
			wantVersions: []int64{2},
		},
		{
			name:         "a filtered-out untagged event is not counted as untagged",
			stored:       []StoredEvent{tagged(1, subject), plain(2)},
			req:          ErasureRequest{SubjectID: subject, Filter: rejectVersion(2)},
			wantTally:    streamKeyTally{tagged: true, matched: true},
			wantVersions: []int64{1},
		},
		{
			name:      "empty stream",
			req:       ErasureRequest{SubjectID: subject},
			wantTally: streamKeyTally{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := &DataEraser{}
			var got []int64
			tally := e.collectStreamKeys(tt.stored, tt.req, func(se StoredEvent) { got = append(got, se.Version) })
			assert.Equal(t, tt.wantTally, tally)
			assert.Equal(t, tt.wantVersions, got)
		})
	}
}

func TestStreamScopeCounts_Annotate_NotesAndPartial(t *testing.T) {
	const legacyNote = "1 of 3 listed stream(s) carry no subject tags; every key found in them was collected (legacy scope), which also shreds any co-tenant sharing those streams"
	const untaggedNote = "4 untagged event(s) in 2 listed stream(s) that also carry subject tags could not be attributed to any subject and were not shredded; keys were collected only from events tagged for the subject"

	tests := []struct {
		name        string
		tallies     []streamKeyTally
		wantNotes   []string
		wantPartial bool
	}{
		{
			name:    "every stream tagged and attributed: no notes, not partial",
			tallies: []streamKeyTally{{tagged: true, matched: true}, {tagged: true}, {tagged: true, matched: true}},
		},
		{
			name:      "a legacy stream is noted but does not make the result partial",
			tallies:   []streamKeyTally{{tagged: false, matched: true}, {tagged: true, matched: true}, {tagged: true}},
			wantNotes: []string{legacyNote},
		},
		{
			name:        "skipped untagged events are summed over the mixed streams and make the result partial",
			tallies:     []streamKeyTally{{tagged: true, matched: true, untagged: 3}, {tagged: true, untagged: 1}, {tagged: true}},
			wantNotes:   []string{untaggedNote},
			wantPartial: true,
		},
		{
			name:        "legacy note precedes the untagged note",
			tallies:     []streamKeyTally{{tagged: false}, {tagged: true, untagged: 3}, {tagged: true, untagged: 1}},
			wantNotes:   []string{legacyNote, untaggedNote},
			wantPartial: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var scope streamScopeCounts
			for _, tally := range tt.tallies {
				scope.add(tally)
			}
			result := &ErasureResult{Notes: []string{"existing"}}
			scope.annotate(result, 3)
			assert.Equal(t, append([]string{"existing"}, tt.wantNotes...), result.Notes)
			assert.Equal(t, tt.wantPartial, result.Partial)
		})
	}
}

func TestDataEraser_LoadErasureStream(t *testing.T) {
	ctx := context.Background()
	// The memory adapter reports a missing stream as an empty load; wrap it so one
	// stream id reports ErrStreamNotFound the way a database-backed adapter does.
	store := New(refactorMissingStreamAdapter{EventStoreAdapter: memory.NewAdapter(), missing: "User-missing"})
	store.RegisterEvents(eraseUserCreated{})
	require.NoError(t, store.Append(ctx, "User-1", []interface{}{
		eraseUserCreated{UserID: "user-1", Email: "a@example.com"},
		eraseUserCreated{UserID: "user-1", Email: "b@example.com"},
	}))

	t.Run("existing stream is returned as found", func(t *testing.T) {
		logger := newTestLogger()
		e := &DataEraser{store: store, logger: logger}
		stored, found, err := e.loadErasureStream(ctx, "user-1", "User-1")
		require.NoError(t, err)
		assert.True(t, found)
		assert.Len(t, stored, 2)
		assert.Empty(t, logger.warnLogs)
	})

	t.Run("missing stream is skipped with a PII-free warning, not an error", func(t *testing.T) {
		logger := newTestLogger()
		e := &DataEraser{store: store, logger: logger}
		stored, found, err := e.loadErasureStream(ctx, "user-1", "User-missing")
		require.NoError(t, err)
		assert.False(t, found)
		assert.Nil(t, stored)
		assert.True(t, logger.hasLogMessage("warn", "stream not found during erasure, skipping"))
	})

	t.Run("an empty load (memory-adapter style) counts as found", func(t *testing.T) {
		logger := newTestLogger()
		e := &DataEraser{store: store, logger: logger}
		stored, found, err := e.loadErasureStream(ctx, "user-1", "User-empty")
		require.NoError(t, err)
		assert.True(t, found)
		assert.Empty(t, stored)
		assert.Empty(t, logger.warnLogs)
	})

	t.Run("cancelled context is a fatal ErasureError", func(t *testing.T) {
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		e := &DataEraser{store: store, logger: newTestLogger()}
		_, found, err := e.loadErasureStream(cancelled, "user-1", "User-1")
		require.Error(t, err)
		assert.False(t, found)
		assert.ErrorIs(t, err, ErrErasureFailed)
		assert.ErrorIs(t, err, context.Canceled)
		assert.Contains(t, err.Error(), `failed to load stream "User-1"`)
	})
}

func TestDataEraser_CollectScanKeys_RecordsMatchedStreams(t *testing.T) {
	e := &DataEraser{}
	batch := []StoredEvent{
		{StreamID: "a", Version: 1},
		{StreamID: "b", Version: 1},
		{StreamID: "a", Version: 2},
	}
	req := ErasureRequest{SubjectID: "s", Filter: func(se StoredEvent) bool { return se.StreamID != "b" }}
	matched := map[string]struct{}{}
	var seen []int64
	e.collectScanKeys(batch, req, matched, func(se StoredEvent) { seen = append(seen, se.Version) })
	assert.Equal(t, map[string]struct{}{"a": {}}, matched)
	assert.Equal(t, []int64{1, 2}, seen)
}

// =============================================================================
// verify.go: buildCertificate helpers
// =============================================================================

func TestCertificateDraft_BlockAndNote(t *testing.T) {
	d := &certificateDraft{verified: true}
	d.note("context")
	assert.True(t, d.verified, "a note must not change the verdict")
	d.block("reason")
	assert.False(t, d.verified)
	d.block("another reason")
	assert.Equal(t, []string{"context", "reason", "another reason"}, d.cert.Notes)
}

func TestAttestableStores_DropsStoresThatSkippedSharedStreams(t *testing.T) {
	tests := []struct {
		name     string
		verified []string
		outcomes []SubjectErasureOutcome
		want     []string
	}{
		{name: "nothing verified, no outcomes"},
		{
			name:     "no shared skips keeps the list as is",
			verified: []string{"audit", "saga"},
			outcomes: []SubjectErasureOutcome{{Name: "audit"}, {Name: "saga", Skipped: true}},
			want:     []string{"audit", "saga"},
		},
		{
			name:     "a store that skipped shared streams is dropped",
			verified: []string{"audit", "saga"},
			outcomes: []SubjectErasureOutcome{{Name: "audit", SharedStreamsSkipped: 2}},
			want:     []string{"saga"},
		},
		{
			name:     "a skip on a store not proven clean changes nothing",
			verified: []string{"saga"},
			outcomes: []SubjectErasureOutcome{{Name: "audit", SharedStreamsSkipped: 1}},
			want:     []string{"saga"},
		},
		{
			name:     "every store dropped leaves an empty list",
			verified: []string{"audit"},
			outcomes: []SubjectErasureOutcome{{Name: "audit", SharedStreamsSkipped: 1}},
			want:     []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, attestableStores(tt.verified, tt.outcomes))
		})
	}
}

func TestCertificateDraft_GateVacuous(t *testing.T) {
	const vacuousNote = "no subject-tagged events could be verified: the erasure was scoped by key ids, explicit streams or untagged events, so the certificate cannot attest that the subject's events are unrecoverable"
	tests := []struct {
		name          string
		eventsChecked int
		result        ErasureResult
		wantVerified  bool
		wantNotes     []string
	}{
		{name: "events were checked: nothing to add", eventsChecked: 1, result: ErasureResult{KeysRevoked: []string{"k"}}, wantVerified: true},
		{name: "nothing checked but keys revoked", result: ErasureResult{KeysRevoked: []string{"k"}}, wantNotes: []string{vacuousNote}},
		{name: "nothing checked but keys failed", result: ErasureResult{KeysFailed: []string{"k"}}, wantNotes: []string{vacuousNote}},
		{name: "nothing checked but streams targeted", result: ErasureResult{Streams: []string{"s"}}, wantNotes: []string{vacuousNote}},
		{name: "nothing checked, nothing attempted: honest empty footprint", wantVerified: true, wantNotes: []string{"nothing to erase: the subject has no resolved streams or keys"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &certificateDraft{verified: true}
			d.gateVacuous(tt.eventsChecked, &tt.result)
			assert.Equal(t, tt.wantVerified, d.verified)
			assert.Equal(t, tt.wantNotes, d.cert.Notes)
		})
	}
}

func TestCertificateDraft_GateKeyOutcome(t *testing.T) {
	tests := []struct {
		name         string
		result       ErasureResult
		wantVerified bool
		wantNotes    []string
	}{
		{name: "clean outcome", result: ErasureResult{KeysRevoked: []string{"k"}}, wantVerified: true},
		{
			name:      "failed keys deny attestation",
			result:    ErasureResult{KeysFailed: []string{"k1", "k2"}},
			wantNotes: []string{"2 key(s) failed to revoke; data under them remains recoverable"},
		},
		{
			name:         "cleartext events are noted but do not deny",
			result:       ErasureResult{CleartextEvents: 3},
			wantVerified: true,
			wantNotes:    []string{cleartextNote(3)},
		},
		{
			name:         "the erasure's own notes are carried over, after the key notes",
			result:       ErasureResult{KeysFailed: []string{"k"}, CleartextEvents: 1, Notes: []string{"scope note", "another"}},
			wantVerified: false,
			wantNotes:    []string{"1 key(s) failed to revoke; data under them remains recoverable", cleartextNote(1), "scope note", "another"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &certificateDraft{verified: true}
			d.gateKeyOutcome(&tt.result)
			assert.Equal(t, tt.wantVerified, d.verified)
			assert.Equal(t, tt.wantNotes, d.cert.Notes)
		})
	}
}

func TestCertificateDraft_GateSubjectStores(t *testing.T) {
	tests := []struct {
		name         string
		outcomes     []SubjectErasureOutcome
		wantVerified bool
		wantNotes    []string
	}{
		{name: "no sibling stores", wantVerified: true},
		{name: "fully purged store", outcomes: []SubjectErasureOutcome{{Name: "audit", Erased: 4}}, wantVerified: true},
		{
			name:      "failed store",
			outcomes:  []SubjectErasureOutcome{{Name: "audit", Err: "boom"}},
			wantNotes: []string{`sibling store "audit" was not erased (failed or skipped)`},
		},
		{
			name:      "skipped store",
			outcomes:  []SubjectErasureOutcome{{Name: "saga", Skipped: true}},
			wantNotes: []string{`sibling store "saga" was not erased (failed or skipped)`},
		},
		{
			name:      "store that left shared streams untouched",
			outcomes:  []SubjectErasureOutcome{{Name: "snapshots", SharedStreamsSkipped: 2}},
			wantNotes: []string{`sibling store "snapshots" left 2 footprint stream(s) shared with other subjects untouched; rows for the subject on those streams may remain and are not attested`},
		},
		{
			name:      "a failed store that also skipped shared streams is reported once, as failed",
			outcomes:  []SubjectErasureOutcome{{Name: "audit", Err: "boom", SharedStreamsSkipped: 1}},
			wantNotes: []string{`sibling store "audit" was not erased (failed or skipped)`},
		},
		{
			name: "one note per offending store, in registration order",
			outcomes: []SubjectErasureOutcome{
				{Name: "ok"},
				{Name: "saga", Skipped: true},
				{Name: "snapshots", SharedStreamsSkipped: 1},
			},
			wantNotes: []string{
				`sibling store "saga" was not erased (failed or skipped)`,
				`sibling store "snapshots" left 1 footprint stream(s) shared with other subjects untouched; rows for the subject on those streams may remain and are not attested`,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &certificateDraft{verified: true}
			d.gateSubjectStores(tt.outcomes)
			assert.Equal(t, tt.wantVerified, d.verified)
			assert.Equal(t, tt.wantNotes, d.cert.Notes)
		})
	}
}

// =============================================================================
// retention.go: detectSharedShredKeys helper
// =============================================================================

func TestTallyOutOfScopeKeys_CountsUncoveredCandidateEvents(t *testing.T) {
	now := time.Now()
	old := now.Add(-48 * time.Hour)
	policies := []RetentionPolicy{
		{Name: "shred-orders", Category: "order", Action: ActionShred, MaxAge: 24 * time.Hour},
		{Name: "redact-users", Category: "user", Action: ActionRedactFields, Fields: []string{"email"}},
	}
	candidates := map[string]struct{}{"k1": {}, "k2": {}}
	batch := []StoredEvent{
		{StreamID: "order-1", Timestamp: old, Metadata: refactorEnvelopedMeta("k1")}, // covered by the Shred policy
		{StreamID: "order-2", Timestamp: now, Metadata: refactorEnvelopedMeta("k1")}, // statically matches but too young
		{StreamID: "user-1", Timestamp: old, Metadata: refactorEnvelopedMeta("k1")},  // only a RedactFields policy: no consent to shred
		{StreamID: "user-2", Timestamp: old, Metadata: refactorEnvelopedMeta("k3")},  // not a candidate key
		{StreamID: "user-3", Timestamp: old},                                         // plaintext
		{StreamID: "user-4", Timestamp: old, Metadata: Metadata{Custom: map[string]string{ // incomplete envelope
			encryptedFieldsKey: `["email"]`, encryptionKeyIDKey: "k2",
		}}},
		{StreamID: "order-3", Timestamp: old, Metadata: refactorEnvelopedMeta("k2")}, // covered: k2 stays exclusive
	}

	outOfScope := map[string]int{}
	tallyOutOfScopeKeys(batch, candidates, policies, now, outOfScope)
	assert.Equal(t, map[string]int{"k1": 2}, outOfScope)

	// Accumulates across batches rather than resetting.
	tallyOutOfScopeKeys(batch[1:2], candidates, policies, now, outOfScope)
	assert.Equal(t, map[string]int{"k1": 3}, outOfScope)
}

// =============================================================================
// subjectindex.go: BackfillSubjectIndexWithReport helpers
// =============================================================================

type refactorIndexCall struct {
	stream   string
	subjects []string
}

// refactorIndexWriter is a SubjectIndexWriter that records its calls and fails on demand.
type refactorIndexWriter struct {
	err   error
	calls []refactorIndexCall
}

func (w *refactorIndexWriter) IndexSubjects(_ context.Context, streamID string, subjects []string) error {
	w.calls = append(w.calls, refactorIndexCall{stream: streamID, subjects: append([]string(nil), subjects...)})
	return w.err
}

func TestSubjectBackfill_IndexEvent(t *testing.T) {
	tagsAB := func(string, []byte, Metadata) []string { return []string{"a", "b"} }
	tagsNone := func(string, []byte, Metadata) []string { return nil }

	tests := []struct {
		name      string
		se        StoredEvent
		tagger    SubjectTagger
		writerErr error
		wantRep   BackfillReport
		wantCalls []refactorIndexCall
		wantErr   string
	}{
		{
			name:      "tagger-derived and existing tags are merged, de-duplicated, order-preserving",
			se:        StoredEvent{StreamID: "s-1", Metadata: setSubjectTags(Metadata{}, []string{"b", "c"})},
			tagger:    tagsAB,
			wantRep:   BackfillReport{Scanned: 1, Indexed: 1},
			wantCalls: []refactorIndexCall{{stream: "s-1", subjects: []string{"a", "b", "c"}}},
		},
		{
			name:    "no subject from either source counts as untagged and writes nothing",
			se:      StoredEvent{StreamID: "s-2"},
			tagger:  tagsNone,
			wantRep: BackfillReport{Scanned: 1, Untagged: 1},
		},
		{
			name:      "an index-write failure is wrapped with the stream id",
			se:        StoredEvent{StreamID: "s-3"},
			tagger:    tagsAB,
			writerErr: errors.New("db down"),
			wantRep:   BackfillReport{Scanned: 1},
			wantCalls: []refactorIndexCall{{stream: "s-3", subjects: []string{"a", "b"}}},
			wantErr:   `mink: backfill index stream "s-3": db down`,
		},
		{
			name:      "an encrypted event the store cannot decrypt is indexed from its existing tags only",
			se:        StoredEvent{StreamID: "s-4", Metadata: setSubjectTags(refactorEnvelopedMeta("k"), []string{"c"})},
			tagger:    tagsAB,
			wantRep:   BackfillReport{Scanned: 1, Indexed: 1, Undecryptable: 1},
			wantCalls: []refactorIndexCall{{stream: "s-4", subjects: []string{"c"}}},
		},
		{
			name:    "an undecryptable event without tags is both undecryptable and untagged",
			se:      StoredEvent{StreamID: "s-5", Metadata: refactorEnvelopedMeta("k")},
			tagger:  tagsAB,
			wantRep: BackfillReport{Scanned: 1, Untagged: 1, Undecryptable: 1},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := &refactorIndexWriter{err: tt.writerErr}
			var rep BackfillReport
			// A zero EventStore has no encryption configured, so encrypted events are
			// undecryptable and plaintext ones pass through untouched.
			b := &subjectBackfill{store: &EventStore{}, tagger: tt.tagger, writer: w, rep: &rep}
			err := b.indexEvent(context.Background(), tt.se)
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr)
				assert.ErrorIs(t, err, tt.writerErr)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.wantRep, rep)
			assert.Equal(t, tt.wantCalls, w.calls)
		})
	}
}

func TestSubjectBackfill_IndexBatch(t *testing.T) {
	batch := []StoredEvent{{StreamID: "s-1"}, {StreamID: "s-2"}, {StreamID: "s-3"}}
	tagsA := func(string, []byte, Metadata) []string { return []string{"a"} }

	t.Run("indexes every event in order", func(t *testing.T) {
		w := &refactorIndexWriter{}
		var rep BackfillReport
		b := &subjectBackfill{store: &EventStore{}, tagger: tagsA, writer: w, rep: &rep}
		require.NoError(t, b.indexBatch(context.Background(), batch))
		assert.Equal(t, BackfillReport{Scanned: 3, Indexed: 3}, rep)
		require.Len(t, w.calls, 3)
		assert.Equal(t, "s-3", w.calls[2].stream)
	})

	t.Run("stops at the first index-write failure", func(t *testing.T) {
		w := &refactorIndexWriter{err: errors.New("db down")}
		var rep BackfillReport
		b := &subjectBackfill{store: &EventStore{}, tagger: tagsA, writer: w, rep: &rep}
		err := b.indexBatch(context.Background(), batch)
		require.EqualError(t, err, `mink: backfill index stream "s-1": db down`)
		assert.Equal(t, BackfillReport{Scanned: 1}, rep)
		assert.Len(t, w.calls, 1)
	})

	t.Run("honors cancellation between events of one batch", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		w := &refactorIndexWriter{}
		var rep BackfillReport
		cancelling := func(string, []byte, Metadata) []string {
			cancel() // cancel while the first event is being tagged
			return []string{"a"}
		}
		b := &subjectBackfill{store: &EventStore{}, tagger: cancelling, writer: w, rep: &rep}
		err := b.indexBatch(ctx, batch)
		require.ErrorIs(t, err, context.Canceled)
		assert.Equal(t, BackfillReport{Scanned: 1, Indexed: 1}, rep, "the event in flight completes; the next one is not started")
		assert.Len(t, w.calls, 1)
	})
}

func TestWarnUndecryptable_LogsOnlyWhenSomethingWasUndecryptable(t *testing.T) {
	const msg = "mink: subject index backfill could not decrypt some events; indexed them from their existing subject tags only"
	tests := []struct {
		name     string
		rep      BackfillReport
		wantWarn bool
	}{
		{name: "nothing undecryptable: silent", rep: BackfillReport{Scanned: 3, Indexed: 3}},
		{name: "undecryptable events: one warning", rep: BackfillReport{Scanned: 3, Indexed: 2, Undecryptable: 1}, wantWarn: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := newTestLogger()
			warnUndecryptable(logger, &tt.rep)
			assert.Equal(t, tt.wantWarn, logger.hasLogMessage("warn", msg))
			if tt.wantWarn {
				assert.Len(t, logger.warnLogs, 1)
			}
		})
	}
}

// refactorMissingStreamAdapter wraps an EventStoreAdapter so that loading the stream
// named missing fails with adapters.ErrStreamNotFound, as database-backed adapters
// report an unknown stream (the memory adapter returns an empty load instead).
type refactorMissingStreamAdapter struct {
	adapters.EventStoreAdapter
	missing string
}

func (a refactorMissingStreamAdapter) Load(ctx context.Context, streamID string, fromVersion int64) ([]adapters.StoredEvent, error) {
	if streamID == a.missing {
		return nil, adapters.ErrStreamNotFound
	}
	return a.EventStoreAdapter.Load(ctx, streamID, fromVersion)
}
