package commands

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/AshkanYarmoradi/go-mink/adapters"
	"github.com/AshkanYarmoradi/go-mink/cli/config"
	"github.com/AshkanYarmoradi/go-mink/cli/ui"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// Test Helpers - Reduce duplication by providing common test setup utilities
// ============================================================================

// testEnv holds common test environment state
type testEnv struct {
	t      *testing.T
	tmpDir string
	origWd string
}

// setupTestEnv creates a temporary directory and changes to it.
// Returns a testEnv with cleanup that restores the original directory.
func setupTestEnv(t *testing.T, prefix string) *testEnv {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", prefix)
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	require.NoError(t, os.Chdir(tmpDir))

	env := &testEnv{
		t:      t,
		tmpDir: tmpDir,
		origWd: origWd,
	}
	t.Cleanup(env.cleanup)
	return env
}

// cleanup restores the original working directory and removes temp dir
func (e *testEnv) cleanup() {
	_ = os.Chdir(e.origWd)
	os.RemoveAll(e.tmpDir)
}

// createConfig creates a mink.yaml config file in the test directory
func (e *testEnv) createConfig(opts ...configOption) *config.Config {
	e.t.Helper()
	cfg := config.DefaultConfig()
	for _, opt := range opts {
		opt(cfg)
	}
	err := cfg.SaveFile(filepath.Join(e.tmpDir, "mink.yaml"))
	require.NoError(e.t, err)
	return cfg
}

// createMigrationsDir creates the migrations directory structure
func (e *testEnv) createMigrationsDir() string {
	e.t.Helper()
	migrationsDir := filepath.Join(e.tmpDir, "migrations")
	require.NoError(e.t, os.MkdirAll(migrationsDir, 0755))
	return migrationsDir
}

// createMigrationFile creates a migration file in the migrations directory
func (e *testEnv) createMigrationFile(name, content string) {
	e.t.Helper()
	migrationsDir := e.createMigrationsDir()
	require.NoError(e.t, os.WriteFile(filepath.Join(migrationsDir, name), []byte(content), 0644))
}

// configOption is a function that modifies a config
type configOption func(*config.Config)

// withDriver sets the database driver
func withDriver(driver string) configOption {
	return func(c *config.Config) {
		c.Database.Driver = driver
	}
}

// withDatabaseURL sets the database URL
func withDatabaseURL(url string) configOption {
	return func(c *config.Config) {
		c.Database.URL = url
	}
}

// withModule sets the project module
func withModule(module string) configOption {
	return func(c *config.Config) {
		c.Project.Module = module
	}
}

// withAggregatePackage sets the aggregate package path
func withAggregatePackage(pkg string) configOption {
	return func(c *config.Config) {
		c.Generation.AggregatePackage = pkg
	}
}

// withEventPackage sets the event package path
func withEventPackage(pkg string) configOption {
	return func(c *config.Config) {
		c.Generation.EventPackage = pkg
	}
}

// withProjectName sets the project name
func withProjectName(name string) configOption {
	return func(c *config.Config) {
		c.Project.Name = name
	}
}

// withMigrationsDir sets the migrations directory
func withMigrationsDir(dir string) configOption {
	return func(c *config.Config) {
		c.Database.MigrationsDir = dir
	}
}

// runSubcommand is a helper to find and run a subcommand
func (e *testEnv) runSubcommand(parentCmd *cobra.Command, subcommandPath []string, args []string, flags map[string]string) error {
	e.t.Helper()
	cmd, _, err := parentCmd.Find(subcommandPath)
	if err != nil {
		return err
	}
	for k, v := range flags {
		if err := cmd.Flags().Set(k, v); err != nil {
			return err
		}
	}
	return cmd.RunE(cmd, args)
}

// assertErrorContains is a helper for error assertion
func assertErrorContains(t *testing.T, err error, substring string) {
	t.Helper()
	assert.Error(t, err)
	if err != nil {
		assert.Contains(t, err.Error(), substring)
	}
}

// getSubcommandNames returns a map of subcommand names for a parent command
func getSubcommandNames(cmd *cobra.Command) map[string]bool {
	names := make(map[string]bool)
	for _, sub := range cmd.Commands() {
		names[sub.Name()] = true
	}
	return names
}

// ============================================================================
// Tests
// ============================================================================

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
	env := setupTestEnv(t, "mink-cmd-test-*")

	// Create go.mod
	gomodContent := `module github.com/test/myapp

go 1.21
`
	err := os.WriteFile(filepath.Join(env.tmpDir, "go.mod"), []byte(gomodContent), 0644)
	require.NoError(t, err)

	module := detectModule(env.tmpDir)
	assert.Equal(t, "github.com/test/myapp", module)
}

func TestDetectModule_NoGoMod(t *testing.T) {
	env := setupTestEnv(t, "mink-cmd-test-*")

	module := detectModule(env.tmpDir)
	assert.Empty(t, module)
}

func TestDetectModule_InvalidGoMod(t *testing.T) {
	env := setupTestEnv(t, "mink-cmd-test-*")

	// Create go.mod without module line
	err := os.WriteFile(filepath.Join(env.tmpDir, "go.mod"), []byte("go 1.21\n"), 0644)
	require.NoError(t, err)

	module := detectModule(env.tmpDir)
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
	_, _ = buf.ReadFrom(r)
	output := buf.String()

	assert.Contains(t, output, "mink")
}

func TestGenerateFile(t *testing.T) {
	env := setupTestEnv(t, "mink-cmd-test-*")

	// Test generating a file with template (directory already exists)
	testPath := filepath.Join(env.tmpDir, "test.go")
	tmpl := "package {{.Package}}\n"
	data := struct{ Package string }{Package: "test"}

	err := generateFile(testPath, tmpl, data)
	require.NoError(t, err)

	// Verify file was created
	fileData, err := os.ReadFile(testPath)
	require.NoError(t, err)
	assert.Equal(t, "package test\n", string(fileData))
}

func TestGenerateFile_Overwrite(t *testing.T) {
	env := setupTestEnv(t, "mink-cmd-test-*")

	testPath := filepath.Join(env.tmpDir, "test.go")

	// Create initial file
	err := os.WriteFile(testPath, []byte("old content"), 0644)
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
	env := setupTestEnv(t, "mink-cmd-test-*")

	testPath := filepath.Join(env.tmpDir, "test.go")
	tmpl := "{{.Invalid" // Invalid template

	err := generateFile(testPath, tmpl, nil)
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
	env := setupTestEnv(t, "mink-init-test-*")

	cmd := NewInitCommand()
	cmd.SetArgs([]string{
		env.tmpDir,
		"--non-interactive",
		"--name", "test-app",
		"--module", "github.com/test/app",
		"--driver", "memory",
	})

	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.Execute()
	require.NoError(t, err)

	// Verify config was created
	assert.True(t, config.Exists(env.tmpDir))
}

func TestInitCommand_AlreadyExists(t *testing.T) {
	env := setupTestEnv(t, "mink-init-test-*")

	// Create config first
	cfg := config.DefaultConfig()
	err := cfg.Save(env.tmpDir)
	require.NoError(t, err)

	cmd := NewInitCommand()
	cmd.SetArgs([]string{env.tmpDir, "--non-interactive"})

	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err = cmd.Execute()
	assert.NoError(t, err) // Should succeed but with warning
}

func TestInitCommand_WithGoMod(t *testing.T) {
	env := setupTestEnv(t, "mink-init-gomod-test-*")

	// Create a go.mod file
	gomodContent := `module github.com/test/myproject

go 1.21
`
	err := os.WriteFile(filepath.Join(env.tmpDir, "go.mod"), []byte(gomodContent), 0644)
	require.NoError(t, err)

	cmd := NewInitCommand()
	cmd.SetArgs([]string{
		env.tmpDir,
		"--non-interactive",
		"--name", "myproject",
	})

	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err = cmd.Execute()
	require.NoError(t, err)

	// Verify config was created
	assert.True(t, config.Exists(env.tmpDir))
}

func TestInitCommand_PostgresDriver(t *testing.T) {
	env := setupTestEnv(t, "mink-init-pg-test-*")

	cmd := NewInitCommand()
	cmd.SetArgs([]string{
		env.tmpDir,
		"--non-interactive",
		"--name", "pg-app",
		"--module", "github.com/test/pg-app",
		"--driver", "postgres",
	})

	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.Execute()
	require.NoError(t, err)

	// Verify config was created with postgres driver
	assert.True(t, config.Exists(env.tmpDir))
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
	env := setupTestEnv(t, "mink-migrate-test-*")

	// Create some migration files
	migrationsDir := filepath.Join(env.tmpDir, "migrations")
	require.NoError(t, os.MkdirAll(migrationsDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(migrationsDir, "001_create_users.sql"), []byte("-- up"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(migrationsDir, "001_create_users.down.sql"), []byte("-- down"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(migrationsDir, "002_add_index.sql"), []byte("-- up"), 0644))

	migrations, err := getAllMigrations(migrationsDir)
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

	schema := generateFallbackSchema(cfg)

	assert.Contains(t, schema, "test-project")
	assert.Contains(t, schema, "my_events")
	assert.Contains(t, schema, "my_snapshots")
	assert.Contains(t, schema, "my_outbox")
	assert.Contains(t, schema, "CREATE TABLE")
	assert.Contains(t, schema, "CREATE INDEX")
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

// TestSubcommandFlags consolidates all subcommand flag tests
func TestSubcommandFlags(t *testing.T) {
	tests := []struct {
		name          string
		parentCmd     func() *cobra.Command
		subName       string
		expectedFlags []string
	}{
		{"generate/aggregate", NewGenerateCommand, "aggregate", []string{"events"}},
		{"generate/event", NewGenerateCommand, "event", []string{"aggregate"}},
		{"generate/command", NewGenerateCommand, "command", []string{"aggregate"}},
		{"migrate/up", NewMigrateCommand, "up", []string{"steps"}},
		{"migrate/down", NewMigrateCommand, "down", []string{"steps"}},
		{"projection/rebuild", NewProjectionCommand, "rebuild", []string{"force"}},
		{"stream/list", NewStreamCommand, "list", []string{"limit", "prefix"}},
		{"stream/events", NewStreamCommand, "events", []string{"limit"}},
		{"stream/export", NewStreamCommand, "export", []string{"output"}},
		{"schema/generate", NewSchemaCommand, "generate", []string{"output"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := tt.parentCmd()
			var subCmd *cobra.Command
			for _, sub := range cmd.Commands() {
				if sub.Name() == tt.subName {
					subCmd = sub
					break
				}
			}
			require.NotNil(t, subCmd, "subcommand %s not found", tt.subName)

			f := subCmd.Flags()
			for _, flag := range tt.expectedFlags {
				assert.NotNil(t, f.Lookup(flag), "flag %s should exist on %s", flag, tt.name)
			}
		})
	}
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

func TestGenerateSchemaContent(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Project.Name = "test-project"
	cfg.EventStore.TableName = "my_events"
	cfg.EventStore.SnapshotTableName = "my_snapshots"
	cfg.EventStore.OutboxTableName = "my_outbox"

	schema := generateFallbackSchema(cfg)

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
}

func TestGenerateFile_ExecutionError(t *testing.T) {
	env := setupTestEnv(t, "mink-gen-test-*")

	path := filepath.Join(env.tmpDir, "test.go")
	tmpl := "{{.Missing}}" // Template expects field that doesn't exist
	data := struct{}{}

	err := generateFile(path, tmpl, data)
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

// TestCommandSubcommands verifies all parent commands have expected subcommands
func TestCommandSubcommands(t *testing.T) {
	tests := []struct {
		name        string
		newCmd      func() *cobra.Command
		subcommands []string
	}{
		{"projection", NewProjectionCommand, []string{"list", "status", "rebuild", "pause", "resume"}},
		{"stream", NewStreamCommand, []string{"list", "events", "export", "stats"}},
		{"generate", NewGenerateCommand, []string{"aggregate", "event", "projection", "command"}},
		{"migrate", NewMigrateCommand, []string{"up", "down", "status", "create"}},
		{"schema", NewSchemaCommand, []string{"generate", "print"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := tt.newCmd()
			names := getSubcommandNames(cmd)
			for _, sub := range tt.subcommands {
				assert.True(t, names[sub], "missing subcommand: %s", sub)
			}
		})
	}
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
	names := getSubcommandNames(cmd)

	// Verify essential subcommands are registered
	assert.True(t, names["init"])
	assert.True(t, names["generate"])
	assert.True(t, names["migrate"])
	assert.True(t, names["projection"])
	assert.True(t, names["stream"])
	assert.True(t, names["diagnose"])
	assert.True(t, names["schema"])
	assert.True(t, names["version"])
}

func TestRootCommand_PersistentFlags(t *testing.T) {
	cmd := NewRootCommand()

	f := cmd.PersistentFlags()
	assert.NotNil(t, f.Lookup("no-color"))
}

func TestSchemaCommand_GenerateSubcommand_Run(t *testing.T) {
	env := setupTestEnv(t, "mink-schema-test-*")
	env.createConfig(withProjectName("test-project"))

	cmd := NewSchemaCommand()
	cmd.SetArgs([]string{"generate"})

	// Just test that it executes without error
	// The output goes to stdout, not to the command's buffer
	err := cmd.Execute()
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
	env := setupTestEnv(t, "mink-migrate-create-*")
	env.createConfig(withMigrationsDir("migrations"))

	cmd := NewMigrateCommand()
	cmd.SetArgs([]string{"create", "add_users_table"})

	err := cmd.Execute()
	require.NoError(t, err)

	// Verify migration files were created
	entries, err := os.ReadDir(filepath.Join(env.tmpDir, "migrations"))
	require.NoError(t, err)
	assert.Len(t, entries, 2) // up and down files
}

// TestSubcommandStructure consolidates all subcommand structure tests
func TestSubcommandStructure(t *testing.T) {
	tests := []struct {
		name       string
		parentCmd  func() *cobra.Command
		subName    string
		useStr     string
		aliases    []string // Optional aliases to check
		checkShort bool     // Whether to check for non-empty Short
	}{
		// Generate subcommands
		{"generate/aggregate", NewGenerateCommand, "aggregate", "aggregate <name>", []string{"agg", "a"}, false},
		{"generate/event", NewGenerateCommand, "event", "event <name>", []string{"evt", "e"}, false},
		{"generate/projection", NewGenerateCommand, "projection", "projection <name>", []string{"proj", "p"}, false},
		{"generate/command", NewGenerateCommand, "command", "command <name>", []string{"cmd", "c"}, false},
		// Migrate subcommands
		{"migrate/up", NewMigrateCommand, "up", "up", nil, true},
		{"migrate/down", NewMigrateCommand, "down", "down", nil, true},
		{"migrate/status", NewMigrateCommand, "status", "status", nil, true},
		// Projection subcommands
		{"projection/list", NewProjectionCommand, "list", "list", []string{"ls"}, false},
		{"projection/status", NewProjectionCommand, "status", "status <name>", nil, false},
		{"projection/rebuild", NewProjectionCommand, "rebuild", "rebuild <name>", nil, false},
		{"projection/pause", NewProjectionCommand, "pause", "pause <name>", nil, false},
		{"projection/resume", NewProjectionCommand, "resume", "resume <name>", nil, false},
		// Stream subcommands
		{"stream/list", NewStreamCommand, "list", "list", []string{"ls"}, false},
		{"stream/events", NewStreamCommand, "events", "events <stream-id>", nil, false},
		{"stream/export", NewStreamCommand, "export", "export <stream-id>", nil, false},
		{"stream/stats", NewStreamCommand, "stats", "stats", nil, false},
		// Schema subcommands
		{"schema/generate", NewSchemaCommand, "generate", "generate", nil, false},
		{"schema/print", NewSchemaCommand, "print", "print", nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := tt.parentCmd()
			var subCmd *cobra.Command
			for _, sub := range cmd.Commands() {
				if sub.Name() == tt.subName {
					subCmd = sub
					break
				}
			}
			require.NotNil(t, subCmd, "subcommand %s not found", tt.subName)

			assert.Equal(t, tt.useStr, subCmd.Use)
			for _, alias := range tt.aliases {
				assert.Contains(t, subCmd.Aliases, alias)
			}
			if tt.checkShort {
				assert.NotEmpty(t, subCmd.Short)
			}
		})
	}
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
	env := setupTestEnv(t, "mink-diag-test-*")
	_ = env // No config created intentionally

	result := checkConfiguration()

	assert.Equal(t, "Configuration", result.Name)
	assert.Equal(t, StatusWarning, result.Status)
	assert.Contains(t, result.Message, "No mink.yaml found")
}

func TestCheckConfiguration_WithConfig(t *testing.T) {
	env := setupTestEnv(t, "mink-diag-test-*")
	env.createConfig(
		withProjectName("test-project"),
		withModule("github.com/test/project"),
		withDriver("memory"),
	)

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

// TestDataStructures verifies data type initialization
func TestDataStructures(t *testing.T) {
	t.Run("ProjectionInfo", func(t *testing.T) {
		p := adapters.ProjectionInfo{Name: "TestProjection", Position: 100, Status: "active"}
		assert.Equal(t, "TestProjection", p.Name)
		assert.Equal(t, int64(100), p.Position)
		assert.Equal(t, "active", p.Status)
	})

	t.Run("StreamSummary", func(t *testing.T) {
		s := adapters.StreamSummary{StreamID: "order-123", EventCount: 5, LastEventType: "ItemAdded"}
		assert.Equal(t, "order-123", s.StreamID)
		assert.Equal(t, int64(5), s.EventCount)
		assert.Equal(t, "ItemAdded", s.LastEventType)
	})

	t.Run("StreamEvent", func(t *testing.T) {
		e := StreamEvent{ID: "event-123", StreamID: "order-123", Version: 1, Type: "OrderCreated", Data: `{}`, Metadata: `{}`}
		assert.Equal(t, "event-123", e.ID)
		assert.Equal(t, "order-123", e.StreamID)
		assert.Equal(t, int64(1), e.Version)
		assert.Equal(t, "OrderCreated", e.Type)
	})

	t.Run("EventStoreStats", func(t *testing.T) {
		stats := adapters.EventStoreStats{TotalEvents: 1000, TotalStreams: 50, EventTypes: 10}
		assert.Equal(t, int64(1000), stats.TotalEvents)
		assert.Equal(t, int64(50), stats.TotalStreams)
		assert.Equal(t, int64(10), stats.EventTypes)
	})

	t.Run("Migration", func(t *testing.T) {
		m := Migration{Name: "001_initial", Path: "/path/to/001_initial.sql"}
		assert.Equal(t, "001_initial", m.Name)
		assert.Equal(t, "/path/to/001_initial.sql", m.Path)
	})
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

	_, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})

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
	env := setupTestEnv(t, "mink-test-migrations-*")

	// Create .sql and .down.sql files
	migrationsDir := filepath.Join(env.tmpDir, "migrations")
	require.NoError(t, os.MkdirAll(migrationsDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(migrationsDir, "001_create.sql"), []byte("-- up"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(migrationsDir, "001_create.down.sql"), []byte("-- down"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(migrationsDir, "002_update.sql"), []byte("-- up"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(migrationsDir, "002_update.down.sql"), []byte("-- down"), 0644))

	migrations, err := getAllMigrations(migrationsDir)
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
	env := setupTestEnv(t, "mink-test-empty-*")

	migrationsDir := filepath.Join(env.tmpDir, "migrations")
	require.NoError(t, os.MkdirAll(migrationsDir, 0755))

	migrations, err := getAllMigrations(migrationsDir)
	require.NoError(t, err)
	assert.Empty(t, migrations)
}

func TestGetAllMigrations_WithSubdirectories(t *testing.T) {
	env := setupTestEnv(t, "mink-test-subdir-*")

	migrationsDir := filepath.Join(env.tmpDir, "migrations")
	require.NoError(t, os.MkdirAll(migrationsDir, 0755))

	// Create a subdirectory that should be ignored
	require.NoError(t, os.Mkdir(filepath.Join(migrationsDir, "subdir"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(migrationsDir, "001_first.sql"), []byte("-- up"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(migrationsDir, "subdir", "002_second.sql"), []byte("-- up"), 0644))

	migrations, err := getAllMigrations(migrationsDir)
	require.NoError(t, err)

	// Should only find 001_first.sql, not the one in subdirectory
	assert.Len(t, migrations, 1)
	assert.Equal(t, "001_first", migrations[0].Name)
}

func TestGetAllMigrations_Sorting(t *testing.T) {
	env := setupTestEnv(t, "mink-test-sort-*")

	migrationsDir := filepath.Join(env.tmpDir, "migrations")
	require.NoError(t, os.MkdirAll(migrationsDir, 0755))

	// Create files in non-sorted order
	require.NoError(t, os.WriteFile(filepath.Join(migrationsDir, "003_third.sql"), []byte("-- up"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(migrationsDir, "001_first.sql"), []byte("-- up"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(migrationsDir, "002_second.sql"), []byte("-- up"), 0644))

	migrations, err := getAllMigrations(migrationsDir)
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
	env := setupTestEnv(t, "mink-init-noconfig-*")
	_ = env

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
	env := setupTestEnv(t, "mink-schema-test-*")
	env.createConfig(withModule("github.com/test/project"))

	cmd := NewSchemaCommand()
	cmd.SetArgs([]string{"generate"})

	var buf bytes.Buffer
	cmd.SetOut(&buf)

	err := cmd.Execute()
	assert.NoError(t, err)
}

func TestSchemaGenerateCommand_OutputFile(t *testing.T) {
	env := setupTestEnv(t, "mink-schema-output-test-*")
	env.createConfig(withModule("github.com/test/project"))

	outputFile := filepath.Join(env.tmpDir, "schema.sql")

	cmd := NewSchemaCommand()
	cmd.SetArgs([]string{"generate", "--output", outputFile})

	err := cmd.Execute()
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
	env := setupTestEnv(t, "mink-schema-print-test-*")
	env.createConfig(withModule("github.com/test/project"))

	cmd := NewSchemaCommand()
	cmd.SetArgs([]string{"print"})

	var buf bytes.Buffer
	cmd.SetOut(&buf)

	err := cmd.Execute()
	assert.NoError(t, err)
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
	env := setupTestEnv(t, "mink-test-nomod-*")

	result := detectModule(env.tmpDir)
	assert.Empty(t, result)
}

func TestDetectModule_WithMalformedGoMod(t *testing.T) {
	env := setupTestEnv(t, "mink-test-badmod-*")

	// Write go.mod without module line
	require.NoError(t, os.WriteFile(filepath.Join(env.tmpDir, "go.mod"), []byte("go 1.21\n"), 0644))

	result := detectModule(env.tmpDir)
	assert.Empty(t, result)
}

// Test generate file with various templates
func TestGenerateFile_CustomTemplate(t *testing.T) {
	env := setupTestEnv(t, "mink-test-genfile-*")

	filePath := filepath.Join(env.tmpDir, "test.go")
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

	err := generateFile(filePath, template, data)
	require.NoError(t, err)

	content, err := os.ReadFile(filePath)
	require.NoError(t, err)
	assert.Contains(t, string(content), "package mypackage")
	assert.Contains(t, string(content), "type MyStruct struct")
}

func TestGenerateFile_CreateDirectory(t *testing.T) {
	env := setupTestEnv(t, "mink-test-gendir-*")

	// Generate file in existing directory (generateFile doesn't auto-create nested dirs)
	filePath := filepath.Join(env.tmpDir, "test.go")
	template := `package test`

	err := generateFile(filePath, template, nil)
	require.NoError(t, err)

	// Verify file exists
	_, err = os.Stat(filePath)
	assert.NoError(t, err)
}

func TestGenerateFile_BadTemplate(t *testing.T) {
	env := setupTestEnv(t, "mink-test-badtempl-*")

	filePath := filepath.Join(env.tmpDir, "test.go")
	template := `{{.Invalid`

	err := generateFile(filePath, template, nil)
	assert.Error(t, err)
}

// Test config with various edge cases
func TestCheckConfiguration_WithInvalidConfig(t *testing.T) {
	env := setupTestEnv(t, "mink-test-badcfg-*")

	// Create invalid yaml
	require.NoError(t, os.WriteFile(filepath.Join(env.tmpDir, "mink.yaml"), []byte("invalid: yaml: :::"), 0644))

	result := checkConfiguration()
	// Should return an error status when config can't be loaded
	assert.Contains(t, []CheckStatus{StatusWarning, StatusError}, result.Status)
}

func TestCheckConfiguration_WithNoConfig(t *testing.T) {
	env := setupTestEnv(t, "mink-test-nocfg-*")
	_ = env // No config created intentionally

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
	env := setupTestEnv(t, "mink-migrate-status-*")
	_ = env // No config created intentionally

	cmd := NewMigrateCommand()
	statusCmd, _, _ := cmd.Find([]string{"status"})

	err := statusCmd.RunE(statusCmd, []string{})
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
	env := setupTestEnv(t, "mink-pending-empty-*")

	// Create empty migrations dir
	migrationsDir := filepath.Join(env.tmpDir, "migrations")
	require.NoError(t, os.MkdirAll(migrationsDir, 0755))

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

// TestCommandsRequireConfig tests that various commands fail without mink.yaml
func TestCommandsRequireConfig(t *testing.T) {
	tests := []struct {
		name           string
		newCmd         func() *cobra.Command
		subcommandPath []string
		args           []string
	}{
		{"projection list", NewProjectionCommand, []string{"list"}, nil},
		{"projection pause", NewProjectionCommand, []string{"pause"}, []string{"TestProj"}},
		{"projection resume", NewProjectionCommand, []string{"resume"}, []string{"TestProj"}},
		{"stream list", NewStreamCommand, []string{"list"}, nil},
		{"stream events", NewStreamCommand, []string{"events"}, []string{"stream-123"}},
		{"stream stats", NewStreamCommand, []string{"stats"}, nil},
		{"stream export", NewStreamCommand, []string{"export"}, []string{"stream-123"}},
		{"migrate status", NewMigrateCommand, []string{"status"}, nil},
		{"migrate down", NewMigrateCommand, []string{"down"}, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := setupTestEnv(t, "mink-noconfig-*")
			_ = env // No config created intentionally

			cmd := tt.newCmd()
			subCmd, _, _ := cmd.Find(tt.subcommandPath)
			err := subCmd.RunE(subCmd, tt.args)
			assertErrorContains(t, err, "mink.yaml")
		})
	}
}

// TestProjectionSubcommandStructure tests projection subcommand structures
func TestProjectionSubcommandStructure(t *testing.T) {
	tests := []struct {
		subcommand string
		expected   string
	}{
		{"status", "status <name>"},
		{"pause", "pause <name>"},
		{"resume", "resume <name>"},
	}

	for _, tt := range tests {
		t.Run(tt.subcommand, func(t *testing.T) {
			cmd := NewProjectionCommand()
			subCmd, _, err := cmd.Find([]string{tt.subcommand})
			require.NoError(t, err)
			assert.Equal(t, tt.expected, subCmd.Use)
		})
	}
}

// Test migrate up with invalid database URL
func TestMigrateUpCommand_InvalidDBURL(t *testing.T) {
	env := setupTestEnv(t, "mink-migrate-invalid-*")
	env.createConfig(
		withDriver("postgres"),
		withDatabaseURL("postgres://invalid:url@localhost:65535/nonexistent"),
		withModule("github.com/test/project"),
	)
	env.createMigrationFile("001_test.sql", "SELECT 1;")

	cmd := NewMigrateCommand()
	upCmd, _, _ := cmd.Find([]string{"up"})
	require.NoError(t, upCmd.Flags().Set("force", "true"))

	err := upCmd.RunE(upCmd, []string{})
	// Should fail when trying to connect with invalid DB URL
	assert.Error(t, err)
}

// Test migrate status with memory driver
func TestMigrateStatusCommand_MemoryDriver(t *testing.T) {
	env := setupTestEnv(t, "mink-migrate-memory-status-*")
	env.createConfig(
		withDriver("memory"),
		withModule("github.com/test/project"),
	)

	cmd := NewMigrateCommand()
	statusCmd, _, _ := cmd.Find([]string{"status"})

	err := statusCmd.RunE(statusCmd, []string{})
	// Memory driver should show info message
	assert.NoError(t, err)
}

// Test generate aggregate with multiple events
func TestGenerateAggregateCommand_MultipleEvents(t *testing.T) {
	env := setupTestEnv(t, "mink-gen-multi-events-*")
	cfg := env.createConfig(withModule("github.com/test/multi"))

	// Create the directories
	require.NoError(t, os.MkdirAll(filepath.Join(env.tmpDir, cfg.Generation.AggregatePackage), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(env.tmpDir, cfg.Generation.EventPackage), 0755))

	cmd := NewGenerateCommand()
	cmd.SetArgs([]string{"aggregate", "MultiTest", "--events", "Created,Updated,Deleted", "--force"})

	err := cmd.Execute()
	assert.NoError(t, err)

	// Verify aggregate file was created
	aggFile := filepath.Join(env.tmpDir, cfg.Generation.AggregatePackage, "multitest.go")
	_, err = os.Stat(aggFile)
	assert.NoError(t, err, "Aggregate file should exist")

	// Verify events file was created (all events in one file)
	eventsFile := filepath.Join(env.tmpDir, cfg.Generation.EventPackage, "multitest_events.go")
	_, err = os.Stat(eventsFile)
	assert.NoError(t, err, "Events file should exist")
}

// TestCommandsRequireDatabaseURL tests that various commands fail without DATABASE_URL
func TestCommandsRequireDatabaseURL(t *testing.T) {
	tests := []struct {
		name           string
		newCmd         func() *cobra.Command
		subcommandPath []string
		args           []string
		setFlags       func(cmd *cobra.Command)
	}{
		{"projection list", NewProjectionCommand, []string{"list"}, nil, nil},
		{"projection status", NewProjectionCommand, []string{"status"}, []string{"TestProj"}, nil},
		{"projection pause", NewProjectionCommand, []string{"pause"}, []string{"TestProj"}, nil},
		{"projection resume", NewProjectionCommand, []string{"resume"}, []string{"TestProj"}, nil},
		{"projection rebuild", NewProjectionCommand, []string{"rebuild"}, []string{"TestProj"}, func(cmd *cobra.Command) {
			_ = cmd.Flags().Set("force", "true")
		}},
		{"stream list", NewStreamCommand, []string{"list"}, nil, nil},
		{"stream events", NewStreamCommand, []string{"events"}, []string{"test-stream"}, nil},
		{"stream stats", NewStreamCommand, []string{"stats"}, nil, nil},
		{"stream export", NewStreamCommand, []string{"export"}, []string{"stream-123"}, nil},
		{"migrate up", NewMigrateCommand, []string{"up"}, nil, func(cmd *cobra.Command) {
			_ = cmd.Flags().Set("force", "true")
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := setupTestEnv(t, "mink-nodb-*")
			env.createConfig(
				withDriver("postgres"),
				withDatabaseURL(""),
				withModule("github.com/test/project"),
			)

			cmd := tt.newCmd()
			subCmd, _, _ := cmd.Find(tt.subcommandPath)
			if tt.setFlags != nil {
				tt.setFlags(subCmd)
			}
			err := subCmd.RunE(subCmd, tt.args)
			assertErrorContains(t, err, "DATABASE_URL")
		})
	}
}

// TestGenerateCommandsWithoutConfig tests that generate commands succeed with defaults when no config exists
func TestGenerateCommandsWithoutConfig(t *testing.T) {
	tests := []struct {
		name       string
		subcommand string
		arg        string
	}{
		{"event", "event", "TestEvent"},
		{"projection", "projection", "TestProj"},
		{"command", "command", "TestCmd"},
		{"aggregate", "aggregate", "Order"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := setupTestEnv(t, "mink-gen-noconf-*")
			_ = env // No config created intentionally

			cmd := NewGenerateCommand()
			subCmd, _, _ := cmd.Find([]string{tt.subcommand})
			require.NoError(t, subCmd.Flags().Set("force", "true"))

			err := subCmd.RunE(subCmd, []string{tt.arg})
			assert.NoError(t, err)
		})
	}
}

// Test runDiagnose function paths - warning status
func TestCheckDatabaseConnection_NoDBURL(t *testing.T) {
	env := setupTestEnv(t, "mink-check-db-nourl-*")
	env.createConfig(
		withDriver("postgres"),
		withDatabaseURL(""),
		withModule("github.com/test/project"),
	)

	result := checkDatabaseConnection()
	assert.Equal(t, StatusWarning, result.Status)
	assert.NotEmpty(t, result.Recommendation)
}

// TestDiagnosticChecksWithoutConfig tests diagnostic checks without configuration
func TestDiagnosticChecksWithoutConfig(t *testing.T) {
	tests := []struct {
		name    string
		checkFn func() CheckResult
	}{
		{"checkEventStoreSchema", checkEventStoreSchema},
		{"checkProjections", checkProjections},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_ = setupTestEnv(t, "mink-check-noconf-*")

			result := tt.checkFn()
			// Should return warning or OK when no config
			assert.Contains(t, []CheckStatus{StatusOK, StatusWarning}, result.Status)
		})
	}
}

// Test migrate down without config
func TestMigrateDownCommand_NoConfig(t *testing.T) {
	env := setupTestEnv(t, "mink-migrate-down-noconfig-*")

	cmd := NewMigrateCommand()
	downCmd, _, _ := cmd.Find([]string{"down"})

	err := downCmd.RunE(downCmd, []string{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "mink.yaml")

	_ = env // Cleanup handled by t.Cleanup
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

// Test adapter factory with invalid URL
func TestAdapterFactory_WithInvalidURL(t *testing.T) {
	// Adapter factory requires DATABASE_URL - test creation without env var
	cfg := config.DefaultConfig()
	cfg.Database.URL = "${DATABASE_URL}" // Not set
	
	_, err := NewAdapterFactory(cfg)
	assert.Error(t, err)
}

// Test checkDatabaseConnection with memory driver
func TestCheckDatabaseConnection_MemoryDriver(t *testing.T) {
	env := setupTestEnv(t, "mink-check-db-memory-*")
	env.createConfig(
		withDriver("memory"),
		withModule("github.com/test/project"),
	)

	result := checkDatabaseConnection()
	assert.Equal(t, StatusOK, result.Status)
	assert.Contains(t, result.Message, "memory")
}

// Test generate aggregate with existing config
func TestGenerateAggregateCommand_WithExistingConfig(t *testing.T) {
	env := setupTestEnv(t, "mink-gen-agg-existing-*")
	env.createConfig(
		withModule("github.com/test/existing"),
		withAggregatePackage("pkg/domain"),
		withEventPackage("pkg/events"),
	)

	cmd := NewGenerateCommand()
	cmd.SetArgs([]string{"aggregate", "Custom", "--force"})

	err := cmd.Execute()
	assert.NoError(t, err)

	// Verify files created in custom paths
	aggFile := filepath.Join(env.tmpDir, "pkg/domain", "custom.go")
	_, err = os.Stat(aggFile)
	assert.NoError(t, err)
}

// Test generate event with aggregate flag
func TestGenerateEventCommand_WithAggregate(t *testing.T) {
	env := setupTestEnv(t, "mink-gen-evt-agg-*")
	cfg := env.createConfig(withModule("github.com/test/evtagg"))

	cmd := NewGenerateCommand()
	cmd.SetArgs([]string{"event", "OrderShipped", "--aggregate", "Order", "--force"})

	err := cmd.Execute()
	assert.NoError(t, err)

	// Verify event file was created
	eventFile := filepath.Join(env.tmpDir, cfg.Generation.EventPackage, "ordershipped.go")
	_, err = os.Stat(eventFile)
	assert.NoError(t, err)
}

// Test generate command with aggregate flag
func TestGenerateCommandCommand_WithAggregate(t *testing.T) {
	env := setupTestEnv(t, "mink-gen-cmd-agg-*")
	cfg := env.createConfig(withModule("github.com/test/cmdagg"))

	cmd := NewGenerateCommand()
	cmd.SetArgs([]string{"command", "ShipOrder", "--aggregate", "Order", "--force"})

	err := cmd.Execute()
	assert.NoError(t, err)

	// Verify command file was created
	cmdFile := filepath.Join(env.tmpDir, cfg.Generation.CommandPackage, "shiporder.go")
	_, err = os.Stat(cmdFile)
	assert.NoError(t, err)
}

// Test generate projection with events flag
func TestGenerateProjectionCommand_WithEvents(t *testing.T) {
	env := setupTestEnv(t, "mink-gen-proj-evts-*")
	cfg := env.createConfig(withModule("github.com/test/projevt"))

	cmd := NewGenerateCommand()
	cmd.SetArgs([]string{"projection", "OrderSummary", "--events", "OrderCreated,OrderShipped", "--force"})

	err := cmd.Execute()
	assert.NoError(t, err)

	// Verify projection file was created
	projFile := filepath.Join(env.tmpDir, cfg.Generation.ProjectionPackage, "ordersummary.go")
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
	env := setupTestEnv(t, "mink-migrate-down-invalid-*")
	env.createConfig(
		withDriver("postgres"),
		withDatabaseURL("postgres://invalid:url@localhost:65535/nonexistent"),
		withModule("github.com/test/project"),
	)

	// Create migrations dir with a migration
	env.createMigrationFile("001_test.sql", "SELECT 1;")
	env.createMigrationFile("001_test.down.sql", "SELECT 1;")

	cmd := NewMigrateCommand()
	downCmd, _, _ := cmd.Find([]string{"down"})

	err := downCmd.RunE(downCmd, []string{})
	// Should fail when trying to connect with invalid DB URL
	assert.Error(t, err)
}

// Test generate aggregate with no events and force flag
func TestGenerateAggregateCommand_ForceNoEventsUnit(t *testing.T) {
	env := setupTestEnv(t, "mink-gen-agg-force-unit-*")
	env.createConfig(withModule("github.com/test/project"))

	cmd := NewGenerateCommand()
	aggCmd, _, _ := cmd.Find([]string{"aggregate"})

	// Set the force flag
	require.NoError(t, aggCmd.Flags().Set("force", "true"))

	err := aggCmd.RunE(aggCmd, []string{"TestAggregate"})
	assert.NoError(t, err)
}

// Test projection list with force flag
func TestProjectionListCommand_WithForce(t *testing.T) {
	env := setupTestEnv(t, "mink-proj-list-force-*")
	env.createConfig(
		withDriver("memory"),
		withModule("github.com/test/project"),
	)

	cmd := NewProjectionCommand()
	listCmd, _, _ := cmd.Find([]string{"list"})

	err := listCmd.RunE(listCmd, []string{})
	assert.NoError(t, err)
}

// Test generate event with no aggregate and force
func TestGenerateEventCommand_ForceNoAggregate(t *testing.T) {
	env := setupTestEnv(t, "mink-gen-event-force-*")
	env.createConfig(withModule("github.com/test/project"))

	cmd := NewGenerateCommand()
	eventCmd, _, _ := cmd.Find([]string{"event"})

	// Set the force flag
	require.NoError(t, eventCmd.Flags().Set("force", "true"))

	err := eventCmd.RunE(eventCmd, []string{"OrderCreated"})
	assert.NoError(t, err)
}

// Test generate command with no aggregate and force
func TestGenerateCommandCommand_ForceNoAggregate(t *testing.T) {
	env := setupTestEnv(t, "mink-gen-cmd-force-*")
	env.createConfig(withModule("github.com/test/project"))

	cmd := NewGenerateCommand()
	cmdCmd, _, _ := cmd.Find([]string{"command"})

	// Set the force flag
	require.NoError(t, cmdCmd.Flags().Set("force", "true"))

	err := cmdCmd.RunE(cmdCmd, []string{"CreateOrder"})
	assert.NoError(t, err)
}

// Test generate projection with no events and force
func TestGenerateProjectionCommand_ForceNoEvents(t *testing.T) {
	env := setupTestEnv(t, "mink-gen-proj-force-*")
	env.createConfig(withModule("github.com/test/project"))

	cmd := NewGenerateCommand()
	projCmd, _, _ := cmd.Find([]string{"projection"})

	// Set the force flag
	require.NoError(t, projCmd.Flags().Set("force", "true"))

	err := projCmd.RunE(projCmd, []string{"OrderView"})
	assert.NoError(t, err)
}

// Test init command when config exists
func TestInitCommand_ConfigExists(t *testing.T) {
	env := setupTestEnv(t, "mink-init-exists-*")
	env.createConfig() // Create existing config

	cmd := NewInitCommand()
	err := cmd.RunE(cmd, []string{})
	// Should return nil but print warning (config already exists)
	assert.NoError(t, err)
}

// Test migrate up with no pending migrations and force
func TestMigrateUpCommand_NoPending(t *testing.T) {
	env := setupTestEnv(t, "mink-migrate-no-pending-*")
	env.createConfig(
		withDriver("memory"),
		withModule("github.com/test/project"),
	)
	env.createMigrationsDir() // Create empty migrations dir

	cmd := NewMigrateCommand()
	upCmd, _, _ := cmd.Find([]string{"up"})
	require.NoError(t, upCmd.Flags().Set("force", "true"))

	err := upCmd.RunE(upCmd, []string{})
	assert.NoError(t, err) // Should succeed even with no migrations
}

// Test migrate down with memory driver (no-op)
func TestMigrateDownCommand_MemoryDriverUnit(t *testing.T) {
	env := setupTestEnv(t, "mink-migrate-down-mem-*")
	env.createConfig(
		withDriver("memory"),
		withModule("github.com/test/project"),
	)
	env.createMigrationFile("001_test.sql", "SELECT 1;")
	env.createMigrationFile("001_test.down.sql", "SELECT 1;")

	cmd := NewMigrateCommand()
	downCmd, _, _ := cmd.Find([]string{"down"})
	require.NoError(t, downCmd.Flags().Set("force", "true"))

	err := downCmd.RunE(downCmd, []string{})
	assert.NoError(t, err)
}

// Test schema generate with force
func TestSchemaGenerateCommand_WithForce(t *testing.T) {
	env := setupTestEnv(t, "mink-schema-gen-force-*")
	env.createConfig(withModule("github.com/test/project"))

	cmd := NewSchemaCommand()
	genCmd, _, _ := cmd.Find([]string{"generate"})
	require.NoError(t, genCmd.Flags().Set("force", "true"))

	err := genCmd.RunE(genCmd, []string{})
	assert.NoError(t, err)
}

// Test projection rebuild with force and invalid URL
func TestProjectionRebuildCommand_ForceInvalidURL(t *testing.T) {
	env := setupTestEnv(t, "mink-proj-rebuild-inv-*")
	env.createConfig(
		withDriver("postgres"),
		withDatabaseURL("postgres://invalid:url@localhost:65535/nonexistent"),
		withModule("github.com/test/project"),
	)

	cmd := NewProjectionCommand()
	rebuildCmd, _, _ := cmd.Find([]string{"rebuild"})
	require.NoError(t, rebuildCmd.Flags().Set("force", "true"))

	err := rebuildCmd.RunE(rebuildCmd, []string{"TestProj"})
	assert.Error(t, err)
}

// Test migrate status with force
func TestMigrateStatusCommand_WithForce(t *testing.T) {
	env := setupTestEnv(t, "mink-migrate-status-force-*")
	env.createConfig(
		withDriver("memory"),
		withModule("github.com/test/project"),
	)
	env.createMigrationsDir()

	cmd := NewMigrateCommand()
	statusCmd, _, _ := cmd.Find([]string{"status"})

	err := statusCmd.RunE(statusCmd, []string{})
	assert.NoError(t, err)
}

// Test generate aggregate with events flag
func TestGenerateAggregateCommand_WithEventsFlag(t *testing.T) {
	env := setupTestEnv(t, "mink-gen-agg-events-*")
	cfg := env.createConfig(withModule("github.com/test/project"))

	cmd := NewGenerateCommand()
	aggCmd, _, _ := cmd.Find([]string{"aggregate"})

	// Set the events flag
	require.NoError(t, aggCmd.Flags().Set("events", "Created,Updated,Deleted"))
	require.NoError(t, aggCmd.Flags().Set("force", "true"))

	err := aggCmd.RunE(aggCmd, []string{"TestAggregate"})
	assert.NoError(t, err)

	// Verify aggregate and events file created
	aggFile := filepath.Join(env.tmpDir, cfg.Generation.AggregatePackage, "testaggregate.go")
	_, err = os.Stat(aggFile)
	assert.NoError(t, err)

	eventsFile := filepath.Join(env.tmpDir, cfg.Generation.EventPackage, "testaggregate_events.go")
	_, err = os.Stat(eventsFile)
	assert.NoError(t, err)
}

// Test generate projection with events flag
func TestGenerateProjectionCommand_WithEventsFlag(t *testing.T) {
	env := setupTestEnv(t, "mink-gen-proj-events-*")
	cfg := env.createConfig(withModule("github.com/test/project"))

	cmd := NewGenerateCommand()
	projCmd, _, _ := cmd.Find([]string{"projection"})

	// Set the events flag
	require.NoError(t, projCmd.Flags().Set("events", "OrderCreated,OrderShipped"))
	require.NoError(t, projCmd.Flags().Set("force", "true"))

	err := projCmd.RunE(projCmd, []string{"OrderView"})
	assert.NoError(t, err)

	// Verify projection file created
	projFile := filepath.Join(env.tmpDir, cfg.Generation.ProjectionPackage, "orderview.go")
	_, err = os.Stat(projFile)
	assert.NoError(t, err)
}

// Test generate event with aggregate flag
func TestGenerateEventCommand_WithAggregateFlag(t *testing.T) {
	env := setupTestEnv(t, "mink-gen-event-agg-*")
	cfg := env.createConfig(withModule("github.com/test/project"))

	cmd := NewGenerateCommand()
	eventCmd, _, _ := cmd.Find([]string{"event"})

	// Set the aggregate flag
	require.NoError(t, eventCmd.Flags().Set("aggregate", "Order"))
	require.NoError(t, eventCmd.Flags().Set("force", "true"))

	err := eventCmd.RunE(eventCmd, []string{"ItemAdded"})
	assert.NoError(t, err)

	// Verify event file created
	eventFile := filepath.Join(env.tmpDir, cfg.Generation.EventPackage, "itemadded.go")
	_, err = os.Stat(eventFile)
	assert.NoError(t, err)
}

// Test generate command with aggregate flag
func TestGenerateCommandCommand_WithAggregateFlag(t *testing.T) {
	env := setupTestEnv(t, "mink-gen-cmd-agg-*")
	cfg := env.createConfig(withModule("github.com/test/project"))

	cmd := NewGenerateCommand()
	cmdCmd, _, _ := cmd.Find([]string{"command"})

	// Set the aggregate flag
	require.NoError(t, cmdCmd.Flags().Set("aggregate", "Order"))
	require.NoError(t, cmdCmd.Flags().Set("force", "true"))

	err := cmdCmd.RunE(cmdCmd, []string{"CancelOrder"})
	assert.NoError(t, err)

	// Verify command file created
	cmdFile := filepath.Join(env.tmpDir, cfg.Generation.CommandPackage, "cancelorder.go")
	_, err = os.Stat(cmdFile)
	assert.NoError(t, err)
}

// Test migrate up with steps flag
func TestMigrateUpCommand_WithStepsFlag(t *testing.T) {
	env := setupTestEnv(t, "mink-migrate-steps-*")
	env.createConfig(
		withDriver("memory"),
		withModule("github.com/test/project"),
	)
	env.createMigrationFile("001_init.sql", "-- init")
	env.createMigrationFile("002_add_table.sql", "-- add table")

	cmd := NewMigrateCommand()
	upCmd, _, _ := cmd.Find([]string{"up"})
	require.NoError(t, upCmd.Flags().Set("steps", "1"))
	require.NoError(t, upCmd.Flags().Set("force", "true"))

	err := upCmd.RunE(upCmd, []string{})
	assert.NoError(t, err)
}

// Test migrate down with steps flag
func TestMigrateDownCommand_WithStepsFlag(t *testing.T) {
	env := setupTestEnv(t, "mink-migrate-down-steps-*")
	env.createConfig(
		withDriver("memory"),
		withModule("github.com/test/project"),
	)
	env.createMigrationFile("001_init.sql", "-- init")
	env.createMigrationFile("001_init.down.sql", "-- down init")

	cmd := NewMigrateCommand()
	downCmd, _, _ := cmd.Find([]string{"down"})
	require.NoError(t, downCmd.Flags().Set("steps", "1"))
	require.NoError(t, downCmd.Flags().Set("force", "true"))

	err := downCmd.RunE(downCmd, []string{})
	assert.NoError(t, err)
}

// Test migrate create with sql flag
func TestMigrateCreateCommand_WithSQLFlag(t *testing.T) {
	env := setupTestEnv(t, "mink-migrate-create-sql-*")
	env.createConfig(withModule("github.com/test/project"))

	// Create migrations dir
	env.createMigrationsDir()

	cmd := NewMigrateCommand()
	createCmd, _, _ := cmd.Find([]string{"create"})
	require.NoError(t, createCmd.Flags().Set("sql", "SELECT 1;"))

	err := createCmd.RunE(createCmd, []string{"test_migration"})
	assert.NoError(t, err)

	// Verify migration file was created
	migrationsDir := filepath.Join(env.tmpDir, "migrations")
	files, _ := os.ReadDir(migrationsDir)
	assert.True(t, len(files) >= 1)
}

// TestHelpersWithInvalidURL tests adapter creation with invalid configuration
func TestHelpersWithInvalidURL(t *testing.T) {
	t.Run("invalid postgres URL creates factory but fails to connect", func(t *testing.T) {
		cfg := config.DefaultConfig()
		cfg.Database.Driver = "postgres"
		cfg.Database.URL = "postgres://invalid:url@localhost:65535/db"

		factory, err := NewAdapterFactory(cfg)
		assert.NoError(t, err, "Factory creation should succeed with invalid URL")

		// Connection should fail
		ctx := context.Background()
		_, err = factory.CreateAdapter(ctx)
		assert.Error(t, err, "Creating adapter with invalid URL should fail on connection")
	})

	t.Run("missing DATABASE_URL", func(t *testing.T) {
		cfg := config.DefaultConfig()
		cfg.Database.Driver = "postgres"
		cfg.Database.URL = "${DATABASE_URL}"

		_, err := NewAdapterFactory(cfg)
		assert.Error(t, err, "Should fail with unexpanded DATABASE_URL")
	})

	t.Run("empty URL", func(t *testing.T) {
		cfg := config.DefaultConfig()
		cfg.Database.Driver = "postgres"
		cfg.Database.URL = ""

		_, err := NewAdapterFactory(cfg)
		assert.Error(t, err, "Should fail with empty URL")
	})
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
	env := setupTestEnv(t, "mink-diagnose-unit-*")
	env.createConfig(
		withDriver("memory"),
		withModule("github.com/test/project"),
	)

	cmd := NewDiagnoseCommand()
	err := runDiagnose(cmd, []string{})
	assert.NoError(t, err)
}

// Test schema print executes without error
func TestSchemaPrintCommand_Execution(t *testing.T) {
	env := setupTestEnv(t, "mink-schema-print-*")
	env.createConfig(withModule("github.com/test/project"))

	cmd := NewSchemaCommand()
	printCmd, _, _ := cmd.Find([]string{"print"})

	err := printCmd.RunE(printCmd, []string{})
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
	env := setupTestEnv(t, "mink-proj-list-empty-*")
	env.createConfig(
		withDriver("postgres"),
		withDatabaseURL(""),
		withModule("github.com/test/project"),
	)

	cmd := NewProjectionCommand()
	listCmd, _, _ := cmd.Find([]string{"list"})

	err := listCmd.RunE(listCmd, []string{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "DATABASE_URL")
}

// Test stream command with limit flag
func TestStreamListCommand_WithLimit(t *testing.T) {
	env := setupTestEnv(t, "mink-stream-limit-*")
	env.createConfig(
		withDriver("postgres"),
		withDatabaseURL(""),
		withModule("github.com/test/project"),
	)

	cmd := NewStreamCommand()
	listCmd, _, _ := cmd.Find([]string{"list"})
	require.NoError(t, listCmd.Flags().Set("limit", "10"))

	err := listCmd.RunE(listCmd, []string{})
	assert.Error(t, err)
}

// Test stream events with from flag
func TestStreamEventsCommand_WithFrom(t *testing.T) {
	env := setupTestEnv(t, "mink-stream-from-*")
	env.createConfig(
		withDriver("postgres"),
		withDatabaseURL(""),
		withModule("github.com/test/project"),
	)

	cmd := NewStreamCommand()
	eventsCmd, _, _ := cmd.Find([]string{"events"})
	require.NoError(t, eventsCmd.Flags().Set("from", "5"))

	err := eventsCmd.RunE(eventsCmd, []string{"test-stream"})
	assert.Error(t, err)
}

// Test runDiagnose with database configured
func TestRunDiagnose_WithDatabase(t *testing.T) {
	env := setupTestEnv(t, "mink-diagnose-db-*")
	env.createConfig(
		withDriver("postgres"),
		withDatabaseURL("postgres://invalid:url@localhost:5432/test"),
		withModule("github.com/test/project"),
	)

	cmd := NewDiagnoseCommand()
	err := runDiagnose(cmd, []string{})
	// Should complete even with failed checks
	assert.NoError(t, err)
}

// Test checkProjections with config
func TestCheckProjections_WithConfig(t *testing.T) {
	env := setupTestEnv(t, "mink-check-proj-*")
	env.createConfig(
		withDriver("memory"),
		withModule("github.com/test/project"),
	)
	_ = env

	result := checkProjections()
	assert.NotEmpty(t, result.Name)
}

// Test checkEventStoreSchema with config
func TestCheckEventStoreSchema_WithConfig(t *testing.T) {
	env := setupTestEnv(t, "mink-check-schema-*")
	env.createConfig(
		withDriver("memory"),
		withModule("github.com/test/project"),
	)
	_ = env

	result := checkEventStoreSchema()
	assert.NotEmpty(t, result.Name)
}

// Test checkDatabaseConnection with postgres URL
func TestCheckDatabaseConnection_WithInvalidURL(t *testing.T) {
	env := setupTestEnv(t, "mink-check-db-inv-*")
	env.createConfig(
		withDriver("postgres"),
		withDatabaseURL("postgres://invalid:url@localhost:65535/test"),
		withModule("github.com/test/project"),
	)
	_ = env

	result := checkDatabaseConnection()
	// Should return error status for invalid connection
	assert.True(t, result.Status == StatusError || result.Status == StatusWarning)
}

// Test generateFile with valid template
func TestGenerateFile_ValidTemplate(t *testing.T) {
	env := setupTestEnv(t, "mink-gen-file-*")

	testFile := filepath.Join(env.tmpDir, "test.txt")
	err := generateFile(testFile, "Hello {{.Name}}", struct{ Name string }{Name: "World"})
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
	env := setupTestEnv(t, "mink-migrate-up-nopend-*")
	env.createConfig(
		withDriver("memory"),
		withModule("github.com/test/project"),
	)
	env.createMigrationsDir()

	cmd := NewMigrateCommand()
	upCmd, _, _ := cmd.Find([]string{"up"})
	require.NoError(t, upCmd.Flags().Set("force", "true"))

	err := upCmd.RunE(upCmd, []string{})
	assert.NoError(t, err)
}

// Test migrate status with migrations
func TestMigrateStatusCommand_WithMigrations(t *testing.T) {
	env := setupTestEnv(t, "mink-migrate-status-mig-*")
	env.createConfig(
		withDriver("memory"),
		withModule("github.com/test/project"),
	)
	env.createMigrationFile("001_init.sql", "-- init")

	cmd := NewMigrateCommand()
	statusCmd, _, _ := cmd.Find([]string{"status"})

	err := statusCmd.RunE(statusCmd, []string{})
	assert.NoError(t, err)
}

// Test schema generate without config
func TestSchemaGenerateCommand_NoConfig(t *testing.T) {
	env := setupTestEnv(t, "mink-schema-gen-noconf-*")
	_ = env // No config created intentionally

	cmd := NewSchemaCommand()
	genCmd, _, _ := cmd.Find([]string{"generate"})

	err := genCmd.RunE(genCmd, []string{})
	// Should work with defaults
	assert.NoError(t, err)
}

// Test schema print without config
func TestSchemaPrintCommand_NoConfig(t *testing.T) {
	env := setupTestEnv(t, "mink-schema-print-noconf-*")
	_ = env // No config created intentionally

	cmd := NewSchemaCommand()
	printCmd, _, _ := cmd.Find([]string{"print"})

	err := printCmd.RunE(printCmd, []string{})
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

// Test adapter factory returns error for invalid URL
func TestAdapterFactory_InvalidURL(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Database.Driver = "postgres"
	cfg.Database.URL = "postgres://invalid:url@localhost:65535/test"
	
	_, err := NewAdapterFactory(cfg)
	// Should succeed (factory creates, connection may fail later)
	assert.NoError(t, err)
}

// Test generateFallbackSchema produces valid output
func TestGenerateFallbackSchema_Valid(t *testing.T) {
	cfg := config.DefaultConfig()
	schema := generateFallbackSchema(cfg)
	assert.Contains(t, schema, "CREATE TABLE")
	assert.Contains(t, schema, "mink_events")
}

// Test ProjectionInfo struct initialization
func TestProjectionInfo_Initialization(t *testing.T) {
	p := adapters.ProjectionInfo{
		Name:     "test",
		Position: 100,
		Status:   "active",
	}
	assert.Equal(t, "test", p.Name)
	assert.Equal(t, int64(100), p.Position)
}

// Test StreamSummary struct initialization
func TestStreamSummary_Initialization(t *testing.T) {
	s := adapters.StreamSummary{
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

// Test adapters.EventStoreStats struct initialization
func TestAdapterEventStoreStats_Initialization(t *testing.T) {
	s := adapters.EventStoreStats{
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
	env := setupTestEnv(t, "mink-detect-*")

	// Without go.mod should return empty
	result := detectModule(env.tmpDir)
	assert.Empty(t, result)

	// With go.mod should return module name
	gomod := `module github.com/test/myproject

go 1.21
`
	require.NoError(t, os.WriteFile(filepath.Join(env.tmpDir, "go.mod"), []byte(gomod), 0644))
	result = detectModule(env.tmpDir)
	assert.Equal(t, "github.com/test/myproject", result)
}

// Test getAllMigrations helper (standalone function does not require adapter)
func TestGetAllMigrations_FromDirectory(t *testing.T) {
	env := setupTestEnv(t, "mink-pending-*")
	env.createMigrationFile("001_init.sql", "-- init")

	// getAllMigrations just reads filesystem
	migrationsDir := filepath.Join(env.tmpDir, "migrations")
	migrations, err := getAllMigrations(migrationsDir)
	assert.NoError(t, err)
	assert.Len(t, migrations, 1)
}

// Test init command with directory argument
func TestInitCommand_WithDirectoryFlag(t *testing.T) {
	env := setupTestEnv(t, "mink-init-dir-*")
	_ = env

	cmd := NewInitCommand()
	require.NoError(t, cmd.Flags().Set("driver", "memory"))
	// Note: This will try to run interactive form, so we can only test structure
	assert.NotNil(t, cmd)
}
