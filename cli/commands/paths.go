package commands

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// ensureInsideDir verifies that path, once made absolute, cleaned and with
// symbolic links resolved, lies inside root (or is root itself). It is the
// containment check applied to every file location the CLI derives from
// untrusted input: values read from mink.yaml and rows read back from the
// migrations table.
//
// Relative paths are resolved against the current working directory, matching
// how the CLI has always interpreted them. The check is not purely lexical: a
// symbolic link inside the project (for example <root>/internal/link pointing
// at a directory elsewhere) would otherwise let a configured output path
// escape the root while looking contained. Both root and path therefore have
// their symlinks evaluated first — see resolvePath for how a path whose tail
// does not exist yet is handled — and the containment test runs on the
// resolved locations. A path that resolves to a different volume, to an
// ancestor of root, or to a sibling tree is rejected with an error that names
// both the offending path and the root it had to stay in.
func ensureInsideDir(root, path string) error {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve root %q: %w", root, err)
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve path %q: %w", path, err)
	}

	realRoot, err := resolvePath(absRoot)
	if err != nil {
		return fmt.Errorf("resolve root %q: %w", root, err)
	}
	realPath, err := resolvePath(absPath)
	if err != nil {
		return fmt.Errorf("resolve path %q: %w", path, err)
	}

	rel, err := filepath.Rel(realRoot, realPath)
	if err != nil {
		// Different volumes (Windows) or otherwise unrelated: not inside.
		return fmt.Errorf("path %q resolves outside %q", path, absRoot)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path %q resolves outside %q", path, absRoot)
	}
	return nil
}

// resolvePath returns path with symbolic links evaluated. path must be
// absolute and cleaned (as filepath.Abs returns it).
//
// The locations the CLI checks are usually about to be created, so the whole
// path cannot simply be handed to filepath.EvalSymlinks: it fails on anything
// that does not exist. Instead the longest existing prefix of path is found by
// walking up one component at a time, that prefix is resolved with
// EvalSymlinks, and the not-yet-created tail is appended back lexically. The
// tail cannot hide a link because none of it exists yet; everything that does
// exist — including a link whose target is outside the tree — is resolved to
// where it really points.
//
// A prefix that exists but cannot be resolved (a dangling link, a link loop)
// is an error rather than a pass: os.MkdirAll would follow such a link to
// wherever it points, so the location must not be approved. When nothing on
// the way up exists at all (for example a volume that is not mounted) there
// are no links to follow, and the lexical path is returned unchanged.
func resolvePath(path string) (string, error) {
	prefix := path
	tail := ""
	for {
		if _, err := os.Lstat(prefix); err == nil {
			resolved, err := filepath.EvalSymlinks(prefix)
			if err != nil {
				return "", err
			}
			return filepath.Join(resolved, tail), nil
		} else if !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR) {
			return "", err
		}

		parent := filepath.Dir(prefix)
		if parent == prefix {
			return path, nil
		}
		tail = filepath.Join(filepath.Base(prefix), tail)
		prefix = parent
	}
}
