package mink

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/AshkanYarmoradi/go-mink/encryption"
)

// Metadata keys for field-level encryption.
// The $ prefix marks them as system-managed, preventing collisions with user metadata.
const (
	encryptedFieldsKey     = "$encrypted_fields"
	encryptionKeyIDKey     = "$encryption_key_id"
	encryptedDEKKey        = "$encrypted_dek"
	encryptionAlgorithmKey = "$encryption_algorithm"
	encryptionAlgorithm    = "AES-256-GCM"
)

// FieldEncryptionConfig configures per-event-type field encryption.
// It uses envelope encryption: a data encryption key (DEK) is generated per event,
// encrypted with the master key, and stored in metadata. Individual fields are
// encrypted locally with the DEK for performance.
type FieldEncryptionConfig struct {
	provider          encryption.Provider
	fields            map[string][]string                                        // eventType → field paths
	defaultKeyID      string                                                     // default master key ID
	tenantKeyResolver func(tenantID string) string                               // per-tenant key support
	onDecryptionError func(err error, eventType string, metadata Metadata) error // crypto-shredding handler
}

// EncryptionOption configures a FieldEncryptionConfig.
type EncryptionOption func(*FieldEncryptionConfig)

// WithEncryptionProvider sets the encryption provider.
func WithEncryptionProvider(p encryption.Provider) EncryptionOption {
	return func(c *FieldEncryptionConfig) {
		c.provider = p
	}
}

// WithDefaultKeyID sets the default master key ID used when no tenant key resolver
// is configured or when the tenant ID is empty.
func WithDefaultKeyID(keyID string) EncryptionOption {
	return func(c *FieldEncryptionConfig) {
		c.defaultKeyID = keyID
	}
}

// WithEncryptedFields registers field paths to encrypt for a given event type.
// Field paths are dot-separated JSON field names (e.g., "email", "address.street").
func WithEncryptedFields(eventType string, fields ...string) EncryptionOption {
	return func(c *FieldEncryptionConfig) {
		if c.fields == nil {
			c.fields = make(map[string][]string)
		}
		c.fields[eventType] = append(c.fields[eventType], fields...)
	}
}

// WithTenantKeyResolver sets a function that maps tenant IDs to master key IDs.
// This enables per-tenant encryption keys for multi-tenant applications.
func WithTenantKeyResolver(resolver func(tenantID string) string) EncryptionOption {
	return func(c *FieldEncryptionConfig) {
		c.tenantKeyResolver = resolver
	}
}

// WithDecryptionErrorHandler sets a handler for decryption errors.
// This is used for crypto-shredding: when a key has been deleted, the handler
// can return nil to skip the event or return a custom error.
// If the handler returns nil, the event data is returned as-is (still encrypted).
func WithDecryptionErrorHandler(handler func(err error, eventType string, metadata Metadata) error) EncryptionOption {
	return func(c *FieldEncryptionConfig) {
		c.onDecryptionError = handler
	}
}

// NewFieldEncryptionConfig creates a new FieldEncryptionConfig with the given options.
func NewFieldEncryptionConfig(opts ...EncryptionOption) *FieldEncryptionConfig {
	c := &FieldEncryptionConfig{
		fields: make(map[string][]string),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// HasEncryptedFields reports whether any fields are configured for encryption
// for the given event type.
func (c *FieldEncryptionConfig) HasEncryptedFields(eventType string) bool {
	return len(c.fields[eventType]) > 0
}

// resolveKeyID determines the master key ID for a given metadata context.
// It checks the tenant key resolver first, then falls back to the default key.
func (c *FieldEncryptionConfig) resolveKeyID(metadata Metadata) string {
	if c.tenantKeyResolver != nil && metadata.TenantID != "" {
		if keyID := c.tenantKeyResolver(metadata.TenantID); keyID != "" {
			return keyID
		}
	}
	return c.defaultKeyID
}

// encryptFields encrypts the configured fields in the serialized event data.
// It uses envelope encryption: generates a DEK, encrypts fields with it, and
// stores the encrypted DEK in metadata.
func (c *FieldEncryptionConfig) encryptFields(ctx context.Context, eventType string, data []byte, metadata Metadata) ([]byte, Metadata, error) {
	fieldPaths := c.fields[eventType]
	if len(fieldPaths) == 0 {
		return data, metadata, nil
	}

	keyID := c.resolveKeyID(metadata)
	if keyID == "" {
		return nil, metadata, encryption.NewEncryptionError("", "", fmt.Errorf("no encryption key ID configured"))
	}

	// Generate a DEK for this event
	dk, err := c.provider.GenerateDataKey(ctx, keyID)
	if err != nil {
		return nil, metadata, err
	}
	defer encryption.ClearBytes(dk.Plaintext)

	// Parse JSON data
	var jsonData map[string]interface{}
	if err := json.Unmarshal(data, &jsonData); err != nil {
		return nil, metadata, encryption.NewEncryptionError(keyID, "", fmt.Errorf("failed to parse event data: %w", err))
	}

	// Encrypt each field
	var encryptedFieldNames []string
	for _, fieldPath := range fieldPaths {
		encrypted, err := encryptJSONField(jsonData, fieldPath, dk.Plaintext)
		if err != nil {
			return nil, metadata, encryption.NewEncryptionError(keyID, fieldPath, err)
		}
		if encrypted {
			encryptedFieldNames = append(encryptedFieldNames, fieldPath)
		}
	}

	if len(encryptedFieldNames) == 0 {
		return data, metadata, nil
	}

	// Re-serialize
	encryptedData, err := json.Marshal(jsonData)
	if err != nil {
		return nil, metadata, encryption.NewEncryptionError(keyID, "", fmt.Errorf("failed to serialize encrypted data: %w", err))
	}

	// Store encryption metadata
	fieldsJSON, _ := json.Marshal(encryptedFieldNames)
	metadata = metadata.WithCustom(encryptedFieldsKey, string(fieldsJSON))
	metadata = metadata.WithCustom(encryptionKeyIDKey, dk.KeyID)
	metadata = metadata.WithCustom(encryptedDEKKey, base64.StdEncoding.EncodeToString(dk.Ciphertext))
	metadata = metadata.WithCustom(encryptionAlgorithmKey, encryptionAlgorithm)

	return encryptedData, metadata, nil
}

// decryptFields decrypts fields in the serialized event data using the DEK from metadata.
func (c *FieldEncryptionConfig) decryptFields(ctx context.Context, eventType string, data []byte, metadata Metadata) ([]byte, error) {
	if !IsEncrypted(metadata) {
		return data, nil
	}

	keyID := GetEncryptionKeyID(metadata)
	encryptedDEK, err := base64.StdEncoding.DecodeString(metadata.Custom[encryptedDEKKey])
	if err != nil {
		return nil, encryption.NewDecryptionError(keyID, "", fmt.Errorf("failed to decode encrypted DEK: %w", err))
	}

	// Decrypt the DEK
	dekPlaintext, err := c.provider.DecryptDataKey(ctx, keyID, encryptedDEK)
	if err != nil {
		if c.onDecryptionError != nil {
			if handlerErr := c.onDecryptionError(err, eventType, metadata); handlerErr == nil {
				return data, nil // Handler says skip decryption (crypto-shredding)
			} else {
				return nil, handlerErr
			}
		}
		return nil, err
	}
	defer encryption.ClearBytes(dekPlaintext)

	// Get encrypted field names
	fieldNames := GetEncryptedFields(metadata)
	if len(fieldNames) == 0 {
		return data, nil
	}

	// Parse JSON data
	var jsonData map[string]interface{}
	if err := json.Unmarshal(data, &jsonData); err != nil {
		return nil, encryption.NewDecryptionError(keyID, "", fmt.Errorf("failed to parse event data: %w", err))
	}

	// Decrypt each field
	for _, fieldPath := range fieldNames {
		if err := decryptJSONField(jsonData, fieldPath, dekPlaintext); err != nil {
			return nil, encryption.NewDecryptionError(keyID, fieldPath, err)
		}
	}

	// Re-serialize
	decryptedData, err := json.Marshal(jsonData)
	if err != nil {
		return nil, encryption.NewDecryptionError(keyID, "", fmt.Errorf("failed to serialize decrypted data: %w", err))
	}

	return decryptedData, nil
}

// Metadata helper functions

// GetEncryptedFields extracts the list of encrypted field names from event metadata.
func GetEncryptedFields(m Metadata) []string {
	if m.Custom == nil {
		return nil
	}
	v, ok := m.Custom[encryptedFieldsKey]
	if !ok {
		return nil
	}
	var fields []string
	if err := json.Unmarshal([]byte(v), &fields); err != nil {
		return nil
	}
	return fields
}

// GetEncryptionKeyID extracts the encryption key ID from event metadata.
func GetEncryptionKeyID(m Metadata) string {
	if m.Custom == nil {
		return ""
	}
	return m.Custom[encryptionKeyIDKey]
}

// IsEncrypted reports whether the event has encrypted fields.
func IsEncrypted(m Metadata) bool {
	if m.Custom == nil {
		return false
	}
	_, ok := m.Custom[encryptedFieldsKey]
	return ok
}

// JSON field encryption helpers

// encryptJSONField encrypts a single field in a JSON object using AES-256-GCM.
// The field value is replaced with a base64-encoded ciphertext string.
// Returns true if the field was found and encrypted, false if not found.
func encryptJSONField(data map[string]interface{}, fieldPath string, key []byte) (bool, error) {
	parts := strings.SplitN(fieldPath, ".", 2)

	if len(parts) == 1 {
		// Leaf field
		val, ok := data[parts[0]]
		if !ok {
			return false, nil // Field not present, skip
		}

		// Serialize the value to JSON, then encrypt
		plaintext, err := json.Marshal(val)
		if err != nil {
			return false, fmt.Errorf("failed to marshal field value: %w", err)
		}

		ciphertext, err := encryption.AESGCMEncrypt(key, plaintext)
		if err != nil {
			return false, err
		}

		data[parts[0]] = base64.StdEncoding.EncodeToString(ciphertext)
		return true, nil
	}

	// Nested field — recurse into child object
	child, ok := data[parts[0]]
	if !ok {
		return false, nil
	}

	childMap, ok := child.(map[string]interface{})
	if !ok {
		return false, nil
	}

	return encryptJSONField(childMap, parts[1], key)
}

// decryptJSONField decrypts a single field in a JSON object.
func decryptJSONField(data map[string]interface{}, fieldPath string, key []byte) error {
	parts := strings.SplitN(fieldPath, ".", 2)

	if len(parts) == 1 {
		val, ok := data[parts[0]]
		if !ok {
			return nil // Field not present
		}

		encoded, ok := val.(string)
		if !ok {
			return nil // Not a string (wasn't encrypted)
		}

		ciphertext, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return fmt.Errorf("failed to decode field: %w", err)
		}

		plaintext, err := encryption.AESGCMDecrypt(key, ciphertext)
		if err != nil {
			return err
		}

		// Restore the original JSON value
		var restored interface{}
		if err := json.Unmarshal(plaintext, &restored); err != nil {
			return fmt.Errorf("failed to unmarshal decrypted field: %w", err)
		}

		data[parts[0]] = restored
		return nil
	}

	// Nested field
	child, ok := data[parts[0]]
	if !ok {
		return nil
	}

	childMap, ok := child.(map[string]interface{})
	if !ok {
		return nil
	}

	return decryptJSONField(childMap, parts[1], key)
}
