package mink

import (
	"context"
	"crypto/rand"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go-mink.dev/adapters/memory"
	"go-mink.dev/encryption"
	"go-mink.dev/encryption/local"
)

// covGDPRStore builds an in-memory store with field encryption, subject tagging, and a subject
// index — the setup DataEraser needs. perSubject=true uses per-subject keys (key-<subject>);
// perSubject=false uses one shared key.
func covGDPRStore(t *testing.T, perSubject bool) (*EventStore, *local.Provider, *MemorySubjectIndex) {
	t.Helper()
	provider, err := local.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = provider.Close() })

	opts := []EncryptionOption{
		WithEncryptionProvider(provider),
		WithEncryptedFields("covEncEvent", "name"),
		WithDecryptionErrorHandler(func(err error, _ string, _ Metadata) error {
			if errors.Is(err, encryption.ErrKeyRevoked) {
				return nil
			}
			return err
		}),
	}
	if perSubject {
		opts = append(opts, WithSubjectKeyResolver(func(s string) string { return "key-" + s }))
	} else {
		key := make([]byte, 32)
		_, _ = rand.Read(key)
		require.NoError(t, provider.AddKey("shared", key))
		opts = append(opts, WithDefaultKeyID("shared"))
	}
	tagger := func(_ string, _ []byte, md Metadata) []string {
		if md.UserID != "" {
			return []string{md.UserID}
		}
		return nil
	}
	idx := NewMemorySubjectIndex()
	store := New(memory.NewAdapter(),
		WithFieldEncryption(NewFieldEncryptionConfig(opts...)),
		WithSubjectTagger(tagger),
		WithSubjectIndexWriter(idx),
	)
	store.RegisterEvents(covEncEvent{}, ErasureMarker{})
	return store, provider, idx
}

func covAddKey(t *testing.T, p *local.Provider, subject string) {
	t.Helper()
	key := make([]byte, 32)
	_, err := rand.Read(key)
	require.NoError(t, err)
	require.NoError(t, p.AddKey("key-"+subject, key))
}

func covAppendEnc(t *testing.T, store *EventStore, subject, name string) {
	t.Helper()
	require.NoError(t, store.Append(context.Background(), "cov-"+subject,
		[]interface{}{covEncEvent{Name: name}}, WithAppendMetadata(Metadata{UserID: subject})))
}

// TestReconcileAfterRevoke covers reconcileAfterRevoke: the no-new-keys no-op, the late-key
// revoke-and-mark-partial path, and the shared-key refusal.
func TestReconcileAfterRevoke(t *testing.T) {
	ctx := context.Background()

	t.Run("no new keys is a no-op", func(t *testing.T) {
		store, provider, idx := covGDPRStore(t, true)
		covAddKey(t, provider, "bob")
		covAppendEnc(t, store, "bob", "secret")
		resolver := NewSubjectResolver(store, WithResolverIndex(idx), WithAuthoritativeIndex())
		eraser := NewDataEraser(store, WithEraseSubjectResolver(resolver))
		result := &ErasureResult{}
		eraser.reconcileAfterRevoke(ctx, "bob", store.EncryptionConfig(),
			map[string]struct{}{"key-bob": {}}, result)
		assert.Empty(t, result.KeysRevoked)
		assert.False(t, result.Partial)
	})

	t.Run("late key is revoked and marked partial", func(t *testing.T) {
		store, provider, idx := covGDPRStore(t, true)
		covAddKey(t, provider, "alice")
		covAppendEnc(t, store, "alice", "secret")
		resolver := NewSubjectResolver(store, WithResolverIndex(idx), WithAuthoritativeIndex())
		eraser := NewDataEraser(store, WithEraseSubjectResolver(resolver))
		result := &ErasureResult{}
		// keySet empty -> alice's key is a "new" key discovered post-revoke.
		eraser.reconcileAfterRevoke(ctx, "alice", store.EncryptionConfig(),
			map[string]struct{}{}, result)
		assert.Contains(t, result.KeysRevoked, "key-alice")
		assert.True(t, result.Partial)
		revoked, _ := provider.IsRevoked("key-alice")
		assert.True(t, revoked)
	})

	t.Run("shared late key is refused by the guard", func(t *testing.T) {
		store, _, idx := covGDPRStore(t, false) // one shared key across subjects
		covAppendEnc(t, store, "dave", "d")
		covAppendEnc(t, store, "erin", "e")
		resolver := NewSubjectResolver(store, WithResolverIndex(idx), WithAuthoritativeIndex())
		eraser := NewDataEraser(store, WithEraseSubjectResolver(resolver), WithSharedKeyGuard())
		result := &ErasureResult{}
		eraser.reconcileAfterRevoke(ctx, "dave", store.EncryptionConfig(),
			map[string]struct{}{}, result)
		assert.True(t, result.Partial)
		assert.NotEmpty(t, result.Errors, "the shared late key is refused")
	})
}

// covInlineProj is a minimal InlineProjection that counts applied events.
type covInlineProj struct {
	ProjectionBase
	applied int
}

func (p *covInlineProj) Apply(_ context.Context, _ StoredEvent) error {
	p.applied++
	return nil
}

// TestProcessInlineProjections_SkipsUnhandled covers the "no inline projection handles this
// event" branch (the event is skipped before any decrypt).
func TestProcessInlineProjections_SkipsUnhandled(t *testing.T) {
	ctx := context.Background()
	store, _ := covEncStore(t)
	engine := NewProjectionEngine(store)
	proj := &covInlineProj{ProjectionBase: NewProjectionBase("ci", "covEncEvent")}
	require.NoError(t, engine.RegisterInline(proj))

	// An event of a type no inline projection handles is skipped (and never decrypted).
	require.NoError(t, engine.ProcessInlineProjections(ctx, []StoredEvent{
		{Type: "OtherType", Data: []byte(`{}`), StreamID: "s", Version: 1},
	}))
	assert.Zero(t, proj.applied)
}
