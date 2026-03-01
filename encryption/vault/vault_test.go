package vault

import (
	"context"
	"fmt"
	"testing"

	"github.com/AshkanYarmoradi/go-mink/encryption/providertest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockVaultClient implements VaultClient for testing.
type mockVaultClient struct {
	encryptFunc func(ctx context.Context, keyName string, plaintext []byte) ([]byte, error)
	decryptFunc func(ctx context.Context, keyName string, ciphertext []byte) ([]byte, error)
}

func (m *mockVaultClient) Encrypt(ctx context.Context, keyName string, plaintext []byte) ([]byte, error) {
	if m.encryptFunc != nil {
		return m.encryptFunc(ctx, keyName, plaintext)
	}
	return append([]byte("vault:"), plaintext...), nil
}

func (m *mockVaultClient) Decrypt(ctx context.Context, keyName string, ciphertext []byte) ([]byte, error) {
	if m.decryptFunc != nil {
		return m.decryptFunc(ctx, keyName, ciphertext)
	}
	if len(ciphertext) > 6 && string(ciphertext[:6]) == "vault:" {
		return ciphertext[6:], nil
	}
	return ciphertext, nil
}

func newTestProvider(mock *mockVaultClient) *Provider {
	return New(WithVaultClient(mock))
}

func TestProvider_EncryptDecrypt(t *testing.T) {
	p := newTestProvider(&mockVaultClient{})
	defer func() { _ = p.Close() }()

	ctx := context.Background()
	plaintext := []byte("sensitive data")

	ciphertext, err := p.Encrypt(ctx, "my-key", plaintext)
	require.NoError(t, err)
	assert.Equal(t, []byte("vault:sensitive data"), ciphertext)

	decrypted, err := p.Decrypt(ctx, "my-key", ciphertext)
	require.NoError(t, err)
	assert.Equal(t, plaintext, decrypted)
}

func TestProvider_GenerateDataKey(t *testing.T) {
	p := newTestProvider(&mockVaultClient{})
	defer func() { _ = p.Close() }()

	dk := providertest.AssertGenerateDataKeyBasics(t, p, "master-key")

	// Verify we can round-trip the DEK
	decrypted, err := p.DecryptDataKey(context.Background(), "master-key", dk.Ciphertext)
	require.NoError(t, err)
	assert.Equal(t, dk.Plaintext, decrypted)
}

func TestProvider_DecryptDataKey(t *testing.T) {
	p := newTestProvider(&mockVaultClient{})
	defer func() { _ = p.Close() }()

	encrypted := []byte("vault:dek-plaintext")
	plaintext, err := p.DecryptDataKey(context.Background(), "key-1", encrypted)
	require.NoError(t, err)
	assert.Equal(t, []byte("dek-plaintext"), plaintext)
}

func TestProvider_EncryptError(t *testing.T) {
	p := newTestProvider(&mockVaultClient{
		encryptFunc: func(_ context.Context, _ string, _ []byte) ([]byte, error) {
			return nil, fmt.Errorf("vault sealed")
		},
	})
	defer func() { _ = p.Close() }()

	providertest.AssertEncryptError(t, p, "key-1")
}

func TestProvider_DecryptError(t *testing.T) {
	p := newTestProvider(&mockVaultClient{
		decryptFunc: func(_ context.Context, _ string, _ []byte) ([]byte, error) {
			return nil, fmt.Errorf("key not found")
		},
	})
	defer func() { _ = p.Close() }()

	providertest.AssertDecryptError(t, p, "key-1")
}

func TestProvider_GenerateDataKeyError(t *testing.T) {
	p := newTestProvider(&mockVaultClient{
		encryptFunc: func(_ context.Context, _ string, _ []byte) ([]byte, error) {
			return nil, fmt.Errorf("vault error")
		},
	})
	defer func() { _ = p.Close() }()

	providertest.AssertGenerateDataKeyError(t, p, "key-1")
}

func TestProvider_DecryptDataKeyError(t *testing.T) {
	p := newTestProvider(&mockVaultClient{
		decryptFunc: func(_ context.Context, _ string, _ []byte) ([]byte, error) {
			return nil, fmt.Errorf("key disabled")
		},
	})
	defer func() { _ = p.Close() }()

	providertest.AssertDecryptDataKeyError(t, p, "key-1")
}

func TestProvider_Close(t *testing.T) {
	p := newTestProvider(&mockVaultClient{})
	providertest.AssertCloseBlocksAllOperations(t, p)
}
