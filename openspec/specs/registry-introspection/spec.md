# registry-introspection Specification

## Purpose
TBD - created by archiving change replay-type-safety. Update Purpose after archive.
## Requirements
### Requirement: RegisteredEventTypes introspection
The `EventStore` (and/or its serializer) SHALL expose `RegisteredEventTypes() []string` returning the set of currently-registered event type names. The result SHALL be a snapshot safe to call concurrently with registration, and SHALL let an application assert registration completeness in a test or pre-flight check.

#### Scenario: Introspection lists registered types
- **WHEN** events `A`, `B`, `C` are registered and `RegisteredEventTypes()` is called
- **THEN** the returned slice contains exactly `A`, `B`, and `C` (order-independent)

#### Scenario: A test can assert completeness
- **WHEN** a test compares an aggregate's applied event types against `RegisteredEventTypes()`
- **THEN** a missing registration is detectable as a failing assertion before runtime

### Requirement: RegisterAggregateEvents convenience
The package SHALL provide `RegisterAggregateEvents(events ...any)` (or an equivalent variadic helper) that registers a set of concrete event values in one call, so an aggregate's events can be declared and registered together. It SHALL be equivalent to calling `RegisterEvents`/`RegisterAll` for each and SHALL be idempotent.

#### Scenario: One call registers a whole aggregate's events
- **WHEN** `RegisterAggregateEvents(Created{}, Updated{}, StatusChanged{}, Deleted{})` is called
- **THEN** all four resolve to concrete types on subsequent `LoadAggregate`, and re-registering the same set is a no-op success

