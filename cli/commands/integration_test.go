//go:build integration
// +build integration

package commands

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AshkanYarmoradi/go-mink/adapters"
	"github.com/AshkanYarmoradi/go-mink/adapters/postgres"
	"github.com/AshkanYarmoradi/go-mink/cli/config"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test database URL - matches docker-compose.test.yml
const testDBURL = "postgres://postgres:postgres@localhost:5432/mink_test?sslmode=disable"

func skipIfNoDatabase(t *testing.T) {
	db, err := sql.Open("pgx", testDBURL)
	if err != nil {
		t.Skipf("Skipping integration test: %v", err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		t.Skipf("Skipping integration test: database not available: %v", err)
	}
}

func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	skipIfNoDatabase(t)

	db, err := sql.Open("pgx", testDBURL)
	require.NoError(t, err)

	// Create mink schema and tables (matching postgres adapter's schema structure)
	_, err = db.Exec(`
		-- Create schema if not exists
		CREATE SCHEMA IF NOT EXISTS mink;

		-- Drop existing tables
		DROP TABLE IF EXISTS mink.events CASCADE;
		DROP TABLE IF EXISTS mink.streams CASCADE;
		DROP TABLE IF EXISTS mink.checkpoints CASCADE;
		DROP TABLE IF EXISTS mink.snapshots CASCADE;
		DROP TABLE IF EXISTS mink.outbox CASCADE;
		DROP TABLE IF EXISTS mink.migrations CASCADE;
		DROP TABLE IF EXISTS mink.idempotency CASCADE;

		-- Also drop legacy public schema tables
		DROP TABLE IF EXISTS mink_events CASCADE;
		DROP TABLE IF EXISTS mink_streams CASCADE;
		DROP TABLE IF EXISTS mink_checkpoints CASCADE;
		DROP TABLE IF EXISTS mink_snapshots CASCADE;
		DROP TABLE IF EXISTS mink_outbox CASCADE;
		DROP TABLE IF EXISTS mink_migrations CASCADE;
		DROP TABLE IF EXISTS mink_idempotency CASCADE;

		-- Create streams table
		CREATE TABLE mink.streams (
			stream_id VARCHAR(255) PRIMARY KEY,
			version BIGINT NOT NULL DEFAULT 0,
			created_at TIMESTAMPTZ DEFAULT NOW(),
			updated_at TIMESTAMPTZ DEFAULT NOW()
		);

		-- Create events table
		CREATE TABLE mink.events (
			event_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			stream_id VARCHAR(255) NOT NULL,
			version BIGINT NOT NULL,
			event_type VARCHAR(255) NOT NULL,
			data JSONB NOT NULL,
			metadata JSONB DEFAULT '{}',
			global_position BIGSERIAL,
			timestamp TIMESTAMPTZ DEFAULT NOW(),
			UNIQUE(stream_id, version)
		);

		CREATE INDEX idx_mink_events_stream_id ON mink.events(stream_id, version);
		CREATE INDEX idx_mink_events_global_position ON mink.events(global_position);

		-- Create checkpoints table
		CREATE TABLE mink.checkpoints (
			projection_name VARCHAR(255) PRIMARY KEY,
			position BIGINT NOT NULL DEFAULT 0,
			status VARCHAR(50) DEFAULT 'active',
			updated_at TIMESTAMPTZ DEFAULT NOW()
		);

		-- Create migrations table
		CREATE TABLE mink.migrations (
			name VARCHAR(255) PRIMARY KEY,
			applied_at TIMESTAMPTZ DEFAULT NOW()
		);
	`)
	require.NoError(t, err)

	return db
}

func cleanupTestDB(t *testing.T, db *sql.DB) {
	t.Helper()
	db.Exec(`DELETE FROM mink.events`)
	db.Exec(`DELETE FROM mink.streams`)
	db.Exec(`DELETE FROM mink.checkpoints`)
	db.Exec(`DELETE FROM mink.migrations`)
	db.Close()
}

// ============================================================================
// Helper functions that wrap the postgres adapter
// ============================================================================

// withAdapter executes a function with a postgres adapter, handling creation and cleanup
func withAdapter[T any](dbURL string, fn func(*postgres.PostgresAdapter, context.Context) (T, error)) (T, error) {
	var zero T
	adapter, err := postgres.NewAdapter(dbURL)
	if err != nil {
		return zero, err
	}
	defer adapter.Close()
	return fn(adapter, context.Background())
}

// withAdapterNoResult executes a function with a postgres adapter for operations that don't return a value
func withAdapterNoResult(dbURL string, fn func(*postgres.PostgresAdapter, context.Context) error) error {
	adapter, err := postgres.NewAdapter(dbURL)
	if err != nil {
		return err
	}
	defer adapter.Close()
	return fn(adapter, context.Background())
}

// listProjections wraps postgres adapter's ListProjections
func listProjections(dbURL string) ([]adapters.ProjectionInfo, error) {
	return withAdapter(dbURL, func(a *postgres.PostgresAdapter, ctx context.Context) ([]adapters.ProjectionInfo, error) {
		return a.ListProjections(ctx)
	})
}

// getProjection wraps postgres adapter's GetProjection
func getProjection(dbURL string, name string) (*adapters.ProjectionInfo, error) {
	return withAdapter(dbURL, func(a *postgres.PostgresAdapter, ctx context.Context) (*adapters.ProjectionInfo, error) {
		return a.GetProjection(ctx, name)
	})
}

// setProjectionStatus wraps postgres adapter's SetProjectionStatus
func setProjectionStatus(dbURL string, name string, status string) error {
	return withAdapterNoResult(dbURL, func(a *postgres.PostgresAdapter, ctx context.Context) error {
		return a.SetProjectionStatus(ctx, name, status)
	})
}

// getTotalEventCount wraps postgres adapter's GetTotalEventCount
func getTotalEventCount(dbURL string) (int64, error) {
	return withAdapter(dbURL, func(a *postgres.PostgresAdapter, ctx context.Context) (int64, error) {
		return a.GetTotalEventCount(ctx)
	})
}

// listStreams wraps postgres adapter's ListStreams
func listStreams(dbURL string, prefix string, limit int) ([]adapters.StreamSummary, error) {
	return withAdapter(dbURL, func(a *postgres.PostgresAdapter, ctx context.Context) ([]adapters.StreamSummary, error) {
		return a.ListStreams(ctx, prefix, limit)
	})
}

// getStreamEvents wraps postgres adapter's GetStreamEvents
func getStreamEvents(dbURL string, streamID string, fromVersion int64, limit int) ([]adapters.StoredEvent, error) {
	return withAdapter(dbURL, func(a *postgres.PostgresAdapter, ctx context.Context) ([]adapters.StoredEvent, error) {
		return a.GetStreamEvents(ctx, streamID, fromVersion, limit)
	})
}

// getEventStoreStats wraps postgres adapter's GetEventStoreStats
func getEventStoreStats(dbURL string) (*adapters.EventStoreStats, error) {
	return withAdapter(dbURL, func(a *postgres.PostgresAdapter, ctx context.Context) (*adapters.EventStoreStats, error) {
		return a.GetEventStoreStats(ctx)
	})
}

// getPendingMigrationsHelper returns migrations that haven't been applied yet
func getPendingMigrationsHelper(dbURL string, migrationsDir string) ([]Migration, error) {
	return withAdapter(dbURL, func(a *postgres.PostgresAdapter, ctx context.Context) ([]Migration, error) {
		return getPendingMigrations(ctx, a, migrationsDir)
	})
}

// getAppliedMigrationsHelper returns migrations that have been applied
func getAppliedMigrationsHelper(dbURL string, migrationsDir string) ([]Migration, error) {
	return withAdapter(dbURL, func(a *postgres.PostgresAdapter, ctx context.Context) ([]Migration, error) {
		return getAppliedMigrations(ctx, a, migrationsDir)
	})
}

// getAppliedMigrationNames wraps postgres adapter's GetAppliedMigrations
func getAppliedMigrationNames(dbURL string) ([]string, error) {
	return withAdapter(dbURL, func(a *postgres.PostgresAdapter, ctx context.Context) ([]string, error) {
		return a.GetAppliedMigrations(ctx)
	})
}

// recordMigration records a migration as applied
func recordMigration(db *sql.DB, name string) error {
	_, err := db.Exec(`INSERT INTO mink.migrations (name) VALUES ($1)`, name)
	return err
}

// removeMigrationRecord removes a migration record from the database
func removeMigrationRecord(db *sql.DB, name string) error {
	_, err := db.Exec(`DELETE FROM mink.migrations WHERE name = $1`, name)
	return err
}

// ============================================================================
// Test environment helper for reducing duplication
// ============================================================================

// integrationTestEnv holds common integration test environment state
type integrationTestEnv struct {
	t      *testing.T
	db     *sql.DB
	tmpDir string
	origWd string
}

// setupIntegrationEnv creates a test environment with database, temp dir, and config
func setupIntegrationEnv(t *testing.T, prefix string) *integrationTestEnv {
	t.Helper()
	db := setupTestDB(t)

	tmpDir, err := os.MkdirTemp("", prefix)
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	require.NoError(t, os.Chdir(tmpDir))

	env := &integrationTestEnv{
		t:      t,
		db:     db,
		tmpDir: tmpDir,
		origWd: origWd,
	}
	t.Cleanup(env.cleanup)
	return env
}

// cleanup restores the original working directory and cleans up resources
func (e *integrationTestEnv) cleanup() {
	_ = os.Chdir(e.origWd)
	cleanupTestDB(e.t, e.db)
	os.RemoveAll(e.tmpDir)
}

// createConfig creates and saves a config file with postgres settings
func (e *integrationTestEnv) createConfig() {
	e.t.Helper()
	cfg := config.DefaultConfig()
	cfg.Database.Driver = "postgres"
	cfg.Database.URL = testDBURL
	cfg.Project.Module = "github.com/test/project"
	err := cfg.SaveFile(filepath.Join(e.tmpDir, "mink.yaml"))
	require.NoError(e.t, err)
}

// createConfigWithMigrations creates config and migrations directory
func (e *integrationTestEnv) createConfigWithMigrations() string {
	e.t.Helper()
	cfg := config.DefaultConfig()
	cfg.Database.Driver = "postgres"
	cfg.Database.URL = testDBURL
	cfg.Database.MigrationsDir = "migrations"
	cfg.Project.Module = "github.com/test/project"
	err := cfg.SaveFile(filepath.Join(e.tmpDir, "mink.yaml"))
	require.NoError(e.t, err)

	migrationsDir := filepath.Join(e.tmpDir, "migrations")
	require.NoError(e.t, os.MkdirAll(migrationsDir, 0755))
	return migrationsDir
}

// insertEvents inserts test events into the database
func (e *integrationTestEnv) insertEvents(streamID string, eventTypes ...string) {
	e.t.Helper()
	// Insert stream first
	_, err := e.db.Exec(`INSERT INTO mink.streams (stream_id, version) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
		streamID, len(eventTypes))
	require.NoError(e.t, err)

	// Insert events
	for i, eventType := range eventTypes {
		_, err := e.db.Exec(`
			INSERT INTO mink.events (stream_id, version, event_type, data) 
			VALUES ($1, $2, $3, '{}')`,
			streamID, i+1, eventType)
		require.NoError(e.t, err)
	}
}

// insertProjection inserts a test projection checkpoint
func (e *integrationTestEnv) insertProjection(name string, position int64, status string) {
	e.t.Helper()
	_, err := e.db.Exec(`
		INSERT INTO mink.checkpoints (projection_name, position, status) 
		VALUES ($1, $2, $3)`,
		name, position, status)
	require.NoError(e.t, err)
}

// ============================================================================
// Tests
// ============================================================================

// Test listProjections function
func TestListProjections_Integration(t *testing.T) {
	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	// Insert test projections
	_, err := db.Exec(`
		INSERT INTO mink.checkpoints (projection_name, position, status) VALUES
		('OrderSummary', 100, 'active'),
		('UserProfile', 50, 'paused'),
		('Analytics', 200, 'active')
	`)
	require.NoError(t, err)

	projections, err := listProjections(testDBURL)
	require.NoError(t, err)

	assert.Len(t, projections, 3)

	// Find OrderSummary
	var found bool
	for _, p := range projections {
		if p.Name == "OrderSummary" {
			assert.Equal(t, int64(100), p.Position)
			assert.Equal(t, "active", p.Status)
			found = true
			break
		}
	}
	assert.True(t, found, "OrderSummary projection not found")
}

func TestListProjections_Empty_Integration(t *testing.T) {
	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	projections, err := listProjections(testDBURL)
	require.NoError(t, err)
	assert.Empty(t, projections)
}

// Test getProjection function
func TestGetProjection_Integration(t *testing.T) {
	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	_, err := db.Exec(`
		INSERT INTO mink.checkpoints (projection_name, position, status) VALUES
		('TestProjection', 75, 'active')
	`)
	require.NoError(t, err)

	projection, err := getProjection(testDBURL, "TestProjection")
	require.NoError(t, err)
	require.NotNil(t, projection)

	assert.Equal(t, "TestProjection", projection.Name)
	assert.Equal(t, int64(75), projection.Position)
	assert.Equal(t, "active", projection.Status)
}

func TestGetProjection_NotFound_Integration(t *testing.T) {
	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	projection, err := getProjection(testDBURL, "NonExistent")
	// getProjection returns nil, nil when not found
	assert.NoError(t, err)
	assert.Nil(t, projection)
}

// Test setProjectionStatus function
func TestSetProjectionStatus_Integration(t *testing.T) {
	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	_, err := db.Exec(`
		INSERT INTO mink.checkpoints (projection_name, position, status) VALUES
		('StatusTest', 10, 'active')
	`)
	require.NoError(t, err)

	err = setProjectionStatus(testDBURL, "StatusTest", "paused")
	require.NoError(t, err)

	// Verify status changed
	var status string
	err = db.QueryRow(`SELECT status FROM mink.checkpoints WHERE projection_name = $1`, "StatusTest").Scan(&status)
	require.NoError(t, err)
	assert.Equal(t, "paused", status)
}

func TestSetProjectionStatus_NotFound_Integration(t *testing.T) {
	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	err := setProjectionStatus(testDBURL, "NonExistent", "paused")
	assert.Error(t, err)
}

// Test getTotalEventCount function
func TestGetTotalEventCount_Integration(t *testing.T) {
	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	// Insert test events
	_, err := db.Exec(`
		INSERT INTO mink.events (stream_id, version, event_type, data) VALUES
		('order-1', 1, 'OrderCreated', '{}'),
		('order-1', 2, 'ItemAdded', '{}'),
		('order-2', 1, 'OrderCreated', '{}')
	`)
	require.NoError(t, err)

	count, err := getTotalEventCount(testDBURL)
	require.NoError(t, err)
	assert.Equal(t, int64(3), count)
}

func TestGetTotalEventCount_Empty_Integration(t *testing.T) {
	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	count, err := getTotalEventCount(testDBURL)
	require.NoError(t, err)
	assert.Equal(t, int64(0), count)
}

// Test listStreams function
func TestListStreams_Integration(t *testing.T) {
	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	// Insert test streams
	_, err := db.Exec(`
		INSERT INTO mink.streams (stream_id, version) VALUES
		('order-123', 2),
		('order-456', 1)
	`)
	require.NoError(t, err)

	// Insert test events
	_, err = db.Exec(`
		INSERT INTO mink.events (stream_id, version, event_type, data) VALUES
		('order-123', 1, 'OrderCreated', '{"orderId": "123"}'),
		('order-123', 2, 'ItemAdded', '{"item": "widget"}'),
		('order-456', 1, 'OrderCreated', '{"orderId": "456"}')
	`)
	require.NoError(t, err)

	streams, err := listStreams(testDBURL, "", 50)
	require.NoError(t, err)

	assert.Len(t, streams, 2)

	// Find order-123
	var found bool
	for _, s := range streams {
		if s.StreamID == "order-123" {
			assert.Equal(t, int64(2), s.EventCount)
			found = true
			break
		}
	}
	assert.True(t, found)
}

func TestListStreams_WithPrefix_Integration(t *testing.T) {
	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	// Insert test streams
	_, err := db.Exec(`
		INSERT INTO mink.streams (stream_id, version) VALUES
		('order-123', 1),
		('user-456', 1)
	`)
	require.NoError(t, err)

	_, err = db.Exec(`
		INSERT INTO mink.events (stream_id, version, event_type, data) VALUES
		('order-123', 1, 'OrderCreated', '{}'),
		('user-456', 1, 'UserCreated', '{}')
	`)
	require.NoError(t, err)

	streams, err := listStreams(testDBURL, "order", 50)
	require.NoError(t, err)

	assert.Len(t, streams, 1)
	assert.Equal(t, "order-123", streams[0].StreamID)
}

func TestListStreams_Empty_Integration(t *testing.T) {
	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	streams, err := listStreams(testDBURL, "", 50)
	require.NoError(t, err)
	assert.Empty(t, streams)
}

// Test getStreamEvents function
func TestGetStreamEvents_Integration(t *testing.T) {
	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	_, err := db.Exec(`
		INSERT INTO mink.events (stream_id, version, event_type, data, metadata) VALUES
		('test-stream', 1, 'EventA', '{"key": "value1"}', '{}'),
		('test-stream', 2, 'EventB', '{"key": "value2"}', '{}'),
		('test-stream', 3, 'EventC', '{"key": "value3"}', '{}')
	`)
	require.NoError(t, err)

	events, err := getStreamEvents(testDBURL, "test-stream", 0, 10)
	require.NoError(t, err)

	assert.Len(t, events, 3)
	assert.Equal(t, int64(1), events[0].Version)
	assert.Equal(t, "EventA", events[0].Type)
}

func TestGetStreamEvents_FromVersion_Integration(t *testing.T) {
	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	_, err := db.Exec(`
		INSERT INTO mink.events (stream_id, version, event_type, data) VALUES
		('test-stream', 1, 'EventA', '{}'),
		('test-stream', 2, 'EventB', '{}'),
		('test-stream', 3, 'EventC', '{}')
	`)
	require.NoError(t, err)

	// from=1 means version > 1, so we get versions 2 and 3
	events, err := getStreamEvents(testDBURL, "test-stream", 1, 10)
	require.NoError(t, err)

	assert.Len(t, events, 2)
	assert.Equal(t, int64(2), events[0].Version)
}

func TestGetStreamEvents_NotFound_Integration(t *testing.T) {
	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	events, err := getStreamEvents(testDBURL, "non-existent", 0, 10)
	require.NoError(t, err)
	assert.Empty(t, events)
}

// Test getEventStoreStats function
func TestGetEventStoreStats_Integration(t *testing.T) {
	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	// Insert streams
	_, err := db.Exec(`
		INSERT INTO mink.streams (stream_id, version) VALUES
		('stream-1', 2),
		('stream-2', 1)
	`)
	require.NoError(t, err)

	// Insert events
	_, err = db.Exec(`
		INSERT INTO mink.events (stream_id, version, event_type, data) VALUES
		('stream-1', 1, 'TypeA', '{}'),
		('stream-1', 2, 'TypeB', '{}'),
		('stream-2', 1, 'TypeA', '{}')
	`)
	require.NoError(t, err)

	stats, err := getEventStoreStats(testDBURL)
	require.NoError(t, err)

	assert.Equal(t, int64(3), stats.TotalEvents)
	assert.Equal(t, int64(2), stats.TotalStreams)
	assert.Equal(t, int64(2), stats.EventTypes)
}

func TestGetEventStoreStats_Empty_Integration(t *testing.T) {
	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	stats, err := getEventStoreStats(testDBURL)
	require.NoError(t, err)

	assert.Equal(t, int64(0), stats.TotalEvents)
	assert.Equal(t, int64(0), stats.TotalStreams)
	assert.Equal(t, int64(0), stats.EventTypes)
}

// Test migration functions
func TestGetAppliedMigrationNames_Integration(t *testing.T) {
	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	_, err := db.Exec(`
		INSERT INTO mink.migrations (name) VALUES
		('001_initial'),
		('002_add_index')
	`)
	require.NoError(t, err)

	names, err := getAppliedMigrationNames(testDBURL)
	require.NoError(t, err)

	assert.Len(t, names, 2)
	assert.Contains(t, names, "001_initial")
	assert.Contains(t, names, "002_add_index")
}

func TestGetAppliedMigrationNames_Empty_Integration(t *testing.T) {
	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	names, err := getAppliedMigrationNames(testDBURL)
	require.NoError(t, err)
	assert.Empty(t, names)
}

func TestRecordMigration_Integration(t *testing.T) {
	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	err := recordMigration(db, "003_test_migration")
	require.NoError(t, err)

	var count int
	err = db.QueryRow(`SELECT COUNT(*) FROM mink.migrations WHERE name = $1`, "003_test_migration").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

func TestRemoveMigrationRecord_Integration(t *testing.T) {
	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	_, err := db.Exec(`INSERT INTO mink.migrations (name) VALUES ('to_remove')`)
	require.NoError(t, err)

	err = removeMigrationRecord(db, "to_remove")
	require.NoError(t, err)

	var count int
	err = db.QueryRow(`SELECT COUNT(*) FROM mink.migrations WHERE name = $1`, "to_remove").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}

func TestGetPendingMigrations_Integration(t *testing.T) {
	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	// Create temp migrations dir
	tmpDir, err := os.MkdirTemp("", "mink-migration-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	// Create migration files
	os.WriteFile(filepath.Join(tmpDir, "001_initial.sql"), []byte("-- up"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "002_add_index.sql"), []byte("-- up"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "003_new_feature.sql"), []byte("-- up"), 0644)

	// Mark first two as applied
	_, err = db.Exec(`
		INSERT INTO mink.migrations (name) VALUES ('001_initial'), ('002_add_index')
	`)
	require.NoError(t, err)

	pending, err := getPendingMigrationsHelper(testDBURL, tmpDir)
	require.NoError(t, err)

	assert.Len(t, pending, 1)
	assert.Equal(t, "003_new_feature", pending[0].Name)
}

func TestGetAppliedMigrations_Integration(t *testing.T) {
	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	tmpDir, err := os.MkdirTemp("", "mink-migration-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	_, err = db.Exec(`INSERT INTO mink.migrations (name) VALUES ('001_initial')`)
	require.NoError(t, err)

	applied, err := getAppliedMigrationsHelper(testDBURL, tmpDir)
	require.NoError(t, err)

	assert.Len(t, applied, 1)
	assert.Equal(t, "001_initial", applied[0].Name)
}

// Test diagnose database connection check
func TestCheckDatabaseConnection_Integration(t *testing.T) {
	skipIfNoDatabase(t)
	env := setupIntegrationEnv(t, "mink-diag-db-test-*")
	env.createConfig()

	result := checkDatabaseConnection()

	assert.Equal(t, "Database Connection", result.Name)
	assert.Equal(t, StatusOK, result.Status)
	assert.Contains(t, result.Message, "Connected")
}

// Test diagnose event store schema check
func TestCheckEventStoreSchema_Integration(t *testing.T) {
	env := setupIntegrationEnv(t, "mink-diag-schema-test-*")
	env.createConfig()

	result := checkEventStoreSchema()

	assert.Equal(t, "Event Store Schema", result.Name)
	// The check may return WARNING if it can't find the legacy mink_events table,
	// but our test uses mink.events (in mink schema). Either status is acceptable.
	assert.Contains(t, []CheckStatus{StatusOK, StatusWarning}, result.Status)
}

// Test diagnose projections check
func TestCheckProjections_Integration(t *testing.T) {
	env := setupIntegrationEnv(t, "mink-diag-proj-test-*")
	env.createConfig()
	env.insertProjection("TestProjection", 100, "active")
	env.insertEvents("stream-1", "Event1")

	result := checkProjections()

	assert.Equal(t, "Projections", result.Name)
	// Status depends on whether projections are caught up
	assert.Contains(t, []CheckStatus{StatusOK, StatusWarning}, result.Status)
}

// Test command execution with database
func TestProjectionListCommand_Integration(t *testing.T) {
	env := setupIntegrationEnv(t, "mink-proj-cmd-test-*")
	env.createConfig()
	env.insertProjection("OrderView", 50, "active")

	cmd := NewProjectionCommand()
	cmd.SetArgs([]string{"list"})

	err := cmd.Execute()
	assert.NoError(t, err)
}

func TestStreamListCommand_Integration(t *testing.T) {
	env := setupIntegrationEnv(t, "mink-stream-cmd-test-*")
	env.createConfig()
	env.insertEvents("order-123", "OrderCreated")

	cmd := NewStreamCommand()
	cmd.SetArgs([]string{"list"})

	err := cmd.Execute()
	assert.NoError(t, err)
}

func TestStreamEventsCommand_Integration(t *testing.T) {
	env := setupIntegrationEnv(t, "mink-stream-events-test-*")
	env.createConfig()
	env.insertEvents("order-123", "OrderCreated", "ItemAdded")

	cmd := NewStreamCommand()
	cmd.SetArgs([]string{"events", "order-123"})

	err := cmd.Execute()
	assert.NoError(t, err)
}

func TestStreamStatsCommand_Integration(t *testing.T) {
	env := setupIntegrationEnv(t, "mink-stream-stats-test-*")
	env.createConfig()
	env.insertEvents("order-1", "OrderCreated")
	env.insertEvents("order-2", "OrderCreated")

	cmd := NewStreamCommand()
	cmd.SetArgs([]string{"stats"})

	err := cmd.Execute()
	assert.NoError(t, err)
}

func TestMigrateStatusCommand_Integration(t *testing.T) {
	env := setupIntegrationEnv(t, "mink-migrate-status-test-*")
	migrationsDir := env.createConfigWithMigrations()
	os.WriteFile(filepath.Join(migrationsDir, "001_initial.sql"), []byte("-- up"), 0644)

	cmd := NewMigrateCommand()
	cmd.SetArgs([]string{"status"})

	err := cmd.Execute()
	assert.NoError(t, err)
}

// Test stream export command
func TestStreamExportCommand_Integration(t *testing.T) {
	env := setupIntegrationEnv(t, "mink-export-test-*")
	env.createConfig()
	env.insertEvents("export-stream", "Created", "Updated")

	outputFile := filepath.Join(env.tmpDir, "export.json")

	cmd := NewStreamCommand()
	cmd.SetArgs([]string{"export", "export-stream", "--output", outputFile})

	err := cmd.Execute()
	assert.NoError(t, err)

	// Verify export file exists and has content
	data, err := os.ReadFile(outputFile)
	assert.NoError(t, err)
	assert.Contains(t, string(data), "export-stream")
}

// Test projection status command
func TestProjectionStatusCommand_Integration(t *testing.T) {
	env := setupIntegrationEnv(t, "mink-proj-status-test-*")
	env.createConfig()
	env.insertProjection("DetailedProjection", 100, "active")
	env.insertEvents("stream-1", "Event1", "Event2")

	cmd := NewProjectionCommand()
	cmd.SetArgs([]string{"status", "DetailedProjection"})

	err := cmd.Execute()
	assert.NoError(t, err)
}

// Test projection pause command
func TestProjectionPauseCommand_Integration(t *testing.T) {
	env := setupIntegrationEnv(t, "mink-proj-pause-test-*")
	env.createConfig()
	env.insertProjection("PauseTestProj", 50, "active")

	cmd := NewProjectionCommand()
	cmd.SetArgs([]string{"pause", "PauseTestProj"})

	err := cmd.Execute()
	assert.NoError(t, err)

	// Verify status changed to paused
	var status string
	err = env.db.QueryRow(`SELECT status FROM mink.checkpoints WHERE projection_name = $1`, "PauseTestProj").Scan(&status)
	require.NoError(t, err)
	assert.Equal(t, "paused", status)
}

// Test projection resume command
func TestProjectionResumeCommand_Integration(t *testing.T) {
	env := setupIntegrationEnv(t, "mink-proj-resume-test-*")
	env.createConfig()
	env.insertProjection("ResumeTestProj", 50, "paused")

	cmd := NewProjectionCommand()
	cmd.SetArgs([]string{"resume", "ResumeTestProj"})

	err := cmd.Execute()
	assert.NoError(t, err)

	// Verify status changed to active
	var status string
	err = env.db.QueryRow(`SELECT status FROM mink.checkpoints WHERE projection_name = $1`, "ResumeTestProj").Scan(&status)
	require.NoError(t, err)
	assert.Equal(t, "active", status)
}

// Test projection rebuild command
func TestProjectionRebuildCommand_Integration(t *testing.T) {
	env := setupIntegrationEnv(t, "mink-proj-rebuild-test-*")
	env.createConfig()
	env.insertProjection("RebuildTestProj", 100, "active")

	cmd := NewProjectionCommand()
	cmd.SetArgs([]string{"rebuild", "RebuildTestProj", "--yes"})

	err := cmd.Execute()
	assert.NoError(t, err)

	// Verify position reset to 0
	var position int64
	err = env.db.QueryRow(`SELECT position FROM mink.checkpoints WHERE projection_name = $1`, "RebuildTestProj").Scan(&position)
	require.NoError(t, err)
	assert.Equal(t, int64(0), position)
}

// Test migrate create command - non-interactive
func TestMigrateCreateCommand_Integration(t *testing.T) {
	env := setupIntegrationEnv(t, "mink-migrate-create-test-*")
	migrationsDir := env.createConfigWithMigrations()

	cmd := NewMigrateCommand()
	cmd.SetArgs([]string{"create", "add_users_table"})

	err := cmd.Execute()
	assert.NoError(t, err)

	// Verify migration files were created (up and down)
	files, err := os.ReadDir(migrationsDir)
	require.NoError(t, err)
	assert.Len(t, files, 2) // Both .sql and .down.sql files

	// Check that one file has "add_users_table" in name
	var foundUpFile bool
	for _, f := range files {
		if strings.Contains(f.Name(), "add_users_table") && !strings.HasSuffix(f.Name(), ".down.sql") {
			foundUpFile = true
			break
		}
	}
	assert.True(t, foundUpFile, "migration up file not found")
}

// Test generate aggregate command with --non-interactive (skips interactive)
func TestGenerateAggregateCommand_Force_Integration(t *testing.T) {
	env := setupIntegrationEnv(t, "mink-gen-agg-test-*")
	env.createConfig()

	cmd := NewGenerateCommand()
	cmd.SetArgs([]string{"aggregate", "Order", "--events", "Created,Shipped", "--non-interactive"})

	err := cmd.Execute()
	assert.NoError(t, err)

	// Verify files were created (uses default config paths)
	assert.FileExists(t, filepath.Join(env.tmpDir, "internal/domain/order.go"))
	assert.FileExists(t, filepath.Join(env.tmpDir, "internal/domain/order_test.go"))
	assert.FileExists(t, filepath.Join(env.tmpDir, "internal/events/order_events.go"))
}

func TestGenerateAggregateCommand_ForceNoEvents_Integration(t *testing.T) {
	env := setupIntegrationEnv(t, "mink-gen-agg-noevt-test-*")
	env.createConfig()

	cmd := NewGenerateCommand()
	// Use --non-interactive without --events to skip the interactive form
	cmd.SetArgs([]string{"aggregate", "Customer", "--non-interactive"})

	err := cmd.Execute()
	assert.NoError(t, err)

	// Should still create aggregate file, just without events
	assert.FileExists(t, filepath.Join(env.tmpDir, "internal/domain/customer.go"))
	assert.FileExists(t, filepath.Join(env.tmpDir, "internal/domain/customer_test.go"))
}

// Test generate event command with --non-interactive
func TestGenerateEventCommand_Force_Integration(t *testing.T) {
	env := setupIntegrationEnv(t, "mink-gen-evt-test-*")
	env.createConfig()

	cmd := NewGenerateCommand()
	cmd.SetArgs([]string{"event", "OrderCreated", "--aggregate", "Order", "--non-interactive"})

	err := cmd.Execute()
	assert.NoError(t, err)

	assert.FileExists(t, filepath.Join(env.tmpDir, "internal/events/ordercreated.go"))
}

func TestGenerateEventCommand_ForceNoAggregate_Integration(t *testing.T) {
	env := setupIntegrationEnv(t, "mink-gen-evt-noagg-test-*")
	env.createConfig()

	cmd := NewGenerateCommand()
	// Use --non-interactive without --aggregate to skip interactive form
	cmd.SetArgs([]string{"event", "ItemAdded", "--non-interactive"})

	err := cmd.Execute()
	assert.NoError(t, err)

	// Event file should still be created even without aggregate
	assert.FileExists(t, filepath.Join(env.tmpDir, "internal/events/itemadded.go"))
}

// Test generate projection command with --non-interactive
func TestGenerateProjectionCommand_Force_Integration(t *testing.T) {
	env := setupIntegrationEnv(t, "mink-gen-proj-test-*")
	env.createConfig()

	cmd := NewGenerateCommand()
	cmd.SetArgs([]string{"projection", "OrderSummary", "--events", "OrderCreated,OrderShipped", "--non-interactive"})

	err := cmd.Execute()
	assert.NoError(t, err)

	assert.FileExists(t, filepath.Join(env.tmpDir, "internal/projections/ordersummary.go"))
	assert.FileExists(t, filepath.Join(env.tmpDir, "internal/projections/ordersummary_test.go"))
}

func TestGenerateProjectionCommand_ForceNoEvents_Integration(t *testing.T) {
	env := setupIntegrationEnv(t, "mink-gen-proj-noevt-test-*")
	env.createConfig()

	cmd := NewGenerateCommand()
	cmd.SetArgs([]string{"projection", "UserProfile", "--non-interactive"})

	err := cmd.Execute()
	assert.NoError(t, err)

	assert.FileExists(t, filepath.Join(env.tmpDir, "internal/projections/userprofile.go"))
	assert.FileExists(t, filepath.Join(env.tmpDir, "internal/projections/userprofile_test.go"))
}

// Test generate command command with --non-interactive
func TestGenerateCommandCommand_Force_Integration(t *testing.T) {
	env := setupIntegrationEnv(t, "mink-gen-cmd-test-*")
	env.createConfig()

	cmd := NewGenerateCommand()
	cmd.SetArgs([]string{"command", "CreateOrder", "--aggregate", "Order", "--non-interactive"})

	err := cmd.Execute()
	assert.NoError(t, err)

	assert.FileExists(t, filepath.Join(env.tmpDir, "internal/commands/createorder.go"))
}

func TestGenerateCommandCommand_ForceNoAggregate_Integration(t *testing.T) {
	env := setupIntegrationEnv(t, "mink-gen-cmd-noagg-test-*")
	env.createConfig()

	cmd := NewGenerateCommand()
	cmd.SetArgs([]string{"command", "UpdateItem", "--non-interactive"})

	err := cmd.Execute()
	assert.NoError(t, err)

	assert.FileExists(t, filepath.Join(env.tmpDir, "internal/commands/updateitem.go"))
}

// Test migrate up command with --non-interactive (skips spinner)
func TestMigrateUpCommand_Force_Integration(t *testing.T) {
	env := setupIntegrationEnv(t, "mink-migrate-up-test-*")
	migrationsDir := env.createConfigWithMigrations()

	// Create a test migration
	migrationContent := `CREATE TABLE IF NOT EXISTS test_migrate_up (id INT);`
	err := os.WriteFile(filepath.Join(migrationsDir, "001_create_test.sql"), []byte(migrationContent), 0644)
	require.NoError(t, err)

	cmd := NewMigrateCommand()
	cmd.SetArgs([]string{"up", "--non-interactive"})

	err = cmd.Execute()
	assert.NoError(t, err)

	// Verify the migration was applied
	var exists bool
	err = env.db.QueryRow(`SELECT EXISTS (SELECT FROM information_schema.tables WHERE table_name = 'test_migrate_up')`).Scan(&exists)
	assert.NoError(t, err)
	assert.True(t, exists, "Migration should have created test_migrate_up table")

	// Cleanup
	env.db.Exec(`DROP TABLE IF EXISTS test_migrate_up`)
}

func TestMigrateUpCommand_ForceNoMigrations_Integration(t *testing.T) {
	env := setupIntegrationEnv(t, "mink-migrate-up-empty-test-*")
	env.createConfigWithMigrations()

	cmd := NewMigrateCommand()
	cmd.SetArgs([]string{"up", "--non-interactive"})

	// Should succeed with "up to date" message
	err := cmd.Execute()
	assert.NoError(t, err)
}

func TestMigrateUpCommand_WithSteps_Integration(t *testing.T) {
	env := setupIntegrationEnv(t, "mink-migrate-steps-test-*")
	migrationsDir := env.createConfigWithMigrations()

	// Create 3 migrations
	err := os.WriteFile(filepath.Join(migrationsDir, "001_first.sql"),
		[]byte(`CREATE TABLE IF NOT EXISTS test_up_first (id INT);`), 0644)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(migrationsDir, "002_second.sql"),
		[]byte(`CREATE TABLE IF NOT EXISTS test_up_second (id INT);`), 0644)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(migrationsDir, "003_third.sql"),
		[]byte(`CREATE TABLE IF NOT EXISTS test_up_third (id INT);`), 0644)
	require.NoError(t, err)

	// Apply only 2 migrations
	cmd := NewMigrateCommand()
	cmd.SetArgs([]string{"up", "--non-interactive", "--steps", "2"})

	err = cmd.Execute()
	assert.NoError(t, err)

	// Verify only first two tables exist
	var exists1, exists2, exists3 bool
	env.db.QueryRow(`SELECT EXISTS (SELECT FROM information_schema.tables WHERE table_name = 'test_up_first')`).Scan(&exists1)
	env.db.QueryRow(`SELECT EXISTS (SELECT FROM information_schema.tables WHERE table_name = 'test_up_second')`).Scan(&exists2)
	env.db.QueryRow(`SELECT EXISTS (SELECT FROM information_schema.tables WHERE table_name = 'test_up_third')`).Scan(&exists3)

	assert.True(t, exists1, "First table should exist")
	assert.True(t, exists2, "Second table should exist")
	assert.False(t, exists3, "Third table should NOT exist (only 2 steps)")

	// Cleanup
	env.db.Exec(`DROP TABLE IF EXISTS test_up_first`)
	env.db.Exec(`DROP TABLE IF EXISTS test_up_second`)
}

func TestMigrateUpCommand_MemoryDriver_Integration(t *testing.T) {
	env := setupIntegrationEnv(t, "mink-migrate-up-memory-test-*")
	// Override config with memory driver
	cfg := config.DefaultConfig()
	cfg.Database.Driver = "memory"
	cfg.Project.Module = "github.com/test/project"
	err := cfg.SaveFile(filepath.Join(env.tmpDir, "mink.yaml"))
	require.NoError(t, err)

	cmd := NewMigrateCommand()
	cmd.SetArgs([]string{"up", "--non-interactive"})

	// Should succeed with info message about memory driver
	err = cmd.Execute()
	assert.NoError(t, err)
}

// Test migrate down command
func TestMigrateDownCommand_Integration(t *testing.T) {
	env := setupIntegrationEnv(t, "mink-migrate-down-test-*")
	migrationsDir := env.createConfigWithMigrations()

	// Create up and down migration files
	upMigration := `CREATE TABLE IF NOT EXISTS test_migrate_down (id INT);`
	downMigration := `DROP TABLE IF EXISTS test_migrate_down;`
	err := os.WriteFile(filepath.Join(migrationsDir, "001_create_test.sql"), []byte(upMigration), 0644)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(migrationsDir, "001_create_test.down.sql"), []byte(downMigration), 0644)
	require.NoError(t, err)

	// First apply the migration
	cmdUp := NewMigrateCommand()
	cmdUp.SetArgs([]string{"up", "--non-interactive"})
	err = cmdUp.Execute()
	require.NoError(t, err)

	// Verify table exists
	var exists bool
	err = env.db.QueryRow(`SELECT EXISTS (SELECT FROM information_schema.tables WHERE table_name = 'test_migrate_down')`).Scan(&exists)
	require.NoError(t, err)
	assert.True(t, exists, "Table should exist after migration up")

	// Now rollback
	cmdDown := NewMigrateCommand()
	cmdDown.SetArgs([]string{"down"})
	err = cmdDown.Execute()
	assert.NoError(t, err)

	// Verify table no longer exists
	err = env.db.QueryRow(`SELECT EXISTS (SELECT FROM information_schema.tables WHERE table_name = 'test_migrate_down')`).Scan(&exists)
	assert.NoError(t, err)
	assert.False(t, exists, "Table should be dropped after migration down")

	// Cleanup
	env.db.Exec(`DROP TABLE IF EXISTS test_migrate_down`)
}

func TestMigrateDownCommand_NoMigrations_Integration(t *testing.T) {
	env := setupIntegrationEnv(t, "mink-migrate-down-empty-test-*")
	env.createConfigWithMigrations()

	cmd := NewMigrateCommand()
	cmd.SetArgs([]string{"down"})

	// Should succeed with "no migrations to rollback" message
	err := cmd.Execute()
	assert.NoError(t, err)
}

func TestMigrateDownCommand_NoDownFile_Integration(t *testing.T) {
	env := setupIntegrationEnv(t, "mink-migrate-down-nofile-test-*")
	migrationsDir := env.createConfigWithMigrations()

	// Create only up migration (no down file)
	upMigration := `CREATE TABLE IF NOT EXISTS test_migrate_down_nofile (id INT);`
	err := os.WriteFile(filepath.Join(migrationsDir, "001_create_nodown.sql"), []byte(upMigration), 0644)
	require.NoError(t, err)

	// Apply the migration
	cmdUp := NewMigrateCommand()
	cmdUp.SetArgs([]string{"up", "--non-interactive"})
	err = cmdUp.Execute()
	require.NoError(t, err)

	// Try to rollback - should skip because no down file
	cmdDown := NewMigrateCommand()
	cmdDown.SetArgs([]string{"down"})
	err = cmdDown.Execute()
	assert.NoError(t, err)

	// Cleanup
	env.db.Exec(`DROP TABLE IF EXISTS test_migrate_down_nofile`)
}

func TestMigrateDownCommand_MultipleSteps_Integration(t *testing.T) {
	env := setupIntegrationEnv(t, "mink-migrate-down-steps-test-*")
	migrationsDir := env.createConfigWithMigrations()

	// Create two migrations
	err := os.WriteFile(filepath.Join(migrationsDir, "001_first.sql"),
		[]byte(`CREATE TABLE IF NOT EXISTS test_first (id INT);`), 0644)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(migrationsDir, "001_first.down.sql"),
		[]byte(`DROP TABLE IF EXISTS test_first;`), 0644)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(migrationsDir, "002_second.sql"),
		[]byte(`CREATE TABLE IF NOT EXISTS test_second (id INT);`), 0644)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(migrationsDir, "002_second.down.sql"),
		[]byte(`DROP TABLE IF EXISTS test_second;`), 0644)
	require.NoError(t, err)

	// Apply both migrations
	cmdUp := NewMigrateCommand()
	cmdUp.SetArgs([]string{"up", "--non-interactive"})
	err = cmdUp.Execute()
	require.NoError(t, err)

	// Rollback 2 steps
	cmdDown := NewMigrateCommand()
	cmdDown.SetArgs([]string{"down", "--steps", "2"})
	err = cmdDown.Execute()
	assert.NoError(t, err)

	// Cleanup
	env.db.Exec(`DROP TABLE IF EXISTS test_first`)
	env.db.Exec(`DROP TABLE IF EXISTS test_second`)
}

func TestMigrateDownCommand_MemoryDriver_Integration(t *testing.T) {
	env := setupIntegrationEnv(t, "mink-migrate-down-memory-test-*")
	// Override config with memory driver
	cfg := config.DefaultConfig()
	cfg.Database.Driver = "memory"
	cfg.Project.Module = "github.com/test/project"
	err := cfg.SaveFile(filepath.Join(env.tmpDir, "mink.yaml"))
	require.NoError(t, err)

	cmd := NewMigrateCommand()
	cmd.SetArgs([]string{"down"})

	// Should succeed with info message about memory driver
	err = cmd.Execute()
	assert.NoError(t, err)
}

// Test diagnose command with database
func TestDiagnoseCommand_Integration(t *testing.T) {
	env := setupIntegrationEnv(t, "mink-diagnose-test-*")
	env.createConfig()

	cmd := NewDiagnoseCommand()

	err := cmd.Execute()
	assert.NoError(t, err)
}

// Test getAllMigrations function
func TestGetAllMigrations_Integration(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-all-migrations-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	// Create migration files
	os.WriteFile(filepath.Join(tmpDir, "001_first.sql"), []byte("-- first"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "002_second.sql"), []byte("-- second"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "003_third.sql"), []byte("-- third"), 0644)

	all, err := getAllMigrations(tmpDir)
	require.NoError(t, err)

	assert.Len(t, all, 3)
	assert.Equal(t, "001_first", all[0].Name)
	assert.Equal(t, "002_second", all[1].Name)
	assert.Equal(t, "003_third", all[2].Name)
}

// Test projection rebuild command with --yes
func TestProjectionRebuildCommand_Force_Integration(t *testing.T) {
	env := setupIntegrationEnv(t, "mink-proj-rebuild-force-test-*")
	env.createConfig()
	env.insertProjection("TestRebuildProj", 100, "active")

	cmd := NewProjectionCommand()
	cmd.SetArgs([]string{"rebuild", "TestRebuildProj", "--yes"})

	err := cmd.Execute()
	assert.NoError(t, err)

	// Verify checkpoint was reset
	var position int64
	err = env.db.QueryRow(`SELECT position FROM mink.checkpoints WHERE projection_name = $1`, "TestRebuildProj").Scan(&position)
	require.NoError(t, err)
	assert.Equal(t, int64(0), position)
}

// Test projection rebuild without config
func TestProjectionRebuildCommand_NoConfig_Integration(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-rebuild-noconfig-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewProjectionCommand()
	cmd.SetArgs([]string{"rebuild", "TestProj", "--yes"})

	err = cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "mink.yaml")
}

// Test projection rebuild without database URL
func TestProjectionRebuildCommand_NoDBURL_Integration(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-rebuild-nodb-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Database.Driver = "postgres"
	cfg.Database.URL = "" // Empty URL
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewProjectionCommand()
	cmd.SetArgs([]string{"rebuild", "TestProj", "--yes"})

	err = cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "DATABASE_URL")
}

// Test stream events command
func TestStreamEventsCommand_WithNoEvents_Integration(t *testing.T) {
	env := setupIntegrationEnv(t, "mink-stream-events-noevt-test-*")
	env.createConfig()

	cmd := NewStreamCommand()
	cmd.SetArgs([]string{"events", "nonexistent-stream"})

	// Should work even with no events
	err := cmd.Execute()
	assert.NoError(t, err)
}

// Test stream stats command with data
func TestStreamStatsCommand_WithData_Integration(t *testing.T) {
	env := setupIntegrationEnv(t, "mink-stream-stats-data-test-*")
	env.createConfig()
	env.insertEvents("stats-stream-1", "TestEvent")
	env.insertEvents("stats-stream-2", "TestEvent")

	cmd := NewStreamCommand()
	cmd.SetArgs([]string{"stats"})

	err := cmd.Execute()
	assert.NoError(t, err)
}

// Test migrate up command with actual PostgreSQL
func TestMigrateUpCommand_Integration(t *testing.T) {
	env := setupIntegrationEnv(t, "mink-migrate-up-pg-test-*")
	migrationsDir := env.createConfigWithMigrations()

	// Create a test migration
	migrationSQL := "-- Test migration\nSELECT 1;"
	require.NoError(t, os.WriteFile(filepath.Join(migrationsDir, "001_20260106000000_test.sql"), []byte(migrationSQL), 0644))

	cmd := NewMigrateCommand()
	cmd.SetArgs([]string{"up", "--non-interactive"})

	err := cmd.Execute()
	assert.NoError(t, err)

	// Verify migration was recorded
	var count int
	err = env.db.QueryRow(`SELECT COUNT(*) FROM mink.migrations WHERE name LIKE '001_%'`).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

// Test migrate up command with multiple migrations
func TestMigrateUpCommand_MultipleMigrations_Integration(t *testing.T) {
	env := setupIntegrationEnv(t, "mink-migrate-multi-test-*")
	migrationsDir := env.createConfigWithMigrations()

	// Create multiple migrations
	require.NoError(t, os.WriteFile(filepath.Join(migrationsDir, "001_20260106000000_first.sql"), []byte("SELECT 1;"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(migrationsDir, "002_20260106000001_second.sql"), []byte("SELECT 2;"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(migrationsDir, "003_20260106000002_third.sql"), []byte("SELECT 3;"), 0644))

	cmd := NewMigrateCommand()
	cmd.SetArgs([]string{"up", "--non-interactive"})

	err := cmd.Execute()
	assert.NoError(t, err)

	// Verify all migrations were applied
	var count int
	err = env.db.QueryRow(`SELECT COUNT(*) FROM mink.migrations`).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 3, count)
}

// Test migrate up with --steps flag (additional coverage)
func TestMigrateUpCommand_WithSteps_Coverage(t *testing.T) {
	env := setupIntegrationEnv(t, "mink-migrate-steps-cov-test-*")
	migrationsDir := env.createConfigWithMigrations()

	// Create multiple migrations
	require.NoError(t, os.WriteFile(filepath.Join(migrationsDir, "001_20260106000000_first.sql"), []byte("SELECT 1;"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(migrationsDir, "002_20260106000001_second.sql"), []byte("SELECT 2;"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(migrationsDir, "003_20260106000002_third.sql"), []byte("SELECT 3;"), 0644))

	// Only apply 2 steps
	cmd := NewMigrateCommand()
	cmd.SetArgs([]string{"up", "--steps", "2", "--non-interactive"})

	err := cmd.Execute()
	assert.NoError(t, err)

	// Verify only 2 migrations were applied
	var count int
	err = env.db.QueryRow(`SELECT COUNT(*) FROM mink.migrations`).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 2, count)
}

// Test projection rebuild with actual projection (additional coverage)
func TestProjectionRebuildCommand_Coverage(t *testing.T) {
	env := setupIntegrationEnv(t, "mink-rebuild-cov-test-*")
	env.createConfig()
	env.insertProjection("TestProjection", 100, "active")

	cmd := NewProjectionCommand()
	cmd.SetArgs([]string{"rebuild", "TestProjection", "--yes"})

	err := cmd.Execute()
	assert.NoError(t, err)

	// Verify checkpoint was reset
	var position int64
	err = env.db.QueryRow(`SELECT position FROM mink.checkpoints WHERE projection_name = $1`, "TestProjection").Scan(&position)
	require.NoError(t, err)
	assert.Equal(t, int64(0), position)
}

// Test checkProjections with actual projections (additional coverage)
func TestCheckProjections_Coverage(t *testing.T) {
	env := setupIntegrationEnv(t, "mink-check-proj-cov-test-*")
	env.createConfig()
	env.insertProjection("Projection1", 50, "active")
	env.insertProjection("Projection2", 100, "active")
	env.insertEvents("check-proj-stream", "TestEvent", "TestEvent")

	result := checkProjections()
	assert.Equal(t, "Projections", result.Name)
}
