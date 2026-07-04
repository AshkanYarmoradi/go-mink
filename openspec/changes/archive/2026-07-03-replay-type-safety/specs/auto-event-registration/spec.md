## ADDED Requirements

### Requirement: Opt-in auto-registration on append
The `EventStore` SHALL accept a `WithAutoRegisterOnAppend()` option. When set, `Append` and `SaveAggregate` SHALL register each event's concrete type (in-process) before serialization, so a save-then-load round-trip within the same process resolves to concrete types without a separate `RegisterEvents` call. When unset, append/save behavior SHALL be unchanged. The option SHALL have zero overhead when not configured.

#### Scenario: Save-then-load round-trips without explicit registration
- **WHEN** the store has `WithAutoRegisterOnAppend()` and an aggregate emitting a never-explicitly-registered event is saved and then reloaded in the same process
- **THEN** the event resolves to its concrete type on load and the aggregate state is fully rebuilt (no detection warning, no strict-mode error)

#### Scenario: Disabled by default
- **WHEN** the option is not set
- **THEN** appended event types are not auto-registered and the default registration contract applies

### Requirement: Auto-registration does not mask load-only gaps
Auto-registration SHALL be documented as in-process only: it does NOT persist a registry and does NOT help a cold process that only ever *loads* historical events (those still require explicit `RegisterEvents`/`RegisterAggregateEvents` or a strict-mode/audit check). This keeps explicit registration the durable contract.

#### Scenario: A load-only process still needs registration
- **WHEN** a process that never appends loads historical events of a type that was only ever auto-registered in a different process
- **THEN** the type is unresolved on load and is surfaced by detection (warn) or strict mode (error) — auto-registration did not silently cover it
