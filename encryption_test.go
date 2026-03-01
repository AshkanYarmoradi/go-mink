package mink

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"testing"

	"github.com/AshkanYarmoradi/go-mink/encryption"
	"github.com/AshkanYarmoradi/go-mink/encryption/local"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testProvider(t *testing.T, keyID string) *local.Provider {
	t.Helper()
	key := make([]byte, 32)
	_, err := rand.Read(key)
	require.NoError(t, err)
	return local.New(local.WithKey(keyID, key))
}

func TestFieldEncryptionConfig_HasEncryptedFields(t *testing.T) {
	config := NewFieldEncryptionConfig(
		WithEncryptedFields("UserCreated", "email", "phone"),
	)

	assert.True(t, config.HasEncryptedFields("UserCreated"))
	assert.False(t, config.HasEncryptedFields("OrderCreated"))
}

func TestFieldEncryptionConfig_EncryptDecryptFields(t *testing.T) {
	provider := testProvider(t, "master-1")
	defer provider.Close()

	config := NewFieldEncryptionConfig(
		WithEncryptionProvider(provider),
		WithDefaultKeyID("master-1"),
		WithEncryptedFields("UserCreated", "email", "phone"),
	)

	ctx := context.Background()

	// Original event data
	original := map[string]interface{}{
		"name":  "John Doe",
		"email": "john@example.com",
		"phone": "+1234567890",
	}
	data, err := json.Marshal(original)
	require.NoError(t, err)

	// Encrypt
	encData, encMeta, err := config.encryptFields(ctx, "UserCreated", data, Metadata{})
	require.NoError(t, err)

	// Verify encrypted data is different
	assert.NotEqual(t, data, encData)

	// Verify metadata is set
	assert.True(t, IsEncrypted(encMeta))
	assert.Equal(t, "master-1", GetEncryptionKeyID(encMeta))
	fields := GetEncryptedFields(encMeta)
	assert.ElementsMatch(t, []string{"email", "phone"}, fields)

	// Verify name is NOT encrypted
	var encJSON map[string]interface{}
	require.NoError(t, json.Unmarshal(encData, &encJSON))
	assert.Equal(t, "John Doe", encJSON["name"])
	assert.NotEqual(t, "john@example.com", encJSON["email"])
	assert.NotEqual(t, "+1234567890", encJSON["phone"])

	// Decrypt
	decData, err := config.decryptFields(ctx, "UserCreated", encData, encMeta)
	require.NoError(t, err)

	var decJSON map[string]interface{}
	require.NoError(t, json.Unmarshal(decData, &decJSON))
	assert.Equal(t, "John Doe", decJSON["name"])
	assert.Equal(t, "john@example.com", decJSON["email"])
	assert.Equal(t, "+1234567890", decJSON["phone"])
}

func TestFieldEncryptionConfig_NestedFields(t *testing.T) {
	provider := testProvider(t, "master-1")
	defer provider.Close()

	config := NewFieldEncryptionConfig(
		WithEncryptionProvider(provider),
		WithDefaultKeyID("master-1"),
		WithEncryptedFields("AddressUpdated", "address.street", "address.zip"),
	)

	ctx := context.Background()

	original := map[string]interface{}{
		"name": "John",
		"address": map[string]interface{}{
			"street": "123 Main St",
			"city":   "Springfield",
			"zip":    "12345",
		},
	}
	data, err := json.Marshal(original)
	require.NoError(t, err)

	encData, encMeta, err := config.encryptFields(ctx, "AddressUpdated", data, Metadata{})
	require.NoError(t, err)

	// Verify nested fields are encrypted
	var encJSON map[string]interface{}
	require.NoError(t, json.Unmarshal(encData, &encJSON))
	addr := encJSON["address"].(map[string]interface{})
	assert.Equal(t, "Springfield", addr["city"]) // not encrypted
	assert.NotEqual(t, "123 Main St", addr["street"])
	assert.NotEqual(t, "12345", addr["zip"])

	// Decrypt
	decData, err := config.decryptFields(ctx, "AddressUpdated", encData, encMeta)
	require.NoError(t, err)

	var decJSON map[string]interface{}
	require.NoError(t, json.Unmarshal(decData, &decJSON))
	decAddr := decJSON["address"].(map[string]interface{})
	assert.Equal(t, "123 Main St", decAddr["street"])
	assert.Equal(t, "Springfield", decAddr["city"])
	assert.Equal(t, "12345", decAddr["zip"])
}

func TestFieldEncryptionConfig_MissingField(t *testing.T) {
	provider := testProvider(t, "master-1")
	defer provider.Close()

	config := NewFieldEncryptionConfig(
		WithEncryptionProvider(provider),
		WithDefaultKeyID("master-1"),
		WithEncryptedFields("UserCreated", "email", "ssn"), // ssn not present
	)

	ctx := context.Background()

	original := map[string]interface{}{
		"name":  "John",
		"email": "john@example.com",
	}
	data, err := json.Marshal(original)
	require.NoError(t, err)

	encData, encMeta, err := config.encryptFields(ctx, "UserCreated", data, Metadata{})
	require.NoError(t, err)

	// Only email should be in encrypted fields list
	fields := GetEncryptedFields(encMeta)
	assert.Equal(t, []string{"email"}, fields)

	// Decrypt should work
	decData, err := config.decryptFields(ctx, "UserCreated", encData, encMeta)
	require.NoError(t, err)

	var decJSON map[string]interface{}
	require.NoError(t, json.Unmarshal(decData, &decJSON))
	assert.Equal(t, "john@example.com", decJSON["email"])
}

func TestFieldEncryptionConfig_NoEncryptedFields(t *testing.T) {
	provider := testProvider(t, "master-1")
	defer provider.Close()

	config := NewFieldEncryptionConfig(
		WithEncryptionProvider(provider),
		WithDefaultKeyID("master-1"),
		WithEncryptedFields("UserCreated", "email"),
	)

	ctx := context.Background()
	data := []byte(`{"name":"John"}`)

	// No fields configured for this event type
	result, meta, err := config.encryptFields(ctx, "OrderCreated", data, Metadata{})
	require.NoError(t, err)
	assert.Equal(t, data, result)
	assert.False(t, IsEncrypted(meta))
}

func TestFieldEncryptionConfig_TenantKeyResolver(t *testing.T) {
	key1 := make([]byte, 32)
	key2 := make([]byte, 32)
	_, _ = rand.Read(key1)
	_, _ = rand.Read(key2)

	provider := local.New(
		local.WithKey("tenant-A-key", key1),
		local.WithKey("tenant-B-key", key2),
	)
	defer provider.Close()

	config := NewFieldEncryptionConfig(
		WithEncryptionProvider(provider),
		WithDefaultKeyID("tenant-A-key"),
		WithEncryptedFields("UserCreated", "email"),
		WithTenantKeyResolver(func(tenantID string) string {
			return "tenant-" + tenantID + "-key"
		}),
	)

	ctx := context.Background()
	data := []byte(`{"email":"test@example.com"}`)

	// Encrypt with tenant A
	encData, encMeta, err := config.encryptFields(ctx, "UserCreated", data, Metadata{TenantID: "A"})
	require.NoError(t, err)
	assert.Equal(t, "tenant-A-key", GetEncryptionKeyID(encMeta))

	// Decrypt with tenant A key
	decData, err := config.decryptFields(ctx, "UserCreated", encData, encMeta)
	require.NoError(t, err)
	assert.Contains(t, string(decData), "test@example.com")

	// Encrypt with tenant B
	encData2, encMeta2, err := config.encryptFields(ctx, "UserCreated", data, Metadata{TenantID: "B"})
	require.NoError(t, err)
	assert.Equal(t, "tenant-B-key", GetEncryptionKeyID(encMeta2))

	// Decrypt with tenant B key
	decData2, err := config.decryptFields(ctx, "UserCreated", encData2, encMeta2)
	require.NoError(t, err)
	assert.Contains(t, string(decData2), "test@example.com")
}

func TestFieldEncryptionConfig_CryptoShredding(t *testing.T) {
	key := make([]byte, 32)
	_, _ = rand.Read(key)

	provider := local.New(local.WithKey("master-1", key))
	defer provider.Close()

	var handlerCalled bool
	config := NewFieldEncryptionConfig(
		WithEncryptionProvider(provider),
		WithDefaultKeyID("master-1"),
		WithEncryptedFields("UserCreated", "email"),
		WithDecryptionErrorHandler(func(err error, eventType string, metadata Metadata) error {
			handlerCalled = true
			// Return nil to indicate graceful degradation
			return nil
		}),
	)

	ctx := context.Background()
	data := []byte(`{"email":"test@example.com","name":"John"}`)

	// Encrypt
	encData, encMeta, err := config.encryptFields(ctx, "UserCreated", data, Metadata{})
	require.NoError(t, err)

	// Revoke key (crypto-shredding)
	err = provider.RevokeKey("master-1")
	require.NoError(t, err)

	// Decrypt should return encrypted data (handler returns nil)
	decData, err := config.decryptFields(ctx, "UserCreated", encData, encMeta)
	require.NoError(t, err)
	assert.True(t, handlerCalled)
	assert.Equal(t, encData, decData) // Data returned as-is (still encrypted)
}

func TestFieldEncryptionConfig_NoKeyID(t *testing.T) {
	provider := testProvider(t, "master-1")
	defer provider.Close()

	config := NewFieldEncryptionConfig(
		WithEncryptionProvider(provider),
		// No default key ID set
		WithEncryptedFields("UserCreated", "email"),
	)

	ctx := context.Background()
	data := []byte(`{"email":"test@example.com"}`)

	_, _, err := config.encryptFields(ctx, "UserCreated", data, Metadata{})
	require.Error(t, err)
	assert.ErrorIs(t, err, encryption.ErrEncryptionFailed)
}

func TestMetadataHelpers(t *testing.T) {
	t.Run("IsEncrypted", func(t *testing.T) {
		assert.False(t, IsEncrypted(Metadata{}))
		assert.False(t, IsEncrypted(Metadata{Custom: map[string]string{"foo": "bar"}}))

		m := Metadata{Custom: map[string]string{encryptedFieldsKey: `["email"]`}}
		assert.True(t, IsEncrypted(m))
	})

	t.Run("GetEncryptionKeyID", func(t *testing.T) {
		assert.Equal(t, "", GetEncryptionKeyID(Metadata{}))

		m := Metadata{Custom: map[string]string{encryptionKeyIDKey: "my-key"}}
		assert.Equal(t, "my-key", GetEncryptionKeyID(m))
	})

	t.Run("GetEncryptedFields", func(t *testing.T) {
		assert.Nil(t, GetEncryptedFields(Metadata{}))

		m := Metadata{Custom: map[string]string{encryptedFieldsKey: `["email","phone"]`}}
		assert.Equal(t, []string{"email", "phone"}, GetEncryptedFields(m))

		// Invalid JSON
		m2 := Metadata{Custom: map[string]string{encryptedFieldsKey: "invalid"}}
		assert.Nil(t, GetEncryptedFields(m2))
	})
}

func TestFieldEncryptionConfig_NumericFieldValues(t *testing.T) {
	provider := testProvider(t, "master-1")
	defer provider.Close()

	config := NewFieldEncryptionConfig(
		WithEncryptionProvider(provider),
		WithDefaultKeyID("master-1"),
		WithEncryptedFields("AccountCreated", "balance", "ssn"),
	)

	ctx := context.Background()

	original := map[string]interface{}{
		"name":    "John",
		"balance": 1234.56,
		"ssn":     "123-45-6789",
	}
	data, err := json.Marshal(original)
	require.NoError(t, err)

	encData, encMeta, err := config.encryptFields(ctx, "AccountCreated", data, Metadata{})
	require.NoError(t, err)

	decData, err := config.decryptFields(ctx, "AccountCreated", encData, encMeta)
	require.NoError(t, err)

	var decJSON map[string]interface{}
	require.NoError(t, json.Unmarshal(decData, &decJSON))
	assert.Equal(t, 1234.56, decJSON["balance"])
	assert.Equal(t, "123-45-6789", decJSON["ssn"])
}

func TestDecryptFields_UnencryptedData(t *testing.T) {
	provider := testProvider(t, "master-1")
	defer provider.Close()

	config := NewFieldEncryptionConfig(
		WithEncryptionProvider(provider),
		WithDefaultKeyID("master-1"),
	)

	ctx := context.Background()
	data := []byte(`{"name":"John"}`)

	// No encryption metadata → data returned as-is
	result, err := config.decryptFields(ctx, "UserCreated", data, Metadata{})
	require.NoError(t, err)
	assert.Equal(t, data, result)
}
