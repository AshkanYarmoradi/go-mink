// Package kms provides an AWS KMS encryption provider for field-level encryption.
// It uses KMS for envelope encryption: GenerateDataKey creates DEKs via KMS,
// and Decrypt unwraps them. Field-level encryption uses the plaintext DEK locally.
package kms

import (
	"context"
	"fmt"
	"sync"

	"github.com/AshkanYarmoradi/go-mink/encryption"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"
)

// KMSClient defines the subset of the KMS API used by the provider.
type KMSClient interface {
	Encrypt(ctx context.Context, params *kms.EncryptInput, optFns ...func(*kms.Options)) (*kms.EncryptOutput, error)
	Decrypt(ctx context.Context, params *kms.DecryptInput, optFns ...func(*kms.Options)) (*kms.DecryptOutput, error)
	GenerateDataKey(ctx context.Context, params *kms.GenerateDataKeyInput, optFns ...func(*kms.Options)) (*kms.GenerateDataKeyOutput, error)
}

// Compile-time interface check.
var _ encryption.Provider = (*Provider)(nil)

// Provider implements encryption.Provider using AWS KMS.
type Provider struct {
	client KMSClient
	mu     sync.RWMutex
	closed bool
}

// Option configures a KMS Provider.
type Option func(*Provider)

// WithKMSClient sets the KMS client.
func WithKMSClient(client KMSClient) Option {
	return func(p *Provider) {
		p.client = client
	}
}

// New creates a new KMS encryption provider.
func New(opts ...Option) *Provider {
	p := &Provider{}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Encrypt encrypts plaintext using the KMS key.
func (p *Provider) Encrypt(ctx context.Context, keyID string, plaintext []byte) ([]byte, error) {
	if err := p.checkClosed(); err != nil {
		return nil, err
	}

	output, err := p.client.Encrypt(ctx, &kms.EncryptInput{
		KeyId:     &keyID,
		Plaintext: plaintext,
	})
	if err != nil {
		return nil, encryption.NewEncryptionError(keyID, "", fmt.Errorf("KMS encrypt: %w", err))
	}
	return output.CiphertextBlob, nil
}

// Decrypt decrypts ciphertext using the KMS key.
func (p *Provider) Decrypt(ctx context.Context, keyID string, ciphertext []byte) ([]byte, error) {
	if err := p.checkClosed(); err != nil {
		return nil, err
	}

	output, err := p.client.Decrypt(ctx, &kms.DecryptInput{
		KeyId:          &keyID,
		CiphertextBlob: ciphertext,
	})
	if err != nil {
		return nil, encryption.NewDecryptionError(keyID, "", fmt.Errorf("KMS decrypt: %w", err))
	}
	return output.Plaintext, nil
}

// GenerateDataKey creates a new DEK using KMS GenerateDataKey API.
// Returns a 256-bit (32-byte) AES key.
func (p *Provider) GenerateDataKey(ctx context.Context, keyID string) (*encryption.DataKey, error) {
	if err := p.checkClosed(); err != nil {
		return nil, err
	}

	output, err := p.client.GenerateDataKey(ctx, &kms.GenerateDataKeyInput{
		KeyId:   &keyID,
		KeySpec: types.DataKeySpecAes256,
	})
	if err != nil {
		return nil, encryption.NewEncryptionError(keyID, "", fmt.Errorf("KMS generate data key: %w", err))
	}

	return &encryption.DataKey{
		Plaintext:  output.Plaintext,
		Ciphertext: output.CiphertextBlob,
		KeyID:      keyID,
	}, nil
}

// DecryptDataKey decrypts an encrypted DEK using the KMS Decrypt API.
func (p *Provider) DecryptDataKey(ctx context.Context, keyID string, encryptedKey []byte) ([]byte, error) {
	if err := p.checkClosed(); err != nil {
		return nil, err
	}

	output, err := p.client.Decrypt(ctx, &kms.DecryptInput{
		KeyId:          &keyID,
		CiphertextBlob: encryptedKey,
	})
	if err != nil {
		return nil, encryption.NewDecryptionError(keyID, "", fmt.Errorf("KMS decrypt data key: %w", err))
	}
	return output.Plaintext, nil
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
		return encryption.NewEncryptionError("", "", fmt.Errorf("kms client not configured: use WithKMSClient option"))
	}
	return nil
}
