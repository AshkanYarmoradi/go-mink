package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"go-mink.dev"
)

// Ensure interface compliance at compile time.
var (
	_ mink.SubjectIndexAdapter = (*SubjectIndex)(nil)
	_ mink.SubjectIndexWriter  = (*SubjectIndex)(nil)
)

// SubjectIndex is a PostgreSQL-backed subject index implementing both the read
// (mink.SubjectIndexAdapter) and write (mink.SubjectIndexWriter) sides. It records which
// streams touch each data subject in a mink_subject_index table so a SubjectResolver can
// resolve a subject's footprint in O(the subject's streams) instead of scanning the whole
// event store. Populate it at append time (mink.WithSubjectIndexWriter) and/or backfill
// history with mink.BackfillSubjectIndex; wire it into a resolver with
// mink.WithResolverIndex. The table is created only when Initialize is called, so it is a
// no-forced-schema-change opt-in.
type SubjectIndex struct {
	db     *sql.DB
	schema string
	table  string
}

// SubjectIndexOption configures a SubjectIndex.
type SubjectIndexOption func(*SubjectIndex)

// WithSubjectIndexSchema sets the PostgreSQL schema for the subject-index table.
func WithSubjectIndexSchema(schema string) SubjectIndexOption {
	return func(s *SubjectIndex) { s.schema = schema }
}

// WithSubjectIndexTable sets the table name for the subject index.
func WithSubjectIndexTable(table string) SubjectIndexOption {
	return func(s *SubjectIndex) { s.table = table }
}

// NewSubjectIndex creates a PostgreSQL subject index. Call Initialize to create the table.
func NewSubjectIndex(db *sql.DB, opts ...SubjectIndexOption) *SubjectIndex {
	s := &SubjectIndex{db: db, schema: "public", table: "mink_subject_index"}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// NewSubjectIndexFromAdapter creates a SubjectIndex sharing an adapter's connection and
// schema. Callers must invoke Initialize(ctx) to create the table before use.
func NewSubjectIndexFromAdapter(adapter *PostgresAdapter, opts ...SubjectIndexOption) *SubjectIndex {
	all := []SubjectIndexOption{WithSubjectIndexSchema(adapter.schema)}
	all = append(all, opts...)
	return NewSubjectIndex(adapter.db, all...)
}

func (s *SubjectIndex) fullTableName() string {
	return quoteQualifiedTable(s.schema, s.table)
}

// Initialize creates the subject-index table if it doesn't exist. Safe to call repeatedly.
func (s *SubjectIndex) Initialize(ctx context.Context) error {
	if err := validateIdentifier(s.schema, "schema"); err != nil {
		return err
	}
	if err := validateIdentifier(s.table, "table"); err != nil {
		return err
	}
	tableQ := s.fullTableName()
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS ` + tableQ + ` (
			subject_id TEXT NOT NULL,
			stream_id  TEXT NOT NULL,
			PRIMARY KEY (subject_id, stream_id)
		)`,
		`CREATE INDEX IF NOT EXISTS ` + quoteIdentifier("idx_"+s.table+"_subject") + ` ON ` + tableQ + ` (subject_id)`,
	}
	if _, err := s.db.ExecContext(ctx, joinSQLStatements(stmts)); err != nil {
		return fmt.Errorf("mink/postgres/subjectindex: failed to create table: %w", err)
	}
	return nil
}

// IndexSubjects records that streamID contains events for each subject. It is idempotent
// (ON CONFLICT DO NOTHING). Implements mink.SubjectIndexWriter.
func (s *SubjectIndex) IndexSubjects(ctx context.Context, streamID string, subjectIDs []string) error {
	if streamID == "" || len(subjectIDs) == 0 {
		return nil
	}
	tableQ := s.fullTableName()

	args := make([]interface{}, 0, len(subjectIDs)*2)
	var b strings.Builder
	b.WriteString(`INSERT INTO ` + tableQ + ` (subject_id, stream_id) VALUES `)
	n := 0
	for _, sub := range subjectIDs {
		if sub == "" {
			continue
		}
		if n > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, "($%d,$%d)", n*2+1, n*2+2)
		args = append(args, sub, streamID)
		n++
	}
	if n == 0 {
		return nil
	}
	b.WriteString(` ON CONFLICT (subject_id, stream_id) DO NOTHING`)

	if _, err := s.db.ExecContext(ctx, b.String(), args...); err != nil {
		return fmt.Errorf("mink/postgres/subjectindex: failed to index subjects for stream %q: %w", streamID, err)
	}
	return nil
}

// StreamsBySubject returns the streams recorded for subjectID (sorted, deduplicated).
// Implements mink.SubjectIndexAdapter.
func (s *SubjectIndex) StreamsBySubject(ctx context.Context, subjectID string) ([]string, error) {
	tableQ := s.fullTableName()
	rows, err := s.db.QueryContext(ctx,
		`SELECT stream_id FROM `+tableQ+` WHERE subject_id = $1 ORDER BY stream_id`, subjectID)
	if err != nil {
		return nil, fmt.Errorf("mink/postgres/subjectindex: query streams for subject %q: %w", subjectID, err)
	}
	defer func() { _ = rows.Close() }()

	var streams []string
	for rows.Next() {
		var sid string
		if err := rows.Scan(&sid); err != nil {
			return nil, fmt.Errorf("mink/postgres/subjectindex: scan stream: %w", err)
		}
		streams = append(streams, sid)
	}
	return streams, rows.Err()
}
