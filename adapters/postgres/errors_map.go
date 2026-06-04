package postgres

import (
	"errors"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"go-mink.dev/adapters"
)

// PostgreSQL SQLState error codes.
// See https://www.postgresql.org/docs/current/errcodes-appendix.html
const (
	// pgUniqueViolation is SQLState 23505, raised when an INSERT/UPDATE violates
	// a UNIQUE constraint or primary key.
	pgUniqueViolation = "23505"
)

// pgErrorCode returns the SQLState code of err if it (or anything it wraps) is
// a *pgconn.PgError, or an empty string otherwise.
func pgErrorCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// isUniqueViolation reports whether err is (or wraps) a PostgreSQL
// unique-violation (SQLState 23505). It falls back to a string match on the
// error text so that drivers or wrappers that do not surface a *pgconn.PgError
// are still classified correctly.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	if pgErrorCode(err) == pgUniqueViolation {
		return true
	}
	// Fallback for non-pgconn errors (e.g. other drivers/wrappers).
	msg := err.Error()
	return strings.Contains(msg, "duplicate key") ||
		strings.Contains(msg, "unique constraint")
}

// mapPostgresError translates a low-level PostgreSQL driver error into a
// domain sentinel error where a meaningful mapping exists, and returns the
// original error otherwise. It is a backstop to the explicit FOR UPDATE
// version pre-check in appendInTx: a unique-violation on the streams or events
// table (SQLState 23505) means another writer won the race, which is reported
// to callers as adapters.ErrConcurrencyConflict.
//
// The original error is wrapped so callers can still recover driver details
// via errors.As while errors.Is(err, adapters.ErrConcurrencyConflict) holds.
func mapPostgresError(err error) error {
	if err == nil {
		return nil
	}
	if isUniqueViolation(err) {
		return errors.Join(adapters.ErrConcurrencyConflict, err)
	}
	return err
}
