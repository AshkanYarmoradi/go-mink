package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/AshkanYarmoradi/go-mink/adapters"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupIdempotencyTestStore(t *testing.T) *IdempotencyStore {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	connStr := os.Getenv("TEST_DATABASE_URL")
	if connStr == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}

	adapter, err := NewAdapter(connStr)
	require.NoError(t, err)
	t.Cleanup(func() { _ = adapter.Close() })

	store := NewIdempotencyStoreFromAdapter(adapter)

	// Initialize table
	err = store.Initialize(context.Background())
	require.NoError(t, err)

	// Clear any existing data
	err = store.Clear(context.Background())
	require.NoError(t, err)

	return store
}

func TestIdempotencyStore_Initialize(t *testing.T) {
	store := setupIdempotencyTestStore(t)
	ctx := context.Background()

	// Table should already be initialized by setupIdempotencyTestStore
	// Calling Initialize again should not error (idempotent)
	err := store.Initialize(ctx)
	require.NoError(t, err)
}

func TestIdempotencyStore_PostgreSQL_Store(t *testing.T) {
	store := setupIdempotencyTestStore(t)
	ctx := context.Background()

	t.Run("stores record successfully", func(t *testing.T) {
		record := &adapters.IdempotencyRecord{
			Key:         "pg-test-key-1",
			CommandType: "TestCommand",
			AggregateID: "agg-1",
			Version:     1,
			Success:     true,
			ProcessedAt: time.Now(),
			ExpiresAt:   time.Now().Add(time.Hour),
		}

		err := store.Store(ctx, record)
		require.NoError(t, err)

		count, _ := store.Count(ctx)
		assert.GreaterOrEqual(t, count, int64(1))
	})

	t.Run("stores with response", func(t *testing.T) {
		record := &adapters.IdempotencyRecord{
			Key:         "pg-test-key-2",
			CommandType: "TestCommand",
			Response:    []byte(`{"result":"success"}`),
			Success:     true,
			ProcessedAt: time.Now(),
			ExpiresAt:   time.Now().Add(time.Hour),
		}

		err := store.Store(ctx, record)
		require.NoError(t, err)

		retrieved, _ := store.Get(ctx, "pg-test-key-2")
		require.NotNil(t, retrieved)
		assert.Equal(t, []byte(`{"result":"success"}`), retrieved.Response)
	})

	t.Run("stores error information", func(t *testing.T) {
		record := &adapters.IdempotencyRecord{
			Key:         "pg-test-key-3",
			CommandType: "TestCommand",
			Error:       "something went wrong",
			Success:     false,
			ProcessedAt: time.Now(),
			ExpiresAt:   time.Now().Add(time.Hour),
		}

		err := store.Store(ctx, record)
		require.NoError(t, err)

		retrieved, _ := store.Get(ctx, "pg-test-key-3")
		require.NotNil(t, retrieved)
		assert.Equal(t, "something went wrong", retrieved.Error)
		assert.False(t, retrieved.Success)
	})

	t.Run("upserts on conflict", func(t *testing.T) {
		record1 := &adapters.IdempotencyRecord{
			Key:         "pg-test-key-upsert",
			CommandType: "TestCommand",
			Version:     1,
			Success:     true,
			ProcessedAt: time.Now(),
			ExpiresAt:   time.Now().Add(time.Hour),
		}

		err := store.Store(ctx, record1)
		require.NoError(t, err)

		record2 := &adapters.IdempotencyRecord{
			Key:         "pg-test-key-upsert",
			CommandType: "TestCommand",
			Version:     2,
			Success:     true,
			ProcessedAt: time.Now(),
			ExpiresAt:   time.Now().Add(time.Hour),
		}

		err = store.Store(ctx, record2)
		require.NoError(t, err)

		retrieved, _ := store.Get(ctx, "pg-test-key-upsert")
		require.NotNil(t, retrieved)
		assert.Equal(t, int64(2), retrieved.Version)
	})
}

func TestIdempotencyStore_PostgreSQL_Exists(t *testing.T) {
	store := setupIdempotencyTestStore(t)
	ctx := context.Background()

	t.Run("returns false for non-existent key", func(t *testing.T) {
		exists, err := store.Exists(ctx, "non-existent-key")
		require.NoError(t, err)
		assert.False(t, exists)
	})

	t.Run("returns true for existing key", func(t *testing.T) {
		record := &adapters.IdempotencyRecord{
			Key:         "exists-test-key",
			CommandType: "TestCommand",
			ProcessedAt: time.Now(),
			ExpiresAt:   time.Now().Add(time.Hour),
		}
		_ = store.Store(ctx, record)

		exists, err := store.Exists(ctx, "exists-test-key")
		require.NoError(t, err)
		assert.True(t, exists)
	})

	t.Run("returns false for expired key", func(t *testing.T) {
		record := &adapters.IdempotencyRecord{
			Key:         "expired-exists-key",
			CommandType: "TestCommand",
			ProcessedAt: time.Now(),
			ExpiresAt:   time.Now().Add(-time.Hour), // Already expired
		}
		_ = store.Store(ctx, record)

		exists, err := store.Exists(ctx, "expired-exists-key")
		require.NoError(t, err)
		assert.False(t, exists)
	})
}

func TestIdempotencyStore_PostgreSQL_Get(t *testing.T) {
	store := setupIdempotencyTestStore(t)
	ctx := context.Background()

	t.Run("returns nil for non-existent key", func(t *testing.T) {
		record, err := store.Get(ctx, "non-existent-get-key")
		require.NoError(t, err)
		assert.Nil(t, record)
	})

	t.Run("returns record for existing key", func(t *testing.T) {
		original := &adapters.IdempotencyRecord{
			Key:         "get-test-key",
			CommandType: "TestCommand",
			AggregateID: "agg-get-1",
			Version:     5,
			Success:     true,
			ProcessedAt: time.Now(),
			ExpiresAt:   time.Now().Add(time.Hour),
		}
		_ = store.Store(ctx, original)

		record, err := store.Get(ctx, "get-test-key")
		require.NoError(t, err)
		require.NotNil(t, record)

		assert.Equal(t, original.Key, record.Key)
		assert.Equal(t, original.CommandType, record.CommandType)
		assert.Equal(t, original.AggregateID, record.AggregateID)
		assert.Equal(t, original.Version, record.Version)
		assert.Equal(t, original.Success, record.Success)
	})

	t.Run("returns nil for expired key", func(t *testing.T) {
		record := &adapters.IdempotencyRecord{
			Key:         "expired-get-key",
			CommandType: "TestCommand",
			ProcessedAt: time.Now(),
			ExpiresAt:   time.Now().Add(-time.Hour), // Already expired
		}
		_ = store.Store(ctx, record)

		retrieved, err := store.Get(ctx, "expired-get-key")
		require.NoError(t, err)
		assert.Nil(t, retrieved)
	})
}

func TestIdempotencyStore_PostgreSQL_Delete(t *testing.T) {
	store := setupIdempotencyTestStore(t)
	ctx := context.Background()

	t.Run("deletes existing key", func(t *testing.T) {
		record := &adapters.IdempotencyRecord{
			Key:         "delete-test-key",
			CommandType: "TestCommand",
			ProcessedAt: time.Now(),
			ExpiresAt:   time.Now().Add(time.Hour),
		}
		_ = store.Store(ctx, record)

		err := store.Delete(ctx, "delete-test-key")
		require.NoError(t, err)

		exists, _ := store.Exists(ctx, "delete-test-key")
		assert.False(t, exists)
	})

	t.Run("does not error for non-existent key", func(t *testing.T) {
		err := store.Delete(ctx, "non-existent-delete-key")
		require.NoError(t, err)
	})
}

func TestIdempotencyStore_PostgreSQL_Cleanup(t *testing.T) {
	store := setupIdempotencyTestStore(t)
	ctx := context.Background()

	t.Run("removes old and expired records", func(t *testing.T) {
		// Add old record
		oldRecord := &adapters.IdempotencyRecord{
			Key:         "cleanup-old-key",
			CommandType: "TestCommand",
			ProcessedAt: time.Now().Add(-2 * time.Hour),
			ExpiresAt:   time.Now().Add(-time.Hour),
		}
		_ = store.Store(ctx, oldRecord)

		// Add fresh record
		freshRecord := &adapters.IdempotencyRecord{
			Key:         "cleanup-fresh-key",
			CommandType: "TestCommand",
			ProcessedAt: time.Now(),
			ExpiresAt:   time.Now().Add(time.Hour),
		}
		_ = store.Store(ctx, freshRecord)

		count, err := store.Cleanup(ctx, time.Hour)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, count, int64(1))

		// Fresh record should still exist
		exists, _ := store.Exists(ctx, "cleanup-fresh-key")
		assert.True(t, exists)
	})
}
