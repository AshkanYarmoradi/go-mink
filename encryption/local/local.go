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
	"time"

	"go-mink.dev/encryption"
)

// Compile-time interface checks.
var (
	_ encryption.Provider             = (*Provider)(nil)
	_ encryption.Revocable            = (*Provider)(nil)
	_ encryption.RecoverableRevocable = (*Provider)(nil)
	_ encryption.StatefulRevocable    = (*Provider)(nil)
)

// Provider is an in-memory AES-256-GCM encryption provider for testing.
// Keys are stored in memory and never persisted.
type Provider struct {
	mu          sync.RWMutex
	keys        map[string][]byte    // keyID → 32-byte AES key
	revoked     map[string]bool      // hard (permanent) revocations
	softRevoked map[string]time.Time // soft revocations → grace-window expiry
	closed      bool
}

// Option configures a local Provider.
type Option func(*Provider) error

// WithKey pre-loads a master key into the provider.
// The key must be exactly 32 bytes (AES-256).
//
// WithKey is normally applied at construction time via New, but it mirrors the
// guards of AddKey so it remains safe if applied to an already-constructed or
// closed provider: it takes the write lock, rejects use after Close, and
// initializes the key map if it has not been allocated yet.
func WithKey(keyID string, key []byte) Option {
	return func(p *Provider) error {
		if len(key) != 32 {
			return fmt.Errorf("mink/local: key %q must be 32 bytes, got %d", keyID, len(key))
		}

		p.mu.Lock()
		defer p.mu.Unlock()

		if p.closed {
			return encryption.ErrProviderClosed
		}

		if p.keys == nil {
			p.keys = make(map[string][]byte)
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
		keys:        make(map[string][]byte),
		revoked:     make(map[string]bool),
		softRevoked: make(map[string]time.Time),
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
// can never be decrypted. It implements encryption.Revocable and is idempotent —
// revoking an already-revoked key is a no-op success.
func (p *Provider) RevokeKey(keyID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return encryption.ErrProviderClosed
	}

	// Idempotent: an already-revoked key has no material left to clear.
	if p.revoked[keyID] {
		return nil
	}

	key, ok := p.keys[keyID]
	if !ok {
		return encryption.NewKeyNotFoundError(keyID)
	}

	// Zero out key material before removing
	encryption.ClearBytes(key)
	delete(p.keys, keyID)
	delete(p.softRevoked, keyID)
	p.revoked[keyID] = true
	return nil
}

// IsRevoked reports whether keyID has been revoked — soft or hard.
// It implements encryption.Revocable. Callers that need to distinguish a
// still-recoverable soft-revocation from a permanent shred use RevocationState.
func (p *Provider) IsRevoked(keyID string) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return false, encryption.ErrProviderClosed
	}
	state := p.stateLocked(keyID)
	return state == encryption.Revoked || state == encryption.SoftRevoked, nil
}

// RevocationState reports keyID's fine-grained revocation state, first promoting an
// elapsed soft-revocation to a permanent shred. Implements encryption.StatefulRevocable.
func (p *Provider) RevocationState(keyID string) (encryption.RevocationState, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return encryption.NotRevoked, encryption.ErrProviderClosed
	}
	return p.stateLocked(keyID), nil
}

// promoteIfExpiredLocked converts an elapsed soft-revocation into a permanent
// crypto-shred: it zeroes and removes the key material so "permanent after the grace
// window" actually destroys the key, not merely gates decryption. The caller MUST
// hold the write lock. No background reaper runs — promotion is lazy, on next access.
func (p *Provider) promoteIfExpiredLocked(keyID string) {
	expiry, ok := p.softRevoked[keyID]
	if !ok || time.Now().Before(expiry) {
		return
	}
	if key, ok := p.keys[keyID]; ok {
		encryption.ClearBytes(key)
		delete(p.keys, keyID)
	}
	delete(p.softRevoked, keyID)
	p.revoked[keyID] = true
}

// stateLocked returns keyID's revocation state, promoting an expired soft-revocation
// first. The caller MUST hold the write lock.
func (p *Provider) stateLocked(keyID string) encryption.RevocationState {
	p.promoteIfExpiredLocked(keyID)
	if p.revoked[keyID] {
		return encryption.Revoked
	}
	if _, soft := p.softRevoked[keyID]; soft {
		return encryption.SoftRevoked
	}
	return encryption.NotRevoked
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

// getKey retrieves a master key, checking for revocation and closure. It takes the
// write lock because an elapsed soft-revocation is promoted to a permanent shred on
// access (stateLocked), which mutates key material.
func (p *Provider) getKey(keyID string) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return nil, encryption.ErrProviderClosed
	}

	switch p.stateLocked(keyID) {
	case encryption.Revoked, encryption.SoftRevoked:
		return nil, encryption.NewKeyRevokedError(keyID)
	}

	key, ok := p.keys[keyID]
	if !ok {
		return nil, encryption.NewKeyNotFoundError(keyID)
	}
	return key, nil
}

// SoftRevokeKey blocks decryption under keyID but allows UnrevokeKey to restore it
// until graceWindow elapses, after which it is permanent. Implements
// encryption.RecoverableRevocable.
func (p *Provider) SoftRevokeKey(keyID string, graceWindow time.Duration) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return encryption.ErrProviderClosed
	}
	p.promoteIfExpiredLocked(keyID)
	if p.revoked[keyID] {
		return nil // already permanently revoked
	}
	if _, ok := p.keys[keyID]; !ok {
		return encryption.NewKeyNotFoundError(keyID)
	}
	p.softRevoked[keyID] = time.Now().Add(graceWindow)
	return nil
}

// UnrevokeKey restores a soft-revoked key if still within its grace window.
// Implements encryption.RecoverableRevocable.
func (p *Provider) UnrevokeKey(keyID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return encryption.ErrProviderClosed
	}
	// Promote first so a key whose window has already elapsed is reported as a
	// permanent revocation rather than silently "restored".
	p.promoteIfExpiredLocked(keyID)
	if p.revoked[keyID] {
		return fmt.Errorf("mink/local: grace window elapsed for key %q; revocation is permanent", keyID)
	}
	if _, ok := p.softRevoked[keyID]; !ok {
		return nil // not soft-revoked: nothing to undo
	}
	delete(p.softRevoked, keyID)
	return nil
}
