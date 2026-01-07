package commands

import (
	"context"
	"fmt"
	"os"

	"github.com/AshkanYarmoradi/go-mink/cli/config"
	"github.com/AshkanYarmoradi/go-mink/cli/styles"
	"github.com/spf13/cobra"
)

// NewSchemaCommand creates the schema command
func NewSchemaCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "schema",
		Short: "Manage event store schema",
		Long: `Generate and manage event store database schema.

Examples:
  mink schema generate           # Generate schema SQL
  mink schema print              # Print current schema
  mink schema validate           # Validate schema against database`,
	}

	cmd.AddCommand(newSchemaGenerateCommand())
	cmd.AddCommand(newSchemaPrintCommand())

	return cmd
}

func newSchemaGenerateCommand() *cobra.Command {
	var output string
	var nonInteractive bool

	cmd := &cobra.Command{
		Use:   "generate",
		Short: "Generate event store schema SQL",
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = nonInteractive // Used for scripting (skip interactive elements)
			ctx := cmd.Context()

			cfg, _, err := loadConfigOrDefault()
			if err != nil {
				return err
			}

			schema, err := generateSchemaFromAdapter(ctx, cfg)
			if err != nil {
				return err
			}

			if output != "" {
				if err := os.WriteFile(output, []byte(schema), 0644); err != nil {
					return err
				}
				fmt.Println(styles.FormatSuccess(fmt.Sprintf("Schema written to %s", output)))
			} else {
				fmt.Println(schema)
			}

			return nil
		},
	}

	cmd.Flags().StringVarP(&output, "output", "o", "", "Output file (default: stdout)")
	cmd.Flags().BoolVar(&nonInteractive, "non-interactive", false, "Skip interactive elements (for scripting)")

	return cmd
}

func newSchemaPrintCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "print",
		Short: "Print the event store schema",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			cfg, _, err := loadConfigOrDefault()
			if err != nil {
				return err
			}

			schema, err := generateSchemaFromAdapter(ctx, cfg)
			if err != nil {
				return err
			}

			fmt.Println()
			fmt.Println(styles.Title.Render(styles.IconDatabase + " Event Store Schema"))
			fmt.Println()
			fmt.Println(styles.Code.Render(schema))

			return nil
		},
	}
}

// generateSchemaFromAdapter uses the adapter's schema generation capability.
func generateSchemaFromAdapter(ctx context.Context, cfg *config.Config) (string, error) {
	// Create a temporary adapter just to get the schema
	factory, err := NewAdapterFactory(cfg)
	if err != nil {
		// For schema generation without database, use fallback
		return generateFallbackSchema(cfg), nil
	}

	adapter, err := factory.CreateAdapter(ctx)
	if err != nil {
		// If we can't create adapter, use fallback schema
		return generateFallbackSchema(cfg), nil
	}
	defer func() {
		if closer, ok := adapter.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
	}()

	return adapter.GenerateSchema(
		cfg.Project.Name,
		cfg.EventStore.TableName,
		cfg.EventStore.SnapshotTableName,
		cfg.EventStore.OutboxTableName,
	), nil
}

// generateFallbackSchema generates a basic PostgreSQL schema when adapter is not available.
// This is used when DATABASE_URL is not set but user still wants to see the schema.
func generateFallbackSchema(cfg *config.Config) string {
	return fmt.Sprintf(`-- Mink Event Store Schema (PostgreSQL)
-- Generated for: %s

-- Events table
CREATE TABLE IF NOT EXISTS %s (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    stream_id VARCHAR(255) NOT NULL,
    version BIGINT NOT NULL,
    type VARCHAR(255) NOT NULL,
    data JSONB NOT NULL,
    metadata JSONB DEFAULT '{}',
    global_position BIGSERIAL,
    timestamp TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE(stream_id, version)
);

-- Indexes for events
CREATE INDEX IF NOT EXISTS idx_%s_stream_id ON %s(stream_id, version);
CREATE INDEX IF NOT EXISTS idx_%s_global_position ON %s(global_position);
CREATE INDEX IF NOT EXISTS idx_%s_type ON %s(type);
CREATE INDEX IF NOT EXISTS idx_%s_timestamp ON %s(timestamp);

-- Snapshots table
CREATE TABLE IF NOT EXISTS %s (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    stream_id VARCHAR(255) NOT NULL,
    version BIGINT NOT NULL,
    type VARCHAR(255) NOT NULL,
    data JSONB NOT NULL,
    timestamp TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE(stream_id)
);

CREATE INDEX IF NOT EXISTS idx_%s_stream_id ON %s(stream_id);

-- Projection checkpoints
CREATE TABLE IF NOT EXISTS mink_checkpoints (
    projection_name VARCHAR(255) PRIMARY KEY,
    position BIGINT NOT NULL DEFAULT 0,
    status VARCHAR(50) DEFAULT 'active',
    updated_at TIMESTAMPTZ DEFAULT NOW()
);

-- Outbox table
CREATE TABLE IF NOT EXISTS %s (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    event_type VARCHAR(255) NOT NULL,
    payload JSONB NOT NULL,
    destination VARCHAR(255),
    created_at TIMESTAMPTZ DEFAULT NOW(),
    processed_at TIMESTAMPTZ,
    error TEXT
);

CREATE INDEX IF NOT EXISTS idx_%s_unprocessed ON %s(created_at) WHERE processed_at IS NULL;

-- Migrations tracking
CREATE TABLE IF NOT EXISTS mink_migrations (
    name VARCHAR(255) PRIMARY KEY,
    applied_at TIMESTAMPTZ DEFAULT NOW()
);

-- Idempotency keys
CREATE TABLE IF NOT EXISTS mink_idempotency (
    key VARCHAR(255) PRIMARY KEY,
    result JSONB,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    expires_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_mink_idempotency_expires ON mink_idempotency(expires_at);

COMMENT ON TABLE %s IS 'Mink event store - immutable event log';
COMMENT ON TABLE %s IS 'Aggregate snapshots for optimization';
COMMENT ON TABLE mink_checkpoints IS 'Projection checkpoint positions';
COMMENT ON TABLE %s IS 'Transactional outbox for reliable messaging';
`,
		cfg.Project.Name,
		cfg.EventStore.TableName,
		cfg.EventStore.TableName, cfg.EventStore.TableName,
		cfg.EventStore.TableName, cfg.EventStore.TableName,
		cfg.EventStore.TableName, cfg.EventStore.TableName,
		cfg.EventStore.TableName, cfg.EventStore.TableName,
		cfg.EventStore.SnapshotTableName,
		cfg.EventStore.SnapshotTableName, cfg.EventStore.SnapshotTableName,
		cfg.EventStore.OutboxTableName,
		cfg.EventStore.OutboxTableName, cfg.EventStore.OutboxTableName,
		cfg.EventStore.TableName,
		cfg.EventStore.SnapshotTableName,
		cfg.EventStore.OutboxTableName,
	)
}
