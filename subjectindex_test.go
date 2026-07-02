package mink

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go-mink.dev/adapters/memory"
	"go-mink.dev/encryption/local"
)

func TestMemorySubjectIndex(t *testing.T) {
	ctx := context.Background()
	idx := NewMemorySubjectIndex()
	require.NoError(t, idx.IndexSubjects(ctx, "s1", []string{"u1", "u2"}))
	require.NoError(t, idx.IndexSubjects(ctx, "s2", []string{"u1"}))
	require.NoError(t, idx.IndexSubjects(ctx, "s1", []string{"u1"})) // idempotent

	got, err := idx.StreamsBySubject(ctx, "u1")
	require.NoError(t, err)
	assert.Equal(t, []string{"s1", "s2"}, got)

	got, err = idx.StreamsBySubject(ctx, "u2")
	require.NoError(t, err)
	assert.Equal(t, []string{"s1"}, got)

	got, err = idx.StreamsBySubject(ctx, "nobody")
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestSubjectResolver_IndexAuthorityContract(t *testing.T) {
	ctx := context.Background()
	store, _ := newSubjectTestStore(t, "k")
	appendUser(t, ctx, store, "User-u1", "u1")
	// An untagged legacy event exists → a full scan cannot prove completeness.
	require.NoError(t, store.Append(ctx, "Legacy-x", []interface{}{eraseUserCreated{Email: "x@example.com"}}))

	// Scan-based resolution is (correctly) Partial.
	scanFP, err := NewSubjectResolver(store).Resolve(ctx, "u1")
	require.NoError(t, err)
	assert.True(t, scanFP.Partial)

	idx := NewMemorySubjectIndex()
	require.NoError(t, idx.IndexSubjects(ctx, "User-u1", []string{"u1"}))

	// An index WITHOUT an authority assertion cannot claim completeness — a best-effort
	// append-time write may have dropped a stream, so the footprint is honestly Partial.
	nonAuth, err := NewSubjectResolver(store, WithResolverIndex(idx)).Resolve(ctx, "u1")
	require.NoError(t, err)
	assert.True(t, nonAuth.Partial, "a non-authoritative index must not claim a complete footprint")
	assert.Equal(t, []string{"User-u1"}, nonAuth.Streams)

	// Asserting authority (e.g. after a clean backfill) makes it complete.
	auth, err := NewSubjectResolver(store, WithResolverIndex(idx), WithAuthoritativeIndex()).Resolve(ctx, "u1")
	require.NoError(t, err)
	assert.False(t, auth.Partial, "an authoritative index resolves historical subjects completely")
	assert.Equal(t, []string{"User-u1"}, auth.Streams)
}

func TestBackfillSubjectIndex(t *testing.T) {
	ctx := context.Background()
	store, _ := newSubjectTestStore(t, "k") // tagged, but NO index writer
	appendUser(t, ctx, store, "User-u1", "u1")
	appendUser(t, ctx, store, "Order-o1", "u1")
	appendUser(t, ctx, store, "User-u2", "u2")

	idx := NewMemorySubjectIndex()
	n, err := BackfillSubjectIndex(ctx, store, userIDTagger, idx, 100)
	require.NoError(t, err)
	assert.Equal(t, 3, n)

	streams, err := idx.StreamsBySubject(ctx, "u1")
	require.NoError(t, err)
	assert.Equal(t, []string{"Order-o1", "User-u1"}, streams)

	// After a clean backfill the index is authoritative and resolves the same footprint
	// the scan would.
	fp, err := NewSubjectResolver(store, WithResolverIndex(idx), WithAuthoritativeIndex()).Resolve(ctx, "u1")
	require.NoError(t, err)
	assert.Equal(t, []string{"Order-o1", "User-u1"}, fp.Streams)
	assert.Equal(t, 2, fp.EventCount)
	assert.False(t, fp.Partial)
}

func TestBackfillSubjectIndex_RequiresTaggerAndWriter(t *testing.T) {
	store, _ := newSubjectTestStore(t, "k")
	_, err := BackfillSubjectIndex(context.Background(), store, nil, NewMemorySubjectIndex(), 0)
	assert.Error(t, err)
	_, err = BackfillSubjectIndex(context.Background(), store, userIDTagger, nil, 0)
	assert.Error(t, err)
}

func TestBackfillSubjectIndex_ScanNotSupported(t *testing.T) {
	// An adapter without SubscriptionAdapter can't be scanned; backfill reports the
	// missing subscription capability (not the export sentinel with its irrelevant
	// "provide explicit stream IDs" guidance).
	store := New(&minimalExportAdapter{})
	_, err := BackfillSubjectIndex(context.Background(), store, userIDTagger, NewMemorySubjectIndex(), 0)
	assert.ErrorIs(t, err, ErrSubscriptionNotSupported)
}

func TestStore_AppendWritesSubjectIndex(t *testing.T) {
	ctx := context.Background()
	idx := NewMemorySubjectIndex()

	provider, err := local.New(local.WithKey("k", make([]byte, 32)))
	require.NoError(t, err)
	cfg := NewFieldEncryptionConfig(
		WithEncryptionProvider(provider),
		WithDefaultKeyID("k"),
		WithEncryptedFields("eraseUserCreated", "email"),
		WithDecryptionErrorHandler(func(error, string, Metadata) error { return nil }),
	)
	store := New(memory.NewAdapter(),
		WithFieldEncryption(cfg),
		WithSubjectTagger(userIDTagger),
		WithSubjectIndexWriter(idx),
	)
	store.RegisterEvents(eraseUserCreated{})

	appendUser(t, ctx, store, "User-u1", "u1")
	appendUser(t, ctx, store, "Order-o1", "u1")

	streams, err := idx.StreamsBySubject(ctx, "u1")
	require.NoError(t, err)
	assert.Equal(t, []string{"Order-o1", "User-u1"}, streams, "append-time indexing keeps the index complete")
}
