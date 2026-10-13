//go:build integration
// +build integration

package commands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Postgres-backed security regression tests for `mink migrate down`. They
// reuse the integration harness in integration_test.go and skip when the test
// database is unavailable or -short is set.

func TestMigrateDown_DeclinedConfirmation_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}
	env := setupIntegrationEnv(t, "mink-sec-down-confirm-*")
	migrationsDir := env.createConfigWithMigrations()

	require.NoError(t, os.WriteFile(filepath.Join(migrationsDir, "001_sec_confirm.sql"),
		[]byte(`CREATE TABLE IF NOT EXISTS test_sec_confirm (id INT);`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(migrationsDir, "001_sec_confirm.down.sql"),
		[]byte(`DROP TABLE IF EXISTS test_sec_confirm;`), 0o644))
	t.Cleanup(func() { _, _ = env.db.Exec(`DROP TABLE IF EXISTS test_sec_confirm`) })

	require.NoError(t, executeCmd(NewMigrateCommand(), []string{"up", "--non-interactive"}))

	tableExists := func() bool {
		var exists bool
		require.NoError(t, env.db.QueryRow(
			`SELECT EXISTS (SELECT FROM information_schema.tables WHERE table_name = 'test_sec_confirm')`).Scan(&exists))
		return exists
	}
	require.True(t, tableExists(), "table should exist after migrate up")

	// Declining the prompt leaves the schema untouched.
	var asked []string
	declined := newMigrateDownCommandWithConfirm(func(names []string) (bool, error) {
		asked = names
		return false, nil
	})
	require.NoError(t, executeCmd(declined, []string{}))
	assert.Equal(t, []string{"001_sec_confirm"}, asked)
	assert.True(t, tableExists(), "declined rollback must not execute the down migration")

	// --yes waives the prompt entirely.
	waived := newMigrateDownCommandWithConfirm(func([]string) (bool, error) {
		t.Fatal("the confirmer must not be called with --yes")
		return false, nil
	})
	require.NoError(t, executeCmd(waived, []string{"--yes"}))
	assert.False(t, tableExists(), "--yes must roll back without prompting")
}

func TestMigrateDown_PoisonedMigrationRow_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}
	env := setupIntegrationEnv(t, "mink-sec-down-poison-*")
	migrationsDir := env.createConfigWithMigrations()

	require.NoError(t, os.WriteFile(filepath.Join(migrationsDir, "001_sec_poison.sql"),
		[]byte(`CREATE TABLE IF NOT EXISTS test_sec_poison (id INT);`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(migrationsDir, "001_sec_poison.down.sql"),
		[]byte(`DROP TABLE IF EXISTS test_sec_poison;`), 0o644))
	t.Cleanup(func() { _, _ = env.db.Exec(`DROP TABLE IF EXISTS test_sec_poison`) })

	require.NoError(t, executeCmd(NewMigrateCommand(), []string{"up", "--non-interactive"}))

	// A row that points outside the migrations directory.
	require.NoError(t, recordMigration(env.db, "../../evil"))

	err := executeCmd(NewMigrateCommand(), []string{"down", "--steps", "2", "--yes"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid migration name")

	var exists bool
	require.NoError(t, env.db.QueryRow(
		`SELECT EXISTS (SELECT FROM information_schema.tables WHERE table_name = 'test_sec_poison')`).Scan(&exists))
	assert.True(t, exists, "a poisoned migrations table must abort the rollback before any SQL runs")

	_, err = getAppliedMigrationsHelper(testDBURL, migrationsDir)
	assert.Error(t, err)
}
