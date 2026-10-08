package mink

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The scan path flags a footprint stream as shared even when the co-tenant's event
// precedes the subject's first event in it.
func TestSubjectResolver_SharedStreams_ScanPath(t *testing.T) {
	ctx := context.Background()
	store, _ := newSubjectTestStore(t, "k")
	appendUser(t, ctx, store, "Shared-s1", "u2") // co-tenant first
	appendUser(t, ctx, store, "Shared-s1", "u1")
	appendUser(t, ctx, store, "User-u1", "u1")
	appendUser(t, ctx, store, "User-u2", "u2") // not in u1's footprint at all

	fp, err := NewSubjectResolver(store).Resolve(ctx, "u1")
	require.NoError(t, err)
	assert.Equal(t, []string{"Shared-s1", "User-u1"}, fp.Streams)
	assert.Equal(t, []string{"Shared-s1"}, fp.SharedStreams)
	assert.Equal(t, []string{"User-u1"}, fp.ExclusiveStreams())
	assert.Equal(t, 2, fp.EventCount, "shared streams change nothing about the subject's own events")
	assert.False(t, fp.Partial)

	// From the co-tenant's point of view the same stream is shared too.
	fp2, err := NewSubjectResolver(store).Resolve(ctx, "u2")
	require.NoError(t, err)
	assert.Equal(t, []string{"Shared-s1"}, fp2.SharedStreams)
	assert.Equal(t, []string{"User-u2"}, fp2.ExclusiveStreams())
}

// The index path observes sharing while loading each indexed stream.
func TestSubjectResolver_SharedStreams_IndexPath(t *testing.T) {
	ctx := context.Background()
	store, _ := newSubjectTestStore(t, "k")
	appendUser(t, ctx, store, "Shared-s1", "u2")
	appendUser(t, ctx, store, "Shared-s1", "u1")
	appendUser(t, ctx, store, "User-u1", "u1")
	idx := NewMemorySubjectIndex()
	require.NoError(t, idx.IndexSubjects(ctx, "Shared-s1", []string{"u1", "u2"}))
	require.NoError(t, idx.IndexSubjects(ctx, "User-u1", []string{"u1"}))
	require.NoError(t, idx.IndexSubjects(ctx, "Stale-x", []string{"u1"})) // indexed, but holds no u1 event

	fp, err := NewSubjectResolver(store, WithResolverIndex(idx), WithAuthoritativeIndex()).Resolve(ctx, "u1")
	require.NoError(t, err)
	assert.Equal(t, []string{"Shared-s1", "User-u1"}, fp.Streams)
	assert.Equal(t, []string{"Shared-s1"}, fp.SharedStreams)
	assert.Equal(t, []string{"User-u1"}, fp.ExclusiveStreams())
	assert.False(t, fp.Partial)
}

// Untagged events never make a stream shared — they make the footprint Partial.
func TestSubjectResolver_UntaggedEventsAreNotSharing(t *testing.T) {
	ctx := context.Background()
	store, _ := newSubjectTestStore(t, "k")
	require.NoError(t, store.Append(ctx, "User-u1", []interface{}{eraseUserCreated{Email: "legacy@example.com"}})) // untagged
	appendUser(t, ctx, store, "User-u1", "u1")

	fp, err := NewSubjectResolver(store).Resolve(ctx, "u1")
	require.NoError(t, err)
	assert.Equal(t, []string{"User-u1"}, fp.Streams)
	assert.Empty(t, fp.SharedStreams)
	assert.True(t, fp.Partial)
}

// A tagged event with a bare "$encryption_key_id" (no envelope) names no key worth
// revoking: it is cleartext to the resolver, exactly as to the eraser and retention.
func TestSubjectResolver_BareKeyIDIsCleartext(t *testing.T) {
	ctx := context.Background()
	store, _ := newSubjectTestStore(t, "k")
	appendRawEnvelope(t, ctx, store.Adapter(), "User-bare", map[string]string{encryptionKeyIDKey: "k", subjectTagsKey: `["u1"]`})
	appendRawEnvelope(t, ctx, store.Adapter(), "User-nodek", map[string]string{encryptedFieldsKey: `["email"]`, encryptionKeyIDKey: "k", subjectTagsKey: `["u1"]`})
	appendUser(t, ctx, store, "User-u1", "u1") // a complete envelope under "k"

	for name, resolver := range map[string]*SubjectResolver{
		"scan": NewSubjectResolver(store),
		"index": func() *SubjectResolver {
			idx := NewMemorySubjectIndex()
			for _, s := range []string{"User-bare", "User-nodek", "User-u1"} {
				require.NoError(t, idx.IndexSubjects(ctx, s, []string{"u1"}))
			}
			return NewSubjectResolver(store, WithResolverIndex(idx), WithAuthoritativeIndex())
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			fp, err := resolver.Resolve(ctx, "u1")
			require.NoError(t, err)
			assert.Equal(t, 3, fp.EventCount)
			assert.Equal(t, []string{"k"}, fp.KeyIDs, "only the complete envelope contributes a key")
			assert.Equal(t, 2, fp.CleartextEvents, "a bare key id and a damaged envelope are cleartext")
		})
	}
}
