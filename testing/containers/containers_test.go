package containers

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// PostgresContainer Tests
// =============================================================================

func TestGetEnvOrDefault(t *testing.T) {
	t.Run("returns env value when set", func(t *testing.T) {
		os.Setenv("TEST_CONTAINERS_KEY", "test-value")
		defer os.Unsetenv("TEST_CONTAINERS_KEY")

		result := getEnvOrDefault("TEST_CONTAINERS_KEY", "default")
		assert.Equal(t, "test-value", result)
	})

	t.Run("returns default when not set", func(t *testing.T) {
		os.Unsetenv("TEST_CONTAINERS_MISSING_KEY")

		result := getEnvOrDefault("TEST_CONTAINERS_MISSING_KEY", "default")
		assert.Equal(t, "default", result)
	})
}

func TestDefaultPostgresConfig(t *testing.T) {
	t.Run("returns default values when no env set", func(t *testing.T) {
		// Clear any env vars that might interfere
		envVars := []string{"POSTGRES_IMAGE", "POSTGRES_DB", "TEST_POSTGRES_DB",
			"POSTGRES_USER", "TEST_POSTGRES_USER", "POSTGRES_PASSWORD",
			"TEST_POSTGRES_PASSWORD", "POSTGRES_PORT", "TEST_POSTGRES_PORT"}
		originalValues := make(map[string]string)
		for _, key := range envVars {
			originalValues[key] = os.Getenv(key)
			os.Unsetenv(key)
		}
		defer func() {
			for key, value := range originalValues {
				if value != "" {
					os.Setenv(key, value)
				}
			}
		}()

		cfg := defaultPostgresConfig()

		assert.Equal(t, "postgres:17", cfg.image)
		assert.Equal(t, "mink_test", cfg.database)
		assert.Equal(t, "postgres", cfg.user)
		assert.Equal(t, "postgres", cfg.password)
		assert.Equal(t, "5432", cfg.port)
	})

	t.Run("reads from environment variables", func(t *testing.T) {
		os.Setenv("POSTGRES_IMAGE", "postgres:16")
		os.Setenv("POSTGRES_DB", "custom_db")
		os.Setenv("POSTGRES_USER", "custom_user")
		os.Setenv("POSTGRES_PASSWORD", "custom_pass")
		os.Setenv("POSTGRES_PORT", "5433")
		defer func() {
			os.Unsetenv("POSTGRES_IMAGE")
			os.Unsetenv("POSTGRES_DB")
			os.Unsetenv("POSTGRES_USER")
			os.Unsetenv("POSTGRES_PASSWORD")
			os.Unsetenv("POSTGRES_PORT")
		}()

		cfg := defaultPostgresConfig()

		assert.Equal(t, "postgres:16", cfg.image)
		assert.Equal(t, "custom_db", cfg.database)
		assert.Equal(t, "custom_user", cfg.user)
		assert.Equal(t, "custom_pass", cfg.password)
		assert.Equal(t, "5433", cfg.port)
	})

	t.Run("fallback to TEST_ prefixed env vars", func(t *testing.T) {
		os.Setenv("TEST_POSTGRES_DB", "test_db")
		os.Setenv("TEST_POSTGRES_USER", "test_user")
		os.Setenv("TEST_POSTGRES_PASSWORD", "test_pass")
		os.Setenv("TEST_POSTGRES_PORT", "5434")
		defer func() {
			os.Unsetenv("TEST_POSTGRES_DB")
			os.Unsetenv("TEST_POSTGRES_USER")
			os.Unsetenv("TEST_POSTGRES_PASSWORD")
			os.Unsetenv("TEST_POSTGRES_PORT")
		}()

		cfg := defaultPostgresConfig()

		assert.Equal(t, "test_db", cfg.database)
		assert.Equal(t, "test_user", cfg.user)
		assert.Equal(t, "test_pass", cfg.password)
		assert.Equal(t, "5434", cfg.port)
	})
}

func TestPostgresOptions(t *testing.T) {
	t.Run("WithPostgresImage", func(t *testing.T) {
		cfg := defaultPostgresConfig()
		WithPostgresImage("postgres:15")(cfg)
		assert.Equal(t, "postgres:15", cfg.image)
	})

	t.Run("WithPostgresDatabase", func(t *testing.T) {
		cfg := defaultPostgresConfig()
		WithPostgresDatabase("testdb")(cfg)
		assert.Equal(t, "testdb", cfg.database)
	})

	t.Run("WithPostgresUser", func(t *testing.T) {
		cfg := defaultPostgresConfig()
		WithPostgresUser("testuser")(cfg)
		assert.Equal(t, "testuser", cfg.user)
	})

	t.Run("WithPostgresPassword", func(t *testing.T) {
		cfg := defaultPostgresConfig()
		WithPostgresPassword("testpass")(cfg)
		assert.Equal(t, "testpass", cfg.password)
	})

	t.Run("WithPostgresPort", func(t *testing.T) {
		cfg := defaultPostgresConfig()
		WithPostgresPort("5433")(cfg)
		assert.Equal(t, "5433", cfg.port)
	})
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
	t.Run("quotes simple identifier", func(t *testing.T) {
		assert.Equal(t, `"myschema"`, quoteIdentifier("myschema"))
	})

	t.Run("quotes identifier with numbers", func(t *testing.T) {
		assert.Equal(t, `"schema123"`, quoteIdentifier("schema123"))
	})

	t.Run("quotes empty identifier", func(t *testing.T) {
		assert.Equal(t, `""`, quoteIdentifier(""))
	})
}

// =============================================================================
// Integration Test Options Tests
// =============================================================================

func TestIntegrationTestOptions(t *testing.T) {
	t.Run("WithSchemaPrefix", func(t *testing.T) {
		cfg := &integrationTestConfig{}
		WithSchemaPrefix("custom")(cfg)
		assert.Equal(t, "custom", cfg.schemaPrefix)
	})

	t.Run("WithTimeout", func(t *testing.T) {
		cfg := &integrationTestConfig{}
		WithTimeout(60 * time.Second)(cfg)
		assert.Equal(t, 60*time.Second, cfg.timeout)
	})
}

// =============================================================================
// Integration Tests (require PostgreSQL)
// =============================================================================

func TestStartPostgres_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	t.Run("starts PostgreSQL container", func(t *testing.T) {
		container := StartPostgres(t)

		assert.NotNil(t, container)
		assert.Equal(t, "localhost", container.Host)
		assert.NotEmpty(t, container.ConnectionString())
	})
}

func TestPostgresContainer_DB_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	t.Run("returns database connection", func(t *testing.T) {
		container := StartPostgres(t)
		ctx := context.Background()

		db, err := container.DB(ctx)
		require.NoError(t, err)
		defer db.Close()

		assert.NotNil(t, db)

		// Verify connection works
		var result int
		err = db.QueryRowContext(ctx, "SELECT 1").Scan(&result)
		require.NoError(t, err)
		assert.Equal(t, 1, result)
	})

	t.Run("returns error for invalid connection", func(t *testing.T) {
		container := &PostgresContainer{
			Host:     "localhost",
			Port:     "9999", // Invalid port
			Database: "invalid",
			User:     "invalid",
			Password: "invalid",
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		_, err := container.DB(ctx)
		assert.Error(t, err)
	})
}

func TestPostgresContainer_MustDB_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	t.Run("returns database connection", func(t *testing.T) {
		container := StartPostgres(t)
		ctx := context.Background()

		db := container.MustDB(ctx)
		defer db.Close()

		assert.NotNil(t, db)
	})

	t.Run("panics on invalid connection", func(t *testing.T) {
		container := &PostgresContainer{
			Host:     "localhost",
			Port:     "9999",
			Database: "invalid",
			User:     "invalid",
			Password: "invalid",
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		assert.Panics(t, func() {
			container.MustDB(ctx)
		})
	})
}

func TestPostgresContainer_Schema_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	t.Run("creates and drops schema", func(t *testing.T) {
		container := StartPostgres(t)
		ctx := context.Background()

		db, err := container.DB(ctx)
		require.NoError(t, err)
		defer db.Close()

		// Create schema
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
		container := StartPostgres(t)
		ctx := context.Background()

		db, err := container.DB(ctx)
		require.NoError(t, err)
		db.Close() // Close the connection

		_, err = container.CreateSchema(ctx, db, "test")
		assert.Error(t, err)
	})
}

// =============================================================================
// IntegrationTest Tests
// =============================================================================

func TestNewIntegrationTest_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

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
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	t.Run("Context returns context", func(t *testing.T) {
		it := NewIntegrationTest(t)
		assert.NotNil(t, it.Context())
	})

	t.Run("DB returns connection", func(t *testing.T) {
		it := NewIntegrationTest(t)
		assert.NotNil(t, it.DB())
	})

	t.Run("Schema returns schema name", func(t *testing.T) {
		it := NewIntegrationTest(t)
		assert.NotEmpty(t, it.Schema())
	})

	t.Run("Container returns container", func(t *testing.T) {
		it := NewIntegrationTest(t)
		assert.NotNil(t, it.Container())
	})

	t.Run("ConnectionString includes schema", func(t *testing.T) {
		it := NewIntegrationTest(t)
		assert.Contains(t, it.ConnectionString(), "search_path=")
	})
}

func TestIntegrationTest_Exec_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	t.Run("executes SQL", func(t *testing.T) {
		it := NewIntegrationTest(t)

		// Should not panic
		it.Exec("SELECT 1")
	})
}

func TestIntegrationTest_Query_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	t.Run("executes query", func(t *testing.T) {
		it := NewIntegrationTest(t)

		rows := it.Query("SELECT 1 as num")
		defer rows.Close()

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
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	t.Run("creates full stack test environment", func(t *testing.T) {
		fst := NewFullStackTest(t)

		assert.NotNil(t, fst)
		assert.Contains(t, fst.Schema(), "fullstack_")
	})
}

func TestFullStackTest_SetupMinkSchema_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	t.Run("creates mink tables", func(t *testing.T) {
		fst := NewFullStackTest(t)
		fst.SetupMinkSchema()

		// Verify events table exists
		var exists bool
		err := fst.DB().QueryRowContext(fst.Context(),
			"SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_name = 'events')").Scan(&exists)
		require.NoError(t, err)
		assert.True(t, exists)

		// Verify checkpoints table exists
		err = fst.DB().QueryRowContext(fst.Context(),
			"SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_name = 'checkpoints')").Scan(&exists)
		require.NoError(t, err)
		assert.True(t, exists)

		// Verify idempotency_keys table exists
		err = fst.DB().QueryRowContext(fst.Context(),
			"SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_name = 'idempotency_keys')").Scan(&exists)
		require.NoError(t, err)
		assert.True(t, exists)
	})
}
