package encryption

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"io"
)

// AESGCMEncrypt encrypts plaintext using AES-256-GCM with additional authenticated data.
// The aad parameter binds the ciphertext to its context (e.g., field path, key ID),
// preventing ciphertext from being moved between contexts undetected.
// Output format: nonce (12 bytes) || ciphertext+tag
func AESGCMEncrypt(key, plaintext, aad []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("mink: failed to create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("mink: failed to create GCM: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("mink: failed to generate nonce: %w", err)
	}

	return gcm.Seal(nonce, nonce, plaintext, aad), nil
}

// AESGCMDecrypt decrypts ciphertext produced by AESGCMEncrypt.
// The aad parameter must match the value used during encryption.
func AESGCMDecrypt(key, ciphertext, aad []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("mink: failed to create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("mink: failed to create GCM: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, fmt.Errorf("mink: ciphertext too short")
	}

	nonce, body := ciphertext[:nonceSize], ciphertext[nonceSize:]
	return gcm.Open(nil, nonce, body, aad)
}
