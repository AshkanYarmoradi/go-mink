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
	"go-mink.dev/adapters/mongodb"
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
}

// AdapterFactory creates the appropriate adapter based on configuration.
type AdapterFactory struct {
	config *config.Config
	dbURL  string
}

// NewAdapterFactory creates a new adapter factory.
func NewAdapterFactory(cfg *config.Config) (*AdapterFactory, error) {
	isPersistentDriver := cfg.Database.Driver != "memory"
	rawURL := cfg.Database.URL
	dbURL := os.ExpandEnv(cfg.Database.URL)
	if isPersistentDriver && (dbURL == "" || unresolvedEnvRef(rawURL, dbURL)) {
		return nil, fmt.Errorf("%s environment variable is not set", envNameFromRef(rawURL))
	}

	return &AdapterFactory{
		config: cfg,
		dbURL:  dbURL,
	}, nil
}

func unresolvedEnvRef(rawURL, expandedURL string) bool {
	return rawURL == expandedURL && strings.HasPrefix(rawURL, "${") && strings.HasSuffix(rawURL, "}")
}

func envNameFromRef(rawURL string) string {
	if strings.HasPrefix(rawURL, "${") && strings.HasSuffix(rawURL, "}") {
		return strings.TrimSuffix(strings.TrimPrefix(rawURL, "${"), "}")
	}
	return "DATABASE_URL"
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

	case "mongodb", "mongo":
		names := mongodb.CollectionNames{
			Events:    f.config.EventStore.TableName,
			Snapshots: f.config.EventStore.SnapshotTableName,
			Outbox:    f.config.EventStore.OutboxTableName,
		}
		transactionMode, err := parseMongoTransactionMode(f.config.Database.TransactionMode)
		if err != nil {
			return nil, err
		}
		subscriptionMode, err := parseMongoSubscriptionMode(f.config.Database.SubscriptionMode)
		if err != nil {
			return nil, err
		}
		adapter, err := mongodb.NewAdapter(
			f.dbURL,
			mongodb.WithDatabase(f.config.Database.Schema),
			mongodb.WithCollectionNames(names),
			mongodb.WithTransactionMode(transactionMode),
			mongodb.WithSubscriptionMode(subscriptionMode),
		)
		if err != nil {
			return nil, fmt.Errorf("failed to create mongodb adapter: %w", err)
		}

		pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()

		if err := adapter.Ping(pingCtx); err != nil {
			_ = adapter.Close()
			return nil, fmt.Errorf("failed to connect to mongodb: %w", err)
		}

		return adapter, nil

	case "memory":
		return memory.NewAdapter(), nil

	default:
		return nil, fmt.Errorf("unsupported database driver: %s", f.config.Database.Driver)
	}
}

func parseMongoTransactionMode(value string) (mongodb.TransactionMode, error) {
	switch strings.ToLower(value) {
	case "", "auto":
		return mongodb.TransactionModeAuto, nil
	case "required":
		return mongodb.TransactionModeRequired, nil
	case "disabled":
		return mongodb.TransactionModeDisabled, nil
	default:
		return mongodb.TransactionModeAuto, fmt.Errorf("database.transaction_mode must be 'auto', 'required', or 'disabled'")
	}
}

func parseMongoSubscriptionMode(value string) (mongodb.SubscriptionMode, error) {
	switch strings.ToLower(value) {
	case "", "auto":
		return mongodb.SubscriptionModeAuto, nil
	case "polling":
		return mongodb.SubscriptionModePolling, nil
	case "change_stream":
		return mongodb.SubscriptionModeChangeStream, nil
	default:
		return mongodb.SubscriptionModeAuto, fmt.Errorf("database.subscription_mode must be 'auto', 'polling', or 'change_stream'")
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
	cwd, err := os.Getwd()
	if err != nil {
		return nil, "", err
	}

	_, cfg, err := config.FindConfig(cwd)
	if err != nil {
		return config.DefaultConfig(), cwd, nil
	}

	return cfg, cwd, nil
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

	dbURL := os.ExpandEnv(cfg.Database.URL)
	if dbURL == "" || dbURL == "${DATABASE_URL}" {
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
