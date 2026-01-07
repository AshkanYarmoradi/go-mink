package commands

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/AshkanYarmoradi/go-mink/adapters"
	"github.com/AshkanYarmoradi/go-mink/adapters/memory"
	"github.com/AshkanYarmoradi/go-mink/adapters/postgres"
	"github.com/AshkanYarmoradi/go-mink/cli/config"
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
	dbURL := os.ExpandEnv(cfg.Database.URL)
	if cfg.Database.Driver != "memory" && (dbURL == "" || dbURL == "${DATABASE_URL}") {
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
		adapter, err := postgres.NewAdapter(f.dbURL)
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

// getAdapter is a helper function used by CLI commands to get an adapter.
// It handles config loading, URL resolution, and adapter creation.
func getAdapter(ctx context.Context) (CLIAdapter, func(), error) {
	ctx = ensureContext(ctx)

	cfg, _, err := loadConfig()
	if err != nil {
		return nil, nil, fmt.Errorf("no mink.yaml found: %w", err)
	}

	factory, err := NewAdapterFactory(cfg)
	if err != nil {
		return nil, nil, err
	}

	adapter, err := factory.CreateAdapter(ctx)
	if err != nil {
		return nil, nil, err
	}

	cleanup := func() {
		if closer, ok := adapter.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
	}

	return adapter, cleanup, nil
}

// getAdapterWithConfig is like getAdapter but also returns the config.
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

	cleanup := func() {
		if closer, ok := adapter.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
	}

	return adapter, cfg, cleanup, nil
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
