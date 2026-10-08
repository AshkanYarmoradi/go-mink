---
title: "ADR-010: Multi-tenancy via Metadata"
sidebar_position: 10
---

# ADR-010: Multi-tenancy via Metadata

| Status | Date | Deciders |
|--------|------|----------|
| Accepted — revised 2026-10-05 | 2024-02-20 | Core Team |

:::warning Revision note (2026-10-05)
An earlier version of this ADR described `WithTenant` / `TenantFromContext`, a `TenantAware` command interface, `SubscribeByTenant`, and an "automatic tenant injection" inside `EventStore.Append` — none of which were ever shipped — and it took the tenant id from a client-supplied `X-Tenant-ID` header. Both were security-relevant inaccuracies: the first overstated what the library enforces, the second described an isolation boundary any client could bypass by sending a different header. The text below matches the code (`middleware.go`, `event.go`, `export.go`, `encryption.go`, `idempotency.go`, `audit.go`).
:::

## Context

Many applications need to support multiple tenants (customers, organizations) while:

1. **Data Isolation**: Tenants cannot see each other's data
2. **Query Scoping**: Queries filter by tenant
3. **Flexibility**: Support different isolation strategies
4. **Performance**: Tenant filtering should be efficient
5. **Trust boundary**: The tenant a request acts on is decided by the server from the authenticated identity. A tenant id the client can choose freely is not an isolation boundary

Common multi-tenancy approaches:
- **Database per tenant**: Complete isolation, complex operations
- **Schema per tenant**: Good isolation, schema management overhead
- **Shared table with tenant column**: Simpler, requires careful filtering
- **Shared table with metadata**: Flexible, works with event sourcing

## Decision

We implement multi-tenancy via **event metadata** (`Metadata.TenantID`). The tenant id is established **server-side from the authenticated principal**, carried in `context.Context`, and stamped on events and read models **explicitly by the application**. go-mink provides the carrier, a command-bus middleware, and tenant-aware filters and key selection; it does **not** enforce isolation on its own.

### What go-mink ships

```go
// event.go — tenant is a first-class metadata field persisted with every event
type Metadata struct {
    CorrelationID string            `json:"correlationId,omitempty"`
    CausationID   string            `json:"causationId,omitempty"`
    UserID        string            `json:"userId,omitempty"`
    TenantID      string            `json:"tenantId,omitempty"`
    Custom        map[string]string `json:"custom,omitempty"`
}
func (m Metadata) WithTenantID(id string) Metadata

// middleware.go — context carrier + command-bus middleware
func WithTenantID(ctx context.Context, tenantID string) context.Context
func TenantIDFromContext(ctx context.Context) string // "" when unset
func TenantMiddleware(extractor func(Command) string, required bool) Middleware

// export.go — GDPR export filter on Metadata.TenantID
func FilterByTenantID(tenantID string) ExportFilter

// encryption.go — per-tenant master keys (an erasure's blast radius is then the tenant)
func WithTenantKeyResolver(resolver func(tenantID string) string) EncryptionOption
```

Inside the library the tenant id feeds exactly these places:

| Component | What it does with the tenant |
|-----------|------------------------------|
| `IdempotencyMiddleware` | `DefaultIdempotencyScope` prefixes every idempotency key with `TenantIDFromContext(ctx)` (`tenant\|key`), so two principals presenting the same client-supplied key cannot replay or suppress each other's commands. |
| `AuditMiddleware` | Records `TenantIDFromContext(ctx)` on every `AuditEntry`. |
| Field-level encryption | `WithTenantKeyResolver` maps the **event's** `Metadata.TenantID` (not the context) to the master key that wraps its fields. |

### What go-mink does NOT do (the application must)

1. **No automatic injection into events.** `EventStore.Append` and `SaveAggregate` never read the tenant from the context. Stamp it explicitly:

   ```go
   tenantID := mink.TenantIDFromContext(ctx) // set by your authentication layer, see below
   err := store.Append(ctx, streamID, events,
       mink.WithAppendMetadata(mink.Metadata{}.WithTenantID(tenantID)))
   ```

   An event appended without this has an empty `TenantID` — and, with `WithTenantKeyResolver`, is encrypted under the *default* key (or a subject-derived key, if a `SubjectTagger` tagged it) rather than the tenant's. `TenantMiddleware(nil, true)` rejects a command when no tenant is known, but it cannot see what your handler writes afterwards.

2. **No tenant-scoped reads.** There is no `SubscribeByTenant` and no tenant-aware repository. Projections receive every tenant's events, so each projection stores `event.Metadata.TenantID` in its read model and every query filters on it. `FilterByTenantID` exists only for `DataExporter`.

3. **No authentication.** `TenantMiddleware` trusts what it is given:
   - If the context **already carries** a tenant id it is used as-is (the middleware short-circuits). This is the intended path: your authentication layer calls `WithTenantID` *after* verifying the principal.
   - Otherwise it calls `extractor(cmd)` to read a tenant id **from the command**. A command is deserialized from the request body, so this path is only safe when the extractor validates the value against the authenticated principal, or when commands come from a trusted internal caller. With `required == true` a missing id fails validation (`NewValidationError(cmd.CommandType(), "tenantId", "tenant ID is required")`).

### The trust boundary

The tenant id MUST be derived server-side from the authenticated identity — a verified JWT or session claim, an mTLS client certificate, the owner record of an API key — and set with `mink.WithTenantID` **before** the command bus runs. It MUST NOT be taken from a client-controlled value (an `X-Tenant-ID` header, a query parameter, a command field) unless that value is checked against the tenants the principal is authorized for. A client that can pick its own tenant id can read and write any tenant's data: every filter below would faithfully scope to the tenant *the attacker chose*.

```go
// HTTP middleware: derive the tenant from the verified principal, never from the request.
func TenantFromPrincipal(auth Authenticator, next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        principal, err := auth.Verify(r) // validates the bearer token / session cookie
        if err != nil {
            http.Error(w, "unauthorized", http.StatusUnauthorized)
            return
        }
        // principal.TenantID comes from the token's verified claims or your user store —
        // not from r.Header. If a principal may act in several tenants, accept a client
        // *hint* only after confirming it is in principal.AllowedTenants.
        ctx := mink.WithTenantID(r.Context(), principal.TenantID)
        next.ServeHTTP(w, r.WithContext(ctx))
    })
}
```

Order the bus so the tenant is known before anything that depends on it:

```go
bus := mink.NewCommandBus()
bus.Use(
    mink.TenantMiddleware(nil, true),         // fail closed: no verified tenant, no command
    mink.IdempotencyMiddleware(idempConfig),  // keys are now tenant-scoped
    mink.AuditMiddleware(auditConfig),        // entries carry the tenant
)
```

### Stream Naming Convention

Optionally prefix streams with the tenant id so a cross-tenant read cannot even be expressed:

```go
func StreamID(tenantID, aggregateType, aggregateID string) string {
    return fmt.Sprintf("%s/%s-%s", tenantID, aggregateType, aggregateID)
}
// Example: "tenant-123/order-456"
```

### PostgreSQL Index for Tenant Queries

```sql
-- Index for tenant-scoped queries over the events table
CREATE INDEX idx_events_tenant ON events ((metadata->>'tenantId'));

-- Partial index for a specific high-volume tenant
CREATE INDEX idx_events_tenant_abc ON events (global_position)
WHERE metadata->>'tenantId' = 'tenant-abc';
```

## Consequences

### Positive

1. **Flexible**: Works with any isolation requirement
2. **Simple**: No database or schema changes per tenant
3. **Queryable**: Can query across tenants if needed (admin)
4. **Auditable**: Tenant info in every event and audit entry
5. **Portable**: Same approach works across databases

### Negative

1. **Query Overhead**: Every query must include the tenant filter
2. **Bug Risk**: Forgetting to stamp or filter exposes data — the library does not catch it
3. **Index Size**: Tenant index adds storage overhead
4. **No Physical Isolation**: All data in the same tables
5. **Shared-key blast radius**: With `WithTenantKeyResolver`, crypto-shredding one subject revokes the whole tenant's key (see `WithSharedKeyGuard`)

### Neutral

1. **Migration**: Can add tenancy to existing systems
2. **Testing**: Tenant isolation must be tested explicitly — a test that submits a command for tenant B with tenant A's credentials should fail

## Isolation Strategies

### Strategy 1: Metadata Only (Default)

All tenants share tables; isolation is the application's explicit stamping of, and filtering on, `Metadata.TenantID`.

```go
// Good for: Most SaaS applications
store := mink.New(adapter)
bus := mink.NewCommandBus()
bus.Use(mink.TenantMiddleware(nil, true)) // tenant set by the auth layer via WithTenantID
```

### Strategy 2: Stream Prefix

Tenant id in the stream name provides implicit isolation per stream.

```go
// Good for: Clear tenant separation in stream names
streamID := fmt.Sprintf("%s/%s", tenantID, aggregateID)
```

### Strategy 3: Schema per Tenant

Use PostgreSQL schemas for isolation (one adapter per tenant, selected by the authenticated tenant).

```go
// Good for: Regulatory requirements, large tenants
adapter, err := postgres.NewAdapter(connStr, postgres.WithSchema(tenantID))
```

### Strategy 4: Database per Tenant

Separate database connections per tenant.

```go
// Good for: Maximum isolation, enterprise customers
adapters := map[string]*postgres.Adapter{
    "tenant-1": adapter1, // postgres.NewAdapter(connStr1)
    "tenant-2": adapter2, // postgres.NewAdapter(connStr2)
}
adapter := adapters[mink.TenantIDFromContext(ctx)] // the verified tenant picks the store
```

## Example Implementation

```go
// Command handler: the tenant comes from the context the auth layer populated.
func (h *OrderHandler) CreateOrder(ctx context.Context, cmd CreateOrderCommand) (mink.CommandResult, error) {
    tenantID := mink.TenantIDFromContext(ctx)
    if tenantID == "" {
        return mink.CommandResult{}, errors.New("tenant required") // unreachable behind TenantMiddleware(nil, true)
    }

    order := NewOrder(cmd.OrderID)
    if err := order.Create(cmd.CustomerID, cmd.Items); err != nil {
        return mink.CommandResult{}, err
    }

    // Stamp the tenant explicitly — the store does not read it from ctx.
    md := mink.Metadata{}.WithTenantID(tenantID).WithUserID(cmd.UserID)
    if err := h.store.Append(ctx, StreamID(tenantID, "Order", order.AggregateID()),
        order.UncommittedEvents(), mink.ExpectVersion(mink.NoStream), mink.WithAppendMetadata(md)); err != nil {
        return mink.CommandResult{}, err
    }
    order.ClearUncommittedEvents()
    return mink.NewSuccessResult(order.AggregateID(), order.Version()), nil
}

// Projection: store the tenant with every row and filter on it in every query.
// StoredEvent.Data is the serialized payload ([]byte): dispatch on the event type name and
// deserialize it (or run store.ProcessStoredEvent for the decrypt → upcast → deserialize pipeline).
func (p *OrderSummaryProjection) Apply(ctx context.Context, event mink.StoredEvent) error {
    switch event.Type {
    case "OrderCreated":
        var e OrderCreated
        if err := json.Unmarshal(event.Data, &e); err != nil {
            return err
        }
        return p.repo.Insert(ctx, &OrderSummary{
            TenantID: event.Metadata.TenantID, // always include the tenant
            OrderID:  e.OrderID,
        })
    }
    return nil
}

// Query: the tenant comes from the verified context, never from the request.
func (q *OrderQueries) ListOrders(ctx context.Context) ([]OrderSummary, error) {
    return q.repo.FindByTenant(ctx, mink.TenantIDFromContext(ctx))
}
```

## Alternatives Considered

### Alternative 1: Row-Level Security (PostgreSQL)

**Description**: Use PostgreSQL RLS policies.

**Pros**:
- Database-enforced isolation
- Can't forget filter

**Rejected as primary because**:
- PostgreSQL-specific
- Complex policy management
- Performance overhead
- Can be added on top if needed

### Alternative 2: Separate Event Stores

**Description**: Different EventStore instance per tenant.

**Pros**:
- Complete isolation
- Easy to reason about

**Rejected as default because**:
- Resource overhead
- Complex routing
- Hard to query across tenants

### Alternative 3: Encryption per Tenant

**Description**: Encrypt events with tenant-specific keys.

**Pros**:
- Strong isolation
- Key-based access control

**Rejected as primary because**:
- Performance overhead
- Key management complexity
- Can be added for sensitive data (and is: `WithTenantKeyResolver` + field-level encryption)

## References

- [Multi-tenant SaaS Patterns](https://docs.microsoft.com/en-us/azure/architecture/guide/multitenant/overview)
- [PostgreSQL Row-Level Security](https://www.postgresql.org/docs/current/ddl-rowsecurity.html)
- [Event Sourcing Multi-tenancy](https://eventstore.com/blog/multi-tenancy-in-event-sourcing-systems/)
