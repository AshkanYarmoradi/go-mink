package mink_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	mink "go-mink.dev"
	"go-mink.dev/adapters/postgres"
	"go-mink.dev/testing/containers"
)

// e2ePG is the shared end-to-end fixture: a real EventStore over the PostgreSQL adapter on a
// per-test isolated schema, plus the raw *sql.DB and schema name so a suite can make direct SQL
// assertions (ciphertext-at-rest, append-only row counts, outbox status). It self-skips under
// -short or when TEST_DATABASE_URL is unset.
//
// The adapter doubles as the CheckpointStore / SubscriptionAdapter / SnapshotAdapter (the
// postgres adapter implements all three), so suites pass it straight to
// mink.WithCheckpointStore(adapter) and the GDPR sibling erasers.
type e2ePG struct {
	Store   *mink.EventStore
	Adapter *postgres.PostgresAdapter
	DB      *sql.DB
	Schema  string
	Ctx     context.Context
}

// newE2EPGBase builds the isolated-schema adapter + db + ctx WITHOUT a store, so callers that
// need custom store options (e.g. field encryption, subject tagging) can construct their own
// EventStore over p.Adapter and assign p.Store.
func newE2EPGBase(t *testing.T) *e2ePG {
	t.Helper()

	if testing.Short() {
		t.Skip("Skipping PostgreSQL E2E test in short mode")
	}
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL E2E test")
	}

	ctx := context.Background()
	container := containers.StartPostgres(t) // skips the test if PG is unreachable
	db := container.MustDB(ctx)

	schema, err := container.CreateSchema(ctx, db, "e2e")
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = container.DropSchema(context.Background(), db, schema)
		_ = db.Close()
	})

	adapter, err := postgres.NewAdapter(container.ConnectionString(), postgres.WithSchema(schema))
	require.NoError(t, err)
	t.Cleanup(func() { _ = adapter.Close() })
	require.NoError(t, adapter.Initialize(ctx)) // creates streams/events/snapshots/checkpoints in `schema`

	return &e2ePG{Adapter: adapter, DB: db, Schema: schema, Ctx: ctx}
}

// newE2EPG builds the fixture with a default JSON-serialized store and registers the given
// event types.
func newE2EPG(t *testing.T, register ...interface{}) *e2ePG {
	t.Helper()
	p := newE2EPGBase(t)
	p.Store = mink.New(p.Adapter, mink.WithSerializer(mink.NewJSONSerializer()))
	if len(register) > 0 {
		p.Store.RegisterEvents(register...)
	}
	return p
}

// table returns the schema-qualified, quoted table name for direct SQL.
func (p *e2ePG) table(name string) string {
	return fmt.Sprintf("%q.%q", p.Schema, name)
}

// countRows returns the row count of a schema-qualified table.
func (p *e2ePG) countRows(t *testing.T, table string) int {
	t.Helper()
	var n int
	require.NoError(t, p.DB.QueryRowContext(p.Ctx, "SELECT count(*) FROM "+p.table(table)).Scan(&n))
	return n
}

// rawEventData returns the raw (as-stored) data bytes of the first event of a stream, for
// ciphertext-at-rest assertions (bypasses the store's decrypt path).
func (p *e2ePG) rawEventData(t *testing.T, streamID string) []byte {
	t.Helper()
	var data []byte
	err := p.DB.QueryRowContext(p.Ctx,
		"SELECT data FROM "+p.table("events")+" WHERE stream_id = $1 ORDER BY version ASC LIMIT 1",
		streamID,
	).Scan(&data)
	require.NoError(t, err)
	return data
}

// contains reports whether s is in xs.
func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// eventually polls fn until it returns true or the timeout elapses, failing the test otherwise.
// Used instead of time.Sleep for asserting real async delivery.
func eventually(t *testing.T, timeout time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	require.True(t, fn(), "condition not met within %s", timeout)
}
