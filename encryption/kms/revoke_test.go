package kms

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go-mink.dev/encryption"
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
