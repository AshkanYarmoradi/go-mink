package commands

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go-mink.dev/cli/styles"
)

// TestEventsCommand_PG exercises `mink events` against a real PostgreSQL store seeded
// with a mix of event types, streams, and categories, asserting each filter axis, the
// position window, the limit, and JSON output. Self-skips under -short or without
// TEST_DATABASE_URL. Reuses cliTypeA/cliTypeB from stream_types_pg_test.go and the shared
// newPGCLIStore harness.
func TestEventsCommand_PG(t *testing.T) {
	store, ctx := newPGCLIStore(t, "cli_events")

	// Seed a fixed feed (event type = Go type name: cliTypeA / cliTypeB):
	//   pos 1 order-1/cliTypeA, 2 order-1/cliTypeB, 3 order-2/cliTypeA, 4 user-1/cliTypeB
	store.RegisterEvents(cliTypeA{}, cliTypeB{})
	require.NoError(t, store.Append(ctx, "order-1", []interface{}{cliTypeA{ID: "1"}, cliTypeB{ID: "1"}}))
	require.NoError(t, store.Append(ctx, "order-2", []interface{}{cliTypeA{ID: "2"}}))
	require.NoError(t, store.Append(ctx, "user-1", []interface{}{cliTypeB{ID: "3"}}))

	styles.DisableColors()

	loadJSON := func(t *testing.T, args ...string) []feedEvent {
		t.Helper()
		out, err := runCmdCapture(NewEventsCommand(), append(args, "--json"))
		require.NoError(t, err)
		var evs []feedEvent
		require.NoError(t, json.Unmarshal([]byte(out), &evs), "output was: %s", out)
		return evs
	}

	// Unfiltered: all four events, ascending global position.
	all := loadJSON(t, "--limit", "100")
	require.Len(t, all, 4)
	for i := 1; i < len(all); i++ {
		assert.Greater(t, all[i].GlobalPosition, all[i-1].GlobalPosition)
	}

	t.Run("type filter across streams", func(t *testing.T) {
		got := loadJSON(t, "--type", "cliTypeA")
		require.Len(t, got, 2) // order-1 and order-2
		for _, e := range got {
			assert.Equal(t, "cliTypeA", e.Type)
		}
	})

	t.Run("exact stream set", func(t *testing.T) {
		got := loadJSON(t, "--stream", "order-1")
		require.Len(t, got, 2)
		for _, e := range got {
			assert.Equal(t, "order-1", e.StreamID)
		}
	})

	t.Run("category prefix", func(t *testing.T) {
		got := loadJSON(t, "--category", "order")
		require.Len(t, got, 3) // order-1 (2) + order-2 (1), not user-1
		for _, e := range got {
			assert.True(t, strings.HasPrefix(e.StreamID, "order-"))
		}
	})

	t.Run("axes AND-compose", func(t *testing.T) {
		got := loadJSON(t, "--type", "cliTypeB", "--category", "order")
		require.Len(t, got, 1) // only order-1's cliTypeB
		assert.Equal(t, "order-1", got[0].StreamID)
		assert.Equal(t, "cliTypeB", got[0].Type)
	})

	t.Run("from position is exclusive", func(t *testing.T) {
		pivot := all[1].GlobalPosition
		got := loadJSON(t, "--from", strconv.FormatUint(pivot, 10), "--limit", "100")
		require.Len(t, got, 2)
		for _, e := range got {
			assert.Greater(t, e.GlobalPosition, pivot)
		}
	})

	t.Run("limit caps results", func(t *testing.T) {
		got := loadJSON(t, "--limit", "1")
		require.Len(t, got, 1)
		assert.Equal(t, all[0].GlobalPosition, got[0].GlobalPosition)
	})

	t.Run("table output", func(t *testing.T) {
		// The table path renders (row count in the footer); cell content is asserted
		// through the JSON subtests above, since ui.NewTable wraps cells with no TTY.
		out, err := runCmdCapture(NewEventsCommand(), []string{"--type", "cliTypeA"})
		require.NoError(t, err)
		assert.Contains(t, out, "Showing 2 event(s)")
	})
}
