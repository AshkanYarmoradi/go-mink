package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/AshkanYarmoradi/go-mink/adapters"
	"github.com/AshkanYarmoradi/go-mink/cli/config"
	"github.com/AshkanYarmoradi/go-mink/cli/styles"
	"github.com/AshkanYarmoradi/go-mink/cli/ui"
	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"
)

// NewProjectionCommand creates the projection command
func NewProjectionCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "projection",
		Short: "Manage projections",
		Long: `Manage event projections - list, rebuild, pause, and resume.

Examples:
  mink projection list              # List all projections
  mink projection status OrderView  # Show projection status
  mink projection rebuild OrderView # Rebuild a projection
  mink projection pause OrderView   # Pause projection updates
  mink projection resume OrderView  # Resume projection updates`,
		Aliases: []string{"proj"},
	}

	cmd.AddCommand(newProjectionListCommand())
	cmd.AddCommand(newProjectionStatusCommand())
	cmd.AddCommand(newProjectionRebuildCommand())
	cmd.AddCommand(newProjectionPauseCommand())
	cmd.AddCommand(newProjectionResumeCommand())

	return cmd
}

func newProjectionListCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Short:   "List all projections",
		Aliases: []string{"ls"},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			adapter, cfg, cleanup, err := getAdapterWithConfig(ctx)
			if err != nil {
				return err
			}
			defer cleanup()

			if cfg.Database.Driver == "memory" {
				fmt.Println(styles.FormatInfo("Memory driver - projections are in-memory only"))
				return nil
			}

			projections, err := adapter.ListProjections(ctx)
			if err != nil {
				return err
			}

			if len(projections) == 0 {
				fmt.Println(styles.FormatInfo("No projections registered"))
				return nil
			}

			fmt.Println()
			fmt.Println(styles.Title.Render(styles.IconList + " Projections"))
			fmt.Println()

			table := ui.NewTable("Name", "Position", "Status", "Last Updated")
			for _, p := range projections {
				status := ui.StatusBadge(p.Status)
				table.AddRow(p.Name, fmt.Sprintf("%d", p.Position), status, p.UpdatedAt.Format("2006-01-02 15:04:05"))
			}

			fmt.Println(table.Render())
			fmt.Println()

			return nil
		},
	}
}

func newProjectionStatusCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "status <name>",
		Short: "Show detailed projection status",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			ctx := cmd.Context()

			adapter, cleanup, err := getAdapter(ctx)
			if err != nil {
				return err
			}
			defer cleanup()

			projection, err := adapter.GetProjection(ctx, name)
			if err != nil {
				return err
			}

			if projection == nil {
				return fmt.Errorf("projection '%s' not found", name)
			}

			totalEvents, err := adapter.GetTotalEventCount(ctx)
			if err != nil {
				return err
			}

			fmt.Println()
			fmt.Println(styles.Title.Render(fmt.Sprintf("%s Projection: %s", styles.IconDatabase, name)))
			fmt.Println()

			details := []string{
				fmt.Sprintf("Name:        %s", projection.Name),
				fmt.Sprintf("Position:    %d / %d", projection.Position, totalEvents),
				fmt.Sprintf("Status:      %s", ui.StatusBadge(projection.Status)),
				fmt.Sprintf("Last Update: %s", projection.UpdatedAt.Format(time.RFC3339)),
			}

			for _, d := range details {
				fmt.Println("  " + styles.Normal.Render(d))
			}

			if totalEvents > 0 {
				progress := float64(projection.Position) / float64(totalEvents) * 100
				fmt.Println()
				fmt.Println(styles.Subtitle.Render("  Progress:"))
				fmt.Println(renderProgressBar(progress, 40))
			}

			return nil
		},
	}
}

func newProjectionRebuildCommand() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "rebuild <name>",
		Short: "Rebuild a projection from scratch",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			ctx := cmd.Context()

			adapter, cleanup, err := getAdapter(ctx)
			if err != nil {
				return err
			}
			defer cleanup()

			// Check if projection exists
			projection, err := adapter.GetProjection(ctx, name)
			if err != nil {
				return err
			}
			if projection == nil {
				return fmt.Errorf("projection '%s' not found", name)
			}

			// Confirm rebuild
			if !force {
				var confirmed bool
				form := huh.NewForm(
					huh.NewGroup(
						huh.NewConfirm().
							Title(fmt.Sprintf("Rebuild projection '%s'?", name)).
							Description("This will reset the checkpoint and replay all events").
							Value(&confirmed),
					),
				).WithTheme(huh.ThemeDracula())

				if err := form.Run(); err != nil {
					return err
				}

				if !confirmed {
					fmt.Println(styles.FormatInfo("Cancelled"))
					return nil
				}
			}

			fmt.Println()
			fmt.Printf("%s Rebuilding projection '%s'...\n\n", styles.IconPending, name)

			// Reset checkpoint to 0
			if err := adapter.ResetProjectionCheckpoint(ctx, name); err != nil {
				return fmt.Errorf("failed to reset checkpoint: %w", err)
			}

			fmt.Println(styles.FormatSuccess("Checkpoint reset to 0"))
			fmt.Println(styles.FormatInfo("Projection will rebuild on next run"))

			return nil
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Skip confirmation")

	return cmd
}

func newProjectionPauseCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "pause <name>",
		Short: "Pause a projection",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			ctx := cmd.Context()

			adapter, cleanup, err := getAdapter(ctx)
			if err != nil {
				return err
			}
			defer cleanup()

			if err := adapter.SetProjectionStatus(ctx, name, "paused"); err != nil {
				return err
			}

			fmt.Println(styles.FormatSuccess(fmt.Sprintf("Paused projection '%s'", name)))
			return nil
		},
	}
}

func newProjectionResumeCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "resume <name>",
		Short: "Resume a paused projection",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			ctx := cmd.Context()

			adapter, cleanup, err := getAdapter(ctx)
			if err != nil {
				return err
			}
			defer cleanup()

			if err := adapter.SetProjectionStatus(ctx, name, "active"); err != nil {
				return err
			}

			fmt.Println(styles.FormatSuccess(fmt.Sprintf("Resumed projection '%s'", name)))
			return nil
		},
	}
}

// NewStreamCommand creates the stream command
func NewStreamCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stream",
		Short: "Inspect and manage event streams",
		Long: `Inspect event streams, view events, and manage stream data.

Examples:
  mink stream list                    # List all streams
  mink stream events order-123        # Show events for a stream
  mink stream export order-123        # Export stream to JSON
  mink stream replay order-123        # Replay stream events`,
	}

	cmd.AddCommand(newStreamListCommand())
	cmd.AddCommand(newStreamEventsCommand())
	cmd.AddCommand(newStreamExportCommand())
	cmd.AddCommand(newStreamStatsCommand())

	return cmd
}

func newStreamListCommand() *cobra.Command {
	var (
		limit  int
		prefix string
	)

	cmd := &cobra.Command{
		Use:     "list",
		Short:   "List event streams",
		Aliases: []string{"ls"},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			adapter, cleanup, err := getAdapter(ctx)
			if err != nil {
				return err
			}
			defer cleanup()

			streams, err := adapter.ListStreams(ctx, prefix, limit)
			if err != nil {
				return err
			}

			if len(streams) == 0 {
				fmt.Println(styles.FormatInfo("No streams found"))
				return nil
			}

			fmt.Println()
			fmt.Println(styles.Title.Render(styles.IconStream + " Event Streams"))
			fmt.Println()

			table := ui.NewTable("Stream ID", "Events", "Last Event", "Last Updated")
			for _, s := range streams {
				table.AddRow(s.StreamID, fmt.Sprintf("%d", s.EventCount), s.LastEventType, s.LastUpdated.Format("2006-01-02 15:04"))
			}

			fmt.Println(table.Render())
			fmt.Printf("\nShowing %d streams\n", len(streams))

			return nil
		},
	}

	cmd.Flags().IntVarP(&limit, "limit", "n", 50, "Maximum streams to show")
	cmd.Flags().StringVarP(&prefix, "prefix", "p", "", "Filter by stream ID prefix")

	return cmd
}

func newStreamEventsCommand() *cobra.Command {
	var (
		limit int
		from  int64
	)

	cmd := &cobra.Command{
		Use:   "events <stream-id>",
		Short: "Show events in a stream",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			streamID := args[0]
			ctx := cmd.Context()

			adapter, cleanup, err := getAdapter(ctx)
			if err != nil {
				return err
			}
			defer cleanup()

			events, err := adapter.GetStreamEvents(ctx, streamID, from, limit)
			if err != nil {
				return err
			}

			if len(events) == 0 {
				fmt.Println(styles.FormatInfo(fmt.Sprintf("No events in stream '%s'", streamID)))
				return nil
			}

			fmt.Println()
			fmt.Println(styles.Title.Render(fmt.Sprintf("%s Stream: %s", styles.IconStream, streamID)))
			fmt.Println()

			for _, e := range events {
				fmt.Println(styles.Subtitle.Render(fmt.Sprintf("Event #%d: %s", e.Version, e.Type)))
				fmt.Println(styles.Muted.Render(fmt.Sprintf("  ID: %s", e.ID)))
				fmt.Println(styles.Muted.Render(fmt.Sprintf("  Time: %s", e.Timestamp.Format(time.RFC3339))))
				fmt.Println()

				// Pretty print JSON data
				var prettyData bytes.Buffer
				if err := json.Indent(&prettyData, e.Data, "  ", "  "); err == nil {
					fmt.Println(styles.Code.Render("  " + prettyData.String()))
				} else {
					fmt.Println(styles.Code.Render("  " + string(e.Data)))
				}
				fmt.Println()
				fmt.Println(ui.Divider(60))
				fmt.Println()
			}

			return nil
		},
	}

	cmd.Flags().IntVarP(&limit, "limit", "n", 20, "Maximum events to show")
	cmd.Flags().Int64VarP(&from, "from", "f", 0, "Start from version")

	return cmd
}

func newStreamExportCommand() *cobra.Command {
	var output string

	cmd := &cobra.Command{
		Use:   "export <stream-id>",
		Short: "Export stream events to JSON",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			streamID := args[0]
			ctx := cmd.Context()

			adapter, cleanup, err := getAdapter(ctx)
			if err != nil {
				return err
			}
			defer cleanup()

			events, err := adapter.GetStreamEvents(ctx, streamID, 0, 10000)
			if err != nil {
				return err
			}

			// Convert to exportable format
			exportEvents := make([]StreamEvent, len(events))
			for i, e := range events {
				exportEvents[i] = StreamEvent{
					ID:        e.ID,
					StreamID:  e.StreamID,
					Version:   e.Version,
					Type:      e.Type,
					Data:      string(e.Data),
					Metadata:  formatMetadata(e.Metadata),
					Timestamp: e.Timestamp,
				}
			}

			data, err := json.MarshalIndent(exportEvents, "", "  ")
			if err != nil {
				return err
			}

			if output == "" {
				output = streamID + ".json"
			}

			if err := os.WriteFile(output, data, 0644); err != nil {
				return err
			}

			fmt.Println(styles.FormatSuccess(fmt.Sprintf("Exported %d events to %s", len(events), output)))
			return nil
		},
	}

	cmd.Flags().StringVarP(&output, "output", "o", "", "Output file (default: <stream-id>.json)")

	return cmd
}

func formatMetadata(m adapters.Metadata) string {
	data, err := json.Marshal(m)
	if err != nil {
		return "{}"
	}
	return string(data)
}

func newStreamStatsCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "stats",
		Short: "Show event store statistics",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			adapter, cleanup, err := getAdapter(ctx)
			if err != nil {
				return err
			}
			defer cleanup()

			stats, err := adapter.GetEventStoreStats(ctx)
			if err != nil {
				return err
			}

			fmt.Println()
			fmt.Println(styles.Title.Render(styles.IconChart + " Event Store Statistics"))
			fmt.Println()

			details := []string{
				fmt.Sprintf("Total Events:   %d", stats.TotalEvents),
				fmt.Sprintf("Total Streams:  %d", stats.TotalStreams),
				fmt.Sprintf("Event Types:    %d", stats.EventTypes),
				fmt.Sprintf("Avg per Stream: %.1f events", stats.AvgEventsPerStream),
			}

			for _, d := range details {
				fmt.Println("  " + styles.Normal.Render(d))
			}

			if len(stats.TopEventTypes) > 0 {
				fmt.Println()
				fmt.Println(styles.Subtitle.Render("  Top Event Types:"))
				for i, t := range stats.TopEventTypes {
					fmt.Printf("    %d. %s (%d)\n", i+1, t.Type, t.Count)
				}
			}

			return nil
		},
	}
}

// StreamEvent represents an event in a stream (for export)
type StreamEvent struct {
	ID        string    `json:"id"`
	StreamID  string    `json:"stream_id"`
	Version   int64     `json:"version"`
	Type      string    `json:"type"`
	Data      string    `json:"data"`
	Metadata  string    `json:"metadata,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

// loadConfig loads the mink configuration from the current directory.
func loadConfig() (*config.Config, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}

	_, cfg, err := config.FindConfig(cwd)
	if err != nil {
		return nil, fmt.Errorf("no mink.yaml found: %w", err)
	}

	return cfg, nil
}

// renderProgressBar renders a simple text-based progress bar.
func renderProgressBar(percent float64, width int) string {
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}

	filled := int(float64(width) * percent / 100)
	empty := width - filled

	bar := strings.Repeat("█", filled) + strings.Repeat("░", empty)
	return fmt.Sprintf("  [%s] %.1f%%", bar, percent)
}
