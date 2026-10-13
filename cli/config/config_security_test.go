package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeGoMod plants a minimal go.mod in dir, marking it as a module root.
func writeGoMod(t *testing.T, dir string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, goModFileName), []byte("module example.com/app\n\ngo 1.26\n"), 0o644))
}

func TestSaveFile_WritesOwnerOnlyPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not enforced on Windows")
	}

	cfg := DefaultConfig()
	cfg.Database.URL = "postgres://user:secret@localhost/db"

	path := filepath.Join(t.TempDir(), ConfigFileName)
	require.NoError(t, cfg.SaveFile(path))

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(),
		"mink.yaml can hold database credentials and must not be group/world readable")
}

func TestConfigFileMode_IsOwnerOnly(t *testing.T) {
	assert.Equal(t, os.FileMode(0o600), ConfigFileMode)
}

func TestFindConfig_StopsAtModuleBoundary(t *testing.T) {
	tmpDir := t.TempDir()

	// A mink.yaml planted above the module root, pointing the CLI at an
	// attacker-chosen DSN that would expand arbitrary environment variables.
	planted := DefaultConfig()
	planted.Project.Name = "planted-above-module"
	planted.Database.URL = "postgres://attacker@evil.example/db?token=${HOME}"
	require.NoError(t, planted.Save(tmpDir))

	// The module root sits below it, and the working directory is nested
	// inside the module.
	moduleRoot := filepath.Join(tmpDir, "module")
	nested := filepath.Join(moduleRoot, "internal", "pkg")
	writeGoMod(t, moduleRoot)
	require.NoError(t, os.MkdirAll(nested, 0o755))

	tests := []struct {
		name string
		from string
	}{
		{"from a directory nested inside the module", nested},
		{"from the module root itself", moduleRoot},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir, cfg, err := FindConfig(tt.from)
			require.Error(t, err, "a mink.yaml above go.mod must not be picked up")
			assert.ErrorIs(t, err, os.ErrNotExist)
			assert.Empty(t, dir)
			assert.Nil(t, cfg)
		})
	}
}

func TestFindConfig_ConfigBesideGoModIsFound(t *testing.T) {
	moduleRoot := filepath.Join(t.TempDir(), "module")
	nested := filepath.Join(moduleRoot, "cmd", "app")
	writeGoMod(t, moduleRoot)
	require.NoError(t, os.MkdirAll(nested, 0o755))

	cfg := DefaultConfig()
	cfg.Project.Name = "module-project"
	require.NoError(t, cfg.Save(moduleRoot))

	foundDir, found, err := FindConfig(nested)
	require.NoError(t, err, "the directory holding go.mod is itself searched")
	assert.Equal(t, moduleRoot, foundDir)
	assert.Equal(t, "module-project", found.Project.Name)
}

func TestFindConfig_ConfigBelowGoModIsFound(t *testing.T) {
	moduleRoot := filepath.Join(t.TempDir(), "module")
	configDir := filepath.Join(moduleRoot, "services", "orders")
	nested := filepath.Join(configDir, "internal")
	writeGoMod(t, moduleRoot)
	require.NoError(t, os.MkdirAll(nested, 0o755))

	cfg := DefaultConfig()
	cfg.Project.Name = "nested-project"
	require.NoError(t, cfg.Save(configDir))

	foundDir, found, err := FindConfig(nested)
	require.NoError(t, err, "a config below the module boundary is reachable")
	assert.Equal(t, configDir, foundDir)
	assert.Equal(t, "nested-project", found.Project.Name)
}

func TestFindConfig_WithoutGoModWalksUpToAncestor(t *testing.T) {
	// No go.mod anywhere in the tree: the pre-existing walk-up behaviour is
	// preserved and an ancestor's mink.yaml is found.
	tmpDir := t.TempDir()
	cfg := DefaultConfig()
	cfg.Project.Name = "ancestor-project"
	require.NoError(t, cfg.Save(tmpDir))

	nested := filepath.Join(tmpDir, "a", "b", "c")
	require.NoError(t, os.MkdirAll(nested, 0o755))

	foundDir, found, err := FindConfig(nested)
	require.NoError(t, err)
	assert.Equal(t, tmpDir, foundDir)
	assert.Equal(t, "ancestor-project", found.Project.Name)
}

func TestFindConfig_InvalidConfigBelowBoundaryIsReported(t *testing.T) {
	moduleRoot := filepath.Join(t.TempDir(), "module")
	writeGoMod(t, moduleRoot)
	require.NoError(t, os.WriteFile(filepath.Join(moduleRoot, ConfigFileName), []byte("invalid: yaml: ["), 0o644))

	_, _, err := FindConfig(moduleRoot)
	require.Error(t, err)
	assert.NotErrorIs(t, err, os.ErrNotExist, "a malformed config is a load error, not a missing one")
}
