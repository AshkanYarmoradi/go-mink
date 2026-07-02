package local

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go-mink.dev/encryption"
)

func TestProvider_SoftRevoke_RestoreWithinWindow(t *testing.T) {
	p, err := New(WithKey("k", bytes.Repeat([]byte{1}, 32)))
	require.NoError(t, err)

	require.NoError(t, p.SoftRevokeKey("k", time.Hour))

	// Decryption is blocked while soft-revoked...
	_, err = p.Encrypt(context.Background(), "k", []byte("x"))
	require.ErrorIs(t, err, encryption.ErrKeyRevoked)

	st, err := p.RevocationState("k")
	require.NoError(t, err)
	assert.Equal(t, encryption.SoftRevoked, st)

	// ...but restorable within the grace window.
	require.NoError(t, p.UnrevokeKey("k"))

	st, err = p.RevocationState("k")
	require.NoError(t, err)
	assert.Equal(t, encryption.NotRevoked, st)

	_, err = p.Encrypt(context.Background(), "k", []byte("x"))
	require.NoError(t, err, "encryption works again after restore")
}

func TestProvider_SoftRevoke_PromotesAndShredsAfterWindow(t *testing.T) {
	p, err := New(WithKey("k", bytes.Repeat([]byte{1}, 32)))
	require.NoError(t, err)

	// A window that has already elapsed forces promotion on the next access.
	require.NoError(t, p.SoftRevokeKey("k", -time.Second))

	st, err := p.RevocationState("k")
	require.NoError(t, err)
	assert.Equal(t, encryption.Revoked, st, "elapsed soft-revoke must promote to permanent")

	// White-box: the key material must actually be GONE, not merely gated —
	// this is the core of the finding (soft-revoke used to leave the AES key resident).
	p.mu.RLock()
	_, present := p.keys["k"]
	_, stillSoft := p.softRevoked["k"]
	hard := p.revoked["k"]
	p.mu.RUnlock()
	assert.False(t, present, "key material must be cleared after the grace window")
	assert.False(t, stillSoft, "soft-revoke entry must be gone after promotion")
	assert.True(t, hard, "key must be permanently revoked after the window")

	// Cannot be undone anymore.
	err = p.UnrevokeKey("k")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "permanent")

	// Decryption stays blocked.
	_, err = p.Decrypt(context.Background(), "k", []byte("x"))
	require.ErrorIs(t, err, encryption.ErrKeyRevoked)
}

func TestProvider_RevocationState_HardRevoke(t *testing.T) {
	p, err := New(WithKey("k", bytes.Repeat([]byte{1}, 32)))
	require.NoError(t, err)

	st, err := p.RevocationState("k")
	require.NoError(t, err)
	assert.Equal(t, encryption.NotRevoked, st)

	require.NoError(t, p.RevokeKey("k"))

	st, err = p.RevocationState("k")
	require.NoError(t, err)
	assert.Equal(t, encryption.Revoked, st)
}
