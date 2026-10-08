package mink

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go-mink.dev/adapters"
	"go-mink.dev/adapters/memory"
	"go-mink.dev/encryption"
	"go-mink.dev/encryption/local"
)

// newRetentionKeyedStore builds a memory store whose master key is selected per
// Metadata.TenantID ("k-<tenant>"; the default "k" when no tenant is set), so tests can
// give each stream its own key and exercise the shared-key guard's exclusive and shared
// paths deliberately.
func newRetentionKeyedStore(t *testing.T, tenants ...string) (*EventStore, *local.Provider) {
	t.Helper()
	opts := []local.Option{local.WithKey("k", make([]byte, 32))}
	for _, tn := range tenants {
		opts = append(opts, local.WithKey("k-"+tn, make([]byte, 32)))
	}
	provider, err := local.New(opts...)
	require.NoError(t, err)
	cfg := NewFieldEncryptionConfig(
		WithEncryptionProvider(provider),
		WithDefaultKeyID("k"),
		WithTenantKeyResolver(func(tenant string) string {
			if tenant == "" {
				return "k"
			}
			return "k-" + tenant
		}),
		WithEncryptedFields("eraseUserCreated", "email"),
		WithDecryptionErrorHandler(func(err error, _ string, _ Metadata) error {
			if errors.Is(err, encryption.ErrKeyRevoked) {
				return nil
			}
			return err
		}),
	)
	store := New(memory.NewAdapter(), WithFieldEncryption(cfg))
	store.RegisterEvents(eraseUserCreated{})
	return store, provider
}

// appendUnderTenant appends one encrypted user event to stream, keyed by tenant (see
// newRetentionKeyedStore).
func appendUnderTenant(t *testing.T, ctx context.Context, store *EventStore, stream, tenant string) {
	t.Helper()
	require.NoError(t, store.Append(ctx, stream,
		[]interface{}{eraseUserCreated{UserID: stream, Email: "a@b.c"}},
		WithAppendMetadata(Metadata{TenantID: tenant})))
}

// seedRetention writes one user and one order event, each under its OWN key ("k-user",
// "k-order") so a policy scoped to one of them owns its key exclusively.
func seedRetention(t *testing.T, ctx context.Context, store *EventStore) {
	t.Helper()
	appendUnderTenant(t, ctx, store, "User-u1", "user")
	appendUnderTenant(t, ctx, store, "Order-o1", "order")
}

// seedSharedKey writes one user and one order event under the single default key "k" of
// a newEraseTestStore, so a policy scoped to only one of them shares its key with the
// other — the blast-radius scenario.
func seedSharedKey(t *testing.T, ctx context.Context, store *EventStore) {
	t.Helper()
	require.NoError(t, store.Append(ctx, "User-u1",
		[]interface{}{eraseUserCreated{UserID: "u1", Email: "a@b.c"}}))
	require.NoError(t, store.Append(ctx, "Order-o1",
		[]interface{}{eraseUserCreated{UserID: "u1", Email: "a@b.c"}}))
}

// appendRawEnvelope writes an event straight through the adapter — bypassing the store's
// reserved-metadata sanitizer — with the given $-metadata, to simulate on-disk envelopes
// the store itself would never stamp (a bare key id; a key the provider does not hold).
func appendRawEnvelope(t *testing.T, ctx context.Context, adapter adapters.EventStoreAdapter, streamID string, custom map[string]string) {
	t.Helper()
	_, err := adapter.Append(ctx, streamID, []adapters.EventRecord{{
		Type:     "eraseUserCreated",
		Data:     []byte(`{"userId":"u","email":"x"}`),
		Metadata: adapters.Metadata{Custom: custom},
	}}, AnyVersion)
	require.NoError(t, err)
}

// fullEnvelope is a complete field-encryption envelope under keyID (the DEK bytes are
// never unwrapped by a retention sweep, so any base64 will do).
func fullEnvelope(keyID string) map[string]string {
	return map[string]string{
		encryptedFieldsKey:     `["email"]`,
		encryptionKeyIDKey:     keyID,
		encryptedDEKKey:        "AAAA",
		encryptionAlgorithmKey: "AES-256-GCM",
	}
}

func sharedKeyErrors(errs []error) []*RetentionSharedKeyError {
	var out []*RetentionSharedKeyError
	for _, e := range errs {
		var ske *RetentionSharedKeyError
		if errors.As(e, &ske) {
			out = append(out, ske)
		}
	}
	return out
}

func hasErr(errs []error, target error) bool {
	for _, e := range errs {
		if errors.Is(e, target) {
			return true
		}
	}
	return false
}

func TestRetention_ShredByPrefix(t *testing.T) {
	ctx := context.Background()
	store, provider := newRetentionKeyedStore(t, "user", "order")
	seedRetention(t, ctx, store)

	mgr := NewRetentionManager(store, []RetentionPolicy{
		{Name: "users", StreamPrefix: "User-", Action: ActionShred},
	})
	report, err := mgr.Apply(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"k-user"}, report.KeysToRevoke)
	assert.Equal(t, []string{"k-user"}, report.KeysRevoked)
	assert.Empty(t, report.SharedKeysSkipped)
	assert.Equal(t, 1, report.Matched) // only User-u1
	assert.Empty(t, report.Errors)

	revoked, _ := provider.IsRevoked("k-user")
	assert.True(t, revoked)
	orderRevoked, _ := provider.IsRevoked("k-order")
	assert.False(t, orderRevoked, "the order stream's key is outside the policy and untouched")

	// Append-only: the events are still present (only the key was revoked).
	raw, err := store.LoadRaw(ctx, "User-u1", 0)
	require.NoError(t, err)
	assert.Len(t, raw, 1)
}

func TestRetention_AgeBased(t *testing.T) {
	ctx := context.Background()
	store, provider := newRetentionKeyedStore(t, "user", "order")
	seedRetention(t, ctx, store)

	// MaxAge 1h; with the clock at default now, nothing is old enough.
	young := NewRetentionManager(store, []RetentionPolicy{
		{Name: "old-users", StreamPrefix: "User-", MaxAge: time.Hour, Action: ActionShred},
	})
	report, err := young.Apply(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, report.Matched)
	revoked, _ := provider.IsRevoked("k-user")
	assert.False(t, revoked)

	// Advance the clock 2h: now the events exceed MaxAge.
	old := NewRetentionManager(store, []RetentionPolicy{
		{Name: "old-users", StreamPrefix: "User-", MaxAge: time.Hour, Action: ActionShred},
	}, WithRetentionClock(func() time.Time { return time.Now().Add(2 * time.Hour) }))
	report, err = old.Apply(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"k-user"}, report.KeysRevoked)
}

func TestRetention_DryRunChangesNothing(t *testing.T) {
	ctx := context.Background()
	store, provider := newRetentionKeyedStore(t, "user", "order")
	seedRetention(t, ctx, store)

	mgr := NewRetentionManager(store, []RetentionPolicy{
		{Name: "users", StreamPrefix: "User-", Action: ActionShred},
	})
	report, err := mgr.DryRun(ctx)
	require.NoError(t, err)
	assert.True(t, report.DryRun)
	assert.Equal(t, 1, report.Matched)
	assert.Equal(t, 0, report.Acted, "a dry run acts on nothing")
	assert.Equal(t, []string{"k-user"}, report.KeysToRevoke, "the preview names the key Apply would revoke")
	assert.Empty(t, report.SharedKeysSkipped)
	assert.Empty(t, report.KeysRevoked)
	assert.False(t, report.Failed())

	revoked, _ := provider.IsRevoked("k-user")
	assert.False(t, revoked, "dry-run must not revoke")
}

// Two Shred policies that together cover every event under a key make that key exclusive
// to the sweep: coverage is the union of the sweep's Shred policies, not any single one.
func TestRetention_Composition(t *testing.T) {
	ctx := context.Background()
	store, provider := newEraseTestStore(t, "k")
	seedSharedKey(t, ctx, store)

	mgr := NewRetentionManager(store, []RetentionPolicy{
		{Name: "users", StreamPrefix: "User-", Action: ActionShred},
		{Name: "orders", StreamPrefix: "Order-", Action: ActionShred},
	})
	report, err := mgr.Apply(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, report.Matched) // one per policy
	assert.Equal(t, []string{"k"}, report.KeysRevoked)
	assert.Empty(t, report.SharedKeysSkipped)
	assert.Empty(t, report.Errors)
	revoked, _ := provider.IsRevoked("k")
	assert.True(t, revoked)
}

func TestRetention_RedactWithoutHookIsLoud(t *testing.T) {
	ctx := context.Background()
	store, _ := newEraseTestStore(t, "k")
	seedSharedKey(t, ctx, store)

	policy := RetentionPolicy{Name: "mask", StreamPrefix: "User-", Action: ActionRedactFields, Fields: []string{"email"}}
	// The misconfiguration is catchable up front.
	assert.Error(t, policy.Validate())

	mgr := NewRetentionManager(store, []RetentionPolicy{policy})
	assert.NotEmpty(t, mgr.Validate())

	report, err := mgr.Apply(ctx)
	require.NoError(t, err) // non-fatal...
	assert.Equal(t, 1, report.Matched)
	assert.Equal(t, 1, report.Skipped)
	assert.Empty(t, report.KeysRevoked)
	// ...but NOT silent: a redact/anonymize policy with no Apply hook is surfaced loudly.
	assert.True(t, report.Failed(), "a redact/anonymize policy with no Apply hook must fail loudly, not skip silently")
	assert.NotEmpty(t, report.Errors)

	// A DryRun surfaces it too, before any real sweep.
	dry, err := mgr.DryRun(ctx)
	require.NoError(t, err)
	assert.True(t, dry.Failed())
}

func TestRetention_RedactHookErrorIsReported(t *testing.T) {
	ctx := context.Background()
	store, _ := newEraseTestStore(t, "k")
	seedSharedKey(t, ctx, store)

	mgr := NewRetentionManager(store, []RetentionPolicy{{
		Name: "mask", StreamPrefix: "User-", Action: ActionRedactFields, Fields: []string{"email"},
		Apply: func(context.Context, StoredEvent) error { return errors.New("mask failed") },
	}})
	report, err := mgr.Apply(ctx)
	require.NoError(t, err) // a hook failure is reported, never fatal to the sweep
	assert.Equal(t, 1, report.Matched)
	assert.Equal(t, 0, report.Acted)
	assert.True(t, report.Failed())
	require.NotEmpty(t, report.Errors)
	assert.Contains(t, report.Errors[0].Error(), "mask failed")
}

// A Shred match on plaintext cannot be erased. It is skipped — and the report says so
// loudly (UnencryptedMatches + ErrRetentionUnencryptedMatches) on Apply and DryRun alike,
// so a shred sweep never looks fully successful while matched plaintext remains.
func TestRetention_ShredWithoutEncryptionIsSkippedAndLoud(t *testing.T) {
	ctx := context.Background()
	// A plain store with no field encryption: events carry no envelope, so there is
	// nothing to crypto-shred — the policy matches but the event is skipped.
	store := New(memory.NewAdapter())
	store.RegisterEvents(eraseUserCreated{})
	require.NoError(t, store.Append(ctx, "Plain-p1", []interface{}{eraseUserCreated{UserID: "u1"}}))

	mgr := NewRetentionManager(store, []RetentionPolicy{{Name: "p", StreamPrefix: "Plain-", Action: ActionShred}})
	report, err := mgr.Apply(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, report.Matched)
	assert.Equal(t, 1, report.Skipped, "no encryption envelope means nothing to shred")
	assert.Equal(t, 1, report.UnencryptedMatches)
	assert.Empty(t, report.KeysToRevoke)
	assert.Empty(t, report.KeysRevoked)
	assert.True(t, report.Failed(), "matched plaintext must never pass as a successful shred")
	assert.True(t, hasErr(report.Errors, ErrRetentionUnencryptedMatches))

	dry, err := mgr.DryRun(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, dry.UnencryptedMatches)
	assert.Equal(t, 0, dry.Skipped, "DryRun classifies but does not count as skipped")
	assert.True(t, hasErr(dry.Errors, ErrRetentionUnencryptedMatches))
}

// A bare $encryption_key_id with no envelope (no encrypted-fields list, no wrapped DEK)
// is plaintext as far as shredding is concerned: the key it names must NOT be collected,
// or the sweep would revoke — and shred everything under — a key protecting none of the
// matched data.
func TestRetention_Shred_BareKeyIDIsNotEncrypted(t *testing.T) {
	ctx := context.Background()
	store, provider := newEraseTestStore(t, "k")
	appendRawEnvelope(t, ctx, store.Adapter(), "User-bare", map[string]string{encryptionKeyIDKey: "k"})

	mgr := NewRetentionManager(store, []RetentionPolicy{{Name: "users", StreamPrefix: "User-", Action: ActionShred}})
	report, err := mgr.Apply(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, report.Matched)
	assert.Equal(t, 1, report.UnencryptedMatches, "a bare key id is not an encryption envelope")
	assert.Empty(t, report.KeysToRevoke)
	assert.Empty(t, report.KeysRevoked)
	revoked, _ := provider.IsRevoked("k")
	assert.False(t, revoked, "the named key must survive: nothing under it was ciphertext")

	// Envelope semantics, directly.
	assert.False(t, HasEncryptionEnvelope(Metadata{}))
	assert.False(t, HasEncryptionEnvelope(Metadata{Custom: map[string]string{encryptedFieldsKey: `["email"]`}}), "fields without key id / DEK")
	assert.False(t, HasEncryptionEnvelope(Metadata{Custom: map[string]string{encryptedFieldsKey: `["email"]`, encryptionKeyIDKey: "k"}}), "no DEK")
	assert.True(t, HasEncryptionEnvelope(Metadata{Custom: fullEnvelope("k")}))
}

// With a single default key, a policy scoped to one category shares its key with every
// other category: the guard refuses to revoke, lists the key, and fails the report.
func TestRetention_SharedKeyGuard_SkipsSharedKey(t *testing.T) {
	ctx := context.Background()
	store, provider := newEraseTestStore(t, "k")
	seedSharedKey(t, ctx, store)

	mgr := NewRetentionManager(store, []RetentionPolicy{
		{Name: "users", Category: "User", Action: ActionShred},
	})
	report, err := mgr.Apply(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, report.Matched)
	assert.Equal(t, 0, report.Acted, "a match whose key the guard refused was not acted on")
	assert.Equal(t, 1, report.Skipped, "the refused match is a residual")
	assert.Equal(t, []string{"k"}, report.SharedKeysSkipped)
	assert.Empty(t, report.KeysToRevoke)
	assert.Empty(t, report.KeysRevoked)
	assert.True(t, report.Failed(), "a skipped key must make the sweep visibly incomplete")
	assert.True(t, hasErr(report.Errors, ErrRetentionSharedKey))
	skes := sharedKeyErrors(report.Errors)
	require.Len(t, skes, 1, "exactly one error per shared key")
	assert.Equal(t, "k", skes[0].KeyID)
	assert.Equal(t, 1, skes[0].OutOfScope, "Order-o1 is the one event outside the policy")
	assert.Contains(t, skes[0].Error(), `"k"`)
	assert.ErrorIs(t, skes[0], ErrRetentionSharedKey)
	assert.Equal(t, ErrRetentionSharedKey, errors.Unwrap(skes[0]))

	revoked, _ := provider.IsRevoked("k")
	assert.False(t, revoked, "a shared key must never be revoked by default")
}

// A key is also shared when the only other events under it are matched by the policy but
// not yet old enough: revoking it now would shred them ahead of their retention window.
func TestRetention_SharedKeyGuard_PendingEventsShareKey(t *testing.T) {
	ctx := context.Background()
	store, provider := newEraseTestStore(t, "k")
	require.NoError(t, store.Append(ctx, "User-old", []interface{}{eraseUserCreated{UserID: "old", Email: "o@x.y"}}))
	time.Sleep(120 * time.Millisecond)
	require.NoError(t, store.Append(ctx, "User-young", []interface{}{eraseUserCreated{UserID: "young", Email: "y@x.y"}}))

	mgr := NewRetentionManager(store, []RetentionPolicy{
		{Name: "users", StreamPrefix: "User-", MaxAge: 60 * time.Millisecond, Action: ActionShred},
	})
	report, err := mgr.Apply(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, report.Matched, "only the aged event matches")
	assert.Equal(t, []string{"k"}, report.SharedKeysSkipped, "the young event shares the key")
	assert.Empty(t, report.KeysRevoked)
	revoked, _ := provider.IsRevoked("k")
	assert.False(t, revoked)
}

// A RedactFields/Anonymize match does not consent to erasure: only Shred policies count as
// covering an event, so a key split between a Shred scope and a Redact scope is shared.
func TestRetention_SharedKeyGuard_RedactPolicyDoesNotCover(t *testing.T) {
	ctx := context.Background()
	store, provider := newEraseTestStore(t, "k")
	seedSharedKey(t, ctx, store)

	mgr := NewRetentionManager(store, []RetentionPolicy{
		{Name: "users", StreamPrefix: "User-", Action: ActionShred},
		{Name: "orders", StreamPrefix: "Order-", Action: ActionRedactFields, Fields: []string{"email"},
			Apply: func(context.Context, StoredEvent) error { return nil }},
	})
	report, err := mgr.Apply(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, report.Matched)
	assert.Equal(t, []string{"k"}, report.SharedKeysSkipped)
	assert.Empty(t, report.KeysRevoked)
	revoked, _ := provider.IsRevoked("k")
	assert.False(t, revoked)
}

// DryRun previews the blast radius — which keys would be revoked and which the guard
// would refuse — without revoking anything.
func TestRetention_SharedKeyGuard_DryRunPreview(t *testing.T) {
	ctx := context.Background()
	store, provider := newRetentionKeyedStore(t, "user")
	appendUnderTenant(t, ctx, store, "User-u1", "user") // exclusive key k-user
	// Both under the default key "k": a policy on Order- alone shares it with Invoice-.
	require.NoError(t, store.Append(ctx, "Order-o1", []interface{}{eraseUserCreated{UserID: "o1", Email: "o@x.y"}}))
	require.NoError(t, store.Append(ctx, "Invoice-i1", []interface{}{eraseUserCreated{UserID: "i1", Email: "i@x.y"}}))

	mgr := NewRetentionManager(store, []RetentionPolicy{
		{Name: "users", StreamPrefix: "User-", Action: ActionShred},
		{Name: "orders", StreamPrefix: "Order-", Action: ActionShred},
	})
	dry, err := mgr.DryRun(ctx)
	require.NoError(t, err)
	assert.True(t, dry.DryRun)
	assert.Equal(t, []string{"k-user"}, dry.KeysToRevoke, "exclusive key: would be revoked")
	assert.Equal(t, []string{"k"}, dry.SharedKeysSkipped, "shared key: would be refused")
	assert.Empty(t, dry.KeysRevoked)
	assert.True(t, dry.Failed())
	assert.True(t, hasErr(dry.Errors, ErrRetentionSharedKey))
	for _, k := range []string{"k", "k-user"} {
		revoked, _ := provider.IsRevoked(k)
		assert.False(t, revoked, "dry-run must not revoke %q", k)
	}

	// Apply then does exactly what the preview said.
	rep, err := mgr.Apply(ctx)
	require.NoError(t, err)
	assert.Equal(t, dry.KeysToRevoke, rep.KeysToRevoke)
	assert.Equal(t, dry.SharedKeysSkipped, rep.SharedKeysSkipped)
	assert.Equal(t, []string{"k-user"}, rep.KeysRevoked)
}

// WithAllowSharedKeyRevocation disables the guard: the shared key is revoked, nothing is
// skipped, and the out-of-scope events are shredded with it (the documented blast radius).
func TestRetention_SharedKeyGuard_AllowSharedKeyRevocation(t *testing.T) {
	ctx := context.Background()
	store, provider := newEraseTestStore(t, "k")
	seedSharedKey(t, ctx, store)

	mgr := NewRetentionManager(store, []RetentionPolicy{
		{Name: "users", StreamPrefix: "User-", Action: ActionShred},
	}, WithAllowSharedKeyRevocation())
	report, err := mgr.Apply(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"k"}, report.KeysToRevoke)
	assert.Equal(t, []string{"k"}, report.KeysRevoked)
	assert.Empty(t, report.SharedKeysSkipped)
	assert.Empty(t, report.Errors)
	revoked, _ := provider.IsRevoked("k")
	assert.True(t, revoked)
}

// flakyScanAdapter wraps the memory adapter so a test can fail a chosen scan call:
// LoadFromPosition errors on its failOnCall-th invocation (1-based; 0 = never), and/or
// cancels the context once a call has returned. Call 1 is the main sweep's first batch;
// with a store smaller than the batch size, call 2 is the shared-key guard's scan.
type flakyScanAdapter struct {
	*memory.MemoryAdapter
	calls      int
	failOnCall int
	cancel     context.CancelFunc
}

func (f *flakyScanAdapter) LoadFromPosition(ctx context.Context, from uint64, limit int) ([]adapters.StoredEvent, error) {
	f.calls++
	if f.failOnCall > 0 && f.calls == f.failOnCall {
		return nil, errors.New("scan boom")
	}
	res, err := f.MemoryAdapter.LoadFromPosition(ctx, from, limit)
	if f.cancel != nil {
		f.cancel()
	}
	return res, err
}

// failingCheckpointStore fails GetCheckpoint / SetCheckpoint with the configured errors.
type failingCheckpointStore struct {
	getErr, setErr error
}

func (f failingCheckpointStore) GetCheckpoint(context.Context, string) (uint64, error) {
	return 0, f.getErr
}
func (f failingCheckpointStore) SetCheckpoint(context.Context, string, uint64) error { return f.setErr }
func (f failingCheckpointStore) DeleteCheckpoint(context.Context, string) error      { return nil }
func (f failingCheckpointStore) GetAllCheckpoints(context.Context) (map[string]uint64, error) {
	return nil, nil
}

func newFlakyRetentionStore(t *testing.T, adapter adapters.EventStoreAdapter) (*EventStore, *local.Provider) {
	t.Helper()
	provider, err := local.New(local.WithKey("k", make([]byte, 32)))
	require.NoError(t, err)
	cfg := NewFieldEncryptionConfig(
		WithEncryptionProvider(provider),
		WithDefaultKeyID("k"),
		WithEncryptedFields("eraseUserCreated", "email"),
	)
	store := New(adapter, WithFieldEncryption(cfg))
	store.RegisterEvents(eraseUserCreated{})
	return store, provider
}

// If the guard cannot complete its verification scan, exclusivity is unproven and nothing
// may be revoked: the sweep fails rather than guessing.
func TestRetention_SharedKeyGuard_ScanFailureIsFatal(t *testing.T) {
	t.Run("load error", func(t *testing.T) {
		ctx := context.Background()
		adapter := &flakyScanAdapter{MemoryAdapter: memory.NewAdapter(), failOnCall: 2}
		store, provider := newFlakyRetentionStore(t, adapter)
		require.NoError(t, store.Append(ctx, "User-u1", []interface{}{eraseUserCreated{UserID: "u1", Email: "a@b.c"}}))

		mgr := NewRetentionManager(store, []RetentionPolicy{{Name: "users", StreamPrefix: "User-", Action: ActionShred}})
		rep, err := mgr.Apply(ctx)
		require.Error(t, err)
		assert.Nil(t, rep)
		assert.Contains(t, err.Error(), "shared-key scan")
		revoked, _ := provider.IsRevoked("k")
		assert.False(t, revoked, "nothing is revoked when the guard cannot verify")
	})

	t.Run("context cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		adapter := &flakyScanAdapter{MemoryAdapter: memory.NewAdapter(), cancel: cancel}
		store, provider := newFlakyRetentionStore(t, adapter)
		require.NoError(t, store.Append(ctx, "User-u1", []interface{}{eraseUserCreated{UserID: "u1", Email: "a@b.c"}}))

		mgr := NewRetentionManager(store, []RetentionPolicy{{Name: "users", StreamPrefix: "User-", Action: ActionShred}})
		_, err := mgr.Apply(ctx)
		require.ErrorIs(t, err, context.Canceled)
		revoked, _ := provider.IsRevoked("k")
		assert.False(t, revoked)
	})
}

// The guard walks the store in batches; a batch boundary must not drop events, and
// plaintext events (no envelope) never count as sharing a key — a revocation does not
// touch them.
func TestRetention_SharedKeyGuard_BatchedScan(t *testing.T) {
	ctx := context.Background()
	store, provider := newEraseTestStore(t, "k")
	for _, s := range []string{"User-a", "User-b", "User-c"} {
		require.NoError(t, store.Append(ctx, s, []interface{}{eraseUserCreated{UserID: s, Email: "a@b.c"}}))
	}
	// A plaintext event outside the policy: irrelevant to the guard.
	appendRawEnvelope(t, ctx, store.Adapter(), "Audit-plain", nil)
	// The sharing event is last, past the first batch boundary.
	require.NoError(t, store.Append(ctx, "Order-o1", []interface{}{eraseUserCreated{UserID: "o1", Email: "a@b.c"}}))

	mgr := NewRetentionManager(store, []RetentionPolicy{{Name: "users", StreamPrefix: "User-", Action: ActionShred}},
		WithRetentionBatchSize(2))
	report, err := mgr.Apply(ctx)
	require.NoError(t, err)
	assert.Equal(t, 3, report.Matched)
	assert.Equal(t, []string{"k"}, report.SharedKeysSkipped)
	skes := sharedKeyErrors(report.Errors)
	require.Len(t, skes, 1)
	assert.Equal(t, 1, skes[0].OutOfScope, "only the encrypted order event shares the key; plaintext does not count")
	revoked, _ := provider.IsRevoked("k")
	assert.False(t, revoked)

	// Without the sharing event the batched guard proves exclusivity (the plaintext event
	// is still ignored).
	exclusive, _ := newEraseTestStore(t, "k")
	for _, s := range []string{"User-a", "User-b"} {
		require.NoError(t, exclusive.Append(ctx, s, []interface{}{eraseUserCreated{UserID: s, Email: "a@b.c"}}))
	}
	appendRawEnvelope(t, ctx, exclusive.Adapter(), "Audit-plain", nil)
	rep2, err := NewRetentionManager(exclusive, []RetentionPolicy{{Name: "users", StreamPrefix: "User-", Action: ActionShred}},
		WithRetentionBatchSize(1)).Apply(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"k"}, rep2.KeysRevoked)
	assert.Empty(t, rep2.SharedKeysSkipped)
}

// The TenantID matcher scopes a policy to one tenant's events.
func TestRetention_TenantMatcher(t *testing.T) {
	ctx := context.Background()
	store, provider := newRetentionKeyedStore(t, "user", "order")
	seedRetention(t, ctx, store)

	mgr := NewRetentionManager(store, []RetentionPolicy{{Name: "tenant", TenantID: "user", Action: ActionShred}})
	report, err := mgr.Apply(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, report.Matched, "only the user tenant's event matches")
	assert.Equal(t, []string{"k-user"}, report.KeysRevoked)
	assert.Empty(t, report.Errors)
	revoked, _ := provider.IsRevoked("k-order")
	assert.False(t, revoked)
}

// Scan failures in the main sweep are fatal (the report would be meaningless), as is an
// adapter that cannot scan at all.
func TestRetention_ScanFailures(t *testing.T) {
	policies := []RetentionPolicy{{Name: "users", StreamPrefix: "User-", Action: ActionShred}}

	t.Run("load error", func(t *testing.T) {
		ctx := context.Background()
		adapter := &flakyScanAdapter{MemoryAdapter: memory.NewAdapter(), failOnCall: 1}
		store, _ := newFlakyRetentionStore(t, adapter)
		require.NoError(t, store.Append(ctx, "User-u1", []interface{}{eraseUserCreated{UserID: "u1", Email: "a@b.c"}}))

		rep, err := NewRetentionManager(store, policies).Apply(ctx)
		require.Error(t, err)
		assert.Nil(t, rep)
		assert.Contains(t, err.Error(), "retention scan from")
	})

	t.Run("context cancelled between batches", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		adapter := &flakyScanAdapter{MemoryAdapter: memory.NewAdapter(), cancel: cancel}
		store, provider := newFlakyRetentionStore(t, adapter)
		require.NoError(t, store.Append(ctx, "User-u1", []interface{}{eraseUserCreated{UserID: "u1", Email: "a@b.c"}}))
		require.NoError(t, store.Append(ctx, "User-u2", []interface{}{eraseUserCreated{UserID: "u2", Email: "a@b.c"}}))

		// Batch size 1: the first batch returns, the adapter cancels, and the next loop
		// iteration sees the cancelled context before loading the second batch.
		_, err := NewRetentionManager(store, policies, WithRetentionBatchSize(1)).Apply(ctx)
		require.ErrorIs(t, err, context.Canceled)
		revoked, _ := provider.IsRevoked("k")
		assert.False(t, revoked)
	})

	t.Run("adapter cannot scan", func(t *testing.T) {
		store := New(&minimalExportAdapter{})
		rep, err := NewRetentionManager(store, policies).Apply(context.Background())
		require.ErrorIs(t, err, ErrExportScanNotSupported)
		assert.Nil(t, rep)
	})
}

// A checkpoint that cannot be read is fatal (the sweep has no safe start point); one that
// cannot be written is reported, since the sweep itself has already done its work.
func TestRetention_CheckpointErrors(t *testing.T) {
	ctx := context.Background()
	store, _ := newRetentionKeyedStore(t, "user")
	appendUnderTenant(t, ctx, store, "User-u1", "user")
	policies := []RetentionPolicy{{Name: "users", StreamPrefix: "User-", Action: ActionShred}}

	t.Run("read failure is fatal", func(t *testing.T) {
		cp := failingCheckpointStore{getErr: errors.New("get boom")}
		rep, err := NewRetentionManager(store, policies, WithRetentionCheckpoint(cp, retentionCP)).Apply(ctx)
		require.Error(t, err)
		assert.Nil(t, rep)
		assert.Contains(t, err.Error(), "retention read checkpoint")
		assert.Contains(t, err.Error(), "get boom")
	})

	t.Run("write failure is reported", func(t *testing.T) {
		cp := failingCheckpointStore{setErr: errors.New("set boom")}
		rep, err := NewRetentionManager(store, policies, WithRetentionCheckpoint(cp, retentionCP)).Apply(ctx)
		require.NoError(t, err)
		assert.Equal(t, []string{"k-user"}, rep.KeysRevoked, "the sweep still acted")
		require.True(t, rep.Failed())
		assert.True(t, hasErr(rep.Errors, cp.setErr), "the checkpoint write failure must be surfaced: %v", rep.Errors)
	})
}

// A store without an encryption config can still read envelopes written by one; it can
// match them but has no provider to revoke through — reported, not silent.
func TestRetention_Shred_EncryptionNotConfigured(t *testing.T) {
	ctx := context.Background()
	store := New(memory.NewAdapter())
	store.RegisterEvents(eraseUserCreated{})
	appendRawEnvelope(t, ctx, store.Adapter(), "User-u1", fullEnvelope("k"))

	mgr := NewRetentionManager(store, []RetentionPolicy{{Name: "users", StreamPrefix: "User-", Action: ActionShred}})
	report, err := mgr.Apply(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"k"}, report.KeysToRevoke)
	assert.Empty(t, report.KeysRevoked)
	assert.True(t, hasErr(report.Errors, ErrErasureNotConfigured))
}

// A revocation failure (here: the provider does not hold the key named by the envelope)
// is reported per key and does not abort the sweep.
func TestRetention_Shred_RevokeErrorIsReported(t *testing.T) {
	ctx := context.Background()
	store, provider := newEraseTestStore(t, "k")
	require.NoError(t, store.Append(ctx, "User-u1", []interface{}{eraseUserCreated{UserID: "u1", Email: "a@b.c"}}))
	appendRawEnvelope(t, ctx, store.Adapter(), "User-ghost", fullEnvelope("ghost"))

	mgr := NewRetentionManager(store, []RetentionPolicy{{Name: "users", StreamPrefix: "User-", Action: ActionShred}})
	report, err := mgr.Apply(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"ghost", "k"}, report.KeysToRevoke)
	assert.Equal(t, []string{"k"}, report.KeysRevoked, "the good key is still revoked")
	assert.True(t, hasErr(report.Errors, encryption.ErrKeyNotFound))
	revoked, _ := provider.IsRevoked("k")
	assert.True(t, revoked)
}

func TestRetentionPolicy_Validate_Unscoped(t *testing.T) {
	noop := func(context.Context, StoredEvent) error { return nil }
	tests := []struct {
		name    string
		policy  RetentionPolicy
		wantErr error
	}{
		{"shred with no matchers", RetentionPolicy{Name: "p", Action: ActionShred}, ErrRetentionUnscopedPolicy},
		{"redact with no matchers", RetentionPolicy{Name: "p", Action: ActionRedactFields, Apply: noop}, ErrRetentionUnscopedPolicy},
		{"anonymize with no matchers", RetentionPolicy{Name: "p", Action: ActionAnonymize, Apply: noop}, ErrRetentionUnscopedPolicy},
		{"unscoped beats missing-hook", RetentionPolicy{Name: "p", Action: ActionRedactFields}, ErrRetentionUnscopedPolicy},
		{"category scopes", RetentionPolicy{Name: "p", Category: "User", Action: ActionShred}, nil},
		{"stream prefix scopes", RetentionPolicy{Name: "p", StreamPrefix: "User-", Action: ActionShred}, nil},
		{"event types scope", RetentionPolicy{Name: "p", EventTypes: []string{"E"}, Action: ActionShred}, nil},
		{"tenant scopes", RetentionPolicy{Name: "p", TenantID: "t", Action: ActionShred}, nil},
		{"max age scopes", RetentionPolicy{Name: "p", MaxAge: time.Hour, Action: ActionShred}, nil},
		{"negative max age does not scope", RetentionPolicy{Name: "p", MaxAge: -time.Hour, Action: ActionShred}, ErrRetentionUnscopedPolicy},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.policy.Validate()
			if tt.wantErr == nil {
				assert.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, tt.wantErr)
			assert.True(t, len(err.Error()) > 5 && err.Error()[:5] == "mink:", "error is mink-prefixed: %q", err.Error())
			assert.Contains(t, err.Error(), `"p"`)
		})
	}
}

// An unscoped policy is reported on every run AND left inert: it never matches, acts or
// revokes — a whole-store shred reported after the fact would be no guard at all.
func TestRetention_UnscopedPolicyIsInert(t *testing.T) {
	ctx := context.Background()
	store, provider := newEraseTestStore(t, "k")
	seedSharedKey(t, ctx, store)

	mgr := NewRetentionManager(store, []RetentionPolicy{
		{Name: "everything", Action: ActionShred},
		{Name: "users", StreamPrefix: "User-", Action: ActionShred, MaxAge: time.Hour}, // scoped, pending
	})
	require.Len(t, mgr.Validate(), 1)

	for _, run := range []func(context.Context) (*RetentionReport, error){mgr.DryRun, mgr.Apply} {
		report, err := run(ctx)
		require.NoError(t, err)
		assert.Equal(t, 2, report.Scanned)
		assert.Equal(t, 0, report.Matched, "the unscoped policy must not match anything")
		assert.Empty(t, report.KeysToRevoke)
		assert.Empty(t, report.KeysRevoked)
		assert.True(t, report.Failed())
		assert.True(t, hasErr(report.Errors, ErrRetentionUnscopedPolicy))
	}
	revoked, _ := provider.IsRevoked("k")
	assert.False(t, revoked)
}

func TestRetention_CategoryDashlessAndEventTypeMiss(t *testing.T) {
	ctx := context.Background()
	store, _ := newEraseTestStore(t, "k")
	// A dash-less stream id: streamCategory("Singleton") returns the whole id.
	require.NoError(t, store.Append(ctx, "Singleton",
		[]interface{}{eraseUserCreated{UserID: "u1", Email: "a@b.c"}}))

	// EventTypes that exclude the event's type must not match (containsString miss).
	miss := NewRetentionManager(store, []RetentionPolicy{
		{Name: "miss", Category: "Singleton", EventTypes: []string{"OtherEvent"}, Action: ActionShred},
	})
	rep, err := miss.Apply(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, rep.Matched)

	// The category alone (dash-less) matches.
	hit := NewRetentionManager(store, []RetentionPolicy{
		{Name: "hit", Category: "Singleton", Action: ActionShred},
	})
	rep2, err := hit.Apply(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, rep2.Matched)
	assert.Equal(t, []string{"k"}, rep2.KeysRevoked)
}

func TestRetention_RedactWithHook(t *testing.T) {
	ctx := context.Background()
	store, _ := newEraseTestStore(t, "k")
	seedSharedKey(t, ctx, store)

	var applied int
	mgr := NewRetentionManager(store, []RetentionPolicy{{
		Name: "mask", StreamPrefix: "User-", Action: ActionRedactFields, Fields: []string{"email"},
		Apply: func(context.Context, StoredEvent) error { applied++; return nil },
	}})
	report, err := mgr.Apply(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, report.Acted)
	assert.Equal(t, 1, applied)
	assert.False(t, report.Failed(), "a correctly-configured redact policy does not flag Failed")

	// A dry run never invokes the hook.
	dry, err := mgr.DryRun(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, dry.Matched)
	assert.Equal(t, 0, dry.Acted)
	assert.Equal(t, 1, applied, "DryRun must not run Apply hooks")
}
