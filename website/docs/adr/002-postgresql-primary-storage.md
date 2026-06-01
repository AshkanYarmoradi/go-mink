---
title: "ADR-002: PostgreSQL as Primary Storage"
sidebar_position: 2
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

The adapter uses a **two-table design**: a `streams` table that tracks the
current version (and category) of each stream, plus an `events` table that
stores the immutable event records. The `events` table uses `global_position`
(a `BIGSERIAL`) as its **primary key**, which gives both a compact row identity
and a total global ordering for catch-up subscriptions. The unique
`(stream_id, version)` constraint enforces optimistic concurrency. The schema is
configurable via `WithSchema`; the core table names are `streams` and `events`.

```sql
CREATE TABLE streams (
    id              BIGSERIAL PRIMARY KEY,
    stream_id       VARCHAR(500) NOT NULL UNIQUE,
    category        VARCHAR(250) NOT NULL,
    version         BIGINT NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_streams_category ON streams (category);

CREATE TABLE events (
    global_position BIGSERIAL PRIMARY KEY,
    stream_id       VARCHAR(500) NOT NULL,
    version         BIGINT NOT NULL,
    event_id        UUID NOT NULL DEFAULT gen_random_uuid(),
    event_type      VARCHAR(500) NOT NULL,
    data            JSONB NOT NULL,
    metadata        JSONB,
    timestamp       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (stream_id, version)
);

CREATE INDEX idx_events_stream ON events (stream_id, version);
CREATE INDEX idx_events_type ON events (event_type);
CREATE INDEX idx_events_timestamp ON events (timestamp);
```

The adapter also creates `snapshots` and `checkpoints` tables as part of the
core schema. Outbox, idempotency, and saga tables are created separately by
their respective sub-stores.

### Key Features Used

1. **JSONB**: Flexible event `data`/`metadata` storage with indexing
2. **BIGSERIAL primary key**: Global ordering and row identity via `global_position`
3. **UNIQUE Constraint**: Optimistic concurrency via `(stream_id, version)` on `events`
4. **`streams` table**: Authoritative current version per stream for fast version checks
5. **Polling subscriptions**: Catch-up via `global_position` (see below)

### Concurrency Control

Appends run inside a transaction that checks the stream's current version in the
`streams` table, inserts the new rows into `events`, and updates the stream
version — all atomically. A mismatch between the expected version and the
stored version aborts the transaction with a concurrency conflict:

```sql
-- Inside a single transaction:
-- 1. Read and lock the current version.
SELECT version FROM streams WHERE stream_id = $1 FOR UPDATE;

-- 2. If it matches the expected version, insert the new event(s).
INSERT INTO events (stream_id, version, event_type, data, metadata)
VALUES ($1, $2, $3, $4, $5)
RETURNING global_position;

-- 3. Bump the stream's version.
UPDATE streams SET version = $2, updated_at = NOW() WHERE stream_id = $1;
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
        SELECT * FROM events
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
