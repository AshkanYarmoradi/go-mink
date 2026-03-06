# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
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
- RC (release candidate) release workflow for `develop` branch (`.github/workflows/rc-release.yml`)
  - Automatic pre-release versioning: `v1.0.4-rc.1`, `v1.0.4-rc.2`, etc.
  - Contextual changelogs (first RC diffs from stable, subsequent RCs diff from previous RC)
  - GitHub pre-release flag and Go module proxy indexing
  - Concurrency control to prevent race conditions on rapid pushes

### Changed
- CI test workflow now triggers on `develop` branch (push and pull request)
- Stable release workflow uses stable-only tag filter to ignore RC tags when calculating next version

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
