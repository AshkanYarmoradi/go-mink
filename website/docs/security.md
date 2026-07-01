---
title: Security & GDPR
sidebar_position: 12
---

# Security & GDPR

go-mink treats personal data as a first-class concern. This guide covers the full
data-governance lifecycle: **encryption → subject discovery → export → erasure →
retention**, all built to preserve the append-only event log (no row is ever
deleted or mutated).

## Field-level encryption

Protect PII at rest with envelope encryption — individual JSON fields are encrypted
while the rest of the event stays queryable. Configure it once on the store:

```go
cfg := mink.NewFieldEncryptionConfig(
    mink.WithEncryptionProvider(provider),          // local / AWS KMS / Vault
    mink.WithDefaultKeyID("tenant-A"),
    mink.WithEncryptedFields("CustomerCreated", "email", "address.street"),
    mink.WithDecryptionErrorHandler(func(err error, _ string, _ mink.Metadata) error {
        if errors.Is(err, encryption.ErrKeyRevoked) {
            return nil // crypto-shredded — surface as redacted, don't fail
        }
        return err
    }),
)
store := mink.New(adapter, mink.WithFieldEncryption(cfg))
```

Encryption metadata lives in `Metadata.Custom` (no DB schema changes). It is
zero-overhead when unconfigured.

## Crypto-shredding (key revocation)

The GDPR right to erasure is implemented by **crypto-shredding**: revoke a key and
the data encrypted under it becomes permanently unrecoverable. Providers opt in to
the optional `encryption.Revocable` interface:

```go
// RevokeKey is idempotent. IsRevoked reports current state.
err := encryption.Revoke(provider, "tenant-A")   // or provider.(Revocable).RevokeKey(...)
```

| Provider | Revocation mechanism | Notes |
|----------|---------------------|-------|
| **local** | deletes/zeroes key material | testing only; immediate, permanent |
| **AWS KMS** | `ScheduleKeyDeletion` (via `KMSRevocationClient`) | key is immediately unusable; destroyed after the pending window (7–30 days) |
| **Vault Transit** | `DeleteKey` (via `VaultRevocationClient`) | requires `deletion_allowed` on the key |

KMS/Vault gain revocation through an **optional client sub-interface**, so the base
`KMSClient`/`VaultClient` you inject is never forced to change. A provider whose
client lacks it returns `encryption.ErrRevocationUnsupported`.

### Recoverable revocation

`encryption.RecoverableRevocable` adds a grace window: `SoftRevokeKey(keyID, window)`
blocks decryption but `UnrevokeKey(keyID)` can restore it until the window elapses,
after which it becomes permanent — so an accidental erasure can be undone.

## Subject discovery

A data subject usually spans many streams (a user *and* their orders, payments,
…). Tag events at append time so a subject's complete footprint can be resolved:

```go
store := mink.New(adapter,
    mink.WithFieldEncryption(cfg),
    mink.WithSubjectTagger(func(_ string, _ []byte, md mink.Metadata) []string {
        if md.UserID != "" { return []string{md.UserID} }
        return nil
    }),
)

resolver := mink.NewSubjectResolver(store)
fp, _ := resolver.Resolve(ctx, "user-123") // fp.Streams, fp.KeyIDs, fp.EventCount, fp.Partial
```

The resolver uses an adapter subject index when available (`SubjectIndexAdapter`),
else a scan. If completeness can't be proven (legacy untagged events), `fp.Partial`
is `true` — **never a silent partial**. `Resolve` is read-only, so it doubles as an
erasure preview.

## Data export (Article 15 / 20)

`DataExporter` collects a subject's events, decrypting where possible and marking
crypto-shredded events as `Redacted`. Wire the resolver in to export a subject by id
alone:

```go
exporter := mink.NewDataExporter(store, mink.WithExportSubjectResolver(resolver))
res, _ := exporter.Export(ctx, mink.ExportRequest{SubjectID: "user-123"})
// res.Events, res.RedactedCount, res.Partial
```

## Data erasure (Article 17)

`DataEraser` is the erasure counterpart to `DataExporter`. In one call it resolves
the subject, revokes its keys, redacts read models, runs external-PII hooks, appends
an optional marker, and emits a verification certificate:

```go
eraser := mink.NewDataEraser(store,
    mink.WithEraseSubjectResolver(resolver),
    mink.WithReadModelRedactor(usersReadModel),               // in-place hook (preferred)
    mink.WithReadModelRebuilder(mink.ReadModelRebuilder{...}), // or rebuild-to-redacted
    mink.WithErasureHook(mink.ErasureHook{Name: "blob-storage", Run: deleteBlobs}),
    mink.WithErasureMarker("erasure-log"),
    mink.WithCertificateSink(writeToAuditStore),
)
res, _ := eraser.Erase(ctx, mink.ErasureRequest{SubjectID: "user-123"})
// res.KeysRevoked, res.RedactedReadModels, res.SideEffects, res.Partial, res.Errors

report, _ := eraser.Verify(ctx, "user-123") // report.Verified, report.ResidualEncrypted/Cleartext
```

`Erase` is idempotent. Partial failures (a read-model hook, a side-effect hook) are
reported in `res.Errors`, never fatal.

> **Per-subject vs per-tenant keys.** Crypto-shredding erases *everything* under a
> revoked key. For subject-scoped erasure, use per-subject keys; with per-tenant
> keys (`WithTenantKeyResolver`) revoking a key erases the whole tenant.
> `res.KeysRevoked` always reports the blast radius.

> **Legacy cleartext.** PII written *before* field-encryption was enabled cannot be
> crypto-shredded. `Verify` flags it as `ResidualCleartext`; remediate with a
> `RedactFields`/`Anonymize` retention policy on the read side.

## Retention policies

Enforce configurable retention rules on a schedulable sweep:

```go
mgr := mink.NewRetentionManager(store, []mink.RetentionPolicy{
    {Name: "old-customers", Category: "Customer", MaxAge: 365 * 24 * time.Hour, Action: mink.ActionShred},
})
report, _ := mgr.DryRun(ctx) // preview, no changes
report, _ = mgr.Apply(ctx)   // report.Matched, report.KeysRevoked, report.Skipped, report.Errors
```

Actions preserve the append-only log: `ActionShred` revokes keys; `ActionRedactFields`
and `ActionAnonymize` delegate to the policy's `Apply` hook (go-mink cannot mutate
event rows). Pseudonymize with a deterministic, one-way `Anonymizer`.

## Key lifecycle

- **Rotation** is transparent: each event records its key id, so rotating the default
  key only affects new appends; old events keep decrypting. KMS/Vault native rotation
  is likewise transparent.
- **Re-encryption** after a suspected compromise is append-only via
  `mink.ReEncryptStream(ctx, store, src, dst)` — it re-encrypts into a new stream
  under the current key, leaving the source intact.
