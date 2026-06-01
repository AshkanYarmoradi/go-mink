package mink

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"go-mink.dev/encryption"
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
//
// Integrity model: each field's ciphertext is bound (via AES-GCM AAD) to its
// stream ID and field name, so it cannot be relocated to a different stream/field
// and still decrypt. Event metadata itself (correlation/causation/tenant IDs and
// the $encryption_* markers) is NOT authenticated by the library — tampering with
// it causes decryption to fail closed (never a plaintext leak), but guaranteeing
// metadata integrity is the storage layer's responsibility (the event store is
// append-only and access-controlled).
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
func (c *FieldEncryptionConfig) encryptFields(ctx context.Context, streamID, eventType string, data []byte, metadata Metadata) ([]byte, Metadata, error) {
	fieldPaths := c.fields[eventType]
	if len(fieldPaths) == 0 {
		return data, metadata, nil
	}

	if c.provider == nil {
		return nil, metadata, encryption.NewEncryptionError("", "", fmt.Errorf("encryption provider not configured: use WithEncryptionProvider option"))
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

	// Parse JSON data — field-level encryption requires JSON-encoded event bodies
	var jsonData map[string]interface{}
	if err := json.Unmarshal(data, &jsonData); err != nil {
		return nil, metadata, encryption.NewEncryptionError(keyID, "", fmt.Errorf("field-level encryption requires JSON-encoded event data; failed to parse as JSON (incompatible with non-JSON serializers like MessagePack/Protobuf): %w", err))
	}

	// Encrypt each field
	var encryptedFieldNames []string
	for _, fieldPath := range fieldPaths {
		encrypted, err := encryptJSONField(jsonData, fieldPath, streamID, dk.Plaintext)
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
func (c *FieldEncryptionConfig) decryptFields(ctx context.Context, streamID, eventType string, data []byte, metadata Metadata) ([]byte, error) {
	if !IsEncrypted(metadata) {
		return data, nil
	}

	if c.provider == nil {
		return nil, encryption.NewDecryptionError("", "", fmt.Errorf("encryption provider not configured: use WithEncryptionProvider option"))
	}

	keyID := GetEncryptionKeyID(metadata)

	// Algorithm agility: honor the algorithm recorded at encryption time.
	// Absent key means a legacy event written before the algorithm was stamped;
	// those are always AES-256-GCM, so default to it for backward compatibility.
	// A present-but-unsupported value must NOT be silently decrypted with the
	// hardcoded algorithm — fail closed instead.
	if alg := GetEncryptionAlgorithm(metadata); alg != "" && alg != encryptionAlgorithm {
		return nil, encryption.NewDecryptionError(keyID, "", fmt.Errorf("unsupported algorithm %q (only %q is supported)", alg, encryptionAlgorithm))
	}
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
		if err := decryptJSONField(jsonData, fieldPath, streamID, dekPlaintext); err != nil {
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

// GetEncryptionAlgorithm extracts the encryption algorithm recorded in event
// metadata. It returns an empty string for legacy events written before the
// algorithm was stamped; callers should treat an empty value as the default
// AES-256-GCM algorithm.
func GetEncryptionAlgorithm(m Metadata) string {
	if m.Custom == nil {
		return ""
	}
	return m.Custom[encryptionAlgorithmKey]
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

// fieldAAD builds the AES-GCM additional authenticated data for a field, binding
// the ciphertext to its stream and full field path so it cannot be relocated to a
// different stream (or, within an event, a different field — including a sibling
// nested field that shares the same leaf name) and still decrypt. An empty
// streamID yields the legacy field-path-only AAD for backward compatibility.
func fieldAAD(streamID, fieldPath string) []byte {
	if streamID == "" {
		return []byte(fieldPath)
	}
	return []byte(streamID + "\x00" + fieldPath)
}

// encryptJSONField encrypts a single field in a JSON object using AES-256-GCM.
// The field value is replaced with a base64-encoded ciphertext string.
// Returns true if the field was found and encrypted, false if not found.
func encryptJSONField(data map[string]interface{}, fieldPath, streamID string, key []byte) (bool, error) {
	return encryptJSONFieldWithPath(data, fieldPath, fieldPath, streamID, key)
}

// encryptJSONFieldWithPath walks fieldPath into nested objects and encrypts the
// leaf value, binding the ciphertext to fullPath (the complete dotted path from
// the event root) so it cannot be relocated to a different field and still
// decrypt. fullPath stays constant across the recursion while fieldPath shrinks.
func encryptJSONFieldWithPath(data map[string]interface{}, fieldPath, fullPath, streamID string, key []byte) (bool, error) {
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

		ciphertext, err := encryption.AESGCMEncrypt(key, plaintext, fieldAAD(streamID, fullPath))
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

	return encryptJSONFieldWithPath(childMap, parts[1], fullPath, streamID, key)
}

// decryptJSONField decrypts a single field in a JSON object.
func decryptJSONField(data map[string]interface{}, fieldPath, streamID string, key []byte) error {
	return decryptJSONFieldWithPath(data, fieldPath, fieldPath, streamID, key)
}

// decryptJSONFieldWithPath mirrors encryptJSONFieldWithPath, walking into nested
// objects and decrypting the leaf using full-path-bound AAD (with backward-
// compatible fallbacks for events written by earlier versions).
func decryptJSONFieldWithPath(data map[string]interface{}, fieldPath, fullPath, streamID string, key []byte) error {
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

		plaintext, err := decryptFieldValue(key, ciphertext, streamID, fullPath, parts[0])
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

	return decryptJSONFieldWithPath(childMap, parts[1], fullPath, streamID, key)
}

// decryptFieldValue decrypts a field's ciphertext using the current AAD (stream +
// full field path) and falls back to AAD formats used by earlier versions so that
// events written before full-path binding still decrypt. Candidates are tried in
// order and de-duplicated; for a top-level field they reduce to exactly the prior
// behavior (stream+name, then name-only).
func decryptFieldValue(key, ciphertext []byte, streamID, fullPath, leaf string) ([]byte, error) {
	candidates := [][]byte{
		fieldAAD(streamID, fullPath), // current: stream + full path
		fieldAAD(streamID, leaf),     // earlier: stream + leaf segment only
		[]byte(fullPath),             // no stream binding: full path
		[]byte(leaf),                 // legacy: leaf segment only
	}

	seen := make(map[string]struct{}, len(candidates))
	var firstErr error
	for _, aad := range candidates {
		if _, dup := seen[string(aad)]; dup {
			continue
		}
		seen[string(aad)] = struct{}{}

		plaintext, err := encryption.AESGCMDecrypt(key, ciphertext, aad)
		if err == nil {
			return plaintext, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	return nil, firstErr
}
