# Development Roadmap

## Version Overview

```
v0.1.0 ──► v0.2.0 ──► v0.3.0 ──► v0.4.0 ──► v1.0.0
  │          │          │          │          │
  ▼          ▼          ▼          ▼          ▼
 Core     Read       CLI &      Multi-    Production
 Event    Models     DX         tenant    Ready
 Store                          & Scale
```

## Phase 1: Foundation (v0.1.0)

**Timeline: 6-8 weeks**

### Core Event Store
- [ ] Event store interface design
- [ ] PostgreSQL adapter (primary)
- [ ] In-memory adapter (testing)
- [ ] Event serialization (JSON)
- [ ] Optimistic concurrency control
- [ ] Stream management

### Basic Aggregates
- [ ] Aggregate base implementation
- [ ] Event application pattern
- [ ] Save/Load aggregate

### Initial Testing
- [ ] Unit test framework
- [ ] Integration test setup
- [ ] Example application

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

## Phase 2: Read Models (v0.2.0)

**Timeline: 6-8 weeks**

### Projection Engine
- [ ] Inline projections (same transaction)
- [ ] Async projections (background)
- [ ] Projection checkpointing
- [ ] Projection rebuilding
- [ ] Error handling & retries

### Read Model Repository
- [ ] Generic repository interface
- [ ] Query builder
- [ ] PostgreSQL read model adapter
- [ ] MongoDB read model adapter

### Subscriptions
- [ ] Subscribe to all events
- [ ] Subscribe to stream
- [ ] Subscribe to category
- [ ] Catch-up subscriptions

### Deliverables
```go
// By end of v0.2.0, projections work:
engine := go-mink.NewProjectionEngine(store)
engine.RegisterInline(&OrderSummaryProjection{})
engine.RegisterAsync(&OrderAnalyticsProjection{})
engine.Start(ctx)

// Query read models
repo := postgres.NewRepository[OrderSummary](db)
orders, _ := repo.Find(ctx, 
    go-mink.NewQuery().Where("status", "=", "pending"))
```

---

## Phase 3: Developer Experience (v0.3.0)

**Timeline: 6-8 weeks**

### CLI Tool
- [ ] `go-mink init` - Project scaffolding
- [ ] `go-mink generate` - Code generation
- [ ] `go-mink migrate` - Schema management
- [ ] `go-mink projection` - Projection management
- [ ] `go-mink stream` - Stream inspection
- [ ] `go-mink diagnose` - Health checks

### Additional Serializers
- [ ] Protocol Buffers support
- [ ] MessagePack support
- [ ] Custom serializer interface

### Middleware & Observability
- [ ] Logging middleware
- [ ] Metrics middleware (Prometheus)
- [ ] Tracing middleware (OpenTelemetry)
- [ ] Retry middleware

### Documentation
- [ ] API reference docs
- [ ] Getting started guide
- [ ] Tutorial: Building an e-commerce app
- [ ] Architecture decision records

### Deliverables
```bash
# By end of v0.3.0, CLI works:
$ go-mink init myapp
$ go-mink generate aggregate Order
$ go-mink migrate up
$ go-mink projection rebuild OrderSummary
```

---

## Phase 4: Scale & Multi-tenancy (v0.4.0)

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
- [ ] Configurable snapshot policies

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
// By end of v0.4.0, multi-tenancy works:
store := go-mink.New(adapter, go-mink.WithMultiTenancy(go-mink.SchemaPerTenant))

tenantStore := store.ForTenant("acme-corp")
tenantStore.SaveAggregate(ctx, order)
```

---

## Phase 5: Production Ready (v1.0.0)

**Timeline: 8-10 weeks**

### Reliability
- [ ] Outbox pattern implementation
- [ ] Idempotency support
- [ ] Dead letter queue
- [ ] Circuit breaker

### Event Store Features
- [ ] Event archival
- [ ] Event encryption
- [ ] Event versioning/upcasting
- [ ] Global event ordering guarantees

### Advanced Projections
- [ ] Live projections (real-time)
- [ ] Projection versioning
- [ ] Projection dependencies
- [ ] Projection analytics

### Production Tooling
- [ ] Health check endpoints
- [ ] Graceful shutdown
- [ ] Configuration validation
- [ ] Migration rollback

### Enterprise Features
- [ ] Audit logging
- [ ] GDPR data deletion
- [ ] Event retention policies
- [ ] Backup/restore utilities

### Deliverables
```go
// v1.0.0 - Production ready
store := go-mink.New(adapter,
    go-mink.WithOutbox(outboxAdapter),
    go-mink.WithEncryption(kms),
    go-mink.WithRetentionPolicy(90 * 24 * time.Hour),
    go-mink.WithHealthCheck("/health"),
)
```

---

## Future Considerations (Post v1.0)

### v1.1+ Candidates
- gRPC API for remote event store
- Event store clustering
- Cross-aggregate transactions (Sagas)
- GraphQL read model API
- Admin UI dashboard
- Event replay to external systems
- Cloud-native deployment (Kubernetes operator)

### Community Adapters
- CockroachDB
- ClickHouse (analytics)
- Elasticsearch (search projections)
- Apache Kafka (event bus)
- NATS JetStream

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
| MongoDB adapter | Medium | High |
| CLI commands | Medium | Medium |
| Redis adapter | Medium | Medium |
| Performance benchmarks | Medium | Medium |
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
| v0.2.0 release | 100 GitHub stars |
| v0.3.0 release | First production user |
| v1.0.0 release | 500 GitHub stars |
| v1.0.0 + 6 months | 5 production users |

### Quality Metrics

- Test coverage > 80%
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
