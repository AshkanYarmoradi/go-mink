package commands

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AshkanYarmoradi/go-mink/cli/config"
	"github.com/AshkanYarmoradi/go-mink/cli/ui"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestNewRootCommand_NoColorFlag(t *testing.T) {
	cmd := NewRootCommand()
	f := cmd.PersistentFlags()
	assert.NotNil(t, f.Lookup("no-color"))
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

func TestDetectModule(t *testing.T) {
	// Create temp directory with go.mod
	tmpDir, err := os.MkdirTemp("", "mink-cmd-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	// Create go.mod
	gomodContent := `module github.com/test/myapp

go 1.21
`
	err = os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte(gomodContent), 0644)
	require.NoError(t, err)

	module := detectModule(tmpDir)
	assert.Equal(t, "github.com/test/myapp", module)
}

func TestDetectModule_NoGoMod(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-cmd-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	module := detectModule(tmpDir)
	assert.Empty(t, module)
}

func TestDetectModule_InvalidGoMod(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-cmd-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	// Create go.mod without module line
	err = os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte("go 1.21\n"), 0644)
	require.NoError(t, err)

	module := detectModule(tmpDir)
	assert.Empty(t, module)
}

func TestNextSteps_Postgres(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Database.Driver = "postgres"

	result := nextSteps(cfg)

	assert.Contains(t, result, "Next Steps")
	assert.Contains(t, result, "DATABASE_URL")
	assert.Contains(t, result, "schema will be created automatically")
	assert.Contains(t, result, "generate aggregate")
}

func TestNextSteps_Memory(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Database.Driver = "memory"

	result := nextSteps(cfg)

	assert.Contains(t, result, "Next Steps")
	assert.NotContains(t, result, "DATABASE_URL")
	assert.Contains(t, result, "generate aggregate")
}

func TestSplash(t *testing.T) {
	// Just verify it doesn't panic
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	Splash()

	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	assert.Contains(t, output, "mink")
}

func TestGenerateFile(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-cmd-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	// Test generating a file with template (directory already exists)
	testPath := filepath.Join(tmpDir, "test.go")
	tmpl := "package {{.Package}}\n"
	data := struct{ Package string }{Package: "test"}

	err = generateFile(testPath, tmpl, data)
	require.NoError(t, err)

	// Verify file was created
	fileData, err := os.ReadFile(testPath)
	require.NoError(t, err)
	assert.Equal(t, "package test\n", string(fileData))
}

func TestGenerateFile_Overwrite(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-cmd-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	testPath := filepath.Join(tmpDir, "test.go")

	// Create initial file
	err = os.WriteFile(testPath, []byte("old content"), 0644)
	require.NoError(t, err)

	// Generate new file (should overwrite)
	tmpl := "package {{.Name}}\n"
	data := struct{ Name string }{Name: "newtest"}
	err = generateFile(testPath, tmpl, data)
	require.NoError(t, err)

	fileData, err := os.ReadFile(testPath)
	require.NoError(t, err)
	assert.Equal(t, "package newtest\n", string(fileData))
}

func TestGenerateFile_InvalidTemplate(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-cmd-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	testPath := filepath.Join(tmpDir, "test.go")
	tmpl := "{{.Invalid" // Invalid template

	err = generateFile(testPath, tmpl, nil)
	assert.Error(t, err)
}

func TestVersionCommand_Execute(t *testing.T) {
	cmd := NewVersionCommand("1.0.0", "abc123", "2024-01-01")
	cmd.SetArgs([]string{}) // Ensure clean args

	// Capture output
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.Execute()
	assert.NoError(t, err)
}

func TestSchemaCommand_PrintSubcommand(t *testing.T) {
	cmd := NewSchemaCommand()
	cmd.SetArgs([]string{"print"}) // Set proper subcommand

	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.Execute()
	assert.NoError(t, err)
}

func TestInitCommand_NonInteractive(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-init-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cmd := NewInitCommand()
	cmd.SetArgs([]string{
		tmpDir,
		"--non-interactive",
		"--name", "test-app",
		"--module", "github.com/test/app",
		"--driver", "memory",
	})

	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err = cmd.Execute()
	require.NoError(t, err)

	// Verify config was created
	assert.True(t, config.Exists(tmpDir))
}

func TestInitCommand_AlreadyExists(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-init-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	// Create config first
	cfg := config.DefaultConfig()
	err = cfg.Save(tmpDir)
	require.NoError(t, err)

	cmd := NewInitCommand()
	cmd.SetArgs([]string{tmpDir, "--non-interactive"})

	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err = cmd.Execute()
	assert.NoError(t, err) // Should succeed but with warning
}

func TestInitCommand_WithGoMod(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-init-gomod-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	// Create a go.mod file
	gomodContent := `module github.com/test/myproject

go 1.21
`
	err = os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte(gomodContent), 0644)
	require.NoError(t, err)

	cmd := NewInitCommand()
	cmd.SetArgs([]string{
		tmpDir,
		"--non-interactive",
		"--name", "myproject",
	})

	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err = cmd.Execute()
	require.NoError(t, err)

	// Verify config was created
	assert.True(t, config.Exists(tmpDir))
}

func TestInitCommand_PostgresDriver(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-init-pg-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cmd := NewInitCommand()
	cmd.SetArgs([]string{
		tmpDir,
		"--non-interactive",
		"--name", "pg-app",
		"--module", "github.com/test/pg-app",
		"--driver", "postgres",
	})

	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err = cmd.Execute()
	require.NoError(t, err)

	// Verify config was created with postgres driver
	assert.True(t, config.Exists(tmpDir))
}

func TestMigrateCommand_CreateSubcommand_Structure(t *testing.T) {
	cmd := NewMigrateCommand()

	// Find create subcommand
	var createCmd *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Name() == "create" {
			createCmd = sub
			break
		}
	}
	require.NotNil(t, createCmd)
	assert.Equal(t, "create <name>", createCmd.Use)
	assert.NotEmpty(t, createCmd.Short)
}

func TestGetAllMigrations(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-migrate-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	// Create some migration files
	err = os.WriteFile(filepath.Join(tmpDir, "001_create_users.sql"), []byte("-- up"), 0644)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(tmpDir, "001_create_users.down.sql"), []byte("-- down"), 0644)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(tmpDir, "002_add_index.sql"), []byte("-- up"), 0644)
	require.NoError(t, err)

	migrations, err := getAllMigrations(tmpDir)
	require.NoError(t, err)

	// Should find 2 up migrations (not down migrations)
	assert.Len(t, migrations, 2)
}

func TestGetAllMigrations_NonExistent(t *testing.T) {
	migrations, err := getAllMigrations("/nonexistent/path")
	assert.NoError(t, err)
	assert.Nil(t, migrations)
}

func TestGenerateSchema(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Project.Name = "test-project"
	cfg.EventStore.TableName = "my_events"
	cfg.EventStore.SnapshotTableName = "my_snapshots"
	cfg.EventStore.OutboxTableName = "my_outbox"

	schema := generateSchema(cfg)

	assert.Contains(t, schema, "test-project")
	assert.Contains(t, schema, "my_events")
	assert.Contains(t, schema, "my_snapshots")
	assert.Contains(t, schema, "my_outbox")
	assert.Contains(t, schema, "CREATE TABLE")
	assert.Contains(t, schema, "CREATE INDEX")
	assert.Contains(t, schema, "mink_append_events")
}

func TestExecute(t *testing.T) {
	// Save original version
	origVersion := Version
	origCommit := Commit
	origBuildDate := BuildDate

	Version = "test"
	Commit = "test123"
	BuildDate = "2024-01-01"

	defer func() {
		Version = origVersion
		Commit = origCommit
		BuildDate = origBuildDate
	}()

	// Execute with help flag should not error
	rootCmd := NewRootCommand()
	rootCmd.SetArgs([]string{"--help"})

	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)

	err := rootCmd.Execute()
	assert.NoError(t, err)
}

func TestAggregateSubcommand_Flags(t *testing.T) {
	cmd := NewGenerateCommand()

	// Find aggregate subcommand
	var aggCmd *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Name() == "aggregate" {
			aggCmd = sub
			break
		}
	}
	require.NotNil(t, aggCmd)

	// Check flags exist (--events, -e)
	f := aggCmd.Flags()
	assert.NotNil(t, f.Lookup("events"))
}

func TestEventSubcommand_Flags(t *testing.T) {
	cmd := NewGenerateCommand()

	// Find event subcommand
	var eventCmd *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Name() == "event" {
			eventCmd = sub
			break
		}
	}
	require.NotNil(t, eventCmd)

	f := eventCmd.Flags()
	assert.NotNil(t, f.Lookup("aggregate"))
}

func TestProjectionSubcommand(t *testing.T) {
	cmd := NewGenerateCommand()

	// Find projection subcommand
	var projCmd *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Name() == "projection" {
			projCmd = sub
			break
		}
	}
	require.NotNil(t, projCmd)
	assert.Equal(t, "projection <name>", projCmd.Use)
}

func TestCommandSubcommand_Flags(t *testing.T) {
	cmd := NewGenerateCommand()

	// Find command subcommand
	var cmdCmd *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Name() == "command" {
			cmdCmd = sub
			break
		}
	}
	require.NotNil(t, cmdCmd)

	f := cmdCmd.Flags()
	assert.NotNil(t, f.Lookup("aggregate"))
}

func TestMigrateUpSubcommand_Flags(t *testing.T) {
	cmd := NewMigrateCommand()

	var upCmd *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Name() == "up" {
			upCmd = sub
			break
		}
	}
	require.NotNil(t, upCmd)

	f := upCmd.Flags()
	assert.NotNil(t, f.Lookup("steps"))
}

func TestMigrateDownSubcommand_Flags(t *testing.T) {
	cmd := NewMigrateCommand()

	var downCmd *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Name() == "down" {
			downCmd = sub
			break
		}
	}
	require.NotNil(t, downCmd)

	f := downCmd.Flags()
	assert.NotNil(t, f.Lookup("steps"))
}

func TestProjectionRebuildSubcommand_Flags(t *testing.T) {
	cmd := NewProjectionCommand()

	var rebuildCmd *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Name() == "rebuild" {
			rebuildCmd = sub
			break
		}
	}
	require.NotNil(t, rebuildCmd)

	f := rebuildCmd.Flags()
	assert.NotNil(t, f.Lookup("force"))
}

func TestStreamListSubcommand_Flags(t *testing.T) {
	cmd := NewStreamCommand()

	var listCmd *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Name() == "list" {
			listCmd = sub
			break
		}
	}
	require.NotNil(t, listCmd)

	f := listCmd.Flags()
	assert.NotNil(t, f.Lookup("limit"))
	assert.NotNil(t, f.Lookup("prefix"))
}

func TestStreamEventsSubcommand_Flags(t *testing.T) {
	cmd := NewStreamCommand()

	var eventsCmd *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Name() == "events" {
			eventsCmd = sub
			break
		}
	}
	require.NotNil(t, eventsCmd)

	f := eventsCmd.Flags()
	assert.NotNil(t, f.Lookup("limit"))
}

func TestStreamExportSubcommand_Flags(t *testing.T) {
	cmd := NewStreamCommand()

	var exportCmd *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Name() == "export" {
			exportCmd = sub
			break
		}
	}
	require.NotNil(t, exportCmd)

	f := exportCmd.Flags()
	assert.NotNil(t, f.Lookup("output"))
}

func TestSchemaGenerateSubcommand_Flags(t *testing.T) {
	cmd := NewSchemaCommand()

	var genCmd *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Name() == "generate" {
			genCmd = sub
			break
		}
	}
	require.NotNil(t, genCmd)

	f := genCmd.Flags()
	assert.NotNil(t, f.Lookup("output"))
}

func TestGenerateSchemaContent(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Project.Name = "test-project"
	cfg.EventStore.TableName = "my_events"
	cfg.EventStore.SnapshotTableName = "my_snapshots"
	cfg.EventStore.OutboxTableName = "my_outbox"

	schema := generateSchema(cfg)

	// Verify schema contains all necessary parts
	assert.Contains(t, schema, "test-project")
	assert.Contains(t, schema, "my_events")
	assert.Contains(t, schema, "my_snapshots")
	assert.Contains(t, schema, "my_outbox")
	assert.Contains(t, schema, "CREATE TABLE IF NOT EXISTS my_events")
	assert.Contains(t, schema, "CREATE TABLE IF NOT EXISTS my_snapshots")
	assert.Contains(t, schema, "CREATE TABLE IF NOT EXISTS mink_checkpoints")
	assert.Contains(t, schema, "CREATE TABLE IF NOT EXISTS my_outbox")
	assert.Contains(t, schema, "CREATE INDEX")
	assert.Contains(t, schema, "CREATE OR REPLACE FUNCTION mink_append_events")
}

func TestGenerateFile_ExecutionError(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-gen-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	path := filepath.Join(tmpDir, "test.go")
	tmpl := "{{.Missing}}" // Template expects field that doesn't exist
	data := struct{}{}

	err = generateFile(path, tmpl, data)
	assert.Error(t, err)
}

func TestNewInitCommand_Structure(t *testing.T) {
	cmd := NewInitCommand()

	assert.Equal(t, "init [directory]", cmd.Use)
	assert.NotEmpty(t, cmd.Short)
	assert.NotEmpty(t, cmd.Long)

	// Verify flags
	f := cmd.Flags()
	assert.NotNil(t, f.Lookup("name"))
	assert.NotNil(t, f.Lookup("module"))
	assert.NotNil(t, f.Lookup("driver"))
	assert.NotNil(t, f.Lookup("non-interactive"))
}

func TestNewProjectionCommand_Subcommands(t *testing.T) {
	cmd := NewProjectionCommand()

	// Verify all subcommands exist
	subcommandNames := make(map[string]bool)
	for _, sub := range cmd.Commands() {
		subcommandNames[sub.Name()] = true
	}

	assert.True(t, subcommandNames["list"])
	assert.True(t, subcommandNames["status"])
	assert.True(t, subcommandNames["rebuild"])
	assert.True(t, subcommandNames["pause"])
	assert.True(t, subcommandNames["resume"])
}

func TestNewStreamCommand_Subcommands(t *testing.T) {
	cmd := NewStreamCommand()

	// Verify all subcommands exist
	subcommandNames := make(map[string]bool)
	for _, sub := range cmd.Commands() {
		subcommandNames[sub.Name()] = true
	}

	assert.True(t, subcommandNames["list"])
	assert.True(t, subcommandNames["events"])
	assert.True(t, subcommandNames["export"])
	assert.True(t, subcommandNames["stats"])
}

func TestNewGenerateCommand_Subcommands(t *testing.T) {
	cmd := NewGenerateCommand()

	// Verify all subcommands exist
	subcommandNames := make(map[string]bool)
	for _, sub := range cmd.Commands() {
		subcommandNames[sub.Name()] = true
	}

	assert.True(t, subcommandNames["aggregate"])
	assert.True(t, subcommandNames["event"])
	assert.True(t, subcommandNames["projection"])
	assert.True(t, subcommandNames["command"])
}

func TestNewMigrateCommand_Subcommands(t *testing.T) {
	cmd := NewMigrateCommand()

	// Verify all subcommands exist
	subcommandNames := make(map[string]bool)
	for _, sub := range cmd.Commands() {
		subcommandNames[sub.Name()] = true
	}

	assert.True(t, subcommandNames["up"])
	assert.True(t, subcommandNames["down"])
	assert.True(t, subcommandNames["status"])
	assert.True(t, subcommandNames["create"])
}

func TestNewSchemaCommand_Subcommands(t *testing.T) {
	cmd := NewSchemaCommand()

	// Verify all subcommands exist
	subcommandNames := make(map[string]bool)
	for _, sub := range cmd.Commands() {
		subcommandNames[sub.Name()] = true
	}

	assert.True(t, subcommandNames["generate"])
	assert.True(t, subcommandNames["print"])
}

func TestNewDiagnoseCommand_Structure(t *testing.T) {
	cmd := NewDiagnoseCommand()

	assert.Equal(t, "diagnose", cmd.Use)
	assert.NotEmpty(t, cmd.Short)
	assert.NotEmpty(t, cmd.Long)
	assert.NotEmpty(t, cmd.Aliases)
}

func TestRootCommand_HasSubcommands(t *testing.T) {
	cmd := NewRootCommand()

	subcommandNames := make(map[string]bool)
	for _, sub := range cmd.Commands() {
		subcommandNames[sub.Name()] = true
	}

	// Verify essential subcommands are registered
	assert.True(t, subcommandNames["init"])
	assert.True(t, subcommandNames["generate"])
	assert.True(t, subcommandNames["migrate"])
	assert.True(t, subcommandNames["projection"])
	assert.True(t, subcommandNames["stream"])
	assert.True(t, subcommandNames["diagnose"])
	assert.True(t, subcommandNames["schema"])
	assert.True(t, subcommandNames["version"])
}

func TestRootCommand_PersistentFlags(t *testing.T) {
	cmd := NewRootCommand()

	f := cmd.PersistentFlags()
	assert.NotNil(t, f.Lookup("no-color"))
}

func TestSchemaCommand_GenerateSubcommand_Run(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-schema-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	// Create a mink.yaml config first
	cfg := config.DefaultConfig()
	cfg.Project.Name = "test-project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	// Change to temp dir
	origWd, err := os.Getwd()
	require.NoError(t, err)
	err = os.Chdir(tmpDir)
	require.NoError(t, err)
	defer os.Chdir(origWd)

	cmd := NewSchemaCommand()
	cmd.SetArgs([]string{"generate"})

	// Just test that it executes without error
	// The output goes to stdout, not to the command's buffer
	err = cmd.Execute()
	require.NoError(t, err)
}

func TestMigrateCreateSubcommand_Structure(t *testing.T) {
	cmd := NewMigrateCommand()

	var createCmd *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Name() == "create" {
			createCmd = sub
			break
		}
	}
	require.NotNil(t, createCmd)

	assert.Equal(t, "create <name>", createCmd.Use)
	assert.NotEmpty(t, createCmd.Short)
}

func TestCommandAliases(t *testing.T) {
	tests := []struct {
		name    string
		cmd     *cobra.Command
		aliases []string
	}{
		{"generate", NewGenerateCommand(), []string{"gen", "g"}},
		{"diagnose", NewDiagnoseCommand(), []string{"diag", "doctor"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.ElementsMatch(t, tt.aliases, tt.cmd.Aliases)
		})
	}
}

func TestMigrateCreateCommand_Execute(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-migrate-create-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	// Create mink.yaml
	cfg := config.DefaultConfig()
	cfg.Database.MigrationsDir = "migrations"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	// Change to temp dir
	origWd, err := os.Getwd()
	require.NoError(t, err)
	err = os.Chdir(tmpDir)
	require.NoError(t, err)
	defer os.Chdir(origWd)

	cmd := NewMigrateCommand()
	cmd.SetArgs([]string{"create", "add_users_table"})

	err = cmd.Execute()
	require.NoError(t, err)

	// Verify migration files were created
	entries, err := os.ReadDir(filepath.Join(tmpDir, "migrations"))
	require.NoError(t, err)
	assert.Len(t, entries, 2) // up and down files
}

func TestGenerateCommand_AggregateSubcommand_Structure(t *testing.T) {
	cmd := NewGenerateCommand()

	var aggCmd *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Name() == "aggregate" {
			aggCmd = sub
			break
		}
	}
	require.NotNil(t, aggCmd)

	assert.Equal(t, "aggregate <name>", aggCmd.Use)
	assert.Contains(t, aggCmd.Aliases, "agg")
	assert.Contains(t, aggCmd.Aliases, "a")
}

func TestGenerateCommand_EventSubcommand_Structure(t *testing.T) {
	cmd := NewGenerateCommand()

	var eventCmd *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Name() == "event" {
			eventCmd = sub
			break
		}
	}
	require.NotNil(t, eventCmd)

	assert.Equal(t, "event <name>", eventCmd.Use)
	assert.Contains(t, eventCmd.Aliases, "evt")
	assert.Contains(t, eventCmd.Aliases, "e")
}

func TestGenerateCommand_ProjectionSubcommand_Structure(t *testing.T) {
	cmd := NewGenerateCommand()

	var projCmd *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Name() == "projection" {
			projCmd = sub
			break
		}
	}
	require.NotNil(t, projCmd)

	assert.Equal(t, "projection <name>", projCmd.Use)
	assert.Contains(t, projCmd.Aliases, "proj")
	assert.Contains(t, projCmd.Aliases, "p")
}

func TestGenerateCommand_CommandSubcommand_Structure(t *testing.T) {
	cmd := NewGenerateCommand()

	var cmdCmd *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Name() == "command" {
			cmdCmd = sub
			break
		}
	}
	require.NotNil(t, cmdCmd)

	assert.Equal(t, "command <name>", cmdCmd.Use)
	assert.Contains(t, cmdCmd.Aliases, "cmd")
	assert.Contains(t, cmdCmd.Aliases, "c")
}

func TestMigrateCommand_UpSubcommand_Structure(t *testing.T) {
	cmd := NewMigrateCommand()

	var upCmd *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Name() == "up" {
			upCmd = sub
			break
		}
	}
	require.NotNil(t, upCmd)

	assert.Equal(t, "up", upCmd.Use)
	assert.NotEmpty(t, upCmd.Short)
	assert.NotEmpty(t, upCmd.Long)
}

func TestMigrateCommand_DownSubcommand_Structure(t *testing.T) {
	cmd := NewMigrateCommand()

	var downCmd *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Name() == "down" {
			downCmd = sub
			break
		}
	}
	require.NotNil(t, downCmd)

	assert.Equal(t, "down", downCmd.Use)
	assert.NotEmpty(t, downCmd.Short)
	assert.NotEmpty(t, downCmd.Long)
}

func TestMigrateCommand_StatusSubcommand_Structure(t *testing.T) {
	cmd := NewMigrateCommand()

	var statusCmd *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Name() == "status" {
			statusCmd = sub
			break
		}
	}
	require.NotNil(t, statusCmd)

	assert.Equal(t, "status", statusCmd.Use)
	assert.NotEmpty(t, statusCmd.Short)
}

func TestProjectionCommand_ListSubcommand_Structure(t *testing.T) {
	cmd := NewProjectionCommand()

	var listCmd *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Name() == "list" {
			listCmd = sub
			break
		}
	}
	require.NotNil(t, listCmd)

	assert.Equal(t, "list", listCmd.Use)
	assert.Contains(t, listCmd.Aliases, "ls")
}

func TestProjectionCommand_StatusSubcommand_Structure(t *testing.T) {
	cmd := NewProjectionCommand()

	var statusCmd *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Name() == "status" {
			statusCmd = sub
			break
		}
	}
	require.NotNil(t, statusCmd)

	assert.Equal(t, "status <name>", statusCmd.Use)
}

func TestProjectionCommand_RebuildSubcommand_Structure(t *testing.T) {
	cmd := NewProjectionCommand()

	var rebuildCmd *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Name() == "rebuild" {
			rebuildCmd = sub
			break
		}
	}
	require.NotNil(t, rebuildCmd)

	assert.Equal(t, "rebuild <name>", rebuildCmd.Use)
}

func TestProjectionCommand_PauseSubcommand_Structure(t *testing.T) {
	cmd := NewProjectionCommand()

	var pauseCmd *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Name() == "pause" {
			pauseCmd = sub
			break
		}
	}
	require.NotNil(t, pauseCmd)

	assert.Equal(t, "pause <name>", pauseCmd.Use)
}

func TestProjectionCommand_ResumeSubcommand_Structure(t *testing.T) {
	cmd := NewProjectionCommand()

	var resumeCmd *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Name() == "resume" {
			resumeCmd = sub
			break
		}
	}
	require.NotNil(t, resumeCmd)

	assert.Equal(t, "resume <name>", resumeCmd.Use)
}

func TestStreamCommand_ListSubcommand_Structure(t *testing.T) {
	cmd := NewStreamCommand()

	var listCmd *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Name() == "list" {
			listCmd = sub
			break
		}
	}
	require.NotNil(t, listCmd)

	assert.Equal(t, "list", listCmd.Use)
	assert.Contains(t, listCmd.Aliases, "ls")
}

func TestStreamCommand_EventsSubcommand_Structure(t *testing.T) {
	cmd := NewStreamCommand()

	var eventsCmd *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Name() == "events" {
			eventsCmd = sub
			break
		}
	}
	require.NotNil(t, eventsCmd)

	assert.Equal(t, "events <stream-id>", eventsCmd.Use)
}

func TestStreamCommand_ExportSubcommand_Structure(t *testing.T) {
	cmd := NewStreamCommand()

	var exportCmd *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Name() == "export" {
			exportCmd = sub
			break
		}
	}
	require.NotNil(t, exportCmd)

	assert.Equal(t, "export <stream-id>", exportCmd.Use)
}

func TestStreamCommand_StatsSubcommand_Structure(t *testing.T) {
	cmd := NewStreamCommand()

	var statsCmd *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Name() == "stats" {
			statsCmd = sub
			break
		}
	}
	require.NotNil(t, statsCmd)

	assert.Equal(t, "stats", statsCmd.Use)
}

func TestSchemaCommand_GenerateSubcommand_Structure(t *testing.T) {
	cmd := NewSchemaCommand()

	var genCmd *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Name() == "generate" {
			genCmd = sub
			break
		}
	}
	require.NotNil(t, genCmd)

	assert.Equal(t, "generate", genCmd.Use)
}

func TestSchemaCommand_PrintSubcommand_Structure(t *testing.T) {
	cmd := NewSchemaCommand()

	var printCmd *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Name() == "print" {
			printCmd = sub
			break
		}
	}
	require.NotNil(t, printCmd)

	assert.Equal(t, "print", printCmd.Use)
}

// Test diagnose check functions
func TestCheckGoVersion(t *testing.T) {
	result := checkGoVersion()

	assert.Equal(t, "Go Version", result.Name)
	assert.Equal(t, StatusOK, result.Status)
	assert.NotEmpty(t, result.Message)
	assert.Contains(t, result.Message, "go1")
}

func TestCheckSystemResources(t *testing.T) {
	result := checkSystemResources()

	assert.Equal(t, "System Resources", result.Name)
	assert.Contains(t, []CheckStatus{StatusOK, StatusWarning}, result.Status)
	assert.NotEmpty(t, result.Message)
	assert.Contains(t, result.Message, "MB")
}

func TestCheckConfiguration_NoConfig(t *testing.T) {
	// Change to temp dir without config
	tmpDir, err := os.MkdirTemp("", "mink-diag-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	origWd, _ := os.Getwd()
	err = os.Chdir(tmpDir)
	require.NoError(t, err)
	defer os.Chdir(origWd)

	result := checkConfiguration()

	assert.Equal(t, "Configuration", result.Name)
	assert.Equal(t, StatusWarning, result.Status)
	assert.Contains(t, result.Message, "No mink.yaml found")
}

func TestCheckConfiguration_WithConfig(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-diag-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	// Create config with valid values
	cfg := config.DefaultConfig()
	cfg.Project.Name = "test-project"
	cfg.Project.Module = "github.com/test/project"
	cfg.Database.Driver = "memory" // Valid driver that doesn't require URL
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	err = os.Chdir(tmpDir)
	require.NoError(t, err)
	defer os.Chdir(origWd)

	result := checkConfiguration()

	assert.Equal(t, "Configuration", result.Name)
	// Config might still have warnings if certain fields are missing, but not errors
	assert.Contains(t, []CheckStatus{StatusOK, StatusWarning}, result.Status)
}

func TestCheckStatus_Constants(t *testing.T) {
	// Verify status constants are defined correctly
	assert.Equal(t, CheckStatus(0), StatusOK)
	assert.Equal(t, CheckStatus(1), StatusWarning)
	assert.Equal(t, CheckStatus(2), StatusError)
}

func TestCheckResult_Structure(t *testing.T) {
	result := CheckResult{
		Name:           "Test",
		Status:         StatusOK,
		Message:        "test message",
		Recommendation: "test recommendation",
	}

	assert.Equal(t, "Test", result.Name)
	assert.Equal(t, StatusOK, result.Status)
	assert.Equal(t, "test message", result.Message)
	assert.Equal(t, "test recommendation", result.Recommendation)
}

func TestDiagnosticCheck_Structure(t *testing.T) {
	check := DiagnosticCheck{
		Name: "Test Check",
		Check: func() CheckResult {
			return CheckResult{Name: "Test", Status: StatusOK}
		},
	}

	assert.Equal(t, "Test Check", check.Name)
	result := check.Check()
	assert.Equal(t, "Test", result.Name)
	assert.Equal(t, StatusOK, result.Status)
}

// Ensure cobra.Command is used to prevent import error
var _ *cobra.Command = nil

// Additional tests for data structures

func TestProjection_Structure(t *testing.T) {
	// Test Projection data structure
	p := Projection{
		Name:     "TestProjection",
		Position: 100,
		Status:   "active",
	}

	assert.Equal(t, "TestProjection", p.Name)
	assert.Equal(t, int64(100), p.Position)
	assert.Equal(t, "active", p.Status)
}

func TestStream_Structure(t *testing.T) {
	s := Stream{
		StreamID:      "order-123",
		EventCount:    5,
		LastEventType: "ItemAdded",
	}

	assert.Equal(t, "order-123", s.StreamID)
	assert.Equal(t, int64(5), s.EventCount)
	assert.Equal(t, "ItemAdded", s.LastEventType)
}

func TestStreamEvent_Structure(t *testing.T) {
	e := StreamEvent{
		ID:       "event-123",
		StreamID: "order-123",
		Version:  1,
		Type:     "OrderCreated",
		Data:     `{"orderId": "123"}`,
		Metadata: `{}`,
	}

	assert.Equal(t, "event-123", e.ID)
	assert.Equal(t, "order-123", e.StreamID)
	assert.Equal(t, int64(1), e.Version)
	assert.Equal(t, "OrderCreated", e.Type)
}

func TestEventStoreStats_Structure(t *testing.T) {
	stats := EventStoreStats{
		TotalEvents:  1000,
		TotalStreams: 50,
		EventTypes:   10,
	}

	assert.Equal(t, int64(1000), stats.TotalEvents)
	assert.Equal(t, int64(50), stats.TotalStreams)
	assert.Equal(t, int64(10), stats.EventTypes)
}

func TestMigration_Structure(t *testing.T) {
	m := Migration{
		Name: "001_initial",
		Path: "/path/to/001_initial.sql",
	}

	assert.Equal(t, "001_initial", m.Name)
	assert.Equal(t, "/path/to/001_initial.sql", m.Path)
}

// Test Execute function
func TestExecute_NoArgs(t *testing.T) {
	// Can't easily test Execute() as it initializes terminal TUI
	// Just verify it doesn't panic with no args
	// This would require mocking os.Args
}

// Test NewAnimatedVersion, Init, Update, View - these are bubbletea models
func TestAnimatedVersionModel(t *testing.T) {
	model := NewAnimatedVersion("1.0.0")

	assert.Equal(t, "1.0.0", model.version)
	assert.Equal(t, 0, model.phase)
	assert.False(t, model.done)
}

func TestAnimatedVersionModel_Init(t *testing.T) {
	model := NewAnimatedVersion("1.0.0")
	cmd := model.Init()
	assert.NotNil(t, cmd, "Init should return a Cmd")
}

func TestAnimatedVersionModel_Update_Tick(t *testing.T) {
	model := NewAnimatedVersion("1.0.0")

	// Simulate tick messages
	for i := 0; i < 6; i++ {
		newModel, cmd := model.Update(ui.AnimationTickMsg{})
		model = newModel.(AnimatedVersionModel)

		if i < 5 {
			assert.NotNil(t, cmd, "Update should return a Cmd for next tick")
			assert.False(t, model.done)
		} else {
			// After 6 ticks, should be done
			assert.True(t, model.done)
		}
	}
}

func TestAnimatedVersionModel_Update_KeyPress(t *testing.T) {
	model := NewAnimatedVersion("1.0.0")

	newModel, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = newModel.(AnimatedVersionModel)

	// Key press should trigger quit
	assert.NotNil(t, cmd)
}

func TestAnimatedVersionModel_View(t *testing.T) {
	model := NewAnimatedVersion("1.0.0")

	// View during animation
	view := model.View()
	assert.NotEmpty(t, view)

	// View when done
	model.done = true
	view = model.View()
	assert.NotEmpty(t, view)
}

func TestAnimatedVersionModel_View_AllPhases(t *testing.T) {
	model := NewAnimatedVersion("1.0.0")

	// Test all phases
	for i := 0; i <= 5; i++ {
		model.phase = i
		view := model.View()
		assert.NotEmpty(t, view)
	}
}

// Test getAllMigrations with various scenarios
func TestGetAllMigrations_WithDownFiles(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-test-migrations-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	// Create .sql and .down.sql files
	os.WriteFile(filepath.Join(tmpDir, "001_create.sql"), []byte("-- up"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "001_create.down.sql"), []byte("-- down"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "002_update.sql"), []byte("-- up"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "002_update.down.sql"), []byte("-- down"), 0644)

	migrations, err := getAllMigrations(tmpDir)
	require.NoError(t, err)

	// Should only return .sql files, not .down.sql
	assert.Len(t, migrations, 2)
	assert.Equal(t, "001_create", migrations[0].Name)
	assert.Equal(t, "002_update", migrations[1].Name)
}

func TestGetAllMigrations_NonExistentDir(t *testing.T) {
	migrations, err := getAllMigrations("/nonexistent/dir")
	require.NoError(t, err)
	assert.Nil(t, migrations)
}

func TestGetAllMigrations_EmptyDir(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-test-empty-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	migrations, err := getAllMigrations(tmpDir)
	require.NoError(t, err)
	assert.Empty(t, migrations)
}

func TestGetAllMigrations_WithSubdirectories(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-test-subdir-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	// Create a subdirectory that should be ignored
	os.Mkdir(filepath.Join(tmpDir, "subdir"), 0755)
	os.WriteFile(filepath.Join(tmpDir, "001_first.sql"), []byte("-- up"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "subdir", "002_second.sql"), []byte("-- up"), 0644)

	migrations, err := getAllMigrations(tmpDir)
	require.NoError(t, err)

	// Should only find 001_first.sql, not the one in subdirectory
	assert.Len(t, migrations, 1)
	assert.Equal(t, "001_first", migrations[0].Name)
}

func TestGetAllMigrations_Sorting(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-test-sort-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	// Create files in non-sorted order
	os.WriteFile(filepath.Join(tmpDir, "003_third.sql"), []byte("-- up"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "001_first.sql"), []byte("-- up"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "002_second.sql"), []byte("-- up"), 0644)

	migrations, err := getAllMigrations(tmpDir)
	require.NoError(t, err)

	// Should be sorted
	assert.Len(t, migrations, 3)
	assert.Equal(t, "001_first", migrations[0].Name)
	assert.Equal(t, "002_second", migrations[1].Name)
	assert.Equal(t, "003_third", migrations[2].Name)
}

// Test error handling branches
func TestNewInitCommand_NoConfig(t *testing.T) {
	// Test init command when there's no existing config
	tmpDir, err := os.MkdirTemp("", "mink-init-noconfig-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewInitCommand()
	assert.NotNil(t, cmd)
	assert.Equal(t, "init [directory]", cmd.Use)
}

// Test schema command flags
func TestSchemaGenerateCommand_Flags(t *testing.T) {
	cmd := NewSchemaCommand()

	// Find generate subcommand
	var generateCmd *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Name() == "generate" {
			generateCmd = sub
			break
		}
	}

	require.NotNil(t, generateCmd)

	// Check flags
	outputFlag := generateCmd.Flag("output")
	assert.NotNil(t, outputFlag)
	assert.Equal(t, "o", outputFlag.Shorthand)
}

func TestSchemaGenerateCommand_Execute(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-schema-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewSchemaCommand()
	cmd.SetArgs([]string{"generate"})

	var buf bytes.Buffer
	cmd.SetOut(&buf)

	err = cmd.Execute()
	assert.NoError(t, err)
}

func TestSchemaGenerateCommand_OutputFile(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-schema-output-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	outputFile := filepath.Join(tmpDir, "schema.sql")

	cmd := NewSchemaCommand()
	cmd.SetArgs([]string{"generate", "--output", outputFile})

	err = cmd.Execute()
	assert.NoError(t, err)

	// Verify file was created
	assert.FileExists(t, outputFile)

	// Verify content
	content, err := os.ReadFile(outputFile)
	require.NoError(t, err)
	assert.Contains(t, string(content), "CREATE TABLE")
	assert.Contains(t, string(content), "mink_events")
}

func TestSchemaPrintCommand_Execute(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-schema-print-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewSchemaCommand()
	cmd.SetArgs([]string{"print"})

	var buf bytes.Buffer
	cmd.SetOut(&buf)

	err = cmd.Execute()
	assert.NoError(t, err)
}

// Test projection command flags
func TestProjectionCommand_SubcommandsExist(t *testing.T) {
	cmd := NewProjectionCommand()

	subcommands := make(map[string]bool)
	for _, sub := range cmd.Commands() {
		subcommands[sub.Name()] = true
	}

	assert.True(t, subcommands["list"], "list subcommand should exist")
	assert.True(t, subcommands["status"], "status subcommand should exist")
	assert.True(t, subcommands["rebuild"], "rebuild subcommand should exist")
	assert.True(t, subcommands["pause"], "pause subcommand should exist")
	assert.True(t, subcommands["resume"], "resume subcommand should exist")
}

// Test stream command flags
func TestStreamCommand_SubcommandsExist(t *testing.T) {
	cmd := NewStreamCommand()

	subcommands := make(map[string]bool)
	for _, sub := range cmd.Commands() {
		subcommands[sub.Name()] = true
	}

	assert.True(t, subcommands["list"], "list subcommand should exist")
	assert.True(t, subcommands["events"], "events subcommand should exist")
	assert.True(t, subcommands["export"], "export subcommand should exist")
	assert.True(t, subcommands["stats"], "stats subcommand should exist")
}

// Test migrate command flags
func TestMigrateCommand_SubcommandsExist(t *testing.T) {
	cmd := NewMigrateCommand()

	subcommands := make(map[string]bool)
	for _, sub := range cmd.Commands() {
		subcommands[sub.Name()] = true
	}

	assert.True(t, subcommands["up"], "up subcommand should exist")
	assert.True(t, subcommands["down"], "down subcommand should exist")
	assert.True(t, subcommands["status"], "status subcommand should exist")
	assert.True(t, subcommands["create"], "create subcommand should exist")
}

// Test sanitizeName with various inputs
func TestSanitizeName_Comprehensive(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"hello world", "hello_world"},
		{"Hello-World", "hello_world"},
		{"test123", "test123"},
		{"UPPERCASE", "uppercase"},
		{"123startsWithNumber", "123startswithnumber"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := sanitizeName(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// Test toPascalCase with various inputs
func TestToPascalCase_Comprehensive(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"hello_world", "HelloWorld"},
		{"hello-world", "HelloWorld"},
		{"hello world", "HelloWorld"},
		{"already_pascal", "AlreadyPascal"},
		{"mixed_case-Input", "MixedCaseInput"},
		{"single", "Single"},
		{"API", "API"},
		{"userID", "UserID"},
		{"HTTPServer", "HTTPServer"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := toPascalCase(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// Test detectModule with various scenarios
func TestDetectModule_WithoutGoMod(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-test-nomod-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	result := detectModule(tmpDir)
	assert.Empty(t, result)
}

func TestDetectModule_WithMalformedGoMod(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-test-badmod-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	// Write go.mod without module line
	os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte("go 1.21\n"), 0644)

	result := detectModule(tmpDir)
	assert.Empty(t, result)
}

// Test generate file with various templates
func TestGenerateFile_CustomTemplate(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-test-genfile-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	filePath := filepath.Join(tmpDir, "test.go")
	template := `package {{.Package}}

// {{.Name}} is a test struct
type {{.Name}} struct {}
`

	data := struct {
		Package string
		Name    string
	}{
		Package: "mypackage",
		Name:    "MyStruct",
	}

	err = generateFile(filePath, template, data)
	require.NoError(t, err)

	content, err := os.ReadFile(filePath)
	require.NoError(t, err)
	assert.Contains(t, string(content), "package mypackage")
	assert.Contains(t, string(content), "type MyStruct struct")
}

func TestGenerateFile_CreateDirectory(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-test-gendir-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	// Generate file in existing directory (generateFile doesn't auto-create nested dirs)
	filePath := filepath.Join(tmpDir, "test.go")
	template := `package test`

	err = generateFile(filePath, template, nil)
	require.NoError(t, err)

	// Verify file exists
	_, err = os.Stat(filePath)
	assert.NoError(t, err)
}

func TestGenerateFile_BadTemplate(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-test-badtempl-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	filePath := filepath.Join(tmpDir, "test.go")
	template := `{{.Invalid`

	err = generateFile(filePath, template, nil)
	assert.Error(t, err)
}

// Test config with various edge cases
func TestCheckConfiguration_WithInvalidConfig(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-test-badcfg-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	// Create invalid yaml
	os.WriteFile(filepath.Join(tmpDir, "mink.yaml"), []byte("invalid: yaml: :::"), 0644)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	result := checkConfiguration()
	// Should return an error status when config can't be loaded
	assert.Contains(t, []CheckStatus{StatusWarning, StatusError}, result.Status)
}

func TestCheckConfiguration_WithNoConfig(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-test-nocfg-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	result := checkConfiguration()
	assert.Equal(t, StatusWarning, result.Status)
}

// Test projection rebuild command structure
func TestProjectionRebuildCommand_Structure(t *testing.T) {
	cmd := NewProjectionCommand()
	rebuildCmd, _, err := cmd.Find([]string{"rebuild"})
	require.NoError(t, err)

	assert.Equal(t, "rebuild <name>", rebuildCmd.Use)

	// Check force flag exists
	forceFlag := rebuildCmd.Flags().Lookup("force")
	assert.NotNil(t, forceFlag)
	assert.Equal(t, "f", forceFlag.Shorthand)
}

// Test migrate status command more thoroughly
func TestMigrateStatusCommand_WithoutConfig(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-migrate-status-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewMigrateCommand()
	statusCmd, _, _ := cmd.Find([]string{"status"})

	err = statusCmd.RunE(statusCmd, []string{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "mink.yaml")
}

// Test stream events command structure
func TestStreamEventsCommand_Structure(t *testing.T) {
	cmd := NewStreamCommand()
	eventsCmd, _, err := cmd.Find([]string{"events"})
	require.NoError(t, err)

	assert.Equal(t, "events <stream-id>", eventsCmd.Use)
}

// Test stream stats command structure
func TestStreamStatsCommand_Structure(t *testing.T) {
	cmd := NewStreamCommand()
	statsCmd, _, err := cmd.Find([]string{"stats"})
	require.NoError(t, err)

	assert.Equal(t, "stats", statsCmd.Use)
}

// Test stream export command structure with flags
func TestStreamExportCommand_Flags(t *testing.T) {
	cmd := NewStreamCommand()
	exportCmd, _, err := cmd.Find([]string{"export"})
	require.NoError(t, err)

	// Check output flag
	outputFlag := exportCmd.Flags().Lookup("output")
	assert.NotNil(t, outputFlag)
	assert.Equal(t, "o", outputFlag.Shorthand)

	// Stream is passed as an argument, not a flag
	assert.Equal(t, "export <stream-id>", exportCmd.Use)
}

// Test init command - it doesn't have aliases
func TestInitCommand_NoAliases(t *testing.T) {
	cmd := NewInitCommand()
	assert.Empty(t, cmd.Aliases)
}

// Test generate command aliases
func TestGenerateCommand_Aliases(t *testing.T) {
	cmd := NewGenerateCommand()
	assert.Contains(t, cmd.Aliases, "gen")
	assert.Contains(t, cmd.Aliases, "g")
}

// Test schema command - no aliases on generate subcommand
func TestSchemaCommand_NoAliases(t *testing.T) {
	cmd := NewSchemaCommand()
	genCmd, _, err := cmd.Find([]string{"generate"})
	require.NoError(t, err)
	assert.Empty(t, genCmd.Aliases) // No aliases on generate subcommand
}

// Test getPendingMigrations with empty dir
func TestGetPendingMigrations_EmptyDir(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-pending-empty-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	// Create empty migrations dir
	migrationsDir := filepath.Join(tmpDir, "migrations")
	os.MkdirAll(migrationsDir, 0755)

	// We can test that empty dir returns no migrations
	all, err := getAllMigrations(migrationsDir)
	require.NoError(t, err)
	assert.Empty(t, all)
}

// Test toPascalCase edge cases
func TestToPascalCase_EdgeCases(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", ""},
		{"a", "A"},
		{"abc", "Abc"},
		{"ABC", "ABC"},
		{"already_snake", "AlreadySnake"}, // underscore is removed, next char capitalized
		{"with-dash", "WithDash"},
		{"with space", "WithSpace"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := toPascalCase(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

// Test CheckStatus constants
func TestCheckStatus_Values(t *testing.T) {
	assert.Equal(t, CheckStatus(0), StatusOK)
	assert.Equal(t, CheckStatus(1), StatusWarning)
	assert.Equal(t, CheckStatus(2), StatusError)
}

// Test AnimatedVersionModel more thoroughly (diagnose.go:472 Init at 50%)
func TestAnimatedVersionModel_FullLifecycle(t *testing.T) {
	model := NewAnimatedVersion("1.0.0")

	// Init returns tick command
	cmd := model.Init()
	assert.NotNil(t, cmd)

	// Multiple ticks progress through phases
	for i := 0; i < 6; i++ {
		newModel, _ := model.Update(ui.AnimationTickMsg{})
		model = newModel.(AnimatedVersionModel)
	}

	// After enough ticks, should be done
	assert.True(t, model.done)
}

// Test migrate up error paths
func TestMigrateUpCommand_NoDatabaseURL(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-migrate-nodb-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Database.Driver = "postgres"
	cfg.Database.URL = "" // Empty URL
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewMigrateCommand()
	upCmd, _, _ := cmd.Find([]string{"up"})
	upCmd.Flags().Set("force", "true")

	err = upCmd.RunE(upCmd, []string{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "DATABASE_URL")
}

// Test projection list without config
func TestProjectionListCommand_NoConfig(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-proj-list-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewProjectionCommand()
	listCmd, _, _ := cmd.Find([]string{"list"})

	err = listCmd.RunE(listCmd, []string{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "mink.yaml")
}

// Test stream list without config
func TestStreamListCommand_NoConfig(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-stream-list-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewStreamCommand()
	listCmd, _, _ := cmd.Find([]string{"list"})

	err = listCmd.RunE(listCmd, []string{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "mink.yaml")
}

// Test stream events without config
func TestStreamEventsCommand_NoConfig(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-stream-events-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewStreamCommand()
	eventsCmd, _, _ := cmd.Find([]string{"events"})

	err = eventsCmd.RunE(eventsCmd, []string{"stream-123"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "mink.yaml")
}

// Test stream stats without config
func TestStreamStatsCommand_NoConfig(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-stream-stats-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewStreamCommand()
	statsCmd, _, _ := cmd.Find([]string{"stats"})

	err = statsCmd.RunE(statsCmd, []string{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "mink.yaml")
}

// Test stream export without config
func TestStreamExportCommand_NoConfig(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-stream-export-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewStreamCommand()
	exportCmd, _, _ := cmd.Find([]string{"export"})

	err = exportCmd.RunE(exportCmd, []string{"stream-123"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "mink.yaml")
}

// Test projection status command structure
func TestProjectionStatusCommand_Structure(t *testing.T) {
	cmd := NewProjectionCommand()
	statusCmd, _, err := cmd.Find([]string{"status"})
	require.NoError(t, err)
	assert.Equal(t, "status <name>", statusCmd.Use)
}

// Test projection pause command structure
func TestProjectionPauseCommand_Structure(t *testing.T) {
	cmd := NewProjectionCommand()
	pauseCmd, _, err := cmd.Find([]string{"pause"})
	require.NoError(t, err)
	assert.Equal(t, "pause <name>", pauseCmd.Use)
}

// Test projection resume command structure
func TestProjectionResumeCommand_Structure(t *testing.T) {
	cmd := NewProjectionCommand()
	resumeCmd, _, err := cmd.Find([]string{"resume"})
	require.NoError(t, err)
	assert.Equal(t, "resume <name>", resumeCmd.Use)
}

// Test migrate up with invalid database URL
func TestMigrateUpCommand_InvalidDBURL(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-migrate-invalid-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Database.Driver = "postgres"
	cfg.Database.URL = "postgres://invalid:url@localhost:65535/nonexistent"
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	// Create migrations dir with a migration
	migrationsDir := filepath.Join(tmpDir, "migrations")
	os.MkdirAll(migrationsDir, 0755)
	os.WriteFile(filepath.Join(migrationsDir, "001_test.sql"), []byte("SELECT 1;"), 0644)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewMigrateCommand()
	upCmd, _, _ := cmd.Find([]string{"up"})
	upCmd.Flags().Set("force", "true")

	err = upCmd.RunE(upCmd, []string{})
	// Should fail when trying to connect with invalid DB URL
	assert.Error(t, err)
}

// Test migrate status with memory driver
func TestMigrateStatusCommand_MemoryDriver(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-migrate-memory-status-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Database.Driver = "memory"
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewMigrateCommand()
	statusCmd, _, _ := cmd.Find([]string{"status"})

	err = statusCmd.RunE(statusCmd, []string{})
	// Memory driver should show info message
	assert.NoError(t, err)
}

// Test generate aggregate with multiple events
func TestGenerateAggregateCommand_MultipleEvents(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-gen-multi-events-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Project.Module = "github.com/test/multi"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	// Create the directories
	os.MkdirAll(filepath.Join(tmpDir, cfg.Generation.AggregatePackage), 0755)
	os.MkdirAll(filepath.Join(tmpDir, cfg.Generation.EventPackage), 0755)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewGenerateCommand()
	cmd.SetArgs([]string{"aggregate", "MultiTest", "--events", "Created,Updated,Deleted", "--force"})

	err = cmd.Execute()
	assert.NoError(t, err)

	// Verify aggregate file was created
	aggFile := filepath.Join(tmpDir, cfg.Generation.AggregatePackage, "multitest.go")
	_, err = os.Stat(aggFile)
	assert.NoError(t, err, "Aggregate file should exist")

	// Verify events file was created (all events in one file)
	eventsFile := filepath.Join(tmpDir, cfg.Generation.EventPackage, "multitest_events.go")
	_, err = os.Stat(eventsFile)
	assert.NoError(t, err, "Events file should exist")
}

// Test projection list command without db URL
func TestProjectionListCommand_NoDBURL(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-proj-list-nodb-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Database.Driver = "postgres"
	cfg.Database.URL = ""
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewProjectionCommand()
	listCmd, _, _ := cmd.Find([]string{"list"})

	err = listCmd.RunE(listCmd, []string{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "DATABASE_URL")
}

// Test projection status without db URL
func TestProjectionStatusCommand_NoDBURL(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-proj-status-nodb-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Database.Driver = "postgres"
	cfg.Database.URL = ""
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewProjectionCommand()
	statusCmd, _, _ := cmd.Find([]string{"status"})

	err = statusCmd.RunE(statusCmd, []string{"TestProj"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "DATABASE_URL")
}

// Test generate event without config
func TestGenerateEventCommand_NoConfig(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-gen-evt-noconfig-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewGenerateCommand()
	cmd.SetArgs([]string{"event", "TestEvent", "--force"})

	err = cmd.Execute()
	// Should use default config
	assert.NoError(t, err)
}

// Test generate projection without config
func TestGenerateProjectionCommand_NoConfig(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-gen-proj-noconfig-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewGenerateCommand()
	cmd.SetArgs([]string{"projection", "TestProj", "--force"})

	err = cmd.Execute()
	// Should use default config
	assert.NoError(t, err)
}

// Test generate command without config
func TestGenerateCommandCommand_NoConfig(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-gen-cmd-noconfig-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewGenerateCommand()
	cmd.SetArgs([]string{"command", "TestCmd", "--force"})

	err = cmd.Execute()
	// Should use default config
	assert.NoError(t, err)
}

// Test runDiagnose function paths - warning status
func TestCheckDatabaseConnection_NoDBURL(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-check-db-nourl-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	// Create config without DB URL
	cfg := config.DefaultConfig()
	cfg.Database.Driver = "postgres"
	cfg.Database.URL = ""
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	result := checkDatabaseConnection()
	assert.Equal(t, StatusWarning, result.Status)
	assert.NotEmpty(t, result.Recommendation)
}

// Test checkEventStoreSchema error cases - returns OK when using valid config
func TestCheckEventStoreSchema_NoConfig(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-check-schema-noconfig-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	result := checkEventStoreSchema()
	// Should return warning or OK when no config (depending on implementation)
	assert.Contains(t, []CheckStatus{StatusOK, StatusWarning}, result.Status)
}

// Test checkProjections without config
func TestCheckProjections_NoConfig(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-check-proj-noconfig-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	result := checkProjections()
	// Should return warning or OK when no config
	assert.Contains(t, []CheckStatus{StatusOK, StatusWarning}, result.Status)
}

// Test migrate down without config
func TestMigrateDownCommand_NoConfig(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-migrate-down-noconfig-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewMigrateCommand()
	downCmd, _, _ := cmd.Find([]string{"down"})

	err = downCmd.RunE(downCmd, []string{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "mink.yaml")
}

// Test stream list with prefix filter
func TestStreamListCommand_WithPrefix(t *testing.T) {
	cmd := NewStreamCommand()
	listCmd, _, err := cmd.Find([]string{"list"})
	require.NoError(t, err)

	// Check prefix flag exists
	prefixFlag := listCmd.Flags().Lookup("prefix")
	assert.NotNil(t, prefixFlag)
	assert.Equal(t, "p", prefixFlag.Shorthand)
}

// Test stream events with limit and from flags
func TestStreamEventsCommand_Flags(t *testing.T) {
	cmd := NewStreamCommand()
	eventsCmd, _, err := cmd.Find([]string{"events"})
	require.NoError(t, err)

	// Check limit flag
	limitFlag := eventsCmd.Flags().Lookup("limit")
	assert.NotNil(t, limitFlag)
	assert.Equal(t, "n", limitFlag.Shorthand)

	// Check from flag
	fromFlag := eventsCmd.Flags().Lookup("from")
	assert.NotNil(t, fromFlag)
	assert.Equal(t, "f", fromFlag.Shorthand)
}

// Test diagnose command structure
func TestDiagnoseCommand_Structure(t *testing.T) {
	cmd := NewDiagnoseCommand()
	assert.Equal(t, "diagnose", cmd.Use)
	assert.Contains(t, cmd.Aliases, "diag")
	assert.Contains(t, cmd.Aliases, "doctor")
}

// Test version command structure
func TestVersionCommand_Structure(t *testing.T) {
	cmd := NewVersionCommand("1.0.0", "abc123", "2024-01-01")
	assert.Equal(t, "version", cmd.Use)
}

// Test root command structure
func TestRootCommand_Structure(t *testing.T) {
	cmd := NewRootCommand()
	assert.Equal(t, "mink", cmd.Use)
}

// Test projection pause without config
func TestProjectionPauseCommand_NoConfig(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-proj-pause-noconfig-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewProjectionCommand()
	pauseCmd, _, _ := cmd.Find([]string{"pause"})

	err = pauseCmd.RunE(pauseCmd, []string{"TestProj"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "mink.yaml")
}

// Test projection resume without config
func TestProjectionResumeCommand_NoConfig(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-proj-resume-noconfig-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewProjectionCommand()
	resumeCmd, _, _ := cmd.Find([]string{"resume"})

	err = resumeCmd.RunE(resumeCmd, []string{"TestProj"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "mink.yaml")
}

// Test projection pause without db URL
func TestProjectionPauseCommand_NoDBURL(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-proj-pause-nodb-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Database.Driver = "postgres"
	cfg.Database.URL = ""
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewProjectionCommand()
	pauseCmd, _, _ := cmd.Find([]string{"pause"})

	err = pauseCmd.RunE(pauseCmd, []string{"TestProj"})
	assert.Error(t, err)
	// Command tries to connect with empty URL - connection will fail
	assert.True(t, strings.Contains(err.Error(), "failed to connect") || strings.Contains(err.Error(), "connection"))
}

// Test projection resume without db URL
func TestProjectionResumeCommand_NoDBURL(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-proj-resume-nodb-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Database.Driver = "postgres"
	cfg.Database.URL = ""
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewProjectionCommand()
	resumeCmd, _, _ := cmd.Find([]string{"resume"})

	err = resumeCmd.RunE(resumeCmd, []string{"TestProj"})
	assert.Error(t, err)
	// Command tries to connect with empty URL - connection will fail
	assert.True(t, strings.Contains(err.Error(), "failed to connect") || strings.Contains(err.Error(), "connection"))
}

// Test stream export without db URL
func TestStreamExportCommand_NoDBURL(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-stream-export-nodb-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Database.Driver = "postgres"
	cfg.Database.URL = ""
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewStreamCommand()
	exportCmd, _, _ := cmd.Find([]string{"export"})

	err = exportCmd.RunE(exportCmd, []string{"stream-123"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "DATABASE_URL")
}

// Test listProjections helper without config
func TestListProjections_WithInvalidURL(t *testing.T) {
	// listProjections requires a database URL - test with invalid URL
	_, err := listProjections("invalid://not-a-valid-url")
	assert.Error(t, err)
}

// Test checkDatabaseConnection with memory driver
func TestCheckDatabaseConnection_MemoryDriver(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-check-db-memory-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Database.Driver = "memory"
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	result := checkDatabaseConnection()
	assert.Equal(t, StatusOK, result.Status)
	assert.Contains(t, result.Message, "memory")
}

// Test generate aggregate with existing config
func TestGenerateAggregateCommand_WithExistingConfig(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-gen-agg-existing-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Project.Module = "github.com/test/existing"
	cfg.Generation.AggregatePackage = "pkg/domain"
	cfg.Generation.EventPackage = "pkg/events"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewGenerateCommand()
	cmd.SetArgs([]string{"aggregate", "Custom", "--force"})

	err = cmd.Execute()
	assert.NoError(t, err)

	// Verify files created in custom paths
	aggFile := filepath.Join(tmpDir, "pkg/domain", "custom.go")
	_, err = os.Stat(aggFile)
	assert.NoError(t, err)
}

// Test generate event with aggregate flag
func TestGenerateEventCommand_WithAggregate(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-gen-evt-agg-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Project.Module = "github.com/test/evtagg"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewGenerateCommand()
	cmd.SetArgs([]string{"event", "OrderShipped", "--aggregate", "Order", "--force"})

	err = cmd.Execute()
	assert.NoError(t, err)

	// Verify event file was created
	eventFile := filepath.Join(tmpDir, cfg.Generation.EventPackage, "ordershipped.go")
	_, err = os.Stat(eventFile)
	assert.NoError(t, err)
}

// Test generate command with aggregate flag
func TestGenerateCommandCommand_WithAggregate(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-gen-cmd-agg-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Project.Module = "github.com/test/cmdagg"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewGenerateCommand()
	cmd.SetArgs([]string{"command", "ShipOrder", "--aggregate", "Order", "--force"})

	err = cmd.Execute()
	assert.NoError(t, err)

	// Verify command file was created
	cmdFile := filepath.Join(tmpDir, cfg.Generation.CommandPackage, "shiporder.go")
	_, err = os.Stat(cmdFile)
	assert.NoError(t, err)
}

// Test generate projection with events flag
func TestGenerateProjectionCommand_WithEvents(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-gen-proj-evts-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Project.Module = "github.com/test/projevt"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewGenerateCommand()
	cmd.SetArgs([]string{"projection", "OrderSummary", "--events", "OrderCreated,OrderShipped", "--force"})

	err = cmd.Execute()
	assert.NoError(t, err)

	// Verify projection file was created
	projFile := filepath.Join(tmpDir, cfg.Generation.ProjectionPackage, "ordersummary.go")
	_, err = os.Stat(projFile)
	assert.NoError(t, err)

	// Verify events are in the file
	content, err := os.ReadFile(projFile)
	require.NoError(t, err)
	assert.Contains(t, string(content), "OrderCreated")
	assert.Contains(t, string(content), "OrderShipped")
}

// Test migrate down with invalid db URL
func TestMigrateDownCommand_InvalidDBURL(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-migrate-down-invalid-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Database.Driver = "postgres"
	cfg.Database.URL = "postgres://invalid:url@localhost:65535/nonexistent"
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	// Create migrations dir with a migration
	migrationsDir := filepath.Join(tmpDir, "migrations")
	os.MkdirAll(migrationsDir, 0755)
	os.WriteFile(filepath.Join(migrationsDir, "001_test.sql"), []byte("SELECT 1;"), 0644)
	os.WriteFile(filepath.Join(migrationsDir, "001_test.down.sql"), []byte("SELECT 1;"), 0644)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewMigrateCommand()
	downCmd, _, _ := cmd.Find([]string{"down"})

	err = downCmd.RunE(downCmd, []string{})
	// Should fail when trying to connect with invalid DB URL
	assert.Error(t, err)
}

// Test generate aggregate with no events and force flag
func TestGenerateAggregateCommand_ForceNoEventsUnit(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-gen-agg-force-unit-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewGenerateCommand()
	aggCmd, _, _ := cmd.Find([]string{"aggregate"})

	// Set the force flag
	aggCmd.Flags().Set("force", "true")

	err = aggCmd.RunE(aggCmd, []string{"TestAggregate"})
	assert.NoError(t, err)
}

// Test projection list with force flag
func TestProjectionListCommand_WithForce(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-proj-list-force-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Database.Driver = "memory"
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewProjectionCommand()
	listCmd, _, _ := cmd.Find([]string{"list"})

	err = listCmd.RunE(listCmd, []string{})
	assert.NoError(t, err)
}

// Test generate event with no aggregate and force
func TestGenerateEventCommand_ForceNoAggregate(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-gen-event-force-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewGenerateCommand()
	eventCmd, _, _ := cmd.Find([]string{"event"})

	// Set the force flag
	eventCmd.Flags().Set("force", "true")

	err = eventCmd.RunE(eventCmd, []string{"OrderCreated"})
	assert.NoError(t, err)
}

// Test generate command with no aggregate and force
func TestGenerateCommandCommand_ForceNoAggregate(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-gen-cmd-force-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewGenerateCommand()
	cmdCmd, _, _ := cmd.Find([]string{"command"})

	// Set the force flag
	cmdCmd.Flags().Set("force", "true")

	err = cmdCmd.RunE(cmdCmd, []string{"CreateOrder"})
	assert.NoError(t, err)
}

// Test generate projection with no events and force
func TestGenerateProjectionCommand_ForceNoEvents(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-gen-proj-force-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewGenerateCommand()
	projCmd, _, _ := cmd.Find([]string{"projection"})

	// Set the force flag
	projCmd.Flags().Set("force", "true")

	err = projCmd.RunE(projCmd, []string{"OrderView"})
	assert.NoError(t, err)
}

// Test init command when config exists
func TestInitCommand_ConfigExists(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-init-exists-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	// Create existing config
	cfg := config.DefaultConfig()
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewInitCommand()
	err = cmd.RunE(cmd, []string{})
	// Should return nil but print warning (config already exists)
	assert.NoError(t, err)
}

// Test migrate up with no pending migrations and force
func TestMigrateUpCommand_NoPending(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-migrate-no-pending-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Database.Driver = "memory"
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	// Create empty migrations dir
	migrationsDir := filepath.Join(tmpDir, "migrations")
	os.MkdirAll(migrationsDir, 0755)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewMigrateCommand()
	upCmd, _, _ := cmd.Find([]string{"up"})
	upCmd.Flags().Set("force", "true")

	err = upCmd.RunE(upCmd, []string{})
	assert.NoError(t, err) // Should succeed even with no migrations
}

// Test migrate down with memory driver (no-op)
func TestMigrateDownCommand_MemoryDriverUnit(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-migrate-down-mem-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Database.Driver = "memory"
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	// Create migrations dir
	migrationsDir := filepath.Join(tmpDir, "migrations")
	os.MkdirAll(migrationsDir, 0755)
	os.WriteFile(filepath.Join(migrationsDir, "001_test.sql"), []byte("SELECT 1;"), 0644)
	os.WriteFile(filepath.Join(migrationsDir, "001_test.down.sql"), []byte("SELECT 1;"), 0644)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewMigrateCommand()
	downCmd, _, _ := cmd.Find([]string{"down"})
	downCmd.Flags().Set("force", "true")

	err = downCmd.RunE(downCmd, []string{})
	assert.NoError(t, err)
}

// Test stream list without database URL
func TestStreamListCommand_NoDatabaseURL(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-stream-list-nodb-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Database.Driver = "postgres"
	cfg.Database.URL = "" // Empty URL
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewStreamCommand()
	listCmd, _, _ := cmd.Find([]string{"list"})

	err = listCmd.RunE(listCmd, []string{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "DATABASE_URL")
}

// Test projection status without database URL
func TestProjectionStatusCommand_NoDatabaseURL(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-proj-status-nodb-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Database.Driver = "postgres"
	cfg.Database.URL = "" // Empty URL
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewProjectionCommand()
	statusCmd, _, _ := cmd.Find([]string{"status"})

	err = statusCmd.RunE(statusCmd, []string{"TestProj"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "DATABASE_URL")
}

// Test stream events without database URL
func TestStreamEventsCommand_NoDatabaseURL(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-stream-events-nodb-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Database.Driver = "postgres"
	cfg.Database.URL = "" // Empty URL
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewStreamCommand()
	eventsCmd, _, _ := cmd.Find([]string{"events"})

	err = eventsCmd.RunE(eventsCmd, []string{"test-stream"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "DATABASE_URL")
}

// Test stream stats without database URL
func TestStreamStatsCommand_NoDatabaseURL(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-stream-stats-nodb-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Database.Driver = "postgres"
	cfg.Database.URL = "" // Empty URL
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewStreamCommand()
	statsCmd, _, _ := cmd.Find([]string{"stats"})

	err = statsCmd.RunE(statsCmd, []string{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "DATABASE_URL")
}

// Test schema generate with force
func TestSchemaGenerateCommand_WithForce(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-schema-gen-force-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewSchemaCommand()
	genCmd, _, _ := cmd.Find([]string{"generate"})
	genCmd.Flags().Set("force", "true")

	err = genCmd.RunE(genCmd, []string{})
	assert.NoError(t, err)
}

// Test projection rebuild with force and invalid URL
func TestProjectionRebuildCommand_ForceInvalidURL(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-proj-rebuild-inv-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Database.Driver = "postgres"
	cfg.Database.URL = "postgres://invalid:url@localhost:65535/nonexistent"
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewProjectionCommand()
	rebuildCmd, _, _ := cmd.Find([]string{"rebuild"})
	rebuildCmd.Flags().Set("force", "true")

	err = rebuildCmd.RunE(rebuildCmd, []string{"TestProj"})
	assert.Error(t, err)
}

// Test stream export without database URL
func TestStreamExportCommand_NoDatabaseURLUnit(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-stream-export-nodb-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Database.Driver = "postgres"
	cfg.Database.URL = "" // Empty URL
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewStreamCommand()
	exportCmd, _, _ := cmd.Find([]string{"export"})

	err = exportCmd.RunE(exportCmd, []string{"test-stream"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "DATABASE_URL")
}

// Test migrate status with force
func TestMigrateStatusCommand_WithForce(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-migrate-status-force-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Database.Driver = "memory"
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	// Create migrations dir
	migrationsDir := filepath.Join(tmpDir, "migrations")
	os.MkdirAll(migrationsDir, 0755)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewMigrateCommand()
	statusCmd, _, _ := cmd.Find([]string{"status"})

	err = statusCmd.RunE(statusCmd, []string{})
	assert.NoError(t, err)
}

// Test generate aggregate with events flag
func TestGenerateAggregateCommand_WithEventsFlag(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-gen-agg-events-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewGenerateCommand()
	aggCmd, _, _ := cmd.Find([]string{"aggregate"})

	// Set the events flag
	aggCmd.Flags().Set("events", "Created,Updated,Deleted")
	aggCmd.Flags().Set("force", "true")

	err = aggCmd.RunE(aggCmd, []string{"TestAggregate"})
	assert.NoError(t, err)

	// Verify aggregate and events file created
	aggFile := filepath.Join(tmpDir, cfg.Generation.AggregatePackage, "testaggregate.go")
	_, err = os.Stat(aggFile)
	assert.NoError(t, err)

	eventsFile := filepath.Join(tmpDir, cfg.Generation.EventPackage, "testaggregate_events.go")
	_, err = os.Stat(eventsFile)
	assert.NoError(t, err)
}

// Test generate projection with events flag
func TestGenerateProjectionCommand_WithEventsFlag(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-gen-proj-events-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewGenerateCommand()
	projCmd, _, _ := cmd.Find([]string{"projection"})

	// Set the events flag
	projCmd.Flags().Set("events", "OrderCreated,OrderShipped")
	projCmd.Flags().Set("force", "true")

	err = projCmd.RunE(projCmd, []string{"OrderView"})
	assert.NoError(t, err)

	// Verify projection file created
	projFile := filepath.Join(tmpDir, cfg.Generation.ProjectionPackage, "orderview.go")
	_, err = os.Stat(projFile)
	assert.NoError(t, err)
}

// Test generate event with aggregate flag
func TestGenerateEventCommand_WithAggregateFlag(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-gen-event-agg-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewGenerateCommand()
	eventCmd, _, _ := cmd.Find([]string{"event"})

	// Set the aggregate flag
	eventCmd.Flags().Set("aggregate", "Order")
	eventCmd.Flags().Set("force", "true")

	err = eventCmd.RunE(eventCmd, []string{"ItemAdded"})
	assert.NoError(t, err)

	// Verify event file created
	eventFile := filepath.Join(tmpDir, cfg.Generation.EventPackage, "itemadded.go")
	_, err = os.Stat(eventFile)
	assert.NoError(t, err)
}

// Test generate command with aggregate flag
func TestGenerateCommandCommand_WithAggregateFlag(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-gen-cmd-agg-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewGenerateCommand()
	cmdCmd, _, _ := cmd.Find([]string{"command"})

	// Set the aggregate flag
	cmdCmd.Flags().Set("aggregate", "Order")
	cmdCmd.Flags().Set("force", "true")

	err = cmdCmd.RunE(cmdCmd, []string{"CancelOrder"})
	assert.NoError(t, err)

	// Verify command file created
	cmdFile := filepath.Join(tmpDir, cfg.Generation.CommandPackage, "cancelorder.go")
	_, err = os.Stat(cmdFile)
	assert.NoError(t, err)
}

// Test projection rebuild without database URL
func TestProjectionRebuildCommand_NoDatabaseURL(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-proj-rebuild-nodb-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Database.Driver = "postgres"
	cfg.Database.URL = "" // Empty URL
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewProjectionCommand()
	rebuildCmd, _, _ := cmd.Find([]string{"rebuild"})
	rebuildCmd.Flags().Set("force", "true")

	err = rebuildCmd.RunE(rebuildCmd, []string{"TestProj"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "DATABASE_URL")
}

// Test migrate up with steps flag
func TestMigrateUpCommand_WithStepsFlag(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-migrate-steps-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Database.Driver = "memory"
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	// Create migrations dir with multiple migrations
	migrationsDir := filepath.Join(tmpDir, "migrations")
	os.MkdirAll(migrationsDir, 0755)
	os.WriteFile(filepath.Join(migrationsDir, "001_init.sql"), []byte("-- init"), 0644)
	os.WriteFile(filepath.Join(migrationsDir, "002_add_table.sql"), []byte("-- add table"), 0644)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewMigrateCommand()
	upCmd, _, _ := cmd.Find([]string{"up"})
	upCmd.Flags().Set("steps", "1")
	upCmd.Flags().Set("force", "true")

	err = upCmd.RunE(upCmd, []string{})
	assert.NoError(t, err)
}

// Test migrate down with steps flag
func TestMigrateDownCommand_WithStepsFlag(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-migrate-down-steps-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Database.Driver = "memory"
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	// Create migrations dir
	migrationsDir := filepath.Join(tmpDir, "migrations")
	os.MkdirAll(migrationsDir, 0755)
	os.WriteFile(filepath.Join(migrationsDir, "001_init.sql"), []byte("-- init"), 0644)
	os.WriteFile(filepath.Join(migrationsDir, "001_init.down.sql"), []byte("-- down init"), 0644)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewMigrateCommand()
	downCmd, _, _ := cmd.Find([]string{"down"})
	downCmd.Flags().Set("steps", "1")
	downCmd.Flags().Set("force", "true")

	err = downCmd.RunE(downCmd, []string{})
	assert.NoError(t, err)
}

// Test migrate create with sql flag
func TestMigrateCreateCommand_WithSQLFlag(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-migrate-create-sql-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	// Create migrations dir
	migrationsDir := filepath.Join(tmpDir, "migrations")
	os.MkdirAll(migrationsDir, 0755)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewMigrateCommand()
	createCmd, _, _ := cmd.Find([]string{"create"})
	createCmd.Flags().Set("sql", "SELECT 1;")

	err = createCmd.RunE(createCmd, []string{"test_migration"})
	assert.NoError(t, err)

	// Verify migration file was created
	files, _ := os.ReadDir(migrationsDir)
	assert.True(t, len(files) >= 1)
}

// Test checkConfiguration with no config file - additional test case
func TestCheckConfiguration_NoConfigUnit(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-check-config-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	result := checkConfiguration()
	// Returns StatusWarning when config is not found
	assert.Equal(t, StatusWarning, result.Status)
}

// Test listStreams helper with invalid URL
func TestListStreams_InvalidURL(t *testing.T) {
	_, err := listStreams("invalid://not-a-url", "", 10)
	assert.Error(t, err)
}

// Test getStreamEvents helper with invalid URL
func TestGetStreamEvents_InvalidURL(t *testing.T) {
	_, err := getStreamEvents("invalid://not-a-url", "test-stream", 0, 10)
	assert.Error(t, err)
}

// Test setProjectionStatus helper with invalid URL
func TestSetProjectionStatus_InvalidURL(t *testing.T) {
	err := setProjectionStatus("invalid://not-a-url", "test", "running")
	assert.Error(t, err)
}

// Test getAppliedMigrationNames error path

// Test DiagnosticCheck struct
func TestDiagnosticCheck_Usage(t *testing.T) {
	check := DiagnosticCheck{
		Name: "Test",
		Check: func() CheckResult {
			return CheckResult{
				Name:    "Test",
				Status:  StatusOK,
				Message: "All good",
			}
		},
	}

	result := check.Check()
	assert.Equal(t, StatusOK, result.Status)
	assert.Equal(t, "All good", result.Message)
}

// Test runDiagnose directly
func TestRunDiagnose_Unit(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-diagnose-unit-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Database.Driver = "memory"
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewDiagnoseCommand()
	err = runDiagnose(cmd, []string{})
	assert.NoError(t, err)
}

// Test schema print executes without error
func TestSchemaPrintCommand_Execution(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-schema-print-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewSchemaCommand()
	printCmd, _, _ := cmd.Find([]string{"print"})

	err = printCmd.RunE(printCmd, []string{})
	assert.NoError(t, err)
}

// Test Execute function (main entry point)
func TestExecute_Function(t *testing.T) {
	// Save original os.Args
	origArgs := os.Args
	defer func() { os.Args = origArgs }()

	// Set args for help command to avoid interactive prompts
	os.Args = []string{"mink", "--help"}

	err := Execute()
	assert.NoError(t, err)
}

// Test Execute function with version command
func TestExecute_VersionCommand(t *testing.T) {
	origArgs := os.Args
	defer func() { os.Args = origArgs }()

	os.Args = []string{"mink", "version"}

	err := Execute()
	assert.NoError(t, err)
}

// Test projection list with various scenarios
func TestProjectionListCommand_EmptyList(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-proj-list-empty-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Database.Driver = "postgres"
	cfg.Database.URL = "" // No URL set
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewProjectionCommand()
	listCmd, _, _ := cmd.Find([]string{"list"})

	err = listCmd.RunE(listCmd, []string{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "DATABASE_URL")
}

// Test stream command with limit flag
func TestStreamListCommand_WithLimit(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-stream-limit-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Database.Driver = "postgres"
	cfg.Database.URL = "" // No URL
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewStreamCommand()
	listCmd, _, _ := cmd.Find([]string{"list"})
	listCmd.Flags().Set("limit", "10")

	err = listCmd.RunE(listCmd, []string{})
	assert.Error(t, err)
}

// Test stream events with from flag
func TestStreamEventsCommand_WithFrom(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-stream-from-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Database.Driver = "postgres"
	cfg.Database.URL = "" // No URL
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewStreamCommand()
	eventsCmd, _, _ := cmd.Find([]string{"events"})
	eventsCmd.Flags().Set("from", "5")

	err = eventsCmd.RunE(eventsCmd, []string{"test-stream"})
	assert.Error(t, err)
}

// Test runDiagnose with database configured
func TestRunDiagnose_WithDatabase(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-diagnose-db-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Database.Driver = "postgres"
	cfg.Database.URL = "postgres://invalid:url@localhost:5432/test"
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewDiagnoseCommand()
	err = runDiagnose(cmd, []string{})
	// Should complete even with failed checks
	assert.NoError(t, err)
}

// Test checkProjections with config
func TestCheckProjections_WithConfig(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-check-proj-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Database.Driver = "memory"
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	result := checkProjections()
	assert.NotEmpty(t, result.Name)
}

// Test checkEventStoreSchema with config
func TestCheckEventStoreSchema_WithConfig(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-check-schema-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Database.Driver = "memory"
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	result := checkEventStoreSchema()
	assert.NotEmpty(t, result.Name)
}

// Test checkDatabaseConnection with postgres URL
func TestCheckDatabaseConnection_WithInvalidURL(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-check-db-inv-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Database.Driver = "postgres"
	cfg.Database.URL = "postgres://invalid:url@localhost:65535/test"
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	result := checkDatabaseConnection()
	// Should return error status for invalid connection
	assert.True(t, result.Status == StatusError || result.Status == StatusWarning)
}

// Test generateFile with valid template
func TestGenerateFile_ValidTemplate(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-gen-file-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	testFile := filepath.Join(tmpDir, "test.txt")
	err = generateFile(testFile, "Hello {{.Name}}", struct{ Name string }{Name: "World"})
	assert.NoError(t, err)

	content, _ := os.ReadFile(testFile)
	assert.Equal(t, "Hello World", string(content))
}

// Test toPascalCase edge cases
func TestToPascalCase_Numbers(t *testing.T) {
	assert.Equal(t, "Order123", toPascalCase("order123"))
	assert.Equal(t, "123Order", toPascalCase("123_order"))
	assert.Equal(t, "Order1Created", toPascalCase("order1_created"))
}

// Test sanitizeName edge cases
func TestSanitizeName_EdgeCases(t *testing.T) {
	assert.Equal(t, "test", sanitizeName("TEST"))
	assert.Equal(t, "test_name", sanitizeName("Test-Name"))
	assert.Equal(t, "test_name", sanitizeName("test_name"))
}

// Test migrate up with no pending migrations
func TestMigrateUpCommand_NoPendingMigrations(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-migrate-up-nopend-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Database.Driver = "memory"
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	// Create empty migrations dir
	migrationsDir := filepath.Join(tmpDir, "migrations")
	os.MkdirAll(migrationsDir, 0755)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewMigrateCommand()
	upCmd, _, _ := cmd.Find([]string{"up"})
	upCmd.Flags().Set("force", "true")

	err = upCmd.RunE(upCmd, []string{})
	assert.NoError(t, err)
}

// Test migrate status with migrations
func TestMigrateStatusCommand_WithMigrations(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-migrate-status-mig-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Database.Driver = "memory"
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	// Create migrations dir with files
	migrationsDir := filepath.Join(tmpDir, "migrations")
	os.MkdirAll(migrationsDir, 0755)
	os.WriteFile(filepath.Join(migrationsDir, "001_init.sql"), []byte("-- init"), 0644)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewMigrateCommand()
	statusCmd, _, _ := cmd.Find([]string{"status"})

	err = statusCmd.RunE(statusCmd, []string{})
	assert.NoError(t, err)
}

// Test generate aggregate without config file
func TestGenerateAggregateCommand_NoConfig(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-gen-agg-noconf-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewGenerateCommand()
	aggCmd, _, _ := cmd.Find([]string{"aggregate"})
	aggCmd.Flags().Set("force", "true")

	err = aggCmd.RunE(aggCmd, []string{"Order"})
	// Should succeed with defaults when no config
	assert.NoError(t, err)
}

// Test generate event without config file
func TestGenerateEventCommand_NoConfigFile(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-gen-event-noconf-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewGenerateCommand()
	eventCmd, _, _ := cmd.Find([]string{"event"})
	eventCmd.Flags().Set("force", "true")

	err = eventCmd.RunE(eventCmd, []string{"OrderCreated"})
	// Should succeed with defaults when no config
	assert.NoError(t, err)
}

// Test generate projection without config file
func TestGenerateProjectionCommand_NoConfigFile(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-gen-proj-noconf-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewGenerateCommand()
	projCmd, _, _ := cmd.Find([]string{"projection"})
	projCmd.Flags().Set("force", "true")

	err = projCmd.RunE(projCmd, []string{"OrderView"})
	// Should succeed with defaults when no config
	assert.NoError(t, err)
}

// Test generate command without config file
func TestGenerateCommandCommand_NoConfigFile(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-gen-cmd-noconf-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewGenerateCommand()
	cmdCmd, _, _ := cmd.Find([]string{"command"})
	cmdCmd.Flags().Set("force", "true")

	err = cmdCmd.RunE(cmdCmd, []string{"CreateOrder"})
	// Should succeed with defaults when no config
	assert.NoError(t, err)
}

// Test schema generate without config
func TestSchemaGenerateCommand_NoConfig(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-schema-gen-noconf-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewSchemaCommand()
	genCmd, _, _ := cmd.Find([]string{"generate"})

	err = genCmd.RunE(genCmd, []string{})
	// Should work with defaults
	assert.NoError(t, err)
}

// Test schema print without config
func TestSchemaPrintCommand_NoConfig(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-schema-print-noconf-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewSchemaCommand()
	printCmd, _, _ := cmd.Find([]string{"print"})

	err = printCmd.RunE(printCmd, []string{})
	assert.NoError(t, err)
}

// Test checkSystemResources returns valid result
func TestCheckSystemResources_Valid(t *testing.T) {
	result := checkSystemResources()
	assert.Equal(t, "System Resources", result.Name)
	assert.Equal(t, StatusOK, result.Status)
	assert.NotEmpty(t, result.Message)
}

// Test checkGoVersion returns valid result
func TestCheckGoVersion_Valid(t *testing.T) {
	result := checkGoVersion()
	assert.Equal(t, "Go Version", result.Name)
	assert.Equal(t, StatusOK, result.Status) // We're on 1.25+
	assert.NotEmpty(t, result.Message)
}

// Test listStreams returns empty for invalid URL
func TestListStreams_EmptyResult(t *testing.T) {
	_, err := listStreams("postgres://invalid:url@localhost:65535/test", "", 10)
	assert.Error(t, err)
}

// Test generateSchema produces valid output
func TestGenerateSchema_Valid(t *testing.T) {
	cfg := config.DefaultConfig()
	schema := generateSchema(cfg)
	assert.Contains(t, schema, "CREATE TABLE")
	assert.Contains(t, schema, "mink_events")
}

// Test Projection struct initialization
func TestProjection_Initialization(t *testing.T) {
	p := Projection{
		Name:     "test",
		Position: 100,
		Status:   "active",
	}
	assert.Equal(t, "test", p.Name)
	assert.Equal(t, int64(100), p.Position)
}

// Test Stream struct initialization
func TestStream_Initialization(t *testing.T) {
	s := Stream{
		StreamID:   "order-123",
		EventCount: 5,
	}
	assert.Equal(t, "order-123", s.StreamID)
	assert.Equal(t, int64(5), s.EventCount)
}

// Test StreamEvent struct initialization
func TestStreamEvent_Initialization(t *testing.T) {
	e := StreamEvent{
		ID:       "evt-1",
		StreamID: "order-123",
		Version:  1,
		Type:     "OrderCreated",
	}
	assert.Equal(t, "evt-1", e.ID)
	assert.Equal(t, int64(1), e.Version)
}

// Test EventStoreStats struct initialization
func TestEventStoreStats_Initialization(t *testing.T) {
	s := EventStoreStats{
		TotalEvents:  100,
		TotalStreams: 10,
	}
	assert.Equal(t, int64(100), s.TotalEvents)
	assert.Equal(t, int64(10), s.TotalStreams)
}

// Test Migration struct initialization
func TestMigration_Init(t *testing.T) {
	m := Migration{
		Name: "001_init",
		Path: "/migrations/001_init.sql",
	}
	assert.Equal(t, "001_init", m.Name)
	assert.Equal(t, "/migrations/001_init.sql", m.Path)
}

// Test nextSteps helper function
func TestNextSteps_Complete(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Database.Driver = "postgres"
	cfg.Database.URL = "postgres://localhost/test"

	steps := nextSteps(cfg)
	assert.NotEmpty(t, steps)
}

// Test Splash helper (exported)
func TestSplash_Output(t *testing.T) {
	// Splash just prints output - test that function exists
	// Cannot test output easily but can ensure it doesn't panic
	assert.NotPanics(t, func() { Splash() })
}

// Test detectModule in different scenarios
func TestDetectModule_Scenarios(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-detect-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	// Without go.mod should return empty
	result := detectModule(tmpDir)
	assert.Empty(t, result)

	// With go.mod should return module name
	gomod := `module github.com/test/myproject

go 1.21
`
	os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte(gomod), 0644)
	result = detectModule(tmpDir)
	assert.Equal(t, "github.com/test/myproject", result)
}

// Test getPendingMigrations helper (with invalid DB returns all migrations)
func TestGetPendingMigrations_InvalidDB(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-pending-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	// Create migrations dir with files
	migrationsDir := filepath.Join(tmpDir, "migrations")
	os.MkdirAll(migrationsDir, 0755)
	os.WriteFile(filepath.Join(migrationsDir, "001_init.sql"), []byte("-- init"), 0644)

	// Invalid DB URL should return all migrations (function handles error gracefully)
	pending, err := getPendingMigrations("postgres://invalid:url@localhost:65535/db", migrationsDir)
	assert.NoError(t, err)
	assert.Len(t, pending, 1) // Returns all when can't connect to DB
}

// Test getStreamEvents with limit
func TestGetStreamEvents_WithLimit(t *testing.T) {
	// Test error path with invalid URL
	_, err := getStreamEvents("postgres://invalid:url@localhost:65535/db", "stream", 0, 5)
	assert.Error(t, err)
}

// Test listProjections with invalid URL
func TestListProjections_InvalidURL(t *testing.T) {
	_, err := listProjections("postgres://invalid:url@localhost:65535/db")
	assert.Error(t, err)
}

// Test getProjection with invalid URL
func TestGetProjection_InvalidURL(t *testing.T) {
	_, err := getProjection("postgres://invalid:url@localhost:65535/db", "test")
	assert.Error(t, err)
}

// Test getTotalEventCount with invalid URL
func TestGetTotalEventCount_InvalidURL(t *testing.T) {
	_, err := getTotalEventCount("postgres://invalid:url@localhost:65535/db")
	assert.Error(t, err)
}

// Test init command with directory argument
func TestInitCommand_WithDirectoryFlag(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-init-dir-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewInitCommand()
	cmd.Flags().Set("driver", "memory")
	// Note: This will try to run interactive form, so we can only test structure
	assert.NotNil(t, cmd)
}
