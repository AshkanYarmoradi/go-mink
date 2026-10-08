// Package testutil provides utilities for integration testing.
// It provides helpers for connecting to test infrastructure and
// waiting for services to be ready.
package testutil

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// DefaultPostgresURL is the TEST-ONLY PostgreSQL DSN used by DefaultConfig when
// TEST_DATABASE_URL is unset. It targets the disposable database started by
// the repository's docker-compose.test.yml (`make infra-up`): the well-known
// postgres/postgres credentials and sslmode=disable are intentional for that
// throw-away local/CI instance and must never be reused for, or pointed at, a
// shared or production server. Set TEST_DATABASE_URL to override it.
const DefaultPostgresURL = "postgres://postgres:postgres@localhost:5432/mink_test?sslmode=disable"

// TestConfig holds configuration for test infrastructure.
type TestConfig struct {
	PostgresURL string
	// Future adapters:
	// MongoURL    string
	// RedisURL    string
}

// DefaultConfig returns the default test configuration from environment variables.
//
// PostgresURL is taken from TEST_DATABASE_URL when that variable is set and
// non-empty; otherwise it falls back to DefaultPostgresURL, a test-only DSN
// for the docker-compose test database (see DefaultPostgresURL for why those
// hard-coded credentials are acceptable there and nowhere else). Integration
// tests in this repository use the same TEST_DATABASE_URL variable to decide
// whether to run at all, so exporting it both selects the database and
// enables the tests.
func DefaultConfig() *TestConfig {
	return &TestConfig{
		PostgresURL: getEnvOrDefault("TEST_DATABASE_URL", DefaultPostgresURL),
	}
}

// getEnvOrDefault returns environment variable value or default.
func getEnvOrDefault(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}

// PostgresDB returns a database connection for PostgreSQL testing.
// It waits for the database to be ready with retries.
func PostgresDB(ctx context.Context, connStr string) (*sql.DB, error) {
	var db *sql.DB
	var err error

	// Retry connection with backoff
	for i := 0; i < 30; i++ {
		db, err = sql.Open("pgx", connStr)
		if err != nil {
			time.Sleep(time.Second)
			continue
		}

		// Test the connection
		pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		err = db.PingContext(pingCtx)
		cancel()

		if err == nil {
			return db, nil
		}

		time.Sleep(time.Second)
	}

	return nil, fmt.Errorf("testutil: failed to connect to postgres after retries: %w", err)
}

// MustPostgresDB returns a database connection or panics.
func MustPostgresDB(ctx context.Context, connStr string) *sql.DB {
	db, err := PostgresDB(ctx, connStr)
	if err != nil {
		panic(err)
	}
	return db
}

// quoteIdentifier quotes a PostgreSQL identifier using double quotes.
func quoteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// CleanupSchema drops a schema and all its objects.
// The schema name should come from UniqueSchema() which generates safe names.
func CleanupSchema(ctx context.Context, db *sql.DB, schema string) error {
	schemaQ := quoteIdentifier(schema)
	_, err := db.ExecContext(ctx, `DROP SCHEMA IF EXISTS `+schemaQ+` CASCADE`)
	return err
}

// UniqueSchema generates a unique schema name for testing by appending a
// nanosecond timestamp to prefix.
//
// The result is guaranteed to be a plain PostgreSQL identifier matching
// ^[A-Za-z_][A-Za-z0-9_]*$, so it is safe to interpolate into DDL (quoted or
// not). The caller-supplied prefix is sanitised rather than trusted:
//   - every character outside [A-Za-z0-9_] (punctuation, whitespace, quotes,
//     non-ASCII) is replaced with '_';
//   - a prefix whose first character is a digit gets a leading '_';
//   - an empty prefix becomes "test".
//
// A prefix that is already a valid identifier is used unchanged, so existing
// callers see identical output. Prefixes longer than ~43 characters will be
// truncated by PostgreSQL's 63-byte identifier limit; keep them short.
func UniqueSchema(prefix string) string {
	return fmt.Sprintf("%s_%d", sanitizeSchemaPrefix(prefix), time.Now().UnixNano())
}

// sanitizeSchemaPrefix maps an arbitrary string onto a safe identifier prefix
// as documented on UniqueSchema.
func sanitizeSchemaPrefix(prefix string) string {
	if prefix == "" {
		return "test"
	}
	var b strings.Builder
	b.Grow(len(prefix) + 1)
	for i, r := range prefix {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			if i == 0 {
				b.WriteByte('_')
			}
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}
