package kms

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go-mink.dev/encryption"
)

// A missing ALIAS proves only that the alias is gone, never that the key the data
// was sealed under is gone, so every revocation probe must report an unknown state
// (ErrAliasNotFound) instead of certifying a crypto-shred that never happened.
func TestProvider_AliasNotFound_IsNotRevoked(t *testing.T) {
	tests := []struct {
		name  string
		keyID string
	}{
		{"bare alias", "alias/customer-42"},
		{"alias ARN", "arn:aws:kms:eu-west-1:123456789012:alias/customer-42"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rc := &mockKMSRevocationClient{mockKMSClient: &mockKMSClient{}, notFound: true}
			p := New(WithKMSClient(rc))
			defer func() { _ = p.Close() }()

			revoked, err := p.IsRevoked(tt.keyID)
			require.Error(t, err)
			assert.False(t, revoked)
			assert.True(t, errors.Is(err, ErrAliasNotFound), "want ErrAliasNotFound, got %v", err)
			assert.True(t, errors.Is(err, encryption.ErrDecryptionFailed), "the probe error must stay classifiable as a decryption failure")

			state, err := p.RevocationState(tt.keyID)
			require.Error(t, err)
			assert.NotEqual(t, encryption.Revoked, state, "a missing alias must never read as Revoked")

			err = p.RevokeKey(tt.keyID)
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrAliasNotFound))
			assert.Zero(t, rc.scheduled, "nothing must be scheduled for deletion through a dangling alias")
		})
	}
}

// A key id or ARN that DescribeKey cannot find is the terminal crypto-shred state
// and keeps reading as revoked (unchanged behavior).
func TestProvider_KeyIDNotFound_StillRevoked(t *testing.T) {
	for _, keyID := range []string{"1234abcd-12ab-34cd-56ef-1234567890ab", "arn:aws:kms:eu-west-1:123456789012:key/1234abcd-12ab-34cd-56ef-1234567890ab"} {
		rc := &mockKMSRevocationClient{mockKMSClient: &mockKMSClient{}, notFound: true}
		p := New(WithKMSClient(rc))
		revoked, err := p.IsRevoked(keyID)
		require.NoError(t, err)
		assert.True(t, revoked)
		_ = p.Close()
	}
}

func TestIsAliasID(t *testing.T) {
	assert.True(t, isAliasID("alias/x"))
	assert.True(t, isAliasID("arn:aws:kms:eu-west-1:123456789012:alias/x"))
	assert.False(t, isAliasID("1234abcd-12ab-34cd-56ef-1234567890ab"))
	assert.False(t, isAliasID("arn:aws:kms:eu-west-1:123456789012:key/1234abcd"))
	assert.False(t, isAliasID(""))
}
