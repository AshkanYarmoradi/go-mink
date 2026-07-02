// Package encryption defines interfaces and types for field-level encryption
// in event sourcing. Providers implement the Provider interface to support
// envelope encryption with various key management systems (local, AWS KMS,
// HashiCorp Vault).
package encryption

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Provider abstracts crypto operations for field-level encryption.
// Implementations must be safe for concurrent use.
type Provider interface {
	// Encrypt encrypts plaintext using the specified master key.
	Encrypt(ctx context.Context, keyID string, plaintext []byte) (ciphertext []byte, err error)

	// Decrypt decrypts ciphertext using the specified master key.
	Decrypt(ctx context.Context, keyID string, ciphertext []byte) (plaintext []byte, err error)

	// GenerateDataKey creates a new data encryption key (DEK) protected by the master key.
	// The returned DataKey contains both the plaintext DEK (for immediate use) and the
	// encrypted DEK (for storage in event metadata). The plaintext must be zeroed after use.
	GenerateDataKey(ctx context.Context, keyID string) (*DataKey, error)

	// DecryptDataKey decrypts a previously encrypted DEK using the specified master key.
	// Returns the plaintext DEK for use in decrypting event fields.
	DecryptDataKey(ctx context.Context, keyID string, encryptedKey []byte) ([]byte, error)

	// Close releases any resources held by the provider.
	Close() error
}

// Revocable is an OPTIONAL extension of Provider that supports crypto-shredding:
// revoking a master key renders all data encrypted under it permanently
// unrecoverable, which is how go-mink satisfies the GDPR right to erasure
// (Article 17). Providers MAY implement it; detect support with a type assertion
// or the Revoke / IsRevoked package helpers.
//
// The signature intentionally mirrors the existing local provider's RevokeKey
// (no context.Context): revocation is a deliberate administrative operation and
// the established provider revocation signature is context-free. Providers whose
// backend needs a context (KMS, Vault) use a background context internally.
type Revocable interface {
	// RevokeKey permanently revokes keyID (crypto-shredding). It is idempotent:
	// revoking an already-revoked key returns nil.
	RevokeKey(keyID string) error

	// IsRevoked reports whether keyID is currently revoked.
	IsRevoked(keyID string) (bool, error)
}

// Revoke crypto-shreds keyID via p when p implements Revocable, otherwise it
// returns ErrRevocationUnsupported. It is the detection entry point the erasure
// machinery uses so callers need not type-assert the provider themselves.
func Revoke(p Provider, keyID string) error {
	r, ok := p.(Revocable)
	if !ok {
		return ErrRevocationUnsupported
	}
	return r.RevokeKey(keyID)
}

// IsRevoked reports whether keyID is revoked via p, returning
// ErrRevocationUnsupported when p does not implement Revocable.
func IsRevoked(p Provider, keyID string) (bool, error) {
	r, ok := p.(Revocable)
	if !ok {
		return false, ErrRevocationUnsupported
	}
	return r.IsRevoked(keyID)
}

// RecoverableRevocable is an OPTIONAL extension of Revocable supporting a grace
// window: SoftRevokeKey blocks decryption but can be undone with UnrevokeKey until
// the window elapses, after which the revocation becomes a permanent crypto-shred.
// This lets an accidental erasure be recovered. Providers MAY implement it.
type RecoverableRevocable interface {
	Revocable

	// SoftRevokeKey blocks decryption under keyID but allows UnrevokeKey to restore
	// it until graceWindow elapses.
	SoftRevokeKey(keyID string, graceWindow time.Duration) error

	// UnrevokeKey restores a soft-revoked key, if still within its grace window.
	UnrevokeKey(keyID string) error
}

// RevocationState is the revocation status of a key. It lets callers (notably
// erasure Verify) distinguish a still-recoverable soft-revocation from a permanent
// crypto-shred, which IsRevoked (a single bool) cannot.
type RevocationState int

const (
	// NotRevoked means the key is active and its data is recoverable.
	NotRevoked RevocationState = iota

	// SoftRevoked means decryption is blocked but the key can still be restored via
	// UnrevokeKey until its grace window elapses — the data is NOT yet permanently
	// erased. Verify MUST NOT certify a soft-revoked key as erased.
	SoftRevoked

	// Revoked means the key is permanently crypto-shredded; its data is unrecoverable.
	Revoked
)

// String returns the state name.
func (s RevocationState) String() string {
	switch s {
	case NotRevoked:
		return "not_revoked"
	case SoftRevoked:
		return "soft_revoked"
	case Revoked:
		return "revoked"
	default:
		return "unknown"
	}
}

// StatefulRevocable is an OPTIONAL extension that reports the fine-grained
// RevocationState of a key. Providers with a grace window (RecoverableRevocable)
// SHOULD implement it so callers can tell SoftRevoked from Revoked. Detect support
// with GetRevocationState, which falls back to IsRevoked for providers without it.
//
// It embeds Revocable — reporting a revocation state only makes sense for a provider
// that can revoke — so GetRevocationState's contract holds: a provider that is not
// Revocable is never treated as stateful, and GetRevocationState reports it as
// unsupported. This mirrors RecoverableRevocable, which also embeds Revocable.
type StatefulRevocable interface {
	Revocable
	RevocationState(keyID string) (RevocationState, error)
}

// GetRevocationState reports the RevocationState of keyID via p. If p implements
// StatefulRevocable it is used directly; otherwise it falls back to IsRevoked,
// mapping true→Revoked and false→NotRevoked (so a provider without soft-revoke can
// never report SoftRevoked). Returns ErrRevocationUnsupported when p is not even
// Revocable.
func GetRevocationState(p Provider, keyID string) (RevocationState, error) {
	if sr, ok := p.(StatefulRevocable); ok {
		return sr.RevocationState(keyID)
	}
	revoked, err := IsRevoked(p, keyID)
	if err != nil {
		return NotRevoked, err
	}
	if revoked {
		return Revoked, nil
	}
	return NotRevoked, nil
}

// SoftRevoke soft-revokes keyID via p when p implements RecoverableRevocable,
// otherwise it returns ErrRevocationUnsupported — so a caller relying on a grace
// window is told explicitly rather than silently falling through to a hard revoke.
func SoftRevoke(p Provider, keyID string, graceWindow time.Duration) error {
	r, ok := p.(RecoverableRevocable)
	if !ok {
		return ErrRevocationUnsupported
	}
	return r.SoftRevokeKey(keyID, graceWindow)
}

// Unrevoke restores a soft-revoked keyID via p when p implements
// RecoverableRevocable, otherwise it returns ErrRevocationUnsupported.
func Unrevoke(p Provider, keyID string) error {
	r, ok := p.(RecoverableRevocable)
	if !ok {
		return ErrRevocationUnsupported
	}
	return r.UnrevokeKey(keyID)
}

// DataKey holds the plaintext and ciphertext forms of a data encryption key (DEK).
// The plaintext is used for local AES-256-GCM encryption and must NEVER be persisted.
// The ciphertext is safe to store in event metadata.
type DataKey struct {
	// Plaintext is the 32-byte AES key used for field encryption.
	// Must be zeroed after use via ClearBytes.
	Plaintext []byte

	// Ciphertext is the encrypted form of the DEK, safe to persist.
	Ciphertext []byte

	// KeyID is the master key that encrypted this DEK.
	KeyID string
}

// ClearBytes zeroes out a byte slice to prevent key material from lingering in memory.
func ClearBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// Sentinel errors for encryption operations.
var (
	// ErrEncryptionFailed indicates a field encryption operation failed.
	ErrEncryptionFailed = errors.New("mink: encryption failed")

	// ErrDecryptionFailed indicates a field decryption operation failed.
	ErrDecryptionFailed = errors.New("mink: decryption failed")

	// ErrKeyNotFound indicates the requested encryption key does not exist.
	ErrKeyNotFound = errors.New("mink: encryption key not found")

	// ErrKeyRevoked indicates the encryption key has been revoked (crypto-shredding).
	ErrKeyRevoked = errors.New("mink: encryption key revoked")

	// ErrRevocationUnsupported indicates the configured provider does not implement
	// Revocable, so crypto-shredding (key revocation) is not available.
	ErrRevocationUnsupported = errors.New("mink: key revocation unsupported by provider")

	// ErrProviderClosed indicates the encryption provider has been closed.
	ErrProviderClosed = errors.New("mink: encryption provider closed")
)

// EncryptionError provides detailed information about an encryption or decryption failure.
type EncryptionError struct {
	Operation string // "encrypt" or "decrypt"
	KeyID     string
	Field     string
	Cause     error
}

// Error returns the error message.
func (e *EncryptionError) Error() string {
	if e.Field != "" {
		return fmt.Sprintf("mink: failed to %s field %q with key %q: %v",
			e.Operation, e.Field, e.KeyID, e.Cause)
	}
	return fmt.Sprintf("mink: failed to %s with key %q: %v",
		e.Operation, e.KeyID, e.Cause)
}

// Is reports whether this error matches the target error.
func (e *EncryptionError) Is(target error) bool {
	if e.Operation == "encrypt" {
		return target == ErrEncryptionFailed
	}
	return target == ErrDecryptionFailed
}

// Unwrap returns the underlying cause for errors.Unwrap().
func (e *EncryptionError) Unwrap() error {
	return e.Cause
}

// NewEncryptionError creates a new EncryptionError for an encrypt operation.
func NewEncryptionError(keyID, field string, cause error) *EncryptionError {
	return &EncryptionError{
		Operation: "encrypt",
		KeyID:     keyID,
		Field:     field,
		Cause:     cause,
	}
}

// NewDecryptionError creates a new EncryptionError for a decrypt operation.
func NewDecryptionError(keyID, field string, cause error) *EncryptionError {
	return &EncryptionError{
		Operation: "decrypt",
		KeyID:     keyID,
		Field:     field,
		Cause:     cause,
	}
}

// KeyNotFoundError provides detailed information about a missing encryption key.
type KeyNotFoundError struct {
	KeyID string
}

// Error returns the error message.
func (e *KeyNotFoundError) Error() string {
	return fmt.Sprintf("mink: encryption key %q not found", e.KeyID)
}

// Is reports whether this error matches the target error.
func (e *KeyNotFoundError) Is(target error) bool {
	return target == ErrKeyNotFound
}

// Unwrap returns the underlying error for errors.Unwrap().
func (e *KeyNotFoundError) Unwrap() error {
	return ErrKeyNotFound
}

// NewKeyNotFoundError creates a new KeyNotFoundError.
func NewKeyNotFoundError(keyID string) *KeyNotFoundError {
	return &KeyNotFoundError{KeyID: keyID}
}

// KeyRevokedError provides detailed information about a revoked encryption key.
type KeyRevokedError struct {
	KeyID string
}

// Error returns the error message.
func (e *KeyRevokedError) Error() string {
	return fmt.Sprintf("mink: encryption key %q has been revoked", e.KeyID)
}

// Is reports whether this error matches the target error.
func (e *KeyRevokedError) Is(target error) bool {
	return target == ErrKeyRevoked
}

// Unwrap returns the underlying error for errors.Unwrap().
func (e *KeyRevokedError) Unwrap() error {
	return ErrKeyRevoked
}

// NewKeyRevokedError creates a new KeyRevokedError.
func NewKeyRevokedError(keyID string) *KeyRevokedError {
	return &KeyRevokedError{KeyID: keyID}
}
