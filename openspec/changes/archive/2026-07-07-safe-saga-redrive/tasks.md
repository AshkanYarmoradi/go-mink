## 1. Required — Safe re-drive primitive (`saga-orchestration`)

- [x] 1.1 Add `ErrSagaNotRetryable` (typed sentinel, with `Is`/`Unwrap`) alongside `ErrSagaNotFound` / `ErrConcurrencyConflict`; add `SagaState.IsRetryable()` returning true for exactly `Failed` / `Compensated` / `CompensationFailed` (mirrors `IsTerminal()`).
- [x] 1.2 Capture the raw last trigger event into `SagaState.Data` under the reserved key `__mink_last_event` in the existing `saveSaga` path (single map write; documented reserved key). No schema change; skip if the event is nil.
- [x] 1.3 Extract the loop's per-saga drive into a private `redrive(ctx, saga, event) error` (dispatch commands → completion/compensation) shared by the event loop and retry, so retry reuses the exact same semantics.
- [x] 1.4 Implement `RetrySaga(ctx, sagaID)`: `Load` → reject non-retryable (`ErrSagaNotRetryable`) / missing (`ErrSagaNotFound`) → acquire `getSagaLock(sagaID)` → **fresh reload under lock** → read `__mink_last_event` (reject with reason if absent) → remove that event's key from `ProcessedEvents` (leave earlier keys) → set `Running`, clear `FailureReason` → `redrive` → `Save` under the loaded `Version` (surface `ErrConcurrencyConflict`, do not internally loop).
- [x] 1.5 Idempotency: on re-drive, only the last event is re-processed; earlier `ProcessedEvents` entries still short-circuit `eventAlreadyProcessed`, so steps 0..N-1 are not re-dispatched.
- [x] 1.6 Observability: append a `SagaStep` marking the re-drive (from-status → outcome); add `WithSagaRetryObserver(func(RetryEvent)) SagaManagerOption` invoked per attempt (id, type, from-status, at, err). Record the `SagaStep` even with no observer.
- [x] 1.7 Doc comments on all new public APIs: the retryable-status matrix, the idempotency contract (handlers must be idempotent), the reserved `Data` key, and that a concurrent change surfaces `ErrConcurrencyConflict`.
- [x] 1.8 Table-driven tests (`saga_retry_test.go`, in-memory saga store): `Failed`→`Completed` re-drive (`TestRetrySaga_ReDrivesFailedToCompleted`); idempotent — earlier steps not re-dispatched (`...IsIdempotentAcrossSteps`); `Completed`/`Running`/`Compensating` rejected with `ErrSagaNotRetryable` (`...RejectsNonRetryableStatuses`); missing last event rejected with reason (`...RejectsMissingTriggerEvent`); concurrent change ⇒ `ErrConcurrencyConflict` (`...SurfacesConcurrencyConflict`); per-saga lock serialises against the loop (`...SerialisesWithEventLoop`); observer sees the attempt + `SagaStep` written without an observer (`...IsObservable`).

## 2. Good-to-have — Stalled resume + batch (`saga-orchestration`)

- [x] 2.1 `ResumeStalled(ctx, sagaID)`: accept only `Running`; require `UpdatedAt` older than the configured `sagaTimeout` (or a caller threshold); else `ErrSagaNotRetryable`. Reuse the `RetrySaga` machinery (lock / OCC / idempotency / observer).
- [x] 2.2 `RetrySagasByType(ctx, sagaType, statuses...) (RetryReport, error)`: `FindByType` → `RetrySaga` each sequentially under its own lock; a per-saga failure does not abort the batch; return a per-saga `RetryReport` (succeeded / failed-again / conflicted / skipped).
- [x] 2.3 Tests: stalled `Running` resumed vs recently-updated rejected (`TestResumeStalled_*`); batch reports per-saga outcomes and continues past one failure (`TestRetrySagasByType_*`).

## 3. Docs, changelog & validation

- [x] 3.1 `CHANGELOG` `[Unreleased]`: add the operator-initiated safe saga re-drive (`RetrySaga` + optional `ResumeStalled` / batch + retry observer), noting it is additive/opt-in with no schema change.
- [x] 3.2 Note saga re-drive on the saga line in `website/docs/roadmap.md`.
- [x] 3.3 `gofmt` + `go vet ./...` clean; `go test ./...` green (root incl. saga e2e + all adapters); `openspec validate safe-saga-redrive --strict` passes.
