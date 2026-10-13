package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go-mink.dev"
)

// captureStdout runs fn with os.Stdout redirected to a pipe and returns what it printed.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	defer func() { os.Stdout = old }()

	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(r)
		done <- buf.String()
	}()
	fn()
	_ = w.Close()
	return <-done
}

// The time-range demo must name the real subject: with explicit Streams and no Filter the
// exporter scopes the result to events tagged for SubjectID, so a label such as
// "alice-recent" would export nothing. The expected counts pin that rule, the time bounds,
// and the explicit FilterByStreams override — and the demo prints exactly those counts.
func TestTimeRangeExport_SubjectScopedByDefault(t *testing.T) {
	ctx := context.Background()

	provider, err := newProvider()
	require.NoError(t, err)
	t.Cleanup(func() { _ = provider.Close() })

	store := newStore(provider)
	require.NoError(t, seedData(ctx, store))

	demos := timeRangeDemos(time.Now())
	require.Len(t, demos, 4)
	want := []int{
		2, // alice-1: Customer-alice-1 + Order-ord-1 within the last hour
		1, // alice-1: Customer-alice-1 before the cutoff
		0, // bob-1 on Alice's streams: every event is tagged alice-1, so none is his
		1, // explicit FilterByStreams: the whole stream regardless of the (label) SubjectID
	}

	exporter := mink.NewDataExporter(store)
	for i, d := range demos {
		res, err := exporter.Export(ctx, d.req)
		require.NoError(t, err, d.label)
		assert.Equal(t, want[i], res.TotalEvents, d.label)
	}

	// The pre-fix shape — a label in place of the subject id — exports nothing.
	oneHourAgo := time.Now().Add(-time.Hour)
	res, err := exporter.Export(ctx, mink.ExportRequest{
		SubjectID: "alice-recent",
		Streams:   []string{"Customer-alice-1", "Order-ord-1"},
		FromTime:  &oneHourAgo,
	})
	require.NoError(t, err)
	assert.Equal(t, 0, res.TotalEvents, "a label is not a subject id: the default scoping drops Alice's tagged events")

	// The demo itself prints those counts.
	out := captureStdout(t, func() { timeRangeExport(ctx, store) })
	assert.Contains(t, out, "=== Time Range Export ===")
	for i, d := range demos {
		assert.Contains(t, out, fmt.Sprintf("%s: %d\n", d.label, want[i]))
	}
}
