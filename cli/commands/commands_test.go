package commands

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewRootCommand(t *testing.T) {
	cmd := NewRootCommand()
	
	assert.Equal(t, "mink", cmd.Use)
	assert.NotEmpty(t, cmd.Short)
	assert.NotEmpty(t, cmd.Long)
	
	// Check subcommands are registered
	subcommands := cmd.Commands()
	assert.NotEmpty(t, subcommands)
	
	// Check for expected subcommands
	foundInit := false
	foundGenerate := false
	foundMigrate := false
	foundProjection := false
	foundStream := false
	foundDiagnose := false
	foundSchema := false
	foundVersion := false
	
	for _, sub := range subcommands {
		switch sub.Name() {
		case "init":
			foundInit = true
		case "generate":
			foundGenerate = true
		case "migrate":
			foundMigrate = true
		case "projection":
			foundProjection = true
		case "stream":
			foundStream = true
		case "diagnose":
			foundDiagnose = true
		case "schema":
			foundSchema = true
		case "version":
			foundVersion = true
		}
	}
	
	assert.True(t, foundInit, "init command should be registered")
	assert.True(t, foundGenerate, "generate command should be registered")
	assert.True(t, foundMigrate, "migrate command should be registered")
	assert.True(t, foundProjection, "projection command should be registered")
	assert.True(t, foundStream, "stream command should be registered")
	assert.True(t, foundDiagnose, "diagnose command should be registered")
	assert.True(t, foundSchema, "schema command should be registered")
	assert.True(t, foundVersion, "version command should be registered")
}

func TestNewInitCommand(t *testing.T) {
	cmd := NewInitCommand()
	
	assert.Equal(t, "init [directory]", cmd.Use)
	assert.NotEmpty(t, cmd.Short)
	assert.NotEmpty(t, cmd.Long)
	
	// Check flags
	f := cmd.Flags()
	assert.NotNil(t, f.Lookup("name"))
	assert.NotNil(t, f.Lookup("module"))
	assert.NotNil(t, f.Lookup("driver"))
	assert.NotNil(t, f.Lookup("non-interactive"))
}

func TestNewGenerateCommand(t *testing.T) {
	cmd := NewGenerateCommand()
	
	assert.Equal(t, "generate", cmd.Use)
	assert.NotEmpty(t, cmd.Short)
	assert.Contains(t, cmd.Aliases, "gen")
	assert.Contains(t, cmd.Aliases, "g")
	
	// Check subcommands
	subcommands := cmd.Commands()
	foundAggregate := false
	foundEvent := false
	foundProjection := false
	foundCommand := false
	
	for _, sub := range subcommands {
		switch sub.Name() {
		case "aggregate":
			foundAggregate = true
		case "event":
			foundEvent = true
		case "projection":
			foundProjection = true
		case "command":
			foundCommand = true
		}
	}
	
	assert.True(t, foundAggregate)
	assert.True(t, foundEvent)
	assert.True(t, foundProjection)
	assert.True(t, foundCommand)
}

func TestNewMigrateCommand(t *testing.T) {
	cmd := NewMigrateCommand()
	
	assert.Equal(t, "migrate", cmd.Use)
	assert.NotEmpty(t, cmd.Short)
	
	// Check subcommands
	subcommands := cmd.Commands()
	foundUp := false
	foundDown := false
	foundStatus := false
	foundCreate := false
	
	for _, sub := range subcommands {
		switch sub.Name() {
		case "up":
			foundUp = true
		case "down":
			foundDown = true
		case "status":
			foundStatus = true
		case "create":
			foundCreate = true
		}
	}
	
	assert.True(t, foundUp)
	assert.True(t, foundDown)
	assert.True(t, foundStatus)
	assert.True(t, foundCreate)
}

func TestNewProjectionCommand(t *testing.T) {
	cmd := NewProjectionCommand()
	
	assert.Equal(t, "projection", cmd.Use)
	assert.NotEmpty(t, cmd.Short)
	assert.Contains(t, cmd.Aliases, "proj")
	
	// Check subcommands
	subcommands := cmd.Commands()
	foundList := false
	foundStatus := false
	foundRebuild := false
	foundPause := false
	foundResume := false
	
	for _, sub := range subcommands {
		switch sub.Name() {
		case "list":
			foundList = true
		case "status":
			foundStatus = true
		case "rebuild":
			foundRebuild = true
		case "pause":
			foundPause = true
		case "resume":
			foundResume = true
		}
	}
	
	assert.True(t, foundList)
	assert.True(t, foundStatus)
	assert.True(t, foundRebuild)
	assert.True(t, foundPause)
	assert.True(t, foundResume)
}

func TestNewStreamCommand(t *testing.T) {
	cmd := NewStreamCommand()
	
	assert.Equal(t, "stream", cmd.Use)
	assert.NotEmpty(t, cmd.Short)
	
	// Check subcommands
	subcommands := cmd.Commands()
	foundList := false
	foundEvents := false
	foundExport := false
	foundStats := false
	
	for _, sub := range subcommands {
		switch sub.Name() {
		case "list":
			foundList = true
		case "events":
			foundEvents = true
		case "export":
			foundExport = true
		case "stats":
			foundStats = true
		}
	}
	
	assert.True(t, foundList)
	assert.True(t, foundEvents)
	assert.True(t, foundExport)
	assert.True(t, foundStats)
}

func TestNewDiagnoseCommand(t *testing.T) {
	cmd := NewDiagnoseCommand()
	
	assert.Equal(t, "diagnose", cmd.Use)
	assert.NotEmpty(t, cmd.Short)
	assert.Contains(t, cmd.Aliases, "diag")
	assert.Contains(t, cmd.Aliases, "doctor")
}

func TestNewSchemaCommand(t *testing.T) {
	cmd := NewSchemaCommand()
	
	assert.Equal(t, "schema", cmd.Use)
	assert.NotEmpty(t, cmd.Short)
	
	// Check subcommands
	subcommands := cmd.Commands()
	foundGenerate := false
	foundPrint := false
	
	for _, sub := range subcommands {
		switch sub.Name() {
		case "generate":
			foundGenerate = true
		case "print":
			foundPrint = true
		}
	}
	
	assert.True(t, foundGenerate)
	assert.True(t, foundPrint)
}

func TestNewVersionCommand(t *testing.T) {
	cmd := NewVersionCommand("1.0.0", "abc123", "2024-01-01")
	
	assert.Equal(t, "version", cmd.Use)
	assert.NotEmpty(t, cmd.Short)
}

func TestToPascalCase(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"order", "Order"},
		{"order_created", "OrderCreated"},
		{"order-shipped", "OrderShipped"},
		{"order item added", "OrderItemAdded"},
		{"", ""},
		{"OrderCreated", "OrderCreated"},
	}
	
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := toPascalCase(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestSanitizeName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"create users", "create_users"},
		{"add-column", "add_column"},
		{"MixedCase", "mixedcase"},
		{"already_valid", "already_valid"},
	}
	
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := sanitizeName(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}
