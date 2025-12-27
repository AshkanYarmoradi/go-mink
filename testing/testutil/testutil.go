// Package testutil provides utilities for integration testing.
// It provides helpers for connecting to test infrastructure and
// waiting for services to be ready.
package testutil

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestConfig holds configuration for test infrastructure.
type TestConfig struct {
	PostgresURL string
	// Future adapters:
	// MongoURL    string
	// RedisURL    string
}

// DefaultConfig returns the default test configuration from environment variables.
func DefaultConfig() *TestConfig {
	return &TestConfig{
		PostgresURL: getEnvOrDefault("TEST_DATABASE_URL", "postgres://postgres:postgres@localhost:5432/mink_test?sslmode=disable"),
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

// CleanupSchema drops a schema and all its objects.
func CleanupSchema(ctx context.Context, db *sql.DB, schema string) error {
	_, err := db.ExecContext(ctx, fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", schema))
	return err
}

// UniqueSchema generates a unique schema name for testing.
func UniqueSchema(prefix string) string {
	return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
}
