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

	// Create mink tables
	_, err = db.Exec(`
		DROP TABLE IF EXISTS mink_events CASCADE;
		DROP TABLE IF EXISTS mink_streams CASCADE;
		DROP TABLE IF EXISTS mink_checkpoints CASCADE;
		DROP TABLE IF EXISTS mink_snapshots CASCADE;
		DROP TABLE IF EXISTS mink_outbox CASCADE;
		DROP TABLE IF EXISTS mink_migrations CASCADE;
		DROP TABLE IF EXISTS mink_idempotency CASCADE;

		CREATE TABLE IF NOT EXISTS mink_events (
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

		CREATE INDEX IF NOT EXISTS idx_mink_events_stream_id ON mink_events(stream_id, version);
		CREATE INDEX IF NOT EXISTS idx_mink_events_global_position ON mink_events(global_position);

		CREATE TABLE IF NOT EXISTS mink_checkpoints (
			projection_name VARCHAR(255) PRIMARY KEY,
			position BIGINT NOT NULL DEFAULT 0,
			status VARCHAR(50) DEFAULT 'active',
			updated_at TIMESTAMPTZ DEFAULT NOW()
		);

		CREATE TABLE IF NOT EXISTS mink_migrations (
			name VARCHAR(255) PRIMARY KEY,
			applied_at TIMESTAMPTZ DEFAULT NOW()
		);
	`)
	require.NoError(t, err)

	return db
}

func cleanupTestDB(t *testing.T, db *sql.DB) {
	t.Helper()
	db.Exec(`DELETE FROM mink_events`)
	db.Exec(`DELETE FROM mink_checkpoints`)
	db.Exec(`DELETE FROM mink_migrations`)
	db.Close()
}

// Test listProjections function
func TestListProjections_Integration(t *testing.T) {
	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	// Insert test projections
	_, err := db.Exec(`
		INSERT INTO mink_checkpoints (projection_name, position, status) VALUES
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
		INSERT INTO mink_checkpoints (projection_name, position, status) VALUES
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
		INSERT INTO mink_checkpoints (projection_name, position, status) VALUES
		('StatusTest', 10, 'active')
	`)
	require.NoError(t, err)

	err = setProjectionStatus(testDBURL, "StatusTest", "paused")
	require.NoError(t, err)

	// Verify status changed
	var status string
	err = db.QueryRow(`SELECT status FROM mink_checkpoints WHERE projection_name = $1`, "StatusTest").Scan(&status)
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
		INSERT INTO mink_events (stream_id, version, type, data) VALUES
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

	// Insert test events
	_, err := db.Exec(`
		INSERT INTO mink_events (stream_id, version, type, data) VALUES
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

	_, err := db.Exec(`
		INSERT INTO mink_events (stream_id, version, type, data) VALUES
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
		INSERT INTO mink_events (stream_id, version, type, data, metadata) VALUES
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
		INSERT INTO mink_events (stream_id, version, type, data) VALUES
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

	_, err := db.Exec(`
		INSERT INTO mink_events (stream_id, version, type, data) VALUES
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
		INSERT INTO mink_migrations (name) VALUES
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
	err = db.QueryRow(`SELECT COUNT(*) FROM mink_migrations WHERE name = $1`, "003_test_migration").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

func TestRemoveMigrationRecord_Integration(t *testing.T) {
	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	_, err := db.Exec(`INSERT INTO mink_migrations (name) VALUES ('to_remove')`)
	require.NoError(t, err)

	err = removeMigrationRecord(db, "to_remove")
	require.NoError(t, err)

	var count int
	err = db.QueryRow(`SELECT COUNT(*) FROM mink_migrations WHERE name = $1`, "to_remove").Scan(&count)
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
		INSERT INTO mink_migrations (name) VALUES ('001_initial'), ('002_add_index')
	`)
	require.NoError(t, err)

	pending, err := getPendingMigrations(testDBURL, tmpDir)
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

	_, err = db.Exec(`INSERT INTO mink_migrations (name) VALUES ('001_initial')`)
	require.NoError(t, err)

	applied, err := getAppliedMigrations(testDBURL, tmpDir)
	require.NoError(t, err)

	assert.Len(t, applied, 1)
	assert.Equal(t, "001_initial", applied[0].Name)
}

// Test diagnose database connection check
func TestCheckDatabaseConnection_Integration(t *testing.T) {
	skipIfNoDatabase(t)

	// Create temp dir with config
	tmpDir, err := os.MkdirTemp("", "mink-diag-db-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Database.Driver = "postgres"
	cfg.Database.URL = testDBURL
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	result := checkDatabaseConnection()

	assert.Equal(t, "Database Connection", result.Name)
	assert.Equal(t, StatusOK, result.Status)
	assert.Contains(t, result.Message, "Connected")
}

// Test diagnose event store schema check
func TestCheckEventStoreSchema_Integration(t *testing.T) {
	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	tmpDir, err := os.MkdirTemp("", "mink-diag-schema-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Database.Driver = "postgres"
	cfg.Database.URL = testDBURL
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	result := checkEventStoreSchema()

	assert.Equal(t, "Event Store Schema", result.Name)
	assert.Equal(t, StatusOK, result.Status)
}

// Test diagnose projections check
func TestCheckProjections_Integration(t *testing.T) {
	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	// Insert test projection
	_, err := db.Exec(`
		INSERT INTO mink_checkpoints (projection_name, position, status) VALUES
		('TestProjection', 100, 'active')
	`)
	require.NoError(t, err)

	// Insert matching events
	_, err = db.Exec(`
		INSERT INTO mink_events (stream_id, version, type, data) VALUES
		('stream-1', 1, 'Event1', '{}')
	`)
	require.NoError(t, err)

	tmpDir, err := os.MkdirTemp("", "mink-diag-proj-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Database.Driver = "postgres"
	cfg.Database.URL = testDBURL
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	result := checkProjections()

	assert.Equal(t, "Projections", result.Name)
	// Status depends on whether projections are caught up
	assert.Contains(t, []CheckStatus{StatusOK, StatusWarning}, result.Status)
}

// Test command execution with database
func TestProjectionListCommand_Integration(t *testing.T) {
	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	_, err := db.Exec(`
		INSERT INTO mink_checkpoints (projection_name, position, status) VALUES
		('OrderView', 50, 'active')
	`)
	require.NoError(t, err)

	tmpDir, err := os.MkdirTemp("", "mink-proj-cmd-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Database.Driver = "postgres"
	cfg.Database.URL = testDBURL
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewProjectionCommand()
	cmd.SetArgs([]string{"list"})

	err = cmd.Execute()
	assert.NoError(t, err)
}

func TestStreamListCommand_Integration(t *testing.T) {
	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	_, err := db.Exec(`
		INSERT INTO mink_events (stream_id, version, type, data) VALUES
		('order-123', 1, 'OrderCreated', '{}')
	`)
	require.NoError(t, err)

	tmpDir, err := os.MkdirTemp("", "mink-stream-cmd-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Database.Driver = "postgres"
	cfg.Database.URL = testDBURL
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewStreamCommand()
	cmd.SetArgs([]string{"list"})

	err = cmd.Execute()
	assert.NoError(t, err)
}

func TestStreamEventsCommand_Integration(t *testing.T) {
	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	_, err := db.Exec(`
		INSERT INTO mink_events (stream_id, version, type, data) VALUES
		('order-123', 1, 'OrderCreated', '{"orderId": "123"}'),
		('order-123', 2, 'ItemAdded', '{"item": "widget"}')
	`)
	require.NoError(t, err)

	tmpDir, err := os.MkdirTemp("", "mink-stream-events-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Database.Driver = "postgres"
	cfg.Database.URL = testDBURL
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewStreamCommand()
	cmd.SetArgs([]string{"events", "order-123"})

	err = cmd.Execute()
	assert.NoError(t, err)
}

func TestStreamStatsCommand_Integration(t *testing.T) {
	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	_, err := db.Exec(`
		INSERT INTO mink_events (stream_id, version, type, data) VALUES
		('order-1', 1, 'OrderCreated', '{}'),
		('order-2', 1, 'OrderCreated', '{}')
	`)
	require.NoError(t, err)

	tmpDir, err := os.MkdirTemp("", "mink-stream-stats-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Database.Driver = "postgres"
	cfg.Database.URL = testDBURL
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewStreamCommand()
	cmd.SetArgs([]string{"stats"})

	err = cmd.Execute()
	assert.NoError(t, err)
}

func TestMigrateStatusCommand_Integration(t *testing.T) {
	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	tmpDir, err := os.MkdirTemp("", "mink-migrate-status-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	// Create migrations dir with files
	migrationsDir := filepath.Join(tmpDir, "migrations")
	os.MkdirAll(migrationsDir, 0755)
	os.WriteFile(filepath.Join(migrationsDir, "001_initial.sql"), []byte("-- up"), 0644)

	cfg := config.DefaultConfig()
	cfg.Database.Driver = "postgres"
	cfg.Database.URL = testDBURL
	cfg.Database.MigrationsDir = "migrations"
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewMigrateCommand()
	cmd.SetArgs([]string{"status"})

	err = cmd.Execute()
	assert.NoError(t, err)
}

// Test stream export command
func TestStreamExportCommand_Integration(t *testing.T) {
	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	_, err := db.Exec(`
		INSERT INTO mink_events (stream_id, version, type, data, metadata) VALUES
		('export-stream', 1, 'Created', '{"id": "123"}', '{"user": "test"}'),
		('export-stream', 2, 'Updated', '{"status": "active"}', '{}')
	`)
	require.NoError(t, err)

	tmpDir, err := os.MkdirTemp("", "mink-export-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Database.Driver = "postgres"
	cfg.Database.URL = testDBURL
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	outputFile := filepath.Join(tmpDir, "export.json")

	cmd := NewStreamCommand()
	cmd.SetArgs([]string{"export", "export-stream", "--output", outputFile})

	err = cmd.Execute()
	assert.NoError(t, err)

	// Verify export file exists and has content
	data, err := os.ReadFile(outputFile)
	assert.NoError(t, err)
	assert.Contains(t, string(data), "export-stream")
}

// Test projection status command
func TestProjectionStatusCommand_Integration(t *testing.T) {
	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	_, err := db.Exec(`
		INSERT INTO mink_checkpoints (projection_name, position, status) VALUES
		('DetailedProjection', 100, 'active')
	`)
	require.NoError(t, err)

	// Insert some events for lag calculation
	_, err = db.Exec(`
		INSERT INTO mink_events (stream_id, version, type, data) VALUES
		('stream-1', 1, 'Event1', '{}'),
		('stream-1', 2, 'Event2', '{}')
	`)
	require.NoError(t, err)

	tmpDir, err := os.MkdirTemp("", "mink-proj-status-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Database.Driver = "postgres"
	cfg.Database.URL = testDBURL
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewProjectionCommand()
	cmd.SetArgs([]string{"status", "DetailedProjection"})

	err = cmd.Execute()
	assert.NoError(t, err)
}

// Test projection pause command
func TestProjectionPauseCommand_Integration(t *testing.T) {
	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	_, err := db.Exec(`
		INSERT INTO mink_checkpoints (projection_name, position, status) VALUES
		('PauseTestProj', 50, 'active')
	`)
	require.NoError(t, err)

	tmpDir, err := os.MkdirTemp("", "mink-proj-pause-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Database.Driver = "postgres"
	cfg.Database.URL = testDBURL
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewProjectionCommand()
	cmd.SetArgs([]string{"pause", "PauseTestProj"})

	err = cmd.Execute()
	assert.NoError(t, err)

	// Verify status changed to paused
	var status string
	err = db.QueryRow(`SELECT status FROM mink_checkpoints WHERE projection_name = $1`, "PauseTestProj").Scan(&status)
	require.NoError(t, err)
	assert.Equal(t, "paused", status)
}

// Test projection resume command
func TestProjectionResumeCommand_Integration(t *testing.T) {
	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	_, err := db.Exec(`
		INSERT INTO mink_checkpoints (projection_name, position, status) VALUES
		('ResumeTestProj', 50, 'paused')
	`)
	require.NoError(t, err)

	tmpDir, err := os.MkdirTemp("", "mink-proj-resume-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Database.Driver = "postgres"
	cfg.Database.URL = testDBURL
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewProjectionCommand()
	cmd.SetArgs([]string{"resume", "ResumeTestProj"})

	err = cmd.Execute()
	assert.NoError(t, err)

	// Verify status changed to active
	var status string
	err = db.QueryRow(`SELECT status FROM mink_checkpoints WHERE projection_name = $1`, "ResumeTestProj").Scan(&status)
	require.NoError(t, err)
	assert.Equal(t, "active", status)
}

// Test projection rebuild command
func TestProjectionRebuildCommand_Integration(t *testing.T) {
	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	_, err := db.Exec(`
		INSERT INTO mink_checkpoints (projection_name, position, status) VALUES
		('RebuildTestProj', 100, 'active')
	`)
	require.NoError(t, err)

	tmpDir, err := os.MkdirTemp("", "mink-proj-rebuild-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Database.Driver = "postgres"
	cfg.Database.URL = testDBURL
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewProjectionCommand()
	cmd.SetArgs([]string{"rebuild", "RebuildTestProj", "--force"})

	err = cmd.Execute()
	assert.NoError(t, err)

	// Verify position reset to 0
	var position int64
	err = db.QueryRow(`SELECT position FROM mink_checkpoints WHERE projection_name = $1`, "RebuildTestProj").Scan(&position)
	require.NoError(t, err)
	assert.Equal(t, int64(0), position)
}

// Test migrate create command - non-interactive
func TestMigrateCreateCommand_Integration(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-migrate-create-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	// Create migrations dir
	migrationsDir := filepath.Join(tmpDir, "migrations")
	os.MkdirAll(migrationsDir, 0755)

	cfg := config.DefaultConfig()
	cfg.Database.Driver = "postgres"
	cfg.Database.URL = testDBURL
	cfg.Database.MigrationsDir = "migrations"
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewMigrateCommand()
	cmd.SetArgs([]string{"create", "add_users_table"})

	err = cmd.Execute()
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

// Test generate aggregate command with --force (skips interactive)
func TestGenerateAggregateCommand_Force_Integration(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-gen-agg-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	// Create mink.yaml config
	cfg := config.DefaultConfig()
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewGenerateCommand()
	cmd.SetArgs([]string{"aggregate", "Order", "--events", "Created,Shipped", "--force"})

	err = cmd.Execute()
	assert.NoError(t, err)

	// Verify files were created (uses default config paths)
	assert.FileExists(t, filepath.Join(tmpDir, "internal/domain/order.go"))
	assert.FileExists(t, filepath.Join(tmpDir, "internal/domain/order_test.go"))
	assert.FileExists(t, filepath.Join(tmpDir, "internal/events/order_events.go"))
}

func TestGenerateAggregateCommand_ForceNoEvents_Integration(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-gen-agg-noevt-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewGenerateCommand()
	// Use --force without --events to skip the interactive form
	cmd.SetArgs([]string{"aggregate", "Customer", "--force"})

	err = cmd.Execute()
	assert.NoError(t, err)

	// Should still create aggregate file, just without events
	assert.FileExists(t, filepath.Join(tmpDir, "internal/domain/customer.go"))
	assert.FileExists(t, filepath.Join(tmpDir, "internal/domain/customer_test.go"))
}

// Test generate event command with --force
func TestGenerateEventCommand_Force_Integration(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-gen-evt-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewGenerateCommand()
	cmd.SetArgs([]string{"event", "OrderCreated", "--aggregate", "Order", "--force"})

	err = cmd.Execute()
	assert.NoError(t, err)

	assert.FileExists(t, filepath.Join(tmpDir, "internal/events/ordercreated.go"))
}

func TestGenerateEventCommand_ForceNoAggregate_Integration(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-gen-evt-noagg-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewGenerateCommand()
	// Use --force without --aggregate to skip interactive form
	cmd.SetArgs([]string{"event", "ItemAdded", "--force"})

	err = cmd.Execute()
	assert.NoError(t, err)

	// Event file should still be created even without aggregate
	assert.FileExists(t, filepath.Join(tmpDir, "internal/events/itemadded.go"))
}

// Test generate projection command with --force
func TestGenerateProjectionCommand_Force_Integration(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-gen-proj-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewGenerateCommand()
	cmd.SetArgs([]string{"projection", "OrderSummary", "--events", "OrderCreated,OrderShipped", "--force"})

	err = cmd.Execute()
	assert.NoError(t, err)

	assert.FileExists(t, filepath.Join(tmpDir, "internal/projections/ordersummary.go"))
	assert.FileExists(t, filepath.Join(tmpDir, "internal/projections/ordersummary_test.go"))
}

func TestGenerateProjectionCommand_ForceNoEvents_Integration(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-gen-proj-noevt-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewGenerateCommand()
	cmd.SetArgs([]string{"projection", "UserProfile", "--force"})

	err = cmd.Execute()
	assert.NoError(t, err)

	assert.FileExists(t, filepath.Join(tmpDir, "internal/projections/userprofile.go"))
	assert.FileExists(t, filepath.Join(tmpDir, "internal/projections/userprofile_test.go"))
}

// Test generate command command with --force
func TestGenerateCommandCommand_Force_Integration(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-gen-cmd-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewGenerateCommand()
	cmd.SetArgs([]string{"command", "CreateOrder", "--aggregate", "Order", "--force"})

	err = cmd.Execute()
	assert.NoError(t, err)

	assert.FileExists(t, filepath.Join(tmpDir, "internal/commands/createorder.go"))
}

func TestGenerateCommandCommand_ForceNoAggregate_Integration(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-gen-cmd-noagg-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewGenerateCommand()
	cmd.SetArgs([]string{"command", "UpdateItem", "--force"})

	err = cmd.Execute()
	assert.NoError(t, err)

	assert.FileExists(t, filepath.Join(tmpDir, "internal/commands/updateitem.go"))
}

// Test migrate up command with --force (skips spinner)
func TestMigrateUpCommand_Force_Integration(t *testing.T) {
	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	tmpDir, err := os.MkdirTemp("", "mink-migrate-up-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	// Create mink.yaml config
	cfg := config.DefaultConfig()
	cfg.Database.Driver = "postgres"
	cfg.Database.URL = testDBURL
	cfg.Database.MigrationsDir = "migrations"
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	// Create migrations directory with a test migration
	migrationsDir := filepath.Join(tmpDir, "migrations")
	err = os.MkdirAll(migrationsDir, 0755)
	require.NoError(t, err)

	migrationContent := `CREATE TABLE IF NOT EXISTS test_migrate_up (id INT);`
	err = os.WriteFile(filepath.Join(migrationsDir, "001_create_test.sql"), []byte(migrationContent), 0644)
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewMigrateCommand()
	cmd.SetArgs([]string{"up", "--force"})

	err = cmd.Execute()
	assert.NoError(t, err)

	// Verify the migration was applied
	var exists bool
	err = db.QueryRow(`SELECT EXISTS (SELECT FROM information_schema.tables WHERE table_name = 'test_migrate_up')`).Scan(&exists)
	assert.NoError(t, err)
	assert.True(t, exists, "Migration should have created test_migrate_up table")

	// Cleanup
	db.Exec(`DROP TABLE IF EXISTS test_migrate_up`)
}

func TestMigrateUpCommand_ForceNoMigrations_Integration(t *testing.T) {
	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	tmpDir, err := os.MkdirTemp("", "mink-migrate-up-empty-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Database.Driver = "postgres"
	cfg.Database.URL = testDBURL
	cfg.Database.MigrationsDir = "migrations"
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	// Create empty migrations directory
	err = os.MkdirAll(filepath.Join(tmpDir, "migrations"), 0755)
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewMigrateCommand()
	cmd.SetArgs([]string{"up", "--force"})

	// Should succeed with "up to date" message
	err = cmd.Execute()
	assert.NoError(t, err)
}

func TestMigrateUpCommand_WithSteps_Integration(t *testing.T) {
	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	tmpDir, err := os.MkdirTemp("", "mink-migrate-up-steps-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Database.Driver = "postgres"
	cfg.Database.URL = testDBURL
	cfg.Database.MigrationsDir = "migrations"
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	migrationsDir := filepath.Join(tmpDir, "migrations")
	err = os.MkdirAll(migrationsDir, 0755)
	require.NoError(t, err)

	// Create 3 migrations
	err = os.WriteFile(filepath.Join(migrationsDir, "001_first.sql"),
		[]byte(`CREATE TABLE IF NOT EXISTS test_up_first (id INT);`), 0644)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(migrationsDir, "002_second.sql"),
		[]byte(`CREATE TABLE IF NOT EXISTS test_up_second (id INT);`), 0644)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(migrationsDir, "003_third.sql"),
		[]byte(`CREATE TABLE IF NOT EXISTS test_up_third (id INT);`), 0644)
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	// Apply only 2 migrations
	cmd := NewMigrateCommand()
	cmd.SetArgs([]string{"up", "--force", "--steps", "2"})

	err = cmd.Execute()
	assert.NoError(t, err)

	// Verify only first two tables exist
	var exists1, exists2, exists3 bool
	db.QueryRow(`SELECT EXISTS (SELECT FROM information_schema.tables WHERE table_name = 'test_up_first')`).Scan(&exists1)
	db.QueryRow(`SELECT EXISTS (SELECT FROM information_schema.tables WHERE table_name = 'test_up_second')`).Scan(&exists2)
	db.QueryRow(`SELECT EXISTS (SELECT FROM information_schema.tables WHERE table_name = 'test_up_third')`).Scan(&exists3)

	assert.True(t, exists1, "First table should exist")
	assert.True(t, exists2, "Second table should exist")
	assert.False(t, exists3, "Third table should NOT exist (only 2 steps)")

	// Cleanup
	db.Exec(`DROP TABLE IF EXISTS test_up_first`)
	db.Exec(`DROP TABLE IF EXISTS test_up_second`)
}

func TestMigrateUpCommand_MemoryDriver_Integration(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-migrate-up-memory-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Database.Driver = "memory"
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewMigrateCommand()
	cmd.SetArgs([]string{"up", "--force"})

	// Should succeed with info message about memory driver
	err = cmd.Execute()
	assert.NoError(t, err)
}

// Test migrate down command
func TestMigrateDownCommand_Integration(t *testing.T) {
	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	tmpDir, err := os.MkdirTemp("", "mink-migrate-down-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	// Create mink.yaml config
	cfg := config.DefaultConfig()
	cfg.Database.Driver = "postgres"
	cfg.Database.URL = testDBURL
	cfg.Database.MigrationsDir = "migrations"
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	// Create migrations directory
	migrationsDir := filepath.Join(tmpDir, "migrations")
	err = os.MkdirAll(migrationsDir, 0755)
	require.NoError(t, err)

	// Create up and down migration files
	upMigration := `CREATE TABLE IF NOT EXISTS test_migrate_down (id INT);`
	downMigration := `DROP TABLE IF EXISTS test_migrate_down;`
	err = os.WriteFile(filepath.Join(migrationsDir, "001_create_test.sql"), []byte(upMigration), 0644)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(migrationsDir, "001_create_test.down.sql"), []byte(downMigration), 0644)
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	// First apply the migration
	cmdUp := NewMigrateCommand()
	cmdUp.SetArgs([]string{"up", "--force"})
	err = cmdUp.Execute()
	require.NoError(t, err)

	// Verify table exists
	var exists bool
	err = db.QueryRow(`SELECT EXISTS (SELECT FROM information_schema.tables WHERE table_name = 'test_migrate_down')`).Scan(&exists)
	require.NoError(t, err)
	assert.True(t, exists, "Table should exist after migration up")

	// Now rollback
	cmdDown := NewMigrateCommand()
	cmdDown.SetArgs([]string{"down"})
	err = cmdDown.Execute()
	assert.NoError(t, err)

	// Verify table no longer exists
	err = db.QueryRow(`SELECT EXISTS (SELECT FROM information_schema.tables WHERE table_name = 'test_migrate_down')`).Scan(&exists)
	assert.NoError(t, err)
	assert.False(t, exists, "Table should be dropped after migration down")

	// Cleanup
	db.Exec(`DROP TABLE IF EXISTS test_migrate_down`)
}

func TestMigrateDownCommand_NoMigrations_Integration(t *testing.T) {
	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	tmpDir, err := os.MkdirTemp("", "mink-migrate-down-empty-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Database.Driver = "postgres"
	cfg.Database.URL = testDBURL
	cfg.Database.MigrationsDir = "migrations"
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	// Create empty migrations directory
	err = os.MkdirAll(filepath.Join(tmpDir, "migrations"), 0755)
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewMigrateCommand()
	cmd.SetArgs([]string{"down"})

	// Should succeed with "no migrations to rollback" message
	err = cmd.Execute()
	assert.NoError(t, err)
}

func TestMigrateDownCommand_NoDownFile_Integration(t *testing.T) {
	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	tmpDir, err := os.MkdirTemp("", "mink-migrate-down-nofile-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Database.Driver = "postgres"
	cfg.Database.URL = testDBURL
	cfg.Database.MigrationsDir = "migrations"
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	migrationsDir := filepath.Join(tmpDir, "migrations")
	err = os.MkdirAll(migrationsDir, 0755)
	require.NoError(t, err)

	// Create only up migration (no down file)
	upMigration := `CREATE TABLE IF NOT EXISTS test_migrate_down_nofile (id INT);`
	err = os.WriteFile(filepath.Join(migrationsDir, "001_create_nodown.sql"), []byte(upMigration), 0644)
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	// Apply the migration
	cmdUp := NewMigrateCommand()
	cmdUp.SetArgs([]string{"up", "--force"})
	err = cmdUp.Execute()
	require.NoError(t, err)

	// Try to rollback - should skip because no down file
	cmdDown := NewMigrateCommand()
	cmdDown.SetArgs([]string{"down"})
	err = cmdDown.Execute()
	assert.NoError(t, err)

	// Cleanup
	db.Exec(`DROP TABLE IF EXISTS test_migrate_down_nofile`)
}

func TestMigrateDownCommand_MultipleSteps_Integration(t *testing.T) {
	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	tmpDir, err := os.MkdirTemp("", "mink-migrate-down-steps-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Database.Driver = "postgres"
	cfg.Database.URL = testDBURL
	cfg.Database.MigrationsDir = "migrations"
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	migrationsDir := filepath.Join(tmpDir, "migrations")
	err = os.MkdirAll(migrationsDir, 0755)
	require.NoError(t, err)

	// Create two migrations
	err = os.WriteFile(filepath.Join(migrationsDir, "001_first.sql"),
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

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	// Apply both migrations
	cmdUp := NewMigrateCommand()
	cmdUp.SetArgs([]string{"up", "--force"})
	err = cmdUp.Execute()
	require.NoError(t, err)

	// Rollback 2 steps
	cmdDown := NewMigrateCommand()
	cmdDown.SetArgs([]string{"down", "--steps", "2"})
	err = cmdDown.Execute()
	assert.NoError(t, err)

	// Cleanup
	db.Exec(`DROP TABLE IF EXISTS test_first`)
	db.Exec(`DROP TABLE IF EXISTS test_second`)
}

func TestMigrateDownCommand_MemoryDriver_Integration(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mink-migrate-down-memory-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Database.Driver = "memory"
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewMigrateCommand()
	cmd.SetArgs([]string{"down"})

	// Should succeed with info message about memory driver
	err = cmd.Execute()
	assert.NoError(t, err)
}

// Test diagnose command with database
func TestDiagnoseCommand_Integration(t *testing.T) {
	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	tmpDir, err := os.MkdirTemp("", "mink-diagnose-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Database.Driver = "postgres"
	cfg.Database.URL = testDBURL
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewDiagnoseCommand()

	err = cmd.Execute()
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

// Test projection rebuild command with --force
func TestProjectionRebuildCommand_Force_Integration(t *testing.T) {
	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	// Create checkpoints table and projection entry
	db.Exec(`
		CREATE TABLE IF NOT EXISTS mink_checkpoints (
			projection_name VARCHAR(255) PRIMARY KEY,
			position BIGINT NOT NULL DEFAULT 0,
			status VARCHAR(50) DEFAULT 'active',
			updated_at TIMESTAMPTZ DEFAULT NOW()
		)
	`)
	db.Exec(`INSERT INTO mink_checkpoints (projection_name, position) VALUES ('TestRebuildProj', 100)`)

	tmpDir, err := os.MkdirTemp("", "mink-rebuild-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Database.Driver = "postgres"
	cfg.Database.URL = testDBURL
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewProjectionCommand()
	cmd.SetArgs([]string{"rebuild", "TestRebuildProj", "--force"})

	err = cmd.Execute()
	assert.NoError(t, err)

	// Verify checkpoint was reset
	var position int64
	err = db.QueryRow(`SELECT position FROM mink_checkpoints WHERE projection_name = $1`, "TestRebuildProj").Scan(&position)
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
	cmd.SetArgs([]string{"rebuild", "TestProj", "--force"})

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
	cmd.SetArgs([]string{"rebuild", "TestProj", "--force"})

	err = cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "DATABASE_URL")
}

// Test stream events command
func TestStreamEventsCommand_WithNoEvents_Integration(t *testing.T) {
	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	tmpDir, err := os.MkdirTemp("", "mink-stream-events-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Database.Driver = "postgres"
	cfg.Database.URL = testDBURL
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewStreamCommand()
	cmd.SetArgs([]string{"events", "nonexistent-stream"})

	// Should work even with no events
	err = cmd.Execute()
	assert.NoError(t, err)
}

// Test stream stats command with data
func TestStreamStatsCommand_WithData_Integration(t *testing.T) {
	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	// Insert some test events
	db.Exec(`
		INSERT INTO mink_events (stream_id, version, type, data) 
		VALUES 
			('stats-stream-1', 1, 'TestEvent', '{}'),
			('stats-stream-2', 1, 'TestEvent', '{}')
	`)

	tmpDir, err := os.MkdirTemp("", "mink-stream-stats-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Database.Driver = "postgres"
	cfg.Database.URL = testDBURL
	cfg.Project.Module = "github.com/test/project"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cmd := NewStreamCommand()
	cmd.SetArgs([]string{"stats"})

	err = cmd.Execute()
	assert.NoError(t, err)
}
