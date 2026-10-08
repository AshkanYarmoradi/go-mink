// Package config provides configuration management for the mink CLI.
package config

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config represents the mink CLI configuration
type Config struct {
	// Version of the config file format
	Version string `yaml:"version"`

	// Project configuration
	Project ProjectConfig `yaml:"project"`

	// Database configuration
	Database DatabaseConfig `yaml:"database"`

	// EventStore configuration
	EventStore EventStoreConfig `yaml:"event_store"`

	// Generation configuration
	Generation GenerationConfig `yaml:"generation"`
}

// ProjectConfig contains project-level settings
type ProjectConfig struct {
	// Name of the project
	Name string `yaml:"name"`

	// Module is the Go module path
	Module string `yaml:"module"`

	// SourceDir is the root source directory
	SourceDir string `yaml:"source_dir"`
}

// DatabaseConfig contains database connection settings
type DatabaseConfig struct {
	// Driver is the database driver (postgres, memory)
	Driver string `yaml:"driver"`

	// URL is the database connection string
	URL string `yaml:"url,omitempty"`

	// Schema is the database schema to use
	Schema string `yaml:"schema"`

	// MigrationsDir is the directory for migration files
	MigrationsDir string `yaml:"migrations_dir"`
}

// EventStoreConfig contains event store settings
type EventStoreConfig struct {
	// TableName for events
	TableName string `yaml:"table_name"`

	// SnapshotTableName for snapshots
	SnapshotTableName string `yaml:"snapshot_table_name"`

	// OutboxTableName for outbox messages
	OutboxTableName string `yaml:"outbox_table_name"`
}

// GenerationConfig contains code generation settings
type GenerationConfig struct {
	// AggregatePackage is the package for aggregates
	AggregatePackage string `yaml:"aggregate_package"`

	// EventPackage is the package for events
	EventPackage string `yaml:"event_package"`

	// ProjectionPackage is the package for projections
	ProjectionPackage string `yaml:"projection_package"`

	// CommandPackage is the package for commands
	CommandPackage string `yaml:"command_package"`
}

// DefaultConfig returns a default configuration
func DefaultConfig() *Config {
	return &Config{
		Version: "1",
		Project: ProjectConfig{
			Name:      "my-mink-app",
			Module:    "github.com/user/my-mink-app",
			SourceDir: ".",
		},
		Database: DatabaseConfig{
			Driver:        "postgres",
			Schema:        "mink",
			MigrationsDir: "migrations",
		},
		EventStore: EventStoreConfig{
			TableName:         "events",
			SnapshotTableName: "snapshots",
			OutboxTableName:   "mink_outbox",
		},
		Generation: GenerationConfig{
			AggregatePackage:  "internal/domain",
			EventPackage:      "internal/events",
			ProjectionPackage: "internal/projections",
			CommandPackage:    "internal/commands",
		},
	}
}

// ConfigFileName is the default config file name
const ConfigFileName = "mink.yaml"

// Load loads configuration from the specified directory
func Load(dir string) (*Config, error) {
	path := filepath.Join(dir, ConfigFileName)
	return LoadFile(path)
}

// LoadFile loads configuration from a specific file path
func LoadFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// Save saves the configuration to the specified directory
func (c *Config) Save(dir string) error {
	path := filepath.Join(dir, ConfigFileName)
	return c.SaveFile(path)
}

// ConfigFileMode is the permission mode used when writing mink.yaml.
//
// The file may hold a literal database.url (including a password), so it is
// written owner-read/write only.
const ConfigFileMode os.FileMode = 0o600

// SaveFile saves the configuration to a specific file path.
//
// The file is written with ConfigFileMode (0600): it can contain a literal
// database.url with credentials, so it is not world-readable.
func (c *Config) SaveFile(path string) error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, ConfigFileMode)
}

// Exists checks if a config file exists in the directory
func Exists(dir string) bool {
	path := filepath.Join(dir, ConfigFileName)
	_, err := os.Stat(path)
	return err == nil
}

// goModFileName marks the root of a Go module and bounds the config search.
const goModFileName = "go.mod"

// FindConfig searches for a config file starting from dir and going up.
//
// The search stops at the Go module boundary: once a directory containing
// go.mod has been checked, no ancestor above it is consulted. Without this
// bound, a mink.yaml planted anywhere above the project (for example in a
// shared parent directory) would silently take over the database URL, the
// migrations directory and the code-generation output locations. When no
// go.mod is found on the way up, the search continues to the filesystem root
// as before.
//
// It returns os.ErrNotExist when no config file is found within the boundary.
func FindConfig(dir string) (string, *Config, error) {
	current := dir
	for {
		configPath := filepath.Join(current, ConfigFileName)
		if _, err := os.Stat(configPath); err == nil {
			cfg, err := LoadFile(configPath)
			if err != nil {
				return "", nil, err
			}
			return current, cfg, nil
		}

		if _, err := os.Stat(filepath.Join(current, goModFileName)); err == nil {
			// Reached the module root without finding a config: do not look
			// above the module boundary.
			return "", nil, os.ErrNotExist
		}

		parent := filepath.Dir(current)
		if parent == current {
			// Reached root, config not found
			return "", nil, os.ErrNotExist
		}
		current = parent
	}
}

// Validate validates the configuration
func (c *Config) Validate() []string {
	var errors []string

	if c.Project.Name == "" {
		errors = append(errors, "project.name is required")
	}

	if c.Project.Module == "" {
		errors = append(errors, "project.module is required")
	}

	if c.Database.Driver == "" {
		errors = append(errors, "database.driver is required")
	}

	if c.Database.Driver != "postgres" && c.Database.Driver != "memory" {
		errors = append(errors, "database.driver must be 'postgres' or 'memory'")
	}

	if c.Database.Driver == "postgres" && c.Database.URL == "" {
		errors = append(errors, "database.url is required for postgres driver")
	}

	return errors
}

// GenerateYAML generates YAML content with comments
func GenerateYAML(cfg *Config) string {
	return `# Mink Configuration File
# This file configures the mink CLI and code generation

version: "1"

# Project settings
project:
  # Name of your project
  name: "` + cfg.Project.Name + `"
  
  # Go module path (from go.mod)
  module: "` + cfg.Project.Module + `"
  
  # Source directory relative to this file
  source_dir: "` + cfg.Project.SourceDir + `"

# Database configuration
database:
  # Driver: postgres or memory
  driver: "` + cfg.Database.Driver + `"
  
  # Connection URL (required for postgres)
  url: "${DATABASE_URL}"
  
  # Database schema (postgres only)
  schema: "` + cfg.Database.Schema + `"
  
  # Directory for SQL migrations
  migrations_dir: "` + cfg.Database.MigrationsDir + `"

# Event store table names
event_store:
  table_name: "` + cfg.EventStore.TableName + `"
  snapshot_table_name: "` + cfg.EventStore.SnapshotTableName + `"
  outbox_table_name: "` + cfg.EventStore.OutboxTableName + `"

# Code generation output packages
generation:
  aggregate_package: "` + cfg.Generation.AggregatePackage + `"
  event_package: "` + cfg.Generation.EventPackage + `"
  projection_package: "` + cfg.Generation.ProjectionPackage + `"
  command_package: "` + cfg.Generation.CommandPackage + `"
`
}
