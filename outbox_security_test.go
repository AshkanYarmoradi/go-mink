package mink

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go-mink.dev/adapters"
	"go-mink.dev/adapters/memory"
)

// =============================================================================
// Outbox wrapper feeds the subject index (security hardening)
//
// EventStoreWithOutbox.Append/SaveAggregate call the adapter directly, so before
// this fix an event written through the outbox wrapper never reached the subject
// index and index-backed GDPR export/erasure silently missed it.
// =============================================================================

// orderIDTagger tags each outboxTestOrderCreated event with its order id, so
// SaveAggregate (which carries no caller metadata) still yields a subject.
func orderIDTagger(eventType string, data []byte, _ Metadata) []string {
	if eventType != "outboxTestOrderCreated" {
		return nil
	}
	var e outboxTestOrderCreated
	if err := json.Unmarshal(data, &e); err != nil || e.OrderID == "" {
		return nil
	}
	return []string{"order-" + e.OrderID}
}

// plainEventStoreAdapter hides the memory adapter's OutboxAppender so the wrapper
// takes the non-atomic fallback path even when a route matches.
type plainEventStoreAdapter struct {
	adapters.EventStoreAdapter
}

type outboxIndexEnv struct {
	store  *EventStore
	outbox *memory.OutboxStore
	idx    *MemorySubjectIndex
}

// newOutboxIndexEnv builds a tagger + index-wired store behind an outbox wrapper
// routing matchType to a webhook destination.
func newOutboxIndexEnv(t *testing.T, adapter adapters.EventStoreAdapter, matchType string, outboxStore OutboxStore) (*EventStoreWithOutbox, *outboxIndexEnv) {
	t.Helper()
	idx := NewMemorySubjectIndex()
	store := New(adapter, WithSubjectTagger(orderIDTagger), WithSubjectIndexWriter(idx))
	store.RegisterEvents(outboxTestOrderCreated{})
	memOutbox, _ := outboxStore.(*memory.OutboxStore)
	routes := []OutboxRoute{{EventTypes: []string{matchType}, Destination: "webhook:https://example.com"}}
	return NewEventStoreWithOutbox(store, outboxStore, routes), &outboxIndexEnv{store: store, outbox: memOutbox, idx: idx}
}

func TestEventStoreWithOutbox_Append_WritesSubjectIndex(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name        string
		adapter     func() adapters.EventStoreAdapter
		matchType   string // route event type; a non-matching route skips the atomic path
		wantOutbox  int
		wantWarning bool
	}{
		{
			name:       "atomic AppendWithOutbox path",
			adapter:    func() adapters.EventStoreAdapter { return memory.NewAdapter() },
			matchType:  "outboxTestOrderCreated",
			wantOutbox: 1,
		},
		{
			name:       "fallback path when no route matches",
			adapter:    func() adapters.EventStoreAdapter { return memory.NewAdapter() },
			matchType:  "SomethingElse",
			wantOutbox: 0,
		},
		{
			name: "fallback path when the adapter is not an OutboxAppender",
			adapter: func() adapters.EventStoreAdapter {
				return &plainEventStoreAdapter{EventStoreAdapter: memory.NewAdapter()}
			},
			matchType:   "outboxTestOrderCreated",
			wantOutbox:  1,
			wantWarning: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := &recordingLogger{}
			es, env := newOutboxIndexEnv(t, tt.adapter(), tt.matchType, memory.NewOutboxStore())
			es.logger = logger

			require.NoError(t, es.Append(ctx, "Order-123", []interface{}{outboxTestOrderCreated{OrderID: "123"}}))

			streams, err := env.idx.StreamsBySubject(ctx, "order-123")
			require.NoError(t, err)
			assert.Equal(t, []string{"Order-123"}, streams, "outbox-path append must reach the subject index")
			assert.Equal(t, tt.wantOutbox, env.outbox.Count())
			if tt.wantWarning {
				assert.NotEmpty(t, logger.warns, "non-atomic fallback is logged")
			}

			// The stored event carries the tag the index was derived from.
			raw, err := env.store.LoadRaw(ctx, "Order-123", 0)
			require.NoError(t, err)
			require.Len(t, raw, 1)
			assert.Equal(t, []string{"order-123"}, GetSubjectTags(raw[0].Metadata))
		})
	}
}

func TestEventStoreWithOutbox_Append_IndexWrittenEvenIfOutboxSchedulingFails(t *testing.T) {
	ctx := context.Background()
	failing := &failingOutboxStore{OutboxStore: memory.NewOutboxStore(), scheduleErr: errors.New("boom")}
	es, env := newOutboxIndexEnv(t, &plainEventStoreAdapter{EventStoreAdapter: memory.NewAdapter()}, "outboxTestOrderCreated", failing)

	err := es.Append(ctx, "Order-123", []interface{}{outboxTestOrderCreated{OrderID: "123"}})
	require.Error(t, err, "outbox scheduling failure is reported")

	// The events were appended before scheduling, so the index must reflect them.
	streams, err := env.idx.StreamsBySubject(ctx, "order-123")
	require.NoError(t, err)
	assert.Equal(t, []string{"Order-123"}, streams)
}

func TestEventStoreWithOutbox_Append_IndexNotWrittenWhenAppendFails(t *testing.T) {
	ctx := context.Background()
	es, env := newOutboxIndexEnv(t, memory.NewAdapter(), "outboxTestOrderCreated", memory.NewOutboxStore())

	// A version conflict on the atomic path writes nothing — and must index nothing.
	err := es.Append(ctx, "Order-123", []interface{}{outboxTestOrderCreated{OrderID: "123"}}, ExpectVersion(5))
	require.ErrorIs(t, err, ErrConcurrencyConflict)
	streams, err := env.idx.StreamsBySubject(ctx, "order-123")
	require.NoError(t, err)
	assert.Empty(t, streams)

	// Same on the fallback path.
	es2, env2 := newOutboxIndexEnv(t, memory.NewAdapter(), "SomethingElse", memory.NewOutboxStore())
	err = es2.Append(ctx, "Order-123", []interface{}{outboxTestOrderCreated{OrderID: "123"}}, ExpectVersion(5))
	require.ErrorIs(t, err, ErrConcurrencyConflict)
	streams, err = env2.idx.StreamsBySubject(ctx, "order-123")
	require.NoError(t, err)
	assert.Empty(t, streams)
}

func TestEventStoreWithOutbox_SaveAggregate_WritesSubjectIndex(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name       string
		adapter    func() adapters.EventStoreAdapter
		matchType  string
		wantOutbox int
	}{
		{
			name:       "atomic AppendWithOutbox path",
			adapter:    func() adapters.EventStoreAdapter { return memory.NewAdapter() },
			matchType:  "outboxTestOrderCreated",
			wantOutbox: 1,
		},
		{
			name:       "fallback path when no route matches",
			adapter:    func() adapters.EventStoreAdapter { return memory.NewAdapter() },
			matchType:  "SomethingElse",
			wantOutbox: 0,
		},
		{
			name: "fallback path when the adapter is not an OutboxAppender",
			adapter: func() adapters.EventStoreAdapter {
				return &plainEventStoreAdapter{EventStoreAdapter: memory.NewAdapter()}
			},
			matchType:  "outboxTestOrderCreated",
			wantOutbox: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			es, env := newOutboxIndexEnv(t, tt.adapter(), tt.matchType, memory.NewOutboxStore())

			agg := newOutboxTestOrderAggregate("123")
			agg.CreateOrder("123")
			require.NoError(t, es.SaveAggregate(ctx, agg))

			streams, err := env.idx.StreamsBySubject(ctx, "order-123")
			require.NoError(t, err)
			assert.Equal(t, []string{"Order-123"}, streams, "outbox-path SaveAggregate must reach the subject index")
			assert.Equal(t, tt.wantOutbox, env.outbox.Count())
			assert.Equal(t, int64(1), agg.Version())
		})
	}
}

func TestEventStoreWithOutbox_SaveAggregate_IndexNotWrittenWhenAppendFails(t *testing.T) {
	ctx := context.Background()
	es, env := newOutboxIndexEnv(t, memory.NewAdapter(), "outboxTestOrderCreated", memory.NewOutboxStore())

	// Seed the stream so a fresh aggregate (version 0) conflicts.
	require.NoError(t, env.store.Append(ctx, "Order-123", []interface{}{outboxTestOrderCreated{OrderID: "seed"}}))

	agg := newOutboxTestOrderAggregate("123")
	agg.CreateOrder("123")
	require.ErrorIs(t, es.SaveAggregate(ctx, agg), ErrConcurrencyConflict)

	streams, err := env.idx.StreamsBySubject(ctx, "order-123")
	require.NoError(t, err)
	assert.Empty(t, streams, "a failed append must not be indexed")
}

// TestEventStoreWithOutbox_IndexBackedResolver_SeesOutboxEvents is the end-to-end
// proof: an index-backed SubjectResolver (the fast path export/erasure use) now
// finds a stream whose only events were written through the outbox wrapper.
func TestEventStoreWithOutbox_IndexBackedResolver_SeesOutboxEvents(t *testing.T) {
	ctx := context.Background()
	es, env := newOutboxIndexEnv(t, memory.NewAdapter(), "outboxTestOrderCreated", memory.NewOutboxStore())

	require.NoError(t, es.Append(ctx, "Order-a1", []interface{}{outboxTestOrderCreated{OrderID: "a1"}}))
	// A second stream (aggregate id a2) carrying an event for the same subject.
	agg2 := newOutboxTestOrderAggregate("a2")
	agg2.CreateOrder("a1") // same subject (order-a1), different stream
	require.NoError(t, es.SaveAggregate(ctx, agg2))

	resolver := NewSubjectResolver(env.store, WithResolverIndex(env.idx), WithAuthoritativeIndex())
	fp, err := resolver.Resolve(ctx, "order-a1")
	require.NoError(t, err)
	assert.Equal(t, []string{"Order-a1", "Order-a2"}, fp.Streams)
	assert.Equal(t, 2, fp.EventCount)
	assert.False(t, fp.Partial)
}

// TestEventStoreWithOutbox_NoIndex_ZeroOverhead pins the unconfigured path: without
// a subject index writer, the outbox wrapper neither allocates a subject set nor
// calls into an index — the behavior every existing outbox test already relies on.
func TestEventStoreWithOutbox_NoIndex_ZeroOverhead(t *testing.T) {
	ctx := context.Background()
	store := New(memory.NewAdapter(), WithSubjectTagger(orderIDTagger))
	store.RegisterEvents(outboxTestOrderCreated{})
	es := NewEventStoreWithOutbox(store, memory.NewOutboxStore(), nil)
	require.Nil(t, store.subjectIndex)
	require.NoError(t, es.Append(ctx, "Order-1", []interface{}{outboxTestOrderCreated{OrderID: "1"}}))
	raw, err := store.LoadRaw(ctx, "Order-1", 0)
	require.NoError(t, err)
	assert.Equal(t, []string{"order-1"}, GetSubjectTags(raw[0].Metadata), "tagging still applies without an index")
}

// =============================================================================
// Destination redaction on the Transform-failure log line
// =============================================================================

func TestEventStoreWithOutbox_TransformFailure_LogsRedactedDestination(t *testing.T) {
	ctx := context.Background()
	store := New(memory.NewAdapter())
	store.RegisterEvents(outboxTestOrderCreated{})

	// Credentials in every place a webhook URL can carry them: userinfo, path
	// (Slack/Discord style) and query string.
	dest := "webhook:https://alice:hunter2@hooks.example.com/services/T000/B000/s3cret?api_key=k3y#frag"
	routes := []OutboxRoute{{
		EventTypes:  []string{"outboxTestOrderCreated"},
		Destination: dest,
		Transform: func(_ interface{}, _ StoredEvent) ([]byte, error) {
			return nil, errors.New("transform exploded")
		},
	}}
	logger := &argsRecordingLogger{}
	es := NewEventStoreWithOutbox(store, memory.NewOutboxStore(), routes, WithOutboxLogger(logger))

	require.NoError(t, es.Append(ctx, "order-1", []interface{}{outboxTestOrderCreated{OrderID: "1"}}))

	entry, ok := logger.find("Failed to transform outbox payload")
	require.True(t, ok, "the transform failure must be logged")
	logged, ok := entry.argValue("destination")
	require.True(t, ok)
	assert.Equal(t, "webhook:https://hooks.example.com", logged)
	for _, secret := range []string{"alice", "hunter2", "T000", "s3cret", "api_key", "k3y", "frag"} {
		assert.NotContains(t, entry.rendered(), secret, "secret %q leaked into the log line", secret)
	}
}
