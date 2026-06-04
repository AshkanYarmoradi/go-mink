package mink

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go-mink.dev/encryption"
	"go-mink.dev/encryption/local"
)

func testProvider(t *testing.T, keyID string) *local.Provider {
	t.Helper()
	key := make([]byte, 32)
	_, err := rand.Read(key)
	require.NoError(t, err)
	p, err := local.New(local.WithKey(keyID, key))
	require.NoError(t, err)
	return p
}

func testEncConfig(t *testing.T, keyID string, opts ...EncryptionOption) (*local.Provider, *FieldEncryptionConfig) {
	t.Helper()
	provider := testProvider(t, keyID)
	t.Cleanup(func() { _ = provider.Close() })
	baseOpts := []EncryptionOption{
		WithEncryptionProvider(provider),
		WithDefaultKeyID(keyID),
	}
	return provider, NewFieldEncryptionConfig(append(baseOpts, opts...)...)
}

func TestEncryptDecryptJSONField_NestedRoundTrip(t *testing.T) {
	key := make([]byte, 32)
	_, err := rand.Read(key)
	require.NoError(t, err)
	const streamID = "Order-1"

	data := map[string]interface{}{
		"billing": map[string]interface{}{"city": "NYC"},
	}

	ok, err := encryptJSONField(data, "billing.city", streamID, key)
	require.NoError(t, err)
	require.True(t, ok)

	// The nested leaf is now a base64 ciphertext string, not the plaintext.
	billing := data["billing"].(map[string]interface{})
	require.IsType(t, "", billing["city"])
	require.NotEqual(t, "NYC", billing["city"])

	require.NoError(t, decryptJSONField(data, "billing.city", streamID, key))
	assert.Equal(t, "NYC", data["billing"].(map[string]interface{})["city"])
}

func TestDecryptJSONField_AADBindsFullPath_RejectsRelocatedNestedCiphertext(t *testing.T) {
	key := make([]byte, 32)
	_, err := rand.Read(key)
	require.NoError(t, err)
	const streamID = "Order-1"

	// Two sibling nested fields that share the same leaf name ("city").
	data := map[string]interface{}{
		"billing":  map[string]interface{}{"city": "NYC"},
		"shipping": map[string]interface{}{"city": "LA"},
	}
	_, err = encryptJSONField(data, "billing.city", streamID, key)
	require.NoError(t, err)
	_, err = encryptJSONField(data, "shipping.city", streamID, key)
	require.NoError(t, err)

	billing := data["billing"].(map[string]interface{})
	shipping := data["shipping"].(map[string]interface{})

	// Relocate shipping.city's ciphertext into billing.city. Because the AAD binds
	// the full path, decrypting it as billing.city must fail — when only the leaf
	// segment was authenticated this relocation would have silently succeeded.
	billing["city"] = shipping["city"]

	err = decryptJSONField(data, "billing.city", streamID, key)
	require.Error(t, err)
}

func TestFieldEncryptionConfig_HasEncryptedFields(t *testing.T) {
	config := NewFieldEncryptionConfig(
		WithEncryptedFields("UserCreated", "email", "phone"),
	)

	assert.True(t, config.HasEncryptedFields("UserCreated"))
	assert.False(t, config.HasEncryptedFields("OrderCreated"))
}

func TestFieldEncryptionConfig_EncryptDecryptFields(t *testing.T) {
	_, config := testEncConfig(t, "master-1", WithEncryptedFields("UserCreated", "email", "phone"))
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
	encData, encMeta, err := config.encryptFields(ctx, "test-stream", "UserCreated", data, Metadata{})
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
	decData, err := config.decryptFields(ctx, "test-stream", "UserCreated", encData, encMeta)
	require.NoError(t, err)

	var decJSON map[string]interface{}
	require.NoError(t, json.Unmarshal(decData, &decJSON))
	assert.Equal(t, "John Doe", decJSON["name"])
	assert.Equal(t, "john@example.com", decJSON["email"])
	assert.Equal(t, "+1234567890", decJSON["phone"])
}

func TestFieldEncryptionConfig_AADBindsStream(t *testing.T) {
	_, config := testEncConfig(t, "master-1", WithEncryptedFields("UserCreated", "email"))
	ctx := context.Background()
	data, err := json.Marshal(map[string]interface{}{"email": "a@b.com"})
	require.NoError(t, err)

	// Encrypt bound to stream "A".
	encData, encMeta, err := config.encryptFields(ctx, "stream-A", "UserCreated", data, Metadata{})
	require.NoError(t, err)

	// Same stream decrypts fine.
	dec, err := config.decryptFields(ctx, "stream-A", "UserCreated", encData, encMeta)
	require.NoError(t, err)
	var got map[string]interface{}
	require.NoError(t, json.Unmarshal(dec, &got))
	assert.Equal(t, "a@b.com", got["email"])

	// Relocating the same ciphertext+metadata to a different stream must fail.
	_, err = config.decryptFields(ctx, "stream-B", "UserCreated", encData, encMeta)
	require.Error(t, err, "ciphertext must not decrypt under a different stream ID")

	// Legacy events (encrypted before stream binding, AAD = field path only) must
	// still decrypt under any stream ID for backward compatibility.
	legacyData, legacyMeta, err := config.encryptFields(ctx, "", "UserCreated", data, Metadata{})
	require.NoError(t, err)
	dec2, err := config.decryptFields(ctx, "stream-Z", "UserCreated", legacyData, legacyMeta)
	require.NoError(t, err, "legacy field-only AAD must still decrypt")
	var got2 map[string]interface{}
	require.NoError(t, json.Unmarshal(dec2, &got2))
	assert.Equal(t, "a@b.com", got2["email"])
}

func TestFieldEncryptionConfig_NestedFields(t *testing.T) {
	_, config := testEncConfig(t, "master-1", WithEncryptedFields("AddressUpdated", "address.street", "address.zip"))
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

	encData, encMeta, err := config.encryptFields(ctx, "test-stream", "AddressUpdated", data, Metadata{})
	require.NoError(t, err)

	// Verify nested fields are encrypted
	var encJSON map[string]interface{}
	require.NoError(t, json.Unmarshal(encData, &encJSON))
	addr := encJSON["address"].(map[string]interface{})
	assert.Equal(t, "Springfield", addr["city"]) // not encrypted
	assert.NotEqual(t, "123 Main St", addr["street"])
	assert.NotEqual(t, "12345", addr["zip"])

	// Decrypt
	decData, err := config.decryptFields(ctx, "test-stream", "AddressUpdated", encData, encMeta)
	require.NoError(t, err)

	var decJSON map[string]interface{}
	require.NoError(t, json.Unmarshal(decData, &decJSON))
	decAddr := decJSON["address"].(map[string]interface{})
	assert.Equal(t, "123 Main St", decAddr["street"])
	assert.Equal(t, "Springfield", decAddr["city"])
	assert.Equal(t, "12345", decAddr["zip"])
}

func TestFieldEncryptionConfig_MissingField(t *testing.T) {
	_, config := testEncConfig(t, "master-1", WithEncryptedFields("UserCreated", "email", "ssn"))
	ctx := context.Background()

	original := map[string]interface{}{
		"name":  "John",
		"email": "john@example.com",
	}
	data, err := json.Marshal(original)
	require.NoError(t, err)

	encData, encMeta, err := config.encryptFields(ctx, "test-stream", "UserCreated", data, Metadata{})
	require.NoError(t, err)

	// Only email should be in encrypted fields list
	fields := GetEncryptedFields(encMeta)
	assert.Equal(t, []string{"email"}, fields)

	// Decrypt should work
	decData, err := config.decryptFields(ctx, "test-stream", "UserCreated", encData, encMeta)
	require.NoError(t, err)

	var decJSON map[string]interface{}
	require.NoError(t, json.Unmarshal(decData, &decJSON))
	assert.Equal(t, "john@example.com", decJSON["email"])
}

func TestFieldEncryptionConfig_NoEncryptedFields(t *testing.T) {
	_, config := testEncConfig(t, "master-1", WithEncryptedFields("UserCreated", "email"))
	ctx := context.Background()
	data := []byte(`{"name":"John"}`)

	// No fields configured for this event type
	result, meta, err := config.encryptFields(ctx, "test-stream", "OrderCreated", data, Metadata{})
	require.NoError(t, err)
	assert.Equal(t, data, result)
	assert.False(t, IsEncrypted(meta))
}

func TestFieldEncryptionConfig_TenantKeyResolver(t *testing.T) {
	key1 := make([]byte, 32)
	key2 := make([]byte, 32)
	_, _ = rand.Read(key1)
	_, _ = rand.Read(key2)

	provider, err := local.New(
		local.WithKey("tenant-A-key", key1),
		local.WithKey("tenant-B-key", key2),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = provider.Close() })

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
	encData, encMeta, err := config.encryptFields(ctx, "test-stream", "UserCreated", data, Metadata{TenantID: "A"})
	require.NoError(t, err)
	assert.Equal(t, "tenant-A-key", GetEncryptionKeyID(encMeta))

	// Decrypt with tenant A key
	decData, err := config.decryptFields(ctx, "test-stream", "UserCreated", encData, encMeta)
	require.NoError(t, err)
	assert.Contains(t, string(decData), "test@example.com")

	// Encrypt with tenant B
	encData2, encMeta2, err := config.encryptFields(ctx, "test-stream", "UserCreated", data, Metadata{TenantID: "B"})
	require.NoError(t, err)
	assert.Equal(t, "tenant-B-key", GetEncryptionKeyID(encMeta2))

	// Decrypt with tenant B key
	decData2, err := config.decryptFields(ctx, "test-stream", "UserCreated", encData2, encMeta2)
	require.NoError(t, err)
	assert.Contains(t, string(decData2), "test@example.com")
}

func TestFieldEncryptionConfig_CryptoShredding(t *testing.T) {
	var handlerCalled bool
	provider, config := testEncConfig(t, "master-1",
		WithEncryptedFields("UserCreated", "email"),
		WithDecryptionErrorHandler(func(err error, eventType string, metadata Metadata) error {
			handlerCalled = true
			return nil
		}),
	)
	ctx := context.Background()
	data := []byte(`{"email":"test@example.com","name":"John"}`)

	// Encrypt
	encData, encMeta, err := config.encryptFields(ctx, "test-stream", "UserCreated", data, Metadata{})
	require.NoError(t, err)

	// Revoke key (crypto-shredding)
	err = provider.RevokeKey("master-1")
	require.NoError(t, err)

	// Decrypt should return encrypted data (handler returns nil)
	decData, err := config.decryptFields(ctx, "test-stream", "UserCreated", encData, encMeta)
	require.NoError(t, err)
	assert.True(t, handlerCalled)
	assert.Equal(t, encData, decData) // Data returned as-is (still encrypted)
}

func TestFieldEncryptionConfig_NoKeyID(t *testing.T) {
	provider := testProvider(t, "master-1")
	t.Cleanup(func() { _ = provider.Close() })
	config := NewFieldEncryptionConfig(
		WithEncryptionProvider(provider),
		WithEncryptedFields("UserCreated", "email"),
	)
	ctx := context.Background()
	data := []byte(`{"email":"test@example.com"}`)

	_, _, err := config.encryptFields(ctx, "test-stream", "UserCreated", data, Metadata{})
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
	_, config := testEncConfig(t, "master-1", WithEncryptedFields("AccountCreated", "balance", "ssn"))
	ctx := context.Background()

	original := map[string]interface{}{
		"name":    "John",
		"balance": 1234.56,
		"ssn":     "123-45-6789",
	}
	data, err := json.Marshal(original)
	require.NoError(t, err)

	encData, encMeta, err := config.encryptFields(ctx, "test-stream", "AccountCreated", data, Metadata{})
	require.NoError(t, err)

	decData, err := config.decryptFields(ctx, "test-stream", "AccountCreated", encData, encMeta)
	require.NoError(t, err)

	var decJSON map[string]interface{}
	require.NoError(t, json.Unmarshal(decData, &decJSON))
	assert.Equal(t, 1234.56, decJSON["balance"])
	assert.Equal(t, "123-45-6789", decJSON["ssn"])
}

func TestDecryptFields_UnencryptedData(t *testing.T) {
	_, config := testEncConfig(t, "master-1")
	ctx := context.Background()
	data := []byte(`{"name":"John"}`)

	// No encryption metadata → data returned as-is
	result, err := config.decryptFields(ctx, "test-stream", "UserCreated", data, Metadata{})
	require.NoError(t, err)
	assert.Equal(t, data, result)
}

func TestGetEncryptionAlgorithm(t *testing.T) {
	assert.Equal(t, "", GetEncryptionAlgorithm(Metadata{}))
	assert.Equal(t, "", GetEncryptionAlgorithm(Metadata{Custom: map[string]string{"foo": "bar"}}))

	m := Metadata{Custom: map[string]string{encryptionAlgorithmKey: "AES-256-GCM"}}
	assert.Equal(t, "AES-256-GCM", GetEncryptionAlgorithm(m))
}

func TestEncryptFields_StampsAlgorithm(t *testing.T) {
	_, config := testEncConfig(t, "master-1", WithEncryptedFields("UserCreated", "email"))
	ctx := context.Background()
	data := []byte(`{"email":"john@example.com"}`)

	_, encMeta, err := config.encryptFields(ctx, "test-stream", "UserCreated", data, Metadata{})
	require.NoError(t, err)

	// Encryption must record the algorithm so decrypt can validate it.
	assert.Equal(t, encryptionAlgorithm, GetEncryptionAlgorithm(encMeta))
}

func TestDecryptFields_UnsupportedAlgorithm(t *testing.T) {
	_, config := testEncConfig(t, "master-1", WithEncryptedFields("UserCreated", "email"))
	ctx := context.Background()
	data := []byte(`{"email":"john@example.com"}`)

	// Encrypt normally, then tamper with the recorded algorithm.
	encData, encMeta, err := config.encryptFields(ctx, "test-stream", "UserCreated", data, Metadata{})
	require.NoError(t, err)

	encMeta = encMeta.WithCustom(encryptionAlgorithmKey, "AES-128-CBC")

	// Decrypt must fail closed rather than silently using the hardcoded algorithm.
	_, err = config.decryptFields(ctx, "test-stream", "UserCreated", encData, encMeta)
	require.Error(t, err)
	assert.ErrorIs(t, err, encryption.ErrDecryptionFailed)
	assert.Contains(t, err.Error(), "unsupported algorithm")
	assert.Contains(t, err.Error(), "AES-128-CBC")
}

func TestDecryptFields_LegacyMissingAlgorithm(t *testing.T) {
	_, config := testEncConfig(t, "master-1", WithEncryptedFields("UserCreated", "email"))
	ctx := context.Background()
	data := []byte(`{"email":"john@example.com"}`)

	// Encrypt, then drop the algorithm key to simulate a legacy event written
	// before the algorithm was stamped. Decryption must still succeed (default
	// to AES-256-GCM for backward compatibility).
	encData, encMeta, err := config.encryptFields(ctx, "test-stream", "UserCreated", data, Metadata{})
	require.NoError(t, err)

	delete(encMeta.Custom, encryptionAlgorithmKey)
	require.Equal(t, "", GetEncryptionAlgorithm(encMeta))

	decData, err := config.decryptFields(ctx, "test-stream", "UserCreated", encData, encMeta)
	require.NoError(t, err)
	assert.Contains(t, string(decData), "john@example.com")
}

func TestDecryptFields_ExplicitSupportedAlgorithm(t *testing.T) {
	_, config := testEncConfig(t, "master-1", WithEncryptedFields("UserCreated", "email"))
	ctx := context.Background()
	data := []byte(`{"email":"john@example.com"}`)

	// A normal round-trip (algorithm present and supported) still works.
	encData, encMeta, err := config.encryptFields(ctx, "test-stream", "UserCreated", data, Metadata{})
	require.NoError(t, err)
	require.Equal(t, encryptionAlgorithm, GetEncryptionAlgorithm(encMeta))

	decData, err := config.decryptFields(ctx, "test-stream", "UserCreated", encData, encMeta)
	require.NoError(t, err)
	assert.Contains(t, string(decData), "john@example.com")
}

// --- Error path tests for 95%+ coverage ---

func TestEncryptFields_GenerateDataKeyError(t *testing.T) {
	// Use a provider with no keys to trigger key-not-found on GenerateDataKey
	provider, err := local.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = provider.Close() })
	config := NewFieldEncryptionConfig(
		WithEncryptionProvider(provider),
		WithDefaultKeyID("nonexistent"),
		WithEncryptedFields("UserCreated", "email"),
	)

	ctx := context.Background()
	data := []byte(`{"email":"test@example.com"}`)

	_, _, encErr := config.encryptFields(ctx, "test-stream", "UserCreated", data, Metadata{})
	require.Error(t, encErr)
	assert.ErrorIs(t, encErr, encryption.ErrKeyNotFound)
}

func TestEncryptFields_InvalidJSON(t *testing.T) {
	_, config := testEncConfig(t, "master-1", WithEncryptedFields("UserCreated", "email"))
	ctx := context.Background()
	data := []byte(`{invalid json}`)

	_, _, err := config.encryptFields(ctx, "test-stream", "UserCreated", data, Metadata{})
	require.Error(t, err)
	assert.ErrorIs(t, err, encryption.ErrEncryptionFailed)
	assert.Contains(t, err.Error(), "field-level encryption requires JSON-encoded event data")
}

func TestEncryptFields_AllFieldsMissing(t *testing.T) {
	_, config := testEncConfig(t, "master-1", WithEncryptedFields("UserCreated", "ssn", "dob"))
	ctx := context.Background()
	data := []byte(`{"name":"John","email":"john@example.com"}`)

	// When no configured fields exist in data, returns original data unmodified
	result, meta, err := config.encryptFields(ctx, "test-stream", "UserCreated", data, Metadata{})
	require.NoError(t, err)
	assert.Equal(t, data, result)
	assert.False(t, IsEncrypted(meta))
}

func TestDecryptFields_InvalidBase64DEK(t *testing.T) {
	_, config := testEncConfig(t, "master-1")
	ctx := context.Background()
	data := []byte(`{"email":"encrypted-value"}`)

	// Metadata says encrypted but DEK is not valid base64
	meta := Metadata{Custom: map[string]string{
		encryptedFieldsKey: `["email"]`,
		encryptionKeyIDKey: "master-1",
		encryptedDEKKey:    "!!!not-base64!!!",
	}}

	_, err := config.decryptFields(ctx, "test-stream", "UserCreated", data, meta)
	require.Error(t, err)
	assert.ErrorIs(t, err, encryption.ErrDecryptionFailed)
	assert.Contains(t, err.Error(), "failed to decode encrypted DEK")
}

func TestDecryptFields_DecryptDataKeyError_HandlerReturnsError(t *testing.T) {
	handlerErr := fmt.Errorf("custom handler error")
	provider, config := testEncConfig(t, "master-1",
		WithEncryptedFields("UserCreated", "email"),
		WithDecryptionErrorHandler(func(err error, eventType string, metadata Metadata) error {
			return handlerErr
		}),
	)
	ctx := context.Background()
	data := []byte(`{"email":"test@example.com"}`)

	// Encrypt first
	encData, encMeta, err := config.encryptFields(ctx, "test-stream", "UserCreated", data, Metadata{})
	require.NoError(t, err)

	// Revoke key to cause DecryptDataKey failure
	err = provider.RevokeKey("master-1")
	require.NoError(t, err)

	// Handler returns non-nil error
	_, err = config.decryptFields(ctx, "test-stream", "UserCreated", encData, encMeta)
	require.Error(t, err)
	assert.Equal(t, handlerErr, err)
}

func TestDecryptFields_DecryptDataKeyError_NoHandler(t *testing.T) {
	provider, config := testEncConfig(t, "master-1", WithEncryptedFields("UserCreated", "email"))
	ctx := context.Background()
	data := []byte(`{"email":"test@example.com"}`)

	// Encrypt first
	encData, encMeta, err := config.encryptFields(ctx, "test-stream", "UserCreated", data, Metadata{})
	require.NoError(t, err)

	// Revoke key
	err = provider.RevokeKey("master-1")
	require.NoError(t, err)

	// Without handler, error is returned directly
	_, err = config.decryptFields(ctx, "test-stream", "UserCreated", encData, encMeta)
	require.Error(t, err)
	assert.ErrorIs(t, err, encryption.ErrKeyRevoked)
}

func TestDecryptFields_EmptyFieldNames(t *testing.T) {
	provider, config := testEncConfig(t, "master-1")
	ctx := context.Background()
	data := []byte(`{"name":"John"}`)

	// Encrypt a DEK properly
	dk, err := provider.GenerateDataKey(ctx, "master-1")
	require.NoError(t, err)

	// Metadata says encrypted but field list is empty
	meta := Metadata{Custom: map[string]string{
		encryptedFieldsKey: `[]`,
		encryptionKeyIDKey: "master-1",
		encryptedDEKKey:    base64.StdEncoding.EncodeToString(dk.Ciphertext),
	}}

	// Should return data as-is when no field names
	result, err := config.decryptFields(ctx, "test-stream", "UserCreated", data, meta)
	require.NoError(t, err)
	assert.Equal(t, data, result)
}

func TestDecryptFields_InvalidJSON(t *testing.T) {
	provider, config := testEncConfig(t, "master-1")
	ctx := context.Background()

	// Generate valid DEK
	dk, err := provider.GenerateDataKey(ctx, "master-1")
	require.NoError(t, err)

	// Invalid JSON body with valid encryption metadata
	meta := Metadata{Custom: map[string]string{
		encryptedFieldsKey: `["email"]`,
		encryptionKeyIDKey: "master-1",
		encryptedDEKKey:    base64.StdEncoding.EncodeToString(dk.Ciphertext),
	}}

	_, err = config.decryptFields(ctx, "test-stream", "UserCreated", []byte(`{invalid}`), meta)
	require.Error(t, err)
	assert.ErrorIs(t, err, encryption.ErrDecryptionFailed)
	assert.Contains(t, err.Error(), "failed to parse event data")
}

func TestDecryptFields_FieldDecryptError(t *testing.T) {
	provider, config := testEncConfig(t, "master-1")
	ctx := context.Background()

	// Generate valid DEK
	dk, err := provider.GenerateDataKey(ctx, "master-1")
	require.NoError(t, err)

	// Data with a field that has valid base64 but invalid ciphertext
	invalidCiphertext := base64.StdEncoding.EncodeToString([]byte("not-valid-aes-gcm-ciphertext-at-all-needs-padding"))
	data := []byte(fmt.Sprintf(`{"email":"%s"}`, invalidCiphertext))

	meta := Metadata{Custom: map[string]string{
		encryptedFieldsKey: `["email"]`,
		encryptionKeyIDKey: "master-1",
		encryptedDEKKey:    base64.StdEncoding.EncodeToString(dk.Ciphertext),
	}}

	_, err = config.decryptFields(ctx, "test-stream", "UserCreated", data, meta)
	require.Error(t, err)
	assert.ErrorIs(t, err, encryption.ErrDecryptionFailed)
}

func TestDecryptJSONField_InvalidBase64Value(t *testing.T) {
	key := make([]byte, 32)
	_, _ = rand.Read(key)

	data := map[string]interface{}{
		"email": "!!!not-base64!!!",
	}

	err := decryptJSONField(data, "email", "test-stream", key)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to decode field")
}

func TestDecryptJSONField_NonStringValue(t *testing.T) {
	key := make([]byte, 32)
	_, _ = rand.Read(key)

	data := map[string]interface{}{
		"count": 42, // Not a string, wasn't encrypted
	}

	// Should return nil (skip non-string values)
	err := decryptJSONField(data, "count", "test-stream", key)
	require.NoError(t, err)
	assert.Equal(t, 42, data["count"]) // unchanged
}

func TestDecryptJSONField_MissingField(t *testing.T) {
	key := make([]byte, 32)
	_, _ = rand.Read(key)

	data := map[string]interface{}{
		"name": "John",
	}

	// Should return nil for missing field
	err := decryptJSONField(data, "email", "test-stream", key)
	require.NoError(t, err)
}

func TestDecryptJSONField_NestedMissingParent(t *testing.T) {
	key := make([]byte, 32)
	_, _ = rand.Read(key)

	data := map[string]interface{}{
		"name": "John",
	}

	// Parent key "address" doesn't exist
	err := decryptJSONField(data, "address.street", "test-stream", key)
	require.NoError(t, err)
}

func TestDecryptJSONField_NestedNonMapChild(t *testing.T) {
	key := make([]byte, 32)
	_, _ = rand.Read(key)

	data := map[string]interface{}{
		"address": "not-a-map", // String instead of map
	}

	// Parent exists but isn't a map
	err := decryptJSONField(data, "address.street", "test-stream", key)
	require.NoError(t, err)
}

func TestEncryptJSONField_NestedMissingParent(t *testing.T) {
	key := make([]byte, 32)
	_, _ = rand.Read(key)

	data := map[string]interface{}{
		"name": "John",
	}

	encrypted, err := encryptJSONField(data, "address.street", "test-stream", key)
	require.NoError(t, err)
	assert.False(t, encrypted)
}

func TestEncryptJSONField_NestedNonMapChild(t *testing.T) {
	key := make([]byte, 32)
	_, _ = rand.Read(key)

	data := map[string]interface{}{
		"address": "not-a-map",
	}

	encrypted, err := encryptJSONField(data, "address.street", "test-stream", key)
	require.NoError(t, err)
	assert.False(t, encrypted)
}

func TestAesGCMEncrypt_InvalidKeyLength(t *testing.T) {
	// AES requires 16, 24, or 32 byte keys
	_, err := encryption.AESGCMEncrypt([]byte("short"), []byte("plaintext"), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create cipher")
}

func TestAesGCMDecrypt_InvalidKeyLength(t *testing.T) {
	_, err := encryption.AESGCMDecrypt([]byte("short"), []byte("some-ciphertext-long-enough-for-nonce"), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create cipher")
}

func TestAesGCMDecrypt_CiphertextTooShort(t *testing.T) {
	key := make([]byte, 32)
	_, _ = rand.Read(key)

	_, err := encryption.AESGCMDecrypt(key, []byte("short"), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ciphertext too short")
}

func TestWithEncryptedFields_NilMapInit(t *testing.T) {
	// Apply WithEncryptedFields directly to a config with nil fields map
	config := &FieldEncryptionConfig{}
	opt := WithEncryptedFields("UserCreated", "email")
	opt(config)
	assert.Equal(t, []string{"email"}, config.fields["UserCreated"])
}

func TestEncryptJSONField_MarshalError(t *testing.T) {
	key := make([]byte, 32)
	_, _ = rand.Read(key)

	data := map[string]interface{}{
		"email": make(chan int), // channels cannot be JSON-marshaled
	}

	_, err := encryptJSONField(data, "email", "test-stream", key)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to marshal field value")
}

func TestDecryptJSONField_InvalidDecryptedJSON(t *testing.T) {
	key := make([]byte, 32)
	_, _ = rand.Read(key)

	// Encrypt raw bytes that are NOT valid JSON
	ciphertext, err := encryption.AESGCMEncrypt(key, []byte("not-json"), []byte("email"))
	require.NoError(t, err)

	data := map[string]interface{}{
		"email": base64.StdEncoding.EncodeToString(ciphertext),
	}

	err = decryptJSONField(data, "email", "test-stream", key)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to unmarshal decrypted field")
}
