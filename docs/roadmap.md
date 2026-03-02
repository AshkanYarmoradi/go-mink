---
layout: default
title: Roadmap
nav_order: 11
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
v1.0.0 (current) ──► v1.1.0 ──► v1.2.0 ──► ...
   │                    │          │
   ▼                    ▼          ▼
 Stable              Data       Scale &
 Release           Governance  Performance
```

---

## v1.0.0 — What's Included

go-mink v1.0.0 is the first stable release, providing a complete toolkit for building event-sourced applications in Go.

### Core Event Store
- Event store with append-only storage and optimistic concurrency
- PostgreSQL adapter (production) and in-memory adapter (testing)
- JSON serialization with event registry
- Stream management with version constants (`AnyVersion`, `NoStream`, `StreamExists`)

### Aggregates
- `Aggregate` interface and `AggregateBase` implementation
- Event application pattern with `SaveAggregate` / `LoadAggregate`

### Command Bus & CQRS
- `Command` interface with validation support
- `CommandBus` with middleware pipeline
- Type-safe generic handlers (`NewGenericHandler`, `NewAggregateHandler`)
- Built-in middleware: Validation, Recovery, Logging, Metrics, Timeout, Retry, Correlation ID, Causation ID, Tenant, Idempotency
- Idempotency store (PostgreSQL and in-memory)

### Projection Engine & Read Models
- Inline, async, and live projections
- `ProjectionEngine` with worker pool, checkpointing, and rebuilding
- `ReadModelRepository[T]` generic interface with fluent query builder
- Event subscriptions: `SubscribeAll`, `SubscribeStream`, `SubscribeCategory`
- Catch-up and polling subscriptions with event filters

### Saga / Process Manager
- `Saga` interface and `SagaBase` implementation
- `SagaManager` for orchestration with event correlation and idempotency
- Compensation handling for rollback workflows
- `SagaStore` with PostgreSQL and in-memory implementations

### Outbox Pattern
- `OutboxStore` interface with PostgreSQL and in-memory implementations
- `EventStoreWithOutbox` wrapper with automatic routing
- `OutboxProcessor` background worker with polling, retry, and dead-letter handling
- Built-in publishers: Webhook, Kafka, SNS
- Atomic event+outbox writes (PostgreSQL)
- Prometheus metrics integration

### Event Versioning & Upcasting
- `Upcaster` interface for raw byte-level transformations
- `UpcasterChain` with thread-safe registry and gap validation
- `UpcastingSerializer` decorator
- `SchemaRegistry` with compatibility checking
- Automatic upcasting on load, schema stamping on save
- Zero overhead when not configured

### Field-Level Encryption
- Envelope encryption with AES-256-GCM
- Providers: Local (testing), AWS KMS (production), HashiCorp Vault Transit (production)
- Per-tenant encryption keys via `WithTenantKeyResolver()`
- Encryption metadata in `Metadata.Custom` (no DB schema changes)
- Zero overhead when not configured

### GDPR Compliance
- Crypto-shredding via key revocation
- `WithDecryptionErrorHandler()` for graceful degradation

### Observability
- Prometheus metrics middleware (`middleware/metrics`)
- OpenTelemetry tracing middleware (`middleware/tracing`)
- 17+ Prometheus metrics for commands, event store, projections, outbox

### Serializers
- JSON serializer (built-in)
- MessagePack serializer (`serializer/msgpack`)
- Protocol Buffers serializer (`serializer/protobuf`)

### CLI Tool
- `mink init` — Project scaffolding
- `mink generate` — Code generation (aggregate, event, projection, command)
- `mink migrate` — Schema management (up, down, create, status)
- `mink projection` — Projection management (list, status, pause, resume, rebuild)
- `mink stream` — Stream inspection (list, events, stats, export)
- `mink diagnose` — Health checks and diagnostics
- `mink schema` — Event schema generation

### Testing Utilities
- BDD-style fixtures (`testing/bdd`) — Given/When/Then
- Event assertions and diffing (`testing/assertions`)
- Projection test helpers (`testing/projections`)
- Saga test fixtures (`testing/sagas`)
- PostgreSQL test containers (`testing/containers`)

---

## Future Roadmap

### v1.1.0 — Data Governance

- [ ] Data export (GDPR right to access)
- [ ] Audit logging middleware
- [ ] Data retention policies with configurable rules
- [ ] Time-travel queries (load aggregate at timestamp or version)

### v1.2.0 — Scale & Performance

- [ ] Multi-tenancy strategies (shared table, schema-per-tenant, database-per-tenant)
- [ ] Snapshot store interface and adapters (PostgreSQL, Redis)
- [ ] Additional adapters: MongoDB event store, Redis read models
- [ ] Performance optimization: connection pooling, batch loading, benchmarking suite

### Future

**Reliability**
- [ ] Dead letter queue improvements
- [ ] Circuit breaker pattern
- [ ] Graceful degradation

**Advanced Projections**
- [ ] Projection versioning
- [ ] Projection dependencies
- [ ] Projection analytics

**Production Tooling**
- [ ] Configuration validation
- [ ] Migration rollback improvements
- [ ] Backup/restore utilities

**Community Adapters**
- [ ] DynamoDB, ScyllaDB, CockroachDB
- [ ] ClickHouse (analytics projections)
- [ ] Elasticsearch (search projections)
- [ ] NATS JetStream

**Ecosystem**
- [ ] gRPC API for remote event store
- [ ] GraphQL read model API
- [ ] Admin UI dashboard
- [ ] Kubernetes operator
- [ ] Event sourcing debugger (visual timeline)

---

## Contributing

### How to Contribute

1. **Pick an issue** - Check GitHub issues labeled `good first issue`
2. **Discuss** - Comment on the issue or join Discord
3. **Fork & Branch** - Create feature branch from `develop`
4. **Implement** - Follow code style guidelines
5. **Test** - Add unit and integration tests
6. **PR** - Submit pull request targeting `develop`

### Priority Areas for Contributors

| Area | Difficulty | Impact |
|------|------------|--------|
| Documentation | Easy | High |
| Test coverage | Easy | High |
| MongoDB adapter | Medium | High |
| Redis adapter | Medium | Medium |
| Performance benchmarks | Medium | Medium |
| Data export | Medium | High |
| Audit logging | Medium | High |
| DynamoDB adapter | Hard | Medium |
| Snapshot support | Hard | High |

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
| v1.0.0 release | 500 GitHub stars |
| v1.0.0 + 6 months | 10 production users |
| v1.1.0 release | 1,000 GitHub stars |
| v1.2.0 release | 25 production users |

### Quality Metrics

- Test coverage > 90%
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

**Let's make Event Sourcing accessible to every Go developer!**
