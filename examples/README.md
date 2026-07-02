# go-mink Examples

Fifteen self-contained, runnable programs that show every major go-mink feature in action. Each lives in its own folder with a dedicated README explaining what it does, how to run it, and what to look for.

Most examples need **zero infrastructure** — just `go run`. Two use PostgreSQL (marked 🐘) and start it for you via Docker Compose.

## Prerequisites

- **Go 1.25+**
- **Docker** (only for the 🐘 PostgreSQL examples)

## Running an example

```bash
# From the repository root:
go run ./examples/basic          # in-memory examples — no setup

# bdd-testing is a test suite, run it with `go test`:
go test -v ./examples/bdd-testing
```

For the PostgreSQL examples, start infra first and stop it after:

```bash
docker compose -f docker-compose.test.yml up -d --wait
go run ./examples/cqrs-postgres
docker compose -f docker-compose.test.yml down
```

The adapter auto-creates its schema on first use, so there's nothing to migrate by hand.

## The examples

### Start here

| Example | What it shows | Infra |
|---------|---------------|:-----:|
| [basic](basic) | The fundamentals — define events, model an aggregate, save and replay a stream | — |
| [cqrs](cqrs) | Dispatch commands through a bus with a full middleware pipeline | — |
| [projections](projections) | Turn events into queryable read models (inline & live) | — |

### Core patterns

| Example | What it shows | Infra |
|---------|---------------|:-----:|
| [sagas](sagas) | Coordinate multi-step workflows with automatic compensation | — |
| [cqrs-postgres](cqrs-postgres) | The `cqrs` example backed by a real PostgreSQL database | 🐘 |
| [full-ecommerce](full-ecommerce) | **Everything together** — a realistic order-fulfillment system | 🐘 |

### Schema & serialization

| Example | What it shows | Infra |
|---------|---------------|:-----:|
| [versioning](versioning) | Evolve event schemas (v1 → v2 → v3) with upcasting — no DB migration | — |
| [msgpack](msgpack) | Swap in MessagePack serialization for smaller, faster payloads | — |
| [protobuf](protobuf) | Strongly-typed, schema-enforced Protocol Buffers serialization | — |

### Security, privacy & compliance

| Example | What it shows | Infra |
|---------|---------------|:-----:|
| [encryption](encryption) | Field-level encryption at rest, per-tenant keys, crypto-shredding | — |
| [export](export) | GDPR data export (right to access / portability) | — |
| [audit](audit) | An immutable audit trail of every command (who/what/when/outcome) | — |

### Observability & testing

| Example | What it shows | Infra |
|---------|---------------|:-----:|
| [metrics](metrics) | Prometheus metrics for commands and event-store operations | — |
| [tracing](tracing) | OpenTelemetry distributed tracing | — |
| [bdd-testing](bdd-testing) | Behavior-driven aggregate tests with Given/When/Then | — |

## Suggested learning path

1. **[basic](basic)** — understand events, aggregates, and the event store.
2. **[cqrs](cqrs)** — add commands, handlers, and middleware.
3. **[projections](projections)** — build read models for queries.
4. **[sagas](sagas)** — orchestrate workflows across aggregates.
5. Pick the production concerns you care about — **[versioning](versioning)**, **[encryption](encryption)**, **[export](export)**, **[audit](audit)**, **[metrics](metrics)**, **[tracing](tracing)**.
6. **[full-ecommerce](full-ecommerce)** — see how it all composes into one system.

## Learn more

- 📖 **Documentation:** [go-mink.dev](https://go-mink.dev)
- 🎓 **Guided tutorial:** [Build an e-commerce system](https://go-mink.dev/docs/tutorial/setup)
- 🔎 **API reference:** [pkg.go.dev/go-mink.dev](https://pkg.go.dev/go-mink.dev)
- 🏠 **Project README:** [../README.md](../README.md)
