// Package projections provides testing utilities for projection development.
// It includes fixtures for testing inline, async, and live projections with
// event application and read model verification.
package projections

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/AshkanYarmoradi/go-mink"
	"github.com/AshkanYarmoradi/go-mink/adapters/memory"
)

// TB is an alias for testing.TB to enable easier mocking in tests.
type TB = testing.TB

// ProjectionTestFixture provides testing utilities for projections.
type ProjectionTestFixture[T any] struct {
	t          TB
	ctx        context.Context
	projection mink.Projection
	repo       mink.ReadModelRepository[T]
	store      *mink.EventStore
	adapter    *memory.MemoryAdapter
	events     []mink.StoredEvent
	position   uint64
}

// TestProjection creates a new projection test fixture.
func TestProjection[T any](t TB, projection mink.Projection) *ProjectionTestFixture[T] {
	t.Helper()
	adapter := memory.NewAdapter()
	return &ProjectionTestFixture[T]{
		t:          t,
		ctx:        context.Background(),
		projection: projection,
		repo:       mink.NewInMemoryRepository[T](nil),
		store:      mink.New(adapter),
		adapter:    adapter,
		events:     make([]mink.StoredEvent, 0),
	}
}

// WithContext sets a custom context.
func (f *ProjectionTestFixture[T]) WithContext(ctx context.Context) *ProjectionTestFixture[T] {
	f.ctx = ctx
	return f
}

// WithRepository sets a custom repository.
func (f *ProjectionTestFixture[T]) WithRepository(repo mink.ReadModelRepository[T]) *ProjectionTestFixture[T] {
	f.repo = repo
	return f
}

// GivenEvents applies events to the projection.
func (f *ProjectionTestFixture[T]) GivenEvents(events ...mink.StoredEvent) *ProjectionTestFixture[T] {
	f.t.Helper()

	for _, event := range events {
		f.position++
		event.GlobalPosition = f.position
		if event.Timestamp.IsZero() {
			event.Timestamp = time.Now()
		}
		f.events = append(f.events, event)

		if inline, ok := f.projection.(mink.InlineProjection); ok {
			if err := inline.Apply(f.ctx, event); err != nil {
				f.t.Fatalf("Failed to apply event: %v", err)
			}
		}
	}

	return f
}

// GivenDomainEvents creates stored events from domain events.
func (f *ProjectionTestFixture[T]) GivenDomainEvents(streamID string, domainEvents ...interface{}) *ProjectionTestFixture[T] {
	f.t.Helper()

	for i, event := range domainEvents {
		eventType := reflect.TypeOf(event).Name()
		data, err := json.Marshal(event)
		if err != nil {
			f.t.Fatalf("Failed to marshal event: %v", err)
		}

		f.position++
		storedEvent := mink.StoredEvent{
			ID:             f.generateID(),
			StreamID:       streamID,
			Type:           eventType,
			Data:           data,
			Version:        int64(i + 1),
			GlobalPosition: f.position,
			Timestamp:      time.Now(),
		}
		f.events = append(f.events, storedEvent)

		if inline, ok := f.projection.(mink.InlineProjection); ok {
			if err := inline.Apply(f.ctx, storedEvent); err != nil {
				f.t.Fatalf("Failed to apply event: %v", err)
			}
		}
	}

	return f
}

// ThenReadModel asserts the read model matches the expected state.
func (f *ProjectionTestFixture[T]) ThenReadModel(id string, expected T) {
	f.t.Helper()

	actual, err := f.repo.Get(f.ctx, id)
	if err != nil {
		f.t.Fatalf("Failed to get read model %s: %v", id, err)
	}

	if actual == nil {
		f.t.Fatalf("Read model %s not found", id)
		return // unreachable but helps static analysis
	}

	if !reflect.DeepEqual(*actual, expected) {
		f.t.Errorf("Read model mismatch:\nExpected: %+v\nActual: %+v", expected, *actual)
	}
}

// ThenReadModelExists asserts that a read model exists.
func (f *ProjectionTestFixture[T]) ThenReadModelExists(id string) *T {
	f.t.Helper()

	actual, err := f.repo.Get(f.ctx, id)
	if err != nil {
		f.t.Fatalf("Failed to get read model %s: %v", id, err)
	}

	if actual == nil {
		f.t.Fatalf("Read model %s not found", id)
	}

	return actual
}

// ThenReadModelNotExists asserts that a read model does not exist.
func (f *ProjectionTestFixture[T]) ThenReadModelNotExists(id string) {
	f.t.Helper()

	actual, err := f.repo.Get(f.ctx, id)
	if err != nil && err != mink.ErrNotFound {
		f.t.Fatalf("Unexpected error: %v", err)
	}

	if actual != nil {
		f.t.Errorf("Expected read model %s to not exist, but found: %+v", id, *actual)
	}
}

// ThenReadModelCount asserts the number of read models.
func (f *ProjectionTestFixture[T]) ThenReadModelCount(expected int) {
	f.t.Helper()

	all, err := f.repo.Find(f.ctx, *mink.NewQuery())
	if err != nil {
		f.t.Fatalf("Failed to query read models: %v", err)
	}

	if len(all) != expected {
		f.t.Errorf("Expected %d read models, got %d", expected, len(all))
	}
}

// ThenReadModelMatches asserts the read model passes a custom check.
func (f *ProjectionTestFixture[T]) ThenReadModelMatches(id string, check func(t TB, model *T)) {
	f.t.Helper()

	actual, err := f.repo.Get(f.ctx, id)
	if err != nil {
		f.t.Fatalf("Failed to get read model %s: %v", id, err)
	}

	if actual == nil {
		f.t.Fatalf("Read model %s not found", id)
	}

	check(f.t, actual)
}

// Repository returns the underlying repository for additional assertions.
func (f *ProjectionTestFixture[T]) Repository() mink.ReadModelRepository[T] {
	return f.repo
}

// Events returns the applied events.
func (f *ProjectionTestFixture[T]) Events() []mink.StoredEvent {
	return f.events
}

func (f *ProjectionTestFixture[T]) generateID() string {
	return "evt-" + time.Now().Format("20060102150405.000000000")
}

// =============================================================================
// Inline Projection Test Fixture
// =============================================================================

// InlineProjectionFixture provides specialized testing for inline projections.
type InlineProjectionFixture[T any] struct {
	*ProjectionTestFixture[T]
	inline mink.InlineProjection
}

// TestInlineProjection creates a fixture for testing inline projections.
func TestInlineProjection[T any](t TB, projection mink.InlineProjection) *InlineProjectionFixture[T] {
	t.Helper()
	base := TestProjection[T](t, projection)
	return &InlineProjectionFixture[T]{
		ProjectionTestFixture: base,
		inline:                projection,
	}
}

// WithRepository sets a custom repository for the inline fixture.
func (f *InlineProjectionFixture[T]) WithRepository(repo mink.ReadModelRepository[T]) *InlineProjectionFixture[T] {
	f.ProjectionTestFixture.WithRepository(repo)
	return f
}

// ApplyEvent applies a single event to the inline projection.
func (f *InlineProjectionFixture[T]) ApplyEvent(event mink.StoredEvent) error {
	return f.inline.Apply(f.ctx, event)
}

// =============================================================================
// Async Projection Test Fixture
// =============================================================================

// AsyncProjectionFixture provides specialized testing for async projections.
type AsyncProjectionFixture[T any] struct {
	*ProjectionTestFixture[T]
	async mink.AsyncProjection
}

// TestAsyncProjection creates a fixture for testing async projections.
func TestAsyncProjection[T any](t TB, projection mink.AsyncProjection) *AsyncProjectionFixture[T] {
	t.Helper()
	base := TestProjection[T](t, projection)
	return &AsyncProjectionFixture[T]{
		ProjectionTestFixture: base,
		async:                 projection,
	}
}

// WithRepository sets a custom repository for the async fixture.
func (f *AsyncProjectionFixture[T]) WithRepository(repo mink.ReadModelRepository[T]) *AsyncProjectionFixture[T] {
	f.ProjectionTestFixture.WithRepository(repo)
	return f
}

// ProcessBatch processes a batch of events.
func (f *AsyncProjectionFixture[T]) ProcessBatch(events []mink.StoredEvent) error {
	return f.async.ApplyBatch(f.ctx, events)
}

// =============================================================================
// Live Projection Test Fixture
// =============================================================================

// LiveProjectionFixture provides specialized testing for live projections.
type LiveProjectionFixture[T any] struct {
	*ProjectionTestFixture[T]
	live mink.LiveProjection
}

// TestLiveProjection creates a fixture for testing live projections.
func TestLiveProjection[T any](t TB, projection mink.LiveProjection) *LiveProjectionFixture[T] {
	t.Helper()
	base := TestProjection[T](t, projection)
	return &LiveProjectionFixture[T]{
		ProjectionTestFixture: base,
		live:                  projection,
	}
}

// WithRepository sets a custom repository for the live fixture.
func (f *LiveProjectionFixture[T]) WithRepository(repo mink.ReadModelRepository[T]) *LiveProjectionFixture[T] {
	f.ProjectionTestFixture.WithRepository(repo)
	return f
}

// OnEvent calls OnEvent on the live projection.
func (f *LiveProjectionFixture[T]) OnEvent(event mink.StoredEvent) {
	f.live.OnEvent(f.ctx, event)
}

// ThenIsTransient asserts the projection is transient.
func (f *LiveProjectionFixture[T]) ThenIsTransient(expected bool) {
	f.t.Helper()
	if f.live.IsTransient() != expected {
		f.t.Errorf("Expected IsTransient=%v, got %v", expected, f.live.IsTransient())
	}
}

// =============================================================================
// Projection Engine Test Fixture
// =============================================================================

// EngineTestFixture provides testing utilities for the projection engine.
type EngineTestFixture struct {
	t       TB
	ctx     context.Context
	engine  *mink.ProjectionEngine
	store   *mink.EventStore
	adapter *memory.MemoryAdapter
}

// TestEngine creates a new engine test fixture.
func TestEngine(t TB) *EngineTestFixture {
	t.Helper()
	adapter := memory.NewAdapter()
	store := mink.New(adapter)
	checkpoint := memory.NewCheckpointStore()
	engine := mink.NewProjectionEngine(store, mink.WithCheckpointStore(checkpoint))

	return &EngineTestFixture{
		t:       t,
		ctx:     context.Background(),
		engine:  engine,
		store:   store,
		adapter: adapter,
	}
}

// WithContext sets a custom context.
func (f *EngineTestFixture) WithContext(ctx context.Context) *EngineTestFixture {
	f.ctx = ctx
	return f
}

// RegisterInline registers an inline projection.
func (f *EngineTestFixture) RegisterInline(projection mink.InlineProjection) *EngineTestFixture {
	_ = f.engine.RegisterInline(projection)
	return f
}

// RegisterAsync registers an async projection.
func (f *EngineTestFixture) RegisterAsync(projection mink.AsyncProjection, opts mink.AsyncOptions) *EngineTestFixture {
	_ = f.engine.RegisterAsync(projection, opts)
	return f
}

// RegisterLive registers a live projection.
func (f *EngineTestFixture) RegisterLive(projection mink.LiveProjection, opts ...mink.LiveOptions) *EngineTestFixture {
	_ = f.engine.RegisterLive(projection, opts...)
	return f
}

// Start starts the projection engine.
func (f *EngineTestFixture) Start() *EngineTestFixture {
	if err := f.engine.Start(f.ctx); err != nil {
		f.t.Fatalf("Failed to start engine: %v", err)
	}
	return f
}

// Stop stops the projection engine.
func (f *EngineTestFixture) Stop() *EngineTestFixture {
	if err := f.engine.Stop(f.ctx); err != nil {
		f.t.Fatalf("Failed to stop engine: %v", err)
	}
	return f
}

// AppendEvents appends events to the store.
func (f *EngineTestFixture) AppendEvents(streamID string, events ...interface{}) *EngineTestFixture {
	err := f.store.Append(f.ctx, streamID, events)
	if err != nil {
		f.t.Fatalf("Failed to append events: %v", err)
	}
	return f
}

// WaitForProjection waits for a projection to catch up.
func (f *EngineTestFixture) WaitForProjection(name string, timeout time.Duration) *EngineTestFixture {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		status, err := f.engine.GetStatus(name)
		if err == nil && status != nil && status.Lag == 0 {
			return f
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.t.Fatalf("Timeout waiting for projection %s to catch up", name)
	return f
}

// Engine returns the underlying engine.
func (f *EngineTestFixture) Engine() *mink.ProjectionEngine {
	return f.engine
}

// Store returns the underlying event store.
func (f *EngineTestFixture) Store() *mink.EventStore {
	return f.store
}
