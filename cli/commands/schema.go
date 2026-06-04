package commands

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"go-mink.dev/adapters/postgres"
	"go-mink.dev/cli/config"
	"go-mink.dev/cli/styles"
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
	cmd.AddCommand(newSchemaValidateCommand())

	return requireSubcommand(cmd)
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

// newSchemaValidateCommand validates the event store schema against the
// configured database using the adapter's CheckSchema capability.
func newSchemaValidateCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "Validate the event store schema against the database",
		Long: `Validate that the event store schema (tables) exists in the configured database.

Requires a configured non-memory driver and a reachable database (DATABASE_URL).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			env, skipReason, err := SetupDiagnosticEnv(ctx)
			switch skipReason {
			case DiagnosticSkipNoConfig:
				return fmt.Errorf("no mink.yaml found; run 'mink init' first")
			case DiagnosticSkipMemoryDriver:
				fmt.Println(styles.FormatInfo("Memory driver - schema is in-memory, nothing to validate"))
				return nil
			case DiagnosticSkipNoDBURL:
				return fmt.Errorf("DATABASE_URL is not set; cannot validate schema against the database")
			}
			if err != nil {
				return err
			}
			defer env.Close()

			result, err := env.Adapter.CheckSchema(ctx, env.Config.EventStore.TableName)
			if err != nil {
				return fmt.Errorf("failed to validate schema: %w", err)
			}

			if !result.TableExists {
				fmt.Println(styles.FormatError(result.Message))
				return fmt.Errorf("event store schema is missing; run 'mink migrate up' to create the tables")
			}

			fmt.Println(styles.FormatSuccess(fmt.Sprintf("Schema is valid: %s", result.Message)))
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
	return postgres.GenerateSchema(
		cfg.Project.Name,
		cfg.Database.Schema,
		cfg.EventStore.TableName,
		cfg.EventStore.SnapshotTableName,
		cfg.EventStore.OutboxTableName,
	)
}
