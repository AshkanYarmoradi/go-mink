package commands

import (
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
	var force bool

	cmd := &cobra.Command{
		Use:   "generate",
		Short: "Generate event store schema SQL",
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = force // Used for scripting (skip interactive elements)

			cwd, err := os.Getwd()
			if err != nil {
				return err
			}

			_, cfg, err := config.FindConfig(cwd)
			if err != nil {
				cfg = config.DefaultConfig()
			}

			schema := generateSchema(cfg)

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
	cmd.Flags().BoolVarP(&force, "force", "f", false, "Skip interactive elements (for scripting)")

	return cmd
}

func newSchemaPrintCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "print",
		Short: "Print the event store schema",
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}

			_, cfg, err := config.FindConfig(cwd)
			if err != nil {
				cfg = config.DefaultConfig()
			}

			fmt.Println()
			fmt.Println(styles.Title.Render(styles.IconDatabase + " Event Store Schema"))
			fmt.Println()
			fmt.Println(styles.Code.Render(generateSchema(cfg)))

			return nil
		},
	}
}

func generateSchema(cfg *config.Config) string {
	return fmt.Sprintf(`-- Mink Event Store Schema
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

-- Function for appending events with optimistic concurrency
CREATE OR REPLACE FUNCTION mink_append_events(
    p_stream_id VARCHAR(255),
    p_expected_version BIGINT,
    p_events JSONB
) RETURNS TABLE(
    id UUID,
    stream_id VARCHAR(255),
    version BIGINT,
    type VARCHAR(255),
    data JSONB,
    metadata JSONB,
    global_position BIGINT,
    timestamp TIMESTAMPTZ
) AS $$
DECLARE
    v_current_version BIGINT;
    v_event JSONB;
    v_version BIGINT;
BEGIN
    -- Lock and get current version
    SELECT COALESCE(MAX(e.version), 0) INTO v_current_version
    FROM %s e
    WHERE e.stream_id = p_stream_id
    FOR UPDATE;
    
    -- Check expected version
    -- -1 = AnyVersion, 0 = NoStream, -2 = StreamExists
    IF p_expected_version >= 0 AND v_current_version != p_expected_version THEN
        RAISE EXCEPTION 'mink: concurrency conflict on stream %% expected %% got %%', 
            p_stream_id, p_expected_version, v_current_version;
    ELSIF p_expected_version = 0 AND v_current_version > 0 THEN
        RAISE EXCEPTION 'mink: stream %% already exists', p_stream_id;
    ELSIF p_expected_version = -2 AND v_current_version = 0 THEN
        RAISE EXCEPTION 'mink: stream %% does not exist', p_stream_id;
    END IF;
    
    v_version := v_current_version;
    
    -- Insert events
    FOR v_event IN SELECT * FROM jsonb_array_elements(p_events)
    LOOP
        v_version := v_version + 1;
        
        INSERT INTO %s (stream_id, version, type, data, metadata)
        VALUES (
            p_stream_id,
            v_version,
            v_event->>'type',
            v_event->'data',
            COALESCE(v_event->'metadata', '{}'::jsonb)
        )
        RETURNING 
            %s.id,
            %s.stream_id,
            %s.version,
            %s.type,
            %s.data,
            %s.metadata,
            %s.global_position,
            %s.timestamp
        INTO id, stream_id, version, type, data, metadata, global_position, timestamp;
        
        RETURN NEXT;
    END LOOP;
END;
$$ LANGUAGE plpgsql;

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
		cfg.EventStore.TableName,
		cfg.EventStore.TableName,
		cfg.EventStore.TableName,
		cfg.EventStore.TableName,
		cfg.EventStore.TableName,
		cfg.EventStore.TableName,
		cfg.EventStore.TableName,
		cfg.EventStore.TableName,
		cfg.EventStore.TableName,
		cfg.EventStore.TableName,
		cfg.EventStore.SnapshotTableName,
		cfg.EventStore.OutboxTableName,
	)
}
