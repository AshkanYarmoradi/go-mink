// Package encryption defines interfaces and types for field-level encryption
// in event sourcing. Providers implement the Provider interface to support
// envelope encryption with various key management systems (local, AWS KMS,
// HashiCorp Vault).
package encryption

import (
	"context"
	"errors"
	"fmt"
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
