package mink

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go-mink.dev/adapters/memory"
	"go-mink.dev/encryption/local"
)

// klUserV2 is a versioned event: v1 has no "plan"; the 1→2 upcaster adds it.
type klUserV2 struct {
	UserID string `json:"userId"`
	Email  string `json:"email"`
	Plan   string `json:"plan,omitempty"`
}

func klProvider(t *testing.T) *local.Provider {
	t.Helper()
	provider, err := local.New(
		local.WithKey("key1", make([]byte, 32)),
		local.WithKey("key2", []byte("0123456789abcdef0123456789abcdef")),
		local.WithKey("key-u1", []byte("fedcba9876543210fedcba9876543210")),
		local.WithKey("key-victim", []byte("00112233445566778899aabbccddeeff")),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = provider.Close() })
	return provider
}

func klConfig(provider *local.Provider, defaultKey string, opts ...EncryptionOption) *FieldEncryptionConfig {
	base := []EncryptionOption{
		WithEncryptionProvider(provider),
		WithDefaultKeyID(defaultKey),
		WithEncryptedFields("klUserV2", "email"),
		WithEncryptedFields("eraseUserCreated", "email"),
	}
	return NewFieldEncryptionConfig(append(base, opts...)...)
}

// A key-rotation copy made by a store with NO UpcasterChain (and no tagger) keeps
// the source event's $schema_version and $subjects verbatim: the copy must not be
// silently downgraded to version 1 (which would re-run the 1→N upcasters over
// already-current data when the application reads it with its chain), nor lose
// the tags that define the subject's erasure footprint.
func TestReEncryptStream_NoChain_PreservesSchemaVersionAndTags(t *testing.T) {
	ctx := context.Background()
	provider := klProvider(t)
	adapter := memory.NewAdapter()

	// The application store: chain with latest=2 for klUserV2, key1.
	chain := NewUpcasterChain()
	require.NoError(t, chain.Register(newTestUpcaster("klUserV2", 1, 2, addJSONFieldUpcastFn("plan", "basic"))))
	app := New(adapter, WithFieldEncryption(klConfig(provider, "key1")), WithUpcasters(chain))
	app.RegisterEvents(klUserV2{})
	require.NoError(t, app.Append(ctx, "User-src", []interface{}{klUserV2{UserID: "u1", Email: "carry@example.com", Plan: "pro"}},
		WithAppendMetadata(setSubjectTags(Metadata{CorrelationID: "corr-1"}, []string{"manual"}))))
	src := loadOnlyRaw(t, ctx, app, "User-src")
	require.Equal(t, "2", src.Metadata.Custom[schemaVersionKey], "precondition: source is stamped v2")
	require.Equal(t, []string{"manual"}, GetSubjectTags(src.Metadata))

	// A maintenance store rotating to key2 WITHOUT the application's chain or tagger.
	maint := New(adapter, WithFieldEncryption(klConfig(provider, "key2")))
	maint.RegisterEvents(klUserV2{})
	n, oldKeys, err := ReEncryptStream(ctx, maint, "User-src", "User-dst")
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, []string{"key1"}, oldKeys)

	dst := loadOnlyRaw(t, ctx, maint, "User-dst")
	assert.Equal(t, "2", dst.Metadata.Custom[schemaVersionKey], "version marker carried verbatim, not dropped")
	assert.Equal(t, []string{"manual"}, GetSubjectTags(dst.Metadata), "subject tags carried over")
	assert.Equal(t, "corr-1", dst.Metadata.CorrelationID)
	assert.Equal(t, "key2", GetEncryptionKeyID(dst.Metadata), "re-stamped under the new key")

	// The application (with its chain) reads the copy without re-upcasting: the
	// data is exactly what was written (plan stays "pro", not overwritten by the
	// 1→2 upcaster's "basic").
	events, err := app.Load(ctx, "User-dst")
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, klUserV2{UserID: "u1", Email: "carry@example.com", Plan: "pro"}, events[0].Data)
}

// When the copying store DOES have the chain, Load upcasts the source data to the
// latest version; the copy is then stamped with that version so the marker matches
// the data it carries (the stale source marker would mis-route a later Load).
func TestReEncryptStream_WithChain_StampsLatestVersionForUpcastedData(t *testing.T) {
	ctx := context.Background()
	provider := klProvider(t)
	adapter := memory.NewAdapter()

	// Source written by a store with NO chain: v1 shape (no plan), no marker.
	writer := New(adapter, WithFieldEncryption(klConfig(provider, "key1")))
	writer.RegisterEvents(klUserV2{})
	require.NoError(t, writer.Append(ctx, "User-src", []interface{}{klUserV2{UserID: "u1", Email: "carry@example.com"}}))
	src := loadOnlyRaw(t, ctx, writer, "User-src")
	require.NotContains(t, src.Metadata.Custom, schemaVersionKey, "precondition: unversioned (v1) source")

	chain := NewUpcasterChain()
	require.NoError(t, chain.Register(newTestUpcaster("klUserV2", 1, 2, addJSONFieldUpcastFn("plan", "basic"))))
	copier := New(adapter, WithFieldEncryption(klConfig(provider, "key2")), WithUpcasters(chain))
	copier.RegisterEvents(klUserV2{})

	_, _, err := ReEncryptStream(ctx, copier, "User-src", "User-dst")
	require.NoError(t, err)

	dst := loadOnlyRaw(t, ctx, copier, "User-dst")
	assert.Equal(t, "2", dst.Metadata.Custom[schemaVersionKey], "stamped with the chain's latest version")
	assert.Contains(t, string(dst.Data), `"plan":"basic"`, "the copy carries the upcasted data")

	events, err := copier.Load(ctx, "User-dst")
	require.NoError(t, err)
	assert.Equal(t, klUserV2{UserID: "u1", Email: "carry@example.com", Plan: "basic"}, events[0].Data)
}

// The copy follows the same $subjects policy as Append: a tagger on the copying
// store replaces carried-over tags (so a tag forged at rest cannot select the new
// wrapping key), and WithCallerSubjectTags merges them for trusted operators.
func TestReEncryptStream_TaggerPolicyOnCopies(t *testing.T) {
	ctx := context.Background()
	const src, dst = "User-src", "User-dst"

	plant := func(t *testing.T) (*memory.MemoryAdapter, *local.Provider) {
		t.Helper()
		provider := klProvider(t)
		adapter := memory.NewAdapter()
		writer := New(adapter, WithFieldEncryption(klConfig(provider, "key1")))
		writer.RegisterEvents(eraseUserCreated{})
		forged := setSubjectTags(Metadata{UserID: "u1"}, []string{"victim"})
		require.NoError(t, writer.Append(ctx, src, []interface{}{eraseUserCreated{UserID: "u1", Email: "a@example.com"}}, WithAppendMetadata(forged)))
		return adapter, provider
	}
	subjectKeys := WithSubjectKeyResolver(func(id string) string { return "key-" + id })

	t.Run("default: tagger decides the subject and the key", func(t *testing.T) {
		adapter, provider := plant(t)
		copier := New(adapter, WithFieldEncryption(klConfig(provider, "key2", subjectKeys)), WithSubjectTagger(userIDTagger))
		copier.RegisterEvents(eraseUserCreated{})
		_, _, err := ReEncryptStream(ctx, copier, src, dst)
		require.NoError(t, err)
		copy := loadOnlyRaw(t, ctx, copier, dst)
		assert.Equal(t, []string{"u1"}, GetSubjectTags(copy.Metadata))
		assert.Equal(t, "key-u1", GetEncryptionKeyID(copy.Metadata))
		assert.False(t, SubjectFilter("victim")(copy))
	})

	t.Run("WithCallerSubjectTags: carried-over tags merge first", func(t *testing.T) {
		adapter, provider := plant(t)
		copier := New(adapter, WithFieldEncryption(klConfig(provider, "key2", subjectKeys)), WithSubjectTagger(userIDTagger), WithCallerSubjectTags())
		copier.RegisterEvents(eraseUserCreated{})
		_, _, err := ReEncryptStream(ctx, copier, src, dst)
		require.NoError(t, err)
		copy := loadOnlyRaw(t, ctx, copier, dst)
		assert.Equal(t, []string{"victim", "u1"}, GetSubjectTags(copy.Metadata))
		assert.Equal(t, "key-victim", GetEncryptionKeyID(copy.Metadata))
	})
}
