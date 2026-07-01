package vault

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go-mink.dev/encryption"
)

// mockVaultRevocationClient extends mockVaultClient with the optional revocation API.
type mockVaultRevocationClient struct {
	*mockVaultClient
	deleted map[string]bool
	deletes int
}

func (m *mockVaultRevocationClient) DeleteKey(_ context.Context, keyName string) error {
	if m.deleted == nil {
		m.deleted = map[string]bool{}
	}
	m.deleted[keyName] = true
	m.deletes++
	return nil
}

func (m *mockVaultRevocationClient) KeyExists(_ context.Context, keyName string) (bool, error) {
	return !m.deleted[keyName], nil
}

func TestProvider_RevokeKey(t *testing.T) {
	rc := &mockVaultRevocationClient{mockVaultClient: &mockVaultClient{}}
	p := New(WithVaultClient(rc))
	defer func() { _ = p.Close() }()

	revoked, err := p.IsRevoked("k")
	require.NoError(t, err)
	assert.False(t, revoked)

	require.NoError(t, p.RevokeKey("k"))
	assert.Equal(t, 1, rc.deletes)

	revoked, err = p.IsRevoked("k")
	require.NoError(t, err)
	assert.True(t, revoked)
}

func TestProvider_RevokeKey_Idempotent(t *testing.T) {
	rc := &mockVaultRevocationClient{mockVaultClient: &mockVaultClient{}, deleted: map[string]bool{"k": true}}
	p := New(WithVaultClient(rc))
	defer func() { _ = p.Close() }()

	require.NoError(t, p.RevokeKey("k"))
	assert.Equal(t, 0, rc.deletes, "an already-deleted key must not be re-deleted")
}

func TestProvider_RevokeKey_Unsupported(t *testing.T) {
	p := New(WithVaultClient(&mockVaultClient{})) // base client: no revocation support
	defer func() { _ = p.Close() }()

	assert.ErrorIs(t, p.RevokeKey("k"), encryption.ErrRevocationUnsupported)
	_, err := p.IsRevoked("k")
	assert.ErrorIs(t, err, encryption.ErrRevocationUnsupported)
}
