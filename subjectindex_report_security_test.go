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
// the events neither existing tags nor the tagger could attribute to a subject — and
// what it indexes under a tagger-derived subject, a resolver using the same tagger
// finds (the review scenario: a historical event with Metadata.UserID and no persisted
// tag must be in the footprint, not just in the raw index).
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
	adapter := memory.NewAdapter()
	store := New(adapter, WithFieldEncryption(cfg), WithSubjectTagger(userIDTagger))
	store.RegisterEvents(eraseUserCreated{}, plainNote{})
	// A writer from before subject tagging was enabled: same events, no tagger.
	legacy := New(adapter)
	legacy.RegisterEvents(plainNote{})

	appendUser(t, ctx, store, "User-u1", "u1")                                                              // tagged at append, encrypted
	require.NoError(t, store.Append(ctx, "Sys-1", []interface{}{eraseUserCreated{Email: "x@example.com"}})) // encrypted, nobody's
	require.NoError(t, legacy.Append(ctx, "Note-n1", []interface{}{plainNote{Text: "hi"}},
		WithAppendMetadata(Metadata{UserID: "u3"}))) // plaintext, NO persisted tag, derivable from metadata
	require.NoError(t, store.Append(ctx, "Note-n2", []interface{}{plainNote{Text: "anon"}})) // plaintext, untagged

	raw, err := store.LoadRaw(ctx, "Note-n1", 0)
	require.NoError(t, err)
	require.Empty(t, GetSubjectTags(raw[0].Metadata), "precondition: the historical event carries no persisted tag")

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

	// What the backfill indexed under the derived subject, the resolver attributes the
	// same way: the derived-only event IS in u3's footprint, so the clean report really
	// does make WithAuthoritativeIndex safe.
	fp, err := NewSubjectResolver(store, WithResolverIndex(idx), WithAuthoritativeIndex()).Resolve(ctx, "u3")
	require.NoError(t, err)
	assert.Equal(t, []string{"Note-n1"}, fp.Streams)
	assert.Equal(t, 1, fp.EventCount)
	assert.Equal(t, 1, fp.CleartextEvents)
	assert.False(t, fp.Partial)

	// After the key is shredded, the untagged encrypted event (Sys-1) is undecryptable
	// and stays untagged; User-u1 is attributed from its persisted tag without being
	// decrypted at all, so it does not count as undecryptable.
	require.NoError(t, provider.RevokeKey("k"))
	rep, err = BackfillSubjectIndexWithReport(ctx, store, userIDTagger, NewMemorySubjectIndex(), 0)
	require.NoError(t, err)
	assert.Equal(t, BackfillReport{Scanned: 4, Indexed: 2, Untagged: 2, Undecryptable: 1}, rep)
	assert.LessOrEqual(t, rep.Undecryptable, rep.Untagged)

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
