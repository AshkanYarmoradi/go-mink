---
title: GDPR & Data Governance
sidebar_label: GDPR & Data Governance
sidebar_position: 11
---

# GDPR & Data Governance

go-mink treats personal data as a first-class concern. This guide covers the full
data-governance lifecycle: **encryption → subject discovery → export → erasure →
retention**, all built to preserve the append-only event log (no row is ever
deleted or mutated).

> This is the task-oriented guide to the data-subject rights (Articles 15, 17, 20)
> and retention. For the field-level encryption reference (providers, configuration,
> the on-disk format) see [Security & Compliance](/docs/advanced/security); to drive
> the same operations from the command line see [`mink gdpr`](/docs/guide/cli#mink-gdpr).

At a glance:

| Right / concern | API | Section |
|-----------------|-----|---------|
| Erasure (Art. 17) | `DataEraser.Erase` | [Data erasure](#data-erasure-article-17) |
| Access / portability (Art. 15 / 20) | `DataExporter.Export` | [Data export](#data-export-article-15--20) |
| Find a subject's data | `SubjectResolver.Resolve` | [Subject discovery](#subject-discovery) |
| Make data unrecoverable | `encryption.Revoke` (crypto-shred) | [Crypto-shredding](#crypto-shredding-key-revocation) |
| Time-limited retention | `RetentionManager` | [Retention policies](#retention-policies) |
| Reach derived PII (audit/saga/…) | `WithSubjectStore` | [Sibling stores](#sibling-stores--audit-saga-snapshots-outbox-idempotency) |

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

> **Security note — the envelope is library-owned.** `Append` / `SaveAggregate` (and the
> outbox wrapper) strip the four envelope keys (`$encrypted_fields`, `$encryption_key_id`,
> `$encrypted_dek`, `$encryption_algorithm`) from caller-supplied `Metadata.Custom` before
> anything is stamped — `mink.SanitizeReservedMetadata` is the exported helper — so a writer
> that controls metadata (an HTTP API copying request headers, say) cannot make plaintext look
> encrypted, point decryption or erasure at another key, or skip encryption. In the same pass
> a caller-supplied `$schema_version` is **dropped** — the upcaster chain's latest version is
> stamped instead (`mink.WithCallerSchemaVersion()` lets trusted migration tooling keep an
> in-range value for a type that has upcasters; `ReEncryptStream` preserves it on its own) —
> and a configured `SubjectTagger` **replaces** caller-supplied `$subjects` tags
> (`mink.WithCallerSubjectTags()` restores merging for trusted writers); the same tag policy
> governs `EncryptStoredEvent` / `ReEncryptStreamInPlace`, so a tag forged *at rest* cannot
> pick the wrapping key either. Decryption **fails
> closed**: a field listed in `$encrypted_fields` that is absent or not a string returns
> `encryption.ErrDecryptionFailed` naming the field, instead of passing a substituted value
> through as plaintext — and integers above 2^53 survive the encrypt/decrypt round trip exactly.

## Crypto-shredding (key revocation)

The GDPR right to erasure is implemented by **crypto-shredding**: revoke a key and
the data encrypted under it becomes unrecoverable once the provider has destroyed the
key material — immediately for the local provider, only after the pending-deletion
window for AWS KMS (see the table). Providers opt in to the optional
`encryption.Revocable` interface:

```go
// RevokeKey is idempotent. IsRevoked reports current state.
err := encryption.Revoke(provider, "tenant-A")   // or provider.(Revocable).RevokeKey(...)
```

| Provider | Revocation mechanism | Notes |
|----------|---------------------|-------|
| **local** | deletes/zeroes key material | testing only; immediate, permanent |
| **AWS KMS** | `ScheduleKeyDeletion` (via `KMSRevocationClient`) | key is immediately unusable but **not yet destroyed**: `CancelKeyDeletion` can restore it until the pending window (7–30 days, `WithPendingDeletionWindow`) elapses. The provider reports it as `SoftRevoked` (still recoverable) via `RevocationState` until AWS completes the deletion, so `Verify` does not certify the erasure inside the window. Ids are resolved through `DescribeKey` **at call time**, so **stamp key ARNs, never aliases**: `RevokeKey("alias/x")` shreds whatever the alias points at *then*, re-pointing the alias later changes what `IsRevoked` answers, and a deleted alias is indistinguishable from a deleted key. `UnrevokeKey` (client implementing `KMSDeletionCanceller`) is state-driven and retryable — `CancelKeyDeletion` + `EnableKey` for a pending key, `EnableKey` alone for a key left merely disabled by an earlier half-finished restore, a no-op for an enabled key — and returns `*kms.KeyPermanentlyDeletedError` (`errors.Is` → `kms.ErrKeyPermanentlyDeleted` and `encryption.ErrKeyRevoked`) once the deletion has completed; the context-free revocation calls are bounded by `WithRevocationTimeout` (30s default) |
| **Vault Transit** | `DeleteKey` (via `VaultRevocationClient`) | requires `deletion_allowed` on the key. Key names are validated before any request — non-empty, no `/`, no control bytes, not the whole segment `.` or `..`; anything else (spaces, `?`, `%`, non-ASCII, an inner `a..b`) is accepted and `url.PathEscape`d before the `VaultClient` sees it — so an untrusted tenant/subject id can never escape the Transit mount while existing key names keep decrypting. `RevokeKey`/`IsRevoked` are bounded by `vault.WithRevocationTimeout` (`DefaultRevocationTimeout` 30s) |

KMS/Vault gain revocation through an **optional client sub-interface**, so the base
`KMSClient`/`VaultClient` you inject is never forced to change. A provider whose
client lacks it returns `encryption.ErrRevocationUnsupported`.

### Recoverable revocation

`encryption.RecoverableRevocable` adds a grace window: `SoftRevokeKey(keyID, window)`
blocks decryption but `UnrevokeKey(keyID)` can restore it until the window elapses,
after which it becomes a permanent crypto-shred (the local provider actually wipes the
key material). Use the package helpers so a provider that lacks the capability is
reported, not silently hard-revoked:

```go
encryption.SoftRevoke(provider, "tenant-A", 7*24*time.Hour) // ErrRevocationUnsupported if not RecoverableRevocable
encryption.Unrevoke(provider, "tenant-A")                    // undo within the window
```

`encryption.GetRevocationState(provider, keyID)` returns the fine-grained state —
`NotRevoked`, `SoftRevoked`, or `Revoked` — via the optional `StatefulRevocable`
interface (falling back to `IsRevoked` for providers without it). This is what lets
`Verify` distinguish a still-recoverable soft-revocation from a permanent shred and
refuse to certify the former (see [Accountability](#accountability--the-discovery-race)).

On AWS KMS every scheduled deletion is recoverable until AWS completes it, so the
provider implements both interfaces natively: `SoftRevokeKey(keyID, window)` schedules
deletion with a pending window derived from the grace duration (rounded up to whole days,
clamped to the 7–30 days AWS accepts), `UnrevokeKey` cancels it and re-enables the key, and
`RevocationState` reports `SoftRevoked` until the deletion has actually completed.

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
fp, _ := resolver.Resolve(ctx, "user-123")
// fp.Streams, fp.SharedStreams, fp.ExclusiveStreams(), fp.KeyIDs, fp.CleartextEvents,
// fp.EventCount, fp.Partial
```

The resolver uses an adapter subject index when available (`SubjectIndexAdapter`),
else a scan. If completeness can't be proven (legacy untagged events), `fp.Partial`
is `true` — **never a silent partial**. `Resolve` is read-only, so it doubles as an
erasure preview.

**Shared vs exclusive streams.** `fp.SharedStreams` is the subset of `fp.Streams` in which
the resolver saw an event tagged for *another* subject (an order with buyer and seller, a
conversation); `fp.ExclusiveStreams()` is the rest. Key revocation is unaffected — it is
scoped to the subject's own events — but rows that sibling stores key by a stream id cannot
be attributed to one subject on a shared stream, so the built-in erasers purge and count only
the exclusive streams (see [Sibling stores](#sibling-stores--audit-saga-snapshots-outbox-idempotency)).
Untagged events never make a stream shared; they make the footprint `Partial`.

**What counts as encrypted.** `fp.KeyIDs` lists only the keys of events that carry a
*complete* envelope — `mink.HasEncryptionEnvelope` (`$encrypted_fields` + `$encryption_key_id`
+ `$encrypted_dek`); a tagged event with a bare `$encryption_key_id` and nothing else is
plaintext as far as crypto-shredding is concerned and counts in `fp.CleartextEvents`. The
same predicate drives `DataEraser` key discovery, the shared-key guard, `Verify` and
`RetentionManager`, so a key that protects no ciphertext is never revoked anywhere.

## Data export (Article 15 / 20)

`DataExporter` collects a subject's events, decrypting where possible and marking
crypto-shredded events as `Redacted`. Wire the resolver in to export a subject by id
alone:

```go
exporter := mink.NewDataExporter(store, mink.WithExportSubjectResolver(resolver))
res, _ := exporter.Export(ctx, mink.ExportRequest{SubjectID: "user-123"})
// res.Events, res.RedactedCount, res.Partial
```

**Shared streams are subject-scoped by default.** When you pass explicit `Streams` and no
`Filter`, the exporter applies `mink.SubjectOrUntaggedFilter(SubjectID)`: events tagged
(via `WithSubjectTagger`) for *other* subjects are dropped, while untagged events — which
cannot be attributed to anyone — are still exported, so a shared "order" stream never leaks
a co-subject's events into an Article 15 export. Pass an explicit `Filter` (e.g.
`mink.FilterByStreams(...)`) to export a whole stream deliberately, or `mink.SubjectFilter`
to drop untagged events as well.

**Filters fail closed.** An empty selector matches *nothing*: `FilterByTenantID("")`,
`FilterByUserID("")`, `FilterByStreamPrefix("")` and `FilterByMetadata` with an empty key
or value select no events, and `CombineFilters()` with no filters (or a `nil` filter)
matches nothing — an empty conjunction is not a wildcard in a GDPR export.
`FilterByStreamPrefix` is a plain prefix match (`"user-1"` also matches `"user-10"`); use
`FilterByStreams(ids...)` for exact stream ids or `FilterByStreamCategory("user")` for a
whole category.

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
// res.KeysRevoked, res.KeysFailed, res.CleartextEvents, res.Streams, res.SharedStreams,
// res.RedactedReadModels, res.SideEffects, res.Partial, res.Errors, res.Notes,
// res.SubjectIndexPurged

report, _ := eraser.Verify(ctx, "user-123")
// report.Verified, report.Vacuous, report.ResidualEncrypted / ResidualRecoverable /
// ResidualCleartext, report.ResidualStores, report.UncheckedStores, report.Notes
```

`Erase` is idempotent. Partial failures (a read-model hook, a side-effect hook) are
reported in `res.Errors`, never fatal.

**What the certificate attests.** The `ErasureCertificate` handed to `WithCertificateSink`
is PII-free and is `Verified` only when it could *positively* attest the erasure: every
subject-tagged event checked is unrecoverable, every key was revoked (`res.KeysFailed` is
empty), every sibling store that could be counted is clean, the footprint is complete, and
the marker (if configured) was written. It is **never vacuously verified**: an erasure that
revoked keys or targeted streams but checked zero tagged events — a `KeyIDs`-only request,
explicit `Streams` over untagged data, a legacy untagged store — yields `Verified=false`
with a `Notes` entry saying so (a genuinely empty footprint is verified with a "nothing to
erase" note). `Verify` applies the same rule: a report that checked zero subject-tagged
events sets `report.Vacuous` with `Verified=false` and a note — nothing could be attested;
it is not a finding of residual PII (this is what an index-backed resolver returns after
`WithSubjectIndexPurge`; re-verify with a scan-backed resolver or after a backfill).
`cert.StoresVerified` / `cert.StoresUnchecked` name the sibling stores that
were proven clean / could not be counted (see
[Sibling stores](#sibling-stores--audit-saga-snapshots-outbox-idempotency)); a store that
had to skip footprint streams shared with other subjects is dropped from `StoresVerified`
with a note and keeps the certificate unverified. `res.CleartextEvents` counts matched
events with no complete encryption envelope (`mink.HasEncryptionEnvelope`) that stay
readable after the erasure. All notes are counts and store names only — never ids.

**Stream-scoped erasure.** With `ErasureRequest.Streams`, a listed stream that holds *any*
subject-tagged event contributes only the keys of events tagged for the target subject, so a
co-tenant's key on a shared stream is never revoked. A listed stream with no tagged events
keeps the all-keys behavior and is called out in `res.Notes` (and on the certificate). A
listed stream holding *both* tagged and untagged events makes the result `Partial` (so
`res.Failed()` is true and the certificate is not `Verified`) with a count-only note: the
untagged events in it — possibly the subject's own pre-tagging history — were not shredded.

> **Per-subject vs per-tenant keys.** Crypto-shredding erases *everything* under a
> revoked key. For subject-scoped erasure, use per-subject keys; with per-tenant
> keys (`WithTenantKeyResolver`) revoking a key erases the whole tenant.
> `res.KeysRevoked` always reports the blast radius.

> **Legacy cleartext.** PII written *before* field-encryption was enabled cannot be
> crypto-shredded. `Verify` flags it as `ResidualCleartext`; remediate with a
> `RedactFields`/`Anonymize` retention policy on the read side.

## Retention policies

Enforce configurable retention rules with `RetentionManager`. A `RetentionPolicy` is a
matcher (`Category` / `StreamPrefix` / `EventTypes` / `TenantID` / `MaxAge`, ANDed) plus
an action:

```go
mgr := mink.NewRetentionManager(store, []mink.RetentionPolicy{
    {Name: "old-customers", Category: "Customer", MaxAge: 365 * 24 * time.Hour, Action: mink.ActionShred},
    {Name: "pseudonymize-analytics", EventTypes: []string{"PageViewed"}, MaxAge: 90 * 24 * time.Hour,
        Action: mink.ActionAnonymize, Fields: []string{"ip"},
        Apply: func(ctx context.Context, e mink.StoredEvent) error {
            return analytics.Pseudonymize(ctx, e, anonymizer) // you own the read-side write
        }},
})
report, _ := mgr.DryRun(ctx) // preview: report.KeysToRevoke, report.SharedKeysSkipped, report.UnencryptedMatches
report, _ = mgr.Apply(ctx)   // report.Matched, report.Acted, report.Skipped, report.KeysRevoked, report.Errors
```

Actions preserve the append-only log: `ActionShred` revokes keys; `ActionRedactFields`
and `ActionAnonymize` **cannot** mutate event rows, so they delegate to the policy's
`Apply` hook (applied to read models / external stores).

**Shred guard (on by default).** `ActionShred` revokes a *master* key, which erases every
event encrypted under it — not only the events the policy matched. Before any irreversible
revocation the manager therefore scans the whole store (from position 0, regardless of any
checkpoint) and revokes a key only if **every** field-encrypted event under it is covered by
a Shred policy of this sweep (static matchers *and* age-eligible; `RedactFields` /
`Anonymize` matches do not count). A key that also protects out-of-scope events is skipped,
listed in `report.SharedKeysSkipped`, and reported as a `*mink.RetentionSharedKeyError`
(`errors.Is(err, mink.ErrRetentionSharedKey)`; it carries the key id and an out-of-scope
*count*, never events), so `report.Failed()` is true. `report.KeysToRevoke` is the
guard-approved set. `DryRun` runs the same guard, so it previews the exact blast radius
`Apply` would act on. Keep keys exclusive by giving each retention scope its own key
(`WithSubjectKeyResolver` / `WithTenantKeyResolver`); `mink.WithAllowSharedKeyRevocation()`
disables the guard — dangerous, and only after a `DryRun` has shown that the whole blast
radius is acceptable. **Accounting is honest:** a Shred match counts in `report.Acted` only
when its key ended up in `report.KeysRevoked`; a match whose key the guard refused, failed to
revoke, or could not be revoked (no encryption configured) counts in `report.Skipped` and is
re-swept by the next run (`DryRun` leaves both at `0`). Note that `RetentionSharedKeyError`
and the report's key lists *do* print key ids — an operator needs them to split keys — so
treat retention output as sensitive when key ids embed subject ids.

**Nothing is shredded silently.** A Shred policy collects keys only from events carrying a
complete encryption envelope (`mink.HasEncryptionEnvelope`); matched events with no envelope
(legacy cleartext, or a bare `$encryption_key_id` with no field list / wrapped DEK) are counted
in `report.UnencryptedMatches` and surface `mink.ErrRetentionUnencryptedMatches`, so a
sweep that left matched plaintext behind reports `Failed()` instead of a quiet `Skipped`. A
policy with **no matchers at all** (`Category`, `StreamPrefix`, `EventTypes`, `TenantID`
empty and no `MaxAge`) would match every event: `Validate()` rejects it with
`mink.ErrRetentionUnscopedPolicy` and every run leaves it inert. `StreamPrefix` is a plain
prefix (`"user-1"` also matches `"user-10"`) — end it with the id separator.

**Scheduling is yours.** `Apply` performs a single sweep and returns — go-mink does not
run it on a timer. Wire it to your own cron/gocron at your SLA's cadence.

**Bounded, resumable sweeps (opt-in).** A plain `Apply` scans the whole store on every
run. On a large, ever-growing log that means a scheduled sweep keeps re-scanning history
it already handled. Two opt-in options fix that with no change to default behavior:

```go
mgr := mink.NewRetentionManager(store, policies,
    mink.WithRetentionCheckpoint(checkpointStore, "__mink_retention__"), // resume across runs
    mink.WithRetentionMaxScan(200_000),                                  // bound a single run
)
```

`WithRetentionCheckpoint` persists a **safe-resume frontier** — the highest position below
which no event can *newly* match — through the same `CheckpointStore` your projections use,
so each sweep resumes instead of re-scanning from 0. Steady-state cost then tracks the
retention window, not total history. `WithRetentionMaxScan(n)` caps a single sweep to `n`
events and resumes the remainder next run (it needs a checkpoint; without one it is
reported loudly and runs unbounded) — which bounds the first run after enabling retention
on an already-large store. A capped run sets `report.Truncated`.

The checkpoint and the shred guard compose safely: the persisted frontier is held back to
just before the first Shred match whose key was *not* revoked in this sweep (refused by the
guard, failed `RevokeKey`, or no encryption configured), so those events are re-scanned,
re-matched and re-reported on every later run until the key is revoked — after
`WithAllowSharedKeyRevocation`, a covering Shred policy or a key split — with no checkpoint
reset. `WithRetentionMaxScan` never truncates a sweep whose resume point is held at such an
undecided match (it scans to HEAD, like the pending-event fallback), so a refused key cannot
starve the aged tail.

**Fail-loud validation.** A `RedactFields`/`Anonymize` policy with *no* `Apply` hook can
never act. `mgr.Validate()` (or `policy.Validate()`) surfaces this up front, and every
`Apply`/`DryRun` reports it via `report.Errors` / `report.Failed()` rather than silently
counting it as `Skipped` — so you can never think you anonymized when you didn't.

**Pseudonymization.** `mink.NewAnonymizer(secret, ...)` gives a deterministic, one-way
HMAC pseudonym (stable per scope), suitable for `ActionAnonymize` `Apply` hooks or for
replacing PII subject identifiers:

```go
anon := mink.NewAnonymizer(hmacSecret)
if err := anon.Validate(); err != nil { // mink.ErrAnonymizerSecretRequired on an empty secret
    log.Fatal(err)
}
pseudo := anon.Pseudonymize("email", "alice@example.com") // stable, irreversible
```

An empty secret degrades the HMAC to an unkeyed hash whose pseudonyms can be recovered by
guessing the input, so call `Validate()` at startup — `NewAnonymizer` itself never fails.

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
- **In-place backfill** of a stream that already has data — for a consumer that turned
  encryption on *after* accumulating history — is
  `store.ReEncryptStreamInPlace(ctx, streamID) (reEncrypted int, keyIDs []string, err error)`.
  It seals each not-yet-encrypted event's configured fields under the current key and
  **rewrites the same rows**, preserving id/type/stream/version/global-position/timestamp
  — only the at-rest encoding changes, so history is not rewritten (the same category of
  consumer-owned mutation as retention `RedactFields`). Use it when your aggregates read
  **fixed** stream ids (`ReEncryptStream`'s copy-to-new-stream doesn't fit those). It is:
  - **idempotent / resumable** — already-encrypted events are skipped, so a re-run or a
    resume after a mid-stream failure is safe;
  - **opt-in** — requires an adapter implementing `adapters.EventRewriteAdapter` (postgres
    + memory ship it), else `ErrRewriteNotSupported`; a no-op with zero overhead when no
    encryption is configured;
  - the historical counterpart to crypto-shredding: once backfilled, revoking a subject's
    key shreds their previously-plaintext events too.

  ```go
  // One-off, operator-triggered, in a maintenance window. Idempotent.
  n, keyIDs, err := store.ReEncryptStreamInPlace(ctx, "user-"+userID)
  ```

  `store.EncryptStoredEvent(ctx, stored)` is the underlying encode primitive (the
  counterpart to `DecryptStoredEvent`) if you need to seal a `StoredEvent` yourself.

## Erasure completeness (hardening)

Crypto-shredding only reaches data encrypted under the revoked key. These controls close
the gaps where an erasure can *look* done while leaving recoverable PII behind.

### Sibling stores — audit, saga, snapshots, outbox, idempotency

PII derived from events lives in stores the event key does not protect: the **audit
trail** (plaintext actor / tenant / metadata / error strings), **saga state** (business
data copied out of events), **snapshots** (plaintext aggregate state), **outbox** rows,
and **idempotency** response payloads. Register them so `Erase` reaches them too:

```go
eraser := mink.NewDataEraser(store,
    mink.WithEraseSubjectResolver(resolver),
    mink.WithSubjectStore(
        mink.NewAuditSubjectEraser(auditStore),        // actor == subject OR aggregate_id in SubjectFootprintIDs
        mink.NewSagaSubjectEraser(sagaStore),          // correlation_id in SubjectFootprintIDs
        mink.NewSnapshotSubjectEraser(adapter),        // the snapshot of every footprint stream (shared ones too)
        mink.NewOutboxSubjectEraser(outboxStore),      // aggregate_id in SubjectFootprintIDs
        mink.NewIdempotencySubjectEraser(idempStore),  // aggregate_id in SubjectFootprintIDs
    ),
    // mink.WithDerivedAggregateIDs(), // ONLY with globally unique aggregate ids (UUIDs) — see below
)
// res.SubjectStores reports what each erased (FootprintAware, SharedStreamsSkipped); a
// per-store failure is non-fatal, but a failed or Skipped store — or one that had to skip
// shared streams — blocks the certificate's Verified flag, and Verify / the certificate
// COUNT what remains in each store (SubjectResidualCounter).
```

**The built-in erasers are footprint-aware.** The rows the library writes are *not* keyed
by the bare subject id: the outbox's `AggregateID` is the **producing stream id** (the
stream the event was appended to — `"User-u1"`, never `"u1"`), the audit and idempotency
`AggregateID` columns hold the **raw aggregate id** the command targeted (`"u1"` for stream
`"User-u1"` — the part after the first `-`), and a saga's `CorrelationID` is whatever its
correlation function derived. A purge that only matched `== subjectID` would therefore
silently miss every library-produced row. Each built-in eraser instead purges by
`mink.SubjectFootprintIDs(subjectID, fp)`: the subject id plus every **exclusive** stream
in the subject's *resolved footprint* (`fp.ExclusiveStreams()` — `SubjectFootprint.Streams`
minus `SubjectFootprint.SharedStreams`, from the resolver passed to
`WithEraseSubjectResolver`), de-duplicated — so resolve before you erase: with no footprint
only the id-keyed rows can be reached.

*Shared streams are skipped, not purged.* A footprint stream on which another subject's tag
was observed cannot be attributed to one subject: its co-tenant's pending outbox messages,
an in-flight saga correlated on it, or another subject's audit/idempotency rows would go
with it. The built-in erasers therefore leave rows keyed by such a stream in place, report
how many streams they skipped in `res.SubjectStores[i].SharedStreamsSkipped`, add a
count-only note to `res.Notes`, and keep the certificate unverified (the store is dropped
from `cert.StoresVerified`); `res.Failed()` is *not* set — the store did what was safe. The
snapshot eraser is the exception: a snapshot is a rebuildable cache, so it is deleted on
every footprint stream.

*Derived aggregate ids are opt-in.* The audit/idempotency `AggregateID` and most saga
correlation ids carry the aggregate id **without its type**, so a derived id collides across
aggregate types: `"Order-123"` and `"User-123"` both derive `"123"`, and purging by it would
delete *another* aggregate's — possibly another subject's — accountability rows and
idempotency records. `SubjectFootprintIDs` therefore excludes them by default. When your
aggregate ids are globally unique (UUIDs, or ids that embed their type) opt in with
`mink.WithDerivedAggregateIDs()`, which switches the built-in erasers to
`mink.SubjectFootprintIDsWithDerived(subjectID, fp)` — the same set plus the id after the
**first** `-` of each *exclusive* stream (`"User-u1"` → `"u1"`, `"Order-ord-42"` →
`"ord-42"`). Custom `SubjectErasable` implementations choose between the two helpers
themselves. `res.SubjectStores[i].FootprintAware` reports whether the footprint path ran; a
store that offers only the legacy id-equality purger falls back to the bare subject id.

The purges use optional adapter sub-interfaces: the id-equality purgers
(`SubjectAuditPurger` / `SubjectSagaPurger` / `SubjectOutboxPurger` /
`SubjectIdempotencyPurger`) and their footprint-aware counterparts
(`SubjectAuditFootprintPurger` / `SubjectSagaFootprintPurger` /
`SubjectOutboxFootprintPurger` / `SubjectIdempotencyFootprintPurger`, with matching
`Subject*Counter` methods so verification can count what remains), implemented on the
memory and PostgreSQL stores. A store that lacks its purger is reported as `Skipped`,
not failed. Every built-in eraser also implements `mink.SubjectResidualCounter`, so
`Verify` and the certificate count the rows still attributable to the subject in each
registered store: a non-zero count lands in `report.ResidualStores` and blocks `Verified`;
a store that cannot be counted (a custom `SubjectErasable` without the interface, or a
store returning `mink.ErrResidualCountUnsupported`) is listed under
`report.UncheckedStores` / `cert.StoresUnchecked` — disclosed, not certified — while a
hard count error fails `Verify` and leaves the certificate unverified. The **default
outbox path stores ciphertext** (shredded with the key) — the
outbox eraser matters for a `route.Transform` that emits a *decrypted* payload and for
dead-lettered rows. If your subject↔row association differs (an app-defined column, an
external sink), register a custom `SubjectErasable`.

### Blast-radius guard (per-tenant keys)

With `WithTenantKeyResolver`, one key protects a whole tenant, so erasing a single subject
would crypto-shred everyone under it. `WithSharedKeyGuard()` detects this **before** the
irreversible revoke and fails with `*SharedKeyError`; pair with `AllowSharedKeyRevocation()`
to proceed deliberately (per-subject keys avoid the problem entirely). The error *message*
carries **counts only** — the number of shared keys and the number of other subjects
(`OtherSubjectCount`) — never the other subjects' identifiers and not the key ids either
(under `WithSubjectKeyResolver` a key id embeds a subject id), because error strings get
logged; `SharedKeyError.SharedKeys` / `OtherSubjects` are available for programmatic use
and must not be logged or written to an audit trail. The guard, like every other key-acting
path, ignores events without a complete envelope: a bare `$encryption_key_id` never makes a
key "shared".

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
// One-time migration for pre-adoption history. The report is PII-free:
// Scanned == Indexed + Untagged; Undecryptable events were indexed from existing tags only.
rep, err := mink.BackfillSubjectIndexWithReport(ctx, store, tagger, idx, 1000)
// (mink.BackfillSubjectIndex is the same call returning just rep.Scanned)

// Assert completeness ONLY when the backfill left nothing unattributed (rep.Untagged == 0,
// or the untagged events are known to carry no PII) and no writer bypasses the index;
// without the assertion an index-backed resolve is honestly Partial.
resolver := mink.NewSubjectResolver(store, mink.WithResolverIndex(idx), mink.WithAuthoritativeIndex())
```

**Index authority.** Append-time index writes are best-effort (a failed write is logged,
not fatal), so an index can silently drift behind the log. `WithResolverIndex` therefore
treats the index as possibly-incomplete and marks the footprint `Partial` unless you also
pass `WithAuthoritativeIndex` to assert completeness — so a drifted index can never
produce a falsely-complete footprint that makes `Erase` miss streams while certifying
success. An index-backed resolve never *sees* untagged events, so that assertion is the only
thing standing between a legacy event the backfill could not attribute and a certificate
that claims completeness: make it only when `BackfillSubjectIndexWithReport` reported
`Untagged == 0` (or the untagged events are known to carry no PII; an `Undecryptable` count
means the tagger never saw those events' encrypted fields) **and** every store instance that
appends carries `WithSubjectIndexWriter` — or the index is transactionally consistent, like
the PostgreSQL adapter's drift-free `StreamsBySubject`. Otherwise prefer the scan-backed
resolver, which proves completeness by observing the untagged events itself. Reconcile a
drifted index with `BackfillSubjectIndex`. Without any index, legacy untagged events keep a
footprint `Partial` (never a *silent* partial).

**Backfill sees plaintext.** `BackfillSubjectIndex` decrypts field-encrypted events through
the store's own decrypt path before running your tagger — a tagger that reads an email out
of an encrypted field would otherwise index nothing. An event that cannot be decrypted (a
revoked key, a handler that swallowed the failure, or no encryption config for an encrypted
event) is never handed to the tagger: it is indexed from its existing `$subjects` tags only,
and a single warning with the count is logged.

**Purging the index after erasure (opt-in).** `mink.WithSubjectIndexPurge(idx)` makes
`Erase` delete the subject's index entries (`SubjectIndexPurger.DeleteSubject`, implemented
by `MemorySubjectIndex` and `postgres.SubjectIndex`) at the very end — and only when
`res.Failed()` is false **and** the erasure verified: with a certificate sink, the emitted
certificate is `Verified`; without one, `Erase` runs the very same verification internally
(nothing is emitted) and purges only if it would have been `Verified`. A `KeyIDs`-only or
explicit-`Streams` erasure that checked no subject-tagged event is vacuous and never purges;
otherwise the index is kept and `res.Notes` says so (that note is result-only — the decision
is taken after the certificate). `res.SubjectIndexPurged` reports the outcome. Trade-off: an
index-backed resolver then resolves an *empty* footprint for that subject (it does not fall
back to a scan) and `Verify` reports `Vacuous`, not `Verified` — so re-verify with a
scan-backed resolver or re-run `BackfillSubjectIndex`.

A **drift-free** alternative on PostgreSQL is the event-store adapter's own
`StreamsBySubject` — inject it with `mink.WithResolverIndex(adapter)`. It reads the
events' `$subjects` tags directly (JSONB), so it cannot fall out of sync with the log
(no separate table to maintain). Indexes are always explicit (`WithResolverIndex`) — never
auto-detected — so a store gaining an index never silently swaps the completeness-proving
scan for one that can't detect untagged events.

> **Subject identifiers are plaintext and are NOT shredded.** The `$subjects` tag and
> `Metadata` (UserID / CorrelationID) are stored in cleartext so they stay scannable, so
> crypto-shredding a subject's event *fields* leaves their *identifier* in the append-only
> log forever. If your subject id is itself PII (an email, a national id), you have not
> fully erased the person. Tag with an **opaque/pseudonymous** id (see `Anonymizer`) and
> treat metadata identifiers as PII under the same discipline.

> **Subject ids must be globally unique across tenants.** The `$subjects` tag, the subject
> index (`mink_subject_index` / `StreamsBySubject`) and the sibling-store purges all key on
> the bare subject id — none of them is tenant-scoped. If tenant A's `user-1` and tenant
> B's `user-1` share an id, one subject's export returns both people's events and one
> erasure shreds both. Tag with an id that is unique on its own (a UUID, or one that embeds
> the tenant such as `"acme:user-1"`), never with a per-tenant sequence number.

## From the command line

The [`mink gdpr`](/docs/guide/cli#mink-gdpr) CLI drives the read-only half of these
workflows against a store: `discover` a subject's footprint, `verify` erasure readiness,
print an `erase` plan (the keys to revoke), and `retain` (dry-run a policy). It does not
hold your encryption keys, so actual revocation runs from your application via the APIs
above — the CLI produces the auditable plan.

---

Next: [CLI →](/docs/guide/cli)
