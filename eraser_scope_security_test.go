package mink

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go-mink.dev/adapters/memory"
	"go-mink.dev/encryption/local"
)

// newMixedKeyStore builds a tagged, field-encrypted store where tagged events are
// wrapped under a per-subject key ("key-<subject>") while UNTAGGED events (no UserID
// metadata, so the tagger derives nothing) fall back to the default key "k-default" —
// the shape of a stream written across the moment subject tagging was enabled.
func newMixedKeyStore(t *testing.T) (*EventStore, *local.Provider) {
	t.Helper()
	provider, err := local.New(
		local.WithKey("k-default", make([]byte, 32)),
		local.WithKey("key-u1", make([]byte, 32)),
		local.WithKey("key-u2", make([]byte, 32)),
	)
	require.NoError(t, err)
	cfg := NewFieldEncryptionConfig(
		WithEncryptionProvider(provider),
		WithDefaultKeyID("k-default"),
		WithEncryptedFields("eraseUserCreated", "email"),
		WithSubjectKeyResolver(func(subjectID string) string { return "key-" + subjectID }),
		WithDecryptionErrorHandler(func(error, string, Metadata) error { return nil }),
	)
	store := New(memory.NewAdapter(), WithFieldEncryption(cfg), WithSubjectTagger(userIDTagger))
	store.RegisterEvents(eraseUserCreated{}, ErasureMarker{})
	return store, provider
}

// A listed stream that holds BOTH tagged and untagged events: the untagged (pre-tagging)
// events may be the subject's own, but nothing attributes them, so their key is not
// collected — and the result must say so (Partial + note) instead of certifying.
func TestErase_StreamsScope_PartiallyTaggedStreamIsPartial(t *testing.T) {
	ctx := context.Background()
	store, provider := newMixedKeyStore(t)
	// Written before tagging: no UserID metadata → untagged → default key.
	require.NoError(t, store.Append(ctx, "User-u1", []interface{}{eraseUserCreated{UserID: "u1", Email: "old@example.com"}}))
	appendUser(t, ctx, store, "User-u1", "u1") // tagged → key-u1

	var cert ErasureCertificate
	res, err := NewDataEraser(store, captureCert(&cert)).
		Erase(ctx, ErasureRequest{SubjectID: "u1", Streams: []string{"User-u1"}})
	require.NoError(t, err)

	assert.Equal(t, []string{"key-u1"}, res.KeysRevoked, "only the tagged event's key is collected")
	revoked, _ := provider.IsRevoked("k-default")
	assert.False(t, revoked, "the untagged event's key is not touched (it may protect co-tenants)")
	assert.Equal(t, 1, res.EventsScanned, "the untagged event is not matched")
	assert.True(t, res.Partial, "the subject's pre-tagging events may remain: the erasure is not provably complete")
	assert.True(t, res.Failed())
	assert.Empty(t, res.SharedStreams, "an untagged event does not make the stream shared")
	require.Len(t, res.Notes, 1)
	assert.Contains(t, res.Notes[0], "1 untagged event(s) in 1 listed stream(s) that also carry subject tags")
	assert.NotContains(t, res.Notes[0], "User-u1", "notes carry counts only")

	assert.False(t, cert.Verified, "a partial erasure cannot be attested")
	assert.True(t, cert.Partial)
	assert.Equal(t, 1, cert.EventsChecked)
	assert.True(t, notesContain(cert.Notes, "1 untagged event(s) in 1 listed stream(s)"), "the note reaches the certificate: %v", cert.Notes)
}

// The partially-tagged rule is per stream: a fully tagged listed stream contributes no
// untagged count, and a fully untagged one is the (separately noted) legacy scope.
func TestErase_StreamsScope_PartialOnlyForMixedStreams(t *testing.T) {
	ctx := context.Background()
	store, _ := newMixedKeyStore(t)
	appendUser(t, ctx, store, "User-u1", "u1")                                                                                // fully tagged
	require.NoError(t, store.Append(ctx, "Legacy-l1", []interface{}{eraseUserCreated{UserID: "u1", Email: "l@example.com"}})) // fully untagged

	res, err := NewDataEraser(store).
		Erase(ctx, ErasureRequest{SubjectID: "u1", Streams: []string{"User-u1", "Legacy-l1"}})
	require.NoError(t, err)
	assert.False(t, res.Partial, "no mixed stream: completeness is not in doubt on this path")
	assert.Equal(t, []string{"k-default", "key-u1"}, res.KeysRevoked, "legacy scope collects the untagged stream's key")
	require.Len(t, res.Notes, 1, "only the legacy-scope note: %v", res.Notes)
	assert.Contains(t, res.Notes[0], "1 of 2 listed stream(s) carry no subject tags")
}

// A bare "$encryption_key_id" with no encrypted-fields list / wrapped DEK is plaintext
// as far as crypto-shredding is concerned (HasEncryptionEnvelope): the eraser must not
// revoke the key it names — that would shred everything else under a key protecting
// none of the matched data — and counts the event as cleartext instead.
func TestErase_StreamsScope_BareKeyIDIsCleartext(t *testing.T) {
	ctx := context.Background()
	store, provider := newEraseTestStore(t, "k") // no tagger: legacy scope collects every key
	appendRawEnvelope(t, ctx, store.Adapter(), "User-bare", map[string]string{encryptionKeyIDKey: "k"})
	appendRawEnvelope(t, ctx, store.Adapter(), "User-nodek", map[string]string{encryptedFieldsKey: `["email"]`, encryptionKeyIDKey: "k"})

	res, err := NewDataEraser(store).
		Erase(ctx, ErasureRequest{SubjectID: "u1", Streams: []string{"User-bare", "User-nodek"}})
	require.NoError(t, err)
	assert.Empty(t, res.KeysRevoked, "no complete envelope: no key to revoke")
	assert.Equal(t, 2, res.EventsScanned)
	assert.Equal(t, 2, res.CleartextEvents, "a bare key id and a damaged envelope are both cleartext to an erasure")
	revoked, _ := provider.IsRevoked("k")
	assert.False(t, revoked, "the named key must survive: nothing under it was ciphertext")
}

// The scan (Filter) path applies the same predicate.
func TestErase_FilterScope_BareKeyIDIsCleartext(t *testing.T) {
	ctx := context.Background()
	store, provider := newSubjectTestStore(t, "k")
	appendRawEnvelope(t, ctx, store.Adapter(), "User-bare", map[string]string{encryptionKeyIDKey: "k", subjectTagsKey: `["u1"]`})

	res, err := NewDataEraser(store).Erase(ctx, ErasureRequest{SubjectID: "u1", Filter: SubjectFilter("u1")})
	require.NoError(t, err)
	assert.Empty(t, res.KeysRevoked)
	assert.Equal(t, 1, res.CleartextEvents)
	revoked, _ := provider.IsRevoked("k")
	assert.False(t, revoked)
}

// The shared-key guard uses the same predicate: a co-tenant's event carrying only a bare
// key id is not ciphertext under that key, so it does not make the key shared.
func TestDataEraser_SharedKeyGuard_IgnoresBareKeyIDEvents(t *testing.T) {
	ctx := context.Background()
	store, provider := newSubjectTestStore(t, "k")
	appendUser(t, ctx, store, "User-u1", "u1") // full envelope under "k", tagged u1
	appendRawEnvelope(t, ctx, store.Adapter(), "User-u2", map[string]string{encryptionKeyIDKey: "k", subjectTagsKey: `["u2"]`})

	res, err := NewDataEraser(store, WithEraseSubjectResolver(NewSubjectResolver(store)), WithSharedKeyGuard()).
		Erase(ctx, ErasureRequest{SubjectID: "u1"})
	require.NoError(t, err, "u2's bare-key-id event protects no ciphertext under k, so k is exclusive to u1")
	assert.Equal(t, []string{"k"}, res.KeysRevoked)
	revoked, _ := provider.IsRevoked("k")
	assert.True(t, revoked)
}

// The resolved (SubjectID-only) path and the Streams path classify the same event the
// same way: a bare key id is cleartext on both, so the two request shapes agree.
func TestErase_CleartextPredicateIsTheSameOnEveryPath(t *testing.T) {
	ctx := context.Background()
	store, _ := newSubjectTestStore(t, "k")
	appendRawEnvelope(t, ctx, store.Adapter(), "User-bare", map[string]string{encryptionKeyIDKey: "k", subjectTagsKey: `["u1"]`})
	appendUser(t, ctx, store, "User-u1", "u1")

	resolved, err := NewDataEraser(store, WithEraseSubjectResolver(NewSubjectResolver(store))).
		Erase(ctx, ErasureRequest{SubjectID: "u1"})
	require.NoError(t, err)
	byStreams, err := NewDataEraser(store).
		Erase(ctx, ErasureRequest{SubjectID: "u1", Streams: []string{"User-bare", "User-u1"}})
	require.NoError(t, err)

	assert.Equal(t, 1, resolved.CleartextEvents)
	assert.Equal(t, resolved.CleartextEvents, byStreams.CleartextEvents)
	assert.Equal(t, []string{"k"}, resolved.KeysRevoked)
	assert.Equal(t, resolved.KeysRevoked, byStreams.KeysRevoked)
}

// Shared streams are detected on every request shape, including the scan path where a
// co-tenant's event precedes the subject's first matched event.
func TestErase_SharedStreamsDetectedOnStreamsAndFilterPaths(t *testing.T) {
	ctx := context.Background()
	store, _ := newPerSubjectKeyStore(t)
	appendUser(t, ctx, store, "Shared-s1", "u2") // the co-tenant writes first
	appendUser(t, ctx, store, "Shared-s1", "u1")
	appendUser(t, ctx, store, "User-u1", "u1")

	byStreams, err := NewDataEraser(store).
		Erase(ctx, ErasureRequest{SubjectID: "u1", Streams: []string{"Shared-s1", "User-u1"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"Shared-s1", "User-u1"}, byStreams.Streams)
	assert.Equal(t, []string{"Shared-s1"}, byStreams.SharedStreams)

	byFilter, err := NewDataEraser(store).
		Erase(ctx, ErasureRequest{SubjectID: "u1", Filter: SubjectFilter("u1")})
	require.NoError(t, err)
	assert.Equal(t, []string{"Shared-s1", "User-u1"}, byFilter.Streams)
	assert.Equal(t, []string{"Shared-s1"}, byFilter.SharedStreams)

	// A listed stream where the subject matched nothing is neither in Streams nor shared.
	future := time.Now().Add(time.Hour)
	none, err := NewDataEraser(store).
		Erase(ctx, ErasureRequest{SubjectID: "u1", Streams: []string{"Shared-s1"}, FromTime: &future})
	require.NoError(t, err)
	assert.Empty(t, none.Streams)
	assert.Empty(t, none.SharedStreams)
}
