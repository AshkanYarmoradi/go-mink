package projections

import (
	"context"
	"encoding/json"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/AshkanYarmoradi/go-mink"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Mock testing.TB for testing failure cases
// =============================================================================

// mockT is a mock testing.TB that captures test failures for testing projection functions
type mockT struct {
	testing.TB // embed to satisfy unexported methods
	failed     bool
	fatal      bool
	skipped    bool
	logs       []string
}

func newMockT() *mockT {
	return &mockT{logs: make([]string, 0)}
}

func (m *mockT) Helper() {}

func (m *mockT) Error(args ...any)                 { m.failed = true }
func (m *mockT) Errorf(format string, args ...any) { m.failed = true }
func (m *mockT) Fail()                             { m.failed = true }
func (m *mockT) FailNow()                          { m.failed = true; runtime.Goexit() }
func (m *mockT) Failed() bool                      { return m.failed }
func (m *mockT) Fatal(args ...any)                 { m.failed = true; m.fatal = true; runtime.Goexit() }
func (m *mockT) Fatalf(format string, args ...any) { m.failed = true; m.fatal = true; runtime.Goexit() }

func runWithMockT(fn func(m *mockT)) *mockT {
	mt := newMockT()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn(mt)
	}()
	<-done
	return mt
}

// =============================================================================
// Test Events
// =============================================================================

type TestOrderCreated struct {
	OrderID    string `json:"orderId"`
	CustomerID string `json:"customerId"`
}

type TestItemAdded struct {
	OrderID  string  `json:"orderId"`
	SKU      string  `json:"sku"`
	Quantity int     `json:"quantity"`
	Price    float64 `json:"price"`
}

type TestOrderShipped struct {
	OrderID string `json:"orderId"`
}

// =============================================================================
// Test Read Model
// =============================================================================

type TestOrderSummary struct {
	ID          string
	CustomerID  string
	ItemCount   int
	TotalAmount float64
	Status      string
}

func (m *TestOrderSummary) GetID() string {
	return m.ID
}

// =============================================================================
// Test Inline Projection
// =============================================================================

type testOrderProjection struct {
	mink.ProjectionBase
	repo mink.ReadModelRepository[TestOrderSummary]
}

func newTestOrderProjection(repo mink.ReadModelRepository[TestOrderSummary]) *testOrderProjection {
	p := &testOrderProjection{repo: repo}
	p.ProjectionBase = mink.NewProjectionBase("TestOrderProjection", "TestOrderCreated", "TestItemAdded", "TestOrderShipped")
	return p
}

func (p *testOrderProjection) Apply(ctx context.Context, event mink.StoredEvent) error {
	switch event.Type {
	case "TestOrderCreated":
		var e TestOrderCreated
		if err := json.Unmarshal(event.Data, &e); err != nil {
			return err
		}
		return p.repo.Insert(ctx, &TestOrderSummary{
			ID:         e.OrderID,
			CustomerID: e.CustomerID,
			Status:     "Created",
		})

	case "TestItemAdded":
		var e TestItemAdded
		if err := json.Unmarshal(event.Data, &e); err != nil {
			return err
		}
		return p.repo.Update(ctx, e.OrderID, func(m *TestOrderSummary) {
			m.ItemCount++
			m.TotalAmount += e.Price * float64(e.Quantity)
		})

	case "TestOrderShipped":
		var e TestOrderShipped
		if err := json.Unmarshal(event.Data, &e); err != nil {
			return err
		}
		return p.repo.Update(ctx, e.OrderID, func(m *TestOrderSummary) {
			m.Status = "Shipped"
		})
	}
	return nil
}

// =============================================================================
// Test Async Projection
// =============================================================================

type testAsyncOrderProjection struct {
	mink.AsyncProjectionBase
	repo mink.ReadModelRepository[TestOrderSummary]
	mu   sync.Mutex
}

func newTestAsyncOrderProjection(repo mink.ReadModelRepository[TestOrderSummary]) *testAsyncOrderProjection {
	p := &testAsyncOrderProjection{repo: repo}
	p.AsyncProjectionBase = mink.NewAsyncProjectionBase("TestAsyncOrderProjection", "TestOrderCreated", "TestItemAdded")
	return p
}

func (p *testAsyncOrderProjection) Apply(ctx context.Context, event mink.StoredEvent) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	switch event.Type {
	case "TestOrderCreated":
		var e TestOrderCreated
		if err := json.Unmarshal(event.Data, &e); err != nil {
			return err
		}
		return p.repo.Upsert(ctx, &TestOrderSummary{
			ID:         e.OrderID,
			CustomerID: e.CustomerID,
			Status:     "Created",
		})

	case "TestItemAdded":
		var e TestItemAdded
		if err := json.Unmarshal(event.Data, &e); err != nil {
			return err
		}
		existing, _ := p.repo.Get(ctx, e.OrderID)
		if existing != nil {
			existing.ItemCount++
			existing.TotalAmount += e.Price * float64(e.Quantity)
			return p.repo.Upsert(ctx, existing)
		}
	}
	return nil
}

func (p *testAsyncOrderProjection) ApplyBatch(ctx context.Context, events []mink.StoredEvent) error {
	for _, event := range events {
		if err := p.Apply(ctx, event); err != nil {
			return err
		}
	}
	return nil
}

// =============================================================================
// Test Live Projection
// =============================================================================

type testLiveOrderProjection struct {
	mink.LiveProjectionBase
	repo     mink.ReadModelRepository[TestOrderSummary]
	received []TestOrderSummary
	mu       sync.Mutex
}

func newTestLiveOrderProjection(repo mink.ReadModelRepository[TestOrderSummary]) *testLiveOrderProjection {
	p := &testLiveOrderProjection{repo: repo}
	p.LiveProjectionBase = mink.NewLiveProjectionBase("TestLiveOrderProjection", true, "TestOrderCreated")
	return p
}

func (p *testLiveOrderProjection) OnEvent(ctx context.Context, event mink.StoredEvent) {
	switch event.Type {
	case "TestOrderCreated":
		var e TestOrderCreated
		if err := json.Unmarshal(event.Data, &e); err != nil {
			return
		}
		summary := TestOrderSummary{
			ID:         e.OrderID,
			CustomerID: e.CustomerID,
			Status:     "Created",
		}
		_ = p.repo.Insert(ctx, &summary)

		p.mu.Lock()
		p.received = append(p.received, summary)
		p.mu.Unlock()
	}
}

func (p *testLiveOrderProjection) ReceivedUpdates() []TestOrderSummary {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.received
}

// =============================================================================
// ProjectionTestFixture Tests
// =============================================================================

func TestTestProjection(t *testing.T) {
	t.Run("creates fixture with projection", func(t *testing.T) {
		repo := mink.NewInMemoryRepository[TestOrderSummary](func(m *TestOrderSummary) string { return m.ID })
		projection := newTestOrderProjection(repo)

		fixture := TestProjection[TestOrderSummary](t, projection)

		assert.NotNil(t, fixture)
		assert.NotNil(t, fixture.projection)
	})
}

func TestProjectionTestFixture_WithContext(t *testing.T) {
	t.Run("sets custom context", func(t *testing.T) {
		repo := mink.NewInMemoryRepository[TestOrderSummary](func(m *TestOrderSummary) string { return m.ID })
		projection := newTestOrderProjection(repo)
		ctx := context.WithValue(context.Background(), "key", "value")

		fixture := TestProjection[TestOrderSummary](t, projection).WithContext(ctx)

		assert.Equal(t, ctx, fixture.ctx)
	})
}

func TestProjectionTestFixture_WithRepository(t *testing.T) {
	t.Run("sets custom repository", func(t *testing.T) {
		repo := mink.NewInMemoryRepository[TestOrderSummary](func(m *TestOrderSummary) string { return m.ID })
		projection := newTestOrderProjection(repo)
		customRepo := mink.NewInMemoryRepository[TestOrderSummary](func(m *TestOrderSummary) string { return m.ID })

		fixture := TestProjection[TestOrderSummary](t, projection).WithRepository(customRepo)

		assert.Equal(t, customRepo, fixture.repo)
	})
}

func TestProjectionTestFixture_GivenEvents(t *testing.T) {
	t.Run("applies stored events to projection", func(t *testing.T) {
		repo := mink.NewInMemoryRepository[TestOrderSummary](func(m *TestOrderSummary) string { return m.ID })
		projection := newTestOrderProjection(repo)

		data, _ := json.Marshal(TestOrderCreated{OrderID: "order-123", CustomerID: "cust-456"})
		event := mink.StoredEvent{
			ID:       "evt-1",
			StreamID: "Order-order-123",
			Type:     "TestOrderCreated",
			Data:     data,
			Version:  1,
		}

		fixture := TestProjection[TestOrderSummary](t, projection).
			WithRepository(repo).
			GivenEvents(event)

		assert.Len(t, fixture.events, 1)
		assert.Equal(t, uint64(1), fixture.events[0].GlobalPosition)
	})
}

func TestProjectionTestFixture_GivenDomainEvents(t *testing.T) {
	t.Run("creates stored events from domain events", func(t *testing.T) {
		repo := mink.NewInMemoryRepository[TestOrderSummary](func(m *TestOrderSummary) string { return m.ID })
		projection := newTestOrderProjection(repo)

		fixture := TestProjection[TestOrderSummary](t, projection).
			WithRepository(repo).
			GivenDomainEvents("Order-order-123",
				TestOrderCreated{OrderID: "order-123", CustomerID: "cust-456"},
				TestItemAdded{OrderID: "order-123", SKU: "SKU-1", Quantity: 2, Price: 10.0},
			)

		assert.Len(t, fixture.events, 2)
		assert.Equal(t, "TestOrderCreated", fixture.events[0].Type)
		assert.Equal(t, "TestItemAdded", fixture.events[1].Type)
	})
}

func TestProjectionTestFixture_ThenReadModel(t *testing.T) {
	t.Run("passes when read model matches", func(t *testing.T) {
		repo := mink.NewInMemoryRepository[TestOrderSummary](func(m *TestOrderSummary) string { return m.ID })
		projection := newTestOrderProjection(repo)

		TestProjection[TestOrderSummary](t, projection).
			WithRepository(repo).
			GivenDomainEvents("Order-order-123",
				TestOrderCreated{OrderID: "order-123", CustomerID: "cust-456"},
			).
			ThenReadModel("order-123", TestOrderSummary{
				ID:         "order-123",
				CustomerID: "cust-456",
				Status:     "Created",
			})
	})
}

func TestProjectionTestFixture_ThenReadModelExists(t *testing.T) {
	t.Run("passes when read model exists", func(t *testing.T) {
		repo := mink.NewInMemoryRepository[TestOrderSummary](func(m *TestOrderSummary) string { return m.ID })
		projection := newTestOrderProjection(repo)

		model := TestProjection[TestOrderSummary](t, projection).
			WithRepository(repo).
			GivenDomainEvents("Order-order-123",
				TestOrderCreated{OrderID: "order-123", CustomerID: "cust-456"},
			).
			ThenReadModelExists("order-123")

		assert.NotNil(t, model)
		assert.Equal(t, "cust-456", model.CustomerID)
	})
}

func TestProjectionTestFixture_ThenReadModelNotExists(t *testing.T) {
	t.Run("passes when read model not exists", func(t *testing.T) {
		repo := mink.NewInMemoryRepository[TestOrderSummary](func(m *TestOrderSummary) string { return m.ID })
		projection := newTestOrderProjection(repo)

		TestProjection[TestOrderSummary](t, projection).
			WithRepository(repo).
			ThenReadModelNotExists("non-existent")
	})
}

func TestProjectionTestFixture_ThenReadModelCount(t *testing.T) {
	t.Run("passes when count matches", func(t *testing.T) {
		repo := mink.NewInMemoryRepository[TestOrderSummary](func(m *TestOrderSummary) string { return m.ID })
		projection := newTestOrderProjection(repo)

		TestProjection[TestOrderSummary](t, projection).
			WithRepository(repo).
			GivenDomainEvents("Order-order-1", TestOrderCreated{OrderID: "order-1", CustomerID: "cust-1"}).
			GivenDomainEvents("Order-order-2", TestOrderCreated{OrderID: "order-2", CustomerID: "cust-2"}).
			ThenReadModelCount(2)
	})
}

func TestProjectionTestFixture_ThenReadModelMatches(t *testing.T) {
	t.Run("passes custom check", func(t *testing.T) {
		repo := mink.NewInMemoryRepository[TestOrderSummary](func(m *TestOrderSummary) string { return m.ID })
		projection := newTestOrderProjection(repo)

		TestProjection[TestOrderSummary](t, projection).
			WithRepository(repo).
			GivenDomainEvents("Order-order-123",
				TestOrderCreated{OrderID: "order-123", CustomerID: "cust-456"},
				TestItemAdded{OrderID: "order-123", SKU: "SKU-1", Quantity: 2, Price: 10.0},
			).
			ThenReadModelMatches("order-123", func(tt TB, model *TestOrderSummary) {
				assert.Equal(t, 1, model.ItemCount)
				assert.Equal(t, 20.0, model.TotalAmount)
			})
	})
}

func TestProjectionTestFixture_Repository(t *testing.T) {
	t.Run("returns repository", func(t *testing.T) {
		repo := mink.NewInMemoryRepository[TestOrderSummary](func(m *TestOrderSummary) string { return m.ID })
		projection := newTestOrderProjection(repo)

		fixture := TestProjection[TestOrderSummary](t, projection).WithRepository(repo)

		assert.Equal(t, repo, fixture.Repository())
	})
}

func TestProjectionTestFixture_Events(t *testing.T) {
	t.Run("returns applied events", func(t *testing.T) {
		repo := mink.NewInMemoryRepository[TestOrderSummary](func(m *TestOrderSummary) string { return m.ID })
		projection := newTestOrderProjection(repo)

		fixture := TestProjection[TestOrderSummary](t, projection).
			WithRepository(repo).
			GivenDomainEvents("Order-order-123",
				TestOrderCreated{OrderID: "order-123", CustomerID: "cust-456"},
			)

		assert.Len(t, fixture.Events(), 1)
	})
}

// =============================================================================
// InlineProjectionFixture Tests
// =============================================================================

func TestTestInlineProjection(t *testing.T) {
	t.Run("creates inline fixture", func(t *testing.T) {
		repo := mink.NewInMemoryRepository[TestOrderSummary](func(m *TestOrderSummary) string { return m.ID })
		projection := newTestOrderProjection(repo)

		fixture := TestInlineProjection[TestOrderSummary](t, projection)

		assert.NotNil(t, fixture)
		assert.NotNil(t, fixture.inline)
	})
}

func TestInlineProjectionFixture_ApplyEvent(t *testing.T) {
	t.Run("applies single event", func(t *testing.T) {
		repo := mink.NewInMemoryRepository[TestOrderSummary](func(m *TestOrderSummary) string { return m.ID })
		projection := newTestOrderProjection(repo)
		fixture := TestInlineProjection[TestOrderSummary](t, projection).WithRepository(repo)

		data, _ := json.Marshal(TestOrderCreated{OrderID: "order-123", CustomerID: "cust-456"})
		err := fixture.ApplyEvent(mink.StoredEvent{
			ID:       "evt-1",
			StreamID: "Order-order-123",
			Type:     "TestOrderCreated",
			Data:     data,
			Version:  1,
		})

		require.NoError(t, err)

		model, err := repo.Get(context.Background(), "order-123")
		require.NoError(t, err)
		assert.Equal(t, "cust-456", model.CustomerID)
	})
}

// =============================================================================
// AsyncProjectionFixture Tests
// =============================================================================

func TestTestAsyncProjection(t *testing.T) {
	t.Run("creates async fixture", func(t *testing.T) {
		repo := mink.NewInMemoryRepository[TestOrderSummary](func(m *TestOrderSummary) string { return m.ID })
		projection := newTestAsyncOrderProjection(repo)

		fixture := TestAsyncProjection[TestOrderSummary](t, projection)

		assert.NotNil(t, fixture)
		assert.NotNil(t, fixture.async)
	})
}

func TestAsyncProjectionFixture_ProcessBatch(t *testing.T) {
	t.Run("processes batch of events", func(t *testing.T) {
		repo := mink.NewInMemoryRepository[TestOrderSummary](func(m *TestOrderSummary) string { return m.ID })
		projection := newTestAsyncOrderProjection(repo)
		fixture := TestAsyncProjection[TestOrderSummary](t, projection).WithRepository(repo)

		data1, _ := json.Marshal(TestOrderCreated{OrderID: "order-1", CustomerID: "cust-1"})
		data2, _ := json.Marshal(TestOrderCreated{OrderID: "order-2", CustomerID: "cust-2"})

		events := []mink.StoredEvent{
			{ID: "evt-1", StreamID: "Order-order-1", Type: "TestOrderCreated", Data: data1, Version: 1},
			{ID: "evt-2", StreamID: "Order-order-2", Type: "TestOrderCreated", Data: data2, Version: 1},
		}

		err := fixture.ProcessBatch(events)
		require.NoError(t, err)

		model1, _ := repo.Get(context.Background(), "order-1")
		model2, _ := repo.Get(context.Background(), "order-2")
		assert.NotNil(t, model1)
		assert.NotNil(t, model2)
	})
}

// =============================================================================
// LiveProjectionFixture Tests
// =============================================================================

func TestTestLiveProjection(t *testing.T) {
	t.Run("creates live fixture", func(t *testing.T) {
		repo := mink.NewInMemoryRepository[TestOrderSummary](func(m *TestOrderSummary) string { return m.ID })
		projection := newTestLiveOrderProjection(repo)

		fixture := TestLiveProjection[TestOrderSummary](t, projection)

		assert.NotNil(t, fixture)
		assert.NotNil(t, fixture.live)
	})
}

func TestLiveProjectionFixture_OnEvent(t *testing.T) {
	t.Run("handles event via OnEvent", func(t *testing.T) {
		repo := mink.NewInMemoryRepository[TestOrderSummary](func(m *TestOrderSummary) string { return m.ID })
		projection := newTestLiveOrderProjection(repo)
		fixture := TestLiveProjection[TestOrderSummary](t, projection).WithRepository(repo)

		data, _ := json.Marshal(TestOrderCreated{OrderID: "order-123", CustomerID: "cust-456"})
		event := mink.StoredEvent{
			ID:       "evt-1",
			StreamID: "Order-order-123",
			Type:     "TestOrderCreated",
			Data:     data,
			Version:  1,
		}

		fixture.OnEvent(event)

		model, err := repo.Get(context.Background(), "order-123")
		require.NoError(t, err)
		assert.Equal(t, "cust-456", model.CustomerID)
	})
}

func TestLiveProjectionFixture_ThenIsTransient(t *testing.T) {
	t.Run("passes when transient matches", func(t *testing.T) {
		repo := mink.NewInMemoryRepository[TestOrderSummary](func(m *TestOrderSummary) string { return m.ID })
		projection := newTestLiveOrderProjection(repo)
		fixture := TestLiveProjection[TestOrderSummary](t, projection)

		fixture.ThenIsTransient(true)
	})
}

// =============================================================================
// EngineTestFixture Tests
// =============================================================================

func TestTestEngine(t *testing.T) {
	t.Run("creates engine fixture", func(t *testing.T) {
		fixture := TestEngine(t)

		assert.NotNil(t, fixture)
		assert.NotNil(t, fixture.engine)
		assert.NotNil(t, fixture.store)
	})
}

func TestEngineTestFixture_WithContext(t *testing.T) {
	t.Run("sets custom context", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), "key", "value")
		fixture := TestEngine(t).WithContext(ctx)

		assert.Equal(t, ctx, fixture.ctx)
	})
}

func TestEngineTestFixture_RegisterProjections(t *testing.T) {
	t.Run("registers inline projection", func(t *testing.T) {
		repo := mink.NewInMemoryRepository[TestOrderSummary](func(m *TestOrderSummary) string { return m.ID })
		projection := newTestOrderProjection(repo)

		fixture := TestEngine(t).RegisterInline(projection)

		assert.NotNil(t, fixture.engine)
	})

	t.Run("registers async projection", func(t *testing.T) {
		repo := mink.NewInMemoryRepository[TestOrderSummary](func(m *TestOrderSummary) string { return m.ID })
		projection := newTestAsyncOrderProjection(repo)

		fixture := TestEngine(t).RegisterAsync(projection, mink.DefaultAsyncOptions())

		assert.NotNil(t, fixture.engine)
	})

	t.Run("registers live projection", func(t *testing.T) {
		repo := mink.NewInMemoryRepository[TestOrderSummary](func(m *TestOrderSummary) string { return m.ID })
		projection := newTestLiveOrderProjection(repo)

		fixture := TestEngine(t).RegisterLive(projection)

		assert.NotNil(t, fixture.engine)
	})
}

func TestEngineTestFixture_StartStop(t *testing.T) {
	t.Run("starts and stops engine", func(t *testing.T) {
		fixture := TestEngine(t).Start()

		assert.NotNil(t, fixture)

		fixture.Stop()
	})
}

func TestEngineTestFixture_AppendEvents(t *testing.T) {
	t.Run("appends events to store", func(t *testing.T) {
		fixture := TestEngine(t)

		// Append some test events
		fixture.AppendEvents("test-stream",
			TestOrderCreated{OrderID: "order-123", CustomerID: "cust-456"},
		)

		// Verify events were stored
		events, err := fixture.adapter.Load(context.Background(), "test-stream", 0)
		require.NoError(t, err)
		assert.Len(t, events, 1)
	})
}

func TestEngineTestFixture_Engine(t *testing.T) {
	t.Run("returns engine", func(t *testing.T) {
		fixture := TestEngine(t)
		assert.NotNil(t, fixture.Engine())
	})
}

func TestEngineTestFixture_Store(t *testing.T) {
	t.Run("returns store", func(t *testing.T) {
		fixture := TestEngine(t)
		assert.NotNil(t, fixture.Store())
	})
}

// =============================================================================
// Integration Tests
// =============================================================================

func TestProjectionFixture_CompleteFlow(t *testing.T) {
	t.Run("complete projection testing flow", func(t *testing.T) {
		repo := mink.NewInMemoryRepository[TestOrderSummary](func(m *TestOrderSummary) string { return m.ID })
		projection := newTestOrderProjection(repo)

		TestProjection[TestOrderSummary](t, projection).
			WithRepository(repo).
			GivenDomainEvents("Order-order-123",
				TestOrderCreated{OrderID: "order-123", CustomerID: "cust-456"},
				TestItemAdded{OrderID: "order-123", SKU: "SKU-1", Quantity: 2, Price: 10.0},
				TestItemAdded{OrderID: "order-123", SKU: "SKU-2", Quantity: 1, Price: 30.0},
				TestOrderShipped{OrderID: "order-123"},
			).
			ThenReadModel("order-123", TestOrderSummary{
				ID:          "order-123",
				CustomerID:  "cust-456",
				ItemCount:   2,
				TotalAmount: 50.0,
				Status:      "Shipped",
			})
	})

	t.Run("multiple orders", func(t *testing.T) {
		repo := mink.NewInMemoryRepository[TestOrderSummary](func(m *TestOrderSummary) string { return m.ID })
		projection := newTestOrderProjection(repo)

		TestProjection[TestOrderSummary](t, projection).
			WithRepository(repo).
			GivenDomainEvents("Order-order-1",
				TestOrderCreated{OrderID: "order-1", CustomerID: "cust-1"},
			).
			GivenDomainEvents("Order-order-2",
				TestOrderCreated{OrderID: "order-2", CustomerID: "cust-2"},
			).
			GivenDomainEvents("Order-order-3",
				TestOrderCreated{OrderID: "order-3", CustomerID: "cust-3"},
			).
			ThenReadModelCount(3)
	})
}

// =============================================================================
// Failure Path Tests
// =============================================================================

func TestProjectionTestFixture_ThenReadModel_FailureCases(t *testing.T) {
	t.Run("fails when read model not found", func(t *testing.T) {
		mt := runWithMockT(func(m *mockT) {
			repo := mink.NewInMemoryRepository[TestOrderSummary](func(model *TestOrderSummary) string { return model.ID })
			projection := newTestOrderProjection(repo)
			TestProjection[TestOrderSummary](m, projection).
				WithRepository(repo).
				ThenReadModel("non-existent", TestOrderSummary{})
		})
		assert.True(t, mt.failed)
		assert.True(t, mt.fatal)
	})

	t.Run("fails when read model doesn't match", func(t *testing.T) {
		mt := runWithMockT(func(m *mockT) {
			repo := mink.NewInMemoryRepository[TestOrderSummary](func(model *TestOrderSummary) string { return model.ID })
			projection := newTestOrderProjection(repo)
			TestProjection[TestOrderSummary](m, projection).
				WithRepository(repo).
				GivenDomainEvents("Order-order-123",
					TestOrderCreated{OrderID: "order-123", CustomerID: "cust-456"},
				).
				ThenReadModel("order-123", TestOrderSummary{
					ID:         "order-123",
					CustomerID: "wrong-customer",
					Status:     "Created",
				})
		})
		assert.True(t, mt.failed)
	})
}

func TestProjectionTestFixture_ThenReadModelExists_FailureCases(t *testing.T) {
	t.Run("fails when read model not found", func(t *testing.T) {
		mt := runWithMockT(func(m *mockT) {
			repo := mink.NewInMemoryRepository[TestOrderSummary](func(model *TestOrderSummary) string { return model.ID })
			projection := newTestOrderProjection(repo)
			TestProjection[TestOrderSummary](m, projection).
				WithRepository(repo).
				ThenReadModelExists("non-existent")
		})
		assert.True(t, mt.failed)
		assert.True(t, mt.fatal)
	})
}

func TestProjectionTestFixture_ThenReadModelNotExists_FailureCases(t *testing.T) {
	t.Run("fails when read model exists", func(t *testing.T) {
		mt := runWithMockT(func(m *mockT) {
			repo := mink.NewInMemoryRepository[TestOrderSummary](func(model *TestOrderSummary) string { return model.ID })
			projection := newTestOrderProjection(repo)
			TestProjection[TestOrderSummary](m, projection).
				WithRepository(repo).
				GivenDomainEvents("Order-order-123",
					TestOrderCreated{OrderID: "order-123", CustomerID: "cust-456"},
				).
				ThenReadModelNotExists("order-123")
		})
		assert.True(t, mt.failed)
	})
}

func TestProjectionTestFixture_ThenReadModelCount_FailureCases(t *testing.T) {
	t.Run("fails when count doesn't match", func(t *testing.T) {
		mt := runWithMockT(func(m *mockT) {
			repo := mink.NewInMemoryRepository[TestOrderSummary](func(model *TestOrderSummary) string { return model.ID })
			projection := newTestOrderProjection(repo)
			TestProjection[TestOrderSummary](m, projection).
				WithRepository(repo).
				GivenDomainEvents("Order-order-1",
					TestOrderCreated{OrderID: "order-1", CustomerID: "cust-1"},
				).
				ThenReadModelCount(5) // wrong count
		})
		assert.True(t, mt.failed)
	})
}

func TestProjectionTestFixture_ThenReadModelMatches_FailureCases(t *testing.T) {
	t.Run("fails when read model not found", func(t *testing.T) {
		mt := runWithMockT(func(m *mockT) {
			repo := mink.NewInMemoryRepository[TestOrderSummary](func(model *TestOrderSummary) string { return model.ID })
			projection := newTestOrderProjection(repo)
			TestProjection[TestOrderSummary](m, projection).
				WithRepository(repo).
				ThenReadModelMatches("non-existent", func(tt TB, model *TestOrderSummary) {})
		})
		assert.True(t, mt.failed)
		assert.True(t, mt.fatal)
	})
}

func TestLiveProjectionFixture_ThenIsTransient_FailureCases(t *testing.T) {
	t.Run("fails when transient doesn't match", func(t *testing.T) {
		mt := runWithMockT(func(m *mockT) {
			repo := mink.NewInMemoryRepository[TestOrderSummary](func(model *TestOrderSummary) string { return model.ID })
			projection := newTestLiveOrderProjection(repo)
			fixture := TestLiveProjection[TestOrderSummary](m, projection)
			fixture.ThenIsTransient(false) // projection is transient=true
		})
		assert.True(t, mt.failed)
	})
}

// Test projection that returns error on Apply for testing failure paths
type errorOrderProjection struct {
	mink.ProjectionBase
}

func newErrorOrderProjection() *errorOrderProjection {
	p := &errorOrderProjection{}
	p.ProjectionBase = mink.NewProjectionBase("ErrorOrderProjection", "TestOrderCreated")
	return p
}

func (p *errorOrderProjection) Apply(ctx context.Context, event mink.StoredEvent) error {
	return errors.New("apply error")
}

func TestProjectionTestFixture_GivenEvents_ApplyError(t *testing.T) {
	t.Run("fails when apply returns error", func(t *testing.T) {
		mt := runWithMockT(func(m *mockT) {
			projection := newErrorOrderProjection()
			data, _ := json.Marshal(TestOrderCreated{OrderID: "order-123", CustomerID: "cust-456"})
			event := mink.StoredEvent{
				ID:       "evt-1",
				StreamID: "Order-order-123",
				Type:     "TestOrderCreated",
				Data:     data,
				Version:  1,
			}
			TestProjection[TestOrderSummary](m, projection).
				GivenEvents(event)
		})
		assert.True(t, mt.failed)
		assert.True(t, mt.fatal)
	})
}

func TestProjectionTestFixture_GivenDomainEvents_ApplyError(t *testing.T) {
	t.Run("fails when apply returns error", func(t *testing.T) {
		mt := runWithMockT(func(m *mockT) {
			projection := newErrorOrderProjection()
			TestProjection[TestOrderSummary](m, projection).
				GivenDomainEvents("Order-order-123",
					TestOrderCreated{OrderID: "order-123", CustomerID: "cust-456"},
				)
		})
		assert.True(t, mt.failed)
		assert.True(t, mt.fatal)
	})
}

func TestEngineTestFixture_WaitForProjection(t *testing.T) {
	t.Run("times out when projection never catches up", func(t *testing.T) {
		mt := runWithMockT(func(m *mockT) {
			fixture := TestEngine(m)
			// Don't register any projections, so GetStatus will fail
			fixture.WaitForProjection("non-existent", 50*time.Millisecond)
		})
		assert.True(t, mt.failed)
		assert.True(t, mt.fatal)
	})
}
