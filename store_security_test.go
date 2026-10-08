package mink

import (
	"context"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go-mink.dev/adapters/memory"
	"go-mink.dev/encryption/local"
)

// =============================================================================
// Reserved-metadata sanitization (security hardening of the shared append hook)
//
// A caller that controls Metadata.Custom at Append time (e.g. an HTTP API that
// copies request headers into metadata) must not be able to forge library-owned
// keys: the field-encryption envelope, the schema version routing the upcaster
// chain, or the subject tags that select an encryption key and drive erasure.
// =============================================================================

// forgedEnvelope is a caller-controlled Custom map carrying every library-owned
// encryption envelope key plus a legitimate application key.
func forgedEnvelope() map[string]string {
	return map[string]string{
		encryptedFieldsKey:     `["email"]`,
		encryptionKeyIDKey:     "attacker-key",
		encryptedDEKKey:        "AAAA",
		encryptionAlgorithmKey: "AES-256-GCM",
		"app":                  "kept",
	}
}

func cloneCustom(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// sameMap reports whether two maps share the same underlying storage.
func sameMap(a, b map[string]string) bool {
	return reflect.ValueOf(a).Pointer() == reflect.ValueOf(b).Pointer()
}

// newSecurityUpcasterChain registers two no-op upcasters for StoreOrderCreated so
// its latest schema version is 3; StoreItemAdded has none.
func newSecurityUpcasterChain(t *testing.T) *UpcasterChain {
	t.Helper()
	chain := NewUpcasterChain()
	require.NoError(t, chain.Register(newNoopUpcaster("StoreOrderCreated", 1, 2)))
	require.NoError(t, chain.Register(newNoopUpcaster("StoreOrderCreated", 2, 3)))
	return chain
}

func loadOnlyRaw(t *testing.T, ctx context.Context, store *EventStore, streamID string) StoredEvent {
	t.Helper()
	raw, err := store.LoadRaw(ctx, streamID, 0)
	require.NoError(t, err)
	require.Len(t, raw, 1)
	return raw[0]
}

func TestWithoutCustomKeys(t *testing.T) {
	tests := []struct {
		name       string
		in         map[string]string
		keys       []string
		want       map[string]string
		wantShared bool // output must share the input map (no allocation)
	}{
		{name: "nil map is returned as-is", in: nil, keys: []string{"a"}, want: nil, wantShared: true},
		{name: "absent key shares the input map", in: map[string]string{"x": "1"}, keys: []string{"a"}, want: map[string]string{"x": "1"}, wantShared: true},
		{name: "present key is removed from a copy", in: map[string]string{"a": "1", "x": "2"}, keys: []string{"a"}, want: map[string]string{"x": "2"}},
		{name: "several keys removed in one pass", in: map[string]string{"a": "1", "b": "2", "x": "3"}, keys: []string{"a", "b", "c"}, want: map[string]string{"x": "3"}},
		{name: "map left empty becomes nil", in: map[string]string{"a": "1"}, keys: []string{"a"}, want: nil},
		{name: "duplicate keys do not over-count", in: map[string]string{"a": "1", "x": "2"}, keys: []string{"a", "a"}, want: map[string]string{"x": "2"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snapshot := cloneCustom(tt.in)
			out := withoutCustomKeys(Metadata{UserID: "u", Custom: tt.in}, tt.keys...)
			assert.Equal(t, tt.want, out.Custom)
			assert.Equal(t, "u", out.UserID, "non-Custom fields are carried over")
			assert.Equal(t, snapshot, tt.in, "the input map must never be mutated")
			if tt.wantShared {
				assert.True(t, sameMap(tt.in, out.Custom), "common path must not allocate a new map")
			} else if tt.in != nil && out.Custom != nil {
				assert.False(t, sameMap(tt.in, out.Custom), "a sanitized map must be a copy")
			}
		})
	}
}

func TestSanitizeReservedMetadata(t *testing.T) {
	tests := []struct {
		name       string
		in         map[string]string
		want       map[string]string
		wantShared bool
	}{
		{name: "nil custom", in: nil, want: nil, wantShared: true},
		{name: "no reserved keys shares the map", in: map[string]string{"app": "x", "$app_flag": "y"}, want: map[string]string{"app": "x", "$app_flag": "y"}, wantShared: true},
		{name: "strips $encrypted_fields", in: map[string]string{encryptedFieldsKey: `["email"]`, "app": "x"}, want: map[string]string{"app": "x"}},
		{name: "strips $encryption_key_id", in: map[string]string{encryptionKeyIDKey: "k", "app": "x"}, want: map[string]string{"app": "x"}},
		{name: "strips $encrypted_dek", in: map[string]string{encryptedDEKKey: "d", "app": "x"}, want: map[string]string{"app": "x"}},
		{name: "strips $encryption_algorithm", in: map[string]string{encryptionAlgorithmKey: "a", "app": "x"}, want: map[string]string{"app": "x"}},
		{name: "strips the whole envelope at once", in: forgedEnvelope(), want: map[string]string{"app": "kept"}},
		{name: "only reserved keys leaves nil custom", in: map[string]string{encryptedFieldsKey: `[]`, encryptionKeyIDKey: "k"}, want: nil},
		{
			name: "other $-keys are not its concern",
			in:   map[string]string{schemaVersionKey: "2", subjectTagsKey: `["u1"]`, erasureMarkerSubjectKey: "u1", encryptionKeyIDKey: "k"},
			want: map[string]string{schemaVersionKey: "2", subjectTagsKey: `["u1"]`, erasureMarkerSubjectKey: "u1"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snapshot := cloneCustom(tt.in)
			in := Metadata{CorrelationID: "c", TenantID: "t", Custom: tt.in}
			out := SanitizeReservedMetadata(in)
			assert.Equal(t, tt.want, out.Custom)
			assert.Equal(t, "c", out.CorrelationID)
			assert.Equal(t, "t", out.TenantID)
			assert.Equal(t, snapshot, tt.in, "the caller's map must never be mutated")
			if tt.wantShared {
				assert.True(t, sameMap(tt.in, out.Custom), "no reserved key present: must not allocate")
			}
		})
	}
}

func TestReplaceSubjectTags(t *testing.T) {
	tests := []struct {
		name     string
		existing []string
		subjects []string
		want     []string
	}{
		{name: "no existing, no subjects", want: nil},
		{name: "no existing, subjects set", subjects: []string{"u1"}, want: []string{"u1"}},
		{name: "existing replaced", existing: []string{"victim"}, subjects: []string{"u1"}, want: []string{"u1"}},
		{name: "existing dropped when tagger yields nothing", existing: []string{"victim"}, want: nil},
		{name: "duplicates and empties collapse", subjects: []string{"u1", "", "u1", "u2"}, want: []string{"u1", "u2"}},
		{name: "all-empty subjects drops existing", existing: []string{"victim"}, subjects: []string{""}, want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			md := Metadata{Custom: map[string]string{"app": "x"}}
			if tt.existing != nil {
				md = setSubjectTags(md, tt.existing)
			}
			snapshot := cloneCustom(md.Custom)
			out := replaceSubjectTags(md, tt.subjects)
			assert.Equal(t, tt.want, GetSubjectTags(out))
			assert.Equal(t, "x", out.Custom["app"], "unrelated keys survive")
			assert.Equal(t, snapshot, md.Custom, "input map must not be mutated")
		})
	}
}

func TestEventStore_Append_StripsForgedEncryptionEnvelope(t *testing.T) {
	ctx := context.Background()
	for _, key := range []string{encryptedFieldsKey, encryptionKeyIDKey, encryptedDEKKey, encryptionAlgorithmKey} {
		t.Run(key, func(t *testing.T) {
			forged := map[string]string{key: forgedEnvelope()[key], "app": "kept"}

			// Plain EventStore.Append (no encryption configured).
			store := New(memory.NewAdapter())
			store.RegisterEvents(StoreOrderCreated{})
			require.NoError(t, store.Append(ctx, "Order-1", []interface{}{StoreOrderCreated{OrderID: "1"}},
				WithAppendMetadata(Metadata{Custom: forged})))
			stored := loadOnlyRaw(t, ctx, store, "Order-1")
			assert.NotContains(t, stored.Metadata.Custom, key, "forged envelope key must not be persisted")
			assert.Equal(t, "kept", stored.Metadata.Custom["app"])
			assert.False(t, IsEncrypted(stored.Metadata), "plaintext must never be flagged encrypted")

			// The outbox wrapper's Append goes through the same hook.
			esOutbox := NewEventStoreWithOutbox(store, memory.NewOutboxStore(), nil)
			require.NoError(t, esOutbox.Append(ctx, "Order-2", []interface{}{StoreOrderCreated{OrderID: "2"}},
				WithAppendMetadata(Metadata{Custom: forged})))
			stored = loadOnlyRaw(t, ctx, store, "Order-2")
			assert.NotContains(t, stored.Metadata.Custom, key)
			assert.False(t, IsEncrypted(stored.Metadata))
		})
	}
}

func TestEventStore_Append_ForgedEnvelopeCannotRedirectEncryption(t *testing.T) {
	ctx := context.Background()
	provider, err := local.New(local.WithKey("real-key", make([]byte, 32)))
	require.NoError(t, err)
	t.Cleanup(func() { _ = provider.Close() })
	cfg := NewFieldEncryptionConfig(
		WithEncryptionProvider(provider),
		WithDefaultKeyID("real-key"),
		WithEncryptedFields("eraseUserCreated", "email"),
	)
	store := New(memory.NewAdapter(), WithFieldEncryption(cfg))
	store.RegisterEvents(eraseUserCreated{})

	// The caller pretends the event is already encrypted under a key the provider
	// does not hold, with a garbage DEK. The envelope must be re-stamped by the
	// store under the configured key and the data must actually be encrypted.
	require.NoError(t, store.Append(ctx, "User-u1",
		[]interface{}{eraseUserCreated{UserID: "u1", Email: "alice@example.com"}},
		WithAppendMetadata(Metadata{Custom: forgedEnvelope()})))

	stored := loadOnlyRaw(t, ctx, store, "User-u1")
	assert.True(t, IsEncrypted(stored.Metadata))
	assert.Equal(t, "real-key", GetEncryptionKeyID(stored.Metadata), "key id must be the store's, not the caller's")
	assert.NotEqual(t, "AAAA", stored.Metadata.Custom[encryptedDEKKey], "DEK must be freshly wrapped")
	assert.NotContains(t, string(stored.Data), "alice@example.com", "field must really be encrypted")
	assert.Equal(t, "kept", stored.Metadata.Custom["app"])

	events, err := store.Load(ctx, "User-u1")
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, "alice@example.com", events[0].Data.(eraseUserCreated).Email, "round-trips under the real key")
}

// TestEventStore_PrepareEventData_SanitizesReservedKeys exercises the shared hook
// directly with forged metadata, which is exactly what SaveAggregate (whose
// metadata is always built by the library) and the outbox wrapper route through.
func TestEventStore_PrepareEventData_SanitizesReservedKeys(t *testing.T) {
	ctx := context.Background()
	store := New(memory.NewAdapter(), WithUpcasters(newSecurityUpcasterChain(t)), WithSubjectTagger(userIDTagger))

	forged := forgedEnvelope()
	forged[schemaVersionKey] = "99"
	forged[subjectTagsKey] = `["victim"]`
	forged[erasureMarkerSubjectKey] = "marker"
	snapshot := cloneCustom(forged)

	ed := EventData{Type: "StoreOrderCreated", Data: []byte(`{"orderId":"1"}`), Metadata: Metadata{UserID: "u1", Custom: forged}}
	require.NoError(t, store.prepareEventData(ctx, "Order-1", &ed))

	for _, key := range reservedEncryptionMetadataKeys {
		assert.NotContains(t, ed.Metadata.Custom, key)
	}
	assert.Equal(t, "3", ed.Metadata.Custom[schemaVersionKey], "out-of-range version replaced by latest")
	assert.Equal(t, []string{"u1"}, GetSubjectTags(ed.Metadata), "tagger output replaces caller tags")
	assert.Equal(t, "marker", ed.Metadata.Custom[erasureMarkerSubjectKey], "other $-keys survive")
	assert.Equal(t, "kept", ed.Metadata.Custom["app"])
	assert.Equal(t, snapshot, forged, "the caller's map must never be mutated")
}

func TestEventStore_SaveAggregate_AppliesReservedMetadataPolicy(t *testing.T) {
	ctx := context.Background()
	chain := newSecurityUpcasterChain(t)
	tagger := func(eventType string, _ []byte, _ Metadata) []string { return []string{"subject-of-" + eventType} }
	store := New(memory.NewAdapter(), WithUpcasters(chain), WithSubjectTagger(tagger))
	store.RegisterEvents(StoreOrderCreated{}, StoreItemAdded{})

	order := NewStoreTestOrder("1")
	order.Create("cust-1")
	order.AddItem("sku", 1, 1.0)
	require.NoError(t, store.SaveAggregate(ctx, order))

	raw, err := store.LoadRaw(ctx, "Order-1", 0)
	require.NoError(t, err)
	require.Len(t, raw, 2)
	for _, se := range raw {
		for _, key := range reservedEncryptionMetadataKeys {
			assert.NotContains(t, se.Metadata.Custom, key)
		}
	}
	assert.Equal(t, "3", raw[0].Metadata.Custom[schemaVersionKey], "type with upcasters is stamped with its latest version")
	assert.Equal(t, "1", raw[1].Metadata.Custom[schemaVersionKey], "type without upcasters is stamped with the default")
	assert.Equal(t, []string{"subject-of-StoreOrderCreated"}, GetSubjectTags(raw[0].Metadata))
	assert.Equal(t, []string{"subject-of-StoreItemAdded"}, GetSubjectTags(raw[1].Metadata))

	// The outbox wrapper's SaveAggregate goes through the same hook.
	esOutbox := NewEventStoreWithOutbox(store, memory.NewOutboxStore(), nil)
	other := NewStoreTestOrder("2")
	other.Create("cust-2")
	require.NoError(t, esOutbox.SaveAggregate(ctx, other))
	stored := loadOnlyRaw(t, ctx, store, "Order-2")
	assert.Equal(t, "3", stored.Metadata.Custom[schemaVersionKey])
	assert.Equal(t, []string{"subject-of-StoreOrderCreated"}, GetSubjectTags(stored.Metadata))
}

func TestEventStore_Append_SchemaVersionPolicy(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name      string
		upcasters bool        // configure the chain (StoreOrderCreated latest=3)
		honor     bool        // WithCallerSchemaVersion
		event     interface{} // StoreOrderCreated (has upcasters) or StoreItemAdded (none)
		caller    string      // caller-supplied $schema_version; "" = none
		want      string      // persisted $schema_version; "" = key absent
	}{
		// Default (strict): a caller value is never honored.
		{name: "no upcasters: nothing stamped", event: StoreOrderCreated{}, want: ""},
		{name: "no upcasters: caller value dropped", event: StoreOrderCreated{}, caller: "7", want: ""},
		{name: "no upcasters: unparsable caller value dropped", event: StoreOrderCreated{}, caller: "v2", want: ""},
		{name: "upcasters: no caller value stamps latest", upcasters: true, event: StoreOrderCreated{}, want: "3"},
		{name: "upcasters: in-range caller value (1) dropped by default", upcasters: true, event: StoreOrderCreated{}, caller: "1", want: "3"},
		{name: "upcasters: in-range caller value (2) dropped by default", upcasters: true, event: StoreOrderCreated{}, caller: "2", want: "3"},
		{name: "upcasters: latest caller value re-stamped by default", upcasters: true, event: StoreOrderCreated{}, caller: "3", want: "3"},
		{name: "upcasters: above-latest caller value replaced", upcasters: true, event: StoreOrderCreated{}, caller: "9", want: "3"},
		{name: "upcasters for other types only: caller value replaced by default", upcasters: true, event: StoreItemAdded{}, caller: "5", want: "1"},
		{name: "upcasters for other types only: no caller value stamps default", upcasters: true, event: StoreItemAdded{}, want: "1"},
		// WithCallerSchemaVersion: in-range values of a type with upcasters are honored.
		{name: "honor: in-range caller value kept (1)", upcasters: true, honor: true, event: StoreOrderCreated{}, caller: "1", want: "1"},
		{name: "honor: in-range caller value kept (2)", upcasters: true, honor: true, event: StoreOrderCreated{}, caller: "2", want: "2"},
		{name: "honor: latest caller value kept", upcasters: true, honor: true, event: StoreOrderCreated{}, caller: "3", want: "3"},
		{name: "honor: no caller value stamps latest", upcasters: true, honor: true, event: StoreOrderCreated{}, want: "3"},
		{name: "honor: above-latest caller value replaced", upcasters: true, honor: true, event: StoreOrderCreated{}, caller: "9", want: "3"},
		{name: "honor: zero caller value replaced", upcasters: true, honor: true, event: StoreOrderCreated{}, caller: "0", want: "3"},
		{name: "honor: negative caller value replaced", upcasters: true, honor: true, event: StoreOrderCreated{}, caller: "-1", want: "3"},
		{name: "honor: non-integer caller value replaced", upcasters: true, honor: true, event: StoreOrderCreated{}, caller: "abc", want: "3"},
		{name: "honor: huge caller value replaced", upcasters: true, honor: true, event: StoreOrderCreated{}, caller: "99999999999999999999", want: "3"},
		{name: "honor: upcasters for other types only: caller value replaced", upcasters: true, honor: true, event: StoreItemAdded{}, caller: "5", want: "1"},
		{name: "honor: no upcasters: caller value still dropped (no range to validate against)", honor: true, event: StoreOrderCreated{}, caller: "2", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var opts []Option
			if tt.upcasters {
				opts = append(opts, WithUpcasters(newSecurityUpcasterChain(t)))
			}
			if tt.honor {
				opts = append(opts, WithCallerSchemaVersion())
			}
			store := New(memory.NewAdapter(), opts...)
			store.RegisterEvents(StoreOrderCreated{}, StoreItemAdded{})

			var appendOpts []AppendOption
			var caller map[string]string
			if tt.caller != "" {
				caller = map[string]string{schemaVersionKey: tt.caller, "app": "kept"}
				appendOpts = append(appendOpts, WithAppendMetadata(Metadata{Custom: caller}))
			}
			snapshot := cloneCustom(caller)
			require.NoError(t, store.Append(ctx, "Order-1", []interface{}{tt.event}, appendOpts...))

			stored := loadOnlyRaw(t, ctx, store, "Order-1")
			if tt.want == "" {
				assert.NotContains(t, stored.Metadata.Custom, schemaVersionKey)
			} else {
				assert.Equal(t, tt.want, stored.Metadata.Custom[schemaVersionKey])
			}
			if caller != nil {
				assert.Equal(t, "kept", stored.Metadata.Custom["app"])
				assert.Equal(t, snapshot, caller, "the caller's map must never be mutated")
			}
		})
	}
}

func TestEventStore_Append_SubjectTagsPolicy(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name   string
		tagger bool // configure userIDTagger
		merge  bool // WithCallerSubjectTags
		userID string
		caller []string // caller-supplied $subjects
		want   []string
	}{
		{name: "tagger replaces caller tags by default", tagger: true, userID: "u1", caller: []string{"victim"}, want: []string{"u1"}},
		{name: "tagger yields nothing: caller tags dropped", tagger: true, userID: "", caller: []string{"victim"}, want: nil},
		{name: "tagger without caller tags", tagger: true, userID: "u1", want: []string{"u1"}},
		{name: "WithCallerSubjectTags merges caller tags first", tagger: true, merge: true, userID: "u1", caller: []string{"victim"}, want: []string{"victim", "u1"}},
		{name: "WithCallerSubjectTags keeps caller tags when tagger yields nothing", tagger: true, merge: true, userID: "", caller: []string{"victim"}, want: []string{"victim"}},
		{name: "WithCallerSubjectTags de-duplicates", tagger: true, merge: true, userID: "u1", caller: []string{"u1", "extra"}, want: []string{"u1", "extra"}},
		{name: "no tagger: caller tags untouched", userID: "u1", caller: []string{"victim"}, want: []string{"victim"}},
		{name: "no tagger with WithCallerSubjectTags: caller tags untouched", merge: true, userID: "u1", caller: []string{"victim"}, want: []string{"victim"}},
		{name: "no tagger, no caller tags: nothing recorded", userID: "u1", want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var opts []Option
			if tt.tagger {
				opts = append(opts, WithSubjectTagger(userIDTagger))
			}
			if tt.merge {
				opts = append(opts, WithCallerSubjectTags())
			}
			store := New(memory.NewAdapter(), opts...)
			store.RegisterEvents(eraseUserCreated{})

			md := Metadata{UserID: tt.userID, Custom: map[string]string{"app": "kept"}}
			if tt.caller != nil {
				md = setSubjectTags(md, tt.caller)
			}
			snapshot := cloneCustom(md.Custom)
			require.NoError(t, store.Append(ctx, "User-1",
				[]interface{}{eraseUserCreated{UserID: tt.userID, Email: "e"}}, WithAppendMetadata(md)))

			stored := loadOnlyRaw(t, ctx, store, "User-1")
			assert.Equal(t, tt.want, GetSubjectTags(stored.Metadata))
			assert.Equal(t, "kept", stored.Metadata.Custom["app"])
			assert.Equal(t, snapshot, md.Custom, "the caller's map must never be mutated")
		})
	}
}

// TestEventStore_Append_ForgedSubjectCannotSelectKey proves the concrete attack is
// closed: with subject-scoped keys, a caller-supplied $subjects tag can no longer
// wrap another subject's key around the event (and so inject the event into that
// subject's erasure footprint). WithCallerSubjectTags re-enables it on purpose.
func TestEventStore_Append_ForgedSubjectCannotSelectKey(t *testing.T) {
	ctx := context.Background()
	newStore := func(t *testing.T, opts ...Option) *EventStore {
		t.Helper()
		provider, err := local.New(
			local.WithKey("key-u1", []byte("0123456789abcdef0123456789abcdef")),
			local.WithKey("key-victim", []byte("fedcba9876543210fedcba9876543210")),
			local.WithKey("default-key", make([]byte, 32)),
		)
		require.NoError(t, err)
		t.Cleanup(func() { _ = provider.Close() })
		cfg := NewFieldEncryptionConfig(
			WithEncryptionProvider(provider),
			WithDefaultKeyID("default-key"),
			WithSubjectKeyResolver(func(id string) string { return "key-" + id }),
			WithEncryptedFields("eraseUserCreated", "email"),
		)
		store := New(memory.NewAdapter(), append([]Option{WithFieldEncryption(cfg), WithSubjectTagger(userIDTagger)}, opts...)...)
		store.RegisterEvents(eraseUserCreated{})
		return store
	}
	forged := Metadata{UserID: "u1", Custom: map[string]string{subjectTagsKey: `["victim"]`}}
	event := eraseUserCreated{UserID: "u1", Email: "alice@example.com"}

	t.Run("default: tagger decides the subject and therefore the key", func(t *testing.T) {
		store := newStore(t)
		require.NoError(t, store.Append(ctx, "User-u1", []interface{}{event}, WithAppendMetadata(forged)))
		stored := loadOnlyRaw(t, ctx, store, "User-u1")
		assert.Equal(t, []string{"u1"}, GetSubjectTags(stored.Metadata))
		assert.Equal(t, "key-u1", GetEncryptionKeyID(stored.Metadata))
		assert.False(t, SubjectFilter("victim")(stored), "must not land in the victim's footprint")
	})

	t.Run("WithCallerSubjectTags: caller tags merge in (trusted writers only)", func(t *testing.T) {
		store := newStore(t, WithCallerSubjectTags())
		require.NoError(t, store.Append(ctx, "User-u1", []interface{}{event}, WithAppendMetadata(forged)))
		stored := loadOnlyRaw(t, ctx, store, "User-u1")
		assert.Equal(t, []string{"victim", "u1"}, GetSubjectTags(stored.Metadata))
		assert.Equal(t, "key-victim", GetEncryptionKeyID(stored.Metadata), "first tag selects the key, as documented")
	})
}

func TestEventStore_Append_PreservesOtherDollarKeys(t *testing.T) {
	ctx := context.Background()
	provider, err := local.New(local.WithKey("k", make([]byte, 32)))
	require.NoError(t, err)
	t.Cleanup(func() { _ = provider.Close() })
	cfg := NewFieldEncryptionConfig(
		WithEncryptionProvider(provider),
		WithDefaultKeyID("k"),
		WithEncryptedFields("eraseUserCreated", "email"),
	)
	// Every policy active at once: envelope stripping, version stamping, tagging, encryption.
	chain := NewUpcasterChain()
	require.NoError(t, chain.Register(newNoopUpcaster("eraseUserCreated", 1, 2)))
	store := New(memory.NewAdapter(), WithFieldEncryption(cfg), WithUpcasters(chain), WithSubjectTagger(userIDTagger))
	store.RegisterEvents(eraseUserCreated{})

	custom := map[string]string{
		erasureMarkerSubjectKey: "u1", // written by DataEraser through Append — must survive
		"$app_flag":             "yes",
		"plain":                 "value",
		encryptionKeyIDKey:      "attacker-key",
	}
	snapshot := cloneCustom(custom)
	require.NoError(t, store.Append(ctx, "User-u1",
		[]interface{}{eraseUserCreated{UserID: "u1", Email: "alice@example.com"}},
		WithAppendMetadata(Metadata{UserID: "u1", Custom: custom})))

	stored := loadOnlyRaw(t, ctx, store, "User-u1")
	assert.Equal(t, "u1", stored.Metadata.Custom[erasureMarkerSubjectKey])
	assert.Equal(t, "yes", stored.Metadata.Custom["$app_flag"])
	assert.Equal(t, "value", stored.Metadata.Custom["plain"])
	assert.Equal(t, "k", GetEncryptionKeyID(stored.Metadata))
	assert.Equal(t, "2", stored.Metadata.Custom[schemaVersionKey])
	assert.Equal(t, []string{"u1"}, GetSubjectTags(stored.Metadata))
	assert.Equal(t, snapshot, custom, "the caller's map must never be mutated")
}

func TestEventStore_Append_DoesNotMutateSharedCallerMetadata(t *testing.T) {
	ctx := context.Background()
	store := New(memory.NewAdapter(), WithUpcasters(newSecurityUpcasterChain(t)), WithSubjectTagger(userIDTagger))
	store.RegisterEvents(StoreOrderCreated{}, StoreItemAdded{})

	custom := forgedEnvelope()
	custom[schemaVersionKey] = "42"
	custom[subjectTagsKey] = `["victim"]`
	snapshot := cloneCustom(custom)
	md := Metadata{UserID: "u1", Custom: custom}

	// One metadata value is shared by every event in the batch; sanitizing the
	// first event must not change what the second one (or the caller) sees.
	require.NoError(t, store.Append(ctx, "Order-1",
		[]interface{}{StoreOrderCreated{OrderID: "1"}, StoreItemAdded{OrderID: "1", SKU: "s"}},
		WithAppendMetadata(md)))
	assert.Equal(t, snapshot, custom, "the caller's map must never be mutated")
	assert.Equal(t, snapshot, md.Custom)

	raw, err := store.LoadRaw(ctx, "Order-1", 0)
	require.NoError(t, err)
	require.Len(t, raw, 2)
	for _, se := range raw {
		for _, key := range reservedEncryptionMetadataKeys {
			assert.NotContains(t, se.Metadata.Custom, key)
		}
		assert.Equal(t, []string{"u1"}, GetSubjectTags(se.Metadata))
		assert.Equal(t, "kept", se.Metadata.Custom["app"])
	}
	assert.Equal(t, "3", raw[0].Metadata.Custom[schemaVersionKey])
	assert.Equal(t, "1", raw[1].Metadata.Custom[schemaVersionKey])

	// The caller's metadata is reusable for a further append with identical results.
	require.NoError(t, store.Append(ctx, "Order-2", []interface{}{StoreOrderCreated{OrderID: "2"}}, WithAppendMetadata(md)))
	assert.Equal(t, snapshot, custom)
}

// TestEventStore_Append_CleanMetadataSharesCallerMap pins the zero-overhead
// contract: with no reserved keys present and no feature configured, the stored
// Custom map is the caller's own (the adapter deep-copies on write), so the policy
// allocated nothing on the common path.
func TestEventStore_Append_CleanMetadataSharesCallerMap(t *testing.T) {
	custom := map[string]string{"app": "x", "$app_flag": "y"}
	ed := EventData{Type: "StoreOrderCreated", Data: []byte(`{}`), Metadata: Metadata{Custom: custom}}
	store := New(memory.NewAdapter())
	require.NoError(t, store.prepareEventData(context.Background(), "Order-1", &ed))
	assert.True(t, sameMap(custom, ed.Metadata.Custom), "clean metadata must pass through without a copy")
}
