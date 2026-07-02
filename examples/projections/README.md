# Projections & Read Models Example

> Build queryable read models from events using inline (synchronous) and live (real-time) projections.

Event sourcing stores the *history* of changes, but applications usually need to *query* current state. This example shows how the projection engine turns an event stream into read models: an inline projection that updates a queryable `OrderSummary` synchronously (strong consistency), and a live projection that pushes real-time updates to a dashboard channel.

## What this demonstrates

- **Inline projections** — `OrderSummaryProjection` embeds `mink.ProjectionBase` and implements `Apply`, folding `OrderCreated`, `ItemAdded`, and `OrderShipped` into an `OrderSummary` read model synchronously via `engine.ProcessInlineProjections`.
- **Live projections** — `OrderDashboardProjection` embeds `mink.LiveProjectionBase` and implements `OnEvent`, emitting human-readable updates on a channel as events are notified via `engine.NotifyLiveProjections`.
- **Read model repository** — `mink.NewInMemoryRepository` stores `OrderSummary` documents keyed by `OrderID`, with `Insert`/`Update` from the projection and `Get`/`Find`/`Count` for queries.
- **Checkpoints** — `memory.NewCheckpointStore` tracks projection progress, wired into the engine with `mink.WithCheckpointStore`.
- **Projection status** — `engine.GetAllStatuses` reports each projection's `Name` and `State`.

## Running

```bash
go run ./examples/projections
```

No infrastructure required — uses the in-memory adapter.

## What happens

1. An in-memory `EventStore`, an `OrderSummary` repository, a checkpoint store, and a `ProjectionEngine` are created, then the engine is started. Both projections (`OrderSummary` inline, `OrderDashboard` live) are registered.
2. **Order 1 created** — `OrderCreated` is appended (`Order-001`), reloaded with `store.LoadRaw`, and fed to `ProcessInlineProjections` + `NotifyLiveProjections`. The dashboard prints a `📦 New order` line.
3. **Items added to Order 1** — two `ItemAdded` events are appended and processed inline, incrementing `ItemCount` and `TotalAmount` on the summary.
4. **Order 2 created** — a second `OrderCreated` (`Order-002`) is appended and projected; the dashboard announces it.
5. **Order 1 shipped** — `OrderShipped` is appended; the inline projection flips `Status` to `Shipped` and the dashboard prints a `🚚 Order ... shipped` line.
6. **Query read model** — `orderRepo.Get` prints both order summaries, showing item counts, totals, and shipped status.
7. **Query builder** — `orderRepo.Find(ctx, mink.Query{})` and `orderRepo.Count(ctx, mink.Query{})` report the total number of orders.
8. **Projection status** — `engine.GetAllStatuses` prints the name and state of each registered projection, then the demo exits.

## Key APIs

- `mink.New(adapter)` — create the `EventStore` over the memory adapter.
- `mink.NewProjectionEngine(store, mink.WithCheckpointStore(...))` — the engine that drives projections.
- `engine.RegisterInline` / `engine.RegisterLive` — register synchronous and real-time projections.
- `engine.Start` / `engine.Stop` — start and shut down the engine.
- `engine.ProcessInlineProjections` — synchronously apply a batch of events to inline projections.
- `engine.NotifyLiveProjections` — push a batch of events to live projections.
- `engine.GetAllStatuses` — retrieve `[]*mink.ProjectionStatus` (name, state, lag).
- `mink.ProjectionBase` / `mink.NewProjectionBase` — base for an inline projection; `Apply(ctx, mink.StoredEvent)` handles events.
- `mink.LiveProjectionBase` / `mink.NewLiveProjectionBase` — base for a live projection; `OnEvent(ctx, mink.StoredEvent)` reacts in real time.
- `mink.NewInMemoryRepository` — generic read model store with `Insert`, `Update`, `Get`, `Find`, `Count`.
- `mink.Query` — the query value passed to `Find`/`Count`.
- `memory.NewCheckpointStore` — in-memory checkpoint tracking.
- `store.Append` / `store.LoadRaw` — append events and load them back as raw `StoredEvent`s.
- `mink.ExpectVersion(mink.NoStream)` — optimistic-concurrency expected-version option.

## Related

- **Examples:** [full-ecommerce](../full-ecommerce) · [sagas](../sagas) · [cqrs-postgres](../cqrs-postgres) · [basic](../basic)
- **Docs:** [Read Models](https://go-mink.dev/docs/core/read-models) · [API reference](https://pkg.go.dev/go-mink.dev)
