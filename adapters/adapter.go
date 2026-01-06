// Package adapters provides interfaces for event store backends.
package adapters

import (
	"context"
	"errors"
	"time"
)

// Sentinel errors for adapter implementations.
// Adapters should return these (or errors that match via errors.Is)
// to enable consistent error handling across different backends.
var (
	// ErrConcurrencyConflict is returned when optimistic concurrency check fails.
	ErrConcurrencyConflict = errors.New("mink: concurrency conflict")

	// ErrStreamNotFound is returned when a stream does not exist.
	ErrStreamNotFound = errors.New("mink: stream not found")

	// ErrEmptyStreamID is returned when an empty stream ID is provided.
	ErrEmptyStreamID = errors.New("mink: stream ID is required")

	// ErrNoEvents is returned when attempting to append zero events.
	ErrNoEvents = errors.New("mink: no events to append")

	// ErrInvalidVersion is returned when an invalid version is specified.
	ErrInvalidVersion = errors.New("mink: invalid version")

	// ErrAdapterClosed is returned when operations are attempted on a closed adapter.
	ErrAdapterClosed = errors.New("mink: adapter is closed")
)

// Metadata contains event context for tracing and multi-tenancy.
// These fields are preserved across serialization and can be used
// for correlation, audit trails, and multi-tenant isolation.
type Metadata struct {
	// CorrelationID links related events across services.
	CorrelationID string `json:"correlationId,omitempty"`

	// CausationID identifies the event that caused this event.
	CausationID string `json:"causationId,omitempty"`

	// UserID identifies who triggered this event.
	UserID string `json:"userId,omitempty"`

	// TenantID for multi-tenant applications.
	TenantID string `json:"tenantId,omitempty"`

	// Custom holds any additional metadata.
	Custom map[string]string `json:"custom,omitempty"`
}

// StoredEvent represents a persisted event with its storage metadata.
// This is returned when loading events from the store.
type StoredEvent struct {
	// ID is the unique event identifier.
	ID string

	// StreamID is the stream this event belongs to.
	StreamID string

	// Type is the event type identifier.
	Type string

	// Data is the serialized event payload.
	Data []byte

	// Metadata contains contextual information.
	Metadata Metadata

	// Version is the position within the stream (1-based).
	Version int64

	// GlobalPosition is the global ordering position across all streams.
	GlobalPosition uint64

	// Timestamp is when the event was stored.
	Timestamp time.Time
}

// StreamInfo contains metadata about an event stream.
type StreamInfo struct {
	// StreamID is the stream identifier.
	StreamID string

	// Category is the aggregate type (first part of stream ID).
	Category string

	// Version is the current stream version.
	Version int64

	// EventCount is the number of events in the stream.
	EventCount int64

	// CreatedAt is when the first event was stored.
	CreatedAt time.Time

	// UpdatedAt is when the last event was stored.
	UpdatedAt time.Time
}

// EventRecord represents an event to be appended to a stream.
// This is the adapter-level representation of an event.
type EventRecord struct {
	// Type is the event type identifier.
	Type string

	// Data is the serialized event payload.
	Data []byte

	// Metadata contains optional contextual information.
	Metadata Metadata
}

// EventStoreAdapter is the interface that database adapters must implement.
// It provides the low-level operations for persisting and retrieving events.
type EventStoreAdapter interface {
	// Append stores events to the specified stream with optimistic concurrency control.
	// expectedVersion specifies the expected current version of the stream:
	//   - AnyVersion (-1): Skip version check
	//   - NoStream (0): Stream must not exist
	//   - StreamExists (-2): Stream must exist
	//   - Any positive number: Stream must be at this exact version
	// Returns the stored events with their assigned positions, or an error.
	Append(ctx context.Context, streamID string, events []EventRecord, expectedVersion int64) ([]StoredEvent, error)

	// Load retrieves all events from a stream starting from the specified version.
	// Use fromVersion=0 to load all events.
	Load(ctx context.Context, streamID string, fromVersion int64) ([]StoredEvent, error)

	// GetStreamInfo returns metadata about a stream.
	// Returns ErrStreamNotFound if the stream does not exist.
	GetStreamInfo(ctx context.Context, streamID string) (*StreamInfo, error)

	// GetLastPosition returns the global position of the last stored event.
	// Returns 0 if no events exist.
	GetLastPosition(ctx context.Context) (uint64, error)

	// Initialize sets up the required database schema.
	// This should be called once during application startup.
	Initialize(ctx context.Context) error

	// Close releases any resources held by the adapter.
	Close() error
}

// SubscriptionOptions configures subscription behavior.
// Adapters may support additional options beyond these common ones.
type SubscriptionOptions struct {
	// BufferSize is the size of the event channel buffer.
	// Default: 100
	BufferSize int

	// PollInterval is how often to poll for new events (for polling-based adapters).
	// Default: 100ms
	PollInterval time.Duration

	// OnError is called when an error occurs during subscription.
	// If nil, errors may be logged or silently retried depending on the adapter.
	OnError func(err error)
}

// SubscriptionAdapter provides event subscription capabilities.
// Adapters may optionally implement this interface for real-time event streaming.
type SubscriptionAdapter interface {
	// LoadFromPosition loads events starting from a global position.
	// This is used by projection engines to catch up on historical events.
	LoadFromPosition(ctx context.Context, fromPosition uint64, limit int) ([]StoredEvent, error)

	// SubscribeAll subscribes to all events across all streams.
	// Events are delivered starting from the specified global position.
	// Optional SubscriptionOptions can be provided to configure behavior.
	SubscribeAll(ctx context.Context, fromPosition uint64, opts ...SubscriptionOptions) (<-chan StoredEvent, error)

	// SubscribeStream subscribes to events from a specific stream.
	// Events are delivered starting from the specified version.
	// Optional SubscriptionOptions can be provided to configure behavior.
	SubscribeStream(ctx context.Context, streamID string, fromVersion int64, opts ...SubscriptionOptions) (<-chan StoredEvent, error)

	// SubscribeCategory subscribes to all events from streams in a category.
	// Events are delivered starting from the specified global position.
	// Optional SubscriptionOptions can be provided to configure behavior.
	SubscribeCategory(ctx context.Context, category string, fromPosition uint64, opts ...SubscriptionOptions) (<-chan StoredEvent, error)
}

// SnapshotAdapter stores aggregate snapshots for faster loading.
type SnapshotAdapter interface {
	// SaveSnapshot stores a snapshot for the given stream.
	SaveSnapshot(ctx context.Context, streamID string, version int64, data []byte) error

	// LoadSnapshot retrieves the latest snapshot for the given stream.
	// Returns nil, nil if no snapshot exists.
	LoadSnapshot(ctx context.Context, streamID string) (*SnapshotRecord, error)

	// DeleteSnapshot removes the snapshot for the given stream.
	DeleteSnapshot(ctx context.Context, streamID string) error
}

// SnapshotRecord represents a stored aggregate snapshot.
type SnapshotRecord struct {
	// StreamID is the stream identifier.
	StreamID string

	// Version is the aggregate version at the time of the snapshot.
	Version int64

	// Data is the serialized snapshot payload.
	Data []byte
}

// TransactionalAdapter provides transaction support.
// Adapters may optionally implement this for atomic operations.
type TransactionalAdapter interface {
	// BeginTx starts a new transaction.
	BeginTx(ctx context.Context) (Transaction, error)
}

// Transaction represents a database transaction.
type Transaction interface {
	// Commit commits the transaction.
	Commit() error

	// Rollback aborts the transaction.
	Rollback() error

	// Adapter returns an adapter that operates within this transaction.
	Adapter() EventStoreAdapter
}

// CheckpointAdapter manages projection checkpoints.
type CheckpointAdapter interface {
	// GetCheckpoint returns the last processed position for a projection.
	// Returns 0 if no checkpoint exists.
	GetCheckpoint(ctx context.Context, projectionName string) (uint64, error)

	// SetCheckpoint stores the last processed position for a projection.
	SetCheckpoint(ctx context.Context, projectionName string, position uint64) error
}

// HealthChecker provides health check capabilities.
type HealthChecker interface {
	// Ping checks if the adapter can connect to its backend.
	Ping(ctx context.Context) error
}

// Migrator provides schema migration capabilities.
type Migrator interface {
	// Migrate runs pending database migrations.
	Migrate(ctx context.Context) error

	// MigrationVersion returns the current migration version.
	MigrationVersion(ctx context.Context) (int, error)
}

// IdempotencyStore tracks processed commands to prevent duplicate processing.
// Adapters may implement this to support command idempotency.
type IdempotencyStore interface {
	// Exists checks if a command with the given key was already processed.
	Exists(ctx context.Context, key string) (bool, error)

	// Store records that a command was processed.
	Store(ctx context.Context, record *IdempotencyRecord) error

	// Get retrieves the idempotency record for a key.
	// Returns nil, nil if the key doesn't exist.
	Get(ctx context.Context, key string) (*IdempotencyRecord, error)

	// Delete removes an idempotency record.
	Delete(ctx context.Context, key string) error

	// Cleanup removes expired records.
	Cleanup(ctx context.Context, olderThan time.Duration) (int64, error)
}

// IdempotencyRecord stores information about a processed command.
type IdempotencyRecord struct {
	// Key is the idempotency key.
	Key string `json:"key"`

	// CommandType is the type of the processed command.
	CommandType string `json:"commandType"`

	// AggregateID is the ID of the affected aggregate (if any).
	AggregateID string `json:"aggregateId,omitempty"`

	// Version is the aggregate version after processing (if any).
	Version int64 `json:"version,omitempty"`

	// Response contains serialized response data (optional).
	Response []byte `json:"response,omitempty"`

	// Error contains the error message if the command failed.
	Error string `json:"error,omitempty"`

	// Success indicates if the command was processed successfully.
	Success bool `json:"success"`

	// ProcessedAt is when the command was processed.
	ProcessedAt time.Time `json:"processedAt"`

	// ExpiresAt is when the record should expire.
	ExpiresAt time.Time `json:"expiresAt"`
}

// IsExpired returns true if the record has expired.
func (r *IdempotencyRecord) IsExpired() bool {
	return time.Now().After(r.ExpiresAt)
}

// StreamSummary contains summary information about a stream for listing.
type StreamSummary struct {
	// StreamID is the stream identifier.
	StreamID string

	// EventCount is the number of events in the stream.
	EventCount int64

	// LastEventType is the type of the most recent event.
	LastEventType string

	// LastUpdated is when the last event was stored.
	LastUpdated time.Time
}

// ProjectionInfo contains projection status information.
type ProjectionInfo struct {
	// Name is the projection identifier.
	Name string

	// Position is the last processed global position.
	Position int64

	// Status is the projection state (active, paused, etc.).
	Status string

	// UpdatedAt is when the projection was last updated.
	UpdatedAt time.Time
}

// EventStoreStats contains aggregate statistics about the event store.
type EventStoreStats struct {
	// TotalEvents is the total number of events across all streams.
	TotalEvents int64

	// TotalStreams is the number of unique streams.
	TotalStreams int64

	// EventTypes is the number of unique event types.
	EventTypes int64

	// AvgEventsPerStream is the average events per stream.
	AvgEventsPerStream float64

	// TopEventTypes contains the most common event types.
	TopEventTypes []EventTypeCount
}

// EventTypeCount holds an event type and its count.
type EventTypeCount struct {
	// Type is the event type name.
	Type string

	// Count is the number of occurrences.
	Count int64
}

// MigrationInfo contains information about a database migration.
type MigrationInfo struct {
	// Name is the migration identifier.
	Name string

	// AppliedAt is when the migration was applied (zero if pending).
	AppliedAt time.Time

	// Applied indicates if this migration has been run.
	Applied bool
}

// StreamQueryAdapter provides stream inspection capabilities for CLI tools.
// This allows querying streams without direct SQL access.
type StreamQueryAdapter interface {
	// ListStreams returns a list of stream summaries.
	// prefix filters streams by ID prefix (empty string for all).
	// limit caps the number of results (0 for unlimited).
	ListStreams(ctx context.Context, prefix string, limit int) ([]StreamSummary, error)

	// GetStreamEvents returns events from a stream with pagination.
	// fromVersion starts at this version (0 for beginning).
	// limit caps the number of events returned.
	GetStreamEvents(ctx context.Context, streamID string, fromVersion int64, limit int) ([]StoredEvent, error)

	// GetEventStoreStats returns aggregate statistics about the event store.
	GetEventStoreStats(ctx context.Context) (*EventStoreStats, error)
}

// ProjectionQueryAdapter provides projection management capabilities for CLI tools.
type ProjectionQueryAdapter interface {
	// ListProjections returns all registered projections.
	ListProjections(ctx context.Context) ([]ProjectionInfo, error)

	// GetProjection returns information about a specific projection.
	// Returns nil, nil if the projection doesn't exist.
	GetProjection(ctx context.Context, name string) (*ProjectionInfo, error)

	// SetProjectionStatus updates a projection's status (active, paused).
	SetProjectionStatus(ctx context.Context, name string, status string) error

	// ResetProjectionCheckpoint resets a projection's position to 0 for rebuild.
	ResetProjectionCheckpoint(ctx context.Context, name string) error

	// GetTotalEventCount returns the highest global position (for progress display).
	GetTotalEventCount(ctx context.Context) (int64, error)
}

// MigrationAdapter provides migration management capabilities for CLI tools.
type MigrationAdapter interface {
	// GetAppliedMigrations returns the list of applied migration names.
	GetAppliedMigrations(ctx context.Context) ([]string, error)

	// RecordMigration marks a migration as applied.
	RecordMigration(ctx context.Context, name string) error

	// RemoveMigrationRecord removes a migration record (for rollback).
	RemoveMigrationRecord(ctx context.Context, name string) error

	// ExecuteSQL runs arbitrary SQL (for applying migrations).
	ExecuteSQL(ctx context.Context, sql string) error
}

// SchemaProvider generates database-specific schema SQL.
type SchemaProvider interface {
	// GenerateSchema returns the DDL for the event store schema.
	// tableName is the events table name.
	// snapshotTableName is the snapshots table name.
	// outboxTableName is the outbox table name.
	GenerateSchema(projectName, tableName, snapshotTableName, outboxTableName string) string
}

// DiagnosticInfo contains database diagnostic information.
type DiagnosticInfo struct {
	// Version is the database server version (e.g., "PostgreSQL 16.1").
	Version string

	// Connected indicates if the connection is healthy.
	Connected bool

	// Message provides additional status information.
	Message string
}

// SchemaCheckResult contains information about the event store schema.
type SchemaCheckResult struct {
	// TableExists indicates if the events table exists.
	TableExists bool

	// EventCount is the number of events in the store.
	EventCount int64

	// Message provides additional information.
	Message string
}

// ProjectionHealthResult contains projection health information.
type ProjectionHealthResult struct {
	// TotalProjections is the number of registered projections.
	TotalProjections int64

	// ProjectionsBehind is the number of projections that are behind.
	ProjectionsBehind int64

	// MaxPosition is the highest global position in the event store.
	MaxPosition int64

	// Message provides additional information.
	Message string
}

// DiagnosticAdapter provides diagnostic capabilities for CLI tools.
type DiagnosticAdapter interface {
	// Ping checks if the database connection is healthy.
	Ping(ctx context.Context) error

	// GetDiagnosticInfo returns database version and connection status.
	GetDiagnosticInfo(ctx context.Context) (*DiagnosticInfo, error)

	// CheckSchema verifies the event store schema exists.
	CheckSchema(ctx context.Context, tableName string) (*SchemaCheckResult, error)

	// GetProjectionHealth returns projection health status.
	GetProjectionHealth(ctx context.Context) (*ProjectionHealthResult, error)
}
