package mink

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go-mink.dev/adapters/memory"
	"go-mink.dev/encryption"
	"go-mink.dev/encryption/local"
)

// =============================================================================
// Overlapping field paths, strict parent check, strict key resolution, Validate
// =============================================================================

type overlapAddress struct {
	Street string `json:"street"`
	City   string `json:"city"`
}

type overlapEvent struct {
	Name    string          `json:"name"`
	Address *overlapAddress `json:"address,omitempty"`
}

// A configuration that lists both a parent object and a field nested inside it
// must round-trip whichever order the paths were configured in: the nested leaf
// is sealed first, the parent last, and unsealing restores the parent before the
// nested ciphertext is reached. Before this fix the legacy configuration order
// ["address.street", "address"] produced events that failed decryption as
// "tampered".
func TestFieldEncryptionConfig_OverlappingPaths_RoundTrip(t *testing.T) {
	ctx := context.Background()
	configs := map[string][]string{
		"parent first":            {"address", "address.street"},
		"leaf first (legacy bug)": {"address.street", "address"},
		"three levels":            {"a", "a.b", "a.b.c"},
		"three levels reversed":   {"a.b.c", "a.b", "a"},
		"siblings and parent":     {"address.city", "address", "address.street"},
	}
	for name, paths := range configs {
		t.Run(name, func(t *testing.T) {
			_, config := testEncConfig(t, "master-1", WithEncryptedFields("Overlap", paths...))
			var data []byte
			if paths[0] == "a" || paths[0] == "a.b.c" {
				data = []byte(`{"a":{"b":{"c":"deep","d":1},"e":"x"},"f":true}`)
			} else {
				data = []byte(`{"name":"n","address":{"street":"1 Main St","city":"NYC"}}`)
			}

			enc, md, err := config.encryptFields(ctx, "Overlap-1", "Overlap", data, Metadata{})
			require.NoError(t, err)
			require.True(t, IsEncrypted(md))

			// Deepest paths are recorded (and were sealed) first.
			recorded := GetEncryptedFields(md)
			require.Len(t, recorded, len(paths))
			for i := 1; i < len(recorded); i++ {
				assert.GreaterOrEqual(t, fieldDepth(recorded[i-1]), fieldDepth(recorded[i]), "recorded order must be deepest-first: %v", recorded)
			}

			// On disk the top-level sealed field is a single ciphertext string.
			var onDisk map[string]interface{}
			require.NoError(t, json.Unmarshal(enc, &onDisk))
			top := paths[0]
			if top != "a" && top != "address" {
				top = "address"
				if paths[0] == "a.b.c" {
					top = "a"
				}
			}
			_, isString := onDisk[top].(string)
			assert.True(t, isString, "parent %q must be sealed as a whole", top)

			dec, err := config.decryptFields(ctx, "Overlap-1", "Overlap", enc, md)
			require.NoError(t, err)
			assert.JSONEq(t, string(data), string(dec))
		})
	}
}

// Events already written by earlier versions recorded $encrypted_fields in
// configuration order; the decrypt side must not depend on that order.
func TestFieldEncryptionConfig_OverlappingPaths_LegacyRecordedOrderStillDecrypts(t *testing.T) {
	ctx := context.Background()
	provider, config := testEncConfig(t, "master-1", WithEncryptedFields("Overlap", "address.street", "address"))
	data := []byte(`{"name":"n","address":{"street":"1 Main St","city":"NYC"}}`)
	enc, md, err := config.encryptFields(ctx, "Overlap-1", "Overlap", data, Metadata{})
	require.NoError(t, err)

	for name, order := range map[string][]string{
		"parent recorded first": {"address", "address.street"},
		"leaf recorded first":   {"address.street", "address"},
	} {
		t.Run(name, func(t *testing.T) {
			list, _ := json.Marshal(order)
			legacy := md.WithCustom(encryptedFieldsKey, string(list))
			dec, err := config.decryptFields(ctx, "Overlap-1", "Overlap", enc, legacy)
			require.NoError(t, err)
			assert.JSONEq(t, string(data), string(dec))
		})
	}

	t.Run("legacy event sealed with the parent only (child was silently skipped)", func(t *testing.T) {
		// Same provider (same master key), parent-only configuration.
		parentOnly := NewFieldEncryptionConfig(
			WithEncryptionProvider(provider), WithDefaultKeyID("master-1"),
			WithEncryptedFields("Overlap", "address"))
		enc, md, err := parentOnly.encryptFields(ctx, "Overlap-1", "Overlap", data, Metadata{})
		require.NoError(t, err)
		require.Equal(t, []string{"address"}, GetEncryptedFields(md))
		// Read back by the overlapping configuration: only the recorded field is unsealed.
		dec, err := config.decryptFields(ctx, "Overlap-1", "Overlap", enc, md)
		require.NoError(t, err)
		assert.JSONEq(t, string(data), string(dec))
	})
}

// End-to-end through the event store: written with one configuration order and
// read back with the other.
func TestEventStore_OverlappingEncryptedPaths_WrittenThenRead(t *testing.T) {
	ctx := context.Background()
	key := []byte("0123456789abcdef0123456789abcdef")
	newStore := func(t *testing.T, adapter *memory.MemoryAdapter, paths ...string) *EventStore {
		t.Helper()
		provider, err := local.New(local.WithKey("k", key))
		require.NoError(t, err)
		t.Cleanup(func() { _ = provider.Close() })
		store := New(adapter, WithFieldEncryption(NewFieldEncryptionConfig(
			WithEncryptionProvider(provider), WithDefaultKeyID("k"),
			WithEncryptedFields("overlapEvent", paths...),
		)))
		store.RegisterEvents(overlapEvent{})
		return store
	}
	adapter := memory.NewAdapter()
	writer := newStore(t, adapter, "address.street", "address")
	want := overlapEvent{Name: "n", Address: &overlapAddress{Street: "1 Main St", City: "NYC"}}
	require.NoError(t, writer.Append(ctx, "Overlap-1", []interface{}{want, overlapEvent{Name: "no address"}}))

	raw, err := writer.LoadRaw(ctx, "Overlap-1", 0)
	require.NoError(t, err)
	assert.NotContains(t, string(raw[0].Data), "Main St")
	assert.NotContains(t, string(raw[0].Data), "NYC")
	assert.False(t, IsEncrypted(raw[1].Metadata), "an event without the optional object is stored unsealed")

	reader := newStore(t, adapter, "address", "address.street")
	events, err := reader.Load(ctx, "Overlap-1")
	require.NoError(t, err)
	require.Len(t, events, 2)
	assert.Equal(t, want, events[0].Data)
	assert.Equal(t, overlapEvent{Name: "no address"}, events[1].Data)
}

// A configured path whose parent is present but is not a JSON object is a
// configuration/shape mismatch: the append fails with an EncryptionError naming
// the field instead of silently storing the event in plaintext. An absent or null
// parent is an optional object and is simply not encrypted.
func TestFieldEncryptionConfig_NonObjectParent_FailsClosed(t *testing.T) {
	ctx := context.Background()
	_, config := testEncConfig(t, "master-1", WithEncryptedFields("Shape", "address.street"))

	t.Run("string parent", func(t *testing.T) {
		_, _, err := config.encryptFields(ctx, "s", "Shape", []byte(`{"address":"1 Main St"}`), Metadata{})
		require.Error(t, err)
		assert.ErrorIs(t, err, encryption.ErrEncryptionFailed)
		var ee *encryption.EncryptionError
		require.True(t, errors.As(err, &ee))
		assert.Equal(t, "address.street", ee.Field)
		assert.Contains(t, err.Error(), "not a JSON object")
	})
	t.Run("array parent", func(t *testing.T) {
		_, _, err := config.encryptFields(ctx, "s", "Shape", []byte(`{"address":["1 Main St"]}`), Metadata{})
		require.Error(t, err)
		assert.ErrorIs(t, err, encryption.ErrEncryptionFailed)
	})
	t.Run("null parent is optional", func(t *testing.T) {
		data := []byte(`{"address":null,"name":"n"}`)
		out, md, err := config.encryptFields(ctx, "s", "Shape", data, Metadata{})
		require.NoError(t, err)
		assert.False(t, IsEncrypted(md))
		assert.Equal(t, data, out)
	})
	t.Run("absent parent is optional", func(t *testing.T) {
		data := []byte(`{"name":"n"}`)
		out, md, err := config.encryptFields(ctx, "s", "Shape", data, Metadata{})
		require.NoError(t, err)
		assert.False(t, IsEncrypted(md))
		assert.Equal(t, data, out)
	})
	t.Run("through the store: the append fails, nothing is stored", func(t *testing.T) {
		provider, err := local.New(local.WithKey("k", make([]byte, 32)))
		require.NoError(t, err)
		t.Cleanup(func() { _ = provider.Close() })
		store := New(memory.NewAdapter(), WithFieldEncryption(NewFieldEncryptionConfig(
			WithEncryptionProvider(provider), WithDefaultKeyID("k"),
			WithEncryptedFields("shapeMismatch", "address.street"),
		)))
		store.RegisterEvents(shapeMismatch{})
		err = store.Append(ctx, "Shape-1", []interface{}{shapeMismatch{Address: "1 Main St"}})
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrEncryptionFailed)
		raw, err := store.LoadRaw(ctx, "Shape-1", 0)
		require.NoError(t, err)
		assert.Empty(t, raw)
	})
}

type shapeMismatch struct {
	Address string `json:"address"`
}

func TestWithRequireKeyResolution(t *testing.T) {
	ctx := context.Background()
	newConfig := func(t *testing.T, opts ...EncryptionOption) *FieldEncryptionConfig {
		t.Helper()
		provider, err := local.New(
			local.WithKey("default-key", make([]byte, 32)),
			local.WithKey("key-t1", []byte("0123456789abcdef0123456789abcdef")),
			local.WithKey("key-s1", []byte("fedcba9876543210fedcba9876543210")),
		)
		require.NoError(t, err)
		t.Cleanup(func() { _ = provider.Close() })
		base := []EncryptionOption{
			WithEncryptionProvider(provider),
			WithDefaultKeyID("default-key"),
			WithEncryptedFields("UserCreated", "email"),
		}
		return NewFieldEncryptionConfig(append(base, opts...)...)
	}
	resolver := func(id string) string {
		switch id {
		case "t1":
			return "key-t1"
		case "s1":
			return "key-s1"
		}
		return "" // unknown tenant / subject
	}
	data := []byte(`{"email":"a@example.com"}`)

	tests := []struct {
		name      string
		opts      []EncryptionOption
		metadata  Metadata
		wantKey   string
		wantErr   bool
		wantTen   string
		wantSubj  string
		wantInMsg string
	}{
		{name: "strict: tenant resolves", opts: []EncryptionOption{WithTenantKeyResolver(resolver), WithRequireKeyResolution()}, metadata: Metadata{TenantID: "t1"}, wantKey: "key-t1"},
		{name: "strict: subject tag resolves", opts: []EncryptionOption{WithSubjectKeyResolver(resolver), WithRequireKeyResolution()}, metadata: setSubjectTags(Metadata{}, []string{"s1"}), wantKey: "key-s1"},
		{name: "strict: resolver returns empty for tenant", opts: []EncryptionOption{WithTenantKeyResolver(resolver), WithRequireKeyResolution()}, metadata: Metadata{TenantID: "unknown"}, wantErr: true, wantTen: "unknown", wantInMsg: `tenant "unknown"`},
		{name: "strict: resolver returns empty for subject", opts: []EncryptionOption{WithSubjectKeyResolver(resolver), WithRequireKeyResolution()}, metadata: setSubjectTags(Metadata{}, []string{"unknown"}), wantErr: true, wantSubj: "unknown", wantInMsg: `subject "unknown"`},
		{name: "strict: nothing to resolve from", opts: []EncryptionOption{WithSubjectKeyResolver(resolver), WithRequireKeyResolution()}, metadata: Metadata{}, wantErr: true, wantInMsg: "no tenant id or subject tag"},
		{name: "strict without a resolver: default key as before", opts: []EncryptionOption{WithRequireKeyResolution()}, metadata: Metadata{TenantID: "unknown"}, wantKey: "default-key"},
		{name: "lenient (default): resolver miss falls back to the default key", opts: []EncryptionOption{WithTenantKeyResolver(resolver)}, metadata: Metadata{TenantID: "unknown"}, wantKey: "default-key"},
		{name: "lenient (default): nothing to resolve falls back to the default key", opts: []EncryptionOption{WithTenantKeyResolver(resolver)}, metadata: Metadata{}, wantKey: "default-key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := newConfig(t, tt.opts...)
			_, md, err := config.encryptFields(ctx, "User-1", "UserCreated", data, tt.metadata)
			if !tt.wantErr {
				require.NoError(t, err)
				assert.Equal(t, tt.wantKey, GetEncryptionKeyID(md))
				return
			}
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrKeyResolutionFailed)
			assert.ErrorIs(t, err, ErrEncryptionFailed, "still an encryption failure for existing handlers")
			var kre *KeyResolutionError
			require.True(t, errors.As(err, &kre), "typed cause reachable through EncryptionError.Unwrap")
			assert.Equal(t, "UserCreated", kre.EventType)
			assert.Equal(t, tt.wantTen, kre.TenantID)
			assert.Equal(t, tt.wantSubj, kre.SubjectID)
			assert.Contains(t, err.Error(), tt.wantInMsg)
			assert.False(t, IsEncrypted(md), "nothing is stamped on failure")
		})
	}

	t.Run("through the store: the append fails instead of using the default key", func(t *testing.T) {
		config := newConfig(t, WithSubjectKeyResolver(resolver), WithRequireKeyResolution(),
			WithEncryptedFields("eraseUserCreated", "email"))
		store := New(memory.NewAdapter(), WithFieldEncryption(config), WithSubjectTagger(userIDTagger))
		store.RegisterEvents(eraseUserCreated{})
		// Tagged "s1" → resolves.
		require.NoError(t, store.Append(ctx, "User-s1", []interface{}{eraseUserCreated{UserID: "s1", Email: "e"}},
			WithAppendMetadata(Metadata{UserID: "s1"})))
		assert.Equal(t, "key-s1", GetEncryptionKeyID(loadOnlyRaw(t, ctx, store, "User-s1").Metadata))
		// Untagged (no UserID) → nothing to resolve → fails.
		err := store.Append(ctx, "User-anon", []interface{}{eraseUserCreated{UserID: "", Email: "e"}})
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrKeyResolutionFailed)
		raw, err := store.LoadRaw(ctx, "User-anon", 0)
		require.NoError(t, err)
		assert.Empty(t, raw)
	})
}

func TestKeyResolutionError_Is_Unwrap(t *testing.T) {
	e := &KeyResolutionError{EventType: "X"}
	assert.True(t, errors.Is(e, ErrKeyResolutionFailed))
	assert.False(t, errors.Is(e, ErrEncryptionFailed))
	assert.Equal(t, ErrKeyResolutionFailed, errors.Unwrap(e))
	wrapped := NewEncryptionError("", "", e)
	assert.True(t, errors.Is(wrapped, ErrKeyResolutionFailed))
	assert.True(t, errors.Is(wrapped, ErrEncryptionFailed))
}

func TestFieldEncryptionConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		fields  map[string][]string
		wantErr string
	}{
		{name: "flat", fields: map[string][]string{"A": {"email", "phone"}}},
		{name: "nested and overlapping are valid", fields: map[string][]string{"A": {"address", "address.street", "a.b.c"}}},
		{name: "no fields", fields: nil},
		{name: "empty path", fields: map[string][]string{"A": {"email", ""}}, wantErr: "empty field path"},
		{name: "empty inner segment", fields: map[string][]string{"A": {"a..b"}}, wantErr: `"a..b"`},
		{name: "leading dot", fields: map[string][]string{"A": {".a"}}, wantErr: `".a"`},
		{name: "trailing dot", fields: map[string][]string{"A": {"a."}}, wantErr: `"a."`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var opts []EncryptionOption
			for typ, paths := range tt.fields {
				opts = append(opts, WithEncryptedFields(typ, paths...))
			}
			config := NewFieldEncryptionConfig(opts...)
			err := config.Validate()
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrInvalidEncryptedFieldPath)
			assert.Contains(t, err.Error(), tt.wantErr)
			assert.Contains(t, err.Error(), `"A"`)
		})
	}

	t.Run("first append of the affected type fails; other types are unaffected", func(t *testing.T) {
		_, config := testEncConfig(t, "master-1",
			WithEncryptedFields("Bad", "a..b"),
			WithEncryptedFields("Good", "email"))
		_, _, err := config.encryptFields(context.Background(), "s", "Bad", []byte(`{"a":{"b":"x"}}`), Metadata{})
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrInvalidEncryptedFieldPath)
		assert.ErrorIs(t, err, ErrEncryptionFailed)
		_, md, err := config.encryptFields(context.Background(), "s", "Good", []byte(`{"email":"x"}`), Metadata{})
		require.NoError(t, err)
		assert.True(t, IsEncrypted(md))
	})

	t.Run("config built without the constructor validates lazily", func(t *testing.T) {
		config := &FieldEncryptionConfig{}
		WithEncryptedFields("A", ".bad")(config)
		assert.ErrorIs(t, config.Validate(), ErrInvalidEncryptedFieldPath)
		config2 := &FieldEncryptionConfig{}
		WithEncryptedFields("A", "ok")(config2)
		assert.NoError(t, config2.Validate())
	})
}

func TestWithEncryptedFields_Deduplicates(t *testing.T) {
	_, config := testEncConfig(t, "master-1",
		WithEncryptedFields("A", "email", "email", "address.street"),
		WithEncryptedFields("A", "address.street", "phone"))
	assert.Equal(t, []string{"email", "address.street", "phone"}, config.fields["A"])
	assert.Equal(t, []string{"address.street", "email", "phone"}, config.sealPaths("A"), "deepest first, stable otherwise")

	_, md, err := config.encryptFields(context.Background(), "s", "A",
		[]byte(`{"email":"e","phone":"p","address":{"street":"s"}}`), Metadata{})
	require.NoError(t, err)
	assert.Equal(t, []string{"address.street", "email", "phone"}, GetEncryptedFields(md), "each field sealed exactly once")
}

func TestOrderByDepth(t *testing.T) {
	flat := []string{"b", "a", "c"}
	same := orderByDepth(flat, true)
	assert.True(t, reflect.ValueOf(same).Pointer() == reflect.ValueOf(flat).Pointer(), "flat paths: no allocation, same slice")
	assert.Equal(t, []string{"b", "a", "c"}, same)

	nested := []string{"x", "a.b", "y", "a.b.c", "z.w"}
	assert.Equal(t, []string{"a.b.c", "a.b", "z.w", "x", "y"}, orderByDepth(nested, true), "deepest first, stable")
	assert.Equal(t, []string{"x", "y", "a.b", "z.w", "a.b.c"}, orderByDepth(nested, false), "shallowest first, stable")
	assert.Equal(t, []string{"x", "a.b", "y", "a.b.c", "z.w"}, nested, "input never mutated")
	assert.Equal(t, []string{"x", "y", "a.b", "z.w", "a.b.c"}, unsealOrder(nested))
}
