//go:build integration
// +build integration

package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AshkanYarmoradi/go-mink/cli/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// E2E Test: Complete CLI Workflow
// =============================================================================
//
// This test exercises the full CLI workflow against a real PostgreSQL database:
// 1. Initialize a new mink project
// 2. Generate aggregate, events, projection, command
// 3. Create and apply migrations
// 4. Insert test events into the database
// 5. List and query streams
// 6. Manage projections (list, pause, resume, rebuild)
// 7. Export stream data
// 8. Run diagnostics
// 9. Rollback migrations

func TestE2E_CompleteCliWorkflow(t *testing.T) {
	skipIfNoDatabase(t)

	// Setup: Create a fresh test database and temp directory
	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	tmpDir, err := os.MkdirTemp("", "mink-e2e-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	// Change to temp directory for the entire test
	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	// ==========================================================================
	// Step 1: Initialize mink project (simulated - init requires interactive)
	// ==========================================================================
	t.Log("Step 1: Initialize mink project")

	cfg := config.DefaultConfig()
	cfg.Project.Name = "e2e-test-project"
	cfg.Project.Module = "github.com/test/e2e-project"
	cfg.Database.Driver = "postgres"
	cfg.Database.URL = testDBURL
	cfg.Database.MigrationsDir = "migrations"
	cfg.Generation.AggregatePackage = "internal/domain"
	cfg.Generation.EventPackage = "internal/events"
	cfg.Generation.CommandPackage = "internal/commands"
	cfg.Generation.ProjectionPackage = "internal/projections"

	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(tmpDir, "mink.yaml"))

	// Create required directories
	os.MkdirAll(filepath.Join(tmpDir, "migrations"), 0755)
	os.MkdirAll(filepath.Join(tmpDir, "internal/domain"), 0755)
	os.MkdirAll(filepath.Join(tmpDir, "internal/events"), 0755)
	os.MkdirAll(filepath.Join(tmpDir, "internal/commands"), 0755)
	os.MkdirAll(filepath.Join(tmpDir, "internal/projections"), 0755)

	// ==========================================================================
	// Step 2: Generate aggregate with events
	// ==========================================================================
	t.Log("Step 2: Generate Order aggregate with events")

	cmd := NewGenerateCommand()
	cmd.SetArgs([]string{"aggregate", "Order", "--events", "Created,ItemAdded,Shipped", "--non-interactive"})
	err = cmd.Execute()
	require.NoError(t, err)

	assert.FileExists(t, filepath.Join(tmpDir, "internal/domain/order.go"))
	assert.FileExists(t, filepath.Join(tmpDir, "internal/domain/order_test.go"))
	assert.FileExists(t, filepath.Join(tmpDir, "internal/events/order_events.go"))

	// ==========================================================================
	// Step 3: Generate a projection
	// ==========================================================================
	t.Log("Step 3: Generate OrderSummary projection")

	cmd = NewGenerateCommand()
	cmd.SetArgs([]string{"projection", "OrderSummary", "--events", "OrderCreated,OrderItemAdded,OrderShipped", "--non-interactive"})
	err = cmd.Execute()
	require.NoError(t, err)

	assert.FileExists(t, filepath.Join(tmpDir, "internal/projections/ordersummary.go"))
	assert.FileExists(t, filepath.Join(tmpDir, "internal/projections/ordersummary_test.go"))

	// ==========================================================================
	// Step 4: Generate a command
	// ==========================================================================
	t.Log("Step 4: Generate CreateOrder command")

	cmd = NewGenerateCommand()
	cmd.SetArgs([]string{"command", "CreateOrder", "--aggregate", "Order", "--non-interactive"})
	err = cmd.Execute()
	require.NoError(t, err)

	assert.FileExists(t, filepath.Join(tmpDir, "internal/commands/createorder.go"))

	// ==========================================================================
	// Step 5: Create migration
	// ==========================================================================
	t.Log("Step 5: Create migration")

	cmd = NewMigrateCommand()
	cmd.SetArgs([]string{"create", "add_orders_table"})
	err = cmd.Execute()
	require.NoError(t, err)

	// Find the created migration file
	files, err := os.ReadDir(filepath.Join(tmpDir, "migrations"))
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(files), 1, "Migration file should be created")

	var upMigrationFile string
	for _, f := range files {
		if strings.Contains(f.Name(), "add_orders_table") && !strings.HasSuffix(f.Name(), ".down.sql") {
			upMigrationFile = f.Name()
			break
		}
	}
	require.NotEmpty(t, upMigrationFile, "Up migration file not found")

	// Write actual SQL to the migration
	migrationPath := filepath.Join(tmpDir, "migrations", upMigrationFile)
	migrationSQL := `CREATE TABLE IF NOT EXISTS e2e_orders (
		id VARCHAR(255) PRIMARY KEY,
		customer_id VARCHAR(255) NOT NULL,
		status VARCHAR(50) DEFAULT 'pending',
		created_at TIMESTAMPTZ DEFAULT NOW()
	);`
	err = os.WriteFile(migrationPath, []byte(migrationSQL), 0644)
	require.NoError(t, err)

	// ==========================================================================
	// Step 6: Check migration status (should show pending)
	// ==========================================================================
	t.Log("Step 6: Check migration status")

	cmd = NewMigrateCommand()
	cmd.SetArgs([]string{"status"})
	err = cmd.Execute()
	require.NoError(t, err)

	// ==========================================================================
	// Step 7: Apply migration
	// ==========================================================================
	t.Log("Step 7: Apply migration")

	cmd = NewMigrateCommand()
	cmd.SetArgs([]string{"up", "--non-interactive"})
	err = cmd.Execute()
	require.NoError(t, err)

	// Verify table was created
	var tableExists bool
	err = db.QueryRow(`SELECT EXISTS (SELECT FROM pg_tables WHERE tablename = 'e2e_orders')`).Scan(&tableExists)
	require.NoError(t, err)
	assert.True(t, tableExists, "e2e_orders table should exist after migration")

	// ==========================================================================
	// Step 8: Insert test events into the database
	// ==========================================================================
	t.Log("Step 8: Insert test events")

	// First insert streams
	_, err = db.Exec(`
		INSERT INTO mink.streams (stream_id, version) VALUES
		('order-e2e-001', 4),
		('order-e2e-002', 2)
	`)
	require.NoError(t, err)

	// Then insert events
	_, err = db.Exec(`
		INSERT INTO mink.events (stream_id, version, event_type, data, metadata) VALUES
		('order-e2e-001', 1, 'OrderCreated', '{"orderId": "e2e-001", "customerId": "cust-123"}', '{"correlationId": "corr-001"}'),
		('order-e2e-001', 2, 'OrderItemAdded', '{"orderId": "e2e-001", "sku": "SKU-001", "quantity": 2}', '{}'),
		('order-e2e-001', 3, 'OrderItemAdded', '{"orderId": "e2e-001", "sku": "SKU-002", "quantity": 1}', '{}'),
		('order-e2e-001', 4, 'OrderShipped', '{"orderId": "e2e-001", "trackingNumber": "TRACK-123"}', '{}'),
		('order-e2e-002', 1, 'OrderCreated', '{"orderId": "e2e-002", "customerId": "cust-456"}', '{}'),
		('order-e2e-002', 2, 'OrderItemAdded', '{"orderId": "e2e-002", "sku": "SKU-003", "quantity": 5}', '{}')
	`)
	require.NoError(t, err)

	// ==========================================================================
	// Step 9: List streams
	// ==========================================================================
	t.Log("Step 9: List streams")

	cmd = NewStreamCommand()
	cmd.SetArgs([]string{"list"})
	err = cmd.Execute()
	require.NoError(t, err)

	// ==========================================================================
	// Step 10: Get stream events
	// ==========================================================================
	t.Log("Step 10: Get stream events for order-e2e-001")

	cmd = NewStreamCommand()
	cmd.SetArgs([]string{"events", "order-e2e-001"})
	err = cmd.Execute()
	require.NoError(t, err)

	// ==========================================================================
	// Step 11: Get stream stats
	// ==========================================================================
	t.Log("Step 11: Get stream stats")

	cmd = NewStreamCommand()
	cmd.SetArgs([]string{"stats"})
	err = cmd.Execute()
	require.NoError(t, err)

	// ==========================================================================
	// Step 12: Export stream
	// ==========================================================================
	t.Log("Step 12: Export stream to JSON")

	exportFile := filepath.Join(tmpDir, "order-e2e-001-export.json")
	cmd = NewStreamCommand()
	cmd.SetArgs([]string{"export", "order-e2e-001", "--output", exportFile})
	err = cmd.Execute()
	require.NoError(t, err)

	assert.FileExists(t, exportFile)
	exportData, err := os.ReadFile(exportFile)
	require.NoError(t, err)
	assert.Contains(t, string(exportData), "order-e2e-001")
	assert.Contains(t, string(exportData), "OrderCreated")
	assert.Contains(t, string(exportData), "OrderShipped")

	// ==========================================================================
	// Step 13: Create and manage projections
	// ==========================================================================
	t.Log("Step 13: Create projection checkpoint")

	_, err = db.Exec(`
		INSERT INTO mink.checkpoints (projection_name, position, status) VALUES
		('E2EOrderSummary', 4, 'active')
	`)
	require.NoError(t, err)

	// List projections
	cmd = NewProjectionCommand()
	cmd.SetArgs([]string{"list"})
	err = cmd.Execute()
	require.NoError(t, err)

	// ==========================================================================
	// Step 14: Get projection status
	// ==========================================================================
	t.Log("Step 14: Get projection status")

	cmd = NewProjectionCommand()
	cmd.SetArgs([]string{"status", "E2EOrderSummary"})
	err = cmd.Execute()
	require.NoError(t, err)

	// ==========================================================================
	// Step 15: Pause projection
	// ==========================================================================
	t.Log("Step 15: Pause projection")

	cmd = NewProjectionCommand()
	cmd.SetArgs([]string{"pause", "E2EOrderSummary"})
	err = cmd.Execute()
	require.NoError(t, err)

	// Verify status changed
	var status string
	err = db.QueryRow(`SELECT status FROM mink.checkpoints WHERE projection_name = $1`, "E2EOrderSummary").Scan(&status)
	require.NoError(t, err)
	assert.Equal(t, "paused", status)

	// ==========================================================================
	// Step 16: Resume projection
	// ==========================================================================
	t.Log("Step 16: Resume projection")

	cmd = NewProjectionCommand()
	cmd.SetArgs([]string{"resume", "E2EOrderSummary"})
	err = cmd.Execute()
	require.NoError(t, err)

	// Verify status changed
	err = db.QueryRow(`SELECT status FROM mink.checkpoints WHERE projection_name = $1`, "E2EOrderSummary").Scan(&status)
	require.NoError(t, err)
	assert.Equal(t, "active", status)

	// ==========================================================================
	// Step 17: Rebuild projection
	// ==========================================================================
	t.Log("Step 17: Rebuild projection")

	cmd = NewProjectionCommand()
	cmd.SetArgs([]string{"rebuild", "E2EOrderSummary", "--yes"})
	err = cmd.Execute()
	require.NoError(t, err)

	// Verify position reset
	var position int64
	err = db.QueryRow(`SELECT position FROM mink.checkpoints WHERE projection_name = $1`, "E2EOrderSummary").Scan(&position)
	require.NoError(t, err)
	assert.Equal(t, int64(0), position)

	// ==========================================================================
	// Step 18: Run diagnostics
	// ==========================================================================
	t.Log("Step 18: Run diagnostics")

	cmd = NewDiagnoseCommand()
	err = cmd.Execute()
	require.NoError(t, err)

	// ==========================================================================
	// Step 19: Rollback migration
	// ==========================================================================
	t.Log("Step 19: Rollback migration")

	// Create down migration file
	downMigrationFile := strings.Replace(upMigrationFile, ".sql", ".down.sql", 1)
	downMigrationPath := filepath.Join(tmpDir, "migrations", downMigrationFile)
	downSQL := `DROP TABLE IF EXISTS e2e_orders;`
	err = os.WriteFile(downMigrationPath, []byte(downSQL), 0644)
	require.NoError(t, err)

	cmd = NewMigrateCommand()
	cmd.SetArgs([]string{"down", "--steps", "1"})
	err = cmd.Execute()
	require.NoError(t, err)

	// Verify table was dropped
	err = db.QueryRow(`SELECT EXISTS (SELECT FROM pg_tables WHERE tablename = 'e2e_orders')`).Scan(&tableExists)
	require.NoError(t, err)
	assert.False(t, tableExists, "e2e_orders table should be dropped after rollback")

	// ==========================================================================
	// Step 20: Final verification
	// ==========================================================================
	t.Log("Step 20: Final verification - all commands completed successfully")

	// Verify all generated files exist
	assert.FileExists(t, filepath.Join(tmpDir, "mink.yaml"))
	assert.FileExists(t, filepath.Join(tmpDir, "internal/domain/order.go"))
	assert.FileExists(t, filepath.Join(tmpDir, "internal/projections/ordersummary.go"))
	assert.FileExists(t, filepath.Join(tmpDir, "internal/commands/createorder.go"))
	assert.FileExists(t, exportFile)

	// Verify events are still in database
	var eventCount int64
	err = db.QueryRow(`SELECT COUNT(*) FROM mink.events WHERE stream_id LIKE 'order-e2e-%'`).Scan(&eventCount)
	require.NoError(t, err)
	assert.Equal(t, int64(6), eventCount, "All test events should still exist")

	t.Log("✓ E2E test completed successfully!")
}

// =============================================================================
// E2E Test: Multi-Aggregate Workflow
// =============================================================================

func TestE2E_MultiAggregateWorkflow(t *testing.T) {
	skipIfNoDatabase(t)

	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	tmpDir, err := os.MkdirTemp("", "mink-e2e-multi-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	// Setup config
	cfg := config.DefaultConfig()
	cfg.Project.Name = "multi-aggregate-test"
	cfg.Project.Module = "github.com/test/multi"
	cfg.Database.Driver = "postgres"
	cfg.Database.URL = testDBURL
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	// Generate multiple aggregates
	aggregates := []struct {
		name   string
		events string
	}{
		{"Order", "Created,Shipped,Cancelled"},
		{"Customer", "Registered,Updated,Deactivated"},
		{"Product", "Added,PriceChanged,Discontinued"},
	}

	for _, agg := range aggregates {
		cmd := NewGenerateCommand()
		cmd.SetArgs([]string{"aggregate", agg.name, "--events", agg.events, "--non-interactive"})
		err := cmd.Execute()
		require.NoError(t, err)
		assert.FileExists(t, filepath.Join(tmpDir, "internal/domain", strings.ToLower(agg.name)+".go"))
	}

	// Generate projections for each aggregate
	projections := []struct {
		name   string
		events string
	}{
		{"OrderDashboard", "OrderCreated,OrderShipped"},
		{"CustomerDirectory", "CustomerRegistered,CustomerUpdated"},
		{"ProductCatalog", "ProductAdded,ProductPriceChanged"},
	}

	for _, proj := range projections {
		cmd := NewGenerateCommand()
		cmd.SetArgs([]string{"projection", proj.name, "--events", proj.events, "--non-interactive"})
		err := cmd.Execute()
		require.NoError(t, err)
		assert.FileExists(t, filepath.Join(tmpDir, "internal/projections", strings.ToLower(proj.name)+".go"))
	}

	// Insert events for multiple streams
	// First insert streams
	_, err = db.Exec(`
		INSERT INTO mink.streams (stream_id, version) VALUES
		('order-multi-001', 2),
		('customer-multi-001', 2),
		('product-multi-001', 2)
	`)
	require.NoError(t, err)

	// Then insert events
	_, err = db.Exec(`
		INSERT INTO mink.events (stream_id, version, event_type, data) VALUES
		('order-multi-001', 1, 'OrderCreated', '{}'),
		('order-multi-001', 2, 'OrderShipped', '{}'),
		('customer-multi-001', 1, 'CustomerRegistered', '{}'),
		('customer-multi-001', 2, 'CustomerUpdated', '{}'),
		('product-multi-001', 1, 'ProductAdded', '{}'),
		('product-multi-001', 2, 'ProductPriceChanged', '{}')
	`)
	require.NoError(t, err)

	// Verify all streams via list command
	cmd := NewStreamCommand()
	cmd.SetArgs([]string{"list"})
	err = cmd.Execute()
	require.NoError(t, err)

	// Verify stats show all event types
	cmd = NewStreamCommand()
	cmd.SetArgs([]string{"stats"})
	err = cmd.Execute()
	require.NoError(t, err)

	t.Log("✓ Multi-aggregate E2E test completed successfully!")
}

// =============================================================================
// E2E Test: Migration Lifecycle
// =============================================================================

func TestE2E_MigrationLifecycle(t *testing.T) {
	skipIfNoDatabase(t)

	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	tmpDir, err := os.MkdirTemp("", "mink-e2e-migration-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	// Setup
	migrationsDir := filepath.Join(tmpDir, "migrations")
	os.MkdirAll(migrationsDir, 0755)

	cfg := config.DefaultConfig()
	cfg.Project.Module = "github.com/test/migration-lifecycle"
	cfg.Database.Driver = "postgres"
	cfg.Database.URL = testDBURL
	cfg.Database.MigrationsDir = "migrations"
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	// Create multiple migrations
	migrations := []struct {
		name string
		up   string
		down string
	}{
		{
			name: "001_create_users",
			up:   "CREATE TABLE e2e_users (id SERIAL PRIMARY KEY, name VARCHAR(255));",
			down: "DROP TABLE e2e_users;",
		},
		{
			name: "002_add_email",
			up:   "ALTER TABLE e2e_users ADD COLUMN email VARCHAR(255);",
			down: "ALTER TABLE e2e_users DROP COLUMN email;",
		},
		{
			name: "003_create_posts",
			up:   "CREATE TABLE e2e_posts (id SERIAL PRIMARY KEY, user_id INT REFERENCES e2e_users(id), title VARCHAR(255));",
			down: "DROP TABLE e2e_posts;",
		},
	}

	for _, m := range migrations {
		os.WriteFile(filepath.Join(migrationsDir, m.name+".sql"), []byte(m.up), 0644)
		os.WriteFile(filepath.Join(migrationsDir, m.name+".down.sql"), []byte(m.down), 0644)
	}

	// Check status - all should be pending
	t.Log("Checking migration status (all pending)")
	cmd := NewMigrateCommand()
	cmd.SetArgs([]string{"status"})
	err = cmd.Execute()
	require.NoError(t, err)

	// Apply all migrations
	t.Log("Applying all migrations")
	cmd = NewMigrateCommand()
	cmd.SetArgs([]string{"up", "--non-interactive"})
	err = cmd.Execute()
	require.NoError(t, err)

	// Verify all tables exist
	var usersExists, postsExists bool
	db.QueryRow(`SELECT EXISTS (SELECT FROM pg_tables WHERE tablename = 'e2e_users')`).Scan(&usersExists)
	db.QueryRow(`SELECT EXISTS (SELECT FROM pg_tables WHERE tablename = 'e2e_posts')`).Scan(&postsExists)
	assert.True(t, usersExists, "e2e_users table should exist")
	assert.True(t, postsExists, "e2e_posts table should exist")

	// Check status - all should be applied
	t.Log("Checking migration status (all applied)")
	cmd = NewMigrateCommand()
	cmd.SetArgs([]string{"status"})
	err = cmd.Execute()
	require.NoError(t, err)

	// Rollback one step (should remove posts table)
	t.Log("Rolling back one migration")
	cmd = NewMigrateCommand()
	cmd.SetArgs([]string{"down", "--steps", "1"})
	err = cmd.Execute()
	require.NoError(t, err)

	db.QueryRow(`SELECT EXISTS (SELECT FROM pg_tables WHERE tablename = 'e2e_posts')`).Scan(&postsExists)
	assert.False(t, postsExists, "e2e_posts table should be dropped")

	// Rollback remaining migrations
	t.Log("Rolling back remaining migrations")
	cmd = NewMigrateCommand()
	cmd.SetArgs([]string{"down", "--steps", "2"})
	err = cmd.Execute()
	require.NoError(t, err)

	db.QueryRow(`SELECT EXISTS (SELECT FROM pg_tables WHERE tablename = 'e2e_users')`).Scan(&usersExists)
	assert.False(t, usersExists, "e2e_users table should be dropped")

	t.Log("✓ Migration lifecycle E2E test completed successfully!")
}

// =============================================================================
// E2E Test: Projection Management
// =============================================================================

func TestE2E_ProjectionManagement(t *testing.T) {
	skipIfNoDatabase(t)

	db := setupTestDB(t)
	defer cleanupTestDB(t, db)

	tmpDir, err := os.MkdirTemp("", "mink-e2e-projection-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cfg := config.DefaultConfig()
	cfg.Project.Module = "github.com/test/projection-mgmt"
	cfg.Database.Driver = "postgres"
	cfg.Database.URL = testDBURL
	err = cfg.SaveFile(filepath.Join(tmpDir, "mink.yaml"))
	require.NoError(t, err)

	// Create multiple projections in database
	_, err = db.Exec(`
		INSERT INTO mink.checkpoints (projection_name, position, status) VALUES
		('OrderSummary', 100, 'active'),
		('CustomerStats', 75, 'active'),
		('ProductInventory', 50, 'paused'),
		('SalesReport', 200, 'active')
	`)
	require.NoError(t, err)

	// Insert events
	// First insert streams
	_, err = db.Exec(`
		INSERT INTO mink.streams (stream_id, version) VALUES
		('stream-1', 2),
		('stream-2', 1)
	`)
	require.NoError(t, err)

	// Then insert events
	_, err = db.Exec(`
		INSERT INTO mink.events (stream_id, version, event_type, data) VALUES
		('stream-1', 1, 'Event1', '{}'),
		('stream-1', 2, 'Event2', '{}'),
		('stream-2', 1, 'Event3', '{}')
	`)
	require.NoError(t, err)

	// List all projections
	t.Log("Listing all projections")
	cmd := NewProjectionCommand()
	cmd.SetArgs([]string{"list"})
	err = cmd.Execute()
	require.NoError(t, err)

	// Get status of each projection
	projections := []string{"OrderSummary", "CustomerStats", "ProductInventory", "SalesReport"}
	for _, proj := range projections {
		t.Logf("Getting status for %s", proj)
		cmd = NewProjectionCommand()
		cmd.SetArgs([]string{"status", proj})
		err = cmd.Execute()
		require.NoError(t, err)
	}

	// Pause active projection
	t.Log("Pausing OrderSummary")
	cmd = NewProjectionCommand()
	cmd.SetArgs([]string{"pause", "OrderSummary"})
	err = cmd.Execute()
	require.NoError(t, err)

	// Resume paused projection
	t.Log("Resuming ProductInventory")
	cmd = NewProjectionCommand()
	cmd.SetArgs([]string{"resume", "ProductInventory"})
	err = cmd.Execute()
	require.NoError(t, err)

	// Rebuild projection
	t.Log("Rebuilding SalesReport")
	cmd = NewProjectionCommand()
	cmd.SetArgs([]string{"rebuild", "SalesReport", "--yes"})
	err = cmd.Execute()
	require.NoError(t, err)

	// Verify final states
	var orderStatus, inventoryStatus string
	var salesPosition int64

	db.QueryRow(`SELECT status FROM mink.checkpoints WHERE projection_name = 'OrderSummary'`).Scan(&orderStatus)
	db.QueryRow(`SELECT status FROM mink.checkpoints WHERE projection_name = 'ProductInventory'`).Scan(&inventoryStatus)
	db.QueryRow(`SELECT position FROM mink.checkpoints WHERE projection_name = 'SalesReport'`).Scan(&salesPosition)

	assert.Equal(t, "paused", orderStatus, "OrderSummary should be paused")
	assert.Equal(t, "active", inventoryStatus, "ProductInventory should be active")
	assert.Equal(t, int64(0), salesPosition, "SalesReport position should be reset to 0")

	t.Log("✓ Projection management E2E test completed successfully!")
}
