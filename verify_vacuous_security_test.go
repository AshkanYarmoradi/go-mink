package mink

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Verify() must attest SOMETHING: a subject with no tagged events yields a Vacuous,
// unverified report instead of a positive attestation built on zero evidence.
func TestVerify_NoEventsIsVacuousNotVerified(t *testing.T) {
	ctx := context.Background()
	store, _ := newSubjectTestStore(t, "k")
	appendUser(t, ctx, store, "User-u1", "u1")

	rep, err := NewDataEraser(store, WithEraseSubjectResolver(NewSubjectResolver(store))).Verify(ctx, "ghost")
	require.NoError(t, err)
	assert.True(t, rep.Vacuous)
	assert.False(t, rep.Verified, "nothing was checked, so nothing is attested")
	assert.Equal(t, 0, rep.EventsChecked)
	assert.Empty(t, rep.ResidualEncrypted)
	assert.True(t, notesContain(rep.Notes, "nothing was checked"), "%v", rep.Notes)

	// A real subject is not vacuous.
	rep, err = NewDataEraser(store, WithEraseSubjectResolver(NewSubjectResolver(store))).Verify(ctx, "u1")
	require.NoError(t, err)
	assert.False(t, rep.Vacuous)
	assert.Equal(t, 1, rep.EventsChecked)
}

// After WithSubjectIndexPurge an index-backed resolver resolves an empty footprint: a
// later Verify is Vacuous, never a false "Verified".
func TestVerify_AfterIndexPurgeIsVacuous(t *testing.T) {
	ctx := context.Background()
	store, _ := newSubjectTestStore(t, "k")
	appendUser(t, ctx, store, "User-u1", "u1")
	idx := NewMemorySubjectIndex()
	require.NoError(t, idx.IndexSubjects(ctx, "User-u1", []string{"u1"}))

	var cert ErasureCertificate
	eraser := NewDataEraser(store,
		WithEraseSubjectResolver(NewSubjectResolver(store, WithResolverIndex(idx), WithAuthoritativeIndex())),
		WithSubjectIndexPurge(idx),
		captureCert(&cert),
	)
	res, err := eraser.Erase(ctx, ErasureRequest{SubjectID: "u1"})
	require.NoError(t, err)
	require.True(t, res.SubjectIndexPurged)
	require.True(t, cert.Verified)

	rep, err := eraser.Verify(ctx, "u1")
	require.NoError(t, err)
	assert.True(t, rep.Vacuous)
	assert.False(t, rep.Verified, "an empty index-backed footprint is not evidence of erasure")
	assert.True(t, notesContain(rep.Notes, "purged subject index"), "%v", rep.Notes)

	// A scan-backed resolver still sees the (shredded) events and verifies for real.
	rep, err = NewDataEraser(store, WithEraseSubjectResolver(NewSubjectResolver(store))).Verify(ctx, "u1")
	require.NoError(t, err)
	assert.False(t, rep.Vacuous)
	assert.True(t, rep.Verified)
	assert.Equal(t, 1, rep.RedactedEvents)
}

// Without a certificate sink the index purge is gated on the same verification run
// internally: a vacuous (KeyIDs-only) erasure keeps the index, a resolved and verified
// one purges it, and one whose verification fails keeps it.
func TestDataEraser_WithSubjectIndexPurge_NoSinkIsVerifiedInternally(t *testing.T) {
	ctx := context.Background()

	t.Run("KeyIDs-only erasure checks no event and keeps the index", func(t *testing.T) {
		store, _ := newSubjectTestStore(t, "k")
		appendUser(t, ctx, store, "User-u1", "u1")
		idx := NewMemorySubjectIndex()
		require.NoError(t, idx.IndexSubjects(ctx, "User-u1", []string{"u1"}))

		res, err := NewDataEraser(store, WithSubjectIndexPurge(idx)).
			Erase(ctx, ErasureRequest{SubjectID: "u1", KeyIDs: []string{"k"}})
		require.NoError(t, err)
		assert.Equal(t, []string{"k"}, res.KeysRevoked)
		assert.False(t, res.Failed(), "the revocation itself succeeded: %v", res.Errors)
		assert.False(t, res.SubjectIndexPurged, "nothing was attested, so the index must stay")
		assert.True(t, notesContain(res.Notes, "subject index retained"), "%v", res.Notes)
		got, _ := idx.StreamsBySubject(ctx, "u1")
		assert.Equal(t, []string{"User-u1"}, got)
	})

	t.Run("explicit-Streams erasure over an untagged stream keeps the index", func(t *testing.T) {
		store, _ := newEraseTestStore(t, "k") // no tagger
		require.NoError(t, store.Append(ctx, "User-u1", []interface{}{eraseUserCreated{UserID: "u1", Email: "a@b.c"}}))
		idx := NewMemorySubjectIndex()
		require.NoError(t, idx.IndexSubjects(ctx, "User-u1", []string{"u1"}))

		res, err := NewDataEraser(store, WithSubjectIndexPurge(idx)).
			Erase(ctx, ErasureRequest{SubjectID: "u1", Streams: []string{"User-u1"}})
		require.NoError(t, err)
		assert.Equal(t, []string{"k"}, res.KeysRevoked)
		assert.False(t, res.SubjectIndexPurged)
		got, _ := idx.StreamsBySubject(ctx, "u1")
		assert.Equal(t, []string{"User-u1"}, got)
	})

	t.Run("resolved and verified erasure purges without a sink", func(t *testing.T) {
		store, _ := newSubjectTestStore(t, "k")
		appendUser(t, ctx, store, "User-u1", "u1")
		appendUser(t, ctx, store, "User-u2", "u2")
		idx := NewMemorySubjectIndex()
		require.NoError(t, idx.IndexSubjects(ctx, "User-u1", []string{"u1"}))
		require.NoError(t, idx.IndexSubjects(ctx, "User-u2", []string{"u2"}))

		res, err := NewDataEraser(store,
			WithEraseSubjectResolver(NewSubjectResolver(store, WithResolverIndex(idx), WithAuthoritativeIndex())),
			WithSubjectIndexPurge(idx),
		).Erase(ctx, ErasureRequest{SubjectID: "u1"})
		require.NoError(t, err)
		assert.False(t, res.Failed(), "%v", res.Errors)
		assert.True(t, res.SubjectIndexPurged, "the internal verification attested the erasure")
		for _, n := range res.Notes {
			assert.False(t, strings.Contains(n, "subject index retained"), "%v", res.Notes)
		}
		got, _ := idx.StreamsBySubject(ctx, "u1")
		assert.Empty(t, got)
		got, _ = idx.StreamsBySubject(ctx, "u2")
		assert.Equal(t, []string{"User-u2"}, got, "other subjects' entries are untouched")
	})

	t.Run("soft-revoked key fails the internal verification and keeps the index", func(t *testing.T) {
		store := newSoftRevokedStore(t)
		appendUser(t, ctx, store, "User-u1", "u1")
		idx := NewMemorySubjectIndex()
		require.NoError(t, idx.IndexSubjects(ctx, "User-u1", []string{"u1"}))

		res, err := NewDataEraser(store,
			WithEraseSubjectResolver(NewSubjectResolver(store, WithResolverIndex(idx), WithAuthoritativeIndex())),
			WithSubjectIndexPurge(idx),
		).Erase(ctx, ErasureRequest{SubjectID: "u1"})
		require.NoError(t, err)
		assert.Equal(t, []string{"k"}, res.KeysRevoked)
		assert.False(t, res.Failed(), "a soft revoke is not an erasure error: %v", res.Errors)
		assert.False(t, res.SubjectIndexPurged, "the PII is still recoverable, so the index must stay")
		assert.True(t, notesContain(res.Notes, "subject index retained"), "%v", res.Notes)
		got, _ := idx.StreamsBySubject(ctx, "u1")
		assert.Equal(t, []string{"User-u1"}, got)
	})

	t.Run("no purger and no sink runs no verification at all", func(t *testing.T) {
		store, _ := newSubjectTestStore(t, "k")
		appendUser(t, ctx, store, "User-u1", "u1")
		res, err := NewDataEraser(store, WithEraseSubjectResolver(NewSubjectResolver(store))).
			Erase(ctx, ErasureRequest{SubjectID: "u1"})
		require.NoError(t, err)
		assert.Empty(t, res.Notes)
		assert.Empty(t, res.Errors)
		assert.False(t, res.SubjectIndexPurged)
	})
}

// verifyStreams uses the same ciphertext predicate as discovery: a tagged event with a
// bare key id is residual cleartext, not an encrypted event under a live key.
func TestVerify_BareKeyIDIsResidualCleartext(t *testing.T) {
	ctx := context.Background()
	store, _ := newSubjectTestStore(t, "k")
	appendRawEnvelope(t, ctx, store.Adapter(), "User-bare", map[string]string{encryptionKeyIDKey: "k", subjectTagsKey: `["u1"]`})

	rep, err := NewDataEraser(store, WithEraseSubjectResolver(NewSubjectResolver(store))).Verify(ctx, "u1")
	require.NoError(t, err)
	assert.Equal(t, 1, rep.EventsChecked)
	assert.Len(t, rep.ResidualCleartext, 1)
	assert.Empty(t, rep.ResidualEncrypted)
	assert.False(t, rep.Verified)
	assert.False(t, rep.Vacuous)
	assert.True(t, notesContain(rep.Notes, "1 subject event(s) are not field-encrypted"), "%v", rep.Notes)
}
