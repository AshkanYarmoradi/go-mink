# CQRS & Command Bus Example

> Dispatch commands through a full middleware pipeline into type-safe handlers backed by the event store.

This example layers CQRS on top of the basic event-sourced `Order`: instead of calling aggregate methods directly, callers send commands (`CreateOrderCommand`, `AddItemCommand`, `ShipOrderCommand`) through a `CommandBus`. The bus threads every command through a middleware pipeline — recovery, logging, metrics, correlation/causation tracking, validation, idempotency, and timeout — which is how you get cross-cutting concerns without cluttering domain logic.

## What this demonstrates
- **Commands and validation** — each command embeds `mink.CommandBase` and implements `Validate()`, using `mink.NewValidationError` and `mink.NewMultiValidationError` to report field-level problems.
- **Two handler styles** — `mink.RegisterGenericHandler` handles `CreateOrder` (which mints a new aggregate), while `mink.NewAggregateHandler` with `AggregateHandlerConfig` wires `AddItem` and `ShipOrder` to load, mutate, and save an existing `Order`.
- **Middleware pipeline** — `mink.NewCommandBus` composes eight middlewares via `WithMiddleware`, applied in order to every dispatch.
- **Idempotency** — `IdempotencyMiddleware` (backed by `memory.NewIdempotencyStore`) returns a cached result when the same `CommandID` is dispatched twice.
- **Correlation & causation** — commands carry `CorrelationID`/`CausationID` in `CommandBase`, which the middleware stamps onto stored event metadata for traceability.
- **Metrics** — a custom `SimpleMetrics` (implementing `mink.MetricsCollector`) tallies command counts and success/failure per type.

## Running
```bash
go run ./examples/cqrs
```
No infrastructure required — uses the in-memory adapter.

## What happens
The program runs eight numbered scenarios:

1. **Create an order** — dispatches `CreateOrderCommand` (command `cmd-001`, correlation `request-123`); the generic handler generates an order ID, saves the aggregate, and returns a `CommandResult` with the new ID and version.
2. **Add items** — dispatches two `AddItemCommand`s through the aggregate handler, each carrying a `CausationID` linking it to the previous command, and prints the resulting version.
3. **Validation** — dispatches an invalid `AddItemCommand` (empty SKU, negative quantity); the validation middleware rejects it and the example prints each field error from the `*mink.MultiValidationError`.
4. **Idempotency** — dispatches the same command (`cmd-idempotent-001`) twice and shows the duplicate returns the cached result rather than re-executing.
5. **Ship the order** — dispatches `ShipOrderCommand` with a tracking number and prints the new version.
6. **Load final state** — reloads the `Order` with `LoadAggregate` and prints customer, status, tracking, items, total, and version.
7. **Event stream with metadata** — reads events via `LoadRaw` and prints each event's version, type, and any `CorrelationID`/`CausationID` from its metadata.
8. **Metrics** — prints the collected per-command statistics.

## Key APIs
- `mink.NewCommandBus(mink.WithHandlerRegistry(...), mink.WithMiddleware(...))` — builds the command bus.
- `mink.NewHandlerRegistry()` — registry that maps command types to handlers.
- `mink.RegisterGenericHandler(registry, fn)` — registers a function handler returning `(mink.CommandResult, error)`.
- `mink.NewAggregateHandler(mink.AggregateHandlerConfig[Cmd, *Agg]{...})` — load/mutate/save handler for aggregate commands.
- `mink.CommandBase` — embeddable fields (`CommandID`, `CorrelationID`, `CausationID`) for commands.
- `bus.Dispatch(ctx, cmd)` — runs a command through the pipeline and returns a `CommandResult`.
- `mink.NewSuccessResult(id, version)` / `mink.NewErrorResult(err)` — construct command results.
- Middleware: `mink.RecoveryMiddleware`, `mink.NewLoggingMiddleware(logger).Middleware()`, `mink.MetricsMiddleware`, `mink.CorrelationIDMiddleware`, `mink.CausationIDMiddleware`, `mink.ValidationMiddleware`, `mink.IdempotencyMiddleware`, `mink.TimeoutMiddleware`.
- `mink.IdempotencyConfig` + `mink.GenerateIdempotencyKey` — configure duplicate detection; `memory.NewIdempotencyStore()` backs it.
- `mink.NewValidationError` / `mink.NewMultiValidationError` / `*mink.MultiValidationError` — validation error types.
- `mink.Logger` / `mink.MetricsCollector` — interfaces implemented by the example's `SimpleLogger` and `SimpleMetrics`.

## Related
- **Examples:** [basic](../basic) · [cqrs-postgres](../cqrs-postgres) · [full-ecommerce](../full-ecommerce)
- **Docs:** [Advanced Patterns](https://go-mink.dev/docs/advanced/advanced-patterns) · [API reference](https://pkg.go.dev/go-mink.dev)
