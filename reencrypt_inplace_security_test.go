package mink

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go-mink.dev/adapters/memory"
	"go-mink.dev/encryption/local"
)

// newSubjectKeyedInPlaceStore builds an encryption-enabled store over adapter with
// subject-scoped keys ("key-<subject>") and the userIDTagger, as the live append
// path would be configured, plus any extra options (e.g. WithCallerSubjectTags).
func newSubjectKeyedInPlaceStore(t *testing.T, adapter *memory.MemoryAdapter, opts ...Option) *EventStore {
	t.Helper()
	provider, err := local.New(
		local.WithKey("key-u1", []byte("0123456789abcdef0123456789abcdef")),
		local.WithKey("key-victim", []byte("fedcba9876543210fedcba9876543210")),
		local.WithKey("default-key", make([]byte, 32)),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = provider.Close() })
	cfg := NewFieldEncryptionConfig(
		WithEncryptionProvider(provider),
		WithDefaultKeyID("default-key"),
		WithSubjectKeyResolver(func(id string) string { return "key-" + id }),
		WithEncryptedFields("eraseUserCreated", "email"),
	)
	store := New(adapter, append([]Option{WithFieldEncryption(cfg), WithSubjectTagger(userIDTagger)}, opts...)...)
	store.RegisterEvents(eraseUserCreated{})
	return store
}

// A $subjects tag forged AT REST (written in the merge era, or by a tagger-less
// writer that kept caller tags as-is) must not pick the wrapping key when the
// event is later sealed in place: the in-place path applies the same
// replace-by-default policy as Append, so the tagger decides the subject and
// therefore the key, and the event leaves the victim's erasure footprint.
func TestReEncryptStreamInPlace_ForgedStoredTagCannotSelectKey(t *testing.T) {
	ctx := context.Background()
	const stream = "User-u1"

	plantForgedTag := func(t *testing.T) *memory.MemoryAdapter {
		t.Helper()
		adapter := memory.NewAdapter()
		// No tagger on the writer: the caller's forged tag is persisted verbatim
		// (explicit manual tagging), in plaintext.
		plain := New(adapter)
		plain.RegisterEvents(eraseUserCreated{})
		forged := Metadata{UserID: "u1", Custom: map[string]string{subjectTagsKey: `["victim"]`}}
		require.NoError(t, plain.Append(ctx, stream,
			[]interface{}{eraseUserCreated{UserID: "u1", Email: "alice@example.com"}}, WithAppendMetadata(forged)))
		raw, err := plain.LoadRaw(ctx, stream, 0)
		require.NoError(t, err)
		require.Equal(t, []string{"victim"}, GetSubjectTags(raw[0].Metadata), "precondition: forged tag at rest")
		return adapter
	}

	t.Run("default: tagger replaces the stored tag and selects the key", func(t *testing.T) {
		adapter := plantForgedTag(t)
		enc := newSubjectKeyedInPlaceStore(t, adapter)

		n, keys, err := enc.ReEncryptStreamInPlace(ctx, stream)
		require.NoError(t, err)
		assert.Equal(t, 1, n)
		assert.Equal(t, []string{"key-u1"}, keys, "the wrapping key is the tagger's subject's key")

		stored := loadOnlyRaw(t, ctx, enc, stream)
		assert.True(t, IsEncrypted(stored.Metadata))
		assert.Equal(t, "key-u1", GetEncryptionKeyID(stored.Metadata))
		assert.Equal(t, []string{"u1"}, GetSubjectTags(stored.Metadata))
		assert.False(t, SubjectFilter("victim")(stored), "must leave the victim's footprint")
		assert.True(t, SubjectFilter("u1")(stored))

		// Still decrypts under the real subject's key.
		dec, err := enc.DecryptStoredEvent(ctx, stored)
		require.NoError(t, err)
		assert.Contains(t, string(dec.Data), "alice@example.com")
	})

	t.Run("WithCallerSubjectTags: stored tags merge first and select the key (trusted writers only)", func(t *testing.T) {
		adapter := plantForgedTag(t)
		enc := newSubjectKeyedInPlaceStore(t, adapter, WithCallerSubjectTags())

		n, keys, err := enc.ReEncryptStreamInPlace(ctx, stream)
		require.NoError(t, err)
		assert.Equal(t, 1, n)
		assert.Equal(t, []string{"key-victim"}, keys)

		stored := loadOnlyRaw(t, ctx, enc, stream)
		assert.Equal(t, []string{"victim", "u1"}, GetSubjectTags(stored.Metadata))
		assert.Equal(t, "key-victim", GetEncryptionKeyID(stored.Metadata), "first tag selects the key, as documented")
	})

	t.Run("no tagger: stored tags are kept and the default key is used", func(t *testing.T) {
		adapter := plantForgedTag(t)
		provider, err := local.New(local.WithKey("default-key", make([]byte, 32)))
		require.NoError(t, err)
		t.Cleanup(func() { _ = provider.Close() })
		enc := New(adapter, WithFieldEncryption(NewFieldEncryptionConfig(
			WithEncryptionProvider(provider),
			WithDefaultKeyID("default-key"),
			WithEncryptedFields("eraseUserCreated", "email"),
		)))
		enc.RegisterEvents(eraseUserCreated{})

		_, keys, err := enc.ReEncryptStreamInPlace(ctx, stream)
		require.NoError(t, err)
		assert.Equal(t, []string{"default-key"}, keys)
		stored := loadOnlyRaw(t, ctx, enc, stream)
		assert.Equal(t, []string{"victim"}, GetSubjectTags(stored.Metadata), "explicit manual tagging is left untouched without a tagger")
	})
}

// EncryptStoredEvent strips a stale, incomplete envelope found at rest (a bare
// $encryption_key_id with no $encrypted_fields does not make the event
// IsEncrypted) before sealing, so the re-stamped envelope names the real key.
func TestEncryptStoredEvent_StripsStalePartialEnvelope(t *testing.T) {
	ctx := context.Background()
	enc := newSubjectKeyedInPlaceStore(t, memory.NewAdapter())

	stale := StoredEvent{
		StreamID: "User-u1",
		Type:     "eraseUserCreated",
		Data:     []byte(`{"userId":"u1","email":"alice@example.com"}`),
		Metadata: Metadata{UserID: "u1", Custom: map[string]string{
			encryptionKeyIDKey:     "attacker-key",
			encryptedDEKKey:        "AAAA",
			encryptionAlgorithmKey: "XOR",
			"app":                  "kept",
		}},
	}
	require.False(t, IsEncrypted(stale.Metadata), "precondition: partial envelope is not an encrypted event")

	out, err := enc.EncryptStoredEvent(ctx, stale)
	require.NoError(t, err)
	assert.True(t, HasEncryptionEnvelope(out.Metadata))
	assert.Equal(t, "key-u1", GetEncryptionKeyID(out.Metadata))
	assert.Equal(t, encryptionAlgorithm, GetEncryptionAlgorithm(out.Metadata))
	assert.Equal(t, "kept", out.Metadata.Custom["app"])
	assert.Equal(t, "attacker-key", stale.Metadata.Custom[encryptionKeyIDKey], "input metadata is never mutated")

	back, err := enc.DecryptStoredEvent(ctx, out)
	require.NoError(t, err)
	assert.Contains(t, string(back.Data), "alice@example.com")
}
