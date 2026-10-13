package mink

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go-mink.dev/adapters/memory"
)

// =============================================================================
// withTrustedMetadata — the store's own re-append path (ReEncryptStream)
//
// Trusted metadata originates from events the store already holds, so the
// "$schema_version" marker is persisted verbatim instead of being dropped or
// re-stamped. Nothing else is trusted: the encryption envelope is still stripped
// and "$subjects" follows the same tagger policy as a caller append.
// =============================================================================

func TestEventStore_Append_TrustedMetadata_PreservesSchemaVersionVerbatim(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name      string
		upcasters bool
		event     interface{}
		carried   string // carried-over $schema_version; "" = absent
		want      string // persisted; "" = absent
	}{
		{name: "no chain: in-range marker kept", event: StoreOrderCreated{}, carried: "2", want: "2"},
		{name: "no chain: absent marker stays absent", event: StoreOrderCreated{}, want: ""},
		{name: "chain: older marker kept verbatim (caller is responsible)", upcasters: true, event: StoreOrderCreated{}, carried: "2", want: "2"},
		{name: "chain: latest marker kept", upcasters: true, event: StoreOrderCreated{}, carried: "3", want: "3"},
		{name: "chain: absent marker is NOT stamped", upcasters: true, event: StoreOrderCreated{}, want: ""},
		{name: "chain, type without upcasters: marker kept verbatim", upcasters: true, event: StoreItemAdded{}, carried: "1", want: "1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var opts []Option
			if tt.upcasters {
				opts = append(opts, WithUpcasters(newSecurityUpcasterChain(t)))
			}
			store := New(memory.NewAdapter(), opts...)
			store.RegisterEvents(StoreOrderCreated{}, StoreItemAdded{})

			md := Metadata{Custom: map[string]string{"app": "kept"}}
			if tt.carried != "" {
				md.Custom[schemaVersionKey] = tt.carried
			}
			snapshot := cloneCustom(md.Custom)
			require.NoError(t, store.Append(ctx, "Order-1", []interface{}{tt.event},
				WithAppendMetadata(md), withTrustedMetadata()))

			stored := loadOnlyRaw(t, ctx, store, "Order-1")
			if tt.want == "" {
				assert.NotContains(t, stored.Metadata.Custom, schemaVersionKey)
			} else {
				assert.Equal(t, tt.want, stored.Metadata.Custom[schemaVersionKey])
			}
			assert.Equal(t, "kept", stored.Metadata.Custom["app"])
			assert.Equal(t, snapshot, md.Custom, "the caller's map must never be mutated")
		})
	}
}

// Trust is limited to the schema version: a forged envelope is still stripped and
// re-stamped, and "$subjects" is still decided by the tagger.
func TestEventStore_Append_TrustedMetadata_StillSanitizesEnvelopeAndTags(t *testing.T) {
	ctx := context.Background()

	t.Run("envelope keys are stripped", func(t *testing.T) {
		store := New(memory.NewAdapter())
		store.RegisterEvents(StoreOrderCreated{})
		require.NoError(t, store.Append(ctx, "Order-1", []interface{}{StoreOrderCreated{}},
			WithAppendMetadata(Metadata{Custom: forgedEnvelope()}), withTrustedMetadata()))
		stored := loadOnlyRaw(t, ctx, store, "Order-1")
		for _, key := range reservedEncryptionMetadataKeys {
			assert.NotContains(t, stored.Metadata.Custom, key)
		}
		assert.False(t, IsEncrypted(stored.Metadata))
		assert.Equal(t, "kept", stored.Metadata.Custom["app"])
	})

	t.Run("tagger replaces carried-over tags by default", func(t *testing.T) {
		store := New(memory.NewAdapter(), WithSubjectTagger(userIDTagger))
		store.RegisterEvents(eraseUserCreated{})
		md := setSubjectTags(Metadata{UserID: "u1"}, []string{"victim"})
		require.NoError(t, store.Append(ctx, "User-1", []interface{}{eraseUserCreated{UserID: "u1", Email: "e"}},
			WithAppendMetadata(md), withTrustedMetadata()))
		stored := loadOnlyRaw(t, ctx, store, "User-1")
		assert.Equal(t, []string{"u1"}, GetSubjectTags(stored.Metadata))
	})

	t.Run("WithCallerSubjectTags merges carried-over tags", func(t *testing.T) {
		store := New(memory.NewAdapter(), WithSubjectTagger(userIDTagger), WithCallerSubjectTags())
		store.RegisterEvents(eraseUserCreated{})
		md := setSubjectTags(Metadata{UserID: "u1"}, []string{"manual"})
		require.NoError(t, store.Append(ctx, "User-1", []interface{}{eraseUserCreated{UserID: "u1", Email: "e"}},
			WithAppendMetadata(md), withTrustedMetadata()))
		stored := loadOnlyRaw(t, ctx, store, "User-1")
		assert.Equal(t, []string{"manual", "u1"}, GetSubjectTags(stored.Metadata))
	})

	t.Run("no tagger: carried-over tags are kept", func(t *testing.T) {
		store := New(memory.NewAdapter())
		store.RegisterEvents(eraseUserCreated{})
		md := setSubjectTags(Metadata{}, []string{"manual"})
		require.NoError(t, store.Append(ctx, "User-1", []interface{}{eraseUserCreated{UserID: "u1", Email: "e"}},
			WithAppendMetadata(md), withTrustedMetadata()))
		stored := loadOnlyRaw(t, ctx, store, "User-1")
		assert.Equal(t, []string{"manual"}, GetSubjectTags(stored.Metadata))
	})
}

// The public option is not trusted: a caller-supplied value is only honored when
// it is in range for a type with upcasters, and a downgrade cannot poison replay.
func TestEventStore_Append_CallerSchemaVersion_DowngradeStillRoutesChainCorrectly(t *testing.T) {
	ctx := context.Background()
	chain := NewUpcasterChain()
	require.NoError(t, chain.Register(newTestUpcaster("StoreOrderCreated", 1, 2, addJSONFieldUpcastFn("v2", true))))

	t.Run("default: forged v1 marker on current data is replaced, so the upcaster does not re-run", func(t *testing.T) {
		store := New(memory.NewAdapter(), WithUpcasters(chain))
		store.RegisterEvents(StoreOrderCreated{})
		require.NoError(t, store.Append(ctx, "Order-1", []interface{}{StoreOrderCreated{}},
			WithAppendMetadata(Metadata{Custom: map[string]string{schemaVersionKey: "1"}})))
		stored := loadOnlyRaw(t, ctx, store, "Order-1")
		assert.Equal(t, "2", stored.Metadata.Custom[schemaVersionKey])
	})

	t.Run("WithCallerSchemaVersion: in-range marker honored (migration tooling)", func(t *testing.T) {
		store := New(memory.NewAdapter(), WithUpcasters(chain), WithCallerSchemaVersion())
		store.RegisterEvents(StoreOrderCreated{})
		require.NoError(t, store.Append(ctx, "Order-1", []interface{}{StoreOrderCreated{}},
			WithAppendMetadata(Metadata{Custom: map[string]string{schemaVersionKey: "1"}})))
		stored := loadOnlyRaw(t, ctx, store, "Order-1")
		assert.Equal(t, "1", stored.Metadata.Custom[schemaVersionKey])
	})
}
