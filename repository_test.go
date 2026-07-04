package mink

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestReadModel is a sample read model for testing.
type TestReadModel struct {
	ID      string
	Name    string
	Count   int
	Status  string
	Version int64
}

func (m *TestReadModel) GetID() string {
	return m.ID
}

func TestInMemoryRepository_Get(t *testing.T) {
	repo := NewInMemoryRepository(func(m *TestReadModel) string { return m.ID })
	ctx := context.Background()

	t.Run("returns ErrNotFound for missing item", func(t *testing.T) {
		_, err := repo.Get(ctx, "missing")
		assert.ErrorIs(t, err, ErrNotFound)
	})

	t.Run("returns item after insert", func(t *testing.T) {
		model := &TestReadModel{ID: "1", Name: "Test", Count: 5}
		require.NoError(t, repo.Insert(ctx, model))

		result, err := repo.Get(ctx, "1")
		require.NoError(t, err)
		assert.Equal(t, "Test", result.Name)
		assert.Equal(t, 5, result.Count)
	})
}

func TestInMemoryRepository_UnknownFilterFieldGuard(t *testing.T) {
	repo := NewInMemoryRepository(func(m *TestReadModel) string { return m.ID })
	ctx := context.Background()
	require.NoError(t, repo.Insert(ctx, &TestReadModel{ID: "1", Status: "active"}))
	require.NoError(t, repo.Insert(ctx, &TestReadModel{ID: "2", Status: "inactive"}))

	unknown := Query{Filters: []Filter{{Field: "does_not_exist", Op: FilterOpEq, Value: "x"}}}

	t.Run("Find rejects an unknown field", func(t *testing.T) {
		_, err := repo.Find(ctx, unknown)
		require.ErrorIs(t, err, ErrUnknownFilterField)
		require.ErrorIs(t, err, ErrInvalidQuery)
		var ufe *UnknownFilterFieldError
		require.ErrorAs(t, err, &ufe)
		assert.Equal(t, "does_not_exist", ufe.Field)
	})

	t.Run("Count rejects an unknown field", func(t *testing.T) {
		_, err := repo.Count(ctx, unknown)
		require.ErrorIs(t, err, ErrUnknownFilterField)
	})

	t.Run("typed error implements Unwrap to the wrapped sentinel", func(t *testing.T) {
		err := &UnknownFilterFieldError{Field: "x"}
		assert.Equal(t, ErrUnknownFilterField, errors.Unwrap(err))
		require.ErrorIs(t, err, ErrInvalidQuery) // reachable through the wrapped chain
	})

	t.Run("DeleteMany rejects an unknown field and deletes nothing", func(t *testing.T) {
		n, err := repo.DeleteMany(ctx, unknown)
		require.ErrorIs(t, err, ErrUnknownFilterField)
		assert.Zero(t, n)
		// Whole-table guard: both rows survive.
		count, err := repo.Count(ctx, Query{})
		require.NoError(t, err)
		assert.Equal(t, int64(2), count)
	})

	t.Run("a known field still works", func(t *testing.T) {
		results, err := repo.Find(ctx, Query{Filters: []Filter{{Field: "Status", Op: FilterOpEq, Value: "active"}}})
		require.NoError(t, err)
		assert.Len(t, results, 1)
	})

	t.Run("empty filters remain an explicit match-all", func(t *testing.T) {
		count, err := repo.Count(ctx, Query{})
		require.NoError(t, err)
		assert.Equal(t, int64(2), count)
	})
}

func TestInMemoryRepository_GetMany(t *testing.T) {
	repo := NewInMemoryRepository(func(m *TestReadModel) string { return m.ID })
	ctx := context.Background()

	// Insert some items
	_ = repo.Insert(ctx, &TestReadModel{ID: "1", Name: "One"})
	_ = repo.Insert(ctx, &TestReadModel{ID: "2", Name: "Two"})
	_ = repo.Insert(ctx, &TestReadModel{ID: "3", Name: "Three"})

	t.Run("returns existing items, ignores missing", func(t *testing.T) {
		results, err := repo.GetMany(ctx, []string{"1", "missing", "3"})
		require.NoError(t, err)
		assert.Len(t, results, 2)
	})

	t.Run("returns empty slice for all missing", func(t *testing.T) {
		results, err := repo.GetMany(ctx, []string{"x", "y"})
		require.NoError(t, err)
		assert.Empty(t, results)
	})
}

func TestInMemoryRepository_Insert(t *testing.T) {
	repo := NewInMemoryRepository(func(m *TestReadModel) string { return m.ID })
	ctx := context.Background()

	t.Run("inserts new item", func(t *testing.T) {
		model := &TestReadModel{ID: "new", Name: "New Item"}
		err := repo.Insert(ctx, model)
		require.NoError(t, err)

		result, _ := repo.Get(ctx, "new")
		assert.Equal(t, "New Item", result.Name)
	})

	t.Run("returns ErrAlreadyExists for duplicate", func(t *testing.T) {
		model := &TestReadModel{ID: "dup", Name: "First"}
		_ = repo.Insert(ctx, model)

		err := repo.Insert(ctx, &TestReadModel{ID: "dup", Name: "Second"})
		assert.ErrorIs(t, err, ErrAlreadyExists)

		// Original should be unchanged
		result, _ := repo.Get(ctx, "dup")
		assert.Equal(t, "First", result.Name)
	})
}

func TestInMemoryRepository_Update(t *testing.T) {
	repo := NewInMemoryRepository(func(m *TestReadModel) string { return m.ID })
	ctx := context.Background()

	_ = repo.Insert(ctx, &TestReadModel{ID: "upd", Name: "Original", Count: 1})

	t.Run("updates existing item", func(t *testing.T) {
		err := repo.Update(ctx, "upd", func(m *TestReadModel) {
			m.Name = "Updated"
			m.Count++
		})
		require.NoError(t, err)

		result, _ := repo.Get(ctx, "upd")
		assert.Equal(t, "Updated", result.Name)
		assert.Equal(t, 2, result.Count)
	})

	t.Run("returns ErrNotFound for missing item", func(t *testing.T) {
		err := repo.Update(ctx, "missing", func(m *TestReadModel) {
			m.Name = "Won't work"
		})
		assert.ErrorIs(t, err, ErrNotFound)
	})
}

func TestInMemoryRepository_Upsert(t *testing.T) {
	repo := NewInMemoryRepository(func(m *TestReadModel) string { return m.ID })
	ctx := context.Background()

	t.Run("inserts when not exists", func(t *testing.T) {
		model := &TestReadModel{ID: "ups1", Name: "New"}
		err := repo.Upsert(ctx, model)
		require.NoError(t, err)

		result, _ := repo.Get(ctx, "ups1")
		assert.Equal(t, "New", result.Name)
	})

	t.Run("updates when exists", func(t *testing.T) {
		_ = repo.Insert(ctx, &TestReadModel{ID: "ups2", Name: "Original"})

		err := repo.Upsert(ctx, &TestReadModel{ID: "ups2", Name: "Updated"})
		require.NoError(t, err)

		result, _ := repo.Get(ctx, "ups2")
		assert.Equal(t, "Updated", result.Name)
	})
}

func TestInMemoryRepository_Delete(t *testing.T) {
	repo := NewInMemoryRepository(func(m *TestReadModel) string { return m.ID })
	ctx := context.Background()

	_ = repo.Insert(ctx, &TestReadModel{ID: "del", Name: "ToDelete"})

	t.Run("deletes existing item", func(t *testing.T) {
		err := repo.Delete(ctx, "del")
		require.NoError(t, err)

		_, err = repo.Get(ctx, "del")
		assert.ErrorIs(t, err, ErrNotFound)
	})

	t.Run("returns ErrNotFound for missing item", func(t *testing.T) {
		err := repo.Delete(ctx, "missing")
		assert.ErrorIs(t, err, ErrNotFound)
	})
}

func TestInMemoryRepository_Clear(t *testing.T) {
	repo := NewInMemoryRepository(func(m *TestReadModel) string { return m.ID })
	ctx := context.Background()

	// Insert some items
	_ = repo.Insert(ctx, &TestReadModel{ID: "1"})
	_ = repo.Insert(ctx, &TestReadModel{ID: "2"})
	_ = repo.Insert(ctx, &TestReadModel{ID: "3"})

	assert.Equal(t, 3, repo.Len())

	err := repo.Clear(ctx)
	require.NoError(t, err)

	assert.Equal(t, 0, repo.Len())
}

func TestInMemoryRepository_Count(t *testing.T) {
	repo := NewInMemoryRepository(func(m *TestReadModel) string { return m.ID })
	ctx := context.Background()

	count, err := repo.Count(ctx, Query{})
	require.NoError(t, err)
	assert.Equal(t, int64(0), count)

	_ = repo.Insert(ctx, &TestReadModel{ID: "1"})
	_ = repo.Insert(ctx, &TestReadModel{ID: "2"})

	count, err = repo.Count(ctx, Query{})
	require.NoError(t, err)
	assert.Equal(t, int64(2), count)
}

func TestInMemoryRepository_Find(t *testing.T) {
	repo := NewInMemoryRepository(func(m *TestReadModel) string { return m.ID })
	ctx := context.Background()

	// Insert items
	for i := 0; i < 10; i++ {
		_ = repo.Insert(ctx, &TestReadModel{ID: string(rune('A' + i)), Count: i})
	}

	t.Run("returns all with empty query", func(t *testing.T) {
		results, err := repo.Find(ctx, Query{})
		require.NoError(t, err)
		assert.Len(t, results, 10)
	})

	t.Run("respects limit", func(t *testing.T) {
		results, err := repo.Find(ctx, Query{Limit: 3})
		require.NoError(t, err)
		assert.Len(t, results, 3)
	})

	t.Run("respects offset", func(t *testing.T) {
		results, err := repo.Find(ctx, Query{Offset: 8})
		require.NoError(t, err)
		assert.Len(t, results, 2)
	})

	t.Run("respects limit and offset together", func(t *testing.T) {
		results, err := repo.Find(ctx, Query{Limit: 3, Offset: 2})
		require.NoError(t, err)
		assert.Len(t, results, 3)
	})

	t.Run("returns empty for offset beyond data", func(t *testing.T) {
		results, err := repo.Find(ctx, Query{Offset: 100})
		require.NoError(t, err)
		assert.Empty(t, results)
	})
}

func TestInMemoryRepository_FindOne(t *testing.T) {
	repo := NewInMemoryRepository(func(m *TestReadModel) string { return m.ID })
	ctx := context.Background()

	t.Run("returns ErrNotFound when empty", func(t *testing.T) {
		_, err := repo.FindOne(ctx, Query{})
		assert.ErrorIs(t, err, ErrNotFound)
	})

	t.Run("returns first item", func(t *testing.T) {
		_ = repo.Insert(ctx, &TestReadModel{ID: "1", Name: "First"})
		_ = repo.Insert(ctx, &TestReadModel{ID: "2", Name: "Second"})

		result, err := repo.FindOne(ctx, Query{})
		require.NoError(t, err)
		assert.NotNil(t, result)
	})
}

func TestInMemoryRepository_CountWithFilters(t *testing.T) {
	repo := NewInMemoryRepository(func(m *TestReadModel) string { return m.ID })
	ctx := context.Background()

	// Insert items
	_ = repo.Insert(ctx, &TestReadModel{ID: "1", Name: "Active", Status: "active"})
	_ = repo.Insert(ctx, &TestReadModel{ID: "2", Name: "Inactive", Status: "inactive"})
	_ = repo.Insert(ctx, &TestReadModel{ID: "3", Name: "Active2", Status: "active"})

	t.Run("count without filters", func(t *testing.T) {
		count, err := repo.Count(ctx, Query{})
		require.NoError(t, err)
		assert.Equal(t, int64(3), count)
	})

	t.Run("count with filters returns matching count", func(t *testing.T) {
		query := NewQuery().Where("status", FilterOpEq, "active")
		count, err := repo.Count(ctx, query.Build())
		require.NoError(t, err)
		assert.Equal(t, int64(2), count)
	})
}

func TestInMemoryRepository_DeleteMany(t *testing.T) {
	ctx := context.Background()

	t.Run("empty query deletes all", func(t *testing.T) {
		repo := NewInMemoryRepository(func(m *TestReadModel) string { return m.ID })
		_ = repo.Insert(ctx, &TestReadModel{ID: "1"})
		_ = repo.Insert(ctx, &TestReadModel{ID: "2"})
		_ = repo.Insert(ctx, &TestReadModel{ID: "3"})

		deleted, err := repo.DeleteMany(ctx, Query{})
		require.NoError(t, err)
		assert.Equal(t, int64(3), deleted)
		assert.Equal(t, 0, repo.Len())
	})

	t.Run("filtered query deletes only matches", func(t *testing.T) {
		repo := NewInMemoryRepository(func(m *TestReadModel) string { return m.ID })
		_ = repo.Insert(ctx, &TestReadModel{ID: "1", Status: "stale"})
		_ = repo.Insert(ctx, &TestReadModel{ID: "2", Status: "fresh"})
		_ = repo.Insert(ctx, &TestReadModel{ID: "3", Status: "stale"})

		deleted, err := repo.DeleteMany(ctx, NewQuery().Where("status", FilterOpEq, "stale").Build())
		require.NoError(t, err)
		assert.Equal(t, int64(2), deleted)
		assert.Equal(t, 1, repo.Len())

		remaining, _ := repo.GetAll(ctx)
		require.Len(t, remaining, 1)
		assert.Equal(t, "2", remaining[0].ID)
	})
}

func TestInMemoryRepository_FindWithFiltersAndOrder(t *testing.T) {
	ctx := context.Background()
	repo := NewInMemoryRepository(func(m *TestReadModel) string { return m.ID })
	_ = repo.Insert(ctx, &TestReadModel{ID: "1", Name: "alice", Count: 30, Status: "active"})
	_ = repo.Insert(ctx, &TestReadModel{ID: "2", Name: "bob", Count: 10, Status: "inactive"})
	_ = repo.Insert(ctx, &TestReadModel{ID: "3", Name: "carol", Count: 20, Status: "active"})

	t.Run("equality filter", func(t *testing.T) {
		got, err := repo.Find(ctx, NewQuery().Where("status", FilterOpEq, "active").Build())
		require.NoError(t, err)
		require.Len(t, got, 2)
	})

	t.Run("numeric comparison filter", func(t *testing.T) {
		got, err := repo.Find(ctx, NewQuery().Where("count", FilterOpGte, 20).Build())
		require.NoError(t, err)
		require.Len(t, got, 2)
	})

	t.Run("order by ascending", func(t *testing.T) {
		got, err := repo.Find(ctx, NewQuery().OrderByAsc("count").Build())
		require.NoError(t, err)
		require.Len(t, got, 3)
		assert.Equal(t, "2", got[0].ID) // count 10
		assert.Equal(t, "3", got[1].ID) // count 20
		assert.Equal(t, "1", got[2].ID) // count 30
	})

	t.Run("order by descending with limit", func(t *testing.T) {
		got, err := repo.Find(ctx, NewQuery().OrderByDesc("count").WithLimit(1).Build())
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, "1", got[0].ID)
	})

	t.Run("IN filter", func(t *testing.T) {
		got, err := repo.Find(ctx, NewQuery().Where("name", FilterOpIn, []string{"alice", "carol"}).Build())
		require.NoError(t, err)
		require.Len(t, got, 2)
	})

	t.Run("BETWEEN filter", func(t *testing.T) {
		got, err := repo.Find(ctx, NewQuery().Where("count", FilterOpBetween, []int{15, 35}).Build())
		require.NoError(t, err)
		require.Len(t, got, 2)
	})

	t.Run("LIKE filter", func(t *testing.T) {
		got, err := repo.Find(ctx, NewQuery().Where("name", FilterOpLike, "a%").Build())
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, "alice", got[0].Name)
	})

	t.Run("malformed IN returns ErrInvalidQuery", func(t *testing.T) {
		_, err := repo.Find(ctx, NewQuery().Where("name", FilterOpIn, "notaslice").Build())
		assert.ErrorIs(t, err, ErrInvalidQuery)
	})

	t.Run("returned pointers are copies", func(t *testing.T) {
		got, err := repo.Find(ctx, NewQuery().Where("id", FilterOpEq, "1").Build())
		require.NoError(t, err)
		require.Len(t, got, 1)
		got[0].Name = "mutated"
		fresh, _ := repo.Get(ctx, "1")
		assert.Equal(t, "alice", fresh.Name)
	})
}

type embeddedBase struct {
	Score int    `json:"score"`
	Tier  string `json:"tier"`
}

type embeddingModel struct {
	embeddedBase
	ID   string `json:"id"`
	Name string `json:"name"`
}

type embeddedBase2 struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// shadowModel embeds embeddedBase2 (which has Name) and redeclares Name, which
// shadows the embedded field per Go/JSON promotion.
type shadowModel struct {
	embeddedBase2
	Name string `json:"name"`
}

func TestInMemoryRepository_QueryEngine(t *testing.T) {
	ctx := context.Background()

	t.Run("empty IN/NOT IN errors like Postgres", func(t *testing.T) {
		repo := NewInMemoryRepository(func(m *TestReadModel) string { return m.ID })
		_ = repo.Insert(ctx, &TestReadModel{ID: "1", Status: "a"})

		_, err := repo.Find(ctx, NewQuery().Where("status", FilterOpIn, []string{}).Build())
		assert.ErrorIs(t, err, ErrInvalidQuery, "empty IN must error")

		_, err = repo.Find(ctx, NewQuery().Where("status", FilterOpNotIn, []string{}).Build())
		assert.ErrorIs(t, err, ErrInvalidQuery, "empty NOT IN must error (must NOT match everything)")
	})

	t.Run("LIKE matches across newlines", func(t *testing.T) {
		repo := NewInMemoryRepository(func(m *TestReadModel) string { return m.ID })
		_ = repo.Insert(ctx, &TestReadModel{ID: "1", Name: "line1\nline2"})

		got, err := repo.Find(ctx, NewQuery().Where("name", FilterOpLike, "line1%line2").Build())
		require.NoError(t, err)
		assert.Len(t, got, 1, "%% should match across newlines like SQL LIKE")

		got, err = repo.Find(ctx, NewQuery().Where("name", FilterOpLike, "line1_line2").Build())
		require.NoError(t, err)
		assert.Len(t, got, 1, "_ should match a newline like SQL LIKE")
	})

	t.Run("ordering is deterministic when sort field unresolved", func(t *testing.T) {
		repo := NewInMemoryRepository(func(m *TestReadModel) string { return m.ID })
		for _, id := range []string{"c", "a", "b", "e", "d"} {
			_ = repo.Insert(ctx, &TestReadModel{ID: id})
		}
		// Order by a nonexistent field → must fall back to a stable ID order.
		var first []string
		for run := 0; run < 8; run++ {
			got, err := repo.Find(ctx, NewQuery().OrderByAsc("nope").Build())
			require.NoError(t, err)
			ids := make([]string, len(got))
			for i, g := range got {
				ids[i] = g.ID
			}
			if run == 0 {
				first = ids
			} else {
				assert.Equal(t, first, ids, "order must be deterministic across runs")
			}
		}
		assert.Equal(t, []string{"a", "b", "c", "d", "e"}, first)
	})

	t.Run("outer field shadows embedded field of the same key", func(t *testing.T) {
		repo := NewInMemoryRepository(func(m *shadowModel) string { return m.ID })
		_ = repo.Insert(ctx, &shadowModel{embeddedBase2: embeddedBase2{ID: "1", Name: "inner"}, Name: "outer"})

		// The outer Name shadows the embedded one, mirroring Go/JSON promotion.
		got, err := repo.Find(ctx, NewQuery().Where("name", FilterOpEq, "outer").Build())
		require.NoError(t, err)
		assert.Len(t, got, 1, "filter must match the shadowing (outer) field value")

		got, err = repo.Find(ctx, NewQuery().Where("name", FilterOpEq, "inner").Build())
		require.NoError(t, err)
		assert.Empty(t, got, "filter must NOT match the shadowed (embedded) field value")
	})

	t.Run("filters resolve promoted (embedded) fields", func(t *testing.T) {
		repo := NewInMemoryRepository(func(m *embeddingModel) string { return m.ID })
		_ = repo.Insert(ctx, &embeddingModel{ID: "1", Name: "x", embeddedBase: embeddedBase{Score: 50, Tier: "gold"}})
		_ = repo.Insert(ctx, &embeddingModel{ID: "2", Name: "y", embeddedBase: embeddedBase{Score: 10, Tier: "silver"}})

		got, err := repo.Find(ctx, NewQuery().Where("score", FilterOpGte, 30).Build())
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, "1", got[0].ID)

		got, err = repo.Find(ctx, NewQuery().Where("tier", FilterOpEq, "silver").Build())
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, "2", got[0].ID)
	})
}

func TestInMemoryRepository_FindForReadModelID(t *testing.T) {
	ctx := context.Background()
	repo := NewInMemoryRepositoryFor[TestReadModel]()
	require.NoError(t, repo.Insert(ctx, &TestReadModel{ID: "x", Name: "X"}))
	got, err := repo.Get(ctx, "x")
	require.NoError(t, err)
	assert.Equal(t, "X", got.Name)
}

// --- Query Builder Tests ---

func TestQuery_Builder(t *testing.T) {
	t.Run("creates query with Where", func(t *testing.T) {
		q := NewQuery().Where("status", FilterOpEq, "active")
		assert.Len(t, q.Filters, 1)
		assert.Equal(t, "status", q.Filters[0].Field)
		assert.Equal(t, FilterOpEq, q.Filters[0].Op)
		assert.Equal(t, "active", q.Filters[0].Value)
	})

	t.Run("chains multiple conditions", func(t *testing.T) {
		q := NewQuery().
			Where("status", FilterOpEq, "active").
			And("count", FilterOpGt, 5).
			OrderByAsc("name").
			OrderByDesc("created_at").
			WithLimit(10).
			WithOffset(20)

		assert.Len(t, q.Filters, 2)
		assert.Len(t, q.OrderBy, 2)
		assert.Equal(t, 10, q.Limit)
		assert.Equal(t, 20, q.Offset)
	})

	t.Run("WithPagination calculates offset", func(t *testing.T) {
		q := NewQuery().WithPagination(3, 25)
		assert.Equal(t, 25, q.Limit)
		assert.Equal(t, 50, q.Offset) // (3-1) * 25

		// Page 1
		q2 := NewQuery().WithPagination(1, 10)
		assert.Equal(t, 10, q2.Limit)
		assert.Equal(t, 0, q2.Offset)

		// Page 0 (edge case)
		q3 := NewQuery().WithPagination(0, 10)
		assert.Equal(t, 0, q3.Offset)
	})

	t.Run("WithCount sets flag", func(t *testing.T) {
		q := NewQuery().WithCount()
		assert.True(t, q.IncludeCount)
	})

	t.Run("Build returns copy", func(t *testing.T) {
		q := NewQuery().Where("field", FilterOpEq, "value")
		built := q.Build()
		assert.Equal(t, q.Filters, built.Filters)
	})
}

func TestFilterOp_AllValues(t *testing.T) {
	// Test that all filter operations are defined
	ops := []FilterOp{
		FilterOpEq,
		FilterOpNe,
		FilterOpGt,
		FilterOpGte,
		FilterOpLt,
		FilterOpLte,
		FilterOpIn,
		FilterOpNotIn,
		FilterOpLike,
		FilterOpIsNull,
		FilterOpIsNotNull,
		FilterOpContains,
		FilterOpBetween,
	}

	// Verify they're all distinct
	opSet := make(map[FilterOp]bool)
	for _, op := range ops {
		assert.False(t, opSet[op], "Duplicate op: %s", op)
		opSet[op] = true
	}
}

func TestOrderBy(t *testing.T) {
	t.Run("ascending order", func(t *testing.T) {
		q := NewQuery().OrderByAsc("name")
		assert.Len(t, q.OrderBy, 1)
		assert.Equal(t, "name", q.OrderBy[0].Field)
		assert.False(t, q.OrderBy[0].Desc)
	})

	t.Run("descending order", func(t *testing.T) {
		q := NewQuery().OrderByDesc("created_at")
		assert.Len(t, q.OrderBy, 1)
		assert.Equal(t, "created_at", q.OrderBy[0].Field)
		assert.True(t, q.OrderBy[0].Desc)
	})
}

func TestQueryResult(t *testing.T) {
	result := QueryResult[TestReadModel]{
		Items: []*TestReadModel{
			{ID: "1", Name: "One"},
			{ID: "2", Name: "Two"},
		},
		TotalCount: 100,
		HasMore:    true,
	}

	assert.Len(t, result.Items, 2)
	assert.Equal(t, int64(100), result.TotalCount)
	assert.True(t, result.HasMore)
}

func TestInMemoryRepository_Exists(t *testing.T) {
	repo := NewInMemoryRepository(func(m *TestReadModel) string { return m.ID })
	ctx := context.Background()

	t.Run("returns false for missing item", func(t *testing.T) {
		exists, err := repo.Exists(ctx, "missing")
		require.NoError(t, err)
		assert.False(t, exists)
	})

	t.Run("returns true for existing item", func(t *testing.T) {
		_ = repo.Insert(ctx, &TestReadModel{ID: "exists", Name: "Test"})

		exists, err := repo.Exists(ctx, "exists")
		require.NoError(t, err)
		assert.True(t, exists)
	})

	t.Run("returns false after delete", func(t *testing.T) {
		_ = repo.Insert(ctx, &TestReadModel{ID: "del", Name: "ToDelete"})
		_ = repo.Delete(ctx, "del")

		exists, err := repo.Exists(ctx, "del")
		require.NoError(t, err)
		assert.False(t, exists)
	})
}

func TestInMemoryRepository_GetAll(t *testing.T) {
	repo := NewInMemoryRepository(func(m *TestReadModel) string { return m.ID })
	ctx := context.Background()

	t.Run("returns empty slice when empty", func(t *testing.T) {
		results, err := repo.GetAll(ctx)
		require.NoError(t, err)
		assert.Empty(t, results)
	})

	t.Run("returns all items", func(t *testing.T) {
		_ = repo.Insert(ctx, &TestReadModel{ID: "1", Name: "One"})
		_ = repo.Insert(ctx, &TestReadModel{ID: "2", Name: "Two"})
		_ = repo.Insert(ctx, &TestReadModel{ID: "3", Name: "Three"})

		results, err := repo.GetAll(ctx)
		require.NoError(t, err)
		assert.Len(t, results, 3)

		// Verify all items are present (order may vary)
		ids := make(map[string]bool)
		for _, r := range results {
			ids[r.ID] = true
		}
		assert.True(t, ids["1"])
		assert.True(t, ids["2"])
		assert.True(t, ids["3"])
	})

	t.Run("returns updated count after changes", func(t *testing.T) {
		repo2 := NewInMemoryRepository(func(m *TestReadModel) string { return m.ID })

		_ = repo2.Insert(ctx, &TestReadModel{ID: "a"})
		_ = repo2.Insert(ctx, &TestReadModel{ID: "b"})

		results, _ := repo2.GetAll(ctx)
		assert.Len(t, results, 2)

		_ = repo2.Delete(ctx, "a")

		results, _ = repo2.GetAll(ctx)
		assert.Len(t, results, 1)
		assert.Equal(t, "b", results[0].ID)
	})
}
