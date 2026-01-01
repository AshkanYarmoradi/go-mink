// Package postgres provides a PostgreSQL implementation of the event store adapter.
package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/AshkanYarmoradi/go-mink/adapters"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// schemaNamePattern validates PostgreSQL schema names.
// Schema names must start with a letter or underscore and contain only
// alphanumeric characters and underscores.
var schemaNamePattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// quoteIdentifier quotes a PostgreSQL identifier (schema, table, column name)
// using double quotes. This prevents SQL injection by ensuring the identifier
// is treated as a literal name, not as SQL syntax.
// PostgreSQL identifier quoting: wrap in double quotes, escape internal quotes by doubling.
func quoteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// quoteQualifiedTable returns a properly quoted schema.table identifier.
func quoteQualifiedTable(schema, table string) string {
	return quoteIdentifier(schema) + "." + quoteIdentifier(table)
}

// safeSchemaIdentifier validates and quotes a schema name for safe use in SQL.
// This combines validation and quoting to provide defense-in-depth against SQL injection.
// Returns the quoted schema identifier and any validation error.
func safeSchemaIdentifier(schema string) (string, error) {
	if err := validateSchemaName(schema); err != nil {
		return "", err
	}
	return quoteIdentifier(schema), nil
}

// isValidIdentifier checks if a name is a valid PostgreSQL identifier.
// Returns empty string if valid, or an error message describing the issue.
func isValidIdentifier(name string) string {
	if name == "" {
		return "cannot be empty"
	}
	if len(name) > 63 {
		return "exceeds 63 characters"
	}
	if !schemaNamePattern.MatchString(name) {
		return "contains invalid characters"
	}
	return ""
}

// validateSchemaName checks if the schema name is a valid PostgreSQL identifier.
func validateSchemaName(schema string) error {
	if reason := isValidIdentifier(schema); reason != "" {
		return fmt.Errorf("%w: schema name %s", ErrInvalidSchemaName, reason)
	}
	return nil
}

// validateIdentifier checks if a name is a valid PostgreSQL identifier.
// This helps prevent SQL injection when using identifiers in queries.
func validateIdentifier(name, kind string) error {
	if reason := isValidIdentifier(name); reason != "" {
		return fmt.Errorf("mink/postgres: %s name %s", kind, reason)
	}
	return nil
}

// Version constants for optimistic concurrency control.
// These are re-exported from the adapters package for convenience.
const (
	AnyVersion   = adapters.AnyVersion
	NoStream     = adapters.NoStream
	StreamExists = adapters.StreamExists
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
	ErrInvalidSchemaName   = fmt.Errorf("mink/postgres: invalid schema name")
)

// Ensure PostgresAdapter implements required interfaces.
var (
	_ adapters.EventStoreAdapter   = (*PostgresAdapter)(nil)
	_ adapters.SubscriptionAdapter = (*PostgresAdapter)(nil)
	_ adapters.SnapshotAdapter     = (*PostgresAdapter)(nil)
	_ adapters.CheckpointAdapter   = (*PostgresAdapter)(nil)
	_ adapters.HealthChecker       = (*PostgresAdapter)(nil)
	_ adapters.Migrator            = (*PostgresAdapter)(nil)
)

// PostgresAdapter is a PostgreSQL implementation of EventStoreAdapter.
type PostgresAdapter struct {
	db                *sql.DB
	schema            string
	schemaQ           string // Pre-validated and quoted schema identifier
	closed            bool
	healthCheckCancel context.CancelFunc
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

// WithConnectionMaxIdleTime sets the maximum idle time for connections.
func WithConnectionMaxIdleTime(d time.Duration) Option {
	return func(a *PostgresAdapter) {
		a.db.SetConnMaxIdleTime(d)
	}
}

// WithHealthCheck enables periodic connection pool health checking.
// The health check runs at the specified interval and validates connections.
func WithHealthCheck(interval time.Duration) Option {
	return func(a *PostgresAdapter) {
		ctx, cancel := context.WithCancel(context.Background())
		a.healthCheckCancel = cancel
		go a.runHealthCheck(ctx, interval)
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

	// Validate and cache quoted schema name to prevent SQL injection
	schemaQ, err := safeSchemaIdentifier(adapter.schema)
	if err != nil {
		// Cancel health check goroutine if it was started
		if adapter.healthCheckCancel != nil {
			adapter.healthCheckCancel()
		}
		_ = db.Close()
		return nil, err
	}
	adapter.schemaQ = schemaQ

	return adapter, nil
}

// NewAdapterWithDB creates a new adapter with an existing database connection.
// Returns an error if the schema name is invalid.
func NewAdapterWithDB(db *sql.DB, opts ...Option) (*PostgresAdapter, error) {
	if db == nil {
		return nil, fmt.Errorf("mink/postgres: database connection is nil")
	}

	adapter := &PostgresAdapter{
		db:     db,
		schema: "mink",
	}

	for _, opt := range opts {
		opt(adapter)
	}

	// Validate and cache quoted schema name to prevent SQL injection
	schemaQ, err := safeSchemaIdentifier(adapter.schema)
	if err != nil {
		// Cancel health check goroutine if it was started
		if adapter.healthCheckCancel != nil {
			adapter.healthCheckCancel()
		}
		return nil, err
	}
	adapter.schemaQ = schemaQ

	return adapter, nil
}

// Initialize creates the required database schema and tables.
func (a *PostgresAdapter) Initialize(ctx context.Context) error {
	return a.Migrate(ctx)
}

// Migrate runs database migrations.
func (a *PostgresAdapter) Migrate(ctx context.Context) error {
	// Use pre-validated schemaQ from constructor
	schemaQ := a.schemaQ

	// Create schema
	_, err := a.db.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS `+schemaQ)
	if err != nil {
		return fmt.Errorf("mink/postgres: failed to create schema: %w", err)
	}

	// Create streams table
	streamsSQL := `
		CREATE TABLE IF NOT EXISTS ` + schemaQ + `.streams (
			id              BIGSERIAL PRIMARY KEY,
			stream_id       VARCHAR(500) NOT NULL UNIQUE,
			category        VARCHAR(250) NOT NULL,
			version         BIGINT NOT NULL DEFAULT 0,
			created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`

	_, err = a.db.ExecContext(ctx, streamsSQL)
	if err != nil {
		return fmt.Errorf("mink/postgres: failed to create streams table: %w", err)
	}

	// Create events table
	eventsSQL := `
		CREATE TABLE IF NOT EXISTS ` + schemaQ + `.events (
			global_position BIGSERIAL PRIMARY KEY,
			stream_id       VARCHAR(500) NOT NULL,
			version         BIGINT NOT NULL,
			event_id        UUID NOT NULL DEFAULT gen_random_uuid(),
			event_type      VARCHAR(500) NOT NULL,
			data            JSONB NOT NULL,
			metadata        JSONB,
			timestamp       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			UNIQUE(stream_id, version)
		)`

	_, err = a.db.ExecContext(ctx, eventsSQL)
	if err != nil {
		return fmt.Errorf("mink/postgres: failed to create events table: %w", err)
	}

	// Create indexes
	streamsTableQ := quoteQualifiedTable(a.schema, "streams")
	eventsTableQ := quoteQualifiedTable(a.schema, "events")
	indexes := []string{
		`CREATE INDEX IF NOT EXISTS idx_streams_category ON ` + streamsTableQ + `(category)`,
		`CREATE INDEX IF NOT EXISTS idx_events_stream ON ` + eventsTableQ + `(stream_id, version)`,
		`CREATE INDEX IF NOT EXISTS idx_events_type ON ` + eventsTableQ + `(event_type)`,
		`CREATE INDEX IF NOT EXISTS idx_events_timestamp ON ` + eventsTableQ + `(timestamp)`,
	}

	for _, idx := range indexes {
		_, err = a.db.ExecContext(ctx, idx)
		if err != nil {
			return fmt.Errorf("mink/postgres: failed to create index: %w", err)
		}
	}

	// Create snapshots table
	snapshotsSQL := `
		CREATE TABLE IF NOT EXISTS ` + schemaQ + `.snapshots (
			stream_id       VARCHAR(500) PRIMARY KEY,
			version         BIGINT NOT NULL,
			data            BYTEA NOT NULL,
			created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`

	_, err = a.db.ExecContext(ctx, snapshotsSQL)
	if err != nil {
		return fmt.Errorf("mink/postgres: failed to create snapshots table: %w", err)
	}

	// Create checkpoints table
	checkpointsSQL := `
		CREATE TABLE IF NOT EXISTS ` + schemaQ + `.checkpoints (
			projection_name VARCHAR(500) PRIMARY KEY,
			position        BIGINT NOT NULL DEFAULT 0,
			updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`

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
	err := a.db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT FROM information_schema.tables 
			WHERE table_schema = $1 AND table_name = 'events'
		)`, a.schema).Scan(&exists)

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
	schemaQ := a.schemaQ

	err = tx.QueryRowContext(ctx, `
		SELECT version FROM `+schemaQ+`.streams 
		WHERE stream_id = $1 
		FOR UPDATE`, streamID).Scan(&currentVersion)

	if err == sql.ErrNoRows {
		streamExists = false
		currentVersion = 0
	} else if err != nil {
		return nil, fmt.Errorf("mink/postgres: failed to get stream version: %w", err)
	} else {
		streamExists = true
	}

	// Check expected version
	if err := adapters.CheckVersion(streamID, expectedVersion, currentVersion, streamExists); err != nil {
		return nil, err
	}

	// Create stream if it doesn't exist
	category := adapters.ExtractCategory(streamID)
	if !streamExists {
		_, err = tx.ExecContext(ctx, `
			INSERT INTO `+schemaQ+`.streams (stream_id, category, version)
			VALUES ($1, $2, 0)`, streamID, category)
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

		err = tx.QueryRowContext(ctx, `
			INSERT INTO `+schemaQ+`.events (stream_id, version, event_type, data, metadata)
			VALUES ($1, $2, $3, $4, $5)
			RETURNING global_position, event_id, timestamp`,
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
	_, err = tx.ExecContext(ctx, `
		UPDATE `+schemaQ+`.streams 
		SET version = $1, updated_at = NOW()
		WHERE stream_id = $2`, currentVersion, streamID)
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

	schemaQ := a.schemaQ
	rows, err := a.db.QueryContext(ctx, `
		SELECT event_id, stream_id, version, event_type, data, metadata, global_position, timestamp
		FROM `+schemaQ+`.events
		WHERE stream_id = $1 AND version > $2
		ORDER BY version`, streamID, fromVersion)
	if err != nil {
		return nil, fmt.Errorf("mink/postgres: failed to load events: %w", err)
	}
	defer rows.Close()

	return a.scanEvents(rows)
}

// GetStreamInfo returns metadata about a stream.
func (a *PostgresAdapter) GetStreamInfo(ctx context.Context, streamID string) (*adapters.StreamInfo, error) {
	if a.closed {
		return nil, ErrAdapterClosed
	}

	schemaQ := a.schemaQ
	var info adapters.StreamInfo
	// Query stream info and count events in one query using a subquery
	err := a.db.QueryRowContext(ctx, `
		SELECT s.stream_id, s.category, s.version, s.created_at, s.updated_at,
		       (SELECT COUNT(*) FROM `+schemaQ+`.events e WHERE e.stream_id = s.stream_id) as event_count
		FROM `+schemaQ+`.streams s
		WHERE s.stream_id = $1`, streamID).Scan(
		&info.StreamID,
		&info.Category,
		&info.Version,
		&info.CreatedAt,
		&info.UpdatedAt,
		&info.EventCount,
	)

	if err == sql.ErrNoRows {
		return nil, adapters.NewStreamNotFoundError(streamID)
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

	schemaQ := a.schemaQ
	var pos sql.NullInt64
	err := a.db.QueryRowContext(ctx, `
		SELECT MAX(global_position) FROM `+schemaQ+`.events`).Scan(&pos)
	if err != nil {
		return 0, fmt.Errorf("mink/postgres: failed to get last position: %w", err)
	}

	if pos.Valid {
		// Safe conversion: BIGSERIAL values are always positive,
		// but we check defensively to avoid integer overflow
		if pos.Int64 < 0 {
			return 0, fmt.Errorf("mink/postgres: invalid negative position value: %d", pos.Int64)
		}
		return uint64(pos.Int64), nil
	}
	return 0, nil
}

// Close releases the database connection and stops health checking.
func (a *PostgresAdapter) Close() error {
	a.closed = true
	if a.healthCheckCancel != nil {
		a.healthCheckCancel()
	}
	return a.db.Close()
}

// SaveSnapshot stores a snapshot for the given stream.
func (a *PostgresAdapter) SaveSnapshot(ctx context.Context, streamID string, version int64, data []byte) error {
	if a.closed {
		return ErrAdapterClosed
	}

	schemaQ := a.schemaQ
	_, err := a.db.ExecContext(ctx, `
		INSERT INTO `+schemaQ+`.snapshots (stream_id, version, data)
		VALUES ($1, $2, $3)
		ON CONFLICT (stream_id) DO UPDATE SET
			version = EXCLUDED.version,
			data = EXCLUDED.data,
			created_at = NOW()`, streamID, version, data)
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

	schemaQ := a.schemaQ
	var snapshot adapters.SnapshotRecord
	err := a.db.QueryRowContext(ctx, `
		SELECT stream_id, version, data
		FROM `+schemaQ+`.snapshots
		WHERE stream_id = $1`, streamID).Scan(
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

	schemaQ := a.schemaQ
	_, err := a.db.ExecContext(ctx, `
		DELETE FROM `+schemaQ+`.snapshots WHERE stream_id = $1`, streamID)
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

	schemaQ := a.schemaQ
	var pos sql.NullInt64
	err := a.db.QueryRowContext(ctx, `
		SELECT position FROM `+schemaQ+`.checkpoints
		WHERE projection_name = $1`, projectionName).Scan(&pos)

	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("mink/postgres: failed to get checkpoint: %w", err)
	}

	if pos.Valid {
		// Safe conversion: BIGSERIAL values are always positive,
		// but we check defensively to avoid integer overflow
		if pos.Int64 < 0 {
			return 0, fmt.Errorf("mink/postgres: invalid negative position value: %d", pos.Int64)
		}
		return uint64(pos.Int64), nil
	}
	return 0, nil
}

// SetCheckpoint stores the last processed position for a projection.
func (a *PostgresAdapter) SetCheckpoint(ctx context.Context, projectionName string, position uint64) error {
	if a.closed {
		return ErrAdapterClosed
	}

	schemaQ := a.schemaQ
	_, err := a.db.ExecContext(ctx, `
		INSERT INTO `+schemaQ+`.checkpoints (projection_name, position)
		VALUES ($1, $2)
		ON CONFLICT (projection_name) DO UPDATE SET
			position = EXCLUDED.position,
			updated_at = NOW()`, projectionName, position)
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

// runHealthCheck periodically validates database connections.
func (a *PostgresAdapter) runHealthCheck(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if a.closed {
				return
			}
			// Ping validates the connection and removes stale connections from pool
			_ = a.db.PingContext(ctx)
		}
	}
}

// Stats returns connection pool statistics.
func (a *PostgresAdapter) Stats() sql.DBStats {
	return a.db.Stats()
}

// DB returns the underlying database connection.
func (a *PostgresAdapter) DB() *sql.DB {
	return a.db
}

// Schema returns the schema name.
func (a *PostgresAdapter) Schema() string {
	return a.schema
}

// ConcurrencyError is an alias for adapters.ConcurrencyError for backward compatibility.
type ConcurrencyError = adapters.ConcurrencyError

// StreamNotFoundError is an alias for adapters.StreamNotFoundError for backward compatibility.
type StreamNotFoundError = adapters.StreamNotFoundError

// NewConcurrencyError is an alias for adapters.NewConcurrencyError for backward compatibility.
var NewConcurrencyError = adapters.NewConcurrencyError

// NewStreamNotFoundError is an alias for adapters.NewStreamNotFoundError for backward compatibility.
var NewStreamNotFoundError = adapters.NewStreamNotFoundError
