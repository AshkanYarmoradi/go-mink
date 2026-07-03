## ADDED Requirements

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
