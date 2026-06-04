package postgres_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go-mink.dev"
)

// subSeqEvent is a minimal event type for subscription integration tests.
type subSeqEvent struct {
	Seq int `json:"seq"`
}

// subUniqueStream returns a stream id unique to this test run so integration
// tests sharing the schema do not observe each other's events.
func subUniqueStream(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

// subCollect reads up to n events from the subscription, returning early once
// timeout elapses so a missing event fails the assertion loudly instead of
// hanging the test.
func subCollect(t *testing.T, sub mink.Subscription, n int, timeout time.Duration) []mink.StoredEvent {
	t.Helper()
	got := make([]mink.StoredEvent, 0, n)
	deadline := time.After(timeout)
	for len(got) < n {
		select {
		case e, ok := <-sub.Events():
			if !ok {
				return got
			}
			got = append(got, e)
		case <-deadline:
			return got
		}
	}
	return got
}

// drainUntilClosed reads and discards events until Events() closes, failing the
// test (rather than hanging CI) if the subscription does not close within timeout.
func drainUntilClosed(t *testing.T, sub mink.Subscription, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case _, ok := <-sub.Events():
			if !ok {
				return
			}
		case <-deadline:
			t.Fatalf("subscription did not close within %s", timeout)
		}
	}
}

// TestPostgresIntegration_SubscribeStream_FromVersionIsExclusive verifies that
// fromVersion is exclusive against a real adapter-native subscription: subscribing
// from version 1 delivers v2 and v3, not v1 (consistent with Load).
func TestPostgresIntegration_SubscribeStream_FromVersionIsExclusive(t *testing.T) {
	store, ctx := setupE2EStore(t)
	store.RegisterEvents(&subSeqEvent{})
	streamID := subUniqueStream("ExclBoundary")

	require.NoError(t, store.Append(ctx, streamID, []interface{}{
		&subSeqEvent{Seq: 1}, &subSeqEvent{Seq: 2}, &subSeqEvent{Seq: 3},
	}))

	sub, err := store.SubscribeStream(ctx, streamID, 1)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.Close() })

	got := subCollect(t, sub, 2, 5*time.Second)
	require.Len(t, got, 2, "exclusive fromVersion=1 must deliver exactly v2 and v3")
	assert.Equal(t, int64(2), got[0].Version)
	assert.Equal(t, int64(3), got[1].Version)
}

// TestPostgresIntegration_SubscribeStream_CleanCloseLeavesErrNil verifies the
// Err() contract against the real PostgreSQL subscription: a consumer Close() is
// a clean shutdown and must leave Err() nil.
func TestPostgresIntegration_SubscribeStream_CleanCloseLeavesErrNil(t *testing.T) {
	store, ctx := setupE2EStore(t)
	store.RegisterEvents(&subSeqEvent{})
	streamID := subUniqueStream("CloseNil")

	require.NoError(t, store.Append(ctx, streamID, []interface{}{&subSeqEvent{Seq: 1}}))

	sub, err := store.SubscribeStream(ctx, streamID, 0)
	require.NoError(t, err)

	require.Len(t, subCollect(t, sub, 1, 5*time.Second), 1)

	require.NoError(t, sub.Close())
	// Draining until Events() closes guarantees the bridge goroutine has exited
	// (bounded so a Close/drain regression fails fast instead of hanging CI).
	drainUntilClosed(t, sub, 5*time.Second)
	assert.NoError(t, sub.Err(), "a consumer Close() is a clean shutdown")
}

// TestPostgresIntegration_SubscribeStream_ExternalCancelSurfacesErr verifies that
// cancelling the caller's context (an external stop) is surfaced via Err().
func TestPostgresIntegration_SubscribeStream_ExternalCancelSurfacesErr(t *testing.T) {
	store, _ := setupE2EStore(t)
	store.RegisterEvents(&subSeqEvent{})
	streamID := subUniqueStream("CancelErr")

	subCtx, cancel := context.WithCancel(context.Background())
	sub, err := store.SubscribeStream(subCtx, streamID, 0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.Close() })

	cancel()
	drainUntilClosed(t, sub, 5*time.Second)
	assert.ErrorIs(t, sub.Err(), context.Canceled, "external cancellation must surface via Err()")
}

// TestPostgresIntegration_SubscribeStream_HonorsBufferSizeWithoutDropping verifies
// that a burst delivered through a buffered subscription arrives complete and in
// order (the caller's BufferSize is forwarded to the adapter, not just the bridge).
func TestPostgresIntegration_SubscribeStream_HonorsBufferSizeWithoutDropping(t *testing.T) {
	store, ctx := setupE2EStore(t)
	store.RegisterEvents(&subSeqEvent{})
	streamID := subUniqueStream("BufferAll")

	const n = 25
	events := make([]interface{}, n)
	for i := range events {
		events[i] = &subSeqEvent{Seq: i + 1}
	}
	require.NoError(t, store.Append(ctx, streamID, events))

	sub, err := store.SubscribeStream(ctx, streamID, 0, mink.SubscriptionOptions{BufferSize: n})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.Close() })

	got := subCollect(t, sub, n, 10*time.Second)
	require.Len(t, got, n, "all buffered events must be delivered without loss")
	for i, e := range got {
		assert.Equal(t, int64(i+1), e.Version, "events must arrive in version order")
	}
}
