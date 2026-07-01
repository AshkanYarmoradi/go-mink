package postgres

import (
	"context"
	"database/sql"
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

	table := "mink_subject_index_test_" + time.Now().Format("20060102150405")
	idx := NewSubjectIndex(db, WithSubjectIndexTable(table))
	require.NoError(t, idx.Initialize(context.Background()))

	cleanup := func() {
		_, _ = db.Exec("DROP TABLE IF EXISTS " + quoteQualifiedTable("public", table))
		_ = db.Close()
	}
	return idx, cleanup
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

type indexTestEvent struct {
	UserID string `json:"userId"`
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
	fp, err := mink.NewSubjectResolver(store, mink.WithResolverIndex(idx)).Resolve(ctx, "u1")
	require.NoError(t, err)
	assert.Equal(t, []string{"Order-o1", "User-u1"}, fp.Streams)
	assert.False(t, fp.Partial, "an authoritative postgres index resolves a complete footprint")

	// BackfillSubjectIndex over the same store is idempotent (ON CONFLICT).
	n, err := mink.BackfillSubjectIndex(ctx, store, tagger, idx, 100)
	require.NoError(t, err)
	assert.Equal(t, 2, n)
}
