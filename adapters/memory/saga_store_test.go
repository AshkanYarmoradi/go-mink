package memory

import (
	"context"
	"testing"
	"time"

	"github.com/AshkanYarmoradi/go-mink/adapters"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewSagaStore(t *testing.T) {
	store := NewSagaStore()

	assert.NotNil(t, store)
	assert.Equal(t, 0, store.Count())
}

func TestSagaStore_Save_NewSaga(t *testing.T) {
	store := NewSagaStore()
	ctx := context.Background()

	state := &adapters.SagaState{
		ID:            "saga-123",
		Type:          "OrderFulfillment",
		CorrelationID: "order-456",
		Status:        adapters.SagaStatusRunning,
		CurrentStep:   1,
		Data:          map[string]interface{}{"orderID": "order-456"},
		StartedAt:     time.Now(),
		Version:       0,
	}

	err := store.Save(ctx, state)

	require.NoError(t, err)
	assert.Equal(t, int64(1), state.Version)
	assert.Equal(t, 1, store.Count())
}

func TestSagaStore_Save_UpdateSaga(t *testing.T) {
	store := NewSagaStore()
	ctx := context.Background()

	// Save initial state
	state := &adapters.SagaState{
		ID:        "saga-123",
		Type:      "OrderFulfillment",
		Status:    adapters.SagaStatusRunning,
		StartedAt: time.Now(),
		Version:   0,
	}
	err := store.Save(ctx, state)
	require.NoError(t, err)

	// Update state
	state.Status = adapters.SagaStatusCompleted
	state.CurrentStep = 3
	err = store.Save(ctx, state)

	require.NoError(t, err)
	assert.Equal(t, int64(2), state.Version)
}

func TestSagaStore_Save_ConcurrencyConflict(t *testing.T) {
	store := NewSagaStore()
	ctx := context.Background()

	// Save initial state
	state := &adapters.SagaState{
		ID:        "saga-123",
		Type:      "OrderFulfillment",
		StartedAt: time.Now(),
		Version:   0,
	}
	err := store.Save(ctx, state)
	require.NoError(t, err)

	// Try to save with wrong version
	state.Version = 0 // Should be 1
	err = store.Save(ctx, state)

	assert.Error(t, err)
	var concurrencyErr *adapters.ConcurrencyError
	assert.ErrorAs(t, err, &concurrencyErr)
}

func TestSagaStore_Save_NilState(t *testing.T) {
	store := NewSagaStore()
	ctx := context.Background()

	err := store.Save(ctx, nil)

	assert.ErrorIs(t, err, adapters.ErrNilAggregate)
}

func TestSagaStore_Save_EmptyID(t *testing.T) {
	store := NewSagaStore()
	ctx := context.Background()

	state := &adapters.SagaState{
		ID:   "",
		Type: "Test",
	}

	err := store.Save(ctx, state)

	assert.ErrorIs(t, err, adapters.ErrEmptyStreamID)
}

func TestSagaStore_Save_ContextCancelled(t *testing.T) {
	store := NewSagaStore()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	state := &adapters.SagaState{
		ID:   "saga-123",
		Type: "Test",
	}

	err := store.Save(ctx, state)

	assert.ErrorIs(t, err, context.Canceled)
}

func TestSagaStore_Load(t *testing.T) {
	store := NewSagaStore()
	ctx := context.Background()

	// Save a saga
	original := &adapters.SagaState{
		ID:            "saga-123",
		Type:          "OrderFulfillment",
		CorrelationID: "order-456",
		Status:        adapters.SagaStatusRunning,
		CurrentStep:   2,
		Data:          map[string]interface{}{"key": "value"},
		Steps: []adapters.SagaStep{
			{Name: "step1", Index: 0, Status: adapters.SagaStepCompleted},
		},
		StartedAt: time.Now(),
		Version:   0,
	}
	err := store.Save(ctx, original)
	require.NoError(t, err)

	// Load the saga
	loaded, err := store.Load(ctx, "saga-123")

	require.NoError(t, err)
	assert.Equal(t, original.ID, loaded.ID)
	assert.Equal(t, original.Type, loaded.Type)
	assert.Equal(t, original.CorrelationID, loaded.CorrelationID)
	assert.Equal(t, original.Status, loaded.Status)
	assert.Equal(t, original.CurrentStep, loaded.CurrentStep)
	assert.Equal(t, original.Data, loaded.Data)
	assert.Equal(t, len(original.Steps), len(loaded.Steps))
}

func TestSagaStore_Load_NotFound(t *testing.T) {
	store := NewSagaStore()
	ctx := context.Background()

	_, err := store.Load(ctx, "non-existent")

	assert.Error(t, err)
	assert.ErrorIs(t, err, adapters.ErrSagaNotFound)
}

func TestSagaStore_Load_EmptyID(t *testing.T) {
	store := NewSagaStore()
	ctx := context.Background()

	_, err := store.Load(ctx, "")

	assert.ErrorIs(t, err, adapters.ErrEmptyStreamID)
}

func TestSagaStore_Load_ContextCancelled(t *testing.T) {
	store := NewSagaStore()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := store.Load(ctx, "saga-123")

	assert.ErrorIs(t, err, context.Canceled)
}

func TestSagaStore_Load_ReturnsDeepCopy(t *testing.T) {
	store := NewSagaStore()
	ctx := context.Background()

	original := &adapters.SagaState{
		ID:        "saga-123",
		Type:      "Test",
		Data:      map[string]interface{}{"key": "original"},
		StartedAt: time.Now(),
	}
	err := store.Save(ctx, original)
	require.NoError(t, err)

	// Load and modify
	loaded, err := store.Load(ctx, "saga-123")
	require.NoError(t, err)
	loaded.Data["key"] = "modified"

	// Load again and verify original is unchanged
	reloaded, err := store.Load(ctx, "saga-123")
	require.NoError(t, err)
	assert.Equal(t, "original", reloaded.Data["key"])
}

func TestSagaStore_FindByCorrelationID(t *testing.T) {
	store := NewSagaStore()
	ctx := context.Background()

	// Save sagas with same correlation ID at different times
	older := &adapters.SagaState{
		ID:            "saga-1",
		Type:          "OrderFulfillment",
		CorrelationID: "order-456",
		StartedAt:     time.Now().Add(-time.Hour),
	}
	err := store.Save(ctx, older)
	require.NoError(t, err)

	newer := &adapters.SagaState{
		ID:            "saga-2",
		Type:          "OrderFulfillment",
		CorrelationID: "order-456",
		StartedAt:     time.Now(),
	}
	err = store.Save(ctx, newer)
	require.NoError(t, err)

	// Should return the most recent one
	found, err := store.FindByCorrelationID(ctx, "order-456")

	require.NoError(t, err)
	assert.Equal(t, "saga-2", found.ID)
}

func TestSagaStore_FindByCorrelationID_NotFound(t *testing.T) {
	store := NewSagaStore()
	ctx := context.Background()

	_, err := store.FindByCorrelationID(ctx, "non-existent")

	assert.Error(t, err)
	assert.ErrorIs(t, err, adapters.ErrSagaNotFound)
}

func TestSagaStore_FindByCorrelationID_EmptyID(t *testing.T) {
	store := NewSagaStore()
	ctx := context.Background()

	_, err := store.FindByCorrelationID(ctx, "")

	assert.ErrorIs(t, err, adapters.ErrEmptyStreamID)
}

func TestSagaStore_FindByType(t *testing.T) {
	store := NewSagaStore()
	ctx := context.Background()

	// Save sagas of different types and statuses
	sagas := []*adapters.SagaState{
		{ID: "saga-1", Type: "OrderFulfillment", Status: adapters.SagaStatusRunning, StartedAt: time.Now()},
		{ID: "saga-2", Type: "OrderFulfillment", Status: adapters.SagaStatusCompleted, StartedAt: time.Now()},
		{ID: "saga-3", Type: "OrderFulfillment", Status: adapters.SagaStatusFailed, StartedAt: time.Now()},
		{ID: "saga-4", Type: "OtherSaga", Status: adapters.SagaStatusRunning, StartedAt: time.Now()},
	}

	for _, s := range sagas {
		err := store.Save(ctx, s)
		require.NoError(t, err)
	}

	t.Run("all statuses", func(t *testing.T) {
		result, err := store.FindByType(ctx, "OrderFulfillment")

		require.NoError(t, err)
		assert.Len(t, result, 3)
	})

	t.Run("specific status", func(t *testing.T) {
		result, err := store.FindByType(ctx, "OrderFulfillment", adapters.SagaStatusRunning)

		require.NoError(t, err)
		assert.Len(t, result, 1)
		assert.Equal(t, "saga-1", result[0].ID)
	})

	t.Run("multiple statuses", func(t *testing.T) {
		result, err := store.FindByType(ctx, "OrderFulfillment", adapters.SagaStatusCompleted, adapters.SagaStatusFailed)

		require.NoError(t, err)
		assert.Len(t, result, 2)
	})

	t.Run("non-existent type", func(t *testing.T) {
		result, err := store.FindByType(ctx, "NonExistent")

		require.NoError(t, err)
		assert.Empty(t, result)
	})
}

func TestSagaStore_FindByType_EmptyType(t *testing.T) {
	store := NewSagaStore()
	ctx := context.Background()

	_, err := store.FindByType(ctx, "")

	assert.ErrorIs(t, err, adapters.ErrEmptyStreamID)
}

func TestSagaStore_Delete(t *testing.T) {
	store := NewSagaStore()
	ctx := context.Background()

	// Save a saga
	state := &adapters.SagaState{
		ID:        "saga-123",
		Type:      "Test",
		StartedAt: time.Now(),
	}
	err := store.Save(ctx, state)
	require.NoError(t, err)

	// Delete it
	err = store.Delete(ctx, "saga-123")

	require.NoError(t, err)
	assert.Equal(t, 0, store.Count())

	// Verify it's gone
	_, err = store.Load(ctx, "saga-123")
	assert.ErrorIs(t, err, adapters.ErrSagaNotFound)
}

func TestSagaStore_Delete_NotFound(t *testing.T) {
	store := NewSagaStore()
	ctx := context.Background()

	err := store.Delete(ctx, "non-existent")

	assert.Error(t, err)
	assert.ErrorIs(t, err, adapters.ErrSagaNotFound)
}

func TestSagaStore_Delete_EmptyID(t *testing.T) {
	store := NewSagaStore()
	ctx := context.Background()

	err := store.Delete(ctx, "")

	assert.ErrorIs(t, err, adapters.ErrEmptyStreamID)
}

func TestSagaStore_Close(t *testing.T) {
	store := NewSagaStore()

	err := store.Close()

	assert.NoError(t, err)
}

func TestSagaStore_Clear(t *testing.T) {
	store := NewSagaStore()
	ctx := context.Background()

	// Save some sagas
	for i := 0; i < 5; i++ {
		state := &adapters.SagaState{
			ID:        "saga-" + string(rune('0'+i)),
			Type:      "Test",
			StartedAt: time.Now(),
		}
		err := store.Save(ctx, state)
		require.NoError(t, err)
	}
	assert.Equal(t, 5, store.Count())

	// Clear
	store.Clear()

	assert.Equal(t, 0, store.Count())
}

func TestSagaStore_CountByStatus(t *testing.T) {
	store := NewSagaStore()
	ctx := context.Background()

	// Save sagas with different statuses
	statuses := []adapters.SagaStatus{
		adapters.SagaStatusRunning,
		adapters.SagaStatusRunning,
		adapters.SagaStatusCompleted,
		adapters.SagaStatusFailed,
	}

	for i, status := range statuses {
		state := &adapters.SagaState{
			ID:        "saga-" + string(rune('0'+i)),
			Type:      "Test",
			Status:    status,
			StartedAt: time.Now(),
		}
		err := store.Save(ctx, state)
		require.NoError(t, err)
	}

	counts, err := store.CountByStatus(ctx)

	require.NoError(t, err)
	assert.Equal(t, int64(2), counts[adapters.SagaStatusRunning])
	assert.Equal(t, int64(1), counts[adapters.SagaStatusCompleted])
	assert.Equal(t, int64(1), counts[adapters.SagaStatusFailed])
}

func TestSagaStore_All(t *testing.T) {
	store := NewSagaStore()
	ctx := context.Background()

	// Save some sagas
	for i := 0; i < 3; i++ {
		state := &adapters.SagaState{
			ID:        "saga-" + string(rune('0'+i)),
			Type:      "Test",
			StartedAt: time.Now(),
		}
		err := store.Save(ctx, state)
		require.NoError(t, err)
	}

	all := store.All()

	assert.Len(t, all, 3)
}

func TestSagaStore_Save_WithCompletedAt(t *testing.T) {
	store := NewSagaStore()
	ctx := context.Background()

	completedAt := time.Now()
	state := &adapters.SagaState{
		ID:          "saga-123",
		Type:        "Test",
		Status:      adapters.SagaStatusCompleted,
		CompletedAt: &completedAt,
		StartedAt:   time.Now().Add(-time.Hour),
	}

	err := store.Save(ctx, state)
	require.NoError(t, err)

	loaded, err := store.Load(ctx, "saga-123")
	require.NoError(t, err)
	assert.NotNil(t, loaded.CompletedAt)
	assert.Equal(t, completedAt.Unix(), loaded.CompletedAt.Unix())
}

// ====================================================================
// Additional Coverage Tests
// ====================================================================

func TestSagaStore_Save_NewSagaWithNonZeroVersion(t *testing.T) {
	store := NewSagaStore()
	ctx := context.Background()

	// Trying to save a "new" saga with version > 0 should fail
	state := &adapters.SagaState{
		ID:        "saga-nonzero",
		Type:      "Test",
		StartedAt: time.Now(),
		Version:   5, // Non-zero version for non-existent saga
	}

	err := store.Save(ctx, state)

	assert.Error(t, err)
	assert.ErrorIs(t, err, adapters.ErrSagaNotFound)
}

func TestSagaStore_Save_ContextCanceled(t *testing.T) {
	store := NewSagaStore()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	state := &adapters.SagaState{
		ID:        "saga-ctx",
		Type:      "Test",
		StartedAt: time.Now(),
	}

	err := store.Save(ctx, state)

	assert.ErrorIs(t, err, context.Canceled)
}

func TestSagaStore_Load_ContextCanceled(t *testing.T) {
	store := NewSagaStore()
	ctx := context.Background()

	// Save a saga first
	state := &adapters.SagaState{
		ID:        "saga-load-ctx",
		Type:      "Test",
		StartedAt: time.Now(),
	}
	err := store.Save(ctx, state)
	require.NoError(t, err)

	// Now try to load with canceled context
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = store.Load(canceledCtx, "saga-load-ctx")

	assert.ErrorIs(t, err, context.Canceled)
}

func TestSagaStore_FindByCorrelationID_ContextCanceled(t *testing.T) {
	store := NewSagaStore()
	ctx := context.Background()

	// Save a saga first
	state := &adapters.SagaState{
		ID:            "saga-find-ctx",
		Type:          "Test",
		CorrelationID: "corr-123",
		StartedAt:     time.Now(),
	}
	err := store.Save(ctx, state)
	require.NoError(t, err)

	// Now try to find with canceled context
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = store.FindByCorrelationID(canceledCtx, "corr-123")

	assert.ErrorIs(t, err, context.Canceled)
}

func TestSagaStore_FindByType_ContextCanceled(t *testing.T) {
	store := NewSagaStore()
	ctx := context.Background()

	// Save a saga first
	state := &adapters.SagaState{
		ID:        "saga-type-ctx",
		Type:      "TestType",
		StartedAt: time.Now(),
	}
	err := store.Save(ctx, state)
	require.NoError(t, err)

	// Now try to find by type with canceled context
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = store.FindByType(canceledCtx, "TestType")

	assert.ErrorIs(t, err, context.Canceled)
}

func TestSagaStore_Delete_ContextCanceled(t *testing.T) {
	store := NewSagaStore()
	ctx := context.Background()

	// Save a saga first
	state := &adapters.SagaState{
		ID:        "saga-delete-ctx",
		Type:      "Test",
		StartedAt: time.Now(),
	}
	err := store.Save(ctx, state)
	require.NoError(t, err)

	// Now try to delete with canceled context
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	err = store.Delete(canceledCtx, "saga-delete-ctx")

	assert.ErrorIs(t, err, context.Canceled)
}

func TestSagaStore_CountByStatus_ContextCanceled(t *testing.T) {
	store := NewSagaStore()
	ctx := context.Background()

	// Save a saga first
	state := &adapters.SagaState{
		ID:        "saga-count-ctx",
		Type:      "Test",
		Status:    adapters.SagaStatusRunning,
		StartedAt: time.Now(),
	}
	err := store.Save(ctx, state)
	require.NoError(t, err)

	// Now try to count with canceled context
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = store.CountByStatus(canceledCtx)

	assert.ErrorIs(t, err, context.Canceled)
}

func TestSagaStore_Save_WithSteps(t *testing.T) {
	store := NewSagaStore()
	ctx := context.Background()

	steps := []adapters.SagaStep{
		{Name: "Step1", Status: adapters.SagaStepCompleted},
		{Name: "Step2", Status: adapters.SagaStepPending},
	}

	state := &adapters.SagaState{
		ID:        "saga-steps",
		Type:      "Test",
		Steps:     steps,
		StartedAt: time.Now(),
	}

	err := store.Save(ctx, state)
	require.NoError(t, err)

	loaded, err := store.Load(ctx, "saga-steps")
	require.NoError(t, err)
	assert.Len(t, loaded.Steps, 2)
	assert.Equal(t, "Step1", loaded.Steps[0].Name)
	assert.Equal(t, adapters.SagaStepCompleted, loaded.Steps[0].Status)
}

func TestSagaStore_copyState_WithNilData(t *testing.T) {
	store := NewSagaStore()
	ctx := context.Background()

	state := &adapters.SagaState{
		ID:        "saga-nil-data",
		Type:      "Test",
		Data:      nil,
		StartedAt: time.Now(),
	}

	err := store.Save(ctx, state)
	require.NoError(t, err)

	loaded, err := store.Load(ctx, "saga-nil-data")
	require.NoError(t, err)
	assert.Nil(t, loaded.Data)
}

func TestSagaStore_copyState_WithNilSteps(t *testing.T) {
	store := NewSagaStore()
	ctx := context.Background()

	state := &adapters.SagaState{
		ID:        "saga-nil-steps",
		Type:      "Test",
		Steps:     nil,
		StartedAt: time.Now(),
	}

	err := store.Save(ctx, state)
	require.NoError(t, err)

	loaded, err := store.Load(ctx, "saga-nil-steps")
	require.NoError(t, err)
	assert.Nil(t, loaded.Steps)
}

func TestSagaStore_DeepCopy_NestedMaps(t *testing.T) {
	store := NewSagaStore()
	ctx := context.Background()

	// Create saga with nested maps and slices
	nestedMap := map[string]interface{}{
		"nested": "value",
	}
	nestedSlice := []interface{}{"item1", "item2"}

	original := &adapters.SagaState{
		ID:   "saga-nested",
		Type: "Test",
		Data: map[string]interface{}{
			"simple":      "value",
			"nestedMap":   nestedMap,
			"nestedSlice": nestedSlice,
		},
		StartedAt: time.Now(),
	}
	err := store.Save(ctx, original)
	require.NoError(t, err)

	// Load and modify nested structures
	loaded, err := store.Load(ctx, "saga-nested")
	require.NoError(t, err)

	// Modify nested map
	loadedNestedMap := loaded.Data["nestedMap"].(map[string]interface{})
	loadedNestedMap["nested"] = "modified"

	// Modify nested slice
	loadedNestedSlice := loaded.Data["nestedSlice"].([]interface{})
	loadedNestedSlice[0] = "modified"

	// Load again and verify original nested structures are unchanged
	reloaded, err := store.Load(ctx, "saga-nested")
	require.NoError(t, err)

	reloadedNestedMap := reloaded.Data["nestedMap"].(map[string]interface{})
	assert.Equal(t, "value", reloadedNestedMap["nested"], "Nested map should not be affected by modifications")

	reloadedNestedSlice := reloaded.Data["nestedSlice"].([]interface{})
	assert.Equal(t, "item1", reloadedNestedSlice[0], "Nested slice should not be affected by modifications")
}

func TestDeepCopyMap(t *testing.T) {
	original := map[string]interface{}{
		"string":  "value",
		"number":  42.0,
		"boolean": true,
		"nested": map[string]interface{}{
			"inner": "innerValue",
		},
		"slice": []interface{}{"a", "b"},
	}

	copied := deepCopyMap(original)

	// Verify values are equal
	assert.Equal(t, original["string"], copied["string"])
	assert.Equal(t, original["number"], copied["number"])
	assert.Equal(t, original["boolean"], copied["boolean"])

	// Modify original nested map
	originalNested := original["nested"].(map[string]interface{})
	originalNested["inner"] = "modified"

	// Verify copied nested map is unchanged
	copiedNested := copied["nested"].(map[string]interface{})
	assert.Equal(t, "innerValue", copiedNested["inner"])

	// Modify original slice
	originalSlice := original["slice"].([]interface{})
	originalSlice[0] = "modified"

	// Verify copied slice is unchanged
	copiedSlice := copied["slice"].([]interface{})
	assert.Equal(t, "a", copiedSlice[0])
}

func TestDeepCopyMap_Nil(t *testing.T) {
	result := deepCopyMap(nil)
	assert.Nil(t, result)
}

func TestDeepCopyValue_Primitives(t *testing.T) {
	assert.Equal(t, "string", deepCopyValue("string"))
	assert.Equal(t, 42, deepCopyValue(42))
	assert.Equal(t, 3.14, deepCopyValue(3.14))
	assert.Equal(t, true, deepCopyValue(true))
	assert.Nil(t, deepCopyValue(nil))
}

func TestDeepCopySlice(t *testing.T) {
	original := []interface{}{
		"string",
		42,
		map[string]interface{}{"key": "value"},
	}

	copied := deepCopySlice(original)

	// Verify values are equal
	assert.Equal(t, "string", copied[0])
	assert.Equal(t, 42, copied[1])

	// Modify original nested map in slice
	originalNested := original[2].(map[string]interface{})
	originalNested["key"] = "modified"

	// Verify copied nested map is unchanged
	copiedNested := copied[2].(map[string]interface{})
	assert.Equal(t, "value", copiedNested["key"])
}

func TestDeepCopySlice_Nil(t *testing.T) {
	result := deepCopySlice(nil)
	assert.Nil(t, result)
}
