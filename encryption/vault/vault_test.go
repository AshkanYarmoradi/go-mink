package vault

import (
	"context"
	"fmt"
	"testing"

	"github.com/AshkanYarmoradi/go-mink/encryption"
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

func TestProvider_EncryptDecrypt(t *testing.T) {
	mock := &mockVaultClient{}
	p := New(WithVaultClient(mock))
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
	mock := &mockVaultClient{}
	p := New(WithVaultClient(mock))
	defer func() { _ = p.Close() }()

	ctx := context.Background()
	dk, err := p.GenerateDataKey(ctx, "master-key")
	require.NoError(t, err)

	assert.Len(t, dk.Plaintext, 32)
	assert.NotEmpty(t, dk.Ciphertext)
	assert.Equal(t, "master-key", dk.KeyID)

	// Verify we can round-trip the DEK
	decrypted, err := p.DecryptDataKey(ctx, "master-key", dk.Ciphertext)
	require.NoError(t, err)
	assert.Equal(t, dk.Plaintext, decrypted)
}

func TestProvider_DecryptDataKey(t *testing.T) {
	mock := &mockVaultClient{}
	p := New(WithVaultClient(mock))
	defer func() { _ = p.Close() }()

	encrypted := []byte("vault:dek-plaintext")
	plaintext, err := p.DecryptDataKey(context.Background(), "key-1", encrypted)
	require.NoError(t, err)
	assert.Equal(t, []byte("dek-plaintext"), plaintext)
}

func TestProvider_EncryptError(t *testing.T) {
	mock := &mockVaultClient{
		encryptFunc: func(ctx context.Context, keyName string, plaintext []byte) ([]byte, error) {
			return nil, fmt.Errorf("vault sealed")
		},
	}
	p := New(WithVaultClient(mock))
	defer func() { _ = p.Close() }()

	_, err := p.Encrypt(context.Background(), "key-1", []byte("data"))
	require.Error(t, err)
	assert.ErrorIs(t, err, encryption.ErrEncryptionFailed)
	assert.Contains(t, err.Error(), "vault sealed")
}

func TestProvider_DecryptError(t *testing.T) {
	mock := &mockVaultClient{
		decryptFunc: func(ctx context.Context, keyName string, ciphertext []byte) ([]byte, error) {
			return nil, fmt.Errorf("key not found")
		},
	}
	p := New(WithVaultClient(mock))
	defer func() { _ = p.Close() }()

	_, err := p.Decrypt(context.Background(), "key-1", []byte("data"))
	require.Error(t, err)
	assert.ErrorIs(t, err, encryption.ErrDecryptionFailed)
}

func TestProvider_GenerateDataKeyError(t *testing.T) {
	mock := &mockVaultClient{
		encryptFunc: func(ctx context.Context, keyName string, plaintext []byte) ([]byte, error) {
			return nil, fmt.Errorf("vault error")
		},
	}
	p := New(WithVaultClient(mock))
	defer func() { _ = p.Close() }()

	_, err := p.GenerateDataKey(context.Background(), "key-1")
	require.Error(t, err)
	assert.ErrorIs(t, err, encryption.ErrEncryptionFailed)
}

func TestProvider_DecryptDataKeyError(t *testing.T) {
	mock := &mockVaultClient{
		decryptFunc: func(ctx context.Context, keyName string, ciphertext []byte) ([]byte, error) {
			return nil, fmt.Errorf("key disabled")
		},
	}
	p := New(WithVaultClient(mock))
	defer func() { _ = p.Close() }()

	_, err := p.DecryptDataKey(context.Background(), "key-1", []byte("encrypted"))
	require.Error(t, err)
	assert.ErrorIs(t, err, encryption.ErrDecryptionFailed)
}

func TestProvider_Close(t *testing.T) {
	mock := &mockVaultClient{}
	p := New(WithVaultClient(mock))

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
