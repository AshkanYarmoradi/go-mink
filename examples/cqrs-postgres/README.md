# CQRS with PostgreSQL Example

> The same CQRS order domain as [cqrs](../cqrs), but persisted durably to PostgreSQL.

This example swaps the in-memory adapters for their PostgreSQL counterparts, showing how the exact same commands, handlers, and middleware pipeline run unchanged against a real database. The payoff is durability: both the event store and the idempotency store survive process restarts, so replayed commands stay deduplicated and event history is permanent.

## What this demonstrates
- **PostgreSQL event store** — `postgres.NewAdapter` creates a durable `EventStoreAdapter`; `Initialize` auto-creates the schema and tables.
- **PostgreSQL idempotency store** — `postgres.NewIdempotencyStore` persists command keys so duplicate detection outlives restarts.
- **Custom schema** — `postgres.WithSchema` and `postgres.WithIdempotencySchema` place everything under a dedicated `mink_example` schema.
- **Unchanged domain layer** — the `Order` aggregate, commands, and full eight-middleware pipeline are identical to the in-memory `cqrs` example; only the adapters differ.
- **Durable idempotency & metadata** — the idempotency test and correlation-ID tracking are demonstrated end-to-end against Postgres.

## Running
```bash
# From the repo root: start PostgreSQL
docker compose -f docker-compose.test.yml up -d --wait

go run ./examples/cqrs-postgres

# Stop PostgreSQL when done
docker compose -f docker-compose.test.yml down
```

Requires PostgreSQL. The example reads `TEST_DATABASE_URL` and falls back to the default DSN `postgres://postgres:postgres@localhost:5432/mink_test?sslmode=disable`. Tables are created automatically under the `mink_example` schema on first run.

## What happens
The program runs ten numbered steps:

1. **Initialize adapters** — creates and `Initialize`s the PostgreSQL event store adapter and the idempotency store.
2. **Create event store** — wraps the adapter with `mink.New` and registers the event types.
3. **Set up the command bus** — builds the handler registry and command bus with eight middlewares (recovery, logging, metrics, correlation, causation, validation, PostgreSQL-backed idempotency, and a 30-second timeout).
4. **Execute commands** — dispatches `CreateOrderCommand`, then three `AddItemCommand`s (laptop, mouse, USB hub), printing the version after each.
5. **Idempotency (PostgreSQL)** — dispatches a fixed-ID command (`cmd-idempotent-postgres-test`) twice and shows the duplicate returns the result stored in Postgres.
6. **Validation** — dispatches an invalid `AddItemCommand` and prints the field errors from the `*mink.MultiValidationError`.
7. **Ship the order** — dispatches `ShipOrderCommand` with tracking number `TRACK-PG-12345`.
8. **Load final state** — reloads the order from PostgreSQL with `LoadAggregate` and prints the full order.
9. **Event stream** — reads events via `LoadRaw` and prints each version, type, and correlation ID.
10. **Metrics summary** — prints per-command statistics, followed by a recap of the key points demonstrated.

## Key APIs
- `postgres.NewAdapter(connStr, postgres.WithSchema("mink_example"))` — durable PostgreSQL `EventStoreAdapter`.
- `eventAdapter.Initialize(ctx)` — creates the event store schema/tables.
- `postgres.NewIdempotencyStore(db, postgres.WithIdempotencySchema(...), postgres.WithIdempotencyTable(...))` — PostgreSQL-backed idempotency store.
- `idempotencyStore.Initialize(ctx)` — creates the idempotency table.
- `mink.New(eventAdapter)` — event store over the PostgreSQL adapter.
- `mink.NewCommandBus`, `mink.NewHandlerRegistry`, `mink.RegisterGenericHandler`, `mink.NewAggregateHandler` with `mink.AggregateHandlerConfig` — same command/handler wiring as the `cqrs` example.
- `mink.IdempotencyMiddleware` + `mink.IdempotencyConfig` + `mink.GenerateIdempotencyKey` — configured here with the PostgreSQL store.
- `bus.Dispatch(ctx, cmd)`, `store.LoadAggregate`, `store.LoadRaw` — dispatch commands and read back state/events.

## Related
- **Examples:** [cqrs](../cqrs) · [basic](../basic) · [full-ecommerce](../full-ecommerce)
- **Docs:** [Adapters](https://go-mink.dev/docs/core/adapters) · [API reference](https://pkg.go.dev/go-mink.dev/adapters/postgres)
