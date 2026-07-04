## Context

go-mink resolves a stored event's Go type by name through the serializer registry. `JSONSerializer.Deserialize(data, type)` does `registry.Lookup(type)` and, on a miss, **intentionally** returns a `map[string]interface{}` so that projections, the `DataExporter`, and upcasters can tolerate unknown/older/redacted events (forward-compatibility and crypto-shredding both depend on this).

`EventStore.LoadAggregate` reuses that same deserialization, then calls `agg.ApplyEvent(event.Data)`. Aggregates dispatch on the **concrete type** in a `switch`. So when `event.Data` is the map fallback (the type was never registered), the `switch` has no matching case and the event is dropped — the aggregate finishes replay missing part of its history, silently. Because aggregates seed defaults in their constructors, the reloaded state often looks plausible (e.g. a default `Active` status), which is exactly what makes the bug invisible.

This is a **safety/observability gap, not a correctness bug in the fallback itself**: the fallback is right for read-side consumers and wrong only when it silently swallows an aggregate's own events. The fix must therefore be surgical to the aggregate-replay path and must not change the read-side behavior.

## Goals / Non-Goals

- **Goal**: make an unresolved event type during aggregate replay impossible to miss — logged by default, a typed error available, and opt-in fatal.
- **Goal**: give applications a way to *prove* their registration is complete (introspection + a stream audit) before it bites in production.
- **Non-goal**: changing the lenient map fallback for projections / export / upcasting.
- **Non-goal**: any breaking change to `Aggregate`, `Serializer`, or `EventStore` in v1.x.

## Decisions

### D1 — Detect via "did it resolve to a concrete type?", not by re-deriving the registry
The replay path needs to know whether `Deserialize` produced a concrete event or the map fallback. Rather than duplicate registry logic, add a small serializer probe (`IsRegistered(eventType string) bool`, backed by `registry.Lookup`) and treat a `map[string]interface{}` result on the aggregate path as "unresolved". This keeps `Deserialize`'s public contract unchanged and centralizes the decision.

### D2 — Default is warn, not error (backward-compatible)
Existing v1.x users may unknowingly depend on the silent drop (as in the motivating bug). Returning an error by default would be a breaking behavior change mid-major-version. So:
- **Default**: emit one `WARN` per unresolved `(stream, type, version)` through the store's configured logger; replay continues (current behavior, now observable).
- **Opt-in**: `WithStrictReplay()` returns `UnregisteredEventTypeError` from `LoadAggregate` at the first unresolved event.
- **v2.0 note**: flipping the default to strict is the right long-term default; recorded as a follow-up, out of scope here.

### D3 — Skip-by-design events are not "unresolved"
An aggregate may legitimately consume raw `StoredEvent`s (some aggregates take the `StoredEvent` directly to handle their own decoding). Detection MUST only fire when the event resolved to the **map fallback** AND the aggregate's `ApplyEvent` did not handle it — never for events the aggregate intentionally ignores after receiving a concrete type, and never for the `StoredEvent` pass-through path. The detection therefore keys off "serializer returned a map", not "aggregate's switch hit default".

### D4 — Auto-registration is opt-in and in-process only
`WithAutoRegisterOnAppend()` registers an event's concrete type at append/save time. This removes the footgun for the common save-then-load-same-process flow, but it does **not** persist a registry and does **not** help a cold process that only ever *loads* historical events (those still need explicit `RegisterEvents` or a declaration). Keeping it opt-in preserves "registration is explicit and discoverable" as the default mental model.

### D5 — Introspection + audit make completeness testable
`RegisteredEventTypes()` plus a read-only stream scan let an app assert, in a unit/integration test or a pre-deploy `mink` command, that every event type present in storage is registered. This converts a silent runtime hazard into a checkable invariant.

## Risks / Trade-offs

- **Log noise** if an app deliberately relies on the fallback during a migration — mitigated by warn-once-per-(stream,type) and the opt-in strict mode (so noisy apps can instead choose strict-in-CI / lenient-in-prod).
- **Auto-registration hides missing registration** in single-process tests while it still bites in a load-only process — mitigated by keeping it opt-in and documenting the limitation in D4.
- **Probe cost**: `IsRegistered` is an O(1) map lookup already performed by `Deserialize`; detection adds no extra deserialization, only a branch — zero overhead when no unresolved events occur.

## Backward Compatibility

Additive only. No signature changes to existing public types; new options and helpers; default behavior gains a log line on an otherwise-silent condition. `Deserialize`, projections, `DataExporter`, and upcasting are untouched.
