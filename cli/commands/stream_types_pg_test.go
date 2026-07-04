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

type cliTypeA struct {
	ID string `json:"id"`
}
type cliTypeB struct {
	ID string `json:"id"`
}

// TestStreamTypesCommand_PG exercises `mink stream types <stream-id>` against a real PostgreSQL
// store seeded with a mix of event types, covering the command's count/render path (the memory-
// driver tests only reach the empty-stream branch). Self-skips under -short or without
// TEST_DATABASE_URL.
func TestStreamTypesCommand_PG(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping PostgreSQL CLI test in short mode")
	}
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL CLI test")
	}
	ctx := context.Background()
	schema := fmt.Sprintf("clitypes_%d", time.Now().UnixNano())

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

	store := mink.New(adapter)
	store.RegisterEvents(cliTypeA{}, cliTypeB{})
	require.NoError(t, store.Append(ctx, "order-1", []interface{}{
		cliTypeA{ID: "1"}, cliTypeB{ID: "1"}, cliTypeA{ID: "1"},
	}))

	// Point the CLI at this schema via a mink.yaml in the (chdir'd) temp dir.
	env := setupTestEnv(t, "cli-stream-types-*")
	minkYAML := "version: \"1.0\"\n" +
		"project:\n  name: \"e2e\"\n  module: \"example.com/e2e\"\n" +
		"database:\n  driver: \"postgres\"\n  url: \"" + url + "\"\n  schema: \"" + schema + "\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(env.tmpDir, "mink.yaml"), []byte(minkYAML), 0o600))

	require.NoError(t, executeCmd(NewStreamCommand(), []string{"types", "order-1"}))
}
