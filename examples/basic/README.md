# Basic Event Sourcing Example

> The smallest end-to-end go-mink program: define events, model an aggregate, and persist it to an event store.

This example introduces the core idea behind event sourcing — instead of storing an order's current state, you store the immutable events that led to it (`OrderCreated`, `ItemAdded`, `OrderShipped`) and rebuild state by replaying them. It's the foundation every other example builds on, so start here if go-mink is new to you.

## What this demonstrates
- **Domain events** — `OrderCreated`, `ItemAdded`, and `OrderShipped` are plain structs registered with `store.RegisterEvents` so they can be serialized and replayed.
- **Aggregate root** — `Order` embeds `mink.AggregateBase` and enforces business rules in `Create`, `AddItem`, and `Ship` (e.g. you can't add items to a shipped order).
- **Apply / rebuild split** — command methods call `Apply` to record new events, while `ApplyEvent` rebuilds state from stored history and bumps the version via `IncrementVersion`.
- **Save and load** — `SaveAggregate` persists uncommitted events; `LoadAggregate` replays them into a fresh instance, restoring identical state.
- **Working with the store directly** — `LoadRaw` reads the raw event records off a stream, and `GetStreamInfo` reports stream metadata (version, event count, timestamps).

## Running
```bash
go run ./examples/basic
```
No infrastructure required — uses the in-memory adapter.

## What happens
The program runs six numbered steps and prints the result of each:

1. **Create a new order** — builds `order-001`, adds two items, and prints the in-memory state (customer, item count, total, and the count of uncommitted events), then calls `SaveAggregate` and confirms the uncommitted events have been flushed.
2. **Load from the event store** — creates a fresh `Order` and calls `LoadAggregate`, printing the reconstructed status, items, total, and version to show state was rebuilt purely from events.
3. **Add another item and ship** — mutates the loaded order (`AddItem` + `Ship`) and saves it again, appending more events to the same stream.
4. **Load raw events** — reads the stream via `LoadRaw(ctx, "Order-order-001", 0)` and lists each event's version, type, and global position.
5. **Get stream information** — prints the `StreamInfo`: stream ID, category, current version, event count, and creation timestamp.
6. **Final state** — reloads the order one last time and prints the complete order, including tracking number and a line-by-line item breakdown.

## Key APIs
- `mink.New(adapter)` — constructs an `EventStore` over a storage adapter.
- `memory.NewAdapter()` — in-memory `EventStoreAdapter` for tests and demos.
- `store.RegisterEvents(...)` — registers event types for serialization.
- `mink.AggregateBase` / `mink.NewAggregateBase(id, type)` — embeddable base providing identity, versioning, and event tracking.
- `(*Order).Apply(event)` — records a new uncommitted event and applies it.
- `(*Order).ApplyEvent(event)` — rebuilds state from a historical event.
- `IncrementVersion()`, `Version()`, `AggregateID()`, `UncommittedEvents()` — aggregate bookkeeping from `AggregateBase`.
- `store.SaveAggregate(ctx, agg)` — appends uncommitted events to the aggregate's stream.
- `store.LoadAggregate(ctx, agg)` — loads and replays a stream into an aggregate.
- `store.LoadRaw(ctx, streamID, fromVersion)` — returns raw `StoredEvent` records.
- `store.GetStreamInfo(ctx, streamID)` — returns `StreamInfo` (version, event count, timestamps).

## Related
- **Examples:** [cqrs](../cqrs) · [projections](../projections) · [full-ecommerce](../full-ecommerce)
- **Docs:** [Event Store](https://go-mink.dev/docs/core/event-store) · [API reference](https://pkg.go.dev/go-mink.dev)
