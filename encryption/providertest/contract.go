// Package providertest provides shared test helpers for encryption.Provider implementations.
// It eliminates duplication of common contract tests across provider packages.
package providertest

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go-mink.dev/encryption"
)

// AssertCloseBlocksAllOperations verifies that after Close(), all Provider
// methods return ErrProviderClosed.
func AssertCloseBlocksAllOperations(t *testing.T, p encryption.Provider) {
	t.Helper()

	err := p.Close()
	require.NoError(t, err)

	ctx := context.Background()

	_, err = p.Encrypt(ctx, "key-1", []byte("data"))
	require.Error(t, err)
	assert.ErrorIs(t, err, encryption.ErrProviderClosed)

	_, err = p.Decrypt(ctx, "key-1", []byte("data"))
	require.Error(t, err)
	assert.ErrorIs(t, err, encryption.ErrProviderClosed)

	_, err = p.GenerateDataKey(ctx, "key-1")
	require.Error(t, err)
	assert.ErrorIs(t, err, encryption.ErrProviderClosed)

	_, err = p.DecryptDataKey(ctx, "key-1", []byte("data"))
	require.Error(t, err)
	assert.ErrorIs(t, err, encryption.ErrProviderClosed)
}

// AssertGenerateDataKeyBasics verifies GenerateDataKey returns a valid DataKey
// with 32-byte plaintext, non-empty ciphertext, and the correct key ID.
func AssertGenerateDataKeyBasics(t *testing.T, p encryption.Provider, keyID string) *encryption.DataKey {
	t.Helper()

	dk, err := p.GenerateDataKey(context.Background(), keyID)
	require.NoError(t, err)

	assert.Len(t, dk.Plaintext, 32)
	assert.NotEmpty(t, dk.Ciphertext)
	assert.Equal(t, keyID, dk.KeyID)

	return dk
}

// AssertEncryptError verifies that Encrypt returns ErrEncryptionFailed.
func AssertEncryptError(t *testing.T, p encryption.Provider, keyID string) {
	t.Helper()

	_, err := p.Encrypt(context.Background(), keyID, []byte("data"))
	require.Error(t, err)
	assert.ErrorIs(t, err, encryption.ErrEncryptionFailed)
}

// AssertDecryptError verifies that Decrypt returns ErrDecryptionFailed.
func AssertDecryptError(t *testing.T, p encryption.Provider, keyID string) {
	t.Helper()

	_, err := p.Decrypt(context.Background(), keyID, []byte("data"))
	require.Error(t, err)
	assert.ErrorIs(t, err, encryption.ErrDecryptionFailed)
}

// AssertGenerateDataKeyError verifies that GenerateDataKey returns ErrEncryptionFailed.
func AssertGenerateDataKeyError(t *testing.T, p encryption.Provider, keyID string) {
	t.Helper()

	_, err := p.GenerateDataKey(context.Background(), keyID)
	require.Error(t, err)
	assert.ErrorIs(t, err, encryption.ErrEncryptionFailed)
}

// AssertDecryptDataKeyError verifies that DecryptDataKey returns ErrDecryptionFailed.
func AssertDecryptDataKeyError(t *testing.T, p encryption.Provider, keyID string) {
	t.Helper()

	_, err := p.DecryptDataKey(context.Background(), keyID, []byte("bad"))
	require.Error(t, err)
	assert.ErrorIs(t, err, encryption.ErrDecryptionFailed)
}

// AssertRevokeMakesDecryptFail verifies the core crypto-shred property for any
// Revocable provider: after a key is revoked, data and DEKs encrypted under it can no
// longer be decrypted. This is the property that actually delivers the GDPR right to
// erasure, so every provider (not just local) must satisfy it. keyID must exist and be
// live when called.
func AssertRevokeMakesDecryptFail(t *testing.T, p encryption.Provider, keyID string) {
	t.Helper()
	ctx := context.Background()

	// Encrypt data and wrap a DEK under the key while it is still live.
	ciphertext, err := p.Encrypt(ctx, keyID, []byte("secret"))
	require.NoError(t, err)
	dk, err := p.GenerateDataKey(ctx, keyID)
	require.NoError(t, err)

	// Crypto-shred the key.
	require.NoError(t, encryption.Revoke(p, keyID))

	revoked, err := encryption.IsRevoked(p, keyID)
	require.NoError(t, err)
	assert.True(t, revoked, "IsRevoked must report true after Revoke")

	// Neither the data nor the DEK may be recoverable.
	_, err = p.Decrypt(ctx, keyID, ciphertext)
	assert.Error(t, err, "Decrypt must fail after the key is revoked")

	_, err = p.DecryptDataKey(ctx, keyID, dk.Ciphertext)
	assert.Error(t, err, "DecryptDataKey must fail after the key is revoked")
}
