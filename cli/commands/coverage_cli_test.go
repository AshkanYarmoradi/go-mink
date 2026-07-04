package commands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStreamTypesCommand_EmptyMemory covers the empty-stream branch of `mink stream types`
// against the in-memory driver (no infra needed).
func TestStreamTypesCommand_EmptyMemory(t *testing.T) {
	env := setupTestEnv(t, "cli-stream-types-empty-*")
	env.createConfig(withDriver("memory"))
	require.NoError(t, executeCmd(NewStreamCommand(), []string{"types", "nonexistent"}))
}

// TestGenerateFile_RefusesOverwrite covers generateFile's overwrite guard: it refuses without
// --force and proceeds with it.
func TestGenerateFile_RefusesOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.go")
	require.NoError(t, os.WriteFile(path, []byte("package p\n"), 0o600))

	err := generateFile(path, "package p\n", struct{}{}, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refusing to overwrite")

	require.NoError(t, generateFile(path, "package p\n", struct{}{}, true), "--force overwrites")
}
