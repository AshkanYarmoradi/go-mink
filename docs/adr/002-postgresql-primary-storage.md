---
layout: default
title: "ADR-002: PostgreSQL as Primary Storage"
parent: Architecture Decision Records
nav_order: 2
---

# ADR-002: PostgreSQL as Primary Storage

| Status | Date | Deciders |
|--------|------|----------|
| Accepted | 2024-01-15 | Core Team |

## Context

go-mink needs a reliable, production-ready storage backend for events. The storage must support:

1. **ACID Transactions**: Atomic event appends with version checks
2. **Optimistic Concurrency**: Detect concurrent modifications
3. **Efficient Queries**: Load events by stream, position, or time
4. **Subscriptions**: Support for change notifications
5. **Scalability**: Handle millions of events
6. **Operational Maturity**: Well-understood operations, backup, monitoring

We need to choose between:
- Dedicated event stores (EventStoreDB)
- Relational databases (PostgreSQL, MySQL)
- Document databases (MongoDB)
- Custom solutions

## Decision

We will use **PostgreSQL** as the primary storage backend with a custom schema optimized for event sourcing.

### Schema Design

```sql
CREATE TABLE mink_events (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    stream_id VARCHAR(255) NOT NULL,
    version BIGINT NOT NULL,
    type VARCHAR(255) NOT NULL,
    data JSONB NOT NULL,
    metadata JSONB DEFAULT '{}',
    global_position BIGSERIAL,
    timestamp TIMESTAMPTZ DEFAULT NOW(),
    
    CONSTRAINT unique_stream_version UNIQUE (stream_id, version)
);

CREATE INDEX idx_events_stream_id ON mink_events(stream_id);
CREATE INDEX idx_events_global_position ON mink_events(global_position);
CREATE INDEX idx_events_type ON mink_events(type);
CREATE INDEX idx_events_timestamp ON mink_events(timestamp);
```

### Key Features Used

1. **JSONB**: Flexible event data storage with indexing
2. **BIGSERIAL**: Global ordering via `global_position`
3. **UNIQUE Constraint**: Optimistic concurrency via `(stream_id, version)`
4. **LISTEN/NOTIFY**: Real-time subscriptions

### Concurrency Control

```sql
-- Atomic append with version check
INSERT INTO mink_events (stream_id, version, type, data, metadata)
SELECT $1, COALESCE(MAX(version), 0) + 1, $2, $3, $4
FROM mink_events
WHERE stream_id = $1
HAVING COALESCE(MAX(version), 0) = $5
RETURNING *;
```

## Consequences

### Positive

1. **Familiar Technology**: Most teams already know PostgreSQL
2. **Operational Maturity**: Proven backup, replication, monitoring
3. **JSONB Power**: Flexible schema with query capabilities
4. **Transaction Support**: Can combine event writes with other operations
5. **Cloud Availability**: Available on all major cloud providers
6. **No Vendor Lock-in**: Open source, portable
7. **Cost Effective**: No additional licensing for event store
8. **Ecosystem**: Rich tooling (pgAdmin, pg_dump, etc.)

### Negative

1. **Not Purpose-Built**: Less optimized than dedicated event stores
2. **Subscription Limitations**: LISTEN/NOTIFY has message size limits
3. **Partitioning Complexity**: Manual partitioning for very large datasets
4. **No Built-in Projections**: Must implement projection engine ourselves

### Neutral

1. **Schema Management**: Need to handle migrations
2. **Connection Pooling**: Standard PostgreSQL considerations apply

## Alternatives Considered

### Alternative 1: EventStoreDB

**Description**: Purpose-built event store database.

**Pros**:
- Optimized for event sourcing
- Built-in subscriptions
- Projections in JavaScript

**Rejected because**:
- Additional infrastructure component
- Less familiar to most teams
- Licensing considerations for enterprise features
- Smaller community in Go ecosystem

### Alternative 2: MongoDB

**Description**: Document database with change streams.

**Pros**:
- Flexible schema
- Good Go driver
- Change streams for subscriptions

**Rejected because**:
- Weaker transaction guarantees (improved in recent versions)
- Less suitable for strict ordering requirements
- JSONB in PostgreSQL provides similar flexibility

### Alternative 3: Apache Kafka

**Description**: Distributed event streaming platform.

**Pros**:
- Excellent for high-throughput
- Built-in partitioning
- Strong ecosystem

**Rejected because**:
- Complex infrastructure
- Not designed for entity-level streams
- Retention policies complicate event sourcing
- Overkill for many use cases

### Alternative 4: Custom File-Based Store

**Description**: Append-only log files.

**Rejected because**:
- Would need to build everything from scratch
- No query capabilities
- Complex replication/backup
- Not suitable for production

## Implementation Notes

### Connection Pooling

We use `pgx` for connection pooling:

```go
config, _ := pgxpool.ParseConfig(connStr)
config.MaxConns = 25
config.MinConns = 5
pool, _ := pgxpool.NewWithConfig(ctx, config)
```

### Subscription via Polling

For reliable subscriptions, we use polling with checkpoints rather than LISTEN/NOTIFY:

```go
func (s *Subscriber) Poll(ctx context.Context, fromPosition uint64) ([]StoredEvent, error) {
    return s.db.Query(ctx, `
        SELECT * FROM mink_events 
        WHERE global_position > $1 
        ORDER BY global_position 
        LIMIT 100
    `, fromPosition)
}
```

## References

- [PostgreSQL JSONB Documentation](https://www.postgresql.org/docs/current/datatype-json.html)
- [Marten's PostgreSQL Usage](https://martendb.io/documents/storage.html)
- [pgx Driver](https://github.com/jackc/pgx)
- [Event Sourcing with PostgreSQL](https://dev.to/oliamb/event-sourcing-with-postgresql-28d1)
