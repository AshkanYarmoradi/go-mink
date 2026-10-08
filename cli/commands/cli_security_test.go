package commands

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go-mink.dev/cli/config"
)

// ============================================================================
// Security regression tests for the CLI.
//
// The CLI trusts flags and environment variables, but not files on disk
// (mink.yaml) or rows in the database (the migrations table). These tests pin
// the validation and containment checks that keep hostile config values and
// poisoned migration rows from steering the CLI outside the project.
// ============================================================================

// withProjectionPackage sets the projection package path.
func withProjectionPackage(pkg string) configOption {
	return func(c *config.Config) {
		c.Generation.ProjectionPackage = pkg
	}
}

// withCommandPackage sets the command package path.
func withCommandPackage(pkg string) configOption {
	return func(c *config.Config) {
		c.Generation.CommandPackage = pkg
	}
}

// rollbackMockAdapter is a minimal CLIAdapter for the migrate-down unit tests.
// It embeds the CLIAdapter interface (nil) so only the migration methods need
// real implementations; any other call would panic, which the tested paths
// never trigger.
type rollbackMockAdapter struct {
	CLIAdapter
	applied    []string
	appliedErr error
	executed   []string
	removed    []string
	recorded   []string
}

func (a *rollbackMockAdapter) GetAppliedMigrations(ctx context.Context) ([]string, error) {
	if a.appliedErr != nil {
		return nil, a.appliedErr
	}
	return a.applied, nil
}

func (a *rollbackMockAdapter) ExecuteSQL(ctx context.Context, sql string) error {
	a.executed = append(a.executed, strings.TrimSpace(sql))
	return nil
}

func (a *rollbackMockAdapter) RemoveMigrationRecord(ctx context.Context, name string) error {
	a.removed = append(a.removed, name)
	return nil
}

func (a *rollbackMockAdapter) RecordMigration(ctx context.Context, name string) error {
	a.recorded = append(a.recorded, name)
	return nil
}

// newRollbackEnv builds a MigrationEnv over a fresh migrations directory
// containing up/down pairs for the given names.
func newRollbackEnv(t *testing.T, adapter CLIAdapter, names ...string) *MigrationEnv {
	t.Helper()
	migrationsDir := filepath.Join(t.TempDir(), "migrations")
	require.NoError(t, os.MkdirAll(migrationsDir, 0o755))
	for _, name := range names {
		require.NoError(t, os.WriteFile(filepath.Join(migrationsDir, name+".sql"),
			[]byte("CREATE TABLE "+name+" (id INT);"), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(migrationsDir, name+".down.sql"),
			[]byte("DROP TABLE "+name+";"), 0o644))
	}
	return &MigrationEnv{
		Adapter:       adapter,
		Config:        config.DefaultConfig(),
		Cwd:           filepath.Dir(migrationsDir),
		MigrationsDir: migrationsDir,
	}
}

// assertNoGoFiles fails if any .go file exists anywhere under dir.
func assertNoGoFiles(t *testing.T, dir string) {
	t.Helper()
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(path, ".go") {
			t.Errorf("no Go file should have been generated, found %s", path)
		}
		return nil
	})
}

// ============================================================================
// ensureInsideDir (paths.go)
// ============================================================================

func TestEnsureInsideDir(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()

	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{"root itself", root, false},
		{"direct child", filepath.Join(root, "migrations"), false},
		{"nested child", filepath.Join(root, "a", "b", "c.sql"), false},
		{"dot-dot that stays inside", filepath.Join(root, "a", "..", "b"), false},
		{"parent directory", filepath.Join(root, ".."), true},
		{"escape via dot-dot", filepath.Join(root, "..", "..", "etc", "passwd"), true},
		{"sibling sharing the root's name as a prefix", root + "-sibling", true},
		{"unrelated absolute path", other, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ensureInsideDir(root, tt.path)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "resolves outside")
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestEnsureInsideDir_RelativePathsResolveAgainstCwd(t *testing.T) {
	setupTestEnv(t, "mink-sec-inside-*")
	cwd, err := os.Getwd()
	require.NoError(t, err)

	assert.NoError(t, ensureInsideDir(cwd, "internal/domain"))
	assert.NoError(t, ensureInsideDir(".", filepath.Join("internal", "domain")))

	err = ensureInsideDir(cwd, filepath.Join("..", "escape"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolves outside")
}

func TestEnsureInsideDir_DifferentVolume(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("volume names only exist on Windows")
	}
	err := ensureInsideDir(`C:\project`, `D:\elsewhere\file.sql`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolves outside")
}

// ============================================================================
// Migration names read back from the database (migrate.go)
// ============================================================================

func TestValidateMigrationName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"plain stem", "001_init", false},
		{"dashes and dots", "002_add-users.v2", false},
		{"mixed case", "Add_Users-1.0", false},
		{"empty", "", true},
		{"leading dot-dot", "../001_init", true},
		{"forward slash", "001/init", true},
		{"backslash", `001\init`, true},
		{"only dot-dot", "..", true},
		{"embedded dot-dot", "a..b", true},
		{"space", "001 init", true},
		{"semicolon", "001;drop", true},
		{"newline", "001_init\n", true},
		{"absolute", "/etc/passwd", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateMigrationName(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "invalid migration name")
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestMigrationFilePath(t *testing.T) {
	dir := t.TempDir()

	t.Run("valid name maps inside the migrations dir", func(t *testing.T) {
		path, err := migrationFilePath(dir, "001_init")
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(dir, "001_init.sql"), path)
	})

	t.Run("traversal is rejected before any path is built", func(t *testing.T) {
		for _, bad := range []string{"../../evil", "sub/dir", "..", `..\evil`} {
			path, err := migrationFilePath(dir, bad)
			require.Error(t, err, "name %q", bad)
			assert.Contains(t, err.Error(), "invalid migration name")
			assert.Empty(t, path)
		}
	})
}

func TestGetAppliedMigrations_RejectsPoisonedRow(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	t.Run("valid rows map to files inside the migrations dir", func(t *testing.T) {
		adapter := &rollbackMockAdapter{applied: []string{"001_a", "002_b"}}
		got, err := getAppliedMigrations(ctx, adapter, dir)
		require.NoError(t, err)
		require.Len(t, got, 2)
		assert.Equal(t, "001_a", got[0].Name)
		assert.Equal(t, filepath.Join(dir, "001_a.sql"), got[0].Path)
		assert.Equal(t, filepath.Join(dir, "002_b.sql"), got[1].Path)
	})

	t.Run("one poisoned row fails the whole read", func(t *testing.T) {
		adapter := &rollbackMockAdapter{applied: []string{"001_a", "../../evil"}}
		got, err := getAppliedMigrations(ctx, adapter, dir)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `invalid migration name "../../evil"`)
		assert.Nil(t, got)
	})

	t.Run("read error is returned", func(t *testing.T) {
		sentinel := errors.New("connection reset")
		adapter := &rollbackMockAdapter{appliedErr: sentinel}
		_, err := getAppliedMigrations(ctx, adapter, dir)
		assert.ErrorIs(t, err, sentinel)
		assert.Contains(t, err.Error(), "failed to read applied migrations", "the wrapper the docs promise for migrate down")
	})
}

func TestGetPendingMigrations_ReturnsReadError(t *testing.T) {
	ctx := context.Background()
	sentinel := errors.New("migrations table unreachable")
	adapter := &rollbackMockAdapter{appliedErr: sentinel}
	env := newRollbackEnv(t, adapter, "001_a", "002_b")

	pending, err := getPendingMigrations(ctx, adapter, env.MigrationsDir)
	require.Error(t, err, "a failed read must not be treated as 'nothing applied'")
	assert.ErrorIs(t, err, sentinel)
	assert.Contains(t, err.Error(), "failed to read applied migrations")
	assert.Nil(t, pending)
}

func TestRunMigrateUp_ReadErrorAppliesNothing(t *testing.T) {
	ctx := context.Background()
	sentinel := errors.New("migrations table unreachable")
	adapter := &rollbackMockAdapter{appliedErr: sentinel}
	env := newRollbackEnv(t, adapter, "001_a", "002_b")

	err := runMigrateUp(ctx, env, 0)
	require.Error(t, err)
	assert.ErrorIs(t, err, sentinel)
	assert.Empty(t, adapter.executed, "no SQL may run when the applied set is unknown")
	assert.Empty(t, adapter.recorded)
}

// ============================================================================
// migrate down confirmation (migrate.go)
// ============================================================================

func TestResolveRollbackConfirmer(t *testing.T) {
	confirm := rollbackConfirmer(func([]string) (bool, error) { return true, nil })

	tests := []struct {
		name           string
		yes            bool
		nonInteractive bool
		wantPrompt     bool
	}{
		{"no flags: prompt", false, false, true},
		{"--yes waives the prompt", true, false, false},
		{"--non-interactive waives the prompt", false, true, false},
		{"both flags waive the prompt", true, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveRollbackConfirmer(confirm, tt.yes, tt.nonInteractive)
			if tt.wantPrompt {
				assert.NotNil(t, got)
			} else {
				assert.Nil(t, got)
			}
		})
	}
}

func TestMigrateDownCommand_ConfirmationFlags(t *testing.T) {
	cmd := newMigrateDownCommand()

	yes := cmd.Flags().Lookup("yes")
	require.NotNil(t, yes, "--yes must exist so scripts can skip the prompt")
	assert.Equal(t, "y", yes.Shorthand)
	assert.Equal(t, "false", yes.DefValue)

	ni := cmd.Flags().Lookup("non-interactive")
	require.NotNil(t, ni, "--non-interactive is still accepted")
	assert.Equal(t, "false", ni.DefValue)
	assert.Contains(t, cmd.Long, "confirmation")
}

func TestRunMigrateDown_DeclinedConfirmationRunsNothing(t *testing.T) {
	ctx := context.Background()
	adapter := &rollbackMockAdapter{applied: []string{"001_a", "002_b"}}
	env := newRollbackEnv(t, adapter, "001_a", "002_b")

	var asked []string
	err := runMigrateDown(ctx, env, 1, func(names []string) (bool, error) {
		asked = names
		return false, nil
	})
	require.NoError(t, err, "declining is not an error")
	assert.Equal(t, []string{"002_b"}, asked, "only the migrations about to be rolled back are listed")
	assert.Empty(t, adapter.executed, "no down SQL may run after a declined confirmation")
	assert.Empty(t, adapter.removed)
}

func TestRunMigrateDown_ConfirmationListsMostRecentFirst(t *testing.T) {
	ctx := context.Background()
	adapter := &rollbackMockAdapter{applied: []string{"001_a", "002_b", "003_c"}}
	env := newRollbackEnv(t, adapter, "001_a", "002_b", "003_c")

	var asked []string
	err := runMigrateDown(ctx, env, 2, func(names []string) (bool, error) {
		asked = names
		return false, nil
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"003_c", "002_b"}, asked)
}

func TestRunMigrateDown_AcceptedConfirmationRollsBack(t *testing.T) {
	ctx := context.Background()
	adapter := &rollbackMockAdapter{applied: []string{"001_a", "002_b"}}
	env := newRollbackEnv(t, adapter, "001_a", "002_b")

	err := runMigrateDown(ctx, env, 1, func([]string) (bool, error) { return true, nil })
	require.NoError(t, err)
	assert.Equal(t, []string{"DROP TABLE 002_b;"}, adapter.executed)
	assert.Equal(t, []string{"002_b"}, adapter.removed)
}

func TestRunMigrateDown_WaivedConfirmationRollsBack(t *testing.T) {
	ctx := context.Background()
	adapter := &rollbackMockAdapter{applied: []string{"001_a", "002_b"}}
	env := newRollbackEnv(t, adapter, "001_a", "002_b")

	// nil confirmer == --yes / --non-interactive
	err := runMigrateDown(ctx, env, 2, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"DROP TABLE 002_b;", "DROP TABLE 001_a;"}, adapter.executed)
	assert.Equal(t, []string{"002_b", "001_a"}, adapter.removed)
}

func TestRunMigrateDown_ConfirmationErrorIsReported(t *testing.T) {
	ctx := context.Background()
	adapter := &rollbackMockAdapter{applied: []string{"001_a"}}
	env := newRollbackEnv(t, adapter, "001_a")

	promptErr := errors.New("no tty")
	err := runMigrateDown(ctx, env, 1, func([]string) (bool, error) { return false, promptErr })
	require.Error(t, err)
	assert.ErrorIs(t, err, promptErr)
	assert.Contains(t, err.Error(), "--yes", "the error must tell scripts how to waive the prompt")
	assert.Empty(t, adapter.executed)
}

func TestRunMigrateDown_StepsBelowOneRollsBackOne(t *testing.T) {
	ctx := context.Background()
	adapter := &rollbackMockAdapter{applied: []string{"001_a", "002_b"}}
	env := newRollbackEnv(t, adapter, "001_a", "002_b")

	for _, steps := range []int{0, -3} {
		adapter.executed, adapter.removed = nil, nil
		require.NoError(t, runMigrateDown(ctx, env, steps, nil), "steps=%d", steps)
		assert.Equal(t, []string{"DROP TABLE 002_b;"}, adapter.executed, "steps=%d", steps)
	}
}

func TestRunMigrateDown_NothingAppliedSkipsPrompt(t *testing.T) {
	ctx := context.Background()
	adapter := &rollbackMockAdapter{}
	env := newRollbackEnv(t, adapter, "001_a")

	err := runMigrateDown(ctx, env, 1, func([]string) (bool, error) {
		t.Fatal("the prompt must not be shown when there is nothing to roll back")
		return false, nil
	})
	require.NoError(t, err)
	assert.Empty(t, adapter.executed)
}

func TestRunMigrateDown_MissingDownFileIsSkipped(t *testing.T) {
	ctx := context.Background()
	adapter := &rollbackMockAdapter{applied: []string{"001_a"}}
	env := newRollbackEnv(t, adapter, "001_a")
	require.NoError(t, os.Remove(filepath.Join(env.MigrationsDir, "001_a.down.sql")))

	require.NoError(t, runMigrateDown(ctx, env, 1, nil))
	assert.Empty(t, adapter.executed)
	assert.Empty(t, adapter.removed)
}

func TestRunMigrateDown_PoisonedRowIsRefusedBeforePrompt(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name string
		row  string
	}{
		{"dot-dot traversal", "../../etc/evil"},
		{"subdirectory", "sub/001_a"},
		{"shell-ish", "001_a; rm -rf"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adapter := &rollbackMockAdapter{applied: []string{"001_a", tt.row}}
			env := newRollbackEnv(t, adapter, "001_a")

			err := runMigrateDown(ctx, env, 1, func([]string) (bool, error) {
				t.Fatal("a poisoned migrations table must be refused before confirmation")
				return true, nil
			})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "invalid migration name")
			assert.Empty(t, adapter.executed, "no SQL may run from a poisoned row")
			assert.Empty(t, adapter.removed)
		})
	}
}

func TestRunMigrateDown_ReadErrorIsReturned(t *testing.T) {
	ctx := context.Background()
	sentinel := errors.New("migrations table unreachable")
	adapter := &rollbackMockAdapter{appliedErr: sentinel}
	env := newRollbackEnv(t, adapter, "001_a")

	err := runMigrateDown(ctx, env, 1, nil)
	assert.ErrorIs(t, err, sentinel)
	assert.Empty(t, adapter.executed)
}

func TestMigrateDownCommand_MemoryDriver_WithYes(t *testing.T) {
	env := setupTestEnv(t, "mink-sec-down-memory-*")
	env.createConfig(withDriver("memory"))

	err := executeCmd(NewMigrateCommand(), []string{"down", "--yes"})
	assert.NoError(t, err)
}

// ============================================================================
// Code generation (generate.go)
// ============================================================================

func TestPackageNameFromPath(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		want    string
		wantErr bool
	}{
		{"simple", "internal/domain", "domain", false},
		{"single element", "events", "events", false},
		{"underscore and digits", "internal/read_models2", "read_models2", false},
		{"trailing separator", "internal/domain/", "domain", false},
		{"uppercase", "internal/Domain", "", true},
		{"dash", "internal/my-domain", "", true},
		{"leading digit", "internal/1domain", "", true},
		{"space", "internal/dom ain", "", true},
		{"injected source", "internal/domain; import \"os\"", "", true},
		{"newline", "internal/domain\nfunc init() {}", "", true},
		{"empty", "", "", true},
		{"dot", ".", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := packageNameFromPath("aggregate_package", tt.path)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "generation.aggregate_package")
				assert.Contains(t, err.Error(), "valid Go package name")
				assert.Empty(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestValidateIdentifier(t *testing.T) {
	tests := []struct {
		name    string
		ident   string
		wantErr bool
	}{
		{"pascal", "OrderCreated", false},
		{"underscore prefix", "_private", false},
		{"digits", "Order2", false},
		{"empty", "", true},
		{"leading digit", "2Order", true},
		{"dot", "Order.Created", true},
		{"slash", "Order/Created", true},
		{"space", "Order Created", true},
		{"dash", "Order-Created", true},
		{"parens", "Evil()", true},
		{"semicolon", "Order;x", true},
		{"newline", "Order\n", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateIdentifier("event", tt.ident)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "invalid event name")
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestCheckOutputDir(t *testing.T) {
	setupTestEnv(t, "mink-sec-outdir-*")
	root, err := os.Getwd()
	require.NoError(t, err)

	assert.NoError(t, checkOutputDir(root, "aggregate_package", "internal/domain"))
	assert.NoError(t, checkOutputDir(root, "aggregate_package", "."))

	for _, bad := range []string{"../outside", filepath.Join(root, "..", "outside"), t.TempDir()} {
		err := checkOutputDir(root, "aggregate_package", bad)
		require.Error(t, err, "dir %q", bad)
		assert.Contains(t, err.Error(), "generation.aggregate_package")
		assert.Contains(t, err.Error(), "resolves outside")
	}
}

func TestLoadConfigOrDefaultWithRoot(t *testing.T) {
	sameDir := func(t *testing.T, want, got string) {
		t.Helper()
		w, err := filepath.EvalSymlinks(want)
		require.NoError(t, err)
		g, err := filepath.EvalSymlinks(got)
		require.NoError(t, err)
		assert.Equal(t, w, g)
	}

	t.Run("no config: defaults rooted at cwd", func(t *testing.T) {
		setupTestEnv(t, "mink-sec-root-none-*")
		cfg, root, cwd, err := loadConfigOrDefaultWithRoot()
		require.NoError(t, err)
		assert.Equal(t, cwd, root)
		assert.Equal(t, config.DefaultConfig().Project.Name, cfg.Project.Name)
	})

	t.Run("config in an ancestor without go.mod: root is the config dir", func(t *testing.T) {
		env := setupTestEnv(t, "mink-sec-root-ancestor-*")
		env.createConfig(withProjectName("ancestor-project"))
		nested := filepath.Join(env.tmpDir, "svc", "api")
		require.NoError(t, os.MkdirAll(nested, 0o755))
		require.NoError(t, os.Chdir(nested))

		cfg, root, cwd, err := loadConfigOrDefaultWithRoot()
		require.NoError(t, err)
		assert.Equal(t, "ancestor-project", cfg.Project.Name)
		sameDir(t, env.tmpDir, root)
		sameDir(t, nested, cwd)
	})

	t.Run("config above go.mod is ignored", func(t *testing.T) {
		env := setupTestEnv(t, "mink-sec-root-gomod-*")
		env.createConfig(withProjectName("planted-above-module"))
		module := filepath.Join(env.tmpDir, "module")
		require.NoError(t, os.MkdirAll(module, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(module, "go.mod"), []byte("module example.com/app\n\ngo 1.26\n"), 0o644))
		require.NoError(t, os.Chdir(module))

		cfg, root, cwd, err := loadConfigOrDefaultWithRoot()
		require.NoError(t, err)
		assert.NotEqual(t, "planted-above-module", cfg.Project.Name, "a mink.yaml above the module boundary must not be used")
		assert.Equal(t, cwd, root)
	})
}

func TestGenerate_OutputDirOutsideProjectRoot_Rejected(t *testing.T) {
	tests := []struct {
		name    string
		opt     func(dir string) configOption
		setting string
		args    []string
	}{
		{"aggregate_package", withAggregatePackage, "aggregate_package",
			[]string{"aggregate", "Order", "--non-interactive"}},
		{"event_package via aggregate --events", withEventPackage, "event_package",
			[]string{"aggregate", "Order", "--events", "Created", "--non-interactive"}},
		{"event_package via event", withEventPackage, "event_package",
			[]string{"event", "OrderCreated", "--aggregate", "Order", "--non-interactive"}},
		{"projection_package", withProjectionPackage, "projection_package",
			[]string{"projection", "OrderSummary", "--events", "OrderCreated", "--non-interactive"}},
		{"command_package", withCommandPackage, "command_package",
			[]string{"command", "CreateOrder", "--aggregate", "Order", "--non-interactive"}},
	}
	for _, tt := range tests {
		t.Run(tt.name+" relative escape", func(t *testing.T) {
			env := setupTestEnv(t, "mink-sec-gen-escape-*")
			// A sibling of the project directory, named uniquely so a stray
			// write would be attributable to this test.
			escapeName := filepath.Base(env.tmpDir) + "-escape"
			env.createConfig(withModule("github.com/test/project"), withDriver("memory"),
				tt.opt(filepath.Join("..", escapeName)))

			err := executeCmd(NewGenerateCommand(), tt.args)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "generation."+tt.setting)
			assert.Contains(t, err.Error(), "resolves outside")

			_, statErr := os.Stat(filepath.Join(env.tmpDir, "..", escapeName))
			assert.True(t, os.IsNotExist(statErr), "nothing may be written outside the project root")
			assertNoGoFiles(t, env.tmpDir)
		})

		t.Run(tt.name+" absolute path", func(t *testing.T) {
			env := setupTestEnv(t, "mink-sec-gen-abs-*")
			elsewhere := filepath.Join(t.TempDir(), "domain")
			env.createConfig(withModule("github.com/test/project"), withDriver("memory"), tt.opt(elsewhere))

			err := executeCmd(NewGenerateCommand(), tt.args)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "generation."+tt.setting)

			_, statErr := os.Stat(elsewhere)
			assert.True(t, os.IsNotExist(statErr), "nothing may be written to an absolute path outside the project")
			assertNoGoFiles(t, env.tmpDir)
		})
	}
}

func TestGenerate_InvalidPackageName_Rejected(t *testing.T) {
	tests := []struct {
		name    string
		opt     func(dir string) configOption
		setting string
		args    []string
	}{
		{"aggregate_package", withAggregatePackage, "aggregate_package",
			[]string{"aggregate", "Order", "--non-interactive"}},
		{"event_package via aggregate --events", withEventPackage, "event_package",
			[]string{"aggregate", "Order", "--events", "Created", "--non-interactive"}},
		{"event_package via event", withEventPackage, "event_package",
			[]string{"event", "OrderCreated", "--aggregate", "Order", "--non-interactive"}},
		{"projection_package", withProjectionPackage, "projection_package",
			[]string{"projection", "OrderSummary", "--non-interactive"}},
		{"command_package", withCommandPackage, "command_package",
			[]string{"command", "CreateOrder", "--aggregate", "Order", "--non-interactive"}},
	}
	badNames := []string{"internal/Domain", "internal/my-domain", "internal/1domain", "internal/domain; import \"os\""}

	for _, tt := range tests {
		for _, bad := range badNames {
			t.Run(tt.name+" "+bad, func(t *testing.T) {
				env := setupTestEnv(t, "mink-sec-gen-pkg-*")
				env.createConfig(withModule("github.com/test/project"), withDriver("memory"), tt.opt(bad))

				err := executeCmd(NewGenerateCommand(), tt.args)
				require.Error(t, err)
				assert.Contains(t, err.Error(), "generation."+tt.setting)
				assert.Contains(t, err.Error(), "valid Go package name")
				assertNoGoFiles(t, env.tmpDir)
			})
		}
	}
}

func TestGenerate_InvalidIdentifier_Rejected(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"aggregate name", []string{"aggregate", "Bad.Name", "--non-interactive"}, "invalid aggregate name"},
		{"aggregate event", []string{"aggregate", "Order", "--events", "Created,Bad.Event", "--non-interactive"}, "invalid event name"},
		{"event name", []string{"event", "Evil()", "--aggregate", "Order", "--non-interactive"}, "invalid event name"},
		{"event aggregate", []string{"event", "OrderCreated", "--aggregate", "Bad/Agg", "--non-interactive"}, "invalid aggregate name"},
		{"command name", []string{"command", "Create;Order", "--aggregate", "Order", "--non-interactive"}, "invalid command name"},
		{"command aggregate", []string{"command", "CreateOrder", "--aggregate", "Bad.Agg", "--non-interactive"}, "invalid aggregate name"},
		{"projection name", []string{"projection", "Order.Summary", "--non-interactive"}, "invalid projection name"},
		{"projection event", []string{"projection", "OrderSummary", "--events", "OrderCreated,Bad.Event", "--non-interactive"}, "invalid event name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := setupTestEnv(t, "mink-sec-gen-ident-*")
			env.createConfig(withModule("github.com/test/project"), withDriver("memory"))

			err := executeCmd(NewGenerateCommand(), tt.args)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
			assertNoGoFiles(t, env.tmpDir)
		})
	}
}

func TestGenerate_KebabAndSnakeNamesStillAccepted(t *testing.T) {
	env := setupTestEnv(t, "mink-sec-gen-ok-*")
	env.createConfig(withModule("github.com/test/project"), withDriver("memory"),
		withAggregatePackage("internal/domain"), withEventPackage("internal/events"))

	err := executeCmd(NewGenerateCommand(), []string{"aggregate", "order_item", "--events", "item-added,item removed", "--non-interactive"})
	require.NoError(t, err)

	agg, err := os.ReadFile(filepath.Join(env.tmpDir, "internal", "domain", "order_item.go"))
	require.NoError(t, err)
	assert.Contains(t, string(agg), "package domain")
	assert.Contains(t, string(agg), "type OrderItem struct")

	events, err := os.ReadFile(filepath.Join(env.tmpDir, "internal", "events", "order_item_events.go"))
	require.NoError(t, err)
	assert.Contains(t, string(events), "package events")
	assert.Contains(t, string(events), "type ItemAdded struct")
	assert.Contains(t, string(events), "type ItemRemoved struct")
}

// ============================================================================
// File permissions (init.go, projection.go)
// ============================================================================

func TestInitCommand_ConfigFileIsOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not enforced on Windows")
	}
	env := setupTestEnv(t, "mink-sec-init-perm-*")

	err := executeCmd(NewInitCommand(), []string{
		env.tmpDir, "--non-interactive",
		"--name", "perm-app", "--module", "github.com/test/perm", "--driver", "memory",
	})
	require.NoError(t, err)

	info, err := os.Stat(filepath.Join(env.tmpDir, config.ConfigFileName))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "mink.yaml may hold database credentials")

	// .gitkeep placeholders hold nothing sensitive and keep the default mode.
	defaults := config.DefaultConfig()
	for _, d := range []string{
		defaults.Database.MigrationsDir,
		defaults.Generation.AggregatePackage,
		defaults.Generation.EventPackage,
		defaults.Generation.ProjectionPackage,
		defaults.Generation.CommandPackage,
	} {
		info, err := os.Stat(filepath.Join(env.tmpDir, d, ".gitkeep"))
		require.NoError(t, err, ".gitkeep in %s", d)
		assert.Equal(t, os.FileMode(0o644), info.Mode().Perm(), ".gitkeep in %s", d)
	}
}

func TestStreamExport_WritesOwnerOnlyFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not enforced on Windows")
	}
	env := setupTestEnv(t, "mink-sec-export-perm-*")
	env.createConfig(withDriver("memory"))
	out := filepath.Join(env.tmpDir, "export.json")

	err := executeCmd(NewStreamCommand(), []string{"export", "test-stream", "--output", out})
	require.NoError(t, err)

	info, err := os.Stat(out)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "exported event payloads may contain PII")
}
