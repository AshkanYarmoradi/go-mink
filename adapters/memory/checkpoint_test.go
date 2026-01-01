package memory

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckpointStore_GetCheckpoint(t *testing.T) {
	store := NewCheckpointStore()
	ctx := context.Background()

	t.Run("returns 0 for non-existent checkpoint", func(t *testing.T) {
		pos, err := store.GetCheckpoint(ctx, "NonExistent")
		require.NoError(t, err)
		assert.Equal(t, uint64(0), pos)
	})

	t.Run("returns stored position", func(t *testing.T) {
		_ = store.SetCheckpoint(ctx, "TestProjection", 100)
		pos, err := store.GetCheckpoint(ctx, "TestProjection")
		require.NoError(t, err)
		assert.Equal(t, uint64(100), pos)
	})
}

func TestCheckpointStore_SetCheckpoint(t *testing.T) {
	store := NewCheckpointStore()
	ctx := context.Background()

	t.Run("stores checkpoint", func(t *testing.T) {
		err := store.SetCheckpoint(ctx, "NewProj", 50)
		require.NoError(t, err)

		pos, _ := store.GetCheckpoint(ctx, "NewProj")
		assert.Equal(t, uint64(50), pos)
	})

	t.Run("updates existing checkpoint", func(t *testing.T) {
		_ = store.SetCheckpoint(ctx, "UpdateProj", 10)
		_ = store.SetCheckpoint(ctx, "UpdateProj", 20)

		pos, _ := store.GetCheckpoint(ctx, "UpdateProj")
		assert.Equal(t, uint64(20), pos)
	})
}

func TestCheckpointStore_DeleteCheckpoint(t *testing.T) {
	store := NewCheckpointStore()
	ctx := context.Background()

	_ = store.SetCheckpoint(ctx, "ToDelete", 100)

	t.Run("deletes existing checkpoint", func(t *testing.T) {
		err := store.DeleteCheckpoint(ctx, "ToDelete")
		require.NoError(t, err)

		pos, _ := store.GetCheckpoint(ctx, "ToDelete")
		assert.Equal(t, uint64(0), pos)
	})

	t.Run("deleting non-existent is no-op", func(t *testing.T) {
		err := store.DeleteCheckpoint(ctx, "NonExistent")
		require.NoError(t, err)
	})
}

func TestCheckpointStore_GetAllCheckpoints(t *testing.T) {
	store := NewCheckpointStore()
	ctx := context.Background()

	t.Run("returns empty map when no checkpoints", func(t *testing.T) {
		checkpoints, err := store.GetAllCheckpoints(ctx)
		require.NoError(t, err)
		assert.Empty(t, checkpoints)
	})

	t.Run("returns all checkpoints", func(t *testing.T) {
		_ = store.SetCheckpoint(ctx, "Proj1", 10)
		_ = store.SetCheckpoint(ctx, "Proj2", 20)
		_ = store.SetCheckpoint(ctx, "Proj3", 30)

		checkpoints, err := store.GetAllCheckpoints(ctx)
		require.NoError(t, err)
		assert.Len(t, checkpoints, 3)
		assert.Equal(t, uint64(10), checkpoints["Proj1"])
		assert.Equal(t, uint64(20), checkpoints["Proj2"])
		assert.Equal(t, uint64(30), checkpoints["Proj3"])
	})
}

func TestCheckpointStore_Clear(t *testing.T) {
	store := NewCheckpointStore()
	ctx := context.Background()

	_ = store.SetCheckpoint(ctx, "Proj1", 10)
	_ = store.SetCheckpoint(ctx, "Proj2", 20)

	assert.Equal(t, 2, store.Len())

	store.Clear()

	assert.Equal(t, 0, store.Len())
}

func TestCheckpointStore_Len(t *testing.T) {
	store := NewCheckpointStore()
	ctx := context.Background()

	assert.Equal(t, 0, store.Len())

	_ = store.SetCheckpoint(ctx, "Proj1", 10)
	assert.Equal(t, 1, store.Len())

	_ = store.SetCheckpoint(ctx, "Proj2", 20)
	assert.Equal(t, 2, store.Len())

	// Updating doesn't increase count
	_ = store.SetCheckpoint(ctx, "Proj1", 100)
	assert.Equal(t, 2, store.Len())
}

func TestCheckpointStore_GetCheckpointWithTimestamp(t *testing.T) {
	store := NewCheckpointStore()
	ctx := context.Background()

	t.Run("returns nil for non-existent", func(t *testing.T) {
		cp, err := store.GetCheckpointWithTimestamp(ctx, "NonExistent")
		require.NoError(t, err)
		assert.Nil(t, cp)
	})

	t.Run("returns checkpoint with timestamp", func(t *testing.T) {
		before := time.Now()
		_ = store.SetCheckpoint(ctx, "WithTimestamp", 42)
		after := time.Now()

		cp, err := store.GetCheckpointWithTimestamp(ctx, "WithTimestamp")
		require.NoError(t, err)
		require.NotNil(t, cp)

		assert.Equal(t, "WithTimestamp", cp.ProjectionName)
		assert.Equal(t, uint64(42), cp.Position)
		assert.True(t, cp.UpdatedAt.After(before) || cp.UpdatedAt.Equal(before))
		assert.True(t, cp.UpdatedAt.Before(after) || cp.UpdatedAt.Equal(after))
	})
}

func TestCheckpointStore_ConcurrentAccess(t *testing.T) {
	store := NewCheckpointStore()
	ctx := context.Background()

	// Concurrent writes
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func(idx int) {
			for j := 0; j < 100; j++ {
				_ = store.SetCheckpoint(ctx, "ConcurrentProj", uint64(idx*100+j))
			}
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}

	// Should have stored some value without crashing
	pos, err := store.GetCheckpoint(ctx, "ConcurrentProj")
	require.NoError(t, err)
	assert.Greater(t, pos, uint64(0))
}
