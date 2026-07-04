# in-place-stream-re-encryption Specification

## Purpose
TBD - created by archiving change in-place-stream-re-encryption. Update Purpose after archive.
## Requirements
### Requirement: In-place stream re-encryption
`EventStore` SHALL provide `ReEncryptStreamInPlace(ctx context.Context, streamID
string) (reEncrypted int, keyIDs []string, err error)` that brings a stream's
existing events under the current `FieldEncryptionConfig` in place — sealing each
event's configured fields under the current key and rewriting the same row — while
preserving the event's id, type, stream, version, global position, and timestamp
(the payload's at-rest encoding changes, not the event's identity or order). The key
MUST be resolved exactly as the append path would (running the configured
`SubjectTagger` first). It MUST be idempotent: an event already encrypted under the
current scheme, or of a type with no configured fields, is skipped. It MUST return
the number of events re-encrypted and the distinct wrapping key ids used. When no
encryption is configured it MUST be a no-op with zero overhead. On an adapter that
does not support in-place rewrite it MUST return `ErrRewriteNotSupported` without
writing anything.

#### Scenario: Plaintext history becomes encrypted in place
- **WHEN** `ReEncryptStreamInPlace` runs on a stream of plaintext events whose type has `WithEncryptedFields` and a `WithSubjectTagger` is configured
- **THEN** each event's on-disk data is sealed under the subject's key (`GetEncryptionKeyID` equals the tagged subject) and its id/type/version/global-position/timestamp are unchanged

#### Scenario: Re-encrypted history is crypto-shreddable
- **WHEN** a stream is re-encrypted in place and the subject's key is then revoked
- **THEN** the previously-plaintext events no longer decrypt (export reports them `Redacted`), and before revocation `Load`/`DecryptStoredEvent` return the original plaintext

#### Scenario: Idempotent and resumable
- **WHEN** `ReEncryptStreamInPlace` is run a second time on an already-re-encrypted stream, or re-run after a mid-stream failure
- **THEN** the already-encrypted events are skipped (the second run re-encrypts 0) and any remaining plaintext events are completed, never double-encrypted

#### Scenario: Unsupported adapter is refused, not silently skipped
- **WHEN** the store's adapter does not implement `EventRewriteAdapter`
- **THEN** the call returns `ErrRewriteNotSupported` and writes nothing

#### Scenario: No encryption configured is a zero-overhead no-op
- **WHEN** `ReEncryptStreamInPlace` runs on a store with no `FieldEncryptionConfig`
- **THEN** it returns `(0, nil, nil)` and rewrites no rows

### Requirement: Event rewrite adapter interface
The adapters package SHALL define an optional, structural interface
`EventRewriteAdapter` with `RewriteEventData(ctx context.Context, streamID string,
version int64, data []byte, metadata Metadata) error` that replaces only the data and
metadata of the event at `(streamID, version)`, preserving all other columns. The
PostgreSQL and in-memory adapters SHALL implement it such that a rewritten event is
byte-compatible with a natively-appended one (same metadata encoding). The base
`EventStoreAdapter` interface SHALL be unchanged, so existing custom adapters keep
compiling and behaving as before.

#### Scenario: Rewrite replaces only data and metadata
- **WHEN** `RewriteEventData` is called for an existing `(streamID, version)`
- **THEN** that event's data and metadata are replaced and its id, type, global position, and timestamp are unchanged

#### Scenario: Rewriting an absent event errors
- **WHEN** `RewriteEventData` targets a `(streamID, version)` that does not exist
- **THEN** it returns an error rather than silently inserting or no-oping

#### Scenario: Existing adapters are unaffected
- **WHEN** a custom adapter implements only the base `EventStoreAdapter`
- **THEN** it compiles and behaves exactly as before, and `ReEncryptStreamInPlace` reports it unsupported

