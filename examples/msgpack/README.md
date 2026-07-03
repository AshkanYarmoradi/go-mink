# MessagePack Serializer Example

> Store events as compact MessagePack binaries instead of JSON.

By default go-mink serializes events to JSON, which is human-readable but larger and slower to encode. Swapping in the MessagePack serializer yields smaller payloads and faster serialization — valuable for high-throughput or storage-sensitive systems. This example compares the two formats side by side, then wires MessagePack into a real `EventStore`.

> **Compatibility:** MessagePack is a *binary* format. It works with the in-memory adapter (used here) or any `BYTEA`-backed store, but **not** the PostgreSQL event store, whose `events.data` column is `JSONB`. Pairing this serializer with the PostgreSQL adapter makes `mink.New` panic with `mink.ErrBinarySerializerUnsupported` — use the default JSON serializer there.

## What this demonstrates

- **Pluggable serialization** — `msgpack.NewSerializer()` produces a serializer that drops into an `EventStore` via `mink.WithSerializer(...)`, no other code changes.
- **Payload size savings** — the same `ProductCreated` and a larger array/map-heavy `LargeEvent` are serialized with both JSON and MessagePack, printing byte counts and the percentage saved.
- **Typed deserialization** — event types are registered with `msgpackSerializer.Register(...)`, so `Deserialize` returns the concrete `ProductCreated` struct rather than a generic map.
- **Speed benchmark** — 10,000 serialization iterations per format, reporting duration, ops/sec, and the speedup factor.
- **Event store integration** — three events are appended and reloaded through a store backed by the MessagePack serializer.

## Running

```bash
go run ./examples/msgpack
```

No infrastructure required — uses the in-memory adapter.

## What happens

1. **Set up serializers** — a JSON serializer (`mink.NewJSONSerializer()`) and a MessagePack serializer are created; the four event types (`ProductCreated`, `ProductPriceChanged`, `ProductInventoryUpdated`, `LargeEvent`) are registered with the MessagePack serializer.
2. **Compare a typical event** — a `ProductCreated` is serialized both ways; JSON and MessagePack byte sizes and the savings percentage are printed.
3. **Compare a large event** — a `LargeEvent` with a 1000-byte blob, a map, a 100-element int slice, and nested data repeats the comparison.
4. **Deserialization test** — the JSON bytes deserialize to a `map[string]interface{}` (type not registered) while the MessagePack bytes deserialize to a concrete `ProductCreated`; both print the recovered ProductID.
5. **Speed benchmark** — each format serializes the event 10,000 times; durations, ops/sec, and the MessagePack speedup are reported.
6. **Store and load** — a store built with `mink.WithSerializer(msgpackSerializer)` appends three events with `mink.ExpectVersion(mink.NoStream)`, then loads them back and prints each event's type and version.
7. **Guidance** — the program closes with notes on when to prefer MessagePack versus JSON.

## Key APIs

- `msgpack.NewSerializer()` — creates a MessagePack-backed `Serializer`.
- `msgpackSerializer.Register("ProductCreated", ProductCreated{})` — registers an event type for typed deserialization.
- `serializer.Serialize(event)` / `serializer.Deserialize(data, "ProductCreated")` — encode and decode event payloads.
- `mink.NewJSONSerializer()` — the default JSON serializer, used here as the comparison baseline.
- `mink.WithSerializer(msgpackSerializer)` — option that sets the store's serializer.
- `mink.ExpectVersion(mink.NoStream)` — optimistic-concurrency option asserting the stream does not yet exist.
- `store.Append(...)` / `store.Load(...)` — persist and read events from the store.

## Related

- **Examples:** [protobuf](../protobuf) · [full-ecommerce](../full-ecommerce) · [basic](../basic)
- **Docs:** [Event Store](https://go-mink.dev/docs/core/event-store) · [API reference](https://pkg.go.dev/go-mink.dev/serializer/msgpack)
