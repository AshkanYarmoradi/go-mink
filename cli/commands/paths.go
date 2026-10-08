package commands

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ensureInsideDir verifies that path, once made absolute and cleaned, lies
// inside root (or is root itself). It is the containment check applied to
// every file location the CLI derives from untrusted input: values read from
// mink.yaml and rows read back from the migrations table.
//
// Relative paths are resolved against the current working directory, matching
// how the CLI has always interpreted them. A path that resolves to a different
// volume, to an ancestor of root, or to a sibling tree is rejected with an
// error that names both the offending path and the root it had to stay in.
func ensureInsideDir(root, path string) error {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve root %q: %w", root, err)
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve path %q: %w", path, err)
	}

	rel, err := filepath.Rel(absRoot, absPath)
	if err != nil {
		// Different volumes (Windows) or otherwise unrelated: not inside.
		return fmt.Errorf("path %q resolves outside %q", path, absRoot)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path %q resolves outside %q", path, absRoot)
	}
	return nil
}
