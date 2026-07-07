## Context

go-mink already has the hard parts of GDPR: envelope field-encryption (`FieldEncryptionConfig`, providers local/KMS/Vault), crypto-shredding *signalling* (`ErrKeyRevoked`), `DataExporter` (Article 15/20), and audit logging. What's missing is the **erasure half of the story** and the roadmap's **retention** item:

- The `encryption.Provider` interface (`Encrypt`/`Decrypt`/`GenerateDataKey`/`DecryptDataKey`/`Close`) has **no way to *perform* a revocation** — only to observe one via `ErrKeyRevoked`. Erasure is therefore provider-specific and non-portable.
- There is no orchestrator for Article 17 (no `DataEraser` to mirror `DataExporter`).
- `website/docs/roadmap.md` → **v1.1.0 "Data Governance"** still lists `[ ] Data retention policies with configurable rules`.

This change closes those gaps additively, preserving go-mink's invariants (append-only store, zero-overhead-when-unused, no forced schema changes).

## Goals / Non-Goals

**Goals**
- Make crypto-shredding a portable, callable operation and ship an erasure orchestrator symmetric to `DataExporter`.
- Deliver the configurable retention engine named on the roadmap.
- Keep everything additive and optional — no breaking change to `encryption.Provider`, no mandatory schema.

**Non-Goals**
- Rewriting or deleting historical event rows (erasure = key revocation + redaction + append-only marker).
- Time-travel queries (the other v1.1.0 item).
- A hosted key-management service — go-mink orchestrates revocation through the existing provider backends.

## Decisions

### D1: Revocation is an OPTIONAL `Revocable` interface, not a change to `Provider`
Add `Revocable { RevokeDataKey(ctx, keyID) error; IsRevoked(ctx, keyID) (bool, error) }`. The core path type-asserts for it (the same optional-interface pattern as `adapters.OutboxAppender`). Existing custom providers keep compiling; only providers that opt in gain erasure.
- *Alternative:* add the methods to `Provider` directly — rejected (breaking change for every custom provider).

### D2: `DataEraser` mirrors `DataExporter`
`NewDataEraser(store, opts...)` + `Erase(ctx, ErasureRequest) (*ErasureResult, error)`, sharing the export subject model (`SubjectID`, `Streams`, `Filter`). Symmetry makes the GDPR surface learnable: export and erase are two sides of one coin.
- *Alternative:* fold erasure into `DataExporter` — rejected (different intent, different permissions, clarity).

### D3: Retention actions preserve the append-only log
Actions are `Shred` (revoke key), `RedactFields` (mask via redaction metadata), or `Anonymize` (pseudonymize) — never row deletion/mutation. `RetentionManager.Apply(ctx)` is a schedulable sweep with dry-run + report.
- *Alternative:* physical pruning of old events — rejected (breaks event sourcing and audit).

### D4: No forced DB schema
Revocation/retention bookkeeping lives where the provider already keeps key state (local file/KMS/Vault) and in `Metadata.Custom`, consistent with how encryption metadata is stored today. Adapters MAY add optional helper tables, never required ones.

### D5: Zero overhead when unused
None of this runs unless field-encryption + the new options are configured — matching the existing encryption/upcasting philosophy.

### D6: Recoverable revocation is soft→hard (good-to-have)
A grace window models accidental-erasure recovery: soft-revoke blocks decryption but is reversible; after the window it becomes a permanent crypto-shred. Implemented at the provider/bookkeeping layer.

### D7: Subject discovery via tags + a `SubjectResolver`, completeness is never silent
`DataExporter`/`DataEraser` today take `Streams`/`Filter`/`SubjectID` but assume the caller can enumerate a subject's streams. Real subjects span many stream namespaces (a user *and* their workspaces, properties, conversations, billing), so this assumption silently drops PII. Add an optional `WithSubjectTagger(...)` that records subject id(s) in `Metadata.Custom` at append time (zero overhead when unused, no schema change), and a `SubjectResolver.Resolve(ctx, subjectID) (*SubjectFootprint, error)` — index-backed where tags exist, scan-based fallback otherwise (reusing the export scan machinery). Export/erase resolve `SubjectID` automatically; when the footprint **can't be proven complete** (e.g. legacy untagged events) the operation returns a typed error or sets `Partial`, never a silent partial. `Resolve` is read-only, so it doubles as an erasure preview.
- *Alternative:* require callers to keep their own subject→streams map — rejected (every consumer re-implements the GDPR-critical, easy-to-get-wrong part; a consumer's ops panel hit exactly this and could not ship a complete export).

### D8: Erasure must propagate to read models, not just events
Crypto-shredding makes the *events* unreadable, but projections have already copied PII into read-model tables — which is what the app actually serves. Erasure that stops at the event log leaves plaintext PII on the read side. So `DataEraser` SHALL redact read models for the erased subject: prefer an optional `SubjectRedactable { RedactSubject(ctx, subjectID) error }` projection hook (in-place, cheap), else rebuild the impacted projection over the now-redacted events (the existing `WithDecryptionErrorHandler` already yields redacted payloads on `ErrKeyRevoked`). `ErasureResult` reports redacted projections; a read model that can be neither hooked nor rebuilt is flagged residual-PII rather than reported erased. `Verify` extends to read models.
- *Alternative:* treat read-model cleanup as the app's problem — rejected (it's the part most likely to be forgotten, and it's where the data is served from; go-mink owns the projection machinery, so it can make rebuild-to-redacted the default).

### D9: External artifacts erased via opt-in hooks (good-to-have)
Apps store PII outside the event store (object storage, generated PDFs/exports, caches, search indexes). `WithErasureHook(func(ctx, ErasureContext) error)` lets the app clean those *as part of* `Erase`, with per-hook outcomes on `ErasureResult` (failure reported, not fatal — same partial-failure contract) and listed on the certificate without re-exposing PII. Opt-in, zero overhead when unused.
- *Alternative:* a separate post-erasure callback the caller wires manually — rejected (erasure + its side-effects should be one auditable operation with one result).

## Risks / Trade-offs

- **Provider revocation durability differs** (local delete vs KMS scheduled-deletion vs Vault soft-delete) → Mitigation: document each backend's exact semantics + timing; `IsRevoked` reflects true state.
- **Legacy cleartext PII** (written before encryption) can't be crypto-shredded → Mitigation: `Verify` flags it; a `RedactFields`/`Anonymize` retention policy remediates read-side; documented as a known limitation.
- **KMS key sprawl** with per-subject keys → Mitigation: support per-tenant keys (existing `WithTenantKeyResolver`) and document the per-subject vs per-tenant trade-off (granularity vs key count).
- **Retention sweep cost** on large stores → Mitigation: batch + reuse the export scan/stream machinery; dry-run first; schedulable off-peak.
- **Re-encryption complexity** → kept good-to-have and additive (no row mutation).
- **Subject-discovery completeness on legacy/untagged events** — events appended before a tagger was configured aren't index-discoverable → Mitigation: scan-based fallback matches what it can; the resolver marks the footprint `Partial` and lists unresolved areas (never a silent partial); documented as a migration step (backfill tags / one-time scan).
- **Read-model rebuild cost for in-place erasure** — rebuilding a large projection to redact one subject is expensive → Mitigation: prefer the `SubjectRedactable` in-place hook; fall back to rebuild only when no hook exists; scope rebuilds to the impacted projections from the footprint.

## Migration Plan

Additive and semver-minor (target **v1.1.0**, branch from `develop`):
1. **Required:** `Revocable` interface + provider impls → `DataEraser` → retention engine. Each lands behind tests; no API breakage.
2. **Good-to-have:** verification/certificate, key lifecycle (rotation, recoverable revocation, re-encryption), anonymization.
3. Tick the roadmap retention item; write the `docs/security.md` GDPR guide (currently a broken README link).

Rollback: every addition is opt-in; not configuring it leaves behavior unchanged.

## Open Questions

- Default erasure-marker event type/stream name (and whether it is on by default).
- Per-subject vs per-tenant key granularity guidance as the default for erasure (affects key count + blast radius).
- Should `RetentionManager` own scheduling, or only expose `Apply(ctx)` for the caller's scheduler? (Leaning: expose `Apply`, leave scheduling to the caller, like projections.)
- Does recoverable-revocation state belong in the provider, an adapter table, or `Metadata.Custom`?
- Subject tagging shape: a single `SubjectID` per event vs multiple subject tags (e.g. an event touching two users) — and whether tags are app-supplied only or partly derived from encryption key ids.
- Should a `SubjectResolver` index be maintained eagerly (a projection/outbox over subject tags) or computed lazily by scan? (Leaning: optional index projection, scan fallback — mirrors how projections are opt-in.)
- Default read-model redaction strategy when a projection has no `SubjectRedactable` hook: auto-rebuild vs require explicit opt-in (rebuild can be costly and may touch other subjects' rows).
