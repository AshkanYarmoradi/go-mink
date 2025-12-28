package postgres_test

import (
	"context"
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
