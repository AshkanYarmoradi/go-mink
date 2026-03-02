package postgres

import (
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/AshkanYarmoradi/go-mink/adapters"
	"github.com/AshkanYarmoradi/go-mink/testing/benchmarks"
)

// benchDB opens a database connection for benchmarks.
// Returns nil if TEST_DATABASE_URL is not set.
func benchDB() (*sql.DB, error) {
	connStr := os.Getenv("TEST_DATABASE_URL")
	if connStr == "" {
		return nil, nil
	}
	return sql.Open("pgx", connStr)
}

// benchSchema generates a unique schema name for benchmark isolation.
func benchSchema() string {
	return fmt.Sprintf("bench_%d", time.Now().UnixNano())
}

func newBenchmarkFactory() benchmarks.AdapterBenchmarkFactory {
	return benchmarks.AdapterBenchmarkFactory{
		Name: "postgres",
		CreateAdapter: func() (adapters.EventStoreAdapter, func(), error) {
			db, err := benchDB()
			if err != nil {
				return nil, nil, err
			}
			if db == nil {
				return nil, nil, fmt.Errorf("TEST_DATABASE_URL not set")
			}
			schema := benchSchema()
			a, err := NewAdapterWithDB(db, WithSchema(schema))
			if err != nil {
				_ = db.Close()
				return nil, nil, err
			}
			cleanup := func() {
				schemaQ := quoteIdentifier(schema)
				_, _ = db.Exec(`DROP SCHEMA IF EXISTS ` + schemaQ + ` CASCADE`)
				_ = a.Close()
				_ = db.Close()
			}
			return a, cleanup, nil
		},
		Skip: func() string {
			if testing.Short() {
				return "Skipping integration benchmark in short mode"
			}
			if os.Getenv("TEST_DATABASE_URL") == "" {
				return "TEST_DATABASE_URL not set"
			}
			return ""
		},
		Scale: &benchmarks.ScaleConfig{EventCount: 100_000},
	}
}

func BenchmarkAdapter(b *testing.B) {
	benchmarks.Run(b, newBenchmarkFactory())
}

func TestAdapterScale(t *testing.T) {
	if os.Getenv("MINK_SCALE_TESTS") != "1" {
		t.Skip("Skipping scale test; set MINK_SCALE_TESTS=1 to enable")
	}
	benchmarks.RunScale(t, newBenchmarkFactory())
}
