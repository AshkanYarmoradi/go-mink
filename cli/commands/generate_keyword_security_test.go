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
// Package names that are Go keywords or the blank identifier (generate.go)
// ============================================================================

// goKeywords is the complete keyword list of the Go language specification.
// Every one of them has the shape of a package identifier and is invalid in a
// package clause.
var goKeywords = []string{
	"break", "case", "chan", "const", "continue",
	"default", "defer", "else", "fallthrough", "for",
	"func", "go", "goto", "if", "import",
	"interface", "map", "package", "range", "return",
	"select", "struct", "switch", "type", "var",
}

func TestPackageNameFromPath_RejectsKeywordsAndBlankIdentifier(t *testing.T) {
	type tc struct {
		name     string
		path     string
		want     string
		wantErr  bool
		wantHint string
	}
	tests := []tc{
		{"blank identifier", "internal/_", "", true, "blank identifier"},
		{"blank identifier alone", "_", "", true, "blank identifier"},
		{"keyword as a parent element is fine", "type/domain", "domain", false, ""},
		{"keyword with a suffix is an ordinary identifier", "internal/types", "types", false, ""},
		{"keyword with a prefix is an ordinary identifier", "internal/mytype", "mytype", false, ""},
		{"predeclared identifier is not a keyword", "internal/string", "string", false, ""},
		{"predeclared type is not a keyword", "internal/error", "error", false, ""},
		{"keyword with trailing separator", "internal/func/", "", true, "Go keyword"},
		{"capitalised keyword fails the shape check instead", "internal/Type", "", true, "last path element must match"},
	}
	for _, kw := range goKeywords {
		tests = append(tests, tc{"keyword " + kw, filepath.Join("internal", kw), "", true, "Go keyword"})
		tests = append(tests, tc{"keyword " + kw + " alone", kw, "", true, "Go keyword"})
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := packageNameFromPath("event_package", tt.path)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "generation.event_package")
				assert.Contains(t, err.Error(), "valid Go package name")
				assert.Contains(t, err.Error(), tt.wantHint)
				assert.Empty(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestGenerate_KeywordOutputPackage_Rejected(t *testing.T) {
	tests := []struct {
		name    string
		opt     configOption
		setting string
		args    []string
	}{
		{"aggregate_package", withAggregatePackage("internal/type"), "aggregate_package",
			[]string{"aggregate", "Order", "--non-interactive"}},
		{"event_package", withEventPackage("internal/func"), "event_package",
			[]string{"event", "OrderCreated", "--aggregate", "Order", "--non-interactive"}},
		{"projection_package", withProjectionPackage("internal/package"), "projection_package",
			[]string{"projection", "OrderSummary", "--events", "OrderCreated", "--non-interactive"}},
		{"command_package", withCommandPackage("internal/import"), "command_package",
			[]string{"command", "CreateOrder", "--aggregate", "Order", "--non-interactive"}},
		{"blank identifier", withAggregatePackage("internal/_"), "aggregate_package",
			[]string{"aggregate", "Order", "--non-interactive"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := setupTestEnv(t, "mink-sec-gen-keyword-*")
			env.createConfig(withModule("github.com/test/project"), withDriver("memory"), tt.opt)

			err := executeCmd(NewGenerateCommand(), tt.args)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "generation."+tt.setting)
			assert.Contains(t, err.Error(), "valid Go package name")
			assertNoGoFiles(t, env.tmpDir)
		})
	}
}

// ============================================================================
// Secure overwrite of the stream export (projection.go)
// ============================================================================

func TestStreamExport_OverwritingWorldReadableFileTightensMode(t *testing.T) {
	env := setupTestEnv(t, "mink-sec-export-overwrite-*")
	env.createConfig(withDriver("memory"))
	out := filepath.Join(env.tmpDir, "export.json")

	// A pre-existing export left world-readable by some other tool.
	require.NoError(t, os.WriteFile(out, []byte("stale and much longer than the replacement content\n"), 0o644))
	require.NoError(t, os.Chmod(out, 0o644))

	err := executeCmd(NewStreamCommand(), []string{"export", "test-stream", "--output", out})
	require.NoError(t, err)

	data, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.JSONEq(t, "[]", string(data), "the export replaces the previous content entirely")

	if runtime.GOOS == "windows" {
		t.Log("skipping mode assertion: POSIX permission bits are not enforced on Windows")
		return
	}
	info, err := os.Stat(out)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "exported event payloads may contain PII")
}
