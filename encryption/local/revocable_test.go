package local

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProvider_IsRevoked(t *testing.T) {
	p, err := New(WithKey("k", make([]byte, 32)))
	require.NoError(t, err)

	revoked, err := p.IsRevoked("k")
	require.NoError(t, err)
	assert.False(t, revoked)

	require.NoError(t, p.RevokeKey("k"))

	revoked, err = p.IsRevoked("k")
	require.NoError(t, err)
	assert.True(t, revoked)
}

func TestProvider_RevokeKey_Idempotent(t *testing.T) {
	p, err := New(WithKey("k", make([]byte, 32)))
	require.NoError(t, err)

	require.NoError(t, p.RevokeKey("k"))
	// A second revoke must be a no-op success, NOT a KeyNotFound error.
	require.NoError(t, p.RevokeKey("k"))

	revoked, err := p.IsRevoked("k")
	require.NoError(t, err)
	assert.True(t, revoked)
}
