// Package kms provides an AWS KMS encryption provider for field-level encryption.
// It uses KMS for envelope encryption: GenerateDataKey creates DEKs via KMS,
// and Decrypt unwraps them. Field-level encryption uses the plaintext DEK locally.
package kms

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"
	"go-mink.dev/encryption"
)

// KMSClient defines the subset of the KMS API used by the provider.
type KMSClient interface {
	Encrypt(ctx context.Context, params *kms.EncryptInput, optFns ...func(*kms.Options)) (*kms.EncryptOutput, error)
	Decrypt(ctx context.Context, params *kms.DecryptInput, optFns ...func(*kms.Options)) (*kms.DecryptOutput, error)
	GenerateDataKey(ctx context.Context, params *kms.GenerateDataKeyInput, optFns ...func(*kms.Options)) (*kms.GenerateDataKeyOutput, error)
}

// KMSRevocationClient is an OPTIONAL extension of KMSClient. When the injected
// client also implements it, the provider implements encryption.Revocable and
// supports crypto-shredding (GDPR erasure) by scheduling deletion of the customer
// master key (CMK). A CMK pending deletion is immediately unusable for decrypt,
// and AWS destroys the key material permanently after the pending window.
type KMSRevocationClient interface {
	ScheduleKeyDeletion(ctx context.Context, params *kms.ScheduleKeyDeletionInput, optFns ...func(*kms.Options)) (*kms.ScheduleKeyDeletionOutput, error)
	DescribeKey(ctx context.Context, params *kms.DescribeKeyInput, optFns ...func(*kms.Options)) (*kms.DescribeKeyOutput, error)
}

// Compile-time interface checks. The Revocable assertion holds at the type level;
// RevokeKey/IsRevoked return ErrRevocationUnsupported unless the injected client
// also implements KMSRevocationClient.
var (
	_ encryption.Provider  = (*Provider)(nil)
	_ encryption.Revocable = (*Provider)(nil)
)

// Provider implements encryption.Provider using AWS KMS.
type Provider struct {
	client            KMSClient
	mu                sync.RWMutex
	closed            bool
	pendingWindowDays int32
}

// Option configures a KMS Provider.
type Option func(*Provider)

// WithKMSClient sets the KMS client.
func WithKMSClient(client KMSClient) Option {
	return func(p *Provider) {
		p.client = client
	}
}

// WithPendingDeletionWindow sets the AWS KMS pending-deletion window in days used by
// RevokeKey. AWS only accepts 7–30 days, so an out-of-range value is clamped into that
// range — a misconfigured window cannot fail at RevokeKey time (the erasure moment).
// Defaults to 7 (the AWS minimum) when unset.
func WithPendingDeletionWindow(days int32) Option {
	return func(p *Provider) {
		if days < 7 {
			days = 7
		} else if days > 30 {
			days = 30
		}
		p.pendingWindowDays = days
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

// decryptError maps a decrypt failure to ErrKeyRevoked when keyID has been revoked
// (crypto-shredded) — AWS rejects operations on a key pending deletion — so a
// WithDecryptionErrorHandler checking for ErrKeyRevoked recognizes it as shredded.
// Otherwise it is a genuine ErrDecryptionFailed. The IsRevoked probe runs only on the
// (rare) error path.
func (p *Provider) decryptError(keyID string, cause error) error {
	if revoked, rerr := p.IsRevoked(keyID); rerr == nil && revoked {
		return encryption.NewKeyRevokedError(keyID)
	}
	return encryption.NewDecryptionError(keyID, "", cause)
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
		return nil, p.decryptError(keyID, fmt.Errorf("KMS decrypt: %w", err))
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
		return nil, p.decryptError(keyID, fmt.Errorf("KMS decrypt data key: %w", err))
	}
	return output.Plaintext, nil
}

// RevokeKey crypto-shreds keyID by scheduling deletion of its KMS CMK. It
// implements encryption.Revocable. It requires the injected client to implement
// KMSRevocationClient, otherwise it returns ErrRevocationUnsupported. It is
// idempotent: a CMK already pending deletion returns nil. A merely-disabled CMK
// is reversible (EnableKey), so it is NOT treated as already revoked — RevokeKey
// schedules its deletion so the crypto-shred is actually permanent.
func (p *Provider) RevokeKey(keyID string) error {
	rc, err := p.revocationClient()
	if err != nil {
		return err
	}
	revoked, err := p.revoked(rc, keyID)
	if err != nil {
		return err
	}
	if revoked {
		return nil
	}
	days := p.pendingWindowDays
	if days == 0 {
		days = 7
	}
	if _, err := rc.ScheduleKeyDeletion(context.Background(), &kms.ScheduleKeyDeletionInput{
		KeyId:               &keyID,
		PendingWindowInDays: &days,
	}); err != nil {
		return encryption.NewEncryptionError(keyID, "", fmt.Errorf("KMS schedule key deletion: %w", err))
	}
	return nil
}

// IsRevoked reports whether keyID's CMK is permanently unrecoverable — pending
// deletion, or already deleted (NotFound). A merely-disabled CMK is reversible
// via EnableKey, so it is NOT reported as revoked. It implements encryption.Revocable.
func (p *Provider) IsRevoked(keyID string) (bool, error) {
	rc, err := p.revocationClient()
	if err != nil {
		return false, err
	}
	return p.revoked(rc, keyID)
}

// revocationClient returns the injected client as a KMSRevocationClient, or
// ErrRevocationUnsupported if it does not support revocation.
func (p *Provider) revocationClient() (KMSRevocationClient, error) {
	if err := p.checkClosed(); err != nil {
		return nil, err
	}
	rc, ok := p.client.(KMSRevocationClient)
	if !ok {
		return nil, encryption.ErrRevocationUnsupported
	}
	return rc, nil
}

func (p *Provider) revoked(rc KMSRevocationClient, keyID string) (bool, error) {
	out, err := rc.DescribeKey(context.Background(), &kms.DescribeKeyInput{KeyId: &keyID})
	if err != nil {
		// A CMK whose deletion has completed (after the pending window) no longer exists,
		// so DescribeKey returns NotFound. That is the terminal crypto-shred state — report
		// it as revoked, not an error, so Verify and decryptError keep recognizing a
		// permanently erased key after AWS finishes the deletion.
		var notFound *types.NotFoundException
		if errors.As(err, &notFound) {
			return true, nil
		}
		return false, encryption.NewDecryptionError(keyID, "", fmt.Errorf("KMS describe key: %w", err))
	}
	if out.KeyMetadata == nil {
		return false, nil
	}
	// Only PendingDeletion counts as revoked. A merely Disabled CMK is fully
	// reversible via EnableKey, so treating it as revoked would let RevokeKey skip
	// scheduling deletion and make IsRevoked/Verify certify an erasure whose data is
	// still recoverable. RevokeKey schedules deletion for a disabled key instead.
	st := out.KeyMetadata.KeyState
	return st == types.KeyStatePendingDeletion, nil
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
