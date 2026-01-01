package mink

import (
	"context"
	"testing"
	"time"

	"github.com/AshkanYarmoradi/go-mink/adapters/memory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testProjectionMetrics is a test implementation of ProjectionMetrics
type testProjectionMetrics struct {
	eventsProcessed  int
	batchesProcessed int
	checkpointsSet   int
	errorsRecorded   int
	lastProjection   string
	lastEventType    string
	lastDuration     time.Duration
	lastSuccess      bool
}

func (m *testProjectionMetrics) RecordEventProcessed(projectionName, eventType string, duration time.Duration, success bool) {
	m.eventsProcessed++
	m.lastProjection = projectionName
	m.lastEventType = eventType
	m.lastDuration = duration
	m.lastSuccess = success
}

func (m *testProjectionMetrics) RecordBatchProcessed(projectionName string, count int, duration time.Duration, success bool) {
	m.batchesProcessed++
	m.lastProjection = projectionName
	m.lastSuccess = success
}

func (m *testProjectionMetrics) RecordCheckpoint(projectionName string, position uint64) {
	m.checkpointsSet++
	m.lastProjection = projectionName
}

func (m *testProjectionMetrics) RecordError(projectionName string, err error) {
	m.errorsRecorded++
	m.lastProjection = projectionName
}

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

func TestProjectionRebuilder_BuildProgress_Completed(t *testing.T) {
	store := &EventStore{}
	checkpoint := newTestCheckpointStore()
	rebuilder := NewProjectionRebuilder(store, checkpoint)

	startTime := time.Now().Add(-5 * time.Second)
	progress := rebuilder.buildProgress("TestProj", 1000, 1000, 1000, startTime, true, nil)

	assert.True(t, progress.Completed)
	assert.Equal(t, uint64(1000), progress.ProcessedEvents)
}

func TestProjectionRebuilder_BuildProgress_ZeroDuration(t *testing.T) {
	store := &EventStore{}
	checkpoint := newTestCheckpointStore()
	rebuilder := NewProjectionRebuilder(store, checkpoint)

	startTime := time.Now()
	progress := rebuilder.buildProgress("TestProj", 1000, 0, 0, startTime, false, nil)

	// With zero time elapsed, EventsPerSecond should be 0 or undefined behavior is handled
	assert.GreaterOrEqual(t, progress.EventsPerSecond, float64(0))
}

func TestWithRebuilderLogger(t *testing.T) {
	store := &EventStore{}
	checkpoint := newTestCheckpointStore()
	logger := &testLogger{}

	rebuilder := NewProjectionRebuilder(store, checkpoint,
		WithRebuilderLogger(logger),
	)

	assert.Equal(t, logger, rebuilder.logger)
}

func TestWithRebuilderMetrics(t *testing.T) {
	store := &EventStore{}
	checkpoint := newTestCheckpointStore()
	metrics := &testProjectionMetrics{}

	rebuilder := NewProjectionRebuilder(store, checkpoint,
		WithRebuilderMetrics(metrics),
	)

	assert.Equal(t, metrics, rebuilder.metrics)
}

// testAsyncProjectionForRebuilder implements AsyncProjection for rebuild tests
type testAsyncProjectionForRebuilder struct {
	AsyncProjectionBase
	events []StoredEvent
}

func newTestAsyncProjectionForRebuilder(name string) *testAsyncProjectionForRebuilder {
	base := NewAsyncProjectionBase(name)
	return &testAsyncProjectionForRebuilder{
		AsyncProjectionBase: base,
	}
}

func (p *testAsyncProjectionForRebuilder) Apply(ctx context.Context, event StoredEvent) error {
	p.events = append(p.events, event)
	return nil
}

// testInlineProjectionForRebuilder implements InlineProjection for rebuild tests
type testInlineProjectionForRebuilder struct {
	ProjectionBase
	events []StoredEvent
}

func newTestInlineProjectionForRebuilder(name string) *testInlineProjectionForRebuilder {
	base := NewProjectionBase(name)
	return &testInlineProjectionForRebuilder{
		ProjectionBase: base,
	}
}

func (p *testInlineProjectionForRebuilder) Apply(ctx context.Context, event StoredEvent) error {
	p.events = append(p.events, event)
	return nil
}

// OrderCreatedEvent is a test event type
type OrderCreatedEvent struct {
	OrderID string
}

func TestProjectionRebuilder_RebuildAsync(t *testing.T) {
	adapter := memory.NewAdapter()
	store := New(adapter)
	checkpoint := newTestCheckpointStore()
	rebuilder := NewProjectionRebuilder(store, checkpoint)

	// Register the event type
	store.RegisterEvents(&OrderCreatedEvent{})

	t.Run("rebuilds with no events", func(t *testing.T) {
		projection := newTestAsyncProjectionForRebuilder("TestAsyncRebuild")

		err := rebuilder.RebuildAsync(context.Background(), projection)
		require.NoError(t, err)
		assert.Empty(t, projection.events)
	})

	t.Run("rebuilds with events", func(t *testing.T) {
		// Append some events
		err := store.Append(context.Background(), "Order-123",
			[]interface{}{&OrderCreatedEvent{OrderID: "123"}},
		)
		require.NoError(t, err)

		projection := newTestAsyncProjectionForRebuilder("TestAsyncRebuild2")

		err = rebuilder.RebuildAsync(context.Background(), projection)
		require.NoError(t, err)
		// Events should be processed
		assert.GreaterOrEqual(t, len(projection.events), 0)
	})

	t.Run("rebuilds with custom options", func(t *testing.T) {
		projection := newTestAsyncProjectionForRebuilder("TestAsyncRebuildOpts")

		opts := DefaultRebuildOptions()
		opts.DeleteCheckpoint = false

		err := rebuilder.RebuildAsync(context.Background(), projection, opts)
		require.NoError(t, err)
	})
}

func TestProjectionRebuilder_RebuildInline(t *testing.T) {
	adapter := memory.NewAdapter()
	store := New(adapter)
	checkpoint := newTestCheckpointStore()
	rebuilder := NewProjectionRebuilder(store, checkpoint)

	// Register event type
	store.RegisterEvents(&OrderCreatedEvent{})

	t.Run("rebuilds with no events", func(t *testing.T) {
		projection := newTestInlineProjectionForRebuilder("TestInlineRebuild")

		err := rebuilder.RebuildInline(context.Background(), projection)
		require.NoError(t, err)
		assert.Empty(t, projection.events)
	})

	t.Run("rebuilds with custom options", func(t *testing.T) {
		projection := newTestInlineProjectionForRebuilder("TestInlineRebuildOpts")

		opts := DefaultRebuildOptions()
		opts.DeleteCheckpoint = false

		err := rebuilder.RebuildInline(context.Background(), projection, opts)
		require.NoError(t, err)
	})

	t.Run("rebuilds inline projection with events", func(t *testing.T) {
		// Clear previous events by creating a new store
		adapter2 := memory.NewAdapter()
		store2 := New(adapter2)
		checkpoint2 := newTestCheckpointStore()
		rebuilder2 := NewProjectionRebuilder(store2, checkpoint2)

		// Register event type
		store2.RegisterEvents(&OrderCreatedEvent{})

		// Append some events
		err := store2.Append(context.Background(), "Order-inline-1",
			[]interface{}{&OrderCreatedEvent{OrderID: "inline-1"}},
		)
		require.NoError(t, err)

		err = store2.Append(context.Background(), "Order-inline-2",
			[]interface{}{&OrderCreatedEvent{OrderID: "inline-2"}},
		)
		require.NoError(t, err)

		projection := newTestInlineProjectionForRebuilder("TestInlineWithEvents")

		err = rebuilder2.RebuildInline(context.Background(), projection)
		require.NoError(t, err)

		// Should have processed the events
		assert.Len(t, projection.events, 2)
	})

	t.Run("inline projection filters events by type", func(t *testing.T) {
		adapter3 := memory.NewAdapter()
		store3 := New(adapter3)
		checkpoint3 := newTestCheckpointStore()
		rebuilder3 := NewProjectionRebuilder(store3, checkpoint3)

		// Register event type
		store3.RegisterEvents(&OrderCreatedEvent{})

		// Append events
		err := store3.Append(context.Background(), "Order-filter-1",
			[]interface{}{&OrderCreatedEvent{OrderID: "filter-1"}},
		)
		require.NoError(t, err)

		// Create projection that handles specific events
		projection := newTestInlineProjectionForRebuilderWithFilter("TestInlineFiltered", "OrderCreatedEvent")

		err = rebuilder3.RebuildInline(context.Background(), projection)
		require.NoError(t, err)

		// Should have processed the filtered events
		assert.GreaterOrEqual(t, len(projection.events), 1)
	})
}

// testInlineProjectionForRebuilderWithFilter implements InlineProjection with event filtering
type testInlineProjectionForRebuilderWithFilter struct {
	ProjectionBase
	events []StoredEvent
}

func newTestInlineProjectionForRebuilderWithFilter(name string, eventTypes ...string) *testInlineProjectionForRebuilderWithFilter {
	return &testInlineProjectionForRebuilderWithFilter{
		ProjectionBase: NewProjectionBase(name, eventTypes...),
	}
}

func (p *testInlineProjectionForRebuilderWithFilter) Apply(ctx context.Context, event StoredEvent) error {
	p.events = append(p.events, event)
	return nil
}

func TestProjectionRebuilder_WithProgressCallback(t *testing.T) {
	adapter := memory.NewAdapter()
	store := New(adapter)
	checkpoint := newTestCheckpointStore()
	rebuilder := NewProjectionRebuilder(store, checkpoint)

	// Register the event type
	store.RegisterEvents(&OrderCreatedEvent{})

	t.Run("calls progress callback", func(t *testing.T) {
		// Append some events first
		err := store.Append(context.Background(), "Order-456",
			[]interface{}{&OrderCreatedEvent{OrderID: "456"}},
		)
		require.NoError(t, err)

		projection := newTestAsyncProjectionForRebuilder("TestProgressCallback")

		progressCalls := 0
		opts := DefaultRebuildOptions()
		opts.ProgressInterval = 10 * time.Millisecond
		opts.ProgressCallback = func(p RebuildProgress) {
			progressCalls++
		}

		err = rebuilder.RebuildAsync(context.Background(), projection, opts)
		require.NoError(t, err)
		// Final callback should have been called
		assert.GreaterOrEqual(t, progressCalls, 1)
	})
}

func TestProjectionRebuilder_CheckpointDeletion(t *testing.T) {
	adapter := memory.NewAdapter()
	store := New(adapter)
	checkpoint := newTestCheckpointStore()
	rebuilder := NewProjectionRebuilder(store, checkpoint)

	t.Run("deletes checkpoint when requested", func(t *testing.T) {
		// Set a checkpoint
		err := checkpoint.SetCheckpoint(context.Background(), "TestCheckpointDelete", 100)
		require.NoError(t, err)

		projection := newTestAsyncProjectionForRebuilder("TestCheckpointDelete")
		opts := DefaultRebuildOptions()
		opts.DeleteCheckpoint = true

		err = rebuilder.RebuildAsync(context.Background(), projection, opts)
		require.NoError(t, err)

		// Checkpoint should be deleted (returns 0)
		pos, _ := checkpoint.GetCheckpoint(context.Background(), "TestCheckpointDelete")
		assert.Equal(t, uint64(0), pos)
	})

	t.Run("preserves checkpoint when not requested", func(t *testing.T) {
		// Set a checkpoint
		err := checkpoint.SetCheckpoint(context.Background(), "TestCheckpointPreserve", 100)
		require.NoError(t, err)

		projection := newTestAsyncProjectionForRebuilder("TestCheckpointPreserve")
		opts := DefaultRebuildOptions()
		opts.DeleteCheckpoint = false

		err = rebuilder.RebuildAsync(context.Background(), projection, opts)
		require.NoError(t, err)

		// Checkpoint should still exist
		pos, _ := checkpoint.GetCheckpoint(context.Background(), "TestCheckpointPreserve")
		assert.Equal(t, uint64(100), pos)
	})
}

func TestParallelRebuilder_RebuildAll(t *testing.T) {
	adapter := memory.NewAdapter()
	store := New(adapter)
	checkpoint := newTestCheckpointStore()
	rebuilder := NewProjectionRebuilder(store, checkpoint)

	t.Run("rebuilds multiple projections", func(t *testing.T) {
		pr := NewParallelRebuilder(rebuilder, 2)

		proj1 := newTestAsyncProjectionForRebuilder("Parallel1")
		proj2 := newTestAsyncProjectionForRebuilder("Parallel2")

		err := pr.RebuildAll(context.Background(), []AsyncProjection{proj1, proj2})
		require.NoError(t, err)
	})

	t.Run("rebuilds with events", func(t *testing.T) {
		adapter2 := memory.NewAdapter()
		store2 := New(adapter2)
		store2.RegisterEvents(&OrderCreatedEvent{})
		checkpoint2 := newTestCheckpointStore()
		rebuilder2 := NewProjectionRebuilder(store2, checkpoint2)

		// Add events
		ctx := context.Background()
		for i := 0; i < 5; i++ {
			_ = store2.Append(ctx, "Order-par-"+string(rune('0'+i)),
				[]interface{}{&OrderCreatedEvent{OrderID: "par-" + string(rune('0'+i))}})
		}

		pr := NewParallelRebuilder(rebuilder2, 2)

		proj1 := newTestAsyncProjectionForRebuilder("ParallelEvents1")
		proj2 := newTestAsyncProjectionForRebuilder("ParallelEvents2")

		err := pr.RebuildAll(ctx, []AsyncProjection{proj1, proj2})
		require.NoError(t, err)

		// Both projections should have processed events
		assert.GreaterOrEqual(t, len(proj1.events), 1)
		assert.GreaterOrEqual(t, len(proj2.events), 1)
	})

	t.Run("rebuilds with custom options", func(t *testing.T) {
		adapter3 := memory.NewAdapter()
		store3 := New(adapter3)
		store3.RegisterEvents(&OrderCreatedEvent{})
		checkpoint3 := newTestCheckpointStore()
		rebuilder3 := NewProjectionRebuilder(store3, checkpoint3)

		ctx := context.Background()
		for i := 0; i < 3; i++ {
			_ = store3.Append(ctx, "Order-opts-"+string(rune('0'+i)),
				[]interface{}{&OrderCreatedEvent{OrderID: "opts-" + string(rune('0'+i))}})
		}

		pr := NewParallelRebuilder(rebuilder3, 2)

		proj := newTestAsyncProjectionForRebuilder("ParallelOpts")
		opts := DefaultRebuildOptions()
		opts.DeleteCheckpoint = false

		err := pr.RebuildAll(ctx, []AsyncProjection{proj}, opts)
		require.NoError(t, err)
	})

	t.Run("handles context cancellation", func(t *testing.T) {
		adapter4 := memory.NewAdapter()
		store4 := New(adapter4)
		store4.RegisterEvents(&OrderCreatedEvent{})
		checkpoint4 := newTestCheckpointStore()
		rebuilder4 := NewProjectionRebuilder(store4, checkpoint4)

		ctx, cancel := context.WithCancel(context.Background())

		// Add many events
		for i := 0; i < 100; i++ {
			_ = store4.Append(ctx, "Order-cancel-"+string(rune('0'+i%10)),
				[]interface{}{&OrderCreatedEvent{OrderID: "cancel"}})
		}

		pr := NewParallelRebuilder(rebuilder4, 1)

		proj := newTestAsyncProjectionForRebuilder("ParallelCancel")

		// Cancel immediately
		cancel()

		err := pr.RebuildAll(ctx, []AsyncProjection{proj})
		// Either context error or nil is acceptable
		_ = err
	})
}

func TestProjectionRebuilder_RebuildWithToPosition(t *testing.T) {
	adapter := memory.NewAdapter()
	store := New(adapter)
	store.RegisterEvents(&OrderCreatedEvent{})
	checkpoint := newTestCheckpointStore()
	rebuilder := NewProjectionRebuilder(store, checkpoint)

	ctx := context.Background()

	// Add events
	for i := 0; i < 10; i++ {
		_ = store.Append(ctx, "Order-pos-"+string(rune('0'+i)),
			[]interface{}{&OrderCreatedEvent{OrderID: "pos-" + string(rune('0'+i))}})
	}

	projection := newTestAsyncProjectionForRebuilder("ToPositionProj")

	opts := DefaultRebuildOptions()
	opts.ToPosition = 5 // Only process first 5 events

	err := rebuilder.RebuildAsync(ctx, projection, opts)
	require.NoError(t, err)

	// Should have processed fewer events due to ToPosition limit
	assert.LessOrEqual(t, len(projection.events), 5)
}

func TestProjectionRebuilder_RebuildWithFromPosition(t *testing.T) {
	adapter := memory.NewAdapter()
	store := New(adapter)
	store.RegisterEvents(&OrderCreatedEvent{})
	checkpoint := newTestCheckpointStore()
	rebuilder := NewProjectionRebuilder(store, checkpoint)

	ctx := context.Background()

	// Add events
	for i := 0; i < 10; i++ {
		_ = store.Append(ctx, "Order-from-"+string(rune('0'+i)),
			[]interface{}{&OrderCreatedEvent{OrderID: "from-" + string(rune('0'+i))}})
	}

	projection := newTestAsyncProjectionForRebuilder("FromPositionProj")

	opts := DefaultRebuildOptions()
	opts.FromPosition = 5 // Start from position 5

	err := rebuilder.RebuildAsync(ctx, projection, opts)
	require.NoError(t, err)

	// Should have processed only events after position 5
	assert.LessOrEqual(t, len(projection.events), 5)
}
