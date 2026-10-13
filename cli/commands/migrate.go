package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"
	"go-mink.dev/cli/styles"
	"go-mink.dev/cli/ui"
)

// NewMigrateCommand creates the migrate command
func NewMigrateCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Manage database migrations",
		Long: `Run and manage database schema migrations.

Examples:
  mink migrate up           # Apply all pending migrations
  mink migrate down         # Rollback the last migration
  mink migrate status       # Show migration status
  mink migrate create NAME  # Create a new migration file`,
	}

	cmd.AddCommand(newMigrateUpCommand())
	cmd.AddCommand(newMigrateDownCommand())
	cmd.AddCommand(newMigrateStatusCommand())
	cmd.AddCommand(newMigrateCreateCommand())

	return requireSubcommand(cmd)
}

func newMigrateUpCommand() *cobra.Command {
	var steps int
	var nonInteractive bool

	cmd := &cobra.Command{
		Use:   "up",
		Short: "Apply pending migrations",
		Long: `Apply pending database migrations.

By default, applies all pending migrations. Use --steps to limit.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			env, isMemory, err := SetupMigrationEnv(ctx)
			if err != nil {
				return err
			}
			if isMemory {
				fmt.Println(styles.FormatInfo("Memory driver doesn't require migrations"))
				return nil
			}
			defer env.Close()

			// Show spinner while connecting (skip if --non-interactive)
			if !nonInteractive {
				spinner := ui.NewSpinner("Connecting to database...", ui.SpinnerDots)
				p := tea.NewProgram(spinner)

				go func() {
					time.Sleep(500 * time.Millisecond)
					p.Send(ui.SpinnerDoneMsg{Result: "Connected to database"})
				}()

				if _, err := p.Run(); err != nil {
					return err
				}
			}

			return runMigrateUp(ctx, env, steps)
		},
	}

	cmd.Flags().IntVarP(&steps, "steps", "n", 0, "Number of migrations to apply (0 = all)")
	cmd.Flags().BoolVar(&nonInteractive, "non-interactive", false, "Skip interactive elements (for scripting)")

	return cmd
}

// runMigrateUp applies pending migrations from env.MigrationsDir using the
// adapter. If steps > 0, at most that many migrations are applied. It is
// separated from the cobra closure so the apply/record logic is unit-testable.
//
// A RecordMigration failure is fatal: the migration SQL was already executed,
// so a failure to record leaves the recorded state inconsistent with the
// database. Applying further migrations could re-run non-idempotent SQL on the
// next invocation, so this returns an error and stops immediately.
func runMigrateUp(ctx context.Context, env *MigrationEnv, steps int) error {
	pending, err := getPendingMigrations(ctx, env.Adapter, env.MigrationsDir)
	if err != nil {
		return err
	}

	if len(pending) == 0 {
		fmt.Println(styles.FormatSuccess("Database is up to date"))
		return nil
	}

	if steps > 0 && steps < len(pending) {
		pending = pending[:steps]
	}

	fmt.Printf("\n%s Applying %d migration(s)...\n\n", styles.IconPending, len(pending))

	for _, m := range pending {
		fmt.Printf("  %s Applying %s... ", styles.IconPending, m.Name)

		content, err := os.ReadFile(m.Path)
		if err != nil {
			fmt.Println(styles.ErrorStyle.Render("FAILED"))
			return fmt.Errorf("failed to read migration: %w", err)
		}

		// Execute migration using adapter
		if err := env.Adapter.ExecuteSQL(ctx, string(content)); err != nil {
			fmt.Println(styles.ErrorStyle.Render("FAILED"))
			return fmt.Errorf("migration failed: %w", err)
		}

		// Record migration using adapter. A failure here is fatal (see the
		// function doc): stop and surface the error so the operator knows the
		// recorded state is inconsistent.
		if err := env.Adapter.RecordMigration(ctx, m.Name); err != nil {
			fmt.Println(styles.ErrorStyle.Render("FAILED"))
			return fmt.Errorf("migration %q applied but failed to record (recorded state is now inconsistent; "+
				"reconcile the migrations table before retrying): %w", m.Name, err)
		}
		fmt.Println(styles.SuccessStyle.Render("OK"))
	}

	fmt.Println()
	fmt.Println(styles.FormatSuccess(fmt.Sprintf("Applied %d migration(s)", len(pending))))
	return nil
}

// rollbackConfirmer asks the operator to confirm rolling back the named
// migrations (listed most recent first). A nil rollbackConfirmer means the
// confirmation was waived with --yes / --non-interactive.
type rollbackConfirmer func(names []string) (bool, error)

func newMigrateDownCommand() *cobra.Command {
	return newMigrateDownCommandWithConfirm(confirmRollbackInteractive)
}

// newMigrateDownCommandWithConfirm builds the down command with an explicit
// confirmer so the confirmation flow can be exercised without a terminal.
func newMigrateDownCommandWithConfirm(confirm rollbackConfirmer) *cobra.Command {
	var steps int
	var nonInteractive bool
	var yes bool

	cmd := &cobra.Command{
		Use:   "down",
		Short: "Rollback migrations",
		Long: `Rollback applied database migrations.

By default, rolls back the last migration. Use --steps to rollback more.

Rolling back executes the matching .down.sql files, which is destructive, so
the command asks for confirmation first. Pass --yes (or --non-interactive) to
skip the prompt when scripting.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			env, isMemory, err := SetupMigrationEnv(ctx)
			if err != nil {
				return err
			}
			if isMemory {
				fmt.Println(styles.FormatInfo("Memory driver doesn't require migrations"))
				return nil
			}
			defer env.Close()

			return runMigrateDown(ctx, env, steps, resolveRollbackConfirmer(confirm, yes, nonInteractive))
		},
	}

	cmd.Flags().IntVarP(&steps, "steps", "n", 1, "Number of migrations to rollback")
	cmd.Flags().BoolVar(&nonInteractive, "non-interactive", false, "Skip interactive elements, including the rollback confirmation (for scripting)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Skip the rollback confirmation prompt")

	return cmd
}

// resolveRollbackConfirmer returns the confirmer `mink migrate down` must
// consult before executing any down migration: nil (no prompt) when --yes or
// --non-interactive was passed, otherwise confirm itself. It is the only place
// the two flags are interpreted, so the waiver semantics are testable without
// a database or a terminal.
func resolveRollbackConfirmer(confirm rollbackConfirmer, yes, nonInteractive bool) rollbackConfirmer {
	if yes || nonInteractive {
		return nil
	}
	return confirm
}

// runMigrateDown rolls back the most recently applied migrations recorded in
// the migrations table. If steps is less than 1, a single migration is rolled
// back. When confirm is non-nil it is consulted, with the names about to be
// rolled back, before any SQL runs; a declined confirmation is not an error.
//
// Migration names come from the database and are validated by
// getAppliedMigrations before they are turned into file paths; the derived
// .down.sql path is checked against the migrations directory again here so
// this loop never silently depends on that invariant.
func runMigrateDown(ctx context.Context, env *MigrationEnv, steps int, confirm rollbackConfirmer) error {
	applied, err := getAppliedMigrations(ctx, env.Adapter, env.MigrationsDir)
	if err != nil {
		return err
	}

	if len(applied) == 0 {
		fmt.Println(styles.FormatInfo("No migrations to rollback"))
		return nil
	}

	// Reverse order for rollback
	toRollback := applied
	if steps < 1 {
		steps = 1
	}
	if steps < len(toRollback) {
		toRollback = toRollback[len(toRollback)-steps:]
	}

	if confirm != nil {
		names := make([]string, 0, len(toRollback))
		for i := len(toRollback) - 1; i >= 0; i-- {
			names = append(names, toRollback[i].Name)
		}
		confirmed, err := confirm(names)
		if err != nil {
			return fmt.Errorf("rollback confirmation failed (pass --yes to skip the prompt when scripting): %w", err)
		}
		if !confirmed {
			fmt.Println(styles.FormatInfo("Cancelled"))
			return nil
		}
	}

	fmt.Printf("\n%s Rolling back %d migration(s)...\n\n", styles.IconWarning, len(toRollback))

	for i := len(toRollback) - 1; i >= 0; i-- {
		m := toRollback[i]
		fmt.Printf("  %s Rolling back %s... ", styles.IconPending, m.Name)

		// Look for down migration
		downPath := strings.TrimSuffix(m.Path, ".sql") + ".down.sql"
		if err := ensureInsideDir(env.MigrationsDir, downPath); err != nil {
			fmt.Println(styles.ErrorStyle.Render("FAILED"))
			return fmt.Errorf("refusing to run down migration for %q: %w", m.Name, err)
		}
		if _, err := os.Stat(downPath); os.IsNotExist(err) {
			fmt.Println(styles.WarningStyle.Render("SKIPPED (no down migration)"))
			continue
		}

		content, err := os.ReadFile(downPath)
		if err != nil {
			fmt.Println(styles.ErrorStyle.Render("FAILED"))
			return fmt.Errorf("failed to read down migration: %w", err)
		}

		if err := env.Adapter.ExecuteSQL(ctx, string(content)); err != nil {
			fmt.Println(styles.ErrorStyle.Render("FAILED"))
			return fmt.Errorf("rollback failed: %w", err)
		}

		if err := env.Adapter.RemoveMigrationRecord(ctx, m.Name); err != nil {
			fmt.Println(styles.ErrorStyle.Render("FAILED"))
			return fmt.Errorf("failed to remove migration record %q (schema rolled back but the record remains — the migrations table is now inconsistent): %w", m.Name, err)
		}
		fmt.Println(styles.SuccessStyle.Render("OK"))
	}

	fmt.Println()
	fmt.Println(styles.FormatSuccess("Rollback complete"))
	return nil
}

// confirmRollbackInteractive is the production rollbackConfirmer: a yes/no
// prompt that lists the migrations about to be rolled back. It defaults to
// "No", and a prompt that cannot be shown (no terminal) surfaces as an error,
// so an unattended run never rolls anything back by accident.
func confirmRollbackInteractive(names []string) (bool, error) {
	var confirmed bool
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewConfirm().
				Title(fmt.Sprintf("Roll back %d migration(s)?", len(names))).
				Description("This executes the down migration(s) for:\n  " + strings.Join(names, "\n  ") +
					"\nThe SQL is destructive and is not undone automatically.").
				Value(&confirmed),
		),
	).WithTheme(huh.ThemeDracula())

	if err := form.Run(); err != nil {
		return false, err
	}
	return confirmed, nil
}

func newMigrateStatusCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show migration status",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			env, isMemory, err := SetupMigrationEnv(ctx)
			if err != nil {
				return err
			}
			if isMemory {
				fmt.Println(styles.FormatInfo("Memory driver doesn't use migrations"))
				return nil
			}
			defer env.Close()

			// Get all migrations
			all, err := getAllMigrations(env.MigrationsDir)
			if err != nil {
				return err
			}

			// Get applied migrations using adapter
			applied, err := env.Adapter.GetAppliedMigrations(ctx)
			if err != nil {
				return fmt.Errorf("failed to read applied migrations: %w", err)
			}

			appliedSet := make(map[string]bool)
			for _, name := range applied {
				appliedSet[name] = true
			}

			// Create table
			table := ui.NewTable("Status", "Migration", "Applied")

			pendingCount := 0
			for _, m := range all {
				status := ui.StatusBadge("applied")
				appliedAt := "-"
				if !appliedSet[m.Name] {
					status = ui.StatusBadge("pending")
					pendingCount++
				} else {
					appliedAt = "✓"
				}
				table.AddRow(status, m.Name, appliedAt)
			}

			fmt.Println()
			fmt.Println(styles.Title.Render(styles.IconDatabase + " Migration Status"))
			fmt.Println()
			fmt.Println(table.Render())
			fmt.Println()

			if pendingCount > 0 {
				fmt.Println(styles.FormatWarning(fmt.Sprintf("%d pending migration(s)", pendingCount)))
			} else {
				fmt.Println(styles.FormatSuccess("Database is up to date"))
			}

			return nil
		},
	}
}

func newMigrateCreateCommand() *cobra.Command {
	var sqlContent string

	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new migration file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			cfg, cwd, err := loadConfigOrDefault()
			if err != nil {
				return err
			}

			migrationsDir := filepath.Join(cwd, cfg.Database.MigrationsDir)
			if err := os.MkdirAll(migrationsDir, 0755); err != nil {
				return err
			}

			// Get next migration number
			all, _ := getAllMigrations(migrationsDir)
			nextNum := len(all) + 1

			// Create migration files
			timestamp := time.Now().Format("20060102150405")
			baseName := fmt.Sprintf("%03d_%s_%s", nextNum, timestamp, sanitizeName(name))

			upPath := filepath.Join(migrationsDir, baseName+".sql")
			downPath := filepath.Join(migrationsDir, baseName+".down.sql")

			var upContent string
			if sqlContent != "" {
				upContent = sqlContent
			} else {
				upContent = fmt.Sprintf(`-- Migration: %s
-- Created: %s

-- Write your UP migration here
`, name, time.Now().Format(time.RFC3339))
			}

			downContent := fmt.Sprintf(`-- Rollback: %s
-- Created: %s

-- Write your DOWN migration here
`, name, time.Now().Format(time.RFC3339))

			if err := os.WriteFile(upPath, []byte(upContent), 0644); err != nil {
				return err
			}
			fmt.Println(styles.FormatSuccess(fmt.Sprintf("Created %s", upPath)))

			if err := os.WriteFile(downPath, []byte(downContent), 0644); err != nil {
				return err
			}
			fmt.Println(styles.FormatSuccess(fmt.Sprintf("Created %s", downPath)))

			return nil
		},
	}

	cmd.Flags().StringVar(&sqlContent, "sql", "", "SQL content for the up migration")

	return cmd
}

// Migration represents a migration file
type Migration struct {
	Name string
	Path string
}

func getAllMigrations(dir string) ([]Migration, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var migrations []Migration
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".sql") && !strings.HasSuffix(name, ".down.sql") {
			migrations = append(migrations, Migration{
				Name: strings.TrimSuffix(name, ".sql"),
				Path: filepath.Join(dir, name),
			})
		}
	}

	sort.Slice(migrations, func(i, j int) bool {
		return migrations[i].Name < migrations[j].Name
	})

	return migrations, nil
}

func getPendingMigrations(ctx context.Context, adapter CLIAdapter, migrationsDir string) ([]Migration, error) {
	all, err := getAllMigrations(migrationsDir)
	if err != nil {
		return nil, err
	}

	applied, err := adapter.GetAppliedMigrations(ctx)
	if err != nil {
		// Do not guess: treating every migration as pending on a read failure
		// would re-run already-applied, possibly non-idempotent SQL.
		return nil, fmt.Errorf("failed to read applied migrations: %w", err)
	}

	appliedSet := make(map[string]bool)
	for _, name := range applied {
		appliedSet[name] = true
	}

	var pending []Migration
	for _, m := range all {
		if !appliedSet[m.Name] {
			pending = append(pending, m)
		}
	}

	return pending, nil
}

// getAppliedMigrations reads the applied migration names from the migrations
// table and maps each to its up-migration file inside migrationsDir. The
// names are database rows, not trusted input: every name is validated and its
// resolved path checked against migrationsDir, and a single bad row fails the
// whole read rather than being skipped.
func getAppliedMigrations(ctx context.Context, adapter CLIAdapter, migrationsDir string) ([]Migration, error) {
	applied, err := adapter.GetAppliedMigrations(ctx)
	if err != nil {
		// Same wrapper as getPendingMigrations, so `migrate up`, `migrate down`
		// and `migrate status` all report a failed read the same way.
		return nil, fmt.Errorf("failed to read applied migrations: %w", err)
	}

	var migrations []Migration
	for _, name := range applied {
		path, err := migrationFilePath(migrationsDir, name)
		if err != nil {
			return nil, err
		}
		migrations = append(migrations, Migration{
			Name: name,
			Path: path,
		})
	}

	return migrations, nil
}

// migrationNameRe matches the migration names the CLI accepts when reading
// them back from the migrations table: a plain file stem with no path
// separators.
var migrationNameRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// validateMigrationName rejects migration names that are not plain file
// stems. A poisoned row in the migrations table must not be able to steer
// `mink migrate down` to a .down.sql outside the migrations directory.
func validateMigrationName(name string) error {
	if !migrationNameRe.MatchString(name) || strings.Contains(name, "..") {
		return fmt.Errorf("invalid migration name %q in migrations table: names may only contain letters, digits, '_', '.' and '-', and must not contain \"..\"", name)
	}
	return nil
}

// migrationFilePath returns the path of the up migration file for name inside
// migrationsDir, after validating the name and checking that the resolved
// path stays inside the directory.
func migrationFilePath(migrationsDir, name string) (string, error) {
	if err := validateMigrationName(name); err != nil {
		return "", err
	}
	path := filepath.Join(migrationsDir, name+".sql")
	if err := ensureInsideDir(migrationsDir, path); err != nil {
		return "", fmt.Errorf("invalid migration name %q in migrations table: %w", name, err)
	}
	return path, nil
}

func sanitizeName(name string) string {
	name = strings.ToLower(name)
	name = strings.ReplaceAll(name, " ", "_")
	name = strings.ReplaceAll(name, "-", "_")
	return name
}
