package commands

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"time"

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
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}

			_, cfg, err := config.FindConfig(cwd)
			if err != nil {
				return fmt.Errorf("no mink.yaml found: %w", err)
			}

			if cfg.Database.Driver == "memory" {
				fmt.Println(styles.FormatInfo("Memory driver - projections are in-memory only"))
				return nil
			}

			dbURL := os.ExpandEnv(cfg.Database.URL)
			if dbURL == "" || dbURL == "${DATABASE_URL}" {
				return fmt.Errorf("DATABASE_URL environment variable is not set")
			}

			projections, err := listProjections(dbURL)
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

			cwd, err := os.Getwd()
			if err != nil {
				return err
			}

			_, cfg, err := config.FindConfig(cwd)
			if err != nil {
				return fmt.Errorf("no mink.yaml found: %w", err)
			}

			dbURL := os.ExpandEnv(cfg.Database.URL)
			if dbURL == "" || dbURL == "${DATABASE_URL}" {
				return fmt.Errorf("DATABASE_URL environment variable is not set")
			}

			proj, err := getProjection(dbURL, name)
			if err != nil {
				return err
			}

			if proj == nil {
				return fmt.Errorf("projection '%s' not found", name)
			}

			// Get total event count for progress
			totalEvents, err := getTotalEventCount(dbURL)
			if err != nil {
				totalEvents = 0
			}

			fmt.Println()
			fmt.Println(styles.Title.Render(styles.IconInfo + " Projection: " + proj.Name))
			fmt.Println()

			details := []string{
				fmt.Sprintf("Status:       %s", ui.StatusBadge(proj.Status)),
				fmt.Sprintf("Position:     %d / %d", proj.Position, totalEvents),
				fmt.Sprintf("Last Updated: %s", proj.UpdatedAt.Format(time.RFC3339)),
			}

			if totalEvents > 0 {
				progress := float64(proj.Position) / float64(totalEvents) * 100
				details = append(details, fmt.Sprintf("Progress:     %.1f%%", progress))
			}

			for _, d := range details {
				fmt.Println("  " + d)
			}
			fmt.Println()

			if proj.Position < int64(totalEvents) {
				fmt.Println(styles.FormatWarning(fmt.Sprintf("%d events behind", int64(totalEvents)-proj.Position)))
			} else {
				fmt.Println(styles.FormatSuccess("Up to date"))
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

			cwd, err := os.Getwd()
			if err != nil {
				return err
			}

			_, cfg, err := config.FindConfig(cwd)
			if err != nil {
				return fmt.Errorf("no mink.yaml found: %w", err)
			}

			dbURL := os.ExpandEnv(cfg.Database.URL)
			if dbURL == "" || dbURL == "${DATABASE_URL}" {
				return fmt.Errorf("DATABASE_URL environment variable is not set")
			}

			// Confirm unless --force
			if !force {
				var confirmed bool
				form := huh.NewForm(
					huh.NewGroup(
						huh.NewConfirm().
							Title(fmt.Sprintf("Rebuild projection '%s'?", name)).
							Description("This will delete all projected data and replay from the beginning").
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
			db, err := sql.Open("pgx", dbURL)
			if err != nil {
				return err
			}
			defer db.Close()

			_, err = db.Exec(`
				UPDATE mink_checkpoints 
				SET position = 0, updated_at = NOW() 
				WHERE projection_name = $1
			`, name)
			if err != nil {
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

			cwd, err := os.Getwd()
			if err != nil {
				return err
			}

			_, cfg, err := config.FindConfig(cwd)
			if err != nil {
				return fmt.Errorf("no mink.yaml found: %w", err)
			}

			dbURL := os.ExpandEnv(cfg.Database.URL)
			if err := setProjectionStatus(dbURL, name, "paused"); err != nil {
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

			cwd, err := os.Getwd()
			if err != nil {
				return err
			}

			_, cfg, err := config.FindConfig(cwd)
			if err != nil {
				return fmt.Errorf("no mink.yaml found: %w", err)
			}

			dbURL := os.ExpandEnv(cfg.Database.URL)
			if err := setProjectionStatus(dbURL, name, "active"); err != nil {
				return err
			}

			fmt.Println(styles.FormatSuccess(fmt.Sprintf("Resumed projection '%s'", name)))
			return nil
		},
	}
}

// Projection represents a projection status
type Projection struct {
	Name      string
	Position  int64
	Status    string
	UpdatedAt time.Time
}

func listProjections(dbURL string) ([]Projection, error) {
	db, err := sql.Open("pgx", dbURL)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	// Ensure table exists with status column
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS mink_checkpoints (
			projection_name VARCHAR(255) PRIMARY KEY,
			position BIGINT NOT NULL DEFAULT 0,
			status VARCHAR(50) DEFAULT 'active',
			updated_at TIMESTAMPTZ DEFAULT NOW()
		)
	`)
	if err != nil {
		return nil, err
	}

	// Add status column if it doesn't exist
	_, _ = db.Exec(`ALTER TABLE mink_checkpoints ADD COLUMN IF NOT EXISTS status VARCHAR(50) DEFAULT 'active'`)

	rows, err := db.Query(`
		SELECT projection_name, position, COALESCE(status, 'active'), updated_at 
		FROM mink_checkpoints 
		ORDER BY projection_name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var projections []Projection
	for rows.Next() {
		var p Projection
		if err := rows.Scan(&p.Name, &p.Position, &p.Status, &p.UpdatedAt); err != nil {
			return nil, err
		}
		projections = append(projections, p)
	}

	return projections, rows.Err()
}

func getProjection(dbURL, name string) (*Projection, error) {
	db, err := sql.Open("pgx", dbURL)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	var p Projection
	err = db.QueryRow(`
		SELECT projection_name, position, COALESCE(status, 'active'), updated_at 
		FROM mink_checkpoints 
		WHERE projection_name = $1
	`, name).Scan(&p.Name, &p.Position, &p.Status, &p.UpdatedAt)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	return &p, nil
}

func getTotalEventCount(dbURL string) (int64, error) {
	db, err := sql.Open("pgx", dbURL)
	if err != nil {
		return 0, err
	}
	defer db.Close()

	var count int64
	err = db.QueryRow("SELECT COALESCE(MAX(global_position), 0) FROM mink_events").Scan(&count)
	return count, err
}

func setProjectionStatus(dbURL, name, status string) error {
	if dbURL == "" {
		return fmt.Errorf("DATABASE_URL is not configured")
	}
	
	db, err := sql.Open("pgx", dbURL)
	if err != nil {
		return err
	}
	defer db.Close()

	result, err := db.Exec(`
		UPDATE mink_checkpoints 
		SET status = $1, updated_at = NOW() 
		WHERE projection_name = $2
	`, status, name)
	if err != nil {
		return err
	}

	affected, _ := result.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("projection '%s' not found", name)
	}

	return nil
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
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}

			_, cfg, err := config.FindConfig(cwd)
			if err != nil {
				return fmt.Errorf("no mink.yaml found: %w", err)
			}

			dbURL := os.ExpandEnv(cfg.Database.URL)
			if dbURL == "" || dbURL == "${DATABASE_URL}" {
				return fmt.Errorf("DATABASE_URL environment variable is not set")
			}

			streams, err := listStreams(dbURL, prefix, limit)
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

			cwd, err := os.Getwd()
			if err != nil {
				return err
			}

			_, cfg, err := config.FindConfig(cwd)
			if err != nil {
				return fmt.Errorf("no mink.yaml found: %w", err)
			}

			dbURL := os.ExpandEnv(cfg.Database.URL)
			if dbURL == "" || dbURL == "${DATABASE_URL}" {
				return fmt.Errorf("DATABASE_URL environment variable is not set")
			}

			events, err := getStreamEvents(dbURL, streamID, from, limit)
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
				if err := json.Indent(&prettyData, []byte(e.Data), "  ", "  "); err == nil {
					fmt.Println(styles.Code.Render("  " + prettyData.String()))
				} else {
					fmt.Println(styles.Code.Render("  " + e.Data))
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

			cwd, err := os.Getwd()
			if err != nil {
				return err
			}

			_, cfg, err := config.FindConfig(cwd)
			if err != nil {
				return fmt.Errorf("no mink.yaml found: %w", err)
			}

			dbURL := os.ExpandEnv(cfg.Database.URL)
			if dbURL == "" || dbURL == "${DATABASE_URL}" {
				return fmt.Errorf("DATABASE_URL environment variable is not set")
			}

			events, err := getStreamEvents(dbURL, streamID, 0, 10000)
			if err != nil {
				return err
			}

			data, err := json.MarshalIndent(events, "", "  ")
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

func newStreamStatsCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "stats",
		Short: "Show event store statistics",
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}

			_, cfg, err := config.FindConfig(cwd)
			if err != nil {
				return fmt.Errorf("no mink.yaml found: %w", err)
			}

			dbURL := os.ExpandEnv(cfg.Database.URL)
			if dbURL == "" || dbURL == "${DATABASE_URL}" {
				return fmt.Errorf("DATABASE_URL environment variable is not set")
			}

			stats, err := getEventStoreStats(dbURL)
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

// Stream represents a stream summary
type Stream struct {
	StreamID      string
	EventCount    int64
	LastEventType string
	LastUpdated   time.Time
}

// StreamEvent represents an event in a stream
type StreamEvent struct {
	ID        string    `json:"id"`
	StreamID  string    `json:"stream_id"`
	Version   int64     `json:"version"`
	Type      string    `json:"type"`
	Data      string    `json:"data"`
	Metadata  string    `json:"metadata,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

// EventStoreStats contains event store statistics
type EventStoreStats struct {
	TotalEvents        int64
	TotalStreams       int64
	EventTypes         int64
	AvgEventsPerStream float64
	TopEventTypes      []EventTypeCount
}

// EventTypeCount is a count of events by type
type EventTypeCount struct {
	Type  string
	Count int64
}

func listStreams(dbURL, prefix string, limit int) ([]Stream, error) {
	db, err := sql.Open("pgx", dbURL)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	query := `
		SELECT 
			stream_id,
			COUNT(*) as event_count,
			(SELECT type FROM mink_events e2 WHERE e2.stream_id = e.stream_id ORDER BY version DESC LIMIT 1) as last_event_type,
			MAX(timestamp) as last_updated
		FROM mink_events e
	`

	args := []interface{}{}
	if prefix != "" {
		query += " WHERE stream_id LIKE $1"
		args = append(args, prefix+"%")
	}

	query += " GROUP BY stream_id ORDER BY last_updated DESC"

	if limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", limit)
	}

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var streams []Stream
	for rows.Next() {
		var s Stream
		if err := rows.Scan(&s.StreamID, &s.EventCount, &s.LastEventType, &s.LastUpdated); err != nil {
			return nil, err
		}
		streams = append(streams, s)
	}

	return streams, rows.Err()
}

func getStreamEvents(dbURL, streamID string, from int64, limit int) ([]StreamEvent, error) {
	db, err := sql.Open("pgx", dbURL)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	rows, err := db.Query(`
		SELECT id, stream_id, version, type, data::text, COALESCE(metadata::text, '{}'), timestamp
		FROM mink_events
		WHERE stream_id = $1 AND version > $2
		ORDER BY version
		LIMIT $3
	`, streamID, from, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []StreamEvent
	for rows.Next() {
		var e StreamEvent
		if err := rows.Scan(&e.ID, &e.StreamID, &e.Version, &e.Type, &e.Data, &e.Metadata, &e.Timestamp); err != nil {
			return nil, err
		}
		events = append(events, e)
	}

	return events, rows.Err()
}

func getEventStoreStats(dbURL string) (*EventStoreStats, error) {
	db, err := sql.Open("pgx", dbURL)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	ctx := context.Background()

	stats := &EventStoreStats{}

	// Total events
	_ = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM mink_events").Scan(&stats.TotalEvents)

	// Total streams
	_ = db.QueryRowContext(ctx, "SELECT COUNT(DISTINCT stream_id) FROM mink_events").Scan(&stats.TotalStreams)

	// Event types
	_ = db.QueryRowContext(ctx, "SELECT COUNT(DISTINCT type) FROM mink_events").Scan(&stats.EventTypes)

	// Average events per stream
	if stats.TotalStreams > 0 {
		stats.AvgEventsPerStream = float64(stats.TotalEvents) / float64(stats.TotalStreams)
	}

	// Top event types
	rows, err := db.QueryContext(ctx, `
		SELECT type, COUNT(*) as count
		FROM mink_events
		GROUP BY type
		ORDER BY count DESC
		LIMIT 5
	`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var t EventTypeCount
			if rows.Scan(&t.Type, &t.Count) == nil {
				stats.TopEventTypes = append(stats.TopEventTypes, t)
			}
		}
	}

	return stats, nil
}
