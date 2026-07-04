## Context

Field encryption picks a master key per event in
`FieldEncryptionConfig.resolveKeyID(metadata)`, using only `metadata.TenantID`.
The DEK envelope then wraps a per-event data key under that master key, and
`RevokeKey(masterKeyID)` crypto-shreds every event that used it.

`prepareEventData` is the single shared write hook for `Append`, `SaveAggregate`,
and outbox. It runs the configured `SubjectTagger` **before** `encryptFields`, so
by the time `resolveKeyID` is called the `$subjects` tag is already present in
`metadata.Custom` (`GetSubjectTags(md)`), for aggregate events too. `SaveAggregate`
passes an empty `Metadata{}`, so `TenantID == ""` for aggregates and the current
resolver returns `defaultKeyID` — one key for everyone.

## Goals / Non-Goals

- **Goal**: let aggregate PII (empty `TenantID`) resolve to a per-subject master key
  using the subject the tagger already recorded, so it is individually shreddable.
- **Goal**: keep one source of truth for "who is this event about" — the
  `SubjectTagger` — driving both the erasure footprint and the encryption key.
- **Non-goal**: changing `TenantID` precedence, the decrypt path, or the DEK envelope.
- **Non-goal**: any breaking change; any behavior change when unconfigured.

## Decisions

### D1 — Fallback in `resolveKeyID`, reusing the existing resolver
Add a single fallback: when `TenantID == ""`, use the first `$subjects` tag as the
resolver input. The `$subjects` tag is already produced and stored by the tagger
before encryption, so nothing new is threaded through the call path.

```go
func (c *FieldEncryptionConfig) resolveKeyID(metadata Metadata) string {
    if c.tenantKeyResolver != nil {
        subject := metadata.TenantID          // 1. explicit tenant wins (unchanged)
        if subject == "" {                    // 2. else the primary data subject
            if tags := GetSubjectTags(metadata); len(tags) > 0 {
                subject = tags[0]
            }
        }
        if subject != "" {
            if keyID := c.tenantKeyResolver(subject); keyID != "" {
                return keyID
            }
        }
    }
    return c.defaultKeyID                       // 3. unchanged fallback
}
```

Zero overhead when unused: no tagger ⇒ no tags ⇒ identical to today; no resolver ⇒
`defaultKeyID` as today.

### D2 — First tag is the primary subject
An event normally concerns one data subject; `tags[0]` is that subject. For the
rare multi-subject event, first-tag means the resource is keyed (and thus shredded)
with its primary subject — acceptable and documented. Finer control is a future
`WithKeyResolver(streamID, eventType, md) string` superset, explicitly out of scope
so this change stays a one-line, non-breaking fallback.

### D3 — Decrypt is untouched
`decryptFields` resolves the wrapping key id from the event's stored metadata
(`GetEncryptionKeyID`), not by re-running `resolveKeyID`. Events written under the
old (tenant/default) rule and the new (subject) rule both decrypt correctly with no
migration. The change is write-time only; history is never rewritten.

### D4 — `WithSubjectKeyResolver` is an alias, not a new field
Per-subject keys configured through `WithTenantKeyResolver` read misleadingly.
`WithSubjectKeyResolver(fn)` sets the same `tenantKeyResolver` field — pure
legibility, no new resolution path to keep in sync.

## Interaction with `gdpr-erasure-hardening`
`WithSharedKeyGuard` detects and blocks revoking a key that protects more than one
subject. With per-subject keys now reachable for aggregates, that guard becomes a
backstop rather than the common path: correctly-tagged aggregate PII resolves to a
subject-exclusive key, so erasing one subject no longer trips the guard. The two are
complementary — this change reduces shared keys; the guard catches the ones that
remain (shared-resource events, untagged legacy events).
