package mink

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go-mink.dev/adapters"
	"go-mink.dev/adapters/memory"
	"go-mink.dev/encryption"
	"go-mink.dev/encryption/local"
)

// ====================================================================
// Saga type confusion: correlation lookups scoped by saga type
// ====================================================================

// typeProbeSaga is a minimal saga of a configurable type that never completes, so
// later events keep hydrating the same instance. Two of them registered with
// RegisterSimple (correlation = stream id) share correlation ids by construction.
type typeProbeSaga struct {
	SagaBase
	handled []string
	data    map[string]interface{}
}

func typeProbeFactory(sagaType string, handled ...string) SagaFactory {
	return func(id string) Saga {
		return &typeProbeSaga{SagaBase: NewSagaBase(id, sagaType), handled: handled}
	}
}

func (s *typeProbeSaga) HandledEvents() []string { return s.handled }
func (s *typeProbeSaga) HandleEvent(context.Context, StoredEvent) ([]Command, error) {
	return nil, nil
}
func (s *typeProbeSaga) Compensate(context.Context, int, error) ([]Command, error) {
	return nil, nil
}
func (s *typeProbeSaga) IsComplete() bool                 { return false }
func (s *typeProbeSaga) Data() map[string]interface{}     { return s.data }
func (s *typeProbeSaga) SetData(d map[string]interface{}) { s.data = d }

// unscopedSagaStore hides every optional method of the wrapped store (embedding the
// INTERFACE exposes only SagaStore's methods), so the manager must fall back to the
// unscoped FindByCorrelationID — regardless of what the shipped store grows later.
type unscopedSagaStore struct{ SagaStore }

// typeScopedSagaStore adds the optional FindByCorrelationIDAndType capability on top
// of any SagaStore, implemented over FindByType, and counts how often it is used.
type typeScopedSagaStore struct {
	SagaStore
	calls int32
}

func (s *typeScopedSagaStore) FindByCorrelationIDAndType(ctx context.Context, correlationID, sagaType string) (*adapters.SagaState, error) {
	atomic.AddInt32(&s.calls, 1)
	states, err := s.FindByType(ctx, sagaType)
	if err != nil {
		return nil, err
	}
	var latest *SagaState
	for _, st := range states {
		if st.CorrelationID == correlationID && (latest == nil || st.StartedAt.After(latest.StartedAt)) {
			latest = st
		}
	}
	if latest == nil {
		return nil, &SagaNotFoundError{CorrelationID: correlationID}
	}
	return latest, nil
}

func newTypeProbeManager(t *testing.T, store SagaStore) *SagaManager {
	t.Helper()
	m := NewSagaManager(New(memory.NewAdapter()),
		WithSagaStore(store), WithCommandBus(NewCommandBus()),
		WithSagaRetryAttempts(1), WithSagaRetryDelay(time.Millisecond))
	m.RegisterSimple("SagaA", typeProbeFactory("SagaA", "EvA", "EvA2"), "EvA")
	m.RegisterSimple("SagaB", typeProbeFactory("SagaB", "EvB", "EvB2"), "EvB")
	return m
}

func TestSagaManager_SharedCorrelationID_UnscopedStore_ReturnsTypeMismatch(t *testing.T) {
	ctx := context.Background()
	store := &unscopedSagaStore{SagaStore: memory.NewSagaStore()}
	m := newTypeProbeManager(t, store)

	// Saga A owns correlation id "order-1".
	require.NoError(t, m.ProcessEvent(ctx, retryEvent("e1", "order-1", "EvA", 1)))
	a, err := store.Load(ctx, "SagaA-order-1")
	require.NoError(t, err)
	require.Equal(t, "SagaA", a.Type)
	aVersion := a.Version

	// Starting event for saga B on the same correlation id: refused, typed.
	err = m.StartSaga(ctx, "SagaB", retryEvent("e2", "order-1", "EvB", 2))
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSagaTypeMismatch)
	var typed *SagaTypeMismatchError
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, "order-1", typed.CorrelationID)
	assert.Equal(t, "SagaA-order-1", typed.SagaID)
	assert.Equal(t, "SagaB", typed.ExpectedType)
	assert.Equal(t, "SagaA", typed.ActualType)
	assert.Contains(t, typed.Error(), "mink: saga type mismatch")

	// Non-starting event for saga B on the same correlation id: also refused (never
	// hydrated from A's row), surfaced through the per-type processing path.
	err = m.processSagaEvent(ctx, "SagaB", retryEvent("e3", "order-1", "EvB2", 3), retryEvent("e3", "order-1", "EvB2", 3))
	assert.ErrorIs(t, err, ErrSagaTypeMismatch)

	// A's row is untouched: same type, same version, and no B saga was ever written.
	a, err = store.Load(ctx, "SagaA-order-1")
	require.NoError(t, err)
	assert.Equal(t, "SagaA", a.Type)
	assert.Equal(t, aVersion, a.Version)
	_, err = store.Load(ctx, "SagaB-order-1")
	assert.ErrorIs(t, err, ErrSagaNotFound)
	bs, err := store.FindByType(ctx, "SagaB")
	require.NoError(t, err)
	assert.Empty(t, bs)

	// A keeps working on its own events.
	require.NoError(t, m.ProcessEvent(ctx, retryEvent("e4", "order-1", "EvA2", 4)))
	a, err = store.Load(ctx, "SagaA-order-1")
	require.NoError(t, err)
	assert.Equal(t, "SagaA", a.Type)
	assert.Greater(t, a.Version, aVersion)
}

func TestSagaManager_SharedCorrelationID_UnscopedStore_EventLoopLogsAndContinues(t *testing.T) {
	ctx := context.Background()
	store := &unscopedSagaStore{SagaStore: memory.NewSagaStore()}
	logger := &sagaTestLogger{}
	m := newTypeProbeManager(t, store)
	m.logger = logger

	require.NoError(t, m.ProcessEvent(ctx, retryEvent("e1", "order-1", "EvA", 1)))
	// The event loop swallows per-saga-type errors (it must keep processing), but the
	// mismatch is logged — it is never a silent skip and never a silent overwrite.
	require.NoError(t, m.ProcessEvent(ctx, retryEvent("e2", "order-1", "EvB", 2)))

	logger.mu.Lock()
	defer logger.mu.Unlock()
	assert.Contains(t, logger.messages, "ERROR: Failed to process saga event")
	_, err := store.Load(ctx, "SagaB-order-1")
	assert.ErrorIs(t, err, ErrSagaNotFound)
}

func TestSagaManager_SharedCorrelationID_TypeScopedStore_BothSagasCoexist(t *testing.T) {
	ctx := context.Background()
	store := &typeScopedSagaStore{SagaStore: memory.NewSagaStore()}
	m := newTypeProbeManager(t, store)

	require.NoError(t, m.ProcessEvent(ctx, retryEvent("e1", "order-1", "EvA", 1)))
	require.NoError(t, m.StartSaga(ctx, "SagaB", retryEvent("e2", "order-1", "EvB", 2)))
	assert.Positive(t, atomic.LoadInt32(&store.calls), "the type-scoped lookup must be preferred when the store offers it")

	a, err := store.Load(ctx, "SagaA-order-1")
	require.NoError(t, err)
	b, err := store.Load(ctx, "SagaB-order-1")
	require.NoError(t, err)
	assert.Equal(t, "SagaA", a.Type)
	assert.Equal(t, "SagaB", b.Type)
	assert.Equal(t, "order-1", a.CorrelationID)
	assert.Equal(t, "order-1", b.CorrelationID)

	// Follow-up events route to their own saga type, never the other's row.
	aVersion, bVersion := a.Version, b.Version
	require.NoError(t, m.ProcessEvent(ctx, retryEvent("e3", "order-1", "EvA2", 3)))
	require.NoError(t, m.ProcessEvent(ctx, retryEvent("e4", "order-1", "EvB2", 4)))
	a, err = store.Load(ctx, "SagaA-order-1")
	require.NoError(t, err)
	b, err = store.Load(ctx, "SagaB-order-1")
	require.NoError(t, err)
	assert.Equal(t, "SagaA", a.Type)
	assert.Equal(t, "SagaB", b.Type)
	assert.Equal(t, aVersion+1, a.Version)
	assert.Equal(t, bVersion+1, b.Version)
}

func TestSagaManager_findSagaByCorrelation(t *testing.T) {
	ctx := context.Background()

	t.Run("unscoped store: same type is returned unchanged", func(t *testing.T) {
		store := &unscopedSagaStore{SagaStore: memory.NewSagaStore()}
		m := NewSagaManager(nil, WithSagaStore(store))
		require.NoError(t, store.Save(ctx, &SagaState{ID: "SagaA-c1", Type: "SagaA", CorrelationID: "c1",
			Status: SagaStatusRunning, StartedAt: time.Now(), UpdatedAt: time.Now()}))

		st, err := m.findSagaByCorrelation(ctx, "SagaA", "c1")
		require.NoError(t, err)
		assert.Equal(t, "SagaA-c1", st.ID)
	})

	t.Run("unscoped store: not found propagates", func(t *testing.T) {
		store := &unscopedSagaStore{SagaStore: memory.NewSagaStore()}
		m := NewSagaManager(nil, WithSagaStore(store))
		_, err := m.findSagaByCorrelation(ctx, "SagaA", "missing")
		assert.ErrorIs(t, err, ErrSagaNotFound)
	})

	t.Run("unscoped store: foreign type is a typed mismatch", func(t *testing.T) {
		store := &unscopedSagaStore{SagaStore: memory.NewSagaStore()}
		m := NewSagaManager(nil, WithSagaStore(store))
		require.NoError(t, store.Save(ctx, &SagaState{ID: "SagaA-c1", Type: "SagaA", CorrelationID: "c1",
			Status: SagaStatusRunning, StartedAt: time.Now(), UpdatedAt: time.Now()}))

		st, err := m.findSagaByCorrelation(ctx, "SagaB", "c1")
		assert.Nil(t, st)
		assert.ErrorIs(t, err, ErrSagaTypeMismatch)
		assert.True(t, errors.Is(errors.Unwrap(err), ErrSagaTypeMismatch), "Unwrap must expose the sentinel")
	})

	t.Run("scoped store: delegated, foreign type invisible", func(t *testing.T) {
		store := &typeScopedSagaStore{SagaStore: memory.NewSagaStore()}
		m := NewSagaManager(nil, WithSagaStore(store))
		require.NoError(t, store.Save(ctx, &SagaState{ID: "SagaA-c1", Type: "SagaA", CorrelationID: "c1",
			Status: SagaStatusRunning, StartedAt: time.Now(), UpdatedAt: time.Now()}))

		_, err := m.findSagaByCorrelation(ctx, "SagaB", "c1")
		assert.ErrorIs(t, err, ErrSagaNotFound)
		assert.Equal(t, int32(1), atomic.LoadInt32(&store.calls))
	})
}

// ====================================================================
// Retry capture never persists decrypted PII
// ====================================================================

func TestCaptureCandidate(t *testing.T) {
	ts := time.Now()
	plain := StoredEvent{ID: "e1", StreamID: "User-1", Type: "encRetryEvent", Data: []byte(`{"name":"Alice"}`),
		Metadata: Metadata{UserID: "u1", Custom: map[string]string{"k": "v"}}, Version: 3, GlobalPosition: 9, Timestamp: ts}

	t.Run("plaintext event is captured whole", func(t *testing.T) {
		got := captureCandidate(plain)
		assert.Equal(t, plain, got)
		assert.False(t, isTriggerLocator(got))
	})

	t.Run("field-encrypted event is reduced to a locator", func(t *testing.T) {
		enc := plain
		enc.Data = []byte(`{"name":"Y2lwaGVydGV4dA=="}`)
		enc.Metadata = Metadata{UserID: "u1", Custom: map[string]string{
			encryptedFieldsKey: "name", encryptedDEKKey: "d2VrLWtleQ==", encryptionKeyIDKey: "k"}}

		got := captureCandidate(enc)
		assert.True(t, isTriggerLocator(got))
		assert.Nil(t, got.Data, "no payload — not even ciphertext — enters saga state")
		assert.Equal(t, Metadata{Custom: map[string]string{sagaTriggerLocatorKey: "true"}}, got.Metadata,
			"no metadata (wrapped DEK, key id, subject tags) enters saga state — only the explicit locator marker")
		assert.Equal(t, "e1", got.ID)
		assert.Equal(t, "User-1", got.StreamID)
		assert.Equal(t, "encRetryEvent", got.Type)
		assert.Equal(t, int64(3), got.Version)
		assert.Equal(t, uint64(9), got.GlobalPosition)
		assert.Equal(t, ts, got.Timestamp)
	})
}

// encRetryEvent carries field-encrypted PII (name, emails).
type encRetryEvent struct {
	UserID string   `json:"userId"`
	Name   string   `json:"name"`
	Emails []string `json:"emails"`
}

// encRetryControl steers encRetrySaga from the test: while fail is set the saga's
// HandleEvent fails (the saga settles Compensated, which is retryable); every payload
// the saga is handed is recorded so the test can assert what it actually observed.
type encRetryControl struct {
	mu   sync.Mutex
	fail bool
	seen []encRetryEvent
}

func (c *encRetryControl) setFail(v bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fail = v
}

func (c *encRetryControl) record(ev encRetryEvent) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seen = append(c.seen, ev)
	return c.fail
}

func (c *encRetryControl) snapshot() []encRetryEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]encRetryEvent(nil), c.seen...)
}

type encRetrySaga struct {
	SagaBase
	ctl      *encRetryControl
	data     map[string]interface{}
	complete bool
}

func (s *encRetrySaga) HandledEvents() []string { return []string{"encRetryEvent"} }
func (s *encRetrySaga) HandleEvent(_ context.Context, event StoredEvent) ([]Command, error) {
	var v encRetryEvent
	if err := json.Unmarshal(event.Data, &v); err != nil {
		return nil, err
	}
	if s.ctl.record(v) {
		return nil, errors.New("simulated downstream failure")
	}
	s.complete = true
	return nil, nil
}
func (s *encRetrySaga) Compensate(context.Context, int, error) ([]Command, error) {
	return nil, nil // no compensation commands → Compensated (retryable)
}
func (s *encRetrySaga) IsComplete() bool                 { return s.complete }
func (s *encRetrySaga) Data() map[string]interface{}     { return s.data }
func (s *encRetrySaga) SetData(d map[string]interface{}) { s.data = d }

type encRetryHarness struct {
	store     *EventStore
	manager   *SagaManager
	provider  *local.Provider
	ctl       *encRetryControl
	sagaStore SagaStore
}

func newEncRetryHarness(t *testing.T) *encRetryHarness {
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
		WithEncryptedFields("encRetryEvent", "name", "emails"),
	)))
	store.RegisterEvents(encRetryEvent{})

	h := &encRetryHarness{store: store, provider: provider, ctl: &encRetryControl{}, sagaStore: memory.NewSagaStore()}
	h.manager = NewSagaManager(store,
		WithSagaStore(h.sagaStore), WithCommandBus(NewCommandBus()),
		WithSagaRetryCapture(), WithSagaRetryAttempts(1), WithSagaRetryDelay(time.Millisecond))
	h.manager.RegisterSimple("EncRetrySaga", func(id string) Saga {
		return &encRetrySaga{SagaBase: NewSagaBase(id, "EncRetrySaga"), ctl: h.ctl}
	}, "encRetryEvent")
	return h
}

// appendEncrypted appends one encrypted event and returns it exactly as stored.
func (h *encRetryHarness) appendEncrypted(t *testing.T, streamID string, ev encRetryEvent) StoredEvent {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, h.store.Append(ctx, streamID, []interface{}{ev}))
	raw, err := h.store.LoadRaw(ctx, streamID, 0)
	require.NoError(t, err)
	require.Len(t, raw, 1)
	require.True(t, IsEncrypted(raw[0].Metadata), "precondition: the event is field-encrypted at rest")
	require.NotContains(t, string(raw[0].Data), ev.Name, "precondition: the name is ciphertext at rest")
	return raw[0]
}

// capturedLocator loads the saga and asserts its capture is a locator only.
func capturedLocator(t *testing.T, store SagaStore, sagaID string, raw StoredEvent) *SagaState {
	t.Helper()
	st, err := store.Load(context.Background(), sagaID)
	require.NoError(t, err)
	captured, ok := decodeLastEvent(st.Data[reservedLastEventKey])
	require.True(t, ok, "a trigger event must have been captured")
	assert.True(t, isTriggerLocator(captured))
	assert.Nil(t, captured.Data)
	assert.Equal(t, Metadata{Custom: map[string]string{sagaTriggerLocatorKey: "true"}}, captured.Metadata)
	assert.Equal(t, raw.ID, captured.ID)
	assert.Equal(t, raw.StreamID, captured.StreamID)
	assert.Equal(t, raw.Type, captured.Type)
	assert.Equal(t, raw.Version, captured.Version)
	assert.Equal(t, raw.GlobalPosition, captured.GlobalPosition)
	return st
}

func TestRetrySaga_EncryptedTrigger_CapturesLocatorOnlyAndReloadsThroughDecrypt(t *testing.T) {
	ctx := context.Background()
	h := newEncRetryHarness(t)
	sagaID := "EncRetrySaga-User-1"
	raw := h.appendEncrypted(t, "User-1", encRetryEvent{UserID: "u1", Name: "Alice", Emails: []string{"a@example.com"}})

	// First delivery fails downstream → the saga settles Compensated (retryable).
	h.ctl.setFail(true)
	require.NoError(t, h.manager.ProcessEvent(ctx, raw))
	st := capturedLocator(t, h.sagaStore, sagaID, raw)
	require.Equal(t, SagaStatusCompensated, st.Status)

	// Live delivery decrypted the event for the saga …
	seen := h.ctl.snapshot()
	require.Len(t, seen, 1)
	assert.Equal(t, "Alice", seen[0].Name)
	// … but nothing recoverable about it reached the saga store.
	persisted, err := SagaStateToJSON(st)
	require.NoError(t, err)
	assert.NotContains(t, string(persisted), "Alice")
	assert.NotContains(t, string(persisted), "a@example.com")
	assert.NotContains(t, string(persisted), encryptedDEKKey)

	// Fix the downstream and re-drive: the event is reloaded from the event store and
	// decrypted through the normal path, so the saga sees plaintext and completes.
	h.ctl.setFail(false)
	require.NoError(t, h.manager.RetrySaga(ctx, sagaID))
	seen = h.ctl.snapshot()
	require.Len(t, seen, 2)
	assert.Equal(t, "Alice", seen[1].Name)
	assert.Equal(t, []string{"a@example.com"}, seen[1].Emails)

	// The re-drive did not upgrade the capture into a persisted plaintext payload.
	st = capturedLocator(t, h.sagaStore, sagaID, raw)
	assert.Equal(t, SagaStatusCompleted, st.Status)
}

func TestRetrySaga_EncryptedTrigger_FailsWhenKeyUnavailable(t *testing.T) {
	ctx := context.Background()
	h := newEncRetryHarness(t)
	sagaID := "EncRetrySaga-User-2"
	raw := h.appendEncrypted(t, "User-2", encRetryEvent{UserID: "u2", Name: "Bob", Emails: []string{"b@example.com"}})

	h.ctl.setFail(true)
	require.NoError(t, h.manager.ProcessEvent(ctx, raw))
	capturedLocator(t, h.sagaStore, sagaID, raw)

	// The subject's key is gone (crypto-shredded / provider unavailable): the re-drive
	// must fail with the decryption error — a retry can never resurrect erased data.
	require.NoError(t, h.provider.Close())
	h.ctl.setFail(false)
	err := h.manager.RetrySaga(ctx, sagaID)
	require.Error(t, err)
	assert.ErrorIs(t, err, encryption.ErrProviderClosed)
	assert.Contains(t, err.Error(), "reload captured trigger event")

	// The saga was not re-driven and is unchanged.
	st, err := h.sagaStore.Load(ctx, sagaID)
	require.NoError(t, err)
	assert.Equal(t, SagaStatusCompensated, st.Status)
	assert.Len(t, h.ctl.snapshot(), 1)
}

func TestRetrySaga_LocatorCapture_RequiresEventStore(t *testing.T) {
	ctx := context.Background()
	store := memory.NewSagaStore()
	m := NewSagaManager(nil, WithSagaStore(store), WithCommandBus(NewCommandBus()), WithSagaRetryCapture())
	m.RegisterSimple("RetrySaga", retrySagaFactory(nil), "StepB")

	require.NoError(t, store.Save(ctx, &SagaState{
		ID: "RetrySaga-x", Type: "RetrySaga", CorrelationID: "x", Status: SagaStatusFailed,
		Data:      map[string]interface{}{reservedLastEventKey: triggerLocator("e1", "x", "StepB", 1, 1)},
		StartedAt: time.Now(), UpdatedAt: time.Now(),
	}))

	err := m.RetrySaga(ctx, "RetrySaga-x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no event store")
}

// triggerLocator builds a locator-only capture the way captureCandidate does.
func triggerLocator(id, streamID, typ string, version int64, pos uint64) StoredEvent {
	return captureCandidate(StoredEvent{
		ID: id, StreamID: streamID, Type: typ, Version: version, GlobalPosition: pos,
		Data:     []byte(`{"name":"ciphertext"}`),
		Metadata: Metadata{Custom: map[string]string{encryptedFieldsKey: "name"}},
	})
}

func TestSagaManager_reloadTriggerEvent(t *testing.T) {
	ctx := context.Background()
	adapter := memory.NewAdapter()
	store := New(adapter)
	store.RegisterEvents(encRetryEvent{})
	require.NoError(t, store.Append(ctx, "User-9", []interface{}{
		encRetryEvent{UserID: "u9", Name: "first"},
		encRetryEvent{UserID: "u9", Name: "second"},
	}))
	raw, err := store.LoadRaw(ctx, "User-9", 0)
	require.NoError(t, err)
	require.Len(t, raw, 2)
	m := NewSagaManager(store)

	t.Run("matches by version", func(t *testing.T) {
		got, err := m.reloadTriggerEvent(ctx, StoredEvent{StreamID: "User-9", Version: 2})
		require.NoError(t, err)
		assert.Equal(t, raw[1].ID, got.ID)
		assert.Contains(t, string(got.Data), "second")
	})

	t.Run("matches by id when the locator carries no version", func(t *testing.T) {
		got, err := m.reloadTriggerEvent(ctx, StoredEvent{StreamID: "User-9", ID: raw[0].ID})
		require.NoError(t, err)
		assert.Equal(t, int64(1), got.Version)
		assert.Contains(t, string(got.Data), "first")
	})

	t.Run("missing version is an error", func(t *testing.T) {
		_, err := m.reloadTriggerEvent(ctx, StoredEvent{StreamID: "User-9", Version: 7})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
	})

	t.Run("empty stream id is an error", func(t *testing.T) {
		_, err := m.reloadTriggerEvent(ctx, StoredEvent{ID: "e1", Version: 1})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no stream id")
	})

	t.Run("store read failure propagates", func(t *testing.T) {
		require.NoError(t, adapter.Close())
		_, err := m.reloadTriggerEvent(ctx, StoredEvent{StreamID: "User-9", Version: 1})
		require.Error(t, err)
		assert.ErrorIs(t, err, adapters.ErrAdapterClosed)
		assert.True(t, strings.HasPrefix(err.Error(), "mink: reload captured trigger event"))
	})
}
