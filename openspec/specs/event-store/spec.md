# event-store Specification

## Purpose
TBD - created by archiving change fix-review-correctness-bugs. Update Purpose after archive.
## Requirements
### Requirement: In-memory append does not alias caller buffers
The in-memory adapter SHALL deep-copy an event's `Data` bytes and `Metadata.Custom` map
when appending, so a caller that later mutates or reuses the passed slice/map cannot
retroactively corrupt stored or loaded events, and a concurrent `Load` with buffer reuse
is race-free — matching the deep-copy discipline already used for snapshots/outbox and
the PostgreSQL adapter's freshly-scanned bytes.

#### Scenario: Mutating a reused buffer after Append does not change stored events
- **WHEN** a caller appends an event and then mutates or reuses the same `Data` buffer or `Custom` map
- **THEN** a subsequent `Load` returns the originally-appended bytes and metadata, unaffected by the mutation

#### Scenario: Concurrent load and buffer reuse is race-free
- **WHEN** one goroutine reuses the append buffer while another calls `Load`
- **THEN** there is no data race and `Load` observes the stored copy

