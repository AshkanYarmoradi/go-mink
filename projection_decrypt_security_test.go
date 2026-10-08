package mink

import (
	"context"
	"crypto/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go-mink.dev/adapters/memory"
	"go-mink.dev/encryption/local"
)

// =============================================================================
// Async projections: an undecryptable event is a per-event (poison) failure
//
// Before this hardening the async worker decrypted the whole batch while loading
// it, so one event whose key had been revoked failed the batch load, the worker
// never had a failed event to hand to OnPoisonEvent, and every async projection
// over that feed faulted after its retry budget. Now each handled event is
// decrypted on its own and a failure is attributed to that event.
// =============================================================================

// positionRecordingProjection records the global position of every event it applied.
type positionRecordingProjection struct {
	AsyncProjectionBase
	mu        sync.Mutex
	positions []uint64
	names     []string
}

func newPositionRecordingProjection(name string, handled ...string) *positionRecordingProjection {
	return &positionRecordingProjection{AsyncProjectionBase: NewAsyncProjectionBase(name, handled...)}
}

func (p *positionRecordingProjection) Apply(_ context.Context, event StoredEvent) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.positions = append(p.positions, event.GlobalPosition)
	p.names = append(p.names, string(event.Data))
	return nil
}

func (p *positionRecordingProjection) applied() []uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]uint64(nil), p.positions...)
}

func (p *positionRecordingProjection) payloads() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.names...)
}

// twoKeyEncStore builds a store whose covEncEvent.name is encrypted under a
// per-tenant key: tenant "shred" uses key "k-shred", everything else "k-keep".
func twoKeyEncStore(t *testing.T) (*EventStore, *local.Provider) {
	t.Helper()
	keep, shred := make([]byte, 32), make([]byte, 32)
	_, err := rand.Read(keep)
	require.NoError(t, err)
	_, err = rand.Read(shred)
	require.NoError(t, err)
	provider, err := local.New(local.WithKey("k-keep", keep), local.WithKey("k-shred", shred))
	require.NoError(t, err)
	t.Cleanup(func() { _ = provider.Close() })

	store := New(memory.NewAdapter(), WithFieldEncryption(NewFieldEncryptionConfig(
		WithEncryptionProvider(provider),
		WithDefaultKeyID("k-keep"),
		WithTenantKeyResolver(func(tenantID string) string {
			if tenantID == "shred" {
				return "k-shred"
			}
			return "k-keep"
		}),
		WithEncryptedFields("covEncEvent", "name"),
	)))
	store.RegisterEvents(covEncEvent{})
	return store, provider
}

// seedShreddedMiddle appends three encrypted events — positions 1 and 3 under the
// kept key, position 2 under the key that is then revoked — and returns the store.
func seedShreddedMiddle(t *testing.T) (*EventStore, *local.Provider) {
	t.Helper()
	ctx := context.Background()
	store, provider := twoKeyEncStore(t)
	require.NoError(t, store.Append(ctx, "s-1", []interface{}{covEncEvent{Name: "first"}}))
	require.NoError(t, store.Append(ctx, "s-2", []interface{}{covEncEvent{Name: "shredded"}}, WithAppendMetadata(Metadata{TenantID: "shred"})))
	require.NoError(t, store.Append(ctx, "s-3", []interface{}{covEncEvent{Name: "third"}}))
	require.NoError(t, provider.RevokeKey("k-shred")) // no decryption handler → hard error for position 2
	return store, provider
}

func TestProjectionEngine_Async_UndecryptableEvent_ReachesOnPoisonEvent(t *testing.T) {
	store, _ := seedShreddedMiddle(t)
	checkpoint := newTestCheckpointStore()
	engine := NewProjectionEngine(store, WithCheckpointStore(checkpoint))

	projection := newPositionRecordingProjection("ShredSkip", "covEncEvent")

	var poisoned atomic.Int32
	var poisonedEvent StoredEvent
	var poisonCause error
	var mu sync.Mutex
	opts := fastAsyncOpts()
	opts.BatchSize = 10 // all three events in one batch
	opts.MaxRetries = 2
	opts.RetryPolicy = ExponentialBackoffRetry(2, 2*time.Millisecond, 5*time.Millisecond)
	opts.ErrorClassifier = DefaultErrorClassifier
	opts.OnPoisonEvent = func(_ context.Context, event StoredEvent, cause error) error {
		mu.Lock()
		defer mu.Unlock()
		poisonedEvent, poisonCause = event, cause
		poisoned.Add(1)
		return nil // skip it
	}
	require.NoError(t, engine.RegisterAsync(projection, opts))
	startEngine(t, engine)

	require.Eventually(t, func() bool {
		return poisoned.Load() >= 1 && len(projection.applied()) >= 2
	}, 3*time.Second, 5*time.Millisecond, "the undecryptable event must reach OnPoisonEvent and the rest must still be applied")

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, int32(1), poisoned.Load(), "exactly one poison event")
	assert.Equal(t, uint64(2), poisonedEvent.GlobalPosition, "the undecryptable event, not the batch, is reported")
	assert.Equal(t, "covEncEvent", poisonedEvent.Type)
	assert.True(t, IsEncrypted(poisonedEvent.Metadata), "the poison event is handed over as stored")
	require.Error(t, poisonCause)
	assert.ErrorIs(t, poisonCause, ErrKeyRevoked, "a revoked key surfaces as ErrKeyRevoked so the handler can tell a shred from a transient provider failure")
	assert.NotContains(t, poisonCause.Error(), "shredded", "the cause never carries plaintext")

	// Positions 1 and 3 were applied, decrypted, exactly once each; 2 was skipped.
	assert.Equal(t, []uint64{1, 3}, projection.applied())
	assert.Contains(t, projection.payloads()[0], "first")
	assert.Contains(t, projection.payloads()[1], "third")

	// The worker is healthy and checkpointed past the skipped event.
	status, err := engine.GetStatus("ShredSkip")
	require.NoError(t, err)
	assert.NotEqual(t, ProjectionStateFaulted, status.State)
	require.Eventually(t, func() bool {
		pos, err := checkpoint.GetCheckpoint(context.Background(), "ShredSkip")
		return err == nil && pos == 3
	}, 2*time.Second, 5*time.Millisecond, "checkpoint must advance past the skipped event to the end of the feed")
}

func TestProjectionEngine_Async_UndecryptableEvent_FaultsWhenNoHandler(t *testing.T) {
	store, _ := seedShreddedMiddle(t)
	engine := NewProjectionEngine(store, WithCheckpointStore(newTestCheckpointStore()))
	projection := newPositionRecordingProjection("ShredFault", "covEncEvent")

	opts := fastAsyncOpts()
	opts.BatchSize = 10
	opts.MaxRetries = 2
	opts.RetryPolicy = ExponentialBackoffRetry(2, 2*time.Millisecond, 5*time.Millisecond)
	opts.OnPoisonEvent = nil
	require.NoError(t, engine.RegisterAsync(projection, opts))
	startEngine(t, engine)

	waitForState(t, engine, "ShredFault", ProjectionStateFaulted)

	status, err := engine.GetStatus("ShredFault")
	require.NoError(t, err)
	assert.Contains(t, status.Error, "failed to decrypt event")
	assert.Contains(t, status.Error, "position 2")
	assert.NotContains(t, status.Error, "shredded")
	// The decryptable event before it was applied; the one after it was never reached.
	assert.Equal(t, []uint64{1}, projection.applied())
}

func TestProjectionEngine_Async_UndecryptableEvent_UnhandledTypeIsSkippedWithoutDecrypting(t *testing.T) {
	// A projection that does not handle covEncEvent must not be affected by an
	// undecryptable one at all (it is never decrypted), and must checkpoint past it.
	store, _ := seedShreddedMiddle(t)
	require.NoError(t, store.Append(context.Background(), "p-1", []interface{}{&ProjectionTestEvent{OrderID: "x"}}))
	checkpoint := newTestCheckpointStore()
	engine := NewProjectionEngine(store, WithCheckpointStore(checkpoint))
	projection := newPositionRecordingProjection("Other", "ProjectionTestEvent")

	opts := fastAsyncOpts()
	opts.BatchSize = 10
	opts.OnPoisonEvent = func(context.Context, StoredEvent, error) error {
		t.Error("OnPoisonEvent must not be called for an event the projection does not handle")
		return nil
	}
	require.NoError(t, engine.RegisterAsync(projection, opts))
	startEngine(t, engine)

	require.Eventually(t, func() bool {
		return len(projection.applied()) == 1
	}, 2*time.Second, 5*time.Millisecond)
	assert.Equal(t, []uint64{4}, projection.applied())
	status, err := engine.GetStatus("Other")
	require.NoError(t, err)
	assert.NotEqual(t, ProjectionStateFaulted, status.State)
	assert.Empty(t, status.Error)
}

func TestProjectionEngine_processAsyncBatch_CutsBatchBeforeUndecryptableEvent(t *testing.T) {
	// Direct, deterministic walk through the two branches: the first cycle processes
	// the events before the undecryptable one and checkpoints there; the second
	// cycle starts on it and fails with the failed event recorded.
	ctx := context.Background()
	store, _ := seedShreddedMiddle(t)
	checkpoint := newTestCheckpointStore()
	engine := NewProjectionEngine(store, WithCheckpointStore(checkpoint))
	projection := newPositionRecordingProjection("Direct", "covEncEvent")

	opts := fastAsyncOpts()
	opts.BatchSize = 10
	require.NoError(t, engine.RegisterAsync(projection, opts))
	worker := engine.asyncProjections["Direct"]

	// Cycle 1: position 1 applied, batch cut before position 2, checkpoint = 1.
	require.NoError(t, engine.processAsyncBatch(ctx, worker))
	assert.Equal(t, []uint64{1}, projection.applied())
	assert.Nil(t, worker.getFailedEvent())
	pos, err := checkpoint.GetCheckpoint(ctx, "Direct")
	require.NoError(t, err)
	assert.Equal(t, uint64(1), pos)

	// Cycle 2: position 2 leads the batch and fails as a per-event error.
	err = engine.processAsyncBatch(ctx, worker)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrKeyRevoked)
	failed := worker.getFailedEvent()
	require.NotNil(t, failed)
	assert.Equal(t, uint64(2), failed.GlobalPosition)
	assert.Equal(t, []uint64{1}, projection.applied(), "nothing past the failing event is applied")
	pos, err = checkpoint.GetCheckpoint(ctx, "Direct")
	require.NoError(t, err)
	assert.Equal(t, uint64(1), pos, "a failing cycle never advances the checkpoint")
}

func TestProjectionEngine_processAsyncBatch_NoEncryption_UnchangedPath(t *testing.T) {
	// With encryption unconfigured, events are handed to the projection as loaded.
	ctx := context.Background()
	engine, store, checkpoint := newTestEngineWithStore()
	appendTestEvents(t, store, 3)
	projection := newPositionRecordingProjection("Plain", "ProjectionTestEvent")
	require.NoError(t, engine.RegisterAsync(projection, fastAsyncOpts()))
	worker := engine.asyncProjections["Plain"]

	require.NoError(t, engine.processAsyncBatch(ctx, worker))
	assert.Equal(t, []uint64{1, 2, 3}, projection.applied())
	pos, err := checkpoint.GetCheckpoint(ctx, "Plain")
	require.NoError(t, err)
	assert.Equal(t, uint64(3), pos)
}
