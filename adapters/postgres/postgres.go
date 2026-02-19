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

	mink "github.com/AshkanYarmoradi/go-mink"
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
	_ mink.CheckpointStore         = (*PostgresAdapter)(nil)
	_ adapters.OutboxAppender      = (*PostgresAdapter)(nil)
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

	storedEvents, err := a.appendInTx(ctx, tx, streamID, events, expectedVersion)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("mink/postgres: failed to commit transaction: %w", err)
	}

	return storedEvents, nil
}

// AppendWithOutbox atomically appends events and schedules outbox messages in a single transaction.
func (a *PostgresAdapter) AppendWithOutbox(ctx context.Context, streamID string, events []adapters.EventRecord, expectedVersion int64, outboxMessages []*adapters.OutboxMessage) ([]adapters.StoredEvent, error) {
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

	storedEvents, err := a.appendInTx(ctx, tx, streamID, events, expectedVersion)
	if err != nil {
		return nil, err
	}

	// Insert outbox messages in the same transaction
	if len(outboxMessages) > 0 {
		outboxStore := NewOutboxStoreFromAdapter(a)
		if err := outboxStore.ScheduleInTx(ctx, tx, outboxMessages); err != nil {
			return nil, fmt.Errorf("mink/postgres: failed to schedule outbox messages: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("mink/postgres: failed to commit transaction: %w", err)
	}

	return storedEvents, nil
}

// appendInTx appends events within an existing transaction.
func (a *PostgresAdapter) appendInTx(ctx context.Context, tx *sql.Tx, streamID string, events []adapters.EventRecord, expectedVersion int64) ([]adapters.StoredEvent, error) {
	// Get current stream version with lock
	var currentVersion int64
	var streamExists bool
	schemaQ := a.schemaQ

	err := tx.QueryRowContext(ctx, `
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

// DeleteCheckpoint removes the checkpoint for a projection.
// This implements mink.CheckpointStore interface.
func (a *PostgresAdapter) DeleteCheckpoint(ctx context.Context, projectionName string) error {
	if a.closed {
		return ErrAdapterClosed
	}

	schemaQ := a.schemaQ
	_, err := a.db.ExecContext(ctx, `
		DELETE FROM `+schemaQ+`.checkpoints
		WHERE projection_name = $1`, projectionName)
	if err != nil {
		return fmt.Errorf("mink/postgres: failed to delete checkpoint: %w", err)
	}

	return nil
}

// GetAllCheckpoints returns checkpoints for all projections.
// This implements mink.CheckpointStore interface.
func (a *PostgresAdapter) GetAllCheckpoints(ctx context.Context) (map[string]uint64, error) {
	if a.closed {
		return nil, ErrAdapterClosed
	}

	schemaQ := a.schemaQ
	rows, err := a.db.QueryContext(ctx, `
		SELECT projection_name, position
		FROM `+schemaQ+`.checkpoints
		ORDER BY projection_name`)
	if err != nil {
		return nil, fmt.Errorf("mink/postgres: failed to get all checkpoints: %w", err)
	}
	defer rows.Close()

	checkpoints := make(map[string]uint64)
	for rows.Next() {
		var name string
		var pos sql.NullInt64
		if err := rows.Scan(&name, &pos); err != nil {
			return nil, fmt.Errorf("mink/postgres: failed to scan checkpoint: %w", err)
		}
		if pos.Valid && pos.Int64 >= 0 {
			checkpoints[name] = uint64(pos.Int64)
		}
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("mink/postgres: failed to iterate checkpoints: %w", err)
	}

	return checkpoints, nil
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

// Ensure PostgresAdapter implements CLI-related interfaces.
var (
	_ adapters.StreamQueryAdapter     = (*PostgresAdapter)(nil)
	_ adapters.ProjectionQueryAdapter = (*PostgresAdapter)(nil)
	_ adapters.MigrationAdapter       = (*PostgresAdapter)(nil)
	_ adapters.SchemaProvider         = (*PostgresAdapter)(nil)
)

// ListStreams returns a list of stream summaries for CLI display.
func (a *PostgresAdapter) ListStreams(ctx context.Context, prefix string, limit int) ([]adapters.StreamSummary, error) {
	if a.closed {
		return nil, ErrAdapterClosed
	}

	schemaQ := a.schemaQ
	query := `
		SELECT 
			s.stream_id,
			s.version as event_count,
			COALESCE((SELECT event_type FROM ` + schemaQ + `.events e WHERE e.stream_id = s.stream_id ORDER BY version DESC LIMIT 1), '') as last_event_type,
			s.updated_at as last_updated
		FROM ` + schemaQ + `.streams s
	`

	args := []interface{}{}
	argNum := 1

	if prefix != "" {
		query += fmt.Sprintf(" WHERE s.stream_id LIKE $%d", argNum)
		args = append(args, prefix+"%")
	}

	query += " ORDER BY s.updated_at DESC"

	if limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", limit)
	}

	rows, err := a.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("mink/postgres: failed to list streams: %w", err)
	}
	defer rows.Close()

	streams := make([]adapters.StreamSummary, 0)
	for rows.Next() {
		var s adapters.StreamSummary
		if err := rows.Scan(&s.StreamID, &s.EventCount, &s.LastEventType, &s.LastUpdated); err != nil {
			return nil, fmt.Errorf("mink/postgres: failed to scan stream: %w", err)
		}
		streams = append(streams, s)
	}

	return streams, rows.Err()
}

// GetStreamEvents returns events from a stream with pagination for CLI display.
func (a *PostgresAdapter) GetStreamEvents(ctx context.Context, streamID string, fromVersion int64, limit int) ([]adapters.StoredEvent, error) {
	if a.closed {
		return nil, ErrAdapterClosed
	}

	schemaQ := a.schemaQ
	rows, err := a.db.QueryContext(ctx, `
		SELECT event_id, stream_id, version, event_type, data, COALESCE(metadata, '{}'), global_position, timestamp
		FROM `+schemaQ+`.events
		WHERE stream_id = $1 AND version > $2
		ORDER BY version
		LIMIT $3
	`, streamID, fromVersion, limit)
	if err != nil {
		return nil, fmt.Errorf("mink/postgres: failed to get stream events: %w", err)
	}
	defer rows.Close()

	return a.scanEvents(rows)
}

// GetEventStoreStats returns aggregate statistics about the event store.
func (a *PostgresAdapter) GetEventStoreStats(ctx context.Context) (*adapters.EventStoreStats, error) {
	if a.closed {
		return nil, ErrAdapterClosed
	}

	schemaQ := a.schemaQ
	stats := &adapters.EventStoreStats{}

	// Total events
	_ = a.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+schemaQ+".events").Scan(&stats.TotalEvents)

	// Total streams
	_ = a.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+schemaQ+".streams").Scan(&stats.TotalStreams)

	// Event types
	_ = a.db.QueryRowContext(ctx, "SELECT COUNT(DISTINCT event_type) FROM "+schemaQ+".events").Scan(&stats.EventTypes)

	// Average events per stream
	if stats.TotalStreams > 0 {
		stats.AvgEventsPerStream = float64(stats.TotalEvents) / float64(stats.TotalStreams)
	}

	// Top event types
	rows, err := a.db.QueryContext(ctx, `
		SELECT event_type, COUNT(*) as count
		FROM `+schemaQ+`.events
		GROUP BY event_type
		ORDER BY count DESC
		LIMIT 5
	`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var t adapters.EventTypeCount
			if rows.Scan(&t.Type, &t.Count) == nil {
				stats.TopEventTypes = append(stats.TopEventTypes, t)
			}
		}
	}

	return stats, nil
}

// ListProjections returns all registered projections.
func (a *PostgresAdapter) ListProjections(ctx context.Context) ([]adapters.ProjectionInfo, error) {
	if a.closed {
		return nil, ErrAdapterClosed
	}

	schemaQ := a.schemaQ
	// Ensure checkpoints table has status column
	_, _ = a.db.ExecContext(ctx, `ALTER TABLE `+schemaQ+`.checkpoints ADD COLUMN IF NOT EXISTS status VARCHAR(50) DEFAULT 'active'`)

	rows, err := a.db.QueryContext(ctx, `
		SELECT projection_name, position, COALESCE(status, 'active'), updated_at 
		FROM `+schemaQ+`.checkpoints 
		ORDER BY projection_name
	`)
	if err != nil {
		return nil, fmt.Errorf("mink/postgres: failed to list projections: %w", err)
	}
	defer rows.Close()

	projections := make([]adapters.ProjectionInfo, 0)
	for rows.Next() {
		var p adapters.ProjectionInfo
		if err := rows.Scan(&p.Name, &p.Position, &p.Status, &p.UpdatedAt); err != nil {
			return nil, fmt.Errorf("mink/postgres: failed to scan projection: %w", err)
		}
		projections = append(projections, p)
	}

	return projections, rows.Err()
}

// GetProjection returns information about a specific projection.
func (a *PostgresAdapter) GetProjection(ctx context.Context, name string) (*adapters.ProjectionInfo, error) {
	if a.closed {
		return nil, ErrAdapterClosed
	}

	schemaQ := a.schemaQ
	var p adapters.ProjectionInfo
	err := a.db.QueryRowContext(ctx, `
		SELECT projection_name, position, COALESCE(status, 'active'), updated_at 
		FROM `+schemaQ+`.checkpoints 
		WHERE projection_name = $1
	`, name).Scan(&p.Name, &p.Position, &p.Status, &p.UpdatedAt)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("mink/postgres: failed to get projection: %w", err)
	}

	return &p, nil
}

// SetProjectionStatus updates a projection's status.
func (a *PostgresAdapter) SetProjectionStatus(ctx context.Context, name string, status string) error {
	if a.closed {
		return ErrAdapterClosed
	}

	schemaQ := a.schemaQ
	result, err := a.db.ExecContext(ctx, `
		UPDATE `+schemaQ+`.checkpoints 
		SET status = $1, updated_at = NOW() 
		WHERE projection_name = $2
	`, status, name)
	if err != nil {
		return fmt.Errorf("mink/postgres: failed to set projection status: %w", err)
	}

	affected, _ := result.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("mink/postgres: projection '%s' not found", name)
	}

	return nil
}

// ResetProjectionCheckpoint resets a projection's position to 0 for rebuild.
func (a *PostgresAdapter) ResetProjectionCheckpoint(ctx context.Context, name string) error {
	if a.closed {
		return ErrAdapterClosed
	}

	schemaQ := a.schemaQ
	_, err := a.db.ExecContext(ctx, `
		UPDATE `+schemaQ+`.checkpoints 
		SET position = 0, updated_at = NOW() 
		WHERE projection_name = $1
	`, name)
	if err != nil {
		return fmt.Errorf("mink/postgres: failed to reset checkpoint: %w", err)
	}

	return nil
}

// GetTotalEventCount returns the highest global position.
func (a *PostgresAdapter) GetTotalEventCount(ctx context.Context) (int64, error) {
	if a.closed {
		return 0, ErrAdapterClosed
	}

	schemaQ := a.schemaQ
	var count int64
	err := a.db.QueryRowContext(ctx, "SELECT COALESCE(MAX(global_position), 0) FROM "+schemaQ+".events").Scan(&count)
	return count, err
}

// GetAppliedMigrations returns the list of applied migration names.
func (a *PostgresAdapter) GetAppliedMigrations(ctx context.Context) ([]string, error) {
	if a.closed {
		return nil, ErrAdapterClosed
	}

	schemaQ := a.schemaQ
	// Ensure migrations table exists
	_, err := a.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS `+schemaQ+`.migrations (
			name VARCHAR(255) PRIMARY KEY,
			applied_at TIMESTAMPTZ DEFAULT NOW()
		)
	`)
	if err != nil {
		return nil, fmt.Errorf("mink/postgres: failed to create migrations table: %w", err)
	}

	rows, err := a.db.QueryContext(ctx, "SELECT name FROM "+schemaQ+".migrations ORDER BY name")
	if err != nil {
		return nil, fmt.Errorf("mink/postgres: failed to query migrations: %w", err)
	}
	defer rows.Close()

	names := make([]string, 0)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("mink/postgres: failed to scan migration: %w", err)
		}
		names = append(names, name)
	}

	return names, rows.Err()
}

// RecordMigration marks a migration as applied.
func (a *PostgresAdapter) RecordMigration(ctx context.Context, name string) error {
	if a.closed {
		return ErrAdapterClosed
	}

	schemaQ := a.schemaQ
	_, err := a.db.ExecContext(ctx, "INSERT INTO "+schemaQ+".migrations (name) VALUES ($1)", name)
	if err != nil {
		return fmt.Errorf("mink/postgres: failed to record migration: %w", err)
	}

	return nil
}

// RemoveMigrationRecord removes a migration record (for rollback).
func (a *PostgresAdapter) RemoveMigrationRecord(ctx context.Context, name string) error {
	if a.closed {
		return ErrAdapterClosed
	}

	schemaQ := a.schemaQ
	_, err := a.db.ExecContext(ctx, "DELETE FROM "+schemaQ+".migrations WHERE name = $1", name)
	if err != nil {
		return fmt.Errorf("mink/postgres: failed to remove migration record: %w", err)
	}

	return nil
}

// ExecuteSQL runs arbitrary SQL (for applying migrations).
func (a *PostgresAdapter) ExecuteSQL(ctx context.Context, sql string) error {
	if a.closed {
		return ErrAdapterClosed
	}

	_, err := a.db.ExecContext(ctx, sql)
	if err != nil {
		return fmt.Errorf("mink/postgres: failed to execute SQL: %w", err)
	}

	return nil
}

// GenerateSchema returns the DDL for the PostgreSQL event store schema.
func (a *PostgresAdapter) GenerateSchema(projectName, tableName, snapshotTableName, outboxTableName string) string {
	return fmt.Sprintf(`-- Mink Event Store Schema (PostgreSQL)
-- Generated for: %s

-- Events table
CREATE TABLE IF NOT EXISTS %s (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    stream_id VARCHAR(255) NOT NULL,
    version BIGINT NOT NULL,
    type VARCHAR(255) NOT NULL,
    data JSONB NOT NULL,
    metadata JSONB DEFAULT '{}',
    global_position BIGSERIAL,
    timestamp TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE(stream_id, version)
);

-- Indexes for events
CREATE INDEX IF NOT EXISTS idx_%s_stream_id ON %s(stream_id, version);
CREATE INDEX IF NOT EXISTS idx_%s_global_position ON %s(global_position);
CREATE INDEX IF NOT EXISTS idx_%s_type ON %s(type);
CREATE INDEX IF NOT EXISTS idx_%s_timestamp ON %s(timestamp);

-- Snapshots table
CREATE TABLE IF NOT EXISTS %s (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    stream_id VARCHAR(255) NOT NULL,
    version BIGINT NOT NULL,
    type VARCHAR(255) NOT NULL,
    data JSONB NOT NULL,
    timestamp TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE(stream_id)
);

CREATE INDEX IF NOT EXISTS idx_%s_stream_id ON %s(stream_id);

-- Projection checkpoints
CREATE TABLE IF NOT EXISTS mink_checkpoints (
    projection_name VARCHAR(255) PRIMARY KEY,
    position BIGINT NOT NULL DEFAULT 0,
    status VARCHAR(50) DEFAULT 'active',
    updated_at TIMESTAMPTZ DEFAULT NOW()
);

-- Outbox table (transactional outbox for reliable messaging)
CREATE TABLE IF NOT EXISTS %s (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    aggregate_id VARCHAR(255) NOT NULL,
    event_type VARCHAR(255) NOT NULL,
    destination VARCHAR(255) NOT NULL,
    payload JSONB NOT NULL,
    headers JSONB DEFAULT '{}',
    status INT NOT NULL DEFAULT 0,
    attempts INT NOT NULL DEFAULT 0,
    max_attempts INT NOT NULL DEFAULT 5,
    last_error TEXT,
    scheduled_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_attempt_at TIMESTAMPTZ,
    processed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Partial index for efficient polling of pending messages
CREATE INDEX IF NOT EXISTS idx_%s_pending ON %s(scheduled_at) WHERE status = 0;
-- Index for dead letter queries
CREATE INDEX IF NOT EXISTS idx_%s_dead_letter ON %s(created_at) WHERE status = 4;

-- Migrations tracking
CREATE TABLE IF NOT EXISTS mink_migrations (
    name VARCHAR(255) PRIMARY KEY,
    applied_at TIMESTAMPTZ DEFAULT NOW()
);

-- Idempotency keys
CREATE TABLE IF NOT EXISTS mink_idempotency (
    key VARCHAR(255) PRIMARY KEY,
    result JSONB,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    expires_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_mink_idempotency_expires ON mink_idempotency(expires_at);

-- Function for appending events with optimistic concurrency
CREATE OR REPLACE FUNCTION mink_append_events(
    p_stream_id VARCHAR(255),
    p_expected_version BIGINT,
    p_events JSONB
) RETURNS TABLE(
    id UUID,
    stream_id VARCHAR(255),
    version BIGINT,
    type VARCHAR(255),
    data JSONB,
    metadata JSONB,
    global_position BIGINT,
    timestamp TIMESTAMPTZ
) AS $$
DECLARE
    v_current_version BIGINT;
    v_event JSONB;
    v_version BIGINT;
BEGIN
    -- Lock and get current version
    SELECT COALESCE(MAX(e.version), 0) INTO v_current_version
    FROM %s e
    WHERE e.stream_id = p_stream_id
    FOR UPDATE;

    -- Check expected version
    -- -1 = AnyVersion, 0 = NoStream, -2 = StreamExists
    IF p_expected_version >= 0 AND v_current_version != p_expected_version THEN
        RAISE EXCEPTION 'mink: concurrency conflict on stream %% expected %% got %%',
            p_stream_id, p_expected_version, v_current_version;
    ELSIF p_expected_version = 0 AND v_current_version > 0 THEN
        RAISE EXCEPTION 'mink: stream %% already exists', p_stream_id;
    ELSIF p_expected_version = -2 AND v_current_version = 0 THEN
        RAISE EXCEPTION 'mink: stream %% does not exist', p_stream_id;
    END IF;

    v_version := v_current_version;

    -- Insert events
    FOR v_event IN SELECT * FROM jsonb_array_elements(p_events)
    LOOP
        v_version := v_version + 1;

        INSERT INTO %s (stream_id, version, type, data, metadata)
        VALUES (
            p_stream_id,
            v_version,
            v_event->>'type',
            v_event->'data',
            COALESCE(v_event->'metadata', '{}'::jsonb)
        )
        RETURNING
            %s.id,
            %s.stream_id,
            %s.version,
            %s.type,
            %s.data,
            %s.metadata,
            %s.global_position,
            %s.timestamp
        INTO id, stream_id, version, type, data, metadata, global_position, timestamp;

        RETURN NEXT;
    END LOOP;
END;
$$ LANGUAGE plpgsql;

COMMENT ON TABLE %s IS 'Mink event store - immutable event log';
COMMENT ON TABLE %s IS 'Aggregate snapshots for optimization';
COMMENT ON TABLE mink_checkpoints IS 'Projection checkpoint positions';
COMMENT ON TABLE %s IS 'Transactional outbox for reliable messaging';
`,
		projectName,
		tableName,
		tableName, tableName,
		tableName, tableName,
		tableName, tableName,
		tableName, tableName,
		snapshotTableName,
		snapshotTableName, snapshotTableName,
		outboxTableName,
		outboxTableName, outboxTableName,
		outboxTableName, outboxTableName,
		tableName,
		tableName,
		tableName,
		tableName,
		tableName,
		tableName,
		tableName,
		tableName,
		tableName,
		tableName,
		tableName,
		snapshotTableName,
		outboxTableName,
	)
}

// ============================================================================
// DiagnosticAdapter Implementation
// ============================================================================

// GetDiagnosticInfo returns database version and connection status.
func (a *PostgresAdapter) GetDiagnosticInfo(ctx context.Context) (*adapters.DiagnosticInfo, error) {
	if a.closed {
		return nil, ErrAdapterClosed
	}

	info := &adapters.DiagnosticInfo{}

	// Ping to check connection
	if err := a.db.PingContext(ctx); err != nil {
		info.Connected = false
		info.Message = err.Error()
		return info, nil
	}

	info.Connected = true

	// Get PostgreSQL version
	var version string
	err := a.db.QueryRowContext(ctx, "SELECT version()").Scan(&version)
	if err != nil {
		info.Message = "Connected but couldn't get version"
		return info, nil
	}

	// Truncate version if too long
	if len(version) > 60 {
		version = version[:60] + "..."
	}
	info.Version = version
	info.Message = "Connected successfully"

	return info, nil
}

// CheckSchema verifies the event store schema exists.
func (a *PostgresAdapter) CheckSchema(ctx context.Context, tableName string) (*adapters.SchemaCheckResult, error) {
	if a.closed {
		return nil, ErrAdapterClosed
	}

	result := &adapters.SchemaCheckResult{}

	// Check if events table exists
	var exists bool
	err := a.db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables 
			WHERE table_name = $1
		)
	`, tableName).Scan(&exists)

	if err != nil {
		return nil, fmt.Errorf("failed to check schema: %w", err)
	}

	result.TableExists = exists

	if !exists {
		result.Message = fmt.Sprintf("Table '%s' not found", tableName)
		return result, nil
	}

	// Count events - use quoted identifier if needed
	var count int64
	query := fmt.Sprintf("SELECT COUNT(*) FROM %s", quoteIdentifier(tableName))
	err = a.db.QueryRowContext(ctx, query).Scan(&count)
	if err != nil {
		result.Message = fmt.Sprintf("Table exists but couldn't count events: %v", err)
		return result, nil
	}

	result.EventCount = count
	result.Message = fmt.Sprintf("Table exists (%d events)", count)

	return result, nil
}

// GetProjectionHealth returns projection health status.
func (a *PostgresAdapter) GetProjectionHealth(ctx context.Context) (*adapters.ProjectionHealthResult, error) {
	if a.closed {
		return nil, ErrAdapterClosed
	}

	result := &adapters.ProjectionHealthResult{}

	// Check if checkpoints table exists
	var exists bool
	err := a.db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables 
			WHERE table_name = 'mink_checkpoints'
		)
	`).Scan(&exists)

	if err != nil {
		return nil, fmt.Errorf("failed to check checkpoints table: %w", err)
	}

	if !exists {
		result.Message = "No projections registered yet"
		return result, nil
	}

	// Count total projections
	err = a.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM mink_checkpoints").Scan(&result.TotalProjections)
	if err != nil {
		return nil, fmt.Errorf("failed to count projections: %w", err)
	}

	// Get max position from events
	eventsTable := quoteQualifiedTable(a.schema, "events")
	err = a.db.QueryRowContext(ctx,
		fmt.Sprintf("SELECT COALESCE(MAX(global_position), 0) FROM %s", eventsTable),
	).Scan(&result.MaxPosition)
	if err != nil {
		// If events table doesn't exist, that's ok
		result.MaxPosition = 0
	}

	// Count projections behind
	if result.MaxPosition > 0 {
		err = a.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM mink_checkpoints WHERE position < $1",
			result.MaxPosition,
		).Scan(&result.ProjectionsBehind)
		if err != nil {
			result.ProjectionsBehind = 0
		}
	}

	if result.ProjectionsBehind > 0 {
		result.Message = fmt.Sprintf("%d/%d projections behind", result.ProjectionsBehind, result.TotalProjections)
	} else {
		result.Message = fmt.Sprintf("%d projections, all up to date", result.TotalProjections)
	}

	return result, nil
}
