# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

[Unreleased]: https://github.com/AshkanYarmoradi/go-mink/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/AshkanYarmoradi/go-mink/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/AshkanYarmoradi/go-mink/releases/tag/v0.1.0
