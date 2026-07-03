package mink

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go-mink.dev/adapters/memory"
)

// TestUnregisteredEventTypeError_Unwrap verifies the typed error implements Unwrap to the
// sentinel (CLAUDE.md rule 5: typed errors implement both Is() and Unwrap()).
func TestUnregisteredEventTypeError_Unwrap(t *testing.T) {
	err := &UnregisteredEventTypeError{StreamID: "s", EventType: "T", Version: 3}
	assert.Equal(t, ErrUnregisteredEventType, errors.Unwrap(err))
	assert.ErrorIs(t, err, ErrUnregisteredEventType)
}

type replayCreated struct {
	ID string `json:"id"`
}
type replaySuspended struct {
	Reason string `json:"reason"`
}

// replayAgg defaults to "active" in its constructor and flips to "suspended" only when it
// applies a replaySuspended event — so a dropped suspend event leaves a plausible-but-wrong
// "active" state, exactly the silent-loss bug this change surfaces.
type replayAgg struct {
	AggregateBase
	Status string
}

func newReplayAgg(id string) *replayAgg {
	return &replayAgg{AggregateBase: NewAggregateBase(id, "ReplayAgg"), Status: "active"}
}

func (a *replayAgg) ApplyEvent(event interface{}) error {
	switch event.(type) {
	case replaySuspended:
		a.Status = "suspended"
	case replayCreated:
		a.Status = "active"
	}
	return nil
}

func warnedAboutUnregistered(l *testLogger) bool {
	for _, m := range l.warnLogs {
		if strings.Contains(m, "unregistered event type on aggregate replay") {
			return true
		}
	}
	return false
}

func TestLoadAggregate_UnregisteredType_Detection(t *testing.T) {
	ctx := context.Background()
	logger := newTestLogger()
	store := New(memory.NewAdapter(), WithLogger(logger))
	store.RegisterEvents(replayCreated{}) // NOT replaySuspended
	require.NoError(t, store.Append(ctx, "ReplayAgg-1",
		[]interface{}{replayCreated{ID: "1"}, replaySuspended{Reason: "fraud"}}))

	agg := newReplayAgg("1")
	require.NoError(t, store.LoadAggregate(ctx, agg))

	// Default is lenient: the unregistered suspend event is dropped (state loss reproduced),
	// but now WARNed rather than silent.
	assert.Equal(t, "active", agg.Status, "suspend event dropped → stale state (the bug, now observable)")
	assert.True(t, warnedAboutUnregistered(logger), "expected a WARN about the unregistered replay type")

	// The read path (Load) is unchanged: an unregistered event still deserializes to the map
	// fallback (Non-Goal: projections/export behavior preserved).
	events, err := store.Load(ctx, "ReplayAgg-1")
	require.NoError(t, err)
	require.Len(t, events, 2)
	_, isMap := events[1].Data.(map[string]interface{})
	assert.True(t, isMap, "read path still returns the map fallback for unregistered types")
}

func TestLoadAggregate_UnregisteredType_NoWarnWhenRegistered(t *testing.T) {
	ctx := context.Background()
	logger := newTestLogger()
	store := New(memory.NewAdapter(), WithLogger(logger))
	store.RegisterAggregateEvents(replayCreated{}, replaySuspended{}) // both registered
	require.NoError(t, store.Append(ctx, "ReplayAgg-2",
		[]interface{}{replayCreated{ID: "2"}, replaySuspended{Reason: "fraud"}}))

	agg := newReplayAgg("2")
	require.NoError(t, store.LoadAggregate(ctx, agg))
	assert.Equal(t, "suspended", agg.Status, "both events applied → correct state")
	assert.False(t, warnedAboutUnregistered(logger), "no warning when everything is registered")
}

func TestLoadAggregate_StrictReplay(t *testing.T) {
	ctx := context.Background()

	t.Run("strict + unregistered returns UnregisteredEventTypeError", func(t *testing.T) {
		store := New(memory.NewAdapter(), WithStrictReplay())
		store.RegisterEvents(replayCreated{})
		require.NoError(t, store.Append(ctx, "ReplayAgg-3",
			[]interface{}{replayCreated{ID: "3"}, replaySuspended{Reason: "fraud"}}))

		agg := newReplayAgg("3")
		err := store.LoadAggregate(ctx, agg)
		require.ErrorIs(t, err, ErrUnregisteredEventType)
		var ue *UnregisteredEventTypeError
		require.ErrorAs(t, err, &ue)
		assert.Equal(t, "replaySuspended", ue.EventType)
		assert.Equal(t, "ReplayAgg-3", ue.StreamID)
		assert.Equal(t, int64(2), ue.Version)
	})

	t.Run("strict + fully registered loads identically to lenient", func(t *testing.T) {
		store := New(memory.NewAdapter(), WithStrictReplay())
		store.RegisterEvents(replayCreated{}, replaySuspended{})
		require.NoError(t, store.Append(ctx, "ReplayAgg-4",
			[]interface{}{replayCreated{ID: "4"}, replaySuspended{Reason: "fraud"}}))

		agg := newReplayAgg("4")
		require.NoError(t, store.LoadAggregate(ctx, agg))
		assert.Equal(t, "suspended", agg.Status)
	})
}

func TestRegisteredEventTypes_Introspection(t *testing.T) {
	store := New(memory.NewAdapter())
	store.RegisterAggregateEvents(replayCreated{}, replaySuspended{})
	assert.ElementsMatch(t, []string{"replayCreated", "replaySuspended"}, store.RegisteredEventTypes())
}

func TestUnregisteredStreamTypes_Audit(t *testing.T) {
	ctx := context.Background()
	store := New(memory.NewAdapter())
	store.RegisterEvents(replayCreated{}) // suspend not registered
	require.NoError(t, store.Append(ctx, "ReplayAgg-5",
		[]interface{}{replayCreated{ID: "5"}, replaySuspended{Reason: "fraud"}}))

	missing, err := store.UnregisteredStreamTypes(ctx, "ReplayAgg-5")
	require.NoError(t, err)
	assert.Equal(t, []string{"replaySuspended"}, missing)

	// Also visible in the all-streams audit.
	all, err := store.UnregisteredEventTypes(ctx)
	require.NoError(t, err)
	assert.Contains(t, all, "replaySuspended")

	// Register it → the audit is clean (and it never mutated the log: still 2 events).
	store.RegisterEvents(replaySuspended{})
	missing, err = store.UnregisteredStreamTypes(ctx, "ReplayAgg-5")
	require.NoError(t, err)
	assert.Empty(t, missing)
	events, err := store.Load(ctx, "ReplayAgg-5")
	require.NoError(t, err)
	assert.Len(t, events, 2)
}

func TestAutoRegisterOnAppend(t *testing.T) {
	ctx := context.Background()

	t.Run("with the option, save-then-load round-trips without explicit RegisterEvents", func(t *testing.T) {
		store := New(memory.NewAdapter(), WithAutoRegisterOnAppend()) // NO RegisterEvents
		require.NoError(t, store.Append(ctx, "ReplayAgg-6",
			[]interface{}{replayCreated{ID: "6"}, replaySuspended{Reason: "fraud"}}))

		agg := newReplayAgg("6")
		require.NoError(t, store.LoadAggregate(ctx, agg))
		assert.Equal(t, "suspended", agg.Status, "auto-registered types resolve on load in-process")
		assert.ElementsMatch(t, []string{"replayCreated", "replaySuspended"}, store.RegisteredEventTypes())
	})

	t.Run("without it, behavior is unchanged (unregistered type dropped)", func(t *testing.T) {
		store := New(memory.NewAdapter())
		require.NoError(t, store.Append(ctx, "ReplayAgg-7",
			[]interface{}{replayCreated{ID: "7"}, replaySuspended{Reason: "fraud"}}))
		agg := newReplayAgg("7")
		require.NoError(t, store.LoadAggregate(ctx, agg))
		assert.Equal(t, "active", agg.Status, "no auto-register → suspend dropped, as before")
	})
}
