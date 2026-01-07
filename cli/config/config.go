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
			TableName:         "mink_events",
			SnapshotTableName: "mink_snapshots",
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

// SaveFile saves the configuration to a specific file path
func (c *Config) SaveFile(path string) error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
}

// Exists checks if a config file exists in the directory
func Exists(dir string) bool {
	path := filepath.Join(dir, ConfigFileName)
	_, err := os.Stat(path)
	return err == nil
}

// FindConfig searches for a config file starting from dir and going up
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
