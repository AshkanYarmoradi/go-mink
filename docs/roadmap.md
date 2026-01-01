---
layout: default
title: Roadmap
nav_order: 12
permalink: /docs/roadmap
---

# Development Roadmap
{: .no_toc }

## Table of contents
{: .no_toc .text-delta }

1. TOC
{:toc}

---

## Version Overview

```
v0.1.0 ──► v0.2.0 ──► v0.3.0 ──► v0.4.0 ──► v0.5.0 ──► v1.0.0
  │          │          │          │          │          │
  ▼          ▼          ▼          ▼          ▼          ▼
 Core      CQRS &    Read      CLI &     Security   Production
 Event     Commands  Models    DX        & Scale    Ready
 Store
```

## Phase 1: Foundation (v0.1.0) ✅ COMPLETE

**Status: Released**

### Core Event Store
- [x] Event store interface design
- [x] PostgreSQL adapter (primary)
- [x] In-memory adapter (testing)
- [x] Event serialization (JSON)
- [x] Optimistic concurrency control
- [x] Stream management

### Basic Aggregates
- [x] Aggregate base implementation
- [x] Event application pattern
- [x] Save/Load aggregate

### Initial Testing
- [x] Unit test framework (90%+ coverage)
- [x] Integration test setup (Docker Compose)
- [x] Example application

### Deliverables
```go
// By end of v0.1.0, this works:
store := go-mink.New(postgres.NewAdapter(connStr))

order := NewOrder("order-123")
order.Create("customer-456")
order.AddItem("SKU-001", 2, 29.99)

store.SaveAggregate(ctx, order)

loaded := NewOrder("order-123")
store.LoadAggregate(ctx, loaded)
```

---

## Phase 2: CQRS & Command Pattern (v0.2.0) ✅ COMPLETE

**Status: Released**

### Command Bus
- [x] Command interface design
- [x] Command handler registration (type-safe generic handlers)
- [x] Command bus implementation
- [x] Command middleware pipeline
- [x] Validation middleware
- [x] Recovery middleware (panic handling)
- [x] Logging middleware
- [x] Metrics middleware
- [x] Timeout middleware
- [x] Retry middleware
- [x] Tenant middleware (multi-tenancy)

### Idempotency
- [x] Idempotency key generation (configurable strategies)
- [x] Idempotency store interface
- [x] PostgreSQL idempotency adapter
- [x] In-memory idempotency adapter (testing)
- [x] Idempotency middleware

### Causation & Correlation
- [x] Enhanced metadata (correlation ID, causation ID)
- [x] Correlation ID middleware
- [x] Causation ID middleware
- [x] Request context propagation

### Deliverables
```go
// v0.2.0 Command Bus implementation:
bus := mink.NewCommandBus()

// Register middleware
bus.Use(mink.ValidationMiddleware())
bus.Use(mink.RecoveryMiddleware())
bus.Use(mink.CorrelationIDMiddleware(nil))
bus.Use(mink.CausationIDMiddleware())
bus.Use(mink.IdempotencyMiddleware(mink.DefaultIdempotencyConfig(idempotencyStore)))

// Register handlers (type-safe)
bus.Register(mink.NewGenericHandler[CreateOrder](func(ctx context.Context, cmd CreateOrder) (mink.CommandResult, error) {
    // Handle command...
    return mink.NewSuccessResult("order-123", 1), nil
}))

// Dispatch
result, err := bus.Dispatch(ctx, CreateOrder{CustomerID: "cust-123"})
```

---

## Phase 3: Read Models (v0.3.0) ✅ COMPLETE

**Status: Released**

### Projection Engine
- [x] Inline projections (synchronous processing)
- [x] Async projections (background workers)
- [x] Live projections (real-time notifications)
- [x] Projection checkpointing
- [x] Projection rebuilding (single and parallel)
- [x] Error handling & retries (exponential backoff)
- [x] Projection status monitoring

### Read Model Repository
- [x] Generic repository interface (`ReadModelRepository[T]`)
- [x] Query builder with fluent API
- [x] In-memory repository (testing)
- [x] Filter operators (Eq, NotEq, Gt, Gte, Lt, Lte, In, Contains)

### Subscriptions
- [x] Subscribe to all events
- [x] Subscribe to stream
- [x] Subscribe to category
- [x] Catch-up subscriptions
- [x] Polling subscriptions
- [x] Event filters (type, category, composite)

### Checkpoint Storage
- [x] Checkpoint adapter interface
- [x] In-memory checkpoint store
- [x] Checkpoint with timestamp tracking

### Deliverables
```go
// v0.3.0 Projection Engine implementation:
checkpointStore := memory.NewCheckpointStore()
engine := mink.NewProjectionEngine(store,
    mink.WithCheckpointStore(checkpointStore),
)

// Register projections
engine.RegisterInline(&OrderSummaryProjection{})
engine.RegisterAsync(&AnalyticsProjection{}, mink.AsyncOptions{
    BatchSize:   100,
    Interval:    time.Second,
    Workers:     4,
    RetryPolicy: mink.NewExponentialBackoffRetry(100*time.Millisecond, 5*time.Second, 3),
})
engine.RegisterLive(&DashboardProjection{})

// Start engine
engine.Start(ctx)
defer engine.Stop(ctx)

// Query read models
repo := mink.NewInMemoryRepository[OrderSummary](idExtractor)
orders, _ := repo.Query(ctx, mink.NewQuery().
    Where("Status", mink.Eq, "Pending").
    OrderByDesc("CreatedAt").
    WithLimit(10))

// Rebuild projections
rebuilder := mink.NewProjectionRebuilder(store, checkpointStore)
rebuilder.Rebuild(ctx, projection, mink.RebuildOptions{BatchSize: 1000})
```

---

## Phase 4: Developer Experience (v0.4.0)

**Timeline: 6-8 weeks**

### CLI Tool
- [ ] `mink init` - Project scaffolding
- [ ] `mink generate` - Code generation
- [ ] `mink migrate` - Schema management
- [ ] `mink projection` - Projection management
- [ ] `mink stream` - Stream inspection
- [ ] `mink diagnose` - Health checks
- [ ] `mink schema` - Event schema management

### Additional Serializers
- [ ] Protocol Buffers support
- [ ] MessagePack support
- [ ] Custom serializer interface

### Middleware & Observability
- [ ] Logging middleware
- [ ] Metrics middleware (Prometheus)
- [ ] Tracing middleware (OpenTelemetry)
- [ ] Retry middleware

### Testing Utilities
- [ ] BDD-style test fixtures (Given/When/Then)
- [ ] Event assertions
- [ ] Event diffing
- [ ] Projection test helpers
- [ ] Saga test fixtures
- [ ] Test containers integration

### Documentation
- [ ] API reference docs
- [ ] Getting started guide
- [ ] Tutorial: Building an e-commerce app
- [ ] Architecture decision records

### Deliverables
```bash
# By end of v0.4.0, CLI works:
$ mink init myapp
$ mink generate aggregate Order
$ mink migrate up
$ mink projection rebuild OrderSummary
```

---

## Phase 5: Security & Advanced Patterns (v0.5.0)

**Timeline: 8-10 weeks**

### Saga / Process Manager
- [ ] Saga interface design
- [ ] Saga state persistence
- [ ] Saga manager
- [ ] Compensation handling
- [ ] Saga testing utilities

### Outbox Pattern
- [ ] Outbox message interface
- [ ] Outbox store (PostgreSQL)
- [ ] Outbox processor
- [ ] Built-in publishers (Kafka, Webhooks, SNS)
- [ ] Retry and dead-letter handling

### Event Versioning & Upcasting
- [ ] Schema version tracking
- [ ] Upcaster interface
- [ ] Upcaster chain
- [ ] Schema registry
- [ ] Compatibility checking

### Security & Encryption
- [ ] Field-level encryption
- [ ] AWS KMS provider
- [ ] HashiCorp Vault provider
- [ ] Per-tenant encryption keys

### GDPR Compliance
- [ ] Crypto-shredding (forget data subject)
- [ ] Data export (right to access)
- [ ] Audit logging
- [ ] Data retention policies

### Time-Travel Queries
- [ ] Load aggregate at timestamp
- [ ] Load aggregate at version
- [ ] Historical debugging tools

### Deliverables
```go
// By end of v0.5.0, advanced patterns work:
sagaManager := mink.NewSagaManager(store, bus)
sagaManager.Register("OrderFulfillment", NewOrderFulfillmentSaga)
sagaManager.Start(ctx)

// Encryption
store := mink.New(adapter, mink.WithEncryption(EncryptionConfig{
    Provider: kms.NewProvider(awsConfig),
    EncryptedFields: map[string][]string{
        "CustomerCreated": {"email", "phone", "ssn"},
    },
}))

// GDPR
gdpr := mink.NewGDPRManager(store, keyStore)
gdpr.ForgetDataSubject(ctx, "customer-123")
```

---

## Phase 6: Scale & Multi-tenancy (v0.6.0)

**Timeline: 8-10 weeks**

### Multi-tenancy
- [ ] Shared table strategy
- [ ] Schema-per-tenant strategy
- [ ] Database-per-tenant strategy
- [ ] Tenant context API

### Snapshots
- [ ] Snapshot store interface
- [ ] PostgreSQL snapshot adapter
- [ ] Redis snapshot adapter
- [ ] Configurable snapshot policies (every N events, time-based, size-based)

### Additional Adapters
- [ ] MongoDB event store adapter
- [ ] Redis read model adapter
- [ ] AWS DynamoDB adapter (community)
- [ ] ScyllaDB adapter (community)

### Performance
- [ ] Connection pooling
- [ ] Batch event loading
- [ ] Parallel projection processing
- [ ] Benchmarking suite

### Deliverables
```go
// By end of v0.6.0, multi-tenancy works:
store := mink.New(adapter, mink.WithMultiTenancy(mink.SchemaPerTenant))

tenantStore := store.ForTenant("acme-corp")
tenantStore.SaveAggregate(ctx, order)
```

---

## Phase 7: Production Ready (v1.0.0)

**Timeline: 8-10 weeks**

### Reliability
- [ ] Dead letter queue
- [ ] Circuit breaker
- [ ] Graceful degradation
- [ ] Health check endpoints

### Event Store Features
- [ ] Event archival
- [ ] Global event ordering guarantees
- [ ] Stream compaction

### Advanced Projections
- [ ] Live projections (real-time)
- [ ] Projection versioning
- [ ] Projection dependencies
- [ ] Projection analytics

### Production Tooling
- [ ] Graceful shutdown
- [ ] Configuration validation
- [ ] Migration rollback
- [ ] Backup/restore utilities

### Deliverables
```go
// v1.0.0 - Production ready
store := mink.New(adapter,
    mink.WithOutbox(outboxAdapter),
    mink.WithEncryption(kms),
    mink.WithRetentionPolicy(90 * 24 * time.Hour),
    mink.WithHealthCheck("/health"),
    mink.WithGracefulShutdown(30 * time.Second),
)
```

---

## Future Considerations (Post v1.0)

### v1.1+ Candidates
- gRPC API for remote event store
- Event store clustering
- GraphQL read model API
- Admin UI dashboard
- Event replay to external systems
- Cloud-native deployment (Kubernetes operator)
- Event sourcing debugger (visual timeline)
- AI-powered event analysis

### Community Adapters
- CockroachDB
- ClickHouse (analytics)
- Elasticsearch (search projections)
- Apache Kafka (event bus)
- NATS JetStream
- Google Cloud Spanner
- Azure Cosmos DB

---

## Contributing

### How to Contribute

1. **Pick an issue** - Check GitHub issues labeled `good first issue`
2. **Discuss** - Comment on the issue or join Discord
3. **Fork & Branch** - Create feature branch from `main`
4. **Implement** - Follow code style guidelines
5. **Test** - Add unit and integration tests
6. **PR** - Submit pull request with description

### Priority Areas for Contributors

| Area | Difficulty | Impact |
|------|------------|--------|
| Documentation | Easy | High |
| Test coverage | Easy | High |
| BDD test fixtures | Easy | High |
| MongoDB adapter | Medium | High |
| CLI commands | Medium | Medium |
| Redis adapter | Medium | Medium |
| Performance benchmarks | Medium | Medium |
| Saga implementation | Hard | High |
| Outbox pattern | Hard | High |
| Event upcasting | Hard | High |
| DynamoDB adapter | Hard | Medium |

### Code Style

- Follow standard Go conventions
- Use `gofmt` and `golangci-lint`
- Write table-driven tests
- Document public APIs
- Keep functions focused and small

---

## Success Metrics

### Adoption Targets

| Milestone | Target |
|-----------|--------|
| v0.1.0 release | 10 early adopters |
| v0.2.0 release | 50 GitHub stars |
| v0.3.0 release | 100 GitHub stars |
| v0.4.0 release | First production user |
| v0.5.0 release | 250 GitHub stars |
| v1.0.0 release | 500 GitHub stars |
| v1.0.0 + 6 months | 10 production users |

### Quality Metrics

- Test coverage > 90% ✅
- Zero critical bugs in release
- Documentation for all public APIs
- Response to issues < 48 hours

---

## Get Involved

- **GitHub**: github.com/AshkanYarmoradi/go-mink
- **Discord**: discord.gg/go-mink-go
- **Twitter**: @go-mink_go
- **Blog**: blog.go-mink-go.dev

---

**Let's make Event Sourcing accessible to every Go developer!** 🦫
