package mink

import (
	"context"
	"crypto/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go-mink.dev/adapters/memory"
	"go-mink.dev/encryption/local"
)

// reencEvent has a single encrypted field.
type reencEvent struct {
	UserID string `json:"userId"`
	Name   string `json:"name"`
}

// reencOptionalEvent's encrypted field ("name") is a pointer with omitempty, so a nil
// value is omitted from the instance's JSON entirely — the case where re-encryption finds
// no field to seal and must skip the event rather than count/rewrite it.
type reencOptionalEvent struct {
	UserID string  `json:"userId"`
	Name   *string `json:"name,omitempty"`
}

func newReencProvider(t *testing.T) *local.Provider {
	t.Helper()
	key := make([]byte, 32)
	_, err := rand.Read(key)
	require.NoError(t, err)
	p, err := local.New(local.WithKey("k", key))
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close() })
	return p
}

func reencConfig(p *local.Provider) *FieldEncryptionConfig {
	return NewFieldEncryptionConfig(
		WithEncryptionProvider(p),
		WithDefaultKeyID("k"),
		WithEncryptedFields("reencEvent", "name"),
	)
}

func TestReEncryptStreamInPlace(t *testing.T) {
	ctx := context.Background()
	const stream = "User-1"

	t.Run("plaintext history is encrypted in place, decrypts, and shreds", func(t *testing.T) {
		adapter := memory.NewAdapter()

		// Append plaintext through a store with NO encryption configured.
		plain := New(adapter)
		plain.RegisterEvents(reencEvent{})
		require.NoError(t, plain.Append(ctx, stream, []interface{}{
			reencEvent{UserID: "u1", Name: "Ada"},
			reencEvent{UserID: "u1", Name: "Ada"},
		}))
		raw, err := plain.LoadRaw(ctx, stream, 0)
		require.NoError(t, err)
		require.Len(t, raw, 2)
		require.Contains(t, string(raw[0].Data), "Ada")
		require.False(t, IsEncrypted(raw[0].Metadata))
		pos0, ver0, id0 := raw[0].GlobalPosition, raw[0].Version, raw[0].ID

		// Re-encrypt in place with an encryption-enabled store over the SAME adapter.
		provider := newReencProvider(t)
		enc := New(adapter, WithFieldEncryption(reencConfig(provider)))
		enc.RegisterEvents(reencEvent{})

		n, keys, err := enc.ReEncryptStreamInPlace(ctx, stream)
		require.NoError(t, err)
		require.Equal(t, 2, n)
		require.Equal(t, []string{"k"}, keys)

		// On disk it is now ciphertext, and event identity/order is preserved.
		raw2, err := enc.LoadRaw(ctx, stream, 0)
		require.NoError(t, err)
		require.Len(t, raw2, 2)
		require.NotContains(t, string(raw2[0].Data), "Ada")
		require.True(t, IsEncrypted(raw2[0].Metadata))
		assert.Equal(t, pos0, raw2[0].GlobalPosition, "global position preserved")
		assert.Equal(t, ver0, raw2[0].Version, "version preserved")
		assert.Equal(t, id0, raw2[0].ID, "event id preserved")

		// It decrypts back to the original plaintext...
		dec, err := enc.DecryptStoredEvent(ctx, raw2[0])
		require.NoError(t, err)
		require.Contains(t, string(dec.Data), "Ada")

		// ...and is now crypto-shreddable: revoking the key makes it unrecoverable.
		require.NoError(t, provider.RevokeKey("k"))
		_, err = enc.DecryptStoredEvent(ctx, raw2[0])
		require.Error(t, err, "revoked key must make the re-encrypted event undecryptable")
	})

	t.Run("idempotent — a second run re-encrypts nothing", func(t *testing.T) {
		adapter := memory.NewAdapter()
		plain := New(adapter)
		plain.RegisterEvents(reencEvent{})
		require.NoError(t, plain.Append(ctx, stream, []interface{}{reencEvent{UserID: "u1", Name: "Ada"}}))

		enc := New(adapter, WithFieldEncryption(reencConfig(newReencProvider(t))))
		enc.RegisterEvents(reencEvent{})

		n, _, err := enc.ReEncryptStreamInPlace(ctx, stream)
		require.NoError(t, err)
		require.Equal(t, 1, n)

		n2, _, err := enc.ReEncryptStreamInPlace(ctx, stream)
		require.NoError(t, err)
		require.Equal(t, 0, n2, "already-encrypted events are skipped")
	})

	t.Run("event whose configured field is absent is skipped, not counted, and stays idempotent", func(t *testing.T) {
		adapter := memory.NewAdapter()

		// Append (plaintext) one event WITH the encrypted field and one WITHOUT it.
		plain := New(adapter)
		plain.RegisterEvents(reencOptionalEvent{})
		name := "Ada"
		require.NoError(t, plain.Append(ctx, stream, []interface{}{
			reencOptionalEvent{UserID: "u1", Name: &name}, // field present → sealable
			reencOptionalEvent{UserID: "u2"},              // field absent  → nothing to seal
		}))

		provider := newReencProvider(t)
		enc := New(adapter, WithFieldEncryption(NewFieldEncryptionConfig(
			WithEncryptionProvider(provider),
			WithDefaultKeyID("k"),
			WithEncryptedFields("reencOptionalEvent", "name"),
		)))
		enc.RegisterEvents(reencOptionalEvent{})

		// Only the event that actually carried the field is sealed and counted; the
		// field-absent event is skipped (no no-op rewrite, not counted).
		n, keys, err := enc.ReEncryptStreamInPlace(ctx, stream)
		require.NoError(t, err)
		require.Equal(t, 1, n, "only the event with the field present is sealed")
		require.Equal(t, []string{"k"}, keys)

		// A re-run seals nothing AND must not reprocess the field-absent event (which never
		// becomes IsEncrypted) — otherwise the sweep would never be idempotent.
		n2, _, err := enc.ReEncryptStreamInPlace(ctx, stream)
		require.NoError(t, err)
		require.Equal(t, 0, n2, "field-absent events must not be reprocessed on every run")

		// The field-absent event remains plaintext (it had no PII in that field to seal).
		raw, err := enc.LoadRaw(ctx, stream, 0)
		require.NoError(t, err)
		require.True(t, IsEncrypted(raw[0].Metadata))
		require.False(t, IsEncrypted(raw[1].Metadata), "field-absent event is left untouched")
	})

	t.Run("no encryption configured is a zero-overhead no-op", func(t *testing.T) {
		adapter := memory.NewAdapter()
		plain := New(adapter)
		plain.RegisterEvents(reencEvent{})
		require.NoError(t, plain.Append(ctx, stream, []interface{}{reencEvent{UserID: "u1", Name: "Ada"}}))

		n, keys, err := plain.ReEncryptStreamInPlace(ctx, stream)
		require.NoError(t, err)
		require.Equal(t, 0, n)
		require.Nil(t, keys)

		raw, err := plain.LoadRaw(ctx, stream, 0)
		require.NoError(t, err)
		require.Contains(t, string(raw[0].Data), "Ada", "no rewrite happened")
	})
}

func TestEncryptStoredEvent(t *testing.T) {
	ctx := context.Background()
	const stream = "User-1"
	adapter := memory.NewAdapter()

	plain := New(adapter)
	plain.RegisterEvents(reencEvent{})
	require.NoError(t, plain.Append(ctx, stream, []interface{}{reencEvent{UserID: "u1", Name: "Ada"}}))
	raw, err := plain.LoadRaw(ctx, stream, 0)
	require.NoError(t, err)

	enc := New(adapter, WithFieldEncryption(reencConfig(newReencProvider(t))))
	enc.RegisterEvents(reencEvent{})

	t.Run("encodes and round-trips with DecryptStoredEvent", func(t *testing.T) {
		out, err := enc.EncryptStoredEvent(ctx, raw[0])
		require.NoError(t, err)
		require.True(t, IsEncrypted(out.Metadata))
		require.NotContains(t, string(out.Data), "Ada")

		back, err := enc.DecryptStoredEvent(ctx, out)
		require.NoError(t, err)
		require.Contains(t, string(back.Data), "Ada")

		// Already-encrypted → passthrough (no double-encrypt).
		same, err := enc.EncryptStoredEvent(ctx, out)
		require.NoError(t, err)
		assert.Equal(t, out.Data, same.Data)
	})

	t.Run("passthrough when unconfigured", func(t *testing.T) {
		out, err := plain.EncryptStoredEvent(ctx, raw[0])
		require.NoError(t, err)
		assert.Equal(t, raw[0].Data, out.Data)
		assert.False(t, IsEncrypted(out.Metadata))
	})
}
