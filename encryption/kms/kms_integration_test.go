package kms

// Credential-gated integration test for the AWS KMS provider.
//
// This test talks to the real AWS KMS service and is skipped unless the
// required environment is present (mirrors the Postgres skip pattern):
//
//	AWS_REGION (or AWS_DEFAULT_REGION) – the KMS region
//	AWS_ACCESS_KEY_ID + AWS_SECRET_ACCESS_KEY – static credentials
//	MINK_KMS_TEST_KEY_ID – the KMS key ID/ARN/alias to exercise
//
// It is also skipped under `go test -short`. When configured, it performs a
// GenerateDataKey -> DecryptDataKey round-trip and asserts the unwrapped DEK
// matches the plaintext returned by GenerateDataKey.
//
// The KMS client is built with only the already-vendored `aws` and `kms`
// packages (no aws config/credentials submodule), so this adds no new module
// dependency.

import (
	"context"
	"os"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go-mink.dev/encryption"
)

func awsRegionFromEnv() string {
	if r := os.Getenv("AWS_REGION"); r != "" {
		return r
	}
	return os.Getenv("AWS_DEFAULT_REGION")
}

// staticCredentials builds a credentials provider from env vars using only the
// base aws package, avoiding the aws config/credentials submodules.
func staticCredentials() aws.CredentialsProvider {
	return aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{
			AccessKeyID:     os.Getenv("AWS_ACCESS_KEY_ID"),
			SecretAccessKey: os.Getenv("AWS_SECRET_ACCESS_KEY"),
			SessionToken:    os.Getenv("AWS_SESSION_TOKEN"),
			Source:          "mink-kms-integration-test",
		}, nil
	})
}

func TestProvider_Integration_RoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping KMS integration test in short mode")
	}

	region := awsRegionFromEnv()
	keyID := os.Getenv("MINK_KMS_TEST_KEY_ID")
	if region == "" || os.Getenv("AWS_ACCESS_KEY_ID") == "" || keyID == "" {
		t.Skip("Skipping KMS integration test: set AWS_REGION, AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY and MINK_KMS_TEST_KEY_ID to run")
	}

	cfg := aws.Config{
		Region:      region,
		Credentials: staticCredentials(),
	}
	client := kms.NewFromConfig(cfg)

	p := New(WithKMSClient(client))
	defer func() { _ = p.Close() }()

	ctx := context.Background()

	dk, err := p.GenerateDataKey(ctx, keyID)
	require.NoError(t, err)
	require.NotNil(t, dk)
	defer encryption.ClearBytes(dk.Plaintext)

	assert.Len(t, dk.Plaintext, 32)
	assert.NotEmpty(t, dk.Ciphertext)

	plaintext, err := p.DecryptDataKey(ctx, keyID, dk.Ciphertext)
	require.NoError(t, err)
	defer encryption.ClearBytes(plaintext)

	assert.Equal(t, dk.Plaintext, plaintext)
}
