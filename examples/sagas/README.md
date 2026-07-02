# Saga (Process Manager) Example

> Coordinate a long-running, multi-step business process with automatic compensation on failure.

A saga (process manager) reacts to events and issues the next commands, driving a workflow that spans multiple aggregates. This example implements an `OrderFulfillmentSaga` that orchestrates payment → inventory → shipment, tracks its state across steps, and — when a step fails — emits compensating commands to undo the work already done.

## What this demonstrates

- **Event-driven orchestration** — `OrderFulfillmentSaga.HandleEvent` receives each `mink.StoredEvent` and returns the `[]mink.Command`s to run next (e.g. `OrderPlaced` → `ProcessPaymentCommand`).
- **Explicit state machine** — the saga advances through `SagaState` values (`Started`, `PaymentPending`, `InventoryPending`, `ShipmentPending`, `Completed`, `Cancelled`, `Compensating`) as events arrive.
- **Happy path** — `OrderPlaced` → `PaymentProcessed` → `InventoryReserved` → `ShipmentCreated` walks the saga to `Completed`, generating one command per step.
- **Compensation** — when `InventoryReservationFailed` arrives after payment, the saga moves to `Compensating` and emits `RefundPaymentCommand` + `CancelOrderCommand` to roll back.
- **Completion check** — `IsComplete` reports whether the saga reached a terminal state (`Completed` or `Cancelled`).
- **Event persistence** — the same events are appended to and reloaded from an in-memory `EventStore`.

## Running

```bash
go run ./examples/sagas
```

No infrastructure required — uses the in-memory adapter.

## What happens

1. An in-memory `EventStore` and a fresh `OrderFulfillmentSaga` are created.
2. **Happy path** — four events (`OrderPlaced`, `PaymentProcessed`, `InventoryReserved`, `ShipmentCreated`) are fed one at a time to `saga.HandleEvent`. Each prints a progress line (`📦 Order placed…`, `💳 Payment received…`, `📦 Inventory reserved…`, `✅ Shipment created…`) and returns the next command.
3. The saga state is printed: order ID, final `State` (`Completed`), the `PaymentProcessed`/`InventoryReserved`/`ShipmentCreated` flags, and `IsComplete` (`true`).
4. The generated commands are listed: `ProcessPayment`, `ReserveInventory`, `CreateShipment`, `CompleteOrder`.
5. **Failure path** — a second saga processes `OrderPlaced`, `PaymentProcessed`, then `InventoryReservationFailed`. It prints `⚠️ compensating…`, sets `State` to `Compensating`, and returns the compensation commands.
6. The failed saga's state is printed, followed by its compensation commands: `RefundPayment` and `CancelOrder`.
7. **Event store** — the happy-path events are appended to stream `saga-order-001` with `store.Append`, then reloaded with `store.Load`; the count of stored events is printed and the demo exits.

## Key APIs

- `mink.New(adapter)` — create the `EventStore` over the memory adapter.
- `mink.StoredEvent` — the stored event value passed into the saga's `HandleEvent`.
- `mink.Command` — interface for commands returned by the saga; each defines `CommandType()`.
- `mink.CommandBase` — embedded base for the concrete commands (`ProcessPaymentCommand`, `ReserveInventoryCommand`, `CreateShipmentCommand`, `CompleteOrderCommand`, `CancelOrderCommand`, `RefundPaymentCommand`).
- `store.Append` — persist events to a stream (with `mink.ExpectVersion(mink.NoStream)`).
- `store.Load` — read events back from a stream.

> Note: this example runs the saga logic directly to keep the workflow readable — it collects the commands each step returns rather than dispatching them through a command bus. See [full-ecommerce](../full-ecommerce) for a saga wired into an end-to-end pipeline.

## Related

- **Examples:** [full-ecommerce](../full-ecommerce) · [projections](../projections) · [cqrs-postgres](../cqrs-postgres) · [basic](../basic)
- **Docs:** [Advanced Patterns](https://go-mink.dev/docs/advanced/advanced-patterns) · [API reference](https://pkg.go.dev/go-mink.dev)
