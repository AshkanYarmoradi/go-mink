package kms

import (
	"context"
	"fmt"
	"testing"

	"github.com/AshkanYarmoradi/go-mink/encryption"
	"github.com/AshkanYarmoradi/go-mink/encryption/providertest"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockKMSClient implements KMSClient for testing.
type mockKMSClient struct {
	encryptFunc         func(ctx context.Context, params *kms.EncryptInput) (*kms.EncryptOutput, error)
	decryptFunc         func(ctx context.Context, params *kms.DecryptInput) (*kms.DecryptOutput, error)
	generateDataKeyFunc func(ctx context.Context, params *kms.GenerateDataKeyInput) (*kms.GenerateDataKeyOutput, error)
}

func (m *mockKMSClient) Encrypt(ctx context.Context, params *kms.EncryptInput, _ ...func(*kms.Options)) (*kms.EncryptOutput, error) {
	if m.encryptFunc != nil {
		return m.encryptFunc(ctx, params)
	}
	return &kms.EncryptOutput{CiphertextBlob: append([]byte("enc:"), params.Plaintext...)}, nil
}

func (m *mockKMSClient) Decrypt(ctx context.Context, params *kms.DecryptInput, _ ...func(*kms.Options)) (*kms.DecryptOutput, error) {
	if m.decryptFunc != nil {
		return m.decryptFunc(ctx, params)
	}
	// Reverse the mock encryption
	if len(params.CiphertextBlob) > 4 && string(params.CiphertextBlob[:4]) == "enc:" {
		return &kms.DecryptOutput{Plaintext: params.CiphertextBlob[4:]}, nil
	}
	return &kms.DecryptOutput{Plaintext: params.CiphertextBlob}, nil
}

func (m *mockKMSClient) GenerateDataKey(ctx context.Context, params *kms.GenerateDataKeyInput, _ ...func(*kms.Options)) (*kms.GenerateDataKeyOutput, error) {
	if m.generateDataKeyFunc != nil {
		return m.generateDataKeyFunc(ctx, params)
	}
	plaintext := make([]byte, 32)
	for i := range plaintext {
		plaintext[i] = byte(i)
	}
	return &kms.GenerateDataKeyOutput{
		Plaintext:      plaintext,
		CiphertextBlob: append([]byte("enc:"), plaintext...),
		KeyId:          params.KeyId,
	}, nil
}

func newTestProvider(mock *mockKMSClient) *Provider {
	return New(WithKMSClient(mock))
}

func TestProvider_EncryptDecrypt(t *testing.T) {
	p := newTestProvider(&mockKMSClient{})
	defer func() { _ = p.Close() }()

	ctx := context.Background()
	plaintext := []byte("sensitive data")

	ciphertext, err := p.Encrypt(ctx, "key-1", plaintext)
	require.NoError(t, err)
	assert.Equal(t, []byte("enc:sensitive data"), ciphertext)

	decrypted, err := p.Decrypt(ctx, "key-1", ciphertext)
	require.NoError(t, err)
	assert.Equal(t, plaintext, decrypted)
}

func TestProvider_GenerateDataKey(t *testing.T) {
	p := newTestProvider(&mockKMSClient{})
	defer func() { _ = p.Close() }()

	providertest.AssertGenerateDataKeyBasics(t, p, "master-key")
}

func TestProvider_GenerateDataKey_RequestsAES256(t *testing.T) {
	var capturedSpec types.DataKeySpec
	p := newTestProvider(&mockKMSClient{
		generateDataKeyFunc: func(_ context.Context, params *kms.GenerateDataKeyInput) (*kms.GenerateDataKeyOutput, error) {
			capturedSpec = params.KeySpec
			return &kms.GenerateDataKeyOutput{
				Plaintext:      make([]byte, 32),
				CiphertextBlob: make([]byte, 64),
				KeyId:          params.KeyId,
			}, nil
		},
	})
	defer func() { _ = p.Close() }()

	_, err := p.GenerateDataKey(context.Background(), "key-1")
	require.NoError(t, err)
	assert.Equal(t, types.DataKeySpecAes256, capturedSpec)
}

func TestProvider_DecryptDataKey(t *testing.T) {
	p := newTestProvider(&mockKMSClient{})
	defer func() { _ = p.Close() }()

	encrypted := []byte("enc:decrypted-dek-data")
	plaintext, err := p.DecryptDataKey(context.Background(), "key-1", encrypted)
	require.NoError(t, err)
	assert.Equal(t, []byte("decrypted-dek-data"), plaintext)
}

func TestProvider_EncryptError(t *testing.T) {
	p := newTestProvider(&mockKMSClient{
		encryptFunc: func(_ context.Context, _ *kms.EncryptInput) (*kms.EncryptOutput, error) {
			return nil, fmt.Errorf("access denied")
		},
	})
	defer func() { _ = p.Close() }()

	_, err := p.Encrypt(context.Background(), "key-1", []byte("data"))
	require.Error(t, err)
	assert.ErrorIs(t, err, encryption.ErrEncryptionFailed)
	assert.Contains(t, err.Error(), "access denied")
}

func TestProvider_DecryptError(t *testing.T) {
	p := newTestProvider(&mockKMSClient{
		decryptFunc: func(_ context.Context, _ *kms.DecryptInput) (*kms.DecryptOutput, error) {
			return nil, fmt.Errorf("key disabled")
		},
	})
	defer func() { _ = p.Close() }()

	providertest.AssertDecryptError(t, p, "key-1")
}

func TestProvider_GenerateDataKeyError(t *testing.T) {
	p := newTestProvider(&mockKMSClient{
		generateDataKeyFunc: func(_ context.Context, _ *kms.GenerateDataKeyInput) (*kms.GenerateDataKeyOutput, error) {
			return nil, fmt.Errorf("key not found")
		},
	})
	defer func() { _ = p.Close() }()

	providertest.AssertGenerateDataKeyError(t, p, "bad-key")
}

func TestProvider_DecryptDataKeyError(t *testing.T) {
	p := newTestProvider(&mockKMSClient{
		decryptFunc: func(_ context.Context, _ *kms.DecryptInput) (*kms.DecryptOutput, error) {
			return nil, fmt.Errorf("invalid ciphertext")
		},
	})
	defer func() { _ = p.Close() }()

	providertest.AssertDecryptDataKeyError(t, p, "key-1")
}

func TestProvider_Close(t *testing.T) {
	p := newTestProvider(&mockKMSClient{})
	providertest.AssertCloseBlocksAllOperations(t, p)
}
