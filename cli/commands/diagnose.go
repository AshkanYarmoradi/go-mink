package commands

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"os"
	"runtime"
	"time"

	"github.com/AshkanYarmoradi/go-mink/cli/config"
	"github.com/AshkanYarmoradi/go-mink/cli/styles"
	"github.com/AshkanYarmoradi/go-mink/cli/ui"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
)

// NewDiagnoseCommand creates the diagnose command
func NewDiagnoseCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "diagnose",
		Short: "Run diagnostic checks",
		Long: `Run diagnostic checks on your mink setup.

This command verifies:
  • Configuration file validity
  • Database connectivity
  • Schema and table existence
  • Projection health
  • System requirements`,
		Aliases: []string{"diag", "doctor"},
		RunE:    runDiagnose,
	}

	return cmd
}

func runDiagnose(cmd *cobra.Command, args []string) error {
	fmt.Println()
	fmt.Println(ui.Banner())
	fmt.Println()
	fmt.Println(styles.Title.Render(styles.IconHealth + " Running Diagnostics"))
	fmt.Println()

	checks := []DiagnosticCheck{
		{Name: "Go Version", Check: checkGoVersion},
		{Name: "Configuration", Check: checkConfiguration},
		{Name: "Database Connection", Check: checkDatabaseConnection},
		{Name: "Event Store Schema", Check: checkEventStoreSchema},
		{Name: "Projections", Check: checkProjections},
		{Name: "System Resources", Check: checkSystemResources},
	}

	results := make([]CheckResult, 0, len(checks))
	allPassed := true

	for _, check := range checks {
		fmt.Printf("  %s Checking %s... ", styles.IconPending, check.Name)

		result := check.Check()
		results = append(results, result)

		if result.Status == StatusOK {
			fmt.Println(styles.SuccessStyle.Render("OK"))
		} else if result.Status == StatusWarning {
			fmt.Println(styles.WarningStyle.Render("WARNING"))
			allPassed = false
		} else {
			fmt.Println(styles.ErrorStyle.Render("FAILED"))
			allPassed = false
		}

		if result.Message != "" {
			fmt.Printf("    %s\n", styles.Muted.Render(result.Message))
		}
	}

	fmt.Println()
	fmt.Println(ui.Divider(50))
	fmt.Println()

	// Summary
	if allPassed {
		fmt.Println(styles.FormatSuccess("All checks passed! Your mink setup is healthy."))
	} else {
		fmt.Println(styles.FormatWarning("Some checks failed or have warnings."))
		fmt.Println()

		// Show recommendations
		fmt.Println(styles.Subtitle.Render("Recommendations:"))
		for _, r := range results {
			if r.Recommendation != "" {
				fmt.Printf("  %s %s\n", styles.IconArrow, r.Recommendation)
			}
		}
	}

	return nil
}

// CheckStatus represents the status of a diagnostic check
type CheckStatus int

const (
	StatusOK CheckStatus = iota
	StatusWarning
	StatusError
)

// CheckResult represents the result of a diagnostic check
type CheckResult struct {
	Name           string
	Status         CheckStatus
	Message        string
	Recommendation string
}

// DiagnosticCheck represents a diagnostic check function
type DiagnosticCheck struct {
	Name  string
	Check func() CheckResult
}

func checkGoVersion() CheckResult {
	version := runtime.Version()
	result := CheckResult{
		Name:    "Go Version",
		Status:  StatusOK,
		Message: version,
	}

	// Check if Go version is 1.21+
	if version < "go1.21" {
		result.Status = StatusWarning
		result.Recommendation = "Upgrade to Go 1.21 or later for best performance"
	}

	return result
}

func checkConfiguration() CheckResult {
	cwd, err := os.Getwd()
	if err != nil {
		return CheckResult{
			Name:           "Configuration",
			Status:         StatusError,
			Message:        err.Error(),
			Recommendation: "Check directory permissions",
		}
	}

	if !config.Exists(cwd) {
		return CheckResult{
			Name:           "Configuration",
			Status:         StatusWarning,
			Message:        "No mink.yaml found",
			Recommendation: "Run 'mink init' to create a configuration file",
		}
	}

	cfg, err := config.Load(cwd)
	if err != nil {
		return CheckResult{
			Name:           "Configuration",
			Status:         StatusError,
			Message:        fmt.Sprintf("Invalid config: %v", err),
			Recommendation: "Check mink.yaml syntax",
		}
	}

	// Validate config
	errors := cfg.Validate()
	if len(errors) > 0 {
		return CheckResult{
			Name:           "Configuration",
			Status:         StatusWarning,
			Message:        fmt.Sprintf("%d validation errors", len(errors)),
			Recommendation: errors[0],
		}
	}

	return CheckResult{
		Name:    "Configuration",
		Status:  StatusOK,
		Message: fmt.Sprintf("Project: %s, Driver: %s", cfg.Project.Name, cfg.Database.Driver),
	}
}

func checkDatabaseConnection() CheckResult {
	cwd, _ := os.Getwd()
	_, cfg, err := config.FindConfig(cwd)
	if err != nil {
		return CheckResult{
			Name:           "Database Connection",
			Status:         StatusWarning,
			Message:        "No configuration found",
			Recommendation: "Run 'mink init' first",
		}
	}

	if cfg.Database.Driver == "memory" {
		return CheckResult{
			Name:    "Database Connection",
			Status:  StatusOK,
			Message: "Using in-memory driver (no connection needed)",
		}
	}

	dbURL := os.ExpandEnv(cfg.Database.URL)
	if dbURL == "" || dbURL == "${DATABASE_URL}" {
		return CheckResult{
			Name:           "Database Connection",
			Status:         StatusWarning,
			Message:        "DATABASE_URL not set",
			Recommendation: "Set DATABASE_URL environment variable",
		}
	}

	// Try to connect
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	db, err := sql.Open("pgx", dbURL)
	if err != nil {
		return CheckResult{
			Name:           "Database Connection",
			Status:         StatusError,
			Message:        fmt.Sprintf("Failed to open: %v", err),
			Recommendation: "Check DATABASE_URL format",
		}
	}
	defer db.Close()

	if err := db.PingContext(ctx); err != nil {
		// Check if it's a network error
		if _, ok := err.(*net.OpError); ok {
			return CheckResult{
				Name:           "Database Connection",
				Status:         StatusError,
				Message:        "Cannot reach database server",
				Recommendation: "Check if PostgreSQL is running and accessible",
			}
		}
		return CheckResult{
			Name:           "Database Connection",
			Status:         StatusError,
			Message:        err.Error(),
			Recommendation: "Verify database credentials",
		}
	}

	// Get PostgreSQL version
	var version string
	_ = db.QueryRowContext(ctx, "SELECT version()").Scan(&version)
	if version != "" {
		// Truncate version string
		if len(version) > 50 {
			version = version[:50] + "..."
		}
	}

	return CheckResult{
		Name:    "Database Connection",
		Status:  StatusOK,
		Message: "Connected successfully",
	}
}

func checkEventStoreSchema() CheckResult {
	cwd, _ := os.Getwd()
	_, cfg, err := config.FindConfig(cwd)
	if err != nil || cfg.Database.Driver == "memory" {
		return CheckResult{
			Name:    "Event Store Schema",
			Status:  StatusOK,
			Message: "Skipped (memory driver or no config)",
		}
	}

	dbURL := os.ExpandEnv(cfg.Database.URL)
	if dbURL == "" || dbURL == "${DATABASE_URL}" {
		return CheckResult{
			Name:    "Event Store Schema",
			Status:  StatusWarning,
			Message: "Skipped (no database URL)",
		}
	}

	db, err := sql.Open("pgx", dbURL)
	if err != nil {
		return CheckResult{
			Name:    "Event Store Schema",
			Status:  StatusError,
			Message: err.Error(),
		}
	}
	defer db.Close()

	// Check if events table exists
	var exists bool
	err = db.QueryRow(`
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables 
			WHERE table_name = $1
		)
	`, cfg.EventStore.TableName).Scan(&exists)

	if err != nil {
		return CheckResult{
			Name:           "Event Store Schema",
			Status:         StatusError,
			Message:        err.Error(),
			Recommendation: "Check database permissions",
		}
	}

	if !exists {
		return CheckResult{
			Name:           "Event Store Schema",
			Status:         StatusWarning,
			Message:        fmt.Sprintf("Table '%s' not found", cfg.EventStore.TableName),
			Recommendation: "Run 'mink migrate up' to create tables",
		}
	}

	// Count events
	var count int64
	_ = db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s", cfg.EventStore.TableName)).Scan(&count)

	return CheckResult{
		Name:    "Event Store Schema",
		Status:  StatusOK,
		Message: fmt.Sprintf("Table exists (%d events)", count),
	}
}

func checkProjections() CheckResult {
	cwd, _ := os.Getwd()
	_, cfg, err := config.FindConfig(cwd)
	if err != nil || cfg.Database.Driver == "memory" {
		return CheckResult{
			Name:    "Projections",
			Status:  StatusOK,
			Message: "Skipped (memory driver or no config)",
		}
	}

	dbURL := os.ExpandEnv(cfg.Database.URL)
	if dbURL == "" || dbURL == "${DATABASE_URL}" {
		return CheckResult{
			Name:    "Projections",
			Status:  StatusWarning,
			Message: "Skipped (no database URL)",
		}
	}

	db, err := sql.Open("pgx", dbURL)
	if err != nil {
		return CheckResult{
			Name:    "Projections",
			Status:  StatusError,
			Message: err.Error(),
		}
	}
	defer db.Close()

	// Check checkpoints table
	var exists bool
	_ = db.QueryRow(`
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables 
			WHERE table_name = 'mink_checkpoints'
		)
	`).Scan(&exists)

	if !exists {
		return CheckResult{
			Name:    "Projections",
			Status:  StatusOK,
			Message: "No projections registered yet",
		}
	}

	// Count projections
	var total, behind int64
	_ = db.QueryRow("SELECT COUNT(*) FROM mink_checkpoints").Scan(&total)

	// Check for projections behind
	var maxPosition int64
	_ = db.QueryRow("SELECT COALESCE(MAX(global_position), 0) FROM mink_events").Scan(&maxPosition)
	_ = db.QueryRow("SELECT COUNT(*) FROM mink_checkpoints WHERE position < $1", maxPosition).Scan(&behind)

	if behind > 0 {
		return CheckResult{
			Name:           "Projections",
			Status:         StatusWarning,
			Message:        fmt.Sprintf("%d/%d projections behind", behind, total),
			Recommendation: "Check projection workers or run 'mink projection status'",
		}
	}

	return CheckResult{
		Name:    "Projections",
		Status:  StatusOK,
		Message: fmt.Sprintf("%d projections, all up to date", total),
	}
}

func checkSystemResources() CheckResult {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	// Convert to MB
	allocMB := float64(m.Alloc) / 1024 / 1024
	sysMB := float64(m.Sys) / 1024 / 1024

	message := fmt.Sprintf("Memory: %.1f MB used, %.1f MB total", allocMB, sysMB)

	// Warning if using too much memory
	if allocMB > 500 {
		return CheckResult{
			Name:           "System Resources",
			Status:         StatusWarning,
			Message:        message,
			Recommendation: "Consider optimizing memory usage",
		}
	}

	return CheckResult{
		Name:    "System Resources",
		Status:  StatusOK,
		Message: message,
	}
}

// NewVersionCommand creates the version command
func NewVersionCommand(version, commit, date string) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Show version information",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println()
			fmt.Println(ui.SimpleBanner())
			fmt.Println()

			table := ui.NewTable("", "")
			table.AddRow("Version", version)
			table.AddRow("Commit", commit)
			table.AddRow("Built", date)
			table.AddRow("Go", runtime.Version())
			table.AddRow("OS/Arch", fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH))

			fmt.Println(table.Render())

			return nil
		},
	}
}

// AnimatedVersion shows an animated version display
type AnimatedVersionModel struct {
	version string
	done    bool
	phase   int
}

func NewAnimatedVersion(version string) AnimatedVersionModel {
	return AnimatedVersionModel{version: version}
}

func (m AnimatedVersionModel) Init() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg {
		return ui.AnimationTickMsg{}
	})
}

func (m AnimatedVersionModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg.(type) {
	case ui.AnimationTickMsg:
		m.phase++
		if m.phase > 5 {
			m.done = true
			return m, tea.Quit
		}
		return m, tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg {
			return ui.AnimationTickMsg{}
		})
	case tea.KeyMsg:
		return m, tea.Quit
	}
	return m, nil
}

func (m AnimatedVersionModel) View() string {
	if m.done {
		return ui.SimpleBanner() + "\n"
	}

	phases := []string{
		styles.IconMink,
		styles.IconMink + " ▪",
		styles.IconMink + " ▪▪",
		styles.IconMink + " ▪▪▪",
		styles.IconMink + " mink",
		ui.SimpleBanner(),
	}

	return "\n" + phases[m.phase] + "\n"
}
