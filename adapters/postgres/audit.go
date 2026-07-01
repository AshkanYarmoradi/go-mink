package postgres

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go-mink.dev/adapters"
)

// Ensure interface compliance at compile time
var (
	_ adapters.AuditStore         = (*AuditStore)(nil)
	_ adapters.SubjectAuditPurger = (*AuditStore)(nil)
)

const (
	// defaultAuditFindLimit caps Find when the query specifies no positive limit.
	defaultAuditFindLimit = 100
	// maxAuditFindLimit caps Find regardless of the requested limit.
	maxAuditFindLimit = 10000
)

// AuditStore provides a PostgreSQL implementation of mink.AuditStore.
type AuditStore struct {
	db     *sql.DB
	schema string
	table  string
}

// AuditStoreOption configures an AuditStore.
type AuditStoreOption func(*AuditStore)

// WithAuditSchema sets the PostgreSQL schema for the audit table.
func WithAuditSchema(schema string) AuditStoreOption {
	return func(s *AuditStore) {
		s.schema = schema
	}
}

// WithAuditTable sets the table name for audit entries.
func WithAuditTable(table string) AuditStoreOption {
	return func(s *AuditStore) {
		s.table = table
	}
}

// NewAuditStore creates a new PostgreSQL AuditStore.
func NewAuditStore(db *sql.DB, opts ...AuditStoreOption) *AuditStore {
	s := &AuditStore{
		db:     db,
		schema: "public",
		table:  "mink_audit",
	}

	for _, opt := range opts {
		opt(s)
	}

	return s
}

// NewAuditStoreFromAdapter creates a new AuditStore using an existing
// PostgresAdapter's connection.
//
// The returned store does not share the adapter's migrations: PostgresAdapter.Migrate
// does NOT create the audit table. Callers must invoke Initialize(ctx) on the
// returned AuditStore to create its table before use.
func NewAuditStoreFromAdapter(adapter *PostgresAdapter, opts ...AuditStoreOption) *AuditStore {
	allOpts := []AuditStoreOption{
		WithAuditSchema(adapter.schema),
	}
	allOpts = append(allOpts, opts...)
	return NewAuditStore(adapter.db, allOpts...)
}

// fullTableName returns the fully qualified and quoted table name.
func (s *AuditStore) fullTableName() string {
	return quoteQualifiedTable(s.schema, s.table)
}

// Initialize creates the audit table if it doesn't exist.
func (s *AuditStore) Initialize(ctx context.Context) error {
	if err := validateIdentifier(s.schema, "schema"); err != nil {
		return err
	}
	if err := validateIdentifier(s.table, "table"); err != nil {
		return err
	}

	_, err := s.db.ExecContext(ctx, joinSQLStatements(auditSchemaStatements(s.schema, s.table)))
	if err != nil {
		return fmt.Errorf("mink/postgres/audit: failed to create table: %w", err)
	}

	return nil
}

// Close is a no-op; the store does not own the *sql.DB.
func (s *AuditStore) Close() error {
	return nil
}

// Append writes a single audit entry.
func (s *AuditStore) Append(ctx context.Context, entry *adapters.AuditEntry) error {
	if entry == nil {
		return adapters.ErrNilAuditEntry
	}
	tableQ := s.fullTableName()

	// Build the column list. The id and timestamp columns have DB defaults, but
	// we set them explicitly when provided so generated IDs/timestamps round-trip.
	query := `
		INSERT INTO ` + tableQ + ` (
			id, timestamp, command_type, command_id, aggregate_id, version,
			actor, tenant_id, correlation_id, causation_id, success, error,
			duration_ms, metadata
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
	`

	// The id column is a NOT NULL UUID. Its gen_random_uuid() default only
	// applies when the column is omitted from the INSERT; a bound NULL parameter
	// would violate the constraint. So generate one here when the caller did not
	// provide an ID.
	idVal := entry.ID
	if idVal == "" {
		idVal = newAuditUUID()
	}

	tsVal := entry.Timestamp
	if tsVal.IsZero() {
		tsVal = time.Now()
	}

	var metadataVal interface{}
	if len(entry.Metadata) > 0 {
		raw, err := json.Marshal(entry.Metadata)
		if err != nil {
			return fmt.Errorf("mink/postgres/audit: failed to marshal metadata: %w", err)
		}
		if json.Valid(raw) {
			metadataVal = raw
		}
	}

	_, err := s.db.ExecContext(ctx, query,
		idVal,
		tsVal,
		entry.CommandType,
		nullableString(entry.CommandID),
		nullableString(entry.AggregateID),
		nullableInt64(entry.Version),
		nullableString(entry.Actor),
		nullableString(entry.TenantID),
		nullableString(entry.CorrelationID),
		nullableString(entry.CausationID),
		entry.Success,
		nullableString(entry.Error),
		entry.DurationMs,
		metadataVal,
	)
	if err != nil {
		return fmt.Errorf("mink/postgres/audit: failed to append entry: %w", err)
	}

	return nil
}

// buildWhere accumulates parameterized WHERE conditions for a query.
// It returns the WHERE clause (including the leading " WHERE " or empty) and
// the ordered argument slice.
func buildAuditWhere(q adapters.AuditQuery) (string, []interface{}) {
	var conds []string
	var args []interface{}

	add := func(fragment string, value interface{}) {
		args = append(args, value)
		conds = append(conds, fmt.Sprintf(fragment, len(args)))
	}

	if q.CommandType != "" {
		add("command_type = $%d", q.CommandType)
	}
	if q.Actor != "" {
		add("actor = $%d", q.Actor)
	}
	if q.TenantID != "" {
		add("tenant_id = $%d", q.TenantID)
	}
	if q.AggregateID != "" {
		add("aggregate_id = $%d", q.AggregateID)
	}
	if q.CorrelationID != "" {
		add("correlation_id = $%d", q.CorrelationID)
	}
	if !q.From.IsZero() {
		add("timestamp >= $%d", q.From)
	}
	if !q.To.IsZero() {
		add("timestamp < $%d", q.To)
	}
	if q.Success != nil {
		add("success = $%d", *q.Success)
	}

	if len(conds) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// Find returns audit entries matching the query, honoring Order/Limit/Offset.
func (s *AuditStore) Find(ctx context.Context, q adapters.AuditQuery) ([]*adapters.AuditEntry, error) {
	tableQ := s.fullTableName()
	where, args := buildAuditWhere(q)

	order := "DESC"
	if q.Order == adapters.AuditOrderTimestampAsc {
		order = "ASC"
	}

	limit := q.Limit
	if limit <= 0 {
		limit = defaultAuditFindLimit
	}
	if limit > maxAuditFindLimit {
		limit = maxAuditFindLimit
	}

	// Tie-break on id so pagination (LIMIT/OFFSET) is deterministic even when
	// multiple entries share the same timestamp.
	query := `
		SELECT id, timestamp, command_type, command_id, aggregate_id, version,
			actor, tenant_id, correlation_id, causation_id, success, error,
			duration_ms, metadata
		FROM ` + tableQ + where +
		" ORDER BY timestamp " + order + ", id " + order +
		" LIMIT " + strconv.Itoa(limit)
	if q.Offset > 0 {
		query += " OFFSET " + strconv.Itoa(q.Offset)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("mink/postgres/audit: failed to query entries: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var entries []*adapters.AuditEntry
	for rows.Next() {
		entry, scanErr := scanAuditEntry(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("mink/postgres/audit: row iteration failed: %w", err)
	}

	return entries, nil
}

// scanAuditEntry scans a single row into an AuditEntry.
func scanAuditEntry(rows *sql.Rows) (*adapters.AuditEntry, error) {
	var (
		entry         adapters.AuditEntry
		commandID     sql.NullString
		aggregateID   sql.NullString
		version       sql.NullInt64
		actor         sql.NullString
		tenantID      sql.NullString
		correlationID sql.NullString
		causationID   sql.NullString
		errorMsg      sql.NullString
		metadata      []byte
	)

	if err := rows.Scan(
		&entry.ID,
		&entry.Timestamp,
		&entry.CommandType,
		&commandID,
		&aggregateID,
		&version,
		&actor,
		&tenantID,
		&correlationID,
		&causationID,
		&entry.Success,
		&errorMsg,
		&entry.DurationMs,
		&metadata,
	); err != nil {
		return nil, fmt.Errorf("mink/postgres/audit: failed to scan entry: %w", err)
	}

	entry.CommandID = commandID.String
	entry.AggregateID = aggregateID.String
	if version.Valid {
		entry.Version = version.Int64
	}
	entry.Actor = actor.String
	entry.TenantID = tenantID.String
	entry.CorrelationID = correlationID.String
	entry.CausationID = causationID.String
	entry.Error = errorMsg.String

	if len(metadata) > 0 {
		if err := json.Unmarshal(metadata, &entry.Metadata); err != nil {
			return nil, fmt.Errorf("mink/postgres/audit: failed to unmarshal metadata for entry %s: %w", entry.ID, err)
		}
	}

	return &entry, nil
}

// Count returns the number of entries matching the query, ignoring Limit/Offset.
func (s *AuditStore) Count(ctx context.Context, q adapters.AuditQuery) (int64, error) {
	tableQ := s.fullTableName()
	where, args := buildAuditWhere(q)

	query := `SELECT COUNT(*) FROM ` + tableQ + where

	var count int64
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
		return 0, fmt.Errorf("mink/postgres/audit: failed to count entries: %w", err)
	}

	return count, nil
}

// Cleanup removes entries with a timestamp older than olderThan ago.
// Returns the number of entries removed.
func (s *AuditStore) Cleanup(ctx context.Context, olderThan time.Duration) (int64, error) {
	cutoff := time.Now().Add(-olderThan)

	tableQ := s.fullTableName()
	query := `DELETE FROM ` + tableQ + ` WHERE timestamp < $1`

	result, err := s.db.ExecContext(ctx, query, cutoff)
	if err != nil {
		return 0, fmt.Errorf("mink/postgres/audit: failed to cleanup entries: %w", err)
	}

	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("mink/postgres/audit: failed to get affected rows: %w", err)
	}

	return count, nil
}

// DeleteAuditBySubject removes audit entries attributable to subjectID — those whose
// actor or aggregate_id equals subjectID — for GDPR erasure of the audit trail. Returns
// the number removed. Implements adapters.SubjectAuditPurger.
func (s *AuditStore) DeleteAuditBySubject(ctx context.Context, subjectID string) (int64, error) {
	if subjectID == "" {
		return 0, nil
	}
	tableQ := s.fullTableName()
	query := `DELETE FROM ` + tableQ + ` WHERE actor = $1 OR aggregate_id = $1`

	result, err := s.db.ExecContext(ctx, query, subjectID)
	if err != nil {
		return 0, fmt.Errorf("mink/postgres/audit: failed to delete entries by subject: %w", err)
	}

	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("mink/postgres/audit: failed to get affected rows: %w", err)
	}

	return count, nil
}

// Clear removes all entries from the store.
// Useful for testing.
func (s *AuditStore) Clear(ctx context.Context) error {
	tableQ := s.fullTableName()
	query := `TRUNCATE TABLE ` + tableQ

	_, err := s.db.ExecContext(ctx, query)
	if err != nil {
		return fmt.Errorf("mink/postgres/audit: failed to clear table: %w", err)
	}

	return nil
}

// newAuditUUID returns a random RFC 4122 version 4 UUID string. It is used when
// an audit entry is appended without an ID, since the column's gen_random_uuid()
// default does not apply to a bound NULL parameter.
func newAuditUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand should never fail; derive best-effort bytes from the clock so
		// the result is still a syntactically valid v4 UUID for the UUID column.
		now := uint64(time.Now().UnixNano())
		binary.BigEndian.PutUint64(b[0:8], now)
		binary.BigEndian.PutUint64(b[8:16], now^0x9e3779b97f4a7c15)
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return hex.EncodeToString(b[0:4]) + "-" +
		hex.EncodeToString(b[4:6]) + "-" +
		hex.EncodeToString(b[6:8]) + "-" +
		hex.EncodeToString(b[8:10]) + "-" +
		hex.EncodeToString(b[10:16])
}

// nullableString returns nil for empty strings so they store as SQL NULL.
func nullableString(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// nullableInt64 returns nil for zero so it stores as SQL NULL.
func nullableInt64(v int64) interface{} {
	if v == 0 {
		return nil
	}
	return v
}
