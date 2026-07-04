package mink_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awskms "github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go-mink.dev/encryption"
	minkkms "go-mink.dev/encryption/kms"
)

// awsTestConfig builds an aws.Config with static test credentials, by hand (matching the repo's
// existing KMS integration test) so no aws-sdk-go-v2/config module dependency is needed.
func awsTestConfig() aws.Config {
	return aws.Config{
		Region: "us-east-1",
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: "test", SecretAccessKey: "test", Source: "e2e"}, nil
		}),
	}
}

// TestE2E_KMS_EncryptRevoke: the AWS KMS provider round-trips a data key against a real KMS API
// (a local-kms emulator or real AWS), and revoking the key (crypto-shred) makes decrypt fail.
// Gated on TEST_KMS_ENDPOINT (self-skips otherwise).
func TestE2E_KMS_EncryptRevoke(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping KMS E2E in short mode")
	}
	endpoint := os.Getenv("TEST_KMS_ENDPOINT")
	if endpoint == "" {
		t.Skip("TEST_KMS_ENDPOINT not set; skipping KMS E2E")
	}
	ctx := context.Background()

	client := awskms.NewFromConfig(awsTestConfig(), func(o *awskms.Options) {
		o.BaseEndpoint = aws.String(endpoint)
	})
	ck, err := client.CreateKey(ctx, &awskms.CreateKeyInput{})
	require.NoError(t, err)
	keyID := aws.ToString(ck.KeyMetadata.KeyId)

	provider := minkkms.New(minkkms.WithKMSClient(client))
	t.Cleanup(func() { _ = provider.Close() })

	// Round-trip a data key (the real *kms.Client satisfies both KMSClient and KMSRevocationClient).
	dk, err := provider.GenerateDataKey(ctx, keyID)
	require.NoError(t, err)
	want := append([]byte(nil), dk.Plaintext...) // copy before any zeroing
	pt, err := provider.DecryptDataKey(ctx, keyID, dk.Ciphertext)
	require.NoError(t, err)
	assert.Equal(t, want, pt, "data key round-trips through real KMS")

	// Crypto-shred: schedule deletion, then decrypt must fail with ErrKeyRevoked.
	require.NoError(t, provider.RevokeKey(keyID))
	revoked, err := provider.IsRevoked(keyID)
	require.NoError(t, err)
	assert.True(t, revoked, "the key reports revoked after scheduled deletion")

	_, err = provider.DecryptDataKey(ctx, keyID, dk.Ciphertext)
	require.Error(t, err, "decrypt after revoke fails")
	assert.True(t, errors.Is(err, encryption.ErrKeyRevoked), "the failure is a KeyRevokedError")
}
