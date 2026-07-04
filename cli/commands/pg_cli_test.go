package commands

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	mink "go-mink.dev"
	"go-mink.dev/adapters/postgres"
)

// newPGCLIStore provisions an isolated PostgreSQL schema for a CLI integration test: it
// creates and initializes a postgres adapter, registers cleanup that closes it and drops
// the schema, and writes a mink.yaml in a chdir'd temp dir pointing the CLI at that
// schema. It returns a store for seeding events plus the context to seed with. It
// self-skips under -short or without TEST_DATABASE_URL, so callers need no skip
// boilerplate of their own. Shared by every `mink` PostgreSQL CLI test.
func newPGCLIStore(t *testing.T, prefix string) (*mink.EventStore, context.Context) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping PostgreSQL CLI test in short mode")
	}
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL CLI test")
	}
	ctx := context.Background()
	schema := fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())

	adapter, err := postgres.NewAdapter(url, postgres.WithSchema(schema))
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = adapter.Close()
		if db, derr := sql.Open("pgx", url); derr == nil {
			_, _ = db.ExecContext(ctx, fmt.Sprintf("DROP SCHEMA IF EXISTS %q CASCADE", schema))
			_ = db.Close()
		}
	})
	require.NoError(t, adapter.Initialize(ctx))

	// Point the CLI (which loads config from the working directory) at this schema.
	env := setupTestEnv(t, prefix)
	minkYAML := "version: \"1.0\"\n" +
		"project:\n  name: \"e2e\"\n  module: \"example.com/e2e\"\n" +
		"database:\n  driver: \"postgres\"\n  url: \"" + url + "\"\n  schema: \"" + schema + "\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(env.tmpDir, "mink.yaml"), []byte(minkYAML), 0o600))

	return mink.New(adapter), ctx
}
