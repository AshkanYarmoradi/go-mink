// Package postgres provides a PostgreSQL implementation of the event store adapter.
package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/AshkanYarmoradi/go-mink/adapters"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// Version constants for optimistic concurrency control.
const (
	AnyVersion   int64 = -1
	NoStream     int64 = 0
	StreamExists int64 = -2
)

// Sentinel errors for the postgres adapter.
// These are aliases to the adapters package errors for compatibility with errors.Is().
var (
	ErrAdapterClosed       = adapters.ErrAdapterClosed
	ErrEmptyStreamID       = adapters.ErrEmptyStreamID
	ErrNoEvents            = adapters.ErrNoEvents
	ErrConcurrencyConflict = adapters.ErrConcurrencyConflict
	ErrStreamNotFound      = adapters.ErrStreamNotFound
	ErrInvalidVersion      = adapters.ErrInvalidVersion
)

// Ensure PostgresAdapter implements required interfaces.
var (
	_ adapters.EventStoreAdapter = (*PostgresAdapter)(nil)
	_ adapters.SnapshotAdapter   = (*PostgresAdapter)(nil)
	_ adapters.CheckpointAdapter = (*PostgresAdapter)(nil)
	_ adapters.HealthChecker     = (*PostgresAdapter)(nil)
	_ adapters.Migrator          = (*PostgresAdapter)(nil)
)

// PostgresAdapter is a PostgreSQL implementation of EventStoreAdapter.
type PostgresAdapter struct {
	db     *sql.DB
	schema string
	closed bool
}

// Option configures a PostgresAdapter.
type Option func(*PostgresAdapter)

// WithSchema sets the database schema name.
func WithSchema(schema string) Option {
	return func(a *PostgresAdapter) {
		a.schema = schema
	}
}

// WithMaxConnections sets the maximum number of open connections.
func WithMaxConnections(n int) Option {
	return func(a *PostgresAdapter) {
		a.db.SetMaxOpenConns(n)
	}
}

// WithMaxIdleConnections sets the maximum number of idle connections.
func WithMaxIdleConnections(n int) Option {
	return func(a *PostgresAdapter) {
		a.db.SetMaxIdleConns(n)
	}
}

// WithConnectionMaxLifetime sets the maximum connection lifetime.
func WithConnectionMaxLifetime(d time.Duration) Option {
	return func(a *PostgresAdapter) {
		a.db.SetConnMaxLifetime(d)
	}
}

// NewAdapter creates a new PostgreSQL event store adapter.
func NewAdapter(connStr string, opts ...Option) (*PostgresAdapter, error) {
	db, err := sql.Open("pgx", connStr)
	if err != nil {
		return nil, fmt.Errorf("mink/postgres: failed to open database: %w", err)
	}

	adapter := &PostgresAdapter{
		db:     db,
		schema: "mink",
	}

	for _, opt := range opts {
		opt(adapter)
	}

	return adapter, nil
}

// NewAdapterWithDB creates a new adapter with an existing database connection.
func NewAdapterWithDB(db *sql.DB, opts ...Option) *PostgresAdapter {
	adapter := &PostgresAdapter{
		db:     db,
		schema: "mink",
	}

	for _, opt := range opts {
		opt(adapter)
	}

	return adapter
}

// Initialize creates the required database schema and tables.
func (a *PostgresAdapter) Initialize(ctx context.Context) error {
	return a.Migrate(ctx)
}

// Migrate runs database migrations.
func (a *PostgresAdapter) Migrate(ctx context.Context) error {
	// Create schema
	_, err := a.db.ExecContext(ctx, fmt.Sprintf(`CREATE SCHEMA IF NOT EXISTS %s`, a.schema))
	if err != nil {
		return fmt.Errorf("mink/postgres: failed to create schema: %w", err)
	}

	// Create streams table
	streamsSQL := fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s.streams (
			id              BIGSERIAL PRIMARY KEY,
			stream_id       VARCHAR(500) NOT NULL UNIQUE,
			category        VARCHAR(250) NOT NULL,
			version         BIGINT NOT NULL DEFAULT 0,
			created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`, a.schema)

	_, err = a.db.ExecContext(ctx, streamsSQL)
	if err != nil {
		return fmt.Errorf("mink/postgres: failed to create streams table: %w", err)
	}

	// Create events table
	eventsSQL := fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s.events (
			global_position BIGSERIAL PRIMARY KEY,
			stream_id       VARCHAR(500) NOT NULL,
			version         BIGINT NOT NULL,
			event_id        UUID NOT NULL DEFAULT gen_random_uuid(),
			event_type      VARCHAR(500) NOT NULL,
			data            JSONB NOT NULL,
			metadata        JSONB,
			timestamp       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			UNIQUE(stream_id, version)
		)`, a.schema)

	_, err = a.db.ExecContext(ctx, eventsSQL)
	if err != nil {
		return fmt.Errorf("mink/postgres: failed to create events table: %w", err)
	}

	// Create indexes
	indexes := []string{
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_streams_category ON %s.streams(category)`, a.schema),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_events_stream ON %s.events(stream_id, version)`, a.schema),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_events_type ON %s.events(event_type)`, a.schema),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_events_timestamp ON %s.events(timestamp)`, a.schema),
	}

	for _, idx := range indexes {
		_, err = a.db.ExecContext(ctx, idx)
		if err != nil {
			return fmt.Errorf("mink/postgres: failed to create index: %w", err)
		}
	}

	// Create snapshots table
	snapshotsSQL := fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s.snapshots (
			stream_id       VARCHAR(500) PRIMARY KEY,
			version         BIGINT NOT NULL,
			data            BYTEA NOT NULL,
			created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`, a.schema)

	_, err = a.db.ExecContext(ctx, snapshotsSQL)
	if err != nil {
		return fmt.Errorf("mink/postgres: failed to create snapshots table: %w", err)
	}

	// Create checkpoints table
	checkpointsSQL := fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s.checkpoints (
			projection_name VARCHAR(500) PRIMARY KEY,
			position        BIGINT NOT NULL DEFAULT 0,
			updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`, a.schema)

	_, err = a.db.ExecContext(ctx, checkpointsSQL)
	if err != nil {
		return fmt.Errorf("mink/postgres: failed to create checkpoints table: %w", err)
	}

	return nil
}

// MigrationVersion returns the current migration version.
func (a *PostgresAdapter) MigrationVersion(ctx context.Context) (int, error) {
	// For now, return 1 if tables exist
	var exists bool
	err := a.db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT EXISTS (
			SELECT FROM information_schema.tables 
			WHERE table_schema = '%s' AND table_name = 'events'
		)`, a.schema)).Scan(&exists)

	if err != nil {
		return 0, err
	}

	if exists {
		return 1, nil
	}
	return 0, nil
}

// Append stores events to the specified stream with optimistic concurrency control.
func (a *PostgresAdapter) Append(ctx context.Context, streamID string, events []adapters.EventRecord, expectedVersion int64) ([]adapters.StoredEvent, error) {
	if a.closed {
		return nil, ErrAdapterClosed
	}

	if streamID == "" {
		return nil, ErrEmptyStreamID
	}

	if len(events) == 0 {
		return nil, ErrNoEvents
	}

	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("mink/postgres: failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Get current stream version with lock
	var currentVersion int64
	var streamExists bool

	err = tx.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT version FROM %s.streams 
		WHERE stream_id = $1 
		FOR UPDATE`, a.schema), streamID).Scan(&currentVersion)

	if err == sql.ErrNoRows {
		streamExists = false
		currentVersion = 0
	} else if err != nil {
		return nil, fmt.Errorf("mink/postgres: failed to get stream version: %w", err)
	} else {
		streamExists = true
	}

	// Check expected version
	if err := a.checkVersion(streamID, expectedVersion, currentVersion, streamExists); err != nil {
		return nil, err
	}

	// Create stream if it doesn't exist
	category := extractCategory(streamID)
	if !streamExists {
		_, err = tx.ExecContext(ctx, fmt.Sprintf(`
			INSERT INTO %s.streams (stream_id, category, version)
			VALUES ($1, $2, 0)`, a.schema), streamID, category)
		if err != nil {
			return nil, fmt.Errorf("mink/postgres: failed to create stream: %w", err)
		}
	}

	// Insert events
	storedEvents := make([]adapters.StoredEvent, len(events))
	for i, event := range events {
		currentVersion++

		metadataJSON, err := json.Marshal(event.Metadata)
		if err != nil {
			return nil, fmt.Errorf("mink/postgres: failed to marshal metadata: %w", err)
		}

		var globalPosition uint64
		var eventID string
		var timestamp time.Time

		err = tx.QueryRowContext(ctx, fmt.Sprintf(`
			INSERT INTO %s.events (stream_id, version, event_type, data, metadata)
			VALUES ($1, $2, $3, $4, $5)
			RETURNING global_position, event_id, timestamp`, a.schema),
			streamID, currentVersion, event.Type, event.Data, metadataJSON,
		).Scan(&globalPosition, &eventID, &timestamp)

		if err != nil {
			return nil, fmt.Errorf("mink/postgres: failed to insert event: %w", err)
		}

		storedEvents[i] = adapters.StoredEvent{
			ID:             eventID,
			StreamID:       streamID,
			Type:           event.Type,
			Data:           event.Data,
			Metadata:       event.Metadata,
			Version:        currentVersion,
			GlobalPosition: globalPosition,
			Timestamp:      timestamp,
		}
	}

	// Update stream version
	_, err = tx.ExecContext(ctx, fmt.Sprintf(`
		UPDATE %s.streams 
		SET version = $1, updated_at = NOW()
		WHERE stream_id = $2`, a.schema), currentVersion, streamID)
	if err != nil {
		return nil, fmt.Errorf("mink/postgres: failed to update stream version: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("mink/postgres: failed to commit transaction: %w", err)
	}

	return storedEvents, nil
}

// Load retrieves all events from a stream starting from the specified version.
func (a *PostgresAdapter) Load(ctx context.Context, streamID string, fromVersion int64) ([]adapters.StoredEvent, error) {
	if a.closed {
		return nil, ErrAdapterClosed
	}

	if streamID == "" {
		return nil, ErrEmptyStreamID
	}

	rows, err := a.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT global_position, event_id, stream_id, version, event_type, data, metadata, timestamp
		FROM %s.events
		WHERE stream_id = $1 AND version > $2
		ORDER BY version`, a.schema), streamID, fromVersion)
	if err != nil {
		return nil, fmt.Errorf("mink/postgres: failed to load events: %w", err)
	}
	defer rows.Close()

	events := make([]adapters.StoredEvent, 0)
	for rows.Next() {
		var event adapters.StoredEvent
		var metadataJSON []byte

		err := rows.Scan(
			&event.GlobalPosition,
			&event.ID,
			&event.StreamID,
			&event.Version,
			&event.Type,
			&event.Data,
			&metadataJSON,
			&event.Timestamp,
		)
		if err != nil {
			return nil, fmt.Errorf("mink/postgres: failed to scan event: %w", err)
		}

		if len(metadataJSON) > 0 {
			if err := json.Unmarshal(metadataJSON, &event.Metadata); err != nil {
				return nil, fmt.Errorf("mink/postgres: failed to unmarshal metadata: %w", err)
			}
		}

		events = append(events, event)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("mink/postgres: error iterating events: %w", err)
	}

	return events, nil
}

// GetStreamInfo returns metadata about a stream.
func (a *PostgresAdapter) GetStreamInfo(ctx context.Context, streamID string) (*adapters.StreamInfo, error) {
	if a.closed {
		return nil, ErrAdapterClosed
	}

	var info adapters.StreamInfo
	err := a.db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT stream_id, category, version, created_at, updated_at
		FROM %s.streams
		WHERE stream_id = $1`, a.schema), streamID).Scan(
		&info.StreamID,
		&info.Category,
		&info.Version,
		&info.CreatedAt,
		&info.UpdatedAt,
	)

	if err == sql.ErrNoRows {
		return nil, NewStreamNotFoundError(streamID)
	}
	if err != nil {
		return nil, fmt.Errorf("mink/postgres: failed to get stream info: %w", err)
	}

	return &info, nil
}

// GetLastPosition returns the global position of the last stored event.
func (a *PostgresAdapter) GetLastPosition(ctx context.Context) (uint64, error) {
	if a.closed {
		return 0, ErrAdapterClosed
	}

	var pos sql.NullInt64
	err := a.db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT MAX(global_position) FROM %s.events`, a.schema)).Scan(&pos)
	if err != nil {
		return 0, fmt.Errorf("mink/postgres: failed to get last position: %w", err)
	}

	if pos.Valid {
		return uint64(pos.Int64), nil
	}
	return 0, nil
}

// Close releases the database connection.
func (a *PostgresAdapter) Close() error {
	a.closed = true
	return a.db.Close()
}

// SaveSnapshot stores a snapshot for the given stream.
func (a *PostgresAdapter) SaveSnapshot(ctx context.Context, streamID string, version int64, data []byte) error {
	if a.closed {
		return ErrAdapterClosed
	}

	_, err := a.db.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s.snapshots (stream_id, version, data)
		VALUES ($1, $2, $3)
		ON CONFLICT (stream_id) DO UPDATE SET
			version = EXCLUDED.version,
			data = EXCLUDED.data,
			created_at = NOW()`, a.schema), streamID, version, data)
	if err != nil {
		return fmt.Errorf("mink/postgres: failed to save snapshot: %w", err)
	}

	return nil
}

// LoadSnapshot retrieves the latest snapshot for the given stream (SnapshotAdapter implementation).
func (a *PostgresAdapter) LoadSnapshot(ctx context.Context, streamID string) (*adapters.SnapshotRecord, error) {
	if a.closed {
		return nil, ErrAdapterClosed
	}

	var snapshot adapters.SnapshotRecord
	err := a.db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT stream_id, version, data
		FROM %s.snapshots
		WHERE stream_id = $1`, a.schema), streamID).Scan(
		&snapshot.StreamID,
		&snapshot.Version,
		&snapshot.Data,
	)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("mink/postgres: failed to load snapshot: %w", err)
	}

	return &snapshot, nil
}

// DeleteSnapshot removes the snapshot for the given stream.
func (a *PostgresAdapter) DeleteSnapshot(ctx context.Context, streamID string) error {
	if a.closed {
		return ErrAdapterClosed
	}

	_, err := a.db.ExecContext(ctx, fmt.Sprintf(`
		DELETE FROM %s.snapshots WHERE stream_id = $1`, a.schema), streamID)
	if err != nil {
		return fmt.Errorf("mink/postgres: failed to delete snapshot: %w", err)
	}

	return nil
}

// GetCheckpoint returns the last processed position for a projection.
func (a *PostgresAdapter) GetCheckpoint(ctx context.Context, projectionName string) (uint64, error) {
	if a.closed {
		return 0, ErrAdapterClosed
	}

	var pos sql.NullInt64
	err := a.db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT position FROM %s.checkpoints
		WHERE projection_name = $1`, a.schema), projectionName).Scan(&pos)

	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("mink/postgres: failed to get checkpoint: %w", err)
	}

	if pos.Valid {
		return uint64(pos.Int64), nil
	}
	return 0, nil
}

// SetCheckpoint stores the last processed position for a projection.
func (a *PostgresAdapter) SetCheckpoint(ctx context.Context, projectionName string, position uint64) error {
	if a.closed {
		return ErrAdapterClosed
	}

	_, err := a.db.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s.checkpoints (projection_name, position)
		VALUES ($1, $2)
		ON CONFLICT (projection_name) DO UPDATE SET
			position = EXCLUDED.position,
			updated_at = NOW()`, a.schema), projectionName, position)
	if err != nil {
		return fmt.Errorf("mink/postgres: failed to set checkpoint: %w", err)
	}

	return nil
}

// Ping checks database connectivity.
func (a *PostgresAdapter) Ping(ctx context.Context) error {
	if a.closed {
		return ErrAdapterClosed
	}
	return a.db.PingContext(ctx)
}

// DB returns the underlying database connection.
func (a *PostgresAdapter) DB() *sql.DB {
	return a.db
}

// Schema returns the schema name.
func (a *PostgresAdapter) Schema() string {
	return a.schema
}

// ConcurrencyError provides details about a concurrency conflict.
type ConcurrencyError struct {
	StreamID        string
	ExpectedVersion int64
	ActualVersion   int64
}

// NewConcurrencyError creates a new ConcurrencyError.
func NewConcurrencyError(streamID string, expected, actual int64) *ConcurrencyError {
	return &ConcurrencyError{
		StreamID:        streamID,
		ExpectedVersion: expected,
		ActualVersion:   actual,
	}
}

// Error implements the error interface.
func (e *ConcurrencyError) Error() string {
	return fmt.Sprintf("mink/postgres: concurrency conflict on stream %q: expected version %d, got %d",
		e.StreamID, e.ExpectedVersion, e.ActualVersion)
}

// Is implements errors.Is compatibility.
func (e *ConcurrencyError) Is(target error) bool {
	return target == ErrConcurrencyConflict
}

// StreamNotFoundError provides details about a missing stream.
type StreamNotFoundError struct {
	StreamID string
}

// NewStreamNotFoundError creates a new StreamNotFoundError.
func NewStreamNotFoundError(streamID string) *StreamNotFoundError {
	return &StreamNotFoundError{StreamID: streamID}
}

// Error implements the error interface.
func (e *StreamNotFoundError) Error() string {
	return fmt.Sprintf("mink/postgres: stream %q not found", e.StreamID)
}

// Is implements errors.Is compatibility.
func (e *StreamNotFoundError) Is(target error) bool {
	return target == ErrStreamNotFound
}

// checkVersion validates the expected version against the current version.
func (a *PostgresAdapter) checkVersion(streamID string, expected, current int64, exists bool) error {
	switch expected {
	case AnyVersion:
		return nil
	case NoStream:
		if exists {
			return NewConcurrencyError(streamID, expected, current)
		}
		return nil
	case StreamExists:
		if !exists {
			return NewStreamNotFoundError(streamID)
		}
		return nil
	default:
		if expected < 0 {
			return ErrInvalidVersion
		}
		if current != expected {
			return NewConcurrencyError(streamID, expected, current)
		}
		return nil
	}
}

// extractCategory extracts the category from a stream ID.
func extractCategory(streamID string) string {
	parts := strings.SplitN(streamID, "-", 2)
	if len(parts) > 0 {
		return parts[0]
	}
	return ""
}
