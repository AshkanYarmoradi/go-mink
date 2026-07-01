## 1. Required — Key revocation (`key-revocation`)

- [x] 1.1 Add the optional `Revocable` interface (`RevokeKey`/`IsRevoked` — matched to the existing local-provider signature, no ctx) + `encryption.Revoke`/`IsRevoked` helpers + `ErrRevocationUnsupported`
- [x] 1.2 Wire runtime detection (type assertion via the `Revoke`/`IsRevoked` helpers); return `ErrRevocationUnsupported` when the provider isn't `Revocable`
- [x] 1.3 Implement `Revocable` in the local provider (zero + delete key material; made `RevokeKey` idempotent; added `IsRevoked`)
- [x] 1.4 Implement `Revocable` in the AWS KMS provider (optional `KMSRevocationClient`: `ScheduleKeyDeletion`/`DescribeKey`) and Vault provider (optional `VaultRevocationClient`: `DeleteKey`/`KeyExists`) — base client adapters unchanged; durability documented
- [x] 1.5 Table-driven tests: idempotent re-revoke, `IsRevoked` status, KMS/Vault revoke via mock revocation clients, non-Revocable provider returns `ErrRevocationUnsupported`

## 2. Required — Subject discovery (`subject-discovery`)

- [x] 2.1 Add `WithSubjectTagger(func(eventType string, data []byte, md Metadata) []string)` — applied at the single shared `prepareEventData` hook (covers Append/SaveAggregate/outbox); records subject id(s) in `Metadata.Custom` (`$subjects`). _(signature uses serialized `data []byte`, not `any`)_
- [x] 2.2 `SubjectResolver` + `SubjectFootprint` (Streams, StreamEventCounts, EventCount, KeyIDs); index-backed via optional `SubjectIndexAdapter`
- [x] 2.3 Scan-based fallback over the subscription adapter (reuses the export scan); `ErrExportScanNotSupported` when neither index nor scan is available
- [x] 2.4 Wired `WithExportSubjectResolver`/`WithEraseSubjectResolver` to auto-resolve a `SubjectID`-only request; incompleteness surfaced via `Partial` on `ExportResult`/`ErasureResult` — never silent
- [x] 2.5 `Resolve` is read-only (erasure preview)
- [x] 2.6 Tests: tag→discover, multi-aggregate footprint, scan fallback, legacy-untagged flagged `Partial`, exporter/eraser auto-resolve

## 3. Required — DataEraser (`data-erasure`)

- [x] 3.1 `DataEraser` + `NewDataEraser` mirroring `DataExporter`; `ErasureRequest` (SubjectID/Streams/Filter/KeyIDs) + `ErasureResult` (KeysRevoked, Streams, EventsScanned, Partial, RedactedReadModels, ResidualReadModels, SideEffects, MarkerWritten, Errors)
- [x] 3.2 Implement `Erase(ctx, req)`: discover keys (explicit KeyIDs + scanned events via `GetEncryptionKeyID` + auto-resolved footprint), revoke via `FieldEncryptionConfig.RevokeKey`, collect partial errors
- [x] 3.3 Optional append-only `ErasureMarker` via `WithErasureMarker(stream)` (no PII)
- [x] 3.4 `Erase` idempotent; typed `ErasureError`/`encryption.ErrRevocationUnsupported`/`ErrErasureNotConfigured`
- [x] 3.5 Tests: erase by streams/filter/KeyIDs/auto-resolved SubjectID, idempotent, partial-failure, marker, validation, not-configured

## 4. Required — Read-model redaction (`read-model-redaction`)

- [x] 4.1 Projection rebuild over revoked keys yields redacted payloads (via `WithDecryptionErrorHandler`) — covered by test
- [x] 4.2 Optional `SubjectRedactable { RedactSubject; ReadModelName }` (in-place, preferred) + `ReadModelRebuilder` (named rebuild callback) — DI via `WithReadModelRedactor`/`WithReadModelRebuilder` (decoupled from the projection engine)
- [x] 4.3 Report `RedactedReadModels`; flag un-redactable read models as `ResidualReadModels`
- [x] 4.4 `Verify` checks read models (§6) — residual surfaced via `ResidualReadModels`
- [x] 4.5 Tests: hook redacts, rebuilder triggered, failure → residual (non-fatal), rebuild-over-revoked yields redacted

## 5. Required — Retention policies (`retention-policies`)

- [x] 5.1 `RetentionPolicy` (matcher: Category/StreamPrefix/EventTypes/TenantID/`MaxAge`; action: `Shred`/`RedactFields`/`Anonymize`), composable
- [x] 5.2 `RetentionManager.Apply(ctx)` over the export scan; `RetentionReport` (Scanned/Matched/Acted/Skipped/KeysRevoked/Errors)
- [x] 5.3 `DryRun(ctx)` (report only, no mutation)
- [x] 5.4 Append-only invariant: actions are Shred (revoke key) or Apply-hook delegation — no row deletion/mutation; test asserts events remain after Apply
- [x] 5.5 Tests: age-based shred, composition, dry-run changes nothing, redact-without-hook skipped, redact-with-hook acted
- [x] 5.6 Ticked `website/docs/roadmap.md` retention item

## 6. Good-to-have — Erasure verification (`erasure-verification`)

- [x] 6.1 `Verify(ctx, subjectID) (*VerificationReport, error)` — deterministic (encrypted event erased iff key revoked); flags `ResidualEncrypted` + `ResidualCleartext`
- [x] 6.2 PII-free `ErasureCertificate` via a decoupled `CertificateSink` (e.g. an `AuditStore`), emitted on `Erase` with `WithCertificateSink`
- [x] 6.3 Tests: residual detected before erase, verified after erase, certificate emitted (no PII)

## 7. Good-to-have — Key lifecycle (`key-lifecycle`)

- [x] 7.1 Rotation is transparent (per-event key id); test covers cross-rotation decrypt + documented in `security.md`
- [x] 7.2 `encryption.RecoverableRevocable` (`SoftRevokeKey`/`UnrevokeKey`) — soft-revoke reversible within the grace window, permanent after; implemented in the local provider
- [x] 7.3 `ReEncryptStream` — append-only re-encryption by copy (no row mutation)
- [x] 7.4 Tests: decrypt across rotation, undo within window, permanent after window, re-encrypt decouples from the old key

## 8. Good-to-have — PII anonymization (`pii-anonymization`)

- [x] 8.1 `Anonymizer` — deterministic (HMAC-SHA256), one-way pseudonymization, per-scope (field/tenant)
- [x] 8.2 `ActionAnonymize` selectable in retention policies (via the policy `Apply` hook using `Anonymizer`)
- [x] 8.3 Tests: stable pseudonym, scope separation, prefix/length, used in a retention policy

## 9. Good-to-have — Erasure side-effects (`erasure-side-effects`)

- [x] 9.1 `WithErasureHook(ErasureHook)` (`Run(ctx, ErasureContext)`); run during `Erase` with subject id/streams/keys; zero overhead when none
- [x] 9.2 `ErasureResult.SideEffects` records successes; hook failure reported (non-fatal); side-effect domains on the certificate (no PII)
- [x] 9.3 Tests: hook runs with context, failing hook non-fatal, certificate lists side-effects

## 10. Docs, release & cross-cutting

- [x] 10.1 Wrote `website/docs/security.md` GDPR guide: encryption → subject discovery → export → erasure (events + read models + side-effects) → retention; per-provider revocation semantics documented
- [x] 10.2 Added `mink gdpr` CLI verbs (`discover`, `verify`, `erase`, `retain`) — read-only footprint / erasure-readiness / erasure-plan / retention-preview over the diagnostic adapter (wrapped in a provider-less `mink.EventStore`); actual key revocation stays in the app (DataEraser/RetentionManager own the keys). Registered on root; table-driven tests
- [x] 10.3 CHANGELOG `[Unreleased]` entry added; `gofmt` + `go vet` clean; zero-overhead-when-unused (nil-guarded tagger/encryption/resolver/hooks); branch targets `develop`
- [x] 10.4 All new public APIs documented (doc comments); behavior covered by table-driven tests across the new files
