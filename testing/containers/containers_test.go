package containers

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Test Helpers
// =============================================================================

// setTestEnvVars sets environment variables and restores them when the test completes.
func setTestEnvVars(t *testing.T, envs map[string]string) {
	t.Helper()
	for k, v := range envs {
		_ = os.Setenv(k, v)
	}
	t.Cleanup(func() {
		for k := range envs {
			_ = os.Unsetenv(k)
		}
	})
}

// clearTestEnvVars unsets the given env vars and restores their original values on cleanup.
func clearTestEnvVars(t *testing.T, keys []string) {
	t.Helper()
	originals := make(map[string]string, len(keys))
	for _, key := range keys {
		originals[key] = os.Getenv(key)
		_ = os.Unsetenv(key)
	}
	t.Cleanup(func() {
		for key, value := range originals {
			if value != "" {
				_ = os.Setenv(key, value)
			}
		}
	})
}

// skipShort skips the test if in short mode.
func skipShort(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
}

// invalidContainer returns a PostgresContainer with an unreachable endpoint.
func invalidContainer() *PostgresContainer {
	return &PostgresContainer{
		Host:     "localhost",
		Port:     "9999",
		Database: "invalid",
		User:     "invalid",
		Password: "invalid",
	}
}

// startPostgresWithDB starts a PostgreSQL container and returns a DB connection.
func startPostgresWithDB(t *testing.T) (*PostgresContainer, context.Context, *sql.DB) {
	t.Helper()
	skipShort(t)
	container := StartPostgres(t)
	ctx := context.Background()
	db, err := container.DB(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return container, ctx, db
}

// =============================================================================
// PostgresContainer Tests
// =============================================================================

func TestGetEnvOrDefault(t *testing.T) {
	t.Run("returns env value when set", func(t *testing.T) {
		_ = os.Setenv("TEST_CONTAINERS_KEY", "test-value")
		defer func() { _ = os.Unsetenv("TEST_CONTAINERS_KEY") }()

		result := getEnvOrDefault("TEST_CONTAINERS_KEY", "default")
		assert.Equal(t, "test-value", result)
	})

	t.Run("returns default when not set", func(t *testing.T) {
		_ = os.Unsetenv("TEST_CONTAINERS_MISSING_KEY")

		result := getEnvOrDefault("TEST_CONTAINERS_MISSING_KEY", "default")
		assert.Equal(t, "default", result)
	})
}

func TestDefaultPostgresConfig(t *testing.T) {
	// assertConfig verifies the given config fields match expected values.
	type expected struct {
		image, database, user, password, port string
	}
	assertConfig := func(t *testing.T, cfg *postgresConfig, want expected) {
		t.Helper()
		if want.image != "" {
			assert.Equal(t, want.image, cfg.image)
		}
		assert.Equal(t, want.database, cfg.database)
		assert.Equal(t, want.user, cfg.user)
		assert.Equal(t, want.password, cfg.password)
		assert.Equal(t, want.port, cfg.port)
	}

	t.Run("returns default values when no env set", func(t *testing.T) {
		clearTestEnvVars(t, []string{
			"POSTGRES_IMAGE", "POSTGRES_DB", "TEST_POSTGRES_DB",
			"POSTGRES_USER", "TEST_POSTGRES_USER", "POSTGRES_PASSWORD",
			"TEST_POSTGRES_PASSWORD", "POSTGRES_PORT", "TEST_POSTGRES_PORT",
		})
		assertConfig(t, defaultPostgresConfig(), expected{
			image: "postgres:17", database: "mink_test",
			user: "postgres", password: "postgres", port: "5432",
		})
	})

	t.Run("reads from environment variables", func(t *testing.T) {
		setTestEnvVars(t, map[string]string{
			"POSTGRES_IMAGE":    "postgres:16",
			"POSTGRES_DB":       "custom_db",
			"POSTGRES_USER":     "custom_user",
			"POSTGRES_PASSWORD": "custom_pass",
			"POSTGRES_PORT":     "5433",
		})
		assertConfig(t, defaultPostgresConfig(), expected{
			image: "postgres:16", database: "custom_db",
			user: "custom_user", password: "custom_pass", port: "5433",
		})
	})

	t.Run("fallback to TEST_ prefixed env vars", func(t *testing.T) {
		setTestEnvVars(t, map[string]string{
			"TEST_POSTGRES_DB":       "test_db",
			"TEST_POSTGRES_USER":     "test_user",
			"TEST_POSTGRES_PASSWORD": "test_pass",
			"TEST_POSTGRES_PORT":     "5434",
		})
		assertConfig(t, defaultPostgresConfig(), expected{
			database: "test_db", user: "test_user",
			password: "test_pass", port: "5434",
		})
	})
}

func TestPostgresOptions(t *testing.T) {
	tests := []struct {
		name   string
		option PostgresOption
		field  func(*postgresConfig) string
		want   string
	}{
		{"WithPostgresImage", WithPostgresImage("postgres:15"), func(c *postgresConfig) string { return c.image }, "postgres:15"},
		{"WithPostgresDatabase", WithPostgresDatabase("testdb"), func(c *postgresConfig) string { return c.database }, "testdb"},
		{"WithPostgresUser", WithPostgresUser("testuser"), func(c *postgresConfig) string { return c.user }, "testuser"},
		{"WithPostgresPassword", WithPostgresPassword("testpass"), func(c *postgresConfig) string { return c.password }, "testpass"},
		{"WithPostgresPort", WithPostgresPort("5433"), func(c *postgresConfig) string { return c.port }, "5433"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := defaultPostgresConfig()
			tt.option(cfg)
			assert.Equal(t, tt.want, tt.field(cfg))
		})
	}
}

func TestPostgresContainer_ConnectionString(t *testing.T) {
	t.Run("generates connection string", func(t *testing.T) {
		container := &PostgresContainer{
			Host:     "localhost",
			Port:     "5432",
			Database: "testdb",
			User:     "testuser",
			Password: "testpass",
		}

		connStr := container.ConnectionString()

		assert.Contains(t, connStr, "postgres://")
		assert.Contains(t, connStr, "testuser")
		assert.Contains(t, connStr, "testpass")
		assert.Contains(t, connStr, "localhost")
		assert.Contains(t, connStr, "5432")
		assert.Contains(t, connStr, "testdb")
	})

	t.Run("caches connection string", func(t *testing.T) {
		container := &PostgresContainer{
			connStr: "cached-connection-string",
		}

		assert.Equal(t, "cached-connection-string", container.ConnectionString())
	})
}

func TestQuoteIdentifier(t *testing.T) {
	tests := []struct {
		name, input, want string
	}{
		{"simple identifier", "myschema", `"myschema"`},
		{"identifier with numbers", "schema123", `"schema123"`},
		{"empty identifier", "", `""`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, quoteIdentifier(tt.input))
		})
	}
}

// =============================================================================
// Integration Test Options Tests
// =============================================================================

func TestIntegrationTestOptions(t *testing.T) {
	cfg := &integrationTestConfig{}

	WithSchemaPrefix("custom")(cfg)
	assert.Equal(t, "custom", cfg.schemaPrefix)

	WithTimeout(60 * time.Second)(cfg)
	assert.Equal(t, 60*time.Second, cfg.timeout)
}

// =============================================================================
// Integration Tests (require PostgreSQL)
// =============================================================================

func TestStartPostgres_Integration(t *testing.T) {
	skipShort(t)

	t.Run("starts PostgreSQL container", func(t *testing.T) {
		container := StartPostgres(t)

		assert.NotNil(t, container)
		assert.Equal(t, "localhost", container.Host)
		assert.NotEmpty(t, container.ConnectionString())
	})
}

func TestPostgresContainer_DB_Integration(t *testing.T) {
	skipShort(t)

	t.Run("returns database connection", func(t *testing.T) {
		container := StartPostgres(t)
		ctx := context.Background()

		db, err := container.DB(ctx)
		require.NoError(t, err)
		defer func() { _ = db.Close() }()

		assert.NotNil(t, db)

		// Verify connection works
		var result int
		err = db.QueryRowContext(ctx, "SELECT 1").Scan(&result)
		require.NoError(t, err)
		assert.Equal(t, 1, result)
	})

	t.Run("returns error for invalid connection", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		_, err := invalidContainer().DB(ctx)
		assert.Error(t, err)
	})
}

func TestPostgresContainer_MustDB_Integration(t *testing.T) {
	skipShort(t)

	t.Run("returns database connection", func(t *testing.T) {
		container := StartPostgres(t)
		ctx := context.Background()

		db := container.MustDB(ctx)
		defer func() { _ = db.Close() }()

		assert.NotNil(t, db)
	})

	t.Run("panics on invalid connection", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		assert.Panics(t, func() {
			invalidContainer().MustDB(ctx)
		})
	})
}

func TestPostgresContainer_Schema_Integration(t *testing.T) {
	container, ctx, db := startPostgresWithDB(t)

	t.Run("creates and drops schema", func(t *testing.T) {
		schema, err := container.CreateSchema(ctx, db, "test")
		require.NoError(t, err)
		assert.Contains(t, schema, "test_")

		// Verify schema exists
		var exists bool
		err = db.QueryRowContext(ctx,
			"SELECT EXISTS(SELECT 1 FROM information_schema.schemata WHERE schema_name = $1)",
			schema).Scan(&exists)
		require.NoError(t, err)
		assert.True(t, exists)

		// Drop schema
		err = container.DropSchema(ctx, db, schema)
		require.NoError(t, err)

		// Verify schema is dropped
		err = db.QueryRowContext(ctx,
			"SELECT EXISTS(SELECT 1 FROM information_schema.schemata WHERE schema_name = $1)",
			schema).Scan(&exists)
		require.NoError(t, err)
		assert.False(t, exists)
	})

	t.Run("CreateSchema fails with closed connection", func(t *testing.T) {
		closedDB, err := container.DB(ctx)
		require.NoError(t, err)
		_ = closedDB.Close()

		_, err = container.CreateSchema(ctx, closedDB, "test")
		assert.Error(t, err)
	})
}

// =============================================================================
// IntegrationTest Tests
// =============================================================================

func TestNewIntegrationTest_Integration(t *testing.T) {
	skipShort(t)

	t.Run("creates integration test environment", func(t *testing.T) {
		it := NewIntegrationTest(t)

		assert.NotNil(t, it.Context())
		assert.NotNil(t, it.DB())
		assert.NotEmpty(t, it.Schema())
		assert.NotNil(t, it.Container())
	})

	t.Run("with custom options", func(t *testing.T) {
		it := NewIntegrationTest(t,
			WithSchemaPrefix("custom"),
			WithTimeout(60*time.Second),
		)

		assert.Contains(t, it.Schema(), "custom_")
	})
}

func TestIntegrationTest_Methods_Integration(t *testing.T) {
	skipShort(t)

	it := NewIntegrationTest(t)

	assert.NotNil(t, it.Context(), "Context should not be nil")
	assert.NotNil(t, it.DB(), "DB should not be nil")
	assert.NotEmpty(t, it.Schema(), "Schema should not be empty")
	assert.NotNil(t, it.Container(), "Container should not be nil")
	assert.Contains(t, it.ConnectionString(), "search_path=", "ConnectionString should include schema")
}

func TestIntegrationTest_Exec_Integration(t *testing.T) {
	skipShort(t)

	t.Run("executes SQL", func(t *testing.T) {
		it := NewIntegrationTest(t)

		// Should not panic
		it.Exec("SELECT 1")
	})
}

func TestIntegrationTest_Query_Integration(t *testing.T) {
	skipShort(t)

	t.Run("executes query", func(t *testing.T) {
		it := NewIntegrationTest(t)

		rows := it.Query("SELECT 1 as num")
		defer func() { _ = rows.Close() }()

		require.True(t, rows.Next())

		var num int
		err := rows.Scan(&num)
		require.NoError(t, err)
		assert.Equal(t, 1, num)
	})
}

// =============================================================================
// FullStackTest Tests
// =============================================================================

func TestNewFullStackTest_Integration(t *testing.T) {
	skipShort(t)

	t.Run("creates full stack test environment", func(t *testing.T) {
		fst := NewFullStackTest(t)

		assert.NotNil(t, fst)
		assert.Contains(t, fst.Schema(), "fullstack_")
	})
}

func TestFullStackTest_SetupMinkSchema_Integration(t *testing.T) {
	skipShort(t)

	t.Run("creates mink tables", func(t *testing.T) {
		fst := NewFullStackTest(t)
		fst.SetupMinkSchema()

		assertTableExists := func(tableName string) {
			t.Helper()
			var exists bool
			err := fst.DB().QueryRowContext(fst.Context(),
				"SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_name = '"+tableName+"')").Scan(&exists)
			require.NoError(t, err)
			assert.True(t, exists, "table %s should exist", tableName)
		}

		assertTableExists("events")
		assertTableExists("checkpoints")
		assertTableExists("idempotency_keys")
	})
}
