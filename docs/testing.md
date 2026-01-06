---
layout: default
title: Testing
nav_order: 9
permalink: /docs/testing
---

# Testing
{: .no_toc }

{: .label .label-green }
Phase 4 Complete - Full Testing Utilities

## Table of contents
{: .no_toc .text-delta }

1. TOC
{:toc}

---

## Testing Philosophy

Mink provides comprehensive testing utilities to make event-sourced systems easy to test. The testing packages follow BDD patterns and provide type-safe, expressive assertions.

### Testing Pyramid

```
         /\
        /  \         E2E Tests (few)
       /----\        - Full system integration
      /      \       - Database, projections, sagas
     /--------\      - CLI workflows (20-step E2E)
    /          \     Integration Tests (some)
   /------------\    - Adapter tests
  /              \   - Projection tests
 /----------------\  - CLI commands (67 integration tests)
/                  \ Unit Tests (many)
/--------------------\
                      - Aggregate logic
                      - Command validation
                      - Event handlers
                      - CLI helpers (~200 tests)
```

---

## Testing Packages

Mink v0.4.0 includes a comprehensive suite of testing utilities:

| Package | Purpose |
|---------|---------|
| `testing/bdd` | BDD-style Given-When-Then fixtures |
| `testing/assertions` | Event assertions and diffing |
| `testing/projections` | Projection testing helpers |
| `testing/sagas` | Saga testing fixtures |
| `testing/containers` | PostgreSQL test containers |
| `testing/testutil` | Mock adapters and helpers |

---

## BDD-Style Testing

The `testing/bdd` package provides Given-When-Then style test fixtures.

### Aggregate Testing

```go
import "github.com/AshkanYarmoradi/go-mink/testing/bdd"

func TestOrderCanBeCreated(t *testing.T) {
    order := NewOrder("order-123")

    bdd.Given(t, order).
        When(func() error {
            return order.Create("customer-456")
        }).
        Then(
            OrderCreated{
                OrderID:    "order-123",
                CustomerID: "customer-456",
            },
        )
}

func TestCannotAddItemToShippedOrder(t *testing.T) {
    order := NewOrder("order-123")

    bdd.Given(t, order,
        OrderCreated{OrderID: "order-123", CustomerID: "customer-456"},
        OrderShipped{OrderID: "order-123"},
    ).
        When(func() error {
            return order.AddItem("SKU-001", 1, 29.99)
        }).
        ThenError(ErrOrderAlreadyShipped)
}

func TestErrorMessageContains(t *testing.T) {
    order := NewOrder("order-123")

    bdd.Given(t, order,
        OrderCreated{OrderID: "order-123", CustomerID: "customer-456"},
    ).
        When(func() error {
            return order.AddItem("SKU-001", 0, 29.99) // Invalid quantity
        }).
        ThenErrorContains("quantity must be positive")
}

func TestNoEventsProduced(t *testing.T) {
    order := NewOrder("order-123")

    bdd.Given(t, order,
        OrderCreated{OrderID: "order-123", CustomerID: "customer-456"},
    ).
        When(func() error {
            // Already created, no new events
            return nil
        }).
        ThenNoEvents()
}
```

### Command Bus Testing

```go
func TestCommandBusIntegration(t *testing.T) {
    bus := mink.NewCommandBus()
    store := mink.New(memory.NewAdapter())

    // Register handlers...

    bdd.GivenCommand(t, bus, store).
        WithContext(ctx).
        WithExistingEvents("order-123",
            OrderCreated{OrderID: "order-123", CustomerID: "cust-456"},
        ).
        When(AddItemCommand{
            OrderID:  "order-123",
            SKU:      "SKU-001",
            Quantity: 2,
        }).
        ThenSucceeds().
        ThenReturnsAggregateID("order-123").
        ThenReturnsVersion(2)
}

func TestCommandFailure(t *testing.T) {
    bus := mink.NewCommandBus()

    bdd.GivenCommand(t, bus, nil).
        When(InvalidCommand{}).
        ThenFails(mink.ErrValidationFailed)
}
```

---

## Event Assertions

The `testing/assertions` package provides utilities for asserting event properties.

### Basic Assertions

```go
import "github.com/AshkanYarmoradi/go-mink/testing/assertions"

func TestOrderCreation(t *testing.T) {
    order := NewOrder("order-123")
    order.Create("customer-456")
    order.AddItem("SKU-001", 2, 29.99)

    events := order.UncommittedEvents()

    // Assert event types
    assertions.AssertEventTypes(t, events, "OrderCreated", "ItemAdded")

    // Assert event count
    assertions.AssertEventCount(t, events, 2)

    // Assert first event data
    assertions.AssertFirstEvent(t, events, OrderCreated{
        OrderID:    "order-123",
        CustomerID: "customer-456",
    })

    // Assert last event
    assertions.AssertLastEvent(t, events, ItemAdded{
        OrderID:  "order-123",
        SKU:      "SKU-001",
        Quantity: 2,
        Price:    29.99,
    })

    // Assert event at specific index
    assertions.AssertEventAtIndex(t, events, 0, OrderCreated{
        OrderID:    "order-123",
        CustomerID: "customer-456",
    })
}
```

### Contains Assertions

```go
func TestContainsAssertions(t *testing.T) {
    events := []interface{}{
        OrderCreated{OrderID: "123"},
        ItemAdded{SKU: "SKU-1"},
        ItemAdded{SKU: "SKU-2"},
        OrderShipped{OrderID: "123"},
    }

    // Assert contains specific event
    assertions.AssertContainsEvent(t, events, ItemAdded{SKU: "SKU-1"})

    // Assert contains event type
    assertions.AssertContainsEventType(t, events, "OrderShipped")

    // Assert no events (fails if not empty)
    assertions.AssertNoEvents(t, []interface{}{})
}
```

### Event Diffing

```go
func TestEventDiffing(t *testing.T) {
    expected := []interface{}{
        OrderCreated{OrderID: "123", CustomerID: "cust-1"},
        ItemAdded{SKU: "SKU-1", Quantity: 2},
    }

    actual := []interface{}{
        OrderCreated{OrderID: "123", CustomerID: "cust-2"}, // Different customer
        ItemAdded{SKU: "SKU-1", Quantity: 3},               // Different quantity
    }

    // Get differences
    diffs := assertions.DiffEvents(expected, actual)

    // Format for display
    if len(diffs) > 0 {
        t.Error(assertions.FormatDiffs(diffs))
    }

    // Or use assertion helper
    assertions.AssertEventsEqual(t, expected, actual)
}
```

### Event Matchers

```go
func TestEventMatchers(t *testing.T) {
    events := []interface{}{
        OrderCreated{OrderID: "123"},
        ItemAdded{SKU: "SKU-1"},
        ItemAdded{SKU: "SKU-2"},
    }

    // Match by type
    typeMatch := assertions.MatchEventType("ItemAdded")
    assertions.AssertAnyMatch(t, events, typeMatch)

    // Match specific event
    eventMatch := assertions.MatchEvent(ItemAdded{SKU: "SKU-1"})
    assertions.AssertAnyMatch(t, events, eventMatch)

    // Assert all match
    allItems := []interface{}{
        ItemAdded{SKU: "SKU-1"},
        ItemAdded{SKU: "SKU-2"},
    }
    assertions.AssertAllMatch(t, allItems, assertions.MatchEventType("ItemAdded"))

    // Assert none match
    assertions.AssertNoneMatch(t, events, assertions.MatchEventType("OrderCancelled"))

    // Count matches
    count := assertions.CountMatches(events, assertions.MatchEventType("ItemAdded"))
    assert.Equal(t, 2, count)

    // Filter events
    filtered := assertions.FilterEvents(events, assertions.MatchEventType("ItemAdded"))
    assert.Len(t, filtered, 2)
}
```

---

## Projection Testing

The `testing/projections` package provides fixtures for testing projections.

### Inline Projection Testing

```go
import "github.com/AshkanYarmoradi/go-mink/testing/projections"

func TestOrderSummaryProjection(t *testing.T) {
    projection := &OrderSummaryProjection{repo: mink.NewInMemoryRepository[OrderSummary](nil)}

    projections.TestProjection[OrderSummary](t, projection).
        GivenEvents(
            mink.StoredEvent{
                StreamID: "order-123",
                Type:     "OrderCreated",
                Data:     []byte(`{"order_id":"order-123","customer_id":"cust-456"}`),
            },
            mink.StoredEvent{
                StreamID: "order-123",
                Type:     "ItemAdded",
                Data:     []byte(`{"sku":"SKU-1","quantity":2,"price":29.99}`),
            },
        ).
        ThenReadModel("order-123", OrderSummary{
            ID:          "order-123",
            CustomerID:  "cust-456",
            ItemCount:   2,
            TotalAmount: 59.98,
        })
}
```

### Testing with Domain Events

```go
func TestProjectionWithDomainEvents(t *testing.T) {
    projection := &OrderSummaryProjection{repo: mink.NewInMemoryRepository[OrderSummary](nil)}

    projections.TestProjection[OrderSummary](t, projection).
        GivenDomainEvents("order-123",
            OrderCreated{OrderID: "order-123", CustomerID: "cust-456"},
            ItemAdded{OrderID: "order-123", SKU: "SKU-1", Quantity: 2, Price: 29.99},
        ).
        ThenReadModelExists("order-123")
}
```

### Read Model Assertions

```go
func TestReadModelAssertions(t *testing.T) {
    projection := &OrderSummaryProjection{repo: mink.NewInMemoryRepository[OrderSummary](nil)}

    fixture := projections.TestProjection[OrderSummary](t, projection).
        GivenDomainEvents("order-123",
            OrderCreated{OrderID: "order-123", CustomerID: "cust-456"},
        )

    // Assert existence
    model := fixture.ThenReadModelExists("order-123")

    // Assert non-existence
    fixture.ThenReadModelNotExists("order-456")

    // Assert count
    fixture.ThenReadModelCount(1)

    // Custom assertion
    fixture.ThenReadModelMatches("order-123", func(t testing.TB, rm *OrderSummary) {
        assert.Equal(t, "cust-456", rm.CustomerID)
        assert.Equal(t, 0, rm.ItemCount)
    })
}
```

### Projection Engine Testing

```go
func TestProjectionEngine(t *testing.T) {
    fixture := projections.TestEngine(t).
        RegisterInline(&OrderSummaryProjection{}).
        Start()
    defer fixture.Stop()

    fixture.
        AppendEvents("order-123",
            OrderCreated{OrderID: "order-123", CustomerID: "cust-456"},
        ).
        WaitForProjection("OrderSummary", 5*time.Second)

    status, _ := fixture.Engine().GetStatus("OrderSummary")
    assert.Equal(t, mink.ProjectionStateRunning, status.State)
}
```

---

## Saga Testing

The `testing/sagas` package provides fixtures for testing sagas and process managers.

### Basic Saga Testing

```go
import "github.com/AshkanYarmoradi/go-mink/testing/sagas"

func TestOrderFulfillmentSaga(t *testing.T) {
    saga := NewOrderFulfillmentSaga("saga-123")

    sagas.TestSaga(t, saga).
        GivenEvents(
            mink.StoredEvent{
                Type: "OrderCreated",
                Data: []byte(`{"order_id":"order-123"}`),
            },
        ).
        ThenCommands(
            RequestPaymentCommand{OrderID: "order-123"},
        ).
        ThenNotCompleted()
}

func TestSagaCompletion(t *testing.T) {
    saga := NewOrderFulfillmentSaga("saga-123")

    sagas.TestSaga(t, saga).
        GivenEvents(
            mink.StoredEvent{Type: "OrderCreated", Data: []byte(`{"order_id":"order-123"}`)},
            mink.StoredEvent{Type: "PaymentReceived", Data: []byte(`{"order_id":"order-123"}`)},
            mink.StoredEvent{Type: "InventoryReserved", Data: []byte(`{"order_id":"order-123"}`)},
            mink.StoredEvent{Type: "OrderShipped", Data: []byte(`{"order_id":"order-123"}`)},
        ).
        ThenCompleted()
}
```

### Saga Command Assertions

```go
func TestSagaCommandAssertions(t *testing.T) {
    saga := NewOrderFulfillmentSaga("saga-123")

    sagas.TestSaga(t, saga).
        GivenEvents(
            mink.StoredEvent{Type: "OrderCreated", Data: []byte(`{"order_id":"order-123"}`)},
            mink.StoredEvent{Type: "PaymentReceived", Data: []byte(`{"order_id":"order-123"}`)},
        ).
        ThenCommandCount(2).
        ThenFirstCommand(RequestPaymentCommand{OrderID: "order-123"}).
        ThenLastCommand(ReserveInventoryCommand{OrderID: "order-123"}).
        ThenContainsCommand(RequestPaymentCommand{OrderID: "order-123"})
}
```

### Saga State Testing

```go
func TestSagaState(t *testing.T) {
    saga := NewOrderFulfillmentSaga("saga-123")

    sagas.TestSaga(t, saga).
        GivenEvents(
            mink.StoredEvent{Type: "PaymentReceived", Data: []byte(`{}`)},
        ).
        ThenState(SagaStateAwaitingInventory)
}
```

### Compensation Testing

```go
func TestSagaCompensation(t *testing.T) {
    saga := NewOrderFulfillmentSaga("saga-123")

    sagas.TestCompensation(t, saga).
        GivenFailureAfter(
            mink.StoredEvent{Type: "OrderCreated", Data: []byte(`{"order_id":"order-123"}`)},
            mink.StoredEvent{Type: "PaymentReceived", Data: []byte(`{"order_id":"order-123"}`)},
            mink.StoredEvent{Type: "InventoryFailed", Data: []byte(`{"order_id":"order-123"}`)},
        ).
        ThenCompensates(
            RefundPaymentCommand{OrderID: "order-123"},
            CancelOrderCommand{OrderID: "order-123"},
        )
}
```

---

## Test Containers

The `testing/containers` package provides PostgreSQL test containers for integration tests.

### Starting PostgreSQL

```go
import "github.com/AshkanYarmoradi/go-mink/testing/containers"

func TestWithPostgres(t *testing.T) {
    if testing.Short() {
        t.Skip("Skipping integration test")
    }

    container := containers.StartPostgres(t,
        containers.WithPostgresImage("postgres:17"),
        containers.WithPostgresDatabase("test_db"),
        containers.WithPostgresUser("test_user"),
        containers.WithPostgresPassword("test_pass"),
    )

    // Get connection
    db := container.MustDB(context.Background())
    defer db.Close()

    // Use with mink adapter
    adapter := postgres.NewAdapter(db)
    store := mink.New(adapter)

    // Run tests...
}
```

### Test Isolation with Schemas

```go
func TestWithIsolatedSchema(t *testing.T) {
    container := containers.StartPostgres(t)
    ctx := context.Background()

    // Create isolated schema for this test
    schema := container.CreateSchema(ctx, t, "test_order")
    defer container.DropSchema(ctx, t, schema)

    // Initialize mink tables in schema
    container.SetupMinkSchema(ctx, t, schema)

    // Get connection for schema
    db := container.MustDBWithSchema(ctx, schema)

    // Run tests with isolated data...
}
```

### Integration Test Fixture

```go
func TestFullIntegration(t *testing.T) {
    it := containers.NewIntegrationTest(t)

    // Store events
    err := it.Store().Append(ctx, "order-123", []interface{}{
        OrderCreated{OrderID: "order-123", CustomerID: "cust-456"},
    })
    require.NoError(t, err)

    // Load and verify
    events, err := it.Store().Load(ctx, "order-123", 0)
    require.NoError(t, err)
    assert.Len(t, events, 1)
}
```

### Full Stack Testing

```go
func TestFullStack(t *testing.T) {
    fixture := containers.NewFullStackTest(t)

    // Access components
    store := fixture.Store()
    bus := fixture.CommandBus()
    engine := fixture.ProjectionEngine()

    // Run full integration test...
}
```

---

## Mock Adapters

The `testing/testutil` package provides mock implementations for testing.

### Mock Event Store Adapter

```go
import "github.com/AshkanYarmoradi/go-mink/testing/testutil"

func TestWithMockAdapter(t *testing.T) {
    adapter := &testutil.MockAdapter{}
    store := mink.New(adapter)

    // Configure mock behavior
    adapter.AppendErr = mink.ErrConcurrencyConflict

    // Test error handling
    err := store.Append(ctx, "order-123", events)
    assert.ErrorIs(t, err, mink.ErrConcurrencyConflict)
}
```

### Mock Projection

```go
func TestWithMockProjection(t *testing.T) {
    projection := testutil.NewMockProjection("TestProjection",
        testutil.WithHandledEvents("OrderCreated", "ItemAdded"),
    )

    // Apply events
    err := projection.Apply(ctx, storedEvent)
    require.NoError(t, err)

    // Check applied events
    assert.Len(t, projection.AppliedEvents, 1)
}
```

---

## Running Tests

### Unit Tests Only

```bash
# No infrastructure required
go test -short ./...
```

### All Tests with Infrastructure

```bash
# Start PostgreSQL
docker-compose -f docker-compose.test.yml up -d

# Run all tests
go test ./...

# Or use make
make test
```

### Tests with Coverage

```bash
make test-coverage

# View HTML report
go tool cover -html=coverage.out
```

### Environment Variables

The test containers respect these environment variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `POSTGRES_IMAGE` | `postgres:17` | Docker image |
| `POSTGRES_DB` | `mink_test` | Database name |
| `POSTGRES_USER` | `postgres` | Username |
| `POSTGRES_PASSWORD` | `postgres` | Password |
| `POSTGRES_PORT` | `5432` | Host port |

---

## CLI Testing

The CLI tool (`cli/commands`) has comprehensive test coverage with **84.9% code coverage**.

### CLI Test Categories

| Category | Count | Description |
|----------|-------|-------------|
| Unit Tests | ~200 | Core logic, helpers, validation, error paths |
| Integration Tests | 67 | PostgreSQL operations via real database |
| E2E Tests | 4 | Complete 20-step workflows |

### Running CLI Tests

```bash
# Start PostgreSQL
docker-compose -f docker-compose.test.yml up -d

# Run all CLI tests
cd cli/commands
go test -tags=integration -cover -timeout 180s

# Run E2E tests only
go test -tags=integration -run "TestE2E" -v

# Run unit tests only (no database required)
go test -short ./...
```

### CLI E2E Test Workflows

**`TestE2E_CompleteCliWorkflow`** exercises a full 20-step workflow:

```go
// This test executes these steps against real PostgreSQL:
// 1. Initialize mink project (mink.yaml)
// 2. Generate Order aggregate with events
// 3. Generate OrderSummary projection  
// 4. Generate CreateOrder command
// 5. Create migration file
// 6. Check migration status (pending)
// 7. Apply migration (CREATE TABLE)
// 8. Insert test events into database
// 9. List streams (stream list)
// 10. Get stream events (stream events order-e2e-001)
// 11. Get stream stats (stream stats)
// 12. Export stream to JSON (stream export)
// 13. Create projection checkpoint
// 14. Get projection status (projection status)
// 15. Pause projection (projection pause)
// 16. Resume projection (projection resume)
// 17. Rebuild projection (projection rebuild)
// 18. Run diagnostics (diagnose)
// 19. Rollback migration (DROP TABLE)
// 20. Final verification
```

Additional E2E tests:
- **`TestE2E_MultiAggregateWorkflow`** - 3 aggregates + 3 projections
- **`TestE2E_MigrationLifecycle`** - Full up/down migration cycle
- **`TestE2E_ProjectionManagement`** - List, status, pause, resume, rebuild

### CLI Test Coverage by Package

| Package | Coverage | Notes |
|---------|----------|-------|
| `cli/commands` | 84.9% | Unit + Integration + E2E |
| `cli/config` | 95.5% | Configuration handling |
| `cli/styles` | 100% | Terminal styling |
| `cli/ui` | 96.7% | UI components |

---

## Best Practices

### 1. Test Aggregate Logic First

```go
// Good: Test aggregate behavior in isolation
func TestOrderLogic(t *testing.T) {
    bdd.Given(t, NewOrder("123")).
        When(func() error { return order.AddItem(...) }).
        Then(...)
}
```

### 2. Use BDD for Readability

```go
// Good: Clear Given-When-Then structure
bdd.Given(t, aggregate, previousEvents...).
    When(commandFunc).
    Then(expectedEvents...)

// Avoid: Imperative test code that's hard to read
order.ApplyEvent(event1)
order.ApplyEvent(event2)
err := order.DoSomething()
assert.NoError(t, err)
events := order.UncommittedEvents()
assert.Len(t, events, 1)
```

### 3. Isolate Integration Tests

```go
// Good: Use schema isolation
schema := container.CreateSchema(ctx, t, "test_"+t.Name())
defer container.DropSchema(ctx, t, schema)
```

### 4. Skip Slow Tests in Short Mode

```go
func TestIntegration(t *testing.T) {
    if testing.Short() {
        t.Skip("Skipping integration test")
    }
    // ...
}
```

### 5. Use Table-Driven Tests

```go
func TestOrderValidation(t *testing.T) {
    tests := []struct {
        name    string
        command AddItemCommand
        wantErr error
    }{
        {"valid", AddItemCommand{SKU: "SKU-1", Qty: 1}, nil},
        {"zero quantity", AddItemCommand{SKU: "SKU-1", Qty: 0}, ErrInvalidQuantity},
        {"empty SKU", AddItemCommand{SKU: "", Qty: 1}, ErrInvalidSKU},
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            err := tt.command.Validate()
            assert.ErrorIs(t, err, tt.wantErr)
        })
    }
}
```

---

Next: [Security](security)
