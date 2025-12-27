---
layout: default
title: Testing
nav_order: 9
permalink: /docs/testing
---

# Testing
{: .no_toc }

{: .label .label-green }
Phase 1 Complete - 90%+ Coverage

## Table of contents
{: .no_toc .text-delta }

1. TOC
{:toc}

---

## Testing Philosophy

Mink provides comprehensive testing utilities to make event-sourced systems easy to test.

### Testing Pyramid

```
         ╱╲
        ╱  ╲         E2E Tests (few)
       ╱────╲        - Full system integration
      ╱      ╲       - Database, projections, sagas
     ╱────────╲      
    ╱          ╲     Integration Tests (some)
   ╱────────────╲    - Adapter tests
  ╱              ╲   - Projection tests
 ╱────────────────╲  
╱                  ╲ Unit Tests (many)
╱────────────────────╲
                      - Aggregate logic
                      - Command validation
                      - Event handlers
```

---

## Current Test Coverage

| Package | Coverage |
|---------|----------|
| `mink` (core) | 95.3% ✅ |
| `adapters/memory` | 94.2% ✅ |
| `adapters/postgres` | 86.4% ✅ |
| **Required Threshold** | **90%** |

### Running Tests

```bash
# Unit tests only (no infrastructure required)
make test-unit

# All tests with infrastructure
make test

# Tests with coverage report
make test-coverage
```

### Test Infrastructure

All test infrastructure is defined in `docker-compose.test.yml` - the single source of truth for both local development and CI:

```bash
# Start infrastructure
make infra-up

# Stop infrastructure  
make infra-down
```

---

## In-Memory Test Store

```go
package minktest

import "github.com/AshkanYarmoradi/go-mink"

// NewInMemoryStore creates a test event store
func NewInMemoryStore() *mink.EventStore {
    return mink.New(memory.NewAdapter())
}

// NewInMemoryStoreWithEvents pre-populates events
func NewInMemoryStoreWithEvents(events map[string][]mink.Event) *mink.EventStore {
    adapter := memory.NewAdapter()
    for streamID, evts := range events {
        adapter.Append(context.Background(), streamID, evts, mink.AnyVersion)
    }
    return mink.New(adapter)
}

// Usage
func TestOrderAggregate(t *testing.T) {
    store := minktest.NewInMemoryStore()
    
    order := NewOrder("order-123")
    order.Create("customer-456")
    order.AddItem("SKU-001", 2, 29.99)
    
    err := store.SaveAggregate(context.Background(), order)
    assert.NoError(t, err)
    
    // Reload and verify
    loaded := NewOrder("order-123")
    store.LoadAggregate(context.Background(), loaded)
    
    assert.Equal(t, "Created", loaded.Status)
    assert.Equal(t, 1, len(loaded.Items))
}
```

---

## BDD-Style Test Fixtures

Write expressive tests using Given-When-Then pattern.

### Test Fixture API

```go
package minktest

// TestFixture provides BDD-style testing
type TestFixture struct {
    t          *testing.T
    store      *mink.EventStore
    aggregate  mink.Aggregate
    givenEvents []interface{}
    command    mink.Command
    err        error
}

// Given sets up initial events
func Given(t *testing.T, aggregate mink.Aggregate, events ...interface{}) *TestFixture {
    return &TestFixture{
        t:           t,
        store:       NewInMemoryStore(),
        aggregate:   aggregate,
        givenEvents: events,
    }
}

// When executes a command
func (f *TestFixture) When(cmd mink.Command) *TestFixture {
    // Apply given events
    for _, event := range f.givenEvents {
        f.aggregate.ApplyEvent(event)
    }
    f.aggregate.ClearUncommittedEvents()
    
    // Execute command
    f.command = cmd
    f.err = f.executeCommand(cmd)
    
    return f
}

// Then asserts expected events
func (f *TestFixture) Then(expectedEvents ...interface{}) {
    f.t.Helper()
    
    if f.err != nil {
        f.t.Fatalf("Expected success but got error: %v", f.err)
    }
    
    uncommitted := f.aggregate.UncommittedEvents()
    if len(uncommitted) != len(expectedEvents) {
        f.t.Fatalf("Expected %d events, got %d", len(expectedEvents), len(uncommitted))
    }
    
    for i, expected := range expectedEvents {
        if !reflect.DeepEqual(uncommitted[i], expected) {
            f.t.Errorf("Event %d mismatch:\nExpected: %+v\nActual: %+v", 
                i, expected, uncommitted[i])
        }
    }
}

// ThenError asserts expected error
func (f *TestFixture) ThenError(expectedErr error) {
    f.t.Helper()
    
    if f.err == nil {
        f.t.Fatal("Expected error but got success")
    }
    
    if !errors.Is(f.err, expectedErr) {
        f.t.Errorf("Expected error %v, got %v", expectedErr, f.err)
    }
}

// ThenErrorContains checks error message
func (f *TestFixture) ThenErrorContains(substring string) {
    f.t.Helper()
    
    if f.err == nil {
        f.t.Fatal("Expected error but got success")
    }
    
    if !strings.Contains(f.err.Error(), substring) {
        f.t.Errorf("Expected error containing %q, got %q", substring, f.err.Error())
    }
}

// ThenNoEvents asserts no events produced
func (f *TestFixture) ThenNoEvents() {
    f.t.Helper()
    
    uncommitted := f.aggregate.UncommittedEvents()
    if len(uncommitted) > 0 {
        f.t.Errorf("Expected no events, got %d: %+v", len(uncommitted), uncommitted)
    }
}
```

### BDD Test Examples

```go
func TestOrderCanBeCreated(t *testing.T) {
    Given(t, NewOrder("order-123")).
        When(CreateOrderCommand{
            OrderID:    "order-123",
            CustomerID: "customer-456",
        }).
        Then(
            OrderCreated{
                OrderID:    "order-123",
                CustomerID: "customer-456",
            },
        )
}

func TestCannotAddItemToShippedOrder(t *testing.T) {
    Given(t, NewOrder("order-123"),
        OrderCreated{OrderID: "order-123", CustomerID: "customer-456"},
        ItemAdded{SKU: "SKU-001", Quantity: 1, Price: 29.99},
        OrderShipped{OrderID: "order-123"},
    ).
        When(AddItemCommand{
            OrderID:  "order-123",
            SKU:      "SKU-002",
            Quantity: 1,
        }).
        ThenError(ErrOrderAlreadyShipped)
}

func TestItemQuantityMustBePositive(t *testing.T) {
    Given(t, NewOrder("order-123"),
        OrderCreated{OrderID: "order-123", CustomerID: "customer-456"},
    ).
        When(AddItemCommand{
            OrderID:  "order-123",
            SKU:      "SKU-001",
            Quantity: 0, // Invalid
        }).
        ThenErrorContains("quantity must be positive")
}

func TestMultipleItemsCanBeAdded(t *testing.T) {
    Given(t, NewOrder("order-123"),
        OrderCreated{OrderID: "order-123", CustomerID: "customer-456"},
    ).
        When(AddItemsCommand{
            OrderID: "order-123",
            Items: []Item{
                {SKU: "SKU-001", Quantity: 2, Price: 29.99},
                {SKU: "SKU-002", Quantity: 1, Price: 49.99},
            },
        }).
        Then(
            ItemAdded{SKU: "SKU-001", Quantity: 2, Price: 29.99},
            ItemAdded{SKU: "SKU-002", Quantity: 1, Price: 49.99},
        )
}
```

---

## Projection Testing

```go
package minktest

// ProjectionTestFixture tests projections
type ProjectionTestFixture[T any] struct {
    t          *testing.T
    projection mink.Projection
    repo       mink.Repository[T]
}

// TestProjection creates projection test fixture
func TestProjection[T any](t *testing.T, projection mink.Projection) *ProjectionTestFixture[T] {
    return &ProjectionTestFixture[T]{
        t:          t,
        projection: projection,
        repo:       memory.NewRepository[T](),
    }
}

// GivenEvents applies events to projection
func (f *ProjectionTestFixture[T]) GivenEvents(events ...mink.Event) *ProjectionTestFixture[T] {
    for _, event := range events {
        f.projection.Apply(context.Background(), nil, event)
    }
    return f
}

// ThenReadModel asserts read model state
func (f *ProjectionTestFixture[T]) ThenReadModel(id string, expected T) {
    f.t.Helper()
    
    actual, err := f.repo.Get(context.Background(), id)
    if err != nil {
        f.t.Fatalf("Failed to get read model: %v", err)
    }
    
    if !reflect.DeepEqual(*actual, expected) {
        f.t.Errorf("Read model mismatch:\nExpected: %+v\nActual: %+v", expected, *actual)
    }
}

// Usage
func TestOrderSummaryProjection(t *testing.T) {
    TestProjection[OrderSummary](t, &OrderSummaryProjection{}).
        GivenEvents(
            Event{Type: "OrderCreated", Data: OrderCreated{
                OrderID: "order-123", CustomerID: "cust-456",
            }},
            Event{Type: "ItemAdded", Data: ItemAdded{
                OrderID: "order-123", SKU: "WIDGET", Quantity: 2, Price: 29.99,
            }},
        ).
        ThenReadModel("order-123", OrderSummary{
            ID:          "order-123",
            CustomerID:  "cust-456",
            Status:      "Created",
            ItemCount:   2,
            TotalAmount: 59.98,
        })
}
```

---

## Event Assertions

```go
package minktest

// AssertEventTypes checks event types match
func AssertEventTypes(t *testing.T, events []interface{}, types ...string) {
    t.Helper()
    
    if len(events) != len(types) {
        t.Fatalf("Expected %d events, got %d", len(types), len(events))
    }
    
    for i, expectedType := range types {
        actualType := reflect.TypeOf(events[i]).Name()
        if actualType != expectedType {
            t.Errorf("Event %d: expected type %s, got %s", i, expectedType, actualType)
        }
    }
}

// AssertEventData checks specific event data
func AssertEventData[T any](t *testing.T, event interface{}, expected T) {
    t.Helper()
    
    actual, ok := event.(T)
    if !ok {
        t.Fatalf("Event is not of expected type %T", expected)
    }
    
    if !reflect.DeepEqual(actual, expected) {
        t.Errorf("Event data mismatch:\nExpected: %+v\nActual: %+v", expected, actual)
    }
}

// AssertStreamVersion checks stream version
func AssertStreamVersion(t *testing.T, store *mink.EventStore, streamID string, expected int64) {
    t.Helper()
    
    info, err := store.GetStreamInfo(context.Background(), streamID)
    if err != nil {
        t.Fatalf("Failed to get stream info: %v", err)
    }
    
    if info.Version != expected {
        t.Errorf("Stream %s: expected version %d, got %d", streamID, expected, info.Version)
    }
}

// AssertNoEvents checks no uncommitted events
func AssertNoEvents(t *testing.T, agg mink.Aggregate) {
    t.Helper()
    
    events := agg.UncommittedEvents()
    if len(events) > 0 {
        t.Errorf("Expected no uncommitted events, got %d", len(events))
    }
}

// Usage
func TestOrderCreation(t *testing.T) {
    order := NewOrder("order-123")
    order.Create("customer-456")
    order.AddItem("SKU-001", 2, 29.99)
    
    minktest.AssertEventTypes(t, order.UncommittedEvents(),
        "OrderCreated", "ItemAdded")
    
    minktest.AssertEventData(t, order.UncommittedEvents()[0], OrderCreated{
        OrderID:    "order-123",
        CustomerID: "customer-456",
    })
}
```

---

## Event Diffing

```go
package minktest

// DiffEvents shows differences between event slices
func DiffEvents(t *testing.T, expected, actual []interface{}) {
    t.Helper()
    
    maxLen := len(expected)
    if len(actual) > maxLen {
        maxLen = len(actual)
    }
    
    var diffs []string
    for i := 0; i < maxLen; i++ {
        var exp, act interface{}
        if i < len(expected) {
            exp = expected[i]
        }
        if i < len(actual) {
            act = actual[i]
        }
        
        if !reflect.DeepEqual(exp, act) {
            diffs = append(diffs, formatDiff(i, exp, act))
        }
    }
    
    if len(diffs) > 0 {
        t.Errorf("Event differences:\n%s", strings.Join(diffs, "\n"))
    }
}

func formatDiff(index int, expected, actual interface{}) string {
    var buf strings.Builder
    buf.WriteString(fmt.Sprintf("Event %d:\n", index))
    
    if expected == nil {
        buf.WriteString(fmt.Sprintf("  + %T %+v (unexpected)\n", actual, actual))
    } else if actual == nil {
        buf.WriteString(fmt.Sprintf("  - %T %+v (missing)\n", expected, expected))
    } else {
        buf.WriteString(fmt.Sprintf("  - %T %+v\n", expected, expected))
        buf.WriteString(fmt.Sprintf("  + %T %+v\n", actual, actual))
    }
    
    return buf.String()
}

// Usage
func TestWithDiff(t *testing.T) {
    expected := []interface{}{
        OrderCreated{OrderID: "123"},
        ItemAdded{SKU: "WIDGET", Quantity: 2},
    }
    
    actual := []interface{}{
        OrderCreated{OrderID: "123"},
        ItemAdded{SKU: "WIDGET", Quantity: 3}, // Wrong quantity
    }
    
    minktest.DiffEvents(t, expected, actual)
    // Output:
    // Event 1:
    //   - ItemAdded{SKU: "WIDGET", Quantity: 2}
    //   + ItemAdded{SKU: "WIDGET", Quantity: 3}
}
```

---

## Saga Testing

```go
package minktest

// SagaTestFixture tests saga behavior
type SagaTestFixture struct {
    t          *testing.T
    saga       mink.Saga
    commands   []mink.Command
    err        error
}

func TestSaga(t *testing.T, saga mink.Saga) *SagaTestFixture {
    return &SagaTestFixture{t: t, saga: saga}
}

func (f *SagaTestFixture) GivenEvents(events ...mink.Event) *SagaTestFixture {
    for _, event := range events {
        cmds, err := f.saga.HandleEvent(context.Background(), event)
        if err != nil {
            f.err = err
            return f
        }
        f.commands = append(f.commands, cmds...)
    }
    return f
}

func (f *SagaTestFixture) ThenCommands(expected ...mink.Command) {
    f.t.Helper()
    
    if len(f.commands) != len(expected) {
        f.t.Fatalf("Expected %d commands, got %d", len(expected), len(f.commands))
    }
    
    for i, exp := range expected {
        if !reflect.DeepEqual(f.commands[i], exp) {
            f.t.Errorf("Command %d mismatch:\nExpected: %+v\nActual: %+v",
                i, exp, f.commands[i])
        }
    }
}

func (f *SagaTestFixture) ThenCompleted() {
    f.t.Helper()
    if !f.saga.IsComplete() {
        f.t.Error("Expected saga to be complete")
    }
}

// Usage
func TestOrderFulfillmentSaga(t *testing.T) {
    saga := NewOrderFulfillmentSaga("saga-123")
    
    TestSaga(t, saga).
        GivenEvents(
            Event{Type: "OrderCreated", Data: OrderCreated{OrderID: "order-123"}},
        ).
        ThenCommands(
            RequestPayment{OrderID: "order-123"},
        )
    
    TestSaga(t, saga).
        GivenEvents(
            Event{Type: "PaymentReceived", Data: PaymentReceived{OrderID: "order-123"}},
        ).
        ThenCommands(
            ReserveInventory{OrderID: "order-123"},
        )
}
```

---

## Integration Test Helpers

```go
package minktest

// IntegrationTest provides real database testing
type IntegrationTest struct {
    t      *testing.T
    store  *mink.EventStore
    db     *sql.DB
}

func NewIntegrationTest(t *testing.T, connStr string) *IntegrationTest {
    db, err := sql.Open("pgx", connStr)
    if err != nil {
        t.Fatalf("Failed to connect: %v", err)
    }
    
    adapter := postgres.NewAdapter(db)
    store := mink.New(adapter)
    
    // Create test schema
    testSchema := fmt.Sprintf("test_%d", time.Now().UnixNano())
    db.Exec(fmt.Sprintf("CREATE SCHEMA %s", testSchema))
    
    it := &IntegrationTest{t: t, store: store, db: db}
    
    // Cleanup on test end
    t.Cleanup(func() {
        db.Exec(fmt.Sprintf("DROP SCHEMA %s CASCADE", testSchema))
        db.Close()
    })
    
    return it
}

func (it *IntegrationTest) Store() *mink.EventStore {
    return it.store
}

// Test with real PostgreSQL
func TestWithRealDatabase(t *testing.T) {
    if testing.Short() {
        t.Skip("Skipping integration test")
    }
    
    it := minktest.NewIntegrationTest(t, os.Getenv("TEST_DATABASE_URL"))
    
    order := NewOrder("order-123")
    order.Create("customer-456")
    
    err := it.Store().SaveAggregate(context.Background(), order)
    assert.NoError(t, err)
    
    // Verify in database
    loaded := NewOrder("order-123")
    it.Store().LoadAggregate(context.Background(), loaded)
    assert.Equal(t, "customer-456", loaded.CustomerID)
}
```

---

## Test Containers

```go
package minktest

import (
    "github.com/testcontainers/testcontainers-go"
    "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// PostgresContainer provides PostgreSQL for tests
func PostgresContainer(t *testing.T) (string, func()) {
    ctx := context.Background()
    
    container, err := postgres.RunContainer(ctx,
        testcontainers.WithImage("postgres:15-alpine"),
        postgres.WithDatabase("testdb"),
        postgres.WithUsername("test"),
        postgres.WithPassword("test"),
    )
    if err != nil {
        t.Fatalf("Failed to start container: %v", err)
    }
    
    connStr, _ := container.ConnectionString(ctx, "sslmode=disable")
    
    cleanup := func() {
        container.Terminate(ctx)
    }
    
    return connStr, cleanup
}

// Usage
func TestWithContainer(t *testing.T) {
    connStr, cleanup := minktest.PostgresContainer(t)
    defer cleanup()
    
    adapter := postgres.NewAdapter(connStr)
    store := mink.New(adapter)
    
    // Run tests...
}
```

---

Next: [Security →](security)
