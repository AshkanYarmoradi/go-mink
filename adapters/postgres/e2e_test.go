package postgres_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/AshkanYarmoradi/go-mink"
	"github.com/AshkanYarmoradi/go-mink/adapters/postgres"
	"github.com/AshkanYarmoradi/go-mink/testing/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "github.com/jackc/pgx/v5/stdlib" // PostgreSQL driver
)

// getTestDatabaseURL returns the test database URL or skips the test.
func getTestDatabaseURL(t *testing.T) string {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set, skipping PostgreSQL E2E test")
	}
	return url
}

// =============================================================================
// E2E Test: Complete Order Lifecycle with PostgreSQL Adapter
// =============================================================================

func TestE2E_CompleteOrderLifecycle_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping PostgreSQL E2E test in short mode")
	}

	connStr := getTestDatabaseURL(t)

	// Arrange: Setup event store with PostgreSQL adapter
	adapter, err := postgres.NewAdapter(connStr)
	require.NoError(t, err)
	defer adapter.Close()

	err = adapter.Initialize(context.Background())
	require.NoError(t, err)

	store := mink.New(adapter)
	testutil.RegisterTestEvents(store)
	ctx := context.Background()

	// Use unique order ID to avoid conflicts with other test runs
	orderID := fmt.Sprintf("pg-order-%d", time.Now().UnixNano())

	// Act: Create order through its lifecycle
	order := testutil.NewOrder(orderID)

	// Step 1: Create order
	err = order.Create("pg-customer-456")
	require.NoError(t, err)

	// Step 2: Add items
	err = order.AddItem("PG-SKU-001", 2, 29.99)
	require.NoError(t, err)
	err = order.AddItem("PG-SKU-002", 1, 49.99)
	require.NoError(t, err)

	// Step 3: Save aggregate
	err = store.SaveAggregate(ctx, order)
	require.NoError(t, err)

	// Step 4: Load aggregate in new instance
	loadedOrder := testutil.NewOrder(orderID)
	err = store.LoadAggregate(ctx, loadedOrder)
	require.NoError(t, err)

	// Assert: Loaded order should have version 3 (3 events applied during load)
	assert.Equal(t, "pg-customer-456", loadedOrder.CustomerID)
	assert.Equal(t, "Created", loadedOrder.Status)
	assert.Len(t, loadedOrder.Items, 2)
	assert.Equal(t, int64(3), loadedOrder.Version())

	// Step 5: Ship the order
	err = loadedOrder.Ship("PG-TRACK-789")
	require.NoError(t, err)
	err = store.SaveAggregate(ctx, loadedOrder)
	require.NoError(t, err)

	// Step 6: Reload to verify shipped state
	reloadedOrder := testutil.NewOrder(orderID)
	err = store.LoadAggregate(ctx, reloadedOrder)
	require.NoError(t, err)

	// Assert: Order is now shipped with version 4
	assert.Equal(t, "Shipped", reloadedOrder.Status)
	assert.Equal(t, int64(4), reloadedOrder.Version())

	// Step 7: Verify final state
	finalOrder := testutil.NewOrder(orderID)
	err = store.LoadAggregate(ctx, finalOrder)
	require.NoError(t, err)
	assert.Equal(t, "Shipped", finalOrder.Status)
	assert.Equal(t, "PG-TRACK-789", finalOrder.TrackingNumber)
	assert.Equal(t, int64(4), finalOrder.Version())
}

// =============================================================================
// E2E Test: Concurrent Modifications with PostgreSQL
// =============================================================================

func TestE2E_ConcurrentModification_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping PostgreSQL E2E test in short mode")
	}

	connStr := getTestDatabaseURL(t)

	adapter, err := postgres.NewAdapter(connStr)
	require.NoError(t, err)
	defer adapter.Close()

	err = adapter.Initialize(context.Background())
	require.NoError(t, err)

	store := mink.New(adapter)
	testutil.RegisterTestEvents(store)
	ctx := context.Background()

	// Use unique order ID
	orderID := fmt.Sprintf("pg-concurrent-%d", time.Now().UnixNano())

	// Create initial order
	order := testutil.NewOrder(orderID)
	err = order.Create("customer-1")
	require.NoError(t, err)
	err = store.SaveAggregate(ctx, order)
	require.NoError(t, err)

	// Simulate concurrent modifications
	var wg sync.WaitGroup
	conflictCount := 0
	successCount := 0
	var mu sync.Mutex

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(itemNum int) {
			defer wg.Done()

			// Each goroutine loads the order
			localOrder := testutil.NewOrder(orderID)
			if err := store.LoadAggregate(ctx, localOrder); err != nil {
				return
			}

			// Try to add an item
			sku := fmt.Sprintf("PG-SKU-%03d", itemNum)
			if err := localOrder.AddItem(sku, 1, 10.0); err != nil {
				return
			}

			// Try to save - some will fail due to conflicts
			err := store.SaveAggregate(ctx, localOrder)
			mu.Lock()
			if errors.Is(err, mink.ErrConcurrencyConflict) {
				conflictCount++
			} else if err == nil {
				successCount++
			}
			mu.Unlock()
		}(i)
	}

	wg.Wait()

	// At least one should succeed
	assert.Greater(t, successCount, 0, "At least one concurrent write should succeed")
	t.Logf("PostgreSQL concurrent test: %d successes, %d conflicts", successCount, conflictCount)

	// Verify final state is consistent
	finalOrder := testutil.NewOrder(orderID)
	err = store.LoadAggregate(ctx, finalOrder)
	require.NoError(t, err)
	assert.Equal(t, int64(1+successCount), finalOrder.Version())
}

// =============================================================================
// E2E Test: Read Model Projection with PostgreSQL
// =============================================================================

func TestE2E_ReadModelProjection_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping PostgreSQL E2E test in short mode")
	}

	connStr := getTestDatabaseURL(t)

	adapter, err := postgres.NewAdapter(connStr)
	require.NoError(t, err)
	defer adapter.Close()

	err = adapter.Initialize(context.Background())
	require.NoError(t, err)

	store := mink.New(adapter)
	testutil.RegisterTestEvents(store)
	ctx := context.Background()

	// Create read model
	readModel := testutil.NewOrderReadModel()

	// Create multiple orders with unique IDs
	baseID := time.Now().UnixNano()
	orderIDs := make([]string, 3)
	for i := 1; i <= 3; i++ {
		orderID := fmt.Sprintf("pg-proj-%d-%d", baseID, i)
		orderIDs[i-1] = orderID

		order := testutil.NewOrder(orderID)
		err := order.Create(fmt.Sprintf("pg-customer-%d", i))
		require.NoError(t, err)
		for j := 1; j <= i; j++ { // Order i has i items
			err = order.AddItem(fmt.Sprintf("PG-SKU-%d-%d", i, j), j, 10.0)
			require.NoError(t, err)
		}
		err = store.SaveAggregate(ctx, order)
		require.NoError(t, err)
	}

	// Load all events and project to read model
	for _, orderID := range orderIDs {
		streamID := fmt.Sprintf("Order-%s", orderID)
		events, err := store.LoadRaw(ctx, streamID, 0)
		require.NoError(t, err)

		for _, event := range events {
			err = readModel.Apply(event)
			require.NoError(t, err)
		}
	}

	// Verify projections
	assert.Equal(t, 3, readModel.Count())
}

// =============================================================================
// E2E Test: Event Metadata Flow with PostgreSQL
// =============================================================================

func TestE2E_EventMetadata_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping PostgreSQL E2E test in short mode")
	}

	connStr := getTestDatabaseURL(t)

	adapter, err := postgres.NewAdapter(connStr)
	require.NoError(t, err)
	defer adapter.Close()

	err = adapter.Initialize(context.Background())
	require.NoError(t, err)

	store := mink.New(adapter)
	testutil.RegisterTestEvents(store)
	ctx := context.Background()

	// Use unique order ID
	orderID := fmt.Sprintf("pg-meta-%d", time.Now().UnixNano())

	// Create order
	order := testutil.NewOrder(orderID)
	err = order.Create("pg-customer-meta")
	require.NoError(t, err)
	err = order.AddItem("PG-META-SKU", 1, 50.0)
	require.NoError(t, err)

	// Save aggregate
	err = store.SaveAggregate(ctx, order)
	require.NoError(t, err)

	// Load events and verify
	events, err := store.LoadRaw(ctx, fmt.Sprintf("Order-%s", orderID), 0)
	require.NoError(t, err)
	require.Len(t, events, 2)

	assert.Equal(t, "OrderCreated", events[0].Type)
	assert.Equal(t, "ItemAdded", events[1].Type)
}

// =============================================================================
// E2E Test: Large Event Stream with PostgreSQL
// =============================================================================

func TestE2E_LargeEventStream_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping PostgreSQL large event stream test in short mode")
	}

	connStr := getTestDatabaseURL(t)

	adapter, err := postgres.NewAdapter(connStr)
	require.NoError(t, err)
	defer adapter.Close()

	err = adapter.Initialize(context.Background())
	require.NoError(t, err)

	store := mink.New(adapter)
	testutil.RegisterTestEvents(store)
	ctx := context.Background()

	// Use unique order ID
	orderID := fmt.Sprintf("pg-large-%d", time.Now().UnixNano())

	// Create order with many items
	order := testutil.NewOrder(orderID)
	err = order.Create("pg-customer-large")
	require.NoError(t, err)
	err = store.SaveAggregate(ctx, order)
	require.NoError(t, err)

	// Add 50 items in batches (smaller than memory test for performance)
	for batch := 0; batch < 5; batch++ {
		loadedOrder := testutil.NewOrder(orderID)
		err = store.LoadAggregate(ctx, loadedOrder)
		require.NoError(t, err)

		for i := 0; i < 10; i++ {
			itemNum := batch*10 + i
			err = loadedOrder.AddItem(fmt.Sprintf("PG-SKU-%03d", itemNum), 1, float64(itemNum))
			require.NoError(t, err)
		}
		err = store.SaveAggregate(ctx, loadedOrder)
		require.NoError(t, err)
	}

	// Verify final state
	finalOrder := testutil.NewOrder(orderID)
	start := time.Now()
	err = store.LoadAggregate(ctx, finalOrder)
	loadTime := time.Since(start)
	require.NoError(t, err)

	assert.Len(t, finalOrder.Items, 50)
	assert.Equal(t, int64(51), finalOrder.Version()) // 1 create + 50 items
	t.Logf("PostgreSQL: Loaded 51 events in %v", loadTime)
}

// =============================================================================
// E2E Test: Stream Operations with PostgreSQL
// =============================================================================

func TestE2E_StreamOperations_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping PostgreSQL E2E test in short mode")
	}

	connStr := getTestDatabaseURL(t)

	adapter, err := postgres.NewAdapter(connStr)
	require.NoError(t, err)
	defer adapter.Close()

	err = adapter.Initialize(context.Background())
	require.NoError(t, err)

	store := mink.New(adapter)
	testutil.RegisterTestEvents(store)
	ctx := context.Background()

	// Use unique order ID
	orderID := fmt.Sprintf("pg-stream-%d", time.Now().UnixNano())

	// Create an order
	order := testutil.NewOrder(orderID)
	err = order.Create("pg-stream-customer")
	require.NoError(t, err)
	err = order.AddItem("PG-STREAM-SKU", 2, 25.0)
	require.NoError(t, err)
	err = store.SaveAggregate(ctx, order)
	require.NoError(t, err)

	streamID := fmt.Sprintf("Order-%s", orderID)

	// Test GetStreamInfo
	info, err := store.GetStreamInfo(ctx, streamID)
	require.NoError(t, err)
	assert.Equal(t, int64(2), info.Version)
	assert.Equal(t, int64(2), info.EventCount)

	// Test non-existent stream
	nonExistentID := fmt.Sprintf("Order-nonexistent-%d", time.Now().UnixNano())
	_, err = store.GetStreamInfo(ctx, nonExistentID)
	assert.Error(t, err) // Should return error for non-existent stream
}

// =============================================================================
// E2E Test: Error Recovery with PostgreSQL
// =============================================================================

func TestE2E_ErrorRecovery_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping PostgreSQL E2E test in short mode")
	}

	connStr := getTestDatabaseURL(t)

	adapter, err := postgres.NewAdapter(connStr)
	require.NoError(t, err)
	defer adapter.Close()

	err = adapter.Initialize(context.Background())
	require.NoError(t, err)

	store := mink.New(adapter)
	testutil.RegisterTestEvents(store)
	ctx := context.Background()

	// Use unique order ID
	orderID := fmt.Sprintf("pg-recovery-%d", time.Now().UnixNano())

	// Create and save an order
	order := testutil.NewOrder(orderID)
	err = order.Create("pg-customer")
	require.NoError(t, err)
	err = order.AddItem("PG-ITEM", 1, 10.0)
	require.NoError(t, err)
	err = store.SaveAggregate(ctx, order)
	require.NoError(t, err)

	// Reload order to get correct version for next operation
	order = testutil.NewOrder(orderID)
	err = store.LoadAggregate(ctx, order)
	require.NoError(t, err)

	// Ship the order
	err = order.Ship("PG-TRACK-001")
	require.NoError(t, err)
	err = store.SaveAggregate(ctx, order)
	require.NoError(t, err)

	// Reload to test cancel
	order = testutil.NewOrder(orderID)
	err = store.LoadAggregate(ctx, order)
	require.NoError(t, err)

	// Try to cancel shipped order - should fail at domain level
	err = order.Cancel("Change of mind")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cannot cancel shipped order")

	// Verify no uncommitted events from failed operation
	assert.Empty(t, order.UncommittedEvents())

	// Verify order state is still "Shipped"
	loadedOrder := testutil.NewOrder(orderID)
	err = store.LoadAggregate(ctx, loadedOrder)
	require.NoError(t, err)
	assert.Equal(t, "Shipped", loadedOrder.Status)
}

// =============================================================================
// E2E Test: Multiple Aggregates with PostgreSQL
// =============================================================================

func TestE2E_MultipleAggregates_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping PostgreSQL E2E test in short mode")
	}

	connStr := getTestDatabaseURL(t)

	adapter, err := postgres.NewAdapter(connStr)
	require.NoError(t, err)
	defer adapter.Close()

	err = adapter.Initialize(context.Background())
	require.NoError(t, err)

	store := mink.New(adapter)
	testutil.RegisterTestEvents(store)
	ctx := context.Background()

	// Use unique base ID for this test
	baseID := time.Now().UnixNano()

	// Create multiple orders
	orderIDs := []string{
		fmt.Sprintf("pg-multi-1-%d", baseID),
		fmt.Sprintf("pg-multi-2-%d", baseID),
		fmt.Sprintf("pg-multi-3-%d", baseID),
	}
	for _, id := range orderIDs {
		order := testutil.NewOrder(id)
		err := order.Create("pg-shared-customer")
		require.NoError(t, err)
		err = order.AddItem("PG-WIDGET", 1, 25.0)
		require.NoError(t, err)
		err = store.SaveAggregate(ctx, order)
		require.NoError(t, err)
	}

	// Ship only the first order
	order1 := testutil.NewOrder(orderIDs[0])
	err = store.LoadAggregate(ctx, order1)
	require.NoError(t, err)
	err = order1.Ship("PG-TRACK-001")
	require.NoError(t, err)
	err = store.SaveAggregate(ctx, order1)
	require.NoError(t, err)

	// Cancel the second order
	order2 := testutil.NewOrder(orderIDs[1])
	err = store.LoadAggregate(ctx, order2)
	require.NoError(t, err)
	err = order2.Cancel("Customer request")
	require.NoError(t, err)
	err = store.SaveAggregate(ctx, order2)
	require.NoError(t, err)

	// Verify all orders have correct final state
	verifyOrder1 := testutil.NewOrder(orderIDs[0])
	err = store.LoadAggregate(ctx, verifyOrder1)
	require.NoError(t, err)
	assert.Equal(t, "Shipped", verifyOrder1.Status)

	verifyOrder2 := testutil.NewOrder(orderIDs[1])
	err = store.LoadAggregate(ctx, verifyOrder2)
	require.NoError(t, err)
	assert.Equal(t, "Cancelled", verifyOrder2.Status)

	verifyOrder3 := testutil.NewOrder(orderIDs[2])
	err = store.LoadAggregate(ctx, verifyOrder3)
	require.NoError(t, err)
	assert.Equal(t, "Created", verifyOrder3.Status)
}

// =============================================================================
// E2E Test: Complete Read Model Repository Flow
// =============================================================================

// OrderReadModel represents a projected read model for orders.
type OrderReadModel struct {
	OrderID     string     `mink:"order_id,pk"`
	CustomerID  string     `mink:"customer_id,index"`
	Status      string     `mink:"status,index"`
	ItemCount   int        `mink:"item_count"`
	TotalAmount float64    `mink:"total_amount"`
	CreatedAt   time.Time  `mink:"created_at"`
	UpdatedAt   time.Time  `mink:"updated_at"`
	ShippedAt   *time.Time `mink:"shipped_at,nullable"`
}

// CustomerSummary represents a customer's aggregated statistics.
type CustomerSummary struct {
	CustomerID  string     `mink:"customer_id,pk"`
	Name        string     `mink:"name"`
	OrderCount  int        `mink:"order_count"`
	TotalSpent  float64    `mink:"total_spent"`
	LastOrderAt *time.Time `mink:"last_order_at,nullable"`
	IsVIP       bool       `mink:"is_vip"`
	Tags        []byte     `mink:"tags,nullable"` // JSONB - made nullable for simpler test
	JoinedAt    time.Time  `mink:"joined_at"`
}

func TestE2E_ReadModelRepository_CompleteFlow(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping PostgreSQL E2E test in short mode")
	}

	connStr := getTestDatabaseURL(t)

	// Setup: Create database connection
	db, err := openTestDB(connStr)
	require.NoError(t, err)
	defer db.Close()

	// Create unique schema for this test run
	schema := fmt.Sprintf("e2e_readmodel_%d", time.Now().UnixNano())
	_, err = db.Exec(fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %s", quoteIdentifier(schema)))
	require.NoError(t, err)
	defer func() {
		_, _ = db.Exec(fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", quoteIdentifier(schema)))
	}()

	ctx := context.Background()
	now := time.Now().Truncate(time.Microsecond)

	// ==========================================================================
	// Phase 1: Repository Creation and Auto-Migration
	// ==========================================================================
	t.Run("Phase1_RepositoryCreation", func(t *testing.T) {
		// Create repository with auto-migration
		orderRepo, err := postgres.NewPostgresRepository[OrderReadModel](db,
			postgres.WithReadModelSchema(schema),
			postgres.WithTableName("orders"),
		)
		require.NoError(t, err)
		defer func() { _ = orderRepo.DropTable(ctx) }()

		// Verify table was created
		assert.Equal(t, schema, orderRepo.Schema())
		assert.Contains(t, orderRepo.TableName(), "orders")

		// Create customer repository
		customerRepo, err := postgres.NewPostgresRepository[CustomerSummary](db,
			postgres.WithReadModelSchema(schema),
			postgres.WithTableName("customers"),
		)
		require.NoError(t, err)
		defer func() { _ = customerRepo.DropTable(ctx) }()
	})

	// ==========================================================================
	// Phase 2: CRUD Operations
	// ==========================================================================
	t.Run("Phase2_CRUDOperations", func(t *testing.T) {
		orderRepo, err := postgres.NewPostgresRepository[OrderReadModel](db,
			postgres.WithReadModelSchema(schema),
			postgres.WithTableName("orders_crud"),
		)
		require.NoError(t, err)
		defer func() { _ = orderRepo.DropTable(ctx) }()

		// Insert multiple orders
		orders := []*OrderReadModel{
			{OrderID: "order-001", CustomerID: "cust-001", Status: "pending", ItemCount: 2, TotalAmount: 99.99, CreatedAt: now, UpdatedAt: now},
			{OrderID: "order-002", CustomerID: "cust-001", Status: "pending", ItemCount: 1, TotalAmount: 49.99, CreatedAt: now, UpdatedAt: now},
			{OrderID: "order-003", CustomerID: "cust-002", Status: "pending", ItemCount: 5, TotalAmount: 249.99, CreatedAt: now, UpdatedAt: now},
			{OrderID: "order-004", CustomerID: "cust-002", Status: "shipped", ItemCount: 3, TotalAmount: 149.99, CreatedAt: now, UpdatedAt: now},
			{OrderID: "order-005", CustomerID: "cust-003", Status: "delivered", ItemCount: 1, TotalAmount: 29.99, CreatedAt: now, UpdatedAt: now},
		}

		for _, order := range orders {
			err := orderRepo.Insert(ctx, order)
			require.NoError(t, err, "Failed to insert order %s", order.OrderID)
		}

		// Get single order
		retrieved, err := orderRepo.Get(ctx, "order-001")
		require.NoError(t, err)
		assert.Equal(t, "cust-001", retrieved.CustomerID)
		assert.Equal(t, 2, retrieved.ItemCount)

		// GetMany
		many, err := orderRepo.GetMany(ctx, []string{"order-001", "order-003", "order-005"})
		require.NoError(t, err)
		assert.Len(t, many, 3)

		// Update
		err = orderRepo.Update(ctx, "order-001", func(o *OrderReadModel) {
			o.Status = "processing"
			o.UpdatedAt = time.Now()
		})
		require.NoError(t, err)

		updated, err := orderRepo.Get(ctx, "order-001")
		require.NoError(t, err)
		assert.Equal(t, "processing", updated.Status)

		// Upsert (update existing)
		orders[0].Status = "shipped"
		orders[0].ShippedAt = &now
		err = orderRepo.Upsert(ctx, orders[0])
		require.NoError(t, err)

		upserted, err := orderRepo.Get(ctx, "order-001")
		require.NoError(t, err)
		assert.Equal(t, "shipped", upserted.Status)

		// Upsert (create new)
		newOrder := &OrderReadModel{
			OrderID: "order-006", CustomerID: "cust-004", Status: "new",
			ItemCount: 1, TotalAmount: 19.99, CreatedAt: now, UpdatedAt: now,
		}
		err = orderRepo.Upsert(ctx, newOrder)
		require.NoError(t, err)

		created, err := orderRepo.Get(ctx, "order-006")
		require.NoError(t, err)
		assert.Equal(t, "new", created.Status)

		// Exists
		exists, err := orderRepo.Exists(ctx, "order-001")
		require.NoError(t, err)
		assert.True(t, exists)

		notExists, err := orderRepo.Exists(ctx, "non-existent")
		require.NoError(t, err)
		assert.False(t, notExists)

		// Delete
		err = orderRepo.Delete(ctx, "order-006")
		require.NoError(t, err)

		_, err = orderRepo.Get(ctx, "order-006")
		assert.ErrorIs(t, err, mink.ErrNotFound)
	})

	// ==========================================================================
	// Phase 3: Querying with Filters
	// ==========================================================================
	t.Run("Phase3_QueryingWithFilters", func(t *testing.T) {
		orderRepo, err := postgres.NewPostgresRepository[OrderReadModel](db,
			postgres.WithReadModelSchema(schema),
			postgres.WithTableName("orders_query"),
		)
		require.NoError(t, err)
		defer func() { _ = orderRepo.DropTable(ctx) }()

		// Insert test data
		testOrders := []*OrderReadModel{
			{OrderID: "q-001", CustomerID: "cust-A", Status: "pending", ItemCount: 1, TotalAmount: 50.00, CreatedAt: now.Add(-5 * time.Hour), UpdatedAt: now},
			{OrderID: "q-002", CustomerID: "cust-A", Status: "shipped", ItemCount: 2, TotalAmount: 100.00, CreatedAt: now.Add(-4 * time.Hour), UpdatedAt: now},
			{OrderID: "q-003", CustomerID: "cust-B", Status: "pending", ItemCount: 3, TotalAmount: 150.00, CreatedAt: now.Add(-3 * time.Hour), UpdatedAt: now},
			{OrderID: "q-004", CustomerID: "cust-B", Status: "delivered", ItemCount: 4, TotalAmount: 200.00, CreatedAt: now.Add(-2 * time.Hour), UpdatedAt: now},
			{OrderID: "q-005", CustomerID: "cust-C", Status: "cancelled", ItemCount: 5, TotalAmount: 250.00, CreatedAt: now.Add(-1 * time.Hour), UpdatedAt: now},
		}
		for _, o := range testOrders {
			require.NoError(t, orderRepo.Insert(ctx, o))
		}

		// Filter by equality
		query := mink.NewQuery().Where("status", mink.FilterOpEq, "pending")
		results, err := orderRepo.Find(ctx, query.Build())
		require.NoError(t, err)
		assert.Len(t, results, 2)

		// Filter by IN
		query = mink.NewQuery().Where("status", mink.FilterOpIn, []string{"pending", "shipped"})
		results, err = orderRepo.Find(ctx, query.Build())
		require.NoError(t, err)
		assert.Len(t, results, 3)

		// Filter by greater than
		query = mink.NewQuery().Where("total_amount", mink.FilterOpGt, 100.0)
		results, err = orderRepo.Find(ctx, query.Build())
		require.NoError(t, err)
		assert.Len(t, results, 3) // 150, 200, 250

		// Filter by less than or equal
		query = mink.NewQuery().Where("item_count", mink.FilterOpLte, 2)
		results, err = orderRepo.Find(ctx, query.Build())
		require.NoError(t, err)
		assert.Len(t, results, 2) // items 1 and 2

		// Multiple filters (AND)
		query = mink.NewQuery().
			Where("customer_id", mink.FilterOpEq, "cust-B").
			And("total_amount", mink.FilterOpGte, 150.0)
		results, err = orderRepo.Find(ctx, query.Build())
		require.NoError(t, err)
		assert.Len(t, results, 2)

		// Ordering
		query = mink.NewQuery().OrderByDesc("total_amount")
		results, err = orderRepo.Find(ctx, query.Build())
		require.NoError(t, err)
		require.Len(t, results, 5)
		assert.Equal(t, "q-005", results[0].OrderID) // Highest amount
		assert.Equal(t, "q-001", results[4].OrderID) // Lowest amount

		// Pagination
		query = mink.NewQuery().OrderByAsc("order_id").WithLimit(2).WithOffset(2)
		results, err = orderRepo.Find(ctx, query.Build())
		require.NoError(t, err)
		assert.Len(t, results, 2)
		assert.Equal(t, "q-003", results[0].OrderID)
		assert.Equal(t, "q-004", results[1].OrderID)

		// FindOne
		query = mink.NewQuery().Where("customer_id", mink.FilterOpEq, "cust-C")
		single, err := orderRepo.FindOne(ctx, query.Build())
		require.NoError(t, err)
		assert.Equal(t, "q-005", single.OrderID)

		// Count
		query = mink.NewQuery().Where("status", mink.FilterOpEq, "pending")
		count, err := orderRepo.Count(ctx, query.Build())
		require.NoError(t, err)
		assert.Equal(t, int64(2), count)

		// GetAll
		all, err := orderRepo.GetAll(ctx)
		require.NoError(t, err)
		assert.Len(t, all, 5)

		// DeleteMany
		query = mink.NewQuery().Where("status", mink.FilterOpEq, "cancelled")
		deleted, err := orderRepo.DeleteMany(ctx, query.Build())
		require.NoError(t, err)
		assert.Equal(t, int64(1), deleted)

		// Verify deletion
		count, err = orderRepo.Count(ctx, mink.Query{})
		require.NoError(t, err)
		assert.Equal(t, int64(4), count)
	})

	// ==========================================================================
	// Phase 4: Transaction Support
	// ==========================================================================
	t.Run("Phase4_TransactionSupport", func(t *testing.T) {
		orderRepo, err := postgres.NewPostgresRepository[OrderReadModel](db,
			postgres.WithReadModelSchema(schema),
			postgres.WithTableName("orders_tx"),
		)
		require.NoError(t, err)
		defer func() { _ = orderRepo.DropTable(ctx) }()

		customerRepo, err := postgres.NewPostgresRepository[CustomerSummary](db,
			postgres.WithReadModelSchema(schema),
			postgres.WithTableName("customers_tx"),
		)
		require.NoError(t, err)
		defer func() { _ = customerRepo.DropTable(ctx) }()

		// Test: Successful transaction with multiple repositories
		t.Run("commit_multi_repo_transaction", func(t *testing.T) {
			tx, err := db.BeginTx(ctx, nil)
			require.NoError(t, err)

			orderTx := orderRepo.WithTx(tx)
			customerTx := customerRepo.WithTx(tx)

			// Insert order
			order := &OrderReadModel{
				OrderID: "tx-001", CustomerID: "tx-cust-001", Status: "pending",
				ItemCount: 2, TotalAmount: 99.99, CreatedAt: now, UpdatedAt: now,
			}
			err = orderTx.Insert(ctx, order)
			require.NoError(t, err)

			// Insert/update customer stats
			customer := &CustomerSummary{
				CustomerID: "tx-cust-001", Name: "Transaction Test Customer",
				OrderCount: 1, TotalSpent: 99.99, JoinedAt: now,
			}
			err = customerTx.Insert(ctx, customer)
			require.NoError(t, err)

			// Commit
			err = tx.Commit()
			require.NoError(t, err)

			// Verify both were persisted
			savedOrder, err := orderRepo.Get(ctx, "tx-001")
			require.NoError(t, err)
			assert.Equal(t, "pending", savedOrder.Status)

			savedCustomer, err := customerRepo.Get(ctx, "tx-cust-001")
			require.NoError(t, err)
			assert.Equal(t, 1, savedCustomer.OrderCount)
		})

		// Test: Rollback transaction
		t.Run("rollback_transaction", func(t *testing.T) {
			tx, err := db.BeginTx(ctx, nil)
			require.NoError(t, err)

			orderTx := orderRepo.WithTx(tx)

			// Insert order
			order := &OrderReadModel{
				OrderID: "tx-rollback", CustomerID: "tx-cust-002", Status: "pending",
				ItemCount: 1, TotalAmount: 50.00, CreatedAt: now, UpdatedAt: now,
			}
			err = orderTx.Insert(ctx, order)
			require.NoError(t, err)

			// Verify it exists within transaction
			_, err = orderTx.Get(ctx, "tx-rollback")
			require.NoError(t, err)

			// Rollback
			err = tx.Rollback()
			require.NoError(t, err)

			// Verify it was NOT persisted
			_, err = orderRepo.Get(ctx, "tx-rollback")
			assert.ErrorIs(t, err, mink.ErrNotFound)
		})

		// Test: Transaction with all operations
		t.Run("transaction_all_operations", func(t *testing.T) {
			// Setup: insert initial data outside transaction
			initial := &OrderReadModel{
				OrderID: "tx-update", CustomerID: "tx-cust-003", Status: "pending",
				ItemCount: 1, TotalAmount: 25.00, CreatedAt: now, UpdatedAt: now,
			}
			require.NoError(t, orderRepo.Insert(ctx, initial))

			tx, err := db.BeginTx(ctx, nil)
			require.NoError(t, err)
			defer func() { _ = tx.Rollback() }()

			orderTx := orderRepo.WithTx(tx)

			// Update
			err = orderTx.Update(ctx, "tx-update", func(o *OrderReadModel) {
				o.Status = "shipped"
				o.ItemCount = 5
			})
			require.NoError(t, err)

			// Upsert new
			upsertOrder := &OrderReadModel{
				OrderID: "tx-upsert", CustomerID: "tx-cust-003", Status: "new",
				ItemCount: 2, TotalAmount: 40.00, CreatedAt: now, UpdatedAt: now,
			}
			err = orderTx.Upsert(ctx, upsertOrder)
			require.NoError(t, err)

			// Find within transaction
			query := mink.NewQuery().Where("customer_id", mink.FilterOpEq, "tx-cust-003")
			results, err := orderTx.Find(ctx, query.Build())
			require.NoError(t, err)
			assert.Len(t, results, 2)

			// Count within transaction
			count, err := orderTx.Count(ctx, query.Build())
			require.NoError(t, err)
			assert.Equal(t, int64(2), count)

			// GetMany within transaction
			many, err := orderTx.GetMany(ctx, []string{"tx-update", "tx-upsert"})
			require.NoError(t, err)
			assert.Len(t, many, 2)

			// FindOne within transaction
			single, err := orderTx.FindOne(ctx, mink.NewQuery().Where("status", mink.FilterOpEq, "shipped").Build())
			require.NoError(t, err)
			assert.Equal(t, "tx-update", single.OrderID)

			// Exists within transaction
			exists, err := orderTx.Exists(ctx, "tx-upsert")
			require.NoError(t, err)
			assert.True(t, exists)

			// GetAll within transaction
			all, err := orderTx.GetAll(ctx)
			require.NoError(t, err)
			assert.GreaterOrEqual(t, len(all), 2)

			// Delete
			err = orderTx.Delete(ctx, "tx-upsert")
			require.NoError(t, err)

			// DeleteMany would delete "tx-update", let's verify count again
			count, err = orderTx.Count(ctx, query.Build())
			require.NoError(t, err)
			assert.Equal(t, int64(1), count) // Only tx-update remains

			err = tx.Commit()
			require.NoError(t, err)

			// Verify final state
			final, err := orderRepo.Get(ctx, "tx-update")
			require.NoError(t, err)
			assert.Equal(t, "shipped", final.Status)
			assert.Equal(t, 5, final.ItemCount)
		})
	})

	// ==========================================================================
	// Phase 5: Schema Evolution (Migration)
	// ==========================================================================
	t.Run("Phase5_SchemaMigration", func(t *testing.T) {
		// Create table with minimal schema first
		tableName := "orders_evolve"
		tableQ := fmt.Sprintf(`"%s"."%s"`, schema, tableName)
		_, err := db.Exec(fmt.Sprintf(`
			CREATE TABLE %s (
				order_id TEXT PRIMARY KEY,
				status TEXT NOT NULL
			)
		`, tableQ))
		require.NoError(t, err)

		// Insert data with old schema
		_, err = db.Exec(fmt.Sprintf(`INSERT INTO %s (order_id, status) VALUES ('evolve-001', 'pending')`, tableQ))
		require.NoError(t, err)

		// Create repository with expanded model - should auto-migrate
		orderRepo, err := postgres.NewPostgresRepository[OrderReadModel](db,
			postgres.WithReadModelSchema(schema),
			postgres.WithTableName(tableName),
		)
		require.NoError(t, err)
		defer func() { _ = orderRepo.DropTable(ctx) }()

		// Verify old data is still accessible
		old, err := orderRepo.Get(ctx, "evolve-001")
		require.NoError(t, err)
		assert.Equal(t, "pending", old.Status)

		// Insert new data with all fields
		newOrder := &OrderReadModel{
			OrderID: "evolve-002", CustomerID: "cust-evolve", Status: "new",
			ItemCount: 3, TotalAmount: 75.00, CreatedAt: now, UpdatedAt: now,
		}
		err = orderRepo.Insert(ctx, newOrder)
		require.NoError(t, err)

		// Verify new data
		retrieved, err := orderRepo.Get(ctx, "evolve-002")
		require.NoError(t, err)
		assert.Equal(t, "cust-evolve", retrieved.CustomerID)
		assert.Equal(t, 3, retrieved.ItemCount)
	})

	// ==========================================================================
	// Phase 6: Error Handling
	// ==========================================================================
	t.Run("Phase6_ErrorHandling", func(t *testing.T) {
		orderRepo, err := postgres.NewPostgresRepository[OrderReadModel](db,
			postgres.WithReadModelSchema(schema),
			postgres.WithTableName("orders_errors"),
		)
		require.NoError(t, err)
		defer func() { _ = orderRepo.DropTable(ctx) }()

		// Insert initial order
		order := &OrderReadModel{
			OrderID: "err-001", CustomerID: "err-cust", Status: "pending",
			ItemCount: 1, TotalAmount: 10.00, CreatedAt: now, UpdatedAt: now,
		}
		require.NoError(t, orderRepo.Insert(ctx, order))

		// ErrNotFound on Get
		_, err = orderRepo.Get(ctx, "non-existent")
		assert.ErrorIs(t, err, mink.ErrNotFound)

		// ErrNotFound on Update
		err = orderRepo.Update(ctx, "non-existent", func(o *OrderReadModel) {})
		assert.ErrorIs(t, err, mink.ErrNotFound)

		// ErrNotFound on Delete
		err = orderRepo.Delete(ctx, "non-existent")
		assert.ErrorIs(t, err, mink.ErrNotFound)

		// ErrAlreadyExists on duplicate Insert
		err = orderRepo.Insert(ctx, order)
		assert.ErrorIs(t, err, mink.ErrAlreadyExists)

		// ErrNotFound on FindOne with no match
		query := mink.NewQuery().Where("status", mink.FilterOpEq, "nonexistent-status")
		_, err = orderRepo.FindOne(ctx, query.Build())
		assert.ErrorIs(t, err, mink.ErrNotFound)

		// Invalid query: negative limit
		_, err = orderRepo.Find(ctx, mink.Query{Limit: -1})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "limit must be non-negative")

		// Invalid query: negative offset
		_, err = orderRepo.Find(ctx, mink.Query{Offset: -1})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "offset must be non-negative")
	})

	// ==========================================================================
	// Phase 7: Clear and Cleanup
	// ==========================================================================
	t.Run("Phase7_ClearAndCleanup", func(t *testing.T) {
		orderRepo, err := postgres.NewPostgresRepository[OrderReadModel](db,
			postgres.WithReadModelSchema(schema),
			postgres.WithTableName("orders_clear"),
		)
		require.NoError(t, err)
		defer func() { _ = orderRepo.DropTable(ctx) }()

		// Insert multiple orders
		for i := 1; i <= 10; i++ {
			order := &OrderReadModel{
				OrderID: fmt.Sprintf("clear-%03d", i), CustomerID: "clear-cust", Status: "pending",
				ItemCount: i, TotalAmount: float64(i) * 10, CreatedAt: now, UpdatedAt: now,
			}
			require.NoError(t, orderRepo.Insert(ctx, order))
		}

		// Verify count
		count, err := orderRepo.Count(ctx, mink.Query{})
		require.NoError(t, err)
		assert.Equal(t, int64(10), count)

		// Clear all
		err = orderRepo.Clear(ctx)
		require.NoError(t, err)

		// Verify empty
		count, err = orderRepo.Count(ctx, mink.Query{})
		require.NoError(t, err)
		assert.Equal(t, int64(0), count)
	})
}

// openTestDB opens a database connection for testing.
func openTestDB(connStr string) (*sql.DB, error) {
	db, err := sql.Open("pgx", connStr)
	if err != nil {
		return nil, err
	}
	return db, db.Ping()
}

// quoteIdentifier quotes a PostgreSQL identifier.
func quoteIdentifier(s string) string {
	return `"` + s + `"`
}
