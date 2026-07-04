package commands

import (
	"testing"

	"github.com/stretchr/testify/require"
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
// TEST_DATABASE_URL via the shared newPGCLIStore harness.
func TestStreamTypesCommand_PG(t *testing.T) {
	store, ctx := newPGCLIStore(t, "cli_stream_types")
	store.RegisterEvents(cliTypeA{}, cliTypeB{})
	require.NoError(t, store.Append(ctx, "order-1", []interface{}{
		cliTypeA{ID: "1"}, cliTypeB{ID: "1"}, cliTypeA{ID: "1"},
	}))

	require.NoError(t, executeCmd(NewStreamCommand(), []string{"types", "order-1"}))
}
