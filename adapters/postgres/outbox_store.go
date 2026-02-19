package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/AshkanYarmoradi/go-mink/adapters"
)

// Ensure interface compliance at compile time
var _ adapters.OutboxStore = (*OutboxStore)(nil)

// OutboxStore provides a PostgreSQL implementation of adapters.OutboxStore.
type OutboxStore struct {
	db     *sql.DB
	schema string
	table  string
}

// OutboxStoreOption configures an OutboxStore.
type OutboxStoreOption func(*OutboxStore)

// WithOutboxSchema sets the PostgreSQL schema for the outbox table.
func WithOutboxSchema(schema string) OutboxStoreOption {
	return func(s *OutboxStore) {
		s.schema = schema
	}
}

// WithOutboxTableName sets the table name for outbox records.
func WithOutboxTableName(table string) OutboxStoreOption {
	return func(s *OutboxStore) {
		s.table = table
	}
}

// NewOutboxStore creates a new PostgreSQL OutboxStore.
func NewOutboxStore(db *sql.DB, opts ...OutboxStoreOption) *OutboxStore {
	s := &OutboxStore{
		db:     db,
		schema: "public",
		table:  "mink_outbox",
	}

	for _, opt := range opts {
		opt(s)
	}

	return s
}

// NewOutboxStoreFromAdapter creates a new OutboxStore using an existing PostgresAdapter's connection.
func NewOutboxStoreFromAdapter(adapter *PostgresAdapter, opts ...OutboxStoreOption) *OutboxStore {
	allOpts := []OutboxStoreOption{
		WithOutboxSchema(adapter.schema),
	}
	allOpts = append(allOpts, opts...)
	return NewOutboxStore(adapter.db, allOpts...)
}

// fullTableName returns the fully qualified and quoted table name.
func (s *OutboxStore) fullTableName() string {
	return quoteQualifiedTable(s.schema, s.table)
}

// Initialize creates the outbox table if it doesn't exist.
func (s *OutboxStore) Initialize(ctx context.Context) error {
	if err := validateIdentifier(s.schema, "schema"); err != nil {
		return err
	}
	if err := validateIdentifier(s.table, "table"); err != nil {
		return err
	}

	tableQ := s.fullTableName()
	query := `
		CREATE TABLE IF NOT EXISTS ` + tableQ + ` (
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

		CREATE INDEX IF NOT EXISTS ` + quoteIdentifier("idx_"+s.table+"_pending") + ` ON ` + tableQ + ` (scheduled_at) WHERE status = 0;
		CREATE INDEX IF NOT EXISTS ` + quoteIdentifier("idx_"+s.table+"_dead_letter") + ` ON ` + tableQ + ` (created_at) WHERE status = 4;
	`

	_, err := s.db.ExecContext(ctx, query)
	if err != nil {
		return fmt.Errorf("mink/postgres/outbox: failed to create table: %w", err)
	}

	return nil
}

// Schedule stores outbox messages for later processing.
func (s *OutboxStore) Schedule(ctx context.Context, messages []*adapters.OutboxMessage) error {
	if len(messages) == 0 {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("mink/postgres/outbox: failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := s.insertMessages(ctx, tx, messages); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("mink/postgres/outbox: failed to commit: %w", err)
	}

	return nil
}

// ScheduleInTx stores outbox messages within an existing database transaction.
func (s *OutboxStore) ScheduleInTx(ctx context.Context, tx interface{}, messages []*adapters.OutboxMessage) error {
	if len(messages) == 0 {
		return nil
	}

	sqlTx, ok := tx.(*sql.Tx)
	if !ok {
		return fmt.Errorf("mink/postgres/outbox: tx must be *sql.Tx, got %T", tx)
	}

	return s.insertMessages(ctx, sqlTx, messages)
}

// insertMessages inserts outbox messages into the database within a transaction.
func (s *OutboxStore) insertMessages(ctx context.Context, tx *sql.Tx, messages []*adapters.OutboxMessage) error {
	tableQ := s.fullTableName()
	query := `
		INSERT INTO ` + tableQ + ` (
			aggregate_id, event_type, destination, payload, headers,
			status, attempts, max_attempts, scheduled_at, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING id
	`

	for _, msg := range messages {
		headersJSON, err := json.Marshal(msg.Headers)
		if err != nil {
			return fmt.Errorf("mink/postgres/outbox: failed to marshal headers: %w", err)
		}

		scheduledAt := msg.ScheduledAt
		if scheduledAt.IsZero() {
			scheduledAt = time.Now()
		}

		createdAt := msg.CreatedAt
		if createdAt.IsZero() {
			createdAt = time.Now()
		}

		maxAttempts := msg.MaxAttempts
		if maxAttempts == 0 {
			maxAttempts = 5
		}

		var id string
		err = tx.QueryRowContext(ctx, query,
			msg.AggregateID,
			msg.EventType,
			msg.Destination,
			msg.Payload,
			headersJSON,
			int(adapters.OutboxPending),
			0,
			maxAttempts,
			scheduledAt,
			createdAt,
		).Scan(&id)
		if err != nil {
			return fmt.Errorf("mink/postgres/outbox: failed to insert message: %w", err)
		}

		msg.ID = id
	}

	return nil
}

// FetchPending atomically claims up to limit pending messages for processing.
// Uses SELECT ... FOR UPDATE SKIP LOCKED to prevent double-claiming.
func (s *OutboxStore) FetchPending(ctx context.Context, limit int) ([]*adapters.OutboxMessage, error) {
	tableQ := s.fullTableName()
	query := `
		UPDATE ` + tableQ + ` SET
			status = $1,
			last_attempt_at = NOW(),
			attempts = attempts + 1
		WHERE id IN (
			SELECT id FROM ` + tableQ + `
			WHERE status = 0 AND scheduled_at <= NOW()
			ORDER BY scheduled_at
			LIMIT $2
			FOR UPDATE SKIP LOCKED
		)
		RETURNING id, aggregate_id, event_type, destination, payload, headers,
			status, attempts, max_attempts, last_error, scheduled_at,
			last_attempt_at, processed_at, created_at
	`

	rows, err := s.db.QueryContext(ctx, query, int(adapters.OutboxProcessing), limit)
	if err != nil {
		return nil, fmt.Errorf("mink/postgres/outbox: failed to fetch pending messages: %w", err)
	}
	defer rows.Close()

	return s.scanMessages(rows)
}

// MarkCompleted marks messages as successfully delivered.
func (s *OutboxStore) MarkCompleted(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}

	tableQ := s.fullTableName()

	// Build IN clause
	args := make([]interface{}, len(ids)+1)
	args[0] = int(adapters.OutboxCompleted)
	placeholders := make([]string, len(ids))
	for i, id := range ids {
		args[i+1] = id
		placeholders[i] = fmt.Sprintf("$%d", i+2)
	}

	query := `
		UPDATE ` + tableQ + ` SET
			status = $1,
			processed_at = NOW()
		WHERE id IN (` + joinStrings(placeholders, ", ") + `)
	`

	_, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("mink/postgres/outbox: failed to mark completed: %w", err)
	}

	return nil
}

// MarkFailed marks a message as failed with an error description.
func (s *OutboxStore) MarkFailed(ctx context.Context, id string, lastErr error) error {
	tableQ := s.fullTableName()
	query := `
		UPDATE ` + tableQ + ` SET
			status = $1,
			last_error = $2
		WHERE id = $3
	`

	errMsg := ""
	if lastErr != nil {
		errMsg = lastErr.Error()
	}

	result, err := s.db.ExecContext(ctx, query, int(adapters.OutboxFailed), errMsg, id)
	if err != nil {
		return fmt.Errorf("mink/postgres/outbox: failed to mark failed: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("mink/postgres/outbox: failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return adapters.ErrOutboxMessageNotFound
	}

	return nil
}

// RetryFailed resets eligible failed messages (below maxAttempts) to pending.
func (s *OutboxStore) RetryFailed(ctx context.Context, maxAttempts int) (int64, error) {
	tableQ := s.fullTableName()
	query := `
		UPDATE ` + tableQ + ` SET
			status = $1
		WHERE status = $2 AND attempts < $3
	`

	result, err := s.db.ExecContext(ctx, query,
		int(adapters.OutboxPending),
		int(adapters.OutboxFailed),
		maxAttempts,
	)
	if err != nil {
		return 0, fmt.Errorf("mink/postgres/outbox: failed to retry failed messages: %w", err)
	}

	return result.RowsAffected()
}

// MoveToDeadLetter transitions messages that exceeded maxAttempts to dead letter.
func (s *OutboxStore) MoveToDeadLetter(ctx context.Context, maxAttempts int) (int64, error) {
	tableQ := s.fullTableName()
	query := `
		UPDATE ` + tableQ + ` SET
			status = $1
		WHERE status = $2 AND attempts >= $3
	`

	result, err := s.db.ExecContext(ctx, query,
		int(adapters.OutboxDeadLetter),
		int(adapters.OutboxFailed),
		maxAttempts,
	)
	if err != nil {
		return 0, fmt.Errorf("mink/postgres/outbox: failed to move to dead letter: %w", err)
	}

	return result.RowsAffected()
}

// GetDeadLetterMessages retrieves dead-lettered messages.
func (s *OutboxStore) GetDeadLetterMessages(ctx context.Context, limit int) ([]*adapters.OutboxMessage, error) {
	tableQ := s.fullTableName()
	query := `
		SELECT id, aggregate_id, event_type, destination, payload, headers,
			status, attempts, max_attempts, last_error, scheduled_at,
			last_attempt_at, processed_at, created_at
		FROM ` + tableQ + `
		WHERE status = $1
		ORDER BY created_at DESC
		LIMIT $2
	`

	rows, err := s.db.QueryContext(ctx, query, int(adapters.OutboxDeadLetter), limit)
	if err != nil {
		return nil, fmt.Errorf("mink/postgres/outbox: failed to get dead letter messages: %w", err)
	}
	defer rows.Close()

	return s.scanMessages(rows)
}

// Cleanup removes old completed messages.
func (s *OutboxStore) Cleanup(ctx context.Context, olderThan time.Duration) (int64, error) {
	tableQ := s.fullTableName()
	cutoff := time.Now().Add(-olderThan)

	query := `
		DELETE FROM ` + tableQ + `
		WHERE status = $1 AND processed_at IS NOT NULL AND processed_at < $2
	`

	result, err := s.db.ExecContext(ctx, query, int(adapters.OutboxCompleted), cutoff)
	if err != nil {
		return 0, fmt.Errorf("mink/postgres/outbox: failed to cleanup: %w", err)
	}

	return result.RowsAffected()
}

// Close releases any resources (no-op as db is shared).
func (s *OutboxStore) Close() error {
	return nil
}

// messageScanTargets holds the temporary scan targets for reading an OutboxMessage row.
type messageScanTargets struct {
	headersJSON   []byte
	status        int
	lastError     sql.NullString
	lastAttemptAt sql.NullTime
	processedAt   sql.NullTime
}

// scanDest returns the ordered list of scan destinations matching the SELECT column order.
func (t *messageScanTargets) scanDest(msg *adapters.OutboxMessage) []interface{} {
	return []interface{}{
		&msg.ID, &msg.AggregateID, &msg.EventType, &msg.Destination,
		&msg.Payload, &t.headersJSON, &t.status, &msg.Attempts,
		&msg.MaxAttempts, &t.lastError, &msg.ScheduledAt,
		&t.lastAttemptAt, &t.processedAt, &msg.CreatedAt,
	}
}

// populate applies the scanned nullable fields onto the message.
func (t *messageScanTargets) populate(msg *adapters.OutboxMessage) error {
	msg.Status = adapters.OutboxStatus(t.status)
	msg.LastError = t.lastError.String
	if t.lastAttemptAt.Valid {
		msg.LastAttemptAt = &t.lastAttemptAt.Time
	}
	if t.processedAt.Valid {
		msg.ProcessedAt = &t.processedAt.Time
	}
	if len(t.headersJSON) > 0 && string(t.headersJSON) != "null" {
		if err := json.Unmarshal(t.headersJSON, &msg.Headers); err != nil {
			return fmt.Errorf("mink/postgres/outbox: failed to unmarshal headers: %w", err)
		}
	}
	return nil
}

// scanMessages scans rows into OutboxMessage slice.
func (s *OutboxStore) scanMessages(rows *sql.Rows) ([]*adapters.OutboxMessage, error) {
	var messages []*adapters.OutboxMessage

	for rows.Next() {
		msg := &adapters.OutboxMessage{}
		var targets messageScanTargets

		if err := rows.Scan(targets.scanDest(msg)...); err != nil {
			return nil, fmt.Errorf("mink/postgres/outbox: failed to scan message: %w", err)
		}
		if err := targets.populate(msg); err != nil {
			return nil, err
		}

		messages = append(messages, msg)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("mink/postgres/outbox: error iterating rows: %w", err)
	}

	return messages, nil
}

// DB returns the underlying database connection (for testing).
func (s *OutboxStore) DB() *sql.DB {
	return s.db
}

// get retrieves a single outbox message by ID.
func (s *OutboxStore) get(ctx context.Context, id string) (*adapters.OutboxMessage, error) {
	tableQ := s.fullTableName()
	query := `
		SELECT id, aggregate_id, event_type, destination, payload, headers,
			status, attempts, max_attempts, last_error, scheduled_at,
			last_attempt_at, processed_at, created_at
		FROM ` + tableQ + `
		WHERE id = $1
	`

	msg := &adapters.OutboxMessage{}
	var targets messageScanTargets

	err := s.db.QueryRowContext(ctx, query, id).Scan(targets.scanDest(msg)...)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, adapters.ErrOutboxMessageNotFound
		}
		return nil, fmt.Errorf("mink/postgres/outbox: failed to get message: %w", err)
	}

	if err := targets.populate(msg); err != nil {
		return nil, err
	}

	return msg, nil
}
