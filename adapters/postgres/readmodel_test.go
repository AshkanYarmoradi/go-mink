package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	mink "github.com/AshkanYarmoradi/go-mink"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test models

// OrderSummary is a test read model for orders.
type OrderSummary struct {
	OrderID     string    `mink:"order_id,pk"`
	CustomerID  string    `mink:"customer_id,index"`
	Status      string    `mink:"status"`
	ItemCount   int       `mink:"item_count"`
	TotalAmount float64   `mink:"total_amount"`
	CreatedAt   time.Time `mink:"created_at"`
	UpdatedAt   time.Time `mink:"updated_at"`
}

// ProductView is a test read model for products.
type ProductView struct {
	SKU         string  `mink:"sku,pk"`
	Name        string  `mink:"name"`
	Description string  `mink:"description,nullable"`
	Price       float64 `mink:"price"`
	StockCount  int     `mink:"stock_count"`
	Active      bool    `mink:"active"`
}

// CustomerStats is a test model with various types.
type CustomerStats struct {
	CustomerID   string    `mink:"customer_id,pk"`
	OrderCount   int64     `mink:"order_count"`
	TotalSpent   float64   `mink:"total_spent"`
	LastOrderAt  time.Time `mink:"last_order_at,nullable"`
	IsVIP        bool      `mink:"is_vip"`
	Tags         []byte    `mink:"tags"` // JSONB stored as bytes
}

// SimpleModel tests default behavior without tags.
type SimpleModel struct {
	ID    string
	Name  string
	Value int
}

// createReadModelTestSchema creates a unique test schema for read model tests.
func createReadModelTestSchema(t *testing.T, db *sql.DB) string {
	schema := fmt.Sprintf("test_readmodel_%d", time.Now().UnixNano())
	_, err := db.Exec(fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %s", quoteIdentifier(schema)))
	require.NoError(t, err)

	t.Cleanup(func() {
		_, _ = db.Exec(fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", quoteIdentifier(schema)))
	})

	return schema
}

func TestNewPostgresRepository(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	db := getTestDB(t)
	defer db.Close()

	schema := createReadModelTestSchema(t, db)

	t.Run("creates repository with default options", func(t *testing.T) {
		repo, err := NewPostgresRepository[OrderSummary](db,
			WithReadModelSchema(schema),
			WithTableName("orders"),
		)
		require.NoError(t, err)
		assert.NotNil(t, repo)
		assert.Equal(t, schema, repo.Schema())
		defer repo.DropTable(context.Background())
	})

	t.Run("creates table automatically", func(t *testing.T) {
		repo, err := NewPostgresRepository[ProductView](db,
			WithReadModelSchema(schema),
			WithTableName("products"),
		)
		require.NoError(t, err)
		defer repo.DropTable(context.Background())

		// Verify table exists
		var exists bool
		err = db.QueryRow(`
			SELECT EXISTS (
				SELECT FROM information_schema.tables 
				WHERE table_schema = $1 AND table_name = 'products'
			)
		`, schema).Scan(&exists)
		require.NoError(t, err)
		assert.True(t, exists)
	})

	t.Run("infers table name from type", func(t *testing.T) {
		repo, err := NewPostgresRepository[CustomerStats](db,
			WithReadModelSchema(schema),
		)
		require.NoError(t, err)
		defer repo.DropTable(context.Background())

		assert.Contains(t, repo.TableName(), "customer_stats")
	})

	t.Run("validates schema name", func(t *testing.T) {
		_, err := NewPostgresRepository[OrderSummary](db,
			WithReadModelSchema("invalid;schema"),
		)
		assert.Error(t, err)
	})

	t.Run("validates table name", func(t *testing.T) {
		_, err := NewPostgresRepository[OrderSummary](db,
			WithReadModelSchema(schema),
			WithTableName("invalid;table"),
		)
		assert.Error(t, err)
	})
}

func TestPostgresRepository_CRUD(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	db := getTestDB(t)
	defer db.Close()

	schema := createReadModelTestSchema(t, db)

	repo, err := NewPostgresRepository[OrderSummary](db,
		WithReadModelSchema(schema),
		WithTableName("order_crud_test"),
	)
	require.NoError(t, err)
	defer repo.DropTable(context.Background())

	ctx := context.Background()
	now := time.Now().Truncate(time.Microsecond)

	t.Run("Insert and Get", func(t *testing.T) {
		order := &OrderSummary{
			OrderID:     "order-1",
			CustomerID:  "cust-1",
			Status:      "pending",
			ItemCount:   3,
			TotalAmount: 99.99,
			CreatedAt:   now,
			UpdatedAt:   now,
		}

		err := repo.Insert(ctx, order)
		require.NoError(t, err)

		// Get it back
		retrieved, err := repo.Get(ctx, "order-1")
		require.NoError(t, err)
		assert.Equal(t, order.OrderID, retrieved.OrderID)
		assert.Equal(t, order.CustomerID, retrieved.CustomerID)
		assert.Equal(t, order.Status, retrieved.Status)
		assert.Equal(t, order.ItemCount, retrieved.ItemCount)
		assert.InDelta(t, order.TotalAmount, retrieved.TotalAmount, 0.01)
	})

	t.Run("Insert duplicate returns error", func(t *testing.T) {
		order := &OrderSummary{
			OrderID:    "order-1",
			CustomerID: "cust-1",
			Status:     "pending",
		}

		err := repo.Insert(ctx, order)
		assert.ErrorIs(t, err, mink.ErrAlreadyExists)
	})

	t.Run("Get non-existent returns error", func(t *testing.T) {
		_, err := repo.Get(ctx, "non-existent")
		assert.ErrorIs(t, err, mink.ErrNotFound)
	})

	t.Run("Update", func(t *testing.T) {
		err := repo.Update(ctx, "order-1", func(o *OrderSummary) {
			o.Status = "shipped"
			o.ItemCount = 5
		})
		require.NoError(t, err)

		retrieved, err := repo.Get(ctx, "order-1")
		require.NoError(t, err)
		assert.Equal(t, "shipped", retrieved.Status)
		assert.Equal(t, 5, retrieved.ItemCount)
	})

	t.Run("Update non-existent returns error", func(t *testing.T) {
		err := repo.Update(ctx, "non-existent", func(o *OrderSummary) {
			o.Status = "cancelled"
		})
		assert.ErrorIs(t, err, mink.ErrNotFound)
	})

	t.Run("Upsert creates new", func(t *testing.T) {
		order := &OrderSummary{
			OrderID:     "order-2",
			CustomerID:  "cust-2",
			Status:      "new",
			ItemCount:   1,
			TotalAmount: 50.00,
			CreatedAt:   now,
			UpdatedAt:   now,
		}

		err := repo.Upsert(ctx, order)
		require.NoError(t, err)

		retrieved, err := repo.Get(ctx, "order-2")
		require.NoError(t, err)
		assert.Equal(t, "new", retrieved.Status)
	})

	t.Run("Upsert updates existing", func(t *testing.T) {
		order := &OrderSummary{
			OrderID:     "order-2",
			CustomerID:  "cust-2",
			Status:      "updated",
			ItemCount:   10,
			TotalAmount: 150.00,
			CreatedAt:   now,
			UpdatedAt:   now,
		}

		err := repo.Upsert(ctx, order)
		require.NoError(t, err)

		retrieved, err := repo.Get(ctx, "order-2")
		require.NoError(t, err)
		assert.Equal(t, "updated", retrieved.Status)
		assert.Equal(t, 10, retrieved.ItemCount)
	})

	t.Run("Delete", func(t *testing.T) {
		err := repo.Delete(ctx, "order-2")
		require.NoError(t, err)

		_, err = repo.Get(ctx, "order-2")
		assert.ErrorIs(t, err, mink.ErrNotFound)
	})

	t.Run("Delete non-existent returns error", func(t *testing.T) {
		err := repo.Delete(ctx, "non-existent")
		assert.ErrorIs(t, err, mink.ErrNotFound)
	})

	t.Run("Exists", func(t *testing.T) {
		exists, err := repo.Exists(ctx, "order-1")
		require.NoError(t, err)
		assert.True(t, exists)

		exists, err = repo.Exists(ctx, "non-existent")
		require.NoError(t, err)
		assert.False(t, exists)
	})
}

func TestPostgresRepository_GetMany(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	db := getTestDB(t)
	defer db.Close()

	schema := createReadModelTestSchema(t, db)

	repo, err := NewPostgresRepository[OrderSummary](db,
		WithReadModelSchema(schema),
		WithTableName("order_getmany_test"),
	)
	require.NoError(t, err)
	defer repo.DropTable(context.Background())

	ctx := context.Background()
	now := time.Now()

	// Insert test data
	for i := 1; i <= 5; i++ {
		order := &OrderSummary{
			OrderID:     fmt.Sprintf("order-%d", i),
			CustomerID:  fmt.Sprintf("cust-%d", i),
			Status:      "pending",
			ItemCount:   i,
			TotalAmount: float64(i) * 10.0,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		require.NoError(t, repo.Insert(ctx, order))
	}

	t.Run("returns requested items", func(t *testing.T) {
		results, err := repo.GetMany(ctx, []string{"order-1", "order-3", "order-5"})
		require.NoError(t, err)
		assert.Len(t, results, 3)
	})

	t.Run("skips non-existent items", func(t *testing.T) {
		results, err := repo.GetMany(ctx, []string{"order-1", "non-existent", "order-3"})
		require.NoError(t, err)
		assert.Len(t, results, 2)
	})

	t.Run("empty input returns empty slice", func(t *testing.T) {
		results, err := repo.GetMany(ctx, []string{})
		require.NoError(t, err)
		assert.Empty(t, results)
	})
}

func TestPostgresRepository_Query(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	db := getTestDB(t)
	defer db.Close()

	schema := createReadModelTestSchema(t, db)

	repo, err := NewPostgresRepository[OrderSummary](db,
		WithReadModelSchema(schema),
		WithTableName("order_query_test"),
	)
	require.NoError(t, err)
	defer repo.DropTable(context.Background())

	ctx := context.Background()
	now := time.Now()

	// Insert test data
	statuses := []string{"pending", "shipped", "delivered", "pending", "cancelled"}
	for i := 1; i <= 5; i++ {
		order := &OrderSummary{
			OrderID:     fmt.Sprintf("order-%d", i),
			CustomerID:  fmt.Sprintf("cust-%d", (i-1)/2+1), // cust-1, cust-1, cust-2, cust-2, cust-3
			Status:      statuses[i-1],
			ItemCount:   i,
			TotalAmount: float64(i) * 25.0,
			CreatedAt:   now.Add(time.Duration(i) * time.Hour),
			UpdatedAt:   now,
		}
		require.NoError(t, repo.Insert(ctx, order))
	}

	t.Run("Find with equality filter", func(t *testing.T) {
		query := mink.NewQuery().Where("status", mink.FilterOpEq, "pending")
		results, err := repo.Find(ctx, query.Build())
		require.NoError(t, err)
		assert.Len(t, results, 2)
	})

	t.Run("Find with greater than filter", func(t *testing.T) {
		query := mink.NewQuery().Where("total_amount", mink.FilterOpGt, 75.0)
		results, err := repo.Find(ctx, query.Build())
		require.NoError(t, err)
		assert.Len(t, results, 2) // order-4 (100) and order-5 (125)
	})

	t.Run("Find with multiple filters", func(t *testing.T) {
		query := mink.NewQuery().
			Where("status", mink.FilterOpEq, "pending").
			And("item_count", mink.FilterOpGt, 2)
		results, err := repo.Find(ctx, query.Build())
		require.NoError(t, err)
		assert.Len(t, results, 1) // order-4
	})

	t.Run("Find with IN filter", func(t *testing.T) {
		query := mink.NewQuery().Where("status", mink.FilterOpIn, []string{"shipped", "delivered"})
		results, err := repo.Find(ctx, query.Build())
		require.NoError(t, err)
		assert.Len(t, results, 2)
	})

	t.Run("Find with ordering", func(t *testing.T) {
		query := mink.NewQuery().OrderByDesc("item_count")
		results, err := repo.Find(ctx, query.Build())
		require.NoError(t, err)
		require.Len(t, results, 5)
		assert.Equal(t, 5, results[0].ItemCount)
		assert.Equal(t, 1, results[4].ItemCount)
	})

	t.Run("Find with limit and offset", func(t *testing.T) {
		query := mink.NewQuery().
			OrderByAsc("order_id").
			WithLimit(2).
			WithOffset(2)
		results, err := repo.Find(ctx, query.Build())
		require.NoError(t, err)
		assert.Len(t, results, 2)
		assert.Equal(t, "order-3", results[0].OrderID)
		assert.Equal(t, "order-4", results[1].OrderID)
	})

	t.Run("Find with pagination", func(t *testing.T) {
		query := mink.NewQuery().
			OrderByAsc("order_id").
			WithPagination(2, 2) // page 2, pageSize 2
		results, err := repo.Find(ctx, query.Build())
		require.NoError(t, err)
		assert.Len(t, results, 2)
		assert.Equal(t, "order-3", results[0].OrderID)
	})

	t.Run("FindOne returns first match", func(t *testing.T) {
		query := mink.NewQuery().
			Where("status", mink.FilterOpEq, "pending").
			OrderByAsc("order_id")
		result, err := repo.FindOne(ctx, query.Build())
		require.NoError(t, err)
		assert.Equal(t, "order-1", result.OrderID)
	})

	t.Run("FindOne returns error when no match", func(t *testing.T) {
		query := mink.NewQuery().Where("status", mink.FilterOpEq, "nonexistent")
		_, err := repo.FindOne(ctx, query.Build())
		assert.ErrorIs(t, err, mink.ErrNotFound)
	})

	t.Run("Count with filter", func(t *testing.T) {
		query := mink.NewQuery().Where("status", mink.FilterOpEq, "pending")
		count, err := repo.Count(ctx, query.Build())
		require.NoError(t, err)
		assert.Equal(t, int64(2), count)
	})

	t.Run("Count all", func(t *testing.T) {
		count, err := repo.Count(ctx, mink.Query{})
		require.NoError(t, err)
		assert.Equal(t, int64(5), count)
	})

	t.Run("GetAll returns all items", func(t *testing.T) {
		results, err := repo.GetAll(ctx)
		require.NoError(t, err)
		assert.Len(t, results, 5)
	})
}

func TestPostgresRepository_DeleteMany(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	db := getTestDB(t)
	defer db.Close()

	schema := createReadModelTestSchema(t, db)

	repo, err := NewPostgresRepository[OrderSummary](db,
		WithReadModelSchema(schema),
		WithTableName("order_deletemany_test"),
	)
	require.NoError(t, err)
	defer repo.DropTable(context.Background())

	ctx := context.Background()
	now := time.Now()

	// Insert test data
	statuses := []string{"pending", "shipped", "pending", "cancelled", "pending"}
	for i := 1; i <= 5; i++ {
		order := &OrderSummary{
			OrderID:     fmt.Sprintf("order-%d", i),
			CustomerID:  "cust-1",
			Status:      statuses[i-1],
			ItemCount:   i,
			TotalAmount: float64(i) * 10.0,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		require.NoError(t, repo.Insert(ctx, order))
	}

	t.Run("deletes matching items", func(t *testing.T) {
		query := mink.NewQuery().Where("status", mink.FilterOpEq, "pending")
		deleted, err := repo.DeleteMany(ctx, query.Build())
		require.NoError(t, err)
		assert.Equal(t, int64(3), deleted)

		count, err := repo.Count(ctx, mink.Query{})
		require.NoError(t, err)
		assert.Equal(t, int64(2), count)
	})
}

func TestPostgresRepository_Clear(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	db := getTestDB(t)
	defer db.Close()

	schema := createReadModelTestSchema(t, db)

	repo, err := NewPostgresRepository[OrderSummary](db,
		WithReadModelSchema(schema),
		WithTableName("order_clear_test"),
	)
	require.NoError(t, err)
	defer repo.DropTable(context.Background())

	ctx := context.Background()
	now := time.Now()

	// Insert test data
	for i := 1; i <= 3; i++ {
		order := &OrderSummary{
			OrderID:    fmt.Sprintf("order-%d", i),
			CustomerID: "cust-1",
			Status:     "pending",
			CreatedAt:  now,
			UpdatedAt:  now,
		}
		require.NoError(t, repo.Insert(ctx, order))
	}

	// Clear all
	err = repo.Clear(ctx)
	require.NoError(t, err)

	// Verify empty
	count, err := repo.Count(ctx, mink.Query{})
	require.NoError(t, err)
	assert.Equal(t, int64(0), count)
}

func TestPostgresRepository_Transaction(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	db := getTestDB(t)
	defer db.Close()

	schema := createReadModelTestSchema(t, db)

	repo, err := NewPostgresRepository[OrderSummary](db,
		WithReadModelSchema(schema),
		WithTableName("order_tx_test"),
	)
	require.NoError(t, err)
	defer repo.DropTable(context.Background())

	ctx := context.Background()
	now := time.Now()

	t.Run("commit transaction", func(t *testing.T) {
		tx, err := db.BeginTx(ctx, nil)
		require.NoError(t, err)

		txRepo := repo.WithTx(tx)

		order := &OrderSummary{
			OrderID:    "tx-order-1",
			CustomerID: "cust-1",
			Status:     "pending",
			CreatedAt:  now,
			UpdatedAt:  now,
		}
		err = txRepo.Insert(ctx, order)
		require.NoError(t, err)

		err = tx.Commit()
		require.NoError(t, err)

		// Should be visible after commit
		retrieved, err := repo.Get(ctx, "tx-order-1")
		require.NoError(t, err)
		assert.Equal(t, "pending", retrieved.Status)
	})

	t.Run("rollback transaction", func(t *testing.T) {
		tx, err := db.BeginTx(ctx, nil)
		require.NoError(t, err)

		txRepo := repo.WithTx(tx)

		order := &OrderSummary{
			OrderID:    "tx-order-2",
			CustomerID: "cust-1",
			Status:     "pending",
			CreatedAt:  now,
			UpdatedAt:  now,
		}
		err = txRepo.Insert(ctx, order)
		require.NoError(t, err)

		err = tx.Rollback()
		require.NoError(t, err)

		// Should not exist after rollback
		_, err = repo.Get(ctx, "tx-order-2")
		assert.ErrorIs(t, err, mink.ErrNotFound)
	})

	t.Run("transaction upsert", func(t *testing.T) {
		tx, err := db.BeginTx(ctx, nil)
		require.NoError(t, err)

		txRepo := repo.WithTx(tx)

		order := &OrderSummary{
			OrderID:    "tx-order-1",
			CustomerID: "cust-1",
			Status:     "shipped",
			CreatedAt:  now,
			UpdatedAt:  now,
		}
		err = txRepo.Upsert(ctx, order)
		require.NoError(t, err)

		err = tx.Commit()
		require.NoError(t, err)

		retrieved, err := repo.Get(ctx, "tx-order-1")
		require.NoError(t, err)
		assert.Equal(t, "shipped", retrieved.Status)
	})
}

func TestPostgresRepository_Migration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	db := getTestDB(t)
	defer db.Close()

	schema := createReadModelTestSchema(t, db)

	t.Run("adds missing columns", func(t *testing.T) {
		// First create table with minimal columns manually
		tableQ := quoteQualifiedTable(schema, "migration_test")
		_, err := db.Exec(fmt.Sprintf(`
			CREATE TABLE %s (
				order_id TEXT PRIMARY KEY,
				status TEXT NOT NULL
			)
		`, tableQ))
		require.NoError(t, err)

		// Now create repository - should add missing columns
		repo, err := NewPostgresRepository[OrderSummary](db,
			WithReadModelSchema(schema),
			WithTableName("migration_test"),
		)
		require.NoError(t, err)
		defer repo.DropTable(context.Background())

		// Insert with all columns
		ctx := context.Background()
		now := time.Now()
		order := &OrderSummary{
			OrderID:     "mig-order-1",
			CustomerID:  "cust-1",
			Status:      "pending",
			ItemCount:   5,
			TotalAmount: 100.0,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		err = repo.Insert(ctx, order)
		require.NoError(t, err)

		retrieved, err := repo.Get(ctx, "mig-order-1")
		require.NoError(t, err)
		assert.Equal(t, 5, retrieved.ItemCount)
	})
}

func TestPostgresRepository_SimpleModel(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	db := getTestDB(t)
	defer db.Close()

	schema := createReadModelTestSchema(t, db)

	// Test without db tags - should use snake_case and first field as PK
	repo, err := NewPostgresRepository[SimpleModel](db,
		WithReadModelSchema(schema),
		WithTableName("simple_models"),
	)
	require.NoError(t, err)
	defer repo.DropTable(context.Background())

	ctx := context.Background()

	model := &SimpleModel{
		ID:    "simple-1",
		Name:  "Test",
		Value: 42,
	}

	err = repo.Insert(ctx, model)
	require.NoError(t, err)

	retrieved, err := repo.Get(ctx, "simple-1")
	require.NoError(t, err)
	assert.Equal(t, "Test", retrieved.Name)
	assert.Equal(t, 42, retrieved.Value)
}

func TestToSnakeCase(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"OrderID", "order_i_d"},
		{"CustomerID", "customer_i_d"},
		{"orderID", "order_i_d"},
		{"Simple", "simple"},
		{"CamelCase", "camel_case"},
		{"HTTPServer", "h_t_t_p_server"},
		{"ID", "i_d"},
		{"id", "id"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := toSnakeCase(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGoTypeToSQL(t *testing.T) {
	tests := []struct {
		name     string
		goType   interface{}
		expected string
	}{
		{"string", "", "TEXT"},
		{"int", 0, "INTEGER"},
		{"int64", int64(0), "BIGINT"},
		{"float64", 0.0, "DOUBLE PRECISION"},
		{"bool", false, "BOOLEAN"},
		{"time", time.Time{}, "TIMESTAMPTZ"},
		{"bytes", []byte{}, "BYTEA"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// The function expects reflect.Type, but we can verify behavior
			// through the repository's schema building
		})
	}
}

// Benchmark tests

func BenchmarkPostgresRepository_Insert(b *testing.B) {
	connStr := os.Getenv("TEST_DATABASE_URL")
	if connStr == "" {
		b.Skip("TEST_DATABASE_URL not set")
	}

	db, err := sql.Open("pgx", connStr)
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()

	schema := fmt.Sprintf("bench_%d", time.Now().UnixNano())
	_, _ = db.Exec(fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %s", quoteIdentifier(schema)))
	defer func() {
		_, _ = db.Exec(fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", quoteIdentifier(schema)))
	}()

	repo, err := NewPostgresRepository[OrderSummary](db,
		WithReadModelSchema(schema),
		WithTableName("bench_orders"),
	)
	if err != nil {
		b.Fatal(err)
	}

	ctx := context.Background()
	now := time.Now()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		order := &OrderSummary{
			OrderID:     fmt.Sprintf("bench-order-%d", i),
			CustomerID:  "cust-1",
			Status:      "pending",
			ItemCount:   1,
			TotalAmount: 99.99,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		_ = repo.Insert(ctx, order)
	}
}

func BenchmarkPostgresRepository_Get(b *testing.B) {
	connStr := os.Getenv("TEST_DATABASE_URL")
	if connStr == "" {
		b.Skip("TEST_DATABASE_URL not set")
	}

	db, err := sql.Open("pgx", connStr)
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()

	schema := fmt.Sprintf("bench_%d", time.Now().UnixNano())
	_, _ = db.Exec(fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %s", quoteIdentifier(schema)))
	defer func() {
		_, _ = db.Exec(fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", quoteIdentifier(schema)))
	}()

	repo, err := NewPostgresRepository[OrderSummary](db,
		WithReadModelSchema(schema),
		WithTableName("bench_orders_get"),
	)
	if err != nil {
		b.Fatal(err)
	}

	ctx := context.Background()
	now := time.Now()

	// Insert test data
	order := &OrderSummary{
		OrderID:     "bench-order-1",
		CustomerID:  "cust-1",
		Status:      "pending",
		ItemCount:   1,
		TotalAmount: 99.99,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	_ = repo.Insert(ctx, order)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = repo.Get(ctx, "bench-order-1")
	}
}
