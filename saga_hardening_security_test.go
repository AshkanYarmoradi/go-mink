package mink

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go-mink.dev/adapters"
	"go-mink.dev/adapters/memory"
)

// ====================================================================
// Locator marker is explicit: a payload-less plaintext trigger is NOT a locator
// ====================================================================

func TestIsTriggerLocator_OnlyTheMarkerDecides(t *testing.T) {
	tests := []struct {
		name string
		ev   StoredEvent
		want bool
	}{
		{"plaintext with payload", StoredEvent{ID: "e", Type: "T", Data: []byte(`{}`)}, false},
		{"plaintext with no payload", StoredEvent{ID: "e", Type: "T"}, false},
		{"plaintext with other custom metadata", StoredEvent{ID: "e", Type: "T", Metadata: Metadata{Custom: map[string]string{"k": "v"}}}, false},
		{"marker present", triggerLocator("e", "s", "T", 1, 1), true},
		{"marker with a wrong value", StoredEvent{ID: "e", Metadata: Metadata{Custom: map[string]string{sagaTriggerLocatorKey: "yes"}}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isTriggerLocator(tt.ev))
		})
	}
}

func TestTriggerLocator_SurvivesJSONRoundTrip(t *testing.T) {
	// A JSON-backed saga store (PostgreSQL) round-trips the capture as a map;
	// the marker must survive that.
	loc := triggerLocator("e1", "User-1", "encRetryEvent", 3, 9)
	b, err := SagaStateToJSON(&SagaState{ID: "s", Type: "T", Data: map[string]interface{}{reservedLastEventKey: loc}})
	require.NoError(t, err)
	st, err := SagaStateFromJSON(b)
	require.NoError(t, err)

	decoded, ok := decodeLastEvent(st.Data[reservedLastEventKey])
	require.True(t, ok)
	assert.True(t, isTriggerLocator(decoded))
	assert.Equal(t, int64(3), decoded.Version)
	assert.Equal(t, uint64(9), decoded.GlobalPosition)
}

func TestRetrySaga_PayloadLessPlaintextTrigger_IsCapturedWholeAndReDriven(t *testing.T) {
	ctx := context.Background()
	h := newRetryHarness(t, memory.NewSagaStore(), "StepB", nil)
	sagaID := "RetrySaga-order-1"

	// A synthetic trigger with NO payload (e.g. handed to StartSaga by application
	// code) was previously misclassified as a locator and became un-retryable.
	trigger := StoredEvent{ID: "e1", StreamID: "order-1", Type: "StepB", GlobalPosition: 1}
	h.cmdBFails.Store(true)
	require.NoError(t, h.manager.StartSaga(ctx, "RetrySaga", trigger))

	st, err := h.store.Load(ctx, sagaID)
	require.NoError(t, err)
	require.Equal(t, SagaStatusCompensated, st.Status, "precondition: the saga settled retryable")
	captured, ok := decodeLastEvent(st.Data[reservedLastEventKey])
	require.True(t, ok)
	assert.False(t, isTriggerLocator(captured), "a plaintext trigger is captured whole, payload or not")
	assert.Equal(t, "e1", captured.ID)

	h.cmdBFails.Store(false)
	require.NoError(t, h.manager.RetrySaga(ctx, sagaID), "a whole capture needs no event-store read")

	done, err := h.store.Load(ctx, sagaID)
	require.NoError(t, err)
	assert.Equal(t, SagaStatusCompleted, done.Status)
	assert.Equal(t, int32(2), atomic.LoadInt32(&h.cmdBCalls))
}

// ====================================================================
// reloadTriggerEvent reads one event, and verifies it is the right one
// ====================================================================

// countingAdapter wraps the memory adapter and counts the stream reads the saga
// manager issues, recording the limit of the last paged read.
type countingAdapter struct {
	*memory.MemoryAdapter
	loads      int32
	pagedReads int32
	lastLimit  int32
}

func (a *countingAdapter) Load(ctx context.Context, streamID string, fromVersion int64) ([]adapters.StoredEvent, error) {
	atomic.AddInt32(&a.loads, 1)
	return a.MemoryAdapter.Load(ctx, streamID, fromVersion)
}

func (a *countingAdapter) GetStreamEvents(ctx context.Context, streamID string, fromVersion int64, limit int) ([]adapters.StoredEvent, error) {
	atomic.AddInt32(&a.pagedReads, 1)
	atomic.StoreInt32(&a.lastLimit, int32(limit))
	return a.MemoryAdapter.GetStreamEvents(ctx, streamID, fromVersion, limit)
}

// coreOnlyAdapter exposes ONLY the required EventStoreAdapter methods of the
// wrapped adapter (no StreamQueryAdapter, no SubscriptionAdapter), so the manager
// must fall back to the LoadRaw scan.
type coreOnlyAdapter struct {
	inner *countingAdapter
}

func (a *coreOnlyAdapter) Append(ctx context.Context, streamID string, events []adapters.EventRecord, expectedVersion int64) ([]adapters.StoredEvent, error) {
	return a.inner.Append(ctx, streamID, events, expectedVersion)
}

func (a *coreOnlyAdapter) Load(ctx context.Context, streamID string, fromVersion int64) ([]adapters.StoredEvent, error) {
	return a.inner.Load(ctx, streamID, fromVersion)
}

func (a *coreOnlyAdapter) GetStreamInfo(ctx context.Context, streamID string) (*adapters.StreamInfo, error) {
	return a.inner.GetStreamInfo(ctx, streamID)
}

func (a *coreOnlyAdapter) GetLastPosition(ctx context.Context) (uint64, error) {
	return a.inner.GetLastPosition(ctx)
}

func (a *coreOnlyAdapter) Initialize(ctx context.Context) error { return a.inner.Initialize(ctx) }
func (a *coreOnlyAdapter) Close() error                         { return a.inner.Close() }

var _ adapters.EventStoreAdapter = (*coreOnlyAdapter)(nil)

// seedLongStream appends n encRetryEvents to streamID and returns them as stored.
func seedLongStream(t *testing.T, store *EventStore, streamID string, n int) []StoredEvent {
	t.Helper()
	ctx := context.Background()
	store.RegisterEvents(encRetryEvent{})
	events := make([]interface{}, n)
	for i := range events {
		events[i] = encRetryEvent{UserID: "u", Name: fmt.Sprintf("name-%d", i+1)}
	}
	require.NoError(t, store.Append(ctx, streamID, events))
	raw, err := store.LoadRaw(ctx, streamID, 0)
	require.NoError(t, err)
	require.Len(t, raw, n)
	return raw
}

func TestSagaManager_reloadTriggerEvent_ReadsOneEventWhenTheAdapterCanPage(t *testing.T) {
	ctx := context.Background()
	adapter := &countingAdapter{MemoryAdapter: memory.NewAdapter()}
	store := New(adapter)
	raw := seedLongStream(t, store, "User-long", 50)
	m := NewSagaManager(store)
	atomic.StoreInt32(&adapter.loads, 0) // seeding read the stream once

	loc := raw[1] // version 2 of 50
	got, err := m.reloadTriggerEvent(ctx, triggerLocator(loc.ID, loc.StreamID, loc.Type, loc.Version, loc.GlobalPosition))
	require.NoError(t, err)
	assert.Equal(t, loc.ID, got.ID)
	assert.Equal(t, int64(2), got.Version)
	assert.Contains(t, string(got.Data), "name-2")

	assert.Equal(t, int32(1), atomic.LoadInt32(&adapter.pagedReads), "exactly one paged read")
	assert.Equal(t, int32(1), atomic.LoadInt32(&adapter.lastLimit), "with a limit of one event")
	assert.Equal(t, int32(0), atomic.LoadInt32(&adapter.loads), "the stream tail is never scanned")
}

func TestSagaManager_reloadTriggerEvent_FallsBackToScanWithoutPaging(t *testing.T) {
	ctx := context.Background()
	inner := &countingAdapter{MemoryAdapter: memory.NewAdapter()}
	store := New(&coreOnlyAdapter{inner: inner})
	raw := seedLongStream(t, store, "User-core", 10)
	m := NewSagaManager(store)
	atomic.StoreInt32(&inner.loads, 0) // seeding read the stream once

	loc := raw[2] // version 3
	got, err := m.reloadTriggerEvent(ctx, triggerLocator(loc.ID, loc.StreamID, loc.Type, loc.Version, loc.GlobalPosition))
	require.NoError(t, err)
	assert.Equal(t, int64(3), got.Version)
	assert.Equal(t, int32(0), atomic.LoadInt32(&inner.pagedReads))
	assert.Equal(t, int32(1), atomic.LoadInt32(&inner.loads), "one LoadRaw from the locator's version")
}

func TestSagaManager_reloadTriggerEvent_RefusesAMismatchedEvent(t *testing.T) {
	ctx := context.Background()
	store := New(memory.NewAdapter())
	raw := seedLongStream(t, store, "User-m", 3)
	m := NewSagaManager(store)
	loc := raw[1]

	tests := []struct {
		name string
		loc  StoredEvent
		want string
	}{
		{"global position", triggerLocator(loc.ID, loc.StreamID, loc.Type, loc.Version, loc.GlobalPosition+40), "global position"},
		{"type", triggerLocator(loc.ID, loc.StreamID, "OtherType", loc.Version, loc.GlobalPosition), "type"},
		{"id", triggerLocator("not-the-id", loc.StreamID, loc.Type, loc.Version, loc.GlobalPosition), "id"},
		{"version past the stream", triggerLocator(loc.ID, loc.StreamID, loc.Type, 99, loc.GlobalPosition), "not found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := m.reloadTriggerEvent(ctx, tt.loc)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}

	t.Run("a locator with only a stream id and version still matches", func(t *testing.T) {
		got, err := m.reloadTriggerEvent(ctx, triggerLocator("", loc.StreamID, "", loc.Version, 0))
		require.NoError(t, err)
		assert.Equal(t, loc.ID, got.ID)
	})

	t.Run("the scan path verifies too", func(t *testing.T) {
		inner := &countingAdapter{MemoryAdapter: memory.NewAdapter()}
		coreStore := New(&coreOnlyAdapter{inner: inner})
		coreRaw := seedLongStream(t, coreStore, "User-core-m", 3)
		cm := NewSagaManager(coreStore)
		_, err := cm.reloadTriggerEvent(ctx, triggerLocator(coreRaw[0].ID, coreRaw[0].StreamID, "OtherType", 1, coreRaw[0].GlobalPosition))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "type")
	})
}

// ====================================================================
// SagaCorrelationTypeFinder alias and (nil, nil) store guard
// ====================================================================

// The shipped memory store implements the exported capability through the alias.
var _ SagaCorrelationTypeFinder = (*memory.SagaStore)(nil)

func TestSagaCorrelationTypeFinder_AliasIsTheAdapterInterface(t *testing.T) {
	// Compile-time proof that the two names are one interface: a value of the adapter
	// interface type is assignable to the mink alias without conversion.
	var viaAdapter adapters.SagaCorrelationTypeFinder = &typeScopedSagaStore{SagaStore: memory.NewSagaStore()}
	assertAlias := func(SagaCorrelationTypeFinder) {}
	assertAlias(viaAdapter)

	_, ok := interface{}(memory.NewSagaStore()).(SagaCorrelationTypeFinder)
	assert.True(t, ok)
	_, ok = interface{}(&unscopedSagaStore{SagaStore: memory.NewSagaStore()}).(SagaCorrelationTypeFinder)
	assert.False(t, ok)
}

// nilNilSagaStore violates the SagaStore contract by reporting "not found" as
// (nil, nil) from the unscoped lookup.
type nilNilSagaStore struct{ SagaStore }

func (s *nilNilSagaStore) FindByCorrelationID(context.Context, string) (*SagaState, error) {
	return nil, nil
}

// nilNilScopedSagaStore does the same through the type-scoped lookup.
type nilNilScopedSagaStore struct{ SagaStore }

func (s *nilNilScopedSagaStore) FindByCorrelationIDAndType(context.Context, string, string) (*adapters.SagaState, error) {
	return nil, nil
}

func TestSagaManager_findSagaByCorrelation_NeverReturnsNilNil(t *testing.T) {
	ctx := context.Background()
	for name, store := range map[string]SagaStore{
		"unscoped": &nilNilSagaStore{SagaStore: memory.NewSagaStore()},
		"scoped":   &nilNilScopedSagaStore{SagaStore: memory.NewSagaStore()},
	} {
		t.Run(name, func(t *testing.T) {
			m := NewSagaManager(nil, WithSagaStore(store))
			st, err := m.findSagaByCorrelation(ctx, "SagaA", "c1")
			assert.Nil(t, st)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrSagaNotFound)
		})
	}
}

func TestSagaManager_ProcessEvent_NilNilStore_StartsSagaWithoutPanicking(t *testing.T) {
	ctx := context.Background()
	store := &nilNilSagaStore{SagaStore: memory.NewSagaStore()}
	m := newTypeProbeManager(t, store)

	require.NoError(t, m.ProcessEvent(ctx, StoredEvent{ID: "e1", StreamID: "c1", Type: "EvA", Data: []byte(`{}`), GlobalPosition: 1}))

	st, err := store.Load(ctx, "SagaA-c1")
	require.NoError(t, err)
	require.NotNil(t, st)
	assert.Equal(t, "SagaA", st.Type)
}

// ====================================================================
// Registered name must equal SagaType()
// ====================================================================

const sagaTypeMismatchLogMsg = "Saga factory reports a different SagaType than the name it is registered under; " +
	"lookups, persistence and re-drive key on the registered name, so events for this saga will fail with ErrSagaTypeMismatch"

func TestSagaManager_Register_SagaTypeMismatch_IsLoggedAndRefusedAtFirstUse(t *testing.T) {
	ctx := context.Background()
	logger := &argsRecordingLogger{}
	store := memory.NewSagaStore()
	m := NewSagaManager(New(memory.NewAdapter()), WithSagaStore(store), WithCommandBus(NewCommandBus()), WithSagaLogger(logger))

	// Registered as "Alpha" but the factory's sagas say they are "Beta".
	m.RegisterSimple("Alpha", typeProbeFactory("Beta", "EvA"), "EvA")

	entry, ok := logger.find(sagaTypeMismatchLogMsg)
	require.True(t, ok, "the mismatch must be logged at registration")
	assert.Equal(t, "error", entry.level)
	registered, _ := entry.argValue("registered")
	reported, _ := entry.argValue("sagaType")
	assert.Equal(t, "Alpha", registered)
	assert.Equal(t, "Beta", reported)

	// StartSaga surfaces the typed error; nothing is persisted under either name.
	err := m.StartSaga(ctx, "Alpha", StoredEvent{ID: "e1", StreamID: "c1", Type: "EvA", Data: []byte(`{}`), GlobalPosition: 1})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSagaTypeMismatch)
	var mismatch *SagaTypeMismatchError
	require.ErrorAs(t, err, &mismatch)
	assert.Equal(t, "Alpha", mismatch.ExpectedType)
	assert.Equal(t, "Beta", mismatch.ActualType)
	assert.Equal(t, "c1", mismatch.CorrelationID)
	assert.Equal(t, "Alpha-c1", mismatch.SagaID)
	for _, typ := range []string{"Alpha", "Beta"} {
		states, err := store.FindByType(ctx, typ)
		require.NoError(t, err)
		assert.Empty(t, states, "no row may be persisted for %q", typ)
	}

	// The event loop reports it and continues (returns nil like any per-saga failure).
	require.NoError(t, m.ProcessEvent(ctx, StoredEvent{ID: "e2", StreamID: "c2", Type: "EvA", Data: []byte(`{}`), GlobalPosition: 2}))
	_, ok = logger.find("Failed to process saga event")
	assert.True(t, ok)
}

func TestSagaManager_Register_MatchingSagaType_IsNotLogged(t *testing.T) {
	logger := &argsRecordingLogger{}
	m := NewSagaManager(nil, WithSagaStore(memory.NewSagaStore()), WithSagaLogger(logger))
	m.RegisterSimple("Alpha", typeProbeFactory("Alpha", "EvA"), "EvA")

	_, ok := logger.find(sagaTypeMismatchLogMsg)
	assert.False(t, ok)
}
