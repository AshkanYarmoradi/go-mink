package testutil

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
)

// safeIdentifier is the identifier shape UniqueSchema promises to return.
var safeIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func TestSanitizeSchemaPrefix(t *testing.T) {
	tests := []struct {
		name, prefix, want string
	}{
		{"valid prefix unchanged", "my_schema", "my_schema"},
		{"mixed case unchanged", "MySchema_01", "MySchema_01"},
		{"leading underscore unchanged", "_x", "_x"},
		{"hyphen replaced", "my-schema", "my_schema"},
		{"space replaced", "my schema", "my_schema"},
		{"dot replaced", "a.b", "a_b"},
		{"quote and statement injection neutralised", `x"; DROP SCHEMA public; --`, "x___DROP_SCHEMA_public____"},
		{"leading digit prefixed", "1abc", "_1abc"},
		{"single digit prefixed", "9", "_9"},
		{"empty becomes test", "", "test"},
		{"non ascii letter replaced", "héllo", "h_llo"},
		{"all non ascii replaced per rune", "日本", "__"},
		{"newline replaced", "a\nb", "a_b"},
		{"null byte replaced", "a\x00b", "a_b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeSchemaPrefix(tt.prefix)
			assert.Equal(t, tt.want, got)
			assert.Regexp(t, safeIdentifier, got)
		})
	}
}

func TestUniqueSchema_SanitizesPrefix(t *testing.T) {
	tests := []struct {
		name, prefix, wantPattern string
	}{
		{"valid prefix unchanged", "my_schema", `^my_schema_\d+$`},
		{"hyphen replaced", "my-schema", `^my_schema_\d+$`},
		{"space replaced", "my schema", `^my_schema_\d+$`},
		{"injection attempt neutralised", `x"; DROP SCHEMA public; --`, `^x___DROP_SCHEMA_public_{5}\d+$`},
		{"leading digit prefixed", "1abc", `^_1abc_\d+$`},
		{"empty becomes test", "", `^test_\d+$`},
		{"non ascii replaced", "héllo", `^h_llo_\d+$`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := UniqueSchema(tt.prefix)
			assert.Regexp(t, tt.wantPattern, got)
			assert.Regexp(t, safeIdentifier, got, "UniqueSchema must always return a plain identifier")
		})
	}
}

func TestUniqueSchema_OutputIsQuoteSafe(t *testing.T) {
	// Even a hostile prefix must not be able to close the quoted identifier that
	// CleanupSchema builds around it.
	got := UniqueSchema(`evil"; DROP SCHEMA public CASCADE; --`)
	assert.NotContains(t, got, `"`)
	assert.NotContains(t, got, `;`)
	assert.Equal(t, `"`+got+`"`, quoteIdentifier(got))
}

func TestDefaultConfig_UsesDefaultPostgresURL(t *testing.T) {
	unsetTestEnv(t, "TEST_DATABASE_URL")

	config := DefaultConfig()

	assert.Equal(t, DefaultPostgresURL, config.PostgresURL)
	// The hard-coded DSN is only acceptable because it targets the local
	// docker-compose test database; guard against it drifting elsewhere.
	assert.Contains(t, DefaultPostgresURL, "@localhost:")
	assert.Contains(t, DefaultPostgresURL, "/mink_test")
}

func TestDefaultConfig_EnvOverridesDefaultPostgresURL(t *testing.T) {
	setTestEnv(t, "TEST_DATABASE_URL", "postgres://ci:ci@db.internal:5433/ci?sslmode=require")

	config := DefaultConfig()

	assert.Equal(t, "postgres://ci:ci@db.internal:5433/ci?sslmode=require", config.PostgresURL)
	assert.NotEqual(t, DefaultPostgresURL, config.PostgresURL)
}
