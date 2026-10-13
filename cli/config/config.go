// Package config provides configuration management for the mink CLI.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

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
// The file is written with ConfigFileMode (0600) through WriteSecureFile: it
// can contain a literal database.url with credentials, so it is not
// world-readable — not even when it replaces an existing, more permissive
// mink.yaml.
func (c *Config) SaveFile(path string) error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}

	return WriteSecureFile(path, data)
}

// WriteSecureFile atomically replaces the file at path with data, written
// owner-read/write only (ConfigFileMode, 0600).
//
// It is for files that hold credentials or personal data (mink.yaml, stream
// exports) and must never be readable by other users whatever existed at path
// before. os.WriteFile applies its mode only when it creates the file, and
// opening path for truncation follows symlinks, so neither is safe here.
//
// Guarantees:
//
//   - The data is written to a fresh temporary file in the same directory
//     (named ".<name>.<random>.tmp"), created 0600 and chmod'ed to 0600 so the
//     process umask has no say, fsynced and closed, then renamed over path.
//     Readers see either the previous file or the complete new one, never a
//     partially written one.
//   - A symlink or hard link at path is replaced, never followed: the rename
//     swaps the directory entry, so a link planted at the config or export
//     path cannot redirect the write (and the chmod and truncation that go with
//     it) to any other file the CLI user can write.
//   - The result is always a regular file with mode 0600, whether path held a
//     world-readable file, a link, or nothing.
//   - On failure the temporary file is removed and whatever was at path is
//     left untouched.
//
// Consequences: write permission is needed on the directory, not only on the
// file, and a process that still has the previous file open keeps reading the
// old content. On Unix the directory is fsynced best-effort after the rename
// so the new entry survives a crash. On Windows POSIX permission bits are not
// enforced (the chmod is best-effort there) and os.Rename replaces an existing
// destination (MoveFileEx with MOVEFILE_REPLACE_EXISTING); a rename that
// fails because the destination is briefly held by a concurrent writer or an
// antivirus scanner is retried with backoff for up to renameRetryBudget, as
// cmd/go does. A process killed mid-write can leave a 0600
// ".<name>.<random>.tmp" file behind; it holds only the data that was being
// written and can be deleted.
func WriteSecureFile(path string, data []byte) (err error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	closed := false
	defer func() {
		if err == nil {
			return
		}
		if !closed {
			_ = tmp.Close()
		}
		_ = os.Remove(tmpPath)
	}()

	// os.CreateTemp creates the file 0600, but the umask may have stripped
	// bits from that. Fix the mode on the open descriptor before any data is
	// written; on Windows File.Chmod is not supported and permission bits are
	// not enforced, so a failure there must not fail the write.
	if cerr := tmp.Chmod(ConfigFileMode); cerr != nil && runtime.GOOS != "windows" {
		return cerr
	}
	if _, err = tmp.Write(data); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	closed = true
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = renameReplace(tmpPath, path); err != nil {
		return err
	}
	syncDir(dir)
	return nil
}

// syncDir flushes the directory entry written by a rename to disk. It is
// best-effort: a directory cannot be fsynced on Windows, and a failure here
// does not undo the (already complete) rename.
func syncDir(dir string) {
	if runtime.GOOS == "windows" {
		return
	}
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}

// renameRetryBudget bounds how long renameReplace keeps retrying a rename on
// Windows (the same arbitrary 2s cmd/go's robustio uses).
const renameRetryBudget = 2 * time.Second

// errnoSharingViolation is Windows' ERROR_SHARING_VIOLATION: the destination
// is open by someone who did not allow deletion. It is only compared against
// on Windows.
const errnoSharingViolation = syscall.Errno(32)

// renameReplace renames tmpPath over path, replacing whatever entry is there.
//
// On Unix rename(2) is atomic and either succeeds or fails for good, so it is
// attempted once. On Windows, MoveFileEx(MOVEFILE_REPLACE_EXISTING) fails
// with a transient ERROR_ACCESS_DENIED or ERROR_SHARING_VIOLATION while
// another writer is replacing the same destination or an antivirus scanner
// has it open; those are retried with exponential backoff for at most
// renameRetryBudget, then the last error is returned. A failure with a
// permanent cause the caller can see (the destination is a directory, or a
// read-only file) is returned at once.
func renameReplace(tmpPath, path string) error {
	if runtime.GOOS != "windows" {
		return os.Rename(tmpPath, path)
	}
	delay := time.Millisecond
	deadline := time.Now().Add(renameRetryBudget)
	for {
		err := os.Rename(tmpPath, path)
		if err == nil || !isTransientWindowsRenameError(err, path) || !time.Now().Before(deadline) {
			return err
		}
		time.Sleep(delay)
		if delay < 50*time.Millisecond {
			delay *= 2
		}
	}
}

// isTransientWindowsRenameError reports whether err from a rename onto path
// is worth retrying on Windows: an access-denied or sharing-violation failure
// whose destination is not a directory and not a read-only file (either of
// which would fail the same way forever).
func isTransientWindowsRenameError(err error, path string) bool {
	var errno syscall.Errno
	sharing := errors.As(err, &errno) && errno == errnoSharingViolation
	if !errors.Is(err, os.ErrPermission) && !sharing {
		return false
	}
	info, statErr := os.Lstat(path)
	if statErr != nil {
		// Absent or in flux under a concurrent replace: retry.
		return true
	}
	return !info.IsDir() && info.Mode().Perm()&0o200 != 0
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
// The search stops at the Go module boundary: once a directory containing a
// go.mod file has been checked, no ancestor above it is consulted. Without
// this bound, a mink.yaml planted anywhere above the project (for example in
// a shared parent directory) would silently take over the database URL, the
// migrations directory and the code-generation output locations. When no
// go.mod is found on the way up, the search continues to the filesystem root
// as before. Like the go tool itself, FindConfig treats a directory named
// go.mod as absent: it is not a module file, so it is not a boundary.
//
// The search fails closed. Only "does not exist" counts as a file being
// absent; any other failure to look for mink.yaml or go.mod (permission
// denied on a directory in the chain, an I/O error, ...) aborts the search
// with that error, wrapped. Treating such a failure as "no go.mod here" would
// walk past a boundary the search could not see and let an ancestor config
// take over.
//
// It returns os.ErrNotExist when no config file is found within the boundary.
func FindConfig(dir string) (string, *Config, error) {
	current := dir
	for {
		configPath := filepath.Join(current, ConfigFileName)
		cfgInfo, err := lookup(configPath)
		if err != nil {
			return "", nil, fmt.Errorf("config search aborted: %w", err)
		}
		if cfgInfo != nil {
			cfg, err := LoadFile(configPath)
			if err != nil {
				return "", nil, err
			}
			return current, cfg, nil
		}

		modInfo, err := lookup(filepath.Join(current, goModFileName))
		if err != nil {
			return "", nil, fmt.Errorf("config search aborted: %w", err)
		}
		if modInfo != nil && !modInfo.IsDir() {
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

// lookup stats path and distinguishes "absent" from "could not look". It
// returns (nil, nil) only when path does not exist (os.ErrNotExist, which is
// fs.ErrNotExist); every other Stat failure is returned so that a caller never
// mistakes an unreadable directory for an empty one.
func lookup(path string) (os.FileInfo, error) {
	info, err := os.Stat(path)
	if err == nil {
		return info, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return nil, err
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
