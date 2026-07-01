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
  `mink.ReEncryptStream(ctx, store, src, dst)` — it re-encrypts into a new stream under
  the current key and returns `(copied, oldKeyIDs, err)`. It **erases nothing**: the
  source stream and its old-key-recoverable PII survive until you retire the source and
  revoke the returned `oldKeyIDs`. Re-running against an existing destination errors
  rather than duplicating the copy.

## Erasure completeness (hardening)

Crypto-shredding only reaches data encrypted under the revoked key. These controls close
the gaps where an erasure can *look* done while leaving recoverable PII behind.

### Sibling stores — audit, saga, snapshots

PII derived from events lives in stores the event key does not protect: the **audit
trail** (plaintext actor / tenant / metadata / error strings), **saga state** (business
data copied out of events), and **snapshots** (plaintext aggregate state). Register them
so `Erase` reaches them too:

```go
eraser := mink.NewDataEraser(store,
    mink.WithEraseSubjectResolver(resolver),
    mink.WithSubjectStore(
        mink.NewAuditSubjectEraser(auditStore),      // deletes rows where actor|aggregate_id == subject
        mink.NewSagaSubjectEraser(sagaStore),        // deletes sagas where correlation_id == subject
        mink.NewSnapshotSubjectEraser(adapter),      // deletes snapshots for the footprint's streams
    ),
)
// res.SubjectStores reports what each erased; a per-store failure is non-fatal.
```

The audit/saga purges use optional adapter sub-interfaces (`SubjectAuditPurger` /
`SubjectSagaPurger`) implemented on the memory and PostgreSQL stores; a store that lacks
them is reported as `Skipped`, not failed. For an **outbox** with a `route.Transform` that
emits *decrypted* payloads to an external sink, revoking the key does not touch that
sink's copy — register a `SubjectErasable` for the sink (or keep transforms
ciphertext-only).

### Blast-radius guard (per-tenant keys)

With `WithTenantKeyResolver`, one key protects a whole tenant, so erasing a single subject
would crypto-shred everyone under it. `WithSharedKeyGuard()` detects this **before** the
irreversible revoke and fails with `*SharedKeyError`; pair with `AllowSharedKeyRevocation()`
to proceed deliberately (per-subject keys avoid the problem entirely).

### Accountability & the discovery race

- `WithStrictAccountability()` makes a lost marker/certificate a fatal error (after the
  idempotent revoke), and a certificate is never `Verified` unless its marker was written.
- `Erase` re-resolves once after revoking and shreds any late-appearing keys, flagging
  `Partial` if the footprint grew. For a race-free erasure, **quiesce the subject's writes
  first** (mark it non-writable) — the guarantee holds only when writes are stopped.
- Soft-revoke is not erasure: `Verify` reports a soft-revoked (still-restorable) key as
  `ResidualRecoverable` and refuses to certify it until the grace window elapses (at which
  point the local provider shreds the key material).
- Partial failures are non-fatal by contract; check `res.Failed()` (or the gap between
  requested keys and `res.KeysRevoked`), not just the returned error.

## Subject index & backfill

Discovery/erasure scan the whole store unless a subject index is available. Wire an index
to make them O(a subject's events), and **backfill** it so subjects whose events predate
tag adoption are still fully resolvable — and therefore fully erasable:

```go
idx := mink.NewMemorySubjectIndex() // or postgres.NewSubjectIndex(db) — a durable mink_subject_index table
store := mink.New(adapter,
    mink.WithSubjectTagger(tagger),
    mink.WithSubjectIndexWriter(idx), // append-time indexing keeps it complete
)
// One-time migration for pre-adoption history:
mink.BackfillSubjectIndex(ctx, store, tagger, idx, 1000)

// After a clean backfill (or with a transactionally-consistent index) you may assert
// completeness; without the assertion an index-backed resolve is honestly Partial.
resolver := mink.NewSubjectResolver(store, mink.WithResolverIndex(idx), mink.WithAuthoritativeIndex())
```

**Index authority.** Append-time index writes are best-effort (a failed write is logged,
not fatal), so an index can silently drift behind the log. `WithResolverIndex` therefore
treats the index as possibly-incomplete and marks the footprint `Partial` unless you also
pass `WithAuthoritativeIndex` to assert completeness — so a drifted index can never
produce a falsely-complete footprint that makes `Erase` miss streams while certifying
success. Reconcile a drifted index with `BackfillSubjectIndex`. Without any index, legacy
untagged events keep a footprint `Partial` (never a *silent* partial).

> **Subject identifiers are plaintext and are NOT shredded.** The `$subjects` tag and
> `Metadata` (UserID / CorrelationID) are stored in cleartext so they stay scannable, so
> crypto-shredding a subject's event *fields* leaves their *identifier* in the append-only
> log forever. If your subject id is itself PII (an email, a national id), you have not
> fully erased the person. Tag with an **opaque/pseudonymous** id (see `Anonymizer`) and
> treat metadata identifiers as PII under the same discipline.
