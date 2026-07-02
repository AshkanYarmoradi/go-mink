# Event Versioning Example

> Evolve your event schema over time without rewriting stored events.

Events are immutable once written, but their shape needs to change as your domain grows. go-mink solves this with **upcasters** — small, ordered transformations that rebuild old events into the latest schema at read time. This example evolves a `ProductAdded` event from v1 (Name, Price) to v2 (adds Currency) to v3 (adds TaxRate), and loads a v1 event back as if it had always been v3.

## What this demonstrates

- **Schema-agnostic upcasters** — `productAddedV1ToV2` and `productAddedV2ToV3` transform raw JSON bytes; each declares `FromVersion()`/`ToVersion()` (must differ by exactly 1) and defaults the newly added field.
- **Transparent upcasting on load** — a v1 event stored before any upcasters existed is read back through `storeV3.Load` / `storeV3.LoadAggregate` already carrying Currency and TaxRate.
- **Automatic version stamping** — newly saved events are marked with the latest schema version in metadata, read back via `mink.GetSchemaVersion`.
- **Upcaster chains** — `mink.NewUpcasterChain` collects upcasters; `chain.Validate` checks for gaps and `chain.LatestVersion` reports the target version per event type.
- **Compatibility checking** — `mink.NewSchemaRegistry` records field-level schema definitions and `registry.CheckCompatibility` classifies each version bump.

## Running

```bash
go run ./examples/versioning
```

No infrastructure required — uses the in-memory adapter.

## What happens

1. **Store a v1 event** — a store with no upcasters appends a `ProductAdded` with only Name (`Wireless Mouse`) and Price (`29.99`); Currency and TaxRate are zero-valued, representing the old schema.
2. **Register upcasters** — `productAddedV1ToV2` and `productAddedV2ToV3` are registered into an `UpcasterChain`, which is then validated. The chain reports its registered event types and that the latest version for `ProductAdded` is v3.
3. **Load the old event** — a second store over the *same* adapter but configured with `mink.WithUpcasters(chain)` loads the v1 event; it comes back with `Currency: USD` (from the v1→v2 upcaster) and `TaxRate: 0.0%` (from the v2→v3 upcaster).
4. **Rebuild the aggregate** — `LoadAggregate` replays the upcasted events into a `Product`, showing the fully-populated state and version.
5. **Save a v3 event** — a new `Product` (`Mechanical Keyboard`, EUR, 19% tax) is saved; `LoadRaw` plus `mink.GetSchemaVersion` confirms the schema version stamped into metadata.
6. **Inspect the registry** — three schema definitions (v1, v2, v3) are registered and `CheckCompatibility` is called for v1→v2 and v2→v3, printing the compatibility classification for each field addition.

## Key APIs

- `mink.NewUpcasterChain()` — creates an ordered chain of upcasters.
- `chain.Register(...)` — adds an upcaster (implementing `EventType`, `FromVersion`, `ToVersion`, `Upcast`) to the chain.
- `chain.Validate()` — verifies the chain has no version gaps.
- `chain.RegisteredEventTypes()` — lists event types the chain can upcast.
- `chain.LatestVersion("ProductAdded")` — returns the highest target version for an event type.
- `mink.WithUpcasters(chain)` — option that wires the chain into an `EventStore`.
- `mink.GetSchemaVersion(metadata)` — reads the schema version stamped in event metadata.
- `mink.NewSchemaRegistry()` — creates a registry of `SchemaDefinition`s with `FieldDefinition`s.
- `registry.Register(...)` / `registry.GetLatestVersion(...)` — record and query schema versions.
- `registry.CheckCompatibility("ProductAdded", 1, 2)` — classifies compatibility between two schema versions.

## Related

- **Examples:** [full-ecommerce](../full-ecommerce) · [basic](../basic) · [msgpack](../msgpack)
- **Docs:** [Event Versioning](https://go-mink.dev/docs/advanced/versioning) · [API reference](https://pkg.go.dev/go-mink.dev)
