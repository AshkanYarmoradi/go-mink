# Protocol Buffers Serializer Example

> Serialize events as compact, strongly-typed Protocol Buffers.

Protocol Buffers give you the smallest binary payloads of go-mink's built-in serializers, plus schema-enforced typing and field-numbered forward/backward compatibility. This example uses the protobuf well-known wrapper types (`wrapperspb`, `timestamppb`) as stand-in events across four demos, from basic round-tripping to building a projection.

> **Compatibility:** Protocol Buffers is a *binary* format. It works with the in-memory adapter (used here) or any `BYTEA`-backed store, but **not** the PostgreSQL event store, whose `events.data` column is `JSONB`. Pairing this serializer with the PostgreSQL adapter makes the first `Append`/`SaveAggregate` fail fast with `mink.ErrBinarySerializerUnsupported` (detected at construction, before any write) — use the default JSON serializer there.

## What this demonstrates

- **Strongly-typed serialization** — `protobuf.NewSerializer()` with `s.MustRegister(...)` binds event type names to protobuf message types; in production you would register your own `.proto`-generated types.
- **Round-tripping well-known types** — string, int32, double, bool, and timestamp wrappers are serialized, sized, and deserialized back to their values.
- **Direct adapter integration** — pre-serialized payloads are appended as `adapters.EventRecord`s straight to the memory adapter and loaded back for deserialization.
- **Size comparison vs JSON** — a table pits protobuf byte sizes against `mink.NewJSONSerializer()` across seven data types, with per-row and total savings.
- **Projection building** — a stream of protobuf events is folded into an `OrderSummary` read model by type-switching on each deserialized message.

## Running

```bash
go run ./examples/protobuf
```

No infrastructure required — uses the in-memory adapter.

## What happens

1. **Basic serialization** — a serializer registers five wrapper types (`OrderID`, `ItemCount`, `TotalAmount`, `IsShipped`, `Timestamp`), then serializes one event of each, printing its byte size and the value recovered by `Deserialize`.
2. **Event store integration** — five `adapters.EventRecord`s (customer ID, two item-added events, order total, shipped flag) are appended to stream `order-12345` via `adapter.Append(ctx, streamID, events, mink.NoStream)`, then reloaded with `adapter.Load` and deserialized one by one.
3. **Size comparison** — a formatted table serializes seven values (small/large strings, integers, double, boolean, binary) with both JSON and protobuf, printing each row's sizes and savings plus a total row.
4. **Projection building** — five protobuf events are deserialized and folded into an `OrderSummary` (customer ID, item batches, total items, complete flag); the final projection state is printed.
5. The program prints a success line once all four demos complete.

## Key APIs

- `protobuf.NewSerializer()` — creates a Protocol Buffers `Serializer`.
- `s.MustRegister("OrderID", &wrapperspb.StringValue{})` — registers an event type to a protobuf message type, panicking on error.
- `serializer.Serialize(event)` / `serializer.Deserialize(data, typ)` — encode and decode protobuf payloads (Deserialize returns the message by value).
- `adapters.EventRecord` — the pre-serialized event record (`Type` + `Data`) appended directly to an adapter.
- `adapter.Append(ctx, streamID, events, mink.NoStream)` / `adapter.Load(ctx, streamID, 0)` — low-level append and load on the memory adapter.
- `mink.NoStream` — expected-version constant asserting the stream does not yet exist.
- `mink.NewJSONSerializer()` — the JSON serializer used as the size-comparison baseline.
- `wrapperspb` / `timestamppb` — protobuf well-known types used as stand-in events.

## Related

- **Examples:** [msgpack](../msgpack) · [full-ecommerce](../full-ecommerce) · [projections](../projections)
- **Docs:** [Event Store](https://go-mink.dev/docs/core/event-store) · [API reference](https://pkg.go.dev/go-mink.dev/serializer/protobuf)
