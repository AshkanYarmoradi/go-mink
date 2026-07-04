package mink

import (
	"context"
	"crypto/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go-mink.dev/adapters"
	"go-mink.dev/adapters/memory"
	"go-mink.dev/encryption/local"
)

// covNonSubAdapter is an EventStoreAdapter that is NOT a SubscriptionAdapter, so
// LoadEventsFromPosition returns ErrSubscriptionNotSupported.
type covNonSubAdapter struct{ adapters.EventStoreAdapter }

// covEncEvent has one encrypted field.
type covEncEvent struct {
	Name string `json:"name"`
}

// covLiveProj is a minimal LiveProjection.
type covLiveProj struct {
	ProjectionBase
}

func newCovLiveProj() *covLiveProj {
	return &covLiveProj{ProjectionBase: NewProjectionBase("covlive", "covEncEvent")}
}
func (p *covLiveProj) OnEvent(context.Context, StoredEvent) {}
func (p *covLiveProj) IsTransient() bool                    { return true }

// covEncStore builds an in-memory store with field encryption for the "covEncEvent" type.
func covEncStore(t *testing.T) (*EventStore, *local.Provider) {
	t.Helper()
	key := make([]byte, 32)
	_, err := rand.Read(key)
	require.NoError(t, err)
	provider, err := local.New(local.WithKey("k", key))
	require.NoError(t, err)
	t.Cleanup(func() { _ = provider.Close() })
	store := New(memory.NewAdapter(), WithFieldEncryption(NewFieldEncryptionConfig(
		WithEncryptionProvider(provider),
		WithDefaultKeyID("k"),
		WithEncryptedFields("covEncEvent", "name"),
	)))
	store.RegisterEvents(covEncEvent{})
	return store, provider
}

// TestNotifyLiveProjections_Decrypt covers the encryption decrypt fan-out in
// NotifyLiveProjections (liveWorkersHandling + the decrypt-skip/error branches).
func TestNotifyLiveProjections_Decrypt(t *testing.T) {
	ctx := context.Background()
	store, provider := covEncStore(t)
	engine := NewProjectionEngine(store)
	require.NoError(t, engine.RegisterLive(newCovLiveProj()))

	require.NoError(t, store.Append(ctx, "s1", []interface{}{covEncEvent{Name: "secret"}}))
	raw, err := store.LoadRaw(ctx, "s1", 0)
	require.NoError(t, err)

	// Encryption configured + a registered live worker -> decrypt path + liveWorkersHandling.
	assert.NotPanics(t, func() { engine.NotifyLiveProjections(ctx, raw) })

	// Revoke the key (no handler) so decrypt fails -> the "dropping event that failed to decrypt"
	// branch runs (still attributing to the handling projections).
	require.NoError(t, provider.RevokeKey("k"))
	assert.NotPanics(t, func() { engine.NotifyLiveProjections(ctx, raw) })
}

// TestErasureResult_Failed covers each Failed() branch.
func TestErasureResult_Failed(t *testing.T) {
	assert.False(t, (&ErasureResult{}).Failed())
	assert.True(t, (&ErasureResult{Errors: []error{assert.AnError}}).Failed())
	assert.True(t, (&ErasureResult{Partial: true}).Failed())
	assert.True(t, (&ErasureResult{ResidualReadModels: []string{"rm"}}).Failed())
	assert.True(t, (&ErasureResult{SubjectStores: []SubjectErasureOutcome{{Skipped: true}}}).Failed())
	assert.False(t, (&ErasureResult{SubjectStores: []SubjectErasureOutcome{{Name: "ok", Skipped: false}}}).Failed())
}

// TestMarkerExists covers the metadata hit, the JSON-Data fallback, and the LoadRaw-error path.
func TestMarkerExists(t *testing.T) {
	ctx := context.Background()
	store := New(memory.NewAdapter())
	store.RegisterEvents(ErasureMarker{})
	eraser := NewDataEraser(store, WithErasureMarker("m"))

	// LoadRaw error (the marker stream does not exist yet) -> false.
	assert.False(t, eraser.markerExists(ctx, "x"))

	// JSON-Data fallback: a marker appended directly carries no metadata subject tag.
	require.NoError(t, store.Append(ctx, "m", []interface{}{ErasureMarker{SubjectID: "x"}}))
	assert.True(t, eraser.markerExists(ctx, "x"), "found via the JSON-Data fallback")
	assert.False(t, eraser.markerExists(ctx, "absent"))

	// Metadata tag: appendMarker stamps the subject in metadata.
	require.NoError(t, eraser.appendMarker(ctx, "z", &ErasureResult{}))
	assert.True(t, eraser.markerExists(ctx, "z"), "found via the metadata subject tag")
}

// TestDecryptStoredEvents_Error covers the error branch of the shared batch decrypt primitive.
func TestDecryptStoredEvents_Error(t *testing.T) {
	ctx := context.Background()
	store, provider := covEncStore(t)
	require.NoError(t, store.Append(ctx, "s", []interface{}{covEncEvent{Name: "secret"}}))
	raw, err := store.LoadRaw(ctx, "s", 0)
	require.NoError(t, err)

	require.NoError(t, provider.RevokeKey("k")) // no decryption-error handler -> a hard error
	_, err = store.decryptStoredEvents(ctx, raw)
	require.Error(t, err)
}

// TestLoadEventsFromPosition_DecryptError covers the decrypt-error branch of the projection
// engine's and the rebuilder's loadEventsFromPosition.
func TestLoadEventsFromPosition_DecryptError(t *testing.T) {
	ctx := context.Background()
	store, provider := covEncStore(t)
	require.NoError(t, store.Append(ctx, "s", []interface{}{covEncEvent{Name: "secret"}}))
	require.NoError(t, provider.RevokeKey("k")) // no handler -> decrypt is a hard error

	engine := NewProjectionEngine(store)
	_, err := engine.loadEventsFromPosition(ctx, 0, 10)
	require.Error(t, err, "engine load surfaces the decrypt error")

	rb := NewProjectionRebuilder(store, newTestCheckpointStore())
	_, err = rb.loadEventsFromPosition(ctx, 0, 10)
	require.Error(t, err, "rebuild load surfaces the decrypt error")
}

// TestLoadEventsFromPosition_LoadError covers the underlying-load-error branch (the adapter does
// not support subscriptions) of the engine's and rebuilder's loadEventsFromPosition.
func TestLoadEventsFromPosition_LoadError(t *testing.T) {
	ctx := context.Background()
	store := New(covNonSubAdapter{}) // default JSON serializer, no subscription support

	engine := NewProjectionEngine(store)
	_, err := engine.loadEventsFromPosition(ctx, 0, 10)
	require.ErrorIs(t, err, ErrSubscriptionNotSupported)

	rb := NewProjectionRebuilder(store, newTestCheckpointStore())
	_, err = rb.loadEventsFromPosition(ctx, 0, 10)
	require.ErrorIs(t, err, ErrSubscriptionNotSupported)
}

// TestSubscribeViaAdapter_Decrypt covers the transparent-decryption path of an adapter-backed
// subscription — both the success branch and the hard-error branch (surfaced via Err()).
func TestSubscribeViaAdapter_Decrypt(t *testing.T) {
	t.Run("delivers decrypted events", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		store, _ := covEncStore(t)
		require.NoError(t, store.Append(ctx, "s1", []interface{}{covEncEvent{Name: "secret"}}))

		sub, err := store.SubscribeAll(ctx, 0)
		require.NoError(t, err)
		defer func() { _ = sub.Close() }()

		select {
		case ev := <-sub.Events():
			assert.Contains(t, string(ev.Data), "secret", "subscriber receives decrypted plaintext")
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for the decrypted event")
		}
	})

	t.Run("hard decrypt error surfaces via Err()", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		store, provider := covEncStore(t)
		require.NoError(t, store.Append(ctx, "s1", []interface{}{covEncEvent{Name: "secret"}}))
		require.NoError(t, provider.RevokeKey("k")) // historical event can no longer decrypt

		sub, err := store.SubscribeAll(ctx, 0)
		require.NoError(t, err)
		defer func() { _ = sub.Close() }()

		require.Eventually(t, func() bool { return sub.Err() != nil }, 2*time.Second, 20*time.Millisecond,
			"the subscription stops with a decrypt error")
	})
}
