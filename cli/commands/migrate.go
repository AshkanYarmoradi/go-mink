package commands

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/AshkanYarmoradi/go-mink/cli/config"
	"github.com/AshkanYarmoradi/go-mink/cli/styles"
	"github.com/AshkanYarmoradi/go-mink/cli/ui"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
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

	return cmd
}

func newMigrateUpCommand() *cobra.Command {
	var steps int

	cmd := &cobra.Command{
		Use:   "up",
		Short: "Apply pending migrations",
		Long: `Apply pending database migrations.

By default, applies all pending migrations. Use --steps to limit.`,
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
				fmt.Println(styles.FormatInfo("Memory driver doesn't require migrations"))
				return nil
			}

			dbURL := os.ExpandEnv(cfg.Database.URL)
			if dbURL == "" || dbURL == "${DATABASE_URL}" {
				return fmt.Errorf("DATABASE_URL environment variable is not set")
			}

			// Show spinner while connecting
			spinner := ui.NewSpinner("Connecting to database...", ui.SpinnerDots)
			p := tea.NewProgram(spinner)

			go func() {
				time.Sleep(500 * time.Millisecond)
				p.Send(ui.SpinnerDoneMsg{Result: "Connected to database"})
			}()

			if _, err := p.Run(); err != nil {
				return err
			}

			// Get pending migrations
			migrationsDir := filepath.Join(cwd, cfg.Database.MigrationsDir)
			pending, err := getPendingMigrations(dbURL, migrationsDir)
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

			db, err := sql.Open("pgx", dbURL)
			if err != nil {
				return err
			}
			defer db.Close()

			for _, m := range pending {
				fmt.Printf("  %s Applying %s... ", styles.IconPending, m.Name)

				content, err := os.ReadFile(m.Path)
				if err != nil {
					fmt.Println(styles.ErrorStyle.Render("FAILED"))
					return fmt.Errorf("failed to read migration: %w", err)
				}

				// Execute migration
				if _, err := db.Exec(string(content)); err != nil {
					fmt.Println(styles.ErrorStyle.Render("FAILED"))
					return fmt.Errorf("migration failed: %w", err)
				}

				// Record migration
				if err := recordMigration(db, m.Name); err != nil {
					fmt.Println(styles.WarningStyle.Render("WARNING"))
					fmt.Printf("    %s\n", styles.FormatWarning("Migration applied but not recorded"))
				} else {
					fmt.Println(styles.SuccessStyle.Render("OK"))
				}
			}

			fmt.Println()
			fmt.Println(styles.FormatSuccess(fmt.Sprintf("Applied %d migration(s)", len(pending))))
			return nil
		},
	}

	cmd.Flags().IntVarP(&steps, "steps", "n", 0, "Number of migrations to apply (0 = all)")

	return cmd
}

func newMigrateDownCommand() *cobra.Command {
	var steps int

	cmd := &cobra.Command{
		Use:   "down",
		Short: "Rollback migrations",
		Long: `Rollback applied database migrations.

By default, rolls back the last migration. Use --steps to rollback more.`,
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
				fmt.Println(styles.FormatInfo("Memory driver doesn't require migrations"))
				return nil
			}

			dbURL := os.ExpandEnv(cfg.Database.URL)
			if dbURL == "" || dbURL == "${DATABASE_URL}" {
				return fmt.Errorf("DATABASE_URL environment variable is not set")
			}

			migrationsDir := filepath.Join(cwd, cfg.Database.MigrationsDir)
			applied, err := getAppliedMigrations(dbURL, migrationsDir)
			if err != nil {
				return err
			}

			if len(applied) == 0 {
				fmt.Println(styles.FormatInfo("No migrations to rollback"))
				return nil
			}

			// Reverse order for rollback
			toRollback := applied
			if steps == 0 {
				steps = 1
			}
			if steps < len(toRollback) {
				toRollback = toRollback[len(toRollback)-steps:]
			}

			fmt.Printf("\n%s Rolling back %d migration(s)...\n\n", styles.IconWarning, len(toRollback))

			db, err := sql.Open("pgx", dbURL)
			if err != nil {
				return err
			}
			defer db.Close()

			for i := len(toRollback) - 1; i >= 0; i-- {
				m := toRollback[i]
				fmt.Printf("  %s Rolling back %s... ", styles.IconPending, m.Name)

				// Look for down migration
				downPath := strings.TrimSuffix(m.Path, ".sql") + ".down.sql"
				if _, err := os.Stat(downPath); os.IsNotExist(err) {
					fmt.Println(styles.WarningStyle.Render("SKIPPED (no down migration)"))
					continue
				}

				content, err := os.ReadFile(downPath)
				if err != nil {
					fmt.Println(styles.ErrorStyle.Render("FAILED"))
					return fmt.Errorf("failed to read down migration: %w", err)
				}

				if _, err := db.Exec(string(content)); err != nil {
					fmt.Println(styles.ErrorStyle.Render("FAILED"))
					return fmt.Errorf("rollback failed: %w", err)
				}

				if err := removeMigrationRecord(db, m.Name); err != nil {
					fmt.Println(styles.WarningStyle.Render("WARNING"))
				} else {
					fmt.Println(styles.SuccessStyle.Render("OK"))
				}
			}

			fmt.Println()
			fmt.Println(styles.FormatSuccess("Rollback complete"))
			return nil
		},
	}

	cmd.Flags().IntVarP(&steps, "steps", "n", 1, "Number of migrations to rollback")

	return cmd
}

func newMigrateStatusCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show migration status",
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
				fmt.Println(styles.FormatInfo("Memory driver doesn't use migrations"))
				return nil
			}

			dbURL := os.ExpandEnv(cfg.Database.URL)
			if dbURL == "" || dbURL == "${DATABASE_URL}" {
				return fmt.Errorf("DATABASE_URL environment variable is not set")
			}

			migrationsDir := filepath.Join(cwd, cfg.Database.MigrationsDir)

			// Get all migrations
			all, err := getAllMigrations(migrationsDir)
			if err != nil {
				return err
			}

			// Get applied migrations
			applied, err := getAppliedMigrationNames(dbURL)
			if err != nil {
				return err
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
	return &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new migration file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			cwd, err := os.Getwd()
			if err != nil {
				return err
			}

			_, cfg, err := config.FindConfig(cwd)
			if err != nil {
				cfg = config.DefaultConfig()
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

			upContent := fmt.Sprintf(`-- Migration: %s
-- Created: %s

-- Write your UP migration here
`, name, time.Now().Format(time.RFC3339))

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

func getPendingMigrations(dbURL, migrationsDir string) ([]Migration, error) {
	all, err := getAllMigrations(migrationsDir)
	if err != nil {
		return nil, err
	}

	applied, err := getAppliedMigrationNames(dbURL)
	if err != nil {
		// If we can't get applied migrations, assume all are pending
		return all, nil
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

func getAppliedMigrations(dbURL, migrationsDir string) ([]Migration, error) {
	applied, err := getAppliedMigrationNames(dbURL)
	if err != nil {
		return nil, err
	}

	var migrations []Migration
	for _, name := range applied {
		migrations = append(migrations, Migration{
			Name: name,
			Path: filepath.Join(migrationsDir, name+".sql"),
		})
	}

	return migrations, nil
}

func getAppliedMigrationNames(dbURL string) ([]string, error) {
	db, err := sql.Open("pgx", dbURL)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	// Ensure migrations table exists
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS mink_migrations (
			name VARCHAR(255) PRIMARY KEY,
			applied_at TIMESTAMPTZ DEFAULT NOW()
		)
	`)
	if err != nil {
		return nil, err
	}

	rows, err := db.Query("SELECT name FROM mink_migrations ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}

	return names, rows.Err()
}

func recordMigration(db *sql.DB, name string) error {
	_, err := db.Exec("INSERT INTO mink_migrations (name) VALUES ($1)", name)
	return err
}

func removeMigrationRecord(db *sql.DB, name string) error {
	_, err := db.Exec("DELETE FROM mink_migrations WHERE name = $1", name)
	return err
}

func sanitizeName(name string) string {
	name = strings.ToLower(name)
	name = strings.ReplaceAll(name, " ", "_")
	name = strings.ReplaceAll(name, "-", "_")
	return name
}
