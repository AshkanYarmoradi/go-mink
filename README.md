# go-mink 🦫

**A Comprehensive Event Sourcing & CQRS Toolkit for Go**

<p align="center">
  <a href="https://pkg.go.dev/github.com/AshkanYarmoradi/go-mink"><img src="https://pkg.go.dev/badge/github.com/AshkanYarmoradi/go-mink.svg" alt="Go Reference"></a>
  <a href="https://goreportcard.com/report/github.com/AshkanYarmoradi/go-mink"><img src="https://goreportcard.com/badge/github.com/AshkanYarmoradi/go-mink" alt="Go Report Card"></a>
  <a href="https://github.com/AshkanYarmoradi/go-mink/actions/workflows/test.yml"><img src="https://github.com/AshkanYarmoradi/go-mink/actions/workflows/test.yml/badge.svg" alt="Build Status"></a>
  <a href="https://codecov.io/gh/AshkanYarmoradi/go-mink"><img src="https://codecov.io/gh/AshkanYarmoradi/go-mink/graph/badge.svg?token=ZCB3IDSI2Q" alt="codecov"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-Apache%202.0-blue.svg" alt="License"></a>
  <a href="https://go.dev/"><img src="https://img.shields.io/badge/Go-1.22+-00ADD8?style=flat&logo=go" alt="Go Version"></a>
</p>

<p align="center">
  <a href="https://sonarcloud.io/summary/new_code?id=AshkanYarmoradi_go-mink"><img src="https://sonarcloud.io/api/project_badges/measure?project=AshkanYarmoradi_go-mink&metric=alert_status" alt="Quality Gate Status"></a>
  <a href="https://sonarcloud.io/summary/new_code?id=AshkanYarmoradi_go-mink"><img src="https://sonarcloud.io/api/project_badges/measure?project=AshkanYarmoradi_go-mink&metric=reliability_rating" alt="Reliability Rating"></a>
  <a href="https://sonarcloud.io/summary/new_code?id=AshkanYarmoradi_go-mink"><img src="https://sonarcloud.io/api/project_badges/measure?project=AshkanYarmoradi_go-mink&metric=security_rating" alt="Security Rating"></a>
  <a href="https://sonarcloud.io/summary/new_code?id=AshkanYarmoradi_go-mink"><img src="https://sonarcloud.io/api/project_badges/measure?project=AshkanYarmoradi_go-mink&metric=sqale_rating" alt="Maintainability Rating"></a>
</p>

<p align="center">
  <a href="https://sonarcloud.io/summary/new_code?id=AshkanYarmoradi_go-mink"><img src="https://sonarcloud.io/api/project_badges/measure?project=AshkanYarmoradi_go-mink&metric=bugs" alt="Bugs"></a>
  <a href="https://sonarcloud.io/summary/new_code?id=AshkanYarmoradi_go-mink"><img src="https://sonarcloud.io/api/project_badges/measure?project=AshkanYarmoradi_go-mink&metric=vulnerabilities" alt="Vulnerabilities"></a>
  <a href="https://sonarcloud.io/summary/new_code?id=AshkanYarmoradi_go-mink"><img src="https://sonarcloud.io/api/project_badges/measure?project=AshkanYarmoradi_go-mink&metric=sqale_index" alt="Technical Debt"></a>
</p>

---

## v1.0.0 — Stable Release

go-mink includes everything you need to build production event-sourced systems in Go:

- **Event Store** with optimistic concurrency, PostgreSQL & in-memory adapters
- **Command Bus** with middleware pipeline (validation, idempotency, correlation, recovery)
- **Projection Engine** with inline, async, and live projections
- **Saga / Process Manager** with compensation handling
- **Outbox Pattern** for reliable messaging (Webhook, Kafka, SNS publishers)
- **Event Versioning** with schema evolution via upcasting (zero DB migration)
- **Field-Level Encryption** with AWS KMS, HashiCorp Vault, and local AES-256-GCM
- **GDPR Compliance** via crypto-shredding (key revocation)
- **Observability** with Prometheus metrics and OpenTelemetry tracing
- **Testing Utilities** with BDD fixtures, assertions, and test containers
- **CLI Tool** for code generation, migrations, and diagnostics

---

## What is go-mink?

go-mink is a batteries-included Event Sourcing and CQRS (Command Query Responsibility Segregation) library for Go. Inspired by [MartenDB](https://martendb.io/) for .NET, go-mink brings the same developer-friendly experience to the Go ecosystem.

> **Why "go-mink"?** Just as Marten (the animal) inspired the .NET library name, we chose go-mink - another member of the Mustelidae family - for our Go counterpart.

## Vision

**"Make Event Sourcing in Go as simple as using a traditional ORM"**

go-mink aims to eliminate the boilerplate code typically required when implementing Event Sourcing in Go, while providing a pluggable architecture that allows teams to choose their preferred storage backends.

## Key Features

| Feature | Status | Description |
|---------|--------|-------------|
| 🎯 **Event Store** | ✅ | Append-only event storage with optimistic concurrency |
| 🔌 **PostgreSQL Adapter** | ✅ | Production-ready PostgreSQL support |
| 🧪 **Memory Adapter** | ✅ | In-memory adapter for testing |
| 🧱 **Aggregates** | ✅ | Base implementation with event application |
| 📋 **Command Bus** | ✅ | Full CQRS with command handlers and middleware |
| 🔐 **Idempotency** | ✅ | Prevent duplicate command processing |
| 🔗 **Correlation/Causation** | ✅ | Distributed tracing support |
| 📖 **Projections** | ✅ | Inline, async, and live projection engine |
| 📊 **Read Models** | ✅ | Generic repository with query builder |
| 📡 **Subscriptions** | ✅ | Catch-up and polling event subscriptions |
| 🧪 **Testing Utilities** | ✅ | BDD fixtures, assertions, test containers |
| 📊 **Observability** | ✅ | Prometheus metrics & OpenTelemetry tracing |
| 📦 **MessagePack** | ✅ | Alternative serializer for performance |
| 🛠️ **CLI Tool** | ✅ | Code generation, migrations, diagnostics |
| � **Sagas** | ✅ | Process manager for long-running workflows |
| 🔄 **Event Versioning** | ✅ | Schema evolution with upcasting (zero DB migration) |
| 🔐 **Security** | ✅ | Field-level encryption and GDPR compliance |
| 📤 **Outbox Pattern** | ✅ | Reliable event publishing to external systems |

## Quick Example

```go
package main

import (
    "context"
    
    "github.com/AshkanYarmoradi/go-mink"
    "github.com/AshkanYarmoradi/go-mink/adapters/postgres"
)

func main() {
    ctx := context.Background()
    
    // Initialize PostgreSQL adapter
    adapter, _ := postgres.NewAdapter("postgres://localhost/mydb")
    defer adapter.Close()
    
    // Create event store
    store := mink.New(adapter)
    
    // Create and populate an aggregate
    order := NewOrder("order-123")
    order.Create("customer-456")
    order.AddItem("SKU-001", 2, 29.99)
    
    // Save aggregate (events are persisted)
    store.SaveAggregate(ctx, order)
    
    // Load aggregate (events are replayed)
    loaded := NewOrder("order-123")
    store.LoadAggregate(ctx, loaded)
}
```

## CQRS with Command Bus

```go
package main

import (
    "context"
    
    "github.com/AshkanYarmoradi/go-mink"
    "github.com/AshkanYarmoradi/go-mink/adapters/memory"
)

// Define a command
type CreateOrder struct {
    mink.CommandBase
    CustomerID string `json:"customerId"`
}

func (c CreateOrder) CommandType() string { return "CreateOrder" }
func (c CreateOrder) Validate() error {
    if c.CustomerID == "" {
        return mink.NewValidationError("CreateOrder", "CustomerID", "required")
    }
    return nil
}

func main() {
    ctx := context.Background()
    
    // Create command bus with middleware
    bus := mink.NewCommandBus()
    bus.Use(mink.ValidationMiddleware())
    bus.Use(mink.RecoveryMiddleware())
    bus.Use(mink.CorrelationIDMiddleware(nil))
    
    // Add idempotency (prevents duplicate processing)
    idempotencyStore := memory.NewIdempotencyStore()
    bus.Use(mink.IdempotencyMiddleware(mink.DefaultIdempotencyConfig(idempotencyStore)))
    
    // Register command handler
    bus.RegisterFunc("CreateOrder", func(ctx context.Context, cmd mink.Command) (mink.CommandResult, error) {
        c := cmd.(CreateOrder)
        // Process command...
        return mink.NewSuccessResult("order-123", 1), nil
    })
    
    // Dispatch command
    result, err := bus.Dispatch(ctx, CreateOrder{CustomerID: "cust-456"})
    if err != nil {
        panic(err)
    }
    
    fmt.Printf("Created order: %s (version %d)\n", result.AggregateID, result.Version)
}
```

## Projections & Read Models

```go
package main

import (
    "context"
    "time"

    "github.com/AshkanYarmoradi/go-mink"
    "github.com/AshkanYarmoradi/go-mink/adapters/memory"
)

// Define a read model
type OrderSummary struct {
    OrderID    string
    CustomerID string
    Status     string
    ItemCount  int
    Total      float64
}

// Define a projection
type OrderSummaryProjection struct {
    mink.ProjectionBase
    repo *mink.InMemoryRepository[OrderSummary]
}

func (p *OrderSummaryProjection) Apply(ctx context.Context, event mink.StoredEvent) error {
    // Transform events into read model updates
    // ...
    return nil
}

func main() {
    ctx := context.Background()

    // Create projection engine
    checkpointStore := memory.NewCheckpointStore()
    engine := mink.NewProjectionEngine(store,
        mink.WithCheckpointStore(checkpointStore),
    )

    // Register projections
    repo := mink.NewInMemoryRepository[OrderSummary](func(o *OrderSummary) string {
        return o.OrderID
    })
    engine.RegisterInline(&OrderSummaryProjection{repo: repo})

    // Start engine
    engine.Start(ctx)
    defer engine.Stop(ctx)

    // Query read models with fluent API
    orders, _ := repo.Query(ctx, mink.NewQuery().
        Where("Status", mink.Eq, "Pending").
        OrderByDesc("Total").
        WithLimit(10))

    // Rebuild projections when needed
    rebuilder := mink.NewProjectionRebuilder(store, checkpointStore)
    rebuilder.RebuildInline(ctx, projection, mink.RebuildOptions{BatchSize: 1000})
}
```

## Testing Utilities

```go
import (
    "github.com/AshkanYarmoradi/go-mink/testing/bdd"
    "github.com/AshkanYarmoradi/go-mink/testing/assertions"
    "github.com/AshkanYarmoradi/go-mink/testing/containers"
)

// BDD-style aggregate testing
func TestOrderCreation(t *testing.T) {
    order := NewOrder("order-123")

    bdd.Given(t, order).
        When(func() error {
            return order.Create("customer-456")
        }).
        Then(OrderCreated{OrderID: "order-123", CustomerID: "customer-456"})
}

// Event assertions
assertions.AssertEventTypes(t, events, "OrderCreated", "ItemAdded")

// PostgreSQL test containers
container := containers.StartPostgres(t)
db := container.MustDB(ctx)
```

## Observability

```go
import (
    "github.com/AshkanYarmoradi/go-mink/middleware/metrics"
    "github.com/AshkanYarmoradi/go-mink/middleware/tracing"
)

// Prometheus metrics
m := metrics.New(metrics.WithMetricsServiceName("order-service"))
m.MustRegister()
bus.Use(m.CommandMiddleware())

// OpenTelemetry tracing
tracer := tracing.NewTracer(tracing.WithServiceName("order-service"))
bus.Use(tracer.CommandMiddleware())
```

## Outbox Pattern

```go
import (
    "github.com/AshkanYarmoradi/go-mink"
    "github.com/AshkanYarmoradi/go-mink/adapters/memory"
    "github.com/AshkanYarmoradi/go-mink/outbox/webhook"
)

// Wrap event store with outbox for reliable publishing
outboxStore := memory.NewOutboxStore()
esWithOutbox := mink.NewEventStoreWithOutbox(store, outboxStore, []mink.OutboxRoute{
    {EventTypes: []string{"OrderCreated"}, Destination: "webhook:https://partner.example.com/events"},
    {Destination: "kafka:all-events"}, // All events
})

// Append events - outbox messages scheduled automatically
esWithOutbox.Append(ctx, "Order-123", []interface{}{OrderCreated{OrderID: "123"}})

// Start background processor with publishers
processor := mink.NewOutboxProcessor(outboxStore,
    mink.WithPublisher(webhook.New()),
    mink.WithPollInterval(time.Second),
)
processor.Start(ctx)
defer processor.Stop(ctx)
```

## Event Versioning & Upcasting

```go
import (
    "encoding/json"
    "github.com/AshkanYarmoradi/go-mink"
)

// Define upcaster: v1 → v2 (add Currency field)
type orderCreatedV1ToV2 struct{}

func (u orderCreatedV1ToV2) EventType() string { return "OrderCreated" }
func (u orderCreatedV1ToV2) FromVersion() int  { return 1 }
func (u orderCreatedV1ToV2) ToVersion() int    { return 2 }
func (u orderCreatedV1ToV2) Upcast(data []byte, m mink.Metadata) ([]byte, error) {
    var obj map[string]interface{}
    json.Unmarshal(data, &obj)
    obj["currency"] = "USD" // default for old events
    return json.Marshal(obj)
}

// Register with event store — old events upcasted transparently on load
chain := mink.NewUpcasterChain()
chain.Register(orderCreatedV1ToV2{})

store := mink.New(adapter, mink.WithUpcasters(chain))

// Schema compatibility checking
registry := mink.NewSchemaRegistry()
registry.Register("OrderCreated", mink.SchemaDefinition{Version: 1, Fields: v1Fields})
registry.Register("OrderCreated", mink.SchemaDefinition{Version: 2, Fields: v2Fields})
compat, _ := registry.CheckCompatibility("OrderCreated", 1, 2)
// compat == SchemaBackwardCompatible
```

## Field-Level Encryption

```go
import (
    "github.com/AshkanYarmoradi/go-mink"
    "github.com/AshkanYarmoradi/go-mink/encryption/local"
)

// Set up encryption provider (local for dev, KMS/Vault for production)
provider := local.New(local.WithKey("master-1", myKey))
defer provider.Close()

// Configure which fields to encrypt per event type
encConfig := mink.NewFieldEncryptionConfig(
    mink.WithEncryptionProvider(provider),
    mink.WithDefaultKeyID("master-1"),
    mink.WithEncryptedFields("CustomerCreated", "email", "phone", "ssn"),
    // Per-tenant keys for multi-tenant apps
    mink.WithTenantKeyResolver(func(tenantID string) string {
        return "tenant-" + tenantID
    }),
    // Crypto-shredding: graceful degradation when key is revoked
    mink.WithDecryptionErrorHandler(func(err error, eventType string, meta mink.Metadata) error {
        if errors.Is(err, encryption.ErrKeyRevoked) {
            return nil // Return encrypted data as-is
        }
        return err
    }),
)

// Create event store with encryption
store := mink.New(adapter, mink.WithFieldEncryption(encConfig))

// PII fields are encrypted at rest, decrypted transparently on load
// Revoking a key makes that tenant's data permanently unrecoverable (GDPR)
```

## Installation

```bash
go get github.com/AshkanYarmoradi/go-mink
go get github.com/AshkanYarmoradi/go-mink/adapters/postgres
```

## Documentation

| Document | Description |
|----------|-------------|
| [Introduction](docs/introduction.md) | Problem statement and goals |
| [Architecture](docs/architecture.md) | System design and components |
| [Event Store](docs/event-store.md) | Event storage design |
| [Read Models](docs/read-models.md) | Projection system |
| [Adapters](docs/adapters.md) | Database adapter system |
| [CLI](docs/cli.md) | Command-line tooling |
| [API Design](docs/api-design.md) | Public API reference |
| [Roadmap](docs/roadmap.md) | Future development plans |
| [Advanced Patterns](docs/advanced-patterns.md) | Commands, Sagas, Outbox, Encryption |
| [Event Versioning](docs/versioning.md) | Schema evolution & upcasting |
| [Security](docs/security.md) | Encryption, GDPR compliance |
| [Testing](docs/testing.md) | BDD fixtures and test utilities |

## License

Apache License 2.0 - See [LICENSE](LICENSE) for details.

---

**go-mink** - Event Sourcing for Go, Done Right.
