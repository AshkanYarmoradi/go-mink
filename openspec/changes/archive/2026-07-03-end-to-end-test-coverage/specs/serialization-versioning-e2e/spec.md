## ADDED Requirements

### Requirement: Event upcasting on load is verified end-to-end on PostgreSQL (good-to-have)
There SHALL be an end-to-end test that stores v1 events in PostgreSQL, registers an `UpcasterChain`, and asserts `Load`/`LoadAggregate` returns upcasted (latest-schema) events while `Append` stamps the latest `$schema_version` in `Metadata.Custom` — with no DB-schema change and no rewrite of the stored historical rows.

#### Scenario: Older events are upcasted transparently on load
- **WHEN** v1 events are stored in PostgreSQL and an upcaster chain to v2 is registered
- **THEN** `Load`/`LoadAggregate` yields v2-shaped events, the stored rows remain v1 on disk (history not rewritten), and newly appended events carry the latest `$schema_version`

#### Scenario: Schema-version gap is surfaced, not silently skipped
- **WHEN** a stored event's schema version is newer than any registered upcaster can handle (a gap)
- **THEN** the load surfaces a typed schema-version/upcast error rather than returning malformed data

### Requirement: Alternative serializers round-trip through PostgreSQL (good-to-have)
There SHALL be an end-to-end test that persists events serialized with the msgpack and protobuf serializers to PostgreSQL and reloads them, asserting the round-trip reproduces the original event and that schema-registry compatibility checks hold over the stored streams.

#### Scenario: msgpack/protobuf event survives a PG round-trip
- **WHEN** an event serialized with msgpack (and, separately, protobuf) is appended to PostgreSQL and reloaded
- **THEN** the deserialized event equals the original, with the correct concrete type, from real storage
