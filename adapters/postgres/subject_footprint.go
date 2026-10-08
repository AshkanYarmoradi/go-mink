package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	mink "go-mink.dev"
	"go-mink.dev/adapters"
)

// Ensure the footprint-aware GDPR extensions are implemented at compile time.
var (
	_ adapters.SubjectOutboxFootprintPurger      = (*OutboxStore)(nil)
	_ adapters.SubjectOutboxCounter              = (*OutboxStore)(nil)
	_ adapters.SubjectAuditFootprintPurger       = (*AuditStore)(nil)
	_ adapters.SubjectAuditCounter               = (*AuditStore)(nil)
	_ adapters.SubjectIdempotencyFootprintPurger = (*IdempotencyStore)(nil)
	_ adapters.SubjectIdempotencyCounter         = (*IdempotencyStore)(nil)
	_ adapters.SubjectSagaFootprintPurger        = (*SagaStore)(nil)
	_ adapters.SubjectSagaCounter                = (*SagaStore)(nil)
	_ adapters.SagaCorrelationTypeFinder         = (*SagaStore)(nil)
)

// dedupeIDs returns ids with empty strings dropped and duplicates removed,
// preserving first-seen order. It returns nil when nothing remains, so callers
// can short-circuit with (0, nil) without touching the database.
func dedupeIDs(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// textArrayLiteral renders ids as a PostgreSQL array literal ({"a","b"}) for use
// as a single bound parameter in `col = ANY($n::text[])`.
//
// The literal form is used rather than a driver-native []string because the
// stores accept any *sql.DB: pgx encodes a Go string into any text-format type
// (including text[]) and lib/pq sends every parameter as text, so a literal with
// an explicit ::text[] cast works identically under both drivers, whereas a bare
// []string is rejected by lib/pq. Every element is double-quoted with backslash
// and double-quote escaped, so ids containing commas, braces, quotes, spaces or
// the word NULL are matched literally.
func textArrayLiteral(ids []string) string {
	var b strings.Builder
	b.Grow(len(ids) * 16)
	b.WriteByte('{')
	for i, id := range ids {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('"')
		for j := 0; j < len(id); j++ {
			c := id[j]
			if c == '\\' || c == '"' {
				b.WriteByte('\\')
			}
			b.WriteByte(c)
		}
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String()
}

// ---------------------------------------------------------------------------
// OutboxStore
// ---------------------------------------------------------------------------

// DeleteOutboxByAggregateIDs removes outbox messages whose aggregate_id equals any of
// aggregateIDs and returns the count removed. The outbox's aggregate_id column holds
// the producing STREAM id, so the mink eraser passes the subject's resolved footprint
// streams. An empty slice returns (0, nil) without touching the database. Implements
// adapters.SubjectOutboxFootprintPurger.
func (s *OutboxStore) DeleteOutboxByAggregateIDs(ctx context.Context, aggregateIDs []string) (int64, error) {
	ids := dedupeIDs(aggregateIDs)
	if len(ids) == 0 {
		return 0, nil
	}
	result, err := s.db.ExecContext(ctx,
		`DELETE FROM `+s.fullTableName()+` WHERE aggregate_id = ANY($1::text[])`, textArrayLiteral(ids))
	if err != nil {
		return 0, fmt.Errorf("mink/postgres/outbox: failed to delete by aggregate ids: %w", err)
	}
	return result.RowsAffected()
}

// CountOutboxByAggregateIDs returns the number of outbox messages whose aggregate_id
// equals any of aggregateIDs (the producing STREAM ids). An empty slice returns
// (0, nil) without touching the database. Implements adapters.SubjectOutboxCounter.
func (s *OutboxStore) CountOutboxByAggregateIDs(ctx context.Context, aggregateIDs []string) (int64, error) {
	ids := dedupeIDs(aggregateIDs)
	if len(ids) == 0 {
		return 0, nil
	}
	var n int64
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM `+s.fullTableName()+` WHERE aggregate_id = ANY($1::text[])`, textArrayLiteral(ids)).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("mink/postgres/outbox: failed to count by aggregate ids: %w", err)
	}
	return n, nil
}

// ---------------------------------------------------------------------------
// AuditStore
// ---------------------------------------------------------------------------

// DeleteAuditByAggregateIDs removes audit entries whose aggregate_id equals any of
// aggregateIDs (the STREAM ids the audited commands targeted) and returns the count
// removed. An empty slice returns (0, nil) without touching the database. Implements
// adapters.SubjectAuditFootprintPurger.
func (s *AuditStore) DeleteAuditByAggregateIDs(ctx context.Context, aggregateIDs []string) (int64, error) {
	ids := dedupeIDs(aggregateIDs)
	if len(ids) == 0 {
		return 0, nil
	}
	result, err := s.db.ExecContext(ctx,
		`DELETE FROM `+s.fullTableName()+` WHERE aggregate_id = ANY($1::text[])`, textArrayLiteral(ids))
	if err != nil {
		return 0, fmt.Errorf("mink/postgres/audit: failed to delete entries by aggregate ids: %w", err)
	}
	return result.RowsAffected()
}

// CountAuditBySubject returns the number of audit entries whose actor equals
// subjectID OR whose aggregate_id equals any of aggregateIDs (the subject's resolved
// footprint STREAM ids). An empty subjectID disables the actor predicate and an
// empty slice disables the aggregate_id predicate; when both are empty it returns
// (0, nil) without touching the database. Implements adapters.SubjectAuditCounter.
func (s *AuditStore) CountAuditBySubject(ctx context.Context, subjectID string, aggregateIDs []string) (int64, error) {
	ids := dedupeIDs(aggregateIDs)
	if subjectID == "" && len(ids) == 0 {
		return 0, nil
	}

	var conds []string
	var args []interface{}
	if subjectID != "" {
		args = append(args, subjectID)
		conds = append(conds, fmt.Sprintf("actor = $%d", len(args)))
	}
	if len(ids) > 0 {
		args = append(args, textArrayLiteral(ids))
		conds = append(conds, fmt.Sprintf("aggregate_id = ANY($%d::text[])", len(args)))
	}

	var n int64
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM `+s.fullTableName()+` WHERE `+strings.Join(conds, " OR "), args...).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("mink/postgres/audit: failed to count entries by subject: %w", err)
	}
	return n, nil
}

// ---------------------------------------------------------------------------
// IdempotencyStore
// ---------------------------------------------------------------------------

// DeleteIdempotencyByAggregateIDs removes idempotency records whose aggregate_id
// equals any of aggregateIDs (the STREAM ids the commands affected) and returns the
// count removed. An empty slice returns (0, nil) without touching the database.
// Implements adapters.SubjectIdempotencyFootprintPurger.
func (s *IdempotencyStore) DeleteIdempotencyByAggregateIDs(ctx context.Context, aggregateIDs []string) (int64, error) {
	ids := dedupeIDs(aggregateIDs)
	if len(ids) == 0 {
		return 0, nil
	}
	result, err := s.db.ExecContext(ctx,
		`DELETE FROM `+s.fullTableName()+` WHERE aggregate_id = ANY($1::text[])`, textArrayLiteral(ids))
	if err != nil {
		return 0, fmt.Errorf("mink/postgres/idempotency: failed to delete records by aggregate ids: %w", err)
	}
	return result.RowsAffected()
}

// CountIdempotencyByAggregateIDs returns the number of idempotency records whose
// aggregate_id equals any of aggregateIDs. An empty slice returns (0, nil) without
// touching the database. Implements adapters.SubjectIdempotencyCounter.
func (s *IdempotencyStore) CountIdempotencyByAggregateIDs(ctx context.Context, aggregateIDs []string) (int64, error) {
	ids := dedupeIDs(aggregateIDs)
	if len(ids) == 0 {
		return 0, nil
	}
	var n int64
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM `+s.fullTableName()+` WHERE aggregate_id = ANY($1::text[])`, textArrayLiteral(ids)).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("mink/postgres/idempotency: failed to count records by aggregate ids: %w", err)
	}
	return n, nil
}

// ---------------------------------------------------------------------------
// SagaStore
// ---------------------------------------------------------------------------

// DeleteSagasByCorrelationIDs removes saga states whose correlation_id equals any of
// correlationIDs and returns the count removed. An empty slice returns (0, nil)
// without touching the database. Implements adapters.SubjectSagaFootprintPurger.
func (s *SagaStore) DeleteSagasByCorrelationIDs(ctx context.Context, correlationIDs []string) (int64, error) {
	ids := dedupeIDs(correlationIDs)
	if len(ids) == 0 {
		return 0, nil
	}
	result, err := s.db.ExecContext(ctx,
		`DELETE FROM `+s.fullTableName()+` WHERE correlation_id = ANY($1::text[])`, textArrayLiteral(ids))
	if err != nil {
		return 0, fmt.Errorf("mink/postgres/saga: failed to delete sagas by correlation ids: %w", err)
	}
	return result.RowsAffected()
}

// CountSagasByCorrelationIDs returns the number of saga states whose correlation_id
// equals any of correlationIDs. An empty slice returns (0, nil) without touching the
// database. Implements adapters.SubjectSagaCounter.
func (s *SagaStore) CountSagasByCorrelationIDs(ctx context.Context, correlationIDs []string) (int64, error) {
	ids := dedupeIDs(correlationIDs)
	if len(ids) == 0 {
		return 0, nil
	}
	var n int64
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM `+s.fullTableName()+` WHERE correlation_id = ANY($1::text[])`, textArrayLiteral(ids)).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("mink/postgres/saga: failed to count sagas by correlation ids: %w", err)
	}
	return n, nil
}

// FindByCorrelationIDAndType returns the most recently started saga whose
// correlation_id equals correlationID AND whose type equals sagaType. Unlike
// FindByCorrelationID it never returns a saga of a different type that happens to
// share the correlation id, so a type-B saga cannot hydrate from (and overwrite) a
// type-A row. Returns a SagaNotFoundError (errors.Is ErrSagaNotFound) when no such
// saga exists. Implements adapters.SagaCorrelationTypeFinder.
func (s *SagaStore) FindByCorrelationIDAndType(ctx context.Context, correlationID, sagaType string) (*mink.SagaState, error) {
	if correlationID == "" {
		return nil, errors.New("mink/postgres/saga: correlation ID is required")
	}
	if sagaType == "" {
		return nil, errors.New("mink/postgres/saga: saga type is required")
	}

	query := `
		SELECT id, type, correlation_id, status, current_step,
			data, processed_events, steps, failure_reason, started_at, updated_at,
			completed_at, version
		FROM ` + s.fullTableName() + `
		WHERE correlation_id = $1 AND type = $2
		ORDER BY started_at DESC
		LIMIT 1
	`

	var scanner sagaRowScanner
	err := s.db.QueryRowContext(ctx, query, correlationID, sagaType).Scan(scanner.scanTargets()...)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, &mink.SagaNotFoundError{CorrelationID: correlationID}
		}
		return nil, fmt.Errorf("mink/postgres/saga: failed to find saga by correlation id and type: %w", err)
	}

	return scanner.toSagaState()
}
