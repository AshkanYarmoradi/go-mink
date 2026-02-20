---
layout: default
title: Advanced Patterns
nav_order: 8
permalink: /docs/advanced-patterns
---

# Advanced Patterns
{: .no_toc }

## Table of contents
{: .no_toc .text-delta }

1. TOC
{:toc}

---

## Command Bus & CQRS (v0.2.0) ✅

Complete CQRS pattern implementation with first-class Command support.

### Command Definition

```go
// Command represents intent to change state
type Command interface {
    CommandType() string
    Validate() error
}

// CommandBase provides common functionality (embed in your commands)
type CommandBase struct {
    id            string
    correlationID string
    causationID   string
    tenantID      string
    metadata      map[string]string
}

// CommandBase methods
func (c *CommandBase) GetID() string                   { return c.id }
func (c *CommandBase) SetID(id string)                 { c.id = id }
func (c *CommandBase) GetCorrelationID() string        { return c.correlationID }
func (c *CommandBase) SetCorrelationID(id string)      { c.correlationID = id }
func (c *CommandBase) GetCausationID() string          { return c.causationID }
func (c *CommandBase) SetCausationID(id string)        { c.causationID = id }
func (c *CommandBase) GetTenantID() string             { return c.tenantID }
func (c *CommandBase) SetTenantID(id string)           { c.tenantID = id }
func (c *CommandBase) GetMetadata() map[string]string  { return c.metadata }
func (c *CommandBase) SetMetadata(m map[string]string) { c.metadata = m }

// Example command with validation
type CreateOrder struct {
    mink.CommandBase
    CustomerID string `json:"customerId"`
    Items      []Item `json:"items"`
}

func (c CreateOrder) CommandType() string { return "CreateOrder" }
func (c CreateOrder) Validate() error {
    if c.CustomerID == "" {
        return mink.NewValidationError("CreateOrder", "CustomerID", "required")
    }
    return nil
}
```

### Command Handler (Type-Safe Generics)

```go
// GenericHandler provides type-safe command handling
handler := mink.NewGenericHandler[CreateOrder](func(ctx context.Context, cmd CreateOrder) (mink.CommandResult, error) {
    // Create aggregate and apply command
    order := NewOrder(uuid.NewString())
    if err := order.Create(cmd.CustomerID, cmd.Items); err != nil {
        return mink.CommandResult{}, err
    }
    
    // Persist using event store
    if err := store.SaveAggregate(ctx, order); err != nil {
        return mink.CommandResult{}, err
    }
    
    return mink.NewSuccessResult(order.AggregateID(), order.Version()), nil
})

// Or use struct-based handler implementing CommandHandler interface
type CreateOrderHandler struct {
    store *mink.EventStore
}

func (h *CreateOrderHandler) CommandType() string { return "CreateOrder" }

func (h *CreateOrderHandler) Handle(ctx context.Context, cmd mink.Command) (mink.CommandResult, error) {
    c := cmd.(CreateOrder)
    // ... implementation
    return mink.NewSuccessResult("order-123", 1), nil
}
```

### AggregateHandler (Combined Load/Save)

```go
// AggregateHandler automatically loads/saves aggregates
handler := mink.NewAggregateHandler[CreateOrder, *Order](
    store,
    func(cmd CreateOrder) string {
        return cmd.OrderID // Aggregate ID from command
    },
    func() *Order {
        return NewOrder("") // Aggregate factory
    },
    func(ctx context.Context, agg *Order, cmd CreateOrder) error {
        return agg.Create(cmd.CustomerID, cmd.Items)
    },
)
```

### Command Bus

```go
// Create command bus
bus := mink.NewCommandBus()

// Register handlers
bus.Register(handler)                                    // Struct handler
bus.RegisterFunc("CreateOrder", handleFunc)             // Function handler

// Dispatch command
result, err := bus.Dispatch(ctx, CreateOrder{
    CustomerID: "cust-123",
    Items:      []Item{{SKU: "WIDGET-01", Qty: 2}},
})

// CommandResult contains execution results
type CommandResult struct {
    AggregateID string          // ID of affected aggregate
    Version     int64           // New version after command
    Events      []interface{}   // Events produced
    Data        interface{}     // Optional return data
}

// Helper constructor
result := mink.NewSuccessResult("order-123", 1)
```

### Command Middleware Pipeline

```go
bus := mink.NewCommandBus()

// Add middleware (executed in order)
bus.Use(mink.ValidationMiddleware())      // Validate commands
bus.Use(mink.RecoveryMiddleware())        // Panic recovery
bus.Use(mink.LoggingMiddleware(logger))   // Log commands
bus.Use(mink.MetricsMiddleware(metrics))  // Record metrics
bus.Use(mink.TimeoutMiddleware(5*time.Second))  // Timeout
bus.Use(mink.RetryMiddleware(3, time.Second))   // Retry on failure
bus.Use(mink.CorrelationIDMiddleware(nil))      // Auto-generate correlation ID
bus.Use(mink.CausationIDMiddleware())           // Track causation chain
bus.Use(mink.TenantMiddleware(func(ctx context.Context) string {
    return ctx.Value("tenantID").(string)  // Multi-tenancy
}))
```

### Built-in Middleware Reference

| Middleware | Description |
|-----------|-------------|
| `ValidationMiddleware()` | Calls `cmd.Validate()` before handling |
| `RecoveryMiddleware()` | Catches panics and returns `PanicError` |
| `LoggingMiddleware(logger)` | Logs command start/end with timing |
| `MetricsMiddleware(metrics)` | Records command count, duration, errors |
| `TimeoutMiddleware(duration)` | Adds context timeout |
| `RetryMiddleware(attempts, delay)` | Retries on transient failures |
| `CorrelationIDMiddleware(generator)` | Sets/generates correlation ID |
| `CausationIDMiddleware()` | Tracks event causation chain |
| `TenantMiddleware(resolver)` | Sets tenant ID from context |
| `IdempotencyMiddleware(config)` | Prevents duplicate processing |

---

## Idempotency (v0.2.0) ✅

Prevent duplicate command processing - essential for reliability.

### Idempotency Store Interface

```go
// IdempotencyStore tracks processed commands
type IdempotencyStore interface {
    // Store records a command execution result
    Store(ctx context.Context, key string, response []byte, expiration time.Duration) error
    
    // Get retrieves a previously stored result
    Get(ctx context.Context, key string) ([]byte, error)
    
    // Close releases resources
    Close() error
}
```

### In-Memory Store (Testing)

```go
import "github.com/AshkanYarmoradi/go-mink/adapters/memory"

store := memory.NewIdempotencyStore()
defer store.Close()

// Use with middleware
bus.Use(mink.IdempotencyMiddleware(mink.DefaultIdempotencyConfig(store)))
```

### PostgreSQL Store (Production)

```go
import "github.com/AshkanYarmoradi/go-mink/adapters/postgres"

store, err := postgres.NewIdempotencyStore(connStr)
if err != nil {
    log.Fatal(err)
}
defer store.Close()

// Initialize schema (run once)
if err := store.Initialize(ctx); err != nil {
    log.Fatal(err)
}

// Use with middleware
bus.Use(mink.IdempotencyMiddleware(mink.DefaultIdempotencyConfig(store)))
```

### Idempotency Key Generation

```go
// Commands can implement IdempotentCommand for custom keys
type IdempotentCommand interface {
    Command
    IdempotencyKey() string
}

// Example with explicit idempotency key
type CreateOrder struct {
    mink.CommandBase
    ClientRequestID string `json:"clientRequestId"` // Client-provided
    CustomerID      string `json:"customerId"`
}

func (c CreateOrder) IdempotencyKey() string {
    if c.ClientRequestID != "" {
        return c.ClientRequestID
    }
    return "" // Fall back to auto-generation
}

// Auto-generated keys use SHA256 hash of command content
// Format: "CreateOrder:<hash>" or "CreateOrder:type-only:<hash>" for fallback
```

### Idempotency Configuration

```go
// IdempotencyConfig controls middleware behavior
type IdempotencyConfig struct {
    Store       IdempotencyStore        // Required: storage backend
    KeyFunc     func(Command) string    // Optional: custom key generator
    TTL         time.Duration           // Optional: result expiration (default: 24h)
    Serializer  func(CommandResult) []byte  // Optional: result serializer
}

// Use default configuration
config := mink.DefaultIdempotencyConfig(store)
bus.Use(mink.IdempotencyMiddleware(config))

// Or customize
config := mink.IdempotencyConfig{
    Store: store,
    TTL:   1 * time.Hour,
    KeyFunc: func(cmd mink.Command) string {
        // Custom key generation
        return fmt.Sprintf("%s:%s", cmd.CommandType(), extractKey(cmd))
    },
}
bus.Use(mink.IdempotencyMiddleware(config))
```

### Idempotency Flow

```
1. Command arrives
2. IdempotencyMiddleware generates key
3. Check if key exists in store
   ├─ EXISTS: Return cached result (skip handler)
   └─ NOT EXISTS: Continue to handler
4. Handler processes command
5. Store result with key and TTL
6. Return result to caller
```

### PostgreSQL Schema

```sql
-- Created by store.Initialize(ctx)
CREATE TABLE IF NOT EXISTS mink_idempotency (
    key VARCHAR(255) PRIMARY KEY,
    response JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_mink_idempotency_expires_at 
    ON mink_idempotency(expires_at);
```

---

## Correlation & Causation Tracking (v0.2.0) ✅

Track request flow through distributed systems.

### Correlation ID

Links all events/commands from a single external request.

```go
// Middleware auto-generates or propagates correlation ID
bus.Use(mink.CorrelationIDMiddleware(nil)) // Uses UUID generator

// Or provide custom generator
bus.Use(mink.CorrelationIDMiddleware(func() string {
    return fmt.Sprintf("req-%d", time.Now().UnixNano())
}))

// Access in handler
func (h *Handler) Handle(ctx context.Context, cmd mink.Command) (mink.CommandResult, error) {
    correlationID := cmd.(interface{ GetCorrelationID() string }).GetCorrelationID()
    // Use for logging, tracing, etc.
}
```

### Causation ID

Links events to the command/event that caused them.

```go
// Middleware automatically sets causation ID
bus.Use(mink.CausationIDMiddleware())

// Access in handler
func (h *Handler) Handle(ctx context.Context, cmd mink.Command) (mink.CommandResult, error) {
    causationID := cmd.(interface{ GetCausationID() string }).GetCausationID()
    // The causation ID is the ID of the previous command in the chain
}
```

### Metadata Propagation

```go
// Commands carry metadata through the system
type CreateOrder struct {
    mink.CommandBase
    CustomerID string
}

// Set metadata before dispatch
cmd := CreateOrder{CustomerID: "cust-123"}
cmd.SetCorrelationID("external-request-id")
cmd.SetMetadata(map[string]string{
    "source": "web-api",
    "version": "v2",
})

result, err := bus.Dispatch(ctx, cmd)
```

---

## Saga / Process Manager (v0.5.0) ✅

Orchestrate long-running business processes across aggregates. Sagas coordinate operations that span multiple aggregates or external services, providing compensation (rollback) when things go wrong.

### Core Interfaces

```go
// Saga defines the interface for saga implementations.
// A saga coordinates long-running business processes across multiple aggregates.
type Saga interface {
    // SagaID returns the unique identifier for this saga instance.
    SagaID() string

    // SagaType returns the type of this saga (e.g., "OrderFulfillment").
    SagaType() string

    // Status returns the current status of the saga.
    Status() SagaStatus

    // SetStatus sets the saga status.
    SetStatus(status SagaStatus)

    // CurrentStep returns the current step number (0-based).
    CurrentStep() int

    // SetCurrentStep sets the current step number.
    SetCurrentStep(step int)

    // CorrelationID returns the correlation ID for this saga.
    CorrelationID() string

    // SetCorrelationID sets the correlation ID.
    SetCorrelationID(id string)

    // HandledEvents returns the list of event types this saga handles.
    HandledEvents() []string

    // HandleEvent processes an event and returns commands to dispatch.
    HandleEvent(ctx context.Context, event StoredEvent) ([]Command, error)

    // Compensate is called when the saga needs to rollback.
    Compensate(ctx context.Context, failedStep int, failureReason error) ([]Command, error)

    // IsComplete returns true if the saga has completed successfully.
    IsComplete() bool

    // StartedAt returns when the saga started.
    StartedAt() time.Time

    // CompletedAt returns when the saga completed (nil if not completed).
    CompletedAt() *time.Time

    // Data returns the saga's internal state as a map.
    Data() map[string]interface{}

    // SetData restores the saga's internal state from a map.
    SetData(data map[string]interface{})

    // Version returns the saga version for optimistic concurrency.
    Version() int64
}

// SagaStatus represents the current status of a saga.
type SagaStatus int

const (
    SagaStatusStarted      SagaStatus = iota  // Saga has started
    SagaStatusRunning                         // Actively processing
    SagaStatusCompleted                       // Completed successfully
    SagaStatusFailed                          // Failed without compensation
    SagaStatusCompensating                    // Executing compensation
    SagaStatusCompensated                     // Compensated after failure
)

// SagaState represents the persisted state of a saga.
type SagaState struct {
    ID              string                 `json:"id"`
    Type            string                 `json:"type"`
    CorrelationID   string                 `json:"correlationId,omitempty"`
    Status          SagaStatus             `json:"status"`
    CurrentStep     int                    `json:"currentStep"`
    Data            map[string]interface{} `json:"data,omitempty"`
    ProcessedEvents []string               `json:"processedEvents,omitempty"` // For idempotency
    Steps           []SagaStep             `json:"steps,omitempty"`
    StartedAt       time.Time              `json:"startedAt"`
    UpdatedAt       time.Time              `json:"updatedAt"`
    CompletedAt     *time.Time             `json:"completedAt,omitempty"`
    FailureReason   string                 `json:"failureReason,omitempty"`
    Version         int64                  `json:"version"`
}

// SagaStore defines the interface for saga persistence.
type SagaStore interface {
    Save(ctx context.Context, state *SagaState) error
    Load(ctx context.Context, sagaID string) (*SagaState, error)
    FindByCorrelationID(ctx context.Context, correlationID string) (*SagaState, error)
    FindByType(ctx context.Context, sagaType string, statuses ...SagaStatus) ([]*SagaState, error)
    Delete(ctx context.Context, sagaID string) error
    Close() error
}
```

### SagaBase - Default Implementation

```go
// Embed SagaBase in your saga types to get default behavior
type OrderFulfillmentSaga struct {
    mink.SagaBase  // Provides ID, Type, Status, Version management
    
    // Your saga-specific state
    OrderID        string
    CustomerID     string
    PaymentDone    bool
    InventoryDone  bool
}

func NewOrderFulfillmentSaga(id string) *OrderFulfillmentSaga {
    return &OrderFulfillmentSaga{
        SagaBase: mink.NewSagaBase(id, "OrderFulfillment"),
    }
}
```

### Example: Order Fulfillment Saga

```go
type OrderFulfillmentSaga struct {
    mink.SagaBase
    
    // Saga state
    OrderID         string
    CustomerID      string
    Items           []Item
    TotalAmount     float64
    
    // Step tracking
    PaymentReceived    bool
    InventoryReserved  bool
    ShipmentCreated    bool
}

func (s *OrderFulfillmentSaga) HandledEvents() []string {
    return []string{
        "OrderCreated",
        "PaymentReceived", "PaymentFailed",
        "InventoryReserved", "InventoryReservationFailed",
        "ShipmentCreated", "ShipmentFailed",
    }
}

func (s *OrderFulfillmentSaga) HandleEvent(ctx context.Context, event Event) ([]Command, error) {
    switch e := event.Data.(type) {
    
    case OrderCreated:
        // Step 1: Request payment
        s.OrderID = e.OrderID
        s.CustomerID = e.CustomerID
        s.Items = e.Items
        s.TotalAmount = e.TotalAmount
        return []Command{
            RequestPayment{
                OrderID:    s.OrderID,
                CustomerID: s.CustomerID,
                Amount:     s.TotalAmount,
            },
        }, nil
        
    case PaymentReceived:
        // Step 2: Reserve inventory
        s.PaymentReceived = true
        return []Command{
            ReserveInventory{
                OrderID: s.OrderID,
                Items:   s.Items,
            },
        }, nil
        
    case PaymentFailed:
        // Saga failed at step 1 - no compensation needed
        return nil, &SagaFailedError{Step: 1, Reason: e.Reason}
        
    case InventoryReserved:
        // Step 3: Create shipment
        s.InventoryReserved = true
        return []Command{
            CreateShipment{
                OrderID: s.OrderID,
                Items:   s.Items,
            },
        }, nil
        
    case InventoryReservationFailed:
        // Saga failed at step 2 - compensate payment
        return nil, &SagaFailedError{Step: 2, Reason: e.Reason}
        
    case ShipmentCreated:
        // Saga complete!
        s.ShipmentCreated = true
        return []Command{
            CompleteOrder{OrderID: s.OrderID},
        }, nil
        
    case ShipmentFailed:
        // Saga failed at step 3 - compensate inventory and payment
        return nil, &SagaFailedError{Step: 3, Reason: e.Reason}
    }
    
    return nil, nil
}

func (s *OrderFulfillmentSaga) Compensate(ctx context.Context, failedStep int, err error) ([]Command, error) {
    var commands []Command
    
    // Compensate in reverse order
    if failedStep >= 3 && s.InventoryReserved {
        commands = append(commands, ReleaseInventory{
            OrderID: s.OrderID,
            Items:   s.Items,
        })
    }
    
    if failedStep >= 2 && s.PaymentReceived {
        commands = append(commands, RefundPayment{
            OrderID:    s.OrderID,
            CustomerID: s.CustomerID,
            Amount:     s.TotalAmount,
        })
    }
    
    commands = append(commands, FailOrder{
        OrderID: s.OrderID,
        Reason:  err.Error(),
    })
    
    return commands, nil
}

func (s *OrderFulfillmentSaga) IsComplete() bool {
    return s.PaymentReceived && s.InventoryReserved && s.ShipmentCreated
}
```

### Saga Manager

The SagaManager orchestrates saga lifecycle, event processing, and command dispatch. It provides:
- **Event correlation**: Routes events to the correct saga instances
- **Idempotency**: Automatically deduplicates events (stored in `ProcessedEvents`)
- **Concurrency control**: Per-saga locking with retry on conflicts
- **Compensation**: Triggers rollback commands when sagas fail

```go
// SagaManager orchestrates saga lifecycle
type SagaManager struct {
    store      SagaStore                        // Persists saga state
    eventSub   adapters.SubscriptionAdapter     // Event subscription
    commandBus *CommandBus                      // Dispatches commands
    registry   map[string]SagaFactory           // Creates saga instances
    handlers   map[string][]string              // Event -> Saga type mappings
}

// SagaManagerOption configures the manager
type SagaManagerOption func(*SagaManager)

// Create a new SagaManager
func NewSagaManager(store SagaStore, eventSub adapters.SubscriptionAdapter, 
    commandBus *CommandBus, opts ...SagaManagerOption) *SagaManager

// Register a saga type with the manager
func (m *SagaManager) Register(sagaType string, factory SagaFactory, correlations ...SagaCorrelation)

// Start begins listening for events
func (m *SagaManager) Start(ctx context.Context) error

// Stop gracefully shuts down the manager
func (m *SagaManager) Stop() error

// Compensate manually triggers compensation for a saga
func (m *SagaManager) Compensate(ctx context.Context, sagaID string, reason error) error

// Resume resumes a stalled saga
func (m *SagaManager) Resume(ctx context.Context, sagaID string) error
```

### Usage Example

```go
// Create saga manager
manager := mink.NewSagaManager(
    sagaStore,
    postgresAdapter,  // SubscriptionAdapter
    commandBus,
    mink.WithSagaRetries(3),
    mink.WithSagaTimeout(5 * time.Minute),
)

// Register saga types
manager.Register("OrderFulfillment", 
    func(id string) mink.Saga { return NewOrderFulfillmentSaga(id) },
    mink.SagaCorrelation{
        SagaType:       "OrderFulfillment",
        StartingEvents: []string{"OrderCreated"},
        CorrelationIDFunc: func(e mink.StoredEvent) string {
            return e.StreamID  // Correlate by order stream
        },
    },
)

// Start processing events
go manager.Start(ctx)
defer manager.Stop()
```

### Saga Store

```go
// PostgreSQL implementation
sagaStore := postgres.NewSagaStore(db,
    postgres.WithSagaSchema("myapp"),
    postgres.WithSagaTable("sagas"),
)

// Initialize schema
if err := sagaStore.Initialize(ctx); err != nil {
    log.Fatal(err)
}

// In-memory for testing
testStore := memory.NewSagaStore()
```

### PostgreSQL Schema

```sql
-- Saga state storage
CREATE TABLE mink_sagas (
    id VARCHAR(255) PRIMARY KEY,
    type VARCHAR(255) NOT NULL,
    correlation_id VARCHAR(255),
    status INT NOT NULL DEFAULT 0,
    current_step INT NOT NULL DEFAULT 0,
    data JSONB NOT NULL DEFAULT '{}',
    processed_events JSONB NOT NULL DEFAULT '[]',  -- For idempotency
    steps JSONB NOT NULL DEFAULT '[]',
    failure_reason TEXT,
    started_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMPTZ,
    version BIGINT NOT NULL DEFAULT 0
);

-- Indexes for efficient querying
CREATE INDEX idx_sagas_correlation ON mink_sagas(correlation_id);
CREATE INDEX idx_sagas_type_status ON mink_sagas(type, status);
CREATE INDEX idx_sagas_started ON mink_sagas(started_at);

-- Partial indexes for active sagas
CREATE INDEX idx_sagas_running ON mink_sagas(type) 
    WHERE status IN (0, 1, 4);  -- Started, Running, Compensating
```

### Testing Sagas

```go
import "github.com/AshkanYarmoradi/go-mink/testing/sagas"

func TestOrderFulfillmentSaga_HappyPath(t *testing.T) {
    saga := NewOrderFulfillmentSaga("saga-123")
    
    // Use the testing fixture
    adapter := sagas.NewMinkSagaAdapter(saga)
    
    sagas.TestSaga(t, adapter).
        GivenEvents(
            createOrderEvent("order-123", "customer-456"),
            paymentReceivedEvent("order-123"),
            inventoryReservedEvent("order-123"),
            shipmentCreatedEvent("order-123"),
        ).
        ThenCompleted().
        ThenCommands(
            RequestPayment{OrderID: "order-123"},
            ReserveInventory{OrderID: "order-123"},
            CreateShipment{OrderID: "order-123"},
            CompleteOrder{OrderID: "order-123"},
        )
}

func TestOrderFulfillmentSaga_Compensation(t *testing.T) {
    saga := NewOrderFulfillmentSaga("saga-123")
    adapter := sagas.NewMinkSagaAdapter(saga)
    
    sagas.TestCompensation(t, adapter).
        GivenFailureAfter(
            createOrderEvent("order-123", "customer-456"),
            paymentReceivedEvent("order-123"),
            inventoryReservationFailedEvent("order-123"),
        ).
        ThenCompensates(
            RefundPayment{OrderID: "order-123"},
            FailOrder{OrderID: "order-123"},
        )
}
```

---

## Outbox Pattern (v0.5.0) ✅

Reliable event publishing to external systems. The outbox pattern ensures messages are published atomically with events by writing them in the same database transaction, then delivering them asynchronously via a background processor.

### Core Types

```go
// OutboxMessage represents a message to be published
type OutboxMessage struct {
    ID            string
    AggregateID   string
    EventType     string
    Destination   string            // "webhook:https://example.com", "kafka:orders", "sns:arn:..."
    Payload       []byte
    Headers       map[string]string
    Status        OutboxStatus
    Attempts      int
    MaxAttempts   int
    LastError     string
    ScheduledAt   time.Time
    LastAttemptAt *time.Time
    ProcessedAt   *time.Time
    CreatedAt     time.Time
}

type OutboxStatus int
const (
    OutboxPending    OutboxStatus = iota // Waiting to be processed
    OutboxProcessing                     // Currently being delivered
    OutboxCompleted                      // Successfully delivered
    OutboxFailed                         // Delivery failed
    OutboxDeadLetter                     // Exceeded max retries
)
```

### OutboxStore Interface

```go
// OutboxStore defines the interface for outbox message persistence
type OutboxStore interface {
    Schedule(ctx context.Context, messages []*OutboxMessage) error
    ScheduleInTx(ctx context.Context, tx interface{}, messages []*OutboxMessage) error
    FetchPending(ctx context.Context, limit int) ([]*OutboxMessage, error)
    MarkCompleted(ctx context.Context, ids []string) error
    MarkFailed(ctx context.Context, id string, lastErr error) error
    RetryFailed(ctx context.Context, maxAttempts int) (int64, error)
    MoveToDeadLetter(ctx context.Context, maxAttempts int) (int64, error)
    GetDeadLetterMessages(ctx context.Context, limit int) ([]*OutboxMessage, error)
    Cleanup(ctx context.Context, olderThan time.Duration) (int64, error)
    Initialize(ctx context.Context) error
    Close() error
}
```

### EventStoreWithOutbox

Wraps an `EventStore` to automatically schedule outbox messages when events are appended. If the underlying adapter implements `OutboxAppender`, events and outbox messages are written atomically in the same transaction.

```go
import (
    "github.com/AshkanYarmoradi/go-mink"
    "github.com/AshkanYarmoradi/go-mink/adapters/memory"
)

// Create stores
adapter := memory.NewAdapter()
store := mink.New(adapter)
outboxStore := memory.NewOutboxStore()

// Define routes: which events go where
routes := []mink.OutboxRoute{
    {
        EventTypes:  []string{"OrderCreated", "OrderShipped"},
        Destination: "kafka:orders",
    },
    {
        EventTypes:  []string{"OrderShipped"},
        Destination: "webhook:https://partner.example.com/events",
    },
    {
        Destination: "sns:arn:aws:sns:us-east-1:123456:all-events", // All events
    },
}

// Create event store with outbox
esWithOutbox := mink.NewEventStoreWithOutbox(store, outboxStore, routes,
    mink.WithOutboxMaxAttempts(10),
)

// Append events - outbox messages are scheduled automatically
err := esWithOutbox.Append(ctx, "Order-123", []interface{}{
    OrderCreated{OrderID: "123", CustomerID: "cust-456"},
})

// Or save aggregates - same automatic outbox scheduling
err = esWithOutbox.SaveAggregate(ctx, order)
```

### OutboxRoute Configuration

```go
// Routes define which events get published where
type OutboxRoute struct {
    EventTypes  []string                           // Empty = all events
    Destination string                             // "prefix:target"
    Transform   func(event interface{}, stored StoredEvent) ([]byte, error)  // Optional payload transform
    Filter      func(event interface{}, stored StoredEvent) bool             // Optional filter
}
```

### Outbox Processor

Background worker that polls for pending messages and delivers them via registered publishers.

```go
import (
    "github.com/AshkanYarmoradi/go-mink"
    "github.com/AshkanYarmoradi/go-mink/outbox/webhook"
    "github.com/AshkanYarmoradi/go-mink/outbox/kafka"
)

// Create publishers
webhookPub := webhook.New(
    webhook.WithTimeout(10 * time.Second),
    webhook.WithDefaultHeaders(map[string]string{
        "Authorization": "Bearer token123",
    }),
)

kafkaPub := kafka.New(
    kafka.WithBrokers("localhost:9092"),
)

// Create and configure processor
processor := mink.NewOutboxProcessor(outboxStore,
    mink.WithPublisher(webhookPub),
    mink.WithPublisher(kafkaPub),
    mink.WithBatchSize(100),
    mink.WithPollInterval(1 * time.Second),
    mink.WithMaxRetries(5),
    mink.WithRetryBackoff(5 * time.Second),
    mink.WithCleanupInterval(1 * time.Hour),
    mink.WithCleanupAge(7 * 24 * time.Hour),
)

// Start processing
err := processor.Start(ctx)

// Graceful shutdown
stopCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
defer cancel()
processor.Stop(stopCtx)
```

### Built-in Publishers

**Webhook Publisher** - HTTP POST delivery:
```go
import "github.com/AshkanYarmoradi/go-mink/outbox/webhook"

pub := webhook.New(
    webhook.WithTimeout(10 * time.Second),
    webhook.WithDefaultHeaders(map[string]string{
        "X-API-Key": "secret",
    }),
)
// Destination format: "webhook:https://example.com/events"
```

**Kafka Publisher** - Apache Kafka delivery:
```go
import "github.com/AshkanYarmoradi/go-mink/outbox/kafka"

pub := kafka.New(
    kafka.WithBrokers("broker1:9092", "broker2:9092"),
    kafka.WithBatchTimeout(100 * time.Millisecond),
)
// Destination format: "kafka:topic-name"
```

**SNS Publisher** - AWS SNS delivery:
```go
import "github.com/AshkanYarmoradi/go-mink/outbox/sns"

pub := sns.New(
    sns.WithSNSClient(snsClient),
    sns.WithMessageGroupID("orders"),
)
// Destination format: "sns:arn:aws:sns:us-east-1:123456:topic"
```

### Outbox Stores

**PostgreSQL** (production):
```go
import "github.com/AshkanYarmoradi/go-mink/adapters/postgres"

outboxStore := postgres.NewOutboxStore(db,
    postgres.WithOutboxSchema("myapp"),
    postgres.WithOutboxTableName("outbox_messages"),
)
if err := outboxStore.Initialize(ctx); err != nil {
    log.Fatal(err)
}
```

The PostgreSQL implementation uses `SELECT ... FOR UPDATE SKIP LOCKED` for concurrent-safe message claiming.

**In-Memory** (testing):
```go
import "github.com/AshkanYarmoradi/go-mink/adapters/memory"

outboxStore := memory.NewOutboxStore()
```

### Atomic Append with Outbox (PostgreSQL)

The PostgreSQL adapter implements `OutboxAppender` for atomic event+outbox writes:

```go
// When using PostgreSQL, events and outbox messages are written
// in the same database transaction automatically.
// No messages are published if the event append fails, and vice versa.

// The EventStoreWithOutbox detects this automatically:
// - PostgreSQL adapter: atomic (same transaction)
// - Memory adapter: non-atomic fallback (separate calls, logged warning)
```

### Metrics Integration

```go
import "github.com/AshkanYarmoradi/go-mink/middleware/metrics"

m := metrics.New(metrics.WithMetricsServiceName("order-service"))

processor := mink.NewOutboxProcessor(outboxStore,
    mink.WithOutboxMetrics(m),
    // ...
)

// Available metrics:
// - outbox_messages_processed_total (labels: destination, status)
// - outbox_messages_failed_total (labels: destination)
// - outbox_messages_dead_lettered_total
// - outbox_batch_duration_seconds
// - outbox_pending_messages (gauge)
```

### Dead Letter Handling

Messages that exceed `MaxRetries` are moved to the dead letter queue during the processor's maintenance cycle.

```go
// Retrieve dead-lettered messages for manual inspection
deadLetters, err := outboxStore.GetDeadLetterMessages(ctx, 100)
for _, msg := range deadLetters {
    log.Printf("Dead letter: %s (destination: %s, error: %s)",
        msg.ID, msg.Destination, msg.LastError)
}
```

### PostgreSQL Schema

```sql
CREATE TABLE IF NOT EXISTS mink_outbox (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    aggregate_id VARCHAR(255) NOT NULL,
    event_type VARCHAR(255) NOT NULL,
    destination VARCHAR(255) NOT NULL,
    payload JSONB NOT NULL,
    headers JSONB DEFAULT '{}',
    status INT NOT NULL DEFAULT 0,
    attempts INT NOT NULL DEFAULT 0,
    max_attempts INT NOT NULL DEFAULT 5,
    last_error TEXT,
    scheduled_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_attempt_at TIMESTAMPTZ,
    processed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Partial index for efficient pending message polling
CREATE INDEX idx_mink_outbox_pending ON mink_outbox (scheduled_at) WHERE status = 0;

-- Partial index for dead letter inspection
CREATE INDEX idx_mink_outbox_dead_letter ON mink_outbox (created_at) WHERE status = 4;
```

---

Next: [Testing →](testing)
