package mink

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewProjectionRebuilder(t *testing.T) {
	store := &EventStore{}
	checkpoint := newTestCheckpointStore()

	t.Run("creates rebuilder with defaults", func(t *testing.T) {
		rebuilder := NewProjectionRebuilder(store, checkpoint)
		assert.NotNil(t, rebuilder)
	})

	t.Run("creates rebuilder with options", func(t *testing.T) {
		rebuilder := NewProjectionRebuilder(store, checkpoint,
			WithRebuilderBatchSize(500),
		)
		assert.NotNil(t, rebuilder)
		assert.Equal(t, 500, rebuilder.batchSize)
	})
}

func TestDefaultRebuildOptions(t *testing.T) {
	opts := DefaultRebuildOptions()

	assert.True(t, opts.DeleteCheckpoint)
	assert.True(t, opts.ClearReadModel)
	assert.Equal(t, time.Second, opts.ProgressInterval)
	assert.Equal(t, uint64(0), opts.FromPosition)
	assert.Equal(t, uint64(0), opts.ToPosition)
	assert.Nil(t, opts.ProgressCallback)
}

func TestRebuildProgress(t *testing.T) {
	progress := RebuildProgress{
		ProjectionName:     "TestProjection",
		TotalEvents:        1000,
		ProcessedEvents:    500,
		CurrentPosition:    500,
		StartedAt:          time.Now().Add(-5 * time.Second),
		Duration:           5 * time.Second,
		EventsPerSecond:    100.0,
		EstimatedRemaining: 5 * time.Second,
		Completed:          false,
		Error:              nil,
	}

	assert.Equal(t, "TestProjection", progress.ProjectionName)
	assert.Equal(t, uint64(1000), progress.TotalEvents)
	assert.Equal(t, uint64(500), progress.ProcessedEvents)
	assert.Equal(t, float64(100), progress.EventsPerSecond)
	assert.False(t, progress.Completed)
}

func TestParallelRebuilder(t *testing.T) {
	store := &EventStore{}
	checkpoint := newTestCheckpointStore()
	rebuilder := NewProjectionRebuilder(store, checkpoint)

	t.Run("creates parallel rebuilder", func(t *testing.T) {
		pr := NewParallelRebuilder(rebuilder, 4)
		assert.NotNil(t, pr)
	})

	t.Run("handles zero concurrency", func(t *testing.T) {
		pr := NewParallelRebuilder(rebuilder, 0)
		assert.NotNil(t, pr)
		assert.Equal(t, 1, pr.concurrency) // Should default to 1
	})

	t.Run("handles negative concurrency", func(t *testing.T) {
		pr := NewParallelRebuilder(rebuilder, -5)
		assert.NotNil(t, pr)
		assert.Equal(t, 1, pr.concurrency)
	})

	t.Run("RebuildAll with empty projections", func(t *testing.T) {
		pr := NewParallelRebuilder(rebuilder, 4)
		err := pr.RebuildAll(context.Background(), []AsyncProjection{})
		require.NoError(t, err)
	})
}

func TestContainsString(t *testing.T) {
	slice := []string{"OrderCreated", "OrderShipped", "OrderCanceled"}

	t.Run("returns true for existing string", func(t *testing.T) {
		assert.True(t, containsString(slice, "OrderCreated"))
		assert.True(t, containsString(slice, "OrderShipped"))
		assert.True(t, containsString(slice, "OrderCanceled"))
	})

	t.Run("returns false for missing string", func(t *testing.T) {
		assert.False(t, containsString(slice, "CustomerRegistered"))
		assert.False(t, containsString(slice, ""))
	})

	t.Run("returns false for empty slice", func(t *testing.T) {
		assert.False(t, containsString([]string{}, "anything"))
	})
}

func TestProjectionRebuilder_BuildProgress(t *testing.T) {
	store := &EventStore{}
	checkpoint := newTestCheckpointStore()
	rebuilder := NewProjectionRebuilder(store, checkpoint)

	startTime := time.Now().Add(-10 * time.Second)
	progress := rebuilder.buildProgress("TestProj", 1000, 500, 500, startTime, false, nil)

	assert.Equal(t, "TestProj", progress.ProjectionName)
	assert.Equal(t, uint64(1000), progress.TotalEvents)
	assert.Equal(t, uint64(500), progress.ProcessedEvents)
	assert.Equal(t, uint64(500), progress.CurrentPosition)
	assert.False(t, progress.Completed)
	assert.Nil(t, progress.Error)

	// Check rate calculation
	assert.Greater(t, progress.EventsPerSecond, float64(0))
	assert.Greater(t, progress.EstimatedRemaining, time.Duration(0))
}

func TestProjectionRebuilder_ProgressCallback(t *testing.T) {
	// Skip this test since it requires a fully configured store with adapter
	t.Skip("Requires full store configuration with adapter")
}
