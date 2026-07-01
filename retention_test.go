package mink

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func seedRetention(t *testing.T, ctx context.Context, store *EventStore) {
	t.Helper()
	require.NoError(t, store.Append(ctx, "User-u1",
		[]interface{}{eraseUserCreated{UserID: "u1", Email: "a@b.c"}}))
	require.NoError(t, store.Append(ctx, "Order-o1",
		[]interface{}{eraseUserCreated{UserID: "u1", Email: "a@b.c"}}))
}

func TestRetention_ShredByPrefix(t *testing.T) {
	ctx := context.Background()
	store, provider := newEraseTestStore(t, "k")
	seedRetention(t, ctx, store)

	mgr := NewRetentionManager(store, []RetentionPolicy{
		{Name: "users", StreamPrefix: "User-", Action: ActionShred},
	})
	report, err := mgr.Apply(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"k"}, report.KeysRevoked)
	assert.Equal(t, 1, report.Matched) // only User-u1
	assert.Empty(t, report.Errors)

	revoked, _ := provider.IsRevoked("k")
	assert.True(t, revoked)

	// Append-only: the events are still present (only the key was revoked).
	raw, err := store.LoadRaw(ctx, "User-u1", 0)
	require.NoError(t, err)
	assert.Len(t, raw, 1)
}

func TestRetention_AgeBased(t *testing.T) {
	ctx := context.Background()
	store, provider := newEraseTestStore(t, "k")
	seedRetention(t, ctx, store)

	// MaxAge 1h; with the clock at default now, nothing is old enough.
	young := NewRetentionManager(store, []RetentionPolicy{
		{Name: "old-users", StreamPrefix: "User-", MaxAge: time.Hour, Action: ActionShred},
	})
	report, err := young.Apply(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, report.Matched)
	revoked, _ := provider.IsRevoked("k")
	assert.False(t, revoked)

	// Advance the clock 2h: now the events exceed MaxAge.
	old := NewRetentionManager(store, []RetentionPolicy{
		{Name: "old-users", StreamPrefix: "User-", MaxAge: time.Hour, Action: ActionShred},
	}, WithRetentionClock(func() time.Time { return time.Now().Add(2 * time.Hour) }))
	report, err = old.Apply(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"k"}, report.KeysRevoked)
}

func TestRetention_DryRunChangesNothing(t *testing.T) {
	ctx := context.Background()
	store, provider := newEraseTestStore(t, "k")
	seedRetention(t, ctx, store)

	mgr := NewRetentionManager(store, []RetentionPolicy{
		{Name: "users", StreamPrefix: "User-", Action: ActionShred},
	})
	report, err := mgr.DryRun(ctx)
	require.NoError(t, err)
	assert.True(t, report.DryRun)
	assert.Equal(t, 1, report.Matched)
	assert.Empty(t, report.KeysRevoked)

	revoked, _ := provider.IsRevoked("k")
	assert.False(t, revoked, "dry-run must not revoke")
}

func TestRetention_Composition(t *testing.T) {
	ctx := context.Background()
	store, _ := newEraseTestStore(t, "k")
	seedRetention(t, ctx, store)

	mgr := NewRetentionManager(store, []RetentionPolicy{
		{Name: "users", StreamPrefix: "User-", Action: ActionShred},
		{Name: "orders", StreamPrefix: "Order-", Action: ActionShred},
	})
	report, err := mgr.Apply(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, report.Matched) // one per policy
	assert.Equal(t, []string{"k"}, report.KeysRevoked)
}

func TestRetention_RedactWithoutHookIsLoud(t *testing.T) {
	ctx := context.Background()
	store, _ := newEraseTestStore(t, "k")
	seedRetention(t, ctx, store)

	policy := RetentionPolicy{Name: "mask", StreamPrefix: "User-", Action: ActionRedactFields, Fields: []string{"email"}}
	// The misconfiguration is catchable up front.
	assert.Error(t, policy.Validate())

	mgr := NewRetentionManager(store, []RetentionPolicy{policy})
	assert.NotEmpty(t, mgr.Validate())

	report, err := mgr.Apply(ctx)
	require.NoError(t, err) // non-fatal...
	assert.Equal(t, 1, report.Matched)
	assert.Equal(t, 1, report.Skipped)
	assert.Empty(t, report.KeysRevoked)
	// ...but NOT silent: a redact/anonymize policy with no Apply hook is surfaced loudly.
	assert.True(t, report.Failed(), "a redact/anonymize policy with no Apply hook must fail loudly, not skip silently")
	assert.NotEmpty(t, report.Errors)

	// A DryRun surfaces it too, before any real sweep.
	dry, err := mgr.DryRun(ctx)
	require.NoError(t, err)
	assert.True(t, dry.Failed())
}

func TestRetention_RedactWithHook(t *testing.T) {
	ctx := context.Background()
	store, _ := newEraseTestStore(t, "k")
	seedRetention(t, ctx, store)

	var applied int
	mgr := NewRetentionManager(store, []RetentionPolicy{{
		Name: "mask", StreamPrefix: "User-", Action: ActionRedactFields, Fields: []string{"email"},
		Apply: func(context.Context, StoredEvent) error { applied++; return nil },
	}})
	report, err := mgr.Apply(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, report.Acted)
	assert.Equal(t, 1, applied)
	assert.False(t, report.Failed(), "a correctly-configured redact policy does not flag Failed")
}
