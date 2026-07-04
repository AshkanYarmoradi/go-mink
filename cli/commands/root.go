// Package commands provides the CLI command implementations for mink.
package commands

import (
	"fmt"

	"github.com/spf13/cobra"
	"go-mink.dev/cli/styles"
	"go-mink.dev/cli/ui"
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
	rootCmd.AddCommand(NewEventsCommand())
	rootCmd.AddCommand(NewDiagnoseCommand())
	rootCmd.AddCommand(NewSchemaCommand())
	rootCmd.AddCommand(NewGdprCommand())
	rootCmd.AddCommand(NewVersionCommand(Version, Commit, BuildDate))

	return rootCmd
}

// requireSubcommand configures a parent command (one that only groups
// subcommands and has no action of its own) so that a missing or unknown
// subcommand results in a non-zero exit instead of silently printing help.
//
// Without this, cobra's default behavior for a parent command with no RunE is
// to print help and return nil for both the bare invocation (`mink stream`)
// and an unknown subcommand (`mink stream bogus`), which is a scripting hazard.
//
// `--help`/`-h` is still handled by cobra before Args validation and RunE run,
// so `mink stream --help` continues to show help and exit 0.
func requireSubcommand(cmd *cobra.Command) *cobra.Command {
	// Reject any positional argument: an unknown subcommand is parsed as an
	// arg here, so NoArgs makes cobra emit "unknown command" and exit non-zero.
	cmd.Args = cobra.NoArgs
	// Handle the bare-parent (no args) case: print help to stderr and return an
	// error so the process exits non-zero. cobra's Help() writes to the command's
	// out writer (stdout by default), so redirect it to stderr first to keep this
	// non-zero-exit path from polluting stdout for scripts.
	cmd.RunE = func(c *cobra.Command, args []string) error {
		c.SetOut(c.ErrOrStderr())
		_ = c.Help()
		return fmt.Errorf("%q requires a subcommand; see '%s --help'", c.CommandPath(), c.CommandPath())
	}
	return cmd
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
