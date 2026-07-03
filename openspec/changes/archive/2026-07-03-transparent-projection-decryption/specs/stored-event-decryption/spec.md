## ADDED Requirements

### Requirement: Exported StoredEvent decryption primitive
The event store SHALL expose `DecryptStoredEvent(ctx context.Context, stored
StoredEvent) (StoredEvent, error)` — the read-path counterpart to the existing
`ProcessStoredEvent` (which returns a deserialized `Event`) — returning a
`StoredEvent` whose field-encrypted `Data` has been decrypted. It SHALL reuse the
store's `FieldEncryptionConfig` (the same `decryptFields` + `WithDecryptionErrorHandler`
path used by the deserializing reads) and SHALL NOT apply upcasting (raw fields are
preserved for `Type`-based consumers). When no encryption is configured or the event
is not encrypted, it SHALL return the input `StoredEvent` unchanged with zero
overhead. The projection engine, and any other component delivering raw
`StoredEvent`s (event subscriptions, live catch-up), SHALL route through this single
primitive so decryption transparency is defined in one place.

#### Scenario: Passthrough when unencrypted or unconfigured
- **WHEN** `DecryptStoredEvent` is called on an event that is not encrypted, or on a store with no `FieldEncryptionConfig`
- **THEN** it returns the same `StoredEvent` with `Data` untouched and does no work

#### Scenario: Decrypts an encrypted event's fields
- **WHEN** `DecryptStoredEvent` is called on an event whose fields were encrypted with `WithEncryptedFields`
- **THEN** it returns a `StoredEvent` with those fields decrypted to plaintext and all other fields (`Type`, `Metadata`, `GlobalPosition`) unchanged

#### Scenario: Surfaces a hard decryption error
- **WHEN** decryption fails and no `DecryptionErrorHandler` resolves it
- **THEN** `DecryptStoredEvent` returns a non-nil error and a zero `StoredEvent`, leaving the caller to decide (retry, poison, fail)

#### Scenario: Event subscriptions receive plaintext
- **WHEN** a subscriber consumes an encrypted stream through a delivery surface routed via `DecryptStoredEvent`
- **THEN** the subscriber receives decrypted event data, matching what `Load` returns
