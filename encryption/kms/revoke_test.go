package kms

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go-mink.dev/encryption"
	"go-mink.dev/encryption/providertest"
)

// mockKMSRevocationClient extends mockKMSClient with the optional revocation API.
type mockKMSRevocationClient struct {
	*mockKMSClient
	state     types.KeyState
	scheduled int
}

func (m *mockKMSRevocationClient) ScheduleKeyDeletion(_ context.Context, _ *kms.ScheduleKeyDeletionInput, _ ...func(*kms.Options)) (*kms.ScheduleKeyDeletionOutput, error) {
	m.scheduled++
	m.state = types.KeyStatePendingDeletion
	return &kms.ScheduleKeyDeletionOutput{}, nil
}

func (m *mockKMSRevocationClient) DescribeKey(_ context.Context, params *kms.DescribeKeyInput, _ ...func(*kms.Options)) (*kms.DescribeKeyOutput, error) {
	st := m.state
	if st == "" {
		st = types.KeyStateEnabled
	}
	return &kms.DescribeKeyOutput{KeyMetadata: &types.KeyMetadata{KeyId: params.KeyId, KeyState: st}}, nil
}

// A key pending deletion (or disabled) is unusable for crypto operations — mirror AWS
// so the shred property is actually exercised.
func (m *mockKMSRevocationClient) revokedForCrypto() bool {
	return m.state == types.KeyStatePendingDeletion || m.state == types.KeyStateDisabled
}

func (m *mockKMSRevocationClient) Decrypt(ctx context.Context, params *kms.DecryptInput, o ...func(*kms.Options)) (*kms.DecryptOutput, error) {
	if m.revokedForCrypto() {
		return nil, errors.New("kms: key is pending deletion")
	}
	return m.mockKMSClient.Decrypt(ctx, params, o...)
}

func (m *mockKMSRevocationClient) GenerateDataKey(ctx context.Context, params *kms.GenerateDataKeyInput, o ...func(*kms.Options)) (*kms.GenerateDataKeyOutput, error) {
	if m.revokedForCrypto() {
		return nil, errors.New("kms: key is pending deletion")
	}
	return m.mockKMSClient.GenerateDataKey(ctx, params, o...)
}

func TestProvider_RevokeMakesDecryptFail(t *testing.T) {
	rc := &mockKMSRevocationClient{mockKMSClient: &mockKMSClient{}}
	p := New(WithKMSClient(rc))
	defer func() { _ = p.Close() }()

	providertest.AssertRevokeMakesDecryptFail(t, p, "k")
}

func TestProvider_RevokeKey(t *testing.T) {
	rc := &mockKMSRevocationClient{mockKMSClient: &mockKMSClient{}}
	p := New(WithKMSClient(rc))
	defer func() { _ = p.Close() }()

	revoked, err := p.IsRevoked("k")
	require.NoError(t, err)
	assert.False(t, revoked)

	require.NoError(t, p.RevokeKey("k"))
	assert.Equal(t, 1, rc.scheduled)

	revoked, err = p.IsRevoked("k")
	require.NoError(t, err)
	assert.True(t, revoked)
}

func TestProvider_RevokeKey_Idempotent(t *testing.T) {
	rc := &mockKMSRevocationClient{mockKMSClient: &mockKMSClient{}, state: types.KeyStatePendingDeletion}
	p := New(WithKMSClient(rc))
	defer func() { _ = p.Close() }()

	require.NoError(t, p.RevokeKey("k"))
	assert.Equal(t, 0, rc.scheduled, "a key already pending deletion must not be re-scheduled")
}

func TestProvider_RevokeKey_Unsupported(t *testing.T) {
	p := New(WithKMSClient(&mockKMSClient{})) // base client: no revocation support
	defer func() { _ = p.Close() }()

	assert.ErrorIs(t, p.RevokeKey("k"), encryption.ErrRevocationUnsupported)
	_, err := p.IsRevoked("k")
	assert.ErrorIs(t, err, encryption.ErrRevocationUnsupported)
}
