package encryption

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRevocationState_String(t *testing.T) {
	assert.Equal(t, "not_revoked", NotRevoked.String())
	assert.Equal(t, "soft_revoked", SoftRevoked.String())
	assert.Equal(t, "revoked", Revoked.String())
	assert.Equal(t, "unknown", RevocationState(99).String())
}

// bareProvider implements Provider but none of the optional revocation interfaces.
type bareProvider struct{}

func (bareProvider) Encrypt(context.Context, string, []byte) ([]byte, error)        { return nil, nil }
func (bareProvider) Decrypt(context.Context, string, []byte) ([]byte, error)        { return nil, nil }
func (bareProvider) GenerateDataKey(context.Context, string) (*DataKey, error)      { return nil, nil }
func (bareProvider) DecryptDataKey(context.Context, string, []byte) ([]byte, error) { return nil, nil }
func (bareProvider) Close() error                                                   { return nil }

// revokableOnly adds Revocable but NOT StatefulRevocable/RecoverableRevocable.
type revokableOnly struct {
	bareProvider
	revoked bool
}

func (revokableOnly) RevokeKey(string) error           { return nil }
func (r revokableOnly) IsRevoked(string) (bool, error) { return r.revoked, nil }

func TestGetRevocationState_FallbackAndUnsupported(t *testing.T) {
	// A provider without any revocation support is reported as unsupported.
	_, err := GetRevocationState(bareProvider{}, "k")
	assert.ErrorIs(t, err, ErrRevocationUnsupported)

	// A Revocable-but-not-Stateful provider falls back to IsRevoked: true → Revoked.
	st, err := GetRevocationState(revokableOnly{revoked: true}, "k")
	require.NoError(t, err)
	assert.Equal(t, Revoked, st)

	// false → NotRevoked.
	st, err = GetRevocationState(revokableOnly{revoked: false}, "k")
	require.NoError(t, err)
	assert.Equal(t, NotRevoked, st)
}

func TestSoftRevokeUnrevoke_UnsupportedProvider(t *testing.T) {
	// Neither soft-revoke nor un-revoke silently falls through to a hard revoke.
	assert.ErrorIs(t, SoftRevoke(revokableOnly{}, "k", time.Hour), ErrRevocationUnsupported)
	assert.ErrorIs(t, Unrevoke(revokableOnly{}, "k"), ErrRevocationUnsupported)
}
