package commands

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// Symlink-aware containment (paths.go)
// ============================================================================

// mustSymlink creates a symbolic link at link pointing at target. Creating a
// symlink needs a privilege (or Developer Mode) on Windows, so the test is
// skipped when the platform refuses rather than failed.
func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(link), 0o755))
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot create a symlink on this platform: %v", err)
	}
}

func TestEnsureInsideDir_SymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "existing.sql"), []byte("SELECT 1;"), 0o644))

	link := filepath.Join(root, "internal", "link")
	mustSymlink(t, outside, link)

	tests := []struct {
		name string
		path string
	}{
		{"the link itself", link},
		{"existing file reached through the link", filepath.Join(link, "existing.sql")},
		{"not-yet-created directory below the link", filepath.Join(link, "domain")},
		{"not-yet-created file several levels below the link", filepath.Join(link, "domain", "orders", "order.go")},
		{"trailing separator", link + string(filepath.Separator)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ensureInsideDir(root, tt.path)
			require.Error(t, err, "a lexically contained path that resolves through a link to %s must be rejected", outside)
			assert.Contains(t, err.Error(), "resolves outside")
		})
	}
}

func TestEnsureInsideDir_SymlinkToParentEscapes(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "project")
	require.NoError(t, os.MkdirAll(root, 0o755))

	// <root>/up points at the directory holding the project: anything below
	// it that is not the project itself is a sibling of the root.
	up := filepath.Join(root, "up")
	mustSymlink(t, parent, up)

	err := ensureInsideDir(root, filepath.Join(up, "sibling", "file.go"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolves outside")

	// ... whereas going back into the project through the link is contained.
	assert.NoError(t, ensureInsideDir(root, filepath.Join(up, "project", "internal", "file.go")))
}

func TestEnsureInsideDir_SymlinkStayingInsideIsAllowed(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	require.NoError(t, os.MkdirAll(realDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(realDir, "existing.sql"), []byte("SELECT 1;"), 0o644))

	alias := filepath.Join(root, "alias")
	mustSymlink(t, realDir, alias)

	for _, p := range []string{
		alias,
		filepath.Join(alias, "existing.sql"),
		filepath.Join(alias, "new", "file.go"),
	} {
		assert.NoError(t, ensureInsideDir(root, p), "path %q", p)
	}
}

func TestEnsureInsideDir_RootReachedThroughSymlink(t *testing.T) {
	base := t.TempDir()
	realRoot := filepath.Join(base, "real-root")
	require.NoError(t, os.MkdirAll(realRoot, 0o755))

	rootLink := filepath.Join(base, "root-link")
	mustSymlink(t, realRoot, rootLink)

	tests := []struct {
		name string
		root string
		path string
	}{
		{"link root, path under the link", rootLink, filepath.Join(rootLink, "migrations", "001.sql")},
		{"link root, path under the real directory", rootLink, filepath.Join(realRoot, "migrations", "001.sql")},
		{"real root, path under the link", realRoot, filepath.Join(rootLink, "migrations", "001.sql")},
		{"link root is itself", rootLink, rootLink},
		{"link root, real directory is itself", rootLink, realRoot},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.NoError(t, ensureInsideDir(tt.root, tt.path))
		})
	}

	err := ensureInsideDir(rootLink, filepath.Join(base, "elsewhere"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolves outside")
}

func TestEnsureInsideDir_DanglingSymlinkIsRejected(t *testing.T) {
	root := t.TempDir()
	dangling := filepath.Join(root, "dangling")
	mustSymlink(t, filepath.Join(t.TempDir(), "never-created"), dangling)

	// os.MkdirAll would follow the link and create its target, so a location
	// that cannot be resolved is not approved even though it looks contained.
	for _, p := range []string{dangling, filepath.Join(dangling, "domain", "order.go")} {
		err := ensureInsideDir(root, p)
		require.Error(t, err, "path %q", p)
		assert.Contains(t, err.Error(), "resolve path")
	}
}

func TestEnsureInsideDir_SymlinkedRootThatCannotBeResolvedIsRejected(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root-link")
	mustSymlink(t, filepath.Join(base, "never-created"), root)

	err := ensureInsideDir(root, filepath.Join(root, "file.go"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolve root")
}

func TestEnsureInsideDir_FileAsIntermediateComponent(t *testing.T) {
	// <root>/file.txt exists as a regular file. A path "below" it can never
	// be created, but it is lexically contained and hides no link, so the
	// containment check passes and the later filesystem operation fails on
	// its own terms.
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "file.txt"), []byte("x"), 0o644))

	assert.NoError(t, ensureInsideDir(root, filepath.Join(root, "file.txt", "sub", "x.go")))
}

func TestEnsureInsideDir_NothingOnThePathExistsYet(t *testing.T) {
	// Neither the root nor the path exists: the check is lexical over the
	// resolved longest existing prefix (the temp directory).
	root := filepath.Join(t.TempDir(), "not-yet", "project")

	assert.NoError(t, ensureInsideDir(root, filepath.Join(root, "migrations", "001.sql")))
	assert.NoError(t, ensureInsideDir(root, root))

	err := ensureInsideDir(root, filepath.Join(root, "..", "sibling"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolves outside")
}

func TestResolvePath_ExistingPathMatchesEvalSymlinks(t *testing.T) {
	dir := t.TempDir()
	want, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)

	got, err := resolvePath(dir)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestResolvePath_MissingTailIsAppendedLexically(t *testing.T) {
	dir := t.TempDir()
	resolvedDir, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)

	got, err := resolvePath(filepath.Join(dir, "a", "b", "c.sql"))
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(resolvedDir, "a", "b", "c.sql"), got)
}

func TestResolvePath_MissingTailThroughSymlinkResolvesTheLink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	resolvedOutside, err := filepath.EvalSymlinks(outside)
	require.NoError(t, err)

	link := filepath.Join(root, "link")
	mustSymlink(t, outside, link)

	got, err := resolvePath(filepath.Join(link, "a", "b.go"))
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(resolvedOutside, "a", "b.go"), got)
}

func TestResolvePath_UnmountedVolumeFallsBackToLexicalPath(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("volume names only exist on Windows")
	}
	var missing string
	for letter := 'Z'; letter >= 'D'; letter-- {
		candidate := string(letter) + `:\`
		if _, err := os.Lstat(candidate); err != nil {
			missing = candidate
			break
		}
	}
	if missing == "" {
		t.Skip("every drive letter from D: to Z: is in use")
	}

	path := filepath.Join(missing, "elsewhere", "file.sql")
	got, err := resolvePath(path)
	require.NoError(t, err)
	assert.Equal(t, path, got)
}

// ============================================================================
// Code generation through a symlinked output directory (generate.go)
// ============================================================================

func TestGenerate_OutputDirThroughSymlink_Rejected(t *testing.T) {
	env := setupTestEnv(t, "mink-sec-gen-symlink-*")
	outside := t.TempDir()
	mustSymlink(t, outside, filepath.Join(env.tmpDir, "internal", "link"))

	env.createConfig(withModule("github.com/test/project"), withDriver("memory"),
		withAggregatePackage(filepath.Join("internal", "link", "domain")))

	err := executeCmd(NewGenerateCommand(), []string{"aggregate", "Order", "--non-interactive"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "generation.aggregate_package")
	assert.Contains(t, err.Error(), "resolves outside")

	entries, err := os.ReadDir(outside)
	require.NoError(t, err)
	assert.Empty(t, entries, "nothing may be written through the link")
	assertNoGoFiles(t, env.tmpDir)
}

func TestGenerate_DotDotAfterSymlinkIsCleanedBeforeUse(t *testing.T) {
	// "internal/link/../domain" cleans to the contained internal/domain. The
	// generator must create exactly that directory; handing the raw string to
	// the OS would resolve it through the link to <outside's parent>/domain.
	env := setupTestEnv(t, "mink-sec-gen-dotdot-*")
	outsideParent := t.TempDir()
	outside := filepath.Join(outsideParent, "target")
	require.NoError(t, os.MkdirAll(outside, 0o755))
	mustSymlink(t, outside, filepath.Join(env.tmpDir, "internal", "link"))

	env.createConfig(withModule("github.com/test/project"), withDriver("memory"),
		withAggregatePackage("internal/link/../domain"))

	err := executeCmd(NewGenerateCommand(), []string{"aggregate", "Order", "--non-interactive"})
	require.NoError(t, err)

	assert.FileExists(t, filepath.Join(env.tmpDir, "internal", "domain", "order.go"))
	_, statErr := os.Stat(filepath.Join(outsideParent, "domain"))
	assert.True(t, os.IsNotExist(statErr), "no directory may be created by resolving .. through the link")
}
