package containers

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Identifier quoting / validation (no database required)
// =============================================================================

func TestQuoteIdentifier_EscapesEmbeddedQuotes(t *testing.T) {
	tests := []struct {
		name, input, want string
	}{
		{"single embedded quote", `a"b`, `"a""b"`},
		{"quote that would terminate the identifier", `x"; DROP SCHEMA public CASCADE; --`, `"x""; DROP SCHEMA public CASCADE; --"`},
		{"leading quote", `"x`, `"""x"`},
		{"trailing quote", `x"`, `"x"""`},
		{"only quotes", `""`, `""""""`},
		{"no quotes unchanged", "plain_name", `"plain_name"`},
		{"empty", "", `""`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := quoteIdentifier(tt.input)
			assert.Equal(t, tt.want, got)

			// The quoted form must start and end with a quote and every interior
			// quote must be doubled, so no lone quote can close the identifier.
			require.GreaterOrEqual(t, len(got), 2)
			assert.Equal(t, byte('"'), got[0])
			assert.Equal(t, byte('"'), got[len(got)-1])
			inner := got[1 : len(got)-1]
			assert.NotContains(t, strings.ReplaceAll(inner, `""`, ""), `"`)
		})
	}
}

func TestValidateSchemaName(t *testing.T) {
	valid := []string{
		"a",
		"_",
		"test_1700000000000000000",
		"fullstack_1700000000000000000",
		"MixedCase_09",
		"_leading_underscore",
	}
	for _, name := range valid {
		t.Run("valid/"+name, func(t *testing.T) {
			assert.NoError(t, validateSchemaName(name))
		})
	}

	invalid := []struct {
		name   string
		schema string
	}{
		{"empty", ""},
		{"64 bytes would be truncated onto a 63-byte schema", strings.Repeat("a", MaxSchemaNameBytes+1)},
		{"far too long", strings.Repeat("a", 200)},
		{"leading digit", "1abc"},
		{"embedded double quote", `abc"def`},
		{"quote plus statement injection", `abc"; DROP SCHEMA public CASCADE; --`},
		{"trailing quote", `abc"`},
		{"semicolon", "abc;def"},
		{"space", "ab c"},
		{"hyphen", "ab-c"},
		{"dot qualified", "public.events"},
		{"non ascii", "schéma"},
		{"newline", "abc\n"},
		{"null byte", "abc\x00"},
	}
	for _, tt := range invalid {
		t.Run("invalid/"+tt.name, func(t *testing.T) {
			err := validateSchemaName(tt.schema)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrInvalidSchemaName)
		})
	}
}

func TestValidateSchemaName_AcceptsCreateSchemaOutput(t *testing.T) {
	// CreateSchema produces "<prefix>_<unix-nanos>"; DropSchema must accept every
	// such name or per-test cleanup would always fail — including the longest
	// prefix CreateSchema itself accepts.
	for _, prefix := range []string{"test", "fullstack", "e2e", "_x", "Custom_1", strings.Repeat("p", MaxSchemaPrefixBytes)} {
		name := fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
		assert.NoError(t, validateSchemaName(name), name)
		assert.LessOrEqual(t, len(name), MaxSchemaNameBytes, name)
	}
}

// TestValidateSchemaName_LengthBound pins PostgreSQL's 63-byte identifier limit:
// the server silently truncates a longer identifier instead of rejecting it, so
// without the bound DROP SCHEMA on a 64-byte caller-supplied name would drop the
// existing 63-byte schema that shares its head.
func TestValidateSchemaName_LengthBound(t *testing.T) {
	assert.Equal(t, 63, MaxSchemaNameBytes, "PostgreSQL NAMEDATALEN-1")

	at := strings.Repeat("s", MaxSchemaNameBytes)
	over := at + "x"
	require.Len(t, at, 63)
	require.Len(t, over, 64)

	assert.NoError(t, validateSchemaName(at), "a 63-byte name is accepted")
	err := validateSchemaName(over)
	require.Error(t, err, "a 64-byte name is rejected")
	assert.ErrorIs(t, err, ErrInvalidSchemaName)

	// The byte that pushes the name over the limit is exactly the one the server
	// would discard: untrapped, "over" addresses "at".
	assert.Equal(t, at, over[:MaxSchemaNameBytes])

	// DropSchema refuses it before any database access (a nil *sql.DB would panic).
	c := &PostgresContainer{}
	assert.ErrorIs(t, c.DropSchema(context.Background(), nil, over), ErrInvalidSchemaName)
}

// TestValidateSchemaPrefix_LengthBound pins the matching bound on the prefix:
// CreateSchema must never mint a name the server would truncate (collapsing
// successive schemas onto one) and DropSchema would then refuse.
func TestValidateSchemaPrefix_LengthBound(t *testing.T) {
	assert.Equal(t, 43, MaxSchemaPrefixBytes, `63 - len("_<19-digit UnixNano>")`)

	at := strings.Repeat("p", MaxSchemaPrefixBytes)
	assert.NoError(t, validateSchemaPrefix(at), "a 43-byte prefix is accepted")
	err := validateSchemaPrefix(at + "x")
	require.Error(t, err, "a 44-byte prefix is rejected")
	assert.ErrorIs(t, err, ErrInvalidSchemaPrefix)

	// The longest accepted prefix still yields a name DropSchema accepts whole.
	name := fmt.Sprintf("%s_%d", at, time.Now().UnixNano())
	assert.Len(t, name, MaxSchemaNameBytes)
	assert.NoError(t, validateSchemaName(name))

	// CreateSchema refuses the oversize prefix before any database access.
	c := &PostgresContainer{}
	schema, err := c.CreateSchema(context.Background(), nil, at+"x")
	assert.ErrorIs(t, err, ErrInvalidSchemaPrefix)
	assert.Empty(t, schema)
}

// =============================================================================
// DropSchema / CreateSchema reject unsafe identifiers before touching the DB
// =============================================================================

func TestPostgresContainer_DropSchema_RejectsInvalidName(t *testing.T) {
	c := &PostgresContainer{}
	cases := []string{
		"",
		`t"; DROP SCHEMA public CASCADE; --`,
		"1abc",
		"a b",
		"a.b",
		"a-b",
		`a"b`,
	}
	for _, schema := range cases {
		t.Run(fmt.Sprintf("%q", schema), func(t *testing.T) {
			// A nil *sql.DB would panic if DropSchema reached ExecContext, so this
			// also proves validation runs before any database access.
			err := c.DropSchema(context.Background(), nil, schema)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrInvalidSchemaName)
		})
	}
}

func TestPostgresContainer_CreateSchema_RejectsInvalidPrefix(t *testing.T) {
	c := &PostgresContainer{}
	cases := []string{
		"",
		`t"`,
		"1abc",
		"a-b",
		"a;b",
		"a b",
	}
	for _, prefix := range cases {
		t.Run(fmt.Sprintf("%q", prefix), func(t *testing.T) {
			// nil *sql.DB: validation must reject the prefix before ExecContext.
			schema, err := c.CreateSchema(context.Background(), nil, prefix)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrInvalidSchemaPrefix)
			assert.Empty(t, schema)
		})
	}
}

func TestErrInvalidSchemaName_Message(t *testing.T) {
	assert.True(t, strings.HasPrefix(ErrInvalidSchemaName.Error(), "containers: "))
	assert.NotErrorIs(t, ErrInvalidSchemaName, ErrInvalidSchemaPrefix)
	// Both messages state their length bound so a rejected caller knows why.
	assert.Contains(t, ErrInvalidSchemaName.Error(), "at most 63 bytes")
	assert.Contains(t, ErrInvalidSchemaPrefix.Error(), "at most 43 bytes")
}
