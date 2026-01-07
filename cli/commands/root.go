// Package commands provides the CLI command implementations for mink.
package commands

import (
	"fmt"

	"github.com/AshkanYarmoradi/go-mink/cli/styles"
	"github.com/AshkanYarmoradi/go-mink/cli/ui"
	"github.com/spf13/cobra"
)

var (
	// Version information (set at build time)
	Version   = "dev"
	Commit    = "none"
	BuildDate = "unknown"
)

// NewRootCommand creates the root command for the mink CLI
func NewRootCommand() *cobra.Command {
	var noColor bool

	rootCmd := &cobra.Command{
		Use:   "mink",
		Short: "Event Sourcing toolkit for Go",
		Long: ui.SimpleBanner() + `

Mink is an Event Sourcing and CQRS toolkit for Go applications.
It provides a complete solution for building event-driven systems.

` + styles.Title.Render("Quick Start:") + `

  ` + styles.Code.Render("mink init") + `           Initialize a new project
  ` + styles.Code.Render("mink generate") + `       Generate code scaffolding
  ` + styles.Code.Render("mink migrate up") + `     Run database migrations
  ` + styles.Code.Render("mink diagnose") + `       Check your setup

` + styles.Title.Render("Documentation:") + `

  https://github.com/AshkanYarmoradi/go-mink`,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			if noColor {
				styles.DisableColors()
			}
		},
	}

	// Global flags
	rootCmd.PersistentFlags().BoolVar(&noColor, "no-color", false, "Disable colored output")

	// Add subcommands
	rootCmd.AddCommand(NewInitCommand())
	rootCmd.AddCommand(NewGenerateCommand())
	rootCmd.AddCommand(NewMigrateCommand())
	rootCmd.AddCommand(NewProjectionCommand())
	rootCmd.AddCommand(NewStreamCommand())
	rootCmd.AddCommand(NewDiagnoseCommand())
	rootCmd.AddCommand(NewSchemaCommand())
	rootCmd.AddCommand(NewVersionCommand(Version, Commit, BuildDate))

	return rootCmd
}

// Execute runs the root command
func Execute() error {
	rootCmd := NewRootCommand()

	if err := rootCmd.Execute(); err != nil {
		fmt.Println(styles.FormatError(err.Error()))
		return err
	}

	return nil
}
