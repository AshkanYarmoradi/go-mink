// Package vault provides a HashiCorp Vault Transit encryption provider
// for field-level encryption. It uses the Vault Transit engine for key management
// while generating DEKs locally for envelope encryption.
//
// # Key names
//
// The keyID passed to every method is the Transit key name, and the injected
// VaultClient embeds it in request paths such as /v1/transit/encrypt/<name>. In
// go-mink that id is frequently derived from untrusted input (a tenant id, a
// subject id recorded by WithSubjectTagger), so the provider validates it before
// the client ever sees it. A name must be non-empty, must not contain '/', must
// not contain control characters (bytes below 0x20 or 0x7f), and must not be the
// path segment "." or "..". Anything else is rejected with an
// encryption.EncryptionError (never forwarded), so a crafted id cannot escape the
// Transit mount or address a different Vault API path. Every other byte — spaces,
// '?', '#', '%', non-ASCII — is allowed, so a deployment that already named keys
// that way keeps decrypting its existing ciphertext.
//
// The validated name is handed to the VaultClient url.PathEscape'd (a no-op for
// the [A-Za-z0-9_.~-] alphabet): "tenant 42" arrives as "tenant%2042" and "k?x"
// as "k%3Fx", so a client that concatenates it into a URL path is safe by
// construction and Vault decodes it back to the original key name. A VaultClient
// built on an SDK that escapes path segments itself must url.PathUnescape the
// name first to avoid double-escaping names that contain such bytes.
package vault

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"sync"
	"time"

	"go-mink.dev/encryption"
)

// DefaultRevocationTimeout bounds the Vault calls made by the context-free
// revocation methods (RevokeKey, IsRevoked) when no WithRevocationTimeout is
// configured, so an unreachable Vault cannot hang an erasure forever.
const DefaultRevocationTimeout = 30 * time.Second

// VaultClient defines the minimal interface for Vault Transit operations.
// Users inject their own implementation (e.g., wrapping the official Vault SDK).
// The keyName it receives has already been validated and url.PathEscape'd by the
// provider (see the package documentation), so it is safe to embed in a URL path
// as-is; a client whose HTTP layer escapes path segments itself should
// url.PathUnescape it first.
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
	client            VaultClient
	mu                sync.RWMutex
	closed            bool
	revocationTimeout time.Duration
}

// Option configures a Vault Provider.
type Option func(*Provider)

// WithVaultClient sets the Vault Transit client.
func WithVaultClient(client VaultClient) Option {
	return func(p *Provider) {
		p.client = client
	}
}

// WithRevocationTimeout bounds every Vault call made by the context-free
// revocation methods (RevokeKey, IsRevoked), which have no caller context to
// inherit a deadline from. It also caps the revocation probe run on the
// Decrypt/DecryptDataKey error path when the caller's own context carries no
// deadline. Non-positive values are ignored; the default is
// DefaultRevocationTimeout (30s). It mirrors the AWS KMS provider's option of the
// same name.
func WithRevocationTimeout(d time.Duration) Option {
	return func(p *Provider) {
		if d > 0 {
			p.revocationTimeout = d
		}
	}
}

// New creates a new Vault Transit encryption provider.
func New(opts ...Option) *Provider {
	p := &Provider{revocationTimeout: DefaultRevocationTimeout}
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
	name, err := transitKeyName(keyID)
	if err != nil {
		return nil, encryption.NewEncryptionError(keyID, "", err)
	}

	ciphertext, err := p.client.Encrypt(ctx, name, plaintext)
	if err != nil {
		return nil, encryption.NewEncryptionError(keyID, "", fmt.Errorf("vault encrypt: %w", err))
	}
	return ciphertext, nil
}

// decryptError maps a decrypt failure to ErrKeyRevoked when keyID has been revoked
// (crypto-shredded) — the Transit key was deleted — so a WithDecryptionErrorHandler
// checking for ErrKeyRevoked recognizes it as shredded. Otherwise it is a genuine
// ErrDecryptionFailed. The revocation probe runs only on the (rare) error path,
// under the caller's ctx so its cancellation and deadline are honored (a ctx with
// no deadline is bounded by the revocation timeout); name is the already-validated
// Transit key name. A probe that cannot determine the key state fails closed as a
// decryption error — never as a confirmed revocation.
func (p *Provider) decryptError(ctx context.Context, keyID, name string, cause error) error {
	if rc, ok := p.client.(VaultRevocationClient); ok {
		probeCtx, cancel := p.boundedContext(ctx)
		defer cancel()
		if exists, err := rc.KeyExists(probeCtx, name); err == nil && !exists {
			return encryption.NewKeyRevokedError(keyID)
		}
	}
	return encryption.NewDecryptionError(keyID, "", cause)
}

// Decrypt decrypts ciphertext using the Vault Transit key.
func (p *Provider) Decrypt(ctx context.Context, keyID string, ciphertext []byte) ([]byte, error) {
	if err := p.checkClosed(); err != nil {
		return nil, err
	}
	name, err := transitKeyName(keyID)
	if err != nil {
		return nil, encryption.NewDecryptionError(keyID, "", err)
	}

	plaintext, err := p.client.Decrypt(ctx, name, ciphertext)
	if err != nil {
		return nil, p.decryptError(ctx, keyID, name, fmt.Errorf("vault decrypt: %w", err))
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
	name, err := transitKeyName(keyID)
	if err != nil {
		return nil, encryption.NewEncryptionError(keyID, "", err)
	}

	// Generate random 32-byte DEK locally
	dek := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, dek); err != nil {
		return nil, encryption.NewEncryptionError(keyID, "", fmt.Errorf("failed to generate DEK: %w", err))
	}

	// Encrypt DEK with Vault Transit
	encryptedDEK, err := p.client.Encrypt(ctx, name, dek)
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
	name, err := transitKeyName(keyID)
	if err != nil {
		return nil, encryption.NewDecryptionError(keyID, "", err)
	}

	plaintext, err := p.client.Decrypt(ctx, name, encryptedKey)
	if err != nil {
		return nil, p.decryptError(ctx, keyID, name, fmt.Errorf("vault decrypt DEK: %w", err))
	}
	return plaintext, nil
}

// RevokeKey crypto-shreds keyID by deleting its Vault Transit key. It implements
// encryption.Revocable. It requires the injected client to implement
// VaultRevocationClient, otherwise it returns ErrRevocationUnsupported. It is
// idempotent: a key that no longer exists returns nil. The calls are bounded by
// the revocation timeout (WithRevocationTimeout, default DefaultRevocationTimeout)
// so an unreachable Vault fails the erasure instead of hanging it.
func (p *Provider) RevokeKey(keyID string) error {
	rc, err := p.revocationClient()
	if err != nil {
		return err
	}
	name, err := transitKeyName(keyID)
	if err != nil {
		return encryption.NewEncryptionError(keyID, "", err)
	}
	ctx, cancel := p.adminContext()
	defer cancel()
	exists, err := rc.KeyExists(ctx, name)
	if err != nil {
		return encryption.NewEncryptionError(keyID, "", fmt.Errorf("vault key exists: %w", err))
	}
	if !exists {
		return nil // idempotent: already deleted
	}
	if err := rc.DeleteKey(ctx, name); err != nil {
		return encryption.NewEncryptionError(keyID, "", fmt.Errorf("vault delete key: %w", err))
	}
	return nil
}

// IsRevoked reports whether keyID's Transit key has been deleted.
// It implements encryption.Revocable. The call is bounded by the revocation
// timeout like RevokeKey.
func (p *Provider) IsRevoked(keyID string) (bool, error) {
	rc, err := p.revocationClient()
	if err != nil {
		return false, err
	}
	name, err := transitKeyName(keyID)
	if err != nil {
		return false, encryption.NewDecryptionError(keyID, "", err)
	}
	ctx, cancel := p.adminContext()
	defer cancel()
	exists, err := rc.KeyExists(ctx, name)
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

// timeout returns the effective revocation timeout.
func (p *Provider) timeout() time.Duration {
	if p.revocationTimeout > 0 {
		return p.revocationTimeout
	}
	return DefaultRevocationTimeout
}

// adminContext returns the context used by the context-free revocation methods:
// a background context bounded by the revocation timeout.
func (p *Provider) adminContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), p.timeout())
}

// boundedContext returns ctx itself when it already carries a deadline (the
// caller's own budget is respected, never shortened or extended) and otherwise
// derives a child bounded by the revocation timeout. Either way the caller's
// cancellation and values propagate.
func (p *Provider) boundedContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, p.timeout())
}

// transitKeyName validates keyID as a Vault Transit key name and returns the form
// that is safe to embed in a request path. Key ids reach the provider from
// tenant/subject-derived input, and the VaultClient concatenates them into URL
// paths (/v1/transit/encrypt/<name>), so an unchecked name could traverse out of
// the Transit mount ("../sys/policies/acl/root"), address a sub-path ("a/b"), or
// smuggle a header-breaking control byte. The rules are deliberately the minimum
// that closes those holes — non-empty, no '/', no control bytes, not the segment
// "." or ".." — so a deployment that already used other characters (spaces,
// non-ASCII, '?', '%', ...) is not locked out of its ciphertext; such bytes are
// neutralized by url.PathEscape instead, which Vault decodes back to the original
// key name.
func transitKeyName(keyID string) (string, error) {
	if keyID == "" {
		return "", errors.New("vault transit key name must not be empty")
	}
	if strings.Contains(keyID, "/") {
		return "", fmt.Errorf("vault transit key name %q must not contain '/'", keyID)
	}
	if keyID == "." || keyID == ".." {
		return "", fmt.Errorf("vault transit key name %q must not be a relative path segment", keyID)
	}
	for i := 0; i < len(keyID); i++ {
		if c := keyID[i]; c < 0x20 || c == 0x7f {
			return "", fmt.Errorf("vault transit key name %q contains control byte %q at offset %d", keyID, c, i)
		}
	}
	return url.PathEscape(keyID), nil
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
