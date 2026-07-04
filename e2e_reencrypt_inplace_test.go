package mink_test

import (
	"crypto/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mink "go-mink.dev"
	"go-mink.dev/encryption/local"
)

type pgReencEvent struct {
	UserID string `json:"userId"`
	Name   string `json:"name"`
}

// TestReEncryptStreamInPlace_Postgres verifies the in-place backfill against a real
// PostgreSQL adapter: an event appended as plaintext before encryption is re-encrypted
// in place and is then indistinguishable from a natively-appended encrypted one —
// ciphertext on disk, decrypts on read, identity preserved, crypto-shreddable.
// Requires TEST_DATABASE_URL (skips otherwise).
func TestReEncryptStreamInPlace_Postgres(t *testing.T) {
	pg := newE2EPGBase(t) // skips in -short or without TEST_DATABASE_URL
	ctx := pg.Ctx
	const stream = "User-pg-1"

	// Append plaintext through a store with no encryption over the shared adapter.
	plain := mink.New(pg.Adapter)
	plain.RegisterEvents(pgReencEvent{})
	require.NoError(t, plain.Append(ctx, stream, []interface{}{pgReencEvent{UserID: "u1", Name: "Ada"}}))

	raw, err := plain.LoadRaw(ctx, stream, 0)
	require.NoError(t, err)
	require.Len(t, raw, 1)
	require.Contains(t, string(raw[0].Data), "Ada")
	pos0, ver0 := raw[0].GlobalPosition, raw[0].Version

	// Re-encrypt in place with an encryption-enabled store over the same adapter.
	key := make([]byte, 32)
	_, err = rand.Read(key)
	require.NoError(t, err)
	provider, err := local.New(local.WithKey("k", key))
	require.NoError(t, err)
	t.Cleanup(func() { _ = provider.Close() })

	enc := mink.New(pg.Adapter, mink.WithFieldEncryption(mink.NewFieldEncryptionConfig(
		mink.WithEncryptionProvider(provider),
		mink.WithDefaultKeyID("k"),
		mink.WithEncryptedFields("pgReencEvent", "name"),
	)))
	enc.RegisterEvents(pgReencEvent{})

	n, keys, err := enc.ReEncryptStreamInPlace(ctx, stream)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.Equal(t, []string{"k"}, keys)

	// On disk: ciphertext, with event identity/order preserved.
	raw2, err := enc.LoadRaw(ctx, stream, 0)
	require.NoError(t, err)
	require.Len(t, raw2, 1)
	require.NotContains(t, string(raw2[0].Data), "Ada")
	require.True(t, mink.IsEncrypted(raw2[0].Metadata))
	assert.Equal(t, pos0, raw2[0].GlobalPosition, "global position preserved")
	assert.Equal(t, ver0, raw2[0].Version, "version preserved")

	// Decrypts back to plaintext; revoking the key then shreds it.
	dec, err := enc.DecryptStoredEvent(ctx, raw2[0])
	require.NoError(t, err)
	require.Contains(t, string(dec.Data), "Ada")

	require.NoError(t, provider.RevokeKey("k"))
	_, err = enc.DecryptStoredEvent(ctx, raw2[0])
	require.Error(t, err, "revoked key must make the re-encrypted event undecryptable")
}
