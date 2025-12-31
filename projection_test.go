package mink

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Test Projections ---

// testInlineProjection is a test inline projection.
type testInlineProjection struct {
	ProjectionBase
	events []StoredEvent
	mu     sync.Mutex
	err    error
}

func newTestInlineProjection(name string, handledEvents ...string) *testInlineProjection {
	return &testInlineProjection{
		ProjectionBase: NewProjectionBase(name, handledEvents...),
		events:         make([]StoredEvent, 0),
	}
}

func (p *testInlineProjection) Apply(ctx context.Context, event StoredEvent) error {
	if p.err != nil {
		return p.err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, event)
	return nil
}

func (p *testInlineProjection) Events() []StoredEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := make([]StoredEvent, len(p.events))
	copy(result, p.events)
	return result
}

func (p *testInlineProjection) SetError(err error) {
	p.err = err
}

// testAsyncProjection is a test async projection.
type testAsyncProjection struct {
	AsyncProjectionBase
	events        []StoredEvent
	mu            sync.Mutex
	applyErr      error
	batchApplyErr error
	supportsBatch bool
}

func newTestAsyncProjection(name string, handledEvents ...string) *testAsyncProjection {
	return &testAsyncProjection{
		AsyncProjectionBase: NewAsyncProjectionBase(name, handledEvents...),
		events:              make([]StoredEvent, 0),
		supportsBatch:       false,
	}
}

func (p *testAsyncProjection) Apply(ctx context.Context, event StoredEvent) error {
	if p.applyErr != nil {
		return p.applyErr
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, event)
	return nil
}

func (p *testAsyncProjection) ApplyBatch(ctx context.Context, events []StoredEvent) error {
	if !p.supportsBatch {
		return ErrNotImplemented
	}
	if p.batchApplyErr != nil {
		return p.batchApplyErr
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, events...)
	return nil
}

func (p *testAsyncProjection) Events() []StoredEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := make([]StoredEvent, len(p.events))
	copy(result, p.events)
	return result
}

func (p *testAsyncProjection) EnableBatch() {
	p.supportsBatch = true
}

// testLiveProjection is a test live projection.
type testLiveProjection struct {
	LiveProjectionBase
	events    []StoredEvent
	mu        sync.Mutex
	eventChan chan StoredEvent
}

func newTestLiveProjection(name string, transient bool, handledEvents ...string) *testLiveProjection {
	return &testLiveProjection{
		LiveProjectionBase: NewLiveProjectionBase(name, transient, handledEvents...),
		events:             make([]StoredEvent, 0),
		eventChan:          make(chan StoredEvent, 100),
	}
}

func (p *testLiveProjection) OnEvent(ctx context.Context, event StoredEvent) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, event)

	// Non-blocking send to channel for testing
	select {
	case p.eventChan <- event:
	default:
	}
}

func (p *testLiveProjection) Events() []StoredEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := make([]StoredEvent, len(p.events))
	copy(result, p.events)
	return result
}

func (p *testLiveProjection) WaitForEvent(timeout time.Duration) (StoredEvent, bool) {
	select {
	case event := <-p.eventChan:
		return event, true
	case <-time.After(timeout):
		return StoredEvent{}, false
	}
}

// --- In-Memory Checkpoint Store for Testing ---

type testCheckpointStore struct {
	checkpoints map[string]uint64
	mu          sync.RWMutex
	setErr      error
	getErr      error
}

func newTestCheckpointStore() *testCheckpointStore {
	return &testCheckpointStore{
		checkpoints: make(map[string]uint64),
	}
}

func (s *testCheckpointStore) GetCheckpoint(ctx context.Context, projectionName string) (uint64, error) {
	if s.getErr != nil {
		return 0, s.getErr
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.checkpoints[projectionName], nil
}

func (s *testCheckpointStore) SetCheckpoint(ctx context.Context, projectionName string, position uint64) error {
	if s.setErr != nil {
		return s.setErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checkpoints[projectionName] = position
	return nil
}

func (s *testCheckpointStore) DeleteCheckpoint(ctx context.Context, projectionName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.checkpoints, projectionName)
	return nil
}

func (s *testCheckpointStore) GetAllCheckpoints(ctx context.Context) (map[string]uint64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make(map[string]uint64, len(s.checkpoints))
	for k, v := range s.checkpoints {
		result[k] = v
	}
	return result, nil
}

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
		engine.RegisterInline(projection)

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
		engine.RegisterAsync(projection)

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
		engine.RegisterLive(projection)

		err := engine.RegisterLive(projection)
		assert.ErrorIs(t, err, ErrProjectionAlreadyRegistered)
	})
}

func TestProjectionEngine_Unregister(t *testing.T) {
	store := &EventStore{}
	engine := NewProjectionEngine(store)

	t.Run("unregisters inline projection", func(t *testing.T) {
		projection := newTestInlineProjection("UnregInline")
		engine.RegisterInline(projection)

		err := engine.Unregister("UnregInline")
		require.NoError(t, err)

		// Verify it's gone
		_, err = engine.GetStatus("UnregInline")
		assert.ErrorIs(t, err, ErrProjectionNotFound)
	})

	t.Run("unregisters async projection", func(t *testing.T) {
		projection := newTestAsyncProjection("UnregAsync")
		engine.RegisterAsync(projection)

		err := engine.Unregister("UnregAsync")
		require.NoError(t, err)
	})

	t.Run("unregisters live projection", func(t *testing.T) {
		projection := newTestLiveProjection("UnregLive", true)
		engine.RegisterLive(projection)

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
		engine.Stop(context.Background())
		assert.False(t, engine.IsRunning())
	})

	t.Run("rejects double start", func(t *testing.T) {
		engine := NewProjectionEngine(store, WithCheckpointStore(newTestCheckpointStore()))
		
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		engine.Start(ctx)
		defer engine.Stop(context.Background())

		err := engine.Start(ctx)
		assert.ErrorIs(t, err, ErrProjectionEngineAlreadyRunning)
	})
}

func TestProjectionEngine_GetStatus(t *testing.T) {
	store := &EventStore{}
	engine := NewProjectionEngine(store, WithCheckpointStore(newTestCheckpointStore()))

	t.Run("returns status for registered projection", func(t *testing.T) {
		projection := newTestAsyncProjection("StatusAsync")
		engine.RegisterAsync(projection)

		status, err := engine.GetStatus("StatusAsync")
		require.NoError(t, err)
		assert.Equal(t, "StatusAsync", status.Name)
		assert.Equal(t, ProjectionStateStopped, status.State)
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
	engine.RegisterInline(newTestInlineProjection("Inline1"))
	engine.RegisterAsync(newTestAsyncProjection("Async1"))
	engine.RegisterLive(newTestLiveProjection("Live1", true))

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
	engine.RegisterInline(projection1)
	engine.RegisterInline(projection2)

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
		engine.RegisterInline(errorProjection)

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
	engine.RegisterLive(projection)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start engine to activate live projections
	engine.Start(ctx)
	defer engine.Stop(context.Background())

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
