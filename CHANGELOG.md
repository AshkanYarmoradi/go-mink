# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Security
- **Comprehensive security hardening (2026-10 whole-repository audit)** — a verified, source-compatible pass over every package: every existing call site still compiles. One exported signature did change — `RecoveryMiddleware() Middleware` became `RecoveryMiddleware(opts ...RecoveryOption) Middleware` — which is compatible for callers (`RecoveryMiddleware()` still works) but not for function-value use: a variadic function is not assignable to a `func() Middleware` variable, field or parameter, so code that stored it as such a value must adapt. Items marked **Behavior change:** alter a default and are worth scanning before upgrading; everything else is additive or opt-in and zero-overhead when unused. Grouped by area:
  - **Reserved metadata & subject tagging (`EventStore.Append` / `SaveAggregate` / `EventStoreWithOutbox`)**
    - **Behavior change:** the four field-encryption envelope keys (`$encrypted_fields`, `$encryption_key_id`, `$encrypted_dek`, `$encryption_algorithm`) are always stripped from caller-supplied `Metadata.Custom` before anything is stamped — via the new exported `SanitizeReservedMetadata(m Metadata) Metadata` (copy-on-write; never mutates the input, returns the same map when no reserved key is present) — so a writer that controls metadata (e.g. an HTTP API copying request headers) can no longer make plaintext look encrypted, point decryption/erasure at another key, or skip encryption. The encryption path re-stamps the envelope when configured.
    - **Behavior change:** a caller-supplied `$schema_version` is now **dropped** by default: with an `UpcasterChain` configured the chain's latest version for the event type is always stamped, and without a chain nothing is stamped (previously any value was persisted verbatim and could mis-route the upcaster chain on `Load` — a writer could downgrade one event's version and have the chain re-run over already-current data). The new `WithCallerSchemaVersion()` `EventStore` option restores honoring a caller value that is an integer within `[1, LatestVersion(type)]` for a type that has upcasters (trusted migration tooling); out-of-range, unparsable and no-upcasters values are still replaced, and with no chain the value is still dropped. `ReEncryptStream` needs no option: its copies are appended through an internal trusted path that preserves `$schema_version` verbatim when the copying store has no chain and stamps the chain's latest version when its `Load` upcasted the data (previously the stale source version travelled alongside already-upcasted data, or was dropped).
    - **Behavior change:** with a `SubjectTagger` configured, the tagger's output now **replaces** caller-supplied `$subjects` tags instead of merging with them (previously a writer could attribute an event to another subject, select that subject's encryption key, and inject the event into their erasure footprint). The new `WithCallerSubjectTags()` `EventStore` option restores merging for trusted writers; with no tagger configured, caller tags are left untouched as before. Every other `$`-key (e.g. `$erasure_marker_subject`) is stored as supplied. **Behavior change:** the same replace-unless-`WithCallerSubjectTags` policy now governs `EncryptStoredEvent` / `ReEncryptStreamInPlace` (previously the *stored* tags were merged first, so a `$subjects` tag forged at rest selected the wrapping key and kept the event in the victim's erasure footprint; `EncryptStoredEvent` also strips a stale or partial encryption envelope before sealing) and the copies made by `ReEncryptStream` (a configured tagger replaces the carried-over tags; with no tagger they are carried over as-is).
    - `EventStoreWithOutbox.Append` / `SaveAggregate` now record subject tags into the index configured with `WithSubjectIndexWriter` on both the atomic `AppendWithOutbox` path and the non-atomic fallback. Previously events written through the outbox wrapper never reached the subject index, so index-backed export/erasure silently missed them.
  - **GDPR erasure & verification**
    - **Footprint-aware sibling-store erasers** — `NewOutboxSubjectEraser` / `NewAuditSubjectEraser` / `NewIdempotencySubjectEraser` / `NewSagaSubjectEraser` now purge by the subject's *resolved footprint* whenever the store implements the new footprint purger (both built-in adapters do). The id set is the new exported `SubjectFootprintIDs(subjectID, fp) []string` = the subject id ∪ every **exclusive** footprint stream id (`fp.ExclusiveStreams()`: `SubjectFootprint.Streams` minus `SubjectFootprint.SharedStreams`), de-duplicated. Derived aggregate ids are **opt-in**: the new `SubjectFootprintIDsWithDerived(subjectID, fp)` adds the aggregate id after the **first** `-` of each exclusive stream (`"User-u1"` → `"u1"`, `"Order-ord-42"` → `"ord-42"`), and the new `WithDerivedAggregateIDs()` `DataEraserOption` switches the built-in erasers to that set (applied to the erasers registered with `WithSubjectStore` in any option order; custom `SubjectErasable` implementations pick the set themselves). A derived id is matched without its aggregate type, so the opt-in is safe only when aggregate ids are globally unique (UUIDs, or ids that embed their type) — with per-type sequential ids the footprint stream `"Order-123"` would also purge the audit/idempotency rows and sagas of `"Invoice-123"`, possibly another subject's. This reaches the rows the library itself writes (the outbox's `AggregateID` is the producing *stream* id; the audit/idempotency `AggregateID` columns hold the raw aggregate id the command targeted — `"u1"` for stream `"User-u1"` — which only the derived opt-in reaches; a saga's `CorrelationID` is whatever its correlation function derived) that a bare `== subjectID` purge never matched. Footprint streams **shared with other subjects** (another subject's `$subjects` tag observed on the stream) are skipped on the footprint-aware path: a co-tenant's outbox rows (any status, pending included), sagas correlated on the shared stream and audit/idempotency rows keyed by it are left in place, the count is reported in the new `SubjectErasureOutcome.SharedStreamsSkipped`, `ErasureResult.Notes` carries a count-only note, and the certificate is not `Verified` (a per-store note; the store is dropped from `StoresVerified`); `ErasureResult.Failed()` is unaffected. The snapshot eraser still deletes the (rebuildable) snapshot of every footprint stream, shared ones included. Stores implementing only the legacy `Subject*Purger` keep the old bare-id behavior; `SubjectErasureOutcome.FootprintAware` reports which path ran. Package `mink` re-exports the adapter seams as `SubjectOutboxFootprintPurger` / `SubjectAuditFootprintPurger` / `SubjectIdempotencyFootprintPurger` / `SubjectSagaFootprintPurger` / `SubjectOutboxCounter` / `SubjectAuditCounter` / `SubjectIdempotencyCounter` / `SubjectSagaCounter` type aliases.
    - **Residual counting** — new `SubjectResidualCounter` interface (`CountSubjectResidual(ctx, subjectID, *SubjectFootprint) (int64, error)`) and `SubjectStoreResidual{Name, Count}`, implemented by all five built-in erasers (the new sentinel `ErrResidualCountUnsupported` is returned when the underlying store lacks the counter extension). `DataEraser.Verify` and the certificate now count residual rows in every registered `WithSubjectStore`: a non-zero count makes `Verified=false` (`VerificationReport.ResidualStores`); stores that cannot be counted are disclosed in `VerificationReport.UncheckedStores` / `ErasureCertificate.StoresUnchecked` and do not block `Verified`; a hard count error makes `Verify` return an `ErasureError` and leaves the certificate unverified (recorded in `ErasureResult.Errors`, so `Failed()` is `true`). `ErasureCertificate.StoresVerified` names the stores proven clean; `VerificationReport.Notes` / `ErasureCertificate.Notes` carry PII-free explanations (counts and store names only, never ids). Residual counters receive the same exclusive id set the purge used, so rows keyed by a footprint stream shared with other subjects are neither purged nor counted — `Verify` adds a count-only note whenever the footprint has `SharedStreams` and a sibling store was counted (that note does not affect `Verified`).
    - **Behavior change: non-vacuous certificates** — `ErasureCertificate.Verified` is no longer `true` for an erasure that revoked keys (or failed to) or targeted streams but checked zero subject-tagged events (a `KeyIDs`-only request, explicit `Streams` over untagged data, or legacy untagged stores): it is now `false` with an explanatory `Note`. A genuinely empty footprint (no streams, no keys) stays `Verified=true` with a "nothing to erase" note. `Verified` is also `false` whenever any key failed to revoke, independent of what the event check saw. **Behavior change:** `DataEraser.Verify` follows the same rule — a report that checked zero subject-tagged events (`EventsChecked == 0`, including a subject whose index entries were purged by `WithSubjectIndexPurge` and is then resolved through an index-backed resolver) is flagged by the new `VerificationReport.Vacuous` with `Verified=false` and an explanatory note (previously such a report was `Verified=true`); it is not a finding of residual PII — re-verify with a scan-backed resolver or after `BackfillSubjectIndex`.
    - `ErasureResult` gains `KeysFailed []string` (keys whose revocation failed, sorted — populated on initial and late-key revokes), `CleartextEvents int` (matched subject events with no field encryption, which stay readable after erasure; mirrored on the new `SubjectFootprint.CleartextEvents`), `Notes []string`, `SubjectIndexPurged bool` and `SharedStreams []string` (the sorted subset of `Streams` in which another subject's `$subjects` tag was observed — computed for every request shape: from the resolver, from the listed `Streams`, or by re-reading the matched streams after a `Filter` scan; untagged events never make a stream shared). `SubjectFootprint` gains the matching `SharedStreams []string` (detected on both the index and the scan path of `SubjectResolver`) and `(*SubjectFootprint).ExclusiveStreams() []string` (`Streams` minus `SharedStreams`, nil-safe). A stream-load failure during certification is now recorded in `ErasureResult.Errors` (previously swallowed with `Verified=false`).
    - **Behavior change: stream-scoped erasure** — with `ErasureRequest.Streams`, inside a listed stream that contains *any* subject-tagged event only the events tagged with the target subject contribute keys, so co-tenants' keys on a shared stream are no longer revoked. A listed stream with no tagged events keeps the previous all-keys behavior and adds a note to the result and certificate; `EventsScanned` on this path now counts only the subject's matched events. A listed stream that holds **both** tagged and untagged events now sets `ErasureResult.Partial = true` (so `Failed()` is `true` and the certificate is not `Verified`) and adds a count-only note — the untagged events in it could not be attributed to any subject and were not shredded; previously the subject's own pre-tagging events were silently excluded with no trace.
    - **Behavior change:** `SharedKeyError.Error()` no longer prints other subjects' identifiers or the shared key ids (an error message is routinely logged, and under `WithSubjectKeyResolver` a key id embeds a subject id) — it prints the target subject, the *number* of shared keys and the *number* of other subjects (the new `SharedKeyError.OtherSubjectCount`); `SharedKeyError.SharedKeys` / `OtherSubjects` are unchanged for programmatic use and must not be logged. (`RetentionSharedKeyError.Error()`, `RetentionReport.KeysToRevoke` / `SharedKeysSkipped` and the `mink gdpr retain` report still print key ids by design — an operator needs them to split keys — so treat retention output as sensitive when key ids embed subject ids.)
    - **Subject-index purge (opt-in)** — new `SubjectIndexPurger` interface (`DeleteSubject(ctx, subjectID) error`), implemented by `MemorySubjectIndex.DeleteSubject` and `postgres.SubjectIndex.DeleteSubject`, plus the `WithSubjectIndexPurge(p)` `DataEraserOption`: `Erase` deletes the subject's index entries at the very end, only when `ErasureResult.Failed()` is `false` **and** the erasure verified: with a certificate sink, the emitted certificate is `Verified`; **without** a sink, `Erase` now runs the very same verification internally (nothing is emitted) and purges only if it would have been `Verified` — so a `KeyIDs`-only, explicit-`Streams`-over-untagged or soft-revoked erasure now retains the index (an earlier revision purged whenever `Failed()` was false). Otherwise the index is retained and a result-only note says so (the purge decision is taken after, and gated on, the certificate, which therefore cannot carry that note). A purge failure is a non-fatal `Errors` entry. Trade-off: after a purge an index-backed resolver resolves an *empty* footprint for that subject and `Verify` reports `Vacuous` — re-verify with a scan resolver or re-run `BackfillSubjectIndex`.
    - **Behavior change:** `BackfillSubjectIndex` now decrypts field-encrypted events (through the store's own decrypt path, `WithDecryptionErrorHandler` honored) before running the tagger, which previously ran over ciphertext and silently under-indexed every subject whose id lives in an encrypted field. Events that cannot be decrypted (revoked key, handler-swallowed failure, or an encrypted event in a store with no encryption config) are no longer handed to the tagger at all: they are indexed from their existing `$subjects` tags and one warning with the count is logged via the store's logger. New `BackfillSubjectIndexWithReport(ctx, store, tagger, writer, batchSize) (BackfillReport, error)` returns PII-free counts — `BackfillReport{Scanned, Indexed, Untagged, Undecryptable}` (`Scanned == Indexed + Untagged`; `Untagged` = events for which neither the tagger nor existing tags produced a subject; `Undecryptable` = encrypted events the tagger never saw, indexed from existing tags only; on error the report covers the events processed so far). `BackfillSubjectIndex` is now a thin wrapper returning `Scanned` (signature unchanged). `WithAuthoritativeIndex` is documented as safe only when the report shows `Untagged == 0` (or the untagged events are known to carry no PII) and no write has bypassed the index writer since.
    - **Behavior change: one ciphertext predicate everywhere** — `DataEraser` key discovery (`Streams` and `Filter` paths), the `WithSharedKeyGuard` shared-key detection, `SubjectResolver` (`SubjectFootprint.KeyIDs` / `CleartextEvents`) and `Verify` now all decide "is this event ciphertext a key revocation would erase?" with `mink.HasEncryptionEnvelope` — the same predicate `RetentionManager` uses. An event with a bare `$encryption_key_id` (or `$encrypted_fields` without a key id / wrapped DEK) is cleartext everywhere: its key is **not** collected or revoked (previously `DataEraser` revoked keys named by a bare key id), it is not listed in `SubjectFootprint.KeyIDs`, it does not make a key "shared" for the guard, it counts in `CleartextEvents` on every path (the resolver and the `Streams`/`Filter` paths now agree), and `Verify` lists it under `ResidualCleartext` (was `ResidualEncrypted`).
    - **Behavior change:** erasure-marker idempotency now ignores any event in the marker stream whose stored `Type` is not `ErasureMarker`, even if it carries the `$erasure_marker_subject` metadata key or a lookalike payload, so a forged lookalike can no longer suppress the genuine marker.
  - **Retention & export**
    - **Behavior change: shared-key blast-radius guard for `ActionShred`, ON by default** — a key is revoked only if *every* field-encrypted event under it (whole store, scanned from position 0 regardless of checkpoint) is covered by an `ActionShred` policy of this sweep (static matchers AND age-eligible; coverage is the union of the sweep's Shred policies, `RedactFields`/`Anonymize` matches do not count). Otherwise the key is skipped, listed in the new `RetentionReport.SharedKeysSkipped`, and one `*RetentionSharedKeyError{KeyID, OutOfScope}` (`errors.Is(err, ErrRetentionSharedKey)`; a count, never events) is appended to `Errors`, so `Failed()` is `true`. Previously the whole key was revoked, shredding out-of-scope events. Opt out with the new `WithAllowSharedKeyRevocation()` (dangerous; skips the guard scan entirely). Cost: one extra full scan per sweep that has candidate keys; a guard-scan failure makes `Apply`/`DryRun` return an error and nothing is revoked. The new `RetentionReport.KeysToRevoke` is the guard-approved set revocation was attempted for; `KeysRevoked` holds the successes. **Accounting:** a Shred match counts in `RetentionReport.Acted` only when its key ended up in `KeysRevoked`; a match whose key the guard refused, whose `RevokeKey` failed, or that had no encryption config (`ErrErasureNotConfigured`) now counts in `Skipped` (previously `Acted`; `DryRun` keeps `Acted`/`Skipped` at `0`). **Checkpoints:** with `WithRetentionCheckpoint`, the persisted frontier is clamped to just before the first Shred match whose key was *not* revoked in this sweep (a settled prefix ahead of it is still persisted), so those events are re-scanned, re-matched and re-reported on every later run until the key is revoked — after `WithAllowSharedKeyRevocation`, a covering Shred policy or a key split — with no checkpoint reset; previously they were settled behind the checkpoint and never re-swept. `WithRetentionMaxScan` no longer truncates a sweep whose resume point is held at a Shred match the guard has yet to decide (it scans to HEAD, like the existing pending-event fallback), so a refused key can never starve the aged tail.
    - **Behavior change:** `ActionShred` collects a key only for events with a complete encryption envelope (`$encrypted_fields` + `$encryption_key_id` + `$encrypted_dek`) — the new exported predicate `mink.HasEncryptionEnvelope(m Metadata) bool` (`IsEncrypted` remains the weaker read-path signal, true as soon as `$encrypted_fields` is present); an event carrying only `$encryption_key_id` is treated as plaintext (previously its key was collected and revoked). Shred matches without an envelope are counted in the new `RetentionReport.UnencryptedMatches` (on `Apply` also still counted as `Skipped`) and, when `> 0`, the report carries the new `ErrRetentionUnencryptedMatches` — so a shred sweep that left matched plaintext now reports `Failed()` instead of a silent `Skipped`.
    - **Behavior change:** `RetentionPolicy.Validate()` rejects a policy with no matchers at all (`Category`, `StreamPrefix`, `EventTypes`, `TenantID` empty and `MaxAge <= 0`) with the new `ErrRetentionUnscopedPolicy` for **every** action; `RetentionManager.Validate`/`Apply`/`DryRun` report it on every run and such a policy is left inert (never matches, acts or revokes). Previously it matched every event — a whole-store shred.
    - `DryRun` now previews the blast radius: it populates `KeysToRevoke`, `SharedKeysSkipped` and `UnencryptedMatches` (plus their errors) without revoking anything or running `Apply` hooks; `Acted`/`Skipped` remain `0` as before. `ErrErasureNotConfigured` is now reported only when there is a guard-approved key to revoke and no encryption config.
    - **Behavior change: export filters fail closed** — `FilterByTenantID("")`, `FilterByUserID("")`, `FilterByMetadata` with an empty key or value, and `FilterByStreamPrefix("")` now match **nothing** (previously they matched every event whose field was empty, i.e. most of the store). `CombineFilters()` with zero filters matches nothing (previously everything), and a `nil` filter passed to `CombineFilters` fails closed instead of panicking.
    - New filters `FilterByStreams(ids ...string)` (exact stream ids, no prefix semantics) and `FilterByStreamCategory(category)` (text before the first `-`); `FilterByStreamPrefix` now documents that `"user-1"` also matches `"user-10"` (end the prefix with the id separator, or use the new filters).
    - **Behavior change: stream-scoped export default** — `Export`/`ExportStream` with explicit `Streams` and a `nil` `Filter` now apply the new `SubjectOrUntaggedFilter(req.SubjectID)`: events whose `$subjects` tags are non-empty and do not include `SubjectID` are dropped, while untagged events are still exported. Callers that used `SubjectID` as a mere label over tagged shared streams now receive fewer events — pass an explicit `Filter` (e.g. `FilterByStreams(...)`) to export a whole stream deliberately.
    - `Anonymizer.Validate() error` + the new `ErrAnonymizerSecretRequired`: an empty HMAC secret degrades pseudonymization to an unkeyed hash whose pseudonyms are re-identifiable by guessing. `NewAnonymizer`'s signature is unchanged; call `Validate` at startup.
  - **Field-level encryption & providers**
    - **Behavior change: fail-closed field decryption** — a field listed in `$encrypted_fields` that is absent from the stored event, is not a string, or whose parent is missing / not an object now fails with `ErrDecryptionFailed` (an `EncryptionError` naming the full field path and key id, mentioning possible tampering); previously such a field was silently skipped and the substituted value passed through as plaintext.
    - **Behavior change:** field encryption/decryption preserves integers above 2^53 (`int64`/`uint64`) exactly in every field of an encrypted event (JSON is decoded with `UseNumber`); previously they were silently rounded through `float64`.
    - **Behavior change: overlapping field paths round-trip** — configured paths are sealed deepest-first and the recorded `$encrypted_fields` are unsealed shallowest-first regardless of the order they were recorded in, so a configuration that lists both a parent and one of its nested fields (`"address"` + `"address.street"`, in either order) now encrypts and decrypts correctly instead of failing decryption as tampered; the `$encrypted_fields` list written for a type with nested paths is now in deepest-first order. `WithEncryptedFields` de-duplicates paths per event type (first occurrence wins).
    - **Behavior change: non-object parents fail closed** — a configured nested path whose parent is present but is not a JSON object (string, number, array, bool) now fails the append with an `EncryptionError` naming the field; previously it was silently skipped and the plaintext stored. An absent or JSON-`null` parent, and an absent leaf, remain optional (not encrypted).
    - **Field-path validation** — malformed paths (empty, or with an empty dot-separated segment such as `"a..b"`, `".a"`, `"a."`) are detected at `NewFieldEncryptionConfig`: the new `(*FieldEncryptionConfig).Validate() error` reports the first offending type/path up front (wrapping the new sentinel `ErrInvalidEncryptedFieldPath`), and the first append of an affected event type fails with an `EncryptionError` wrapping it (previously the path silently never matched). Overlapping paths are valid.
    - **`WithRequireKeyResolution()`** — new opt-in `EncryptionOption`: when a tenant/subject key resolver is configured and yields no key (the event has no `Metadata.TenantID` and no `$subjects` tag to resolve, or the resolver returns `""`), encryption fails with an `EncryptionError` whose cause is the new typed `*KeyResolutionError{EventType, TenantID, SubjectID}` (`errors.Is` matches `ErrEncryptionFailed` and the new sentinel `ErrKeyResolutionFailed`; `errors.As` reaches it) instead of silently wrapping the event under `WithDefaultKeyID` — which would let its PII escape the tenant's or subject's shred key. Default behavior is unchanged.
    - `DecryptStoredEvent` strips the envelope markers through `SanitizeReservedMetadata` after a successful decrypt (the single envelope stripper now shared with `EncryptStoredEvent`, `ReEncryptStream` and `Append`); a `Metadata.Custom` map that becomes empty is now `nil` rather than an empty map.
    - Local provider: master-key material is no longer shared with callers by reference — each crypto operation works on a private copy that is zeroed afterwards, so `RevokeKey`/`Close`/promotion can no longer corrupt an in-flight cipher.
    - **AWS KMS** — `Provider` now implements `encryption.StatefulRevocable` and `encryption.RecoverableRevocable`. **Behavior change:** `RevocationState` / `encryption.GetRevocationState` report a CMK pending deletion as `SoftRevoked` (recoverable via `CancelKeyDeletion`) and only a completed deletion (`DescribeKey` NotFound) as `Revoked`, so erasure verification no longer certifies a pending-deletion key as permanently erased (`IsRevoked` and the `ErrKeyRevoked` decrypt mapping are unchanged — still true for pending deletion). New `SoftRevokeKey(keyID, graceWindow)` (schedules deletion with a window rounded up to whole days and clamped to 7–30; an already-pending key keeps its window) and `UnrevokeKey(keyID)` (`CancelKeyDeletion` then `EnableKey` via the new optional `KMSDeletionCanceller` client interface; `ErrRevocationUnsupported` without it; `*kms.Client` satisfies it). `UnrevokeKey` is state-driven and retryable: a CMK pending deletion gets `CancelKeyDeletion` then `EnableKey`; a CMK that is `Disabled` but not pending deletion — the state a half-finished `UnrevokeKey` leaves behind — now gets `EnableKey` only (**behavior change:** previously a no-op); an enabled key is a no-op; a permanently deleted key returns the new typed `*kms.KeyPermanentlyDeletedError{KeyID}` (`errors.Is` matches the new sentinel `kms.ErrKeyPermanentlyDeleted` **and** `encryption.ErrKeyRevoked`) instead of an untyped error. **Behavior change:** `RevokeKey`/`SoftRevokeKey`/`UnrevokeKey`/`IsRevoked`/`RevocationState` resolve the supplied id (alias or ARN) through `DescribeKey` and act on the canonical `KeyMetadata.KeyId`, so `ScheduleKeyDeletion`/`CancelKeyDeletion`/`EnableKey` no longer receive alias names (which AWS rejects). An alias is resolved **at call time**: `RevokeKey("alias/x")` shreds whatever the alias points at when the call runs, re-pointing the alias later changes what `IsRevoked("alias/x")` answers — stamp and revoke key ARNs, never aliases (`WithDefaultKeyID` doc). A **missing alias** (`DescribeKey` NotFound for an `alias/...` name or alias ARN) is reported as an unknown state through the new sentinel `kms.ErrAliasNotFound` (wrapped in an `ErrDecryptionFailed` probe error) by `IsRevoked`/`RevocationState`/`RevokeKey`/`UnrevokeKey`, never as `Revoked`: an alias is a mutable pointer, so its absence proves nothing about the sealing key and must not let erasure verification certify a crypto-shred that never happened. Only a key id or ARN that `DescribeKey` cannot find is the terminal `Revoked` state. **Behavior change:** context-free revocation methods are bounded by the new `WithRevocationTimeout(d)` (default `DefaultRevocationTimeout` = 30s) instead of an unbounded `context.Background()`; the revocation probe on the `Decrypt`/`DecryptDataKey` error path now runs under the caller's context (its deadline is respected; a deadline-less context is bounded by the revocation timeout), and a probe that cannot determine the key state surfaces as `ErrDecryptionFailed` rather than `ErrKeyRevoked`.
    - **HashiCorp Vault** — **Behavior change:** Transit key names are validated before any request and rejected with an `EncryptionError` (encrypt-op for `Encrypt`/`GenerateDataKey`/`RevokeKey`, decrypt-op for `Decrypt`/`DecryptDataKey`/`IsRevoked`) without ever reaching the `VaultClient`; previously any string was forwarded verbatim into request paths such as `/v1/transit/encrypt/<name>`. The rule is the minimum that closes path traversal: a name must be non-empty, contain no `/`, contain no control bytes (below `0x20`, or `0x7f`) and not be the whole segment `.` or `..`; everything else — spaces, `?`, `#`, `%`, non-ASCII, an inner `..` such as `a..b` — is accepted and handed to the `VaultClient` `url.PathEscape`d (`"tenant 42"` arrives as `"tenant%2042"`; a client built on an SDK that escapes path segments itself must `url.PathUnescape` first), so a deployment that already named keys that way keeps decrypting its ciphertext. **Behavior change:** `RevokeKey`/`IsRevoked` are bounded by the new `vault.WithRevocationTimeout(d)` (default `vault.DefaultRevocationTimeout` = 30s; non-positive values ignored) instead of an unbounded `context.Background()`, and the `KeyExists` probe on the `Decrypt`/`DecryptDataKey` error path runs under the caller's context (its deadline respected; a deadline-less context bounded by the same timeout), mirroring the KMS provider.
  - **Command bus & middleware**
    - **Behavior change: principal-scoped idempotency keys** — new `IdempotencyConfig.Scope func(ctx, cmd) string`, defaulting to the new `DefaultIdempotencyScope` (returns `TenantIDFromContext(ctx)`): the stored key becomes `<len(scope)>:<scope>|<key>` (e.g. `8:tenant-a|req-1`) whenever `TenantMiddleware`/`WithTenantID` has set a tenant, so two tenants presenting the same client key can no longer replay or suppress each other. The length prefix makes the encoding injective across scopes — tenant `a` with key `b|x` stores `1:a|b|x`, tenant `a|b` with key `x` stores `3:a|b|x` — so no client-chosen key, even one containing `|` or `:`, can collide with another scope's; unscoped keys (`Scope` returns `""`) are still stored verbatim. Tenant-scoped deployments will not deduplicate against records written under the old unscoped key (a one-time loss for in-flight keys across the upgrade); supply a `Scope` that returns `""` to keep the old unscoped behavior. The new exported `EffectiveIdempotencyKey(cmdType, scope, key)` lets you predict or pre-seed the exact stored key.
    - **Behavior change:** idempotency keys whose effective form exceeds the new `MaxIdempotencyKeyLength` (255 bytes, the PostgreSQL `key VARCHAR(255)` width) are replaced by `<len:scope|><CommandType>:sha256:<hex digest>` before reaching the store (the visible scope prefix is kept and the prefix + command type is truncated on a rune boundary to fit 255 bytes; the digest covers the full scoped key, so scoping survives hashing), so an over-long client key no longer fails the insert and silently defeats deduplication under fail-open. Likewise the new `MaxIdempotencyFieldLength` (255): `NewIdempotencyRecord` truncates `CommandType` and `AggregateID` to that many bytes on a rune boundary (the reservation record bounds `CommandType` the same way), so an over-long command type or aggregate id can no longer fail the PostgreSQL insert either; a replayed `CommandResult` for an aggregate id longer than 255 bytes carries the truncated id.
    - **Behavior change:** `GenerateIdempotencyKey` returns `""` instead of a per-type `:type-only:` key when the command cannot be JSON-serialized; `GetIdempotencyKey` no longer uses an empty string returned by `IdempotentCommand.IdempotencyKey()` (it falls back to the content-derived key); and `IdempotencyMiddleware` passes any command with an empty effective key straight through to the handler (no idempotency guarantee, nothing stored) instead of deduplicating it against one shared global key. `IdempotencyKeyPrefix` returns `""` (not a bare prefix) when the underlying key is empty.
    - **Behavior change:** `RecoveryMiddleware` no longer records the full JSON-serialized command in `PanicError.CommandData` by default — it records only `{"commandType": …, "aggregateId": …}` (aggregate id omitted when empty). The signature is now the backward-compatible variadic `RecoveryMiddleware(opts ...RecoveryOption)`; `WithPanicCommandCapture()` restores the full capture for pipelines where every consumer of `PanicError` is trusted.
    - **Behavior change:** `CorrelationIDMiddleware`'s default correlation id is now a random v4 UUID (`uuid.NewString`) rather than a nanosecond timestamp string.
    - **Audit middleware** — **Behavior change:** fail-open mode now logs a warning for every dropped audit entry (to the new `AuditConfig.Logger`, or `log/slog`'s default logger when `nil`) and one warning at construction when configured without a store; previously drops were silent. New `AuditConfig.OnError func(ctx, *AuditEntry, error)` hook, invoked on every `Store.Append` failure in both modes, and `AuditConfig.MetadataFilter func(map[string]string) map[string]string`, applied to the private copy captured by `IncludeMetadata` to drop or mask keys (the trail is plaintext; `nil`/empty result omits metadata). **Behavior change:** `CommandType`, `CommandID`, `AggregateID`, `Actor`, `TenantID`, `CorrelationID` and `CausationID` are truncated to the new `MaxAuditFieldLength` (255 bytes, on a rune boundary) before `Append`, so an over-long client-supplied value can no longer make the audit row fail.
  - **Outbox publishers**
    - **Webhook** — **Behavior change:** the default HTTP client no longer follows redirects: a 3xx response fails delivery (retry/dead-letter) instead of re-sending the payload and default headers (which commonly carry a bearer token) to the redirect target. **Behavior change:** a client injected via `WithHTTPClient` is no longer used as-is (nor mutated): the publisher works on a shallow copy, a client with a `nil` `CheckRedirect` refuses redirects exactly like the default client, and a client with its own `CheckRedirect` has every hop validated against the publisher's destination policy — `http`/`https` scheme, non-empty host, `WithRequireHTTPS`, `WithAllowedHosts`, read at request time — before its own function runs, failing the delivery with the same `ErrHostNotAllowed` / `ErrHTTPSRequired` / `ErrInvalidDestination` without contacting the host (`WithTimeout` applied after `WithHTTPClient` changes the copy, never the caller's client). **Behavior change:** every destination is validated before sending — it must parse, use `http`/`https` and have a non-empty host, else `ErrInvalidDestination` without a request; message headers whose key or value contains CR/LF, or whose key is empty, are rejected with `ErrInvalidHeader`; error messages carry only `scheme://host[:port]` of the destination (userinfo, path, query and fragment dropped, including inside the `*url.Error` returned for transport failures — Slack/Discord-style path secrets never reach `last_error`); response bodies are drained up to 1 MiB instead of unbounded. The package's sentinel errors are prefixed `webhook: ` (its pre-existing prefix, not `mink/webhook: `). New options: `WithAllowedHosts(hosts ...string)` (case-insensitive exact host or leading `*.` wildcard for strict subdomains, port ignored, **fails closed** — an empty allowlist denies everything → `ErrHostNotAllowed`), `WithRequireHTTPS()` (→ `ErrHTTPSRequired`) and `WithSigningSecret(secret []byte)`, which adds `X-Outbox-Timestamp` (unix seconds) and `X-Outbox-Signature: sha256=<hex(HMAC-SHA256(secret, timestamp + "." + body))>` (exported `HeaderTimestamp` / `HeaderSignature` constants); the two headers are written after per-message `X-Outbox-*` headers so they cannot be overridden.
    - **Kafka** — new `WithTransport(*kafka.Transport)`, `WithTLS(*tls.Config)` and `WithSASL(sasl.Mechanism)` options (no new dependency — `sasl` is part of the existing `kafka-go` module). The default remains PLAINTEXT and unauthenticated and is now documented as such; SASL/PLAIN must be paired with `WithTLS`. `WithTransport` replaces any earlier TLS/SASL setting, so apply it first and layer `WithTLS`/`WithSASL` after it.
    - **SNS** — **Behavior change:** outbox message headers are validated as SNS message-attribute names: a header whose name starts with `AWS.` or `Amazon.` (case-insensitive) now fails that message with `ErrReservedAttribute` unless `WithAllowReservedAttributes()` is set (SNS treats such attributes as delivery-control directives — SMS sender id, max price, push type), and a name outside `[A-Za-z0-9_.-]` / 1–256 chars / period rules fails with `ErrInvalidAttributeName`. Other messages in the batch are still attempted.
    - **Behavior change:** the `OutboxProcessor` "No publisher for destination" line and the `EventStoreWithOutbox` "Failed to transform outbox payload" line now log the destination reduced to its origin — `<prefix>:scheme://host[:port]` (userinfo, path, query and fragment all dropped; an unparseable URL becomes `<prefix>:<redacted>`; non-URL destinations such as `kafka:orders` are unchanged).
  - **Observability**
    - `middleware/metrics` — **no behavior change.** The `destination` label on `outbox_messages_processed_total` / `outbox_messages_failed_total` already carried only the publisher prefix (`webhook`, `kafka`, `sns` — the text before the first `:`), because `OutboxProcessor` hands `OutboxMetrics` the prefix rather than the full destination; that contract is now documented on the package and pinned by a test, and `RecordMessageProcessed`/`RecordMessageFailed` record the value they are given verbatim, exactly as before. Dashboards need no change. (An intermediate `WithFullDestinationLabels()` opt-out drafted during the audit was never released and does not exist.)
    - **Behavior change (`middleware/tracing`):** span attributes built from runtime values (`mink.command.type`, `mink.command.aggregate_id`, `mink.correlation_id`, `mink.result.aggregate_id`, `mink.stream_id`, each `mink.events.types` entry, `mink.projection.name`, `mink.event.type`, `mink.event.id`, `mink.event.stream_id`) and the `command.<type>` / `projection.<name>.apply` span names are now truncated to `DefaultMaxAttributeLength` (256 runes, rune-boundary safe); `WithMaxAttributeLength(n)` changes the cap and `0` restores unbounded recording (`(*Tracer).MaxAttributeLength()` reads it back). New `WithErrorRedaction(fn func(error) string)` and `WithoutErrorDetails()` (records the fixed `RedactedErrorMessage` = `"operation failed"`, neutral across command, event-store and projection spans) replace the raw error text in the span status and exception event on every span the package creates (command, event store, projection) — error strings routinely embed request data; the error returned to the caller is never altered. Raw errors are still recorded by default. `tracing.SetError` is not bound to a `Tracer`, so redaction does not apply to it.
  - **Sagas, projections, subscriptions & serialization**
    - **Behavior change:** `SagaManager` no longer hydrates from and overwrites another saga *type*'s row that happens to share a correlation id: when the `SagaStore` lacks the optional `FindByCorrelationIDAndType`, such a mismatch now fails with the new `ErrSagaTypeMismatch` (typed `*SagaTypeMismatchError{CorrelationID, SagaID, ExpectedType, ActualType}` with `Is()`/`Unwrap()`); the event loop logs it and continues, `StartSaga` returns it. Stores implementing the new optional `adapters.SagaCorrelationTypeFinder` (memory and PostgreSQL do; package `mink` re-exports it as the `SagaCorrelationTypeFinder` type alias) are preferred and let both saga types coexist on one correlation id. `SagaManager.Register` now logs at Error level when `factory("").SagaType()` differs from the registered name, and processing a starting event for such a registration fails with the same `ErrSagaTypeMismatch` (`ExpectedType` = the registered name, `ActualType` = the saga's `SagaType()`) instead of persisting a row that type-scoped lookups and re-drives could never find. A `SagaStore` that reports a missing saga as `(nil, nil)` is treated as `ErrSagaNotFound` (no nil dereference).
    - **Behavior change:** with `WithSagaRetryCapture`, a field-encrypted trigger event is now captured as a *locator* only (stream id, version, global position, type, id, timestamp) — its decrypted payload and metadata are no longer persisted into saga state. `RetrySaga`/`ResumeStalled` reload such an event from the event store through the normal decrypt path, so a re-drive requires the manager to have been built with an `EventStore` and fails with the decryption error (e.g. a revoked key) if the event can no longer be decrypted. A locator is marked explicitly by `Metadata.Custom["$saga_trigger_locator"] = "true"` rather than inferred from an empty payload, so a plaintext trigger with no `Data` (e.g. a synthetic event handed to `StartSaga`) is captured whole and re-driven without a store read, like every other plaintext event. The reload reads exactly one event — `adapters.StreamQueryAdapter.GetStreamEvents(stream, version-1, 1)` when the adapter can page (both shipped adapters), else a stream scan — and refuses to re-drive when the stored event's id, type, version or global position disagree with the locator.
    - **Behavior change:** a panic inside an inline projection's `Apply` is now recovered by `ProjectionEngine.ProcessInlineProjections` and reported as a `*ProjectionError` (`errors.Is(err, ErrProjectionFailed)`) naming the projection, event type and position — never the payload — instead of unwinding into the caller's append; the same contract the async and live paths already had.
    - **Behavior change:** a panic inside a user upcaster is now recovered by `UpcasterChain.Upcast` (hence `Load`/`LoadAggregate` and `UpcastingSerializer`) and returned as an `*UpcastError` (`errors.Is(err, ErrUpcastFailed)`) naming the event type and the version transition that failed, with no partial data returned.
    - **Behavior change:** an async projection worker no longer fails its whole batch load when one field-encrypted event cannot be decrypted. `ProjectionEngine` loads the batch raw and decrypts only the events the projection handles, one at a time (no per-event call when encryption is unconfigured); a handled event that fails to decrypt is attributed to that event and flows through the normal `ErrorClassifier` / retry budget / `OnPoisonEvent` path (the handler receives the event as stored and a cause satisfying `errors.Is(err, ErrKeyRevoked)` or `errors.Is(err, ErrDecryptionFailed)`), the events before it in the batch are processed and checkpointed normally, and undecryptable events the projection does not handle are skipped without being decrypted. `ProjectionRebuilder` is unchanged (it still decrypts batch-wise and fails a rebuild on an undecryptable event).
    - **Behavior change:** `PollingSubscription` now delivers field-encrypted events decrypted (like `Load`, `CatchupSubscription` and projections) and filters on plaintext; a hard, unhandled decryption error stops the subscription with `Err()` set instead of delivering ciphertext (a crypto-shred handler that swallows the error still yields the event as stored). Zero overhead when encryption is unconfigured.
    - **Behavior change:** `EventRegistry.Register`/`RegisterAll` (and `JSONSerializer.Register`/`RegisterAll`) no longer overwrite an existing event name with a different Go type: the first registration wins and the rejected name is recorded in the new `EventRegistry.Conflicts() []string` (sorted copy; `nil` when none) so startup can fail deterministically. Re-registering the same type remains an idempotent no-op.
  - **Adapters**
    - New optional footprint-aware GDPR interfaces in `adapters`, implemented by the memory and PostgreSQL outbox / audit / idempotency / saga stores: `SubjectOutboxFootprintPurger` (`DeleteOutboxByAggregateIDs`), `SubjectAuditFootprintPurger` (`DeleteAuditByAggregateIDs`), `SubjectIdempotencyFootprintPurger` (`DeleteIdempotencyByAggregateIDs`), `SubjectSagaFootprintPurger` (`DeleteSagasByCorrelationIDs`), `SubjectOutboxCounter` (`CountOutboxByAggregateIDs`), `SubjectAuditCounter` (`CountAuditBySubject(subjectID, aggregateIDs)` — actor OR aggregate id), `SubjectIdempotencyCounter` (`CountIdempotencyByAggregateIDs`), `SubjectSagaCounter` (`CountSagasByCorrelationIDs`) and `SagaCorrelationTypeFinder` (`FindByCorrelationIDAndType(ctx, correlationID, sagaType)`, latest row by `started_at`; an error satisfying `errors.Is(err, ErrSagaNotFound)` when absent). Shared contract: an empty/all-empty id slice returns `(0, nil)` without touching the store; ids are de-duplicated and matched exactly (PostgreSQL binds the list as one `= ANY($n::text[])` array parameter, so commas, braces, quotes and backslashes match literally). The id set the eraser passes is the subject id plus its **exclusive** footprint streams (shared streams are never purged nor counted), and the derived aggregate id only under `mink.WithDerivedAggregateIDs` — see the GDPR section. New exported `adapters.SanitizeSQLComment(s string) string` (control characters including DEL and C1 plus U+2028/U+2029 stripped, single quotes doubled) is the one helper both built-in `GenerateSchema` implementations use.
    - **Behavior change (PostgreSQL):** `ListStreams` treats the prefix as literal text — `%`, `_` and `\` are backslash-escaped and matched with `LIKE` under PostgreSQL's default escape character (the same mechanism as the read-model `CONTAINS` filter and the category subscriptions; no explicit `ESCAPE` clause, so it also works on sessions with `standard_conforming_strings=off`) instead of acting as wildcards. `StreamsBySubject` no longer fails for every subject when any row carries a malformed `$subjects` tag: on SQLSTATE `22P02` it transparently falls back to a slower Go-side scan (results for well-formed data are identical; the fallback never runs while data is healthy). **Behavior change:** that fallback is no longer silently partial — a malformed row whose raw text may name the subject (verbatim or in its JSON-escaped spelling) on a stream not resolved through a well-formed row makes the call fail with the new typed `*postgres.SubjectTagMalformedError{SubjectID, Rows, Streams}` (`errors.Is(err, postgres.ErrSubjectTagMalformed)`; counts and the requested subject id only, never tag text or stream ids), which a resolver using the adapter via `WithResolverIndex` surfaces from `Resolve`; malformed rows that cannot mention the subject are still skipped. Results are now sorted bytewise in Go (`sort.Strings`) on both paths — the JSONB fast path dropped its collation-dependent `ORDER BY` — so mixed-case or punctuated stream ids may come back in a different order (`"B-1"` < `"Zed-1"` < `"_x-1"` < `"a-1"`); the set is unchanged. `sql.ErrNoRows` is detected with `errors.Is` throughout (`appendInTx`, `GetStreamInfo`, `LoadSnapshot`, `GetCheckpoint`, `GetProjection`, `IdempotencyStore.Get`, `PostgresRepository.Get`), so wrapped not-found errors are handled correctly.
    - **Behavior change (PostgreSQL + memory):** `GenerateSchema` sanitizes the project name in the generated `-- Generated for:` header through `adapters.SanitizeSQLComment` (control characters and Unicode line separators stripped, single quotes doubled), so a hostile `mink.yaml` project name can no longer terminate the comment and inject DDL.
    - **Behavior change (memory):** events returned by `Load`, `LoadFromPosition(Filtered)`, `GetStreamEvents` and subscriptions are detached copies (`Data` via `bytes.Clone`, `Metadata.Custom` via `maps.Clone`; one allocation per field per event), the `StoredEvent`s returned by `Append` carry the caller's own `Data`/`Metadata` rather than aliasing the log, `RewriteEventData` stores private copies of the supplied data/metadata, and the `IdempotencyStore` copies the `Response` payload on `Store`/`StoreIfAbsent`/`Get` (`adapters.CopyIdempotencyRecord` now clones `Response` too). Mutating a returned value can no longer alter stored history or undo a redaction.
  - **CLI (`mink`)**
    - **Behavior change:** `mink migrate down` now asks for an interactive confirmation (lists the migrations, defaults to No) before executing any `.down.sql`; pass the new `--yes` / `-y` flag or `--non-interactive` (previously a no-op) to skip it. When no terminal is available and neither flag is passed, the command fails instead of rolling back. Migration names read from the migrations table must match `[A-Za-z0-9_.-]+` with no `..` and resolve inside the migrations directory (one bad row fails the whole read), and `migrate up`/`migrate down`/`migrate status` fail with `failed to read applied migrations: …` when the table cannot be read instead of treating every migration as pending and re-running them.
    - **Behavior change:** config discovery (`config.FindConfig`, hence every command) stops at the nearest directory containing `go.mod`; a `mink.yaml` planted above the Go module boundary is no longer picked up. Without any `go.mod` on the path the walk to the filesystem root is unchanged.
    - **Behavior change:** environment-variable references in `mink.yaml` (`database.url`, expanded by every command that opens the store and by `diagnose`) are allow-listed by variable **name**: only names starting with `MINK_`, `DATABASE_`, `DB_`, `PG` or `POSTGRES` (case-insensitive) are expanded; any other reference — `$HOME`, `${AWS_SECRET_ACCESS_KEY}`, shell specials such as `$1` — now fails with an error naming the variable instead of being silently expanded (or silently blanked when unset), so a planted `mink.yaml` cannot route a secret into the DSN. Operators widen the set with the new `MINK_CONFIG_ENV_ALLOW` environment variable (comma-separated extra name prefixes). The rule applies to every driver; a literal `$` in a DSN password must be percent-encoded as `%24` (previously it was silently mangled by `os.ExpandEnv`). Allowed-but-unset variables still expand to `""` and yield the existing "DATABASE_URL environment variable is not set" error.
    - **Behavior change:** `mink.yaml` written by `mink init` and `config.SaveFile` is created with the new `config.ConfigFileMode` (`0600`, was `0644`) because it can hold a database URL, and `mink stream export` writes its output file `0600` (was `0644`); `.gitkeep` placeholders stay `0644`.
    - **Behavior change:** `mink generate aggregate|event|command|projection` now reject — with no files written — a `generation.*_package` that resolves outside the project root (the directory holding `mink.yaml`, or cwd without a config), a last path element that is not a valid lowercase Go package name (`^[a-z][a-z0-9_]*$`), or aggregate/event/command/projection names (and projection `--events` entries) that are not Go identifiers (`^[A-Za-z_][A-Za-z0-9_]*$`); kebab/snake-case inputs such as `order_item` / `item-added` are still accepted because they are PascalCased before validation (`mink generate projection --events` now PascalCases its entries too — `order-created` → `OrderCreated` becomes the handler method and event-type literal — matching the aggregate generator; an earlier revision validated projection entries verbatim and rejected kebab/snake-case). Errors name the offending `mink.yaml` key. `styles.FormatStep` now renders counters above 9 correctly (`[10/12]`).
    - **Behavior change:** `mink gdpr discover|verify|erase|retain` render stored values — encryption key ids, stream ids, subject ids, error text — with control characters (newline, CR, tab, ESC/ANSI sequences, NUL, DEL, C1) and U+2028/U+2029 replaced by U+FFFD, so a poisoned `$encryption_key_id` or stream id can no longer forge or hide report lines; clean values print unchanged.
  - **Testing utilities**
    - **Behavior change (`testing/containers`):** `PostgresContainer.DropSchema` returns the new sentinel `ErrInvalidSchemaName` (without contacting the database) for a schema name not matching `^[A-Za-z_][A-Za-z0-9_]*$`, and the identifier quoting now doubles embedded `"`; every name `CreateSchema` produces still passes, so `NewIntegrationTest`/`NewFullStackTest` cleanup is unaffected. The built-in `postgres/postgres@localhost:5432/mink_test` defaults are documented as test-only; the package does **not** read `TEST_DATABASE_URL` (use the `POSTGRES_*`/`TEST_POSTGRES_*` overrides).
    - **Behavior change (`testing/testutil`):** `UniqueSchema` sanitizes its prefix instead of trusting it (characters outside `[A-Za-z0-9_]` → `_`, a leading digit is prefixed with `_`, an empty prefix yields `test_<nanos>`); prefixes that were already valid identifiers produce byte-for-byte identical output. New exported `DefaultPostgresURL` constant documents the TEST-ONLY DSN that `DefaultConfig` falls back to when `TEST_DATABASE_URL` is unset.
  - **CI, repository & examples**
    - CI: `branch-protection.yml` now rejects a PR to `main` whose head is `develop` on a fork (the head repo must equal `github.repository`), not just a differently-named branch; `SonarSource/sonarqube-scan-action` and `golangci/golangci-lint-action` are pinned to immutable commit SHAs (v8.3.0 / v9.3.0); Dependabot tracks `gomod`, `npm`, `github-actions` and `docker-compose` (the PostgreSQL / Kafka image tags in `docker-compose.test.yml`) weekly — there is no Dockerfile in the repository, so no `docker` ecosystem entry. Repo: `*.pem`, `*.key`, `*.p12`, `*.pfx` and `.env.*` (except `.env.example`) are git-ignored.
    - Examples: `examples/metrics` serves `/metrics` on `127.0.0.1:9090` (loopback only, dedicated mux, request timeouts) instead of `:9090` on every interface via `http.DefaultServeMux`; `examples/export` tags subjects at append time (`PaymentReceived` gained `CustomerID`), runs a resolver-based subject export first, and uses `SubjectFilter` for the subject export with the actor-scoped `FilterByUserID` shown only as a contrast; `examples/encryption` prints which fields remain plaintext after key revocation and flags `encryption/local` as dev/test only.
    - Docs: production tutorial snippets require `sslmode=verify-full` (+ `sslrootcert`) and a TLS OTLP exporter (plaintext OTLP is an explicit dev-only opt-in refused in production); ADR-010 (multi-tenancy) is rewritten against the real API and no longer claims automatic tenant injection or header-based tenant isolation; the GDPR pages state that AWS KMS revocation is recoverable (`SoftRevoked`) until the pending-deletion window elapses, and document the exclusive-stream id set / `SharedStreams` / derived-id opt-in, the unified `HasEncryptionEnvelope` predicate, vacuous verification, the backfill report, the strict `$schema_version` default, the idempotency scope encoding and the CLI environment allowlist; ADR-010's projection example deserializes `StoredEvent.Data` by `event.Type` (its first rewrite type-switched on the `[]byte` payload and could not compile).

### Added
- **Async projection retry classification & fault supervision** — closes three compounding gaps that let a single transient database error silently and permanently kill an async projection's read model (all additive/opt-in, zero-overhead when unset, no schema change; the async worker only — inline/live projections are unchanged):
  - **Transient-vs-poison error classification** — a new optional `AsyncOptions.ErrorClassifier func(error) ErrorClass` splits the single retry budget in two: an error classified `ErrorClassTransient` is retried with backoff but never consumes the poison budget (`MaxRetries` / `RetryPolicy`), so it never reaches `OnPoisonEvent` or faults the worker, while an `ErrorClassPoison` error is accounted exactly as before. Ships `DefaultErrorClassifier`, which classifies as transient anything whose `Unwrap` chain satisfies `errors.Is(err, ErrTransient)`, implements the exported `Retryable() bool`, or implements `interface{ Temporary() bool }` (the `net.Error` idiom) — but deliberately **not** `context.DeadlineExceeded` (a hung poison event must not retry forever behind a batch timeout). Exports the `ErrTransient` sentinel and `Retryable` interface so callers can mark their own errors. `nil` classifier (the default) treats every error as poison — byte-for-byte the prior behavior and overhead.
  - **Faulted-worker supervision** — a new optional `AsyncOptions.RestartPolicy` (`RestartPolicy` interface, with `ExponentialBackoffRestart(maxRestarts, base, max)` where `maxRestarts <= 0` is unlimited, and `RestartForever(base, max)`) restarts a Faulted worker with backoff **resuming strictly from its persisted checkpoint** — a restart never reprocesses from position 0, even under `StartFromBeginning` (that governs only the first boot); reprocessing history stays the exclusive job of `Rebuild`. A persistent checkpoint-read failure faults (never defaults to 0) and is itself restartable, so a checkpoint-store outage that heals self-recovers. `Stop` still joins a worker parked in restart backoff. `nil` (the default) leaves a fault terminal, exactly as before. Adds the additive `ProjectionStateRestarting` state.
  - **Manual `ProjectionEngine.Restart(ctx, name)`** — an operator primitive symmetric with `Pause`/`Resume`/`Rebuild` that relaunches a Faulted worker from its checkpoint; idempotent (a no-op on a non-Faulted worker), returns `ErrProjectionNotFound` for an unknown name, and concurrency-guarded to relaunch at most one goroutine.
  - **Push-based state observer** — `WithProjectionStateObserver(func(name string, old, new ProjectionState, err error))` fires on every async/live state transition (carrying the fault error when entering `Faulted`), so a fault pushes an alert instead of waiting to be polled via `GetStatus`. Invoked outside the worker's state lock, so the callback may safely re-enter the engine. Does not change or extend the `ProjectionMetrics` interface.
- **Operator-initiated safe saga re-drive** — `SagaManager.RetrySaga(ctx, sagaID)` re-drives a settled-but-unsuccessful saga (`Failed` / `Compensated` / `CompensationFailed`) once its underlying cause is fixed — turning a previously dead-ended saga from inspect-only (`GetSaga` / `FindSagasByType`) into inspect-and-recover, the operator counterpart to the automatic `WithSagaTimeout` sweep. It re-delivers the saga's last trigger event through the normal processing path, so a retry has **identical semantics to a fresh delivery**: on success the saga reaches `Completed`; on a fresh failure it follows the same compensation path. It is **safe by construction**, composing existing primitives: it runs under the same per-saga lock the event loop uses (cannot race an in-flight event), persists under optimistic concurrency (a concurrent change surfaces `ErrConcurrencyConflict`, never silently swallowed), and resets idempotency for **only** the retried event so already-succeeded earlier steps (in `ProcessedEvents`) are not re-dispatched. It **rejects** a terminal `Completed` saga and any in-flight `Started`/`Running`/`Compensating` saga with the new typed `ErrSagaNotRetryable` (`*SagaNotRetryableError`; `SagaStatus.IsRetryable` / `SagaState.IsRetryable` encode the matrix, mirroring `IsTerminal`); a missing saga returns `ErrSagaNotFound`. Re-drive is **auditable** — recorded as a `SagaStep` on the saga's history and reported to an optional `WithSagaRetryObserver(func(RetryEvent))` hook (carrying the from-status and the reached `ResultStatus`, so a re-drive that re-fails into compensation is distinguishable from a clean recovery), so it is never a silent state change. Saga command handlers MUST remain idempotent (the same contract as the store's at-least-once delivery). Also adds `ResumeStalled(ctx, sagaID)` (re-drive a `Running` saga whose worker died mid-dispatch — accepts only `Running`, and only once it is older than the configured `WithSagaTimeout`, so it can't fight a live worker) and `RetrySagasByType(ctx, sagaType, statuses...) (RetryReport, error)` (batch re-drive, per-saga outcomes, does not abort on one failure). **Opt-in and no schema change**: enable capture of the re-drivable last event with `WithSagaRetryCapture()` — off by default (zero overhead; nothing is captured or persisted unless enabled). When on, the event is stored in a **manager-owned** reserved slot of the saga's persisted `SagaState.Data` — the manager stamps and reads it itself and strips it before the saga's own `SetData` sees it, so it never leaks into saga-author code and works for **any** saga, including projection-style sagas that rebuild `Data` from typed fields.
- **In-place stream re-encryption (historical field-encryption backfill)** — `EventStore.ReEncryptStreamInPlace(ctx, streamID) (reEncrypted int, keyIDs []string, err error)` brings a stream's existing events under the current `FieldEncryptionConfig` **in place** — sealing each not-yet-encrypted event's configured fields under the current key (running the configured `SubjectTagger` first, so the key resolves exactly as the append path would) and rewriting the same rows, preserving id/type/stream/version/global-position/timestamp. Only the at-rest encoding changes (ciphertext for plaintext) — the decrypted content and every identity column are preserved — but this is a narrow, opt-in exception to the append-only invariant: the one write that mutates a stored event row in place (unlike retention `RedactFields`, which never rewrites the log — it runs a caller hook against read models). It's the fix for consumers that enabled encryption *after* accumulating plaintext data and whose aggregates read **fixed** stream ids — where `ReEncryptStream`'s copy-to-a-new-stream doesn't fit — making that historical PII crypto-shreddable. Idempotent and resumable (already-encrypted events are skipped). Opt-in: requires an adapter implementing the new optional `adapters.EventRewriteAdapter` (`RewriteEventData`, shipped on the in-memory and PostgreSQL adapters), else `ErrRewriteNotSupported`; a no-op with zero overhead when no encryption is configured. Also adds `EventStore.EncryptStoredEvent` — the exported encode counterpart to `DecryptStoredEvent`. See the [`reencrypt-inplace`](examples/reencrypt-inplace) example.
- **Filtered feed reads** — `EventStore.LoadEventsFromPositionFiltered(ctx, fromPosition, limit, FeedFilter)` reads the global event feed filtered by *indexed* axis — event type, stream id, and/or stream category — pushing the predicate down to storage via the new optional `adapters.FilteredFeedAdapter` (implemented by the in-memory and PostgreSQL adapters; `ErrFilteredFeedNotSupported` otherwise). It reuses the shared load-from-position query, so it keeps the ordering, `limit`, exclusivity, and gapless safe-watermark guarantees of `LoadEventsFromPosition`; an empty `FeedFilter` is identical to it. Indexed-only by design — for unindexed criteria (a tenant in metadata, a payload field) or application reads, project a read model instead. Intended for introspection: audit browsers, migration/backfill scanners, and diagnostics. See the [`feed-filter`](examples/feed-filter) example.
- **`mink events` CLI — filtered global-feed inspector** — a new top-level command that reads the global event feed by *indexed* axis (`--type`, `--stream`, `--category`), starting after a global position (`--from`, exclusive) and bounded by `--limit`, built on the `FilteredFeedAdapter` above. `--type`/`--stream` are repeatable / comma-separated OR-sets; axes AND-compose; an empty filter walks the whole feed. Defaults to a scannable table; `--json` emits a JSON array (each event carrying its `global_position`, with `data`/`metadata` as raw nested JSON for jq/CSV pipelines). Where `mink stream events <id>` walks a single stream by version, `mink events` scans across streams by global position. Introspection / ops / migration tooling — not an application read path (project a read model for unindexed queries); field-encrypted `data` is shown as stored, since the CLI holds no keys.
- **Serializer/adapter compatibility guard** — two optional interfaces let a storage adapter and a serializer declare the on-disk data format they require/produce, so an incompatible pairing is caught up front: `adapters.JSONDataAdapter` (`RequiresJSONData() bool`, implemented by the PostgreSQL adapter, whose `events.data` column is `JSONB`) and `mink.BinaryFormatReporter` (`BinaryFormat() bool`, implemented by the shipped `serializer/msgpack` and `serializer/protobuf`). Both are opt-in and structural — custom serializers and adapters that implement neither behave exactly as before. See the matching entry under Fixed.
- **GDPR Right to Erasure (Article 17) & Retention** — completes the data-governance story alongside `DataExporter`:
  - `encryption.Revocable` (`RevokeKey`/`IsRevoked`) — portable crypto-shredding, implemented for local/KMS/Vault via optional client sub-interfaces (base injected clients unchanged); `encryption.RecoverableRevocable` adds soft-revoke with a grace window
  - `DataEraser` — one-call subject erasure: revoke keys, redact read models (`SubjectRedactable` in-place hook or `ReadModelRebuilder`), run external-PII `WithErasureHook`s, append an optional `ErasureMarker`, and emit a verified `ErasureCertificate`; idempotent, with a partial-failure result
  - `SubjectResolver` + `WithSubjectTagger` — resolve a subject's complete cross-stream footprint (optional `SubjectIndexAdapter`, scan fallback); completeness is never silently partial (`Partial` flag)
  - `RetentionManager` + `RetentionPolicy` — schedulable sweep (Shred / RedactFields / Anonymize) with dry-run and a report. Optionally **resumable**: `WithRetentionCheckpoint(store CheckpointStore, name)` makes a scheduled sweep resume from a persisted safe-resume frontier (the highest position below which no event can newly match) instead of re-scanning the whole append-only store every run — bounding steady-state cost to the events within the retention window rather than total history — and `WithRetentionMaxScan(n)` caps a single sweep (resuming the remainder next run) to bound the first run after enabling retention on a large store. Both are opt-in; unconfigured, a sweep scans from position 0 exactly as before
  - `Anonymizer` — deterministic, one-way pseudonymization; `ReEncryptStream` — append-only re-encryption by copy
  - `DataEraser.Verify` — confirms no recoverable PII remains across events and read models
  - `mink gdpr` CLI — `discover` (subject footprint), `verify` (erasure readiness: encrypted vs residual cleartext), `erase` (auditable erasure plan — keys to revoke), `retain` (dry-run a retention policy); read-only over the diagnostic adapter, since the CLI does not hold the app's encryption keys (revocation runs from the app via `DataEraser`/`RetentionManager`)
- **GDPR erasure hardening** — closes the gaps a post-implementation audit found (all opt-in / optional, zero overhead when unused):
  - **Sibling-store erasure** — `SubjectErasable` + `DataEraser.WithSubjectStore`, with built-in `NewAuditSubjectEraser` / `NewSagaSubjectEraser` / `NewSnapshotSubjectEraser` / `NewOutboxSubjectEraser` / `NewIdempotencySubjectEraser` (optional `adapters.SubjectAuditPurger` / `SubjectSagaPurger` / `SubjectOutboxPurger` / `SubjectIdempotencyPurger` on memory + postgres). Reaches PII derived from events — the plaintext audit trail, saga state, snapshots, dead-lettered/decrypted outbox rows, and idempotency response payloads — that crypto-shredding does not touch. An erasure certificate is not `Verified` unless every registered sibling store succeeded
  - **Soft-revoke now shreds** — an elapsed soft-revocation promotes to a permanent key-material wipe (local provider); `encryption.RevocationState` / `StatefulRevocable` + `GetRevocationState` / `SoftRevoke` / `Unrevoke` helpers. `Verify` surfaces a soft-revoked key as `ResidualRecoverable` and refuses to certify it during the grace window
  - **Blast-radius guard** — `WithSharedKeyGuard()` / `AllowSharedKeyRevocation()` (+ `SharedKeyError`): erasing one subject can no longer silently crypto-shred a whole per-tenant key
  - **Accountability & races** — `WithStrictAccountability()` (lost marker/certificate is fatal; `Verified` gated on `MarkerWritten`); `Erase` re-resolves post-revoke and flags `Partial` on a late append; `ErasureResult.Failed()` surfaces non-fatal partial failures (e.g. a Vault key without `deletion_allowed`)
  - **Subject index + backfill** — `SubjectIndexWriter` + `MemorySubjectIndex` and a PostgreSQL `SubjectIndex` (`mink_subject_index` table), `WithSubjectIndexWriter` (append-time) / `WithResolverIndex`, and `BackfillSubjectIndex` — the migration step that makes pre-tagging historical subjects fully resolvable and erasable; turns discovery/erasure O(a subject's events). The PostgreSQL event-store adapter also implements a **drift-free** `SubjectIndexAdapter` (`StreamsBySubject`) that reads the events' own `$subjects` tags in JSONB — no separate table to fall out of sync. Indexes are **explicit-only** (`WithResolverIndex`; `WithAuthoritativeIndex` to assert completeness) — never auto-detected, so a store gaining an index never silently changes resolution away from the completeness-proving scan
  - **Retention policy validation** — `RetentionPolicy.Validate` / `RetentionManager.Validate` / `RetentionReport.Failed()`: a `RedactFields` or `Anonymize` policy with no `Apply` hook (which can never act — go-mink cannot mutate append-only rows) now surfaces a loud error on every `Apply`/`DryRun` instead of a silent `Skipped`
  - **`ReEncryptStream`** returns `(copied, oldKeyIDs, err)`, strips stale encryption markers, and is idempotency-guarded (re-run errors instead of duplicating); documented to erase nothing until the source is retired and old keys revoked
  - **Provider contract** — `providertest.AssertRevokeMakesDecryptFail` enforces revoke→decrypt-fails for local, KMS, and Vault
  - **Subject-scoped field keys** — `FieldEncryptionConfig.resolveKeyID` now falls back to the first `$subjects` tag (recorded by `WithSubjectTagger`) when `Metadata.TenantID` is empty, so events persisted via `SaveAggregate` (which carry an empty `Metadata{}`, hence no `TenantID`) encrypt under a **per-subject** master key and become individually crypto-shreddable — the one tagger now drives both a subject's erasure footprint and its shred key, so they can never drift. Precedence is `TenantID` → first subject tag → `defaultKeyID`. Adds `WithSubjectKeyResolver`, a legibility alias of `WithTenantKeyResolver` (same resolver field; last-applied wins). Backward compatible and zero-overhead when unused: with no resolver or no tagger the behavior is identical to before, explicit `TenantID` still wins, and decryption is untouched — the wrapping key id is read from each event's own metadata, so no migration is required. Complements `WithSharedKeyGuard`: subject keys let callers *avoid* shared keys, while the guard *detects* the ones that remain
- **Audit Logging Middleware** — `AuditMiddleware` writes an immutable, queryable audit trail of every command (who/what/when/outcome) for compliance and forensics
  - `AuditStore` interface (`Append`/`Find`/`Count`/`Cleanup`/`Initialize`/`Close`) with in-memory and PostgreSQL implementations (append-only `mink_audit` table)
  - `AuditConfig` with `SkipCommands`, `IncludeMetadata`, and `FailClosed` (default fail-open, so auditing never breaks command processing); `DefaultAuditConfig(store)`
  - Actor capture via `WithActor`/`ActorFromContext` or a custom `ActorFunc`
  - `AuditQuery` filters by command type, actor, tenant, aggregate, time range, and success, with ordering and pagination
- **GDPR Data Export** — `DataExporter` for right to access / right to data portability (Article 15 & 20)
  - `NewDataExporter()` with `WithExportBatchSize()` and `WithExportLogger()` options
  - `Export()` — collects all matching events into an `ExportResult`
  - `ExportStream()` — streams events via handler callback (memory-efficient for large exports)
  - Two enumeration strategies: stream-based (efficient, explicit stream IDs) and scan-based (filter all events, requires `SubscriptionAdapter`)
  - Crypto-shredding support: events with revoked encryption keys are included as `Redacted=true` with `nil` Data
  - Built-in filters: `FilterByTenantID`, `FilterByUserID`, `FilterByStreamPrefix`, `FilterByMetadata`, `FilterByEventTypes`, `CombineFilters`
  - Time range filtering via `FromTime` / `ToTime` on `ExportRequest`
  - `ExportError` typed error with `Is(ErrExportFailed)` and `Unwrap()` support
  - `EventStore.ProcessStoredEvent()` — public method exposing the decrypt→upcast→deserialize pipeline
- **Resilience controls:** `IdempotencyConfig.FailClosed` (fail commands on idempotency-store outage instead of fail-open), `AsyncOptions.OnPoisonEvent` (skip/dead-letter an event that keeps failing instead of faulting the projection), `WithSagaTimeout` (background sweep that compensates abandoned `Running`/`Compensating` sagas), and `ProjectionEngine.Pause`/`Resume`/`Rebuild` for runtime projection control.
- RC (release candidate) release workflow for `develop` branch (`.github/workflows/rc-release.yml`)
  - Automatic pre-release versioning: `v1.0.4-rc.1`, `v1.0.4-rc.2`, etc.
  - Contextual changelogs (first RC diffs from stable, subsequent RCs diff from previous RC)
  - GitHub pre-release flag and Go module proxy indexing
  - Concurrency control to prevent race conditions on rapid pushes

### Changed
- **Retry-count convention (`ExponentialBackoffRetry` with a non-positive budget) — deliberate behavior change.** `ExponentialBackoffRetry(maxRetries, …)` now treats a **non-positive** `maxRetries` (`0` or negative) as "retry indefinitely," matching the convention `AsyncOptions.MaxRetries` already documented, so the two paths can no longer disagree. Previously `ExponentialBackoffRetry(0, …)` meant "never retry" — the opposite — so a caller writing `ExponentialBackoffRetry(0, base, max)` intending "retry forever with backoff" silently got "give up on the first error," turning one transient blip into a permanent fault. **Blast radius is only callers that explicitly pass a non-positive count to `ExponentialBackoffRetry`**: the default policy uses `3` and any positive count is unaffected. **Migration:** to never retry use `NoRetry()` (the one canonical "never"); to retry forever use the new `RetryForever(base, max)` (or the now correctly-behaving `ExponentialBackoffRetry(0, …)`).
- **`adapters.OutboxAppender` interface (breaking, optional interface):** `AppendWithOutbox` now takes the caller-configured `OutboxStore` as a parameter — `AppendWithOutbox(ctx, streamID, events, expectedVersion, outbox OutboxStore, messages)`. The adapter schedules the messages into that store within its transaction/critical section (transactional adapters via `ScheduleInTx`), so the atomic path writes to the same store the caller reads from. Previously the PostgreSQL adapter scheduled into an internally-derived store with the default table name, diverging from a caller that configured a custom outbox table. The in-memory adapter now implements `OutboxAppender` too (schedule-first, so a version conflict or scheduling failure writes neither events nor messages). Custom adapters implementing `OutboxAppender` must update their method signature.
- **Minimum Go version is now 1.26** (was 1.25). `go.mod` targets `go 1.26.0`, so consumers must build with Go 1.26 or later. Synchronized everywhere the toolchain is declared or documented: the CI build matrix now covers Go 1.26 & 1.27 across Linux/macOS/Windows and the lint job pins Go 1.26 (coverage/benchmark jobs already follow `go-version-file: go.mod`); the README badge and installation note, `CONTRIBUTING.md` prerequisites, `examples/` prerequisites, the tutorial prerequisites and its Dockerfile base image (`golang:1.26-alpine`), and the bug-report issue template. `mink diagnose`'s Go-version check now warns below 1.26 (was 1.21) so it agrees with what the module actually requires.
- CI test workflow now triggers on `develop` branch (push and pull request)
- Stable release workflow uses stable-only tag filter to ignore RC tags when calculating next version

### Fixed
- **Sagas never saw decrypted fields (field encryption bypassed on the saga path)** — `SagaManager` subscribes through the **raw** adapter (`Adapter().SubscribeAll`) rather than through the `EventStore`'s own read path, so unlike every other read surface (`Load`, catch-up subscriptions, inline/async projections, `DataExporter`) it never applied field decryption. Any saga triggered by an event with configured encrypted fields was handed **ciphertext**: since an encrypted field is stored as a base64 string in place of its real shape, the saga's unmarshal of the payload failed on the first encrypted field (`json: cannot unmarshal string into Go struct field …`), the saga failed on its very first step and compensated, and — because the loop logs per-event failures and moves on — the whole saga type silently dead-ended for every affected event with no other symptom. Sagas are now decrypted at the delivery boundary via the same shared `DecryptStoredEvent` primitive, so a saga observes exactly the plaintext a `Load` or a projection does. Also decrypts a **captured trigger event** recovered on re-drive (`RetrySaga` / `ResumeStalled`), so sagas captured by an affected version become retryable rather than permanently stuck. Decryption happens only after the event-type→saga lookup, so an event no saga handles is never decrypted (mirroring the projection engine's decrypt-only-what-is-handled optimization), and a hard, unhandled decryption failure is now reported instead of silently delivering unparseable ciphertext. Zero overhead and byte-for-byte prior behavior when no encryption is configured; a crypto-shredded subject whose `DecryptionErrorHandler` returns `nil` still yields the event as stored, matching every other surface. **Note:** with `WithSagaRetryCapture` enabled the captured event is persisted into saga state, which therefore holds decrypted fields — the same property `SagaState.Data` has always had, and exactly what `NewSagaSubjectEraser` (register via `DataEraser.WithSubjectStore`) exists to purge on erasure.
- **Read model filters (PostgreSQL):** `FilterOpContains` was defined but never handled by the PostgreSQL repository, so a `CONTAINS` filter was silently ignored and the query returned unfiltered rows. It is now implemented as a case-sensitive substring match on text columns (LIKE metacharacters in the value are escaped) and as a JSONB containment check (`@>`) on JSONB columns.
- `FilterOpBetween` now accepts typed slices (`[]int`, `[]float64`, `[]string`, …) in addition to `[]interface{}`, matching the behavior of `FilterOpIn`.
- The PostgreSQL repository now returns an error for an unrecognized filter operator instead of silently dropping the condition.
- Corrected the read-models documentation, which referenced non-existent symbols (`mink.Eq`, `mink.Contains`, `repo.Query`) instead of the real `mink.FilterOp*` constants and `repo.Find`.
- **Data race (PostgreSQL adapter):** the `WithHealthCheck` goroutine read the adapter's `closed` flag on every tick while `Close()` wrote it without synchronization. The flag is now an `atomic.Bool`, removing the race flagged by the race detector.
- **Review correctness fixes (2026-07-03 audit):** a batch of confirmed correctness bugs found by a full-codebase review.
  - **Read-model whole-table `DELETE`:** the query builder silently dropped a filter whose field did not resolve to a column, so a query built only from unknown/misspelled fields produced an empty `WHERE` — turning `DeleteMany` into an unqualified `DELETE FROM table` (and `Find`/`Count` into full scans). Both adapters now return `ErrUnknownFilterField` (`*UnknownFilterFieldError`) for an unresolved field.
  - **Saga reliability:** `WithSagaRetryAttempts` is clamped to ≥1 (0 silently dropped events while advancing the position); a `context.Canceled`/`DeadlineExceeded` during dispatch (graceful `Stop()`) no longer drives compensation on a dead context, leaving the saga `Running` for restart.
  - **Projections:** a transient checkpoint-load error now faults the worker instead of silently restarting from position 0 (which replayed all history into non-idempotent projections); the poison event handed to `OnPoisonEvent` comes from the applied (filtered) batch; live projections log an observable drop instead of silently discarding events for a not-`Running` worker.
  - **Position-based delivery (PostgreSQL):** the shared read path (`LoadFromPosition`, used by `SubscribeAll`/`SubscribeCategory`, async projections, and sagas) now applies a transaction-snapshot safe-watermark, so a committed event whose lower `global_position` transaction commits out of order is never skipped. Previously, under concurrent writers, the `global_position > cursor` poll could advance past an as-yet-uncommitted lower position (and checkpoint past it for projections), silently losing projection updates and saga triggers. No schema change; at-least-once preserved (only at-most-once loss removed); long read-only transactions do not stall it. For guaranteed delivery of must-not-miss side effects use the (gap-free) outbox; a projection rebuild-from-0 recovers any historically-skipped events.
  - **Subscriptions:** the in-memory `SubscribeAll` registers the subscriber atomically with its history snapshot, so an `Append` concurrent with subscription setup is no longer lost; `SubscribeCategory` escapes LIKE metacharacters in the category prefix.
  - **Webhook outbox:** delivery is treated as successful only on a `2xx` status — a `3xx` (e.g. an unfollowed `300`/`304`) or other non-2xx no longer marks a message delivered.
  - **GDPR:** export independently redacts an event encrypted under a revoked key even when a `WithDecryptionErrorHandler` swallows the error (no ciphertext leaks into an Article 15/20 export); a merely-disabled AWS KMS CMK is no longer reported as revoked (`RevokeKey` schedules its deletion); the shared-key blast-radius guard now also covers keys that appear during post-revoke reconciliation; `ErasureResult.Failed()` accounts for skipped sibling stores; re-running `Erase` no longer double-appends the erasure marker.
  - **In-memory adapter:** `Append` deep-copies event `Data` and `Metadata.Custom` so a caller reusing the passed buffer/map cannot corrupt stored events.
  - **CLI:** `mink generate` refuses to overwrite an existing file without `--force`; `mink migrate down` treats a migration-record removal failure as fatal (symmetric with `up`) instead of leaving the schema and migrations table inconsistent.
- **Transparent decryption for projections & subscriptions:** field encryption is now transparent on the projection read path. The async worker, the inline path (`ProcessInlineProjections`), and live delivery all decrypt each event's `Data` before `Apply`/`ApplyBatch`, matching `Load`/`LoadAggregate`/`DataExporter`. Previously projections received raw ciphertext, so an encrypted **scalar** field landed in the read model as base64 and an encrypted **non-scalar** field (array/object) failed the projection's `json.Unmarshal` — silently corrupting read models once encryption was enabled. Adds the exported `EventStore.DecryptStoredEvent` (the `StoredEvent`-returning counterpart to `ProcessStoredEvent`) that every raw-`StoredEvent` delivery surface — projections **and** event subscriptions — routes through, so decryption transparency is defined in one place. Backward compatible, zero overhead when encryption is unconfigured; a crypto-shredded subject (revoked key + a nil-returning `WithDecryptionErrorHandler`) is delivered with its fields left as stored and no error, so the worker advances rather than stalls.
- **Aggregate replay type-safety:** `LoadAggregate` no longer *silently* drops an event whose type wasn't registered. An unregistered type deserializes to the map fallback that an aggregate's concrete-type `ApplyEvent` switch can't match, so the aggregate was silently rebuilt from only its *registered* history (often just constructor defaults) — a data bug that was very hard to trace to a missing `RegisterEvents` call. It now logs a WARN (once per stream/type/version) by default, and **`WithStrictReplay()`** makes `LoadAggregate` return a typed `UnregisteredEventTypeError` (sentinel `ErrUnregisteredEventType`) to fail fast in dev/CI. Adds `EventStore.RegisteredEventTypes()` + `RegisterAggregateEvents(...)` for pre-flight registration checks, read-only `UnregisteredStreamTypes`/`UnregisteredEventTypes` audits (+ a `mink stream types` CLI verb), and an opt-in `WithAutoRegisterOnAppend()`. The lenient map fallback for projections / `DataExporter` / upcasting is unchanged. Additive and backward compatible (the default only adds a log line); zero overhead when unused.
- **Review follow-up hardening (2026-07-03):** fixes for issues found while reviewing the changes above.
  - **Rebuild decryption:** `ProjectionRebuilder` now decrypts field-encrypted events before applying them, so rebuilding a projection over encrypted events no longer writes ciphertext into the read model — the one pull-based delivery surface the transparent-decryption change had missed. A shared `EventStore.decryptStoredEvents` primitive is now the single point the async worker, rebuild, and catch-up subscription all route through.
  - **`DecryptStoredEvent` consistency:** a successfully-decrypted event has its `$encrypted_*` metadata markers cleared (on a copy), so a now-plaintext event is never still flagged encrypted — re-processing it can no longer double-decrypt and fail. A crypto-shred no-op (ciphertext left in place) keeps its markers.
  - **Async worker startup:** a *transient* checkpoint-read error is retried with a bounded backoff before the worker faults, so a momentary checkpoint-store blip no longer permanently downs a projection; a persistent error still faults (never restart-from-0), and a shutdown stops cleanly.
  - **Live projections:** an event that fails to decrypt on the best-effort live path is logged at error with the affected projections (was an unattributed warn), and events no live projection handles are no longer decrypted.
  - **Inline projections:** events no inline projection handles are no longer decrypted (the decrypt-fails-the-append behavior is intentional, consistent with an inline `Apply` error).
  - **Erasure marker idempotency:** the marker's subject is stamped in event metadata (serializer-independent), so re-running `Erase` no longer appends a duplicate marker under a msgpack/protobuf serializer (the previous check `json.Unmarshal`-ed the Data, which only works for JSON).
  - **Export efficiency:** key-revocation status is memoized per key across an export, so a KMS/Vault provider is queried at most once per distinct key instead of once per event.
  - **In-memory `SubscribeAll`:** the historical drain no longer blocks under the read lock (the channel is sized to hold pending history), so subscribing to a large history with a small buffer can no longer deadlock setup and stall writers.
  - **Replay-safety overhead:** the serializer's introspection view is cached once at construction, so the per-event unregistered-type check on `LoadAggregate` no longer does a per-event interface assertion; `UnregisteredEventTypes` documents that it should run against a quiescent store on PostgreSQL (the safe-watermark excludes in-flight events).
  - **Typed errors:** `UnknownFilterFieldError` and `UnregisteredEventTypeError` implement `Unwrap()` (alongside `Is()`), per the project's typed-error convention.
- **Binary serializer + JSONB PostgreSQL adapter now fails fast** instead of erroring cryptically at write time. Because the PostgreSQL adapter's `events.data` column is `JSONB` but `serializer/msgpack` and `serializer/protobuf` emit binary, appending such an event previously failed on the first `Append` with a deferred, opaque driver error (`invalid input syntax for type json`). `mink.New` now detects this incompatible pairing at construction, records it, and returns an actionable error wrapping the new `ErrBinarySerializerUnsupported` sentinel from the first `Append`/`SaveAggregate` — before any write (message directs callers to use the default JSON serializer or an adapter with a `BYTEA` data column). `New` records the incompatibility rather than panicking, honoring the "don't panic on a recoverable configuration error" convention while keeping `New`'s panic-free signature. The guard is zero-cost and backward compatible: it only triggers for a pairing that could never have worked, so the default JSON serializer, the in-memory adapter (which stores raw bytes), and any custom serializer that does not report a binary format are unaffected. Decorators forward `BinaryFormat()` to their wrapped serializer (e.g. `UpcastingSerializer`), so wrapping a binary serializer does not slip past the check.
- **Review follow-up hardening (2026-07-04):** fixes for issues found while reviewing the changes above.
  - **Saga cursor on shutdown:** the `SagaManager` run loop no longer advances its position past an event when the manager is shutting down (context cancelled mid-dispatch). Previously the cursor advanced unconditionally after `processEvent`, so a consumer that persisted and restored `Position()` across a restart could skip the in-flight event and stall the saga; the loop now leaves the cursor on the event so a restart re-delivers it (re-delivery is idempotent via per-saga processed-event tracking).
  - **Erasure marker scan:** `DataEraser` loads the marker stream at most once per instance (caching the set of already-marked subjects) instead of re-scanning the whole, growing marker stream on every `Erase`. A bulk erasure of *M* subjects is now O(*M*) rather than O(*M²*); `appendMarker` keeps the cache current, and a load error is still best-effort (uncached, retried).
- **Read-model NULL-safe reads for nullable scalar columns (PostgreSQL):** the `mink:"...,nullable"` tag was honored on the write/DDL side (the column is emitted without `NOT NULL`) but not on reads. `Find`/`FindOne`/`Get` handed `rows.Scan` the field's own address for every column, so a stored `NULL` in a non-pointer scalar field (`string`, the `int`/`uint` kinds, `float32/64`, `bool`, `time.Time`) failed the whole scan with the driver's opaque `converting NULL to <type> is unsupported` — aborting the *entire* result set, so one `NULL` cell took down the query. A column declared `nullable` is now scanned through an intermediate `sql.Null[T]` holder and coalesced (`NULL` → the field's Go zero value; otherwise the scanned value), so a column the tag promises is nullable is finally safe to read once it holds `NULL` (e.g. after a redaction/erasure blanks it — closing a gap under the Data Governance work). Pointer fields and `[]byte`/JSONB columns are unchanged (they already scan `NULL` → `nil`), and the write path is unchanged (a zero-value scalar still persists as that zero value, never `NULL` — persist a distinguishable `NULL` with a pointer field). The wrap set is computed once at construction, so a read model with no nullable scalar columns takes the identical path as before (zero overhead when unused). Adds the additive typed `*NullColumnError` (sentinel `ErrNullColumn`; names the column/field, wraps the driver error via `Unwrap`), returned when a column **not** tagged `nullable` nonetheless contains `NULL` (an external write or manual migration) — replacing the driver's opaque message with a fix hint, and never silently substituting a value. Because nullable integers/floats coalesce through a wide `int64`/`float64` intermediate, a non-`NULL` value that does not fit the destination field (a value beyond `int8`, or a negative read into an unsigned field — only reachable via an out-of-band write) fails loudly with a typed `*ColumnValueRangeError` (sentinel `ErrColumnValueRange`) instead of silently truncating/wrapping, preserving the fail-loud behavior of a direct `database/sql` scan. No schema change; the in-memory adapter (which stores Go values directly) is unaffected.
### Documentation
- Overhauled the root `README.md`: fixed the broken documentation links (they now point to https://go-mink.dev), expanded the feature overview to cover the GDPR erasure/retention, audit-logging, anonymization, and subject-discovery capabilities, added a "Why Event Sourcing?" section and a Mermaid architecture diagram, an examples index, and a Contributing / Community section. Corrected the required Go version to 1.25+.
- Added a `README.md` to every project under `examples/` (14 new files) plus an `examples/README.md` index with a suggested learning path.
- Refreshed `CONTRIBUTING.md` (Makefile-based workflow, a project-layout map, and good-first-contribution ideas), `.github/SECURITY.md` (accurate supported-versions table and GitHub private vulnerability reporting), `CODE_OF_CONDUCT.md` (real enforcement contact), and the issue/PR templates.

## [1.0.0] - 2026-03-02

First stable release consolidating all features from the development phases.

### Added

#### Saga / Process Manager
- `Saga` interface - Contract for saga/process manager implementations
- `SagaBase` - Embeddable base struct with ID, Type, Status, Version management
- `SagaStatus` enum - Started, Running, Completed, Failed, Compensating, Compensated
- `SagaStepStatus` enum - Pending, InProgress, Completed, Failed, Compensated
- `SagaStep` struct - Represents a step in the saga with name, status, timestamps
- `SagaState` struct - Persisted state including steps, data, correlation ID
- `SagaStore` interface - Abstraction for saga persistence
- `NewSagaBase()` - Create new saga base with ID and type
- `SetStatus()/Status()` - Manage saga status
- `SetCurrentStep()/CurrentStep()` - Track current step
- `SetCorrelationID()/CorrelationID()` - Correlation for distributed tracing
- `StartedAt()/CompletedAt()` - Lifecycle timestamps
- `Data()/SetData()` - Saga-specific state storage
- `IsComplete()` - Check if saga completed successfully
- `HandledEvents()` - Declare events the saga responds to
- `HandleEvent()` - Process events and return commands
- `Compensate()` - Generate compensation commands on failure

#### Saga Manager
- `SagaManager` - Orchestrates saga lifecycle and event processing
- `SagaCorrelation` - Configuration for correlating events to sagas
- `SagaFactory` - Function type for creating saga instances
- `NewSagaManager()` - Create manager with store, subscription adapter, command bus
- `Register()` - Register saga type with factory and correlations
- `Start()/Stop()` - Lifecycle management for event subscription
- `Compensate()` - Manually trigger compensation for a saga
- `Resume()` - Resume a stalled saga
- `WithSagaWorkers()` - Configure number of worker goroutines
- `WithSagaLogger()` - Configure logger for saga operations

#### Saga Idempotency
- `SagaState.ProcessedEvents` - Tracks processed event IDs for at-least-once delivery
- Automatic deduplication of duplicate events (e.g., from pg_notify + polling)
- Transparent handling by `SagaManager` - no user code changes required
- Persisted to PostgreSQL `processed_events JSONB` column

#### Saga Store Implementations
- `memory.NewSagaStore()` - In-memory saga store for testing
- `memory.SagaStore.Save()` - Persist saga state with optimistic concurrency
- `memory.SagaStore.Load()` - Load saga by ID
- `memory.SagaStore.FindByCorrelationID()` - Find saga by correlation
- `memory.SagaStore.FindByType()` - Find sagas by type and status
- `memory.SagaStore.Delete()` - Remove saga state
- `postgres.NewSagaStore()` - PostgreSQL saga store implementation
- `postgres.SagaStore.Initialize()` - Create saga table and indexes
- `postgres.WithSagaSchema()` - Configure PostgreSQL schema
- `postgres.WithSagaTable()` - Configure table name

#### Field-Level Encryption
- `encryption.Provider` interface - Abstraction for key management and crypto operations
- `encryption.DataKey` struct - Holds plaintext + ciphertext of data encryption keys
- `encryption.ClearBytes()` - Securely zero key material after use
- Sentinel errors: `ErrEncryptionFailed`, `ErrDecryptionFailed`, `ErrKeyNotFound`, `ErrKeyRevoked`, `ErrProviderClosed`
- Typed errors: `EncryptionError`, `KeyNotFoundError`, `KeyRevokedError` with `Is()`, `Unwrap()`
- `NewEncryptionError()`, `NewDecryptionError()`, `NewKeyNotFoundError()`, `NewKeyRevokedError()` constructors

#### Local AES-256-GCM Provider (`encryption/local`)
- `local.Provider` - In-memory AES-256-GCM encryption provider for development and testing
- `local.New()` - Create provider with options
- `local.WithKey()` - Pre-register encryption keys
- `local.Provider.AddKey()` - Add keys at runtime
- `local.Provider.RevokeKey()` - Revoke keys for crypto-shredding simulation
- Thread-safe concurrent access with `sync.RWMutex`

#### AWS KMS Provider (`encryption/kms`)
- `kms.Provider` - AWS KMS encryption provider for production
- `kms.KMSClient` interface - Minimal KMS client abstraction (wraps official SDK)
- `kms.New()` - Create provider with options
- `kms.WithKMSClient()` - Inject KMS client
- `GenerateDataKey` uses `AES_256` spec for envelope encryption

#### HashiCorp Vault Transit Provider (`encryption/vault`)
- `vault.Provider` - Vault Transit encryption provider for production
- `vault.VaultClient` interface - Minimal Transit client abstraction
- `vault.New()` - Create provider with options
- `vault.WithVaultClient()` - Inject Vault client
- `GenerateDataKey` generates DEK locally, encrypts via Vault Transit

#### Field Encryption Config (Root Package)
- `FieldEncryptionConfig` - Per-event-type field encryption configuration
- `EncryptionOption` type - Functional options pattern
- `NewFieldEncryptionConfig()` - Create config with options
- `WithEncryptionProvider()` - Set encryption provider
- `WithDefaultKeyID()` - Set default master key ID
- `WithEncryptedFields()` - Register fields to encrypt per event type (dot-path support)
- `WithTenantKeyResolver()` - Per-tenant encryption key mapping
- `WithDecryptionErrorHandler()` - Crypto-shredding handler (graceful degradation)
- `WithFieldEncryption()` - EventStore option to enable field-level encryption
- `GetEncryptedFields()` - Extract encrypted field names from metadata
- `GetEncryptionKeyID()` - Extract key ID from metadata
- `IsEncrypted()` - Check if event has encrypted fields
- Envelope encryption: 1 provider call per event, local AES-256-GCM per field
- Encryption metadata stored in `Metadata.Custom` with `$`-prefixed keys
- Zero overhead when encryption not configured (nil check short-circuit)

#### Encryption Error Aliases (Root Package)
- `mink.ErrEncryptionFailed`, `mink.ErrDecryptionFailed`, `mink.ErrKeyNotFound`, `mink.ErrKeyRevoked`, `mink.ErrProviderClosed`
- Type aliases: `mink.EncryptionError`, `mink.KeyNotFoundError`, `mink.KeyRevokedError`
- Constructor aliases: `mink.NewEncryptionError()`, etc.

#### EventStore Encryption Integration
- `Append()` and `SaveAggregate()` encrypt configured fields before persisting
- `Load()`, `LoadFrom()`, and `LoadAggregate()` decrypt fields transparently
- Decrypt before upcast ordering in the event loading pipeline
- `EventStoreWithOutbox` also supports field encryption in `Append()` and `SaveAggregate()`

#### Encryption Example (`examples/encryption/`)
- Full working example: encrypt on save, decrypt on load
- Per-tenant encryption keys
- Crypto-shredding (GDPR right to erasure) demonstration

#### Event Versioning & Upcasting
- `Upcaster` interface - Transform event data from one schema version to the next
- `UpcasterChain` - Thread-safe registry with gap/duplicate validation
- `NewUpcasterChain()` - Create empty upcaster chain
- `UpcasterChain.Register()` - Register upcaster with version transition validation
- `UpcasterChain.Validate()` - Check chain for contiguous version coverage
- `UpcasterChain.Upcast()` - Apply upcasters in sequence from source to latest version
- `UpcasterChain.HasUpcasters()` - Check if upcasters exist for event type
- `UpcasterChain.LatestVersion()` - Get latest schema version for event type
- `UpcasterChain.RegisteredEventTypes()` - List event types with upcasters
- `GetSchemaVersion()` - Extract schema version from event metadata (defaults to 1)
- `SetSchemaVersion()` - Set schema version in event metadata
- `WithUpcasters()` - EventStore option to configure upcaster chain
- `EventStore.RegisterUpcasters()` - Convenience method to register upcasters
- Automatic upcasting during `Load()`, `LoadFrom()`, and `LoadAggregate()`
- Automatic schema version stamping during `Append()` and `SaveAggregate()`
- Zero overhead when no upcasters configured (nil chain short-circuit)

#### UpcastingSerializer
- `UpcastingSerializer` - Serializer decorator that applies upcasting on deserialize
- `NewUpcastingSerializer()` - Create decorator wrapping any Serializer
- `Serialize()` - Pass-through to inner serializer
- `Deserialize()` - Upcast from DefaultSchemaVersion before deserializing
- `DeserializeWithVersion()` - Upcast from explicit version with metadata context
- `Inner()` - Access the wrapped serializer
- `Chain()` - Access the upcaster chain
- `SerializeEventWithVersion()` - Convenience function to serialize with version stamp

#### Schema Registry
- `SchemaRegistry` - In-memory registry for event schema definitions
- `NewSchemaRegistry()` - Create empty schema registry
- `SchemaRegistry.Register()` - Register schema definition for event type and version
- `SchemaRegistry.GetSchema()` - Retrieve specific schema version
- `SchemaRegistry.GetLatestVersion()` - Get highest registered version
- `SchemaRegistry.CheckCompatibility()` - Compare schema versions
- `SchemaRegistry.RegisteredEventTypes()` - List event types with schemas
- `SchemaCompatibility` enum - FullyCompatible, BackwardCompatible, ForwardCompatible, Breaking
- `SchemaDefinition` - Schema metadata with version, fields, and optional JSON Schema
- `FieldDefinition` - Field metadata with name, type, and required flag

#### Versioning Errors
- `ErrUpcastFailed` - Sentinel error for upcasting failures
- `ErrSchemaVersionGap` - Sentinel error for gaps in upcaster chain
- `ErrIncompatibleSchema` - Sentinel error for schema incompatibility
- `ErrSchemaNotFound` - Sentinel error for missing schema
- `UpcastError` - Typed error with EventType, FromVersion, ToVersion, Cause
- `SchemaVersionGapError` - Typed error with EventType, MissingVersion, ExpectedVersion
- `IncompatibleSchemaError` - Typed error with EventType, versions, Compatibility, Reason

#### Saga Testing Utilities (`testing/sagas`)
- `MinkSagaAdapter` - Adapter to use mink sagas with test fixtures
- `NewMinkSagaAdapter()` - Create adapter from mink.Saga
- `TestSaga()` - Create saga test fixture
- `TestCompensation()` - Test compensation flows
- `GivenEvents()` - Set up triggering events
- `ThenCommands()` - Assert commands issued by saga
- `ThenCompleted()` - Assert saga completion
- `ThenNotCompleted()` - Assert saga still in progress
- `ThenState()` - Assert saga state
- `ThenCompensates()` - Assert compensation commands

## [0.4.0] - 2026-01-03

### Added

#### Testing Utilities - BDD Package (`testing/bdd`)
- `TestFixture` - BDD-style test fixture for aggregate testing
- `CommandTestFixture` - Test fixture for command bus integration
- `Given()` - Set up initial events for test
- `When()` - Execute command or method
- `Then()` - Assert expected events
- `ThenError()` - Assert expected error
- `ThenNoEvents()` - Assert no events emitted

#### Testing Utilities - Assertions Package (`testing/assertions`)
- `AssertEventTypes()` - Assert event types match expected
- `AssertEventData()` - Assert event data matches expected
- `DiffEvents()` - Compute differences between event slices
- `FormatDiffs()` - Format diff results for display
- `EventMatcher` - Fluent interface for event matching
- `MatchEventType()` - Match single event type
- `MatchEvent()` - Match event with data
- `FilterEvents()` - Filter events by predicate

#### Testing Utilities - Projections Package (`testing/projections`)
- `ProjectionTestFixture[T]` - Generic projection test fixture
- `InlineProjectionFixture` - Test inline projections
- `AsyncProjectionFixture` - Test async projections
- `LiveProjectionFixture` - Test live projections with channels
- `EngineTestFixture` - Test full projection engine
- `GivenEvents()` - Set up events for projection
- `GivenDomainEvents()` - Set up domain events with serialization
- `ThenReadModel()` - Assert read model state
- `ThenReadModelExists()` - Assert read model exists
- `ThenReadModelCount()` - Assert read model count
- `ThenReadModelMatches()` - Assert read model with custom predicate

#### Testing Utilities - Sagas Package (`testing/sagas`)
- `Saga` interface - Saga/Process Manager contract
- `SagaTestFixture` - Test fixture for saga testing
- `SagaStateMachineFixture` - Test saga state transitions
- `CompensationFixture` - Test compensation flows
- `TimeoutFixture` - Test saga timeout handling
- `TestSaga()` - Create saga test fixture
- `GivenEvents()` - Set up triggering events
- `ThenCommands()` - Assert commands issued
- `ThenCompleted()` - Assert saga completion
- `ThenState()` - Assert saga state
- `ThenCompensates()` - Assert compensation commands

#### Testing Utilities - Containers Package (`testing/containers`)
- `PostgresContainer` - PostgreSQL test container management
- `StartPostgres()` - Start PostgreSQL container for tests
- `IntegrationTest` - Full integration test environment
- `FullStackTest` - Complete mink stack test environment
- `ConnectionString()` - Get database connection string
- `CreateSchema()` - Create isolated test schema
- `DropSchema()` - Clean up test schema
- `SetupMinkSchema()` - Initialize mink tables

#### Serializers - MessagePack (`serializer/msgpack`)
- `Serializer` - MessagePack serializer implementation
- `NewSerializer()` - Create new MessagePack serializer
- `NewSerializerWithOptions()` - Create with options
- `WithRegistry()` - Pre-configure type registry
- `Register()` - Register event type
- `RegisterAll()` - Register multiple event types
- `Serialize()` - Convert event to MessagePack bytes
- `Deserialize()` - Convert bytes back to event
- `SerializationError` - Detailed serialization errors

#### Middleware - Tracing (`middleware/tracing`)
- `Tracer` - OpenTelemetry tracer wrapper
- `NewTracer()` - Create tracer with options
- `WithTracerProvider()` - Custom TracerProvider
- `WithServiceName()` - Set service name for spans
- `CommandMiddleware()` - Trace command execution
- `EventStoreMiddleware` - Trace event store operations
- `ProjectionMiddleware` - Trace projection processing
- `SpanFromContext()` - Get current span
- `AddEvent()` - Add event to current span
- `SetError()` - Set error on current span
- `SetAttributes()` - Set attributes on current span

#### Middleware - Metrics (`middleware/metrics`)
- `Metrics` - Prometheus metrics collection
- `New()` - Create metrics with options
- `WithNamespace()` - Set Prometheus namespace
- `WithSubsystem()` - Set Prometheus subsystem
- `WithMetricsServiceName()` - Set service name label
- `CommandMiddleware()` - Record command metrics
- `WrapEventStore()` - Wrap event store with metrics
- `WrapProjection()` - Wrap projection with metrics
- `Collectors()` - Get all Prometheus collectors
- `MustRegister()` - Register with default registry
- `Register()` - Register with custom registry
- `RecordProjectionLag()` - Record projection lag
- `RecordProjectionCheckpoint()` - Record checkpoint position
- `RecordError()` - Record custom error

#### Prometheus Metrics Collected
- `mink_commands_total` - Command execution count by type/status
- `mink_command_duration_seconds` - Command execution duration histogram
- `mink_commands_in_flight` - Currently executing commands gauge
- `mink_eventstore_operations_total` - Event store operations by type/status
- `mink_eventstore_operation_duration_seconds` - Event store operation duration
- `mink_events_appended_total` - Events appended by type
- `mink_events_loaded_total` - Events loaded count
- `mink_projections_processed_total` - Projection events by name/type/status
- `mink_projection_duration_seconds` - Projection processing duration
- `mink_projection_lag_events` - Projection lag gauge
- `mink_projection_checkpoint_position` - Checkpoint position gauge
- `mink_errors_total` - Error count by type

### Changed
- Version updated to 0.4.0

## [0.3.0] - 2025-12-15

### Added

#### Projection System
- `Projection` interface - Base interface for all projection types
- `InlineProjection` interface - Synchronous projections in same transaction
- `AsyncProjection` interface - Background projections with checkpointing
- `LiveProjection` interface - Real-time projections with change notifications
- `ProjectionBase` - Embeddable base struct with name and event filtering
- `AsyncProjectionBase` - Base for async projections with batch support
- `LiveProjectionBase` - Base for live projections with update channels
- `ProjectionState` enum - NotStarted, Running, Paused, Stopped, Faulted
- `ProjectionStatus` - Runtime status with position, lag, and error info
- `CheckpointStore` interface - Checkpoint persistence abstraction

#### Projection Engine
- `ProjectionEngine` - Central orchestrator for all projection types
- `RegisterInline()` - Register synchronous projections
- `RegisterAsync()` - Register background projections with options
- `RegisterLive()` - Register real-time projections
- `Start()/Stop()` - Lifecycle management
- `ProcessInlineProjections()` - Manual inline processing trigger
- `NotifyLiveProjections()` - Send events to live projections
- `GetStatus()/GetAllStatuses()` - Query projection health
- `WithCheckpointStore()` - Engine configuration option
- `AsyncOptions` - Configure batch size, interval, workers

#### Read Model Repository
- `ReadModelRepository[T]` interface - Generic read model storage
- `InMemoryRepository[T]` - In-memory implementation for testing
- `Insert()/Get()/Update()/Delete()` - CRUD operations
- `Query()/FindOne()` - Query with filters
- `Count()/Exists()` - Aggregate queries
- `GetAll()/Clear()` - Bulk operations

#### Query Builder
- `Query` struct - Fluent query construction
- `Where()` - Add filter conditions
- `And()` - Combine multiple filters
- `OrderByAsc()/OrderByDesc()` - Sorting
- `WithLimit()/WithOffset()` - Pagination
- `WithPagination()` - Combined limit/offset
- `Filter` struct with operators (Eq, NotEq, Gt, Gte, Lt, Lte, In, Contains)

#### Subscription System
- `Subscription` interface - Event subscription abstraction
- `SubscriptionOptions` - Configure from position, filters, buffer size
- `EventFilter` interface - Filter events in subscriptions
- `EventTypeFilter` - Filter by event type(s)
- `CategoryFilter` - Filter by stream category
- `CompositeFilter` - Combine multiple filters (AND logic)
- `CatchupSubscription` - Subscribe with catch-up from position
- `PollingSubscription` - Poll-based subscription for adapters without push

#### Projection Rebuilding
- `ProjectionRebuilder` - Rebuild projections from event log
- `Rebuild()` - Single projection rebuild
- `RebuildAll()` - Rebuild all projections
- `RebuildProgress` - Track rebuild progress with callbacks
- `RebuildOptions` - Configure batch size, parallelism
- `ParallelRebuilder` - Concurrent multi-projection rebuilding
- `Clearable` interface - Projections that can be cleared before rebuild

#### Retry Policy
- `RetryPolicy` interface - Customizable retry behavior
- `ExponentialBackoffRetry` - Exponential backoff with jitter
- Configurable initial delay, max delay, max attempts

#### Adapters
- `memory.NewCheckpointStore()` - In-memory checkpoint storage
- `postgres.LoadFromPosition()` - Load all events from global position
- `postgres.SubscribeAll()` - Subscribe to all events
- `postgres.SubscribeStream()` - Subscribe to specific stream
- `postgres.SubscribeCategory()` - Subscribe to stream category
- `adapters.CheckpointAdapter` interface - Checkpoint storage contract
- `adapters.SubscriptionAdapter` interface - Subscription capabilities contract

#### Errors
- `ErrNilProjection` - Nil projection registration attempt
- `ErrEmptyProjectionName` - Empty projection name
- `ErrProjectionNotFound` - Projection lookup failure
- `ErrProjectionAlreadyRegistered` - Duplicate projection name
- `ErrProjectionEngineAlreadyRunning` - Double start attempt
- `ErrProjectionEngineStopped` - Operation on stopped engine
- `ErrNoCheckpointStore` - Async projection without checkpoint store
- `ErrNotImplemented` - Feature not implemented
- `ErrProjectionFailed` - Projection processing failure
- `ProjectionError` - Detailed error with projection name and event info

### Changed
- Version updated to 0.3.0

## [0.2.0] - 2025-01-XX

### Added

#### Command Bus
- `CommandBus` - Routes commands to handlers with middleware support
- `Command` interface - Represents intent to change state
- `CommandBase` - Embeddable base struct with correlation/causation/tenant tracking
- `CommandResult` - Structured result from command execution
- `CommandHandler` interface - Type-safe command handling
- `CommandHandlerFunc` - Function-based command handlers

#### Generic Handlers
- `NewGenericHandler[T]()` - Type-safe generic command handler
- `NewAggregateHandler[C, A]()` - Combined load/handle/save for aggregates

#### Middleware Pipeline
- `ValidationMiddleware()` - Calls `cmd.Validate()` before handling
- `RecoveryMiddleware()` - Catches panics, returns `PanicError` with command data
- `LoggingMiddleware(logger)` - Logs command start/end with timing
- `MetricsMiddleware(metrics)` - Records command count, duration, errors
- `TimeoutMiddleware(duration)` - Adds context timeout
- `RetryMiddleware(attempts, delay)` - Retries on transient failures
- `CorrelationIDMiddleware(generator)` - Sets/generates correlation ID
- `CausationIDMiddleware()` - Tracks event causation chain
- `TenantMiddleware(resolver)` - Multi-tenancy support
- `IdempotencyMiddleware(config)` - Prevents duplicate command processing

#### Idempotency
- `IdempotencyStore` interface - Storage for idempotency records
- `IdempotencyConfig` - Configuration for idempotency middleware
- `GenerateIdempotencyKey()` - Deterministic key generation from command content
- `DefaultIdempotencyConfig()` - Sensible defaults for idempotency
- `IdempotentCommand` interface - Commands with custom idempotency keys

#### Adapters
- `memory.NewIdempotencyStore()` - In-memory idempotency store for testing
- `postgres.NewIdempotencyStore()` - PostgreSQL idempotency store with expiration

#### Errors
- `ValidationError` - Structured validation error with field info
- `PanicError` - Captures panic with stack trace and command data
- `ErrHandlerNotFound` - Sentinel error for missing handlers
- `ErrCommandValidation` - Sentinel error for validation failures

### Changed
- `CommandBase` now has private fields with getter/setter methods
- Idempotency key fallback uses deterministic hash instead of timestamp

### Fixed
- Race condition in memory idempotency store `Close()` method
- JSON validation in PostgreSQL idempotency store `Get()` method

## [0.1.0] - 2025-01-XX

### Added

#### Event Store
- `EventStore` - Core event store implementation
- `EventStoreAdapter` interface - Pluggable storage backends
- `Append()` - Store events with optimistic concurrency
- `Load()` - Load events from a stream
- `SaveAggregate()` - Persist aggregate events
- `LoadAggregate()` - Reconstitute aggregate from events

#### Event Types
- `EventData` - Event to be stored
- `StoredEvent` - Persisted event with metadata
- `Metadata` - Event context (correlation, causation, tenant, user)

#### Version Constants
- `AnyVersion` (-1) - Skip version check
- `NoStream` (0) - Stream must not exist
- `StreamExists` (-2) - Stream must exist

#### Aggregates
- `Aggregate` interface - Event-sourced aggregate contract
- `AggregateBase` - Default aggregate implementation
- `Apply()` - Record uncommitted event

#### Adapters
- `postgres.NewAdapter()` - PostgreSQL event store adapter
- `postgres.Initialize()` - Schema initialization
- `memory.NewAdapter()` - In-memory adapter for testing

#### Serialization
- `JSONSerializer` - JSON event serialization
- `EventRegistry` - Type registration for deserialization

#### Errors
- `ErrConcurrencyConflict` - Optimistic concurrency failure
- `ErrStreamNotFound` - Stream does not exist
- `ConcurrencyError` - Detailed concurrency error info

[Unreleased]: https://github.com/AshkanYarmoradi/go-mink/compare/v1.0.0...HEAD
[1.0.0]: https://github.com/AshkanYarmoradi/go-mink/compare/v0.4.0...v1.0.0
[0.4.0]: https://github.com/AshkanYarmoradi/go-mink/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/AshkanYarmoradi/go-mink/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/AshkanYarmoradi/go-mink/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/AshkanYarmoradi/go-mink/releases/tag/v0.1.0
