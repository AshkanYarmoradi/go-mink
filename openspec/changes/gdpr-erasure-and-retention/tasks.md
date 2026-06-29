## 1. Required — Key revocation (`key-revocation`)

- [ ] 1.1 Add the optional `Revocable` interface (`RevokeDataKey`, `IsRevoked`) to the `encryption` package; document it as opt-in
- [ ] 1.2 Wire runtime detection (type assertion) into the encryption/decryption path; return a typed "revocation unsupported" error when needed
- [ ] 1.3 Implement `Revocable` in the local provider (delete/tombstone key material)
- [ ] 1.4 Implement `Revocable` in the AWS KMS provider (schedule deletion / disable) and the Vault Transit provider (soft-delete / min_decryption_version); document durability semantics of each
- [ ] 1.5 Table-driven tests: revoke → `ErrKeyRevoked` on decrypt, idempotent re-revoke, `IsRevoked` status, non-Revocable provider stays valid

## 2. Required — Subject discovery (`subject-discovery`)

- [ ] 2.1 Add `WithSubjectTagger(func(eventType string, data any, md Metadata) []string)`; record subject id(s) in `Metadata.Custom` at append (zero overhead when unset; no schema change)
- [ ] 2.2 Define `SubjectResolver` + `SubjectFootprint` (streams, per-stream event counts, distinct key ids); index-backed resolution over subject tags
- [ ] 2.3 Add a scan-based fallback resolver over the subscription adapter (reuse the export scan machinery); typed "subject resolution unavailable" error when neither index nor scan is possible
- [ ] 2.4 Wire `DataExporter`/`DataEraser` to auto-resolve a `SubjectID` given without explicit `Streams`/`Filter`; surface incompleteness via a typed error or a `Partial` flag — never a silent partial
- [ ] 2.5 Keep `Resolve` read-only so it doubles as an erasure preview
- [ ] 2.6 Table-driven tests: tag→discover, multi-aggregate footprint, scan fallback, legacy-untagged flagged `Partial`, preview mutates nothing

## 3. Required — DataEraser (`data-erasure`)

- [ ] 3.1 Add `DataEraser` + `NewDataEraser(store, opts...)` mirroring `DataExporter`; define `ErasureRequest` (SubjectID/Streams/Filter) and `ErasureResult` (keys revoked, streams/events, redacted projections, errors)
- [ ] 3.2 Implement `Erase(ctx, req)`: resolve key(s), revoke via the `Revocable` provider, collect partial errors (don't abort on first)
- [ ] 3.3 Add the optional append-only erasure-marker event (configurable type/stream; no PII)
- [ ] 3.4 Make `Erase` idempotent; return a typed "erasure unsupported" error when the provider isn't `Revocable`
- [ ] 3.5 Table-driven + e2e tests: erase by key, erase by filter, erase by resolved SubjectID, idempotent re-erase, partial-failure reporting, marker emission

## 4. Required — Read-model redaction (`read-model-redaction`)

- [ ] 4.1 Ensure projection rebuild over revoked keys yields redacted payloads (via `WithDecryptionErrorHandler`) instead of failing
- [ ] 4.2 Add the optional `SubjectRedactable { RedactSubject(ctx, subjectID) error }` projection interface; `DataEraser` prefers it, else rebuilds the impacted projections (scoped from the footprint)
- [ ] 4.3 Report redacted projections on `ErasureResult`; flag a read model that can be neither hooked nor rebuilt as residual-PII (not "erased")
- [ ] 4.4 Extend `Verify` to check read models for residual PII, not only the event log
- [ ] 4.5 Table-driven tests: rebuild→redacted rows, shredded event doesn't break rebuild, in-place hook avoids full replay, hook failure reported not fatal, `Verify` catches read-model residue

## 5. Required — Retention policies (`retention-policies`)

- [ ] 5.1 Define `RetentionPolicy` (matcher: category/stream-prefix/event-type/tenant/`MaxAge`; action: `Shred`/`RedactFields`/`Anonymize`) and make policies composable
- [ ] 5.2 Implement `RetentionManager.Apply(ctx)` over the export scan/stream machinery; return `RetentionReport` (matched/acted/skipped/errors)
- [ ] 5.3 Add dry-run mode (report only, no mutation)
- [ ] 5.4 Enforce append-only invariant (no row deletion/mutation) and add a guard/test for it
- [ ] 5.5 Table-driven tests: age-based shred, composition, dry-run changes nothing, report accuracy
- [ ] 5.6 Tick `website/docs/roadmap.md` retention item

## 6. Good-to-have — Erasure verification (`erasure-verification`)

- [ ] 6.1 Add `Verify(ctx, subject) (*VerificationReport, error)` confirming all PII reads redacted (events + read models); flag residual cleartext
- [ ] 6.2 Emit an erasure certificate via the existing `AuditStore` (no PII)
- [ ] 6.3 Tests: verify passes post-erasure, flags legacy cleartext, certificate contains no PII

## 7. Good-to-have — Key lifecycle (`key-lifecycle`)

- [ ] 7.1 Master-key rotation: re-wrap DEKs under a new master, transparent decrypt across rotation (provider-side)
- [ ] 7.2 Recoverable (grace-window) revocation: soft-revoke → reversible within window → permanent after
- [ ] 7.3 Optional re-encryption sweep (no row mutation)
- [ ] 7.4 Tests: decrypt across rotation, undo within window, permanent after window, re-encrypt without mutation

## 8. Good-to-have — PII anonymization (`pii-anonymization`)

- [ ] 8.1 Add a deterministic pseudonymization action (stable per field/tenant; original unrecoverable)
- [ ] 8.2 Make `Anonymize` selectable in retention policies and the eraser
- [ ] 8.3 Tests: stable pseudonym mapping, anonymize-vs-shred selection

## 9. Good-to-have — Erasure side-effects (`erasure-side-effects`)

- [ ] 9.1 Add `WithErasureHook(ErasureHook)` (`func(ctx, ErasureContext) error`); run hooks during `Erase` with subject id/streams/keys; zero overhead when none registered
- [ ] 9.2 Capture per-hook outcomes on `ErasureResult`; a hook failure is reported, not fatal (partial-failure contract); include side-effect domains on the certificate without PII
- [ ] 9.3 Tests: hook runs with context, one failing hook doesn't block erasure, certificate lists side-effects without PII

## 10. Docs, release & cross-cutting

- [ ] 10.1 Write the missing `docs/security.md` (website) GDPR guide: encryption → subject discovery → export → erasure (events + read models + side-effects) → retention; document each provider's revocation semantics
- [ ] 10.2 Add `mink gdpr` CLI verbs (`discover`, `erase`, `verify`, `retain`) consistent with `mink stream`/`mink projection` (optional)
- [ ] 10.3 CHANGELOG `[Unreleased]` entries; `gofmt` + `golangci-lint`; ensure zero-overhead-when-unused; PRs target `develop`
- [ ] 10.4 Document all new public APIs; coverage > 90% per project quality bar
