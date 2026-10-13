// Package testutil provides utilities for integration testing.
// It provides helpers for connecting to test infrastructure and
// waiting for services to be ready.
package testutil

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
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

// UniqueSchema generates a unique schema name for testing:
//
//	<prefix>_<UnixNano>_<nonce>_<n>
//
// where <UnixNano> is the current nanosecond timestamp (human-readable, and
// handy for spotting stale schemas), <nonce> is 8 lowercase hex digits drawn
// from crypto/rand once per process, and <n> is a process-wide atomic counter
// that increments on every call. The counter makes every name distinct within
// a process even when parallel tests call UniqueSchema in the same instant
// (two tests sharing a timestamp used to get the same name, and one cleanup
// then cascade-dropped the other's schema); the nonce keeps the test binaries
// `go test ./...` runs concurrently from colliding with each other. Uniqueness
// never depends on the clock's resolution.
//
// The result is guaranteed to be a plain PostgreSQL identifier matching
// ^[A-Za-z_][A-Za-z0-9_]*$ (lowercase letters only, so it is also safe
// unquoted), so it is safe to interpolate into DDL. The caller-supplied prefix
// is sanitised rather than trusted:
//   - every character outside [A-Za-z0-9_] (punctuation, whitespace, quotes,
//     non-ASCII) is replaced with '_';
//   - a prefix whose first character is a digit gets a leading '_';
//   - an empty prefix becomes "test".
//
// The result never exceeds 63 bytes (pgMaxIdentifierBytes, PostgreSQL's
// identifier limit): the sanitised prefix is truncated so that the complete
// suffix always fits. The suffix is 30 bytes plus the counter's digits, which
// leaves 32 bytes of prefix for the first nine names a process generates and
// never fewer than 28 bytes for its first 99,999 (maxPrefixBytes). Without this
// the server would silently truncate the suffix off a long prefix, successive
// calls would resolve to the same schema, and CleanupSchema could then drop
// another test's schema. Uniqueness therefore always comes from the suffix,
// never from the tail of a long prefix.
func UniqueSchema(prefix string) string {
	suffix := "_" + strconv.FormatInt(time.Now().UnixNano(), 10) +
		"_" + processNonce +
		"_" + strconv.FormatUint(schemaSeq.Add(1), 10)
	name := sanitizeSchemaPrefix(prefix)
	if maxPrefix := pgMaxIdentifierBytes - len(suffix); len(name) > maxPrefix {
		// The sanitised prefix is pure ASCII, so byte slicing never splits a rune,
		// and its first byte (a letter or '_') survives, so it stays an identifier.
		name = name[:maxPrefix]
	}
	return name + suffix
}

// pgMaxIdentifierBytes is PostgreSQL's identifier length limit (NAMEDATALEN-1).
// The server silently truncates longer identifiers, which is why UniqueSchema
// bounds its output itself instead of letting the uniqueness suffix be cut off.
const pgMaxIdentifierBytes = 63

// Layout of the UniqueSchema suffix "_<UnixNano>_<nonce>_<n>".
const (
	// unixNanoDigits is the width of a Unix nanosecond timestamp from 2001 until
	// int64 overflows in 2262.
	unixNanoDigits = 19
	// processNonceBytes is the number of random bytes in the per-process nonce;
	// it is rendered as twice as many hex digits.
	processNonceBytes = 4
	// fixedSuffixBytes is the suffix length without the counter's digits:
	// three underscores, the timestamp and the hex nonce (30 bytes).
	fixedSuffixBytes = 3 + unixNanoDigits + 2*processNonceBytes
	// maxPrefixBytes is the prefix budget UniqueSchema guarantees for the first
	// 99,999 names a process generates (a counter of at most five digits).
	maxPrefixBytes = pgMaxIdentifierBytes - fixedSuffixBytes - 5
)

// processNonce distinguishes this process from every other one creating
// schemas in the same database at the same time (go test runs each package's
// test binary as its own process, in parallel). It is drawn once from
// crypto/rand so that two processes can never share it by construction, unlike
// a PID, which the OS reuses.
var processNonce = newProcessNonce()

// schemaSeq numbers the names UniqueSchema hands out within this process, so
// concurrent callers that observe the same timestamp still get distinct names.
var schemaSeq atomic.Uint64

// newProcessNonce returns 2*processNonceBytes lowercase hex digits of
// cryptographic randomness.
func newProcessNonce() string {
	var b [processNonceBytes]byte
	// crypto/rand.Read never returns an error (since Go 1.24 it crashes the
	// process instead if the OS entropy source is unusable), so there is no
	// failure path to handle here.
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
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
