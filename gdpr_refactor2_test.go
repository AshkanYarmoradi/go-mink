package mink

// gdpr_refactor2_test.go pins the contracts of the unexported helpers extracted from
// DataEraser.collectStreamKeys, SubjectResolver.resolve and DataEraser.verifyStreams
// when their cognitive complexity was reduced (third review round of the hardening
// change). The end-to-end behavior of those three functions is covered by the existing
// eraser, verify and resolver tests, which ran unchanged across the refactor; these
// tests lock each helper's single responsibility in isolation.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go-mink.dev/adapters/memory"
	"go-mink.dev/encryption"
	"go-mink.dev/encryption/local"
)

// =============================================================================
// eraser.go: collectStreamKeys helpers
// =============================================================================

func TestStreamKeyTally_Note_FlagsAreSticky(t *testing.T) {
	const subject = "u1"
	tests := []struct {
		name       string
		at         subjectAttribution
		wantScoped bool
		wantShared bool
	}{
		{name: "unattributable event changes nothing"},
		{name: "the subject's own event scopes the stream", at: subjectAttribution{subjects: []string{subject}}, wantScoped: true},
		{name: "a co-tenant's event scopes and shares", at: subjectAttribution{subjects: []string{"other"}}, wantScoped: true, wantShared: true},
		{name: "an event of both scopes and shares", at: subjectAttribution{subjects: []string{subject, "other"}}, wantScoped: true, wantShared: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var tally streamKeyTally
			tally.note(tt.at, subject)
			assert.Equal(t, streamKeyTally{scoped: tt.wantScoped, shared: tt.wantShared}, tally)
		})
	}

	t.Run("flags accumulate across events and never reset", func(t *testing.T) {
		var tally streamKeyTally
		tally.note(subjectAttribution{subjects: []string{"other"}}, subject)
		tally.note(subjectAttribution{}, subject)
		tally.note(subjectAttribution{subjects: []string{subject}}, subject)
		assert.Equal(t, streamKeyTally{scoped: true, shared: true}, tally)
	})
}

func TestStreamKeyTally_OwnedBySubject(t *testing.T) {
	const subject = "u1"
	tests := []struct {
		name         string
		at           subjectAttribution
		wantOwned    bool
		wantUntagged int
	}{
		{name: "the subject's event is owned", at: subjectAttribution{subjects: []string{subject}}, wantOwned: true},
		{name: "an event of the subject and a co-tenant is owned", at: subjectAttribution{subjects: []string{"other", subject}}, wantOwned: true},
		{name: "a co-tenant's event is not owned and not untagged", at: subjectAttribution{subjects: []string{"other"}}},
		{name: "nobody's event is not owned and counted untagged", wantUntagged: 1},
		{name: "an undecryptable event is nobody's", at: subjectAttribution{undecryptable: true}, wantUntagged: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var tally streamKeyTally
			assert.Equal(t, tt.wantOwned, tally.ownedBySubject(tt.at, subject))
			assert.Equal(t, streamKeyTally{untagged: tt.wantUntagged}, tally, "ownership never touches scoped/matched/shared")
		})
	}
}

func TestDataEraser_AttributeStream_AttributesEachEventOnceInOrder(t *testing.T) {
	const subject = "u1"
	// The tagger records which event it saw via CorrelationID (set to the version).
	var taggerCalls []string
	tagger := func(_ string, _ []byte, md Metadata) []string {
		taggerCalls = append(taggerCalls, md.CorrelationID)
		return userIDTagger("", nil, md)
	}
	store := New(memory.NewAdapter(), WithSubjectTagger(tagger))
	e := &DataEraser{store: store}
	stored := []StoredEvent{
		{StreamID: "s", Version: 1, Metadata: setSubjectTags(Metadata{}, []string{subject})}, // tagged: tagger not consulted
		{StreamID: "s", Version: 2, Metadata: Metadata{UserID: "other", CorrelationID: "2"}}, // derived co-tenant
		{StreamID: "s", Version: 3, Metadata: Metadata{CorrelationID: "3"}},                  // derived nobody's
		{StreamID: "s", Version: 4, Metadata: Metadata{UserID: subject, CorrelationID: "4"}}, // derived subject's
	}

	var tally streamKeyTally
	ats := e.attributeStream(context.Background(), stored, subject, &tally)

	require.Len(t, ats, len(stored), "one attribution per event, by index")
	assert.True(t, ats[0].to(subject) && !ats[0].derived)
	assert.True(t, ats[1].toOther(subject) && ats[1].derived)
	assert.False(t, ats[2].attributed())
	assert.True(t, ats[3].to(subject) && ats[3].derived)
	assert.Equal(t, []string{"2", "3", "4"}, taggerCalls, "the tagger runs exactly once per untagged event, in stream order")
	assert.Equal(t, streamKeyTally{scoped: true, shared: true}, tally, "matched/untagged are collectStreamKeys' to decide")

	t.Run("an empty stream yields no attributions and a zero tally", func(t *testing.T) {
		var tally streamKeyTally
		assert.Empty(t, e.attributeStream(context.Background(), nil, subject, &tally))
		assert.Equal(t, streamKeyTally{}, tally)
	})
}

// =============================================================================
// subject.go: resolve helpers
// =============================================================================

func TestFootprintBuilder_RecordAndFinalize(t *testing.T) {
	memo := erasureMatches{}
	b := newFootprintBuilder("u1", memo)
	require.NotNil(t, b.fp.StreamEventCounts, "StreamEventCounts is allocated up front")

	b.record(StoredEvent{StreamID: "b", Version: 1, Metadata: refactorEnvelopedMeta("k2")})
	b.record(StoredEvent{StreamID: "a", Version: 1, Metadata: refactorEnvelopedMeta("k1")})
	b.record(StoredEvent{StreamID: "a", Version: 2})                                                                          // cleartext
	b.record(StoredEvent{StreamID: "a", Version: 3, Metadata: Metadata{Custom: map[string]string{encryptionKeyIDKey: "k3"}}}) // bare key id: not an envelope
	b.sharedSet["b"] = struct{}{}

	fp := b.finalize()
	assert.Same(t, b.fp, fp, "finalize returns the footprint under construction")
	assert.Equal(t, "u1", fp.SubjectID)
	assert.Equal(t, 4, fp.EventCount)
	assert.Equal(t, map[string]int{"a": 3, "b": 1}, fp.StreamEventCounts)
	assert.Equal(t, 2, fp.CleartextEvents, "only a complete envelope names a key; anything less is cleartext")
	assert.Equal(t, []string{"a", "b"}, fp.Streams, "sorted")
	assert.Equal(t, []string{"b"}, fp.SharedStreams)
	assert.Equal(t, []string{"k1", "k2"}, fp.KeyIDs, "sorted, bare key id excluded")
	assert.False(t, fp.Partial, "the builder never decides Partial")
	for _, v := range []int64{1, 2, 3} {
		assert.True(t, memo.has(StoredEvent{StreamID: "a", Version: v}), "a@%d remembered", v)
	}
	assert.True(t, memo.has(StoredEvent{StreamID: "b", Version: 1}))

	t.Run("a nil memo is accepted and ignored", func(t *testing.T) {
		b := newFootprintBuilder("u1", nil)
		b.record(StoredEvent{StreamID: "a", Version: 1})
		fp := b.finalize()
		assert.Equal(t, 1, fp.EventCount)
		assert.Equal(t, []string{"a"}, fp.Streams)
		assert.Empty(t, fp.SharedStreams)
		assert.Empty(t, fp.KeyIDs)
	})
}

func TestSubjectResolver_LoadFootprintStream(t *testing.T) {
	ctx := context.Background()
	store := New(refactorMissingStreamAdapter{EventStoreAdapter: memory.NewAdapter(), missing: "User-missing"})
	store.RegisterEvents(eraseUserCreated{})
	require.NoError(t, store.Append(ctx, "User-1", []interface{}{
		eraseUserCreated{UserID: "user-1", Email: "a@example.com"},
		eraseUserCreated{UserID: "user-1", Email: "b@example.com"},
	}))
	r := NewSubjectResolver(store)

	t.Run("existing stream is returned as found", func(t *testing.T) {
		stored, found, err := r.loadFootprintStream(ctx, "user-1", "User-1")
		require.NoError(t, err)
		assert.True(t, found)
		assert.Len(t, stored, 2)
	})

	t.Run("missing stream is skipped, not an error", func(t *testing.T) {
		stored, found, err := r.loadFootprintStream(ctx, "user-1", "User-missing")
		require.NoError(t, err)
		assert.False(t, found)
		assert.Nil(t, stored)
	})

	t.Run("an empty load (memory-adapter style) counts as found", func(t *testing.T) {
		stored, found, err := r.loadFootprintStream(ctx, "user-1", "User-empty")
		require.NoError(t, err)
		assert.True(t, found)
		assert.Empty(t, stored)
	})

	t.Run("any other load failure is wrapped with stream and subject", func(t *testing.T) {
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		_, found, err := r.loadFootprintStream(cancelled, "user-1", "User-1")
		require.Error(t, err)
		assert.False(t, found)
		assert.ErrorIs(t, err, context.Canceled)
		assert.Contains(t, err.Error(), `mink: load stream "User-1" for subject "user-1": `)
	})
}

func TestSubjectResolver_CollectIndexedStream_SharedOnlyWhenMine(t *testing.T) {
	const subject = "u1"
	tagged := func(v int64, subjects ...string) StoredEvent {
		return StoredEvent{StreamID: "s", Version: v, Metadata: setSubjectTags(Metadata{}, subjects)}
	}
	tests := []struct {
		name        string
		stored      []StoredEvent
		wantEvents  int
		wantStreams []string
		wantShared  []string
	}{
		{
			name:        "an indexed stream none of whose events are the subject's any more is neither mine nor shared",
			stored:      []StoredEvent{tagged(1, "other"), tagged(2)},
			wantEvents:  0,
			wantStreams: []string{},
			wantShared:  []string{},
		},
		{
			name:        "only the subject's events: mine, not shared",
			stored:      []StoredEvent{tagged(1, subject), tagged(2), tagged(3, subject)},
			wantEvents:  2,
			wantStreams: []string{"s"},
			wantShared:  []string{},
		},
		{
			name:        "a co-tenant anywhere in a stream of mine makes it shared, even before my first event",
			stored:      []StoredEvent{tagged(1, "other"), tagged(2, subject)},
			wantEvents:  1,
			wantStreams: []string{"s"},
			wantShared:  []string{"s"},
		},
		{
			name:        "an event tagged for both is mine and shares the stream",
			stored:      []StoredEvent{tagged(1, subject, "other")},
			wantEvents:  1,
			wantStreams: []string{"s"},
			wantShared:  []string{"s"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// A zero EventStore has no tagger: attribution is by persisted tags only.
			r := NewSubjectResolver(&EventStore{})
			b := newFootprintBuilder(subject, nil)
			r.collectIndexedStream(context.Background(), subject, "s", tt.stored, b)
			fp := b.finalize()
			assert.Equal(t, tt.wantEvents, fp.EventCount)
			assert.Equal(t, tt.wantStreams, fp.Streams)
			assert.Equal(t, tt.wantShared, fp.SharedStreams)
			assert.False(t, fp.Partial, "the index path leaves Partial to resolveByIndex")
		})
	}
}

func TestSubjectResolver_AttributeBatch_PartialOnlyForUnattributable(t *testing.T) {
	const subject = "u1"
	byUser := func(stream string, v int64, userID string) StoredEvent {
		return StoredEvent{StreamID: stream, Version: v, Metadata: Metadata{UserID: userID}}
	}
	r := NewSubjectResolver(New(memory.NewAdapter(), WithSubjectTagger(userIDTagger)))

	t.Run("the subject's events are recorded, a co-tenant's ignored, nobody's makes it Partial", func(t *testing.T) {
		memo := erasureMatches{}
		b := newFootprintBuilder(subject, memo)
		r.attributeBatch(context.Background(), subject, []StoredEvent{
			byUser("a", 1, subject),
			byUser("b", 1, "other"),
			byUser("c", 1, ""),
		}, b)
		fp := b.finalize()
		assert.Equal(t, 1, fp.EventCount)
		assert.Equal(t, []string{"a"}, fp.Streams)
		assert.True(t, fp.Partial, "an unattributable event could be the subject's")
		assert.True(t, memo.has(byUser("a", 1, subject)))
		assert.False(t, memo.has(byUser("b", 1, "other")))
		assert.False(t, memo.has(byUser("c", 1, "")))
	})

	t.Run("a batch of attributable events leaves Partial false", func(t *testing.T) {
		b := newFootprintBuilder(subject, nil)
		r.attributeBatch(context.Background(), subject, []StoredEvent{
			byUser("a", 1, subject),
			byUser("b", 1, "other"),
		}, b)
		assert.False(t, b.finalize().Partial)
	})
}

// =============================================================================
// verify.go: verifyStreams helpers and residualNotes
// =============================================================================

// refactor2RevocationConfig returns a FieldEncryptionConfig over a local provider whose
// key "k" is in the requested revocation state.
func refactor2RevocationConfig(t *testing.T, state encryption.RevocationState) *FieldEncryptionConfig {
	t.Helper()
	p, err := local.New(local.WithKey("k", make([]byte, 32)))
	require.NoError(t, err)
	switch state {
	case encryption.Revoked:
		require.NoError(t, p.RevokeKey("k"))
	case encryption.SoftRevoked:
		require.NoError(t, p.SoftRevokeKey("k", time.Hour))
	}
	return NewFieldEncryptionConfig(WithEncryptionProvider(p), WithDefaultKeyID("k"))
}

func TestVerificationReport_RecordChecked(t *testing.T) {
	tests := []struct {
		name string
		meta Metadata
		cfg  func(t *testing.T) *FieldEncryptionConfig
		want VerificationReport
	}{
		{
			name: "no envelope is residual cleartext",
			meta: Metadata{},
			cfg:  func(t *testing.T) *FieldEncryptionConfig { return refactor2RevocationConfig(t, encryption.Revoked) },
			want: VerificationReport{EventsChecked: 1, ResidualCleartext: []string{"s@1"}},
		},
		{
			name: "a bare key id without the rest of the envelope is residual cleartext",
			meta: Metadata{Custom: map[string]string{encryptionKeyIDKey: "k"}},
			cfg:  func(t *testing.T) *FieldEncryptionConfig { return refactor2RevocationConfig(t, encryption.Revoked) },
			want: VerificationReport{EventsChecked: 1, ResidualCleartext: []string{"s@1"}},
		},
		{
			name: "an envelope under a live key is residual ciphertext",
			meta: refactorEnvelopedMeta("k"),
			cfg:  func(t *testing.T) *FieldEncryptionConfig { return refactor2RevocationConfig(t, encryption.NotRevoked) },
			want: VerificationReport{EventsChecked: 1, ResidualEncrypted: []string{"s@1"}},
		},
		{
			name: "an envelope under a soft-revoked key is still recoverable",
			meta: refactorEnvelopedMeta("k"),
			cfg:  func(t *testing.T) *FieldEncryptionConfig { return refactor2RevocationConfig(t, encryption.SoftRevoked) },
			want: VerificationReport{EventsChecked: 1, ResidualRecoverable: []string{"s@1"}},
		},
		{
			name: "an envelope under a permanently revoked key is redacted",
			meta: refactorEnvelopedMeta("k"),
			cfg:  func(t *testing.T) *FieldEncryptionConfig { return refactor2RevocationConfig(t, encryption.Revoked) },
			want: VerificationReport{EventsChecked: 1, RedactedEvents: 1},
		},
		{
			name: "no encryption config: nothing can be revoked, so the envelope is residual ciphertext",
			meta: refactorEnvelopedMeta("k"),
			cfg:  func(*testing.T) *FieldEncryptionConfig { return nil },
			want: VerificationReport{EventsChecked: 1, ResidualEncrypted: []string{"s@1"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var rep VerificationReport
			rep.recordChecked("s@1", tt.meta, tt.cfg(t))
			assert.Equal(t, tt.want, rep)
		})
	}

	t.Run("checked events accumulate", func(t *testing.T) {
		cfg := refactor2RevocationConfig(t, encryption.Revoked)
		var rep VerificationReport
		rep.recordChecked("s@1", refactorEnvelopedMeta("k"), cfg)
		rep.recordChecked("s@2", Metadata{}, cfg)
		rep.recordChecked("s@3", refactorEnvelopedMeta("k"), cfg)
		assert.Equal(t, VerificationReport{EventsChecked: 3, RedactedEvents: 2, ResidualCleartext: []string{"s@2"}}, rep)
	})
}

func TestStreamVerifier_OwnsEvent(t *testing.T) {
	const subject = "u1"
	tagged := func(subjects ...string) StoredEvent {
		return StoredEvent{StreamID: "s", Version: 1, Metadata: setSubjectTags(Metadata{}, subjects)}
	}
	shredded := refactorEnvelopedMeta("k")
	revokedCfg := refactor2RevocationConfig(t, encryption.Revoked)

	tests := []struct {
		name       string
		se         StoredEvent
		attributed erasureMatches
		cfg        *FieldEncryptionConfig
		wantOwned  bool
		wantReport VerificationReport
	}{
		{
			name:       "remembered by the erasure: owned without re-attributing",
			se:         StoredEvent{StreamID: "s", Version: 1, Metadata: shredded}, // untagged and undecryptable now
			attributed: erasureMatches{"s": {1: {}}},
			cfg:        revokedCfg,
			wantOwned:  true,
		},
		{name: "tagged for the subject: owned", se: tagged(subject), wantOwned: true},
		{name: "tagged for the subject and a co-tenant: owned", se: tagged("other", subject), wantOwned: true},
		{name: "a co-tenant's event: skipped silently", se: tagged("other")},
		{
			name:       "nobody's readable event: filed as recoverable and forces Partial",
			se:         StoredEvent{StreamID: "s", Version: 1},
			wantReport: VerificationReport{UnattributedRecoverable: []string{"s@1"}, Partial: true},
		},
		{
			name:       "nobody's permanently shredded event: filed as redacted, no Partial",
			se:         StoredEvent{StreamID: "s", Version: 1, Metadata: shredded},
			cfg:        revokedCfg,
			wantReport: VerificationReport{UnattributedRedacted: []string{"s@1"}},
		},
		{
			name:       "nobody's event under a live key is recoverable",
			se:         StoredEvent{StreamID: "s", Version: 1, Metadata: shredded},
			cfg:        refactor2RevocationConfig(t, encryption.NotRevoked),
			wantReport: VerificationReport{UnattributedRecoverable: []string{"s@1"}, Partial: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var rep VerificationReport
			// A zero EventStore has no tagger: attribution is by persisted tags only.
			v := &streamVerifier{eraser: &DataEraser{store: &EventStore{}}, subjectID: subject, attributed: tt.attributed, rep: &rep, cfg: tt.cfg}
			assert.Equal(t, tt.wantOwned, v.ownsEvent(context.Background(), tt.se, "s@1"))
			assert.Equal(t, tt.wantReport, rep, "ownsEvent only ever files unattributed events")
		})
	}
}

func TestStreamVerifier_VerifyStream_CountsOnlyOwnedEvents(t *testing.T) {
	const subject = "u1"
	cfg := refactor2RevocationConfig(t, encryption.Revoked)
	stored := []StoredEvent{
		{StreamID: "s", Version: 1, Metadata: setSubjectTags(refactorEnvelopedMeta("k"), []string{subject})}, // mine, shredded
		{StreamID: "s", Version: 2, Metadata: setSubjectTags(Metadata{}, []string{"other"})},                 // co-tenant's: skipped
		{StreamID: "s", Version: 3, Metadata: setSubjectTags(Metadata{}, []string{subject})},                 // mine, cleartext
		{StreamID: "s", Version: 4},                                       // nobody's, readable
		{StreamID: "s", Version: 5, Metadata: refactorEnvelopedMeta("k")}, // remembered by the erasure
	}
	var rep VerificationReport
	v := &streamVerifier{eraser: &DataEraser{store: &EventStore{}}, subjectID: subject, attributed: erasureMatches{"s": {5: {}}}, rep: &rep, cfg: cfg}
	v.verifyStream(context.Background(), stored)
	assert.Equal(t, VerificationReport{
		EventsChecked:           3,
		RedactedEvents:          2,
		ResidualCleartext:       []string{"s@3"},
		UnattributedRecoverable: []string{"s@4"},
		Partial:                 true,
	}, rep)
}

func TestResidualNotes_UnattributedNoteOnlyWhenPresent(t *testing.T) {
	tests := []struct {
		name string
		rep  VerificationReport
		want []string
	}{
		{name: "nothing unattributed: no note", rep: VerificationReport{EventsChecked: 2}},
		{
			name: "recoverable only",
			rep:  VerificationReport{UnattributedRecoverable: []string{"s@1", "s@2"}},
			want: []string{unattributedNote(2, 0)},
		},
		{
			name: "redacted only",
			rep:  VerificationReport{UnattributedRedacted: []string{"s@1"}},
			want: []string{unattributedNote(0, 1)},
		},
		{
			name: "both, after the recoverable-key note",
			rep:  VerificationReport{ResidualRecoverable: []string{"s@9"}, UnattributedRecoverable: []string{"s@1"}, UnattributedRedacted: []string{"s@2", "s@3"}},
			want: []string{
				"1 event(s) remain recoverable: their key is only soft-revoked and can still be restored within its grace window",
				unattributedNote(1, 2),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, residualNotes(&tt.rep))
		})
	}
}
