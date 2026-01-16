# Full E-Commerce Example

This comprehensive example demonstrates all major go-mink features working together in a realistic e-commerce order fulfillment scenario.

## Features Demonstrated

### 1. Event Sourcing
- **Domain Events**: `OrderCreated`, `ItemAdded`, `OrderSubmitted`, `OrderShipped`, `OrderCompleted`
- **Event Storage**: All state changes are persisted as immutable events
- **State Reconstruction**: Order state is rebuilt by replaying events from the event store

### 2. CQRS (Command Query Responsibility Segregation)
- **Commands**: `CreateOrderCommand`, `AddItemCommand`, `SubmitOrderCommand`, `ShipOrderCommand`, `CompleteOrderCommand`
- **Command Bus**: Centralized command routing with middleware pipeline
- **Command Handlers**: Generic handlers that load aggregates, execute business logic, and save results

### 3. Middleware Pipeline
- **Recovery Middleware**: Catches panics and converts to errors
- **Logging Middleware**: Logs command execution and timing
- **Validation Middleware**: Validates command data before execution
- **Correlation ID Middleware**: Propagates correlation IDs for distributed tracing
- **Causation ID Middleware**: Tracks command causation chains
- **Idempotency Middleware**: Ensures exactly-once command processing

### 4. Projections (Read Models)
- **OrderSummaryProjection**: Builds queryable read model from events
- **Real-time Updates**: Projection updates as events are appended
- **Rebuild Support**: Can rebuild projections from event history

### 5. Saga Pattern (Process Manager)
- **OrderFulfillmentSaga**: Orchestrates multi-step order fulfillment
- **State Persistence**: Saga state is durably stored in PostgreSQL
- **Correlation**: Sagas are linked to orders via correlation IDs
- **Step Tracking**: Each step (payment, inventory, shipping) is tracked

### 6. Idempotency
- **Command Deduplication**: Same command ID is processed only once
- **Idempotency Store**: PostgreSQL-backed storage for command tracking
- **Automatic Detection**: Middleware detects and handles duplicates

### 7. Optimistic Concurrency Control
- **Version Tracking**: Each aggregate tracks its version
- **Conflict Detection**: Concurrent modifications are detected
- **Safe Updates**: Only the first of conflicting updates succeeds

## Prerequisites

- Go 1.21+
- PostgreSQL 16+ running locally
- Database credentials: `postgres:postgres@localhost:5432/mink_test`

## Running the Example

```bash
# Start PostgreSQL (if not running)
docker run -d --name mink-pg \
  -e POSTGRES_PASSWORD=postgres \
  -e POSTGRES_DB=mink_test \
  -p 5432:5432 \
  postgres:17

# Run the example
cd examples/full-ecommerce
go run .
```

## Scenarios

### Scenario 1: Happy Path - Complete Order Fulfillment
Demonstrates the full order lifecycle:
1. Create order
2. Add items
3. Submit order
4. Ship order
5. Complete order
6. Query read model

### Scenario 2: Saga State Persistence
Shows saga durability:
1. Create saga instance
2. Progress through steps (Payment → Inventory → Ship)
3. Reload saga from database
4. Query sagas by correlation ID
5. View saga statistics

### Scenario 3: Idempotency
Tests duplicate command prevention:
1. Dispatch a command with a specific ID
2. Dispatch the same command again
3. Verify only one execution occurred

### Scenario 4: Optimistic Concurrency
Validates concurrent access handling:
1. Load same aggregate twice
2. Modify first instance and save
3. Attempt to save second instance (should fail)
4. Retry with fresh load (should succeed)

## Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                         Command Bus                              │
│  ┌─────────┐  ┌─────────┐  ┌──────────┐  ┌──────────────────┐  │
│  │Recovery │→ │Logging  │→ │Validation│→ │Correlation/Cause │  │
│  │Middleware│  │Middleware│  │Middleware│  │Idempotency       │  │
│  └─────────┘  └─────────┘  └──────────┘  └──────────────────┘  │
└──────────────────────────────┬──────────────────────────────────┘
                               ↓
┌─────────────────────────────────────────────────────────────────┐
│                      Command Handlers                            │
│  CreateOrderHandler │ AddItemHandler │ SubmitHandler │ ...      │
└──────────────────────────────┬──────────────────────────────────┘
                               ↓
┌─────────────────────────────────────────────────────────────────┐
│                       Event Store                                │
│  ┌─────────────┐  ┌───────────────┐  ┌───────────────────────┐  │
│  │Load Aggregate│  │Save Aggregate │  │PostgreSQL Events Table│  │
│  └─────────────┘  └───────────────┘  └───────────────────────┘  │
└──────────────────────────────┬──────────────────────────────────┘
                               ↓
┌─────────────────────────────────────────────────────────────────┐
│  Projections          │  Sagas                │  Queries        │
│  ┌─────────────────┐  │  ┌─────────────────┐  │  ┌───────────┐  │
│  │OrderSummary     │  │  │OrderFulfillment │  │  │Read Models│  │
│  │Projection       │  │  │Saga             │  │  │           │  │
│  └─────────────────┘  │  └─────────────────┘  │  └───────────┘  │
└─────────────────────────────────────────────────────────────────┘
```

## Key Code Patterns

### Defining an Aggregate

```go
type Order struct {
    mink.AggregateBase
    CustomerID   string
    Items        []OrderItem
    Status       string
    // ...
}

func (o *Order) AddItem(sku string, qty int, price float64) error {
    if o.Status != "Created" {
        return errors.New("cannot add items after order submitted")
    }
    o.Apply(ItemAdded{OrderID: o.id, SKU: sku, Quantity: qty, Price: price})
    return nil
}

func (o *Order) ApplyEvent(event interface{}) error {
    switch e := event.(type) {
    case ItemAdded:
        o.Items = append(o.Items, OrderItem{SKU: e.SKU, Quantity: e.Quantity, Price: e.Price})
    // ... handle other events
    }
    return nil
}
```

### Command Handler

```go
handler := mink.NewGenericHandler(
    eventStore,
    func(id string) *Order { return NewOrder(id) },
    func(ctx context.Context, agg *Order, cmd *AddItemCommand) error {
        return agg.AddItem(cmd.SKU, cmd.Quantity, cmd.Price)
    },
)
commandBus.Register(handler)
```

### Saga Definition

```go
type OrderFulfillmentSaga struct {
    mink.SagaBase
    PaymentID    string
    ReservationID string
    ShipmentID   string
}

func (s *OrderFulfillmentSaga) ProcessPayment(orderID string, amount float64) error {
    s.PaymentID = fmt.Sprintf("PAY-%d", time.Now().UnixNano())
    s.CompleteStep("ProcessPayment")
    return nil
}
```

## Related Examples

- [basic](../basic) - Minimal event sourcing example
- [cqrs-postgres](../cqrs-postgres) - CQRS with PostgreSQL
- [sagas](../sagas) - Saga pattern in detail
- [projections](../projections) - Projection patterns
- [bdd-testing](../bdd-testing) - BDD-style testing
