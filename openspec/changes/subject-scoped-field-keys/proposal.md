# Subject-scoped field-encryption keys

## Why

Per-subject crypto-shredding (Article 17) requires each subject's PII to be
encrypted under that subject's **own** master key, so revoking it shreds only
that subject. go-mink selects the field-encryption master key in
`FieldEncryptionConfig.resolveKeyID`, which today reads **only `metadata.TenantID`**:

```go
func (c *FieldEncryptionConfig) resolveKeyID(metadata Metadata) string {
    if c.tenantKeyResolver != nil && metadata.TenantID != "" {
        if keyID := c.tenantKeyResolver(metadata.TenantID); keyID != "" {
            return keyID
        }
    }
    return c.defaultKeyID
}
```

`SaveAggregate` serializes events with an empty `Metadata{}`, so **aggregate
events carry no `TenantID`** — and the data that most needs per-subject shredding
(a user's identity aggregate: email, name) is exactly aggregate-sourced. All of it
falls back to the single `defaultKeyID`, so one key protects every subject and
`DataEraser` cannot shred one subject without the `gdpr-erasure-hardening`
blast-radius guard (`WithSharedKeyGuard`) firing.

Yet the subject is **already known at encryption time**: `WithSubjectTagger`
(from `gdpr-erasure-and-retention`) runs inside the shared `prepareEventData`
hook — which covers Append **and** `SaveAggregate` — *before* `encryptFields`, and
records the subject id(s) in `$subjects` metadata. `resolveKeyID` simply doesn't
look at it.

This change lets key selection use that tag. The same `SubjectTagger` that already
defines "who this event is about" for discovery and erasure then also picks the
encryption key — so the erasure footprint and the shred key can never drift, and
per-subject keys become the norm for aggregates instead of impossible. It is the
mechanism that lets callers **avoid** shared keys, complementing
`WithSharedKeyGuard`, which only **detects** them.

## What Changes

### Required

- **Subject-tag fallback in `resolveKeyID`.** When `metadata.TenantID` is empty,
  key resolution SHALL fall back to the first `$subjects` tag (via the same
  resolver) before `defaultKeyID`. Precedence: explicit `TenantID` → first subject
  tag → `defaultKeyID`. Unchanged when no tagger/resolver is configured
  (zero-overhead-when-unused). This makes `SaveAggregate`-persisted PII resolvable
  to a per-subject key.

### Good-to-have

- **`WithSubjectKeyResolver(func(subjectID string) string)`** — a thin alias of
  `WithTenantKeyResolver` so per-subject key configuration reads by intent; it
  populates the same resolver field.

### Non-Goals

- **No change to `TenantID` precedence or existing per-tenant behavior.** Explicit
  `TenantID` still wins; this only adds a fallback when it is empty.
- **No re-resolution on decrypt.** `decryptFields` reads the wrapping key id from
  the event's own metadata (`GetEncryptionKeyID`), so events written under either
  rule decrypt unchanged; no migration.
- **No multi-subject key policy.** First-tag selection covers the identity-aggregate
  case (one subject per event). A future `WithKeyResolver(streamID, eventType, md)`
  superset for multi-subject events is noted, out of scope here.
- **Does not replace `WithSharedKeyGuard`.** Complementary: per-subject keys make
  the guard rarely fire; the guard remains the backstop for genuinely shared keys.
- **No breaking change** to `EventStore`, `FieldEncryptionConfig`, or `Metadata`;
  no forced schema (`$subjects` already lives in `Metadata.Custom`); append-only
  preserved (write-time key selection only).

## Capabilities

### New Capabilities

- `subject-scoped-field-keys` *(required)*: `resolveKeyID` falls back to the first
  `$subjects` tag when `TenantID` is empty, so aggregate events (empty metadata)
  encrypt under a per-subject master key and become individually crypto-shreddable.
- `subject-key-resolver-alias` *(good-to-have)*: `WithSubjectKeyResolver(...)`, a
  legibility alias of `WithTenantKeyResolver(...)` for per-subject key setups.

### Modified Capabilities

<!-- Extends field encryption's key-selection step only. The TenantID path,
     decrypt path, DEK envelope, and `WithSharedKeyGuard` are unchanged; no
     existing requirement is invalidated. -->

## Impact

- **Backward compatible**: `TenantID` resolution is unchanged and still wins; the
  subject-tag fallback fires only when `TenantID` is empty *and* a tagger set a tag.
  Stores with no encryption, no tagger, or no resolver are unaffected.
- **Unblocks** per-subject field encryption of aggregate PII, completing per-subject
  crypto-shred for `SaveAggregate`-sourced identity data.
- Downstream: huisscan can then encrypt clerk/user identity fields per subject and
  erase them via `DataEraser` + `RevokeKey` with a clean `Verify`.
