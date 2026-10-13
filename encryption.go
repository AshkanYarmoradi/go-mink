package mink

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"sort"
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
// and still decrypt. Every field listed in $encrypted_fields must still be present
// as sealed ciphertext at decrypt time: a listed field that is absent, or that no
// longer holds a string, means the stored row was altered after it was written
// (an AEAD bypass), so decryption fails closed with ErrDecryptionFailed naming the
// field instead of passing the substituted value through as plaintext. Event
// metadata itself (correlation/causation/tenant IDs and the $encryption_* markers)
// is NOT authenticated by the library — tampering with it causes decryption to
// fail closed (never a plaintext leak), but guaranteeing metadata integrity is the
// storage layer's responsibility (the event store is append-only and
// access-controlled).
//
// Numeric fidelity: the event body is round-tripped through a generic JSON object
// while fields are sealed and unsealed. Numbers are decoded as json.Number rather
// than float64, so integers beyond 2^53 — in sealed and unsealed fields alike —
// are re-emitted byte-for-byte.
//
// Overlapping paths: a configuration may list both a parent and one of its nested
// fields (e.g. "address" and "address.street"). Fields are sealed leaf-first
// (deepest paths first) and unsealed parent-first (shallowest first), whichever
// order they were configured or recorded in, so the nested ciphertext is a string
// again by the time its own turn comes and the event round-trips. Events written
// by earlier versions, which recorded the fields in configuration order, are
// unsealed in the same depth order and keep decrypting.
//
// Absent vs. mismatched fields: a configured path whose leaf, or whose parent
// object, is absent (or JSON null) is simply not encrypted — optional fields stay
// optional. A path whose parent IS present but is not a JSON object (a string,
// number, array, ...) is a mismatch between the configuration and the event's
// shape, and encryption fails with an EncryptionError naming the field instead of
// silently storing the event in plaintext.
type FieldEncryptionConfig struct {
	provider          encryption.Provider
	fields            map[string][]string                                        // eventType → field paths (configuration order, de-duplicated)
	sealOrder         map[string][]string                                        // eventType → field paths deepest-first; nil until finalize, only for types with nested paths
	defaultKeyID      string                                                     // default master key ID
	tenantKeyResolver func(id string) string                                     // maps a tenant OR subject id to a master key (see resolveKeyID); set via WithTenantKeyResolver / WithSubjectKeyResolver
	onDecryptionError func(err error, eventType string, metadata Metadata) error // crypto-shredding handler

	// requireKeyResolution (WithRequireKeyResolution) fails encryption with a
	// KeyResolutionError when the configured resolver yields no key for an event
	// instead of falling back to defaultKeyID.
	requireKeyResolution bool

	// configErr records a malformed field path found at construction (Validate),
	// surfaced by encryptFields for the affected event types. Constructors cannot
	// return an error without breaking the API, so it is deferred to first use.
	configErr map[string]error // eventType → first invalid-path error
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
//
// Whatever id resolves for an event — this default or a resolver's result — is
// stamped into its metadata ($encryption_key_id) and is what later decryption,
// revocation and erasure verification pass back to the provider. Use a stable,
// immutable identifier: for AWS KMS stamp the key ARN (or bare key id), never an
// "alias/..." name, because an alias can be re-pointed after the event was written
// and would then name a different key than the one the data was sealed under.
// The same applies to resolvers configured via WithTenantKeyResolver /
// WithSubjectKeyResolver.
func WithDefaultKeyID(keyID string) EncryptionOption {
	return func(c *FieldEncryptionConfig) {
		c.defaultKeyID = keyID
	}
}

// WithEncryptedFields registers field paths to encrypt for a given event type.
// Field paths are dot-separated JSON field names (e.g., "email", "address.street").
//
// Which master key wraps each event is chosen by resolveKeyID. When a SubjectTagger
// (WithSubjectTagger) is configured, an event with an empty Metadata.TenantID — every
// event persisted via SaveAggregate — keys off its first $subjects tag instead of the
// default key, so the one tagger that defines a subject's erasure footprint also
// selects its shred key; the two can never drift. Pair with WithTenantKeyResolver or
// WithSubjectKeyResolver to map that id to a per-subject key. See WithSharedKeyGuard
// (blast-radius guard) for the complementary detection of keys shared across subjects.
func WithEncryptedFields(eventType string, fields ...string) EncryptionOption {
	return func(c *FieldEncryptionConfig) {
		if c.fields == nil {
			c.fields = make(map[string][]string)
		}
		// De-duplicate (first occurrence wins) so a path listed twice is sealed once;
		// configuration order is otherwise preserved for Validate's error messages
		// and for the metadata's recorded field list.
		existing := c.fields[eventType]
		for _, f := range fields {
			if !containsString(existing, f) {
				existing = append(existing, f)
			}
		}
		c.fields[eventType] = existing
	}
}

// WithTenantKeyResolver sets a function that maps tenant IDs to master key IDs.
// This enables per-tenant encryption keys for multi-tenant applications.
//
// The resolver also drives the subject-tag fallback in resolveKeyID: when an event's
// Metadata.TenantID is empty (e.g. every SaveAggregate event) but a SubjectTagger has
// recorded a $subjects tag, the first tag is passed to this same resolver, giving
// aggregate PII a per-subject key. For per-subject setups prefer the WithSubjectKeyResolver
// alias, which reads by intent. Cross-link: WithSharedKeyGuard.
func WithTenantKeyResolver(resolver func(tenantID string) string) EncryptionOption {
	return func(c *FieldEncryptionConfig) {
		c.tenantKeyResolver = resolver
	}
}

// WithSubjectKeyResolver sets a function that maps a data-subject id to a master key ID,
// enabling per-subject encryption keys so a subject's PII can be individually
// crypto-shredded (GDPR Article 17) by revoking its key.
//
// It is a legibility alias of WithTenantKeyResolver: both configure the one resolver
// used by resolveKeyID, which selects the input as metadata.TenantID when present and
// otherwise the first $subjects tag (WithSubjectTagger) — so with a tagger configured,
// SaveAggregate events resolve to a per-subject key. Prefer this name for per-subject /
// GDPR setups and WithTenantKeyResolver for per-tenant ones. Because both set the same
// field, passing both is unambiguous: the last option applied wins.
func WithSubjectKeyResolver(resolver func(subjectID string) string) EncryptionOption {
	return func(c *FieldEncryptionConfig) {
		c.tenantKeyResolver = resolver
	}
}

// WithRequireKeyResolution makes key selection strict: when a tenant / subject key
// resolver is configured (WithTenantKeyResolver / WithSubjectKeyResolver) and it
// does not yield a key for an event — the event carries no Metadata.TenantID and no
// $subjects tag to resolve, or the resolver returns "" — encryption fails with an
// EncryptionError whose cause is a KeyResolutionError (errors.Is matches both
// ErrEncryptionFailed and ErrKeyResolutionFailed) instead of silently wrapping the
// event under the default key (WithDefaultKeyID).
//
// Use it in per-tenant / per-subject key setups where an event landing under the
// shared default key would silently escape its tenant's or subject's blast radius
// (its PII could no longer be crypto-shredded by revoking that one key). It has no
// effect when no resolver is configured: every event then uses the default key as
// before. Zero overhead when unset.
func WithRequireKeyResolution() EncryptionOption {
	return func(c *FieldEncryptionConfig) {
		c.requireKeyResolution = true
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
//
// Field paths are validated once here (see Validate): a malformed path is recorded
// rather than panicking, and the first append of an event type that carries one
// fails with an EncryptionError wrapping ErrInvalidEncryptedFieldPath. Call
// Validate at startup to surface it early.
func NewFieldEncryptionConfig(opts ...EncryptionOption) *FieldEncryptionConfig {
	c := &FieldEncryptionConfig{
		fields: make(map[string][]string),
	}
	for _, opt := range opts {
		opt(c)
	}
	c.finalize()
	return c
}

// Provider returns the configured encryption provider (may be nil).
func (c *FieldEncryptionConfig) Provider() encryption.Provider {
	return c.provider
}

// Validate reports whether every field path registered with WithEncryptedFields
// is well-formed: non-empty, with no empty dot-separated segment (no leading or
// trailing dot, no ".."). It returns nil for a valid configuration and otherwise an
// error wrapping ErrInvalidEncryptedFieldPath that names the first offending event
// type and path. Overlapping paths (a parent and one of its nested fields) are
// valid: sealing and unsealing are depth-ordered so they always round-trip.
//
// The same check runs in NewFieldEncryptionConfig; an invalid path is then also
// surfaced, wrapped in an EncryptionError, by the first append of that event type,
// so a misconfiguration can never silently store plaintext.
func (c *FieldEncryptionConfig) Validate() error {
	if c.configErr == nil && c.fields != nil {
		c.finalize()
	}
	types := make([]string, 0, len(c.configErr))
	for t := range c.configErr {
		types = append(types, t)
	}
	sort.Strings(types)
	for _, t := range types {
		return c.configErr[t]
	}
	return nil
}

// RevokeKey crypto-shreds keyID via the configured provider, implementing the
// erasure side of GDPR (right to be forgotten). It returns
// encryption.ErrRevocationUnsupported when the provider does not implement
// encryption.Revocable, and is idempotent for providers that do.
func (c *FieldEncryptionConfig) RevokeKey(keyID string) error {
	return encryption.Revoke(c.provider, keyID)
}

// IsRevoked reports whether keyID is revoked via the configured provider,
// returning encryption.ErrRevocationUnsupported when revocation is unavailable.
func (c *FieldEncryptionConfig) IsRevoked(keyID string) (bool, error) {
	return encryption.IsRevoked(c.provider, keyID)
}

// RevocationState reports keyID's fine-grained revocation state (NotRevoked /
// SoftRevoked / Revoked) via the configured provider. It lets erasure verification
// tell a still-recoverable soft-revocation from a permanent shred; providers without
// StatefulRevocable fall back to IsRevoked (so they never report SoftRevoked).
func (c *FieldEncryptionConfig) RevocationState(keyID string) (encryption.RevocationState, error) {
	return encryption.GetRevocationState(c.provider, keyID)
}

// HasEncryptedFields reports whether any fields are configured for encryption
// for the given event type.
func (c *FieldEncryptionConfig) HasEncryptedFields(eventType string) bool {
	return len(c.fields[eventType]) > 0
}

// resolveKeyID determines the master key ID for a given metadata context.
//
// Precedence (highest first):
//  1. metadata.TenantID, when non-empty — explicit per-tenant selection (unchanged).
//  2. the primary data subject — the first $subjects tag recorded by the configured
//     SubjectTagger before encryption (GetSubjectTags(metadata)[0]). This makes
//     SaveAggregate-persisted events, which carry an empty Metadata{} and therefore
//     no TenantID, resolvable to a per-subject master key so their PII is individually
//     crypto-shreddable. See setSubjectTags / prepareEventData.
//  3. c.defaultKeyID — the unchanged fallback.
//
// The chosen tenant/subject id is passed through c.tenantKeyResolver (also settable
// via the WithSubjectKeyResolver alias). Zero overhead when unused: with no resolver
// this returns defaultKeyID, and with no tagger there are no tags to inspect, so the
// pre-change behavior is preserved exactly. Decryption never re-runs this — the
// wrapping key id is read from the event's own metadata (GetEncryptionKeyID) — so
// events written under either rule decrypt unchanged, with no migration.
func (c *FieldEncryptionConfig) resolveKeyID(metadata Metadata) string {
	if c.tenantKeyResolver != nil {
		subject := metadata.TenantID // 1. explicit tenant wins (unchanged)
		if subject == "" {           // 2. else the primary data subject
			if tags := GetSubjectTags(metadata); len(tags) > 0 {
				subject = tags[0]
			}
		}
		if subject != "" {
			if keyID := c.tenantKeyResolver(subject); keyID != "" {
				return keyID
			}
		}
	}
	return c.defaultKeyID // 3. unchanged fallback
}

// encryptFields encrypts the configured fields in the serialized event data.
// It uses envelope encryption: generates a DEK, encrypts fields with it, and
// stores the encrypted DEK in metadata.
func (c *FieldEncryptionConfig) encryptFields(ctx context.Context, streamID, eventType string, data []byte, metadata Metadata) ([]byte, Metadata, error) {
	fieldPaths := c.sealPaths(eventType)
	if len(fieldPaths) == 0 {
		return data, metadata, nil
	}
	if err := c.configErr[eventType]; err != nil {
		return nil, metadata, encryption.NewEncryptionError("", "", err)
	}

	if c.provider == nil {
		return nil, metadata, encryption.NewEncryptionError("", "", fmt.Errorf("encryption provider not configured: use WithEncryptionProvider option"))
	}

	keyID, err := c.selectKeyID(eventType, metadata)
	if err != nil {
		return nil, metadata, err
	}

	// Generate a DEK for this event
	dk, err := c.provider.GenerateDataKey(ctx, keyID)
	if err != nil {
		return nil, metadata, err
	}
	defer encryption.ClearBytes(dk.Plaintext)

	// Parse JSON data — field-level encryption requires JSON-encoded event bodies.
	// decodeJSON keeps numbers as json.Number so unsealed sibling fields holding
	// integers above 2^53 are re-serialized verbatim instead of being rounded.
	var jsonData map[string]interface{}
	if err := decodeJSON(data, &jsonData); err != nil {
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

	// Parse JSON data (numbers preserved as json.Number — see decodeJSON)
	var jsonData map[string]interface{}
	if err := decodeJSON(data, &jsonData); err != nil {
		return nil, encryption.NewDecryptionError(keyID, "", fmt.Errorf("failed to parse event data: %w", err))
	}

	// Decrypt each field, parents first: a sealed parent object must be restored
	// before a sealed field nested inside it can be reached (see the overlapping-
	// paths note on FieldEncryptionConfig). The recorded order is irrelevant.
	for _, fieldPath := range unsealOrder(fieldNames) {
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

// IsEncrypted reports whether the event has encrypted fields, i.e. whether its metadata
// carries the $encrypted_fields marker. That is deliberately the weakest signal: it is
// what the READ path keys on, so an event whose envelope is damaged or incomplete is
// still routed through decryption and fails loudly instead of being served as
// plaintext. Code that is about to ACT on a key id (crypto-shredding, blast-radius
// guards, erasure planning) must use HasEncryptionEnvelope instead.
func IsEncrypted(m Metadata) bool {
	if m.Custom == nil {
		return false
	}
	_, ok := m.Custom[encryptedFieldsKey]
	return ok
}

// HasEncryptionEnvelope reports whether the event carries a COMPLETE field-encryption
// envelope: the encrypted-fields list ($encrypted_fields), the master key id
// ($encryption_key_id) and the wrapped data key ($encrypted_dek), all present and
// non-empty. Only such an event is ciphertext that revoking its key would actually
// erase.
//
// It differs from IsEncrypted, which is true as soon as $encrypted_fields is present:
//
//   - IsEncrypted answers "must this event go through decryption?" — the right question
//     on the read path, where an incomplete envelope has to surface as a decryption
//     error rather than pass through as plaintext.
//   - HasEncryptionEnvelope answers "would revoking this event's key erase it?" — the
//     right question wherever a key id is about to be acted on: RetentionManager's
//     ActionShred and its shared-key guard, DataEraser key discovery and its shared-key
//     guard, SubjectResolver footprints (KeyIDs / CleartextEvents) and erasure
//     verification all use it. A bare key id with no envelope (legacy or
//     hand-written metadata) protects no ciphertext, so treating it as encrypted would
//     revoke — and crypto-shred everything else under — a key that erases none of the
//     matched data.
//
// $encryption_algorithm is not required: events written before it was stamped default
// to AES-256-GCM (see GetEncryptionAlgorithm). A complete envelope always satisfies
// IsEncrypted; the converse does not hold.
func HasEncryptionEnvelope(m Metadata) bool {
	if !IsEncrypted(m) {
		return false
	}
	return m.Custom[encryptionKeyIDKey] != "" && m.Custom[encryptedDEKKey] != ""
}

// decodeJSON unmarshals data into v like json.Unmarshal, but with
// json.Decoder.UseNumber so every number decodes as json.Number and re-marshals
// verbatim. encryptFields/decryptFields round-trip the whole event body through
// map[string]interface{}; with encoding/json's float64 default any integer above
// 2^53 in ANY field of an encrypted event — not just the sealed ones — would be
// silently rounded on the way through. Trailing data after the top-level value is
// rejected, as json.Unmarshal does, so the decoder swap does not loosen parsing.
func decodeJSON(data []byte, v interface{}) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		if err == nil {
			return fmt.Errorf("invalid character after top-level value")
		}
		return err
	}
	return nil
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
// Returns true if the field was found and encrypted, false if the leaf or one of
// its parent objects is absent (or JSON null). A parent that is present but is not
// a JSON object is an error: the configuration does not match the event's shape,
// and silently skipping the field would store it in plaintext.
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

	// Nested field — recurse into child object. An absent or null parent means an
	// optional nested object that this event did not set: nothing to seal. A parent
	// that is present with any other non-object value is a configuration/shape
	// mismatch and must not be skipped silently (the field would be stored as
	// plaintext while the operator believes it is encrypted).
	child, ok := data[parts[0]]
	if !ok || child == nil {
		return false, nil
	}

	childMap, ok := child.(map[string]interface{})
	if !ok {
		return false, fmt.Errorf("parent %q of encrypted field %q holds a %T, not a JSON object (the field configuration does not match the event's shape)",
			strings.TrimSuffix(fullPath, "."+parts[1]), fullPath, child)
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
//
// Integrity: $encrypted_fields only ever lists fields that encryptFields actually
// sealed, so every listed path MUST still resolve to a base64 ciphertext string.
// A path that is absent, whose parent is no longer an object, or whose leaf is not
// a string means the stored row no longer matches what was written — the sealed
// value was removed or replaced after the fact, bypassing the AEAD tag — and is
// reported as a decryption failure naming the field (see tamperedFieldError)
// instead of being silently passed through as if it were plaintext.
func decryptJSONFieldWithPath(data map[string]interface{}, fieldPath, fullPath, streamID string, key []byte) error {
	parts := strings.SplitN(fieldPath, ".", 2)

	if len(parts) == 1 {
		val, ok := data[parts[0]]
		if !ok {
			return tamperedFieldError(fullPath, "is absent from the stored event")
		}

		encoded, ok := val.(string)
		if !ok {
			return tamperedFieldError(fullPath, fmt.Sprintf("holds a %T instead of sealed ciphertext", val))
		}

		ciphertext, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return fmt.Errorf("failed to decode field: %w", err)
		}

		plaintext, err := decryptFieldValue(key, ciphertext, streamID, fullPath, parts[0])
		if err != nil {
			return err
		}

		// Restore the original JSON value, keeping numbers as json.Number so a sealed
		// integer above 2^53 is re-emitted exactly (see decodeJSON).
		var restored interface{}
		if err := decodeJSON(plaintext, &restored); err != nil {
			return fmt.Errorf("failed to unmarshal decrypted field: %w", err)
		}

		data[parts[0]] = restored
		return nil
	}

	// Nested field
	child, ok := data[parts[0]]
	if !ok {
		return tamperedFieldError(fullPath, "is absent from the stored event")
	}

	childMap, ok := child.(map[string]interface{})
	if !ok {
		return tamperedFieldError(fullPath, fmt.Sprintf("has a %T parent instead of an object", child))
	}

	return decryptJSONFieldWithPath(childMap, parts[1], fullPath, streamID, key)
}

// tamperedFieldError reports a field listed in $encrypted_fields that no longer
// resolves to sealed ciphertext. It wraps ErrDecryptionFailed so errors.Is matches
// both directly and through the EncryptionError that decryptFields adds on top.
func tamperedFieldError(fullPath, problem string) error {
	return fmt.Errorf("%w: encrypted field %q %s (the stored event does not match what was sealed; possible tampering)",
		encryption.ErrDecryptionFailed, fullPath, problem)
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

// Field-path ordering, validation and key-selection helpers

// fieldDepth is the number of dot-separated segments in a field path ("a" → 1,
// "a.b" → 2). Depth, not lexical order, decides sealing/unsealing order.
func fieldDepth(path string) int {
	return strings.Count(path, ".") + 1
}

// orderByDepth returns paths sorted by depth — deepest first when deepestFirst is
// set (sealing), shallowest first otherwise (unsealing). The sort is stable, so
// paths of equal depth keep their given order. It returns the input slice itself,
// with no allocation, when no path is nested (the common flat configuration).
func orderByDepth(paths []string, deepestFirst bool) []string {
	nested := false
	for _, p := range paths {
		if strings.Contains(p, ".") {
			nested = true
			break
		}
	}
	if !nested {
		return paths
	}
	ordered := make([]string, len(paths))
	copy(ordered, paths)
	sort.SliceStable(ordered, func(i, j int) bool {
		if deepestFirst {
			return fieldDepth(ordered[i]) > fieldDepth(ordered[j])
		}
		return fieldDepth(ordered[i]) < fieldDepth(ordered[j])
	})
	return ordered
}

// unsealOrder is the order in which the fields recorded in $encrypted_fields are
// decrypted: shallowest first, so a sealed parent object is restored before a
// field nested inside it is unsealed.
func unsealOrder(recorded []string) []string {
	return orderByDepth(recorded, false)
}

// sealPaths is the order in which an event type's configured fields are encrypted:
// deepest first, so a nested field is sealed before its parent object is. It is
// precomputed by finalize for types with nested paths and falls back to the
// configured list (already flat, no ordering needed) otherwise.
func (c *FieldEncryptionConfig) sealPaths(eventType string) []string {
	if ordered, ok := c.sealOrder[eventType]; ok {
		return ordered
	}
	return c.fields[eventType]
}

// validateFieldPath reports whether path is a well-formed dot-separated field path.
func validateFieldPath(eventType, path string) error {
	if path == "" {
		return fmt.Errorf("%w: event type %q lists an empty field path", ErrInvalidEncryptedFieldPath, eventType)
	}
	for _, seg := range strings.Split(path, ".") {
		if seg == "" {
			return fmt.Errorf("%w: event type %q field path %q has an empty segment", ErrInvalidEncryptedFieldPath, eventType, path)
		}
	}
	return nil
}

// finalize validates every configured field path and precomputes the sealing
// order for event types with nested paths. It runs once from
// NewFieldEncryptionConfig (and lazily from Validate for a config built without
// it). Zero overhead on the hot path for flat configurations: no sealOrder entry
// is created for them and configErr stays nil when every path is valid.
func (c *FieldEncryptionConfig) finalize() {
	c.sealOrder = nil
	c.configErr = nil
	for eventType, paths := range c.fields {
		for _, p := range paths {
			if err := validateFieldPath(eventType, p); err != nil {
				if c.configErr == nil {
					c.configErr = make(map[string]error)
				}
				c.configErr[eventType] = err
				break
			}
		}
		if ordered := orderByDepth(paths, true); len(ordered) > 0 && &ordered[0] != &paths[0] {
			if c.sealOrder == nil {
				c.sealOrder = make(map[string][]string)
			}
			c.sealOrder[eventType] = ordered
		}
	}
}

// selectKeyID is resolveKeyID plus the WithRequireKeyResolution policy. Without the
// option (the default) it returns resolveKeyID's result and the pre-existing "no
// encryption key ID configured" error when that is empty. With it, a configured
// resolver that yields no key for the event — because there is no tenant id or
// subject tag to resolve, or because it returned "" — is a KeyResolutionError
// (wrapped in an EncryptionError) instead of a silent fallback to the default key.
func (c *FieldEncryptionConfig) selectKeyID(eventType string, metadata Metadata) (string, error) {
	if c.requireKeyResolution && c.tenantKeyResolver != nil {
		id := metadata.TenantID
		var subjectID string
		if id == "" {
			if tags := GetSubjectTags(metadata); len(tags) > 0 {
				subjectID = tags[0]
				id = subjectID
			}
		}
		keyID := ""
		if id != "" {
			keyID = c.tenantKeyResolver(id)
		}
		if keyID == "" {
			return "", encryption.NewEncryptionError("", "", &KeyResolutionError{
				EventType: eventType,
				TenantID:  metadata.TenantID,
				SubjectID: subjectID,
			})
		}
		return keyID, nil
	}
	keyID := c.resolveKeyID(metadata)
	if keyID == "" {
		return "", encryption.NewEncryptionError("", "", fmt.Errorf("no encryption key ID configured"))
	}
	return keyID, nil
}
