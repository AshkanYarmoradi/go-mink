package mink

import (
	"context"
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

func TestInMemoryRepository_DeleteMany(t *testing.T) {
	repo := NewInMemoryRepository(func(m *TestReadModel) string { return m.ID })
	ctx := context.Background()

	_ = repo.Insert(ctx, &TestReadModel{ID: "1"})
	_ = repo.Insert(ctx, &TestReadModel{ID: "2"})
	_ = repo.Insert(ctx, &TestReadModel{ID: "3"})

	deleted, err := repo.DeleteMany(ctx, Query{})
	require.NoError(t, err)
	assert.Equal(t, int64(3), deleted)
	assert.Equal(t, 0, repo.Len())
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
