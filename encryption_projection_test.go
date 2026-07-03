package mink

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go-mink.dev/adapters/memory"
	"go-mink.dev/encryption"
	"go-mink.dev/encryption/local"
)

// projEncEvent has a scalar encrypted field (Name) and a non-scalar one (Emails), which
// is stored as a single base64 string when encrypted — the case that breaks a projection's
// json.Unmarshal into the typed struct unless the event is decrypted first.
type projEncEvent struct {
	UserID string   `json:"userId"`
	Name   string   `json:"name"`
	Emails []string `json:"emails"`
}

// captureProjection deserializes each event into projEncEvent and records it — so a test
// can assert the projection saw plaintext (a failed Unmarshal or a ciphertext value fails).
type captureProjection struct {
	name string
	mu   sync.Mutex
	got  []projEncEvent
}

func (p *captureProjection) Name() string            { return p.name }
func (p *captureProjection) HandledEvents() []string { return []string{"projEncEvent"} }
func (p *captureProjection) Apply(_ context.Context, e StoredEvent) error {
	var v projEncEvent
	if err := json.Unmarshal(e.Data, &v); err != nil {
		return err
	}
	p.mu.Lock()
	p.got = append(p.got, v)
	p.mu.Unlock()
	return nil
}
func (p *captureProjection) ApplyBatch(context.Context, []StoredEvent) error {
	return ErrNotImplemented
}
func (p *captureProjection) snapshot() []projEncEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]projEncEvent(nil), p.got...)
}

// countProjection just counts applied events (tolerant of ciphertext) — for the
// crypto-shred case where the event is delivered without decryption.
type countProjection struct {
	name string
	n    int32Counter
}

func (p *countProjection) Name() string                                    { return p.name }
func (p *countProjection) HandledEvents() []string                         { return []string{"projEncEvent"} }
func (p *countProjection) Apply(context.Context, StoredEvent) error        { p.n.inc(); return nil }
func (p *countProjection) ApplyBatch(context.Context, []StoredEvent) error { return ErrNotImplemented }

type int32Counter struct {
	mu sync.Mutex
	v  int
}

func (c *int32Counter) inc()     { c.mu.Lock(); c.v++; c.mu.Unlock() }
func (c *int32Counter) get() int { c.mu.Lock(); defer c.mu.Unlock(); return c.v }

func newProjEncStore(t *testing.T, handler func(error, string, Metadata) error) (*EventStore, *local.Provider) {
	t.Helper()
	key := make([]byte, 32)
	_, err := rand.Read(key)
	require.NoError(t, err)
	provider, err := local.New(local.WithKey("k", key))
	require.NoError(t, err)
	t.Cleanup(func() { _ = provider.Close() })

	opts := []EncryptionOption{
		WithEncryptionProvider(provider),
		WithDefaultKeyID("k"),
		WithEncryptedFields("projEncEvent", "name", "emails"),
	}
	if handler != nil {
		opts = append(opts, WithDecryptionErrorHandler(handler))
	}
	store := New(memory.NewAdapter(), WithFieldEncryption(NewFieldEncryptionConfig(opts...)))
	store.RegisterEvents(projEncEvent{})
	return store, provider
}

func TestEventStore_DecryptStoredEvent(t *testing.T) {
	ctx := context.Background()

	t.Run("passthrough when unconfigured", func(t *testing.T) {
		store := New(memory.NewAdapter())
		store.RegisterEvents(projEncEvent{})
		require.NoError(t, store.Append(ctx, "U-1", []interface{}{projEncEvent{UserID: "u1", Name: "Alice"}}))
		raw, err := store.LoadRaw(ctx, "U-1", 0)
		require.NoError(t, err)
		require.Len(t, raw, 1)

		out, err := store.DecryptStoredEvent(ctx, raw[0])
		require.NoError(t, err)
		assert.Equal(t, raw[0].Data, out.Data, "unencrypted event Data must be untouched")
	})

	t.Run("decrypts an encrypted event", func(t *testing.T) {
		store, _ := newProjEncStore(t, nil)
		require.NoError(t, store.Append(ctx, "U-1", []interface{}{projEncEvent{UserID: "u1", Name: "Alice", Emails: []string{"a@x.com", "b@y.com"}}}))
		raw, err := store.LoadRaw(ctx, "U-1", 0)
		require.NoError(t, err)
		assert.NotContains(t, string(raw[0].Data), "Alice", "stored Data is ciphertext")

		out, err := store.DecryptStoredEvent(ctx, raw[0])
		require.NoError(t, err)
		var v projEncEvent
		require.NoError(t, json.Unmarshal(out.Data, &v))
		assert.Equal(t, "Alice", v.Name)
		assert.Equal(t, []string{"a@x.com", "b@y.com"}, v.Emails)
		assert.Equal(t, raw[0].GlobalPosition, out.GlobalPosition, "non-Data fields unchanged")
	})

	t.Run("surfaces a hard decryption error", func(t *testing.T) {
		store, provider := newProjEncStore(t, nil) // no handler
		require.NoError(t, store.Append(ctx, "U-1", []interface{}{projEncEvent{UserID: "u1", Name: "Alice"}}))
		raw, err := store.LoadRaw(ctx, "U-1", 0)
		require.NoError(t, err)
		require.NoError(t, provider.RevokeKey("k"))

		_, err = store.DecryptStoredEvent(ctx, raw[0])
		require.Error(t, err)
	})
}

func TestProjectionEngine_InlineDecryption(t *testing.T) {
	ctx := context.Background()
	store, _ := newProjEncStore(t, nil)
	engine := NewProjectionEngine(store)
	proj := &captureProjection{name: "inline-cap"}
	require.NoError(t, engine.RegisterInline(proj))

	require.NoError(t, store.Append(ctx, "U-1", []interface{}{
		projEncEvent{UserID: "u1", Name: "Alice", Emails: []string{"a@x.com", "b@y.com"}},
	}))
	raw, err := store.LoadRaw(ctx, "U-1", 0) // encrypted StoredEvents, as a caller would pass
	require.NoError(t, err)

	require.NoError(t, engine.ProcessInlineProjections(ctx, raw))

	got := proj.snapshot()
	require.Len(t, got, 1)
	assert.Equal(t, "Alice", got[0].Name, "scalar field projected as plaintext, not base64")
	assert.Equal(t, []string{"a@x.com", "b@y.com"}, got[0].Emails, "non-scalar field deserialized (would fail on ciphertext)")
}

func TestProjectionEngine_AsyncDecryption(t *testing.T) {
	ctx := context.Background()
	store, _ := newProjEncStore(t, nil)
	require.NoError(t, store.Append(ctx, "U-1", []interface{}{
		projEncEvent{UserID: "u1", Name: "Alice", Emails: []string{"a@x.com"}},
	}))

	checkpoint := newTestCheckpointStore()
	engine := NewProjectionEngine(store, WithCheckpointStore(checkpoint))
	proj := &captureProjection{name: "async-cap"}
	opts := DefaultAsyncOptions()
	opts.PollInterval = 10 * time.Millisecond
	require.NoError(t, engine.RegisterAsync(proj, opts))

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	require.NoError(t, engine.Start(runCtx))
	defer func() { _ = engine.Stop(context.Background()) }()

	require.Eventually(t, func() bool { return len(proj.snapshot()) == 1 }, 2*time.Second, 10*time.Millisecond)
	got := proj.snapshot()
	assert.Equal(t, "Alice", got[0].Name, "async worker projects plaintext")
	assert.Equal(t, []string{"a@x.com"}, got[0].Emails)
}

func TestProjectionEngine_AsyncDecryption_CryptoShreddedDoesNotStall(t *testing.T) {
	ctx := context.Background()
	// Handler returns nil for a revoked key (crypto-shred): the event is delivered with its
	// fields left as stored and NO error, so the worker must advance rather than stall/poison.
	store, provider := newProjEncStore(t, func(err error, _ string, _ Metadata) error {
		if errors.Is(err, encryption.ErrKeyRevoked) {
			return nil
		}
		return err
	})
	require.NoError(t, store.Append(ctx, "U-1", []interface{}{
		projEncEvent{UserID: "u1", Name: "Alice", Emails: []string{"a@x.com"}},
	}))
	require.NoError(t, provider.RevokeKey("k"))

	checkpoint := newTestCheckpointStore()
	engine := NewProjectionEngine(store, WithCheckpointStore(checkpoint))
	proj := &countProjection{name: "shred"}
	opts := DefaultAsyncOptions()
	opts.PollInterval = 10 * time.Millisecond
	require.NoError(t, engine.RegisterAsync(proj, opts))

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	require.NoError(t, engine.Start(runCtx))
	defer func() { _ = engine.Stop(context.Background()) }()

	// The shredded event is delivered and applied (not stalled), and the worker's checkpoint advances.
	require.Eventually(t, func() bool { return proj.n.get() == 1 }, 2*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool {
		pos, err := checkpoint.GetCheckpoint(ctx, "shred")
		return err == nil && pos > 0
	}, 2*time.Second, 10*time.Millisecond)
}

func TestProjectionEngine_Decryption_NoEncryptionNoOp(t *testing.T) {
	ctx := context.Background()
	store := New(memory.NewAdapter())
	store.RegisterEvents(projEncEvent{})
	engine := NewProjectionEngine(store)
	proj := &captureProjection{name: "plain"}
	require.NoError(t, engine.RegisterInline(proj))

	require.NoError(t, store.Append(ctx, "U-1", []interface{}{
		projEncEvent{UserID: "u1", Name: "Alice", Emails: []string{"a@x.com"}},
	}))
	raw, err := store.LoadRaw(ctx, "U-1", 0)
	require.NoError(t, err)
	require.NoError(t, engine.ProcessInlineProjections(ctx, raw))

	got := proj.snapshot()
	require.Len(t, got, 1)
	assert.Equal(t, "Alice", got[0].Name)
}
