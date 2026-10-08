package mink

import (
	"errors"
	"fmt"

	"go-mink.dev/encryption"
)

// Encryption-related sentinel errors.
// These are aliases to the encryption package errors for compatibility.
var (
	// ErrEncryptionFailed indicates a field encryption operation failed.
	ErrEncryptionFailed = encryption.ErrEncryptionFailed

	// ErrDecryptionFailed indicates a field decryption operation failed.
	ErrDecryptionFailed = encryption.ErrDecryptionFailed

	// ErrKeyNotFound indicates the requested encryption key does not exist.
	ErrKeyNotFound = encryption.ErrKeyNotFound

	// ErrKeyRevoked indicates the encryption key has been revoked (crypto-shredding).
	ErrKeyRevoked = encryption.ErrKeyRevoked

	// ErrProviderClosed indicates the encryption provider has been closed.
	ErrProviderClosed = encryption.ErrProviderClosed
)

// Field-encryption configuration and key-selection errors (root package).
var (
	// ErrKeyResolutionFailed indicates that WithRequireKeyResolution is set and the
	// master key for an event could not be resolved through the configured tenant /
	// subject key resolver: either the event carries no tenant id and no subject tag
	// to resolve, or the resolver returned "" for it. The append fails instead of
	// silently falling back to the default key. Wrapped in an EncryptionError, so it
	// also matches ErrEncryptionFailed; the typed form is KeyResolutionError.
	ErrKeyResolutionFailed = errors.New("mink: encryption key resolution failed")

	// ErrInvalidEncryptedFieldPath indicates a field path registered with
	// WithEncryptedFields is malformed: empty, or with an empty dot-separated
	// segment (a leading/trailing dot or ".."). Reported by
	// FieldEncryptionConfig.Validate and, wrapped in an EncryptionError, by the
	// first append of an affected event type.
	ErrInvalidEncryptedFieldPath = errors.New("mink: invalid encrypted field path")
)

// Encryption typed error aliases for convenience.
type (
	// EncryptionError provides detailed information about an encryption or decryption failure.
	EncryptionError = encryption.EncryptionError

	// KeyNotFoundError provides detailed information about a missing encryption key.
	KeyNotFoundError = encryption.KeyNotFoundError

	// KeyRevokedError provides detailed information about a revoked encryption key.
	KeyRevokedError = encryption.KeyRevokedError
)

// KeyResolutionError is the typed form of ErrKeyResolutionFailed: it names the
// event type whose master key could not be resolved and the tenant / subject id
// that was offered to the resolver (both empty when the event carried neither).
// It is the Cause of the EncryptionError returned by Append/SaveAggregate when
// WithRequireKeyResolution is set, so errors.As reaches it through Unwrap.
type KeyResolutionError struct {
	EventType string
	TenantID  string
	SubjectID string
}

// Error returns the error message.
func (e *KeyResolutionError) Error() string {
	switch {
	case e.TenantID != "":
		return fmt.Sprintf("mink: encryption key resolution failed for event type %q: resolver returned no key for tenant %q", e.EventType, e.TenantID)
	case e.SubjectID != "":
		return fmt.Sprintf("mink: encryption key resolution failed for event type %q: resolver returned no key for subject %q", e.EventType, e.SubjectID)
	default:
		return fmt.Sprintf("mink: encryption key resolution failed for event type %q: no tenant id or subject tag to resolve a key from", e.EventType)
	}
}

// Is reports whether this error matches the target error.
func (e *KeyResolutionError) Is(target error) bool {
	return target == ErrKeyResolutionFailed
}

// Unwrap returns the sentinel for errors.Unwrap().
func (e *KeyResolutionError) Unwrap() error {
	return ErrKeyResolutionFailed
}

// NewEncryptionError creates a new EncryptionError for an encrypt operation.
var NewEncryptionError = encryption.NewEncryptionError

// NewDecryptionError creates a new EncryptionError for a decrypt operation.
var NewDecryptionError = encryption.NewDecryptionError

// NewKeyNotFoundError creates a new KeyNotFoundError.
var NewKeyNotFoundError = encryption.NewKeyNotFoundError

// NewKeyRevokedError creates a new KeyRevokedError.
var NewKeyRevokedError = encryption.NewKeyRevokedError
