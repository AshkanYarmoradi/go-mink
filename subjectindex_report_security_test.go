package mink

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go-mink.dev/adapters/memory"
	"go-mink.dev/encryption/local"
)

// BackfillSubjectIndexWithReport counts what an index-backed resolve will never see:
// the events neither the tagger nor existing tags could attribute to a subject.
func TestBackfillSubjectIndexWithReport_Counts(t *testing.T) {
	ctx := context.Background()
	provider, err := local.New(local.WithKey("k", make([]byte, 32)))
	require.NoError(t, err)
	cfg := NewFieldEncryptionConfig(
		WithEncryptionProvider(provider),
		WithDefaultKeyID("k"),
		WithEncryptedFields("eraseUserCreated", "email"),
		WithDecryptionErrorHandler(func(error, string, Metadata) error { return nil }), // crypto-shred: leave ciphertext
	)
	store := New(memory.NewAdapter(), WithFieldEncryption(cfg), WithSubjectTagger(userIDTagger))
	store.RegisterEvents(eraseUserCreated{}, plainNote{})

	appendUser(t, ctx, store, "User-u1", "u1")                                                              // tagged at append, encrypted
	require.NoError(t, store.Append(ctx, "Sys-1", []interface{}{eraseUserCreated{Email: "x@example.com"}})) // encrypted, nobody's
	require.NoError(t, store.Append(ctx, "Note-n1", []interface{}{plainNote{Text: "hi"}},
		WithAppendMetadata(Metadata{UserID: "u3"}))) // plaintext, derivable from metadata
	require.NoError(t, store.Append(ctx, "Note-n2", []interface{}{plainNote{Text: "anon"}})) // plaintext, untagged

	// Before the key is shredded everything decrypts: only the two subject-less events
	// are untagged.
	idx := NewMemorySubjectIndex()
	rep, err := BackfillSubjectIndexWithReport(ctx, store, userIDTagger, idx, 2)
	require.NoError(t, err)
	assert.Equal(t, BackfillReport{Scanned: 4, Indexed: 2, Untagged: 2, Undecryptable: 0}, rep)
	assert.Equal(t, rep.Scanned, rep.Indexed+rep.Untagged)
	got, _ := idx.StreamsBySubject(ctx, "u1")
	assert.Equal(t, []string{"User-u1"}, got)
	got, _ = idx.StreamsBySubject(ctx, "u3")
	assert.Equal(t, []string{"Note-n1"}, got)

	// After the key is shredded the two encrypted events are undecryptable: User-u1 is
	// still indexed from its existing tag; Sys-1 stays untagged.
	require.NoError(t, provider.RevokeKey("k"))
	rep, err = BackfillSubjectIndexWithReport(ctx, store, userIDTagger, NewMemorySubjectIndex(), 0)
	require.NoError(t, err)
	assert.Equal(t, BackfillReport{Scanned: 4, Indexed: 2, Untagged: 2, Undecryptable: 2}, rep)

	// The thin wrapper returns the scanned count, as before.
	n, err := BackfillSubjectIndex(ctx, store, userIDTagger, NewMemorySubjectIndex(), 0)
	require.NoError(t, err)
	assert.Equal(t, 4, n)
}

// A clean backfill (Untagged == 0) is the precondition for WithAuthoritativeIndex.
func TestBackfillSubjectIndexWithReport_CleanBackfillHasNoUntagged(t *testing.T) {
	ctx := context.Background()
	store, _ := newSubjectTestStore(t, "k")
	appendUser(t, ctx, store, "User-u1", "u1")
	appendUser(t, ctx, store, "Order-o1", "u1")
	appendUser(t, ctx, store, "User-u2", "u2")

	idx := NewMemorySubjectIndex()
	rep, err := BackfillSubjectIndexWithReport(ctx, store, userIDTagger, idx, 100)
	require.NoError(t, err)
	assert.Equal(t, BackfillReport{Scanned: 3, Indexed: 3}, rep)

	fp, err := NewSubjectResolver(store, WithResolverIndex(idx), WithAuthoritativeIndex()).Resolve(ctx, "u1")
	require.NoError(t, err)
	assert.Equal(t, []string{"Order-o1", "User-u1"}, fp.Streams)
	assert.False(t, fp.Partial)
}

// Errors still come with the report of what was processed so far.
func TestBackfillSubjectIndexWithReport_ErrorPaths(t *testing.T) {
	ctx := context.Background()
	store, _ := newSubjectTestStore(t, "k")
	appendUser(t, ctx, store, "User-u1", "u1")

	rep, err := BackfillSubjectIndexWithReport(ctx, store, nil, NewMemorySubjectIndex(), 0)
	assert.Error(t, err)
	assert.Equal(t, BackfillReport{}, rep)

	rep, err = BackfillSubjectIndexWithReport(ctx, New(&minimalExportAdapter{}), userIDTagger, NewMemorySubjectIndex(), 0)
	assert.ErrorIs(t, err, ErrSubscriptionNotSupported)
	assert.Equal(t, BackfillReport{}, rep)

	rep, err = BackfillSubjectIndexWithReport(ctx, store, userIDTagger, failingIndexWriter{}, 0)
	require.Error(t, err)
	assert.Equal(t, 1, rep.Scanned, "the failing event was scanned")
	assert.Equal(t, 0, rep.Indexed, "but not indexed")

	cctx, cancel := context.WithCancel(ctx)
	cancel()
	rep, err = BackfillSubjectIndexWithReport(cctx, store, userIDTagger, NewMemorySubjectIndex(), 0)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 0, rep.Scanned)
}
