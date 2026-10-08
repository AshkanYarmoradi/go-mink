package postgres

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go-mink.dev"
)

func TestSubjectIndex_DeleteSubject(t *testing.T) {
	idx, cleanup := setupSubjectIndex(t)
	defer cleanup()
	ctx := context.Background()

	require.NoError(t, idx.IndexSubjects(ctx, "User-u1", []string{"u1", "u2"}))
	require.NoError(t, idx.IndexSubjects(ctx, "Order-o1", []string{"u1"}))

	require.NoError(t, idx.DeleteSubject(ctx, "u1"))

	got, err := idx.StreamsBySubject(ctx, "u1")
	require.NoError(t, err)
	assert.Empty(t, got, "every entry for the purged subject is gone")

	got, err = idx.StreamsBySubject(ctx, "u2")
	require.NoError(t, err)
	assert.Equal(t, []string{"User-u1"}, got, "other subjects keep their entries")

	require.NoError(t, idx.DeleteSubject(ctx, "u1"), "idempotent")
	require.NoError(t, idx.DeleteSubject(ctx, "nobody"), "unknown subject is a no-op")
	require.NoError(t, idx.DeleteSubject(ctx, ""), "empty subject is a no-op")

	var _ mink.SubjectIndexPurger = idx
}

func TestSubjectIndex_DeleteSubject_ClosedDB(t *testing.T) {
	// sql.Open does not connect, so this needs no database: the closed pool makes
	// ExecContext fail, which must surface as a wrapped error.
	db, err := sql.Open("pgx", "postgres://localhost:1/none")
	require.NoError(t, err)
	require.NoError(t, db.Close())

	err = NewSubjectIndex(db).DeleteSubject(context.Background(), "u1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mink/postgres/subjectindex: failed to delete subject")
}
