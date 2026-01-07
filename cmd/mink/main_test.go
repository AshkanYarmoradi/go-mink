package main

import (
	"testing"

	"github.com/AshkanYarmoradi/go-mink/cli/commands"
	"github.com/stretchr/testify/assert"
)

// TestVersionVariables tests that version variables are properly declared.
func TestVersionVariables(t *testing.T) {
	// These are the default values before ldflags override
	assert.Equal(t, "dev", version)
	assert.Equal(t, "none", commit)
	assert.Equal(t, "unknown", buildDate)
}

// TestVersionAssignment tests that version info can be assigned to commands package.
func TestVersionAssignment(t *testing.T) {
	// Store original values
	origVersion := commands.Version
	origCommit := commands.Commit
	origBuildDate := commands.BuildDate

	// Test assignment (as done in main)
	commands.Version = version
	commands.Commit = commit
	commands.BuildDate = buildDate

	assert.Equal(t, "dev", commands.Version)
	assert.Equal(t, "none", commands.Commit)
	assert.Equal(t, "unknown", commands.BuildDate)

	// Restore original values
	commands.Version = origVersion
	commands.Commit = origCommit
	commands.BuildDate = origBuildDate
}
