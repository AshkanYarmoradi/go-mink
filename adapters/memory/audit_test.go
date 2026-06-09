package memory

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go-mink.dev/adapters"
)

func boolPtr(b bool) *bool { return &b }

// seedEntries inserts a fixed set of entries spanning a range of timestamps,
// command types, actors, tenants, aggregates, and success states.
func seedAuditStore(t *testing.T) (*AuditStore, time.Time) {
	t.Helper()
	store := NewAuditStore()
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	entries := []*adapters.AuditEntry{
		{ID: "1", CommandType: "CreateOrder", Actor: "alice", TenantID: "t1", AggregateID: "order-1", Success: true, Timestamp: base},
		{ID: "2", CommandType: "AddItem", Actor: "bob", TenantID: "t1", AggregateID: "order-1", Success: true, Timestamp: base.Add(1 * time.Minute)},
		{ID: "3", CommandType: "ShipOrder", Actor: "alice", TenantID: "t2", AggregateID: "order-2", Success: false, Error: "boom", Timestamp: base.Add(2 * time.Minute)},
		{ID: "4", CommandType: "CreateOrder", Actor: "carol", TenantID: "t2", AggregateID: "order-3", Success: true, Timestamp: base.Add(3 * time.Minute)},
		{ID: "5", CommandType: "AddItem", Actor: "alice", TenantID: "t1", AggregateID: "order-3", Success: false, Error: "nope", Timestamp: base.Add(4 * time.Minute)},
	}
	for _, e := range entries {
		require.NoError(t, store.Append(ctx, e))
	}
	return store, base
}

func ids(entries []*adapters.AuditEntry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.ID
	}
	return out
}

func TestNewAuditStore(t *testing.T) {
	store := NewAuditStore()
	assert.NotNil(t, store)
	assert.Equal(t, 0, store.Len())
}

func TestAuditStore_InitializeAndClose_NoOp(t *testing.T) {
	store := NewAuditStore()
	require.NoError(t, store.Initialize(context.Background()))
	require.NoError(t, store.Close())
}

func TestAuditStore_Append_StoresCopy(t *testing.T) {
	store := NewAuditStore()
	ctx := context.Background()
	entry := &adapters.AuditEntry{
		ID:          "1",
		CommandType: "CreateOrder",
		Metadata:    map[string]string{"k": "v"},
		Timestamp:   time.Now(),
	}
	require.NoError(t, store.Append(ctx, entry))

	// Mutate the original after append; the store must be unaffected.
	entry.CommandType = "Mutated"
	entry.Metadata["k"] = "changed"

	got, err := store.Find(ctx, adapters.AuditQuery{})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "CreateOrder", got[0].CommandType)
	assert.Equal(t, "v", got[0].Metadata["k"])

	// Mutating the returned copy must not affect the stored entry.
	got[0].Metadata["k"] = "again"
	got2, err := store.Find(ctx, adapters.AuditQuery{})
	require.NoError(t, err)
	assert.Equal(t, "v", got2[0].Metadata["k"])
}

func TestAuditStore_Append_NilMetadataStaysNil(t *testing.T) {
	store := NewAuditStore()
	ctx := context.Background()
	require.NoError(t, store.Append(ctx, &adapters.AuditEntry{ID: "1", Timestamp: time.Now()}))
	got, err := store.Find(ctx, adapters.AuditQuery{})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Nil(t, got[0].Metadata)
}

func TestAuditStore_Append_NilEntry(t *testing.T) {
	store := NewAuditStore()
	ctx := context.Background()

	err := store.Append(ctx, nil)
	require.ErrorIs(t, err, adapters.ErrNilAuditEntry)

	// A nil entry must not be stored (and must not panic a later query).
	got, err := store.Find(ctx, adapters.AuditQuery{})
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestAuditStore_Append_ContextCancelled(t *testing.T) {
	store := NewAuditStore()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := store.Append(ctx, &adapters.AuditEntry{ID: "1", Timestamp: time.Now()})
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 0, store.Len())
}

func TestAuditStore_Append_ContextCancelledWaitingForLock(t *testing.T) {
	store := NewAuditStore()
	ctx, cancel := context.WithCancel(context.Background())

	// Hold the lock so the concurrent Append below blocks while acquiring it,
	// then cancel before releasing. The post-lock cancellation check must honor
	// the cancellation and refuse to append, even though the context was still
	// live when Append passed its pre-lock fast-fail check.
	store.mu.Lock()
	appended := make(chan error, 1)
	go func() {
		appended <- store.Append(ctx, &adapters.AuditEntry{ID: "1", Timestamp: time.Now()})
	}()
	cancel()
	store.mu.Unlock()

	require.ErrorIs(t, <-appended, context.Canceled)
	assert.Equal(t, 0, store.Len())
}

func TestAuditStore_Find_ByCorrelationID(t *testing.T) {
	store := NewAuditStore()
	ctx := context.Background()
	require.NoError(t, store.Append(ctx, &adapters.AuditEntry{ID: "1", CorrelationID: "corr-A", Timestamp: time.Now()}))
	require.NoError(t, store.Append(ctx, &adapters.AuditEntry{ID: "2", CorrelationID: "corr-B", Timestamp: time.Now()}))
	require.NoError(t, store.Append(ctx, &adapters.AuditEntry{ID: "3", CorrelationID: "corr-A", Timestamp: time.Now()}))

	got, err := store.Find(ctx, adapters.AuditQuery{CorrelationID: "corr-A"})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"1", "3"}, ids(got))

	n, err := store.Count(ctx, adapters.AuditQuery{CorrelationID: "corr-A"})
	require.NoError(t, err)
	assert.Equal(t, int64(2), n)
}

func TestAuditStore_Find_Filters(t *testing.T) {
	store, base := seedAuditStore(t)
	ctx := context.Background()

	tests := []struct {
		name    string
		query   adapters.AuditQuery
		wantIDs []string // order-independent
	}{
		{"no filter returns all", adapters.AuditQuery{}, []string{"1", "2", "3", "4", "5"}},
		{"by command type", adapters.AuditQuery{CommandType: "CreateOrder"}, []string{"1", "4"}},
		{"by actor", adapters.AuditQuery{Actor: "alice"}, []string{"1", "3", "5"}},
		{"by tenant", adapters.AuditQuery{TenantID: "t1"}, []string{"1", "2", "5"}},
		{"by aggregate", adapters.AuditQuery{AggregateID: "order-3"}, []string{"4", "5"}},
		{"success true", adapters.AuditQuery{Success: boolPtr(true)}, []string{"1", "2", "4"}},
		{"success false", adapters.AuditQuery{Success: boolPtr(false)}, []string{"3", "5"}},
		{"from inclusive", adapters.AuditQuery{From: base.Add(2 * time.Minute)}, []string{"3", "4", "5"}},
		{"to exclusive", adapters.AuditQuery{To: base.Add(2 * time.Minute)}, []string{"1", "2"}},
		{"from and to window", adapters.AuditQuery{From: base.Add(1 * time.Minute), To: base.Add(4 * time.Minute)}, []string{"2", "3", "4"}},
		{"combined filters", adapters.AuditQuery{Actor: "alice", Success: boolPtr(false)}, []string{"3", "5"}},
		{"no match", adapters.AuditQuery{CommandType: "Nonexistent"}, []string{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := store.Find(ctx, tt.query)
			require.NoError(t, err)
			assert.ElementsMatch(t, tt.wantIDs, ids(got))
		})
	}
}

func TestAuditStore_Find_Ordering(t *testing.T) {
	store, _ := seedAuditStore(t)
	ctx := context.Background()

	t.Run("default desc (most recent first)", func(t *testing.T) {
		got, err := store.Find(ctx, adapters.AuditQuery{})
		require.NoError(t, err)
		assert.Equal(t, []string{"5", "4", "3", "2", "1"}, ids(got))
	})

	t.Run("explicit asc (oldest first)", func(t *testing.T) {
		got, err := store.Find(ctx, adapters.AuditQuery{Order: adapters.AuditOrderTimestampAsc})
		require.NoError(t, err)
		assert.Equal(t, []string{"1", "2", "3", "4", "5"}, ids(got))
	})
}

func TestAuditStore_Find_LimitOffset(t *testing.T) {
	store, _ := seedAuditStore(t)
	ctx := context.Background()

	t.Run("limit only (asc)", func(t *testing.T) {
		got, err := store.Find(ctx, adapters.AuditQuery{Order: adapters.AuditOrderTimestampAsc, Limit: 2})
		require.NoError(t, err)
		assert.Equal(t, []string{"1", "2"}, ids(got))
	})

	t.Run("offset only (asc)", func(t *testing.T) {
		got, err := store.Find(ctx, adapters.AuditQuery{Order: adapters.AuditOrderTimestampAsc, Offset: 3})
		require.NoError(t, err)
		assert.Equal(t, []string{"4", "5"}, ids(got))
	})

	t.Run("limit and offset (asc)", func(t *testing.T) {
		got, err := store.Find(ctx, adapters.AuditQuery{Order: adapters.AuditOrderTimestampAsc, Limit: 2, Offset: 1})
		require.NoError(t, err)
		assert.Equal(t, []string{"2", "3"}, ids(got))
	})

	t.Run("offset beyond range returns empty", func(t *testing.T) {
		got, err := store.Find(ctx, adapters.AuditQuery{Offset: 99})
		require.NoError(t, err)
		assert.Empty(t, got)
	})

	t.Run("limit larger than result returns all", func(t *testing.T) {
		got, err := store.Find(ctx, adapters.AuditQuery{Limit: 100})
		require.NoError(t, err)
		assert.Len(t, got, 5)
	})

	t.Run("limit zero returns all", func(t *testing.T) {
		got, err := store.Find(ctx, adapters.AuditQuery{Limit: 0})
		require.NoError(t, err)
		assert.Len(t, got, 5)
	})
}

func TestAuditStore_Count(t *testing.T) {
	store, base := seedAuditStore(t)
	ctx := context.Background()

	t.Run("counts all", func(t *testing.T) {
		c, err := store.Count(ctx, adapters.AuditQuery{})
		require.NoError(t, err)
		assert.Equal(t, int64(5), c)
	})

	t.Run("counts filtered", func(t *testing.T) {
		c, err := store.Count(ctx, adapters.AuditQuery{Actor: "alice"})
		require.NoError(t, err)
		assert.Equal(t, int64(3), c)
	})

	t.Run("ignores limit and offset", func(t *testing.T) {
		c, err := store.Count(ctx, adapters.AuditQuery{Limit: 1, Offset: 2})
		require.NoError(t, err)
		assert.Equal(t, int64(5), c)
	})

	t.Run("counts time window", func(t *testing.T) {
		c, err := store.Count(ctx, adapters.AuditQuery{From: base.Add(2 * time.Minute)})
		require.NoError(t, err)
		assert.Equal(t, int64(3), c)
	})
}

func TestAuditStore_Cleanup(t *testing.T) {
	store := NewAuditStore()
	ctx := context.Background()
	now := time.Now()

	require.NoError(t, store.Append(ctx, &adapters.AuditEntry{ID: "old", Timestamp: now.Add(-48 * time.Hour)}))
	require.NoError(t, store.Append(ctx, &adapters.AuditEntry{ID: "recent", Timestamp: now.Add(-1 * time.Hour)}))
	require.NoError(t, store.Append(ctx, &adapters.AuditEntry{ID: "fresh", Timestamp: now}))

	removed, err := store.Cleanup(ctx, 24*time.Hour)
	require.NoError(t, err)
	assert.Equal(t, int64(1), removed)
	assert.Equal(t, 2, store.Len())

	got, err := store.Find(ctx, adapters.AuditQuery{})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"recent", "fresh"}, ids(got))
}

func TestAuditStore_LenAndClear(t *testing.T) {
	store, _ := seedAuditStore(t)
	assert.Equal(t, 5, store.Len())
	store.Clear()
	assert.Equal(t, 0, store.Len())

	got, err := store.Find(context.Background(), adapters.AuditQuery{})
	require.NoError(t, err)
	assert.Empty(t, got)
}
