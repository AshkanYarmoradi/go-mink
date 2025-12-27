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

// SubscriptionAdapter provides event subscription capabilities.
// Adapters may optionally implement this interface for real-time event streaming.
type SubscriptionAdapter interface {
	// SubscribeAll subscribes to all events across all streams.
	// Events are delivered starting from the specified global position.
	SubscribeAll(ctx context.Context, fromPosition uint64) (<-chan StoredEvent, error)

	// SubscribeStream subscribes to events from a specific stream.
	// Events are delivered starting from the specified version.
	SubscribeStream(ctx context.Context, streamID string, fromVersion int64) (<-chan StoredEvent, error)

	// SubscribeCategory subscribes to all events from streams in a category.
	// Events are delivered starting from the specified global position.
	SubscribeCategory(ctx context.Context, category string, fromPosition uint64) (<-chan StoredEvent, error)
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
