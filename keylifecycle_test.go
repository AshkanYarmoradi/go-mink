package mink

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go-mink.dev/adapters/memory"
	"go-mink.dev/encryption/local"
)

func skipRevokedHandler(error, string, Metadata) error { return nil }

func encStore(t *testing.T, provider *local.Provider, adapter *memory.MemoryAdapter, defaultKey string) *EventStore {
	t.Helper()
	cfg := NewFieldEncryptionConfig(
		WithEncryptionProvider(provider),
		WithDefaultKeyID(defaultKey),
		WithEncryptedFields("eraseUserCreated", "email"),
		WithDecryptionErrorHandler(skipRevokedHandler),
	)
	store := New(adapter, WithFieldEncryption(cfg))
	store.RegisterEvents(eraseUserCreated{})
	return store
}

// 7.1: events encrypted under an old key still decrypt after the default key rotates.
func TestKeyRotation_CrossDecrypt(t *testing.T) {
	ctx := context.Background()
	provider, err := local.New(local.WithKey("key1", make([]byte, 32)), local.WithKey("key2", make([]byte, 32)))
	require.NoError(t, err)
	adapter := memory.NewAdapter()

	store1 := encStore(t, provider, adapter, "key1")
	require.NoError(t, store1.Append(ctx, "User-u1", []interface{}{eraseUserCreated{UserID: "u1", Email: "old@example.com"}}))

	// "Rotate" the default key to key2; old events keep their key1.
	store2 := encStore(t, provider, adapter, "key2")
	require.NoError(t, store2.Append(ctx, "User-u2", []interface{}{eraseUserCreated{UserID: "u2", Email: "new@example.com"}}))

	for _, c := range []struct{ stream, want string }{{"User-u1", "old@example.com"}, {"User-u2", "new@example.com"}} {
		events, err := store2.Load(ctx, c.stream)
		require.NoError(t, err)
		raw, _ := json.Marshal(events[0].Data)
		assert.Contains(t, string(raw), c.want)
	}
}

// 7.2: a soft-revoked key blocks decryption but can be restored within the window.
func TestRecoverableRevocation(t *testing.T) {
	ctx := context.Background()
	store, provider := newEraseTestStore(t, "k")
	require.NoError(t, store.Append(ctx, "User-u1", []interface{}{eraseUserCreated{UserID: "u1", Email: "secret@example.com"}}))

	require.NoError(t, provider.SoftRevokeKey("k", time.Hour))
	events, err := store.Load(ctx, "User-u1")
	require.NoError(t, err)
	raw, _ := json.Marshal(events[0].Data)
	assert.NotContains(t, string(raw), "secret@example.com", "soft-revoked key must block decryption")

	require.NoError(t, provider.UnrevokeKey("k"))
	events, err = store.Load(ctx, "User-u1")
	require.NoError(t, err)
	raw, _ = json.Marshal(events[0].Data)
	assert.Contains(t, string(raw), "secret@example.com", "un-revoke within window restores decryption")
}

func TestRecoverableRevocation_PermanentAfterWindow(t *testing.T) {
	_, provider := newEraseTestStore(t, "k")
	require.NoError(t, provider.SoftRevokeKey("k", 0)) // window already elapsed
	err := provider.UnrevokeKey("k")
	require.Error(t, err, "un-revoke after the window must fail (permanent)")

	revoked, _ := provider.IsRevoked("k")
	assert.True(t, revoked)
}

// 7.3: re-encryption by copy decouples data from the old key (append-only).
func TestReEncryptStream(t *testing.T) {
	ctx := context.Background()
	provider, err := local.New(local.WithKey("key1", make([]byte, 32)), local.WithKey("key2", make([]byte, 32)))
	require.NoError(t, err)
	adapter := memory.NewAdapter()

	store1 := encStore(t, provider, adapter, "key1")
	require.NoError(t, store1.Append(ctx, "User-src", []interface{}{eraseUserCreated{UserID: "u1", Email: "carry@example.com"}}))

	// Re-encrypt the stream under key2 into a new stream.
	store2 := encStore(t, provider, adapter, "key2")
	n, oldKeys, err := ReEncryptStream(ctx, store2, "User-src", "User-dst")
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, []string{"key1"}, oldKeys, "returns the old key id(s) to revoke")

	// Revoking key1 redacts the source but not the re-encrypted destination.
	require.NoError(t, provider.RevokeKey("key1"))

	dst, err := store2.Load(ctx, "User-dst")
	require.NoError(t, err)
	raw, _ := json.Marshal(dst[0].Data)
	assert.Contains(t, string(raw), "carry@example.com")

	// The destination now carries the NEW key marker, not the stale old one.
	dstRaw, err := store2.LoadRaw(ctx, "User-dst", 0)
	require.NoError(t, err)
	assert.Equal(t, "key2", GetEncryptionKeyID(dstRaw[0].Metadata))
}

// 8.1: a re-run against an existing destination errors instead of duplicating.
func TestReEncryptStream_IdempotencyGuard(t *testing.T) {
	ctx := context.Background()
	provider, err := local.New(local.WithKey("key1", make([]byte, 32)), local.WithKey("key2", make([]byte, 32)))
	require.NoError(t, err)
	adapter := memory.NewAdapter()

	store1 := encStore(t, provider, adapter, "key1")
	require.NoError(t, store1.Append(ctx, "User-src", []interface{}{eraseUserCreated{UserID: "u1", Email: "carry@example.com"}}))

	store2 := encStore(t, provider, adapter, "key2")
	_, _, err = ReEncryptStream(ctx, store2, "User-src", "User-dst")
	require.NoError(t, err)

	// Second run must fail (destination already exists) rather than duplicate the copy.
	_, _, err = ReEncryptStream(ctx, store2, "User-src", "User-dst")
	require.Error(t, err)

	dst, err := store2.Load(ctx, "User-dst")
	require.NoError(t, err)
	assert.Len(t, dst, 1, "no duplicate copy was appended")
}
