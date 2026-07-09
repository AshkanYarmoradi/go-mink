package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	mink "go-mink.dev"
)

// nullableAllKinds exercises every nullable scalar kind that must coalesce a
// NULL to the Go zero value on read, plus the fields that must NOT be wrapped:
// a non-nullable scalar (NonNull), a pointer (Ptr) and a []byte/JSON column
// (Blob) — all three are already NULL-safe and keep the direct-address scan.
type nullableAllKinds struct {
	ID      string    `mink:"id,pk"`
	Str     string    `mink:"str,nullable"`
	Flag    bool      `mink:"flag,nullable"`
	I       int       `mink:"i,nullable"`
	I64     int64     `mink:"i64,nullable"`
	U       uint      `mink:"u,nullable"`
	U64     uint64    `mink:"u64,nullable"`
	F32     float32   `mink:"f32,nullable"`
	F64     float64   `mink:"f64,nullable"`
	When    time.Time `mink:"when,nullable"`
	NonNull string    `mink:"non_null"`      // scalar, NOT nullable -> must not wrap
	Ptr     *string   `mink:"ptr,nullable"`  // pointer -> already NULL-safe
	Blob    []byte    `mink:"blob,nullable"` // BYTEA -> already NULL-safe
}

func columnIndex(cols []string, name string) int {
	for i, c := range cols {
		if c == name {
			return i
		}
	}
	return -1
}

// newNullableRepo builds a repository without touching the database
// (WithAutoMigrate(false)), so the pure read-path classification and coalescing
// logic can be unit-tested with no infrastructure.
func newNullableRepo(t *testing.T) *PostgresRepository[nullableAllKinds] {
	t.Helper()
	repo, err := NewPostgresRepository[nullableAllKinds](nil,
		WithAutoMigrate(false),
		WithTableName("nullable_all_kinds"),
	)
	require.NoError(t, err)
	return repo
}

// TestScalarKindForType checks the classifier maps each Go type to the intended
// coalescing kind (and everything NULL-safe to scanKindNone).
func TestScalarKindForType(t *testing.T) {
	tests := []struct {
		name string
		typ  reflect.Type
		want scalarScanKind
	}{
		{"string", reflect.TypeOf(""), scanKindString},
		{"bool", reflect.TypeOf(false), scanKindBool},
		{"int", reflect.TypeOf(int(0)), scanKindInt},
		{"int8", reflect.TypeOf(int8(0)), scanKindInt},
		{"int64", reflect.TypeOf(int64(0)), scanKindInt},
		{"uint", reflect.TypeOf(uint(0)), scanKindUint},
		{"uint64", reflect.TypeOf(uint64(0)), scanKindUint},
		{"float32", reflect.TypeOf(float32(0)), scanKindFloat},
		{"float64", reflect.TypeOf(float64(0)), scanKindFloat},
		{"time.Time", reflect.TypeOf(time.Time{}), scanKindTime},
		{"pointer", reflect.TypeOf((*string)(nil)), scanKindNone},
		{"[]byte", reflect.TypeOf([]byte(nil)), scanKindNone},
		{"[]string", reflect.TypeOf([]string(nil)), scanKindNone},
		{"map", reflect.TypeOf(map[string]int(nil)), scanKindNone},
		{"other struct", reflect.TypeOf(struct{ X int }{}), scanKindNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, scalarKindForType(tt.typ))
		})
	}
}

// TestComputeScanKinds verifies the wrap set precomputed at construction: only
// nullable scalar columns are wrapped; non-nullable, pointer and byte/JSON
// columns stay scanKindNone.
func TestComputeScanKinds(t *testing.T) {
	repo := newNullableRepo(t)

	want := map[string]scalarScanKind{
		"id":       scanKindNone, // pk, not nullable
		"str":      scanKindString,
		"flag":     scanKindBool,
		"i":        scanKindInt,
		"i64":      scanKindInt,
		"u":        scanKindUint,
		"u64":      scanKindUint,
		"f32":      scanKindFloat,
		"f64":      scanKindFloat,
		"when":     scanKindTime,
		"non_null": scanKindNone, // scalar but not nullable
		"ptr":      scanKindNone, // pointer
		"blob":     scanKindNone, // []byte
	}
	require.Len(t, repo.scanKinds, len(repo.columns))
	for col, kind := range want {
		idx := columnIndex(repo.columns, col)
		require.GreaterOrEqualf(t, idx, 0, "column %q not found", col)
		assert.Equalf(t, kind, repo.scanKinds[idx], "column %q", col)
	}
}

// setHolder writes v into the sql.Null[V] holder for column name, asserting the
// holder type — which also proves getScanTargets wrapped that column with the
// intended kind (e.g. uint columns read through sql.Null[int64]).
func setHolder[V any](t *testing.T, sc rowScan, cols []string, name string, v V) {
	t.Helper()
	idx := columnIndex(cols, name)
	require.GreaterOrEqualf(t, idx, 0, "column %q not found", name)
	h, ok := sc.ptrs[idx].(*sql.Null[V])
	require.Truef(t, ok, "column %q holder is %T, want *sql.Null[%T]", name, sc.ptrs[idx], v)
	h.V = v
	h.Valid = true
}

// TestGetScanTargets_NonNullCoalesce proves a valid holder value is copied back
// into every nullable scalar field (the non-NULL path), including the uint via
// int64 and time.Time cases.
func TestGetScanTargets_NonNullCoalesce(t *testing.T) {
	repo := newNullableRepo(t)
	when := time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC)

	m := &nullableAllKinds{}
	sc := repo.getScanTargets(m)

	setHolder[string](t, sc, repo.columns, "str", "hello")
	setHolder[bool](t, sc, repo.columns, "flag", true)
	setHolder[int64](t, sc, repo.columns, "i", 7)
	setHolder[int64](t, sc, repo.columns, "i64", 8)
	setHolder[int64](t, sc, repo.columns, "u", 9)    // uint reads through int64
	setHolder[int64](t, sc, repo.columns, "u64", 10) // uint64 reads through int64
	setHolder[float64](t, sc, repo.columns, "f32", 1.5)
	setHolder[float64](t, sc, repo.columns, "f64", 2.5)
	setHolder[time.Time](t, sc, repo.columns, "when", when)

	sc.run()

	assert.Equal(t, "hello", m.Str)
	assert.True(t, m.Flag)
	assert.Equal(t, 7, m.I)
	assert.Equal(t, int64(8), m.I64)
	assert.Equal(t, uint(9), m.U)
	assert.Equal(t, uint64(10), m.U64)
	assert.Equal(t, float32(1.5), m.F32)
	assert.Equal(t, 2.5, m.F64)
	assert.Equal(t, when, m.When)
}

// TestGetScanTargets_NullCoalesce proves an invalid (NULL) holder coalesces to
// the field's zero value, overwriting any prior value — a NULL never fails.
func TestGetScanTargets_NullCoalesce(t *testing.T) {
	repo := newNullableRepo(t)

	// Start from non-zero fields to prove the NULL path overwrites them to zero.
	m := &nullableAllKinds{
		Str: "x", Flag: true, I: 5, I64: 6, U: 7, U64: 8, F32: 9, F64: 10,
		When: time.Now(),
	}
	sc := repo.getScanTargets(m)
	// Leave every holder zero/Valid=false (simulating a scanned NULL) and run.
	sc.run()

	assert.Equal(t, "", m.Str)
	assert.False(t, m.Flag)
	assert.Equal(t, 0, m.I)
	assert.Equal(t, int64(0), m.I64)
	assert.Equal(t, uint(0), m.U)
	assert.Equal(t, uint64(0), m.U64)
	assert.Equal(t, float32(0), m.F32)
	assert.Equal(t, float64(0), m.F64)
	assert.True(t, m.When.IsZero())
}

// TestGetScanTargets_UnwrappedFieldsDirect confirms non-nullable, pointer and
// []byte columns are scanned straight into the field address (no holder, no
// finalizer) — the unchanged path.
func TestGetScanTargets_UnwrappedFieldsDirect(t *testing.T) {
	repo := newNullableRepo(t)
	m := &nullableAllKinds{}
	sc := repo.getScanTargets(m)

	// 9 nullable scalar columns -> exactly 9 finalizers.
	assert.Len(t, sc.finalize, 9)

	for _, col := range []string{"id", "non_null", "ptr", "blob"} {
		idx := columnIndex(repo.columns, col)
		require.GreaterOrEqual(t, idx, 0)
		_, wrapped := sc.ptrs[idx].(*sql.Null[string])
		assert.Falsef(t, wrapped, "column %q must not be wrapped", col)
	}
}

func TestParseScanErrorColumn(t *testing.T) {
	tests := []struct {
		name string
		msg  string
		want string
	}{
		{
			name: "standard database/sql message",
			msg:  `sql: Scan error on column index 3, name "non_null": converting NULL to string is unsupported`,
			want: "non_null",
		},
		{
			name: "no name fragment",
			msg:  "some other error",
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, parseScanErrorColumn(tt.msg))
		})
	}
}

// TestMapScanError checks the Good-to-have mapping: a NULL-in-non-nullable
// driver failure becomes a typed *mink.NullColumnError naming the column/field
// and wrapping the original; anything else passes through untouched.
func TestMapScanError(t *testing.T) {
	repo := newNullableRepo(t)

	t.Run("maps converting NULL error", func(t *testing.T) {
		driverErr := errors.New(`sql: Scan error on column index 10, name "non_null": converting NULL to string is unsupported`)
		mapped := repo.mapScanError(driverErr)

		var nce *mink.NullColumnError
		require.True(t, errors.As(mapped, &nce))
		assert.Equal(t, "non_null", nce.Column)
		assert.Equal(t, "NonNull", nce.Field)
		assert.Equal(t, "string", nce.GoType)
		assert.Equal(t, driverErr, errors.Unwrap(nce))
		assert.Contains(t, nce.Error(), "nullable")
	})

	t.Run("passes through unrelated error", func(t *testing.T) {
		other := errors.New("connection refused")
		assert.Equal(t, other, repo.mapScanError(other))
	})

	t.Run("passes through when column unidentifiable", func(t *testing.T) {
		// converting-NULL text but no name fragment -> cannot name a column.
		e := errors.New("converting NULL to string is unsupported")
		got := repo.mapScanError(e)
		var nce *mink.NullColumnError
		assert.False(t, errors.As(got, &nce))
		assert.Equal(t, e, got)
	})

	t.Run("nil stays nil", func(t *testing.T) {
		assert.NoError(t, repo.mapScanError(nil))
	})
}

// --- Integration: real NULL round-trip through the driver ---

// TestPostgresRepository_NullableScalarReads exercises the fix end-to-end
// against a real database: genuine SQL NULLs (written out of band) read back as
// the Go zero value across Get/Find/FindOne, a mixed result set scans fully,
// pointer/JSON NULLs stay nil, a persisted zero value stays non-NULL, and a
// NULL in a non-nullable column surfaces a typed *mink.NullColumnError.
func TestPostgresRepository_NullableScalarReads(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	db := getTestDB(t)
	defer func() { _ = db.Close() }()

	schema := createReadModelTestSchema(t, db)
	ctx := context.Background()

	repo, err := NewPostgresRepository[nullableAllKinds](db,
		WithReadModelSchema(schema),
		WithTableName("nullable_all_kinds"),
	)
	require.NoError(t, err)
	defer func() { _ = repo.DropTable(ctx) }()

	tableQ := quoteQualifiedTable(schema, "nullable_all_kinds")
	when := time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC)
	ptr := "ptr-value"

	// A fully populated row.
	full := &nullableAllKinds{
		ID: "full", Str: "s", Flag: true, I: 1, I64: 2, U: 3, U64: 4,
		F32: 5.5, F64: 6.5, When: when, NonNull: "nn", Ptr: &ptr, Blob: []byte(`{"k":1}`),
	}
	require.NoError(t, repo.Insert(ctx, full))

	// A row that we will blank to NULL out of band on every nullable column.
	require.NoError(t, repo.Insert(ctx, &nullableAllKinds{ID: "nulls", NonNull: "nn"}))
	_, err = db.ExecContext(ctx, fmt.Sprintf(
		`UPDATE %s SET str=NULL, flag=NULL, i=NULL, i64=NULL, u=NULL, u64=NULL,
		 f32=NULL, f64=NULL, "when"=NULL, ptr=NULL, blob=NULL WHERE id='nulls'`, tableQ))
	require.NoError(t, err)

	t.Run("Get reads NULL scalar columns as the zero value", func(t *testing.T) {
		got, err := repo.Get(ctx, "nulls")
		require.NoError(t, err)
		assert.Equal(t, "", got.Str)
		assert.False(t, got.Flag)
		assert.Equal(t, 0, got.I)
		assert.Equal(t, int64(0), got.I64)
		assert.Equal(t, uint(0), got.U)
		assert.Equal(t, uint64(0), got.U64)
		assert.Equal(t, float32(0), got.F32)
		assert.Equal(t, float64(0), got.F64)
		assert.True(t, got.When.IsZero())
		// Pointer and []byte columns read NULL as nil, unchanged.
		assert.Nil(t, got.Ptr)
		assert.Nil(t, got.Blob)
	})

	t.Run("Get reads populated row unchanged", func(t *testing.T) {
		got, err := repo.Get(ctx, "full")
		require.NoError(t, err)
		assert.Equal(t, "s", got.Str)
		assert.True(t, got.Flag)
		assert.Equal(t, 1, got.I)
		assert.Equal(t, uint64(4), got.U64)
		assert.InDelta(t, 5.5, got.F32, 1e-6)
		assert.Equal(t, when, got.When.UTC())
		require.NotNil(t, got.Ptr)
		assert.Equal(t, "ptr-value", *got.Ptr)
	})

	t.Run("mixed NULL / non-NULL result set scans fully", func(t *testing.T) {
		results, err := repo.Find(ctx, mink.Query{OrderBy: []mink.OrderBy{{Field: "id"}}})
		require.NoError(t, err)
		require.Len(t, results, 2) // "full" and "nulls" both scanned, no aborted batch
		byID := map[string]*nullableAllKinds{}
		for _, r := range results {
			byID[r.ID] = r
		}
		require.Contains(t, byID, "full")
		require.Contains(t, byID, "nulls")
		assert.Equal(t, "s", byID["full"].Str)
		assert.Equal(t, "", byID["nulls"].Str)
		assert.True(t, byID["nulls"].When.IsZero())
	})

	t.Run("FindOne reads a NULL row without error", func(t *testing.T) {
		got, err := repo.FindOne(ctx, mink.Query{
			Filters: []mink.Filter{{Field: "id", Op: mink.FilterOpEq, Value: "nulls"}},
		})
		require.NoError(t, err)
		assert.Equal(t, "", got.Str)
		assert.True(t, got.When.IsZero())
	})

	t.Run("persisting a zero value stores the zero value, not NULL", func(t *testing.T) {
		require.NoError(t, repo.Insert(ctx, &nullableAllKinds{ID: "zero", NonNull: "nn"}))
		var strIsNull, whenIsNull bool
		require.NoError(t, db.QueryRowContext(ctx, fmt.Sprintf(
			`SELECT str IS NULL, "when" IS NULL FROM %s WHERE id='zero'`, tableQ)).
			Scan(&strIsNull, &whenIsNull))
		assert.False(t, strIsNull, "zero string must persist as '' not NULL")
		assert.False(t, whenIsNull, "zero time must persist as a value not NULL")
	})

	t.Run("NULL in a non-nullable column yields a typed NullColumnError", func(t *testing.T) {
		// Simulate a manual migration / external write: drop NOT NULL and blank
		// the non-nullable scalar column on one row.
		_, err := db.ExecContext(ctx, fmt.Sprintf(`ALTER TABLE %s ALTER COLUMN non_null DROP NOT NULL`, tableQ))
		require.NoError(t, err)
		require.NoError(t, repo.Insert(ctx, &nullableAllKinds{ID: "badnonnull", NonNull: "temp"}))
		_, err = db.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET non_null=NULL WHERE id='badnonnull'`, tableQ))
		require.NoError(t, err)

		_, err = repo.Get(ctx, "badnonnull")
		require.Error(t, err)
		var nce *mink.NullColumnError
		require.Truef(t, errors.As(err, &nce), "want *NullColumnError, got %T: %v", err, err)
		assert.Equal(t, "non_null", nce.Column)
		assert.Equal(t, "NonNull", nce.Field)
		assert.Contains(t, errors.Unwrap(nce).Error(), "converting NULL to")
	})
}
