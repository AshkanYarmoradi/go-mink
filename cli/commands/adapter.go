package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go-mink.dev/adapters"
	"go-mink.dev/adapters/memory"
	"go-mink.dev/adapters/postgres"
	"go-mink.dev/cli/config"
)

// CLIAdapter combines all adapter interfaces needed by CLI commands.
type CLIAdapter interface {
	adapters.EventStoreAdapter
	adapters.StreamQueryAdapter
	adapters.ProjectionQueryAdapter
	adapters.MigrationAdapter
	adapters.SchemaProvider
	adapters.DiagnosticAdapter
	// FilteredFeedAdapter backs `mink events` (the filtered global-feed read).
	// Both built-in adapters (postgres, memory) implement it, so the CLI gets the
	// capability by compile-time composition rather than a runtime type assertion.
	adapters.FilteredFeedAdapter
}

// AdapterFactory creates the appropriate adapter based on configuration.
type AdapterFactory struct {
	config *config.Config
	dbURL  string
}

// NewAdapterFactory creates a new adapter factory. The database URL from mink.yaml
// is expanded with expandConfigEnv: only references to allowed environment-variable
// names are honoured (see configEnvAllowPrefixes / MINK_CONFIG_ENV_ALLOW), and a
// reference to any other variable is an error naming it.
func NewAdapterFactory(cfg *config.Config) (*AdapterFactory, error) {
	dbURL, err := expandConfigEnv(cfg.Database.URL)
	if err != nil {
		return nil, err
	}
	if cfg.Database.Driver != "memory" && dbURL == "" {
		return nil, fmt.Errorf("DATABASE_URL environment variable is not set")
	}

	return &AdapterFactory{
		config: cfg,
		dbURL:  dbURL,
	}, nil
}

// CreateAdapter creates the appropriate adapter based on the driver configuration.
// For PostgreSQL, it validates the connection with a short timeout to fail fast on invalid URLs.
func (f *AdapterFactory) CreateAdapter(ctx context.Context) (CLIAdapter, error) {
	ctx = ensureContext(ctx)

	switch f.config.Database.Driver {
	case "postgres", "postgresql":
		opts := make([]postgres.Option, 0, 1)
		if f.config.Database.Schema != "" {
			opts = append(opts, postgres.WithSchema(f.config.Database.Schema))
		}

		adapter, err := postgres.NewAdapter(f.dbURL, opts...)
		if err != nil {
			return nil, fmt.Errorf("failed to create postgres adapter: %w", err)
		}

		// Ping the database with a timeout to validate connection
		// This ensures fast failure on invalid connection strings
		pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()

		if err := adapter.Ping(pingCtx); err != nil {
			_ = adapter.Close()
			return nil, fmt.Errorf("failed to connect to postgres: %w", err)
		}

		return adapter, nil

	case "memory":
		return memory.NewAdapter(), nil

	default:
		return nil, fmt.Errorf("unsupported database driver: %s", f.config.Database.Driver)
	}
}

// GetDatabaseURL returns the resolved database URL.
func (f *AdapterFactory) GetDatabaseURL() string {
	return f.dbURL
}

// IsMemoryDriver returns true if using the memory driver.
func (f *AdapterFactory) IsMemoryDriver() bool {
	return f.config.Database.Driver == "memory"
}

// ensureContext returns the provided context or a background context if nil.
func ensureContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// createAdapterCleanup returns a cleanup function for closing adapters.
func createAdapterCleanup(adapter CLIAdapter) func() {
	return func() {
		if closer, ok := adapter.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
	}
}

// getAdapterWithConfig loads config and creates an adapter with cleanup function.
// This is the primary function - getAdapter is a convenience wrapper.
func getAdapterWithConfig(ctx context.Context) (CLIAdapter, *config.Config, func(), error) {
	ctx = ensureContext(ctx)

	cfg, _, err := loadConfig()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("no mink.yaml found: %w", err)
	}

	factory, err := NewAdapterFactory(cfg)
	if err != nil {
		return nil, nil, nil, err
	}

	adapter, err := factory.CreateAdapter(ctx)
	if err != nil {
		return nil, nil, nil, err
	}

	return adapter, cfg, createAdapterCleanup(adapter), nil
}

// getAdapter is a convenience wrapper that returns adapter without config.
func getAdapter(ctx context.Context) (CLIAdapter, func(), error) {
	adapter, _, cleanup, err := getAdapterWithConfig(ctx)
	return adapter, cleanup, err
}

// loadConfig is a helper that loads config from the current working directory.
// Returns (config, cwd, error).
func loadConfig() (*config.Config, string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, "", err
	}

	_, cfg, err := config.FindConfig(cwd)
	if err != nil {
		return nil, cwd, err
	}

	return cfg, cwd, nil
}

// loadConfigOrDefault is like loadConfig but returns defaults if no config found.
// Returns (config, cwd, error) - error only for os.Getwd failures.
func loadConfigOrDefault() (*config.Config, string, error) {
	cfg, _, cwd, err := loadConfigOrDefaultWithRoot()
	return cfg, cwd, err
}

// loadConfigOrDefaultWithRoot is like loadConfigOrDefault but also reports
// the project root: the directory that holds the mink.yaml in use, or cwd when
// no config was found and the defaults are used. Commands that write files
// derived from config values use root as the containment boundary.
// Returns (config, root, cwd, error) - error only for os.Getwd failures.
func loadConfigOrDefaultWithRoot() (*config.Config, string, string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, "", "", err
	}

	root, cfg, err := config.FindConfig(cwd)
	if err != nil {
		return config.DefaultConfig(), cwd, cwd, nil
	}

	return cfg, root, cwd, nil
}

// MigrationEnv holds the environment for migration-related commands.
// This consolidates the repeated pattern of loading config, checking for memory driver,
// and creating the adapter used across migrate up/down/status commands.
type MigrationEnv struct {
	Adapter       CLIAdapter
	Config        *config.Config
	Cwd           string
	MigrationsDir string
	cleanup       func()
}

// Close cleans up the MigrationEnv resources.
func (e *MigrationEnv) Close() {
	if e.cleanup != nil {
		e.cleanup()
	}
}

// SetupMigrationEnv creates a MigrationEnv for migration commands.
// Returns (env, isMemory, error). If isMemory is true, migrations are not needed.
func SetupMigrationEnv(ctx context.Context) (*MigrationEnv, bool, error) {
	cfg, cwd, err := loadConfig()
	if err != nil {
		return nil, false, fmt.Errorf("no mink.yaml found: %w", err)
	}

	if cfg.Database.Driver == "memory" {
		return nil, true, nil
	}

	adapter, cleanup, err := getAdapter(ctx)
	if err != nil {
		return nil, false, err
	}

	return &MigrationEnv{
		Adapter:       adapter,
		Config:        cfg,
		Cwd:           cwd,
		MigrationsDir: filepath.Join(cwd, cfg.Database.MigrationsDir),
		cleanup:       cleanup,
	}, false, nil
}

// DiagnosticSkipReason represents why a diagnostic check was skipped.
type DiagnosticSkipReason int

const (
	// DiagnosticNotSkipped means the diagnostic should proceed.
	DiagnosticNotSkipped DiagnosticSkipReason = iota
	// DiagnosticSkipNoConfig means no configuration was found.
	DiagnosticSkipNoConfig
	// DiagnosticSkipMemoryDriver means the memory driver is being used.
	DiagnosticSkipMemoryDriver
	// DiagnosticSkipNoDBURL means the database URL is not set.
	DiagnosticSkipNoDBURL
)

// DiagnosticEnv holds the environment for diagnostic checks that need database access.
// This consolidates the repeated pattern of checking config, memory driver, and DB URL.
type DiagnosticEnv struct {
	Adapter CLIAdapter
	Config  *config.Config
	cleanup func()
}

// Close cleans up the DiagnosticEnv resources.
func (e *DiagnosticEnv) Close() {
	if e.cleanup != nil {
		e.cleanup()
	}
}

// SetupDiagnosticEnv creates a DiagnosticEnv for diagnostic checks.
// Returns (env, skipReason, error). If skipReason != DiagnosticNotSkipped, the check should be skipped.
func SetupDiagnosticEnv(ctx context.Context) (*DiagnosticEnv, DiagnosticSkipReason, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, DiagnosticNotSkipped, err
	}

	_, cfg, err := config.FindConfig(cwd)
	if err != nil {
		return nil, DiagnosticSkipNoConfig, nil
	}

	if cfg.Database.Driver == "memory" {
		return nil, DiagnosticSkipMemoryDriver, nil
	}

	dbURL, err := expandConfigEnv(cfg.Database.URL)
	if err != nil {
		return nil, DiagnosticNotSkipped, err
	}
	if dbURL == "" {
		return nil, DiagnosticSkipNoDBURL, nil
	}

	adapter, cleanup, err := getAdapter(ctx)
	if err != nil {
		return nil, DiagnosticNotSkipped, err
	}

	return &DiagnosticEnv{
		Adapter: adapter,
		Config:  cfg,
		cleanup: cleanup,
	}, DiagnosticNotSkipped, nil
}

// Environment-variable expansion of mink.yaml values.
//
// database.url may reference environment variables as $NAME or ${NAME}. The file
// is not trusted input — config.FindConfig walks up parent directories, so a planted
// mink.yaml can reach the CLI — and os.ExpandEnv would splice ANY variable the file
// names ($HOME, $AWS_SECRET_ACCESS_KEY, $GITHUB_TOKEN, ...) into the DSN, from where
// it reaches connection attempts and error output. Expansion is therefore
// restricted to variable NAMES that look like database settings: names starting
// with MINK_, DATABASE_, DB_, PG or POSTGRES (case-insensitive). A reference to any
// other variable, in either syntax, fails with an error naming the variable.
// Operators widen the set with MINK_CONFIG_ENV_ALLOW, a comma-separated list of
// extra allowed name prefixes (environment variables are trusted; the file is not).
// A literal '$' in a DSN password must be percent-encoded as %24.

// configEnvAllowVar is the environment variable holding extra allowed prefixes.
const configEnvAllowVar = "MINK_CONFIG_ENV_ALLOW"

// configEnvAllowPrefixes are the variable-name prefixes mink.yaml may reference.
var configEnvAllowPrefixes = []string{"MINK_", "DATABASE_", "DB_", "PG", "POSTGRES"}

// extraConfigEnvPrefixes parses MINK_CONFIG_ENV_ALLOW: entries are trimmed and
// upper-cased, empty entries are ignored (so a bare "," opens nothing).
func extraConfigEnvPrefixes() []string {
	var out []string
	for _, p := range strings.Split(os.Getenv(configEnvAllowVar), ",") {
		if p = strings.ToUpper(strings.TrimSpace(p)); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// configEnvAllowed reports whether a mink.yaml value may expand the named
// environment variable: the (case-insensitive) name must start with one of the
// built-in prefixes or one of the extra prefixes.
func configEnvAllowed(name string, extra []string) bool {
	upper := strings.ToUpper(name)
	for _, p := range configEnvAllowPrefixes {
		if strings.HasPrefix(upper, p) {
			return true
		}
	}
	for _, p := range extra {
		if strings.HasPrefix(upper, p) {
			return true
		}
	}
	return false
}

// expandConfigEnv expands $NAME / ${NAME} references in a mink.yaml value through
// the allowlist above. An allowed variable that is unset expands to "" exactly as
// os.ExpandEnv would; a reference to a variable outside the allowlist makes the
// whole expansion fail, naming the first offending variable, so no part of the
// value is ever used.
func expandConfigEnv(value string) (string, error) {
	if !strings.Contains(value, "$") {
		return value, nil
	}
	extra := extraConfigEnvPrefixes()
	var denied []string
	expanded := os.Expand(value, func(name string) string {
		if !configEnvAllowed(name, extra) {
			denied = append(denied, name)
			return ""
		}
		return os.Getenv(name)
	})
	if len(denied) > 0 {
		return "", fmt.Errorf("mink.yaml database.url references environment variable %q, which the CLI will not expand: only names starting with %s are allowed (add a prefix to %s to allow it, or percent-encode a literal $ as %%24)",
			denied[0], strings.Join(configEnvAllowPrefixes, ", "), configEnvAllowVar)
	}
	return expanded, nil
}
