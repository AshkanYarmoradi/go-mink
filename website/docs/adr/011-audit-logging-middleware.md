---
title: "ADR-011: Audit Logging Middleware"
sidebar_position: 11
---

# ADR-011: Audit Logging Middleware

| Status | Date | Deciders |
|--------|------|----------|
| Accepted | 2026-06-04 | Core Team |

## Context

Regulated and security-sensitive systems need a durable record of **who did what,
when, and with what outcome**:

- **Regulatory** — GDPR Article 30 (records of processing), HIPAA, SOC 2, and
  similar regimes require an auditable trail of data access and modification.
- **Forensics** — after an incident, reconstruct exactly which operations ran.
- **Accountability** — attribute actions to a principal for dispute resolution.

go-mink already routes every write through the **command bus**
([ADR-004](command-bus-middleware)). The command is the unit of *intent*, and it
carries (or can resolve) the actor, tenant, correlation, and outcome. A
cross-cutting audit concern is therefore a natural fit for a bus middleware,
mirroring the existing idempotency middleware and store pattern.

## Decision

Provide a first-class **audit logging middleware** plus an `AuditStore`:

- `AuditMiddleware(AuditConfig)` writes one immutable `AuditEntry` per dispatched
  command, capturing **both successes and failures**, with execution duration and
  the actor/tenant/correlation/causation context.
- `AuditStore` is an **append + query** interface (`Append`, `Find`, `Count`,
  `Cleanup`, `Initialize`, `Close`) with in-memory and PostgreSQL implementations,
  re-exported from the root `mink` package like idempotency.
- The actor ("who") is resolved by a configurable `ActorFunc`, defaulting to a
  context value set via `WithActor` — go-mink has no built-in principal concept,
  so identity is supplied by the host application.
- Writes are **synchronous** and **fail-open by default** (a store outage never
  breaks command processing); `FailClosed` surfaces the write failure for flows
  where an un-recorded command is itself a violation.
- **Zero overhead when unused** — the middleware is opt-in via `bus.Use(...)`.

## Consequences

### Positive
- Compliance-ready audit trail with no changes to aggregates or handlers.
- Queryable by actor, tenant, command type, aggregate, time range, and outcome.
- Consistent with the idempotency feature (same store/middleware shape), so it is
  easy to learn, test (in-memory store), and operate (PostgreSQL store).

### Negative
- Storage growth — every command writes a row; retention must be managed
  (`Cleanup`) and indexes cost write throughput.
- I/O on the hot path — a synchronous append adds latency per command.

### Neutral
- **Not transactional.** The entry is written after the command, so `FailClosed`
  detects audit-store failures but cannot prevent a side effect that already ran.
- Audit rows can contain sensitive data (error text, metadata) and must be access-
  controlled.
- The middleware does not recover panics; `RecoveryMiddleware` is composed inside
  it to audit panicking handlers.

## Alternatives Considered

### Event-based auditing (store audit events in the event stream)
Emit a domain "audit event" alongside business events.
- **Rejected** — pollutes the domain stream with cross-cutting concerns, makes
  audit queries expensive (scan/replay), and couples retention of audit data to
  the immutable event log.

### Database triggers / `pgaudit`
Audit at the database layer.
- **Rejected** — database-specific (breaks the adapter abstraction), captures rows
  changed but not the **command intent** or the application-level actor, and is
  invisible to the in-memory adapter used in tests.

### Observability only (logs / OpenTelemetry tracing)
Rely on structured logs and traces.
- **Rejected** — tracing/logging targets debugging and sampling, not durable,
  queryable, complete compliance records. (Audit logging *complements* the
  existing logging/metrics/tracing middleware; it does not replace them.)

## References
- [ADR-004: Command Bus with Middleware Pipeline](command-bus-middleware)
- [ADR-010: Multi-tenancy via Metadata](multi-tenancy)
- [Audit Logging guide](/docs/advanced/audit-logging)
- GDPR Article 30 — Records of processing activities
