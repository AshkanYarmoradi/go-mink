package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/AshkanYarmoradi/go-mink/adapters"
)

// Ensure interface compliance at compile time
var _ adapters.IdempotencyStore = (*IdempotencyStore)(nil)

// IdempotencyStore provides a PostgreSQL implementation of mink.IdempotencyStore.
type IdempotencyStore struct {
	db     *sql.DB
	schema string
	table  string
}

// IdempotencyStoreOption configures an IdempotencyStore
type IdempotencyStoreOption func(*IdempotencyStore)

// WithIdempotencySchema sets the PostgreSQL schema for the idempotency table.
func WithIdempotencySchema(schema string) IdempotencyStoreOption {
	return func(s *IdempotencyStore) {
		s.schema = schema
	}
}

// WithIdempotencyTable sets the table name for idempotency records.
func WithIdempotencyTable(table string) IdempotencyStoreOption {
	return func(s *IdempotencyStore) {
		s.table = table
	}
}

// NewIdempotencyStore creates a new PostgreSQL IdempotencyStore.
func NewIdempotencyStore(db *sql.DB, opts ...IdempotencyStoreOption) *IdempotencyStore {
	s := &IdempotencyStore{
		db:     db,
		schema: "public",
		table:  "mink_idempotency",
	}

	for _, opt := range opts {
		opt(s)
	}

	return s
}

// NewIdempotencyStoreFromAdapter creates a new IdempotencyStore using an existing PostgresAdapter's connection.
func NewIdempotencyStoreFromAdapter(adapter *PostgresAdapter, opts ...IdempotencyStoreOption) *IdempotencyStore {
	// Start with adapter's schema as default
	allOpts := []IdempotencyStoreOption{
		WithIdempotencySchema(adapter.schema),
	}
	allOpts = append(allOpts, opts...)
	return NewIdempotencyStore(adapter.db, allOpts...)
}

// validateIdentifier checks if a name is a valid PostgreSQL identifier.
// This helps prevent SQL injection when using identifiers in queries.
func validateIdentifier(name, kind string) error {
	if name == "" {
		return fmt.Errorf("mink/postgres/idempotency: %s name cannot be empty", kind)
	}
	if len(name) > 63 {
		return fmt.Errorf("mink/postgres/idempotency: %s name exceeds 63 characters", kind)
	}
	if !schemaNamePattern.MatchString(name) {
		return fmt.Errorf("mink/postgres/idempotency: %s name contains invalid characters", kind)
	}
	return nil
}

// fullTableName returns the fully qualified and quoted table name.
func (s *IdempotencyStore) fullTableName() string {
	return quoteQualifiedTable(s.schema, s.table)
}

// Initialize creates the idempotency table if it doesn't exist.
func (s *IdempotencyStore) Initialize(ctx context.Context) error {
	// Validate schema and table names to prevent SQL injection
	if err := validateIdentifier(s.schema, "schema"); err != nil {
		return err
	}
	if err := validateIdentifier(s.table, "table"); err != nil {
		return err
	}

	tableQ := s.fullTableName()
	query := `
		CREATE TABLE IF NOT EXISTS ` + tableQ + ` (
			key VARCHAR(255) PRIMARY KEY,
			command_type VARCHAR(255) NOT NULL,
			aggregate_id VARCHAR(255),
			version BIGINT,
			response JSONB,
			error TEXT,
			success BOOLEAN NOT NULL DEFAULT false,
			processed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			expires_at TIMESTAMPTZ NOT NULL
		);

		CREATE INDEX IF NOT EXISTS idx_` + s.table + `_expires_at ON ` + tableQ + ` (expires_at);
		CREATE INDEX IF NOT EXISTS idx_` + s.table + `_processed_at ON ` + tableQ + ` (processed_at);
	`

	_, err := s.db.ExecContext(ctx, query)
	if err != nil {
		return fmt.Errorf("mink/postgres/idempotency: failed to create table: %w", err)
	}

	return nil
}

// Exists checks if a record with the given key exists and is not expired.
func (s *IdempotencyStore) Exists(ctx context.Context, key string) (bool, error) {
	tableQ := s.fullTableName()
	query := `
		SELECT EXISTS(
			SELECT 1 FROM ` + tableQ + ` 
			WHERE key = $1 AND expires_at > NOW()
		)
	`

	var exists bool
	err := s.db.QueryRowContext(ctx, query, key).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("mink/postgres/idempotency: failed to check existence: %w", err)
	}

	return exists, nil
}

// Store saves a new idempotency record, using upsert to handle conflicts.
func (s *IdempotencyStore) Store(ctx context.Context, record *adapters.IdempotencyRecord) error {
	var responseJSON []byte
	var err error

	if record.Response != nil {
		// Validate that response is valid JSON
		var js json.RawMessage
		if err = json.Unmarshal(record.Response, &js); err == nil {
			responseJSON = record.Response
		}
	}

	tableQ := s.fullTableName()
	query := `
		INSERT INTO ` + tableQ + ` (
			key, command_type, aggregate_id, version, response, error, success, processed_at, expires_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (key) DO UPDATE SET
			command_type = EXCLUDED.command_type,
			aggregate_id = EXCLUDED.aggregate_id,
			version = EXCLUDED.version,
			response = EXCLUDED.response,
			error = EXCLUDED.error,
			success = EXCLUDED.success,
			processed_at = EXCLUDED.processed_at,
			expires_at = EXCLUDED.expires_at
	`

	var nullableResponse interface{}
	if responseJSON != nil {
		nullableResponse = responseJSON
	}

	var nullableAggregateID interface{}
	if record.AggregateID != "" {
		nullableAggregateID = record.AggregateID
	}

	var nullableVersion interface{}
	if record.Version != 0 {
		nullableVersion = record.Version
	}

	var nullableError interface{}
	if record.Error != "" {
		nullableError = record.Error
	}

	_, err = s.db.ExecContext(ctx, query,
		record.Key,
		record.CommandType,
		nullableAggregateID,
		nullableVersion,
		nullableResponse,
		nullableError,
		record.Success,
		record.ProcessedAt,
		record.ExpiresAt,
	)
	if err != nil {
		return fmt.Errorf("mink/postgres/idempotency: failed to store record: %w", err)
	}

	return nil
}

// Get retrieves an idempotency record by key.
// Returns nil, nil if the record doesn't exist or is expired.
func (s *IdempotencyStore) Get(ctx context.Context, key string) (*adapters.IdempotencyRecord, error) {
	tableQ := s.fullTableName()
	query := `
		SELECT key, command_type, aggregate_id, version, response, error, success, processed_at, expires_at
		FROM ` + tableQ + `
		WHERE key = $1 AND expires_at > NOW()
	`

	var record adapters.IdempotencyRecord
	var aggregateID sql.NullString
	var version sql.NullInt64
	var response []byte
	var errorMsg sql.NullString

	err := s.db.QueryRowContext(ctx, query, key).Scan(
		&record.Key,
		&record.CommandType,
		&aggregateID,
		&version,
		&response,
		&errorMsg,
		&record.Success,
		&record.ProcessedAt,
		&record.ExpiresAt,
	)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("mink/postgres/idempotency: failed to get record: %w", err)
	}

	if aggregateID.Valid {
		record.AggregateID = aggregateID.String
	}
	if version.Valid {
		record.Version = version.Int64
	}
	if response != nil {
		// Validate that the response is valid JSON before returning
		if !json.Valid(response) {
			return nil, fmt.Errorf("mink/postgres/idempotency: invalid JSON in response for key %s", key)
		}
		record.Response = response
	}
	if errorMsg.Valid {
		record.Error = errorMsg.String
	}

	return &record, nil
}

// Delete removes an idempotency record by key.
func (s *IdempotencyStore) Delete(ctx context.Context, key string) error {
	tableQ := s.fullTableName()
	query := `DELETE FROM ` + tableQ + ` WHERE key = $1`

	_, err := s.db.ExecContext(ctx, query, key)
	if err != nil {
		return fmt.Errorf("mink/postgres/idempotency: failed to delete record: %w", err)
	}

	return nil
}

// Cleanup removes records older than the specified duration.
// Returns the number of records deleted.
func (s *IdempotencyStore) Cleanup(ctx context.Context, olderThan time.Duration) (int64, error) {
	cutoff := time.Now().Add(-olderThan)

	tableQ := s.fullTableName()
	query := `
		DELETE FROM ` + tableQ + ` 
		WHERE processed_at < $1 OR expires_at < NOW()
	`

	result, err := s.db.ExecContext(ctx, query, cutoff)
	if err != nil {
		return 0, fmt.Errorf("mink/postgres/idempotency: failed to cleanup records: %w", err)
	}

	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("mink/postgres/idempotency: failed to get affected rows: %w", err)
	}

	return count, nil
}

// Count returns the total number of records in the store.
// Useful for testing and monitoring.
func (s *IdempotencyStore) Count(ctx context.Context) (int64, error) {
	tableQ := s.fullTableName()
	query := `SELECT COUNT(*) FROM ` + tableQ

	var count int64
	err := s.db.QueryRowContext(ctx, query).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("mink/postgres/idempotency: failed to count records: %w", err)
	}

	return count, nil
}

// Clear removes all records from the store.
// Useful for testing.
func (s *IdempotencyStore) Clear(ctx context.Context) error {
	tableQ := s.fullTableName()
	query := `TRUNCATE TABLE ` + tableQ

	_, err := s.db.ExecContext(ctx, query)
	if err != nil {
		return fmt.Errorf("mink/postgres/idempotency: failed to clear table: %w", err)
	}

	return nil
}
