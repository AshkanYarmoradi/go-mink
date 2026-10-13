package containers

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Recording driver: a *sql.DB that records every statement instead of running
// it, so the ownership rules can be exercised without a PostgreSQL instance.
// =============================================================================

// recordingDriver captures the SQL text of every ExecContext call made through
// a *sql.DB opened from it. When execErr is set every statement fails with it.
type recordingDriver struct {
	mu      sync.Mutex
	execs   []string
	execErr error
}

func (d *recordingDriver) Open(string) (driver.Conn, error) { return &recordingConn{d: d}, nil }

func (d *recordingDriver) statements() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.execs...)
}

type recordingConnector struct{ d *recordingDriver }

func (c *recordingConnector) Connect(context.Context) (driver.Conn, error) {
	return &recordingConn{d: c.d}, nil
}

func (c *recordingConnector) Driver() driver.Driver { return c.d }

type recordingConn struct{ d *recordingDriver }

func (c *recordingConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("recordingConn: Prepare not supported")
}

func (c *recordingConn) Close() error { return nil }

func (c *recordingConn) Begin() (driver.Tx, error) {
	return nil, errors.New("recordingConn: Begin not supported")
}

// ExecContext satisfies driver.ExecerContext so database/sql never falls back
// to Prepare.
func (c *recordingConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	c.d.mu.Lock()
	defer c.d.mu.Unlock()
	if c.d.execErr != nil {
		return nil, c.d.execErr
	}
	c.d.execs = append(c.d.execs, query)
	return driver.RowsAffected(0), nil
}

var (
	_ driver.Driver        = (*recordingDriver)(nil)
	_ driver.Connector     = (*recordingConnector)(nil)
	_ driver.Conn          = (*recordingConn)(nil)
	_ driver.ExecerContext = (*recordingConn)(nil)
)

// newRecordingDB opens a *sql.DB backed by a fresh recordingDriver. No driver
// registration is needed (sql.OpenDB takes the connector directly), so every
// test gets its own isolated statement log.
func newRecordingDB(t *testing.T) (*sql.DB, *recordingDriver) {
	t.Helper()
	rec := &recordingDriver{}
	db := sql.OpenDB(&recordingConnector{d: rec})
	t.Cleanup(func() { _ = db.Close() })
	return db, rec
}

// dropStatementFor is the exact DDL DropSchema / DropSchemaUnsafe must emit.
func dropStatementFor(schema string) string {
	return fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", quoteIdentifier(schema))
}

// =============================================================================
// Ownership: DropSchema only drops what CreateSchema on the same instance made
// =============================================================================

func TestPostgresContainer_CreateSchema_ThenDropSchema_Succeeds(t *testing.T) {
	db, rec := newRecordingDB(t)
	c := &PostgresContainer{}
	ctx := context.Background()

	schema, err := c.CreateSchema(ctx, db, "owned")
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(schema, "owned_"))
	assert.True(t, c.ownsSchema(schema), "CreateSchema records the name it returns")

	require.NoError(t, c.DropSchema(ctx, db, schema))

	// Dropping an owned schema is idempotent: ownership survives the drop so a
	// cleanup that runs twice (or retries) stays a harmless IF EXISTS no-op.
	require.NoError(t, c.DropSchema(ctx, db, schema))
	assert.True(t, c.ownsSchema(schema), "a successful drop does not forget the name")

	assert.Equal(t, []string{
		"CREATE SCHEMA IF NOT EXISTS " + quoteIdentifier(schema),
		dropStatementFor(schema),
		dropStatementFor(schema),
	}, rec.statements())
}

func TestPostgresContainer_DropSchema_RefusesUnownedName(t *testing.T) {
	// Every name here is a perfectly valid identifier; validation alone would
	// let all of them through. A nil *sql.DB would panic if DropSchema reached
	// ExecContext, so each case also proves the refusal happens before any
	// database access.
	c := &PostgresContainer{}
	cases := []struct {
		name   string
		schema string
	}{
		{"public", "public"},
		{"information_schema", "information_schema"},
		{"pg_catalog", "pg_catalog"},
		{"foreign CreateSchema-shaped name", "test_1700000000000000000"},
		{"foreign fullstack name", "fullstack_1700000000000000000"},
		{"foreign e2e name", "e2e_1700000000000000000"},
		{"longest valid name", strings.Repeat("s", MaxSchemaNameBytes)},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			require.NoError(t, validateSchemaName(tt.schema), "precondition: the identifier itself is valid")
			err := c.DropSchema(context.Background(), nil, tt.schema)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrSchemaNotOwned)
			assert.NotErrorIs(t, err, ErrInvalidSchemaName)
		})
	}
}

func TestPostgresContainer_DropSchema_ValidationPrecedesOwnership(t *testing.T) {
	// Garbage input is reported as an invalid identifier, never as "not owned",
	// so a caller sees the real reason — and nothing reaches the database (nil DB).
	c := &PostgresContainer{}
	for _, schema := range []string{"", `public"; DROP SCHEMA public CASCADE; --`, "1abc", "a b", strings.Repeat("a", MaxSchemaNameBytes+1)} {
		t.Run(fmt.Sprintf("%q", schema), func(t *testing.T) {
			err := c.DropSchema(context.Background(), nil, schema)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrInvalidSchemaName)
			assert.NotErrorIs(t, err, ErrSchemaNotOwned)
		})
	}
}

func TestPostgresContainer_DropSchema_RefusesSchemaOfAnotherInstance(t *testing.T) {
	db, rec := newRecordingDB(t)
	ctx := context.Background()
	owner := &PostgresContainer{}
	other := &PostgresContainer{}

	schema, err := owner.CreateSchema(ctx, db, "shared")
	require.NoError(t, err)
	require.True(t, owner.ownsSchema(schema))
	assert.False(t, other.ownsSchema(schema), "ownership is per instance, not per process")

	// Another instance cannot drop it: refused before any SQL (nil DB would panic).
	err = other.DropSchema(ctx, nil, schema)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSchemaNotOwned)

	// Only the CREATE has reached the driver so far.
	require.Equal(t, []string{"CREATE SCHEMA IF NOT EXISTS " + quoteIdentifier(schema)}, rec.statements())

	// The creating instance still can.
	require.NoError(t, owner.DropSchema(ctx, db, schema))
	assert.Equal(t, dropStatementFor(schema), rec.statements()[1])
}

func TestPostgresContainer_CreateSchema_FailureDoesNotRecordOwnership(t *testing.T) {
	db, rec := newRecordingDB(t)
	rec.execErr = errors.New("boom")
	c := &PostgresContainer{}

	schema, err := c.CreateSchema(context.Background(), db, "failing")
	require.Error(t, err)
	assert.ErrorContains(t, err, "containers: failed to create schema")
	assert.Empty(t, schema)

	c.mu.Lock()
	defer c.mu.Unlock()
	assert.Empty(t, c.owned, "a name is recorded only after CREATE SCHEMA succeeds")
}

// =============================================================================
// DropSchemaUnsafe: explicit opt-out of ownership, same identifier validation
// =============================================================================

func TestPostgresContainer_DropSchemaUnsafe_RejectsInvalidName(t *testing.T) {
	c := &PostgresContainer{}
	cases := []string{
		"",
		`t"; DROP SCHEMA public CASCADE; --`,
		"1abc",
		"a b",
		"a.b",
		"a-b",
		`a"b`,
		strings.Repeat("a", MaxSchemaNameBytes+1),
	}
	for _, schema := range cases {
		t.Run(fmt.Sprintf("%q", schema), func(t *testing.T) {
			// nil *sql.DB: the identifier check must still run before ExecContext.
			err := c.DropSchemaUnsafe(context.Background(), nil, schema)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrInvalidSchemaName)
		})
	}
}

func TestPostgresContainer_DropSchemaUnsafe_DropsSchemaItDidNotCreate(t *testing.T) {
	db, rec := newRecordingDB(t)
	ctx := context.Background()
	owner := &PostgresContainer{}
	other := &PostgresContainer{}

	schema, err := owner.CreateSchema(ctx, db, "orphan")
	require.NoError(t, err)

	// DropSchema on the non-owner refuses; DropSchemaUnsafe is the opt-in.
	require.ErrorIs(t, other.DropSchema(ctx, db, schema), ErrSchemaNotOwned)
	require.NoError(t, other.DropSchemaUnsafe(ctx, db, schema))
	assert.False(t, other.ownsSchema(schema), "DropSchemaUnsafe does not confer ownership")

	// Likewise for a name no instance in this process created.
	foreign := "leftover_1700000000000000000"
	require.ErrorIs(t, other.DropSchema(ctx, db, foreign), ErrSchemaNotOwned)
	require.NoError(t, other.DropSchemaUnsafe(ctx, db, foreign))

	assert.Equal(t, []string{
		"CREATE SCHEMA IF NOT EXISTS " + quoteIdentifier(schema),
		dropStatementFor(schema),
		dropStatementFor(foreign),
	}, rec.statements(), "the refused DropSchema calls emitted no SQL")
}

func TestPostgresContainer_SchemaOwnership_ConcurrentAccess(t *testing.T) {
	// One handle shared by parallel subtests must not race: every goroutine
	// creates its own schema and drops it, and none may drop a sibling's.
	db, rec := newRecordingDB(t)
	ctx := context.Background()
	c := &PostgresContainer{}

	const n = 16
	schemas := make([]string, n)
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			schema, err := c.CreateSchema(ctx, db, fmt.Sprintf("par%d", i))
			if err != nil {
				errs[i] = err
				return
			}
			schemas[i] = schema
			errs[i] = c.DropSchema(ctx, db, schema)
		}(i)
	}
	wg.Wait()

	for i := 0; i < n; i++ {
		require.NoError(t, errs[i], "goroutine %d", i)
		assert.True(t, c.ownsSchema(schemas[i]))
	}
	assert.Len(t, rec.statements(), 2*n)

	// A different handle still owns none of them.
	other := &PostgresContainer{}
	for _, schema := range schemas {
		assert.ErrorIs(t, other.DropSchema(ctx, nil, schema), ErrSchemaNotOwned)
	}
}

func TestErrSchemaNotOwned_Message(t *testing.T) {
	assert.True(t, strings.HasPrefix(ErrSchemaNotOwned.Error(), "containers: "))
	assert.Contains(t, ErrSchemaNotOwned.Error(), "DropSchemaUnsafe", "the message names the opt-out")
	assert.NotErrorIs(t, ErrSchemaNotOwned, ErrInvalidSchemaName)
	assert.NotErrorIs(t, ErrSchemaNotOwned, ErrInvalidSchemaPrefix)
}
