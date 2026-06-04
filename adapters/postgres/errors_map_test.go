package postgres

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"go-mink.dev/adapters"
)

// These tests are pure logic and need no database connection.

func TestPgErrorCode(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"plain error", errors.New("boom"), ""},
		{"direct pg error", &pgconn.PgError{Code: pgUniqueViolation}, pgUniqueViolation},
		{"wrapped pg error", fmt.Errorf("insert: %w", &pgconn.PgError{Code: "23503"}), "23503"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, pgErrorCode(tt.err))
		})
	}
}

func TestIsUniqueViolation(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"unrelated error", errors.New("connection refused"), false},
		{"pg unique violation", &pgconn.PgError{Code: pgUniqueViolation}, true},
		{"wrapped pg unique violation", fmt.Errorf("wrap: %w", &pgconn.PgError{Code: pgUniqueViolation}), true},
		{"pg foreign-key violation is not unique", &pgconn.PgError{Code: "23503"}, false},
		{"string fallback duplicate key", errors.New(`pq: duplicate key value violates unique constraint "x"`), true},
		{"string fallback unique constraint", errors.New("unique constraint failed"), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isUniqueViolation(tt.err))
		})
	}
}

func TestMapPostgresError(t *testing.T) {
	t.Run("nil passes through", func(t *testing.T) {
		assert.NoError(t, mapPostgresError(nil))
	})

	t.Run("unique violation maps to concurrency conflict", func(t *testing.T) {
		pgErr := &pgconn.PgError{Code: pgUniqueViolation, ConstraintName: "events_stream_id_version_key"}
		mapped := mapPostgresError(fmt.Errorf("insert event: %w", pgErr))

		// Sentinel is reachable via errors.Is.
		assert.True(t, errors.Is(mapped, adapters.ErrConcurrencyConflict),
			"mapped error should satisfy errors.Is(ErrConcurrencyConflict)")

		// Original driver details are still recoverable via errors.As.
		var recovered *pgconn.PgError
		assert.True(t, errors.As(mapped, &recovered),
			"original *pgconn.PgError should remain recoverable via errors.As")
		assert.Equal(t, "events_stream_id_version_key", recovered.ConstraintName)
	})

	t.Run("non-unique error passes through unchanged", func(t *testing.T) {
		orig := fmt.Errorf("deadlock: %w", &pgconn.PgError{Code: "40P01"})
		mapped := mapPostgresError(orig)
		assert.Equal(t, orig, mapped)
		assert.False(t, errors.Is(mapped, adapters.ErrConcurrencyConflict))
	})
}
