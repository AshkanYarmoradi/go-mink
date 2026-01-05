// Package commands provides the CLI command implementations for mink.
package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/AshkanYarmoradi/go-mink/cli/config"
	"github.com/AshkanYarmoradi/go-mink/cli/styles"
	"github.com/AshkanYarmoradi/go-mink/cli/ui"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
)

// NewInitCommand creates the init command
func NewInitCommand() *cobra.Command {
	var (
		name       string
		module     string
		driver     string
		nonInteractive bool
	)
	
	cmd := &cobra.Command{
		Use:   "init [directory]",
		Short: "Initialize a new mink project",
		Long: `Initialize a new mink project with the required configuration and directory structure.

This command will:
  • Create a mink.yaml configuration file
  • Set up the recommended directory structure
  • Generate initial migration files for the event store

Examples:
  mink init                    # Initialize in current directory
  mink init my-project         # Initialize in a new directory
  mink init --driver=postgres  # Use PostgreSQL driver`,
		
		RunE: func(cmd *cobra.Command, args []string) error {
			// Determine target directory
			dir := "."
			if len(args) > 0 {
				dir = args[0]
			}
			
			// Make absolute path
			absDir, err := filepath.Abs(dir)
			if err != nil {
				return err
			}
			
			// Check if config already exists
			if config.Exists(absDir) {
				fmt.Println(styles.FormatWarning("mink.yaml already exists in this directory"))
				return nil
			}
			
			// Print welcome banner
			fmt.Println(ui.Banner())
			fmt.Println()
			
			cfg := config.DefaultConfig()
			
			// Interactive mode
			if !nonInteractive {
				// Detect module name from go.mod if possible
				detectedModule := detectModule(absDir)
				if detectedModule != "" {
					cfg.Project.Module = detectedModule
				}
				
				// Use name from flag or directory name
				if name == "" {
					name = filepath.Base(absDir)
				}
				cfg.Project.Name = name
				
				// Use driver from flag or default
				if driver != "" {
					cfg.Database.Driver = driver
				}
				
				// Use module from flag if provided
				if module != "" {
					cfg.Project.Module = module
				}
				
				// Interactive form
				form := huh.NewForm(
					huh.NewGroup(
						huh.NewInput().
							Title("Project Name").
							Description("The name of your project").
							Value(&cfg.Project.Name).
							Placeholder(name),
						
						huh.NewInput().
							Title("Go Module").
							Description("The Go module path (from go.mod)").
							Value(&cfg.Project.Module).
							Placeholder(cfg.Project.Module),
					).Title("Project Configuration"),
					
					huh.NewGroup(
						huh.NewSelect[string]().
							Title("Database Driver").
							Description("Select the database driver to use").
							Options(
								huh.NewOption("PostgreSQL (recommended for production)", "postgres"),
								huh.NewOption("In-Memory (for testing only)", "memory"),
							).
							Value(&cfg.Database.Driver),
					).Title("Database Configuration"),
					
					huh.NewGroup(
						huh.NewInput().
							Title("Aggregates Package").
							Description("Package path for aggregates").
							Value(&cfg.Generation.AggregatePackage),
						
						huh.NewInput().
							Title("Events Package").
							Description("Package path for events").
							Value(&cfg.Generation.EventPackage),
						
						huh.NewInput().
							Title("Projections Package").
							Description("Package path for projections").
							Value(&cfg.Generation.ProjectionPackage),
					).Title("Code Generation"),
				).WithTheme(huh.ThemeDracula())
				
				if err := form.Run(); err != nil {
					return err
				}
			} else {
				// Non-interactive mode: use flags
				if name != "" {
					cfg.Project.Name = name
				}
				if module != "" {
					cfg.Project.Module = module
				}
				if driver != "" {
					cfg.Database.Driver = driver
				}
			}
			
			// Create directories
			dirs := []string{
				cfg.Database.MigrationsDir,
				cfg.Generation.AggregatePackage,
				cfg.Generation.EventPackage,
				cfg.Generation.ProjectionPackage,
				cfg.Generation.CommandPackage,
			}
			
			fmt.Println()
			fmt.Println(styles.Title.Render(styles.IconFolder + " Creating project structure..."))
			fmt.Println()
			
			for _, d := range dirs {
				dirPath := filepath.Join(absDir, d)
				if err := os.MkdirAll(dirPath, 0755); err != nil {
					fmt.Println(styles.FormatError(fmt.Sprintf("Failed to create %s: %v", d, err)))
				} else {
					fmt.Println(styles.FormatSuccess(fmt.Sprintf("Created %s", d)))
				}
			}
			
			// Create config file
			fmt.Println()
			configContent := config.GenerateYAML(cfg)
			configPath := filepath.Join(absDir, config.ConfigFileName)
			if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
				return fmt.Errorf("failed to create config file: %w", err)
			}
			fmt.Println(styles.FormatSuccess("Created mink.yaml"))
			
			// Create .gitkeep files for empty directories
			for _, d := range dirs {
				gitkeepPath := filepath.Join(absDir, d, ".gitkeep")
				_ = os.WriteFile(gitkeepPath, []byte(""), 0644)
			}
			
			// Print next steps
			fmt.Println()
			fmt.Println(styles.InfoBox.Render(nextSteps(cfg)))
			
			return nil
		},
	}
	
	cmd.Flags().StringVarP(&name, "name", "n", "", "Project name")
	cmd.Flags().StringVarP(&module, "module", "m", "", "Go module path")
	cmd.Flags().StringVarP(&driver, "driver", "d", "", "Database driver (postgres, memory)")
	cmd.Flags().BoolVar(&nonInteractive, "non-interactive", false, "Run in non-interactive mode")
	
	return cmd
}

// detectModule tries to detect the Go module from go.mod
func detectModule(dir string) string {
	gomodPath := filepath.Join(dir, "go.mod")
	data, err := os.ReadFile(gomodPath)
	if err != nil {
		return ""
	}
	
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			return strings.TrimPrefix(line, "module ")
		}
	}
	
	return ""
}

func nextSteps(cfg *config.Config) string {
	steps := []string{
		styles.Bold.Render("Next Steps:"),
		"",
	}
	
	stepNum := 1
	
	if cfg.Database.Driver == "postgres" {
		steps = append(steps,
			fmt.Sprintf("%d. Set your database URL:", stepNum),
			"   "+styles.Code.Render("export DATABASE_URL=\"postgres://user:pass@localhost:5432/db\""),
			"",
		)
		stepNum++
		
		steps = append(steps,
			fmt.Sprintf("%d. The event store schema will be created automatically", stepNum),
			"   when you first use the PostgreSQL adapter.",
			"",
		)
		stepNum++
	}
	
	steps = append(steps,
		fmt.Sprintf("%d. Generate your first aggregate:", stepNum),
		"   "+styles.Code.Render("mink generate aggregate Order"),
		"",
		"Happy event sourcing! "+styles.IconMink,
	)
	
	return strings.Join(steps, "\n")
}

// Splash shows a quick loading animation
func Splash() {
	style := lipgloss.NewStyle().
		Bold(true).
		Foreground(styles.Primary)
	
	fmt.Println(style.Render("\n" + styles.IconMink + " mink\n"))
}
