package commands

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go-mink.dev/adapters"
	"go-mink.dev/cli/styles"
)

// runCmdCapture runs a cobra command with args and returns its combined out/err output.
func runCmdCapture(cmd *cobra.Command, args []string) (string, error) {
	cmd.SetArgs(args)
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	err := cmd.Execute()
	return buf.String(), err
}

func sampleStoredEvents() []adapters.StoredEvent {
	ts := time.Date(2026, 7, 4, 10, 0, 0, 0, time.UTC)
	return []adapters.StoredEvent{
		{ID: "e1", StreamID: "order-1", Type: "OrderPlaced", Data: []byte(`{"id":"1"}`), Version: 1, GlobalPosition: 10, Timestamp: ts},
		{ID: "e2", StreamID: "order-2", Type: "OrderShipped", Data: []byte(`{"id":"2"}`), Version: 1, GlobalPosition: 11, Timestamp: ts.Add(time.Second)},
	}
}

func TestFeedEventsJSON_Empty(t *testing.T) {
	// An empty (or nil) feed marshals to "[]", never "null" — a stable shape for scripts.
	data, err := feedEventsJSON(nil)
	require.NoError(t, err)
	assert.Equal(t, "[]", string(data))
}

func TestFeedEventsJSON_Fields(t *testing.T) {
	data, err := feedEventsJSON(sampleStoredEvents())
	require.NoError(t, err)

	var got []feedEvent
	require.NoError(t, json.Unmarshal(data, &got))
	require.Len(t, got, 2)

	assert.Equal(t, uint64(10), got[0].GlobalPosition)
	assert.Equal(t, "order-1", got[0].StreamID)
	assert.Equal(t, "OrderPlaced", got[0].Type)
	assert.Equal(t, int64(1), got[0].Version)
	// data is preserved as raw JSON (queryable with jq), not a re-encoded string.
	assert.JSONEq(t, `{"id":"1"}`, string(got[0].Data))
}

func TestFeedEventsJSON_NonJSONDataStaysValid(t *testing.T) {
	events := []adapters.StoredEvent{
		{ID: "e1", StreamID: "s", Type: "T", Data: []byte("\x00not json"), Version: 1, GlobalPosition: 1},
		{ID: "e2", StreamID: "s", Type: "T", Data: nil, Version: 2, GlobalPosition: 2},
	}
	data, err := feedEventsJSON(events)
	require.NoError(t, err)
	assert.True(t, json.Valid(data), "output must be valid JSON even for non-JSON payloads")

	var got []feedEvent
	require.NoError(t, json.Unmarshal(data, &got))
	require.Len(t, got, 2)

	// A non-JSON payload is emitted as a JSON string so the array stays valid.
	var s string
	require.NoError(t, json.Unmarshal(got[0].Data, &s))
	assert.Equal(t, "\x00not json", s)
}

func TestRenderFeedTable_Empty(t *testing.T) {
	assert.Contains(t, renderFeedTable(nil), "No events match the filter")
}

func TestRenderFeedTable_Rows(t *testing.T) {
	styles.DisableColors()
	out := renderFeedTable(sampleStoredEvents())
	assert.Contains(t, out, "Event Feed")         // title
	assert.Contains(t, out, "Showing 2 event(s)") // footer encodes the row count
	// Cell content (stream id, type, data) is verified via the JSON path: the shared
	// ui.NewTable wraps cells to the detected terminal width, which is narrow with no
	// TTY (CI, pipes), so asserting on wrapped cell text would be brittle.
}

func TestEventsCommand_FlagParsing(t *testing.T) {
	cmd := NewEventsCommand()
	require.NoError(t, cmd.ParseFlags([]string{
		"--type", "A", "--type", "B", "-s", "s1,s2", "-c", "order", "-f", "42", "-n", "7", "--json",
	}))

	types, _ := cmd.Flags().GetStringSlice("type")
	assert.Equal(t, []string{"A", "B"}, types) // repeatable
	streams, _ := cmd.Flags().GetStringSlice("stream")
	assert.Equal(t, []string{"s1", "s2"}, streams) // comma-separated
	cat, _ := cmd.Flags().GetString("category")
	assert.Equal(t, "order", cat)
	from, _ := cmd.Flags().GetUint64("from")
	assert.Equal(t, uint64(42), from)
	limit, _ := cmd.Flags().GetInt("limit")
	assert.Equal(t, 7, limit)
	asJSON, _ := cmd.Flags().GetBool("json")
	assert.True(t, asJSON)
}

func TestEventsCommand_DefaultLimit(t *testing.T) {
	limit, _ := NewEventsCommand().Flags().GetInt("limit")
	assert.Equal(t, 50, limit)
}

func TestEventsCommand_MemoryEmptyStore(t *testing.T) {
	env := setupTestEnv(t, "cli-events-empty-*")
	env.createConfig(withDriver("memory"))
	styles.DisableColors()

	// JSON on an empty store is an empty array, exit zero.
	out, err := runCmdCapture(NewEventsCommand(), []string{"--json"})
	require.NoError(t, err)
	assert.Equal(t, "[]", strings.TrimSpace(out))

	// Table on an empty store prints the notice, exit zero.
	out, err = runCmdCapture(NewEventsCommand(), nil)
	require.NoError(t, err)
	assert.Contains(t, out, "No events match the filter")

	// Filters + paging flags still succeed on an empty store.
	_, err = runCmdCapture(NewEventsCommand(), []string{"--type", "X", "--stream", "s1", "--category", "c", "--from", "5", "--limit", "3"})
	require.NoError(t, err)
}

func TestEventsCommand_MissingConfig(t *testing.T) {
	// No mink.yaml in the temp dir → a friendly error from getAdapter.
	setupTestEnv(t, "cli-events-noconfig-*")
	_, err := runCmdCapture(NewEventsCommand(), []string{"--json"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mink.yaml")
}

func TestEventsCommand_RejectsPositionalArgs(t *testing.T) {
	// `mink events` takes no positional args; a stray one is a usage error.
	_, err := runCmdCapture(NewEventsCommand(), []string{"bogus"})
	require.Error(t, err)
}
