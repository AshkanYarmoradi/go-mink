## Why

go-mink markets **GDPR compliance** and ships strong primitives — field-level envelope encryption, `DataExporter` (Article 15/20 right-to-access/portability), and audit logging. But **right-to-erasure (Article 17) is only half-built**, and the roadmap's next milestone (**v1.1.0 "Data Governance"**) lists `Data retention policies with configurable rules` as still open. Concretely:

- **Crypto-shredding has no first-class API.** Revocation is only *signalled* by `ErrKeyRevoked` on decrypt; the `encryption.Provider` interface has **no `Revoke` method**, so actually erasing a subject is out-of-band and provider-specific. There is no portable way to *perform* an erasure.
- **No erasure orchestrator.** `DataExporter` gives a clean Article 15/20 surface, but there is no symmetric `DataEraser` for Article 17 — no single call to crypto-shred a subject, append an erasure marker, and report what was erased.
- **No retention engine.** The roadmap item — configurable rules to redact/shred/anonymize data past a retention period — does not exist.

This change completes go-mink's data-governance story so downstream users (e.g. a multi-tenant SaaS building an operations/compliance panel) can satisfy erasure + retention by *configuring* go-mink rather than hand-rolling it.

## What Changes

### Required

- **First-class key revocation** — an optional `Revocable` provider interface (`RevokeDataKey`, `IsRevoked`) so crypto-shredding is a portable, callable operation; implemented for the local, AWS KMS, and Vault providers. (Optional interface, mirroring `OutboxAppender`, so existing custom providers don't break.)
- **`DataEraser`** — an Article 17 orchestrator symmetric to `DataExporter`: for a subject it revokes the relevant key(s), optionally appends an erasure-marker event, and returns an `ErasureResult` (keys revoked, streams/events affected, redacted counts, per-item errors). Idempotent.
- **Retention policies** — a configurable `RetentionPolicy` + `RetentionManager` that matches events (by category / stream prefix / event type / tenant / age) and applies an action (crypto-shred, field redaction, or anonymize) on a schedulable sweep, with **dry-run** and a report. This closes the named v1.1.0 roadmap item.
- **Subject discovery** — optional subject tagging (in `Metadata.Custom`) + a `SubjectResolver` that resolves a `SubjectID` to its **complete cross-aggregate footprint** (every stream/event/key, plus a read-only preview). Without this, `DataExporter`/`DataEraser` silently depend on the caller pre-enumerating a subject's streams — so a multi-aggregate subject (a user *and* their workspaces, properties, conversations, billing) leaves **orphaned PII** that export misses and erasure never reaches.
- **Read-model redaction** — erasure must propagate to projections. Crypto-shredding makes the *events* unreadable, but projections have already copied PII into read-model tables the app actually serves; `DataEraser` SHALL drive read-model redaction (rebuild over now-redacted events and/or an optional `SubjectRedactable` projection hook), and `Verify` SHALL check read models too. Otherwise "erasure" leaves plaintext PII sitting in the read side.

### Good-to-have

- **Erasure verification & certificate** — `Verify` confirms a subject now reads as fully redacted and emits an erasure certificate recorded via the existing audit store (Article 17 accountability).
- **Key lifecycle** — master-key rotation (provider-transparent) + an optional re-encryption sweep, and **recoverable revocation** (a soft-revoke grace window before permanent crypto-shred) so an accidental erasure can be undone.
- **PII anonymization / pseudonymization** — a retention/erasure action that replaces PII with stable pseudonyms (deterministic tokenization) instead of full shredding, preserving analytic/referential utility while complying.
- **Erasure side-effect hooks** — `WithErasureHook(...)` so an application erases PII it owns *outside* the event store (blob storage, generated exports/PDFs, caches, search indexes, short-URLs) as part of `Erase`, with per-hook outcomes captured in `ErasureResult` (failures reported, not fatal) and listed on the erasure certificate without re-exposing PII.

### Non-Goals

- **No event-store rewriting.** Erasure is crypto-shredding (key revocation) + optional field redaction + an append-only marker — never deleting or mutating historical event rows.
- **No new mandatory DB schema.** Follow the existing convention of storing crypto metadata in `Metadata.Custom`; any state (e.g. revocation/retention bookkeeping) is provider/adapter-local and optional.
- **Not time-travel queries** (the other v1.1.0 item) — out of scope.
- **No breaking changes to `encryption.Provider`** — revocation is added as an *optional* interface, not a required method.

## Capabilities

### New Capabilities

- `key-revocation` *(required)*: An optional `Revocable` provider interface to perform and query crypto-shredding portably, implemented across local/KMS/Vault.
- `data-erasure` *(required)*: `DataEraser` — the Article 17 orchestrator (revoke keys, append marker, report), symmetric to `DataExporter`, idempotent.
- `retention-policies` *(required)*: Configurable retention rules + a schedulable manager that shreds/redacts/anonymizes matched events; dry-run + report. (The named v1.1.0 roadmap item.)
- `subject-discovery` *(required)*: Optional subject tagging + a `SubjectResolver` that resolves a `SubjectID` to its complete cross-aggregate footprint (streams/events/keys), making export and erasure complete-by-default and providing a read-only preview.
- `read-model-redaction` *(required)*: Erasure propagates to projections/read models (redacted rebuild + optional `SubjectRedactable` hook) so PII does not survive on the read side; `Verify` covers read models.
- `erasure-verification` *(good-to-have)*: Verify a subject is fully redacted and emit an audit-recorded erasure certificate.
- `key-lifecycle` *(good-to-have)*: Master-key rotation + optional re-encryption, and recoverable (grace-window) revocation.
- `pii-anonymization` *(good-to-have)*: Pseudonymization as an alternative erasure/retention action.
- `erasure-side-effects` *(good-to-have)*: `WithErasureHook(...)` so an app erases PII it owns outside the event store during `Erase`, with outcomes captured on the result/certificate.

### Modified Capabilities

<!-- First OpenSpec change in this repo; existing encryption/export behavior is
     extended (new optional interfaces + orchestrators), not redefined. -->

## Impact

- **`encryption` package**: new optional `Revocable` interface + `RevokeDataKey`/`IsRevoked` on the local/KMS/Vault providers; new sentinel/typed errors as needed (reuse `ErrKeyRevoked`).
- **Root `mink` package**: new `DataEraser` (mirrors `DataExporter`), `RetentionPolicy`/`RetentionManager`, a `SubjectResolver` + `WithSubjectTagger` (subject tags in `Metadata.Custom`), read-model redaction (redacted rebuild + optional `SubjectRedactable` projection hook), and (good-to-have) `Verify`, key-rotation helpers, an anonymization action, and `WithErasureHook`. Options-pattern constructors; typed errors.
- **Audit middleware**: erasure/retention operations are recordable via the existing `AuditStore`; the audit trail must not re-expose erased PII.
- **CLI** (optional follow-up): `mink gdpr` verbs (`erase`, `verify`, `retain`) consistent with existing `mink stream`/`mink projection`.
- **Docs**: fill in `website/docs/roadmap.md` (tick retention), and the (currently missing) `docs/security.md` GDPR guide; document all new public APIs.
- **Compatibility**: additive and optional — zero overhead when unused; no breaking change to `encryption.Provider`.
