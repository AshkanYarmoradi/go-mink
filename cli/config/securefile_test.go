package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// assertOwnerOnly asserts that path is mode 0600. POSIX permission bits are
// not enforced on Windows, so there only the content assertions apply.
func assertOwnerOnly(t *testing.T, path string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Log("skipping mode assertion: POSIX permission bits are not enforced on Windows")
		return
	}
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, ConfigFileMode, info.Mode().Perm(), "file must be owner-read/write only")
}

// writeWorldReadable plants a pre-existing 0644 file at path. The explicit
// Chmod makes the test independent of the process umask.
func writeWorldReadable(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	require.NoError(t, os.Chmod(path, 0o644))
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o644), info.Mode().Perm(), "precondition: the file starts out world-readable")
	}
}

func TestWriteSecureFile_TightensPermissionsOfExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigFileName)
	writeWorldReadable(t, path, "database:\n  url: postgres://user:old-secret@db/app\n")

	require.NoError(t, WriteSecureFile(path, []byte("database:\n  url: postgres://user:new-secret@db/app\n")))

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "database:\n  url: postgres://user:new-secret@db/app\n", string(got), "content is replaced")
	assertOwnerOnly(t, path)
}

func TestWriteSecureFile_CreatesOwnerOnlyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "export.json")

	require.NoError(t, WriteSecureFile(path, []byte(`[{"id":"1"}]`)))

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, `[{"id":"1"}]`, string(got))
	assertOwnerOnly(t, path)
}

func TestWriteSecureFile_ShorterContentTruncatesOldContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.txt")
	writeWorldReadable(t, path, "a much longer body of text than the replacement\n")

	require.NoError(t, WriteSecureFile(path, []byte("short\n")))

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "short\n", string(got), "no tail of the previous content may survive")
}

func TestWriteSecureFile_EmptyContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.txt")
	writeWorldReadable(t, path, "previous")

	require.NoError(t, WriteSecureFile(path, nil))

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Empty(t, got)
	assertOwnerOnly(t, path)
}

func TestWriteSecureFile_Errors(t *testing.T) {
	tmp := t.TempDir()
	tests := []struct {
		name string
		path string
	}{
		{"missing parent directory", filepath.Join(tmp, "missing", "mink.yaml")},
		{"path is a directory", tmp},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := WriteSecureFile(tt.path, []byte("x"))
			require.Error(t, err)
		})
	}
}

func TestSaveFile_TightensExistingWorldReadableConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigFileName)
	writeWorldReadable(t, path, "version: \"1\"\nproject:\n  name: stale\n")

	cfg := DefaultConfig()
	cfg.Project.Name = "rewritten"
	cfg.Database.URL = "postgres://user:secret@localhost/db"
	require.NoError(t, cfg.SaveFile(path))

	loaded, err := LoadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "rewritten", loaded.Project.Name)
	assert.Equal(t, "postgres://user:secret@localhost/db", loaded.Database.URL)
	assertOwnerOnly(t, path)
}

func TestSave_TightensExistingWorldReadableConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ConfigFileName)
	writeWorldReadable(t, path, "version: \"1\"\n")

	cfg := DefaultConfig()
	require.NoError(t, cfg.Save(dir))

	assertOwnerOnly(t, path)
}
