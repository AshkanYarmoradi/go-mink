package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	assert.Equal(t, "1", cfg.Version)
	assert.Equal(t, "my-mink-app", cfg.Project.Name)
	assert.Equal(t, "postgres", cfg.Database.Driver)
	assert.Equal(t, "mink", cfg.Database.Schema)
	assert.Equal(t, "mink_events", cfg.EventStore.TableName)
}

func TestConfig_Validate(t *testing.T) {
	tests := []struct {
		name       string
		modify     func(*Config)
		wantErrors int
	}{
		{
			name:       "valid default config with postgres URL",
			modify:     func(c *Config) { c.Database.URL = "postgres://localhost/db" },
			wantErrors: 0,
		},
		{
			name:       "valid memory driver",
			modify:     func(c *Config) { c.Database.Driver = "memory" },
			wantErrors: 0,
		},
		{
			name:       "missing project name",
			modify:     func(c *Config) { c.Project.Name = ""; c.Database.URL = "postgres://localhost/db" },
			wantErrors: 1,
		},
		{
			name:       "missing project module",
			modify:     func(c *Config) { c.Project.Module = ""; c.Database.URL = "postgres://localhost/db" },
			wantErrors: 1,
		},
		{
			name:       "missing driver",
			modify:     func(c *Config) { c.Database.Driver = "" },
			wantErrors: 2, // Both "required" and "invalid driver" errors
		},
		{
			name:       "invalid driver",
			modify:     func(c *Config) { c.Database.Driver = "mysql" },
			wantErrors: 1,
		},
		{
			name:       "postgres without URL",
			modify:     func(c *Config) { c.Database.Driver = "postgres"; c.Database.URL = "" },
			wantErrors: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			tt.modify(cfg)
			errors := cfg.Validate()
			assert.Equal(t, tt.wantErrors, len(errors), "errors: %v", errors)
		})
	}
}

func TestConfig_SaveAndLoad(t *testing.T) {
	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "mink-config-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	// Create and save config
	cfg := DefaultConfig()
	cfg.Project.Name = "test-project"
	cfg.Project.Module = "github.com/test/project"
	cfg.Database.URL = "postgres://localhost/test"

	err = cfg.Save(tmpDir)
	require.NoError(t, err)

	// Verify file exists
	configPath := filepath.Join(tmpDir, ConfigFileName)
	_, err = os.Stat(configPath)
	require.NoError(t, err)

	// Load config
	loaded, err := Load(tmpDir)
	require.NoError(t, err)

	assert.Equal(t, cfg.Project.Name, loaded.Project.Name)
	assert.Equal(t, cfg.Project.Module, loaded.Project.Module)
	assert.Equal(t, cfg.Database.URL, loaded.Database.URL)
}

func TestExists(t *testing.T) {
	// Test directory without config
	tmpDir, err := os.MkdirTemp("", "mink-config-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	assert.False(t, Exists(tmpDir))

	// Create config
	cfg := DefaultConfig()
	err = cfg.Save(tmpDir)
	require.NoError(t, err)

	assert.True(t, Exists(tmpDir))
}

func TestFindConfig(t *testing.T) {
	// Create nested directory structure
	tmpDir, err := os.MkdirTemp("", "mink-config-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	// Create config at root
	cfg := DefaultConfig()
	cfg.Project.Name = "root-project"
	err = cfg.Save(tmpDir)
	require.NoError(t, err)

	// Create nested directories
	nested := filepath.Join(tmpDir, "a", "b", "c")
	err = os.MkdirAll(nested, 0755)
	require.NoError(t, err)

	// Find config from nested directory
	foundDir, foundCfg, err := FindConfig(nested)
	require.NoError(t, err)

	assert.Equal(t, tmpDir, foundDir)
	assert.Equal(t, "root-project", foundCfg.Project.Name)
}

func TestGenerateYAML(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Project.Name = "test-app"
	cfg.Project.Module = "github.com/test/app"

	yaml := GenerateYAML(cfg)

	assert.Contains(t, yaml, "test-app")
	assert.Contains(t, yaml, "github.com/test/app")
	assert.Contains(t, yaml, "postgres")
	assert.Contains(t, yaml, "mink_events")
	assert.Contains(t, yaml, "# Mink Configuration File")
}
