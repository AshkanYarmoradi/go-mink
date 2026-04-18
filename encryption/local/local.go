// Package local provides an in-memory AES-256-GCM encryption provider for testing.
// It stores master keys in memory and supports key revocation for crypto-shredding simulation.
//
// This provider should NOT be used in production. Use the kms or vault providers instead.
package local

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"sync"

	"go-mink.dev/encryption"
)

// Compile-time interface check.
var _ encryption.Provider = (*Provider)(nil)

// Provider is an in-memory AES-256-GCM encryption provider for testing.
// Keys are stored in memory and never persisted.
type Provider struct {
	mu      sync.RWMutex
	keys    map[string][]byte // keyID → 32-byte AES key
	revoked map[string]bool
	closed  bool
}

// Option configures a local Provider.
type Option func(*Provider) error

// WithKey pre-loads a master key into the provider.
// The key must be exactly 32 bytes (AES-256).
func WithKey(keyID string, key []byte) Option {
	return func(p *Provider) error {
		if len(key) != 32 {
			return fmt.Errorf("mink/local: key %q must be 32 bytes, got %d", keyID, len(key))
		}
		keyCopy := make([]byte, len(key))
		copy(keyCopy, key)
		p.keys[keyID] = keyCopy
		return nil
	}
}

// New creates a new local encryption provider.
func New(opts ...Option) (*Provider, error) {
	p := &Provider{
		keys:    make(map[string][]byte),
		revoked: make(map[string]bool),
	}
	for _, opt := range opts {
		if err := opt(p); err != nil {
			return nil, err
		}
	}
	return p, nil
}

// AddKey adds a master key to the provider.
// The key must be exactly 32 bytes (AES-256).
func (p *Provider) AddKey(keyID string, key []byte) error {
	if len(key) != 32 {
		return fmt.Errorf("mink: key must be 32 bytes, got %d", len(key))
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return encryption.ErrProviderClosed
	}

	keyCopy := make([]byte, len(key))
	copy(keyCopy, key)
	p.keys[keyID] = keyCopy
	return nil
}

// RevokeKey marks a key as revoked and removes the key material.
// This simulates crypto-shredding: once revoked, data encrypted with this key
// can never be decrypted.
func (p *Provider) RevokeKey(keyID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return encryption.ErrProviderClosed
	}

	key, ok := p.keys[keyID]
	if !ok {
		return encryption.NewKeyNotFoundError(keyID)
	}

	// Zero out key material before removing
	encryption.ClearBytes(key)
	delete(p.keys, keyID)
	p.revoked[keyID] = true
	return nil
}

// Encrypt encrypts plaintext using AES-256-GCM with the specified master key.
func (p *Provider) Encrypt(_ context.Context, keyID string, plaintext []byte) ([]byte, error) {
	key, err := p.getKey(keyID)
	if err != nil {
		return nil, err
	}
	return encryption.AESGCMEncrypt(key, plaintext, []byte(keyID))
}

// Decrypt decrypts ciphertext using AES-256-GCM with the specified master key.
func (p *Provider) Decrypt(_ context.Context, keyID string, ciphertext []byte) ([]byte, error) {
	key, err := p.getKey(keyID)
	if err != nil {
		return nil, err
	}
	return encryption.AESGCMDecrypt(key, ciphertext, []byte(keyID))
}

// GenerateDataKey creates a new random 32-byte DEK and encrypts it with the master key.
func (p *Provider) GenerateDataKey(_ context.Context, keyID string) (*encryption.DataKey, error) {
	key, err := p.getKey(keyID)
	if err != nil {
		return nil, err
	}

	// Generate random 32-byte DEK
	dek := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, dek); err != nil {
		return nil, encryption.NewEncryptionError(keyID, "", fmt.Errorf("failed to generate DEK: %w", err))
	}

	// Encrypt DEK with master key
	encryptedDEK, err := encryption.AESGCMEncrypt(key, dek, []byte(keyID))
	if err != nil {
		encryption.ClearBytes(dek)
		return nil, encryption.NewEncryptionError(keyID, "", fmt.Errorf("failed to encrypt DEK: %w", err))
	}

	return &encryption.DataKey{
		Plaintext:  dek,
		Ciphertext: encryptedDEK,
		KeyID:      keyID,
	}, nil
}

// DecryptDataKey decrypts a previously encrypted DEK using the master key.
func (p *Provider) DecryptDataKey(_ context.Context, keyID string, encryptedKey []byte) ([]byte, error) {
	key, err := p.getKey(keyID)
	if err != nil {
		return nil, err
	}

	plaintext, err := encryption.AESGCMDecrypt(key, encryptedKey, []byte(keyID))
	if err != nil {
		return nil, encryption.NewDecryptionError(keyID, "", fmt.Errorf("failed to decrypt DEK: %w", err))
	}
	return plaintext, nil
}

// Close releases all key material.
func (p *Provider) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return nil
	}

	// Zero out all key material
	for _, key := range p.keys {
		encryption.ClearBytes(key)
	}
	p.keys = nil
	p.revoked = nil
	p.closed = true
	return nil
}

// getKey retrieves a master key, checking for revocation and closure.
func (p *Provider) getKey(keyID string) ([]byte, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if p.closed {
		return nil, encryption.ErrProviderClosed
	}

	if p.revoked[keyID] {
		return nil, encryption.NewKeyRevokedError(keyID)
	}

	key, ok := p.keys[keyID]
	if !ok {
		return nil, encryption.NewKeyNotFoundError(keyID)
	}
	return key, nil
}
