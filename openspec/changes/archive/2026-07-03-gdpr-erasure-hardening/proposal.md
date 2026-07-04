# Harden GDPR erasure & retention

## Why

A deep post-implementation audit of `gdpr-erasure-and-retention` (four independent
review passes over adapters, sibling stores, the encryption providers, and the
operational path) confirmed the core is production-sound — `Metadata.Custom`
(subject tags + key ids) round-trips through PostgreSQL, the scan path never
decrypts, a hard revoke shreds within the running process (no DEK cache), and all
providers forward revocation. But it also found a set of real gaps where the
feature **looks** complete yet silently leaves recoverable PII behind or attests to
an erasure that did not fully happen. This change closes them.

The throughline: crypto-shredding only reaches data encrypted under the revoked
key, and the eraser's only extensibility point for **non-event stores** is a
`WithErasureHook` that ships with zero wiring. So everything *derived* from the
events — the audit trail, saga state, snapshots, and decrypting outbox transforms —
retains plaintext PII the eraser never touches. Separately, soft-revoke never
actually shreds key material (only gates decryption) yet `Verify` certifies it as
erased; a per-tenant key erasure silently revokes the whole tenant; the erasure
certificate is best-effort; and there is no way to fully erase subjects whose
events predate tag adoption.

## What Changes

### Required

- **Sibling-store erasure** (`subject-erasure`, NEW). A `SubjectErasable` optional
  interface + `DataEraser.WithSubjectStore(...)`, with built-in erasers for the
  **audit trail**, **saga state**, and **snapshots** (new optional adapter
  sub-interfaces `SubjectAuditPurger`/`SubjectSagaPurger` on memory + postgres, and
  a footprint-driven snapshot eraser). Erasure now reaches derived PII, and the
  result reports what each sibling store erased. (Findings 1, 2, 5)
- **Revocation state: soft vs permanent** (`key-revocation`, MODIFIED). Soft-revoke
  in the local provider promotes to a hard shred (clears key material) once the
  grace window elapses; a new `encryption.RevocationState`/optional
  `StatefulRevocable` distinguishes `NotRevoked`/`SoftRevoked`/`Revoked`;
  `encryption.SoftRevoke`/`Unrevoke` package helpers return
  `ErrRevocationUnsupported` on providers that lack it (KMS/Vault today). (Findings
  3-material, 10)
- **`Verify` must not certify a recoverable key** (`erasure-verification`,
  MODIFIED). Verification counts only permanently-revoked keys as erased; a
  soft-revoked (still-restorable) key is surfaced as `ResidualRecoverable` and
  blocks `Verified=true`, so a certificate can never claim final erasure during a
  grace window. (Finding 3-verify)
- **Blast-radius guard for shared keys** (`data-erasure`, MODIFIED). Opt-in
  `WithSharedKeyGuard()`: before the irreversible revoke, detect any key that also
  protects events tagged for a *different* subject (the per-tenant-key case) and
  fail with `ErrSharedKeyRevocation` unless `AllowSharedKeyRevocation()` is set.
  (Finding 4)
- **Accountability durability** (`data-erasure`, MODIFIED). `WithStrictAccountability()`
  makes a marker/certificate persistence failure fatal (after the idempotent
  revoke); `Certificate.Verified` is gated on `MarkerWritten` when a marker stream
  is configured. (Finding 6)

### Good-to-have

- **Erase TOCTOU mitigation** (`data-erasure`, MODIFIED). After revoking, re-resolve
  the footprint; revoke any newly-appeared keys once and flag `Partial` if the set
  is still growing; document the quiescence contract. (Finding 8)
- **Subject index + backfill** (`subject-discovery`, MODIFIED). An optional
  companion `subject_index` table (`SubjectIndexAdapter` read + `SubjectIndexWriter`
  write on memory + postgres, populated at append-time and via
  `BackfillSubjectIndex`), turning discovery/erasure from O(all events) into
  O(subject's events) and giving late-adopting stores a real path to full erasure.
  Reconciles the design-doc "migration step" claim with shipped code. (Findings 7, 9)
- **`ReEncryptStream` hardening** (`key-lifecycle`, MODIFIED). Destination
  `expectedVersion` guard (idempotent, no duplicate copy), strip stale encryption
  markers from carried metadata, return the source stream + old key ids, and
  document that the old-key PII survives until the source is retired + its key
  revoked. (Finding 13)
- **Revoke→decrypt provider contract test** (`key-revocation`, MODIFIED). A shared
  `providertest.AssertRevokeMakesDecryptFail` wired into local/KMS/Vault suites, so
  the core shred property is enforced for every provider, not just local. (Finding 11)
- **Vault revoke-failure clarity** (`key-revocation`, MODIFIED). Surface a
  `deletion_allowed=false` refusal via an `ErasureResult.Failed()` convenience and
  docs, so a caller checking only `err` cannot mistake a partial failure for
  success. (Finding 15)
- **Docs** (cross-cutting). `RetentionManager` "you must schedule `Apply` yourself"
  (Finding 14); a "subject identifiers are plaintext and never shredded — tag with
  opaque ids" guidance, since a raw email/user-id in `$subjects`/`Metadata` survives
  erasure (audit addition); wire the sibling-store + backfill story into
  `security.md`.

## Non-Goals

- **Encrypting the audit trail / saga state / snapshots.** This change adds
  subject-scoped *erasure* of those stores, not envelope-encrypting them; that is a
  larger, separate change (and would change their query semantics).
- **Mutating or deleting event rows.** The append-only invariant is preserved
  everywhere: sibling-store erasure targets *read-side* stores; event erasure stays
  crypto-shred + marker only.
- **A retention scheduler / cron runtime.** Scheduling stays caller-owned (a
  deliberate choice, consistent with projections); this change only clarifies it.
- **Vault/KMS recoverable (soft) revoke.** KMS *could* support it via
  schedule/cancel-deletion, but implementing recoverable revocation on the cloud
  providers is deferred; this change only makes their *absence* explicit
  (`ErrRevocationUnsupported`) rather than a silent fall-through to hard revoke.
- **The huisscan-side integration audit.** Whether huisscan configures tagging,
  encryption, and sibling-store erasers for its Clerk saga / audit recorder is a
  separate follow-up in that repo.
