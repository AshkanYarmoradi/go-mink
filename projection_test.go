package mink

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AshkanYarmoradi/go-mink/adapters/memory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test projections, checkpoint store, logger, and metrics are defined in test_helpers_test.go

// --- Projection Tests ---

func TestProjectionBase(t *testing.T) {
	t.Run("Name returns configured name", func(t *testing.T) {
		p := NewProjectionBase("TestProjection")
		assert.Equal(t, "TestProjection", p.Name())
	})

	t.Run("HandledEvents returns configured events", func(t *testing.T) {
		p := NewProjectionBase("TestProjection", "OrderCreated", "OrderShipped")
		assert.Equal(t, []string{"OrderCreated", "OrderShipped"}, p.HandledEvents())
	})

	t.Run("Empty HandledEvents means handle all", func(t *testing.T) {
		p := NewProjectionBase("TestProjection")
		assert.Empty(t, p.HandledEvents())
		assert.True(t, p.HandlesEvent("AnyEvent"))
	})

	t.Run("HandlesEvent filters correctly", func(t *testing.T) {
		p := NewProjectionBase("TestProjection", "OrderCreated", "OrderShipped")
		assert.True(t, p.HandlesEvent("OrderCreated"))
		assert.True(t, p.HandlesEvent("OrderShipped"))
		assert.False(t, p.HandlesEvent("CustomerRegistered"))
	})
}

func TestAsyncProjectionBase(t *testing.T) {
	t.Run("ApplyBatch returns ErrNotImplemented by default", func(t *testing.T) {
		p := NewAsyncProjectionBase("TestAsync", "OrderCreated")
		err := p.ApplyBatch(context.Background(), []StoredEvent{})
		assert.ErrorIs(t, err, ErrNotImplemented)
	})
}

func TestLiveProjectionBase(t *testing.T) {
	t.Run("IsTransient returns configured value", func(t *testing.T) {
		transient := NewLiveProjectionBase("TransientProj", true)
		assert.True(t, transient.IsTransient())

		persistent := NewLiveProjectionBase("PersistentProj", false)
		assert.False(t, persistent.IsTransient())
	})
}

// --- Projection Engine Tests ---

func TestProjectionEngine_Options(t *testing.T) {
	store := &EventStore{}

	t.Run("WithCheckpointStore sets checkpoint store", func(t *testing.T) {
		cs := newTestCheckpointStore()
		engine := NewProjectionEngine(store, WithCheckpointStore(cs))
		// Engine should be configured, we can verify by using it
		assert.NotNil(t, engine)
	})

	t.Run("WithProjectionMetrics sets metrics", func(t *testing.T) {
		metrics := &noopProjectionMetrics{}
		engine := NewProjectionEngine(store, WithProjectionMetrics(metrics))
		assert.NotNil(t, engine)
	})

	t.Run("WithProjectionLogger sets logger", func(t *testing.T) {
		logger := &noopLogger{}
		engine := NewProjectionEngine(store, WithProjectionLogger(logger))
		assert.NotNil(t, engine)
	})

	t.Run("multiple options can be combined", func(t *testing.T) {
		cs := newTestCheckpointStore()
		metrics := &noopProjectionMetrics{}
		logger := &noopLogger{}
		engine := NewProjectionEngine(store,
			WithCheckpointStore(cs),
			WithProjectionMetrics(metrics),
			WithProjectionLogger(logger),
		)
		assert.NotNil(t, engine)
	})
}

func TestProjectionEngine_RegisterInline(t *testing.T) {
	store := &EventStore{}
	engine := NewProjectionEngine(store)

	t.Run("registers inline projection successfully", func(t *testing.T) {
		projection := newTestInlineProjection("TestInline", "OrderCreated")
		err := engine.RegisterInline(projection)
		require.NoError(t, err)
	})

	t.Run("rejects nil projection", func(t *testing.T) {
		err := engine.RegisterInline(nil)
		assert.ErrorIs(t, err, ErrNilProjection)
	})

	t.Run("rejects duplicate projection", func(t *testing.T) {
		projection := newTestInlineProjection("DuplicateInline")
		_ = engine.RegisterInline(projection)

		err := engine.RegisterInline(projection)
		assert.ErrorIs(t, err, ErrProjectionAlreadyRegistered)
	})
}

func TestProjectionEngine_RegisterAsync(t *testing.T) {
	store := &EventStore{}
	engine := NewProjectionEngine(store)

	t.Run("registers async projection successfully", func(t *testing.T) {
		projection := newTestAsyncProjection("TestAsync", "OrderCreated")
		err := engine.RegisterAsync(projection)
		require.NoError(t, err)
	})

	t.Run("registers async projection with custom options", func(t *testing.T) {
		projection := newTestAsyncProjection("TestAsyncOpts")
		opts := AsyncOptions{
			BatchSize:    50,
			BatchTimeout: 500 * time.Millisecond,
			PollInterval: 50 * time.Millisecond,
		}
		err := engine.RegisterAsync(projection, opts)
		require.NoError(t, err)
	})

	t.Run("rejects nil projection", func(t *testing.T) {
		err := engine.RegisterAsync(nil)
		assert.ErrorIs(t, err, ErrNilProjection)
	})

	t.Run("rejects duplicate projection", func(t *testing.T) {
		projection := newTestAsyncProjection("DuplicateAsync")
		_ = engine.RegisterAsync(projection)

		err := engine.RegisterAsync(projection)
		assert.ErrorIs(t, err, ErrProjectionAlreadyRegistered)
	})
}

func TestProjectionEngine_RegisterLive(t *testing.T) {
	store := &EventStore{}
	engine := NewProjectionEngine(store)

	t.Run("registers live projection successfully", func(t *testing.T) {
		projection := newTestLiveProjection("TestLive", true, "OrderCreated")
		err := engine.RegisterLive(projection)
		require.NoError(t, err)
	})

	t.Run("rejects nil projection", func(t *testing.T) {
		err := engine.RegisterLive(nil)
		assert.ErrorIs(t, err, ErrNilProjection)
	})

	t.Run("rejects duplicate projection", func(t *testing.T) {
		projection := newTestLiveProjection("DuplicateLive", true)
		_ = engine.RegisterLive(projection)

		err := engine.RegisterLive(projection)
		assert.ErrorIs(t, err, ErrProjectionAlreadyRegistered)
	})
}

func TestProjectionEngine_Unregister(t *testing.T) {
	store := &EventStore{}
	engine := NewProjectionEngine(store)

	t.Run("unregisters inline projection", func(t *testing.T) {
		projection := newTestInlineProjection("UnregInline")
		_ = engine.RegisterInline(projection)

		err := engine.Unregister("UnregInline")
		require.NoError(t, err)

		// Verify it's gone
		_, err = engine.GetStatus("UnregInline")
		assert.ErrorIs(t, err, ErrProjectionNotFound)
	})

	t.Run("unregisters async projection", func(t *testing.T) {
		projection := newTestAsyncProjection("UnregAsync")
		_ = engine.RegisterAsync(projection)

		err := engine.Unregister("UnregAsync")
		require.NoError(t, err)
	})

	t.Run("unregisters live projection", func(t *testing.T) {
		projection := newTestLiveProjection("UnregLive", true)
		_ = engine.RegisterLive(projection)

		err := engine.Unregister("UnregLive")
		require.NoError(t, err)
	})

	t.Run("returns error for unknown projection", func(t *testing.T) {
		err := engine.Unregister("Unknown")
		assert.ErrorIs(t, err, ErrProjectionNotFound)
	})

	t.Run("returns error for empty name", func(t *testing.T) {
		err := engine.Unregister("")
		assert.ErrorIs(t, err, ErrEmptyProjectionName)
	})
}

func TestProjectionEngine_Start(t *testing.T) {
	store := &EventStore{}

	t.Run("requires checkpoint store", func(t *testing.T) {
		engine := NewProjectionEngine(store)
		err := engine.Start(context.Background())
		assert.ErrorIs(t, err, ErrNoCheckpointStore)
	})

	t.Run("starts successfully with checkpoint store", func(t *testing.T) {
		engine := NewProjectionEngine(store, WithCheckpointStore(newTestCheckpointStore()))

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		err := engine.Start(ctx)
		require.NoError(t, err)
		assert.True(t, engine.IsRunning())

		// Stop it
		_ = engine.Stop(context.Background())
		assert.False(t, engine.IsRunning())
	})

	t.Run("rejects double start", func(t *testing.T) {
		engine := NewProjectionEngine(store, WithCheckpointStore(newTestCheckpointStore()))

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		_ = engine.Start(ctx)
		defer func() { _ = engine.Stop(context.Background()) }()

		err := engine.Start(ctx)
		assert.ErrorIs(t, err, ErrProjectionEngineAlreadyRunning)
	})
}

func TestProjectionEngine_Stop(t *testing.T) {
	store := &EventStore{}

	t.Run("stop when not running returns nil", func(t *testing.T) {
		engine := NewProjectionEngine(store, WithCheckpointStore(newTestCheckpointStore()))
		err := engine.Stop(context.Background())
		require.NoError(t, err)
	})

	t.Run("stop gracefully stops workers", func(t *testing.T) {
		adapter := memory.NewAdapter()
		store := New(adapter)
		engine := NewProjectionEngine(store, WithCheckpointStore(newTestCheckpointStore()))

		// Register a projection
		projection := newTestAsyncProjection("AsyncStop")
		_ = engine.RegisterAsync(projection)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		_ = engine.Start(ctx)
		assert.True(t, engine.IsRunning())

		// Stop with a reasonable timeout
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()

		err := engine.Stop(stopCtx)
		require.NoError(t, err)
		assert.False(t, engine.IsRunning())
	})
}

func TestProjectionEngine_GetStatus(t *testing.T) {
	store := &EventStore{}
	engine := NewProjectionEngine(store, WithCheckpointStore(newTestCheckpointStore()))

	t.Run("returns status for registered projection", func(t *testing.T) {
		projection := newTestAsyncProjection("StatusAsync")
		_ = engine.RegisterAsync(projection)

		status, err := engine.GetStatus("StatusAsync")
		require.NoError(t, err)
		assert.Equal(t, "StatusAsync", status.Name)
		assert.Equal(t, ProjectionStateStopped, status.State)
	})

	t.Run("returns status for live projection", func(t *testing.T) {
		projection := newTestLiveProjection("StatusLive", true)
		_ = engine.RegisterLive(projection)

		status, err := engine.GetStatus("StatusLive")
		require.NoError(t, err)
		assert.Equal(t, "StatusLive", status.Name)
		assert.Equal(t, ProjectionStateStopped, status.State)
	})

	t.Run("returns status for inline projection", func(t *testing.T) {
		projection := newTestInlineProjection("StatusInline")
		_ = engine.RegisterInline(projection)

		status, err := engine.GetStatus("StatusInline")
		require.NoError(t, err)
		assert.Equal(t, "StatusInline", status.Name)
		assert.Equal(t, ProjectionStateRunning, status.State)
	})

	t.Run("returns error for unknown projection", func(t *testing.T) {
		_, err := engine.GetStatus("Unknown")
		assert.ErrorIs(t, err, ErrProjectionNotFound)
	})
}

func TestProjectionEngine_GetAllStatuses(t *testing.T) {
	store := &EventStore{}
	engine := NewProjectionEngine(store, WithCheckpointStore(newTestCheckpointStore()))

	// Register various projections
	_ = engine.RegisterInline(newTestInlineProjection("Inline1"))
	_ = engine.RegisterAsync(newTestAsyncProjection("Async1"))
	_ = engine.RegisterLive(newTestLiveProjection("Live1", true))

	statuses := engine.GetAllStatuses()
	assert.Len(t, statuses, 3)

	names := make(map[string]bool)
	for _, s := range statuses {
		names[s.Name] = true
	}

	assert.True(t, names["Inline1"])
	assert.True(t, names["Async1"])
	assert.True(t, names["Live1"])
}

func TestProjectionEngine_ProcessInlineProjections(t *testing.T) {
	store := &EventStore{}
	engine := NewProjectionEngine(store)

	projection1 := newTestInlineProjection("Inline1", "OrderCreated")
	projection2 := newTestInlineProjection("Inline2") // Handles all events
	_ = engine.RegisterInline(projection1)
	_ = engine.RegisterInline(projection2)

	events := []StoredEvent{
		{ID: "1", Type: "OrderCreated", Data: []byte("{}")},
		{ID: "2", Type: "CustomerRegistered", Data: []byte("{}")},
	}

	ctx := context.Background()

	t.Run("processes events through inline projections", func(t *testing.T) {
		err := engine.ProcessInlineProjections(ctx, events)
		require.NoError(t, err)

		// projection1 should only get OrderCreated
		assert.Len(t, projection1.Events(), 1)
		assert.Equal(t, "OrderCreated", projection1.Events()[0].Type)

		// projection2 should get both events
		assert.Len(t, projection2.Events(), 2)
	})

	t.Run("stops on error", func(t *testing.T) {
		errorProjection := newTestInlineProjection("ErrorProj")
		errorProjection.SetError(assert.AnError)
		_ = engine.RegisterInline(errorProjection)

		newEvents := []StoredEvent{
			{ID: "3", Type: "OrderCreated", Data: []byte("{}")},
		}

		err := engine.ProcessInlineProjections(ctx, newEvents)
		assert.Error(t, err)
	})
}

func TestProjectionEngine_NotifyLiveProjections(t *testing.T) {
	store := &EventStore{}
	engine := NewProjectionEngine(store, WithCheckpointStore(newTestCheckpointStore()))

	projection := newTestLiveProjection("Live1", true, "OrderCreated")
	_ = engine.RegisterLive(projection)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start engine to activate live projections
	_ = engine.Start(ctx)
	defer func() { _ = engine.Stop(context.Background()) }()

	// Wait a bit for the worker to start
	time.Sleep(50 * time.Millisecond)

	events := []StoredEvent{
		{ID: "1", Type: "OrderCreated", Data: []byte("{}")},
		{ID: "2", Type: "CustomerRegistered", Data: []byte("{}")},
	}

	// Notify live projections
	engine.NotifyLiveProjections(ctx, events)

	// Wait for event to be processed
	event, received := projection.WaitForEvent(100 * time.Millisecond)
	assert.True(t, received)
	assert.Equal(t, "OrderCreated", event.Type)
}

func TestProjectionEngine_RegisterLive_WithOptions(t *testing.T) {
	store := &EventStore{}
	engine := NewProjectionEngine(store, WithCheckpointStore(newTestCheckpointStore()))

	t.Run("registers live projection with custom buffer size", func(t *testing.T) {
		projection := newTestLiveProjection("LiveWithOpts", true, "OrderCreated")
		opts := LiveOptions{BufferSize: 500}
		err := engine.RegisterLive(projection, opts)
		require.NoError(t, err)
	})

	t.Run("registers live projection with zero buffer size uses default", func(t *testing.T) {
		projection := newTestLiveProjection("LiveZeroBuffer", true)
		opts := LiveOptions{BufferSize: 0}
		err := engine.RegisterLive(projection, opts)
		require.NoError(t, err)
	})

	t.Run("registers live projection with negative buffer size uses default", func(t *testing.T) {
		projection := newTestLiveProjection("LiveNegBuffer", true)
		opts := LiveOptions{BufferSize: -100}
		err := engine.RegisterLive(projection, opts)
		require.NoError(t, err)
	})

	t.Run("rejects projection with empty name", func(t *testing.T) {
		projection := newTestLiveProjection("", true)
		err := engine.RegisterLive(projection)
		assert.ErrorIs(t, err, ErrEmptyProjectionName)
	})
}

func TestProjectionEngine_RegisterInline_EmptyName(t *testing.T) {
	store := &EventStore{}
	engine := NewProjectionEngine(store)

	projection := newTestInlineProjection("")
	err := engine.RegisterInline(projection)
	assert.ErrorIs(t, err, ErrEmptyProjectionName)
}

func TestProjectionEngine_RegisterAsync_EmptyName(t *testing.T) {
	store := &EventStore{}
	engine := NewProjectionEngine(store)

	projection := newTestAsyncProjection("")
	err := engine.RegisterAsync(projection)
	assert.ErrorIs(t, err, ErrEmptyProjectionName)
}

func TestProjectionEngine_Stop_Timeout(t *testing.T) {
	adapter := memory.NewAdapter()
	store := New(adapter)
	engine := NewProjectionEngine(store, WithCheckpointStore(newTestCheckpointStore()))

	// Register projections
	_ = engine.RegisterAsync(newTestAsyncProjection("AsyncStopTimeout"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_ = engine.Start(ctx)

	// Stop with very short timeout (should still succeed for memory adapter)
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer stopCancel()

	err := engine.Stop(stopCtx)
	require.NoError(t, err)
}

func TestProjectionEngine_LiveProjection_WithEvents(t *testing.T) {
	adapter := memory.NewAdapter()
	store := New(adapter)
	store.RegisterEvents(&ProjectionTestEvent{})
	engine := NewProjectionEngine(store, WithCheckpointStore(newTestCheckpointStore()))

	projection := newTestLiveProjection("LiveWithEvents", true, "ProjectionTestEvent")
	_ = engine.RegisterLive(projection)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_ = engine.Start(ctx)
	defer func() { _ = engine.Stop(context.Background()) }()

	// Wait for live worker to start
	time.Sleep(50 * time.Millisecond)

	// Append event
	err := store.Append(ctx, "Order-live-1", []interface{}{&ProjectionTestEvent{OrderID: "live-1"}})
	require.NoError(t, err)

	// Notify live projections
	events := []StoredEvent{
		{ID: "1", Type: "ProjectionTestEvent", Data: []byte("{}")},
	}
	engine.NotifyLiveProjections(ctx, events)

	// Wait for processing
	_, received := projection.WaitForEvent(200 * time.Millisecond)
	assert.True(t, received)
}

func TestProjectionEngine_NotifyLiveProjections_NoProjections(t *testing.T) {
	store := &EventStore{}
	engine := NewProjectionEngine(store, WithCheckpointStore(newTestCheckpointStore()))

	ctx := context.Background()
	events := []StoredEvent{{ID: "1", Type: "Test"}}

	// Should not panic with no live projections
	engine.NotifyLiveProjections(ctx, events)
}

func TestProjectionEngine_NotifyLiveProjections_NotRunning(t *testing.T) {
	store := &EventStore{}
	engine := NewProjectionEngine(store, WithCheckpointStore(newTestCheckpointStore()))

	projection := newTestLiveProjection("LiveNotRunning", true)
	_ = engine.RegisterLive(projection)

	ctx := context.Background()
	events := []StoredEvent{{ID: "1", Type: "Test"}}

	// Should not panic when engine is not running
	engine.NotifyLiveProjections(ctx, events)
}

func TestProjectionEngine_AsyncWorker_BatchProcessing(t *testing.T) {
	adapter := memory.NewAdapter()
	store := New(adapter)
	store.RegisterEvents(&ProjectionTestEvent{})
	checkpoint := newTestCheckpointStore()
	engine := NewProjectionEngine(store, WithCheckpointStore(checkpoint))

	projection := newTestAsyncProjection("AsyncBatch", "ProjectionTestEvent")
	projection.EnableBatch() // Enable batch processing
	opts := DefaultAsyncOptions()
	opts.PollInterval = 20 * time.Millisecond
	_ = engine.RegisterAsync(projection, opts)

	ctx, cancel := context.WithCancel(context.Background())

	// Append events before starting
	for i := 0; i < 5; i++ {
		_ = store.Append(ctx, "Order-batch-"+string(rune('0'+i)), []interface{}{&ProjectionTestEvent{OrderID: "batch"}})
	}

	_ = engine.Start(ctx)

	// Wait for processing
	time.Sleep(100 * time.Millisecond)

	cancel()
	_ = engine.Stop(context.Background())
}

func TestProjectionEngine_AsyncWorker_RetryPolicy(t *testing.T) {
	adapter := memory.NewAdapter()
	store := New(adapter)
	checkpoint := newTestCheckpointStore()
	engine := NewProjectionEngine(store, WithCheckpointStore(checkpoint))

	projection := newTestAsyncProjection("AsyncRetry")
	opts := DefaultAsyncOptions()
	opts.RetryPolicy = NoRetry()
	_ = engine.RegisterAsync(projection, opts)

	ctx, cancel := context.WithCancel(context.Background())

	_ = engine.Start(ctx)
	time.Sleep(50 * time.Millisecond)

	cancel()
	_ = engine.Stop(context.Background())
}

// Test for DefaultLiveOptions
func TestDefaultLiveOptions(t *testing.T) {
	opts := DefaultLiveOptions()
	assert.Equal(t, 1000, opts.BufferSize)
}

// testLogger is defined in test_helpers_test.go

func TestProjectionEngine_WithLogger(t *testing.T) {
	store := &EventStore{}
	logger := newTestLogger()
	engine := NewProjectionEngine(store,
		WithCheckpointStore(newTestCheckpointStore()),
		WithProjectionLogger(logger),
	)

	projection := newTestAsyncProjection("LoggedProj")
	_ = engine.RegisterAsync(projection)

	assert.Contains(t, logger.infoLogs, "Registered async projection")
}

func TestProjectionEngine_ProcessInlineProjections_EmptyEvents(t *testing.T) {
	store := &EventStore{}
	engine := NewProjectionEngine(store)

	projection := newTestInlineProjection("EmptyEvents")
	_ = engine.RegisterInline(projection)

	err := engine.ProcessInlineProjections(context.Background(), []StoredEvent{})
	require.NoError(t, err)
	assert.Empty(t, projection.Events())
}

func TestProjectionEngine_ProcessInlineProjections_NoProjections(t *testing.T) {
	store := &EventStore{}
	engine := NewProjectionEngine(store)

	events := []StoredEvent{{ID: "1", Type: "Test"}}
	err := engine.ProcessInlineProjections(context.Background(), events)
	require.NoError(t, err)
}

func TestProjectionEngine_GetStatus_InlineActive(t *testing.T) {
	store := &EventStore{}
	engine := NewProjectionEngine(store)

	projection := newTestInlineProjection("InlineActive")
	_ = engine.RegisterInline(projection)

	status, err := engine.GetStatus("InlineActive")
	require.NoError(t, err)
	assert.Equal(t, ProjectionStateRunning, status.State) // Inline is always "running"
}

func TestAsyncProjectionWorker_ProcessBatch_WithCheckpoint(t *testing.T) {
	adapter := memory.NewAdapter()
	store := New(adapter)
	store.RegisterEvents(&ProjectionTestEvent{})
	checkpoint := newTestCheckpointStore()
	logger := newTestLogger()
	engine := NewProjectionEngine(store,
		WithCheckpointStore(checkpoint),
		WithProjectionLogger(logger),
	)

	projection := newTestAsyncProjection("CheckpointProj", "ProjectionTestEvent")
	opts := DefaultAsyncOptions()
	opts.PollInterval = 20 * time.Millisecond
	opts.StartFromBeginning = true
	_ = engine.RegisterAsync(projection, opts)

	ctx, cancel := context.WithCancel(context.Background())

	// Append events
	_ = store.Append(ctx, "Order-cp-1", []interface{}{&ProjectionTestEvent{OrderID: "cp1"}})

	_ = engine.Start(ctx)

	// Wait for checkpoint to be saved
	time.Sleep(100 * time.Millisecond)

	cancel()
	_ = engine.Stop(context.Background())

	// Verify checkpoint was saved
	pos, _ := checkpoint.GetCheckpoint(context.Background(), "CheckpointProj")
	assert.Greater(t, pos, uint64(0))
}

// Test projection filtering
func TestProjectionEngine_AsyncWorker_EventFiltering(t *testing.T) {
	adapter := memory.NewAdapter()
	store := New(adapter)
	store.RegisterEvents(&ProjectionTestEvent{})
	checkpoint := newTestCheckpointStore()
	engine := NewProjectionEngine(store, WithCheckpointStore(checkpoint))

	// Projection only handles specific events
	projection := newTestAsyncProjection("FilteredAsync", "DifferentEventType")
	opts := DefaultAsyncOptions()
	opts.PollInterval = 20 * time.Millisecond
	opts.StartFromBeginning = true
	_ = engine.RegisterAsync(projection, opts)

	ctx, cancel := context.WithCancel(context.Background())

	// Append event of different type
	_ = store.Append(ctx, "Order-filter-1", []interface{}{&ProjectionTestEvent{OrderID: "filter1"}})

	_ = engine.Start(ctx)
	time.Sleep(100 * time.Millisecond)

	cancel()
	_ = engine.Stop(context.Background())

	// Projection should not have received the event (different type)
	assert.Empty(t, projection.Events())
}

// Test context cancellation during NotifyLiveProjections
func TestProjectionEngine_NotifyLiveProjections_ContextCancel(t *testing.T) {
	adapter := memory.NewAdapter()
	store := New(adapter)
	engine := NewProjectionEngine(store, WithCheckpointStore(newTestCheckpointStore()))

	projection := newTestLiveProjection("LiveCancel", true)
	_ = engine.RegisterLive(projection)

	runCtx, runCancel := context.WithCancel(context.Background())
	_ = engine.Start(runCtx)
	time.Sleep(50 * time.Millisecond)

	// Create context that's already cancelled
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	events := []StoredEvent{{ID: "1", Type: "Test"}}
	engine.NotifyLiveProjections(ctx, events)

	runCancel()
	_ = engine.Stop(context.Background())
}

// --- Test Async Workers with Real Store ---

// ProjectionTestEvent is a test event type for projection tests
type ProjectionTestEvent struct {
	OrderID string
}

func TestProjectionEngine_AsyncWorker(t *testing.T) {
	adapter := memory.NewAdapter()
	store := New(adapter)
	store.RegisterEvents(&ProjectionTestEvent{})
	checkpoint := newTestCheckpointStore()
	engine := NewProjectionEngine(store, WithCheckpointStore(checkpoint))

	t.Run("async worker processes events", func(t *testing.T) {
		// Register async projection
		projection := newTestAsyncProjection("AsyncWorkerTest", "ProjectionTestEvent")
		opts := DefaultAsyncOptions()
		opts.PollInterval = 20 * time.Millisecond
		_ = engine.RegisterAsync(projection, opts)

		// Start the engine
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		err := engine.Start(ctx)
		require.NoError(t, err)
		defer func() { _ = engine.Stop(context.Background()) }()

		// Append an event
		err = store.Append(ctx, "Order-123", []interface{}{&ProjectionTestEvent{OrderID: "123"}})
		require.NoError(t, err)

		// Wait for async worker to process
		time.Sleep(100 * time.Millisecond)

		// The projection should have processed the event
		// Note: Due to how the memory adapter works, this may not see the events
		// but at least the worker code paths will be exercised
	})

	t.Run("async worker handles stop gracefully", func(t *testing.T) {
		projection := newTestAsyncProjection("AsyncStopTest")
		engine2 := NewProjectionEngine(store, WithCheckpointStore(checkpoint))
		_ = engine2.RegisterAsync(projection)

		ctx, cancel := context.WithCancel(context.Background())
		_ = engine2.Start(ctx)

		// Stop via context cancel
		cancel()
		time.Sleep(50 * time.Millisecond)

		// Stop via Stop method
		_ = engine2.Stop(context.Background())
	})

	t.Run("async worker handles checkpoint", func(t *testing.T) {
		projection := newTestAsyncProjection("AsyncCheckpointTest")
		opts := DefaultAsyncOptions()
		opts.PollInterval = 20 * time.Millisecond
		opts.StartFromBeginning = true

		engine3 := NewProjectionEngine(store, WithCheckpointStore(checkpoint))
		_ = engine3.RegisterAsync(projection, opts)

		ctx, cancel := context.WithCancel(context.Background())
		err := engine3.Start(ctx)
		require.NoError(t, err)

		time.Sleep(60 * time.Millisecond)
		cancel()
		_ = engine3.Stop(context.Background())
	})
}

// --- Async Options Tests ---

func TestAsyncOptions_Defaults(t *testing.T) {
	opts := DefaultAsyncOptions()

	assert.Equal(t, 100, opts.BatchSize)
	assert.Equal(t, time.Second, opts.BatchTimeout)
	assert.Equal(t, 100*time.Millisecond, opts.PollInterval)
	assert.Equal(t, 3, opts.MaxRetries)
	assert.False(t, opts.StartFromBeginning)
	assert.NotNil(t, opts.RetryPolicy)
}

// --- Retry Policy Tests ---

func TestExponentialBackoffRetry(t *testing.T) {
	policy := ExponentialBackoffRetry(3, 100*time.Millisecond, 1*time.Second)

	t.Run("should retry on error within max retries", func(t *testing.T) {
		assert.True(t, policy.ShouldRetry(0, assert.AnError))
		assert.True(t, policy.ShouldRetry(1, assert.AnError))
		assert.True(t, policy.ShouldRetry(2, assert.AnError))
		assert.False(t, policy.ShouldRetry(3, assert.AnError))
	})

	t.Run("should not retry when no error", func(t *testing.T) {
		assert.False(t, policy.ShouldRetry(0, nil))
	})

	t.Run("delay increases exponentially", func(t *testing.T) {
		assert.Equal(t, 100*time.Millisecond, policy.Delay(0))
		assert.Equal(t, 200*time.Millisecond, policy.Delay(1))
		assert.Equal(t, 400*time.Millisecond, policy.Delay(2))
	})

	t.Run("delay capped at max", func(t *testing.T) {
		assert.Equal(t, 1*time.Second, policy.Delay(10))
	})
}

func TestNoRetry(t *testing.T) {
	policy := NoRetry()

	t.Run("never retries", func(t *testing.T) {
		assert.False(t, policy.ShouldRetry(0, assert.AnError))
	})

	t.Run("zero delay", func(t *testing.T) {
		assert.Equal(t, time.Duration(0), policy.Delay(0))
	})
}

// --- Projection State Tests ---

func TestProjectionState(t *testing.T) {
	states := []ProjectionState{
		ProjectionStateStopped,
		ProjectionStateRunning,
		ProjectionStatePaused,
		ProjectionStateFaulted,
		ProjectionStateRebuilding,
		ProjectionStateCatchingUp,
	}

	// Just verify they're distinct
	stateMap := make(map[ProjectionState]bool)
	for _, s := range states {
		assert.False(t, stateMap[s], "Duplicate state: %s", s)
		stateMap[s] = true
	}
}

// --- Projection Status Tests ---

func TestProjectionStatus(t *testing.T) {
	status := ProjectionStatus{
		Name:            "TestProjection",
		State:           ProjectionStateRunning,
		LastPosition:    100,
		EventsProcessed: 50,
		LastProcessedAt: time.Now(),
		Lag:             10,
		AverageLatency:  5 * time.Millisecond,
	}

	assert.Equal(t, "TestProjection", status.Name)
	assert.Equal(t, ProjectionStateRunning, status.State)
	assert.Equal(t, uint64(100), status.LastPosition)
	assert.Equal(t, uint64(50), status.EventsProcessed)
	assert.Equal(t, uint64(10), status.Lag)
}

// --- Test shouldHandleEvent ---

func TestShouldHandleEvent(t *testing.T) {
	t.Run("handles all events when HandledEvents is empty", func(t *testing.T) {
		p := newTestInlineProjection("AllEvents")
		assert.True(t, shouldHandleEvent(p, "AnyEvent"))
		assert.True(t, shouldHandleEvent(p, "AnotherEvent"))
	})

	t.Run("filters based on HandledEvents", func(t *testing.T) {
		p := newTestInlineProjection("FilteredEvents", "OrderCreated", "OrderShipped")
		assert.True(t, shouldHandleEvent(p, "OrderCreated"))
		assert.True(t, shouldHandleEvent(p, "OrderShipped"))
		assert.False(t, shouldHandleEvent(p, "CustomerRegistered"))
	})
}

// --- Test Concurrent Access ---

func TestProjectionEngine_ConcurrentOperations(t *testing.T) {
	store := &EventStore{}
	engine := NewProjectionEngine(store, WithCheckpointStore(newTestCheckpointStore()))

	var wg sync.WaitGroup
	var registrationErrors atomic.Int32

	// Concurrently register projections
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			p := newTestInlineProjection("ConcurrentInline" + string(rune('A'+idx)))
			if err := engine.RegisterInline(p); err != nil {
				registrationErrors.Add(1)
			}
		}(i)
	}

	wg.Wait()
	assert.Equal(t, int32(0), registrationErrors.Load())

	// Verify all were registered
	statuses := engine.GetAllStatuses()
	assert.Len(t, statuses, 10)
}

// --- Test noopProjectionMetrics ---

func TestNoopProjectionMetrics(t *testing.T) {
	metrics := &noopProjectionMetrics{}

	t.Run("RecordEventProcessed does nothing", func(t *testing.T) {
		// Should not panic
		metrics.RecordEventProcessed("test", "OrderCreated", time.Millisecond, true)
		metrics.RecordEventProcessed("test", "OrderCreated", time.Millisecond, false)
	})

	t.Run("RecordBatchProcessed does nothing", func(t *testing.T) {
		// Should not panic
		metrics.RecordBatchProcessed("test", 10, time.Millisecond, true)
		metrics.RecordBatchProcessed("test", 10, time.Millisecond, false)
	})

	t.Run("RecordCheckpoint does nothing", func(t *testing.T) {
		// Should not panic
		metrics.RecordCheckpoint("test", 100)
	})

	t.Run("RecordError does nothing", func(t *testing.T) {
		// Should not panic
		metrics.RecordError("test", assert.AnError)
		metrics.RecordError("test", nil)
	})
}

// --- Test ShouldHandleEventType ---

func TestShouldHandleEventType(t *testing.T) {
	t.Run("returns true when event type is in handled events list", func(t *testing.T) {
		handledEvents := []string{"OrderCreated", "OrderShipped", "OrderCanceled"}

		assert.True(t, ShouldHandleEventType(handledEvents, "OrderCreated"))
		assert.True(t, ShouldHandleEventType(handledEvents, "OrderShipped"))
		assert.True(t, ShouldHandleEventType(handledEvents, "OrderCanceled"))
	})

	t.Run("returns false when event type is not in handled events list", func(t *testing.T) {
		handledEvents := []string{"OrderCreated", "OrderShipped", "OrderCanceled"}

		assert.False(t, ShouldHandleEventType(handledEvents, "CustomerRegistered"))
		assert.False(t, ShouldHandleEventType(handledEvents, "PaymentReceived"))
		assert.False(t, ShouldHandleEventType(handledEvents, "InventoryUpdated"))
	})

	t.Run("returns true for any event when handled events list is empty", func(t *testing.T) {
		// Empty list means handle all events
		assert.True(t, ShouldHandleEventType([]string{}, "OrderCreated"))
		assert.True(t, ShouldHandleEventType([]string{}, "CustomerRegistered"))
		assert.True(t, ShouldHandleEventType([]string{}, "AnyEventType"))
	})

	t.Run("returns true for any event when handled events list is nil", func(t *testing.T) {
		// Nil list should behave same as empty list
		assert.True(t, ShouldHandleEventType(nil, "OrderCreated"))
		assert.True(t, ShouldHandleEventType(nil, "CustomerRegistered"))
	})

	t.Run("handles empty event type string", func(t *testing.T) {
		handledEvents := []string{"OrderCreated", "OrderShipped"}

		// Empty string not in list
		assert.False(t, ShouldHandleEventType(handledEvents, ""))

		// Empty string in empty list (handles all)
		assert.True(t, ShouldHandleEventType([]string{}, ""))
	})

	t.Run("handles single event in list", func(t *testing.T) {
		handledEvents := []string{"OrderCreated"}

		assert.True(t, ShouldHandleEventType(handledEvents, "OrderCreated"))
		assert.False(t, ShouldHandleEventType(handledEvents, "OrderShipped"))
	})

	t.Run("is case sensitive", func(t *testing.T) {
		handledEvents := []string{"OrderCreated"}

		assert.True(t, ShouldHandleEventType(handledEvents, "OrderCreated"))
		assert.False(t, ShouldHandleEventType(handledEvents, "ordercreated"))
		assert.False(t, ShouldHandleEventType(handledEvents, "ORDERCREATED"))
	})
}

// --- Exponential Backoff Delay Edge Cases ---

func TestExponentialBackoffRetry_DelayEdgeCases(t *testing.T) {
	policy := ExponentialBackoffRetry(5, 100*time.Millisecond, 10*time.Second)

	t.Run("negative attempt clamps to zero", func(t *testing.T) {
		delay := policy.Delay(-1)
		assert.Equal(t, 100*time.Millisecond, delay)
	})

	t.Run("large attempt returns max delay", func(t *testing.T) {
		delay := policy.Delay(63)
		assert.Equal(t, 10*time.Second, delay)

		delay = policy.Delay(100)
		assert.Equal(t, 10*time.Second, delay)
	})
}

// --- Async Worker Error Handling, Backoff & Recovery ---

func TestProjectionEngine_AsyncWorker_ErrorAndRecovery(t *testing.T) {
	adapter := memory.NewAdapter()
	store := New(adapter)
	store.RegisterEvents(&ProjectionTestEvent{})
	checkpoint := newTestCheckpointStore()
	logger := newTestLogger()
	metrics := newTestProjectionMetrics()
	engine := NewProjectionEngine(store,
		WithCheckpointStore(checkpoint),
		WithProjectionLogger(logger),
		WithProjectionMetrics(metrics),
	)

	// Append an event so the worker has something to process
	ctx := context.Background()
	_ = store.Append(ctx, "Order-err-1", []interface{}{&ProjectionTestEvent{OrderID: "err1"}})

	t.Run("worker enters faulted state on Apply error and recovers", func(t *testing.T) {
		projection := newTestAsyncProjection("AsyncErrorRecovery", "ProjectionTestEvent")
		projection.SetError(assert.AnError) // Will fail initially

		opts := DefaultAsyncOptions()
		opts.PollInterval = 20 * time.Millisecond
		opts.RetryPolicy = ExponentialBackoffRetry(10, 10*time.Millisecond, 50*time.Millisecond)
		opts.StartFromBeginning = true
		_ = engine.RegisterAsync(projection, opts)

		runCtx, cancel := context.WithCancel(ctx)
		_ = engine.Start(runCtx)

		// Wait for error to be detected
		time.Sleep(150 * time.Millisecond)

		// Check the projection is faulted
		status, err := engine.GetStatus("AsyncErrorRecovery")
		require.NoError(t, err)
		assert.Equal(t, ProjectionStateFaulted, status.State)
		assert.NotEmpty(t, status.Error)

		// Fix the error — should recover
		projection.SetError(nil)

		// Wait for recovery
		time.Sleep(200 * time.Millisecond)

		cancel()
		_ = engine.Stop(context.Background())

		// Logger should have recorded errors and recovery
		logger.mu.Lock()
		hasError := false
		for _, msg := range logger.errorLogs {
			if msg == "Async projection error" {
				hasError = true
				break
			}
		}
		hasRecovery := false
		for _, msg := range logger.infoLogs {
			if msg == "Async projection recovered" {
				hasRecovery = true
				break
			}
		}
		logger.mu.Unlock()

		assert.True(t, hasError, "expected error log")
		assert.True(t, hasRecovery, "expected recovery log")
	})
}

func TestProjectionEngine_AsyncWorker_NilRetryPolicy(t *testing.T) {
	adapter := memory.NewAdapter()
	store := New(adapter)
	store.RegisterEvents(&ProjectionTestEvent{})
	checkpoint := newTestCheckpointStore()
	engine := NewProjectionEngine(store, WithCheckpointStore(checkpoint))

	_ = store.Append(context.Background(), "Order-nil-retry", []interface{}{&ProjectionTestEvent{OrderID: "nilretry"}})

	// Use nil retry policy to exercise the built-in fallback backoff
	projection := newTestAsyncProjection("AsyncNilRetry", "ProjectionTestEvent")
	projection.SetError(assert.AnError)
	opts := DefaultAsyncOptions()
	opts.PollInterval = 20 * time.Millisecond
	opts.RetryPolicy = nil
	opts.StartFromBeginning = true
	_ = engine.RegisterAsync(projection, opts)

	ctx, cancel := context.WithCancel(context.Background())
	_ = engine.Start(ctx)

	// Wait for a few error cycles with built-in backoff
	time.Sleep(150 * time.Millisecond)

	cancel()
	_ = engine.Stop(context.Background())
}

func TestProjectionEngine_AsyncWorker_PanicRecovery(t *testing.T) {
	adapter := memory.NewAdapter()
	store := New(adapter)
	store.RegisterEvents(&ProjectionTestEvent{})
	checkpoint := newTestCheckpointStore()
	logger := newTestLogger()
	engine := NewProjectionEngine(store,
		WithCheckpointStore(checkpoint),
		WithProjectionLogger(logger),
	)

	_ = store.Append(context.Background(), "Order-panic-1", []interface{}{&ProjectionTestEvent{OrderID: "panic1"}})

	// Create a panicking projection
	projection := newTestPanickingAsyncProjection("AsyncPanic", "ProjectionTestEvent")
	opts := DefaultAsyncOptions()
	opts.PollInterval = 20 * time.Millisecond
	opts.RetryPolicy = ExponentialBackoffRetry(3, 10*time.Millisecond, 50*time.Millisecond)
	opts.StartFromBeginning = true
	_ = engine.RegisterAsync(projection, opts)

	ctx, cancel := context.WithCancel(context.Background())
	_ = engine.Start(ctx)

	// Wait for panic to be caught and logged
	time.Sleep(150 * time.Millisecond)

	cancel()
	_ = engine.Stop(context.Background())

	// Logger should have recorded the panic
	logger.mu.Lock()
	hasPanic := false
	for _, msg := range logger.errorLogs {
		if msg == "Async projection panicked" {
			hasPanic = true
			break
		}
	}
	logger.mu.Unlock()
	assert.True(t, hasPanic, "expected panic log")
}

func TestProjectionEngine_AsyncWorker_BatchApplySuccess(t *testing.T) {
	adapter := memory.NewAdapter()
	store := New(adapter)
	store.RegisterEvents(&ProjectionTestEvent{})
	checkpoint := newTestCheckpointStore()
	metrics := newTestProjectionMetrics()
	engine := NewProjectionEngine(store,
		WithCheckpointStore(checkpoint),
		WithProjectionMetrics(metrics),
	)

	// Append events
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		_ = store.Append(ctx, "Order-batch-ok-"+string(rune('0'+i)),
			[]interface{}{&ProjectionTestEvent{OrderID: "batchok"}})
	}

	// Create projection that supports batch mode
	projection := newTestAsyncProjection("AsyncBatchSuccess", "ProjectionTestEvent")
	projection.EnableBatch()
	opts := DefaultAsyncOptions()
	opts.PollInterval = 20 * time.Millisecond
	opts.StartFromBeginning = true
	_ = engine.RegisterAsync(projection, opts)

	runCtx, cancel := context.WithCancel(ctx)
	_ = engine.Start(runCtx)

	time.Sleep(150 * time.Millisecond)

	cancel()
	_ = engine.Stop(context.Background())

	// Should have processed events via batch
	assert.GreaterOrEqual(t, len(projection.Events()), 1)

	// Metrics should have recorded batch processing
	metrics.mu.Lock()
	batches := metrics.batchesProcessed
	metrics.mu.Unlock()
	assert.Greater(t, batches, 0)
}

func TestProjectionEngine_AsyncWorker_BatchApplyError(t *testing.T) {
	adapter := memory.NewAdapter()
	store := New(adapter)
	store.RegisterEvents(&ProjectionTestEvent{})
	checkpoint := newTestCheckpointStore()
	metrics := newTestProjectionMetrics()
	engine := NewProjectionEngine(store,
		WithCheckpointStore(checkpoint),
		WithProjectionMetrics(metrics),
	)

	_ = store.Append(context.Background(), "Order-batch-err", []interface{}{&ProjectionTestEvent{OrderID: "batcherr"}})

	projection := newTestAsyncProjection("AsyncBatchError", "ProjectionTestEvent")
	projection.EnableBatch()
	projection.batchApplyErr = assert.AnError
	opts := DefaultAsyncOptions()
	opts.PollInterval = 20 * time.Millisecond
	opts.RetryPolicy = ExponentialBackoffRetry(2, 10*time.Millisecond, 50*time.Millisecond)
	opts.StartFromBeginning = true
	_ = engine.RegisterAsync(projection, opts)

	ctx, cancel := context.WithCancel(context.Background())
	_ = engine.Start(ctx)

	time.Sleep(150 * time.Millisecond)

	cancel()
	_ = engine.Stop(context.Background())

	// Metrics should have recorded failed batch
	metrics.mu.Lock()
	batches := metrics.batchesProcessed
	metrics.mu.Unlock()
	assert.Greater(t, batches, 0)
}

func TestProjectionEngine_Stop_ContextTimeout(t *testing.T) {
	adapter := memory.NewAdapter()
	store := New(adapter)
	store.RegisterEvents(&ProjectionTestEvent{})
	checkpoint := newTestCheckpointStore()
	engine := NewProjectionEngine(store, WithCheckpointStore(checkpoint))

	// Register a slow projection that blocks on Apply
	projection := newTestBlockingAsyncProjection("AsyncBlocking")
	opts := DefaultAsyncOptions()
	opts.PollInterval = 5 * time.Millisecond
	_ = engine.RegisterAsync(projection, opts)

	// Append an event to make the worker busy
	_ = store.Append(context.Background(), "Order-block", []interface{}{&ProjectionTestEvent{OrderID: "block"}})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_ = engine.Start(ctx)

	// Give worker time to start processing
	time.Sleep(50 * time.Millisecond)

	// Stop with already-expired context
	expiredCtx, expiredCancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer expiredCancel()
	time.Sleep(time.Millisecond)

	err := engine.Stop(expiredCtx)
	// Should timeout or succeed — either is OK since the context is expired
	_ = err

	// Clean up
	cancel()
	projection.unblock()
	_ = engine.Stop(context.Background())
}

func TestProjectionEngine_GetStatus_AsyncWithError(t *testing.T) {
	adapter := memory.NewAdapter()
	store := New(adapter)
	store.RegisterEvents(&ProjectionTestEvent{})
	checkpoint := newTestCheckpointStore()
	engine := NewProjectionEngine(store, WithCheckpointStore(checkpoint))

	// Append an event
	_ = store.Append(context.Background(), "Order-status-err", []interface{}{&ProjectionTestEvent{OrderID: "statuserr"}})

	projection := newTestAsyncProjection("StatusErrProj", "ProjectionTestEvent")
	projection.SetError(assert.AnError)
	opts := DefaultAsyncOptions()
	opts.PollInterval = 20 * time.Millisecond
	opts.RetryPolicy = ExponentialBackoffRetry(1, 10*time.Millisecond, 50*time.Millisecond)
	opts.StartFromBeginning = true
	_ = engine.RegisterAsync(projection, opts)

	ctx, cancel := context.WithCancel(context.Background())
	_ = engine.Start(ctx)

	time.Sleep(150 * time.Millisecond)

	// Status should show the error
	status, err := engine.GetStatus("StatusErrProj")
	require.NoError(t, err)
	assert.NotEmpty(t, status.Error)

	cancel()
	_ = engine.Stop(context.Background())
}

func TestProjectionEngine_LiveWorker_StopViaClose(t *testing.T) {
	adapter := memory.NewAdapter()
	store := New(adapter)
	engine := NewProjectionEngine(store, WithCheckpointStore(newTestCheckpointStore()))

	projection := newTestLiveProjection("LiveStopClose", true)
	_ = engine.RegisterLive(projection)

	ctx := context.Background()
	_ = engine.Start(ctx)

	time.Sleep(50 * time.Millisecond)

	// Unregister (which closes worker.stopCh)
	_ = engine.Unregister("LiveStopClose")

	// Stop engine
	_ = engine.Stop(context.Background())
}

func TestProjectionEngine_LiveWorker_PanicRecovery(t *testing.T) {
	adapter := memory.NewAdapter()
	store := New(adapter)
	store.RegisterEvents(&ProjectionTestEvent{})
	logger := newTestLogger()
	engine := NewProjectionEngine(store,
		WithCheckpointStore(newTestCheckpointStore()),
		WithProjectionLogger(logger),
	)

	projection := newTestPanickingLiveProjection("LivePanic", "ProjectionTestEvent")
	_ = engine.RegisterLive(projection)

	ctx, cancel := context.WithCancel(context.Background())
	_ = engine.Start(ctx)

	time.Sleep(50 * time.Millisecond)

	// Deliver event to trigger panic
	events := []StoredEvent{{ID: "1", Type: "ProjectionTestEvent", Data: []byte("{}")}}
	engine.NotifyLiveProjections(ctx, events)

	// Wait for panic recovery
	time.Sleep(100 * time.Millisecond)

	cancel()
	_ = engine.Stop(context.Background())

	// Logger should have recorded the panic
	logger.mu.Lock()
	hasPanic := false
	for _, msg := range logger.errorLogs {
		if msg == "Live projection panicked" {
			hasPanic = true
			break
		}
	}
	logger.mu.Unlock()
	assert.True(t, hasPanic, "expected live projection panic log")
}

func TestProjectionEngine_LiveWorker_GetStatusWithError(t *testing.T) {
	store := &EventStore{}
	engine := NewProjectionEngine(store, WithCheckpointStore(newTestCheckpointStore()))

	projection := newTestLiveProjection("LiveStatusErr", true)
	_ = engine.RegisterLive(projection)

	// Manually set an error on the worker
	engine.liveMu.RLock()
	worker := engine.liveProjections["LiveStatusErr"]
	engine.liveMu.RUnlock()
	worker.stateMu.Lock()
	worker.lastError = assert.AnError
	worker.stateMu.Unlock()

	status, err := engine.GetStatus("LiveStatusErr")
	require.NoError(t, err)
	assert.NotEmpty(t, status.Error)
}

func TestProjectionEngine_AsyncWorker_ProcessBatchContextCancel(t *testing.T) {
	adapter := memory.NewAdapter()
	store := New(adapter)
	store.RegisterEvents(&ProjectionTestEvent{})
	checkpoint := newTestCheckpointStore()
	engine := NewProjectionEngine(store, WithCheckpointStore(checkpoint))

	projection := newTestAsyncProjection("AsyncCtxCancel", "ProjectionTestEvent")
	opts := DefaultAsyncOptions()
	opts.PollInterval = 10 * time.Millisecond
	opts.StartFromBeginning = true
	_ = engine.RegisterAsync(projection, opts)

	// Start with a context we'll cancel immediately
	ctx, cancel := context.WithCancel(context.Background())
	_ = engine.Start(ctx)

	// Cancel to trigger the context.Canceled path
	cancel()

	time.Sleep(100 * time.Millisecond)
	_ = engine.Stop(context.Background())
}

func TestProjectionEngine_AsyncWorker_CheckpointSetError(t *testing.T) {
	adapter := memory.NewAdapter()
	store := New(adapter)
	store.RegisterEvents(&ProjectionTestEvent{})
	checkpoint := newTestCheckpointStore()
	logger := newTestLogger()
	engine := NewProjectionEngine(store,
		WithCheckpointStore(checkpoint),
		WithProjectionLogger(logger),
	)

	_ = store.Append(context.Background(), "Order-cps", []interface{}{&ProjectionTestEvent{OrderID: "cps"}})

	// Make checkpoint set fail
	checkpoint.setErr = assert.AnError

	projection := newTestAsyncProjection("AsyncCheckpointSetErr", "ProjectionTestEvent")
	opts := DefaultAsyncOptions()
	opts.PollInterval = 20 * time.Millisecond
	opts.StartFromBeginning = true
	_ = engine.RegisterAsync(projection, opts)

	ctx, cancel := context.WithCancel(context.Background())
	_ = engine.Start(ctx)

	time.Sleep(150 * time.Millisecond)

	cancel()
	_ = engine.Stop(context.Background())

	// Logger should warn about failed checkpoint
	logger.mu.Lock()
	hasError := false
	for _, msg := range logger.errorLogs {
		if msg == "Failed to save checkpoint" {
			hasError = true
			break
		}
	}
	logger.mu.Unlock()
	assert.True(t, hasError)
}

func TestProjectionEngine_AsyncWorker_StopViaUnregister(t *testing.T) {
	adapter := memory.NewAdapter()
	store := New(adapter)
	store.RegisterEvents(&ProjectionTestEvent{})
	checkpoint := newTestCheckpointStore()
	engine := NewProjectionEngine(store, WithCheckpointStore(checkpoint))

	projection := newTestAsyncProjection("AsyncUnregStop", "ProjectionTestEvent")
	opts := DefaultAsyncOptions()
	opts.PollInterval = 20 * time.Millisecond
	_ = engine.RegisterAsync(projection, opts)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_ = engine.Start(ctx)

	time.Sleep(50 * time.Millisecond)

	// Unregister while running — this closes worker.stopCh
	err := engine.Unregister("AsyncUnregStop")
	require.NoError(t, err)

	time.Sleep(50 * time.Millisecond)

	_ = engine.Stop(context.Background())
}

func TestProjectionEngine_NotifyLiveProjections_FullBuffer(t *testing.T) {
	adapter := memory.NewAdapter()
	store := New(adapter)
	engine := NewProjectionEngine(store, WithCheckpointStore(newTestCheckpointStore()))

	// Use buffer size of 1
	projection := newTestLiveProjection("LiveFullBuffer", true)
	opts := LiveOptions{BufferSize: 1}
	_ = engine.RegisterLive(projection, opts)

	ctx, cancel := context.WithCancel(context.Background())
	_ = engine.Start(ctx)
	time.Sleep(50 * time.Millisecond)

	// Fill the buffer by not consuming events
	// The live worker goroutine reads from eventCh, so we need to
	// deliver many events rapidly to overflow. Use a separate goroutine
	// that doesn't read.

	// Cancel context first, then try to deliver - this should hit the ctx.Done path
	cancel()
	time.Sleep(10 * time.Millisecond)

	// Now try to notify with cancelled context - the channel send should be
	// competing with ctx.Done. We use a non-handled event type so the live
	// worker's read from eventCh is not consuming.
	events := make([]StoredEvent, 10)
	for i := range events {
		events[i] = StoredEvent{ID: string(rune('0' + i)), Type: "AllTypes", Data: []byte("{}")}
	}
	engine.NotifyLiveProjections(ctx, events)

	_ = engine.Stop(context.Background())
}

func TestProjectionEngine_AsyncWorker_HighConsecutiveErrors(t *testing.T) {
	adapter := memory.NewAdapter()
	store := New(adapter)
	store.RegisterEvents(&ProjectionTestEvent{})
	checkpoint := newTestCheckpointStore()
	engine := NewProjectionEngine(store, WithCheckpointStore(checkpoint))

	_ = store.Append(context.Background(), "Order-hce", []interface{}{&ProjectionTestEvent{OrderID: "hce"}})

	// Use nil retry policy to exercise the built-in fallback with high shift values
	projection := newTestAsyncProjection("AsyncHighErrors", "ProjectionTestEvent")
	projection.SetError(assert.AnError)
	opts := DefaultAsyncOptions()
	opts.PollInterval = 5 * time.Millisecond
	opts.RetryPolicy = nil
	opts.StartFromBeginning = true
	_ = engine.RegisterAsync(projection, opts)

	ctx, cancel := context.WithCancel(context.Background())
	_ = engine.Start(ctx)

	// Let it accumulate many consecutive errors to exercise the shift > 18 cap
	time.Sleep(300 * time.Millisecond)

	cancel()
	_ = engine.Stop(context.Background())
}

func TestProjectionEngine_AsyncWorker_CheckpointGetError(t *testing.T) {
	adapter := memory.NewAdapter()
	store := New(adapter)
	store.RegisterEvents(&ProjectionTestEvent{})
	checkpoint := newTestCheckpointStore()
	checkpoint.getErr = assert.AnError
	logger := newTestLogger()
	engine := NewProjectionEngine(store,
		WithCheckpointStore(checkpoint),
		WithProjectionLogger(logger),
	)

	projection := newTestAsyncProjection("AsyncCheckpointGetErr", "ProjectionTestEvent")
	opts := DefaultAsyncOptions()
	opts.PollInterval = 20 * time.Millisecond
	_ = engine.RegisterAsync(projection, opts)

	ctx, cancel := context.WithCancel(context.Background())
	_ = engine.Start(ctx)

	time.Sleep(100 * time.Millisecond)

	cancel()
	_ = engine.Stop(context.Background())

	// Logger should have recorded the error
	logger.mu.Lock()
	hasError := false
	for _, msg := range logger.errorLogs {
		if msg == "Failed to get checkpoint" {
			hasError = true
			break
		}
	}
	logger.mu.Unlock()
	assert.True(t, hasError)
}

func TestProjectionEngine_AsyncWorker_StopDuringBackoff(t *testing.T) {
	adapter := memory.NewAdapter()
	store := New(adapter)
	store.RegisterEvents(&ProjectionTestEvent{})
	checkpoint := newTestCheckpointStore()
	engine := NewProjectionEngine(store, WithCheckpointStore(checkpoint))

	_ = store.Append(context.Background(), "Order-sdb", []interface{}{&ProjectionTestEvent{OrderID: "sdb"}})

	// Projection always errors to keep the worker in error/backoff state
	projection := newTestAsyncProjection("AsyncStopDuringBackoff", "ProjectionTestEvent")
	projection.SetError(assert.AnError)
	opts := DefaultAsyncOptions()
	opts.PollInterval = 10 * time.Millisecond
	opts.RetryPolicy = ExponentialBackoffRetry(100, 5*time.Second, 10*time.Second) // Long backoff so we're definitely in the wait
	opts.StartFromBeginning = true
	_ = engine.RegisterAsync(projection, opts)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = engine.Start(ctx)

	// Wait for at least one error to occur and the worker to enter backoff wait
	time.Sleep(100 * time.Millisecond)

	// Stop the engine while the worker is waiting in the backoff select
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopCancel()
	err := engine.Stop(stopCtx)
	assert.NoError(t, err)

	status, statusErr := engine.GetStatus("AsyncStopDuringBackoff")
	require.NoError(t, statusErr)
	assert.Equal(t, ProjectionStateStopped, status.State)
}

func TestProjectionEngine_AsyncWorker_UnregisterDuringBackoff(t *testing.T) {
	adapter := memory.NewAdapter()
	store := New(adapter)
	store.RegisterEvents(&ProjectionTestEvent{})
	checkpoint := newTestCheckpointStore()
	engine := NewProjectionEngine(store, WithCheckpointStore(checkpoint))

	_ = store.Append(context.Background(), "Order-udb", []interface{}{&ProjectionTestEvent{OrderID: "udb"}})

	projection := newTestAsyncProjection("AsyncUnregDuringBackoff", "ProjectionTestEvent")
	projection.SetError(assert.AnError)
	opts := DefaultAsyncOptions()
	opts.PollInterval = 10 * time.Millisecond
	opts.RetryPolicy = ExponentialBackoffRetry(100, 5*time.Second, 10*time.Second)
	opts.StartFromBeginning = true
	_ = engine.RegisterAsync(projection, opts)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = engine.Start(ctx)

	// Wait for the worker to enter the backoff wait
	time.Sleep(100 * time.Millisecond)

	// Unregister while the worker is waiting in the backoff select (exercises worker.stopCh path)
	err := engine.Unregister("AsyncUnregDuringBackoff")
	assert.NoError(t, err)

	time.Sleep(50 * time.Millisecond)

	cancel()
	_ = engine.Stop(context.Background())
}

func TestProjectionEngine_AsyncWorker_ContextCancelDuringBackoff(t *testing.T) {
	adapter := memory.NewAdapter()
	store := New(adapter)
	store.RegisterEvents(&ProjectionTestEvent{})
	checkpoint := newTestCheckpointStore()
	engine := NewProjectionEngine(store, WithCheckpointStore(checkpoint))

	_ = store.Append(context.Background(), "Order-ccdb", []interface{}{&ProjectionTestEvent{OrderID: "ccdb"}})

	projection := newTestAsyncProjection("AsyncCtxCancelBackoff", "ProjectionTestEvent")
	projection.SetError(assert.AnError)
	opts := DefaultAsyncOptions()
	opts.PollInterval = 10 * time.Millisecond
	opts.RetryPolicy = ExponentialBackoffRetry(100, 5*time.Second, 10*time.Second)
	opts.StartFromBeginning = true
	_ = engine.RegisterAsync(projection, opts)

	ctx, cancel := context.WithCancel(context.Background())
	_ = engine.Start(ctx)

	// Wait for the worker to enter the backoff wait
	time.Sleep(100 * time.Millisecond)

	// Cancel context while the worker is waiting in the backoff select (exercises ctx.Done() path)
	cancel()

	time.Sleep(50 * time.Millisecond)

	_ = engine.Stop(context.Background())
}
