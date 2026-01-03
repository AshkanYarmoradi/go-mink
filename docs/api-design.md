---
layout: default
title: API Design
nav_order: 7
permalink: /docs/api-design
---

# API Design
{: .no_toc }

## Table of contents
{: .no_toc .text-delta }

1. TOC
{:toc}

---

## Core Package (`mink`)

### Event Store

```go
package go-mink

// EventStore is the main entry point
type EventStore struct {
    adapter     EventStoreAdapter
    serializer  Serializer
    projections *ProjectionEngine
    middleware  []Middleware
}

// New creates a new event store
func New(adapter EventStoreAdapter, opts ...Option) *EventStore

// Options
func WithSerializer(s Serializer) Option
func WithMiddleware(m ...Middleware) Option
func WithSnapshots(adapter SnapshotAdapter, policy SnapshotPolicy) Option
func WithProjections(engine *ProjectionEngine) Option
func WithLogger(logger Logger) Option
func WithMetrics(provider MetricsProvider) Option

// Core operations
func (s *EventStore) Append(ctx context.Context, streamID string, 
    events []interface{}, opts ...AppendOption) error

func (s *EventStore) Load(ctx context.Context, streamID string) ([]Event, error)

func (s *EventStore) LoadAggregate(ctx context.Context, agg Aggregate) error

func (s *EventStore) SaveAggregate(ctx context.Context, agg Aggregate) error

// Append options
func ExpectVersion(v int64) AppendOption
func WithMetadata(m Metadata) AppendOption
```

### Aggregates

```go
// Aggregate interface for domain objects
type Aggregate interface {
    AggregateID() string
    AggregateType() string
    Version() int64
    ApplyEvent(event interface{}) error
    UncommittedEvents() []interface{}
    ClearUncommittedEvents()
}

// AggregateBase provides default implementation
type AggregateBase struct {
    id               string
    version          int64
    uncommittedEvents []interface{}
}

func (a *AggregateBase) AggregateID() string { return a.id }
func (a *AggregateBase) SetID(id string)     { a.id = id }
func (a *AggregateBase) Version() int64      { return a.version }
func (a *AggregateBase) SetVersion(v int64)  { a.version = v }

func (a *AggregateBase) Apply(event interface{}) {
    a.uncommittedEvents = append(a.uncommittedEvents, event)
}

func (a *AggregateBase) UncommittedEvents() []interface{} {
    return a.uncommittedEvents
}

func (a *AggregateBase) ClearUncommittedEvents() {
    a.uncommittedEvents = nil
}
```

### Commands (v0.2.0)

```go
// Command represents intent to change state
type Command interface {
    CommandType() string
    Validate() error
}

// CommandBase provides common command functionality
type CommandBase struct {
    id            string
    correlationID string
    causationID   string
    tenantID      string
    metadata      map[string]string
}

// Accessors for command metadata
func (c *CommandBase) GetID() string
func (c *CommandBase) SetID(id string)
func (c *CommandBase) GetCorrelationID() string
func (c *CommandBase) SetCorrelationID(id string)
func (c *CommandBase) GetCausationID() string
func (c *CommandBase) SetCausationID(id string)
func (c *CommandBase) GetTenantID() string
func (c *CommandBase) SetTenantID(id string)
func (c *CommandBase) GetMetadata() map[string]string
func (c *CommandBase) SetMetadata(m map[string]string)

// CommandResult contains the result of command execution
type CommandResult struct {
    AggregateID string
    Version     int64
    Events      []interface{}
    Data        interface{}
}

func NewSuccessResult(aggregateID string, version int64) CommandResult

// IdempotentCommand provides custom idempotency keys
type IdempotentCommand interface {
    Command
    IdempotencyKey() string
}
```

### Command Bus (v0.2.0)

```go
// CommandBus routes commands to handlers
type CommandBus struct {
    handlers   map[string]CommandHandler
    middleware []CommandMiddleware
}

func NewCommandBus() *CommandBus

// Registration
func (b *CommandBus) Register(handler CommandHandler)
func (b *CommandBus) RegisterFunc(cmdType string, fn CommandHandlerFunc)

// Middleware
func (b *CommandBus) Use(middleware ...CommandMiddleware)

// Dispatch
func (b *CommandBus) Dispatch(ctx context.Context, cmd Command) (CommandResult, error)

// CommandHandler interface
type CommandHandler interface {
    CommandType() string
    Handle(ctx context.Context, cmd Command) (CommandResult, error)
}

// CommandHandlerFunc for function-based handlers
type CommandHandlerFunc func(ctx context.Context, cmd Command) (CommandResult, error)

// CommandMiddleware wraps command handling
type CommandMiddleware func(CommandHandlerFunc) CommandHandlerFunc
```

### Generic Handler (v0.2.0)

```go
// GenericHandler provides type-safe command handling
type GenericHandler[T Command] struct {
    fn func(ctx context.Context, cmd T) (CommandResult, error)
}

func NewGenericHandler[T Command](
    fn func(ctx context.Context, cmd T) (CommandResult, error),
) *GenericHandler[T]

func (h *GenericHandler[T]) CommandType() string
func (h *GenericHandler[T]) Handle(ctx context.Context, cmd Command) (CommandResult, error)
```

### Aggregate Handler (v0.2.0)

```go
// AggregateHandler combines load/save with command handling
type AggregateHandler[C Command, A Aggregate] struct {
    store      AggregateStore
    idFunc     func(C) string
    factory    func() A
    handleFunc func(ctx context.Context, agg A, cmd C) error
}

func NewAggregateHandler[C Command, A Aggregate](
    store AggregateStore,
    idFunc func(C) string,
    factory func() A,
    handleFunc func(ctx context.Context, agg A, cmd C) error,
) *AggregateHandler[C, A]
```

### Built-in Middleware (v0.2.0)

```go
// Validation middleware - calls cmd.Validate()
func ValidationMiddleware() CommandMiddleware

// Recovery middleware - catches panics
func RecoveryMiddleware() CommandMiddleware

// Logging middleware - logs command execution
func LoggingMiddleware(logger Logger) CommandMiddleware

// Metrics middleware - records command metrics
func MetricsMiddleware(metrics MetricsRecorder) CommandMiddleware

// Timeout middleware - adds context timeout
func TimeoutMiddleware(timeout time.Duration) CommandMiddleware

// Retry middleware - retries on transient failures
func RetryMiddleware(maxAttempts int, initialDelay time.Duration) CommandMiddleware

// Correlation ID middleware - sets/generates correlation ID
func CorrelationIDMiddleware(generator func() string) CommandMiddleware

// Causation ID middleware - tracks causation chain
func CausationIDMiddleware() CommandMiddleware

// Tenant middleware - sets tenant ID from context
func TenantMiddleware(resolver func(context.Context) string) CommandMiddleware

// Idempotency middleware - prevents duplicate processing
func IdempotencyMiddleware(config IdempotencyConfig) CommandMiddleware
```

### Idempotency Store (v0.2.0)

```go
// IdempotencyStore tracks processed commands
type IdempotencyStore interface {
    Store(ctx context.Context, key string, response []byte, expiration time.Duration) error
    Get(ctx context.Context, key string) ([]byte, error)
    Close() error
}

// IdempotencyConfig configures idempotency middleware
type IdempotencyConfig struct {
    Store      IdempotencyStore
    KeyFunc    func(Command) string
    TTL        time.Duration
    Serializer func(CommandResult) []byte
}

func DefaultIdempotencyConfig(store IdempotencyStore) IdempotencyConfig
func GenerateIdempotencyKey(cmd Command) string
```

### Events

```go
// Event represents a stored event
type Event struct {
    ID             string
    StreamID       string
    Type           string
    Data           interface{}
    Metadata       Metadata
    Version        int64
    GlobalPosition uint64
    Timestamp      time.Time
}

// Metadata carries event context
type Metadata struct {
    CorrelationID string
    CausationID   string
    UserID        string
    TenantID      string
    Custom        map[string]string
}

// EventRegistry maps type names to Go types
type EventRegistry struct {
    types map[string]reflect.Type
}

func NewEventRegistry() *EventRegistry

func (r *EventRegistry) Register(eventType string, example interface{})
func (r *EventRegistry) RegisterAll(events ...interface{}) // Uses struct name
func (r *EventRegistry) Lookup(eventType string) (reflect.Type, bool)
```

### Projections

```go
// Projection transforms events to read models
type Projection interface {
    Name() string
    HandledEvents() []string
}

// InlineProjection runs in same transaction
type InlineProjection interface {
    Projection
    Apply(ctx context.Context, tx Transaction, event Event) error
}

// AsyncProjection runs in background
type AsyncProjection interface {
    Projection
    Apply(ctx context.Context, events []Event) error
    Checkpoint() uint64
    SetCheckpoint(pos uint64) error
}

// ProjectionEngine manages all projections
type ProjectionEngine struct {
    inline []InlineProjection
    async  []asyncRunner
    store  EventStore
}

func NewProjectionEngine(store *EventStore) *ProjectionEngine

func (e *ProjectionEngine) RegisterInline(p InlineProjection)
func (e *ProjectionEngine) RegisterAsync(p AsyncProjection, opts AsyncOptions)
func (e *ProjectionEngine) Start(ctx context.Context) error
func (e *ProjectionEngine) Stop() error
func (e *ProjectionEngine) Rebuild(ctx context.Context, name string) error

// AsyncOptions configures async projection processing
type AsyncOptions struct {
    BatchSize    int
    BatchTimeout time.Duration
    Workers      int
    RetryPolicy  RetryPolicy
}
```

### Subscriptions

```go
// Subscribe to event streams
type Subscription interface {
    Events() <-chan Event
    Errors() <-chan error
    Close() error
}

func (s *EventStore) SubscribeAll(ctx context.Context, 
    fromPosition uint64) (Subscription, error)

func (s *EventStore) SubscribeStream(ctx context.Context, 
    streamID string, fromVersion int64) (Subscription, error)

func (s *EventStore) SubscribeCategory(ctx context.Context, 
    category string, fromPosition uint64) (Subscription, error)

// Example usage
sub, _ := store.SubscribeAll(ctx, 0)
defer sub.Close()

for {
    select {
    case event := <-sub.Events():
        fmt.Printf("Event: %s\n", event.Type)
    case err := <-sub.Errors():
        log.Printf("Error: %v\n", err)
    case <-ctx.Done():
        return
    }
}
```

### Read Model Repository

```go
// Generic repository for read models
type Repository[T any] interface {
    Get(ctx context.Context, id string) (*T, error)
    GetAll(ctx context.Context) ([]*T, error)
    Find(ctx context.Context, query Query) ([]*T, error)
    FindOne(ctx context.Context, query Query) (*T, error)
    Save(ctx context.Context, model *T) error
    Delete(ctx context.Context, id string) error
    Count(ctx context.Context, query Query) (int64, error)
}

// Query builder
type Query struct {
    filters  []Filter
    orderBy  []OrderBy
    limit    int
    offset   int
}

func NewQuery() *Query
func (q *Query) Where(field string, op string, value interface{}) *Query
func (q *Query) And(field string, op string, value interface{}) *Query
func (q *Query) Or(field string, op string, value interface{}) *Query
func (q *Query) OrderBy(field string, desc bool) *Query
func (q *Query) Limit(n int) *Query
func (q *Query) Offset(n int) *Query

// Usage
orders, _ := repo.Find(ctx, 
    go-mink.NewQuery().
        Where("status", "=", "pending").
        And("total", ">", 100).
        OrderBy("created_at", true).
        Limit(10),
)
```

### Middleware

```go
// Middleware wraps event store operations
type Middleware func(next Handler) Handler

type Handler func(ctx context.Context, cmd Command) error

// Built-in middleware
func LoggingMiddleware(logger Logger) Middleware
func MetricsMiddleware(provider MetricsProvider) Middleware
func TracingMiddleware(tracer Tracer) Middleware
func RetryMiddleware(policy RetryPolicy) Middleware
func ValidationMiddleware() Middleware

// Usage
store := go-mink.New(adapter,
    go-mink.WithMiddleware(
        go-mink.LoggingMiddleware(logger),
        go-mink.MetricsMiddleware(prometheus.NewProvider()),
        go-mink.TracingMiddleware(otel.Tracer("go-mink")),
    ),
)
```

### Multi-tenancy

```go
// TenantContext adds tenant awareness
type TenantContext struct {
    TenantID string
    store    *EventStore
}

func (s *EventStore) ForTenant(tenantID string) *TenantContext

// Tenant isolation strategies
type TenantStrategy int

const (
    // Shared tables with tenant_id column
    SharedTable TenantStrategy = iota
    
    // Separate schema per tenant
    SchemaPerTenant
    
    // Separate database per tenant  
    DatabasePerTenant
)

// Configuration
config := go-mink.Config{
    MultiTenancy: go-mink.MultiTenancyConfig{
        Strategy: go-mink.SchemaPerTenant,
        SchemaPrefix: "tenant_",
    },
}

// Usage
tenantStore := store.ForTenant("acme-corp")
tenantStore.Append(ctx, "order-123", events)
```

### Errors

```go
// Sentinel errors
var (
    ErrStreamNotFound      = errors.New("stream not found")
    ErrConcurrencyConflict = errors.New("concurrency conflict")
    ErrEventNotFound       = errors.New("event not found")
    ErrProjectionNotFound  = errors.New("projection not found")
    ErrSerializationFailed = errors.New("serialization failed")
    ErrAdapterNotSupported = errors.New("adapter not supported")
)

// Typed errors with details
type ConcurrencyError struct {
    StreamID        string
    ExpectedVersion int64
    ActualVersion   int64
}

func (e *ConcurrencyError) Error() string {
    return fmt.Sprintf("concurrency conflict on stream %s: expected %d, got %d",
        e.StreamID, e.ExpectedVersion, e.ActualVersion)
}

// Error checking
if errors.Is(err, go-mink.ErrConcurrencyConflict) {
    // Handle conflict
}

var concErr *go-mink.ConcurrencyError
if errors.As(err, &concErr) {
    // Access error details
    fmt.Printf("Stream: %s\n", concErr.StreamID)
}
```

### Testing Utilities (v0.4.0)

```go
import (
    "github.com/AshkanYarmoradi/go-mink/testing/bdd"
    "github.com/AshkanYarmoradi/go-mink/testing/assertions"
    "github.com/AshkanYarmoradi/go-mink/testing/projections"
    "github.com/AshkanYarmoradi/go-mink/testing/sagas"
    "github.com/AshkanYarmoradi/go-mink/testing/containers"
)

// BDD-style aggregate testing
func TestOrderAggregate(t *testing.T) {
    order := NewOrder("order-123")

    bdd.Given(t, order).
        When(func() error {
            return order.Create("customer-456")
        }).
        Then(OrderCreated{OrderID: "order-123", CustomerID: "customer-456"})
}

// Event assertions
assertions.AssertEventTypes(t, events, "OrderCreated", "ItemAdded")
assertions.AssertEventsEqual(t, expected, actual)
assertions.AssertContainsEvent(t, events, OrderCreated{OrderID: "123"})

// Event diffing
diffs := assertions.DiffEvents(expected, actual)
t.Error(assertions.FormatDiffs(diffs))

// Projection testing
projections.TestProjection[OrderSummary](t, projection).
    GivenEvents(storedEvents...).
    ThenReadModel("order-123", expectedModel)

// Saga testing
sagas.TestSaga(t, saga).
    GivenEvents(events...).
    ThenCommands(expectedCommands...).
    ThenCompleted()

// PostgreSQL test containers
container := containers.StartPostgres(t)
db := container.MustDB(ctx)
```

---

Next: [Advanced Patterns →](advanced-patterns)
