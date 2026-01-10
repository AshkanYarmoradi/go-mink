package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"reflect"
	"strings"
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
	CustomerID  string    `mink:"customer_id,pk"`
	OrderCount  int64     `mink:"order_count"`
	TotalSpent  float64   `mink:"total_spent"`
	LastOrderAt time.Time `mink:"last_order_at,nullable"`
	IsVIP       bool      `mink:"is_vip"`
	Tags        []byte    `mink:"tags"` // JSONB stored as bytes
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

// testOrderSummaryRepo is a helper to create an OrderSummary repository for testing.
type testOrderSummaryRepo struct {
	repo *PostgresRepository[OrderSummary]
	db   *sql.DB
	ctx  context.Context
	now  time.Time
}

// setupOrderSummaryRepo creates a repository with standard test setup.
func setupOrderSummaryRepo(t *testing.T, tableName string) *testOrderSummaryRepo {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}
	db := getTestDB(t)
	t.Cleanup(func() { db.Close() })
	schema := createReadModelTestSchema(t, db)
	repo, err := NewPostgresRepository[OrderSummary](db,
		WithReadModelSchema(schema),
		WithTableName(tableName),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = repo.DropTable(context.Background()) })
	return &testOrderSummaryRepo{
		repo: repo,
		db:   db,
		ctx:  context.Background(),
		now:  time.Now().Truncate(time.Microsecond),
	}
}

// newOrder creates a test OrderSummary with the given ID.
func (tr *testOrderSummaryRepo) newOrder(id, customerID, status string, itemCount int, totalAmount float64) *OrderSummary {
	return &OrderSummary{
		OrderID:     id,
		CustomerID:  customerID,
		Status:      status,
		ItemCount:   itemCount,
		TotalAmount: totalAmount,
		CreatedAt:   tr.now,
		UpdatedAt:   tr.now,
	}
}

// insertOrders inserts multiple test orders.
func (tr *testOrderSummaryRepo) insertOrders(t *testing.T, count int, statusFn func(i int) string) {
	for i := 1; i <= count; i++ {
		status := "pending"
		if statusFn != nil {
			status = statusFn(i)
		}
		order := tr.newOrder(fmt.Sprintf("order-%d", i), fmt.Sprintf("cust-%d", i), status, i, float64(i)*10.0)
		require.NoError(t, tr.repo.Insert(tr.ctx, order))
	}
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
		defer func() { _ = repo.DropTable(context.Background()) }()
	})

	t.Run("creates table automatically", func(t *testing.T) {
		repo, err := NewPostgresRepository[ProductView](db,
			WithReadModelSchema(schema),
			WithTableName("products"),
		)
		require.NoError(t, err)
		defer func() { _ = repo.DropTable(context.Background()) }()

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
		defer func() { _ = repo.DropTable(context.Background()) }()

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
	defer func() { _ = repo.DropTable(context.Background()) }()

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
	tr := setupOrderSummaryRepo(t, "order_getmany_test")
	tr.insertOrders(t, 5, nil)

	t.Run("returns requested items", func(t *testing.T) {
		results, err := tr.repo.GetMany(tr.ctx, []string{"order-1", "order-3", "order-5"})
		require.NoError(t, err)
		assert.Len(t, results, 3)
	})

	t.Run("skips non-existent items", func(t *testing.T) {
		results, err := tr.repo.GetMany(tr.ctx, []string{"order-1", "non-existent", "order-3"})
		require.NoError(t, err)
		assert.Len(t, results, 2)
	})

	t.Run("empty input returns empty slice", func(t *testing.T) {
		results, err := tr.repo.GetMany(tr.ctx, []string{})
		require.NoError(t, err)
		assert.Empty(t, results)
	})
}

func TestPostgresRepository_Query(t *testing.T) {
	tr := setupOrderSummaryRepo(t, "order_query_test")
	statuses := []string{"pending", "shipped", "delivered", "pending", "cancelled"}
	for i := 1; i <= 5; i++ {
		order := &OrderSummary{
			OrderID:     fmt.Sprintf("order-%d", i),
			CustomerID:  fmt.Sprintf("cust-%d", (i-1)/2+1),
			Status:      statuses[i-1],
			ItemCount:   i,
			TotalAmount: float64(i) * 25.0,
			CreatedAt:   tr.now.Add(time.Duration(i) * time.Hour),
			UpdatedAt:   tr.now,
		}
		require.NoError(t, tr.repo.Insert(tr.ctx, order))
	}

	t.Run("Find with equality filter", func(t *testing.T) {
		query := mink.NewQuery().Where("status", mink.FilterOpEq, "pending")
		results, err := tr.repo.Find(tr.ctx, query.Build())
		require.NoError(t, err)
		assert.Len(t, results, 2)
	})

	t.Run("Find with greater than filter", func(t *testing.T) {
		query := mink.NewQuery().Where("total_amount", mink.FilterOpGt, 75.0)
		results, err := tr.repo.Find(tr.ctx, query.Build())
		require.NoError(t, err)
		assert.Len(t, results, 2) // order-4 (100) and order-5 (125)
	})

	t.Run("Find with multiple filters", func(t *testing.T) {
		query := mink.NewQuery().
			Where("status", mink.FilterOpEq, "pending").
			And("item_count", mink.FilterOpGt, 2)
		results, err := tr.repo.Find(tr.ctx, query.Build())
		require.NoError(t, err)
		assert.Len(t, results, 1) // order-4
	})

	t.Run("Find with IN filter", func(t *testing.T) {
		query := mink.NewQuery().Where("status", mink.FilterOpIn, []string{"shipped", "delivered"})
		results, err := tr.repo.Find(tr.ctx, query.Build())
		require.NoError(t, err)
		assert.Len(t, results, 2)
	})

	t.Run("Find with ordering", func(t *testing.T) {
		query := mink.NewQuery().OrderByDesc("item_count")
		results, err := tr.repo.Find(tr.ctx, query.Build())
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
		results, err := tr.repo.Find(tr.ctx, query.Build())
		require.NoError(t, err)
		assert.Len(t, results, 2)
		assert.Equal(t, "order-3", results[0].OrderID)
		assert.Equal(t, "order-4", results[1].OrderID)
	})

	t.Run("Find with pagination", func(t *testing.T) {
		query := mink.NewQuery().
			OrderByAsc("order_id").
			WithPagination(2, 2) // page 2, pageSize 2
		results, err := tr.repo.Find(tr.ctx, query.Build())
		require.NoError(t, err)
		assert.Len(t, results, 2)
		assert.Equal(t, "order-3", results[0].OrderID)
	})

	t.Run("FindOne returns first match", func(t *testing.T) {
		query := mink.NewQuery().
			Where("status", mink.FilterOpEq, "pending").
			OrderByAsc("order_id")
		result, err := tr.repo.FindOne(tr.ctx, query.Build())
		require.NoError(t, err)
		assert.Equal(t, "order-1", result.OrderID)
	})

	t.Run("FindOne returns error when no match", func(t *testing.T) {
		query := mink.NewQuery().Where("status", mink.FilterOpEq, "nonexistent")
		_, err := tr.repo.FindOne(tr.ctx, query.Build())
		assert.ErrorIs(t, err, mink.ErrNotFound)
	})

	t.Run("Count with filter", func(t *testing.T) {
		query := mink.NewQuery().Where("status", mink.FilterOpEq, "pending")
		count, err := tr.repo.Count(tr.ctx, query.Build())
		require.NoError(t, err)
		assert.Equal(t, int64(2), count)
	})

	t.Run("Count all", func(t *testing.T) {
		count, err := tr.repo.Count(tr.ctx, mink.Query{})
		require.NoError(t, err)
		assert.Equal(t, int64(5), count)
	})

	t.Run("GetAll returns all items", func(t *testing.T) {
		results, err := tr.repo.GetAll(tr.ctx)
		require.NoError(t, err)
		assert.Len(t, results, 5)
	})

	t.Run("Find with negative limit returns error", func(t *testing.T) {
		query := mink.Query{Limit: -1}
		_, err := tr.repo.Find(tr.ctx, query)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "limit must be non-negative")
	})

	t.Run("Find with negative offset returns error", func(t *testing.T) {
		query := mink.Query{Offset: -5}
		_, err := tr.repo.Find(tr.ctx, query)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "offset must be non-negative")
	})
}

func TestPostgresRepository_DeleteMany(t *testing.T) {
	tr := setupOrderSummaryRepo(t, "order_deletemany_test")
	statuses := []string{"pending", "shipped", "pending", "cancelled", "pending"}
	tr.insertOrders(t, 5, func(i int) string { return statuses[i-1] })

	t.Run("deletes matching items", func(t *testing.T) {
		query := mink.NewQuery().Where("status", mink.FilterOpEq, "pending")
		deleted, err := tr.repo.DeleteMany(tr.ctx, query.Build())
		require.NoError(t, err)
		assert.Equal(t, int64(3), deleted)

		count, err := tr.repo.Count(tr.ctx, mink.Query{})
		require.NoError(t, err)
		assert.Equal(t, int64(2), count)
	})
}

func TestPostgresRepository_Clear(t *testing.T) {
	tr := setupOrderSummaryRepo(t, "order_clear_test")
	tr.insertOrders(t, 3, nil)

	err := tr.repo.Clear(tr.ctx)
	require.NoError(t, err)

	count, err := tr.repo.Count(tr.ctx, mink.Query{})
	require.NoError(t, err)
	assert.Equal(t, int64(0), count)
}

func TestPostgresRepository_Transaction(t *testing.T) {
	tr := setupOrderSummaryRepo(t, "order_tx_test")

	t.Run("commit transaction", func(t *testing.T) {
		tx, err := tr.db.BeginTx(tr.ctx, nil)
		require.NoError(t, err)

		txRepo := tr.repo.WithTx(tx)
		order := tr.newOrder("tx-order-1", "cust-1", "pending", 0, 0)
		err = txRepo.Insert(tr.ctx, order)
		require.NoError(t, err)

		err = tx.Commit()
		require.NoError(t, err)

		retrieved, err := tr.repo.Get(tr.ctx, "tx-order-1")
		require.NoError(t, err)
		assert.Equal(t, "pending", retrieved.Status)
	})

	t.Run("rollback transaction", func(t *testing.T) {
		tx, err := tr.db.BeginTx(tr.ctx, nil)
		require.NoError(t, err)

		txRepo := tr.repo.WithTx(tx)
		order := tr.newOrder("tx-order-2", "cust-1", "pending", 0, 0)
		err = txRepo.Insert(tr.ctx, order)
		require.NoError(t, err)

		err = tx.Rollback()
		require.NoError(t, err)

		_, err = tr.repo.Get(tr.ctx, "tx-order-2")
		assert.ErrorIs(t, err, mink.ErrNotFound)
	})

	t.Run("transaction upsert", func(t *testing.T) {
		tx, err := tr.db.BeginTx(tr.ctx, nil)
		require.NoError(t, err)

		txRepo := tr.repo.WithTx(tx)
		order := tr.newOrder("tx-order-1", "cust-1", "shipped", 0, 0)
		err = txRepo.Upsert(tr.ctx, order)
		require.NoError(t, err)

		err = tx.Commit()
		require.NoError(t, err)

		retrieved, err := tr.repo.Get(tr.ctx, "tx-order-1")
		require.NoError(t, err)
		assert.Equal(t, "shipped", retrieved.Status)
	})

	t.Run("transaction Find", func(t *testing.T) {
		tx, err := tr.db.BeginTx(tr.ctx, nil)
		require.NoError(t, err)
		defer func() { _ = tx.Rollback() }()

		txRepo := tr.repo.WithTx(tx)
		query := mink.NewQuery().Where("status", mink.FilterOpEq, "shipped")
		results, err := txRepo.Find(tr.ctx, query.Build())
		require.NoError(t, err)
		assert.Len(t, results, 1)
		assert.Equal(t, "tx-order-1", results[0].OrderID)
	})

	t.Run("transaction FindOne", func(t *testing.T) {
		tx, err := tr.db.BeginTx(tr.ctx, nil)
		require.NoError(t, err)
		defer func() { _ = tx.Rollback() }()

		txRepo := tr.repo.WithTx(tx)
		query := mink.NewQuery().Where("status", mink.FilterOpEq, "shipped")
		result, err := txRepo.FindOne(tr.ctx, query.Build())
		require.NoError(t, err)
		assert.Equal(t, "tx-order-1", result.OrderID)
	})

	t.Run("transaction GetMany", func(t *testing.T) {
		tx, err := tr.db.BeginTx(tr.ctx, nil)
		require.NoError(t, err)
		defer func() { _ = tx.Rollback() }()

		txRepo := tr.repo.WithTx(tx)
		results, err := txRepo.GetMany(tr.ctx, []string{"tx-order-1"})
		require.NoError(t, err)
		assert.Len(t, results, 1)
	})

	t.Run("transaction Count", func(t *testing.T) {
		tx, err := tr.db.BeginTx(tr.ctx, nil)
		require.NoError(t, err)
		defer func() { _ = tx.Rollback() }()

		txRepo := tr.repo.WithTx(tx)
		count, err := txRepo.Count(tr.ctx, mink.Query{})
		require.NoError(t, err)
		assert.Equal(t, int64(1), count)
	})

	t.Run("transaction DeleteMany", func(t *testing.T) {
		// Insert some orders to delete
		tx, err := tr.db.BeginTx(tr.ctx, nil)
		require.NoError(t, err)

		txRepo := tr.repo.WithTx(tx)
		for i := 10; i <= 12; i++ {
			order := tr.newOrder(fmt.Sprintf("tx-order-%d", i), "cust-1", "to-delete", i, float64(i)*10)
			require.NoError(t, txRepo.Insert(tr.ctx, order))
		}
		require.NoError(t, tx.Commit())

		// Now delete in transaction
		tx, err = tr.db.BeginTx(tr.ctx, nil)
		require.NoError(t, err)

		txRepo = tr.repo.WithTx(tx)
		query := mink.NewQuery().Where("status", mink.FilterOpEq, "to-delete")
		deleted, err := txRepo.DeleteMany(tr.ctx, query.Build())
		require.NoError(t, err)
		assert.Equal(t, int64(3), deleted)

		require.NoError(t, tx.Commit())
	})

	t.Run("transaction Clear", func(t *testing.T) {
		// Insert some orders to clear
		for i := 20; i <= 22; i++ {
			order := tr.newOrder(fmt.Sprintf("tx-order-%d", i), "cust-1", "to-clear", i, float64(i)*10)
			require.NoError(t, tr.repo.Insert(tr.ctx, order))
		}

		tx, err := tr.db.BeginTx(tr.ctx, nil)
		require.NoError(t, err)

		txRepo := tr.repo.WithTx(tx)
		err = txRepo.Clear(tr.ctx)
		require.NoError(t, err)

		require.NoError(t, tx.Commit())

		count, err := tr.repo.Count(tr.ctx, mink.Query{})
		require.NoError(t, err)
		assert.Equal(t, int64(0), count)
	})

	t.Run("transaction Exists", func(t *testing.T) {
		// Insert an order to check
		order := tr.newOrder("tx-exists-order", "cust-1", "pending", 1, 10.0)
		require.NoError(t, tr.repo.Insert(tr.ctx, order))

		tx, err := tr.db.BeginTx(tr.ctx, nil)
		require.NoError(t, err)
		defer func() { _ = tx.Rollback() }()

		txRepo := tr.repo.WithTx(tx)

		exists, err := txRepo.Exists(tr.ctx, "tx-exists-order")
		require.NoError(t, err)
		assert.True(t, exists)

		exists, err = txRepo.Exists(tr.ctx, "non-existent")
		require.NoError(t, err)
		assert.False(t, exists)
	})

	t.Run("transaction GetAll", func(t *testing.T) {
		// Clear and insert fresh data
		require.NoError(t, tr.repo.Clear(tr.ctx))
		for i := 1; i <= 3; i++ {
			order := tr.newOrder(fmt.Sprintf("tx-getall-%d", i), "cust-1", "pending", i, float64(i)*10)
			require.NoError(t, tr.repo.Insert(tr.ctx, order))
		}

		tx, err := tr.db.BeginTx(tr.ctx, nil)
		require.NoError(t, err)
		defer func() { _ = tx.Rollback() }()

		txRepo := tr.repo.WithTx(tx)
		results, err := txRepo.GetAll(tr.ctx)
		require.NoError(t, err)
		assert.Len(t, results, 3)
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
		defer func() { _ = repo.DropTable(context.Background()) }()

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

func TestPostgresRepository_NullableFields(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	db := getTestDB(t)
	defer db.Close()

	schema := createReadModelTestSchema(t, db)

	repo, err := NewPostgresRepository[ProductView](db,
		WithReadModelSchema(schema),
		WithTableName("products_nullable_test"),
	)
	require.NoError(t, err)
	defer func() { _ = repo.DropTable(context.Background()) }()

	ctx := context.Background()

	t.Run("insert with nullable field empty", func(t *testing.T) {
		product := &ProductView{
			SKU:         "SKU-001",
			Name:        "Test Product",
			Description: "", // Empty string for nullable field
			Price:       29.99,
			StockCount:  100,
			Active:      true,
		}

		err := repo.Insert(ctx, product)
		require.NoError(t, err)

		retrieved, err := repo.Get(ctx, "SKU-001")
		require.NoError(t, err)
		assert.Equal(t, "", retrieved.Description)
		assert.Equal(t, "Test Product", retrieved.Name)
	})

	t.Run("insert with nullable field set", func(t *testing.T) {
		product := &ProductView{
			SKU:         "SKU-002",
			Name:        "Another Product",
			Description: "This is a detailed description",
			Price:       49.99,
			StockCount:  50,
			Active:      false,
		}

		err := repo.Insert(ctx, product)
		require.NoError(t, err)

		retrieved, err := repo.Get(ctx, "SKU-002")
		require.NoError(t, err)
		assert.Equal(t, "This is a detailed description", retrieved.Description)
		assert.False(t, retrieved.Active)
	})

	t.Run("update nullable field to empty", func(t *testing.T) {
		err := repo.Update(ctx, "SKU-002", func(p *ProductView) {
			p.Description = ""
		})
		require.NoError(t, err)

		retrieved, err := repo.Get(ctx, "SKU-002")
		require.NoError(t, err)
		assert.Equal(t, "", retrieved.Description)
	})
}

func TestPostgresRepository_CustomerStats(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	db := getTestDB(t)
	defer db.Close()

	schema := createReadModelTestSchema(t, db)

	repo, err := NewPostgresRepository[CustomerStats](db,
		WithReadModelSchema(schema),
		WithTableName("customer_stats_test"),
	)
	require.NoError(t, err)
	defer func() { _ = repo.DropTable(context.Background()) }()

	ctx := context.Background()
	now := time.Now().Truncate(time.Microsecond)

	t.Run("insert and retrieve with all types", func(t *testing.T) {
		stats := &CustomerStats{
			CustomerID:  "cust-001",
			OrderCount:  42,
			TotalSpent:  1234.56,
			LastOrderAt: now,
			IsVIP:       true,
			Tags:        []byte(`{"tags":["premium","loyal"]}`),
		}

		err := repo.Insert(ctx, stats)
		require.NoError(t, err)

		retrieved, err := repo.Get(ctx, "cust-001")
		require.NoError(t, err)
		assert.Equal(t, int64(42), retrieved.OrderCount)
		assert.InDelta(t, 1234.56, retrieved.TotalSpent, 0.01)
		assert.True(t, retrieved.IsVIP)
		assert.Equal(t, []byte(`{"tags":["premium","loyal"]}`), retrieved.Tags)
	})

	t.Run("update stats", func(t *testing.T) {
		err := repo.Update(ctx, "cust-001", func(s *CustomerStats) {
			s.OrderCount = 43
			s.TotalSpent = 1334.56
		})
		require.NoError(t, err)

		retrieved, err := repo.Get(ctx, "cust-001")
		require.NoError(t, err)
		assert.Equal(t, int64(43), retrieved.OrderCount)
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
	defer func() { _ = repo.DropTable(context.Background()) }()

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
		{"OrderID", "order_id"},
		{"CustomerID", "customer_id"},
		{"orderID", "order_id"},
		{"Simple", "simple"},
		{"CamelCase", "camel_case"},
		{"HTTPServer", "http_server"},
		{"ID", "id"},
		{"id", "id"},
		{"XMLParser", "xml_parser"},
		{"getHTTPResponse", "get_http_response"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := toSnakeCase(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestSafeIndexName(t *testing.T) {
	tests := []struct {
		name      string
		schema    string
		indexName string
		wantLen   int // 0 means check exact match, >0 means check max length
		wantExact string
	}{
		{
			name:      "short name unchanged",
			schema:    "public",
			indexName: "idx_orders_customer_id",
			wantExact: "public_idx_orders_customer_id",
		},
		{
			name:      "exactly 63 chars unchanged",
			schema:    "schema",
			indexName: "idx_" + strings.Repeat("a", 52), // 6 + 1 + 4 + 52 = 63
			wantLen:   63,
		},
		{
			name:      "long name truncated with hash",
			schema:    "test_readmodel_1768082982493819337",
			indexName: "idx_order_crud_test_customer_id",
			wantLen:   63,
		},
		{
			name:      "very long schema",
			schema:    strings.Repeat("a", 50),
			indexName: "idx_table_column",
			wantLen:   63,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := safeIndexName(tt.schema, tt.indexName)
			if tt.wantExact != "" {
				assert.Equal(t, tt.wantExact, result)
			}
			if tt.wantLen > 0 {
				assert.LessOrEqual(t, len(result), tt.wantLen, "index name %q exceeds %d chars (%d)", result, tt.wantLen, len(result))
			}
			// All results should be <= 63 chars
			assert.LessOrEqual(t, len(result), 63, "index name %q exceeds 63 chars", result)
		})
	}

	// Test uniqueness: different long names should produce different results
	t.Run("uniqueness", func(t *testing.T) {
		name1 := safeIndexName("test_readmodel_1768082982493819337", "idx_order_crud_test_customer_id")
		name2 := safeIndexName("test_readmodel_1768082982493819337", "idx_order_crud_test_order_id")
		assert.NotEqual(t, name1, name2, "different index names should produce different results")
	})
}

func TestValidateSQLLiteral(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		context string
		wantErr bool
	}{
		{"valid string literal", "'test'", "default", false},
		{"valid number", "0", "default", false},
		{"valid type", "VARCHAR(100)", "type", false},
		{"valid timestamp", "NOW()", "default", false},
		{"semicolon injection", "'; DROP TABLE users;--", "default", true},
		{"comment injection", "1/**/OR/**/1=1", "default", true},
		{"double dash comment", "1--comment", "default", true},
		{"drop keyword", "DROP TABLE users", "type", true},
		{"alter keyword", "ALTER TABLE users", "type", true},
		{"backslash", "test\\x00", "default", true},
		// Word boundary tests - should NOT reject words that contain keywords as substrings
		{"dropbox allowed", "dropbox", "default", false},
		{"update_at allowed", "update_at", "default", false},
		{"created_at allowed", "created_at", "default", false},
		{"backdrop allowed", "backdrop", "default", false},
		// But should reject actual keywords
		{"drop standalone", "drop", "default", true},
		{"update standalone", "update", "default", true},
		{"drop with space", "drop users", "default", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateSQLLiteral(tt.value, tt.context)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
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
		{"int", int(0), "INTEGER"},
		{"int32", int32(0), "INTEGER"},
		{"int64", int64(0), "BIGINT"},
		{"int8", int8(0), "SMALLINT"},
		{"int16", int16(0), "SMALLINT"},
		{"uint", uint(0), "INTEGER"},
		{"uint32", uint32(0), "INTEGER"},
		{"uint64", uint64(0), "BIGINT"},
		{"float32", float32(0), "REAL"},
		{"float64", float64(0), "DOUBLE PRECISION"},
		{"bool", false, "BOOLEAN"},
		{"time", time.Time{}, "TIMESTAMPTZ"},
		{"bytes", []byte{}, "BYTEA"},
		{"string_slice", []string{}, "JSONB"},
		{"map", map[string]interface{}{}, "JSONB"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := goTypeToSQL(reflect.TypeOf(tt.goType))
			assert.Equal(t, tt.expected, result)
		})
	}
}

// Benchmark tests

// setupBenchmarkRepo creates a repository for benchmark tests.
func setupBenchmarkRepo(b *testing.B, tableName string) (*PostgresRepository[OrderSummary], func()) {
	connStr := os.Getenv("TEST_DATABASE_URL")
	if connStr == "" {
		b.Skip("TEST_DATABASE_URL not set")
	}
	db, err := sql.Open("pgx", connStr)
	if err != nil {
		b.Fatal(err)
	}
	schema := fmt.Sprintf("bench_%d", time.Now().UnixNano())
	_, _ = db.Exec(fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %s", quoteIdentifier(schema)))
	repo, err := NewPostgresRepository[OrderSummary](db, WithReadModelSchema(schema), WithTableName(tableName))
	if err != nil {
		b.Fatal(err)
	}
	cleanup := func() {
		_, _ = db.Exec(fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", quoteIdentifier(schema)))
		db.Close()
	}
	return repo, cleanup
}

func BenchmarkPostgresRepository_Insert(b *testing.B) {
	repo, cleanup := setupBenchmarkRepo(b, "bench_orders")
	defer cleanup()

	ctx := context.Background()
	now := time.Now()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		order := &OrderSummary{
			OrderID: fmt.Sprintf("bench-order-%d", i), CustomerID: "cust-1", Status: "pending",
			ItemCount: 1, TotalAmount: 99.99, CreatedAt: now, UpdatedAt: now,
		}
		_ = repo.Insert(ctx, order)
	}
}

func BenchmarkPostgresRepository_Get(b *testing.B) {
	repo, cleanup := setupBenchmarkRepo(b, "bench_orders_get")
	defer cleanup()

	ctx := context.Background()
	now := time.Now()
	// Setup: insert test data (check error to ensure benchmark is measuring actual DB operations)
	if err := repo.Insert(ctx, &OrderSummary{
		OrderID: "bench-order-1", CustomerID: "cust-1", Status: "pending",
		ItemCount: 1, TotalAmount: 99.99, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		b.Fatalf("setup failed: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = repo.Get(ctx, "bench-order-1")
	}
}
