# strict-replay-mode Specification

## Purpose
TBD - created by archiving change replay-type-safety. Update Purpose after archive.
## Requirements
### Requirement: WithStrictReplay option
The `EventStore` SHALL accept a `WithStrictReplay()` option (options-pattern constructor, consistent with the existing `WithX(...)` style). When set, the aggregate-replay path SHALL return an `UnregisteredEventTypeError` from `LoadAggregate` at the first unresolved (unregistered, map-fallback) event instead of warning-and-continuing. When unset, the default lenient warn-and-continue behavior applies. The option SHALL have zero overhead when not configured.

#### Scenario: Strict mode fails fast on an unregistered event
- **WHEN** the store is configured with `WithStrictReplay()` and `LoadAggregate` reaches an unregistered event
- **THEN** `LoadAggregate` returns an `UnregisteredEventTypeError` (matching `errors.Is(err, ErrUnregisteredEventType)`) identifying the stream, type, and version, and the aggregate is not returned as partially-replayed state

#### Scenario: Strict mode is a no-op when registration is complete
- **WHEN** the store is strict and every event type in the stream is registered
- **THEN** `LoadAggregate` succeeds and produces exactly the same aggregate state as a lenient load

### Requirement: Lenient remains the default
Absent `WithStrictReplay()`, `LoadAggregate` SHALL preserve the pre-change behavior (continue past unresolved events) augmented only by the detection WARN. Strict mode SHALL NOT be enabled implicitly by any other option.

#### Scenario: Default store does not error on unregistered events
- **WHEN** a store is constructed without `WithStrictReplay()` and loads a stream with an unregistered event
- **THEN** `LoadAggregate` returns no error (it warns and continues), matching prior behavior

