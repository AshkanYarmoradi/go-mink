## 1. Required — Replay type detection (`replay-type-detection`)

- [x] 1.1 Add `IsRegistered(eventType string) bool` to the serializer (backed by `registry.Lookup`); do not change `Deserialize`'s public behavior
- [x] 1.2 Add typed `UnregisteredEventTypeError` (fields: StreamID, EventType, Version) + sentinel `ErrUnregisteredEventType`, with `Is`/`Unwrap`
- [x] 1.3 In the aggregate-replay path (`LoadAggregate` + shared helpers), detect when a stored event resolved to the map fallback and the aggregate did not consume it as a raw `StoredEvent`; emit one WARN per `(stream, type, version)` via the configured logger and continue (default)
- [x] 1.4 Ensure the `StoredEvent` pass-through path and intentionally-ignored concrete events never trigger detection (D3)
- [x] 1.5 Table-driven tests: unregistered event → WARN + state-loss reproduced; registered event → no warning; `StoredEvent`-consuming aggregate → no warning; map-fallback still works for projections/export (unchanged)

## 2. Required — Strict replay mode (`strict-replay-mode`)

- [x] 2.1 Add `WithStrictReplay()` store option (options-pattern; zero overhead when unset)
- [x] 2.2 When strict, `LoadAggregate` returns `UnregisteredEventTypeError` at the first unresolved event instead of warning-and-continuing
- [x] 2.3 Table-driven tests: strict + unregistered → error with stream/type/version; strict + fully-registered → loads identically to lenient; lenient remains the default

## 3. Required — Registry introspection (`registry-introspection`)

- [x] 3.1 Add `RegisteredEventTypes() []string` (store/serializer) returning registered type names
- [x] 3.2 Add `RegisterAggregateEvents(events ...any)` convenience that registers a set in one call
- [x] 3.3 Table-driven tests: register a set → introspection lists them; round-trip a declared set → all load as concrete types
- [x] 3.4 Docs: a short "register the events your aggregates apply" note on the aggregate + serializer APIs, referencing strict mode and the audit helper

## 4. Good-to-have — Stream type audit (`stream-type-audit`)

- [x] 4.1 Add a read-only helper that scans a stream (and an all-streams variant) and returns event types present in storage but not registered
- [x] 4.2 Add a `mink` CLI verb (e.g. `mink stream types` / `mink doctor`) that prints unregistered types, consistent with existing `mink stream` / `mink projection` verbs
- [x] 4.3 Table-driven tests: stream with an unregistered type → reported; fully-registered stream → empty; helper never mutates the log (append-only invariant)

## 5. Good-to-have — Auto-registration on append (`auto-event-registration`)

- [x] 5.1 Add `WithAutoRegisterOnAppend()` store option; on `Append`/`SaveAggregate`, register each event's concrete type before serialization (in-process only)
- [x] 5.2 Document the limitation: a load-only cold process still needs explicit registration (D4)
- [x] 5.3 Table-driven tests: with the option, save-then-load round-trips without an explicit `RegisterEvents`; without it, behavior is unchanged
