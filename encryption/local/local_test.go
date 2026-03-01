package local

import (
	"context"
	"crypto/rand"
	"sync"
	"testing"

	"github.com/AshkanYarmoradi/go-mink/encryption"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testKey(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, 32)
	_, err := rand.Read(key)
	require.NoError(t, err)
	return key
}

func TestProvider_EncryptDecrypt(t *testing.T) {
	key := testKey(t)
	p := New(WithKey("master-1", key))
	defer func() { _ = p.Close() }()

	ctx := context.Background()
	plaintext := []byte("sensitive-email@example.com")

	ciphertext, err := p.Encrypt(ctx, "master-1", plaintext)
	require.NoError(t, err)
	assert.NotEqual(t, plaintext, ciphertext)

	decrypted, err := p.Decrypt(ctx, "master-1", ciphertext)
	require.NoError(t, err)
	assert.Equal(t, plaintext, decrypted)
}

func TestProvider_EncryptDecrypt_EmptyPlaintext(t *testing.T) {
	key := testKey(t)
	p := New(WithKey("master-1", key))
	defer func() { _ = p.Close() }()

	ctx := context.Background()

	ciphertext, err := p.Encrypt(ctx, "master-1", []byte{})
	require.NoError(t, err)

	decrypted, err := p.Decrypt(ctx, "master-1", ciphertext)
	require.NoError(t, err)
	assert.Empty(t, decrypted)
}

func TestProvider_KeyNotFound(t *testing.T) {
	p := New()
	defer func() { _ = p.Close() }()

	ctx := context.Background()

	_, err := p.Encrypt(ctx, "nonexistent", []byte("data"))
	require.Error(t, err)
	assert.ErrorIs(t, err, encryption.ErrKeyNotFound)

	_, err = p.Decrypt(ctx, "nonexistent", []byte("data"))
	require.Error(t, err)
	assert.ErrorIs(t, err, encryption.ErrKeyNotFound)
}

func TestProvider_GenerateDataKey(t *testing.T) {
	key := testKey(t)
	p := New(WithKey("master-1", key))
	defer func() { _ = p.Close() }()

	ctx := context.Background()

	dk, err := p.GenerateDataKey(ctx, "master-1")
	require.NoError(t, err)
	assert.Len(t, dk.Plaintext, 32)
	assert.NotEmpty(t, dk.Ciphertext)
	assert.Equal(t, "master-1", dk.KeyID)

	// Verify round-trip: decrypt the encrypted DEK
	decrypted, err := p.DecryptDataKey(ctx, "master-1", dk.Ciphertext)
	require.NoError(t, err)
	assert.Equal(t, dk.Plaintext, decrypted)
}

func TestProvider_GenerateDataKey_KeyNotFound(t *testing.T) {
	p := New()
	defer func() { _ = p.Close() }()

	_, err := p.GenerateDataKey(context.Background(), "nonexistent")
	require.Error(t, err)
	assert.ErrorIs(t, err, encryption.ErrKeyNotFound)
}

func TestProvider_DecryptDataKey_KeyNotFound(t *testing.T) {
	p := New()
	defer func() { _ = p.Close() }()

	_, err := p.DecryptDataKey(context.Background(), "nonexistent", []byte("data"))
	require.Error(t, err)
	assert.ErrorIs(t, err, encryption.ErrKeyNotFound)
}

func TestProvider_RevokeKey(t *testing.T) {
	key := testKey(t)
	p := New(WithKey("master-1", key))
	defer func() { _ = p.Close() }()

	ctx := context.Background()

	// Encrypt some data
	ciphertext, err := p.Encrypt(ctx, "master-1", []byte("sensitive"))
	require.NoError(t, err)

	// Revoke the key
	err = p.RevokeKey("master-1")
	require.NoError(t, err)

	// Cannot decrypt anymore
	_, err = p.Decrypt(ctx, "master-1", ciphertext)
	require.Error(t, err)
	assert.ErrorIs(t, err, encryption.ErrKeyRevoked)

	// Cannot encrypt anymore
	_, err = p.Encrypt(ctx, "master-1", []byte("data"))
	require.Error(t, err)
	assert.ErrorIs(t, err, encryption.ErrKeyRevoked)

	// Cannot generate data key
	_, err = p.GenerateDataKey(ctx, "master-1")
	require.Error(t, err)
	assert.ErrorIs(t, err, encryption.ErrKeyRevoked)
}

func TestProvider_RevokeKey_NotFound(t *testing.T) {
	p := New()
	defer func() { _ = p.Close() }()

	err := p.RevokeKey("nonexistent")
	require.Error(t, err)
	assert.ErrorIs(t, err, encryption.ErrKeyNotFound)
}

func TestProvider_AddKey(t *testing.T) {
	p := New()
	defer func() { _ = p.Close() }()

	key := testKey(t)
	err := p.AddKey("new-key", key)
	require.NoError(t, err)

	ctx := context.Background()
	ciphertext, err := p.Encrypt(ctx, "new-key", []byte("data"))
	require.NoError(t, err)

	decrypted, err := p.Decrypt(ctx, "new-key", ciphertext)
	require.NoError(t, err)
	assert.Equal(t, []byte("data"), decrypted)
}

func TestProvider_AddKey_InvalidLength(t *testing.T) {
	p := New()
	defer func() { _ = p.Close() }()

	err := p.AddKey("bad-key", []byte("too-short"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "32 bytes")
}

func TestProvider_Close(t *testing.T) {
	key := testKey(t)
	p := New(WithKey("master-1", key))

	err := p.Close()
	require.NoError(t, err)

	// Double close is safe
	err = p.Close()
	require.NoError(t, err)

	ctx := context.Background()

	// Operations fail after close
	_, err = p.Encrypt(ctx, "master-1", []byte("data"))
	require.Error(t, err)
	assert.ErrorIs(t, err, encryption.ErrProviderClosed)

	_, err = p.Decrypt(ctx, "master-1", []byte("data"))
	require.Error(t, err)
	assert.ErrorIs(t, err, encryption.ErrProviderClosed)

	_, err = p.GenerateDataKey(ctx, "master-1")
	require.Error(t, err)
	assert.ErrorIs(t, err, encryption.ErrProviderClosed)

	err = p.AddKey("key", testKey(t))
	require.Error(t, err)
	assert.ErrorIs(t, err, encryption.ErrProviderClosed)

	err = p.RevokeKey("master-1")
	require.Error(t, err)
	assert.ErrorIs(t, err, encryption.ErrProviderClosed)
}

func TestProvider_Decrypt_InvalidCiphertext(t *testing.T) {
	key := testKey(t)
	p := New(WithKey("master-1", key))
	defer func() { _ = p.Close() }()

	ctx := context.Background()

	// Too short for nonce
	_, err := p.Decrypt(ctx, "master-1", []byte("short"))
	require.Error(t, err)

	// Invalid ciphertext (correct nonce length but garbage data)
	garbage := make([]byte, 100)
	_, _ = rand.Read(garbage)
	_, err = p.Decrypt(ctx, "master-1", garbage)
	require.Error(t, err)
}

func TestProvider_ConcurrentAccess(t *testing.T) {
	key := testKey(t)
	p := New(WithKey("master-1", key))
	defer func() { _ = p.Close() }()

	ctx := context.Background()
	var wg sync.WaitGroup

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ciphertext, err := p.Encrypt(ctx, "master-1", []byte("concurrent-data"))
			if err != nil {
				return
			}
			_, _ = p.Decrypt(ctx, "master-1", ciphertext)
		}()
	}

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dk, err := p.GenerateDataKey(ctx, "master-1")
			if err != nil {
				return
			}
			_, _ = p.DecryptDataKey(ctx, "master-1", dk.Ciphertext)
			encryption.ClearBytes(dk.Plaintext)
		}()
	}

	wg.Wait()
}

func TestClearBytes(t *testing.T) {
	data := []byte{1, 2, 3, 4, 5}
	encryption.ClearBytes(data)
	for _, b := range data {
		assert.Equal(t, byte(0), b)
	}
}

func TestProvider_KeyIsolation(t *testing.T) {
	key1 := testKey(t)
	key2 := testKey(t)
	p := New(WithKey("key-1", key1), WithKey("key-2", key2))
	defer func() { _ = p.Close() }()

	ctx := context.Background()

	// Encrypt with key-1
	ciphertext, err := p.Encrypt(ctx, "key-1", []byte("data"))
	require.NoError(t, err)

	// Cannot decrypt with key-2
	_, err = p.Decrypt(ctx, "key-2", ciphertext)
	require.Error(t, err)

	// Can decrypt with key-1
	decrypted, err := p.Decrypt(ctx, "key-1", ciphertext)
	require.NoError(t, err)
	assert.Equal(t, []byte("data"), decrypted)
}
