package testutil

import (
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// safeIdentifier is the identifier shape UniqueSchema promises to return.
var safeIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// hexNonce is the shape of the per-process nonce in a UniqueSchema suffix.
var hexNonce = regexp.MustCompile(`^[0-9a-f]{8}$`)

// uniqueSchemaParts is a UniqueSchema name split into its documented pieces.
type uniqueSchemaParts struct {
	prefix string
	nanos  int64
	nonce  string
	seq    uint64
}

// splitUniqueSchema parses "<prefix>_<UnixNano>_<nonce>_<n>" from the right, so a
// prefix that itself contains underscores is handled, and fails the test if any
// piece is missing or malformed.
func splitUniqueSchema(t *testing.T, name string) uniqueSchemaParts {
	t.Helper()
	parts := strings.Split(name, "_")
	require.GreaterOrEqual(t, len(parts), 4, "name %q must carry the three-part suffix", name)
	n := len(parts)
	nanos, err := strconv.ParseInt(parts[n-3], 10, 64)
	require.NoError(t, err, "UnixNano part of %q", name)
	seq, err := strconv.ParseUint(parts[n-1], 10, 64)
	require.NoError(t, err, "counter part of %q", name)
	return uniqueSchemaParts{
		prefix: strings.Join(parts[:n-3], "_"),
		nanos:  nanos,
		nonce:  parts[n-2],
		seq:    seq,
	}
}

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
		// The suffix is "_<UnixNano>_<8-hex nonce>_<counter>".
		{"valid prefix unchanged", "my_schema", `^my_schema_\d+_[0-9a-f]{8}_\d+$`},
		{"hyphen replaced", "my-schema", `^my_schema_\d+_[0-9a-f]{8}_\d+$`},
		{"space replaced", "my schema", `^my_schema_\d+_[0-9a-f]{8}_\d+$`},
		{"injection attempt neutralised", `x"; DROP SCHEMA public; --`, `^x___DROP_SCHEMA_public_{5}\d+_[0-9a-f]{8}_\d+$`},
		{"leading digit prefixed", "1abc", `^_1abc_\d+_[0-9a-f]{8}_\d+$`},
		{"empty becomes test", "", `^test_\d+_[0-9a-f]{8}_\d+$`},
		{"non ascii replaced", "héllo", `^h_llo_\d+_[0-9a-f]{8}_\d+$`},
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
// PostgreSQL's 63-byte identifier limit would otherwise cause: with a long
// prefix the server used to truncate the uniqueness suffix away, so successive
// calls named the same schema and CleanupSchema could drop another test's
// schema. UniqueSchema truncates the prefix itself and keeps the whole
// "_<UnixNano>_<nonce>_<n>" suffix.
func TestUniqueSchema_LongPrefixKeepsSuffix(t *testing.T) {
	tests := []struct {
		name   string
		prefix string
	}{
		{"short prefix kept whole", "short"},
		{"prefix at the guaranteed budget kept whole", strings.Repeat("a", maxPrefixBytes)},
		{"prefix with underscores kept whole", "Test_Some_Thing"},
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

			// The WHOLE suffix survives, and the sanitised prefix fills whatever is left.
			got := splitUniqueSchema(t, first)
			suffixLen := len(first) - len(got.prefix)
			wantPrefix := sanitizeSchemaPrefix(tt.prefix)
			if maxPrefix := pgMaxIdentifierBytes - suffixLen; len(wantPrefix) > maxPrefix {
				wantPrefix = wantPrefix[:maxPrefix]
			}
			assert.Equal(t, wantPrefix, got.prefix, "got %q", first)
			assert.WithinDuration(t, time.Now(), time.Unix(0, got.nanos), time.Minute, "the complete UnixNano must survive: %q", first)
			assert.Equal(t, processNonce, got.nonce, "the nonce is the same for the whole process")
			assert.Regexp(t, hexNonce, got.nonce)

			// Two calls never collide, deterministically: the counter decides, not the
			// clock's resolution and never the server's truncation.
			second := UniqueSchema(tt.prefix)
			assert.NotEqual(t, first, second, "successive calls with a long prefix must produce distinct names")
			assert.LessOrEqual(t, len(second), pgMaxIdentifierBytes)
			assert.Equal(t, got.seq+1, splitUniqueSchema(t, second).seq, "the counter advances by one per call")
		})
	}
}

// TestUniqueSchema_SequentialCallsAreDistinct: 10,000 back-to-back calls — far
// faster than any wall clock ticks — all yield distinct, bounded identifiers.
// Before the counter, two calls within one clock tick returned the same name.
func TestUniqueSchema_SequentialCallsAreDistinct(t *testing.T) {
	const n = 10_000
	seen := make(map[string]struct{}, n)
	for i := 0; i < n; i++ {
		name := UniqueSchema("seq")
		if len(name) > pgMaxIdentifierBytes {
			t.Fatalf("%q exceeds %d bytes", name, pgMaxIdentifierBytes)
		}
		if !safeIdentifier.MatchString(name) {
			t.Fatalf("%q is not a plain identifier", name)
		}
		if _, dup := seen[name]; dup {
			t.Fatalf("duplicate schema name %q after %d calls", name, i)
		}
		seen[name] = struct{}{}
	}
	assert.Len(t, seen, n)
}

// TestUniqueSchema_ConcurrentCallsAreDistinct: 100 goroutines x 100 calls racing
// on the same (long, truncated) prefix never collide. This is the parallel-test
// scenario in which one test's CleanupSchema used to cascade-drop another's.
func TestUniqueSchema_ConcurrentCallsAreDistinct(t *testing.T) {
	const goroutines, perGoroutine = 100, 100
	prefix := "TestParallel_" + strings.Repeat("x", 100) // long enough to be truncated

	results := make(chan string, goroutines*perGoroutine)
	var start, done sync.WaitGroup
	start.Add(1)
	for g := 0; g < goroutines; g++ {
		done.Add(1)
		go func() {
			defer done.Done()
			start.Wait() // release every goroutine at once to maximise contention
			for i := 0; i < perGoroutine; i++ {
				results <- UniqueSchema(prefix)
			}
		}()
	}
	start.Done()
	done.Wait()
	close(results)

	seen := make(map[string]struct{}, goroutines*perGoroutine)
	for name := range results {
		if len(name) > pgMaxIdentifierBytes {
			t.Fatalf("%q exceeds %d bytes", name, pgMaxIdentifierBytes)
		}
		if !safeIdentifier.MatchString(name) {
			t.Fatalf("%q is not a plain identifier", name)
		}
		if _, dup := seen[name]; dup {
			t.Fatalf("duplicate schema name %q under concurrency", name)
		}
		seen[name] = struct{}{}
	}
	assert.Len(t, seen, goroutines*perGoroutine)
}

// TestUniqueSchema_PrefixBudget pins the documented guarantee: while a process
// has generated fewer than 100,000 names, at least maxPrefixBytes (28) bytes of
// prefix survive whole.
func TestUniqueSchema_PrefixBudget(t *testing.T) {
	assert.Equal(t, 30, fixedSuffixBytes, `"_" + 19 digits + "_" + 8 hex + "_"`)
	assert.Equal(t, 28, maxPrefixBytes)
	if schemaSeq.Load() >= 99_998 {
		t.Skip("counter already past the five-digit budget this test pins")
	}
	prefix := strings.Repeat("q", maxPrefixBytes)
	name := UniqueSchema(prefix)
	assert.LessOrEqual(t, len(name), pgMaxIdentifierBytes)
	assert.Equal(t, prefix, splitUniqueSchema(t, name).prefix, "a prefix within the budget is never truncated")
}

func TestNewProcessNonce(t *testing.T) {
	assert.Regexp(t, hexNonce, processNonce, "the package-level nonce is 8 lowercase hex digits")
	a, b := newProcessNonce(), newProcessNonce()
	assert.Regexp(t, hexNonce, a)
	assert.Regexp(t, hexNonce, b)
	assert.NotEqual(t, a, b, "nonces are random (a 1 in 2^32 coincidence would fail this)")
}
