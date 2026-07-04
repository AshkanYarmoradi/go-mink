package mink_test

import (
	"crypto/rand"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mink "go-mink.dev"
	"go-mink.dev/encryption/local"
)

// e2eUser has one encrypted field (Email) and one plaintext field (UserID).
type e2eUser struct {
	UserID string `json:"userId"`
	Email  string `json:"email"`
}

// TestE2E_Encryption_CiphertextAtRest: a field-encrypted event is stored as ciphertext in the
// PostgreSQL `data` column and decrypted transparently on load.
func TestE2E_Encryption_CiphertextAtRest(t *testing.T) {
	key := make([]byte, 32)
	_, err := rand.Read(key)
	require.NoError(t, err)
	provider, err := local.New(local.WithKey("k", key))
	require.NoError(t, err)
	t.Cleanup(func() { _ = provider.Close() })

	p := newE2EPGBase(t)
	p.Store = mink.New(p.Adapter, mink.WithFieldEncryption(mink.NewFieldEncryptionConfig(
		mink.WithEncryptionProvider(provider),
		mink.WithDefaultKeyID("k"),
		mink.WithEncryptedFields("e2eUser", "email"),
	)))
	p.Store.RegisterEvents(e2eUser{})

	require.NoError(t, p.Store.Append(p.Ctx, "user-1",
		[]interface{}{e2eUser{UserID: "1", Email: "secret@example.com"}}))

	// Ciphertext at rest: a direct SQL read of the row must not contain the plaintext email,
	// while the non-encrypted field is stored as-is. Parse the JSONB (PG re-serializes with
	// its own spacing) rather than string-matching.
	raw := p.rawEventData(t, "user-1")
	var atRest map[string]string
	require.NoError(t, json.Unmarshal(raw, &atRest))
	assert.Equal(t, "1", atRest["userId"], "the non-encrypted field stays plaintext")
	assert.NotEqual(t, "secret@example.com", atRest["email"], "the encrypted field is ciphertext at rest")
	assert.NotEmpty(t, atRest["email"], "the encrypted field is present (as ciphertext)")

	// Transparent decryption on load.
	loaded, err := p.Store.Load(p.Ctx, "user-1")
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	b, err := json.Marshal(loaded[0].Data)
	require.NoError(t, err)
	assert.Contains(t, string(b), "secret@example.com", "Load decrypts the field transparently")
}

// TestE2E_Encryption_ZeroOverheadWhenUnconfigured: with no field encryption, events are stored
// plaintext and carry no encryption metadata.
func TestE2E_Encryption_ZeroOverheadWhenUnconfigured(t *testing.T) {
	p := newE2EPG(t, e2eUser{})

	require.NoError(t, p.Store.Append(p.Ctx, "user-2",
		[]interface{}{e2eUser{UserID: "2", Email: "plain@example.com"}}))

	raw := p.rawEventData(t, "user-2")
	assert.Contains(t, string(raw), "plain@example.com", "no encryption → plaintext at rest")

	stored, err := p.Store.LoadRaw(p.Ctx, "user-2", 0)
	require.NoError(t, err)
	require.Len(t, stored, 1)
	assert.False(t, mink.IsEncrypted(stored[0].Metadata), "no encryption metadata added")
}
