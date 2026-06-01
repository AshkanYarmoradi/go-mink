package providertest_test

// This file exercises the shared providertest contract helpers against the
// local AES-256-GCM provider. The local provider needs no credentials, so it
// runs everywhere (including CI) and gives the otherwise-untested helpers real
// coverage. Credential-gated KMS/Vault contract runs live in their respective
// packages.
//
// Note: only the helpers whose asserted sentinel actually holds for the local
// provider are exercised here. The local provider surfaces a missing key as
// ErrKeyNotFound (not ErrEncryptionFailed/ErrDecryptionFailed), so the
// Encrypt/GenerateDataKey error helpers — which target injected-failure mocks —
// are covered by the KMS/Vault packages instead. AssertDecryptDataKeyError does
// hold for local: with a valid key, a malformed DEK ciphertext fails AES-GCM
// and is wrapped as ErrDecryptionFailed.

import (
	"context"
	"crypto/rand"
	"testing"

	"github.com/stretchr/testify/require"
	"go-mink.dev/encryption/local"
	"go-mink.dev/encryption/providertest"
)

func newLocalProvider(t *testing.T, keyID string) *local.Provider {
	t.Helper()
	key := make([]byte, 32)
	_, err := rand.Read(key)
	require.NoError(t, err)
	p, err := local.New(local.WithKey(keyID, key))
	require.NoError(t, err)
	return p
}

// TestContract_LocalProvider runs the contract helpers that hold for the local
// provider, giving the shared helper suite real execution coverage.
func TestContract_LocalProvider(t *testing.T) {
	const keyID = "contract-key"

	t.Run("GenerateDataKeyBasics", func(t *testing.T) {
		p := newLocalProvider(t, keyID)
		defer func() { _ = p.Close() }()

		dk := providertest.AssertGenerateDataKeyBasics(t, p, keyID)

		// Round-trip the DEK to confirm the helper handed back a usable key.
		plaintext, err := p.DecryptDataKey(context.Background(), keyID, dk.Ciphertext)
		require.NoError(t, err)
		require.Equal(t, dk.Plaintext, plaintext)
	})

	// A valid key with a malformed DEK ciphertext exercises the decrypt-error
	// helper: AES-GCM rejects the bad ciphertext and the local provider wraps
	// it as ErrDecryptionFailed.
	t.Run("DecryptDataKeyError", func(t *testing.T) {
		p := newLocalProvider(t, keyID)
		defer func() { _ = p.Close() }()

		providertest.AssertDecryptDataKeyError(t, p, keyID)
	})

	// CloseBlocksAllOperations closes the provider itself, so it needs its own
	// instance separate from the success-path checks above.
	t.Run("CloseBlocksAllOperations", func(t *testing.T) {
		p := newLocalProvider(t, keyID)
		providertest.AssertCloseBlocksAllOperations(t, p)
	})
}
