package encryption

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEncryptionError_Error_WithField(t *testing.T) {
	err := NewEncryptionError("key-1", "email", errors.New("cipher failed"))
	assert.Equal(t, `mink: failed to encrypt field "email" with key "key-1": cipher failed`, err.Error())
}

func TestEncryptionError_Error_WithoutField(t *testing.T) {
	err := NewEncryptionError("key-1", "", errors.New("cipher failed"))
	assert.Equal(t, `mink: failed to encrypt with key "key-1": cipher failed`, err.Error())
}

func TestEncryptionError_Is_Encrypt(t *testing.T) {
	err := NewEncryptionError("k", "f", errors.New("x"))
	assert.True(t, errors.Is(err, ErrEncryptionFailed))
	assert.False(t, errors.Is(err, ErrDecryptionFailed))
}

func TestEncryptionError_Is_Decrypt(t *testing.T) {
	err := NewDecryptionError("k", "f", errors.New("x"))
	assert.True(t, errors.Is(err, ErrDecryptionFailed))
	assert.False(t, errors.Is(err, ErrEncryptionFailed))
}

func TestEncryptionError_Unwrap(t *testing.T) {
	cause := errors.New("root cause")
	err := NewEncryptionError("k", "", cause)
	assert.Equal(t, cause, errors.Unwrap(err))
}

func TestDecryptionError_Unwrap(t *testing.T) {
	cause := errors.New("root cause")
	err := NewDecryptionError("k", "", cause)
	assert.Equal(t, cause, errors.Unwrap(err))
}

func TestDecryptionError_Error_WithField(t *testing.T) {
	err := NewDecryptionError("key-1", "ssn", errors.New("bad key"))
	assert.Equal(t, `mink: failed to decrypt field "ssn" with key "key-1": bad key`, err.Error())
}

func TestDecryptionError_Error_WithoutField(t *testing.T) {
	err := NewDecryptionError("key-1", "", errors.New("bad key"))
	assert.Equal(t, `mink: failed to decrypt with key "key-1": bad key`, err.Error())
}

func TestKeyNotFoundError_Error(t *testing.T) {
	err := NewKeyNotFoundError("missing-key")
	assert.Equal(t, `mink: encryption key "missing-key" not found`, err.Error())
}

func TestKeyNotFoundError_Is(t *testing.T) {
	err := NewKeyNotFoundError("k")
	assert.True(t, errors.Is(err, ErrKeyNotFound))
	assert.False(t, errors.Is(err, ErrKeyRevoked))
}

func TestKeyNotFoundError_Unwrap(t *testing.T) {
	err := NewKeyNotFoundError("k")
	assert.Equal(t, ErrKeyNotFound, errors.Unwrap(err))
}

func TestKeyRevokedError_Error(t *testing.T) {
	err := NewKeyRevokedError("revoked-key")
	assert.Equal(t, `mink: encryption key "revoked-key" has been revoked`, err.Error())
}

func TestKeyRevokedError_Is(t *testing.T) {
	err := NewKeyRevokedError("k")
	assert.True(t, errors.Is(err, ErrKeyRevoked))
	assert.False(t, errors.Is(err, ErrKeyNotFound))
}

func TestKeyRevokedError_Unwrap(t *testing.T) {
	err := NewKeyRevokedError("k")
	assert.Equal(t, ErrKeyRevoked, errors.Unwrap(err))
}

func TestClearBytes_Empty(t *testing.T) {
	// Edge case: empty slice
	ClearBytes(nil)
	ClearBytes([]byte{})
}
