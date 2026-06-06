---
title: Audit Logging
sidebar_position: 10
---

# Audit Logging

Persist an immutable, queryable **audit trail** of every command processed by the
command bus — who did what, when, and with what outcome. Audit logging is a
[command bus middleware](/docs/adr/command-bus-middleware), so it composes with
the rest of the pipeline and requires no changes to your aggregates or handlers.

Use it for:

- **Compliance** — GDPR (Article 30, records of processing), HIPAA, SOC 2, and
  similar regimes that require a record of who accessed or changed data.
- **Forensics** — reconstruct exactly which commands ran during an incident.
- **Accountability** — prove (or disprove) that a given actor performed an action.
- **Debugging** — answer "who changed this aggregate, and when?"

---

## Quick start

```go
import (
    "go-mink.dev"
    "go-mink.dev/adapters/memory"
)

auditStore := memory.NewAuditStore()        // swap for the PostgreSQL store in production

bus := mink.NewCommandBus()
bus.Use(mink.AuditMiddleware(mink.DefaultAuditConfig(auditStore)))

// ... register handlers and dispatch commands as usual.

// Later, query the trail:
entries, _ := auditStore.Find(ctx, mink.AuditQuery{Limit: 50})
for _, e := range entries {
    fmt.Printf("%s  %s  actor=%s  success=%t\n", e.Timestamp, e.CommandType, e.Actor, e.Success)
}
```

One entry is written per dispatched command, capturing **both successful and
failed** executions.

---

## The audit entry

Each command produces an `AuditEntry` (re-exported as `mink.AuditEntry`):

```go
type AuditEntry struct {
    ID            string            // unique entry id (UUIDv4, generated if empty)
    CommandType   string            // e.g. "CreateOrder"
    CommandID     string            // command instance id, if it exposes GetCommandID()
    AggregateID   string            // affected aggregate (from result, else AggregateCommand)
    Actor         string            // who initiated the command (see "Capturing the actor")
    TenantID      string            // from TenantIDFromContext
    CorrelationID string            // from CorrelationIDFromContext
    CausationID   string            // from CausationIDFromContext
    Error         string            // failure message (empty on success)
    Version       int64             // aggregate version after processing
    DurationMs    int64             // execution time in milliseconds
    Timestamp     time.Time         // when the command was audited
    Success       bool              // err == nil && result succeeded
    Metadata      map[string]string // optional, see IncludeMetadata
}
```

Context fields (`Actor`, `TenantID`, `CorrelationID`, `CausationID`) are read from
the context the audit middleware *receives*. Because Go contexts flow inward, any
middleware that sets these values (e.g. `CorrelationIDMiddleware`,
`TenantMiddleware`, or your auth layer via `WithActor`) must run **before**
(outside) the audit middleware — or the value must be set before dispatch — for it
to appear in the entry. See [Placement in the middleware chain](#placement-in-the-middleware-chain).

---

## Configuring the middleware

```go
type AuditConfig struct {
    Store           AuditStore  // required
    ActorFunc       ActorFunc   // resolve the actor; nil → read from context
    SkipCommands    []string    // command types to NOT audit
    FailClosed      bool         // see "Failure semantics"; default false (fail-open)
    IncludeMetadata bool         // copy the command's metadata map into the entry
}

func DefaultAuditConfig(store AuditStore) AuditConfig
```

`DefaultAuditConfig(store)` is the common case: fail-open, actor from context, all
commands audited. Customize as needed:

```go
cfg := mink.DefaultAuditConfig(auditStore)
cfg.SkipCommands = []string{"HealthCheck", "Ping"}  // high-frequency, low-value
cfg.IncludeMetadata = true                          // capture command metadata
bus.Use(mink.AuditMiddleware(cfg))
```

`IncludeMetadata` copies the command's metadata map when the command exposes
`GetMetadataMap() map[string]string` (commands embedding `mink.CommandBase` do).

---

## Capturing the actor

There is no implicit "current user" in go-mink, so you tell the middleware how to
resolve the actor. Two mechanisms:

**1. From the context** (default). Set the actor upstream — for example in your
auth middleware or HTTP handler — and the default `ActorFunc` reads it:

```go
ctx = mink.WithActor(ctx, "user-42")
// ... dispatch; AuditEntry.Actor == "user-42"
```

**2. A custom `ActorFunc`** — derive the actor from the context or the command:

```go
cfg := mink.DefaultAuditConfig(auditStore)
cfg.ActorFunc = func(ctx context.Context, cmd mink.Command) string {
    return principalFromContext(ctx).Email()
}
```

```go
type ActorFunc func(ctx context.Context, cmd Command) string
```

`mink.ActorFromContext(ctx)` reads whatever `mink.WithActor` set.

---

## Failure semantics

The audit write happens **after** the command runs. The `FailClosed` flag controls
what happens if that write fails:

- **Fail-open (default)** — a store error is ignored and the original command
  result is returned. Auditing never breaks command processing.
- **Fail-closed** (`FailClosed: true`) — a store error is surfaced as the command
  result/error, so callers can react (e.g. reject the request for compliance).

:::warning Auditing is not transactional
Because the audit entry is written *after* the command, `FailClosed` surfaces the
audit-write failure but **the command's side effect has already happened**. Use
`FailClosed` to *detect and react to* audit-store outages, not as a guarantee that
an un-auditable command never runs. For a hard transactional guarantee, write your
own audit record inside the same database transaction as your aggregate.
:::

---

## Placement in the middleware chain

Middleware runs in registration order (the first registered is the outermost), and
Go contexts flow **inward** — a middleware enriches the context only for what it
wraps, never for the middleware above it. `AuditMiddleware` reads
`Actor`/`TenantID`/`CorrelationID`/`CausationID` from the context it *receives*, so
register it **after** the middleware that populates those values, but still
**before** validation and the handler so it captures their outcomes and the full
wall-clock duration:

```go
bus.Use(
    mink.RecoveryMiddleware(),                                  // 1. catch panics
    mink.CorrelationIDMiddleware(uuid.NewString),               // 2. populate context FIRST...
    // ...plus any causation / tenant / auth (WithActor) middleware...
    mink.AuditMiddleware(mink.DefaultAuditConfig(auditStore)),  // 3. reads the enriched context,
    mink.ValidationMiddleware(),                                //    records the outcomes below
    // ... idempotency, timeout, handler
)
```

Registering `AuditMiddleware` *before* the context-setting middleware would leave
`CorrelationID`, `TenantID`, and the actor empty in the recorded entries.

The audit middleware does **not** recover panics itself. To audit a handler that
*panics*, place `RecoveryMiddleware` *inside* the audit middleware so the panic is
converted to an error result before the entry is written:

```go
bus.Use(mink.ChainMiddleware(
    mink.AuditMiddleware(cfg),
    mink.RecoveryMiddleware(),
))
```

---

## Querying the trail

`Find` and `Count` take an `AuditQuery`. Empty/zero fields are ignored, so a
zero-value query returns everything (the PostgreSQL store caps results at 100 by
default; the in-memory store returns all unless `Limit` is set).

```go
type AuditQuery struct {
    CommandType string      // exact match
    Actor       string      // exact match
    TenantID    string      // exact match
    AggregateID string      // exact match
    From        time.Time   // Timestamp >= From (inclusive)
    To          time.Time   // Timestamp <  To  (exclusive)
    Success     *bool       // nil = both; &true = successes; &false = failures
    Limit       int         // <= 0 means the store default
    Offset      int         // for pagination
    Order       AuditOrder  // AuditOrderTimestampDesc (default) | AuditOrderTimestampAsc
}
```

```go
// Everything actor "user-42" did to a specific order, newest first.
entries, _ := store.Find(ctx, mink.AuditQuery{
    Actor:       "user-42",
    AggregateID: "order-123",
})

// Failed commands in the last 24h, paginated.
failed := false
page1, _ := store.Find(ctx, mink.AuditQuery{
    Success: &failed,
    From:    time.Now().Add(-24 * time.Hour),
    Limit:   100, Offset: 0,
    Order:   mink.AuditOrderTimestampAsc,
})

// Count for a compliance report.
n, _ := store.Count(ctx, mink.AuditQuery{CommandType: "ExportUserData"})
```

`Count` ignores `Limit`/`Offset`. Filters are combined with AND.

---

## Retention

Audit trails grow without bound. Trim old entries with `Cleanup`, typically from a
scheduled job:

```go
// Remove entries older than 365 days; returns the number deleted.
removed, _ := store.Cleanup(ctx, 365*24*time.Hour)
```

Check your retention obligations before pruning — many regimes mandate minimum
retention periods.

---

## PostgreSQL store

For production, use the PostgreSQL store (`go-mink.dev/adapters/postgres`):

```go
import "go-mink.dev/adapters/postgres"

// Share the event-store adapter's connection (recommended):
auditStore := postgres.NewAuditStoreFromAdapter(adapter)
// or a standalone connection:
//   auditStore := postgres.NewAuditStore(db, postgres.WithAuditTable("mink_audit"))

if err := auditStore.Initialize(ctx); err != nil {   // creates the table + indexes
    log.Fatal(err)
}
defer auditStore.Close()
```

:::note Initialize is required
`PostgresAdapter.Migrate` does **not** create the audit table. Call
`auditStore.Initialize(ctx)` once at startup. Options: `WithAuditSchema`,
`WithAuditTable` (default table `mink_audit`).
:::

The store auto-creates this table (`Initialize` is idempotent):

```sql
CREATE TABLE IF NOT EXISTS mink_audit (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    timestamp      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    command_type   VARCHAR(255) NOT NULL,
    command_id     VARCHAR(255),
    aggregate_id   VARCHAR(255),
    version        BIGINT,
    actor          VARCHAR(255),
    tenant_id      VARCHAR(255),
    correlation_id VARCHAR(255),
    causation_id   VARCHAR(255),
    success        BOOLEAN NOT NULL,
    error          TEXT,
    duration_ms    BIGINT NOT NULL DEFAULT 0,
    metadata       JSONB
);
-- indexes on: timestamp, command_type, actor, tenant_id, aggregate_id, correlation_id
```

The indexes back the `AuditQuery` filters. The table is **append-only** — the store
never updates or deletes entries except via `Cleanup`.

---

## In-memory store

`memory.NewAuditStore()` implements the same `AuditStore` interface with no
external dependencies. It is ideal for tests and local development but does **not**
persist across restarts — never use it in production.

---

## Multi-tenancy

When `TenantMiddleware` (or `mink.WithTenantID`) populates the tenant on the
context, every audit entry records its `TenantID`, and you can scope queries per
tenant:

```go
entries, _ := store.Find(ctx, mink.AuditQuery{TenantID: "tenant-acme"})
```

See [Multi-tenancy via Metadata](/docs/adr/multi-tenancy).

---

## Integration with GDPR & compliance

Audit logging complements the other [Security & Compliance](/docs/advanced/security)
features:

- It records the **commands** that touched data — pair it with **data export**
  (right to access) and **crypto-shredding** (right to erasure) for a complete
  compliance story.
- It captures intent (the command), which database-level auditing cannot.

:::warning Audit logs can contain sensitive data
`error` messages and captured `metadata` may include PII. Treat the audit table as
sensitive: restrict access, and avoid placing secrets in command metadata if you
enable `IncludeMetadata`.
:::

---

## Testing

The in-memory store makes assertions trivial:

```go
store := memory.NewAuditStore()
bus := mink.NewCommandBus()
bus.Use(mink.AuditMiddleware(mink.DefaultAuditConfig(store)))
// ... dispatch

entries, _ := store.Find(context.Background(), mink.AuditQuery{})
require.Len(t, entries, 1)
require.Equal(t, "CreateOrder", entries[0].CommandType)
require.True(t, entries[0].Success)
```

A complete, runnable example (no database required) lives in
[`examples/audit`](https://github.com/AshkanYarmoradi/go-mink/tree/main/examples/audit).

---

## Best practices

- **Audit intent, not noise.** Skip high-frequency, low-value commands
  (`HealthCheck`, `Ping`) via `SkipCommands`.
- **Set the actor early.** Resolve the principal in your auth layer and
  `mink.WithActor(ctx, id)` before dispatch, or supply an `ActorFunc`.
- **Default to fail-open.** Reserve `FailClosed` for flows where an un-recorded
  command is itself a compliance violation — and remember it is not transactional.
- **Plan retention and storage.** Index growth is real; schedule `Cleanup` to your
  retention policy and budget storage accordingly.
- **Guard the trail.** Restrict read access; the audit table is sensitive.

---

Next: [Security & Compliance →](/docs/advanced/security)
