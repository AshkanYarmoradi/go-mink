package testutil

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go-mink.dev/adapters"
)

// makeRecords builds a batch of EventRecords with the given types.
func makeRecords(types ...string) []adapters.EventRecord {
	records := make([]adapters.EventRecord, len(types))
	for i, t := range types {
		records[i] = adapters.EventRecord{
			Type: t,
			Data: []byte(`{}`),
		}
	}
	return records
}

func TestMockAdapter_Append_MonotonicVersionsAndPositions(t *testing.T) {
	ctx := context.Background()
	m := &MockAdapter{}

	// First batch: two events on stream-a, expecting an empty stream.
	first, err := m.Append(ctx, "stream-a", makeRecords("E1", "E2"), adapters.NoStream)
	require.NoError(t, err)
	require.Len(t, first, 2)
	assert.Equal(t, int64(1), first[0].Version)
	assert.Equal(t, int64(2), first[1].Version)
	assert.Equal(t, uint64(1), first[0].GlobalPosition)
	assert.Equal(t, uint64(2), first[1].GlobalPosition)

	// Second batch on the same stream: versions and positions must continue,
	// not reset to 1.
	second, err := m.Append(ctx, "stream-a", makeRecords("E3"), 2)
	require.NoError(t, err)
	require.Len(t, second, 1)
	assert.Equal(t, int64(3), second[0].Version, "version must advance across calls")
	assert.Equal(t, uint64(3), second[0].GlobalPosition, "global position must advance across calls")

	// A different stream keeps its own version sequence but shares the global
	// position counter.
	other, err := m.Append(ctx, "stream-b", makeRecords("E4"), adapters.NoStream)
	require.NoError(t, err)
	require.Len(t, other, 1)
	assert.Equal(t, int64(1), other[0].Version, "per-stream version restarts for a new stream")
	assert.Equal(t, uint64(4), other[0].GlobalPosition, "global position is shared across streams")

	// All events must have been recorded.
	assert.Len(t, m.Events, 4)

	// Global positions must be distinct and monotonic across the recorded events.
	var lastPos uint64
	for _, e := range m.Events {
		assert.Greater(t, e.GlobalPosition, lastPos, "global positions must be strictly increasing")
		lastPos = e.GlobalPosition
	}

	lastPos, err = m.GetLastPosition(ctx)
	require.NoError(t, err)
	assert.Equal(t, uint64(4), lastPos)
}

func TestMockAdapter_Append_ConcurrencyConflict(t *testing.T) {
	ctx := context.Background()
	m := &MockAdapter{}

	_, err := m.Append(ctx, "stream-a", makeRecords("E1"), adapters.NoStream)
	require.NoError(t, err)

	// Stream now at version 1. Appending again with the wrong expected version
	// must be rejected as a concurrency conflict.
	_, err = m.Append(ctx, "stream-a", makeRecords("E2"), 5)
	require.Error(t, err)
	assert.ErrorIs(t, err, adapters.ErrConcurrencyConflict)

	// Appending with NoStream against an existing stream is also a conflict.
	_, err = m.Append(ctx, "stream-a", makeRecords("E3"), adapters.NoStream)
	require.Error(t, err)
	assert.ErrorIs(t, err, adapters.ErrConcurrencyConflict)

	// AnyVersion bypasses the check and succeeds.
	_, err = m.Append(ctx, "stream-a", makeRecords("E4"), adapters.AnyVersion)
	require.NoError(t, err)

	// The failed appends must not have mutated state: only E1 and E4 recorded.
	assert.Len(t, m.Events, 2)
}

func TestMockAdapter_Append_AppendErrOverride(t *testing.T) {
	ctx := context.Background()
	sentinel := errors.New("boom")
	m := &MockAdapter{AppendErr: sentinel}

	_, err := m.Append(ctx, "stream-a", makeRecords("E1"), adapters.AnyVersion)
	assert.ErrorIs(t, err, sentinel)
	assert.Empty(t, m.Events)
}

func TestMockAdapter_LoadFromPosition_Paginates(t *testing.T) {
	ctx := context.Background()
	m := &MockAdapter{}

	// Seed five events.
	_, err := m.Append(ctx, "stream-a", makeRecords("E1", "E2", "E3"), adapters.NoStream)
	require.NoError(t, err)
	_, err = m.Append(ctx, "stream-b", makeRecords("E4", "E5"), adapters.NoStream)
	require.NoError(t, err)

	// Page through the events two at a time, advancing the cursor each call.
	var fromPos uint64
	var collected []adapters.StoredEvent
	const limit = 2
	for iterations := 0; ; iterations++ {
		require.Less(t, iterations, 10, "pagination must terminate, not loop forever")

		batch, err := m.LoadFromPosition(ctx, fromPos, limit)
		require.NoError(t, err)
		if len(batch) == 0 {
			break
		}
		assert.LessOrEqual(t, len(batch), limit, "must respect the limit")
		collected = append(collected, batch...)
		fromPos = batch[len(batch)-1].GlobalPosition
	}

	// We must have walked exactly the five events, in order.
	require.Len(t, collected, 5)
	for i, e := range collected {
		assert.Equal(t, uint64(i+1), e.GlobalPosition)
	}

	// Advancing past the end returns empty.
	tail, err := m.LoadFromPosition(ctx, fromPos, limit)
	require.NoError(t, err)
	assert.Empty(t, tail)

	// limit <= 0 returns everything after fromPosition.
	all, err := m.LoadFromPosition(ctx, 0, 0)
	require.NoError(t, err)
	assert.Len(t, all, 5)
}

func TestMockAdapter_Load_FiltersByStreamAndVersion(t *testing.T) {
	ctx := context.Background()
	m := &MockAdapter{}

	_, err := m.Append(ctx, "stream-a", makeRecords("E1", "E2"), adapters.NoStream)
	require.NoError(t, err)
	_, err = m.Append(ctx, "stream-b", makeRecords("E3"), adapters.NoStream)
	require.NoError(t, err)

	// Only stream-a events.
	aEvents, err := m.Load(ctx, "stream-a", 0)
	require.NoError(t, err)
	require.Len(t, aEvents, 2)

	// fromVersion filters out already-seen versions.
	afterFirst, err := m.Load(ctx, "stream-a", 1)
	require.NoError(t, err)
	require.Len(t, afterFirst, 1)
	assert.Equal(t, int64(2), afterFirst[0].Version)
}

func TestMockAdapter_Append_ConcurrentIsRaceFree(t *testing.T) {
	ctx := context.Background()
	m := &MockAdapter{}

	const goroutines = 8
	const perGoroutine = 25

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			streamID := "stream-" + string(rune('a'+g))
			for i := 0; i < perGoroutine; i++ {
				// AnyVersion avoids cross-goroutine ordering requirements; the
				// point of this test is that the shared counters/state are
				// guarded (run with -race).
				_, err := m.Append(ctx, streamID, makeRecords("E"), adapters.AnyVersion)
				assert.NoError(t, err)
			}
		}(g)
	}
	wg.Wait()

	assert.Len(t, m.Events, goroutines*perGoroutine)

	lastPos, err := m.GetLastPosition(ctx)
	require.NoError(t, err)
	assert.Equal(t, uint64(goroutines*perGoroutine), lastPos)
}
