// Package containers provides test container utilities for integration testing.
// It wraps testcontainers-go to provide easy-to-use database containers
// for testing event stores and adapters.
package containers

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// PostgresContainer represents a PostgreSQL test container.
type PostgresContainer struct {
	Host     string
	Port     string
	Database string
	User     string
	Password string
	connStr  string
}

// PostgresOption configures a PostgreSQL container.
type PostgresOption func(*postgresConfig)

type postgresConfig struct {
	image    string
	database string
	user     string
	password string
	port     string
}

// WithPostgresImage sets the PostgreSQL Docker image.
func WithPostgresImage(image string) PostgresOption {
	return func(c *postgresConfig) {
		c.image = image
	}
}

// WithPostgresDatabase sets the database name.
func WithPostgresDatabase(database string) PostgresOption {
	return func(c *postgresConfig) {
		c.database = database
	}
}

// WithPostgresUser sets the database user.
func WithPostgresUser(user string) PostgresOption {
	return func(c *postgresConfig) {
		c.user = user
	}
}

// WithPostgresPassword sets the database password.
func WithPostgresPassword(password string) PostgresOption {
	return func(c *postgresConfig) {
		c.password = password
	}
}

// WithPostgresPort sets the host port.
func WithPostgresPort(port string) PostgresOption {
	return func(c *postgresConfig) {
		c.port = port
	}
}

// getEnvOrDefault returns environment variable value or default.
func getEnvOrDefault(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}

// defaultPostgresConfig returns the default PostgreSQL configuration.
// Configuration is read from environment variables with fallback to defaults
// that match docker-compose.test.yml for seamless local testing.
//
// Environment variables:
//   - POSTGRES_IMAGE: Docker image (default: postgres:17)
//   - POSTGRES_DB or TEST_POSTGRES_DB: Database name (default: mink_test)
//   - POSTGRES_USER or TEST_POSTGRES_USER: Username (default: postgres)
//   - POSTGRES_PASSWORD or TEST_POSTGRES_PASSWORD: Password (default: postgres)
//   - POSTGRES_PORT or TEST_POSTGRES_PORT: Port (default: 5432)
func defaultPostgresConfig() *postgresConfig {
	return &postgresConfig{
		image:    getEnvOrDefault("POSTGRES_IMAGE", "postgres:17"),
		database: getEnvOrDefault("POSTGRES_DB", getEnvOrDefault("TEST_POSTGRES_DB", "mink_test")),
		user:     getEnvOrDefault("POSTGRES_USER", getEnvOrDefault("TEST_POSTGRES_USER", "postgres")),
		password: getEnvOrDefault("POSTGRES_PASSWORD", getEnvOrDefault("TEST_POSTGRES_PASSWORD", "postgres")),
		port:     getEnvOrDefault("POSTGRES_PORT", getEnvOrDefault("TEST_POSTGRES_PORT", "5432")),
	}
}

// StartPostgres starts a PostgreSQL test container.
// It uses the already running Docker container from docker-compose.test.yml
// in CI environments or can start a new container for local development.
//
// For full testcontainers integration, install testcontainers-go:
// go get github.com/testcontainers/testcontainers-go
//
// This implementation provides a lightweight alternative that works with
// the existing docker-compose.test.yml setup.
func StartPostgres(t *testing.T, opts ...PostgresOption) *PostgresContainer {
	t.Helper()

	cfg := defaultPostgresConfig()
	for _, opt := range opts {
		opt(cfg)
	}

	container := &PostgresContainer{
		Host:     "localhost",
		Port:     cfg.port,
		Database: cfg.database,
		User:     cfg.user,
		Password: cfg.password,
	}
	container.connStr = container.ConnectionString()

	// Wait for database to be ready
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := waitForPostgres(ctx, container.connStr); err != nil {
		t.Skipf("PostgreSQL not available (run docker-compose -f docker-compose.test.yml up -d): %v", err)
	}

	return container
}

// ConnectionString returns the PostgreSQL connection string.
func (c *PostgresContainer) ConnectionString() string {
	if c.connStr != "" {
		return c.connStr
	}
	return fmt.Sprintf(
		"postgres://%s:%s@%s:%s/%s?sslmode=disable",
		c.User, c.Password, c.Host, c.Port, c.Database,
	)
}

// DB returns a database connection.
func (c *PostgresContainer) DB(ctx context.Context) (*sql.DB, error) {
	db, err := sql.Open("pgx", c.ConnectionString())
	if err != nil {
		return nil, fmt.Errorf("containers: failed to open connection: %w", err)
	}

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("containers: failed to ping database: %w", err)
	}

	return db, nil
}

// MustDB returns a database connection or panics.
func (c *PostgresContainer) MustDB(ctx context.Context) *sql.DB {
	db, err := c.DB(ctx)
	if err != nil {
		panic(err)
	}
	return db
}

// CreateSchema creates a unique test schema.
func (c *PostgresContainer) CreateSchema(ctx context.Context, db *sql.DB, prefix string) (string, error) {
	schema := fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	_, err := db.ExecContext(ctx, fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %s", quoteIdentifier(schema)))
	if err != nil {
		return "", fmt.Errorf("containers: failed to create schema: %w", err)
	}
	return schema, nil
}

// DropSchema drops a test schema.
func (c *PostgresContainer) DropSchema(ctx context.Context, db *sql.DB, schema string) error {
	_, err := db.ExecContext(ctx, fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", quoteIdentifier(schema)))
	return err
}

// waitForPostgres waits for PostgreSQL to be ready.
func waitForPostgres(ctx context.Context, connStr string) error {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			db, err := sql.Open("pgx", connStr)
			if err != nil {
				continue
			}
			err = db.PingContext(ctx)
			db.Close()
			if err == nil {
				return nil
			}
		}
	}
}

// quoteIdentifier quotes a PostgreSQL identifier.
func quoteIdentifier(name string) string {
	return `"` + name + `"`
}

// =============================================================================
// Integration Test Helper
// =============================================================================

// IntegrationTest provides a complete integration test environment.
type IntegrationTest struct {
	t         *testing.T
	ctx       context.Context
	container *PostgresContainer
	db        *sql.DB
	schema    string
}

// IntegrationTestOption configures an integration test.
type IntegrationTestOption func(*integrationTestConfig)

type integrationTestConfig struct {
	schemaPrefix string
	timeout      time.Duration
}

// WithSchemaPrefix sets the schema prefix.
func WithSchemaPrefix(prefix string) IntegrationTestOption {
	return func(c *integrationTestConfig) {
		c.schemaPrefix = prefix
	}
}

// WithTimeout sets the test timeout.
func WithTimeout(timeout time.Duration) IntegrationTestOption {
	return func(c *integrationTestConfig) {
		c.timeout = timeout
	}
}

// NewIntegrationTest creates a new integration test environment.
func NewIntegrationTest(t *testing.T, opts ...IntegrationTestOption) *IntegrationTest {
	t.Helper()

	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	cfg := &integrationTestConfig{
		schemaPrefix: "test",
		timeout:      30 * time.Second,
	}
	for _, opt := range opts {
		opt(cfg)
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.timeout)
	t.Cleanup(cancel)

	container := StartPostgres(t)

	db, err := container.DB(ctx)
	if err != nil {
		t.Fatalf("Failed to connect to database: %v", err)
	}

	schema, err := container.CreateSchema(ctx, db, cfg.schemaPrefix)
	if err != nil {
		db.Close()
		t.Fatalf("Failed to create schema: %v", err)
	}

	it := &IntegrationTest{
		t:         t,
		ctx:       ctx,
		container: container,
		db:        db,
		schema:    schema,
	}

	t.Cleanup(func() {
		if err := container.DropSchema(context.Background(), db, schema); err != nil {
			t.Logf("Warning: failed to drop schema %s: %v", schema, err)
		}
		db.Close()
	})

	return it
}

// Context returns the test context.
func (it *IntegrationTest) Context() context.Context {
	return it.ctx
}

// DB returns the database connection.
func (it *IntegrationTest) DB() *sql.DB {
	return it.db
}

// Schema returns the test schema name.
func (it *IntegrationTest) Schema() string {
	return it.schema
}

// Container returns the PostgreSQL container.
func (it *IntegrationTest) Container() *PostgresContainer {
	return it.container
}

// ConnectionString returns the connection string with schema.
func (it *IntegrationTest) ConnectionString() string {
	return it.container.ConnectionString() + "&search_path=" + it.schema
}

// Exec executes a SQL statement.
func (it *IntegrationTest) Exec(query string, args ...interface{}) {
	it.t.Helper()
	_, err := it.db.ExecContext(it.ctx, query, args...)
	if err != nil {
		it.t.Fatalf("Failed to execute SQL: %v", err)
	}
}

// Query executes a SQL query.
func (it *IntegrationTest) Query(query string, args ...interface{}) *sql.Rows {
	it.t.Helper()
	rows, err := it.db.QueryContext(it.ctx, query, args...)
	if err != nil {
		it.t.Fatalf("Failed to execute query: %v", err)
	}
	return rows
}

// =============================================================================
// Test Fixture for Full Stack Testing
// =============================================================================

// FullStackTest provides complete end-to-end test infrastructure.
type FullStackTest struct {
	*IntegrationTest
}

// NewFullStackTest creates a new full stack test environment.
func NewFullStackTest(t *testing.T) *FullStackTest {
	t.Helper()
	return &FullStackTest{
		IntegrationTest: NewIntegrationTest(t, WithSchemaPrefix("fullstack")),
	}
}

// SetupMinkSchema creates the mink event store schema.
func (fst *FullStackTest) SetupMinkSchema() {
	fst.t.Helper()

	// Create events table
	fst.Exec(`
		CREATE TABLE IF NOT EXISTS events (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			stream_id VARCHAR(255) NOT NULL,
			version BIGINT NOT NULL,
			type VARCHAR(255) NOT NULL,
			data JSONB NOT NULL,
			metadata JSONB DEFAULT '{}',
			global_position BIGSERIAL,
			timestamp TIMESTAMPTZ DEFAULT NOW(),
			UNIQUE(stream_id, version)
		)
	`)

	// Create index for efficient stream queries
	fst.Exec(`CREATE INDEX IF NOT EXISTS idx_events_stream ON events(stream_id, version)`)
	fst.Exec(`CREATE INDEX IF NOT EXISTS idx_events_global ON events(global_position)`)
	fst.Exec(`CREATE INDEX IF NOT EXISTS idx_events_type ON events(type)`)

	// Create checkpoints table
	fst.Exec(`
		CREATE TABLE IF NOT EXISTS checkpoints (
			projection_name VARCHAR(255) PRIMARY KEY,
			position BIGINT NOT NULL,
			updated_at TIMESTAMPTZ DEFAULT NOW()
		)
	`)

	// Create idempotency table
	fst.Exec(`
		CREATE TABLE IF NOT EXISTS idempotency_keys (
			key VARCHAR(255) PRIMARY KEY,
			result JSONB NOT NULL,
			created_at TIMESTAMPTZ DEFAULT NOW(),
			expires_at TIMESTAMPTZ
		)
	`)
}
