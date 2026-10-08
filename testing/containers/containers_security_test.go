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
	// such name or per-test cleanup would always fail.
	for _, prefix := range []string{"test", "fullstack", "e2e", "_x", "Custom_1"} {
		name := fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
		assert.NoError(t, validateSchemaName(name), name)
	}
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
}
