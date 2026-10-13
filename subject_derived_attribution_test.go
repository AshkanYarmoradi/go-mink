package mink

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go-mink.dev/adapters/memory"
	"go-mink.dev/encryption/local"
)

// subject_derived_attribution_test.go pins the ONE attribution rule shared by the
// resolver, eraser, exporter, verification and backfill: an event is a subject's when it
// carries the subject's persisted "$subjects" tag OR, when untagged, when the tagger
// derives the subject from its plaintext. A derived-only historical event (written before
// tagging was enabled; Metadata.UserID set, no tag) must therefore be resolved, exported,
// shredded, guarded and verified exactly like a tagged one.

// derivedStores builds a tagged, field-encrypted store over a fresh adapter plus a
// "legacy" writer over the SAME adapter with the same encryption but NO tagger — the
// writer that produced history before subject tagging was enabled. Events appended
// through legacy carry no "$subjects" tag.
func derivedStores(t *testing.T, keyID string) (tagged, legacy *EventStore, provider *local.Provider) {
	t.Helper()
	var err error
	provider, err = local.New(local.WithKey(keyID, make([]byte, 32)))
	require.NoError(t, err)
	cfg := NewFieldEncryptionConfig(
		WithEncryptionProvider(provider),
		WithDefaultKeyID(keyID),
		WithEncryptedFields("eraseUserCreated", "email"),
		WithDecryptionErrorHandler(func(error, string, Metadata) error { return nil }), // crypto-shred: leave ciphertext
	)
	adapter := memory.NewAdapter()
	tagged = New(adapter, WithFieldEncryption(cfg), WithSubjectTagger(userIDTagger))
	tagged.RegisterEvents(eraseUserCreated{}, plainNote{}, ErasureMarker{})
	legacy = New(adapter, WithFieldEncryption(cfg))
	legacy.RegisterEvents(eraseUserCreated{}, plainNote{})
	return tagged, legacy, provider
}

// appendLegacyNote appends a plaintext note with Metadata.UserID through the legacy
// writer: derivable by userIDTagger, but carrying no persisted tag.
func appendLegacyNote(t *testing.T, ctx context.Context, legacy *EventStore, stream, userID string) {
	t.Helper()
	var opts []AppendOption
	if userID != "" {
		opts = append(opts, WithAppendMetadata(Metadata{UserID: userID}))
	}
	require.NoError(t, legacy.Append(ctx, stream, []interface{}{plainNote{Text: "note"}}, opts...))
	raw, err := legacy.LoadRaw(ctx, stream, 0)
	require.NoError(t, err)
	require.Empty(t, GetSubjectTags(raw[len(raw)-1].Metadata), "precondition: no persisted tag")
}

// appendLegacyUser appends an encrypted eraseUserCreated with Metadata.UserID through
// the legacy writer: derivable, encrypted under the default key, no persisted tag.
func appendLegacyUser(t *testing.T, ctx context.Context, legacy *EventStore, stream, userID string) {
	t.Helper()
	require.NoError(t, legacy.Append(ctx, stream,
		[]interface{}{eraseUserCreated{UserID: userID, Email: userID + "@example.com"}},
		WithAppendMetadata(Metadata{UserID: userID})))
	raw, err := legacy.LoadRaw(ctx, stream, 0)
	require.NoError(t, err)
	require.Empty(t, GetSubjectTags(raw[len(raw)-1].Metadata), "precondition: no persisted tag")
	require.True(t, HasEncryptionEnvelope(raw[len(raw)-1].Metadata), "precondition: encrypted")
}

// The review scenario: Note-n1 holds an event with Metadata.UserID "u3" and no persisted
// tag. Backfill indexes it; an index-backed (authoritative) resolve and a scan both put
// it in u3's footprint — not just in the raw index.
func TestSubjectResolver_DerivedAttribution_IndexAndScanPaths(t *testing.T) {
	ctx := context.Background()
	tagged, legacy, _ := derivedStores(t, "k")
	appendLegacyNote(t, ctx, legacy, "Note-n1", "u3")
	appendUser(t, ctx, tagged, "User-u1", "u1") // a tagged neighbour, not u3's

	idx := NewMemorySubjectIndex()
	rep, err := BackfillSubjectIndexWithReport(ctx, tagged, userIDTagger, idx, 10)
	require.NoError(t, err)
	assert.Equal(t, BackfillReport{Scanned: 2, Indexed: 2}, rep, "a clean backfill: every event attributed")

	for name, resolver := range map[string]*SubjectResolver{
		"index": NewSubjectResolver(tagged, WithResolverIndex(idx), WithAuthoritativeIndex()),
		"scan":  NewSubjectResolver(tagged),
	} {
		t.Run(name, func(t *testing.T) {
			fp, err := resolver.Resolve(ctx, "u3")
			require.NoError(t, err)
			assert.Equal(t, []string{"Note-n1"}, fp.Streams)
			assert.Equal(t, 1, fp.EventCount)
			assert.Equal(t, map[string]int{"Note-n1": 1}, fp.StreamEventCounts)
			assert.Equal(t, 1, fp.CleartextEvents, "a plaintext note has no key to revoke")
			assert.Empty(t, fp.KeyIDs)
			assert.Empty(t, fp.SharedStreams)
			assert.False(t, fp.Partial, "every event in the store is attributable")

			// u1's footprint is unaffected: the derived event is u3's, not u1's.
			fp, err = resolver.Resolve(ctx, "u1")
			require.NoError(t, err)
			assert.Equal(t, []string{"User-u1"}, fp.Streams)
			assert.Equal(t, 1, fp.EventCount)
		})
	}
}

// An untagged event the tagger cannot attribute (no UserID), or cannot see (encrypted,
// undecryptable), still keeps a scan footprint honestly Partial — exactly as before.
func TestSubjectResolver_DerivedAttribution_UnattributableStillPartial(t *testing.T) {
	ctx := context.Background()

	t.Run("tagger derives nothing", func(t *testing.T) {
		tagged, legacy, _ := derivedStores(t, "k")
		appendUser(t, ctx, tagged, "User-u1", "u1")
		appendLegacyNote(t, ctx, legacy, "Note-anon", "") // no UserID: nobody's

		fp, err := NewSubjectResolver(tagged).Resolve(ctx, "u1")
		require.NoError(t, err)
		assert.Equal(t, []string{"User-u1"}, fp.Streams)
		assert.True(t, fp.Partial, "an unattributable event could be the subject's")
	})

	t.Run("undecryptable: the tagger is never run over ciphertext", func(t *testing.T) {
		tagged, legacy, provider := derivedStores(t, "k")
		appendUser(t, ctx, tagged, "User-u1", "u1")
		appendLegacyUser(t, ctx, legacy, "Legacy-u3", "u3") // derivable from metadata... while readable
		require.NoError(t, provider.RevokeKey("k"))

		fp, err := NewSubjectResolver(tagged).Resolve(ctx, "u3")
		require.NoError(t, err)
		assert.Empty(t, fp.Streams, "once shredded, a derived-only encrypted event is unattributable")
		assert.True(t, fp.Partial)
	})

	t.Run("no tagger anywhere: persisted tags only, as before", func(t *testing.T) {
		_, legacy, _ := derivedStores(t, "k")
		appendLegacyNote(t, ctx, legacy, "Note-n1", "u3")

		fp, err := NewSubjectResolver(legacy).Resolve(ctx, "u3")
		require.NoError(t, err)
		assert.Empty(t, fp.Streams)
		assert.True(t, fp.Partial)
	})
}

// WithResolverTagger overrides the store's tagger (or supplies one to a store without),
// and the eraser and exporter built on that resolver attribute with the same tagger.
func TestWithResolverTagger_OverridesStoreTagger(t *testing.T) {
	ctx := context.Background()
	_, legacy, _ := derivedStores(t, "k") // legacy has no tagger
	appendLegacyNote(t, ctx, legacy, "Note-n1", "u3")
	appendLegacyNote(t, ctx, legacy, "Note-n2", "u4")

	// Supplied to a tagger-less store.
	fp, err := NewSubjectResolver(legacy, WithResolverTagger(userIDTagger)).Resolve(ctx, "u3")
	require.NoError(t, err)
	assert.Equal(t, []string{"Note-n1"}, fp.Streams)
	assert.False(t, fp.Partial)

	// A different tagger changes attribution: this one attributes everything to "all".
	allTagger := func(string, []byte, Metadata) []string { return []string{"all"} }
	fp, err = NewSubjectResolver(legacy, WithResolverTagger(allTagger)).Resolve(ctx, "all")
	require.NoError(t, err)
	assert.Equal(t, []string{"Note-n1", "Note-n2"}, fp.Streams)
	fp, err = NewSubjectResolver(legacy, WithResolverTagger(allTagger)).Resolve(ctx, "u3")
	require.NoError(t, err)
	assert.Empty(t, fp.Streams, "under the override, nothing is u3's")

	// nil is ignored.
	r := NewSubjectResolver(legacy, WithResolverTagger(nil))
	assert.Nil(t, r.tagger)

	// The exporter and eraser take the resolver's tagger over the store's (none here).
	resolver := NewSubjectResolver(legacy, WithResolverTagger(userIDTagger))
	res, err := NewDataExporter(legacy, WithExportSubjectResolver(resolver)).Export(ctx, ExportRequest{SubjectID: "u3"})
	require.NoError(t, err)
	assert.Equal(t, 1, res.TotalEvents)
	assert.Equal(t, []string{"Note-n1"}, res.Streams)
	assert.False(t, res.Partial)

	e := NewDataEraser(legacy, WithEraseSubjectResolver(resolver))
	require.NotNil(t, e.tagger(), "the eraser attributes with the resolver's tagger")
	assert.Nil(t, NewDataEraser(legacy).tagger(), "without a resolver the store's tagger (here none) applies")
	assert.Nil(t, NewDataExporter(legacy).tagger())
}

// A resolved export contains the derived-only event; explicit Streams with no Filter drop
// untagged events the tagger attributes to OTHER subjects only and keep the ones nobody can
// attribute.
func TestDataExporter_DerivedAttribution(t *testing.T) {
	ctx := context.Background()
	tagged, legacy, _ := derivedStores(t, "k")
	appendLegacyNote(t, ctx, legacy, "Note-n1", "u3")
	appendLegacyNote(t, ctx, legacy, "Shared-s1", "u3")
	appendLegacyNote(t, ctx, legacy, "Shared-s1", "u4") // a co-tenant's, derived only
	appendLegacyNote(t, ctx, legacy, "Shared-s1", "")   // nobody's
	appendUser(t, ctx, tagged, "Shared-s1", "u1")       // a tagged co-tenant

	t.Run("resolved export includes the derived-only events and nothing else", func(t *testing.T) {
		exporter := NewDataExporter(tagged, WithExportSubjectResolver(NewSubjectResolver(tagged)))
		res, err := exporter.Export(ctx, ExportRequest{SubjectID: "u3"})
		require.NoError(t, err)
		assert.True(t, res.Partial, "the nobody's event keeps the scan footprint Partial")
		assert.Equal(t, 2, res.TotalEvents, "Note-n1 and u3's event in Shared-s1")
		for _, ev := range res.Events {
			assert.Contains(t, []string{"Note-n1", "Shared-s1"}, ev.StreamID)
		}
		// ExportStream refuses a Partial footprint rather than streaming it.
		err = exporter.ExportStream(ctx, ExportRequest{SubjectID: "u3"}, func(context.Context, ExportedEvent) error { return nil })
		assert.ErrorIs(t, err, ErrExportPartialFootprint)
	})

	t.Run("explicit streams: other subjects' derived events are dropped, unattributable kept", func(t *testing.T) {
		res, err := NewDataExporter(tagged).Export(ctx, ExportRequest{SubjectID: "u3", Streams: []string{"Shared-s1"}})
		require.NoError(t, err)
		assert.Equal(t, 2, res.TotalEvents, "u3's derived event + the unattributable one; u4's (derived) and u1's (tagged) are dropped")

		// Without a tagger the same request keeps every untagged event (SubjectOrUntaggedFilter).
		res, err = NewDataExporter(legacy).Export(ctx, ExportRequest{SubjectID: "u3", Streams: []string{"Shared-s1"}})
		require.NoError(t, err)
		assert.Equal(t, 3, res.TotalEvents, "u3's, u4's and nobody's untagged events; only the tagged u1 is dropped")
	})

	t.Run("an explicit SubjectFilter stays tag-only", func(t *testing.T) {
		res, err := NewDataExporter(tagged).Export(ctx, ExportRequest{SubjectID: "u3", Streams: []string{"Shared-s1"}, Filter: SubjectFilter("u3")})
		require.NoError(t, err)
		assert.Equal(t, 0, res.TotalEvents, "SubjectFilter reads persisted tags only, by contract")
	})
}

// A derived-only ENCRYPTED event: the eraser finds its key through the tagger while it
// is still readable, revokes it, and the certificate verifies that very event (the
// attribution was established before the revoke made the event undecryptable). A
// standalone Verify afterwards is honestly Vacuous: nothing attributes the shredded event
// any more.
func TestDataEraser_DerivedAttribution_EncryptedEventIsShreddedAndCertified(t *testing.T) {
	ctx := context.Background()
	for name, indexed := range map[string]bool{"index": true, "scan": false} {
		t.Run(name, func(t *testing.T) {
			tagged, legacy, provider := derivedStores(t, "k3")
			appendLegacyUser(t, ctx, legacy, "User-u3", "u3")

			resolver := NewSubjectResolver(tagged)
			if indexed {
				idx := NewMemorySubjectIndex()
				rep, err := BackfillSubjectIndexWithReport(ctx, tagged, userIDTagger, idx, 10)
				require.NoError(t, err)
				require.Equal(t, BackfillReport{Scanned: 1, Indexed: 1}, rep)
				resolver = NewSubjectResolver(tagged, WithResolverIndex(idx), WithAuthoritativeIndex())
			}
			var cert ErasureCertificate
			eraser := NewDataEraser(tagged, WithEraseSubjectResolver(resolver), captureCert(&cert))

			// Before erasure the derived-only event is attributable and residual.
			vr, err := eraser.Verify(ctx, "u3")
			require.NoError(t, err)
			assert.Equal(t, 1, vr.EventsChecked)
			assert.Equal(t, []string{"User-u3@1"}, vr.ResidualEncrypted)
			assert.False(t, vr.Verified)

			res, err := eraser.Erase(ctx, ErasureRequest{SubjectID: "u3"})
			require.NoError(t, err)
			assert.Equal(t, []string{"k3"}, res.KeysRevoked, "the derived-only event's key is discovered and revoked")
			assert.Equal(t, []string{"User-u3"}, res.Streams)
			assert.Equal(t, 1, res.EventsScanned)
			assert.False(t, res.Failed(), "%v", res.Errors)
			assert.False(t, res.Partial)
			revoked, _ := provider.IsRevoked("k3")
			assert.True(t, revoked)

			assert.Equal(t, 1, cert.EventsChecked, "the certificate verifies the event matched before the revoke")
			assert.True(t, cert.Verified, "%v", cert.Notes)
			assert.False(t, cert.Partial)

			// Post-shred, nothing can attribute the event: a fresh Verify says so instead
			// of certifying from zero evidence.
			vr, err = eraser.Verify(ctx, "u3")
			require.NoError(t, err)
			assert.True(t, vr.Vacuous)
			assert.False(t, vr.Verified)
			assert.Equal(t, 0, vr.EventsChecked)
			assert.True(t, notesContain(vr.Notes, "can no longer be attributed once its key is revoked"), "%v", vr.Notes)
		})
	}
}

// A subject with both a tagged and a derived-only encrypted event under one key: after
// the erasure, a standalone Verify through an authoritative index checks the tagged
// event, lists the now-unattributable shredded one as UnattributedRedacted (no recoverable
// PII, so it does not block), and verifies; a scan resolver, which sees the unattributable
// event store-wide, stays honestly Partial.
func TestVerify_DerivedAttribution_ShreddedUnattributableIsReportedNotBlocking(t *testing.T) {
	ctx := context.Background()
	tagged, legacy, _ := derivedStores(t, "k")
	appendUser(t, ctx, tagged, "User-u3", "u3")       // tagged, encrypted under k
	appendLegacyUser(t, ctx, legacy, "User-u3", "u3") // derived only, encrypted under k

	idx := NewMemorySubjectIndex()
	_, err := BackfillSubjectIndexWithReport(ctx, tagged, userIDTagger, idx, 10)
	require.NoError(t, err)
	indexed := NewSubjectResolver(tagged, WithResolverIndex(idx), WithAuthoritativeIndex())

	var cert ErasureCertificate
	res, err := NewDataEraser(tagged, WithEraseSubjectResolver(indexed), captureCert(&cert)).Erase(ctx, ErasureRequest{SubjectID: "u3"})
	require.NoError(t, err)
	assert.Equal(t, []string{"k"}, res.KeysRevoked)
	assert.Equal(t, 2, res.EventsScanned)
	assert.Equal(t, 2, cert.EventsChecked, "both events were attributed before the revoke")
	assert.True(t, cert.Verified, "%v", cert.Notes)

	rep, err := NewDataEraser(tagged, WithEraseSubjectResolver(indexed)).Verify(ctx, "u3")
	require.NoError(t, err)
	assert.Equal(t, 1, rep.EventsChecked, "only the tagged event is still attributable")
	assert.Equal(t, 1, rep.RedactedEvents)
	assert.Equal(t, []string{"User-u3@2"}, rep.UnattributedRedacted)
	assert.Empty(t, rep.UnattributedRecoverable)
	assert.False(t, rep.Partial)
	assert.True(t, rep.Verified, "a shredded event nobody can attribute holds no recoverable PII: %v", rep.Notes)
	assert.True(t, notesContain(rep.Notes, "1 permanently crypto-shredded (no recoverable PII)"), "%v", rep.Notes)

	rep, err = NewDataEraser(tagged, WithEraseSubjectResolver(NewSubjectResolver(tagged))).Verify(ctx, "u3")
	require.NoError(t, err)
	assert.Equal(t, 1, rep.EventsChecked)
	assert.True(t, rep.Partial, "the scan saw an unattributable event in the store")
	assert.False(t, rep.Verified)
}

// An unattributable event with still-readable PII in the subject's own stream is never
// silently skipped by Verify or the certificate: it is listed, noted, and forces Partial —
// even behind an authoritative index that cannot see it.
func TestVerify_UnattributedRecoverableEventForcesPartial(t *testing.T) {
	ctx := context.Background()
	tagged, legacy, _ := derivedStores(t, "k")
	appendUser(t, ctx, tagged, "User-u1", "u1")
	appendLegacyNote(t, ctx, legacy, "User-u1", "") // in u1's stream, nobody's, cleartext

	idx := NewMemorySubjectIndex()
	require.NoError(t, idx.IndexSubjects(ctx, "User-u1", []string{"u1"}))
	indexed := NewSubjectResolver(tagged, WithResolverIndex(idx), WithAuthoritativeIndex())
	fp, err := indexed.Resolve(ctx, "u1")
	require.NoError(t, err)
	require.False(t, fp.Partial, "the index path trusts the authority assertion")

	rep, err := NewDataEraser(tagged, WithEraseSubjectResolver(indexed)).Verify(ctx, "u1")
	require.NoError(t, err)
	assert.Equal(t, 1, rep.EventsChecked)
	assert.Equal(t, []string{"User-u1@2"}, rep.UnattributedRecoverable)
	assert.Empty(t, rep.UnattributedRedacted)
	assert.True(t, rep.Partial, "a readable event nobody can attribute may be the subject's")
	assert.False(t, rep.Verified)
	assert.True(t, notesContain(rep.Notes, "1 event(s) in the subject's streams could not be attributed to any subject"), "%v", rep.Notes)
	assert.True(t, notesContain(rep.Notes, "1 still readable or recoverable"), "%v", rep.Notes)
	for _, n := range rep.Notes {
		assert.NotContains(t, n, "User-u1", "notes carry counts only")
	}

	var cert ErasureCertificate
	res, err := NewDataEraser(tagged, WithEraseSubjectResolver(indexed), captureCert(&cert)).Erase(ctx, ErasureRequest{SubjectID: "u1"})
	require.NoError(t, err)
	assert.False(t, res.Partial, "the erasure's footprint itself was not Partial")
	assert.Equal(t, 1, cert.EventsChecked)
	assert.True(t, cert.Partial, "the certificate carries the verification's Partial")
	assert.False(t, cert.Verified)
	assert.True(t, notesContain(cert.Notes, "could not be attributed to any subject"), "%v", cert.Notes)
}

// Another subject's derived-only event makes a stream shared on every path: the
// resolver's scan and index paths, and the eraser's Streams and Filter paths.
func TestSharedStreams_DerivedAttributionOfOtherSubject(t *testing.T) {
	ctx := context.Background()
	tagged, legacy, _ := derivedStores(t, "k")
	appendLegacyNote(t, ctx, legacy, "Shared-s1", "u2") // the co-tenant: derived only, written first
	appendUser(t, ctx, tagged, "Shared-s1", "u1")
	appendUser(t, ctx, tagged, "User-u1", "u1")

	idx := NewMemorySubjectIndex()
	_, err := BackfillSubjectIndexWithReport(ctx, tagged, userIDTagger, idx, 10)
	require.NoError(t, err)

	for name, resolver := range map[string]*SubjectResolver{
		"scan":  NewSubjectResolver(tagged),
		"index": NewSubjectResolver(tagged, WithResolverIndex(idx), WithAuthoritativeIndex()),
	} {
		t.Run("resolver "+name, func(t *testing.T) {
			fp, err := resolver.Resolve(ctx, "u1")
			require.NoError(t, err)
			assert.Equal(t, []string{"Shared-s1", "User-u1"}, fp.Streams)
			assert.Equal(t, []string{"Shared-s1"}, fp.SharedStreams)
			assert.Equal(t, []string{"User-u1"}, fp.ExclusiveStreams())
			assert.Equal(t, 2, fp.EventCount)
			assert.False(t, fp.Partial)
		})
	}

	byStreams, err := NewDataEraser(tagged).Erase(ctx, ErasureRequest{SubjectID: "u1", Streams: []string{"Shared-s1", "User-u1"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"Shared-s1"}, byStreams.SharedStreams)
	assert.Equal(t, 2, byStreams.EventsScanned, "u2's derived event is a co-tenant's, not matched")
	assert.False(t, byStreams.Partial)

	byFilter, err := NewDataEraser(tagged).Erase(ctx, ErasureRequest{SubjectID: "u1", Filter: SubjectFilter("u1")})
	require.NoError(t, err)
	assert.Equal(t, []string{"Shared-s1"}, byFilter.SharedStreams)
}

// The shared-key guard sees a derived-only co-tenant under the candidate key and refuses
// — before anything is revoked — exactly as it would for a tagged one.
func TestDataEraser_SharedKeyGuard_DerivedAttributionOfOtherSubject(t *testing.T) {
	ctx := context.Background()
	tagged, legacy, provider := derivedStores(t, "k") // one key for everyone
	appendUser(t, ctx, tagged, "User-u1", "u1")
	appendLegacyUser(t, ctx, legacy, "User-u2", "u2") // derived only, same key

	_, err := NewDataEraser(tagged, WithEraseSubjectResolver(NewSubjectResolver(tagged)), WithSharedKeyGuard()).
		Erase(ctx, ErasureRequest{SubjectID: "u1"})
	require.Error(t, err)
	var ske *SharedKeyError
	require.True(t, errors.As(err, &ske))
	assert.ErrorIs(t, err, ErrSharedKeyRevocation)
	assert.Equal(t, []string{"k"}, ske.SharedKeys)
	assert.Equal(t, 1, ske.OtherSubjectCount, "the derived-only co-tenant is counted")
	revoked, _ := provider.IsRevoked("k")
	assert.False(t, revoked, "nothing was revoked")

	// A store without a tagger cannot attribute u2's event: ownership unprovable → still
	// shared, the conservative verdict the guard always gave untagged events.
	_, err = NewDataEraser(legacy, WithSharedKeyGuard()).Erase(ctx, ErasureRequest{SubjectID: "u1", Streams: []string{"User-u1"}})
	require.Error(t, err)
	require.True(t, errors.As(err, &ske))
	assert.Equal(t, 0, ske.OtherSubjectCount, "unattributable: shared, but no other subject is named")
}

// On the Streams path a tagger scopes a legacy (untagged) stream by derived attribution
// exactly as persisted tags would: the subject's derived events are matched and attested,
// a co-tenant's are skipped (and make the stream shared), unattributable ones are skipped,
// counted and make the result Partial.
func TestErase_StreamsScope_DerivedAttributionScopesLegacyStream(t *testing.T) {
	ctx := context.Background()
	tagged, legacy, provider := derivedStores(t, "k")
	appendLegacyUser(t, ctx, legacy, "Legacy-l1", "u1")
	appendLegacyUser(t, ctx, legacy, "Legacy-l1", "u2")
	require.NoError(t, legacy.Append(ctx, "Legacy-l1", []interface{}{eraseUserCreated{Email: "nobody@example.com"}})) // no UserID

	var cert ErasureCertificate
	res, err := NewDataEraser(tagged, captureCert(&cert)).Erase(ctx, ErasureRequest{SubjectID: "u1", Streams: []string{"Legacy-l1"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"k"}, res.KeysRevoked)
	assert.Equal(t, 1, res.EventsScanned, "only u1's derived event is matched")
	assert.Equal(t, []string{"Legacy-l1"}, res.SharedStreams, "u2's derived event makes the stream shared")
	assert.True(t, res.Partial, "the unattributable event may be u1's")
	require.Len(t, res.Notes, 1)
	assert.Contains(t, res.Notes[0], "1 untagged event(s) in 1 listed stream(s) that also hold subject-attributed events")
	assert.Equal(t, 1, cert.EventsChecked, "u1's event is attested from the pre-revoke attribution")
	assert.False(t, cert.Verified)
	revoked, _ := provider.IsRevoked("k")
	assert.True(t, revoked)

	// The same stream through a store WITHOUT a tagger is legacy scope: everything
	// collected, nothing attested — unchanged behavior.
	_, legacy2, _ := derivedStores(t, "k")
	appendLegacyUser(t, ctx, legacy2, "Legacy-l1", "u1")
	appendLegacyUser(t, ctx, legacy2, "Legacy-l1", "u2")
	var cert2 ErasureCertificate
	res, err = NewDataEraser(legacy2, captureCert(&cert2)).Erase(ctx, ErasureRequest{SubjectID: "u1", Streams: []string{"Legacy-l1"}})
	require.NoError(t, err)
	assert.Equal(t, 2, res.EventsScanned)
	assert.False(t, res.Partial)
	assert.Empty(t, res.SharedStreams)
	require.Len(t, res.Notes, 1)
	assert.Contains(t, res.Notes[0], "1 of 1 listed stream(s) hold no event attributable to any subject")
	assert.Equal(t, 0, cert2.EventsChecked)
	assert.False(t, cert2.Verified)
	assert.True(t, notesContain(cert2.Notes, "no events attributed to the subject could be verified"), "%v", cert2.Notes)
}

// The attribution helper itself: tags win, the tagger covers untagged events, and an
// undecryptable encrypted event is never handed to the tagger.
func TestEventStore_AttributeSubjects(t *testing.T) {
	ctx := context.Background()
	tagged, legacy, provider := derivedStores(t, "k")
	appendLegacyUser(t, ctx, legacy, "Legacy-u3", "u3")
	raw, err := tagged.LoadRaw(ctx, "Legacy-u3", 0)
	require.NoError(t, err)
	encrypted := raw[0]

	taggerCalls := 0
	spy := func(_ string, data []byte, md Metadata) []string {
		taggerCalls++
		assert.False(t, IsEncrypted(md), "the tagger sees the envelope stripped")
		assert.Contains(t, string(data), "u3@example.com", "the tagger sees plaintext")
		return userIDTagger("", data, md)
	}

	// Persisted tags win without consulting the tagger.
	at := tagged.attributeSubjects(ctx, spy, StoredEvent{Metadata: setSubjectTags(Metadata{UserID: "someone-else"}, []string{"t1", "t2"})})
	assert.Equal(t, subjectAttribution{subjects: []string{"t1", "t2"}}, at)
	assert.True(t, at.attributed())
	assert.True(t, at.to("t1"))
	assert.False(t, at.to("zz"))
	assert.True(t, at.toOther("t1"), "t2 is another subject")
	assert.True(t, at.toOther("zz"))
	assert.False(t, subjectAttribution{subjects: []string{"t1"}}.toOther("t1"))
	assert.Equal(t, 0, taggerCalls)

	// Untagged + encrypted + readable: decrypted, then derived.
	at = tagged.attributeSubjects(ctx, spy, encrypted)
	assert.Equal(t, subjectAttribution{subjects: []string{"u3"}, derived: true}, at)
	assert.Equal(t, 1, taggerCalls)

	// nil tagger → the store's own.
	at = tagged.attributeSubjects(ctx, nil, encrypted)
	assert.Equal(t, subjectAttribution{subjects: []string{"u3"}, derived: true}, at)

	// A store with neither tagger: unattributed, and no decryption attempted.
	at = legacy.attributeSubjects(ctx, nil, encrypted)
	assert.Equal(t, subjectAttribution{}, at)

	// A plaintext untagged event the tagger derives nothing for.
	at = tagged.attributeSubjects(ctx, nil, StoredEvent{Type: "plainNote", Data: []byte(`{"text":"x"}`)})
	assert.Equal(t, subjectAttribution{}, at)
	assert.False(t, at.attributed())

	// Undecryptable (key revoked, handler swallows): never handed to the tagger.
	require.NoError(t, provider.RevokeKey("k"))
	taggerCalls = 0
	at = tagged.attributeSubjects(ctx, spy, encrypted)
	assert.Equal(t, subjectAttribution{undecryptable: true}, at)
	assert.Equal(t, 0, taggerCalls)

	// Encrypted event in a store with no encryption configured: undecryptable too.
	plain := New(tagged.Adapter(), WithSubjectTagger(userIDTagger))
	at = plain.attributeSubjects(ctx, nil, encrypted)
	assert.Equal(t, subjectAttribution{undecryptable: true}, at)

	// A tagger returning empty ids or duplicates yields a clean, fresh slice.
	noisy := func(string, []byte, Metadata) []string { return []string{"", "a", "a", "b"} }
	at = tagged.attributeSubjects(ctx, noisy, StoredEvent{Type: "plainNote", Data: []byte(`{}`)})
	assert.Equal(t, []string{"a", "b"}, at.subjects)
	assert.True(t, at.derived)
}

// streamSharedWithOthers follows the same rule.
func TestEventStore_StreamSharedWithOthers(t *testing.T) {
	ctx := context.Background()
	tagged, _, _ := derivedStores(t, "k")
	mine := StoredEvent{Metadata: setSubjectTags(Metadata{}, []string{"u1"})}
	derivedOther := StoredEvent{Metadata: Metadata{UserID: "u2"}}
	nobody := StoredEvent{}
	assert.False(t, tagged.streamSharedWithOthers(ctx, nil, []StoredEvent{mine, nobody}, "u1"), "unattributable events do not share")
	assert.True(t, tagged.streamSharedWithOthers(ctx, nil, []StoredEvent{mine, derivedOther}, "u1"))
	assert.False(t, tagged.streamSharedWithOthers(ctx, func(string, []byte, Metadata) []string { return nil }, []StoredEvent{mine, derivedOther}, "u1"),
		"an explicit tagger overrides the store's")
}
