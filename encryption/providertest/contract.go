// Package providertest provides shared test helpers for encryption.Provider implementations.
// It eliminates duplication of common contract tests across provider packages.
package providertest

import (
	"context"
	"testing"

	"go-mink.dev/encryption"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
