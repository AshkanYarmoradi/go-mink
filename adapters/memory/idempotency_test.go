package memory

import (
	"context"
	"testing"
	"time"

	"github.com/AshkanYarmoradi/go-mink/adapters"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewIdempotencyStore(t *testing.T) {
	t.Run("creates store with defaults", func(t *testing.T) {
		store := NewIdempotencyStore()
		defer store.Close()

		assert.NotNil(t, store)
		assert.Equal(t, 0, store.Len())
	})

	t.Run("accepts options", func(t *testing.T) {
		store := NewIdempotencyStore(
			WithMaxAge(48 * time.Hour),
		)
		defer store.Close()

		assert.Equal(t, 48*time.Hour, store.maxAge)
	})

	t.Run("WithCleanupInterval starts background cleanup", func(t *testing.T) {
		ctx := context.Background()
		// Use a very short interval for testing
		store := NewIdempotencyStore(
			WithCleanupInterval(50*time.Millisecond),
			WithMaxAge(10*time.Millisecond),
		)
		defer store.Close()

		// Store an already-expired record
		record := &adapters.IdempotencyRecord{
			Key:         "cleanup-test-key",
			CommandType: "TestCommand",
			Success:     true,
			ProcessedAt: time.Now().Add(-time.Hour),
			ExpiresAt:   time.Now().Add(-time.Hour),
		}
		err := store.Store(ctx, record)
		require.NoError(t, err)
		assert.Equal(t, 1, store.Len())

		// Wait for cleanup to run
		time.Sleep(100 * time.Millisecond)

		// Record should be cleaned up
		assert.Equal(t, 0, store.Len())
	})
}

func TestIdempotencyStore_Store(t *testing.T) {
	ctx := context.Background()

	t.Run("stores record successfully", func(t *testing.T) {
		store := NewIdempotencyStore()
		defer store.Close()

		record := &adapters.IdempotencyRecord{
			Key:         "test-key",
			CommandType: "TestCommand",
			AggregateID: "agg-1",
			Version:     1,
			Success:     true,
			ProcessedAt: time.Now(),
			ExpiresAt:   time.Now().Add(time.Hour),
		}

		err := store.Store(ctx, record)
		require.NoError(t, err)
		assert.Equal(t, 1, store.Len())
	})

	t.Run("stores with response", func(t *testing.T) {
		store := NewIdempotencyStore()
		defer store.Close()

		record := &adapters.IdempotencyRecord{
			Key:         "test-key",
			CommandType: "TestCommand",
			Response:    []byte(`{"result":"success"}`),
			ExpiresAt:   time.Now().Add(time.Hour),
		}

		err := store.Store(ctx, record)
		require.NoError(t, err)

		retrieved, _ := store.Get(ctx, "test-key")
		assert.Equal(t, []byte(`{"result":"success"}`), retrieved.Response)
	})
}

func TestIdempotencyStore_Exists(t *testing.T) {
	ctx := context.Background()

	t.Run("returns false for non-existent key", func(t *testing.T) {
		store := NewIdempotencyStore()
		defer store.Close()

		exists, err := store.Exists(ctx, "non-existent")
		require.NoError(t, err)
		assert.False(t, exists)
	})

	t.Run("returns true for existing key", func(t *testing.T) {
		store := NewIdempotencyStore()
		defer store.Close()

		record := &adapters.IdempotencyRecord{
			Key:       "test-key",
			ExpiresAt: time.Now().Add(time.Hour),
		}
		_ = store.Store(ctx, record)

		exists, err := store.Exists(ctx, "test-key")
		require.NoError(t, err)
		assert.True(t, exists)
	})

	t.Run("returns false for expired key", func(t *testing.T) {
		store := NewIdempotencyStore()
		defer store.Close()

		record := &adapters.IdempotencyRecord{
			Key:       "test-key",
			ExpiresAt: time.Now().Add(-time.Hour), // Expired
		}
		_ = store.Store(ctx, record)

		exists, err := store.Exists(ctx, "test-key")
		require.NoError(t, err)
		assert.False(t, exists)
	})
}

func TestIdempotencyStore_Get(t *testing.T) {
	ctx := context.Background()

	t.Run("returns nil for non-existent key", func(t *testing.T) {
		store := NewIdempotencyStore()
		defer store.Close()

		record, err := store.Get(ctx, "non-existent")
		require.NoError(t, err)
		assert.Nil(t, record)
	})

	t.Run("returns record for existing key", func(t *testing.T) {
		store := NewIdempotencyStore()
		defer store.Close()

		original := &adapters.IdempotencyRecord{
			Key:         "test-key",
			CommandType: "TestCommand",
			AggregateID: "agg-1",
			Version:     5,
			Success:     true,
			ExpiresAt:   time.Now().Add(time.Hour),
		}
		_ = store.Store(ctx, original)

		record, err := store.Get(ctx, "test-key")
		require.NoError(t, err)
		require.NotNil(t, record)

		assert.Equal(t, original.Key, record.Key)
		assert.Equal(t, original.CommandType, record.CommandType)
		assert.Equal(t, original.AggregateID, record.AggregateID)
		assert.Equal(t, original.Version, record.Version)
		assert.Equal(t, original.Success, record.Success)
	})

	t.Run("returns nil for expired key", func(t *testing.T) {
		store := NewIdempotencyStore()
		defer store.Close()

		record := &adapters.IdempotencyRecord{
			Key:       "test-key",
			ExpiresAt: time.Now().Add(-time.Hour), // Expired
		}
		_ = store.Store(ctx, record)

		retrieved, err := store.Get(ctx, "test-key")
		require.NoError(t, err)
		assert.Nil(t, retrieved)
	})

	t.Run("returns copy not reference", func(t *testing.T) {
		store := NewIdempotencyStore()
		defer store.Close()

		original := &adapters.IdempotencyRecord{
			Key:       "test-key",
			ExpiresAt: time.Now().Add(time.Hour),
		}
		_ = store.Store(ctx, original)

		record1, _ := store.Get(ctx, "test-key")
		record2, _ := store.Get(ctx, "test-key")

		record1.AggregateID = "modified"
		assert.NotEqual(t, record1.AggregateID, record2.AggregateID)
	})
}

func TestIdempotencyStore_Delete(t *testing.T) {
	ctx := context.Background()

	t.Run("deletes existing key", func(t *testing.T) {
		store := NewIdempotencyStore()
		defer store.Close()

		record := &adapters.IdempotencyRecord{
			Key:       "test-key",
			ExpiresAt: time.Now().Add(time.Hour),
		}
		_ = store.Store(ctx, record)

		err := store.Delete(ctx, "test-key")
		require.NoError(t, err)

		exists, _ := store.Exists(ctx, "test-key")
		assert.False(t, exists)
	})

	t.Run("does not error for non-existent key", func(t *testing.T) {
		store := NewIdempotencyStore()
		defer store.Close()

		err := store.Delete(ctx, "non-existent")
		require.NoError(t, err)
	})
}

func TestIdempotencyStore_Cleanup(t *testing.T) {
	ctx := context.Background()

	t.Run("removes old records", func(t *testing.T) {
		store := NewIdempotencyStore()
		defer store.Close()

		// Add old record
		oldRecord := &adapters.IdempotencyRecord{
			Key:         "old-key",
			ProcessedAt: time.Now().Add(-2 * time.Hour),
			ExpiresAt:   time.Now().Add(-time.Hour),
		}
		_ = store.Store(ctx, oldRecord)

		// Add new record
		newRecord := &adapters.IdempotencyRecord{
			Key:         "new-key",
			ProcessedAt: time.Now(),
			ExpiresAt:   time.Now().Add(time.Hour),
		}
		_ = store.Store(ctx, newRecord)

		count, err := store.Cleanup(ctx, time.Hour)
		require.NoError(t, err)
		assert.Equal(t, int64(1), count)
		assert.Equal(t, 1, store.Len())

		exists, _ := store.Exists(ctx, "new-key")
		assert.True(t, exists)
	})

	t.Run("removes expired records", func(t *testing.T) {
		store := NewIdempotencyStore()
		defer store.Close()

		// Add expired record
		expiredRecord := &adapters.IdempotencyRecord{
			Key:         "expired-key",
			ProcessedAt: time.Now(),
			ExpiresAt:   time.Now().Add(-time.Minute),
		}
		_ = store.Store(ctx, expiredRecord)

		count, err := store.Cleanup(ctx, 24*time.Hour)
		require.NoError(t, err)
		assert.Equal(t, int64(1), count)
	})
}

func TestIdempotencyStore_Clear(t *testing.T) {
	ctx := context.Background()

	t.Run("removes all records", func(t *testing.T) {
		store := NewIdempotencyStore()
		defer store.Close()

		for i := 0; i < 5; i++ {
			record := &adapters.IdempotencyRecord{
				Key:       "key-" + string(rune('a'+i)),
				ExpiresAt: time.Now().Add(time.Hour),
			}
			_ = store.Store(ctx, record)
		}

		assert.Equal(t, 5, store.Len())

		store.Clear()
		assert.Equal(t, 0, store.Len())
	})
}

func TestIdempotencyStore_Concurrency(t *testing.T) {
	ctx := context.Background()
	store := NewIdempotencyStore()
	defer store.Close()

	t.Run("handles concurrent operations", func(t *testing.T) {
		done := make(chan bool)
		iterations := 100

		// Concurrent stores
		go func() {
			for i := 0; i < iterations; i++ {
				record := &adapters.IdempotencyRecord{
					Key:       "concurrent-key",
					Version:   int64(i),
					ExpiresAt: time.Now().Add(time.Hour),
				}
				_ = store.Store(ctx, record)
			}
			done <- true
		}()

		// Concurrent reads
		go func() {
			for i := 0; i < iterations; i++ {
				_, _ = store.Get(ctx, "concurrent-key")
			}
			done <- true
		}()

		// Concurrent exists
		go func() {
			for i := 0; i < iterations; i++ {
				_, _ = store.Exists(ctx, "concurrent-key")
			}
			done <- true
		}()

		// Wait for all goroutines
		for i := 0; i < 3; i++ {
			<-done
		}
	})
}
