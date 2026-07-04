## Why

`JSONSerializer.Deserialize` falls back to a `map[string]interface{}` when a stored event's `Type` is not in the registry. For **projections**, the **`DataExporter`**, and forward-compatible **upcasting**, that lenient fallback is deliberate and correct (tolerate unknown/older/redacted events). But on **aggregate replay** (`LoadAggregate`) the unresolved map is passed to `Aggregate.ApplyEvent`, whose type switch has no case for a bare map, so the event is **silently dropped** — no error, no log, no signal.

The consequence is **silent state loss**: a reloaded aggregate is rebuilt from only the *registered* subset of its history — frequently just its constructor defaults — and any command guard that re-reads prior state then misbehaves. A concrete, real failure: an aggregate that defaults `Status = Active` in its constructor, whose status-transition command guards on the current status, accepts "activate" on an already-suspended aggregate because the suspend event was never replayed. It looks like a data bug and is very hard to trace back to a missing `RegisterEvents` call.

Registration is already the contract (`RegisterEvents` / `RegisterAll`), but **forgetting one event type fails invisibly** — and in a multi-aggregate codebase that is easy to do and costly to diagnose. This change makes the dangerous case **observable**, and opt-in **fatal**, while preserving the intentional lenient behavior for projections/export/upcasting. It does not change the registration contract; it makes *forgetting* it loud instead of silent.

## What Changes

### Required

- **Detect unresolved event types on replay.** The aggregate-replay path (`LoadAggregate` and any helper that calls `Aggregate.ApplyEvent` over stored events) SHALL detect when a stored event's `Type` resolves to the map fallback (unregistered) AND the aggregate does not consume it as a raw `StoredEvent`, and SHALL surface it through the configured logger (warn) **by default** — additive and non-breaking. A typed `UnregisteredEventTypeError` (sentinel `ErrUnregisteredEventType`) carries the stream id, event type, and version for diagnosis.
- **Opt-in strict replay.** A `WithStrictReplay()` store option makes `LoadAggregate` **return** `UnregisteredEventTypeError` instead of dropping the event, so applications can fail fast in dev/CI. The default stays lenient (warn-only) to preserve v1.x behavior; making strict the default is a v2.0 consideration, noted but out of scope here.
- **Registry introspection.** `RegisteredEventTypes() []string` (on the store/serializer) lists the registered type names, plus a `RegisterAggregateEvents(events ...any)` convenience, so applications can pre-flight-validate registration in a test and register an aggregate's events in one call.

### Good-to-have

- **Stream type audit.** A read-only helper (and a `mink` CLI verb) that scans a stream — or all streams — and reports event types present in storage that are **not** registered: a pre-deploy / migration safety check that turns "did I forget to register something?" into a command.
- **Auto-registration on append.** An opt-in `WithAutoRegisterOnAppend()` that registers an event's concrete type the first time it is appended/saved in-process, so a save-then-load round-trip within one process does not depend on a separate `RegisterEvents` call. Off by default to keep registration explicit and discoverable.

### Non-Goals

- **No change to the lenient map-fallback for projections, `DataExporter`, or upcasting.** That behavior is intentional (forward-compat, redacted/crypto-shredded events) and is preserved exactly.
- **No breaking change** to `Aggregate`, `Serializer`, or `EventStore` signatures in v1.x. Detection is additive; strict mode and auto-registration are opt-in options; the default behavior change is warn-only logging, which is non-breaking.
- **No event-store rewriting.** Detection and the stream audit are read-only over the append-only log; nothing mutates or deletes history.
- **Not a replacement for `RegisterEvents`.** Registration remains the contract — this makes *forgetting* it loud, not optional.

## Capabilities

### New Capabilities

- `replay-type-detection` *(required)*: The aggregate-replay path detects unresolved (unregistered) event types and reports them via the logger by default, with a typed `UnregisteredEventTypeError` for programmatic handling.
- `strict-replay-mode` *(required)*: `WithStrictReplay()` turns an unresolved replay type into a returned error so loads fail fast instead of silently losing state.
- `registry-introspection` *(required)*: `RegisteredEventTypes()` lists registered types and `RegisterAggregateEvents(...)` registers a set in one call, enabling pre-flight validation in tests.
- `stream-type-audit` *(good-to-have)*: A read-only library helper + CLI verb that reports event types present in storage but not registered.
- `auto-event-registration` *(good-to-have)*: Opt-in `WithAutoRegisterOnAppend()` registers concrete event types as they are appended/saved in-process.

### Modified Capabilities

<!-- Aggregate replay is EXTENDED, not redefined: the default lenient
     map-fallback in the serializer is unchanged for projections / export /
     upcasting; only the aggregate-replay path gains detection + an opt-in
     strict mode. No existing requirement is invalidated. -->

## Impact

- **Root `mink` package**: `LoadAggregate` (and shared replay helpers) gain unresolved-type detection + warn-by-default logging; new `WithStrictReplay()` and (good-to-have) `WithAutoRegisterOnAppend()` store options; `RegisteredEventTypes()` + `RegisterAggregateEvents(...)`; new typed `UnregisteredEventTypeError` / `ErrUnregisteredEventType` in the errors set.
- **`serializer.go`**: a non-fallback resolution probe (e.g. `registry.Lookup`-backed `IsRegistered(type)`), so the replay path can distinguish "deserialized to a concrete type" from "fell back to a map" without changing `Deserialize`'s public behavior.
- **CLI** (good-to-have): a `mink stream types` / `mink doctor` verb that lists unregistered event types in a stream/all streams, consistent with existing `mink stream` / `mink projection` verbs.
- **Docs**: document the registration requirement, the new options, and the failure mode in the aggregate/serializer API docs and a short "why register events" note; table-driven tests for each capability.
- **Compatibility**: additive and opt-in — zero overhead when unused; default behavior changes only by emitting a log line on an otherwise-silent data-loss condition (non-breaking). PRs target `develop`.
