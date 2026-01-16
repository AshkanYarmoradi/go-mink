package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/AshkanYarmoradi/go-mink"
)

// Ensure interface compliance at compile time
var _ mink.SagaStore = (*SagaStore)(nil)

// SagaStore provides a PostgreSQL implementation of mink.SagaStore.
type SagaStore struct {
	db     *sql.DB
	schema string
	table  string
}

// SagaStoreOption configures a SagaStore.
type SagaStoreOption func(*SagaStore)

// WithSagaSchema sets the PostgreSQL schema for the saga table.
func WithSagaSchema(schema string) SagaStoreOption {
	return func(s *SagaStore) {
		s.schema = schema
	}
}

// WithSagaTable sets the table name for saga records.
func WithSagaTable(table string) SagaStoreOption {
	return func(s *SagaStore) {
		s.table = table
	}
}

// NewSagaStore creates a new PostgreSQL SagaStore.
func NewSagaStore(db *sql.DB, opts ...SagaStoreOption) *SagaStore {
	s := &SagaStore{
		db:     db,
		schema: "public",
		table:  "mink_sagas",
	}

	for _, opt := range opts {
		opt(s)
	}

	return s
}

// NewSagaStoreFromAdapter creates a new SagaStore using an existing PostgresAdapter's connection.
func NewSagaStoreFromAdapter(adapter *PostgresAdapter, opts ...SagaStoreOption) *SagaStore {
	// Start with adapter's schema as default
	allOpts := []SagaStoreOption{
		WithSagaSchema(adapter.schema),
	}
	allOpts = append(allOpts, opts...)
	return NewSagaStore(adapter.db, allOpts...)
}

// fullTableName returns the fully qualified and quoted table name.
func (s *SagaStore) fullTableName() string {
	return quoteQualifiedTable(s.schema, s.table)
}

// Initialize creates the saga table if it doesn't exist.
func (s *SagaStore) Initialize(ctx context.Context) error {
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
			id VARCHAR(255) PRIMARY KEY,
			type VARCHAR(255) NOT NULL,
			correlation_id VARCHAR(255),
			status INT NOT NULL DEFAULT 0,
			current_step INT NOT NULL DEFAULT 0,
			data JSONB,
			steps JSONB,
			failure_reason TEXT,
			started_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			completed_at TIMESTAMPTZ,
			version BIGINT NOT NULL DEFAULT 1
		);

		CREATE INDEX IF NOT EXISTS ` + quoteIdentifier("idx_"+s.table+"_correlation_id") + ` ON ` + tableQ + ` (correlation_id) WHERE correlation_id IS NOT NULL;
		CREATE INDEX IF NOT EXISTS ` + quoteIdentifier("idx_"+s.table+"_type") + ` ON ` + tableQ + ` (type);
		CREATE INDEX IF NOT EXISTS ` + quoteIdentifier("idx_"+s.table+"_status") + ` ON ` + tableQ + ` (status);
		CREATE INDEX IF NOT EXISTS ` + quoteIdentifier("idx_"+s.table+"_type_status") + ` ON ` + tableQ + ` (type, status);
	`

	_, err := s.db.ExecContext(ctx, query)
	if err != nil {
		return fmt.Errorf("mink/postgres/saga: failed to create table: %w", err)
	}

	return nil
}

// Save persists a saga state with optimistic concurrency control.
//
// Version semantics:
//   - Version 0: Creates a new saga. Uses INSERT.
//   - Version > 0: Updates an existing saga. Uses UPDATE with version check.
//     If the version doesn't match, returns ErrConcurrencyConflict.
//
// After a successful save, state.Version is incremented to reflect the new version.
func (s *SagaStore) Save(ctx context.Context, state *mink.SagaState) error {
	if state == nil {
		return errors.New("mink/postgres/saga: state is nil")
	}

	if state.ID == "" {
		return errors.New("mink/postgres/saga: saga ID is required")
	}

	// Serialize data and steps
	dataJSON, err := json.Marshal(state.Data)
	if err != nil {
		return fmt.Errorf("mink/postgres/saga: failed to marshal data: %w", err)
	}

	stepsJSON, err := json.Marshal(state.Steps)
	if err != nil {
		return fmt.Errorf("mink/postgres/saga: failed to marshal steps: %w", err)
	}

	tableQ := s.fullTableName()
	query := `
		INSERT INTO ` + tableQ + ` (
			id, type, correlation_id, status, current_step,
			data, steps, failure_reason, started_at, updated_at,
			completed_at, version
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8, $9, $10,
			$11, 1
		)
		ON CONFLICT (id) DO UPDATE SET
			type = EXCLUDED.type,
			correlation_id = EXCLUDED.correlation_id,
			status = EXCLUDED.status,
			current_step = EXCLUDED.current_step,
			data = EXCLUDED.data,
			steps = EXCLUDED.steps,
			failure_reason = EXCLUDED.failure_reason,
			updated_at = EXCLUDED.updated_at,
			completed_at = EXCLUDED.completed_at,
			version = ` + tableQ + `.version + 1
		WHERE ` + tableQ + `.version = $12
		RETURNING version
	`

	var newVersion int64
	err = s.db.QueryRowContext(ctx, query,
		state.ID,
		state.Type,
		nullString(state.CorrelationID),
		int(state.Status),
		state.CurrentStep,
		dataJSON,
		stepsJSON,
		nullString(state.FailureReason),
		state.StartedAt,
		time.Now(),
		nullTime(state.CompletedAt),
		state.Version,
	).Scan(&newVersion)

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Concurrency conflict - the version didn't match
			return fmt.Errorf("mink/postgres/saga: concurrency conflict for saga %q: %w",
				state.ID, mink.ErrConcurrencyConflict)
		}
		return fmt.Errorf("mink/postgres/saga: failed to save saga: %w", err)
	}

	state.Version = newVersion
	return nil
}

// Load retrieves a saga state by ID.
func (s *SagaStore) Load(ctx context.Context, sagaID string) (*mink.SagaState, error) {
	if sagaID == "" {
		return nil, errors.New("mink/postgres/saga: saga ID is required")
	}

	tableQ := s.fullTableName()
	query := `
		SELECT id, type, correlation_id, status, current_step,
			data, steps, failure_reason, started_at, updated_at,
			completed_at, version
		FROM ` + tableQ + `
		WHERE id = $1
	`

	var (
		state         mink.SagaState
		correlationID sql.NullString
		dataJSON      []byte
		stepsJSON     []byte
		failureReason sql.NullString
		completedAt   sql.NullTime
		status        int
	)

	err := s.db.QueryRowContext(ctx, query, sagaID).Scan(
		&state.ID,
		&state.Type,
		&correlationID,
		&status,
		&state.CurrentStep,
		&dataJSON,
		&stepsJSON,
		&failureReason,
		&state.StartedAt,
		&state.UpdatedAt,
		&completedAt,
		&state.Version,
	)

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, &mink.SagaNotFoundError{SagaID: sagaID}
		}
		return nil, fmt.Errorf("mink/postgres/saga: failed to load saga: %w", err)
	}

	state.Status = mink.SagaStatus(status)
	state.CorrelationID = correlationID.String
	state.FailureReason = failureReason.String

	if completedAt.Valid {
		state.CompletedAt = &completedAt.Time
	}

	if len(dataJSON) > 0 && string(dataJSON) != "null" {
		if err := json.Unmarshal(dataJSON, &state.Data); err != nil {
			return nil, fmt.Errorf("mink/postgres/saga: failed to unmarshal data: %w", err)
		}
	}

	if len(stepsJSON) > 0 && string(stepsJSON) != "null" {
		if err := json.Unmarshal(stepsJSON, &state.Steps); err != nil {
			return nil, fmt.Errorf("mink/postgres/saga: failed to unmarshal steps: %w", err)
		}
	}

	return &state, nil
}

// FindByCorrelationID finds a saga by its correlation ID.
func (s *SagaStore) FindByCorrelationID(ctx context.Context, correlationID string) (*mink.SagaState, error) {
	if correlationID == "" {
		return nil, errors.New("mink/postgres/saga: correlation ID is required")
	}

	tableQ := s.fullTableName()
	query := `
		SELECT id, type, correlation_id, status, current_step,
			data, steps, failure_reason, started_at, updated_at,
			completed_at, version
		FROM ` + tableQ + `
		WHERE correlation_id = $1
		ORDER BY started_at DESC
		LIMIT 1
	`

	var (
		state            mink.SagaState
		correlationIDCol sql.NullString
		dataJSON         []byte
		stepsJSON        []byte
		failureReason    sql.NullString
		completedAt      sql.NullTime
		status           int
	)

	err := s.db.QueryRowContext(ctx, query, correlationID).Scan(
		&state.ID,
		&state.Type,
		&correlationIDCol,
		&status,
		&state.CurrentStep,
		&dataJSON,
		&stepsJSON,
		&failureReason,
		&state.StartedAt,
		&state.UpdatedAt,
		&completedAt,
		&state.Version,
	)

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, &mink.SagaNotFoundError{CorrelationID: correlationID}
		}
		return nil, fmt.Errorf("mink/postgres/saga: failed to find saga: %w", err)
	}

	state.Status = mink.SagaStatus(status)
	state.CorrelationID = correlationIDCol.String
	state.FailureReason = failureReason.String

	if completedAt.Valid {
		state.CompletedAt = &completedAt.Time
	}

	if len(dataJSON) > 0 && string(dataJSON) != "null" {
		if err := json.Unmarshal(dataJSON, &state.Data); err != nil {
			return nil, fmt.Errorf("mink/postgres/saga: failed to unmarshal data: %w", err)
		}
	}

	if len(stepsJSON) > 0 && string(stepsJSON) != "null" {
		if err := json.Unmarshal(stepsJSON, &state.Steps); err != nil {
			return nil, fmt.Errorf("mink/postgres/saga: failed to unmarshal steps: %w", err)
		}
	}

	return &state, nil
}

// FindByType finds all sagas of a given type with the specified statuses.
func (s *SagaStore) FindByType(ctx context.Context, sagaType string, statuses ...mink.SagaStatus) ([]*mink.SagaState, error) {
	if sagaType == "" {
		return nil, errors.New("mink/postgres/saga: saga type is required")
	}

	tableQ := s.fullTableName()
	var query string
	var args []interface{}

	if len(statuses) == 0 {
		query = `
			SELECT id, type, correlation_id, status, current_step,
				data, steps, failure_reason, started_at, updated_at,
				completed_at, version
			FROM ` + tableQ + `
			WHERE type = $1
			ORDER BY started_at DESC
		`
		args = []interface{}{sagaType}
	} else {
		// Build IN clause for statuses
		statusInts := make([]interface{}, len(statuses))
		placeholders := make([]string, len(statuses))
		for i, status := range statuses {
			statusInts[i] = int(status)
			placeholders[i] = fmt.Sprintf("$%d", i+2)
		}

		query = `
			SELECT id, type, correlation_id, status, current_step,
				data, steps, failure_reason, started_at, updated_at,
				completed_at, version
			FROM ` + tableQ + `
			WHERE type = $1 AND status IN (` + joinStrings(placeholders, ", ") + `)
			ORDER BY started_at DESC
		`
		args = append([]interface{}{sagaType}, statusInts...)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("mink/postgres/saga: failed to find sagas: %w", err)
	}
	defer rows.Close()

	var sagas []*mink.SagaState
	for rows.Next() {
		var (
			state         mink.SagaState
			correlationID sql.NullString
			dataJSON      []byte
			stepsJSON     []byte
			failureReason sql.NullString
			completedAt   sql.NullTime
			status        int
		)

		if err := rows.Scan(
			&state.ID,
			&state.Type,
			&correlationID,
			&status,
			&state.CurrentStep,
			&dataJSON,
			&stepsJSON,
			&failureReason,
			&state.StartedAt,
			&state.UpdatedAt,
			&completedAt,
			&state.Version,
		); err != nil {
			return nil, fmt.Errorf("mink/postgres/saga: failed to scan row: %w", err)
		}

		state.Status = mink.SagaStatus(status)
		state.CorrelationID = correlationID.String
		state.FailureReason = failureReason.String

		if completedAt.Valid {
			state.CompletedAt = &completedAt.Time
		}

		if len(dataJSON) > 0 && string(dataJSON) != "null" {
			if err := json.Unmarshal(dataJSON, &state.Data); err != nil {
				return nil, fmt.Errorf("mink/postgres/saga: failed to unmarshal data: %w", err)
			}
		}

		if len(stepsJSON) > 0 && string(stepsJSON) != "null" {
			if err := json.Unmarshal(stepsJSON, &state.Steps); err != nil {
				return nil, fmt.Errorf("mink/postgres/saga: failed to unmarshal steps: %w", err)
			}
		}

		sagas = append(sagas, &state)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("mink/postgres/saga: error iterating rows: %w", err)
	}

	return sagas, nil
}

// Delete removes a saga state.
func (s *SagaStore) Delete(ctx context.Context, sagaID string) error {
	if sagaID == "" {
		return errors.New("mink/postgres/saga: saga ID is required")
	}

	tableQ := s.fullTableName()
	query := `DELETE FROM ` + tableQ + ` WHERE id = $1`

	result, err := s.db.ExecContext(ctx, query, sagaID)
	if err != nil {
		return fmt.Errorf("mink/postgres/saga: failed to delete saga: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("mink/postgres/saga: failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return &mink.SagaNotFoundError{SagaID: sagaID}
	}

	return nil
}

// Close releases any resources (no-op for this implementation as db is shared).
func (s *SagaStore) Close() error {
	// Don't close the DB connection as it may be shared
	return nil
}

// Cleanup removes expired or old completed sagas.
func (s *SagaStore) Cleanup(ctx context.Context, olderThan time.Duration) (int64, error) {
	tableQ := s.fullTableName()
	cutoff := time.Now().Add(-olderThan)

	query := `
		DELETE FROM ` + tableQ + `
		WHERE status IN ($1, $2, $3)
		AND (completed_at IS NOT NULL AND completed_at < $4)
	`

	result, err := s.db.ExecContext(ctx, query,
		int(mink.SagaStatusCompleted),
		int(mink.SagaStatusFailed),
		int(mink.SagaStatusCompensated),
		cutoff,
	)
	if err != nil {
		return 0, fmt.Errorf("mink/postgres/saga: failed to cleanup: %w", err)
	}

	return result.RowsAffected()
}

// CountByStatus returns the count of sagas by status.
func (s *SagaStore) CountByStatus(ctx context.Context) (map[mink.SagaStatus]int64, error) {
	tableQ := s.fullTableName()
	query := `
		SELECT status, COUNT(*) as count
		FROM ` + tableQ + `
		GROUP BY status
	`

	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("mink/postgres/saga: failed to count sagas: %w", err)
	}
	defer rows.Close()

	counts := make(map[mink.SagaStatus]int64)
	for rows.Next() {
		var status int
		var count int64
		if err := rows.Scan(&status, &count); err != nil {
			return nil, fmt.Errorf("mink/postgres/saga: failed to scan count: %w", err)
		}
		counts[mink.SagaStatus(status)] = count
	}

	return counts, rows.Err()
}

// Helper functions

func nullString(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}

func nullTime(t *time.Time) sql.NullTime {
	if t == nil {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: *t, Valid: true}
}

func joinStrings(strs []string, sep string) string {
	if len(strs) == 0 {
		return ""
	}
	result := strs[0]
	for i := 1; i < len(strs); i++ {
		result += sep + strs[i]
	}
	return result
}
