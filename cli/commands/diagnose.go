package commands

import (
	"context"
	"fmt"
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

// newCheckResult creates a CheckResult with the given name.
func newCheckResult(name string, status CheckStatus, message string) CheckResult {
	return CheckResult{Name: name, Status: status, Message: message}
}

// withRecommendation adds a recommendation to a CheckResult.
func (r CheckResult) withRecommendation(rec string) CheckResult {
	r.Recommendation = rec
	return r
}

// DiagnosticCheck represents a diagnostic check function
type DiagnosticCheck struct {
	Name  string
	Check func() CheckResult
}

func checkGoVersion() CheckResult {
	version := runtime.Version()
	if version < "go1.21" {
		return newCheckResult("Go Version", StatusWarning, version).
			withRecommendation("Upgrade to Go 1.21 or later for best performance")
	}
	return newCheckResult("Go Version", StatusOK, version)
}

func checkConfiguration() CheckResult {
	const name = "Configuration"
	cwd, err := os.Getwd()
	if err != nil {
		return newCheckResult(name, StatusError, err.Error()).withRecommendation("Check directory permissions")
	}
	if !config.Exists(cwd) {
		return newCheckResult(name, StatusWarning, "No mink.yaml found").
			withRecommendation("Run 'mink init' to create a configuration file")
	}
	cfg, err := config.Load(cwd)
	if err != nil {
		return newCheckResult(name, StatusError, fmt.Sprintf("Invalid config: %v", err)).
			withRecommendation("Check mink.yaml syntax")
	}
	if errors := cfg.Validate(); len(errors) > 0 {
		return newCheckResult(name, StatusWarning, fmt.Sprintf("%d validation errors", len(errors))).
			withRecommendation(errors[0])
	}
	return newCheckResult(name, StatusOK, fmt.Sprintf("Project: %s, Driver: %s", cfg.Project.Name, cfg.Database.Driver))
}

func checkDatabaseConnection() CheckResult {
	const name = "Database Connection"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	env, skipReason, err := SetupDiagnosticEnv(ctx)
	switch skipReason {
	case DiagnosticSkipNoConfig:
		return newCheckResult(name, StatusWarning, "No configuration found").withRecommendation("Run 'mink init' first")
	case DiagnosticSkipMemoryDriver:
		return newCheckResult(name, StatusOK, "Using in-memory driver (no connection needed)")
	case DiagnosticSkipNoDBURL:
		return newCheckResult(name, StatusWarning, "DATABASE_URL not set").withRecommendation("Set DATABASE_URL environment variable")
	}
	if err != nil {
		return newCheckResult(name, StatusError, err.Error()).withRecommendation("Verify database credentials")
	}
	defer env.Close()

	info, err := env.Adapter.GetDiagnosticInfo(ctx)
	if err != nil {
		return newCheckResult(name, StatusError, err.Error()).withRecommendation("Check database server status")
	}
	if !info.Connected {
		return newCheckResult(name, StatusError, info.Message).withRecommendation("Verify database credentials")
	}
	return newCheckResult(name, StatusOK, info.Message)
}

func checkEventStoreSchema() CheckResult {
	const name = "Event Store Schema"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	env, skipReason, err := SetupDiagnosticEnv(ctx)
	if skipReason == DiagnosticSkipNoConfig || skipReason == DiagnosticSkipMemoryDriver {
		return newCheckResult(name, StatusOK, "Skipped (memory driver or no config)")
	}
	if skipReason == DiagnosticSkipNoDBURL {
		return newCheckResult(name, StatusWarning, "Skipped (no database URL)")
	}
	if err != nil {
		return newCheckResult(name, StatusError, err.Error()).withRecommendation("Check database connection")
	}
	defer env.Close()

	result, err := env.Adapter.CheckSchema(ctx, env.Config.EventStore.TableName)
	if err != nil {
		return newCheckResult(name, StatusError, err.Error()).withRecommendation("Check database permissions")
	}
	if !result.TableExists {
		return newCheckResult(name, StatusWarning, result.Message).withRecommendation("Run 'mink migrate up' to create tables")
	}
	return newCheckResult(name, StatusOK, result.Message)
}

func checkProjections() CheckResult {
	const name = "Projections"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	env, skipReason, err := SetupDiagnosticEnv(ctx)
	if skipReason == DiagnosticSkipNoConfig || skipReason == DiagnosticSkipMemoryDriver {
		return newCheckResult(name, StatusOK, "Skipped (memory driver or no config)")
	}
	if skipReason == DiagnosticSkipNoDBURL {
		return newCheckResult(name, StatusWarning, "Skipped (no database URL)")
	}
	if err != nil {
		return newCheckResult(name, StatusError, err.Error()).withRecommendation("Check database connection")
	}
	defer env.Close()

	health, err := env.Adapter.GetProjectionHealth(ctx)
	if err != nil {
		return newCheckResult(name, StatusError, err.Error())
	}
	if health.TotalProjections == 0 || health.ProjectionsBehind == 0 {
		return newCheckResult(name, StatusOK, health.Message)
	}
	return newCheckResult(name, StatusWarning, health.Message).
		withRecommendation("Check projection workers or run 'mink projection status'")
}

func checkSystemResources() CheckResult {
	const name = "System Resources"
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	allocMB := float64(m.Alloc) / 1024 / 1024
	sysMB := float64(m.Sys) / 1024 / 1024
	message := fmt.Sprintf("Memory: %.1f MB used, %.1f MB total", allocMB, sysMB)

	if allocMB > 500 {
		return newCheckResult(name, StatusWarning, message).withRecommendation("Consider optimizing memory usage")
	}
	return newCheckResult(name, StatusOK, message)
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
