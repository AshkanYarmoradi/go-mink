package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// lockDir removes every permission bit from dir so that nothing inside it can
// be stat'ed, restoring them at cleanup so t.TempDir can remove it. It skips
// the test where permission bits are not enforced: Windows, and root on Unix.
func lockDir(t *testing.T, dir string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not enforced on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permission checks")
	}
	require.NoError(t, os.Chmod(dir, 0))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
}

// plantAncestorConfig writes a mink.yaml in dir that a fail-open search would
// pick up from any descendant.
func plantAncestorConfig(t *testing.T, dir, name string) {
	t.Helper()
	planted := DefaultConfig()
	planted.Project.Name = name
	planted.Database.URL = "postgres://attacker@evil.example/db"
	require.NoError(t, planted.Save(dir))
}

// assertSearchAborted asserts that FindConfig failed closed on a permission
// error rather than reporting "no config" or loading an ancestor.
func assertSearchAborted(t *testing.T, dir string, cfg *Config, err error) {
	t.Helper()
	require.Error(t, err, "an unreadable path on the way up must abort the search")
	assert.ErrorIs(t, err, os.ErrPermission)
	assert.NotErrorIs(t, err, os.ErrNotExist, "a permission failure is not a missing config")
	assert.Contains(t, err.Error(), "config search aborted")
	assert.Empty(t, dir)
	assert.Nil(t, cfg)
}

func TestFindConfig_UnreadableDirectoryFailsClosed(t *testing.T) {
	tmpDir := t.TempDir()
	plantAncestorConfig(t, tmpDir, "planted-above-unreadable-module")

	// The module root (holding the go.mod that should bound the search) is
	// made unreadable; the working directory is nested inside it.
	moduleRoot := filepath.Join(tmpDir, "module")
	nested := filepath.Join(moduleRoot, "internal")
	writeGoMod(t, moduleRoot)
	require.NoError(t, os.MkdirAll(nested, 0o755))
	lockDir(t, moduleRoot)

	dir, cfg, err := FindConfig(nested)
	assertSearchAborted(t, dir, cfg, err)
}

func TestFindConfig_UnreadableGoModFailsClosed(t *testing.T) {
	// Exercises the go.mod probe alone: mink.yaml is genuinely absent in the
	// project, while go.mod is a link into a directory that cannot be read.
	tmpDir := t.TempDir()
	plantAncestorConfig(t, tmpDir, "planted-above-unreadable-gomod")

	locked := filepath.Join(tmpDir, "locked")
	writeGoMod(t, locked)
	project := filepath.Join(tmpDir, "project")
	require.NoError(t, os.MkdirAll(project, 0o755))
	symlinkOrSkip(t, filepath.Join(locked, goModFileName), filepath.Join(project, goModFileName))
	lockDir(t, locked)

	dir, cfg, err := FindConfig(project)
	assertSearchAborted(t, dir, cfg, err)
}

func TestFindConfig_UnreadableConfigFailsClosed(t *testing.T) {
	// Exercises the mink.yaml probe alone: the project's mink.yaml is a link
	// into a directory that cannot be read, so the search must stop instead
	// of walking up to the planted ancestor.
	tmpDir := t.TempDir()
	plantAncestorConfig(t, tmpDir, "planted-above-unreadable-config")

	locked := filepath.Join(tmpDir, "locked")
	require.NoError(t, os.MkdirAll(locked, 0o755))
	real := DefaultConfig()
	real.Project.Name = "real-project"
	require.NoError(t, real.Save(locked))
	project := filepath.Join(tmpDir, "project")
	require.NoError(t, os.MkdirAll(project, 0o755))
	symlinkOrSkip(t, filepath.Join(locked, ConfigFileName), filepath.Join(project, ConfigFileName))
	lockDir(t, locked)

	dir, cfg, err := FindConfig(project)
	assertSearchAborted(t, dir, cfg, err)
}

func TestFindConfig_DirectoryNamedGoModIsNotABoundary(t *testing.T) {
	// A directory named go.mod is not a module file (cmd/go ignores it too),
	// so it does not bound the search and the ancestor config is reachable.
	tmpDir := t.TempDir()
	plantAncestorConfig(t, tmpDir, "ancestor-project")

	moduleRoot := filepath.Join(tmpDir, "module")
	nested := filepath.Join(moduleRoot, "internal")
	require.NoError(t, os.MkdirAll(filepath.Join(moduleRoot, goModFileName), 0o755))
	require.NoError(t, os.MkdirAll(nested, 0o755))

	foundDir, cfg, err := FindConfig(nested)
	require.NoError(t, err, "a directory named go.mod must be treated as absent")
	assert.Equal(t, tmpDir, foundDir)
	assert.Equal(t, "ancestor-project", cfg.Project.Name)
}

func TestFindConfig_DanglingGoModLinkIsNotABoundary(t *testing.T) {
	// A link to a go.mod that does not exist resolves to "does not exist",
	// which is the one Stat failure that means absent.
	tmpDir := t.TempDir()
	plantAncestorConfig(t, tmpDir, "ancestor-project")

	project := filepath.Join(tmpDir, "project")
	require.NoError(t, os.MkdirAll(project, 0o755))
	symlinkOrSkip(t, filepath.Join(tmpDir, "nowhere", goModFileName), filepath.Join(project, goModFileName))

	foundDir, cfg, err := FindConfig(project)
	require.NoError(t, err)
	assert.Equal(t, tmpDir, foundDir)
	assert.Equal(t, "ancestor-project", cfg.Project.Name)
}

func TestLookup_DistinguishesAbsentFromUnreadable(t *testing.T) {
	tmpDir := t.TempDir()
	present := filepath.Join(tmpDir, "present.txt")
	require.NoError(t, os.WriteFile(present, []byte("x"), 0o644))
	asDir := filepath.Join(tmpDir, "dir")
	require.NoError(t, os.Mkdir(asDir, 0o755))

	tests := []struct {
		name        string
		path        string
		wantPresent bool
		wantDir     bool
	}{
		{"existing file", present, true, false},
		{"existing directory", asDir, true, true},
		{"missing file", filepath.Join(tmpDir, "missing.txt"), false, false},
		{"missing file under a missing directory", filepath.Join(tmpDir, "missing", "missing.txt"), false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, err := lookup(tt.path)
			require.NoError(t, err)
			if !tt.wantPresent {
				assert.Nil(t, info)
				return
			}
			require.NotNil(t, info)
			assert.Equal(t, tt.wantDir, info.IsDir())
		})
	}

	t.Run("file inside an unreadable directory", func(t *testing.T) {
		locked := filepath.Join(tmpDir, "locked")
		require.NoError(t, os.Mkdir(locked, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(locked, "go.mod"), []byte("module x\n"), 0o644))
		lockDir(t, locked)

		info, err := lookup(filepath.Join(locked, "go.mod"))
		require.Error(t, err)
		assert.ErrorIs(t, err, os.ErrPermission)
		assert.Nil(t, info)
	})
}
