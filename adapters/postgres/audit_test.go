package postgres

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go-mink.dev/adapters"
)

func setupAuditTestStore(t *testing.T) *AuditStore {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	connStr := os.Getenv("TEST_DATABASE_URL")
	if connStr == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}

	adapter, err := NewAdapter(connStr)
	require.NoError(t, err)
	t.Cleanup(func() { _ = adapter.Close() })

	err = adapter.Initialize(context.Background())
	require.NoError(t, err)

	store := NewAuditStoreFromAdapter(adapter)

	err = store.Initialize(context.Background())
	require.NoError(t, err)

	err = store.Clear(context.Background())
	require.NoError(t, err)

	return store
}

func auditBoolPtr(b bool) *bool { return &b }

// seedPGAudit inserts a fixed set of entries and returns the base timestamp.
func seedPGAudit(t *testing.T, store *AuditStore) time.Time {
	t.Helper()
	ctx := context.Background()
	base := time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)

	entries := []*adapters.AuditEntry{
		{ID: "11111111-1111-1111-1111-111111111111", CommandType: "CreateOrder", Actor: "alice", TenantID: "t1", AggregateID: "order-1", CorrelationID: "req-1", Success: true, DurationMs: 5, Timestamp: base},
		{ID: "22222222-2222-2222-2222-222222222222", CommandType: "AddItem", Actor: "bob", TenantID: "t1", AggregateID: "order-1", Success: true, DurationMs: 7, Timestamp: base.Add(1 * time.Minute)},
		{ID: "33333333-3333-3333-3333-333333333333", CommandType: "ShipOrder", Actor: "alice", TenantID: "t2", AggregateID: "order-2", CorrelationID: "req-1", Success: false, Error: "boom", DurationMs: 9, Timestamp: base.Add(2 * time.Minute)},
		{ID: "44444444-4444-4444-4444-444444444444", CommandType: "CreateOrder", Actor: "carol", TenantID: "t2", AggregateID: "order-3", Success: true, DurationMs: 3, Timestamp: base.Add(3 * time.Minute)},
		{ID: "55555555-5555-5555-5555-555555555555", CommandType: "AddItem", Actor: "alice", TenantID: "t1", AggregateID: "order-3", Success: false, Error: "nope", DurationMs: 4, Timestamp: base.Add(4 * time.Minute)},
	}
	for _, e := range entries {
		require.NoError(t, store.Append(ctx, e))
	}
	return base
}

func auditIDs(entries []*adapters.AuditEntry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.ID
	}
	return out
}

func TestAuditStore_Initialize(t *testing.T) {
	store := setupAuditTestStore(t)
	// Calling Initialize again should be idempotent.
	require.NoError(t, store.Initialize(context.Background()))
}

func TestAuditStore_Append_NilEntry(t *testing.T) {
	store := setupAuditTestStore(t)
	ctx := context.Background()

	err := store.Append(ctx, nil)
	require.ErrorIs(t, err, adapters.ErrNilAuditEntry)

	// A nil entry must not be inserted.
	got, err := store.Find(ctx, adapters.AuditQuery{})
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestAuditStore_AppendAndFind_RoundTrip(t *testing.T) {
	store := setupAuditTestStore(t)
	ctx := context.Background()

	entry := &adapters.AuditEntry{
		ID:            "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
		CommandType:   "CreateOrder",
		CommandID:     "cmd-1",
		AggregateID:   "order-1",
		Version:       4,
		Actor:         "alice",
		TenantID:      "tenant-1",
		CorrelationID: "corr-1",
		CausationID:   "cause-1",
		Success:       true,
		DurationMs:    42,
		Timestamp:     time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC),
		Metadata:      map[string]string{"ip": "10.0.0.1", "ua": "curl"},
	}
	require.NoError(t, store.Append(ctx, entry))

	got, err := store.Find(ctx, adapters.AuditQuery{})
	require.NoError(t, err)
	require.Len(t, got, 1)
	g := got[0]
	assert.Equal(t, entry.ID, g.ID)
	assert.Equal(t, "CreateOrder", g.CommandType)
	assert.Equal(t, "cmd-1", g.CommandID)
	assert.Equal(t, "order-1", g.AggregateID)
	assert.Equal(t, int64(4), g.Version)
	assert.Equal(t, "alice", g.Actor)
	assert.Equal(t, "tenant-1", g.TenantID)
	assert.Equal(t, "corr-1", g.CorrelationID)
	assert.Equal(t, "cause-1", g.CausationID)
	assert.True(t, g.Success)
	assert.Equal(t, int64(42), g.DurationMs)
	assert.True(t, entry.Timestamp.Equal(g.Timestamp))
	assert.Equal(t, map[string]string{"ip": "10.0.0.1", "ua": "curl"}, g.Metadata)
}

func TestAuditStore_Append_GeneratesIDWhenEmpty(t *testing.T) {
	store := setupAuditTestStore(t)
	ctx := context.Background()

	require.NoError(t, store.Append(ctx, &adapters.AuditEntry{
		CommandType: "CreateOrder",
		Success:     true,
		Timestamp:   time.Now(),
	}))

	got, err := store.Find(ctx, adapters.AuditQuery{})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.NotEmpty(t, got[0].ID, "Append should generate the id client-side when empty")
}

func TestAuditStore_Append_NullableColumns(t *testing.T) {
	store := setupAuditTestStore(t)
	ctx := context.Background()

	// Minimal entry: empty optional strings, zero version, no metadata.
	require.NoError(t, store.Append(ctx, &adapters.AuditEntry{
		ID:          "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
		CommandType: "MinimalCmd",
		Success:     true,
		Timestamp:   time.Now(),
	}))

	got, err := store.Find(ctx, adapters.AuditQuery{})
	require.NoError(t, err)
	require.Len(t, got, 1)
	g := got[0]
	assert.Empty(t, g.CommandID)
	assert.Empty(t, g.AggregateID)
	assert.Equal(t, int64(0), g.Version)
	assert.Empty(t, g.Actor)
	assert.Empty(t, g.TenantID)
	assert.Empty(t, g.CorrelationID)
	assert.Empty(t, g.CausationID)
	assert.Empty(t, g.Error)
	assert.Nil(t, g.Metadata)
}

func TestAuditStore_Find_Filters(t *testing.T) {
	store := setupAuditTestStore(t)
	base := seedPGAudit(t, store)
	ctx := context.Background()

	tests := []struct {
		name    string
		query   adapters.AuditQuery
		wantLen int
	}{
		{"all", adapters.AuditQuery{}, 5},
		{"command type", adapters.AuditQuery{CommandType: "CreateOrder"}, 2},
		{"actor", adapters.AuditQuery{Actor: "alice"}, 3},
		{"tenant", adapters.AuditQuery{TenantID: "t1"}, 3},
		{"aggregate", adapters.AuditQuery{AggregateID: "order-3"}, 2},
		{"correlation", adapters.AuditQuery{CorrelationID: "req-1"}, 2},
		{"success true", adapters.AuditQuery{Success: auditBoolPtr(true)}, 3},
		{"success false", adapters.AuditQuery{Success: auditBoolPtr(false)}, 2},
		{"from inclusive", adapters.AuditQuery{From: base.Add(2 * time.Minute)}, 3},
		{"to exclusive", adapters.AuditQuery{To: base.Add(2 * time.Minute)}, 2},
		{"window", adapters.AuditQuery{From: base.Add(1 * time.Minute), To: base.Add(4 * time.Minute)}, 3},
		{"combined", adapters.AuditQuery{Actor: "alice", Success: auditBoolPtr(false)}, 2},
		{"no match", adapters.AuditQuery{CommandType: "Nope"}, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := store.Find(ctx, tt.query)
			require.NoError(t, err)
			assert.Len(t, got, tt.wantLen)
		})
	}
}

func TestAuditStore_Find_Ordering(t *testing.T) {
	store := setupAuditTestStore(t)
	seedPGAudit(t, store)
	ctx := context.Background()

	desc, err := store.Find(ctx, adapters.AuditQuery{Order: adapters.AuditOrderTimestampDesc})
	require.NoError(t, err)
	assert.Equal(t, []string{
		"55555555-5555-5555-5555-555555555555",
		"44444444-4444-4444-4444-444444444444",
		"33333333-3333-3333-3333-333333333333",
		"22222222-2222-2222-2222-222222222222",
		"11111111-1111-1111-1111-111111111111",
	}, auditIDs(desc))

	asc, err := store.Find(ctx, adapters.AuditQuery{Order: adapters.AuditOrderTimestampAsc})
	require.NoError(t, err)
	assert.Equal(t, []string{
		"11111111-1111-1111-1111-111111111111",
		"22222222-2222-2222-2222-222222222222",
		"33333333-3333-3333-3333-333333333333",
		"44444444-4444-4444-4444-444444444444",
		"55555555-5555-5555-5555-555555555555",
	}, auditIDs(asc))
}

func TestAuditStore_Find_LimitOffset(t *testing.T) {
	store := setupAuditTestStore(t)
	seedPGAudit(t, store)
	ctx := context.Background()

	page1, err := store.Find(ctx, adapters.AuditQuery{Order: adapters.AuditOrderTimestampAsc, Limit: 2, Offset: 0})
	require.NoError(t, err)
	assert.Equal(t, []string{
		"11111111-1111-1111-1111-111111111111",
		"22222222-2222-2222-2222-222222222222",
	}, auditIDs(page1))

	page2, err := store.Find(ctx, adapters.AuditQuery{Order: adapters.AuditOrderTimestampAsc, Limit: 2, Offset: 2})
	require.NoError(t, err)
	assert.Equal(t, []string{
		"33333333-3333-3333-3333-333333333333",
		"44444444-4444-4444-4444-444444444444",
	}, auditIDs(page2))

	page3, err := store.Find(ctx, adapters.AuditQuery{Order: adapters.AuditOrderTimestampAsc, Limit: 2, Offset: 4})
	require.NoError(t, err)
	assert.Equal(t, []string{"55555555-5555-5555-5555-555555555555"}, auditIDs(page3))
}

func TestAuditStore_Count(t *testing.T) {
	store := setupAuditTestStore(t)
	base := seedPGAudit(t, store)
	ctx := context.Background()

	all, err := store.Count(ctx, adapters.AuditQuery{})
	require.NoError(t, err)
	assert.Equal(t, int64(5), all)

	filtered, err := store.Count(ctx, adapters.AuditQuery{Actor: "alice"})
	require.NoError(t, err)
	assert.Equal(t, int64(3), filtered)

	// Count ignores limit/offset.
	ignored, err := store.Count(ctx, adapters.AuditQuery{Limit: 1, Offset: 3})
	require.NoError(t, err)
	assert.Equal(t, int64(5), ignored)

	windowed, err := store.Count(ctx, adapters.AuditQuery{From: base.Add(2 * time.Minute)})
	require.NoError(t, err)
	assert.Equal(t, int64(3), windowed)
}

func TestAuditStore_Cleanup(t *testing.T) {
	store := setupAuditTestStore(t)
	ctx := context.Background()
	now := time.Now()

	require.NoError(t, store.Append(ctx, &adapters.AuditEntry{ID: "cccccccc-cccc-cccc-cccc-cccccccccc01", CommandType: "Old", Success: true, Timestamp: now.Add(-48 * time.Hour)}))
	require.NoError(t, store.Append(ctx, &adapters.AuditEntry{ID: "cccccccc-cccc-cccc-cccc-cccccccccc02", CommandType: "Fresh", Success: true, Timestamp: now}))

	removed, err := store.Cleanup(ctx, 24*time.Hour)
	require.NoError(t, err)
	assert.Equal(t, int64(1), removed)

	count, err := store.Count(ctx, adapters.AuditQuery{})
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)
}

func TestAuditStore_WithOptions(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
	connStr := os.Getenv("TEST_DATABASE_URL")
	if connStr == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}

	t.Run("WithAuditTable and WithAuditSchema", func(t *testing.T) {
		adapter, err := NewAdapter(connStr)
		require.NoError(t, err)
		defer func() { _ = adapter.Close() }()

		store := NewAuditStore(adapter.db,
			WithAuditSchema("public"),
			WithAuditTable("custom_audit"))
		assert.Equal(t, `"public"."custom_audit"`, store.fullTableName())

		require.NoError(t, store.Initialize(context.Background()))
		require.NoError(t, store.Clear(context.Background()))

		ctx := context.Background()
		require.NoError(t, store.Append(ctx, &adapters.AuditEntry{
			ID: "dddddddd-dddd-dddd-dddd-dddddddddddd", CommandType: "Custom", Success: true, Timestamp: time.Now(),
		}))
		got, err := store.Find(ctx, adapters.AuditQuery{})
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, "Custom", got[0].CommandType)
	})
}

func TestAuditStore_Initialize_RejectsBadIdentifiers(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
	connStr := os.Getenv("TEST_DATABASE_URL")
	if connStr == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}

	adapter, err := NewAdapter(connStr)
	require.NoError(t, err)
	defer func() { _ = adapter.Close() }()

	t.Run("bad schema", func(t *testing.T) {
		store := NewAuditStore(adapter.db, WithAuditSchema("bad; DROP TABLE x"))
		err := store.Initialize(context.Background())
		require.Error(t, err)
	})

	t.Run("bad table", func(t *testing.T) {
		store := NewAuditStore(adapter.db, WithAuditTable("bad table name"))
		err := store.Initialize(context.Background())
		require.Error(t, err)
	})
}

func TestAuditStore_Close(t *testing.T) {
	store := NewAuditStore(nil)
	require.NoError(t, store.Close())
}

func TestAuditSchemaStatements(t *testing.T) {
	stmts := auditSchemaStatements("public", "mink_audit")
	require.NotEmpty(t, stmts)
	assert.Contains(t, stmts[0], `"public"."mink_audit"`)
	assert.Contains(t, stmts[0], "command_type VARCHAR(255) NOT NULL")
	assert.Contains(t, stmts[0], "metadata JSONB")
	// One table + six indexes.
	assert.Len(t, stmts, 7)

	// Short index names keep their original, stable, bare identifiers (no schema
	// prefix), so re-running Initialize never creates a second, redundant set.
	joined := strings.Join(stmts, "\n")
	assert.Contains(t, joined, `"idx_mink_audit_timestamp"`)
	assert.Contains(t, joined, `"idx_mink_audit_correlation_id"`)
	// The schema-prefix marker only appears if an index name was rewritten via
	// safeIndexName; short names must keep their bare identifiers.
	assert.NotContains(t, joined, "public_idx")
}

func TestAuditSchemaStatements_IndexNamesWithinLimit(t *testing.T) {
	// A long custom audit table name (via WithAuditTable) must not produce index
	// identifiers exceeding PostgreSQL's 63-character limit, which would otherwise
	// make Initialize/GenerateSchema fail even though the table name validates.
	indexName := regexp.MustCompile(`CREATE INDEX IF NOT EXISTS "([^"]+)"`)
	stmts := auditSchemaStatements("public", strings.Repeat("a", 60))

	var checked int
	for _, s := range stmts {
		m := indexName.FindStringSubmatch(s)
		if m == nil {
			continue
		}
		checked++
		assert.LessOrEqualf(t, len(m[1]), 63, "index name %q exceeds 63 chars (%d)", m[1], len(m[1]))
	}
	assert.Equal(t, 6, checked, "expected all six audit indexes to be length-checked")
}

func TestAuditStore_Initialize_LongTableName(t *testing.T) {
	// Regression: a long (but valid) custom table name produces raw index names
	// well over Postgres's 63-char limit. Initialize must still succeed because
	// the index names are passed through safeIndexName.
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
	connStr := os.Getenv("TEST_DATABASE_URL")
	if connStr == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}

	adapter, err := NewAdapter(connStr)
	require.NoError(t, err)
	t.Cleanup(func() { _ = adapter.Close() })

	// 61-char table name: valid (<=63), but "idx_<table>_correlation_id" is ~80.
	longTable := "mink_audit_" + strings.Repeat("x", 50)
	require.LessOrEqual(t, len(longTable), 63)

	store := NewAuditStore(adapter.db, WithAuditTable(longTable))
	require.NoError(t, store.Initialize(context.Background()))
}
