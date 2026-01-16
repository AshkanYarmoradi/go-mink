package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/AshkanYarmoradi/go-mink"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ====================================================================
// End-to-End Integration Tests for PostgreSQL Saga Store
// ====================================================================
//
// These tests validate complete saga workflows through all lifecycle stages.
// They require a running PostgreSQL instance (set TEST_DATABASE_URL).
//
// Run with: go test -v ./adapters/postgres/... -run "E2E"
// ====================================================================

// TestSagaStore_E2E_OrderFulfillmentSaga_HappyPath tests a complete order fulfillment saga
// through all stages: Started -> Running -> Completed (happy path)
func TestSagaStore_E2E_OrderFulfillmentSaga_HappyPath(t *testing.T) {
	store, cleanup := setupTestSagaStore(t)
	defer cleanup()

	ctx := context.Background()
	sagaID := "order-saga-e2e-" + time.Now().Format("20060102150405")
	correlationID := "order-12345"

	// ================================================================
	// Phase 1: Create new saga (Started status)
	// ================================================================
	t.Log("Phase 1: Creating new order fulfillment saga")

	saga := &mink.SagaState{
		ID:            sagaID,
		Type:          "OrderFulfillmentSaga",
		CorrelationID: correlationID,
		Status:        mink.SagaStatusStarted,
		CurrentStep:   0,
		StartedAt:     time.Now(),
		Version:       0,
		Data: map[string]interface{}{
			"orderId":     "ORD-12345",
			"customerId":  "CUST-67890",
			"totalAmount": 299.99,
			"items": []interface{}{
				map[string]interface{}{"sku": "ITEM-001", "qty": 2, "price": 99.99},
				map[string]interface{}{"sku": "ITEM-002", "qty": 1, "price": 100.01},
			},
		},
		Steps: []mink.SagaStep{},
	}

	err := store.Save(ctx, saga)
	require.NoError(t, err)
	assert.Equal(t, int64(1), saga.Version)

	// Verify saga was created
	loaded, err := store.Load(ctx, sagaID)
	require.NoError(t, err)
	assert.Equal(t, mink.SagaStatusStarted, loaded.Status)
	assert.Equal(t, correlationID, loaded.CorrelationID)
	assert.Equal(t, "ORD-12345", loaded.Data["orderId"])

	// ================================================================
	// Phase 2: Execute Step 1 - Reserve Inventory (Running status)
	// ================================================================
	t.Log("Phase 2: Executing Step 1 - Reserve Inventory")

	step1CompletedAt := time.Now()
	saga.Status = mink.SagaStatusRunning
	saga.CurrentStep = 1
	saga.Steps = append(saga.Steps, mink.SagaStep{
		Name:        "ReserveInventory",
		Index:       0,
		Status:      mink.SagaStepCompleted,
		Command:     "ReserveInventoryCommand",
		CompletedAt: &step1CompletedAt,
	})
	saga.Data["inventoryReserved"] = true

	err = store.Save(ctx, saga)
	require.NoError(t, err)
	assert.Equal(t, int64(2), saga.Version)

	// Verify step 1 completion
	loaded, err = store.Load(ctx, sagaID)
	require.NoError(t, err)
	assert.Equal(t, mink.SagaStatusRunning, loaded.Status)
	assert.Equal(t, 1, loaded.CurrentStep)
	assert.Len(t, loaded.Steps, 1)
	assert.Equal(t, mink.SagaStepCompleted, loaded.Steps[0].Status)

	// ================================================================
	// Phase 3: Execute Step 2 - Process Payment
	// ================================================================
	t.Log("Phase 3: Executing Step 2 - Process Payment")

	step2CompletedAt := time.Now()
	saga.CurrentStep = 2
	saga.Steps = append(saga.Steps, mink.SagaStep{
		Name:        "ProcessPayment",
		Index:       1,
		Status:      mink.SagaStepCompleted,
		Command:     "ProcessPaymentCommand",
		CompletedAt: &step2CompletedAt,
	})
	saga.Data["paymentTransactionId"] = "TXN-98765"

	err = store.Save(ctx, saga)
	require.NoError(t, err)
	assert.Equal(t, int64(3), saga.Version)

	// ================================================================
	// Phase 4: Execute Step 3 - Ship Order
	// ================================================================
	t.Log("Phase 4: Executing Step 3 - Ship Order")

	step3CompletedAt := time.Now()
	saga.CurrentStep = 3
	saga.Steps = append(saga.Steps, mink.SagaStep{
		Name:        "ShipOrder",
		Index:       2,
		Status:      mink.SagaStepCompleted,
		Command:     "ShipOrderCommand",
		CompletedAt: &step3CompletedAt,
	})
	saga.Data["trackingNumber"] = "TRACK-ABC123"

	err = store.Save(ctx, saga)
	require.NoError(t, err)
	assert.Equal(t, int64(4), saga.Version)

	// ================================================================
	// Phase 5: Complete Saga
	// ================================================================
	t.Log("Phase 5: Completing saga")

	completedAt := time.Now()
	saga.Status = mink.SagaStatusCompleted
	saga.CompletedAt = &completedAt

	err = store.Save(ctx, saga)
	require.NoError(t, err)
	assert.Equal(t, int64(5), saga.Version)

	// ================================================================
	// Final Verification
	// ================================================================
	t.Log("Final Verification: Loading completed saga")

	finalSaga, err := store.Load(ctx, sagaID)
	require.NoError(t, err)

	assert.Equal(t, sagaID, finalSaga.ID)
	assert.Equal(t, "OrderFulfillmentSaga", finalSaga.Type)
	assert.Equal(t, correlationID, finalSaga.CorrelationID)
	assert.Equal(t, mink.SagaStatusCompleted, finalSaga.Status)
	assert.Equal(t, 3, finalSaga.CurrentStep)
	assert.NotNil(t, finalSaga.CompletedAt)
	assert.Equal(t, int64(5), finalSaga.Version)

	// Verify all steps
	assert.Len(t, finalSaga.Steps, 3)
	assert.Equal(t, "ReserveInventory", finalSaga.Steps[0].Name)
	assert.Equal(t, "ProcessPayment", finalSaga.Steps[1].Name)
	assert.Equal(t, "ShipOrder", finalSaga.Steps[2].Name)

	// Verify all data preserved
	assert.Equal(t, "ORD-12345", finalSaga.Data["orderId"])
	assert.Equal(t, "TXN-98765", finalSaga.Data["paymentTransactionId"])
	assert.Equal(t, "TRACK-ABC123", finalSaga.Data["trackingNumber"])

	// Verify can find by correlation ID
	foundByCorr, err := store.FindByCorrelationID(ctx, correlationID)
	require.NoError(t, err)
	assert.Equal(t, sagaID, foundByCorr.ID)

	// Verify appears in type search
	foundByType, err := store.FindByType(ctx, "OrderFulfillmentSaga")
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(foundByType), 1)

	t.Log("E2E Happy Path Test Completed Successfully!")
}

// TestSagaStore_E2E_OrderFulfillmentSaga_FailureAndCompensation tests saga failure
// and compensation flow: Started -> Running -> Failed -> Compensating -> Compensated
func TestSagaStore_E2E_OrderFulfillmentSaga_FailureAndCompensation(t *testing.T) {
	store, cleanup := setupTestSagaStore(t)
	defer cleanup()

	ctx := context.Background()
	sagaID := "order-saga-fail-" + time.Now().Format("20060102150405")
	correlationID := "order-fail-12345"

	// ================================================================
	// Phase 1: Create and start saga
	// ================================================================
	t.Log("Phase 1: Creating new order fulfillment saga")

	saga := &mink.SagaState{
		ID:            sagaID,
		Type:          "OrderFulfillmentSaga",
		CorrelationID: correlationID,
		Status:        mink.SagaStatusStarted,
		CurrentStep:   0,
		StartedAt:     time.Now(),
		Version:       0,
		Data: map[string]interface{}{
			"orderId":     "ORD-FAIL-001",
			"customerId":  "CUST-67890",
			"totalAmount": 1500.00, // Large order that will fail payment
		},
	}

	err := store.Save(ctx, saga)
	require.NoError(t, err)

	// ================================================================
	// Phase 2: Step 1 - Reserve Inventory (Success)
	// ================================================================
	t.Log("Phase 2: Step 1 - Reserve Inventory (Success)")

	step1CompletedAt := time.Now()
	saga.Status = mink.SagaStatusRunning
	saga.CurrentStep = 1
	saga.Steps = append(saga.Steps, mink.SagaStep{
		Name:        "ReserveInventory",
		Index:       0,
		Status:      mink.SagaStepCompleted,
		Command:     "ReserveInventoryCommand",
		CompletedAt: &step1CompletedAt,
	})
	saga.Data["inventoryReservationId"] = "INV-RES-001"

	err = store.Save(ctx, saga)
	require.NoError(t, err)

	// ================================================================
	// Phase 3: Step 2 - Process Payment (FAILURE!)
	// ================================================================
	t.Log("Phase 3: Step 2 - Process Payment (FAILURE - insufficient funds)")

	saga.CurrentStep = 2
	saga.Status = mink.SagaStatusFailed
	saga.FailureReason = "Payment failed: insufficient funds for amount $1500.00"
	saga.Steps = append(saga.Steps, mink.SagaStep{
		Name:    "ProcessPayment",
		Index:   1,
		Status:  mink.SagaStepFailed,
		Command: "ProcessPaymentCommand",
		Error:   "PaymentDeclinedException: Card declined - insufficient funds",
	})

	err = store.Save(ctx, saga)
	require.NoError(t, err)

	// Verify failure state
	loaded, err := store.Load(ctx, sagaID)
	require.NoError(t, err)
	assert.Equal(t, mink.SagaStatusFailed, loaded.Status)
	assert.Contains(t, loaded.FailureReason, "insufficient funds")
	assert.Equal(t, mink.SagaStepFailed, loaded.Steps[1].Status)

	// ================================================================
	// Phase 4: Start Compensation
	// ================================================================
	t.Log("Phase 4: Starting compensation process")

	saga.Status = mink.SagaStatusCompensating

	err = store.Save(ctx, saga)
	require.NoError(t, err)

	// ================================================================
	// Phase 5: Compensate Step 1 - Release Inventory
	// ================================================================
	t.Log("Phase 5: Compensating Step 1 - Release Inventory")

	compensateTime := time.Now()
	saga.Steps[0].Status = mink.SagaStepCompensated
	saga.Steps[0].CompletedAt = &compensateTime
	saga.Data["inventoryReleased"] = true

	err = store.Save(ctx, saga)
	require.NoError(t, err)

	// ================================================================
	// Phase 6: Complete Compensation
	// ================================================================
	t.Log("Phase 6: Completing compensation")

	compensatedAt := time.Now()
	saga.Status = mink.SagaStatusCompensated
	saga.CompletedAt = &compensatedAt

	err = store.Save(ctx, saga)
	require.NoError(t, err)

	// ================================================================
	// Final Verification
	// ================================================================
	t.Log("Final Verification")

	finalSaga, err := store.Load(ctx, sagaID)
	require.NoError(t, err)

	assert.Equal(t, mink.SagaStatusCompensated, finalSaga.Status)
	assert.Contains(t, finalSaga.FailureReason, "insufficient funds")
	assert.NotNil(t, finalSaga.CompletedAt)

	// Step 1 was compensated
	assert.Equal(t, mink.SagaStepCompensated, finalSaga.Steps[0].Status)
	// Step 2 remained failed (nothing to compensate)
	assert.Equal(t, mink.SagaStepFailed, finalSaga.Steps[1].Status)

	// Verify data preserved
	assert.Equal(t, true, finalSaga.Data["inventoryReleased"])

	t.Log("E2E Failure and Compensation Test Completed Successfully!")
}

// TestSagaStore_E2E_ConcurrentSagas tests multiple sagas running concurrently
func TestSagaStore_E2E_ConcurrentSagas(t *testing.T) {
	store, cleanup := setupTestSagaStore(t)
	defer cleanup()

	ctx := context.Background()
	timestamp := time.Now().Format("20060102150405")

	// ================================================================
	// Create multiple sagas with different statuses
	// ================================================================
	t.Log("Creating multiple concurrent sagas")

	sagas := []struct {
		id     string
		status mink.SagaStatus
		step   int
	}{
		{"concurrent-started-" + timestamp, mink.SagaStatusStarted, 0},
		{"concurrent-running-1-" + timestamp, mink.SagaStatusRunning, 1},
		{"concurrent-running-2-" + timestamp, mink.SagaStatusRunning, 2},
		{"concurrent-completed-" + timestamp, mink.SagaStatusCompleted, 3},
		{"concurrent-failed-" + timestamp, mink.SagaStatusFailed, 2},
	}

	for _, s := range sagas {
		completedAt := time.Now()
		saga := &mink.SagaState{
			ID:          s.id,
			Type:        "ConcurrentTestSaga",
			Status:      s.status,
			CurrentStep: s.step,
			StartedAt:   time.Now(),
			Version:     0,
		}
		if s.status == mink.SagaStatusCompleted || s.status == mink.SagaStatusFailed {
			saga.CompletedAt = &completedAt
		}
		if s.status == mink.SagaStatusFailed {
			saga.FailureReason = "Test failure"
		}

		err := store.Save(ctx, saga)
		require.NoError(t, err)
	}

	// ================================================================
	// Query by type and status
	// ================================================================
	t.Log("Querying sagas by type and status")

	// Find all ConcurrentTestSaga
	all, err := store.FindByType(ctx, "ConcurrentTestSaga")
	require.NoError(t, err)
	assert.Len(t, all, 5)

	// Find only running sagas
	running, err := store.FindByType(ctx, "ConcurrentTestSaga", mink.SagaStatusRunning)
	require.NoError(t, err)
	assert.Len(t, running, 2)

	// Find started and running
	activeStates, err := store.FindByType(ctx, "ConcurrentTestSaga", mink.SagaStatusStarted, mink.SagaStatusRunning)
	require.NoError(t, err)
	assert.Len(t, activeStates, 3)

	// ================================================================
	// Count by status
	// ================================================================
	t.Log("Counting sagas by status")

	counts, err := store.CountByStatus(ctx)
	require.NoError(t, err)

	// Should have at least our test sagas
	assert.GreaterOrEqual(t, counts[mink.SagaStatusStarted], int64(1))
	assert.GreaterOrEqual(t, counts[mink.SagaStatusRunning], int64(2))
	assert.GreaterOrEqual(t, counts[mink.SagaStatusCompleted], int64(1))
	assert.GreaterOrEqual(t, counts[mink.SagaStatusFailed], int64(1))

	// ================================================================
	// Update one running saga to completed
	// ================================================================
	t.Log("Updating running saga to completed")

	runningSaga, err := store.Load(ctx, "concurrent-running-1-"+timestamp)
	require.NoError(t, err)

	completedAt := time.Now()
	runningSaga.Status = mink.SagaStatusCompleted
	runningSaga.CompletedAt = &completedAt

	err = store.Save(ctx, runningSaga)
	require.NoError(t, err)

	// Verify update
	updated, err := store.Load(ctx, "concurrent-running-1-"+timestamp)
	require.NoError(t, err)
	assert.Equal(t, mink.SagaStatusCompleted, updated.Status)

	t.Log("E2E Concurrent Sagas Test Completed Successfully!")
}

// TestSagaStore_E2E_CleanupWorkflow tests the cleanup process for old sagas
func TestSagaStore_E2E_CleanupWorkflow(t *testing.T) {
	store, cleanup := setupTestSagaStore(t)
	defer cleanup()

	ctx := context.Background()
	timestamp := time.Now().Format("20060102150405")

	// ================================================================
	// Create sagas with different ages and statuses
	// ================================================================
	t.Log("Creating sagas with different ages")

	oldTime := time.Now().Add(-72 * time.Hour)
	recentTime := time.Now().Add(-1 * time.Hour)

	testSagas := []struct {
		id          string
		status      mink.SagaStatus
		startedAt   time.Time
		completed   *time.Time
		shouldClean bool
	}{
		// Old terminal sagas - should be cleaned
		{"old-completed-" + timestamp, mink.SagaStatusCompleted, oldTime, &oldTime, true},
		{"old-failed-" + timestamp, mink.SagaStatusFailed, oldTime, &oldTime, true},
		{"old-compensated-" + timestamp, mink.SagaStatusCompensated, oldTime, &oldTime, true},
		// Old non-terminal sagas - should NOT be cleaned
		{"old-running-" + timestamp, mink.SagaStatusRunning, oldTime, nil, false},
		{"old-started-" + timestamp, mink.SagaStatusStarted, oldTime, nil, false},
		// Recent terminal sagas - should NOT be cleaned (too new)
		{"recent-completed-" + timestamp, mink.SagaStatusCompleted, recentTime, &recentTime, false},
	}

	for _, s := range testSagas {
		saga := &mink.SagaState{
			ID:          s.id,
			Type:        "CleanupWorkflowSaga",
			Status:      s.status,
			StartedAt:   s.startedAt,
			CompletedAt: s.completed,
			Version:     0,
		}
		err := store.Save(ctx, saga)
		require.NoError(t, err)
	}

	// Verify all created
	all, err := store.FindByType(ctx, "CleanupWorkflowSaga")
	require.NoError(t, err)
	assert.Len(t, all, 6)

	// ================================================================
	// Run cleanup for sagas older than 24 hours
	// ================================================================
	t.Log("Running cleanup for terminal sagas older than 24 hours")

	deleted, err := store.Cleanup(ctx, 24*time.Hour)
	require.NoError(t, err)
	assert.Equal(t, int64(3), deleted) // 3 old terminal sagas

	// ================================================================
	// Verify cleanup results
	// ================================================================
	t.Log("Verifying cleanup results")

	// Cleaned sagas should not exist
	for _, s := range testSagas {
		_, err := store.Load(ctx, s.id)
		if s.shouldClean {
			assert.Error(t, err, "Saga %s should have been cleaned", s.id)
			var notFoundErr *mink.SagaNotFoundError
			assert.ErrorAs(t, err, &notFoundErr)
		} else {
			assert.NoError(t, err, "Saga %s should still exist", s.id)
		}
	}

	// Should have 3 remaining
	remaining, err := store.FindByType(ctx, "CleanupWorkflowSaga")
	require.NoError(t, err)
	assert.Len(t, remaining, 3)

	t.Log("E2E Cleanup Workflow Test Completed Successfully!")
}

// TestSagaStore_E2E_VersionConflictResolution tests optimistic concurrency handling
func TestSagaStore_E2E_VersionConflictResolution(t *testing.T) {
	store, cleanup := setupTestSagaStore(t)
	defer cleanup()

	ctx := context.Background()
	sagaID := "version-conflict-" + time.Now().Format("20060102150405")

	// ================================================================
	// Create initial saga
	// ================================================================
	t.Log("Creating initial saga")

	saga := &mink.SagaState{
		ID:        sagaID,
		Type:      "VersionTestSaga",
		Status:    mink.SagaStatusStarted,
		StartedAt: time.Now(),
		Version:   0,
		Data: map[string]interface{}{
			"counter": 0,
		},
	}

	err := store.Save(ctx, saga)
	require.NoError(t, err)
	assert.Equal(t, int64(1), saga.Version)

	// ================================================================
	// Simulate concurrent access - load saga twice
	// ================================================================
	t.Log("Simulating concurrent access")

	saga1, err := store.Load(ctx, sagaID)
	require.NoError(t, err)

	saga2, err := store.Load(ctx, sagaID)
	require.NoError(t, err)

	// Both have same version
	assert.Equal(t, saga1.Version, saga2.Version)

	// ================================================================
	// First update succeeds
	// ================================================================
	t.Log("First concurrent update")

	saga1.Data["counter"] = 1
	saga1.Status = mink.SagaStatusRunning

	err = store.Save(ctx, saga1)
	require.NoError(t, err)
	assert.Equal(t, int64(2), saga1.Version)

	// ================================================================
	// Second update fails (version conflict)
	// ================================================================
	t.Log("Second concurrent update (should fail)")

	saga2.Data["counter"] = 2
	saga2.Status = mink.SagaStatusRunning

	err = store.Save(ctx, saga2)
	assert.Error(t, err)
	assert.ErrorIs(t, err, mink.ErrConcurrencyConflict)

	// ================================================================
	// Retry with fresh load
	// ================================================================
	t.Log("Retrying with fresh load")

	saga2Fresh, err := store.Load(ctx, sagaID)
	require.NoError(t, err)
	assert.Equal(t, int64(2), saga2Fresh.Version)

	// Verify first update's data is there
	assert.Equal(t, float64(1), saga2Fresh.Data["counter"])

	// Now this update succeeds
	saga2Fresh.Data["counter"] = 2
	err = store.Save(ctx, saga2Fresh)
	require.NoError(t, err)
	assert.Equal(t, int64(3), saga2Fresh.Version)

	// ================================================================
	// Final verification
	// ================================================================
	finalSaga, err := store.Load(ctx, sagaID)
	require.NoError(t, err)
	assert.Equal(t, int64(3), finalSaga.Version)
	assert.Equal(t, float64(2), finalSaga.Data["counter"])

	t.Log("E2E Version Conflict Resolution Test Completed Successfully!")
}

// TestSagaStore_E2E_CompleteLifecycleWithDelete tests full lifecycle including deletion
func TestSagaStore_E2E_CompleteLifecycleWithDelete(t *testing.T) {
	store, cleanup := setupTestSagaStore(t)
	defer cleanup()

	ctx := context.Background()
	sagaID := "lifecycle-delete-" + time.Now().Format("20060102150405")
	correlationID := "delete-test-corr"

	// ================================================================
	// Create -> Run -> Complete -> Delete
	// ================================================================

	// Create
	t.Log("Creating saga")
	saga := &mink.SagaState{
		ID:            sagaID,
		Type:          "LifecycleTestSaga",
		CorrelationID: correlationID,
		Status:        mink.SagaStatusStarted,
		StartedAt:     time.Now(),
		Version:       0,
	}
	err := store.Save(ctx, saga)
	require.NoError(t, err)

	// Verify findable
	_, err = store.FindByCorrelationID(ctx, correlationID)
	require.NoError(t, err)

	// Run
	t.Log("Transitioning to running")
	saga.Status = mink.SagaStatusRunning
	err = store.Save(ctx, saga)
	require.NoError(t, err)

	// Complete
	t.Log("Completing saga")
	completedAt := time.Now()
	saga.Status = mink.SagaStatusCompleted
	saga.CompletedAt = &completedAt
	err = store.Save(ctx, saga)
	require.NoError(t, err)

	// Verify completed
	loaded, err := store.Load(ctx, sagaID)
	require.NoError(t, err)
	assert.Equal(t, mink.SagaStatusCompleted, loaded.Status)

	// Delete
	t.Log("Deleting saga")
	err = store.Delete(ctx, sagaID)
	require.NoError(t, err)

	// Verify deleted
	_, err = store.Load(ctx, sagaID)
	assert.Error(t, err)
	var notFoundErr *mink.SagaNotFoundError
	assert.ErrorAs(t, err, &notFoundErr)

	// Verify not findable by correlation ID
	_, err = store.FindByCorrelationID(ctx, correlationID)
	assert.Error(t, err)

	t.Log("E2E Complete Lifecycle with Delete Test Completed Successfully!")
}
