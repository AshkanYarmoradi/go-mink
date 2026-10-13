package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// leftoverTempFiles lists the ".<name>.<random>.tmp" files WriteSecureFile
// would leave in dir if it failed to clean up after itself.
func leftoverTempFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	var names []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") && strings.HasSuffix(e.Name(), ".tmp") {
			names = append(names, e.Name())
		}
	}
	return names
}

// symlinkOrSkip creates a symlink at link pointing to target, skipping the
// test where the platform or account refuses to create one (on Windows it
// needs Developer Mode or SeCreateSymbolicLinkPrivilege).
func symlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot create symlink on this platform/account: %v", err)
	}
}

// assertRegularFile asserts that path is a regular file, not a link.
func assertRegularFile(t *testing.T, path string) {
	t.Helper()
	info, err := os.Lstat(path)
	require.NoError(t, err)
	assert.True(t, info.Mode().IsRegular(), "path must be a regular file, got mode %v", info.Mode())
	assert.Zero(t, info.Mode()&os.ModeSymlink, "path must not be a symlink")
}

// assertModeUnchanged asserts that path still has its original 0644 mode,
// i.e. that it was not the file WriteSecureFile tightened. POSIX permission
// bits are not enforced on Windows.
func assertModeUnchanged(t *testing.T, path string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), info.Mode().Perm(), "the link target's mode must not be tightened")
}

func TestWriteSecureFile_SymlinkAtPathIsReplacedNotFollowed(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim.txt")
	writeWorldReadable(t, victim, "victim content must survive\n")
	path := filepath.Join(dir, ConfigFileName)
	symlinkOrSkip(t, victim, path)

	const content = "database:\n  url: postgres://u:secret@db/app\n"
	require.NoError(t, WriteSecureFile(path, []byte(content)))

	gotVictim, err := os.ReadFile(victim)
	require.NoError(t, err)
	assert.Equal(t, "victim content must survive\n", string(gotVictim), "the symlink target must not be written through")
	assertModeUnchanged(t, victim)

	assertRegularFile(t, path)
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, content, string(got))
	assertOwnerOnly(t, path)
	assert.Empty(t, leftoverTempFiles(t, dir))
}

func TestWriteSecureFile_DanglingSymlinkAtPathDoesNotCreateTarget(t *testing.T) {
	dir := t.TempDir()
	elsewhere := filepath.Join(dir, "elsewhere")
	require.NoError(t, os.Mkdir(elsewhere, 0o755))
	target := filepath.Join(elsewhere, "planted.json")
	path := filepath.Join(dir, "export.json")
	symlinkOrSkip(t, target, path)

	require.NoError(t, WriteSecureFile(path, []byte(`[{"id":"1"}]`)))

	_, err := os.Lstat(target)
	assert.ErrorIs(t, err, os.ErrNotExist, "a dangling link must not make the write create its target")
	assertRegularFile(t, path)
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, `[{"id":"1"}]`, string(got))
	assertOwnerOnly(t, path)
}

func TestWriteSecureFile_HardLinkAtPathIsReplacedNotWrittenThrough(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim.txt")
	writeWorldReadable(t, victim, "victim content must survive\n")
	path := filepath.Join(dir, "export.json")
	if err := os.Link(victim, path); err != nil {
		t.Skipf("cannot create hard link on this filesystem: %v", err)
	}

	require.NoError(t, WriteSecureFile(path, []byte("new")))

	gotVictim, err := os.ReadFile(victim)
	require.NoError(t, err)
	assert.Equal(t, "victim content must survive\n", string(gotVictim), "the other link to the inode must not see the write")
	assertModeUnchanged(t, victim)

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "new", string(got))
	assertOwnerOnly(t, path)
}

func TestWriteSecureFile_ExistingWorldReadableFileIsReplacedOwnerOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ConfigFileName)
	writeWorldReadable(t, path, "old: content\n")

	require.NoError(t, WriteSecureFile(path, []byte("new: content\n")))

	assertRegularFile(t, path)
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "new: content\n", string(got))
	assertOwnerOnly(t, path)
	assert.Empty(t, leftoverTempFiles(t, dir), "the temporary file must be gone after a successful write")
}

func TestWriteSecureFile_BareFileNameWritesIntoWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	require.NoError(t, WriteSecureFile("export.json", []byte("[]")))

	got, err := os.ReadFile(filepath.Join(dir, "export.json"))
	require.NoError(t, err, "a bare file name must resolve to the working directory, not os.TempDir()")
	assert.Equal(t, "[]", string(got))
	assert.Empty(t, leftoverTempFiles(t, dir))
}

func TestWriteSecureFile_NoTempFileLeftBehindOnError(t *testing.T) {
	parent := t.TempDir()
	// path is an existing directory: the temporary file is created beside it
	// and the final rename fails, so the temporary file must be removed.
	path := filepath.Join(parent, ConfigFileName)
	require.NoError(t, os.Mkdir(path, 0o755))

	start := time.Now()
	err := WriteSecureFile(path, []byte("x"))
	require.Error(t, err)
	assert.Less(t, time.Since(start), renameRetryBudget, "a directory at path is a permanent failure and must not burn the Windows retry budget")

	assert.Empty(t, leftoverTempFiles(t, parent), "a failed write must not leave its temporary file behind")
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.True(t, info.IsDir(), "what was at path is left untouched")
}

func TestWriteSecureFile_UnwritableDirectoryLeavesExistingFileUntouched(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not enforced on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permission checks")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, ConfigFileName)
	writeWorldReadable(t, path, "old: content\n")
	// The file itself is writable, but no entry can be created in the
	// directory: the atomic replace needs directory write permission.
	require.NoError(t, os.Chmod(dir, 0o555))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	err := WriteSecureFile(path, []byte("new: content\n"))
	require.Error(t, err)
	assert.ErrorIs(t, err, os.ErrPermission)

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "old: content\n", string(got), "a failed write must not touch the previous file")
	assert.Empty(t, leftoverTempFiles(t, dir))
}

func TestWriteSecureFile_ConcurrentWritersLeaveOneCompleteFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "export.json")

	const writers = 16
	payloads := make([]string, writers)
	for i := range payloads {
		// Large enough that a torn write (one writer's head, another's tail)
		// would be observable.
		payloads[i] = fmt.Sprintf("writer-%02d:%s\n", i, strings.Repeat(string(rune('a'+i)), 256*1024))
	}

	errs := make([]error, writers)
	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = WriteSecureFile(path, []byte(payloads[i]))
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		require.NoError(t, err, "writer %d", i)
	}

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, payloads, string(got), "the final file must be exactly one writer's complete payload")
	assertRegularFile(t, path)
	assertOwnerOnly(t, path)
	assert.Empty(t, leftoverTempFiles(t, dir), "every writer must clean up its own temporary file")
}

func TestWriteSecureFile_TemporaryFileIsADotfileBesideTheTarget(t *testing.T) {
	// The temporary file must live in the target's directory (so the rename
	// is atomic, not a cross-device copy) and be named after the target.
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", ConfigFileName)
	require.NoError(t, os.Mkdir(filepath.Dir(path), 0o755))

	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	require.NoError(t, err)
	require.NoError(t, tmp.Close())
	base := filepath.Base(tmp.Name())
	assert.True(t, strings.HasPrefix(base, "."+ConfigFileName+"."), "temp name %q must be a dotfile named after the target", base)
	assert.True(t, strings.HasSuffix(base, ".tmp"), "temp name %q must end in .tmp", base)
	assert.Equal(t, filepath.Dir(path), filepath.Dir(tmp.Name()))
	require.NoError(t, os.Remove(tmp.Name()))

	require.NoError(t, WriteSecureFile(path, []byte("x")))
	assert.Empty(t, leftoverTempFiles(t, filepath.Dir(path)))
	assert.Empty(t, leftoverTempFiles(t, dir), "nothing may be written to the parent of the target's directory")
}

func TestWriteSecureFile_ReadOnlyDestination(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ConfigFileName)
	require.NoError(t, os.WriteFile(path, []byte("old\n"), 0o644))
	require.NoError(t, os.Chmod(path, 0o444))
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })

	start := time.Now()
	err := WriteSecureFile(path, []byte("new\n"))
	elapsed := time.Since(start)

	got, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	assert.Empty(t, leftoverTempFiles(t, dir))

	if runtime.GOOS == "windows" {
		// The read-only attribute makes MoveFileEx refuse to replace the
		// file. That is permanent: it must fail at once, not after the retry
		// budget, and the original must be untouched.
		require.Error(t, err)
		assert.Equal(t, "old\n", string(got))
		assert.Less(t, elapsed, renameRetryBudget, "a read-only destination is a permanent failure and must not burn the retry budget")
		return
	}
	// On Unix only the directory's permissions matter for a rename: a
	// read-only file is replaced like any other and ends up 0600.
	require.NoError(t, err)
	assert.Equal(t, "new\n", string(got))
	assertOwnerOnly(t, path)
}

func TestIsTransientWindowsRenameError(t *testing.T) {
	// The classifier is only consulted on Windows, but its logic (error
	// class plus what sits at the destination) is platform-neutral.
	dir := t.TempDir()
	writable := filepath.Join(dir, "writable.txt")
	require.NoError(t, os.WriteFile(writable, []byte("x"), 0o644))
	readOnly := filepath.Join(dir, "readonly.txt")
	require.NoError(t, os.WriteFile(readOnly, []byte("x"), 0o644))
	require.NoError(t, os.Chmod(readOnly, 0o444))
	t.Cleanup(func() { _ = os.Chmod(readOnly, 0o644) })
	asDir := filepath.Join(dir, "dir")
	require.NoError(t, os.Mkdir(asDir, 0o755))
	missing := filepath.Join(dir, "missing.txt")

	linkErr := func(err error) error {
		return &os.LinkError{Op: "rename", Old: "tmp", New: "dst", Err: err}
	}
	denied := linkErr(os.ErrPermission)
	sharing := linkErr(errnoSharingViolation)

	tests := []struct {
		name string
		err  error
		path string
		want bool
	}{
		{"not a permission error", linkErr(os.ErrNotExist), writable, false},
		{"plain error", errors.New("boom"), missing, false},
		{"access denied, destination absent", denied, missing, true},
		{"access denied, destination is a writable file", denied, writable, true},
		{"access denied, destination is a directory", denied, asDir, false},
		{"access denied, destination is read-only", denied, readOnly, false},
		{"sharing violation, destination absent", sharing, missing, true},
		{"sharing violation, destination is a directory", sharing, asDir, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isTransientWindowsRenameError(tt.err, tt.path))
		})
	}
}
