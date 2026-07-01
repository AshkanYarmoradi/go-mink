// Package vault provides a HashiCorp Vault Transit encryption provider
// for field-level encryption. It uses the Vault Transit engine for key management
// while generating DEKs locally for envelope encryption.
package vault

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"sync"

	"go-mink.dev/encryption"
)

// VaultClient defines the minimal interface for Vault Transit operations.
// Users inject their own implementation (e.g., wrapping the official Vault SDK).
type VaultClient interface {
	// Encrypt encrypts plaintext using the named Transit key.
	Encrypt(ctx context.Context, keyName string, plaintext []byte) (ciphertext []byte, err error)

	// Decrypt decrypts ciphertext using the named Transit key.
	Decrypt(ctx context.Context, keyName string, ciphertext []byte) (plaintext []byte, err error)
}

// VaultRevocationClient is an OPTIONAL extension of VaultClient. When the
// injected client also implements it, the provider implements
// encryption.Revocable and supports crypto-shredding (GDPR erasure) by deleting
// the named Transit key so its ciphertext can never be decrypted again.
type VaultRevocationClient interface {
	// DeleteKey deletes the named Transit key (the key must allow deletion).
	DeleteKey(ctx context.Context, keyName string) error
	// KeyExists reports whether the named Transit key still exists.
	KeyExists(ctx context.Context, keyName string) (bool, error)
}

// Compile-time interface checks. The Revocable assertion holds at the type level;
// RevokeKey/IsRevoked return ErrRevocationUnsupported unless the injected client
// also implements VaultRevocationClient.
var (
	_ encryption.Provider  = (*Provider)(nil)
	_ encryption.Revocable = (*Provider)(nil)
)

// Provider implements encryption.Provider using HashiCorp Vault Transit.
type Provider struct {
	client VaultClient
	mu     sync.RWMutex
	closed bool
}

// Option configures a Vault Provider.
type Option func(*Provider)

// WithVaultClient sets the Vault Transit client.
func WithVaultClient(client VaultClient) Option {
	return func(p *Provider) {
		p.client = client
	}
}

// New creates a new Vault Transit encryption provider.
func New(opts ...Option) *Provider {
	p := &Provider{}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Encrypt encrypts plaintext using the Vault Transit key.
func (p *Provider) Encrypt(ctx context.Context, keyID string, plaintext []byte) ([]byte, error) {
	if err := p.checkClosed(); err != nil {
		return nil, err
	}

	ciphertext, err := p.client.Encrypt(ctx, keyID, plaintext)
	if err != nil {
		return nil, encryption.NewEncryptionError(keyID, "", fmt.Errorf("vault encrypt: %w", err))
	}
	return ciphertext, nil
}

// Decrypt decrypts ciphertext using the Vault Transit key.
func (p *Provider) Decrypt(ctx context.Context, keyID string, ciphertext []byte) ([]byte, error) {
	if err := p.checkClosed(); err != nil {
		return nil, err
	}

	plaintext, err := p.client.Decrypt(ctx, keyID, ciphertext)
	if err != nil {
		return nil, encryption.NewDecryptionError(keyID, "", fmt.Errorf("vault decrypt: %w", err))
	}
	return plaintext, nil
}

// GenerateDataKey creates a new random 32-byte DEK and encrypts it via Vault Transit.
// Unlike KMS, Vault Transit doesn't have a native GenerateDataKey API, so we
// generate the DEK locally and encrypt it with Vault.
func (p *Provider) GenerateDataKey(ctx context.Context, keyID string) (*encryption.DataKey, error) {
	if err := p.checkClosed(); err != nil {
		return nil, err
	}

	// Generate random 32-byte DEK locally
	dek := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, dek); err != nil {
		return nil, encryption.NewEncryptionError(keyID, "", fmt.Errorf("failed to generate DEK: %w", err))
	}

	// Encrypt DEK with Vault Transit
	encryptedDEK, err := p.client.Encrypt(ctx, keyID, dek)
	if err != nil {
		encryption.ClearBytes(dek)
		return nil, encryption.NewEncryptionError(keyID, "", fmt.Errorf("vault encrypt DEK: %w", err))
	}

	return &encryption.DataKey{
		Plaintext:  dek,
		Ciphertext: encryptedDEK,
		KeyID:      keyID,
	}, nil
}

// DecryptDataKey decrypts a previously encrypted DEK using Vault Transit.
func (p *Provider) DecryptDataKey(ctx context.Context, keyID string, encryptedKey []byte) ([]byte, error) {
	if err := p.checkClosed(); err != nil {
		return nil, err
	}

	plaintext, err := p.client.Decrypt(ctx, keyID, encryptedKey)
	if err != nil {
		return nil, encryption.NewDecryptionError(keyID, "", fmt.Errorf("vault decrypt DEK: %w", err))
	}
	return plaintext, nil
}

// RevokeKey crypto-shreds keyID by deleting its Vault Transit key. It implements
// encryption.Revocable. It requires the injected client to implement
// VaultRevocationClient, otherwise it returns ErrRevocationUnsupported. It is
// idempotent: a key that no longer exists returns nil.
func (p *Provider) RevokeKey(keyID string) error {
	rc, err := p.revocationClient()
	if err != nil {
		return err
	}
	exists, err := rc.KeyExists(context.Background(), keyID)
	if err != nil {
		return encryption.NewEncryptionError(keyID, "", fmt.Errorf("vault key exists: %w", err))
	}
	if !exists {
		return nil // idempotent: already deleted
	}
	if err := rc.DeleteKey(context.Background(), keyID); err != nil {
		return encryption.NewEncryptionError(keyID, "", fmt.Errorf("vault delete key: %w", err))
	}
	return nil
}

// IsRevoked reports whether keyID's Transit key has been deleted.
// It implements encryption.Revocable.
func (p *Provider) IsRevoked(keyID string) (bool, error) {
	rc, err := p.revocationClient()
	if err != nil {
		return false, err
	}
	exists, err := rc.KeyExists(context.Background(), keyID)
	if err != nil {
		return false, encryption.NewDecryptionError(keyID, "", fmt.Errorf("vault key exists: %w", err))
	}
	return !exists, nil
}

// revocationClient returns the injected client as a VaultRevocationClient, or
// ErrRevocationUnsupported if it does not support revocation.
func (p *Provider) revocationClient() (VaultRevocationClient, error) {
	if err := p.checkClosed(); err != nil {
		return nil, err
	}
	rc, ok := p.client.(VaultRevocationClient)
	if !ok {
		return nil, encryption.ErrRevocationUnsupported
	}
	return rc, nil
}

// Close marks the provider as closed.
func (p *Provider) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	return nil
}

func (p *Provider) checkClosed() error {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.closed {
		return encryption.ErrProviderClosed
	}
	if p.client == nil {
		return encryption.NewEncryptionError("", "", fmt.Errorf("vault client not configured: use WithVaultClient option"))
	}
	return nil
}
