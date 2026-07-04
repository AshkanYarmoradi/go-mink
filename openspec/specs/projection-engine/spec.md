# projection-engine Specification

## Purpose
TBD - created by archiving change fix-review-correctness-bugs. Update Purpose after archive.
## Requirements
### Requirement: Checkpoint-load failure does not silently replay from zero
An async projection worker SHALL treat a non-nil error from checkpoint retrieval at
startup as a failure to start (surfaced via the engine/worker error path) rather than
defaulting its start position to 0. A *missing* checkpoint (the adapter returns position
0 with a nil error) SHALL still start from 0.

#### Scenario: Transient checkpoint error does not reprocess history
- **WHEN** a worker with an existing checkpoint at position N starts and checkpoint retrieval returns a transient error
- **THEN** the worker does not start from 0 and does not reprocess the stream from the beginning

#### Scenario: Missing checkpoint starts from zero
- **WHEN** no checkpoint exists (retrieval returns `(0, nil)`)
- **THEN** the worker starts from position 0 as before

### Requirement: Live projections do not silently drop events
`NotifyLiveProjections` SHALL NOT silently discard an event delivered while a worker is
not `Running`. The engine SHALL either catch the worker up from its last position when
it becomes `Running`, or expose the drop (a counter and a documented, observable
no-catch-up contract) so loss is intentional and visible rather than silent.

#### Scenario: Event arriving before a worker is Running is not silently lost
- **WHEN** an event is notified to a live projection whose worker is not yet `Running`
- **THEN** the event is caught up on start or recorded as a visible drop, never silently discarded with no signal

### Requirement: Poison event identity matches the applied batch
On an `ApplyBatch` failure, the poison event reported to `OnPoisonEvent` SHALL be taken
from the filtered slice actually applied to the projection, not from an unfiltered batch
that may include events the projection does not handle.

#### Scenario: Poison event is one the projection handles
- **WHEN** a batch's last loaded event is filtered out and `ApplyBatch` fails
- **THEN** `OnPoisonEvent` receives an event from the filtered/applied set, not the filtered-out trailing event

### Requirement: Projections receive decrypted event data
The projection engine SHALL decrypt each field-encrypted `StoredEvent`'s `Data`
before delivering it to a projection's `Apply` or `ApplyBatch`, on **both** the
async worker load path and the inline path (`ProcessInlineProjections`), whenever the
store has a `FieldEncryptionConfig` (`mink.WithFieldEncryption`). Decryption SHALL
reuse the same `decryptFields` path as `Load`/`LoadAggregate`/`DataExporter`, so the
configured `WithDecryptionErrorHandler` governs the outcome. The delivered value
SHALL remain a `StoredEvent` (its `Type`, `Metadata`, `GlobalPosition`, etc.
unchanged) with only `Data` decrypted. When no encryption is configured, or the
event is not encrypted (`IsEncrypted(metadata)` is false), the event SHALL be
delivered unchanged with zero overhead.

#### Scenario: Scalar encrypted field is projected as plaintext
- **WHEN** a projection consumes an event whose scalar field (e.g. `first_name`) was encrypted with `WithEncryptedFields`
- **THEN** the projection reads the plaintext value, not its base64 ciphertext

#### Scenario: Non-scalar encrypted field deserializes in the projection
- **WHEN** a projection unmarshals an event whose non-scalar field (an array or object, e.g. `email_addresses`) was encrypted
- **THEN** decryption restores the JSON shape and the projection's `json.Unmarshal` into its typed struct succeeds (rather than failing on a ciphertext string)

#### Scenario: Async and inline delivery are identical
- **WHEN** the same encrypted stream is projected once via an inline projection and once via an async worker
- **THEN** both produce the same plaintext read-model state

#### Scenario: Crypto-shredded subject does not stall the worker
- **WHEN** a projected event belongs to a subject whose key has been revoked and a `WithDecryptionErrorHandler` that returns nil (skip) is configured
- **THEN** the event is delivered without error (its fields left as stored) and the async worker advances its checkpoint rather than stalling or poison-skipping

#### Scenario: Unhandled decryption error is retried, not silently skipped
- **WHEN** a projected event fails decryption and no handler resolves it (hard error)
- **THEN** the async cycle returns the error and does NOT advance the checkpoint (the batch is retried), and the inline path fails the append's inline-projection step — the failure is surfaced, never silently swallowed

#### Scenario: No encryption configured is a zero-overhead no-op
- **WHEN** the store has no `FieldEncryptionConfig`, or the event's metadata is not marked encrypted
- **THEN** the `StoredEvent` is delivered byte-identical to today, with no decryption work performed

