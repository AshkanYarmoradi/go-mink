# stored-event-encryption Specification

## Purpose
TBD - created by archiving change in-place-stream-re-encryption. Update Purpose after archive.
## Requirements
### Requirement: Exported stored-event encryption primitive
`EventStore` SHALL expose `EncryptStoredEvent(ctx context.Context, stored
StoredEvent) (StoredEvent, error)` — the encode counterpart to the existing
`DecryptStoredEvent` — returning a `StoredEvent` whose configured fields are sealed
under the current `FieldEncryptionConfig` and whose metadata carries the current
envelope (`$encryption_*` markers + wrapped DEK). It MUST resolve the key exactly as
the append path does (running the configured `SubjectTagger` to stamp `$subjects`
before encrypting). It MUST be a passthrough that returns the input unchanged when no
encryption is configured, the event type has no configured fields, or the event is
already encrypted. `ReEncryptStreamInPlace` SHALL route its encode through this single
primitive.

#### Scenario: Encodes configured fields and stamps the envelope
- **WHEN** `EncryptStoredEvent` is called on a plaintext event whose type has `WithEncryptedFields`
- **THEN** the returned event's configured fields are ciphertext and its metadata reports encrypted (`IsEncrypted` true) with the wrapping key id set

#### Scenario: Round-trips with DecryptStoredEvent
- **WHEN** an event is passed through `EncryptStoredEvent` and then `DecryptStoredEvent`
- **THEN** the original plaintext data is recovered

#### Scenario: Passthrough when unconfigured or already encrypted
- **WHEN** `EncryptStoredEvent` is called with no encryption configured, on a type with no configured fields, or on an already-encrypted event
- **THEN** it returns the input `StoredEvent` unchanged with no work performed

