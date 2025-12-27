---
layout: default
title: Advanced Patterns
nav_order: 10
permalink: /docs/advanced-patterns
---

# Advanced Patterns
{: .no_toc }

## Table of contents
{: .no_toc .text-delta }

1. TOC
{:toc}

---

## Command Bus & CQRS

Complete the CQRS pattern with first-class Command support.

### Command Definition

```go
// Command represents intent to change state
type Command interface {
    CommandType() string
    AggregateID() string
    Validate() error
}

// CommandBase provides common functionality
type CommandBase struct {
    ID            string            `json:"id"`
    CorrelationID string            `json:"correlationId"`
    Metadata      map[string]string `json:"metadata"`
}

// Example command with validation
type CreateOrder struct {
    CommandBase
    CustomerID string `json:"customerId" validate:"required,uuid"`
    Items      []Item `json:"items" validate:"required,min=1,dive"`
}

func (c CreateOrder) CommandType() string  { return "CreateOrder" }
func (c CreateOrder) AggregateID() string  { return "" } // New aggregate
func (c CreateOrder) Validate() error {
    return validator.Validate(c)
}
```

### Command Handler

```go
// CommandHandler processes a specific command type
type CommandHandler[C Command] interface {
    Handle(ctx context.Context, cmd C) error
}

// Example handler
type CreateOrderHandler struct {
    store *mink.EventStore
}

func (h *CreateOrderHandler) Handle(ctx context.Context, cmd CreateOrder) error {
    // Validate
    if err := cmd.Validate(); err != nil {
        return fmt.Errorf("validation failed: %w", err)
    }
    
    // Create aggregate and apply command
    order := NewOrder(uuid.NewString())
    if err := order.Create(cmd.CustomerID, cmd.Items); err != nil {
        return err
    }
    
    // Persist
    return h.store.SaveAggregate(ctx, order)
}
```

### Command Bus

```go
// CommandBus routes commands to handlers
type CommandBus struct {
    handlers   map[string]interface{}
    middleware []CommandMiddleware
}

func NewCommandBus() *CommandBus

// Register handler for command type
func (b *CommandBus) Register(cmdType string, handler interface{})

// RegisterHandler with type safety using generics
func Register[C Command](bus *CommandBus, handler CommandHandler[C])

// Dispatch command to appropriate handler
func (b *CommandBus) Dispatch(ctx context.Context, cmd Command) error {
    // Run middleware chain
    handler := b.handlers[cmd.CommandType()]
    if handler == nil {
        return ErrHandlerNotFound
    }
    
    // Execute with middleware
    return b.executeWithMiddleware(ctx, cmd, handler)
}

// Usage
bus := mink.NewCommandBus()
Register(bus, &CreateOrderHandler{store: store})
Register(bus, &AddItemHandler{store: store})

err := bus.Dispatch(ctx, CreateOrder{
    CustomerID: "cust-123",
    Items:      []Item{ {SKU: "WIDGET-01", Qty: 2} },
})
```

### Command Middleware

```go
// CommandMiddleware wraps command handling
type CommandMiddleware func(CommandHandlerFunc) CommandHandlerFunc
type CommandHandlerFunc func(ctx context.Context, cmd Command) error

// Logging middleware
func LoggingMiddleware(logger Logger) CommandMiddleware {
    return func(next CommandHandlerFunc) CommandHandlerFunc {
        return func(ctx context.Context, cmd Command) error {
            logger.Info("Handling command",
                "type", cmd.CommandType(),
                "aggregateId", cmd.AggregateID())
            
            start := time.Now()
            err := next(ctx, cmd)
            
            logger.Info("Command completed",
                "type", cmd.CommandType(),
                "duration", time.Since(start),
                "error", err)
            return err
        }
    }
}

// Validation middleware
func ValidationMiddleware() CommandMiddleware {
    return func(next CommandHandlerFunc) CommandHandlerFunc {
        return func(ctx context.Context, cmd Command) error {
            if err := cmd.Validate(); err != nil {
                return &ValidationError{Command: cmd.CommandType(), Err: err}
            }
            return next(ctx, cmd)
        }
    }
}

// Transaction middleware
func TransactionMiddleware(db *sql.DB) CommandMiddleware {
    return func(next CommandHandlerFunc) CommandHandlerFunc {
        return func(ctx context.Context, cmd Command) error {
            tx, _ := db.BeginTx(ctx, nil)
            ctx = context.WithValue(ctx, txKey, tx)
            
            err := next(ctx, cmd)
            if err != nil {
                tx.Rollback()
                return err
            }
            return tx.Commit()
        }
    }
}
```

---

## Idempotency

Prevent duplicate command processing - essential for reliability.

### Idempotency Store

```go
// IdempotencyStore tracks processed commands
type IdempotencyStore interface {
    // Check if command was already processed
    Exists(ctx context.Context, key string) (bool, error)
    
    // Store result of command processing
    Store(ctx context.Context, key string, result *IdempotencyRecord) error
    
    // Get previous result
    Get(ctx context.Context, key string) (*IdempotencyRecord, error)
    
    // Cleanup expired entries
    Cleanup(ctx context.Context, olderThan time.Duration) error
}

type IdempotencyRecord struct {
    Key         string
    CommandType string
    Result      []byte // Serialized response
    Error       string
    ProcessedAt time.Time
    ExpiresAt   time.Time
}
```

### Idempotency Key Generation

```go
// IdempotentCommand provides idempotency key
type IdempotentCommand interface {
    Command
    IdempotencyKey() string
}

// Auto-generate from command content
func GenerateIdempotencyKey(cmd Command) string {
    data, _ := json.Marshal(cmd)
    hash := sha256.Sum256(data)
    return fmt.Sprintf("%s:%s", cmd.CommandType(), hex.EncodeToString(hash[:16]))
}

// Or use explicit key
type CreateOrder struct {
    CommandBase
    IdempotencyID string `json:"idempotencyId"` // Client-provided
}

func (c CreateOrder) IdempotencyKey() string {
    if c.IdempotencyID != "" {
        return c.IdempotencyID
    }
    return GenerateIdempotencyKey(c)
}
```

### Idempotency Middleware

```go
// IdempotencyMiddleware prevents duplicate processing
func IdempotencyMiddleware(store IdempotencyStore, ttl time.Duration) CommandMiddleware {
    return func(next CommandHandlerFunc) CommandHandlerFunc {
        return func(ctx context.Context, cmd Command) error {
            // Get idempotency key
            var key string
            if ic, ok := cmd.(IdempotentCommand); ok {
                key = ic.IdempotencyKey()
            } else {
                key = GenerateIdempotencyKey(cmd)
            }
            
            // Check if already processed
            if record, _ := store.Get(ctx, key); record != nil {
                if record.Error != "" {
                    return errors.New(record.Error)
                }
                return nil // Already succeeded
            }
            
            // Process command
            err := next(ctx, cmd)
            
            // Store result
            record := &IdempotencyRecord{
                Key:         key,
                CommandType: cmd.CommandType(),
                ProcessedAt: time.Now(),
                ExpiresAt:   time.Now().Add(ttl),
            }
            if err != nil {
                record.Error = err.Error()
            }
            store.Store(ctx, key, record)
            
            return err
        }
    }
}
```

### PostgreSQL Implementation

```sql
CREATE TABLE mink_idempotency (
    key VARCHAR(255) PRIMARY KEY,
    command_type VARCHAR(255) NOT NULL,
    result JSONB,
    error TEXT,
    processed_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX idx_idempotency_expires ON mink_idempotency(expires_at);

-- Cleanup job (run periodically)
DELETE FROM mink_idempotency WHERE expires_at < NOW();
```

---

## Saga / Process Manager

Orchestrate long-running business processes across aggregates.

### Saga Definition

```go
// Saga coordinates multi-step distributed transactions
type Saga interface {
    // Unique identifier
    SagaID() string
    
    // Saga type for routing
    SagaType() string
    
    // Events this saga reacts to
    HandledEvents() []string
    
    // Process event and return commands to execute
    HandleEvent(ctx context.Context, event Event) ([]Command, error)
    
    // Compensate on failure
    Compensate(ctx context.Context, failedStep int, err error) ([]Command, error)
    
    // Check if saga is complete
    IsComplete() bool
}

// SagaState tracks progress
type SagaState struct {
    ID            string
    Type          string
    CurrentStep   int
    Status        SagaStatus
    Data          map[string]interface{}
    CompletedSteps []SagaStep
    StartedAt     time.Time
    CompletedAt   *time.Time
}

type SagaStatus int
const (
    SagaRunning SagaStatus = iota
    SagaCompleted
    SagaFailed
    SagaCompensating
    SagaCompensated
)
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

```go
// SagaManager orchestrates saga lifecycle
type SagaManager struct {
    store      SagaStore
    eventStore *EventStore
    commandBus *CommandBus
    registry   map[string]SagaFactory
}

func NewSagaManager(eventStore *EventStore, commandBus *CommandBus) *SagaManager

// Register saga type
func (m *SagaManager) Register(sagaType string, factory SagaFactory)

// Start processes events and routes to sagas
func (m *SagaManager) Start(ctx context.Context) error {
    sub, _ := m.eventStore.SubscribeAll(ctx, 0)
    
    for event := range sub.Events() {
        // Find or create saga for this event
        saga, _ := m.findOrCreateSaga(ctx, event)
        if saga == nil {
            continue
        }
        
        // Handle event
        commands, err := saga.HandleEvent(ctx, event)
        if err != nil {
            // Trigger compensation
            compensateCommands, _ := saga.Compensate(ctx, saga.CurrentStep(), err)
            for _, cmd := range compensateCommands {
                m.commandBus.Dispatch(ctx, cmd)
            }
            continue
        }
        
        // Execute resulting commands
        for _, cmd := range commands {
            if err := m.commandBus.Dispatch(ctx, cmd); err != nil {
                // Handle command failure
            }
        }
        
        // Persist saga state
        m.store.Save(ctx, saga)
    }
    return nil
}
```

### Saga Store

```go
// SagaStore persists saga state
type SagaStore interface {
    Save(ctx context.Context, saga Saga) error
    Load(ctx context.Context, sagaID string) (Saga, error)
    FindByCorrelation(ctx context.Context, correlationKey string) (Saga, error)
}

// PostgreSQL schema
/*
CREATE TABLE mink_sagas (
    id VARCHAR(255) PRIMARY KEY,
    type VARCHAR(255) NOT NULL,
    correlation_key VARCHAR(255),
    status INT NOT NULL,
    current_step INT NOT NULL,
    data JSONB NOT NULL,
    started_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    completed_at TIMESTAMPTZ
);

CREATE INDEX idx_sagas_correlation ON mink_sagas(correlation_key);
CREATE INDEX idx_sagas_status ON mink_sagas(status);
*/
```

---

## Outbox Pattern

Reliable event publishing to external systems.

### Outbox Design

```go
// OutboxMessage represents a message to be published
type OutboxMessage struct {
    ID           string
    AggregateID  string
    EventType    string
    Destination  string            // "kafka:orders-topic", "webhook:partner"
    Payload      []byte
    Headers      map[string]string
    ScheduledAt  time.Time
    Attempts     int
    LastError    string
    Status       OutboxStatus
}

type OutboxStatus int
const (
    OutboxPending OutboxStatus = iota
    OutboxProcessing
    OutboxCompleted
    OutboxFailed
)

// Outbox interface
type Outbox interface {
    // Schedule message in same transaction as events
    Schedule(ctx context.Context, tx Transaction, messages []OutboxMessage) error
    
    // Fetch pending messages for processing
    FetchPending(ctx context.Context, limit int) ([]OutboxMessage, error)
    
    // Mark messages as processed
    MarkCompleted(ctx context.Context, ids []string) error
    
    // Mark as failed with error
    MarkFailed(ctx context.Context, id string, err error) error
    
    // Retry failed messages
    RetryFailed(ctx context.Context, maxAttempts int) error
}
```

### Outbox Processor

```go
// OutboxProcessor publishes messages to external systems
type OutboxProcessor struct {
    outbox     Outbox
    publishers map[string]Publisher
    options    ProcessorOptions
}

type ProcessorOptions struct {
    BatchSize     int
    PollInterval  time.Duration
    MaxRetries    int
    RetryBackoff  time.Duration
}

// Publisher sends messages to specific destination
type Publisher interface {
    Publish(ctx context.Context, messages []OutboxMessage) error
    Destination() string // "kafka", "webhook", "sns"
}

// Built-in publishers
func NewKafkaPublisher(brokers []string, opts ...KafkaOption) Publisher
func NewWebhookPublisher(httpClient *http.Client) Publisher
func NewSNSPublisher(snsClient *sns.Client) Publisher

// Processor loop
func (p *OutboxProcessor) Start(ctx context.Context) error {
    ticker := time.NewTicker(p.options.PollInterval)
    defer ticker.Stop()
    
    for {
        select {
        case <-ctx.Done():
            return ctx.Err()
        case <-ticker.C:
            p.processBatch(ctx)
        }
    }
}

func (p *OutboxProcessor) processBatch(ctx context.Context) error {
    messages, _ := p.outbox.FetchPending(ctx, p.options.BatchSize)
    
    // Group by destination
    byDest := make(map[string][]OutboxMessage)
    for _, msg := range messages {
        dest := strings.Split(msg.Destination, ":")[0]
        byDest[dest] = append(byDest[dest], msg)
    }
    
    // Publish to each destination
    for dest, msgs := range byDest {
        publisher := p.publishers[dest]
        if err := publisher.Publish(ctx, msgs); err != nil {
            for _, msg := range msgs {
                p.outbox.MarkFailed(ctx, msg.ID, err)
            }
            continue
        }
        
        ids := make([]string, len(msgs))
        for i, msg := range msgs {
            ids[i] = msg.ID
        }
        p.outbox.MarkCompleted(ctx, ids)
    }
    
    return nil
}
```

### Integration with Event Store

```go
// Event store with outbox support
type EventStoreWithOutbox struct {
    *EventStore
    outbox Outbox
    routes []OutboxRoute
}

type OutboxRoute struct {
    EventTypes  []string
    Destination string
    Transform   func(Event) ([]byte, error)
}

// Append with automatic outbox scheduling
func (s *EventStoreWithOutbox) Append(ctx context.Context, streamID string, 
    events []interface{}, opts ...AppendOption) error {
    
    return s.adapter.WithTransaction(ctx, func(tx Transaction) error {
        // Store events
        stored, err := s.appendInTx(ctx, tx, streamID, events, opts...)
        if err != nil {
            return err
        }
        
        // Schedule outbox messages
        var messages []OutboxMessage
        for _, event := range stored {
            for _, route := range s.routes {
                if !contains(route.EventTypes, event.Type) {
                    continue
                }
                payload, _ := route.Transform(event)
                messages = append(messages, OutboxMessage{
                    ID:          uuid.NewString(),
                    AggregateID: streamID,
                    EventType:   event.Type,
                    Destination: route.Destination,
                    Payload:     payload,
                    ScheduledAt: time.Now(),
                })
            }
        }
        
        return s.outbox.Schedule(ctx, tx, messages)
    })
}

// Configuration
store := mink.NewEventStoreWithOutbox(adapter, outbox,
    mink.OutboxRoute{
        EventTypes:  []string{"OrderCreated", "OrderShipped"},
        Destination: "kafka:orders",
        Transform:   mink.JSONTransform,
    },
    mink.OutboxRoute{
        EventTypes:  []string{"OrderShipped"},
        Destination: "webhook:shipping-partner",
        Transform:   shippingWebhookTransform,
    },
)
```

---

Next: [Security & Compliance →](security.md)
