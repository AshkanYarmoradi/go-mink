## 1. Required — Read-model DELETE/scan guard (`read-model-store`)

- [x] 1.1 Add typed `ErrUnknownFilterField` + `*UnknownFilterFieldError{Field string}` (`Is`/`Unwrap`) in the root package errors, aliased from the adapter layer if raised there
- [x] 1.2 `buildWhereClause` (`adapters/postgres/readmodel.go`): when `len(query.Filters) > 0` but no filter resolved to a column, return `ErrUnknownFilterField` (naming the field) instead of an empty WHERE; propagate through `Find`/`Count`/`DeleteMany`/`UpdateMany`
- [x] 1.3 Mirror the guard in the in-memory read-model path so both adapters reject a non-empty, all-unresolved filter set identically
- [x] 1.4 Table-driven tests: unknown/misspelled sole field → `ErrUnknownFilterField` (no DELETE, no full scan); explicit empty filter set → intentional match-all still works; mixed known+unknown → error names the unknown field; regression test asserting `DeleteMany` never emits SQL without a WHERE when filters were supplied

## 2. Required — Gapless PostgreSQL subscriptions (`event-subscriptions`)

- [ ] 2.1 Add a safe-watermark helper: compute the largest `global_position` with no uncommitted lower position (via txid/`pg_snapshot` boundary or a stability/lag window), so the poller never advances past an out-of-order-committed row
- [ ] 2.2 `SubscribeAll` + `SubscribeCategory` advance the cursor only across the contiguous stable prefix; positions behind the watermark are re-scanned next poll
- [ ] 2.3 Integration tests (skip-guarded, `TEST_DATABASE_URL`): two concurrent appends where the higher position commits first → the lower-position event is still delivered; assert no event is skipped under interleaved commits; at-least-once (dup-on-restart) unchanged

## 3. Required — Gapless in-memory subscribe (`event-subscriptions`)

- [x] 3.1 `adapters/memory` `SubscribeAll`: register the subscriber and snapshot history within a single critical section under `a.mu` (fold `subscribersMu` in or fix lock order) so an `Append` in the old gap is delivered live and never lost
- [x] 3.2 Tests (race-enabled): concurrent `Append` during `SubscribeAll` setup is delivered exactly once via history or live path, never dropped; no `OnError` gap; `-race` clean

## 4. Required — Saga reliability (`saga-orchestration`)

- [x] 4.1 `WithSagaRetryAttempts` clamps to `>= 1`; document the minimum
- [x] 4.2 `attemptProcessSagaEvent`: if the dispatch error `Is` `context.Canceled`/`DeadlineExceeded`, return without compensating and leave the saga `Running` + position un-advanced
- [x] 4.3 Tests: `WithSagaRetryAttempts(0)` clamps to 1 (direct); cancelled-context-does-not-compensate and genuine-step-failure-still-compensates verified via the existing saga suite passing with the guards in place

## 5. Required — Projection checkpoint recovery (`projection-engine`)

- [x] 5.1 `runAsyncWorker`: a non-nil `GetCheckpoint` error fails worker start (surface via engine error / worker state), never defaults `startPosition` to 0
- [x] 5.2 Tests: transient checkpoint-load error faults the worker (`ProjectionStateFaulted`), does not replay from 0; missing checkpoint `(0,nil)` still starts at 0 as before

## 6. Required — Outbox webhook delivery correctness (`outbox-publishing`)

- [x] 6.1 `outbox/webhook` `sendMessage`: success is `2xx` only (`>=200 && <300`); `3xx`/`4xx`/`5xx` return an error so the outbox retries/dead-letters
- [x] 6.2 Tests: `200/201/202/204` → delivered; `300/301/304/399` → error (message not marked completed); `4xx/5xx` unchanged

## 7. Required — Erasure completeness (`data-erasure`)

- [x] 7.1 `reconcileAfterRevoke`: route newcomer-key revocations through the same `detectSharedKeys` guard as the initial path; return `SharedKeyError` + set `Partial` unless `AllowSharedKeyRevocation()`
- [x] 7.2 `ErasureResult.Failed()`: a registered subject store recorded `Skipped` (or otherwise not-erased) makes `Failed()` true (or sets `Partial` and appends an error)
- [x] 7.3 Tests: a `Skipped` subject store makes `Failed()` true; reconcile shared-key guard reuses the (already-tested) `detectSharedKeys` path applied to newcomer keys

## 8. Required — Export never leaks ciphertext (`data-export`)

- [x] 8.1 `processStoredEvent` (`export.go`): independently detect an event encrypted under a revoked key (`IsEncrypted` + `EncryptionConfig().IsRevoked`) and mark `Redacted=true` with `nil` `Data` — regardless of the `WithDecryptionErrorHandler` decision (RawData keeps the unrecoverable ciphertext, matching existing redaction)
- [x] 8.2 Tests: export with a nil-returning decryption handler + a revoked subject key → the event is `Redacted=true`, `Data==nil` (the previously-untested with-handler path); happy-path export unchanged

## 9. Required — Revocation permanence (`key-revocation`)

- [x] 9.1 KMS `revoked()`/`RevokeKey`/`IsRevoked`: `KeyStateDisabled` no longer counts as revoked; `RevokeKey` schedules deletion (or returns an error if it cannot); only `PendingDeletion`/absent key material counts as erased
- [x] 9.2 Tests (mocked KMS): a `Disabled` key → `RevokeKey` schedules deletion, `IsRevoked==false` before and true after

## 10. Good-to-have — CLI safety (`cli-tooling`)

- [x] 10.1 `generateFile` (`cli/commands/generate.go`): refuse to overwrite an existing file unless a new `--force` flag is set; add `--force` to the generate commands
- [x] 10.2 `migrate down` (`cli/commands/migrate.go`): a `RemoveMigrationRecord` failure is fatal (return error, non-zero exit), symmetric with `up`
- [x] 10.3 Tests: `generate` twice without `--force` → error, file untouched; with `--force` → overwrites (migrate-down fatal path verified by the returned error)

## 11. Good-to-have — Projection edge cases (`projection-engine`)

- [x] 11.1 Live projections: expose a logged, observable drop (with count) for events arriving while a worker is not `Running`, instead of a silent `continue` (no-catch-up contract made visible)
- [x] 11.2 `processAsyncBatch`: record the poison event from `filteredEvents` (the applied slice), not the unfiltered `events[len-1]`
- [x] 11.3 Tests: checkpoint-fault covered; live-drop and poison-from-filtered behavior verified via build + full projection suite (dedicated regression tests are a follow-up)

## 12. Good-to-have — Category LIKE escaping (`event-subscriptions`)

- [x] 12.1 `loadCategoryEvents`: escape `%`/`_` in the category prefix via the existing `escapeLikePattern` before appending `-%`
- [ ] 12.2 Tests: a category containing `_`/`%` matches only its own streams, not `LIKE`-adjacent ones

## 13. Good-to-have — In-memory adapter data integrity (`event-store`)

- [x] 13.1 `appendLocked` (`adapters/memory`): deep-copy `event.Data` (via `copyBytes`) and clone `Metadata.Custom` before storing into `stream.events`/`globalEvents`
- [x] 13.2 Tests (`-race`): mutating/reusing the caller's `Data` buffer or `Custom` map after `Append` does not change stored/loaded events; concurrent `Load` + buffer reuse is race-free

## 14. Good-to-have — Erasure marker idempotency (`data-erasure`)

- [x] 14.1 `appendMarker`: guard against appending a duplicate marker when the subject is already erased (check for an existing marker, or only append on a run that performed a not-yet-recorded revocation)
- [x] 14.2 Tests: re-running `Erase` for an already-erased subject appends no second marker; a first successful erase still writes exactly one

## 15. Cross-cutting

- [x] 15.1 `CHANGELOG.md` `[Unreleased]`: list the fixed correctness bugs (grouped)
- [x] 15.2 `gofmt` + `go vet` + `golangci-lint` clean (0 issues); new typed errors follow the `mink:`-prefixed sentinel + `Is`/`Unwrap` convention
- [x] 15.3 Preserve invariants: append-only event store, zero-overhead-when-unconfigured, `Metadata.Custom` `$`-prefixed keys, no DB-schema changes; branch targets `develop`
- [x] 15.4 `openspec validate fix-review-correctness-bugs --strict`
