## 1. Required — Key revocation (`key-revocation`)

- [ ] 1.1 Add the optional `Revocable` interface (`RevokeDataKey`, `IsRevoked`) to the `encryption` package; document it as opt-in
- [ ] 1.2 Wire runtime detection (type assertion) into the encryption/decryption path; return a typed "revocation unsupported" error when needed
- [ ] 1.3 Implement `Revocable` in the local provider (delete/tombstone key material)
- [ ] 1.4 Implement `Revocable` in the AWS KMS provider (schedule deletion / disable) and the Vault Transit provider (soft-delete / min_decryption_version); document durability semantics of each
- [ ] 1.5 Table-driven tests: revoke → `ErrKeyRevoked` on decrypt, idempotent re-revoke, `IsRevoked` status, non-Revocable provider stays valid

## 2. Required — DataEraser (`data-erasure`)

- [ ] 2.1 Add `DataEraser` + `NewDataEraser(store, opts...)` mirroring `DataExporter`; define `ErasureRequest` (SubjectID/Streams/Filter) and `ErasureResult` (keys revoked, streams/events, errors)
- [ ] 2.2 Implement `Erase(ctx, req)`: resolve key(s), revoke via the `Revocable` provider, collect partial errors (don't abort on first)
- [ ] 2.3 Add the optional append-only erasure-marker event (configurable type/stream; no PII)
- [ ] 2.4 Make `Erase` idempotent; return a typed "erasure unsupported" error when the provider isn't `Revocable`
- [ ] 2.5 Table-driven + e2e tests: erase by key, erase by filter, idempotent re-erase, partial-failure reporting, marker emission

## 3. Required — Retention policies (`retention-policies`)

- [ ] 3.1 Define `RetentionPolicy` (matcher: category/stream-prefix/event-type/tenant/`MaxAge`; action: `Shred`/`RedactFields`/`Anonymize`) and make policies composable
- [ ] 3.2 Implement `RetentionManager.Apply(ctx)` over the export scan/stream machinery; return `RetentionReport` (matched/acted/skipped/errors)
- [ ] 3.3 Add dry-run mode (report only, no mutation)
- [ ] 3.4 Enforce append-only invariant (no row deletion/mutation) and add a guard/test for it
- [ ] 3.5 Table-driven tests: age-based shred, composition, dry-run changes nothing, report accuracy
- [ ] 3.6 Tick `website/docs/roadmap.md` retention item

## 4. Good-to-have — Erasure verification (`erasure-verification`)

- [ ] 4.1 Add `Verify(ctx, subject) (*VerificationReport, error)` confirming all PII reads redacted; flag residual cleartext
- [ ] 4.2 Emit an erasure certificate via the existing `AuditStore` (no PII)
- [ ] 4.3 Tests: verify passes post-erasure, flags legacy cleartext, certificate contains no PII

## 5. Good-to-have — Key lifecycle (`key-lifecycle`)

- [ ] 5.1 Master-key rotation: re-wrap DEKs under a new master, transparent decrypt across rotation (provider-side)
- [ ] 5.2 Recoverable (grace-window) revocation: soft-revoke → reversible within window → permanent after
- [ ] 5.3 Optional re-encryption sweep (no row mutation)
- [ ] 5.4 Tests: decrypt across rotation, undo within window, permanent after window, re-encrypt without mutation

## 6. Good-to-have — PII anonymization (`pii-anonymization`)

- [ ] 6.1 Add a deterministic pseudonymization action (stable per field/tenant; original unrecoverable)
- [ ] 6.2 Make `Anonymize` selectable in retention policies and the eraser
- [ ] 6.3 Tests: stable pseudonym mapping, anonymize-vs-shred selection

## 7. Docs, release & cross-cutting

- [ ] 7.1 Write the missing `docs/security.md` (website) GDPR guide: encryption → export → erasure → retention; document each provider's revocation semantics
- [ ] 7.2 Add `mink gdpr` CLI verbs (`erase`, `verify`, `retain`) consistent with `mink stream`/`mink projection` (optional)
- [ ] 7.3 CHANGELOG `[Unreleased]` entries; `gofmt` + `golangci-lint`; ensure zero-overhead-when-unused; PRs target `develop`
- [ ] 7.4 Document all new public APIs; coverage > 90% per project quality bar
