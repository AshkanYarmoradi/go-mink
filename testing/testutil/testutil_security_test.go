package testutil

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// TestUniqueSchema_LongPrefixKeepsSuffix guards against the silent collision
// PostgreSQL's 63-byte identifier limit would otherwise cause: with a prefix
// longer than 43 bytes the server used to truncate the "_<UnixNano>" suffix
// away, so successive calls named the same schema and CleanupSchema could drop
// another test's schema. UniqueSchema now truncates the prefix itself.
func TestUniqueSchema_LongPrefixKeepsSuffix(t *testing.T) {
	const maxPrefix = pgMaxIdentifierBytes - 1 - 19 // "_" + 19-digit UnixNano

	tests := []struct {
		name   string
		prefix string
	}{
		{"short prefix kept whole", "short"},
		{"prefix at the limit kept whole", strings.Repeat("a", maxPrefix)},
		{"prefix one over the limit truncated", strings.Repeat("b", maxPrefix+1)},
		{"long prefix truncated", strings.Repeat("c", 60)},
		{"very long test-name prefix truncated", "TestSomethingVeryDescriptive_" + strings.Repeat("x", 200)},
		{"long hostile prefix sanitised then truncated", strings.Repeat(`"; DROP SCHEMA public; --`, 5)},
		{"long non ascii prefix sanitised then truncated", strings.Repeat("é", 50)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			first := UniqueSchema(tt.prefix)
			assert.LessOrEqual(t, len(first), pgMaxIdentifierBytes, "must fit PostgreSQL's identifier limit")
			assert.Regexp(t, safeIdentifier, first)

			// The sanitised prefix survives up to the limit, and the WHOLE suffix after it.
			wantPrefix := sanitizeSchemaPrefix(tt.prefix)
			if len(wantPrefix) > maxPrefix {
				wantPrefix = wantPrefix[:maxPrefix]
			}
			require.True(t, strings.HasPrefix(first, wantPrefix+"_"), "got %q, want prefix %q", first, wantPrefix)
			nanos, err := strconv.ParseInt(strings.TrimPrefix(first, wantPrefix+"_"), 10, 64)
			require.NoError(t, err, "the complete UnixNano suffix must survive: %q", first)
			assert.WithinDuration(t, time.Now(), time.Unix(0, nanos), time.Minute)

			// Two calls never collide: the clock decides the name, never the server's
			// truncation. Polled rather than asserted on one call so a coarse platform
			// clock cannot make this flaky.
			var second string
			require.Eventually(t, func() bool {
				second = UniqueSchema(tt.prefix)
				return second != first
			}, 5*time.Second, time.Millisecond, "successive calls with a long prefix must produce distinct names")
			assert.LessOrEqual(t, len(second), pgMaxIdentifierBytes)
		})
	}
}
