// mink is the command-line interface for the go-mink event sourcing library.
//
// Usage:
//
//	mink <command> [flags]
//
// Commands:
//
//	init        Initialize a new mink project
//	generate    Generate code scaffolding (aggregates, events, projections)
//	migrate     Manage database migrations
//	projection  Manage event projections
//	stream      Inspect and manage event streams
//	schema      Generate and manage database schema
//	diagnose    Run diagnostic checks on your setup
//	version     Show version information
//
// Examples:
//
//	# Initialize a new project
//	mink init my-project
//
//	# Generate an aggregate with events
//	mink generate aggregate Order --events Created,ItemAdded,Shipped
//
//	# Run database migrations
//	mink migrate up
//
//	# Check projection status
//	mink projection list
//
//	# Run diagnostics
//	mink diagnose
package main

import (
	"os"

	"github.com/AshkanYarmoradi/go-mink/cli/commands"

	// Register PostgreSQL driver
	_ "github.com/jackc/pgx/v5/stdlib"
)

// Build information (set via ldflags)
var (
	version   = "dev"
	commit    = "none"
	buildDate = "unknown"
)

func main() {
	// Set version info
	commands.Version = version
	commands.Commit = commit
	commands.BuildDate = buildDate

	if err := commands.Execute(); err != nil {
		os.Exit(1)
	}
}
