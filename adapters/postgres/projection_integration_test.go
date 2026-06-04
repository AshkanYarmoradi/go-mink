package postgres_test

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go-mink.dev"
	"go-mink.dev/adapters/postgres"

	_ "github.com/jackc/pgx/v5/stdlib" // PostgreSQL driver
)

// projRebuildEvent is a dedicated event type for the projection rebuild test so a
// global-position projection in a shared schema would not be affected by it.
type projRebuildEvent struct {
	Seq int `json:"seq"`
}

// countingProjection is a minimal async projection (with Clear, so it is
// Clearable) that counts the events it applies, making a rebuild's clear+replay
// observable.
type countingProjection struct {
	mink.AsyncProjectionBase
	mu    sync.Mutex
	count int
}

func newCountingProjection(name string) *countingProjection {
	return &countingProjection{
		AsyncProjectionBase: mink.NewAsyncProjectionBase(name, "projRebuildEvent"),
	}
}

func (p *countingProjection) Apply(_ context.Context, _ mink.StoredEvent) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.count++
	return nil
}

func (p *countingProjection) ApplyBatch(_ context.Context, events []mink.StoredEvent) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.count += len(events)
	return nil
}

func (p *countingProjection) Clear(_ context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.count = 0
	return nil
}

func (p *countingProjection) Count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.count
}

// memCheckpoints is an in-memory CheckpointStore. The events under test live in
// real PostgreSQL; only the checkpoint bookkeeping is in memory.
type memCheckpoints struct {
	mu sync.Mutex
	m  map[string]uint64
}

func newMemCheckpoints() *memCheckpoints { return &memCheckpoints{m: map[string]uint64{}} }

func (c *memCheckpoints) GetCheckpoint(_ context.Context, name string) (uint64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.m[name], nil
}

func (c *memCheckpoints) SetCheckpoint(_ context.Context, name string, pos uint64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[name] = pos
	return nil
}

func (c *memCheckpoints) DeleteCheckpoint(_ context.Context, name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, name)
	return nil
}

func (c *memCheckpoints) GetAllCheckpoints(_ context.Context) (map[string]uint64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]uint64, len(c.m))
	for k, v := range c.m {
		out[k] = v
	}
	return out, nil
}

// setupProjectionStore creates an event store over a throwaway PostgreSQL schema
// (so a global-position projection only ever sees this test's events) and drops
// the schema on cleanup.
func setupProjectionStore(t *testing.T) (*mink.EventStore, context.Context) {
	t.Helper()
	if testing.Short() {
		t.Skip("Skipping PostgreSQL integration test in short mode")
	}
	connStr := getTestDatabaseURL(t)

	schema := fmt.Sprintf("proj_it_%d", time.Now().UnixNano())
	adapter, err := postgres.NewAdapter(connStr, postgres.WithSchema(schema))
	require.NoError(t, err)
	require.NoError(t, adapter.Initialize(context.Background()))

	t.Cleanup(func() {
		if db, derr := sql.Open("pgx", connStr); derr == nil {
			_, _ = db.Exec(`DROP SCHEMA IF EXISTS "` + schema + `" CASCADE`)
			_ = db.Close()
		}
		_ = adapter.Close()
	})

	return mink.New(adapter), context.Background()
}

// TestPostgresIntegration_ProjectionRebuild_ReplaysFromRealStore verifies that an
// async projection catches up against a real PostgreSQL store and that Rebuild
// clears and replays the full history, resuming the worker from the rebuilt
// checkpoint without double-applying. Run with -race to exercise the worker/
// rebuild serialization (processingMu).
func TestPostgresIntegration_ProjectionRebuild_ReplaysFromRealStore(t *testing.T) {
	store, ctx := setupProjectionStore(t)
	store.RegisterEvents(&projRebuildEvent{})

	const n = 12
	events := make([]interface{}, n)
	for i := range events {
		events[i] = &projRebuildEvent{Seq: i + 1}
	}
	require.NoError(t, store.Append(ctx, "ProjStream-1", events))

	engine := mink.NewProjectionEngine(store, mink.WithCheckpointStore(newMemCheckpoints()))
	proj := newCountingProjection(fmt.Sprintf("Counter-%d", time.Now().UnixNano()))
	require.NoError(t, engine.RegisterAsync(proj))

	require.NoError(t, engine.Start(ctx))
	t.Cleanup(func() { _ = engine.Stop(context.Background()) })

	// Catch up against the real store.
	require.Eventually(t, func() bool { return proj.Count() == n }, 15*time.Second, 50*time.Millisecond,
		"projection should catch up to all events")

	// Rebuild clears and replays from the real store; the worker resumes from the
	// rebuilt checkpoint, so the count settles back at exactly n (no double-apply).
	require.NoError(t, engine.Rebuild(ctx, proj.Name()))
	require.Eventually(t, func() bool { return proj.Count() == n }, 15*time.Second, 50*time.Millisecond,
		"projection should hold exactly n events after rebuild")
}
