# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- **Serializer/adapter compatibility guard** — two optional interfaces let a storage adapter and a serializer declare the on-disk data format they require/produce, so an incompatible pairing is caught up front: `adapters.JSONDataAdapter` (`RequiresJSONData() bool`, implemented by the PostgreSQL adapter, whose `events.data` column is `JSONB`) and `mink.BinaryFormatReporter` (`BinaryFormat() bool`, implemented by the shipped `serializer/msgpack` and `serializer/protobuf`). Both are opt-in and structural — custom serializers and adapters that implement neither behave exactly as before. See the matching entry under Fixed.
- **GDPR Right to Erasure (Article 17) & Retention** — completes the data-governance story alongside `DataExporter`:
  - `encryption.Revocable` (`RevokeKey`/`IsRevoked`) — portable crypto-shredding, implemented for local/KMS/Vault via optional client sub-interfaces (base injected clients unchanged); `encryption.RecoverableRevocable` adds soft-revoke with a grace window
  - `DataEraser` — one-call subject erasure: revoke keys, redact read models (`SubjectRedactable` in-place hook or `ReadModelRebuilder`), run external-PII `WithErasureHook`s, append an optional `ErasureMarker`, and emit a verified `ErasureCertificate`; idempotent, with a partial-failure result
  - `SubjectResolver` + `WithSubjectTagger` — resolve a subject's complete cross-stream footprint (optional `SubjectIndexAdapter`, scan fallback); completeness is never silently partial (`Partial` flag)
  - `RetentionManager` + `RetentionPolicy` — schedulable sweep (Shred / RedactFields / Anonymize) with dry-run and a report
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
- **`adapters.OutboxAppender` interface (breaking, optional interface):** `AppendWithOutbox` now takes the caller-configured `OutboxStore` as a parameter — `AppendWithOutbox(ctx, streamID, events, expectedVersion, outbox OutboxStore, messages)`. The adapter schedules the messages into that store within its transaction/critical section (transactional adapters via `ScheduleInTx`), so the atomic path writes to the same store the caller reads from. Previously the PostgreSQL adapter scheduled into an internally-derived store with the default table name, diverging from a caller that configured a custom outbox table. The in-memory adapter now implements `OutboxAppender` too (schedule-first, so a version conflict or scheduling failure writes neither events nor messages). Custom adapters implementing `OutboxAppender` must update their method signature.
- CI test workflow now triggers on `develop` branch (push and pull request)
- Stable release workflow uses stable-only tag filter to ignore RC tags when calculating next version

### Fixed
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
