package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	mink "go-mink.dev"
	"go-mink.dev/cli/config"
)

// ============================================================================
// mink.yaml environment-variable expansion allowlist (adapter.go)
// ============================================================================

func TestExpandConfigEnv_AllowedReferences(t *testing.T) {
	t.Setenv(configEnvAllowVar, "")
	t.Setenv("MINK_TEST_DSN", "postgres://mink")
	t.Setenv("DATABASE_URL", "postgres://database")
	t.Setenv("DB_URL", "postgres://db")
	t.Setenv("PGPASSWORD", "pgpw")
	t.Setenv("POSTGRES_URL", "postgres://postgres")
	require.NoError(t, os.Unsetenv("MINK_UNSET_VARIABLE_FOR_TEST"))

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"no reference passes through", "postgres://localhost:5432/db?sslmode=disable", "postgres://localhost:5432/db?sslmode=disable"},
		{"brace syntax", "${DATABASE_URL}", "postgres://database"},
		{"bare syntax", "$DATABASE_URL", "postgres://database"},
		{"MINK_ prefix", "${MINK_TEST_DSN}", "postgres://mink"},
		{"DB_ prefix", "${DB_URL}", "postgres://db"},
		{"PG prefix spliced into a DSN", "postgres://u:${PGPASSWORD}@h/d", "postgres://u:pgpw@h/d"},
		{"POSTGRES prefix", "$POSTGRES_URL", "postgres://postgres"},
		{"several allowed references", "${DB_URL}?p=${PGPASSWORD}", "postgres://db?p=pgpw"},
		{"allowed but unset expands to empty, like os.ExpandEnv", "${MINK_UNSET_VARIABLE_FOR_TEST}", ""},
		{"empty value", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := expandConfigEnv(tt.in)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestExpandConfigEnv_DeniedReferences(t *testing.T) {
	t.Setenv(configEnvAllowVar, "")
	t.Setenv("OTHER_SECRET", "leaked-secret-value")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "leaked-aws-value")

	tests := []struct {
		name    string
		in      string
		wantVar string
	}{
		{"brace syntax", "postgres://${OTHER_SECRET}@h/d", "OTHER_SECRET"},
		{"bare syntax", "postgres://$OTHER_SECRET@h/d", "OTHER_SECRET"},
		{"well-known secret", "${AWS_SECRET_ACCESS_KEY}", "AWS_SECRET_ACCESS_KEY"},
		{"HOME", "${HOME}/db", "HOME"},
		{"positional parameter", "postgres://u:p$1@h/d", "1"},
		{"first denied name is reported", "${DATABASE_URL}${AWS_SECRET_ACCESS_KEY}${OTHER_SECRET}", "AWS_SECRET_ACCESS_KEY"},
		{"prefix must match from the start", "${XDATABASE_URL}", "XDATABASE_URL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := expandConfigEnv(tt.in)
			require.Error(t, err)
			assert.Empty(t, got, "nothing of a refused value may be used")
			assert.Contains(t, err.Error(), fmt.Sprintf("%q", tt.wantVar))
			assert.Contains(t, err.Error(), configEnvAllowVar)
			assert.NotContains(t, err.Error(), "leaked-")
		})
	}
}

func TestExpandConfigEnv_OperatorWidening(t *testing.T) {
	t.Setenv("OTHER_SECRET", "s3cr3t")
	t.Setenv("CUSTOM_DSN", "postgres://custom")
	t.Setenv("UNRELATED_VAR", "unrelated")

	t.Setenv(configEnvAllowVar, " custom_ , ,other")
	got, err := expandConfigEnv("${CUSTOM_DSN}")
	require.NoError(t, err)
	assert.Equal(t, "postgres://custom", got)
	got, err = expandConfigEnv("$OTHER_SECRET")
	require.NoError(t, err)
	assert.Equal(t, "s3cr3t", got)
	_, err = expandConfigEnv("${UNRELATED_VAR}")
	require.Error(t, err, "widening is per prefix, not global")
	assert.Contains(t, err.Error(), `"UNRELATED_VAR"`)

	// Empty entries never open the allowlist.
	for _, raw := range []string{",", " , ", ""} {
		t.Setenv(configEnvAllowVar, raw)
		_, err = expandConfigEnv("${UNRELATED_VAR}")
		assert.Error(t, err, "MINK_CONFIG_ENV_ALLOW=%q", raw)
	}
}

func TestConfigEnvAllowed(t *testing.T) {
	tests := []struct {
		name  string
		extra []string
		want  bool
	}{
		{"MINK_X", nil, true},
		{"mink_lowercase", nil, true},
		{"DATABASE_URL", nil, true},
		{"database_url", nil, true},
		{"DB_DSN", nil, true},
		{"PGHOST", nil, true},
		{"pguser", nil, true},
		{"POSTGRES_PASSWORD", nil, true},
		{"POSTGRESQL_URL", nil, true},
		{"DATABASE", nil, false},
		{"DB", nil, false},
		{"MINK", nil, false},
		{"HOME", nil, false},
		{"PATH", nil, false},
		{"1", nil, false},
		{"*", nil, false},
		{"", nil, false},
		{"CUSTOM_DSN", []string{"CUSTOM_"}, true},
		{"custom_dsn", []string{"CUSTOM_"}, true},
		{"HOME", []string{"CUSTOM_"}, false},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s/%v", tt.name, tt.extra), func(t *testing.T) {
			assert.Equal(t, tt.want, configEnvAllowed(tt.name, tt.extra))
		})
	}
}

func TestNewAdapterFactory_RefusesDeniedEnvReference(t *testing.T) {
	t.Setenv(configEnvAllowVar, "")
	t.Setenv("OTHER_SECRET", "postgres://would-have-connected")

	for _, driver := range []string{"postgres", "memory"} {
		t.Run(driver, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.Database.Driver = driver
			cfg.Database.URL = "${OTHER_SECRET}"
			factory, err := NewAdapterFactory(cfg)
			require.Error(t, err)
			assert.Nil(t, factory)
			assert.Contains(t, err.Error(), `"OTHER_SECRET"`)
			assert.NotContains(t, err.Error(), "would-have-connected")
		})
	}

	// The default template value keeps working, with and without the variable set.
	cfg := config.DefaultConfig()
	cfg.Database.Driver = "postgres"
	cfg.Database.URL = "${DATABASE_URL}"
	t.Setenv("DATABASE_URL", "")
	_, err := NewAdapterFactory(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DATABASE_URL")
	t.Setenv("DATABASE_URL", "postgres://u:p@localhost:65535/db")
	factory, err := NewAdapterFactory(cfg)
	require.NoError(t, err)
	assert.Equal(t, "postgres://u:p@localhost:65535/db", factory.GetDatabaseURL())
}

func TestSetupDiagnosticEnv_RefusesDeniedEnvReference(t *testing.T) {
	t.Setenv(configEnvAllowVar, "")
	t.Setenv("OTHER_SECRET", "postgres://would-have-connected")
	env := setupTestEnv(t, "mink-sec-env-diag-*")
	env.createConfig(withDriver("postgres"), withDatabaseURL("${OTHER_SECRET}"))

	diag, reason, err := SetupDiagnosticEnv(context.Background())
	require.Error(t, err)
	assert.Nil(t, diag)
	assert.Equal(t, DiagnosticNotSkipped, reason)
	assert.Contains(t, err.Error(), `"OTHER_SECRET"`)
	assert.NotContains(t, err.Error(), "would-have-connected")
}

// ============================================================================
// gdpr output: stored values cannot forge or hide report lines
// ============================================================================

func TestTerminalSafe(t *testing.T) {
	nl, cr, tab := string(rune(10)), string(rune(13)), string(rune(9))
	esc := string(rune(0x1b))
	r := string(unicode.ReplacementChar)
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "key-1", "key-1"},
		{"non-ascii text kept", "clé-ü-ключ", "clé-ü-ключ"},
		{"newline and carriage return", "k" + nl + "forged" + cr + "line", "k" + r + "forged" + r + "line"},
		{"tab", "a" + tab + "b", "a" + r + "b"},
		{"ANSI escape sequences", esc + "[2K" + esc + "[1Ahidden", r + "[2K" + r + "[1Ahidden"},
		{"NUL and DEL", "a" + string(rune(0)) + "b" + string(rune(0x7f)), "a" + r + "b" + r},
		{"C1 control", "a" + string(rune(0x85)) + "b", "a" + r + "b"},
		{"line and paragraph separators", "a" + string(rune(0x2028)) + "b" + string(rune(0x2029)), "a" + r + "b" + r},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, terminalSafe(tt.in))
		})
	}
}

func TestCountWithKeyIDs_SanitizesIDs(t *testing.T) {
	nl := string(rune(10))
	r := string(unicode.ReplacementChar)
	got := countWithKeyIDs("Keys: ", []string{"k-1", "k-2" + nl + "Keys: 99 [forged]"})
	assert.Equal(t, "Keys: 2 [k-1, k-2"+r+"Keys: 99 [forged]]", got)
	assert.NotContains(t, got, nl)
}

func TestPrintRetentionReport_PoisonedKeyIDCannotForgeOrHideLines(t *testing.T) {
	nl, cr, esc := string(rune(10)), string(rune(13)), string(rune(0x1b))
	r := string(unicode.ReplacementChar)
	report := &mink.RetentionReport{
		Scanned:           3,
		Matched:           1,
		KeysToRevoke:      []string{"k-1" + nl + "Keys revoked:        99 [forged]"},
		SharedKeysSkipped: []string{esc + "[2K" + cr + "k-hidden"},
		Errors:            []error{&mink.RetentionSharedKeyError{KeyID: "k-err" + nl + "Errors:              0", OutOfScope: 1}},
	}
	out := captureStdout(t, func() { printRetentionReport(report) })

	assert.NotContains(t, out, esc, "no escape byte may reach the terminal")
	assert.NotContains(t, out, cr)
	assert.Contains(t, out, "Keys to revoke:      1 [k-1"+r+"Keys revoked:        99 [forged]]")
	assert.Contains(t, out, "Shared keys skipped: 1 ["+r+"[2K"+r+"k-hidden]")
	// The forged text survives as inert characters on its own line; it never
	// becomes a report line of its own.
	linesStartingWith := func(prefix string) int {
		n := 0
		for _, line := range strings.Split(out, nl) {
			if strings.HasPrefix(strings.TrimSpace(line), prefix) {
				n++
			}
		}
		return n
	}
	assert.Equal(t, 1, linesStartingWith("Keys revoked:"), "a key id must not add a report line")
	assert.Equal(t, 1, linesStartingWith("Errors:"), "an error text must not add a report line")
	assert.NotContains(t, out, nl+"Errors:              0")
}

func TestPrintFootprint_PoisonedIDsAreSanitized(t *testing.T) {
	nl := string(rune(10))
	r := string(unicode.ReplacementChar)
	stream := "User-u1" + nl + "forged row"
	fp := &mink.SubjectFootprint{
		SubjectID:         "u1" + nl + "forged title",
		Streams:           []string{stream},
		StreamEventCounts: map[string]int{stream: 2},
		EventCount:        2,
	}
	out := captureStdout(t, func() { printFootprint(fp) })

	assert.NotContains(t, out, nl+"forged")
	assert.Contains(t, out, "u1"+r+"forged title")
	assert.Contains(t, out, "User-u1"+r+"forged row")
}

// ============================================================================
// generate projection: --events entries are PascalCased before validation
// ============================================================================

func TestGenerate_ProjectionEventsArePascalCased(t *testing.T) {
	env := setupTestEnv(t, "mink-sec-gen-proj-*")
	env.createConfig(withModule("github.com/test/project"), withDriver("memory"),
		withProjectionPackage("internal/projections"))

	err := executeCmd(NewGenerateCommand(), []string{"projection", "order_summary",
		"--events", "order-created,item_added,Shipped", "--non-interactive"})
	require.NoError(t, err)

	src, err := os.ReadFile(filepath.Join(env.tmpDir, "internal", "projections", "order_summary.go"))
	require.NoError(t, err)
	for _, want := range []string{
		"type OrderSummaryProjection struct",
		"handleOrderCreated", `"OrderCreated"`,
		"handleItemAdded", `"ItemAdded"`,
		"handleShipped", `"Shipped"`,
	} {
		assert.Contains(t, string(src), want)
	}
	assert.NotContains(t, string(src), "order-created")
	assert.NotContains(t, string(src), "item_added")
}
