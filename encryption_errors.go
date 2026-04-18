package mink

import "go-mink.dev/encryption"

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

// Encryption typed error aliases for convenience.
type (
	// EncryptionError provides detailed information about an encryption or decryption failure.
	EncryptionError = encryption.EncryptionError

	// KeyNotFoundError provides detailed information about a missing encryption key.
	KeyNotFoundError = encryption.KeyNotFoundError

	// KeyRevokedError provides detailed information about a revoked encryption key.
	KeyRevokedError = encryption.KeyRevokedError
)

// NewEncryptionError creates a new EncryptionError for an encrypt operation.
var NewEncryptionError = encryption.NewEncryptionError

// NewDecryptionError creates a new EncryptionError for a decrypt operation.
var NewDecryptionError = encryption.NewDecryptionError

// NewKeyNotFoundError creates a new KeyNotFoundError.
var NewKeyNotFoundError = encryption.NewKeyNotFoundError

// NewKeyRevokedError creates a new KeyRevokedError.
var NewKeyRevokedError = encryption.NewKeyRevokedError
