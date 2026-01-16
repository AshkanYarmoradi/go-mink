package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/AshkanYarmoradi/go-mink"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test helper to get database URL
func getTestDatabaseURL(t *testing.T) string {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		// Default for docker-compose.test.yml
		url = "postgres://postgres:postgres@localhost:5432/mink_test?sslmode=disable"
	}
	return url
}

// Test helper to create a test SagaStore
func setupTestSagaStore(t *testing.T) (*SagaStore, func()) {
	t.Helper()

	if testing.Short() {
		t.Skip("Skipping PostgreSQL integration test")
	}

	url := getTestDatabaseURL(t)
	db, err := sql.Open("pgx", url)
	if err != nil {
		t.Skipf("Failed to connect to PostgreSQL: %v", err)
	}

	if err := db.Ping(); err != nil {
		db.Close()
		t.Skipf("PostgreSQL not available: %v", err)
	}

	// Use unique table for test isolation
	tableName := "mink_sagas_test_" + time.Now().Format("20060102150405")
	store := NewSagaStore(db, WithSagaTable(tableName))

	ctx := context.Background()
	if err := store.Initialize(ctx); err != nil {
		db.Close()
		t.Fatalf("Failed to initialize saga store: %v", err)
	}

	cleanup := func() {
		// Drop test table
		db.Exec("DROP TABLE IF EXISTS " + quoteQualifiedTable("public", tableName))
		db.Close()
	}

	return store, cleanup
}

// ====================================================================
// Option Tests
// ====================================================================

func TestWithSagaSchema(t *testing.T) {
	opt := WithSagaSchema("custom_schema")
	store := &SagaStore{}
	opt(store)
	assert.Equal(t, "custom_schema", store.schema)
}

func TestWithSagaTable(t *testing.T) {
	opt := WithSagaTable("custom_table")
	store := &SagaStore{}
	opt(store)
	assert.Equal(t, "custom_table", store.table)
}

// ====================================================================
// Constructor Tests
// ====================================================================

func TestNewSagaStore(t *testing.T) {
	db := &sql.DB{} // Mock DB for unit test
	store := NewSagaStore(db)

	assert.NotNil(t, store)
	assert.Equal(t, db, store.db)
	assert.Equal(t, "public", store.schema)
	assert.Equal(t, "mink_sagas", store.table)
}

func TestNewSagaStore_WithOptions(t *testing.T) {
	db := &sql.DB{}
	store := NewSagaStore(db,
		WithSagaSchema("custom"),
		WithSagaTable("my_sagas"),
	)

	assert.Equal(t, "custom", store.schema)
	assert.Equal(t, "my_sagas", store.table)
}

func TestNewSagaStoreFromAdapter(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping PostgreSQL integration test")
	}

	url := getTestDatabaseURL(t)
	adapter, err := NewAdapter(url, WithSchema("public"))
	if err != nil {
		t.Skipf("Failed to connect to PostgreSQL: %v", err)
	}
	defer adapter.Close()

	store := NewSagaStoreFromAdapter(adapter, WithSagaTable("test_sagas"))

	assert.NotNil(t, store)
	assert.Equal(t, "public", store.schema) // Inherited from adapter
	assert.Equal(t, "test_sagas", store.table)
}

// ====================================================================
// Initialize Tests
// ====================================================================

func TestSagaStore_Initialize(t *testing.T) {
	store, cleanup := setupTestSagaStore(t)
	defer cleanup()

	// Initialize is already called in setup, verify table exists
	ctx := context.Background()
	_, err := store.db.ExecContext(ctx, "SELECT 1 FROM "+store.fullTableName()+" LIMIT 1")
	assert.NoError(t, err)
}

func TestSagaStore_Initialize_InvalidSchema(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping PostgreSQL integration test")
	}

	url := getTestDatabaseURL(t)
	db, err := sql.Open("pgx", url)
	require.NoError(t, err)
	defer db.Close()

	store := NewSagaStore(db, WithSagaSchema("invalid;schema"))

	err = store.Initialize(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid characters")
}

func TestSagaStore_Initialize_InvalidTable(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping PostgreSQL integration test")
	}

	url := getTestDatabaseURL(t)
	db, err := sql.Open("pgx", url)
	require.NoError(t, err)
	defer db.Close()

	store := NewSagaStore(db, WithSagaTable("invalid;table"))

	err = store.Initialize(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid characters")
}

// ====================================================================
// Save Tests
// ====================================================================

func TestSagaStore_Save_NewSaga(t *testing.T) {
	store, cleanup := setupTestSagaStore(t)
	defer cleanup()

	ctx := context.Background()
	state := &mink.SagaState{
		ID:            "saga-123",
		Type:          "OrderSaga",
		CorrelationID: "order-456",
		Status:        mink.SagaStatusStarted,
		CurrentStep:   0,
		Data:          map[string]interface{}{"orderId": "order-456"},
		StartedAt:     time.Now(),
		Version:       0, // New saga
	}

	err := store.Save(ctx, state)
	assert.NoError(t, err)
	assert.Equal(t, int64(1), state.Version) // Version incremented
}

func TestSagaStore_Save_UpdateExisting(t *testing.T) {
	store, cleanup := setupTestSagaStore(t)
	defer cleanup()

	ctx := context.Background()

	// Create initial saga
	state := &mink.SagaState{
		ID:            "saga-123",
		Type:          "OrderSaga",
		CorrelationID: "order-456",
		Status:        mink.SagaStatusStarted,
		StartedAt:     time.Now(),
		Version:       0,
	}
	err := store.Save(ctx, state)
	require.NoError(t, err)
	assert.Equal(t, int64(1), state.Version)

	// Update saga
	state.Status = mink.SagaStatusRunning
	state.CurrentStep = 2
	err = store.Save(ctx, state)
	assert.NoError(t, err)
	assert.Equal(t, int64(2), state.Version)

	// Verify update
	loaded, err := store.Load(ctx, "saga-123")
	require.NoError(t, err)
	assert.Equal(t, mink.SagaStatusRunning, loaded.Status)
	assert.Equal(t, 2, loaded.CurrentStep)
}

func TestSagaStore_Save_ConcurrencyConflict(t *testing.T) {
	store, cleanup := setupTestSagaStore(t)
	defer cleanup()

	ctx := context.Background()

	// Create initial saga
	state := &mink.SagaState{
		ID:            "saga-123",
		Type:          "OrderSaga",
		CorrelationID: "order-456",
		Status:        mink.SagaStatusStarted,
		StartedAt:     time.Now(),
		Version:       0,
	}
	err := store.Save(ctx, state)
	require.NoError(t, err)

	// Simulate another process updating the saga
	stale := *state
	stale.Version = 1 // Current version

	// First update succeeds
	state.Status = mink.SagaStatusRunning
	err = store.Save(ctx, state)
	require.NoError(t, err)
	assert.Equal(t, int64(2), state.Version)

	// Second update with stale version fails
	stale.Status = mink.SagaStatusCompleted
	err = store.Save(ctx, &stale)
	assert.Error(t, err)
	assert.ErrorIs(t, err, mink.ErrConcurrencyConflict)
}

func TestSagaStore_Save_NilState(t *testing.T) {
	store, cleanup := setupTestSagaStore(t)
	defer cleanup()

	err := store.Save(context.Background(), nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "state is nil")
}

func TestSagaStore_Save_EmptyID(t *testing.T) {
	store, cleanup := setupTestSagaStore(t)
	defer cleanup()

	state := &mink.SagaState{
		ID:   "", // Empty ID
		Type: "OrderSaga",
	}

	err := store.Save(context.Background(), state)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "saga ID is required")
}

func TestSagaStore_Save_WithAllFields(t *testing.T) {
	store, cleanup := setupTestSagaStore(t)
	defer cleanup()

	ctx := context.Background()
	completed := time.Now()
	state := &mink.SagaState{
		ID:            "saga-full",
		Type:          "FullSaga",
		CorrelationID: "corr-123",
		Status:        mink.SagaStatusCompleted,
		CurrentStep:   5,
		Data: map[string]interface{}{
			"key1": "value1",
			"key2": float64(42),
		},
		Steps: []mink.SagaStep{
			{Name: "Step1", Status: mink.SagaStepCompleted},
			{Name: "Step2", Status: mink.SagaStepCompleted},
		},
		FailureReason: "test failure",
		StartedAt:     time.Now().Add(-time.Hour),
		CompletedAt:   &completed,
		Version:       0,
	}

	err := store.Save(ctx, state)
	require.NoError(t, err)

	// Load and verify
	loaded, err := store.Load(ctx, "saga-full")
	require.NoError(t, err)
	assert.Equal(t, state.ID, loaded.ID)
	assert.Equal(t, state.Type, loaded.Type)
	assert.Equal(t, state.CorrelationID, loaded.CorrelationID)
	assert.Equal(t, state.Status, loaded.Status)
	assert.Equal(t, state.CurrentStep, loaded.CurrentStep)
	assert.Equal(t, "value1", loaded.Data["key1"])
	assert.Equal(t, float64(42), loaded.Data["key2"])
	assert.Len(t, loaded.Steps, 2)
	assert.Equal(t, "Step1", loaded.Steps[0].Name)
	assert.Equal(t, state.FailureReason, loaded.FailureReason)
	assert.NotNil(t, loaded.CompletedAt)
}

// ====================================================================
// Load Tests
// ====================================================================

func TestSagaStore_Load(t *testing.T) {
	store, cleanup := setupTestSagaStore(t)
	defer cleanup()

	ctx := context.Background()

	// Create saga
	state := &mink.SagaState{
		ID:            "saga-load",
		Type:          "OrderSaga",
		CorrelationID: "order-789",
		Status:        mink.SagaStatusRunning,
		CurrentStep:   3,
		StartedAt:     time.Now(),
		Version:       0,
	}
	err := store.Save(ctx, state)
	require.NoError(t, err)

	// Load saga
	loaded, err := store.Load(ctx, "saga-load")
	require.NoError(t, err)
	assert.Equal(t, "saga-load", loaded.ID)
	assert.Equal(t, "OrderSaga", loaded.Type)
	assert.Equal(t, "order-789", loaded.CorrelationID)
	assert.Equal(t, mink.SagaStatusRunning, loaded.Status)
	assert.Equal(t, 3, loaded.CurrentStep)
	assert.Equal(t, int64(1), loaded.Version)
}

func TestSagaStore_Load_NotFound(t *testing.T) {
	store, cleanup := setupTestSagaStore(t)
	defer cleanup()

	ctx := context.Background()
	loaded, err := store.Load(ctx, "nonexistent")

	assert.Error(t, err)
	assert.Nil(t, loaded)

	var notFoundErr *mink.SagaNotFoundError
	assert.ErrorAs(t, err, &notFoundErr)
	assert.Equal(t, "nonexistent", notFoundErr.SagaID)
}

func TestSagaStore_Load_EmptyID(t *testing.T) {
	store, cleanup := setupTestSagaStore(t)
	defer cleanup()

	loaded, err := store.Load(context.Background(), "")
	assert.Error(t, err)
	assert.Nil(t, loaded)
	assert.Contains(t, err.Error(), "saga ID is required")
}

func TestSagaStore_Load_NullData(t *testing.T) {
	store, cleanup := setupTestSagaStore(t)
	defer cleanup()

	ctx := context.Background()

	// Create saga without data
	state := &mink.SagaState{
		ID:        "saga-null-data",
		Type:      "OrderSaga",
		Status:    mink.SagaStatusStarted,
		StartedAt: time.Now(),
		Version:   0,
		Data:      nil, // Nil data
	}
	err := store.Save(ctx, state)
	require.NoError(t, err)

	// Load saga
	loaded, err := store.Load(ctx, "saga-null-data")
	require.NoError(t, err)
	assert.Nil(t, loaded.Data)
}

// ====================================================================
// FindByCorrelationID Tests
// ====================================================================

func TestSagaStore_FindByCorrelationID(t *testing.T) {
	store, cleanup := setupTestSagaStore(t)
	defer cleanup()

	ctx := context.Background()

	// Create saga
	state := &mink.SagaState{
		ID:            "saga-corr",
		Type:          "OrderSaga",
		CorrelationID: "unique-correlation",
		Status:        mink.SagaStatusRunning,
		StartedAt:     time.Now(),
		Version:       0,
	}
	err := store.Save(ctx, state)
	require.NoError(t, err)

	// Find by correlation ID
	found, err := store.FindByCorrelationID(ctx, "unique-correlation")
	require.NoError(t, err)
	assert.Equal(t, "saga-corr", found.ID)
	assert.Equal(t, "unique-correlation", found.CorrelationID)
}

func TestSagaStore_FindByCorrelationID_NotFound(t *testing.T) {
	store, cleanup := setupTestSagaStore(t)
	defer cleanup()

	ctx := context.Background()
	found, err := store.FindByCorrelationID(ctx, "nonexistent-correlation")

	assert.Error(t, err)
	assert.Nil(t, found)

	var notFoundErr *mink.SagaNotFoundError
	assert.ErrorAs(t, err, &notFoundErr)
	assert.Equal(t, "nonexistent-correlation", notFoundErr.CorrelationID)
}

func TestSagaStore_FindByCorrelationID_EmptyID(t *testing.T) {
	store, cleanup := setupTestSagaStore(t)
	defer cleanup()

	found, err := store.FindByCorrelationID(context.Background(), "")
	assert.Error(t, err)
	assert.Nil(t, found)
	assert.Contains(t, err.Error(), "correlation ID is required")
}

func TestSagaStore_FindByCorrelationID_MultipleReturnsLatest(t *testing.T) {
	store, cleanup := setupTestSagaStore(t)
	defer cleanup()

	ctx := context.Background()

	// Create older saga
	older := &mink.SagaState{
		ID:            "saga-old",
		Type:          "OrderSaga",
		CorrelationID: "shared-correlation",
		Status:        mink.SagaStatusCompleted,
		StartedAt:     time.Now().Add(-time.Hour),
		Version:       0,
	}
	err := store.Save(ctx, older)
	require.NoError(t, err)

	// Create newer saga with same correlation
	time.Sleep(10 * time.Millisecond)
	newer := &mink.SagaState{
		ID:            "saga-new",
		Type:          "OrderSaga",
		CorrelationID: "shared-correlation",
		Status:        mink.SagaStatusRunning,
		StartedAt:     time.Now(),
		Version:       0,
	}
	err = store.Save(ctx, newer)
	require.NoError(t, err)

	// Find should return the latest (newest)
	found, err := store.FindByCorrelationID(ctx, "shared-correlation")
	require.NoError(t, err)
	assert.Equal(t, "saga-new", found.ID)
}

// ====================================================================
// FindByType Tests
// ====================================================================

func TestSagaStore_FindByType(t *testing.T) {
	store, cleanup := setupTestSagaStore(t)
	defer cleanup()

	ctx := context.Background()

	// Create multiple sagas of same type
	for i := 0; i < 3; i++ {
		state := &mink.SagaState{
			ID:        "order-saga-" + string(rune('a'+i)),
			Type:      "OrderSaga",
			Status:    mink.SagaStatusRunning,
			StartedAt: time.Now(),
			Version:   0,
		}
		err := store.Save(ctx, state)
		require.NoError(t, err)
	}

	// Create saga of different type
	other := &mink.SagaState{
		ID:        "payment-saga-1",
		Type:      "PaymentSaga",
		Status:    mink.SagaStatusRunning,
		StartedAt: time.Now(),
		Version:   0,
	}
	err := store.Save(ctx, other)
	require.NoError(t, err)

	// Find by type
	found, err := store.FindByType(ctx, "OrderSaga")
	require.NoError(t, err)
	assert.Len(t, found, 3)
	for _, s := range found {
		assert.Equal(t, "OrderSaga", s.Type)
	}
}

func TestSagaStore_FindByType_WithStatuses(t *testing.T) {
	store, cleanup := setupTestSagaStore(t)
	defer cleanup()

	ctx := context.Background()

	// Create sagas with different statuses
	statuses := []mink.SagaStatus{
		mink.SagaStatusStarted,
		mink.SagaStatusRunning,
		mink.SagaStatusCompleted,
		mink.SagaStatusFailed,
	}

	for i, status := range statuses {
		state := &mink.SagaState{
			ID:        "saga-" + string(rune('a'+i)),
			Type:      "TestSaga",
			Status:    status,
			StartedAt: time.Now(),
			Version:   0,
		}
		err := store.Save(ctx, state)
		require.NoError(t, err)
	}

	// Find only running and failed
	found, err := store.FindByType(ctx, "TestSaga", mink.SagaStatusRunning, mink.SagaStatusFailed)
	require.NoError(t, err)
	assert.Len(t, found, 2)

	foundStatuses := make(map[mink.SagaStatus]bool)
	for _, s := range found {
		foundStatuses[s.Status] = true
	}
	assert.True(t, foundStatuses[mink.SagaStatusRunning])
	assert.True(t, foundStatuses[mink.SagaStatusFailed])
}

func TestSagaStore_FindByType_EmptyType(t *testing.T) {
	store, cleanup := setupTestSagaStore(t)
	defer cleanup()

	found, err := store.FindByType(context.Background(), "")
	assert.Error(t, err)
	assert.Nil(t, found)
	assert.Contains(t, err.Error(), "saga type is required")
}

func TestSagaStore_FindByType_NoResults(t *testing.T) {
	store, cleanup := setupTestSagaStore(t)
	defer cleanup()

	ctx := context.Background()
	found, err := store.FindByType(ctx, "NonexistentSaga")
	require.NoError(t, err)
	assert.Empty(t, found)
}

// ====================================================================
// Delete Tests
// ====================================================================

func TestSagaStore_Delete(t *testing.T) {
	store, cleanup := setupTestSagaStore(t)
	defer cleanup()

	ctx := context.Background()

	// Create saga
	state := &mink.SagaState{
		ID:        "saga-to-delete",
		Type:      "OrderSaga",
		Status:    mink.SagaStatusCompleted,
		StartedAt: time.Now(),
		Version:   0,
	}
	err := store.Save(ctx, state)
	require.NoError(t, err)

	// Verify exists
	_, err = store.Load(ctx, "saga-to-delete")
	require.NoError(t, err)

	// Delete
	err = store.Delete(ctx, "saga-to-delete")
	require.NoError(t, err)

	// Verify deleted
	_, err = store.Load(ctx, "saga-to-delete")
	assert.Error(t, err)
	var notFoundErr *mink.SagaNotFoundError
	assert.ErrorAs(t, err, &notFoundErr)
}

func TestSagaStore_Delete_NotFound(t *testing.T) {
	store, cleanup := setupTestSagaStore(t)
	defer cleanup()

	ctx := context.Background()
	err := store.Delete(ctx, "nonexistent-saga")

	assert.Error(t, err)
	var notFoundErr *mink.SagaNotFoundError
	assert.ErrorAs(t, err, &notFoundErr)
}

func TestSagaStore_Delete_EmptyID(t *testing.T) {
	store, cleanup := setupTestSagaStore(t)
	defer cleanup()

	err := store.Delete(context.Background(), "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "saga ID is required")
}

// ====================================================================
// Cleanup Tests
// ====================================================================

func TestSagaStore_Cleanup(t *testing.T) {
	store, cleanup := setupTestSagaStore(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now()
	oldTime := now.Add(-48 * time.Hour)

	// Create old completed saga
	oldCompleted := &mink.SagaState{
		ID:          "old-completed",
		Type:        "TestSaga",
		Status:      mink.SagaStatusCompleted,
		StartedAt:   oldTime,
		CompletedAt: &oldTime,
		Version:     0,
	}
	err := store.Save(ctx, oldCompleted)
	require.NoError(t, err)

	// Create old failed saga
	oldFailed := &mink.SagaState{
		ID:          "old-failed",
		Type:        "TestSaga",
		Status:      mink.SagaStatusFailed,
		StartedAt:   oldTime,
		CompletedAt: &oldTime,
		Version:     0,
	}
	err = store.Save(ctx, oldFailed)
	require.NoError(t, err)

	// Create recent completed saga
	recentTime := now.Add(-1 * time.Hour)
	recentCompleted := &mink.SagaState{
		ID:          "recent-completed",
		Type:        "TestSaga",
		Status:      mink.SagaStatusCompleted,
		StartedAt:   recentTime,
		CompletedAt: &recentTime,
		Version:     0,
	}
	err = store.Save(ctx, recentCompleted)
	require.NoError(t, err)

	// Create running saga (should not be cleaned up)
	running := &mink.SagaState{
		ID:        "running",
		Type:      "TestSaga",
		Status:    mink.SagaStatusRunning,
		StartedAt: oldTime, // Old but still running
		Version:   0,
	}
	err = store.Save(ctx, running)
	require.NoError(t, err)

	// Cleanup sagas older than 24 hours
	deleted, err := store.Cleanup(ctx, 24*time.Hour)
	require.NoError(t, err)
	assert.Equal(t, int64(2), deleted) // old-completed and old-failed

	// Verify old sagas are gone
	_, err = store.Load(ctx, "old-completed")
	assert.Error(t, err)

	_, err = store.Load(ctx, "old-failed")
	assert.Error(t, err)

	// Verify recent and running sagas still exist
	_, err = store.Load(ctx, "recent-completed")
	assert.NoError(t, err)

	_, err = store.Load(ctx, "running")
	assert.NoError(t, err)
}

func TestSagaStore_Cleanup_NoMatches(t *testing.T) {
	store, cleanup := setupTestSagaStore(t)
	defer cleanup()

	ctx := context.Background()

	// Create only running saga
	state := &mink.SagaState{
		ID:        "running-only",
		Type:      "TestSaga",
		Status:    mink.SagaStatusRunning,
		StartedAt: time.Now(),
		Version:   0,
	}
	err := store.Save(ctx, state)
	require.NoError(t, err)

	// Cleanup should not delete anything
	deleted, err := store.Cleanup(ctx, 24*time.Hour)
	require.NoError(t, err)
	assert.Equal(t, int64(0), deleted)
}

// ====================================================================
// CountByStatus Tests
// ====================================================================

func TestSagaStore_CountByStatus(t *testing.T) {
	store, cleanup := setupTestSagaStore(t)
	defer cleanup()

	ctx := context.Background()

	// Create sagas with different statuses
	statuses := map[string]mink.SagaStatus{
		"s1": mink.SagaStatusStarted,
		"s2": mink.SagaStatusRunning,
		"s3": mink.SagaStatusRunning,
		"s4": mink.SagaStatusCompleted,
		"s5": mink.SagaStatusCompleted,
		"s6": mink.SagaStatusCompleted,
		"s7": mink.SagaStatusFailed,
	}

	for id, status := range statuses {
		state := &mink.SagaState{
			ID:        "count-" + id,
			Type:      "TestSaga",
			Status:    status,
			StartedAt: time.Now(),
			Version:   0,
		}
		err := store.Save(ctx, state)
		require.NoError(t, err)
	}

	// Count by status
	counts, err := store.CountByStatus(ctx)
	require.NoError(t, err)

	assert.Equal(t, int64(1), counts[mink.SagaStatusStarted])
	assert.Equal(t, int64(2), counts[mink.SagaStatusRunning])
	assert.Equal(t, int64(3), counts[mink.SagaStatusCompleted])
	assert.Equal(t, int64(1), counts[mink.SagaStatusFailed])
}

func TestSagaStore_CountByStatus_Empty(t *testing.T) {
	store, cleanup := setupTestSagaStore(t)
	defer cleanup()

	ctx := context.Background()
	counts, err := store.CountByStatus(ctx)
	require.NoError(t, err)
	assert.Empty(t, counts)
}

// ====================================================================
// Close Tests
// ====================================================================

func TestSagaStore_Close(t *testing.T) {
	store, cleanup := setupTestSagaStore(t)
	defer cleanup()

	// Close should not error (no-op for shared DB)
	err := store.Close()
	assert.NoError(t, err)
}

// ====================================================================
// Helper Function Tests
// ====================================================================

func TestNullString(t *testing.T) {
	tests := []struct {
		input    string
		expected sql.NullString
	}{
		{"", sql.NullString{String: "", Valid: false}},
		{"test", sql.NullString{String: "test", Valid: true}},
	}

	for _, tt := range tests {
		result := nullString(tt.input)
		assert.Equal(t, tt.expected, result)
	}
}

func TestNullTime(t *testing.T) {
	// Nil time
	result := nullTime(nil)
	assert.False(t, result.Valid)

	// Valid time
	now := time.Now()
	result = nullTime(&now)
	assert.True(t, result.Valid)
	assert.Equal(t, now, result.Time)
}

func TestJoinStrings(t *testing.T) {
	tests := []struct {
		input    []string
		sep      string
		expected string
	}{
		{[]string{}, ", ", ""},
		{[]string{"a"}, ", ", "a"},
		{[]string{"a", "b", "c"}, ", ", "a, b, c"},
		{[]string{"$1", "$2"}, " OR ", "$1 OR $2"},
	}

	for _, tt := range tests {
		result := joinStrings(tt.input, tt.sep)
		assert.Equal(t, tt.expected, result)
	}
}

// ====================================================================
// Concurrent Access Tests
// ====================================================================

func TestSagaStore_ConcurrentSaves(t *testing.T) {
	store, cleanup := setupTestSagaStore(t)
	defer cleanup()

	ctx := context.Background()

	// Create initial saga
	state := &mink.SagaState{
		ID:        "concurrent-saga",
		Type:      "TestSaga",
		Status:    mink.SagaStatusStarted,
		StartedAt: time.Now(),
		Version:   0,
	}
	err := store.Save(ctx, state)
	require.NoError(t, err)

	// Concurrent updates should result in conflicts
	results := make(chan error, 10)
	for i := 0; i < 10; i++ {
		go func(idx int) {
			// Each goroutine loads the same state and tries to update
			loaded, err := store.Load(ctx, "concurrent-saga")
			if err != nil {
				results <- err
				return
			}
			loaded.CurrentStep = idx
			results <- store.Save(ctx, loaded)
		}(i)
	}

	// Collect results
	successCount := 0
	conflictCount := 0
	for i := 0; i < 10; i++ {
		err := <-results
		if err == nil {
			successCount++
		} else if err != nil && err.Error() != "" {
			conflictCount++
		}
	}

	// At least some should succeed, some should conflict
	assert.Greater(t, successCount, 0, "At least one save should succeed")
	t.Logf("Success: %d, Conflicts: %d", successCount, conflictCount)
}

func TestSagaStore_fullTableName(t *testing.T) {
	store := NewSagaStore(&sql.DB{},
		WithSagaSchema("custom_schema"),
		WithSagaTable("custom_table"),
	)

	name := store.fullTableName()
	assert.Equal(t, `"custom_schema"."custom_table"`, name)
}

// ====================================================================
// Additional Coverage Tests
// ====================================================================

func TestSagaStore_Load_WithSteps(t *testing.T) {
	store, cleanup := setupTestSagaStore(t)
	defer cleanup()

	ctx := context.Background()

	// Create saga with steps
	completedAt := time.Now()
	state := &mink.SagaState{
		ID:            "saga-with-steps",
		Type:          "OrderSaga",
		CorrelationID: "corr-123",
		Status:        mink.SagaStatusCompleted,
		CurrentStep:   3,
		StartedAt:     time.Now().Add(-time.Hour),
		CompletedAt:   &completedAt,
		FailureReason: "test failure",
		Version:       0,
		Data: map[string]interface{}{
			"orderId": "order-123",
			"amount":  99.99,
		},
		Steps: []mink.SagaStep{
			{Name: "step1", Index: 0, Status: mink.SagaStepCompleted, CompletedAt: &completedAt},
			{Name: "step2", Index: 1, Status: mink.SagaStepCompleted, CompletedAt: &completedAt},
			{Name: "step3", Index: 2, Status: mink.SagaStepFailed, Error: "step failed"},
		},
	}
	err := store.Save(ctx, state)
	require.NoError(t, err)

	// Load and verify
	loaded, err := store.Load(ctx, "saga-with-steps")
	require.NoError(t, err)

	assert.Equal(t, "saga-with-steps", loaded.ID)
	assert.Equal(t, "OrderSaga", loaded.Type)
	assert.Equal(t, "corr-123", loaded.CorrelationID)
	assert.Equal(t, mink.SagaStatusCompleted, loaded.Status)
	assert.Equal(t, 3, loaded.CurrentStep)
	assert.Equal(t, "test failure", loaded.FailureReason)
	assert.NotNil(t, loaded.CompletedAt)
	assert.NotNil(t, loaded.Data)
	assert.Equal(t, "order-123", loaded.Data["orderId"])
	assert.Len(t, loaded.Steps, 3)
	assert.Equal(t, "step1", loaded.Steps[0].Name)
	assert.Equal(t, mink.SagaStepFailed, loaded.Steps[2].Status)
}

func TestSagaStore_FindByCorrelationID_WithSteps(t *testing.T) {
	store, cleanup := setupTestSagaStore(t)
	defer cleanup()

	ctx := context.Background()

	// Create saga with data and steps
	completedAt := time.Now()
	state := &mink.SagaState{
		ID:            "saga-find-steps",
		Type:          "OrderSaga",
		CorrelationID: "find-corr-123",
		Status:        mink.SagaStatusRunning,
		StartedAt:     time.Now(),
		Version:       0,
		Data: map[string]interface{}{
			"key": "value",
		},
		Steps: []mink.SagaStep{
			{Name: "step1", Index: 0, Status: mink.SagaStepCompleted, CompletedAt: &completedAt},
		},
	}
	err := store.Save(ctx, state)
	require.NoError(t, err)

	// Find by correlation ID
	found, err := store.FindByCorrelationID(ctx, "find-corr-123")
	require.NoError(t, err)

	assert.Equal(t, "saga-find-steps", found.ID)
	assert.NotNil(t, found.Data)
	assert.Equal(t, "value", found.Data["key"])
	assert.Len(t, found.Steps, 1)
}

func TestSagaStore_FindByType_WithDataAndSteps(t *testing.T) {
	store, cleanup := setupTestSagaStore(t)
	defer cleanup()

	ctx := context.Background()

	completedAt := time.Now()

	// Create multiple sagas
	for i := 0; i < 3; i++ {
		state := &mink.SagaState{
			ID:        fmt.Sprintf("data-test-saga-%d", i),
			Type:      "DataTestSaga",
			Status:    mink.SagaStatus(i % 3), // Mix of statuses
			StartedAt: time.Now(),
			Version:   0,
			Data: map[string]interface{}{
				"index": i,
			},
			Steps: []mink.SagaStep{
				{Name: "step1", Index: 0, Status: mink.SagaStepCompleted, CompletedAt: &completedAt},
			},
		}
		err := store.Save(ctx, state)
		require.NoError(t, err)
	}

	// Find all
	sagas, err := store.FindByType(ctx, "DataTestSaga")
	require.NoError(t, err)
	assert.Len(t, sagas, 3)

	// All should have data and steps
	for _, saga := range sagas {
		assert.NotNil(t, saga.Data)
		assert.Len(t, saga.Steps, 1)
	}
}

func TestSagaStore_Cleanup_ReturnsCount(t *testing.T) {
	store, cleanup := setupTestSagaStore(t)
	defer cleanup()

	ctx := context.Background()

	completedAt := time.Now().Add(-48 * time.Hour)

	// Create old completed sagas
	for i := 0; i < 5; i++ {
		state := &mink.SagaState{
			ID:          fmt.Sprintf("cleanup-saga-%d", i),
			Type:        "CleanupSaga",
			Status:      mink.SagaStatusCompleted,
			StartedAt:   time.Now().Add(-72 * time.Hour),
			CompletedAt: &completedAt,
			Version:     0,
		}
		err := store.Save(ctx, state)
		require.NoError(t, err)
	}

	// Cleanup sagas older than 24 hours
	deleted, err := store.Cleanup(ctx, 24*time.Hour)
	require.NoError(t, err)
	assert.Equal(t, int64(5), deleted)
}

func TestSagaStore_CountByStatus_EmptyTable(t *testing.T) {
	store, cleanup := setupTestSagaStore(t)
	defer cleanup()

	ctx := context.Background()

	counts, err := store.CountByStatus(ctx)
	require.NoError(t, err)
	assert.Empty(t, counts)
}

func TestSagaStore_CountByStatus_MultipleSagas(t *testing.T) {
	store, cleanup := setupTestSagaStore(t)
	defer cleanup()

	ctx := context.Background()

	// Create sagas with various statuses
	statuses := []mink.SagaStatus{
		mink.SagaStatusStarted,
		mink.SagaStatusStarted,
		mink.SagaStatusRunning,
		mink.SagaStatusCompleted,
		mink.SagaStatusCompleted,
		mink.SagaStatusCompleted,
		mink.SagaStatusFailed,
	}

	for i, status := range statuses {
		state := &mink.SagaState{
			ID:        fmt.Sprintf("count-saga-%d", i),
			Type:      "CountSaga",
			Status:    status,
			StartedAt: time.Now().Add(time.Duration(i) * time.Millisecond),
			Version:   0,
		}
		err := store.Save(ctx, state)
		require.NoError(t, err)
	}

	counts, err := store.CountByStatus(ctx)
	require.NoError(t, err)

	assert.Equal(t, int64(2), counts[mink.SagaStatusStarted])
	assert.Equal(t, int64(1), counts[mink.SagaStatusRunning])
	assert.Equal(t, int64(3), counts[mink.SagaStatusCompleted])
	assert.Equal(t, int64(1), counts[mink.SagaStatusFailed])
}

func TestSagaStore_Close_Additional(t *testing.T) {
	store := NewSagaStore(&sql.DB{})
	err := store.Close()
	assert.NoError(t, err)
}

func TestSagaStore_Save_ContextCanceled(t *testing.T) {
	store, cleanup := setupTestSagaStore(t)
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	state := &mink.SagaState{
		ID:        "context-canceled",
		Type:      "TestSaga",
		Status:    mink.SagaStatusStarted,
		StartedAt: time.Now(),
		Version:   0,
	}

	err := store.Save(ctx, state)
	assert.Error(t, err)
}

func TestSagaStore_Load_ContextCanceled(t *testing.T) {
	store, cleanup := setupTestSagaStore(t)
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	_, err := store.Load(ctx, "some-id")
	assert.Error(t, err)
}

func TestSagaStore_FindByCorrelationID_ContextCanceled(t *testing.T) {
	store, cleanup := setupTestSagaStore(t)
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	_, err := store.FindByCorrelationID(ctx, "some-corr-id")
	assert.Error(t, err)
}

func TestSagaStore_FindByType_ContextCanceled(t *testing.T) {
	store, cleanup := setupTestSagaStore(t)
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	_, err := store.FindByType(ctx, "TestSaga")
	assert.Error(t, err)
}

func TestSagaStore_Delete_ContextCanceled(t *testing.T) {
	store, cleanup := setupTestSagaStore(t)
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	err := store.Delete(ctx, "some-id")
	assert.Error(t, err)
}

func TestSagaStore_Cleanup_ContextCanceled(t *testing.T) {
	store, cleanup := setupTestSagaStore(t)
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	_, err := store.Cleanup(ctx, 24*time.Hour)
	assert.Error(t, err)
}

func TestSagaStore_CountByStatus_ContextCanceled(t *testing.T) {
	store, cleanup := setupTestSagaStore(t)
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	_, err := store.CountByStatus(ctx)
	assert.Error(t, err)
}

func TestSagaStore_Initialize_ContextCanceled(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping PostgreSQL integration test")
	}

	url := getTestDatabaseURL(t)
	db, err := sql.Open("pgx", url)
	require.NoError(t, err)
	defer db.Close()

	store := NewSagaStore(db, WithSagaTable("test_init_ctx"))

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	err = store.Initialize(ctx)
	assert.Error(t, err)
}

func TestSagaStore_FindByType_EmptyType_Additional(t *testing.T) {
	store, cleanup := setupTestSagaStore(t)
	defer cleanup()

	ctx := context.Background()
	_, err := store.FindByType(ctx, "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "saga type is required")
}

func TestSagaStore_Delete_EmptyID_Additional(t *testing.T) {
	store, cleanup := setupTestSagaStore(t)
	defer cleanup()

	ctx := context.Background()
	err := store.Delete(ctx, "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "saga ID is required")
}

func TestSagaStore_Cleanup_NoMatchingSagas(t *testing.T) {
	store, cleanup := setupTestSagaStore(t)
	defer cleanup()

	ctx := context.Background()

	// Create only running sagas (not completed/failed)
	state := &mink.SagaState{
		ID:        "running-saga",
		Type:      "TestSaga",
		Status:    mink.SagaStatusRunning,
		StartedAt: time.Now(),
		Version:   0,
	}
	err := store.Save(ctx, state)
	require.NoError(t, err)

	// Cleanup should not delete running sagas
	deleted, err := store.Cleanup(ctx, 0)
	require.NoError(t, err)
	assert.Equal(t, int64(0), deleted)
}

func TestSagaStore_Save_WithEmptyData(t *testing.T) {
	store, cleanup := setupTestSagaStore(t)
	defer cleanup()

	ctx := context.Background()

	state := &mink.SagaState{
		ID:        "empty-data-saga",
		Type:      "TestSaga",
		Status:    mink.SagaStatusStarted,
		StartedAt: time.Now(),
		Version:   0,
		Data:      map[string]interface{}{}, // Empty but not nil
		Steps:     []mink.SagaStep{},        // Empty but not nil
	}
	err := store.Save(ctx, state)
	require.NoError(t, err)

	loaded, err := store.Load(ctx, "empty-data-saga")
	require.NoError(t, err)
	assert.Empty(t, loaded.Data)
	assert.Empty(t, loaded.Steps)
}

func TestSagaStore_Cleanup_WithFailedAndCompensated(t *testing.T) {
	store, cleanup := setupTestSagaStore(t)
	defer cleanup()

	ctx := context.Background()
	completedAt := time.Now().Add(-48 * time.Hour)

	// Create old failed saga
	failed := &mink.SagaState{
		ID:          "failed-saga",
		Type:        "TestSaga",
		Status:      mink.SagaStatusFailed,
		StartedAt:   time.Now().Add(-72 * time.Hour),
		CompletedAt: &completedAt,
		Version:     0,
	}
	err := store.Save(ctx, failed)
	require.NoError(t, err)

	// Create old compensated saga
	compensated := &mink.SagaState{
		ID:          "compensated-saga",
		Type:        "TestSaga",
		Status:      mink.SagaStatusCompensated,
		StartedAt:   time.Now().Add(-72 * time.Hour),
		CompletedAt: &completedAt,
		Version:     0,
	}
	err = store.Save(ctx, compensated)
	require.NoError(t, err)

	// Cleanup should delete both
	deleted, err := store.Cleanup(ctx, 24*time.Hour)
	require.NoError(t, err)
	assert.Equal(t, int64(2), deleted)
}

func TestSagaStore_Save_ConcurrencyConflict_WrongVersion(t *testing.T) {
	store, cleanup := setupTestSagaStore(t)
	defer cleanup()

	ctx := context.Background()

	// Create initial saga
	state := &mink.SagaState{
		ID:        "conflict-saga",
		Type:      "TestSaga",
		Status:    mink.SagaStatusStarted,
		StartedAt: time.Now(),
		Version:   0,
	}
	err := store.Save(ctx, state)
	require.NoError(t, err)
	assert.Equal(t, int64(1), state.Version)

	// Try to save with wrong version (should conflict)
	state.Version = 999 // Wrong version
	state.Status = mink.SagaStatusRunning
	err = store.Save(ctx, state)
	assert.Error(t, err)
	assert.ErrorIs(t, err, mink.ErrConcurrencyConflict)
}

func TestSagaStore_FindByType_WithStatusFilter(t *testing.T) {
	store, cleanup := setupTestSagaStore(t)
	defer cleanup()

	ctx := context.Background()

	// Create sagas with different statuses
	statuses := []mink.SagaStatus{
		mink.SagaStatusStarted,
		mink.SagaStatusRunning,
		mink.SagaStatusRunning,
		mink.SagaStatusCompleted,
		mink.SagaStatusFailed,
	}

	for i, status := range statuses {
		state := &mink.SagaState{
			ID:        fmt.Sprintf("filter-saga-%d", i),
			Type:      "FilterTestSaga",
			Status:    status,
			StartedAt: time.Now(),
			Version:   0,
		}
		err := store.Save(ctx, state)
		require.NoError(t, err)
	}

	// Find only running sagas
	running, err := store.FindByType(ctx, "FilterTestSaga", mink.SagaStatusRunning)
	require.NoError(t, err)
	assert.Len(t, running, 2)

	// Find multiple statuses
	startedAndRunning, err := store.FindByType(ctx, "FilterTestSaga", mink.SagaStatusStarted, mink.SagaStatusRunning)
	require.NoError(t, err)
	assert.Len(t, startedAndRunning, 3)
}

func TestSagaStore_Save_NilState_Additional(t *testing.T) {
	store, cleanup := setupTestSagaStore(t)
	defer cleanup()

	ctx := context.Background()

	err := store.Save(ctx, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "state is nil")
}

func TestSagaStore_Load_LargeData(t *testing.T) {
	store, cleanup := setupTestSagaStore(t)
	defer cleanup()

	ctx := context.Background()

	// Create saga with large data map
	data := make(map[string]interface{})
	for i := 0; i < 100; i++ {
		data[fmt.Sprintf("key%d", i)] = fmt.Sprintf("value%d", i)
	}

	state := &mink.SagaState{
		ID:        "large-data-saga",
		Type:      "TestSaga",
		Status:    mink.SagaStatusStarted,
		StartedAt: time.Now(),
		Version:   0,
		Data:      data,
	}
	err := store.Save(ctx, state)
	require.NoError(t, err)

	// Load and verify all data is preserved
	loaded, err := store.Load(ctx, "large-data-saga")
	require.NoError(t, err)
	assert.Len(t, loaded.Data, 100)
	assert.Equal(t, "value50", loaded.Data["key50"])
}

func TestSagaStore_FindByCorrelationID_NoResult(t *testing.T) {
	store, cleanup := setupTestSagaStore(t)
	defer cleanup()

	ctx := context.Background()

	_, err := store.FindByCorrelationID(ctx, "non-existent-correlation")
	assert.Error(t, err)
	// Should be SagaNotFoundError
	var notFoundErr *mink.SagaNotFoundError
	assert.ErrorAs(t, err, &notFoundErr)
}

func TestSagaStore_Update_ExistingSaga(t *testing.T) {
	store, cleanup := setupTestSagaStore(t)
	defer cleanup()

	ctx := context.Background()

	// Create initial saga
	state := &mink.SagaState{
		ID:        "update-saga",
		Type:      "TestSaga",
		Status:    mink.SagaStatusStarted,
		StartedAt: time.Now(),
		Version:   0,
		Data: map[string]interface{}{
			"step": 1,
		},
	}
	err := store.Save(ctx, state)
	require.NoError(t, err)
	assert.Equal(t, int64(1), state.Version)

	// Update saga
	state.Status = mink.SagaStatusRunning
	state.CurrentStep = 2
	state.Data["step"] = 2
	err = store.Save(ctx, state)
	require.NoError(t, err)
	assert.Equal(t, int64(2), state.Version)

	// Verify updates
	loaded, err := store.Load(ctx, "update-saga")
	require.NoError(t, err)
	assert.Equal(t, mink.SagaStatusRunning, loaded.Status)
	assert.Equal(t, 2, loaded.CurrentStep)
	assert.Equal(t, float64(2), loaded.Data["step"]) // JSON numbers are float64
}

func TestSagaStore_Delete_Nonexistent(t *testing.T) {
	store, cleanup := setupTestSagaStore(t)
	defer cleanup()

	ctx := context.Background()

	err := store.Delete(ctx, "does-not-exist")
	assert.Error(t, err)
	var notFoundErr *mink.SagaNotFoundError
	assert.ErrorAs(t, err, &notFoundErr)
}

func TestSagaStore_FindByType_EmptyResults(t *testing.T) {
	store, cleanup := setupTestSagaStore(t)
	defer cleanup()

	ctx := context.Background()

	// Find by type that doesn't exist
	sagas, err := store.FindByType(ctx, "NonExistentType")
	require.NoError(t, err)
	assert.Empty(t, sagas)
}

func TestSagaStore_CountByStatus_AllStatuses(t *testing.T) {
	store, cleanup := setupTestSagaStore(t)
	defer cleanup()

	ctx := context.Background()

	// Create sagas with all statuses
	allStatuses := []mink.SagaStatus{
		mink.SagaStatusStarted,
		mink.SagaStatusRunning,
		mink.SagaStatusCompleted,
		mink.SagaStatusFailed,
		mink.SagaStatusCompensating,
		mink.SagaStatusCompensated,
	}

	for i, status := range allStatuses {
		state := &mink.SagaState{
			ID:        fmt.Sprintf("all-status-saga-%d", i),
			Type:      "AllStatusSaga",
			Status:    status,
			StartedAt: time.Now(),
			Version:   0,
		}
		err := store.Save(ctx, state)
		require.NoError(t, err)
	}

	counts, err := store.CountByStatus(ctx)
	require.NoError(t, err)

	// Should have exactly one of each status
	assert.Equal(t, int64(1), counts[mink.SagaStatusStarted])
	assert.Equal(t, int64(1), counts[mink.SagaStatusRunning])
	assert.Equal(t, int64(1), counts[mink.SagaStatusCompleted])
	assert.Equal(t, int64(1), counts[mink.SagaStatusFailed])
	assert.Equal(t, int64(1), counts[mink.SagaStatusCompensating])
	assert.Equal(t, int64(1), counts[mink.SagaStatusCompensated])
}

func TestSagaStore_Cleanup_OnlyCompleted(t *testing.T) {
	store, cleanup := setupTestSagaStore(t)
	defer cleanup()

	ctx := context.Background()
	completedAt := time.Now().Add(-48 * time.Hour)

	// Create old saga in different states
	states := []struct {
		id     string
		status mink.SagaStatus
	}{
		{"old-completed", mink.SagaStatusCompleted},
		{"old-running", mink.SagaStatusRunning}, // Should NOT be cleaned
		{"old-started", mink.SagaStatusStarted}, // Should NOT be cleaned
	}

	for _, s := range states {
		state := &mink.SagaState{
			ID:        s.id,
			Type:      "CleanupTestSaga",
			Status:    s.status,
			StartedAt: time.Now().Add(-72 * time.Hour),
			Version:   0,
		}
		if s.status == mink.SagaStatusCompleted {
			state.CompletedAt = &completedAt
		}
		err := store.Save(ctx, state)
		require.NoError(t, err)
	}

	// Cleanup should only delete completed ones
	deleted, err := store.Cleanup(ctx, 24*time.Hour)
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)

	// Verify running and started still exist
	_, err = store.Load(ctx, "old-running")
	assert.NoError(t, err)
	_, err = store.Load(ctx, "old-started")
	assert.NoError(t, err)
}
