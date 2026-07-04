package mink

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go-mink.dev/adapters/memory"
)

const retentionCP = "__mink_retention__"

// newPlainRetentionStore is a memory store with no field encryption. The checkpoint tests
// that only exercise scan/resume/cap mechanics (not actual crypto-shredding) use it so a
// matched Shred is a harmless no-op (no key to revoke) and later appends never hit a revoked
// key — keeping the frontier assertions about positions, not encryption.
func newPlainRetentionStore(t *testing.T) *EventStore {
	t.Helper()
	store := New(memory.NewAdapter())
	store.RegisterEvents(eraseUserCreated{})
	return store
}

func appendUsers(t *testing.T, ctx context.Context, store *EventStore, prefix string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		require.NoError(t, store.Append(ctx, fmt.Sprintf("%s%d", prefix, i),
			[]interface{}{eraseUserCreated{UserID: fmt.Sprintf("u-%s%d", prefix, i)}}))
	}
}

// A checkpointed sweep resumes from the persisted frontier: the second run re-scans only the
// events appended since the first, not the whole store — the core of EF2's fix.
func TestRetention_CheckpointResumesFromHandledPrefix(t *testing.T) {
	ctx := context.Background()
	store := newPlainRetentionStore(t)
	cp := memory.NewCheckpointStore()
	newMgr := func() *RetentionManager {
		// MaxAge 0 ⇒ every event matches immediately, so all scanned events are settled and
		// the frontier advances to HEAD.
		return NewRetentionManager(store,
			[]RetentionPolicy{{Name: "all", Action: ActionShred}},
			WithRetentionCheckpoint(cp, retentionCP))
	}

	appendUsers(t, ctx, store, "A-", 3)
	head1, err := store.GetLastPosition(ctx)
	require.NoError(t, err)

	rep1, err := newMgr().Apply(ctx)
	require.NoError(t, err)
	assert.Equal(t, 3, rep1.Scanned)
	assert.Equal(t, 3, rep1.Matched)
	assert.False(t, rep1.Truncated)
	pos, err := cp.GetCheckpoint(ctx, retentionCP)
	require.NoError(t, err)
	assert.Equal(t, head1, pos, "frontier advances to HEAD when every event is settled")

	// Append more, then sweep again: it must resume from the frontier and scan ONLY the tail.
	appendUsers(t, ctx, store, "B-", 2)
	head2, err := store.GetLastPosition(ctx)
	require.NoError(t, err)

	rep2, err := newMgr().Apply(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, rep2.Scanned, "second run re-scans only the newly-appended tail, not the whole store")
	assert.Equal(t, 2, rep2.Matched)
	pos, err = cp.GetCheckpoint(ctx, retentionCP)
	require.NoError(t, err)
	assert.Equal(t, head2, pos)
}

// The frontier stops at the first pending event (statically matches a policy but is younger
// than MaxAge) while the run still acts on the aged events — and persists the frontier just
// before the pending one, so the young events are re-examined next run.
func TestRetention_CheckpointFreezesAtPendingEvent(t *testing.T) {
	ctx := context.Background()
	store, provider := newEraseTestStore(t, "k")
	cp := memory.NewCheckpointStore()

	// Two "old" events, then a real gap, then two "young" ones. The gap (120ms) is
	// comfortably larger than MaxAge (60ms) and the measurement jitter, so old events are
	// aged and young ones are pending deterministically. (The memory adapter stamps
	// time.Now() at append with no injection hook, so a small real gap is the only way to
	// give events different ages.)
	require.NoError(t, store.Append(ctx, "User-o0", []interface{}{eraseUserCreated{UserID: "o0", Email: "o0@x.y"}}))
	require.NoError(t, store.Append(ctx, "User-o1", []interface{}{eraseUserCreated{UserID: "o1", Email: "o1@x.y"}}))
	oldHead, err := store.GetLastPosition(ctx)
	require.NoError(t, err)
	time.Sleep(120 * time.Millisecond)
	require.NoError(t, store.Append(ctx, "User-y0", []interface{}{eraseUserCreated{UserID: "y0", Email: "y0@x.y"}}))
	require.NoError(t, store.Append(ctx, "User-y1", []interface{}{eraseUserCreated{UserID: "y1", Email: "y1@x.y"}}))

	mgr := NewRetentionManager(store,
		[]RetentionPolicy{{Name: "users", StreamPrefix: "User-", MaxAge: 60 * time.Millisecond, Action: ActionShred}},
		WithRetentionCheckpoint(cp, retentionCP))
	rep, err := mgr.Apply(ctx)
	require.NoError(t, err)

	assert.Equal(t, 2, rep.Matched, "only the two aged events match; the young two are pending")
	assert.Equal(t, 4, rep.Scanned, "the sweep still scans to HEAD, acting past the freeze")
	revoked, _ := provider.IsRevoked("k")
	assert.True(t, revoked, "the aged events were crypto-shredded")

	pos, err := cp.GetCheckpoint(ctx, retentionCP)
	require.NoError(t, err)
	assert.Equal(t, oldHead, pos, "frontier freezes at the last aged event, before the first pending one")
}

// DryRun never advances the checkpoint; a subsequent Apply does.
func TestRetention_CheckpointDryRunPersistsNothing(t *testing.T) {
	ctx := context.Background()
	store := newPlainRetentionStore(t)
	cp := memory.NewCheckpointStore()
	appendUsers(t, ctx, store, "A-", 3)

	mgr := NewRetentionManager(store,
		[]RetentionPolicy{{Name: "all", Action: ActionShred}},
		WithRetentionCheckpoint(cp, retentionCP))

	dry, err := mgr.DryRun(ctx)
	require.NoError(t, err)
	assert.True(t, dry.DryRun)
	assert.Equal(t, 3, dry.Matched)
	pos, err := cp.GetCheckpoint(ctx, retentionCP)
	require.NoError(t, err)
	assert.Equal(t, uint64(0), pos, "dry-run must not persist a checkpoint")

	_, err = mgr.Apply(ctx)
	require.NoError(t, err)
	pos, err = cp.GetCheckpoint(ctx, retentionCP)
	require.NoError(t, err)
	assert.Greater(t, pos, uint64(0), "Apply persists the frontier")
}

// With no checkpoint configured the sweep is byte-for-byte the old behavior: it scans the
// whole store every run and persists nothing.
func TestRetention_UnconfiguredScanIsUnchanged(t *testing.T) {
	ctx := context.Background()
	store := newPlainRetentionStore(t)
	appendUsers(t, ctx, store, "A-", 4)

	mgr := NewRetentionManager(store, []RetentionPolicy{{Name: "all", Action: ActionShred}})

	rep1, err := mgr.Apply(ctx)
	require.NoError(t, err)
	assert.Equal(t, 4, rep1.Scanned)
	assert.False(t, rep1.Truncated)

	// A second run scans the full store again — no hidden resume state.
	rep2, err := mgr.Apply(ctx)
	require.NoError(t, err)
	assert.Equal(t, 4, rep2.Scanned, "without a checkpoint every run is a full scan")
}

// The per-run cap bounds each sweep; the checkpoint carries the remainder across runs until
// the whole store is covered.
func TestRetention_MaxScanBoundsAndResumes(t *testing.T) {
	ctx := context.Background()
	store := newPlainRetentionStore(t)
	cp := memory.NewCheckpointStore()
	appendUsers(t, ctx, store, "A-", 5)
	head, err := store.GetLastPosition(ctx)
	require.NoError(t, err)

	newMgr := func() *RetentionManager {
		return NewRetentionManager(store,
			[]RetentionPolicy{{Name: "all", Action: ActionShred}},
			WithRetentionCheckpoint(cp, retentionCP),
			WithRetentionMaxScan(2))
	}

	rep1, err := newMgr().Apply(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, rep1.Scanned)
	assert.True(t, rep1.Truncated, "a capped run that has not reached HEAD is truncated")

	rep2, err := newMgr().Apply(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, rep2.Scanned)
	assert.True(t, rep2.Truncated)

	rep3, err := newMgr().Apply(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, rep3.Scanned, "the fifth event is the remainder")
	assert.False(t, rep3.Truncated, "the final run reaches HEAD")

	pos, err := cp.GetCheckpoint(ctx, retentionCP)
	require.NoError(t, err)
	assert.Equal(t, head, pos, "across capped runs the checkpoint advances to HEAD")
}

// A per-run cap must not permanently stall when the frontier freezes at the resume
// boundary. If the oldest un-settled event is pending (statically matches a policy but is
// not yet aged), truncating at the cap would persist no progress and re-scan the same
// window every run — starving any aged events behind it (possible when timestamps are not
// monotonic in global position). Instead the sweep falls back to scanning to HEAD until
// the boundary event ages.
func TestRetention_MaxScanDoesNotStallAtPendingFrontier(t *testing.T) {
	ctx := context.Background()
	store := newPlainRetentionStore(t)
	cp := memory.NewCheckpointStore()

	// Freshly appended ⇒ every event is younger than MaxAge ⇒ each is "pending" (statically
	// matches, not yet aged). The first scanned event freezes the frontier at 0.
	appendUsers(t, ctx, store, "A-", 5)

	mgr := NewRetentionManager(store,
		[]RetentionPolicy{{Name: "users", StreamPrefix: "A-", MaxAge: time.Hour, Action: ActionShred}},
		WithRetentionCheckpoint(cp, retentionCP),
		WithRetentionMaxScan(2))

	rep, err := mgr.Apply(ctx)
	require.NoError(t, err)

	assert.False(t, rep.Truncated, "a frozen-frontier run must not truncate — it would never make progress")
	assert.Equal(t, 5, rep.Scanned, "the sweep scans to HEAD instead of stalling at the cap")
	assert.Equal(t, 0, rep.Matched, "every event is still young, so none are acted on yet")

	pos, err := cp.GetCheckpoint(ctx, retentionCP)
	require.NoError(t, err)
	assert.Equal(t, uint64(0), pos, "the frontier stays at 0 while the boundary event is pending")
}

// A cap set without a checkpoint is a loud, non-fatal misconfiguration: the report carries
// ErrRetentionMaxScanNeedsCheckpoint and the sweep runs unbounded rather than silently
// capping and never reaching the tail.
func TestRetention_MaxScanWithoutCheckpointIsLoud(t *testing.T) {
	ctx := context.Background()
	store := newPlainRetentionStore(t)
	appendUsers(t, ctx, store, "A-", 5)

	mgr := NewRetentionManager(store,
		[]RetentionPolicy{{Name: "all", Action: ActionShred}},
		WithRetentionMaxScan(2))
	rep, err := mgr.Apply(ctx)
	require.NoError(t, err)

	assert.Equal(t, 5, rep.Scanned, "without a checkpoint the cap is ignored and the whole store is scanned")
	assert.False(t, rep.Truncated)
	require.True(t, rep.Failed())
	found := false
	for _, e := range rep.Errors {
		if errors.Is(e, ErrRetentionMaxScanNeedsCheckpoint) {
			found = true
		}
	}
	assert.True(t, found, "the misconfiguration must be surfaced in the report")
}
