## ADDED Requirements

### Requirement: Subject-tag fallback for master-key resolution
`FieldEncryptionConfig.resolveKeyID` SHALL resolve the master key id in this
precedence: (1) `metadata.TenantID` when non-empty, (2) otherwise the first
`$subjects` tag (`GetSubjectTags(metadata)[0]`) when present, (3) otherwise
`defaultKeyID`. The chosen subject/tenant id SHALL be passed through the configured
`tenantKeyResolver`. When no `tenantKeyResolver` is configured, resolution SHALL
return `defaultKeyID` unchanged. The tag lookup SHALL add no cost when no
`SubjectTagger` is configured (no tags present).

This makes events written via `SaveAggregate` — which carry an empty `Metadata{}`
and therefore no `TenantID` — resolvable to a per-subject master key, using the
subject the `SubjectTagger` already recorded in `prepareEventData` before
encryption.

#### Scenario: Aggregate event encrypts under the subject's key
- **GIVEN** a store with a `SubjectTagger` that tags a user's identity events with the user id, `WithEncryptedFields` for that event, and a `tenantKeyResolver` mapping id → per-subject key
- **WHEN** the identity aggregate is persisted with `SaveAggregate` (empty metadata)
- **THEN** the stored event's wrapping key id (`GetEncryptionKeyID`) equals the subject's key id, not `defaultKeyID`

#### Scenario: Revoking one subject shreds only that subject
- **GIVEN** two subjects whose aggregate PII was encrypted under their respective subject keys
- **WHEN** one subject's master key is revoked (`RevokeKey`)
- **THEN** that subject's encrypted events fail to decrypt / export as `Redacted`, and the other subject's events still decrypt

#### Scenario: Explicit TenantID takes precedence over a subject tag
- **WHEN** an event has both a non-empty `metadata.TenantID` and a `$subjects` tag
- **THEN** `resolveKeyID` uses the `TenantID` (existing behavior is preserved)

#### Scenario: No tagger and no tenant resolves to the default key
- **WHEN** an event has empty `TenantID`, no `$subjects` tag, or no `tenantKeyResolver` is configured
- **THEN** `resolveKeyID` returns `defaultKeyID` — identical to pre-change behavior (zero overhead when unused)

#### Scenario: Decrypt is unaffected by the resolution rule
- **GIVEN** events written under the tenant rule and events written under the subject-tag rule
- **WHEN** each is loaded/decrypted
- **THEN** both decrypt correctly, because the wrapping key id is read from the event's own metadata and `resolveKeyID` is not re-run on decrypt — no migration is required
