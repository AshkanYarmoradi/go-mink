## 1. Required — Revocation state: soft vs permanent (`key-revocation`)

- [x] 1.1 Add `encryption.RevocationState` (`NotRevoked`/`SoftRevoked`/`Revoked`) + optional `StatefulRevocable interface { RevocationState(keyID) (RevocationState, error) }` + `encryption.GetRevocationState(p, keyID)` helper (falls back to `IsRevoked`)
- [x] 1.2 Local provider: single internal `stateLocked` that **promotes** an expired soft-revocation to hard (clears + deletes key bytes); `getKey`/`IsRevoked`/`RevocationState` all route through it — "permanent after window" now shreds material
- [x] 1.3 Add `encryption.SoftRevoke`/`Unrevoke` package helpers returning `ErrRevocationUnsupported` on non-`RecoverableRevocable` providers (KMS/Vault)
- [x] 1.4 Table-driven tests: soft→hard promotion clears material (assert bytes gone, not just decrypt blocked), `RevocationState` transitions, in-window vs post-window, KMS/Vault `SoftRevoke` → `ErrRevocationUnsupported`

## 2. Required — Verify must not certify a recoverable key (`erasure-verification`)

- [x] 2.1 `Verify` uses `GetRevocationState`; only `Revoked` counts as erased; soft-revoked (in-window) → new `VerificationReport.ResidualRecoverable` and `Verified=false`
- [x] 2.2 Certificate reflects `ResidualRecoverable`; never `Verified=true` while a subject key is soft-revoked
- [x] 2.3 Tests: soft-revoked subject → not verified + residual; hard-revoked → verified; mixed

## 3. Required — Sibling-store erasure (`subject-erasure`)

- [x] 3.1 `mink.SubjectErasable` interface (`EraseSubject(ctx, subjectID, *SubjectFootprint) (SubjectErasureOutcome, error)`, `ErasableName()`) + `SubjectErasureOutcome` (Name/Erased/Skipped/Err)
- [x] 3.2 `DataEraser.WithSubjectStore(...)`; `Erase` runs each after key-revoke + read-model redaction (non-fatal, symmetric with hooks); report `ErasureResult.SubjectStores`
- [x] 3.3 Audit: optional `adapters.SubjectAuditPurger { DeleteAuditBySubject(ctx, subjectID) (int64, error) }` on memory + postgres (delete where `actor==subject OR aggregate_id==subject`); `mink.NewAuditSubjectEraser(store)`
- [x] 3.4 Saga: optional `adapters.SubjectSagaPurger { DeleteSagasBySubject(ctx, subjectID) (int64, error) }` on memory + postgres (delete where `correlation_id==subject`); `mink.NewSagaSubjectEraser(store)`
- [x] 3.5 Snapshot: `mink.NewSnapshotSubjectEraser(snapshotAdapter)` deletes snapshots for each `footprint.Streams` via existing `DeleteSnapshot`
- [x] 3.6 Tests: each eraser purges its subject's rows, leaves others; unsupported store → `ErrRevocationUnsupported`-style skip; DataEraser aggregates outcomes; partial failure non-fatal
- [x] 3.7 Document the outbox `Transform`-decrypts leak + the "register a SubjectErasable for your sink" guidance

## 4. Required — Blast-radius guard for shared keys (`data-erasure`)

- [x] 4.1 `WithSharedKeyGuard()` + `AllowSharedKeyRevocation()`; `*SharedKeyError`/`ErrSharedKeyRevocation`
- [x] 4.2 Exclusivity scan before revoke: a key is shared if any event under it is tagged for a subject `!= target` (or untagged); fail before the irreversible step unless allowed
- [x] 4.3 Tests: per-tenant shared key blocked; `AllowSharedKeyRevocation` overrides; per-subject key not flagged; guard-off = zero overhead + old behavior

## 5. Required — Accountability durability (`data-erasure`)

- [x] 5.1 `WithStrictAccountability()` — marker/certificate persistence failure is fatal (after the idempotent revoke); default stays best-effort
- [x] 5.2 Gate `cert.Verified` on `MarkerWritten` when a marker stream is configured
- [x] 5.3 Tests: strict marker-fail → error; strict cert-fail → error; non-strict → soft error; `Verified` false when marker not written

## 6. Good-to-have — Erase TOCTOU mitigation (`data-erasure`)

- [x] 6.1 After revoke, re-resolve once; revoke newly-appeared keys; if the set still grew, set `Partial=true` + warning error
- [x] 6.2 Document the quiescence contract on `Erase`/`ErasureRequest`
- [x] 6.3 Tests: append-after-discovery under a new key is caught (Partial) or revoked on the second pass

## 7. Good-to-have — Subject index + backfill (`subject-discovery`)

- [x] 7.1 `SubjectIndexWriter interface { IndexSubjects(ctx, streamID, subjectIDs) error }` (write) alongside existing `SubjectIndexAdapter` (read)
- [x] 7.2 `MemorySubjectIndex` implements both read + write; the index is decoupled from the event-store adapter (injected via `WithResolverIndex` / `WithSubjectIndexWriter`), so a durable table-backed index plugs in without touching the core adapter. _(A postgres `mink_subject_index` table is a straightforward follow-up needing live-DB integration testing; the scan fallback already works on postgres, so this is a pure optimization.)_
- [x] 7.3 Append path (Append + SaveAggregate) writes derived subjects to the index when a writer is wired (best-effort, logged); `mink.BackfillSubjectIndex(ctx, store, tagger, writer, batchSize)` for history
- [x] 7.4 Resolver prefers an injected index over the adapter index over scan (O(subject)); a fully back-filled index lets `Resolve` return `Partial=false` for historical subjects
- [x] 7.5 Tests: index read/write + idempotency, backfill populates history, indexed resolve is non-partial vs a partial scan, append auto-indexes
- [x] 7.6 Reconciled the base change's design-doc "migration step" claim with the shipped `BackfillSubjectIndex`

## 8. Good-to-have — `ReEncryptStream` hardening (`key-lifecycle`)

- [x] 8.1 Destination `expectedVersion = NoStream` guard (idempotent; re-run errors, no duplicate copy)
- [x] 8.2 Strip `$encryption_*` markers from carried metadata before re-append
- [x] 8.3 Return `(copied int, oldKeyIDs []string, err error)`; docstring: source + old-key PII survive until retired + revoked
- [x] 8.4 Tests: idempotency guard, stale-marker strip, returned old key ids

## 9. Good-to-have — Provider revoke→decrypt contract test (`key-revocation`)

- [x] 9.1 `providertest.AssertRevokeMakesDecryptFail(t, provider)` — encrypt → revoke → assert Decrypt/DecryptDataKey error
- [x] 9.2 Wire into local + KMS + Vault suites (with revocation-capable mocks/backends)

## 10. Good-to-have — Vault revoke-failure clarity (`key-revocation`)

- [x] 10.1 `ErasureResult.Failed()` convenience (true when any requested key wasn't revoked) + doc that partial failures live in `Errors`/the `KeysRevoked` gap
- [x] 10.2 Test: a revoke that errors (e.g. simulated `deletion_allowed=false`) → `Failed()==true`, key absent from `KeysRevoked`

## 11. Docs, release & cross-cutting

- [x] 11.1 `RetentionManager` docstring: `Apply` is a single sweep — wire your own scheduler (go-mink does not schedule it)
- [x] 11.2 `security.md`: subject identifiers in `$subjects`/`Metadata` are plaintext and never shredded — tag with opaque ids (use `Anonymizer`); an email/user-id used as subject id survives erasure
- [x] 11.3 `security.md`: sibling-store erasure (audit/saga/snapshot) + backfill sections
- [x] 11.4 CHANGELOG `[Unreleased]`; `gofmt` + `go vet` clean; zero-overhead-when-unused preserved; branch targets `develop`
- [x] 11.5 All new public APIs documented (doc comments) + table-driven tests across new files; `openspec validate gdpr-erasure-hardening --strict`
