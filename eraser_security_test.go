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

// newPerSubjectKeyStore builds a tagged, field-encrypted store whose events are
// wrapped under a per-subject key ("key-<subject>"), so a shared stream holds events
// under different keys.
func newPerSubjectKeyStore(t *testing.T) (*EventStore, *local.Provider) {
	t.Helper()
	provider, err := local.New(
		local.WithKey("key-u1", make([]byte, 32)),
		local.WithKey("key-u2", make([]byte, 32)),
	)
	require.NoError(t, err)
	cfg := NewFieldEncryptionConfig(
		WithEncryptionProvider(provider),
		WithDefaultKeyID("key-u1"),
		WithEncryptedFields("eraseUserCreated", "email"),
		WithSubjectKeyResolver(func(subjectID string) string { return "key-" + subjectID }),
		WithDecryptionErrorHandler(func(error, string, Metadata) error { return nil }),
	)
	store := New(memory.NewAdapter(), WithFieldEncryption(cfg), WithSubjectTagger(userIDTagger))
	store.RegisterEvents(eraseUserCreated{}, ErasureMarker{})
	return store, provider
}

func TestErase_StreamsScope_SkipsCoTenantsOnTaggedStream(t *testing.T) {
	ctx := context.Background()
	store, provider := newPerSubjectKeyStore(t)
	appendUser(t, ctx, store, "Shared-s1", "u1")
	appendUser(t, ctx, store, "Shared-s1", "u2") // a co-tenant on the same stream

	var cert ErasureCertificate
	res, err := NewDataEraser(store, captureCert(&cert)).
		Erase(ctx, ErasureRequest{SubjectID: "u1", Streams: []string{"Shared-s1"}})
	require.NoError(t, err)

	assert.Equal(t, []string{"key-u1"}, res.KeysRevoked, "only the target subject's key is collected from a tagged stream")
	assert.Equal(t, 1, res.EventsScanned, "the co-tenant's event is not matched")
	revoked, _ := provider.IsRevoked("key-u2")
	assert.False(t, revoked, "the co-tenant's key must survive a stream-scoped erasure")
	assert.Empty(t, res.Notes, "no legacy-scope note for a tagged stream")

	// The certificate attests the subject's own event in the listed stream.
	assert.Equal(t, 1, cert.EventsChecked)
	assert.True(t, cert.Verified)
}

func TestErase_StreamsScope_UntaggedStreamKeepsLegacyAllKeysWithNote(t *testing.T) {
	ctx := context.Background()
	store, provider := newEraseTestStore(t, "k") // no tagger: nothing is tagged
	require.NoError(t, store.Append(ctx, "Legacy-l1", []interface{}{
		eraseUserCreated{UserID: "u1", Email: "a@b.c"},
		eraseUserCreated{UserID: "u9", Email: "x@y.z"},
	}))

	var cert ErasureCertificate
	res, err := NewDataEraser(store, captureCert(&cert)).
		Erase(ctx, ErasureRequest{SubjectID: "u1", Streams: []string{"Legacy-l1"}})
	require.NoError(t, err)

	assert.Equal(t, []string{"k"}, res.KeysRevoked, "legacy behavior: every key in an untagged stream is collected")
	revoked, _ := provider.IsRevoked("k")
	assert.True(t, revoked)
	require.Len(t, res.Notes, 1)
	assert.Contains(t, res.Notes[0], "1 of 1 listed stream(s) carry no subject tags")
	assert.True(t, notesContain(cert.Notes, "1 of 1 listed stream(s) carry no subject tags"), "the note reaches the certificate: %v", cert.Notes)
	assert.False(t, cert.Verified, "no subject-tagged event could be attested")
}

func TestErase_StreamsScope_MixedTaggedAndLegacyStreams(t *testing.T) {
	ctx := context.Background()
	store, _ := newPerSubjectKeyStore(t)
	appendUser(t, ctx, store, "Shared-s1", "u1")
	appendUser(t, ctx, store, "Shared-s1", "u2")
	// An untagged legacy event (no UserID → the tagger derives nothing).
	require.NoError(t, store.Append(ctx, "Legacy-l1", []interface{}{eraseUserCreated{UserID: "old", Email: "o@l.d"}}))

	res, err := NewDataEraser(store).
		Erase(ctx, ErasureRequest{SubjectID: "u1", Streams: []string{"Shared-s1", "Legacy-l1"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"key-u1"}, res.KeysRevoked, "the legacy stream's default key happens to be key-u1 too; key-u2 is never collected")
	assert.Equal(t, 2, res.EventsScanned, "u1's tagged event + the untagged legacy event")
	require.Len(t, res.Notes, 1)
	assert.Contains(t, res.Notes[0], "1 of 2 listed stream(s) carry no subject tags")
}

func TestSharedKeyError_MessageOmitsOtherSubjects(t *testing.T) {
	ske := &SharedKeyError{
		SubjectID:         "u1",
		SharedKeys:        []string{"tenant-A", "tenant-B"},
		OtherSubjects:     []string{"alice@example.com", "bob@example.com"},
		OtherSubjectCount: 5,
	}
	msg := ske.Error()
	assert.Contains(t, msg, `"u1"`)
	assert.Contains(t, msg, "2 key(s) shared with 5 other subject(s)", "counts only: the total, not the sample size")
	assert.NotContains(t, msg, "tenant-A", "key ids can embed co-subject ids under a per-subject key resolver")
	assert.NotContains(t, msg, "tenant-B")
	assert.NotContains(t, msg, "alice")
	assert.NotContains(t, msg, "bob")
	assert.Equal(t, []string{"tenant-A", "tenant-B"}, ske.SharedKeys, "the key ids stay available for programmatic use")

	// Under a per-subject key resolver the shared key id IS a co-subject's identifier.
	perSubject := &SharedKeyError{SubjectID: "u2", SharedKeys: []string{"key-u1"}, OtherSubjects: []string{"u1"}, OtherSubjectCount: 1}
	assert.NotContains(t, perSubject.Error(), "u1", "a co-subject's id must not leak through the key id")
	assert.Contains(t, perSubject.Error(), "1 key(s) shared with 1 other subject(s)")

	// Hand-built errors without the total fall back to the sample size.
	assert.Contains(t, (&SharedKeyError{OtherSubjects: []string{"x"}}).Error(), "1 other subject(s)")
	assert.Contains(t, (&SharedKeyError{SharedKeys: []string{"k"}}).Error(), "0 other subject(s) and/or untagged events")
}

func TestDataEraser_SharedKeyGuard_ErrorIsPIIFree(t *testing.T) {
	ctx := context.Background()
	store, _ := newSubjectTestStore(t, "shared")
	appendUser(t, ctx, store, "User-u1", "u1")
	appendUser(t, ctx, store, "User-u2", "u2")
	appendUser(t, ctx, store, "User-u3", "u3")

	_, err := NewDataEraser(store, WithEraseSubjectResolver(NewSubjectResolver(store)), WithSharedKeyGuard()).
		Erase(ctx, ErasureRequest{SubjectID: "u1"})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "u2")
	assert.NotContains(t, err.Error(), "u3")
	assert.Contains(t, err.Error(), "2 other subject(s)")

	var ske *SharedKeyError
	require.True(t, errors.As(err, &ske))
	assert.Equal(t, 2, ske.OtherSubjectCount)
	assert.Equal(t, []string{"u2", "u3"}, ske.OtherSubjects, "the sample stays available for programmatic use")
}

// forgedMarkerLookalike is NOT an ErasureMarker but serializes to a payload that
// decodes as one, and is appended with the marker's metadata key.
type forgedMarkerLookalike struct {
	SubjectID string `json:"subjectId"`
}

func TestDataEraser_MarkerExists_IgnoresForgedNonMarkerEvents(t *testing.T) {
	ctx := context.Background()
	assert.Equal(t, "ErasureMarker", erasureMarkerEventType, "the marker type is derived exactly as Append derives it")

	store, _ := newSubjectTestStore(t, "k")
	appendUser(t, ctx, store, "User-u1", "u1")
	// A writer plants a lookalike in the marker stream: wrong type, but it carries the
	// marker's metadata key AND a JSON payload that decodes as an ErasureMarker.
	require.NoError(t, store.Append(ctx, "erasure-log",
		[]interface{}{forgedMarkerLookalike{SubjectID: "u1"}},
		WithAppendMetadata(Metadata{Custom: map[string]string{erasureMarkerSubjectKey: "u1"}})))

	eraser := NewDataEraser(store,
		WithEraseSubjectResolver(NewSubjectResolver(store)),
		WithErasureMarker("erasure-log"),
	)
	assert.False(t, eraser.markerExists(ctx, "u1"), "a forged non-marker event must not count as the subject's marker")

	res, err := eraser.Erase(ctx, ErasureRequest{SubjectID: "u1"})
	require.NoError(t, err)
	assert.True(t, res.MarkerWritten, "the genuine marker is still appended")

	raw, err := store.LoadRaw(ctx, "erasure-log", 0)
	require.NoError(t, err)
	require.Len(t, raw, 2)
	assert.Equal(t, "forgedMarkerLookalike", raw[0].Type)
	assert.Equal(t, "ErasureMarker", raw[1].Type)
}

func TestDataEraser_MarkerExists_TrustsGenuineMarkersOnBothPaths(t *testing.T) {
	ctx := context.Background()
	store, _ := newSubjectTestStore(t, "k")
	// A modern marker (metadata key) and a pre-tag marker (JSON fallback only).
	require.NoError(t, store.Append(ctx, "erasure-log",
		[]interface{}{ErasureMarker{SubjectID: "tagged"}},
		WithAppendMetadata(Metadata{Custom: map[string]string{erasureMarkerSubjectKey: "tagged"}})))
	require.NoError(t, store.Append(ctx, "erasure-log", []interface{}{ErasureMarker{SubjectID: "legacy"}}))

	eraser := NewDataEraser(store, WithErasureMarker("erasure-log"))
	assert.True(t, eraser.markerExists(ctx, "tagged"))
	assert.True(t, eraser.markerExists(ctx, "legacy"))
	assert.False(t, eraser.markerExists(ctx, "nobody"))
}

// failingIndexPurger always fails DeleteSubject.
type failingIndexPurger struct{}

func (failingIndexPurger) DeleteSubject(context.Context, string) error {
	return errors.New("index down")
}

func TestDataEraser_WithSubjectIndexPurge_PurgesAfterVerifiedErasure(t *testing.T) {
	ctx := context.Background()
	store, _ := newSubjectTestStore(t, "k")
	appendUser(t, ctx, store, "User-u1", "u1")
	appendUser(t, ctx, store, "User-u2", "u2")
	idx := NewMemorySubjectIndex()
	require.NoError(t, idx.IndexSubjects(ctx, "User-u1", []string{"u1"}))
	require.NoError(t, idx.IndexSubjects(ctx, "User-u2", []string{"u2"}))

	var cert ErasureCertificate
	eraser := NewDataEraser(store,
		WithEraseSubjectResolver(NewSubjectResolver(store, WithResolverIndex(idx), WithAuthoritativeIndex())),
		WithSubjectIndexPurge(idx),
		captureCert(&cert),
	)
	res, err := eraser.Erase(ctx, ErasureRequest{SubjectID: "u1"})
	require.NoError(t, err)
	assert.False(t, res.Failed(), "%v", res.Errors)
	assert.True(t, cert.Verified)
	assert.True(t, res.SubjectIndexPurged)

	got, err := idx.StreamsBySubject(ctx, "u1")
	require.NoError(t, err)
	assert.Empty(t, got, "the erased subject's index entries are gone")
	got, err = idx.StreamsBySubject(ctx, "u2")
	require.NoError(t, err)
	assert.Equal(t, []string{"User-u2"}, got, "other subjects' entries are untouched")

	// Documented trade-off: an index-backed re-run now resolves an empty footprint.
	res2, err := eraser.Erase(ctx, ErasureRequest{SubjectID: "u1"})
	require.NoError(t, err)
	assert.Empty(t, res2.Streams)
	assert.Empty(t, res2.KeysRevoked)
	assert.True(t, notesContain(cert.Notes, "nothing to erase"), "%v", cert.Notes)
}

func TestDataEraser_WithSubjectIndexPurge_RetainedUnlessFullyVerified(t *testing.T) {
	ctx := context.Background()

	t.Run("Failed() erasure keeps the index", func(t *testing.T) {
		store, _ := newSubjectTestStore(t, "k")
		appendUser(t, ctx, store, "User-u1", "u1")
		idx := NewMemorySubjectIndex()
		require.NoError(t, idx.IndexSubjects(ctx, "User-u1", []string{"u1"}))

		res, err := NewDataEraser(store,
			WithEraseSubjectResolver(NewSubjectResolver(store, WithResolverIndex(idx), WithAuthoritativeIndex())),
			WithSubjectStore(&fakeSubjectStore{name: "audit", err: errors.New("audit down")}),
			WithSubjectIndexPurge(idx),
		).Erase(ctx, ErasureRequest{SubjectID: "u1"})
		require.NoError(t, err)
		assert.True(t, res.Failed())
		assert.False(t, res.SubjectIndexPurged)
		assert.True(t, notesContain(res.Notes, "subject index retained"), "%v", res.Notes)
		got, _ := idx.StreamsBySubject(ctx, "u1")
		assert.Equal(t, []string{"User-u1"}, got, "a re-run can still resolve the subject through the index")
	})

	t.Run("unverified certificate keeps the index even when nothing failed", func(t *testing.T) {
		store, _ := newSubjectTestStore(t, "k")
		appendUser(t, ctx, store, "User-u1", "u1")
		idx := NewMemorySubjectIndex()
		require.NoError(t, idx.IndexSubjects(ctx, "User-u1", []string{"u1"}))

		var cert ErasureCertificate
		res, err := NewDataEraser(store, WithSubjectIndexPurge(idx), captureCert(&cert)).
			Erase(ctx, ErasureRequest{SubjectID: "u1", KeyIDs: []string{"k"}}) // vacuous certificate
		require.NoError(t, err)
		assert.False(t, res.Failed())
		assert.False(t, cert.Verified)
		assert.False(t, res.SubjectIndexPurged)
		got, _ := idx.StreamsBySubject(ctx, "u1")
		assert.Equal(t, []string{"User-u1"}, got)
	})

	t.Run("purge failure is non-fatal and reported", func(t *testing.T) {
		store, _ := newSubjectTestStore(t, "k")
		appendUser(t, ctx, store, "User-u1", "u1")

		res, err := NewDataEraser(store,
			WithEraseSubjectResolver(NewSubjectResolver(store)),
			WithSubjectIndexPurge(failingIndexPurger{}),
		).Erase(ctx, ErasureRequest{SubjectID: "u1"})
		require.NoError(t, err)
		assert.False(t, res.SubjectIndexPurged)
		require.NotEmpty(t, res.Errors)
		assert.Contains(t, res.Errors[len(res.Errors)-1].Error(), "purge subject index")
		assert.True(t, res.Failed())
	})

	t.Run("nil purger is ignored", func(t *testing.T) {
		e := NewDataEraser(nil, WithSubjectIndexPurge(nil))
		assert.Nil(t, e.indexPurger)
	})
}

// revokeFailsProvider encrypts normally but cannot revoke (e.g. a Vault Transit key
// without deletion_allowed).
type revokeFailsProvider struct{ *local.Provider }

func (revokeFailsProvider) RevokeKey(string) error {
	return errors.New("vault: deletion_allowed=false")
}

func TestDataEraser_ReconcileLateKeyRevokeFailure_IsReportedInKeysFailed(t *testing.T) {
	ctx := context.Background()
	inner, err := local.New(local.WithKey("k", make([]byte, 32)))
	require.NoError(t, err)
	cfg := NewFieldEncryptionConfig(
		WithEncryptionProvider(revokeFailsProvider{inner}),
		WithDefaultKeyID("k"),
		WithEncryptedFields("eraseUserCreated", "email"),
	)
	store := New(memory.NewAdapter(), WithFieldEncryption(cfg), WithSubjectTagger(userIDTagger))
	store.RegisterEvents(eraseUserCreated{})
	appendUser(t, ctx, store, "User-u1", "u1")

	e := NewDataEraser(store, WithEraseSubjectResolver(NewSubjectResolver(store)))
	result := &ErasureResult{SubjectID: "u1"}
	// An empty discovery set makes "k" a late-arriving key; its revoke fails.
	e.reconcileAfterRevoke(ctx, "u1", cfg, map[string]struct{}{}, result)

	assert.True(t, result.Partial)
	assert.Equal(t, []string{"k"}, result.KeysFailed, "a late key that fails to revoke is reported like an initial one")
	assert.Empty(t, result.KeysRevoked)
	assert.NotEmpty(t, result.Errors)
}
