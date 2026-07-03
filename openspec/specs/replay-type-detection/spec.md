# replay-type-detection Specification

## Purpose
TBD - created by archiving change replay-type-safety. Update Purpose after archive.
## Requirements
### Requirement: Aggregate replay detects unresolved event types
The aggregate-replay path (`EventStore.LoadAggregate` and any internal helper that replays stored events through `Aggregate.ApplyEvent`) SHALL detect when a stored event's `Type` does not resolve to a registered concrete type — i.e. the serializer returned the `map[string]interface{}` fallback — and the aggregate did not consume the event as a raw `StoredEvent`. On detection it SHALL, by default, emit exactly one WARN log per distinct `(streamID, eventType, version)` through the store's configured logger and continue replay. This default behavior SHALL be additive (no signature change, no returned error) so existing callers are not broken.

#### Scenario: An unregistered event is logged, not silently dropped
- **WHEN** `LoadAggregate` replays a stream containing an event whose type was never registered
- **THEN** a WARN is emitted naming the stream id, event type, and version, and replay continues with the remaining events

#### Scenario: A fully-registered stream is silent
- **WHEN** every event type in the stream is registered
- **THEN** no detection warning is emitted and the aggregate is rebuilt from its full history

#### Scenario: Detection does not change read-side behavior
- **WHEN** a projection or `DataExporter` reads the same unregistered event
- **THEN** it still receives the `map[string]interface{}` fallback exactly as before — detection is scoped to aggregate replay only

### Requirement: Typed UnregisteredEventTypeError
The package SHALL define a typed `UnregisteredEventTypeError` carrying the `StreamID`, `EventType`, and `Version`, and a sentinel `ErrUnregisteredEventType` such that `errors.Is(err, ErrUnregisteredEventType)` holds. The error SHALL be used by strict mode (see `strict-replay-mode`) and SHALL be constructible by detection for programmatic handling.

#### Scenario: The error identifies the offending event
- **WHEN** an `UnregisteredEventTypeError` is produced for a stream
- **THEN** it reports the stream id, the unregistered event type, and the version, and `errors.Is(err, ErrUnregisteredEventType)` is true

### Requirement: Raw StoredEvent consumers are never flagged
Detection SHALL key off "the serializer returned the map fallback", NOT "the aggregate's type switch hit its default branch". An aggregate that intentionally ignores a *resolved* (registered) event, or one that consumes the raw `StoredEvent` to do its own decoding, SHALL NOT trigger a detection warning or error.

#### Scenario: StoredEvent pass-through aggregate is not flagged
- **WHEN** an aggregate accepts the raw `StoredEvent` and decodes events itself
- **THEN** no unresolved-type warning or error is produced for that aggregate's loads

#### Scenario: Intentionally ignored registered event is not flagged
- **WHEN** a registered event resolves to its concrete type but the aggregate's switch has no case and ignores it
- **THEN** no unresolved-type warning is produced (the type was resolved; ignoring a known event is the aggregate's choice)

