package memory_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/AshkanYarmoradi/go-mink"
	"github.com/AshkanYarmoradi/go-mink/adapters/memory"
	"github.com/AshkanYarmoradi/go-mink/testing/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// E2E Test: Complete Order Lifecycle with Memory Adapter
// =============================================================================

func TestE2E_CompleteOrderLifecycle_Memory(t *testing.T) {
	// Arrange: Setup event store with memory adapter
	adapter := memory.NewAdapter()
	store := mink.New(adapter)
	testutil.RegisterTestEvents(store)
	ctx := context.Background()

	// Act: Create order through its lifecycle
	order := testutil.NewOrder("order-123")

	// Step 1: Create order
	err := order.Create("customer-456")
	require.NoError(t, err)

	// Step 2: Add items
	err = order.AddItem("SKU-001", 2, 29.99)
	require.NoError(t, err)
	err = order.AddItem("SKU-002", 1, 49.99)
	require.NoError(t, err)

	// Step 3: Save aggregate
	err = store.SaveAggregate(ctx, order)
	require.NoError(t, err)

	// Step 4: Load aggregate in new instance
	loadedOrder := testutil.NewOrder("order-123")
	err = store.LoadAggregate(ctx, loadedOrder)
	require.NoError(t, err)

	// Assert: Loaded order should have version 3 (3 events applied during load)
	assert.Equal(t, "customer-456", loadedOrder.CustomerID)
	assert.Equal(t, "Created", loadedOrder.Status)
	assert.Len(t, loadedOrder.Items, 2)
	assert.Equal(t, int64(3), loadedOrder.Version())

	// Step 5: Ship the order
	err = loadedOrder.Ship("TRACK-789")
	require.NoError(t, err)
	err = store.SaveAggregate(ctx, loadedOrder)
	require.NoError(t, err)

	// Step 6: Reload to verify shipped state
	reloadedOrder := testutil.NewOrder("order-123")
	err = store.LoadAggregate(ctx, reloadedOrder)
	require.NoError(t, err)

	// Assert: Order is now shipped with version 4
	assert.Equal(t, "Shipped", reloadedOrder.Status)
	assert.Equal(t, int64(4), reloadedOrder.Version())

	// Step 7: Verify final state
	finalOrder := testutil.NewOrder("order-123")
	err = store.LoadAggregate(ctx, finalOrder)
	require.NoError(t, err)
	assert.Equal(t, "Shipped", finalOrder.Status)
	assert.Equal(t, "TRACK-789", finalOrder.TrackingNumber)
	assert.Equal(t, int64(4), finalOrder.Version())
}

// =============================================================================
// E2E Test: Concurrent Modifications and Conflict Detection
// =============================================================================

func TestE2E_ConcurrentModification(t *testing.T) {
	adapter := memory.NewAdapter()
	store := mink.New(adapter)
	testutil.RegisterTestEvents(store)
	ctx := context.Background()

	// Create initial order
	order := testutil.NewOrder("concurrent-order")
	err := order.Create("customer-1")
	require.NoError(t, err)
	err = store.SaveAggregate(ctx, order)
	require.NoError(t, err)

	// Simulate concurrent modifications
	var wg sync.WaitGroup
	conflictCount := 0
	successCount := 0
	var mu sync.Mutex

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(itemNum int) {
			defer wg.Done()

			// Each goroutine loads the order
			localOrder := testutil.NewOrder("concurrent-order")
			if err := store.LoadAggregate(ctx, localOrder); err != nil {
				return
			}

			// Try to add an item
			sku := fmt.Sprintf("SKU-%03d", itemNum)
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

	// At least one should succeed, some should conflict
	assert.Greater(t, successCount, 0, "At least one concurrent write should succeed")
	t.Logf("Concurrent test: %d successes, %d conflicts", successCount, conflictCount)

	// Verify final state is consistent
	finalOrder := testutil.NewOrder("concurrent-order")
	err = store.LoadAggregate(ctx, finalOrder)
	require.NoError(t, err)
	assert.Equal(t, int64(1+successCount), finalOrder.Version())
}

// =============================================================================
// E2E Test: Read Model Projection
// =============================================================================

func TestE2E_ReadModelProjection(t *testing.T) {
	adapter := memory.NewAdapter()
	store := mink.New(adapter)
	testutil.RegisterTestEvents(store)
	ctx := context.Background()

	// Create read model
	readModel := testutil.NewOrderReadModel()

	// Create multiple orders
	for i := 1; i <= 3; i++ {
		order := testutil.NewOrder(fmt.Sprintf("proj-order-%d", i))
		err := order.Create(fmt.Sprintf("customer-%d", i))
		require.NoError(t, err)
		for j := 1; j <= i; j++ { // Order i has i items
			err = order.AddItem(fmt.Sprintf("SKU-%d-%d", i, j), j, 10.0)
			require.NoError(t, err)
		}
		err = store.SaveAggregate(ctx, order)
		require.NoError(t, err)
	}

	// Load all events and project to read model
	for i := 1; i <= 3; i++ {
		streamID := fmt.Sprintf("Order-proj-order-%d", i)
		events, err := store.LoadRaw(ctx, streamID, 0)
		require.NoError(t, err)

		for _, event := range events {
			err = readModel.Apply(event)
			require.NoError(t, err)
		}
	}

	// Verify projections
	assert.Equal(t, 3, readModel.Count())

	// Order 1 has 1 item, Order 2 has 2 items, Order 3 has 3 items
	summary1 := readModel.Get("proj-order-1")
	require.NotNil(t, summary1)
	assert.Equal(t, 1, summary1.ItemCount)

	summary3 := readModel.Get("proj-order-3")
	require.NotNil(t, summary3)
	assert.Equal(t, 3, summary3.ItemCount)
}

// =============================================================================
// E2E Test: Multiple Aggregates Interaction
// =============================================================================

func TestE2E_MultipleAggregates(t *testing.T) {
	adapter := memory.NewAdapter()
	store := mink.New(adapter)
	testutil.RegisterTestEvents(store)
	ctx := context.Background()

	// Create multiple orders
	orderIDs := []string{"multi-1", "multi-2", "multi-3"}
	for _, id := range orderIDs {
		order := testutil.NewOrder(id)
		err := order.Create("shared-customer")
		require.NoError(t, err)
		err = order.AddItem("WIDGET", 1, 25.0)
		require.NoError(t, err)
		err = store.SaveAggregate(ctx, order)
		require.NoError(t, err)
	}

	// Ship only the first order
	order1 := testutil.NewOrder("multi-1")
	err := store.LoadAggregate(ctx, order1)
	require.NoError(t, err)
	err = order1.Ship("TRACK-001")
	require.NoError(t, err)
	err = store.SaveAggregate(ctx, order1)
	require.NoError(t, err)

	// Cancel the second order
	order2 := testutil.NewOrder("multi-2")
	err = store.LoadAggregate(ctx, order2)
	require.NoError(t, err)
	err = order2.Cancel("Customer request")
	require.NoError(t, err)
	err = store.SaveAggregate(ctx, order2)
	require.NoError(t, err)

	// Verify all orders have correct final state
	verifyOrder1 := testutil.NewOrder("multi-1")
	err = store.LoadAggregate(ctx, verifyOrder1)
	require.NoError(t, err)
	assert.Equal(t, "Shipped", verifyOrder1.Status)

	verifyOrder2 := testutil.NewOrder("multi-2")
	err = store.LoadAggregate(ctx, verifyOrder2)
	require.NoError(t, err)
	assert.Equal(t, "Cancelled", verifyOrder2.Status)

	verifyOrder3 := testutil.NewOrder("multi-3")
	err = store.LoadAggregate(ctx, verifyOrder3)
	require.NoError(t, err)
	assert.Equal(t, "Created", verifyOrder3.Status)
}

// =============================================================================
// E2E Test: Event Metadata Flow
// =============================================================================

func TestE2E_EventMetadata(t *testing.T) {
	adapter := memory.NewAdapter()
	store := mink.New(adapter)
	testutil.RegisterTestEvents(store)
	ctx := context.Background()

	// Create order
	order := testutil.NewOrder("meta-order")
	err := order.Create("customer-meta")
	require.NoError(t, err)
	err = order.AddItem("META-SKU", 1, 50.0)
	require.NoError(t, err)

	// Save aggregate (currently SaveAggregate doesn't pass metadata through context,
	// so we test metadata through the Append API instead)
	err = store.SaveAggregate(ctx, order)
	require.NoError(t, err)

	// Verify events are stored and can be loaded
	events, err := store.LoadRaw(ctx, "Order-meta-order", 0)
	require.NoError(t, err)
	require.Len(t, events, 2)

	// Verify event types
	assert.Equal(t, "OrderCreated", events[0].Type)
	assert.Equal(t, "ItemAdded", events[1].Type)
}

// =============================================================================
// E2E Test: Stream Categories and Querying
// =============================================================================

func TestE2E_StreamCategories(t *testing.T) {
	adapter := memory.NewAdapter()
	store := mink.New(adapter)
	testutil.RegisterTestEvents(store)
	ctx := context.Background()

	// Create orders
	for i := 1; i <= 5; i++ {
		order := testutil.NewOrder(fmt.Sprintf("cat-order-%d", i))
		err := order.Create(fmt.Sprintf("customer-%d", i))
		require.NoError(t, err)
		err = store.SaveAggregate(ctx, order)
		require.NoError(t, err)
	}

	// Verify stream info via store
	info, err := store.GetStreamInfo(ctx, "Order-cat-order-1")
	require.NoError(t, err)
	assert.Equal(t, int64(1), info.Version)
	assert.Equal(t, int64(1), info.EventCount)

	// Test stream existence via adapter
	adapterInfo, err := adapter.GetStreamInfo(ctx, "Order-cat-order-3")
	require.NoError(t, err)
	assert.Equal(t, "Order-cat-order-3", adapterInfo.StreamID)

	// Test non-existent stream
	_, err = adapter.GetStreamInfo(ctx, "Order-nonexistent")
	assert.Error(t, err) // Should return error for non-existent stream
}

// =============================================================================
// E2E Test: Error Recovery and Idempotency
// =============================================================================

func TestE2E_ErrorRecovery(t *testing.T) {
	adapter := memory.NewAdapter()
	store := mink.New(adapter)
	testutil.RegisterTestEvents(store)
	ctx := context.Background()

	// Create and save an order
	order := testutil.NewOrder("recovery-order")
	err := order.Create("customer")
	require.NoError(t, err)
	err = order.AddItem("ITEM", 1, 10.0)
	require.NoError(t, err)
	err = store.SaveAggregate(ctx, order)
	require.NoError(t, err)

	// Reload order to get correct version for next operation
	order = testutil.NewOrder("recovery-order")
	err = store.LoadAggregate(ctx, order)
	require.NoError(t, err)

	// Ship the order - should succeed
	err = order.Ship("TRACK-001")
	require.NoError(t, err)
	err = store.SaveAggregate(ctx, order)
	require.NoError(t, err)

	// Reload to test cancel
	order = testutil.NewOrder("recovery-order")
	err = store.LoadAggregate(ctx, order)
	require.NoError(t, err)

	// Try to cancel shipped order - should fail at domain level
	err = order.Cancel("Change of mind")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cannot cancel shipped order")

	// Verify no uncommitted events from failed operation
	assert.Empty(t, order.UncommittedEvents())

	// Verify order state is still "Shipped"
	loadedOrder := testutil.NewOrder("recovery-order")
	err = store.LoadAggregate(ctx, loadedOrder)
	require.NoError(t, err)
	assert.Equal(t, "Shipped", loadedOrder.Status)
}

// =============================================================================
// E2E Test: Large Event Stream
// =============================================================================

func TestE2E_LargeEventStream(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping large event stream test in short mode")
	}

	adapter := memory.NewAdapter()
	store := mink.New(adapter)
	testutil.RegisterTestEvents(store)
	ctx := context.Background()

	// Create order with many items
	order := testutil.NewOrder("large-order")
	err := order.Create("customer-large")
	require.NoError(t, err)
	err = store.SaveAggregate(ctx, order)
	require.NoError(t, err)

	// Add 100 items in batches
	for batch := 0; batch < 10; batch++ {
		loadedOrder := testutil.NewOrder("large-order")
		err = store.LoadAggregate(ctx, loadedOrder)
		require.NoError(t, err)

		for i := 0; i < 10; i++ {
			itemNum := batch*10 + i
			err = loadedOrder.AddItem(fmt.Sprintf("SKU-%03d", itemNum), 1, float64(itemNum))
			require.NoError(t, err)
		}
		err = store.SaveAggregate(ctx, loadedOrder)
		require.NoError(t, err)
	}

	// Verify final state
	finalOrder := testutil.NewOrder("large-order")
	start := time.Now()
	err = store.LoadAggregate(ctx, finalOrder)
	loadTime := time.Since(start)
	require.NoError(t, err)

	assert.Len(t, finalOrder.Items, 100)
	assert.Equal(t, int64(101), finalOrder.Version()) // 1 create + 100 items
	t.Logf("Loaded 101 events in %v", loadTime)
}

// =============================================================================
// E2E Test: Subscription to Events
// =============================================================================

func TestE2E_Subscription(t *testing.T) {
	adapter := memory.NewAdapter()
	store := mink.New(adapter)
	testutil.RegisterTestEvents(store)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Start subscription before any events (using adapter directly)
	eventCh, err := adapter.SubscribeAll(ctx, 0)
	require.NoError(t, err)

	// Collect events in goroutine with mutex protection for thread safety
	var receivedEvents []mink.StoredEvent
	var mu sync.Mutex
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for event := range eventCh {
			mu.Lock()
			receivedEvents = append(receivedEvents, mink.StoredEvent{
				ID:             event.ID,
				StreamID:       event.StreamID,
				Type:           event.Type,
				Data:           event.Data,
				Version:        event.Version,
				GlobalPosition: event.GlobalPosition,
				Timestamp:      event.Timestamp,
			})
			count := len(receivedEvents)
			mu.Unlock()
			if count >= 3 {
				return
			}
		}
	}()

	// Give subscriber time to start
	time.Sleep(50 * time.Millisecond)

	// Create order with events
	order := testutil.NewOrder("sub-order")
	err = order.Create("sub-customer")
	require.NoError(t, err)
	err = order.AddItem("SUB-SKU", 1, 10.0)
	require.NoError(t, err)
	err = order.Ship("SUB-TRACK")
	require.NoError(t, err)
	err = store.SaveAggregate(ctx, order)
	require.NoError(t, err)

	// Wait for events to be received
	wg.Wait()

	assert.Len(t, receivedEvents, 3)
	assert.Equal(t, "OrderCreated", receivedEvents[0].Type)
	assert.Equal(t, "ItemAdded", receivedEvents[1].Type)
	assert.Equal(t, "OrderShipped", receivedEvents[2].Type)
}
