package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go-mink.dev"
	"go-mink.dev/adapters/memory"
)

func setupSubjectIndex(t *testing.T) (*SubjectIndex, func()) {
	t.Helper()
	if testing.Short() {
		t.Skip("Skipping PostgreSQL integration test in short mode")
	}
	url := getTestDatabaseURL(t)
	db, err := sql.Open("pgx", url)
	require.NoError(t, err)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		t.Skipf("PostgreSQL not available: %v", err)
	}

	// UnixNano (not second-resolution) so two tests in the same second don't collide
	// on the table name and clobber each other's data.
	table := fmt.Sprintf("mink_subject_index_test_%d", time.Now().UnixNano())
	idx := NewSubjectIndex(db, WithSubjectIndexTable(table))
	require.NoError(t, idx.Initialize(context.Background()))

	cleanup := func() {
		_, _ = db.Exec("DROP TABLE IF EXISTS " + quoteQualifiedTable("public", table))
		_ = db.Close()
	}
	return idx, cleanup
}

func TestSubjectIndex_LongTableNameInitializes(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping PostgreSQL integration test in short mode")
	}
	url := getTestDatabaseURL(t)
	db, err := sql.Open("pgx", url)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	if err := db.Ping(); err != nil {
		t.Skipf("PostgreSQL not available: %v", err)
	}

	// A valid but maximal 63-char table name. The naive derived index name
	// "idx_<table>_subject" would be 75 chars — over PostgreSQL's 63-byte identifier
	// limit — so Initialize must bound it (boundedIndexName) rather than let PostgreSQL
	// silently truncate it (which could collide with another long table's index).
	table := ("t" + fmt.Sprintf("%d", time.Now().UnixNano()) + strings.Repeat("x", 63))[:63]
	require.Len(t, table, 63)

	idx := NewSubjectIndex(db, WithSubjectIndexTable(table))
	require.NoError(t, idx.Initialize(context.Background()), "Initialize must succeed for a max-length table name")
	defer func() { _, _ = db.Exec("DROP TABLE IF EXISTS " + quoteQualifiedTable("public", table)) }()

	ctx := context.Background()
	require.NoError(t, idx.IndexSubjects(ctx, "User-u1", []string{"u1"}))
	got, err := idx.StreamsBySubject(ctx, "u1")
	require.NoError(t, err)
	assert.Equal(t, []string{"User-u1"}, got)
}

func TestSubjectIndex_ReadWrite(t *testing.T) {
	idx, cleanup := setupSubjectIndex(t)
	defer cleanup()
	ctx := context.Background()

	require.NoError(t, idx.IndexSubjects(ctx, "User-u1", []string{"u1", "u2"}))
	require.NoError(t, idx.IndexSubjects(ctx, "Order-o1", []string{"u1"}))
	require.NoError(t, idx.IndexSubjects(ctx, "User-u1", []string{"u1"})) // idempotent (ON CONFLICT)

	got, err := idx.StreamsBySubject(ctx, "u1")
	require.NoError(t, err)
	assert.Equal(t, []string{"Order-o1", "User-u1"}, got)

	got, err = idx.StreamsBySubject(ctx, "u2")
	require.NoError(t, err)
	assert.Equal(t, []string{"User-u1"}, got)

	got, err = idx.StreamsBySubject(ctx, "nobody")
	require.NoError(t, err)
	assert.Empty(t, got)

	// Initialize is idempotent.
	require.NoError(t, idx.Initialize(ctx))
}

func TestSnapshotSubjectEraser_Postgres(t *testing.T) {
	adapter := setupIntegrationTest(t)
	ctx := context.Background()

	require.NoError(t, adapter.SaveSnapshot(ctx, "User-u1", 1, []byte(`{"email":"alice@example.com"}`)))
	require.NoError(t, adapter.SaveSnapshot(ctx, "Order-o9", 1, []byte(`{}`)))

	fp := &mink.SubjectFootprint{SubjectID: "u1", Streams: []string{"User-u1"}}
	out, err := mink.NewSnapshotSubjectEraser(adapter).EraseSubject(ctx, "u1", fp)
	require.NoError(t, err)
	assert.Equal(t, "snapshot", out.Name)
	assert.Equal(t, 1, out.Erased)

	snap, err := adapter.LoadSnapshot(ctx, "User-u1")
	require.NoError(t, err)
	assert.Nil(t, snap, "the subject's plaintext-state snapshot is deleted")

	other, err := adapter.LoadSnapshot(ctx, "Order-o9")
	require.NoError(t, err)
	assert.NotNil(t, other, "unrelated snapshots survive")
}

type indexTestEvent struct {
	UserID string `json:"userId"`
}

func TestPostgresAdapter_StreamsBySubject(t *testing.T) {
	adapter := setupIntegrationTest(t)
	ctx := context.Background()

	tagger := func(_ string, _ []byte, md mink.Metadata) []string {
		if md.UserID != "" {
			return []string{md.UserID}
		}
		return nil
	}
	store := mink.New(adapter, mink.WithSubjectTagger(tagger))
	store.RegisterEvents(indexTestEvent{})
	require.NoError(t, store.Append(ctx, "User-u1", []interface{}{indexTestEvent{UserID: "u1"}}, mink.WithAppendMetadata(mink.Metadata{UserID: "u1"})))
	require.NoError(t, store.Append(ctx, "Order-o1", []interface{}{indexTestEvent{UserID: "u1"}}, mink.WithAppendMetadata(mink.Metadata{UserID: "u1"})))
	require.NoError(t, store.Append(ctx, "User-u2", []interface{}{indexTestEvent{UserID: "u2"}}, mink.WithAppendMetadata(mink.Metadata{UserID: "u2"})))

	// Drift-free: the adapter reads the events' own $subjects tags (JSONB) directly.
	streams, err := adapter.StreamsBySubject(ctx, "u1")
	require.NoError(t, err)
	assert.Equal(t, []string{"Order-o1", "User-u1"}, streams)

	none, err := adapter.StreamsBySubject(ctx, "nobody")
	require.NoError(t, err)
	assert.Empty(t, none)

	// Injected explicitly into a resolver (never auto-detected) + asserted authoritative.
	fp, err := mink.NewSubjectResolver(store, mink.WithResolverIndex(adapter), mink.WithAuthoritativeIndex()).Resolve(ctx, "u1")
	require.NoError(t, err)
	assert.Equal(t, []string{"Order-o1", "User-u1"}, fp.Streams)
	assert.False(t, fp.Partial)
}

func TestSubjectIndex_EndToEndWithResolver(t *testing.T) {
	idx, cleanup := setupSubjectIndex(t)
	defer cleanup()
	ctx := context.Background()

	tagger := func(_ string, _ []byte, md mink.Metadata) []string {
		if md.UserID != "" {
			return []string{md.UserID}
		}
		return nil
	}

	// A memory-backed event store with two tagged events for u1 across two streams.
	store := mink.New(memory.NewAdapter(), mink.WithSubjectTagger(tagger), mink.WithSubjectIndexWriter(idx))
	store.RegisterEvents(indexTestEvent{})
	require.NoError(t, store.Append(ctx, "User-u1", []interface{}{indexTestEvent{UserID: "u1"}}, mink.WithAppendMetadata(mink.Metadata{UserID: "u1"})))
	require.NoError(t, store.Append(ctx, "Order-o1", []interface{}{indexTestEvent{UserID: "u1"}}, mink.WithAppendMetadata(mink.Metadata{UserID: "u1"})))

	// Append-time indexing populated the postgres index; the resolver reads from it.
	// WithAuthoritativeIndex asserts completeness (no concurrent writes here).
	fp, err := mink.NewSubjectResolver(store, mink.WithResolverIndex(idx), mink.WithAuthoritativeIndex()).Resolve(ctx, "u1")
	require.NoError(t, err)
	assert.Equal(t, []string{"Order-o1", "User-u1"}, fp.Streams)
	assert.False(t, fp.Partial, "an authoritative postgres index resolves a complete footprint")

	// BackfillSubjectIndex over the same store is idempotent (ON CONFLICT).
	n, err := mink.BackfillSubjectIndex(ctx, store, tagger, idx, 100)
	require.NoError(t, err)
	assert.Equal(t, 2, n)
}
