package commands

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"go-mink.dev/adapters"
	"go-mink.dev/cli/styles"
	"go-mink.dev/cli/ui"
)

// feedEvent is the JSON shape emitted by `mink events --json`. Unlike the per-stream
// StreamEvent export, it carries global_position: the global feed is ordered and paged
// by that cursor, and it is the value an operator feeds back as --from to page forward.
// data and metadata are emitted as raw JSON (nested objects), not re-encoded strings, so
// the output stays queryable with jq (e.g. `.[].metadata.tenantId`).
type feedEvent struct {
	GlobalPosition uint64          `json:"global_position"`
	ID             string          `json:"id"`
	StreamID       string          `json:"stream_id"`
	Version        int64           `json:"version"`
	Type           string          `json:"type"`
	Data           json.RawMessage `json:"data"`
	Metadata       json.RawMessage `json:"metadata,omitempty"`
	Timestamp      time.Time       `json:"timestamp"`
}

// NewEventsCommand creates the `mink events` command: a filtered read of the global
// event feed by the indexed axes the FeedFilter exposes (event type, stream id,
// category). It is the CLI consumer of the FilteredFeedAdapter capability.
func NewEventsCommand() *cobra.Command {
	var (
		eventTypes []string
		streamIDs  []string
		category   string
		from       uint64
		limit      int
		asJSON     bool
	)

	cmd := &cobra.Command{
		Use:   "events",
		Short: "Query the global event feed by type, stream, or category",
		Long: `Query the global event feed, filtered by indexed axes and starting after a
global position.

This is an introspection / ops / migration tool over the raw event log — for audit
browsers, backfill scanners, and diagnostics. It is NOT an application read path: for
application queries, or any unindexed criterion (a tenant embedded in metadata, a field
inside the payload), project a read model instead.

Filters cover only columns the event store indexes:
  -t, --type      event type(s)      (event_type IN ...)
  -s, --stream    exact stream id(s) (stream_id IN ...)
  -c, --category  stream category    (stream_id LIKE '<category>-%')

Axes AND-compose; a repeated or comma-separated --type / --stream is an OR set. An empty
filter walks the whole feed (like an unfiltered load-from-position). Results are ordered
by ascending global position, exclusive of --from, and bounded by --limit.

Examples:
  mink events --type OrderPlaced                      # every OrderPlaced, from the start
  mink events --type OrderPlaced --type OrderShipped  # either type (OR)
  mink events --stream order-123 --stream order-456   # two exact streams
  mink events --category order --from 1000 -n 100     # order-* after position 1000
  mink events --type OrderPlaced --json               # machine-readable output`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			adapter, cleanup, err := getAdapter(ctx)
			if err != nil {
				return err
			}
			defer cleanup()

			filter := adapters.FeedFilter{
				EventTypes: eventTypes,
				StreamIDs:  streamIDs,
				Category:   category,
			}

			events, err := adapter.LoadFromPositionFiltered(ctx, from, limit, filter)
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			if asJSON {
				data, jerr := feedEventsJSON(events)
				if jerr != nil {
					return jerr
				}
				_, err = fmt.Fprintln(out, string(data))
				return err
			}

			_, err = fmt.Fprintln(out, renderFeedTable(events))
			return err
		},
	}

	cmd.Flags().StringSliceVarP(&eventTypes, "type", "t", nil, "Filter by event type(s); repeatable or comma-separated")
	cmd.Flags().StringSliceVarP(&streamIDs, "stream", "s", nil, "Filter by exact stream id(s); repeatable or comma-separated")
	cmd.Flags().StringVarP(&category, "category", "c", "", "Filter by stream category (stream_id LIKE '<category>-%')")
	cmd.Flags().Uint64VarP(&from, "from", "f", 0, "Start after this global position (exclusive)")
	cmd.Flags().IntVarP(&limit, "limit", "n", 50, "Maximum events to return (0 = adapter default cap)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output events as a JSON array instead of a table")

	return cmd
}

// renderFeedTable renders a slice of feed events as a scannable table (or an
// empty-feed notice). It is a pure function of its input so it can be unit-tested
// without a live adapter.
func renderFeedTable(events []adapters.StoredEvent) string {
	if len(events) == 0 {
		return styles.FormatInfo("No events match the filter")
	}

	var b strings.Builder
	b.WriteString("\n")
	b.WriteString(styles.Title.Render(styles.IconStream + " Event Feed"))
	b.WriteString("\n\n")

	table := ui.NewTable("Position", "Stream", "Type", "Time")
	for _, e := range events {
		table.AddRow(
			strconv.FormatUint(e.GlobalPosition, 10),
			e.StreamID,
			e.Type,
			e.Timestamp.Format("2006-01-02 15:04:05"),
		)
	}
	b.WriteString(table.Render())
	fmt.Fprintf(&b, "\n\nShowing %d event(s)", len(events))
	return b.String()
}

// feedEventsJSON marshals feed events to an indented JSON array. An empty input
// marshals to "[]" (not "null"). It is pure and unit-testable.
func feedEventsJSON(events []adapters.StoredEvent) ([]byte, error) {
	out := make([]feedEvent, len(events))
	for i, e := range events {
		out[i] = toFeedEvent(e)
	}
	return json.MarshalIndent(out, "", "  ")
}

// toFeedEvent converts a stored event to its JSON projection. event data is emitted as
// raw JSON when it is valid JSON (the normal case for the JSON serializer / JSONB
// storage), so it stays queryable with jq; otherwise it is emitted as a JSON string so
// the array remains valid regardless of payload encoding. The CLI holds no encryption
// keys, so field-encrypted data is emitted as stored, consistent with `mink stream export`.
func toFeedEvent(e adapters.StoredEvent) feedEvent {
	data := e.Data
	if len(data) == 0 || !json.Valid(data) {
		quoted, _ := json.Marshal(string(data))
		data = quoted
	}
	return feedEvent{
		GlobalPosition: e.GlobalPosition,
		ID:             e.ID,
		StreamID:       e.StreamID,
		Version:        e.Version,
		Type:           e.Type,
		Data:           json.RawMessage(data),
		Metadata:       metadataJSON(e.Metadata),
		Timestamp:      e.Timestamp,
	}
}

// metadataJSON renders event metadata as raw JSON for --json output — a nested object
// mirroring the data field, not a re-encoded string, so it stays queryable with jq.
// Empty metadata marshals to "{}", which is dropped to nil so the omitempty field is
// omitted rather than emitting a noise object on every event.
func metadataJSON(m adapters.Metadata) json.RawMessage {
	raw, err := json.Marshal(m)
	if err != nil || len(raw) == 0 || string(raw) == "{}" {
		return nil
	}
	return json.RawMessage(raw)
}
