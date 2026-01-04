---
layout: default
title: "ADR-010: Multi-tenancy via Metadata"
parent: Architecture Decision Records
nav_order: 10
---

# ADR-010: Multi-tenancy via Metadata

| Status | Date | Deciders |
|--------|------|----------|
| Accepted | 2024-02-20 | Core Team |

## Context

Many applications need to support multiple tenants (customers, organizations) while:

1. **Data Isolation**: Tenants cannot see each other's data
2. **Query Scoping**: Queries automatically filter by tenant
3. **Flexibility**: Support different isolation strategies
4. **Performance**: Tenant filtering should be efficient

Common multi-tenancy approaches:
- **Database per tenant**: Complete isolation, complex operations
- **Schema per tenant**: Good isolation, schema management overhead
- **Shared table with tenant column**: Simpler, requires careful filtering
- **Shared table with metadata**: Flexible, works with event sourcing

## Decision

We will implement multi-tenancy via **event metadata** with middleware-based enforcement.

### Metadata-Based Tenant Tracking

Every event carries tenant information in metadata:

```go
type Metadata struct {
    CorrelationID string            `json:"correlationId,omitempty"`
    CausationID   string            `json:"causationId,omitempty"`
    UserID        string            `json:"userId,omitempty"`
    TenantID      string            `json:"tenantId,omitempty"`  // Tenant identifier
    Custom        map[string]string `json:"custom,omitempty"`
}
```

### Tenant Context

```go
type tenantKey struct{}

// WithTenant adds tenant ID to context
func WithTenant(ctx context.Context, tenantID string) context.Context {
    return context.WithValue(ctx, tenantKey{}, tenantID)
}

// TenantFromContext retrieves tenant ID from context
func TenantFromContext(ctx context.Context) (string, bool) {
    tenantID, ok := ctx.Value(tenantKey{}).(string)
    return tenantID, ok
}
```

### Tenant Middleware

```go
// TenantMiddleware enforces tenant isolation on commands
func TenantMiddleware() Middleware {
    return func(next MiddlewareFunc) MiddlewareFunc {
        return func(ctx context.Context, cmd Command) (CommandResult, error) {
            tenantID, ok := TenantFromContext(ctx)
            if !ok {
                return CommandResult{}, NewValidationError("", "tenant ID required")
            }
            
            // Inject tenant into command metadata if supported
            if tm, ok := cmd.(TenantAware); ok {
                tm.SetTenantID(tenantID)
            }
            
            return next(ctx, cmd)
        }
    }
}

// TenantAware interface for commands that support tenancy
type TenantAware interface {
    SetTenantID(tenantID string)
    GetTenantID() string
}
```

### Event Store Integration

Events automatically include tenant from context:

```go
func (s *EventStore) Append(ctx context.Context, streamID string, events []EventData, expectedVersion int64) ([]StoredEvent, error) {
    // Inject tenant ID into event metadata
    tenantID, _ := TenantFromContext(ctx)
    
    for i := range events {
        if events[i].Metadata.TenantID == "" {
            events[i].Metadata.TenantID = tenantID
        }
    }
    
    return s.adapter.Append(ctx, streamID, events, expectedVersion)
}
```

### Tenant-Scoped Queries

```go
// Subscribe with tenant filter
func (s *EventStore) SubscribeByTenant(ctx context.Context, tenantID string, fromPosition uint64) (<-chan StoredEvent, error) {
    return s.adapter.Subscribe(ctx, fromPosition, EventFilter{
        TenantID: tenantID,
    })
}

// Query projections by tenant
func (r *Repository) QueryByTenant(ctx context.Context, tenantID string, query Query) ([]*T, error) {
    return r.Query(ctx, query.Where("TenantID", Eq, tenantID))
}
```

### Stream Naming Convention

Optionally prefix streams with tenant ID:

```go
// Stream ID includes tenant
func StreamID(tenantID, aggregateType, aggregateID string) string {
    return fmt.Sprintf("%s/%s-%s", tenantID, aggregateType, aggregateID)
}

// Example: "tenant-123/order-456"
```

### PostgreSQL Index for Tenant Queries

```sql
-- Index for tenant-scoped queries
CREATE INDEX idx_events_tenant ON mink_events ((metadata->>'tenantId'));

-- Partial index for specific tenant (high-volume tenants)
CREATE INDEX idx_events_tenant_abc ON mink_events (global_position) 
WHERE metadata->>'tenantId' = 'tenant-abc';
```

## Consequences

### Positive

1. **Flexible**: Works with any isolation requirement
2. **Simple**: No database or schema changes per tenant
3. **Queryable**: Can query across tenants if needed (admin)
4. **Auditable**: Tenant info in every event
5. **Portable**: Same approach works across databases

### Negative

1. **Query Overhead**: Every query must include tenant filter
2. **Bug Risk**: Forgetting tenant filter exposes data
3. **Index Size**: Tenant index adds storage overhead
4. **No Physical Isolation**: All data in same tables

### Neutral

1. **Migration**: Can add tenancy to existing systems
2. **Testing**: Need to test tenant isolation explicitly

## Isolation Strategies

### Strategy 1: Metadata Only (Default)

All tenants share tables, isolated by metadata filter.

```go
// Good for: Most SaaS applications
store := mink.New(adapter)
bus.Use(mink.TenantMiddleware())
```

### Strategy 2: Stream Prefix

Tenant ID in stream name provides implicit isolation.

```go
// Good for: Clear tenant separation in stream names
streamID := fmt.Sprintf("%s/%s", tenantID, aggregateID)
```

### Strategy 3: Schema per Tenant

Use PostgreSQL schemas for isolation.

```go
// Good for: Regulatory requirements, large tenants
adapter := postgres.NewAdapter(connStr, 
    postgres.WithSchema(tenantID))
```

### Strategy 4: Database per Tenant

Separate database connections per tenant.

```go
// Good for: Maximum isolation, enterprise customers
adapters := map[string]*postgres.Adapter{
    "tenant-1": postgres.NewAdapter(connStr1),
    "tenant-2": postgres.NewAdapter(connStr2),
}
```

## Example Implementation

```go
// HTTP middleware to extract tenant
func TenantExtractor(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        // Extract from header, JWT, subdomain, etc.
        tenantID := r.Header.Get("X-Tenant-ID")
        if tenantID == "" {
            http.Error(w, "Tenant ID required", http.StatusBadRequest)
            return
        }
        
        ctx := mink.WithTenant(r.Context(), tenantID)
        next.ServeHTTP(w, r.WithContext(ctx))
    })
}

// Command handler with tenant enforcement
func (h *OrderHandler) CreateOrder(ctx context.Context, cmd CreateOrderCommand) (CommandResult, error) {
    tenantID, ok := mink.TenantFromContext(ctx)
    if !ok {
        return CommandResult{}, errors.New("tenant required")
    }
    
    order := NewOrder(cmd.OrderID)
    order.TenantID = tenantID
    
    if err := order.Create(cmd.CustomerID, cmd.Items); err != nil {
        return CommandResult{}, err
    }
    
    return h.store.SaveAggregate(ctx, order)
}

// Projection respects tenant
func (p *OrderSummaryProjection) Apply(ctx context.Context, event StoredEvent) error {
    tenantID := event.Metadata.TenantID
    
    switch event.Type {
    case "OrderCreated":
        var e OrderCreated
        json.Unmarshal(event.Data, &e)
        
        return p.repo.Insert(ctx, &OrderSummary{
            TenantID: tenantID,  // Always include tenant
            OrderID:  e.OrderID,
            // ...
        })
    }
    return nil
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
- Can be added for sensitive data

## References

- [Multi-tenant SaaS Patterns](https://docs.microsoft.com/en-us/azure/architecture/guide/multitenant/overview)
- [PostgreSQL Row-Level Security](https://www.postgresql.org/docs/current/ddl-rowsecurity.html)
- [Event Sourcing Multi-tenancy](https://eventstore.com/blog/multi-tenancy-in-event-sourcing-systems/)
