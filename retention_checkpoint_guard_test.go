package mink

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go-mink.dev/adapters/memory"
	"go-mink.dev/encryption/local"
)

// ---------------------------------------------------------------------------
// Checkpoint × shared-key guard: a Shred match whose key was NOT revoked (refused by the
// guard, failed to revoke, or no encryption config) must never be settled behind the
// persisted frontier — it is re-swept, re-matched and re-reported until the key is revoked.
// ---------------------------------------------------------------------------

func checkpointAt(t *testing.T, ctx context.Context, cp CheckpointStore) uint64 {
	t.Helper()
	pos, err := cp.GetCheckpoint(ctx, retentionCP)
	require.NoError(t, err)
	return pos
}

// A refused key at the resume point persists nothing; re-running with
// WithAllowSharedKeyRevocation resumes from the same point and revokes it.
func TestRetention_CheckpointRefusedKey_AllowSharedKeyRevocationResumes(t *testing.T) {
	ctx := context.Background()
	store, provider := newEraseTestStore(t, "k") // one key for everything
	seedSharedKey(t, ctx, store)                 // User-u1 (pos 1) and Order-o1 (pos 2), both under "k"
	head, err := store.GetLastPosition(ctx)
	require.NoError(t, err)
	cp := memory.NewCheckpointStore()
	policies := []RetentionPolicy{{Name: "users", Category: "User", Action: ActionShred}}

	rep1, err := NewRetentionManager(store, policies, WithRetentionCheckpoint(cp, retentionCP)).Apply(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, rep1.Scanned)
	assert.Equal(t, 1, rep1.Matched)
	assert.Equal(t, []string{"k"}, rep1.SharedKeysSkipped, "Order-o1 shares the key")
	assert.Empty(t, rep1.KeysRevoked)
	assert.Equal(t, 0, rep1.Acted, "a refused match was not acted on")
	assert.Equal(t, 1, rep1.Skipped, "a refused match is a residual")
	assert.True(t, rep1.Failed())
	assert.Equal(t, uint64(0), checkpointAt(t, ctx, cp), "the first run must not persist a frontier past the refused match")

	// Same policies, same checkpoint: the refused match is re-swept and re-reported.
	rep2, err := NewRetentionManager(store, policies, WithRetentionCheckpoint(cp, retentionCP)).Apply(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, rep2.Matched, "the refused match is matched again on the next run")
	assert.Equal(t, []string{"k"}, rep2.SharedKeysSkipped)
	assert.Equal(t, uint64(0), checkpointAt(t, ctx, cp))

	// The operator accepts the blast radius: the resumed sweep revokes the key.
	rep3, err := NewRetentionManager(store, policies,
		WithRetentionCheckpoint(cp, retentionCP), WithAllowSharedKeyRevocation()).Apply(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, rep3.Matched)
	assert.Equal(t, []string{"k"}, rep3.KeysRevoked)
	assert.Equal(t, 1, rep3.Acted)
	assert.Equal(t, 0, rep3.Skipped)
	assert.False(t, rep3.Failed(), "%v", rep3.Errors)
	revoked, _ := provider.IsRevoked("k")
	assert.True(t, revoked)
	assert.Equal(t, head, checkpointAt(t, ctx, cp), "once every match is acted on the frontier reaches HEAD")

	// Steady state: nothing new, nothing re-swept.
	rep4, err := NewRetentionManager(store, policies, WithRetentionCheckpoint(cp, retentionCP)).Apply(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, rep4.Scanned)
	assert.False(t, rep4.Failed(), "%v", rep4.Errors)
}

// A refused key at the resume point persists nothing; adding a Shred policy that covers
// the key's out-of-scope events makes it exclusive, and the resumed sweep revokes it.
func TestRetention_CheckpointRefusedKey_CoveringPolicyResumes(t *testing.T) {
	ctx := context.Background()
	store, provider := newEraseTestStore(t, "k")
	seedSharedKey(t, ctx, store)
	head, err := store.GetLastPosition(ctx)
	require.NoError(t, err)
	cp := memory.NewCheckpointStore()

	rep1, err := NewRetentionManager(store,
		[]RetentionPolicy{{Name: "users", Category: "User", Action: ActionShred}},
		WithRetentionCheckpoint(cp, retentionCP)).Apply(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"k"}, rep1.SharedKeysSkipped)
	assert.Equal(t, 0, rep1.Acted)
	assert.Equal(t, 1, rep1.Skipped)
	assert.Equal(t, uint64(0), checkpointAt(t, ctx, cp), "the refused match holds the frontier at the resume point")

	rep2, err := NewRetentionManager(store,
		[]RetentionPolicy{
			{Name: "users", Category: "User", Action: ActionShred},
			{Name: "orders", Category: "Order", Action: ActionShred}, // now covers Order-o1 too
		},
		WithRetentionCheckpoint(cp, retentionCP)).Apply(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, rep2.Matched, "both events are matched: the first was never settled")
	assert.Equal(t, []string{"k"}, rep2.KeysRevoked)
	assert.Empty(t, rep2.SharedKeysSkipped)
	assert.Equal(t, 2, rep2.Acted)
	assert.Equal(t, 0, rep2.Skipped)
	assert.False(t, rep2.Failed(), "%v", rep2.Errors)
	revoked, _ := provider.IsRevoked("k")
	assert.True(t, revoked)
	assert.Equal(t, head, checkpointAt(t, ctx, cp))
}

// The frontier is clamped to just BEFORE the first unrevoked match — not blindly to the
// resume point — so settled events ahead of it are still persisted as progress.
func TestRetention_CheckpointRefusedKey_ClampsToFirstUnrevokedMatch(t *testing.T) {
	ctx := context.Background()
	store, provider := newEraseTestStore(t, "k")
	// pos 1: no policy matches it (settled), but it shares key "k" with the User match.
	require.NoError(t, store.Append(ctx, "Note-n1", []interface{}{eraseUserCreated{UserID: "n", Email: "n@x.y"}}))
	settledHead, err := store.GetLastPosition(ctx)
	require.NoError(t, err)
	require.NoError(t, store.Append(ctx, "User-u1", []interface{}{eraseUserCreated{UserID: "u1", Email: "a@b.c"}})) // pos 2
	head, err := store.GetLastPosition(ctx)
	require.NoError(t, err)
	cp := memory.NewCheckpointStore()
	policies := []RetentionPolicy{{Name: "users", Category: "User", Action: ActionShred}}

	rep1, err := NewRetentionManager(store, policies, WithRetentionCheckpoint(cp, retentionCP)).Apply(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, rep1.Scanned)
	assert.Equal(t, []string{"k"}, rep1.SharedKeysSkipped, "Note-n1 is out of scope under the same key")
	assert.Equal(t, settledHead, checkpointAt(t, ctx, cp), "the settled non-match before the refused match is persisted; the refused match is not")

	rep2, err := NewRetentionManager(store, policies, WithRetentionCheckpoint(cp, retentionCP)).Apply(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, rep2.Scanned, "only the unsettled match is re-scanned")
	assert.Equal(t, 1, rep2.Matched)
	assert.Equal(t, []string{"k"}, rep2.SharedKeysSkipped)
	assert.Equal(t, settledHead, checkpointAt(t, ctx, cp), "no progress to persist while the key stays refused")

	rep3, err := NewRetentionManager(store, policies,
		WithRetentionCheckpoint(cp, retentionCP), WithAllowSharedKeyRevocation()).Apply(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"k"}, rep3.KeysRevoked)
	assert.Equal(t, 1, rep3.Acted)
	revoked, _ := provider.IsRevoked("k")
	assert.True(t, revoked)
	assert.Equal(t, head, checkpointAt(t, ctx, cp))
}

// A key the guard approved but that FAILED to revoke is just as unsettled as a refused
// one: its match is Skipped and the frontier is held before it.
func TestRetention_CheckpointRevokeFailure_HoldsFrontier(t *testing.T) {
	ctx := context.Background()
	inner, err := local.New(local.WithKey("k", make([]byte, 32)))
	require.NoError(t, err)
	cfg := NewFieldEncryptionConfig(
		WithEncryptionProvider(revokeFailsProvider{inner}),
		WithDefaultKeyID("k"),
		WithEncryptedFields("eraseUserCreated", "email"),
	)
	store := New(memory.NewAdapter(), WithFieldEncryption(cfg))
	store.RegisterEvents(eraseUserCreated{})
	require.NoError(t, store.Append(ctx, "User-u1", []interface{}{eraseUserCreated{UserID: "u1", Email: "a@b.c"}}))
	cp := memory.NewCheckpointStore()

	rep, err := NewRetentionManager(store,
		[]RetentionPolicy{{Name: "users", Category: "User", Action: ActionShred}},
		WithRetentionCheckpoint(cp, retentionCP)).Apply(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"k"}, rep.KeysToRevoke, "the guard approved the exclusive key")
	assert.Empty(t, rep.KeysRevoked, "but the provider could not revoke it")
	assert.Equal(t, 0, rep.Acted)
	assert.Equal(t, 1, rep.Skipped)
	assert.True(t, rep.Failed())
	assert.Equal(t, uint64(0), checkpointAt(t, ctx, cp), "a failed revoke must not settle its match")
}

// With no encryption config there is no provider to revoke through: the match is Skipped
// (plus ErrErasureNotConfigured) and nothing is settled.
func TestRetention_CheckpointEncryptionNotConfigured_HoldsFrontier(t *testing.T) {
	ctx := context.Background()
	store := New(memory.NewAdapter())
	store.RegisterEvents(eraseUserCreated{})
	appendRawEnvelope(t, ctx, store.Adapter(), "User-u1", fullEnvelope("k"))
	cp := memory.NewCheckpointStore()

	rep, err := NewRetentionManager(store,
		[]RetentionPolicy{{Name: "users", StreamPrefix: "User-", Action: ActionShred}},
		WithRetentionCheckpoint(cp, retentionCP)).Apply(ctx)
	require.NoError(t, err)
	assert.True(t, hasErr(rep.Errors, ErrErasureNotConfigured))
	assert.Equal(t, 0, rep.Acted)
	assert.Equal(t, 1, rep.Skipped)
	assert.Equal(t, uint64(0), checkpointAt(t, ctx, cp))
}

// DryRun with a refused key: the preview lists the key under SharedKeysSkipped, acts on
// nothing, counts nothing as Acted/Skipped and persists nothing — as before.
func TestRetention_CheckpointRefusedKey_DryRunAccounting(t *testing.T) {
	ctx := context.Background()
	store, _ := newEraseTestStore(t, "k")
	seedSharedKey(t, ctx, store)
	cp := memory.NewCheckpointStore()

	dry, err := NewRetentionManager(store,
		[]RetentionPolicy{{Name: "users", Category: "User", Action: ActionShred}},
		WithRetentionCheckpoint(cp, retentionCP)).DryRun(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, dry.Matched)
	assert.Equal(t, []string{"k"}, dry.SharedKeysSkipped)
	assert.Equal(t, 0, dry.Acted)
	assert.Equal(t, 0, dry.Skipped)
	assert.Equal(t, uint64(0), checkpointAt(t, ctx, cp))
}

// The per-run cap never truncates while the resume point is held by an undecided Shred
// match: the sweep scans to HEAD (as for a pending event) so a refused key can never
// starve the tail behind the cap.
func TestRetention_MaxScanDoesNotTruncateAtUndecidedShredMatch(t *testing.T) {
	ctx := context.Background()
	store, provider := newRetentionKeyedStore(t, "a", "b") // one key per stream
	appendUnderTenant(t, ctx, store, "User-a", "a")        // pos 1: a Shred match at the resume point
	appendUnderTenant(t, ctx, store, "User-b", "b")        // pos 2
	head, err := store.GetLastPosition(ctx)
	require.NoError(t, err)
	cp := memory.NewCheckpointStore()

	rep, err := NewRetentionManager(store,
		[]RetentionPolicy{{Name: "users", StreamPrefix: "User-", Action: ActionShred}},
		WithRetentionCheckpoint(cp, retentionCP),
		WithRetentionMaxScan(1)).Apply(ctx)
	require.NoError(t, err)
	assert.False(t, rep.Truncated, "a run whose first unsettled event is an undecided Shred match must not truncate")
	assert.Equal(t, 2, rep.Scanned, "the sweep scans to HEAD instead")
	assert.Equal(t, []string{"k-a", "k-b"}, rep.KeysRevoked)
	assert.Equal(t, 2, rep.Acted)
	for _, k := range []string{"k-a", "k-b"} {
		revoked, _ := provider.IsRevoked(k)
		assert.True(t, revoked, k)
	}
	assert.Equal(t, head, checkpointAt(t, ctx, cp), "every match was acted on, so the frontier reaches HEAD")
}

// The cap still truncates once a settled prefix guarantees progress even if every Shred
// key collected so far were refused — and the resumed run picks up the remainder.
func TestRetention_MaxScanTruncatesAfterSettledPrefix(t *testing.T) {
	ctx := context.Background()
	store, _ := newRetentionKeyedStore(t, "n", "a", "b")
	appendUnderTenant(t, ctx, store, "Note-n1", "n") // pos 1: matches no policy (settled)
	settledHead, err := store.GetLastPosition(ctx)
	require.NoError(t, err)
	appendUnderTenant(t, ctx, store, "User-a", "a") // pos 2: Shred match, exclusive key
	afterA, err := store.GetLastPosition(ctx)
	require.NoError(t, err)
	appendUnderTenant(t, ctx, store, "User-b", "b") // pos 3
	head, err := store.GetLastPosition(ctx)
	require.NoError(t, err)
	cp := memory.NewCheckpointStore()
	newMgr := func() *RetentionManager {
		return NewRetentionManager(store,
			[]RetentionPolicy{{Name: "users", StreamPrefix: "User-", Action: ActionShred}},
			WithRetentionCheckpoint(cp, retentionCP),
			WithRetentionMaxScan(2))
	}

	rep1, err := newMgr().Apply(ctx)
	require.NoError(t, err)
	assert.True(t, rep1.Truncated, "the settled Note-n1 guarantees progress past the resume point")
	assert.Equal(t, 2, rep1.Scanned)
	assert.Equal(t, []string{"k-a"}, rep1.KeysRevoked)
	assert.Equal(t, 1, rep1.Acted)
	assert.Greater(t, afterA, settledHead)
	assert.Equal(t, afterA, checkpointAt(t, ctx, cp), "the revoked match is settled too")

	rep2, err := newMgr().Apply(ctx)
	require.NoError(t, err)
	assert.False(t, rep2.Truncated)
	assert.Equal(t, 1, rep2.Scanned, "the remainder is resumed")
	assert.Equal(t, []string{"k-b"}, rep2.KeysRevoked)
	assert.Equal(t, head, checkpointAt(t, ctx, cp))
}
