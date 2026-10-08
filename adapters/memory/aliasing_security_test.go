package memory

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	mink "go-mink.dev"
	"go-mink.dev/adapters"
)

const (
	aliasingOriginalData    = `{"pii":"alice"}`
	aliasingOriginalSubject = "alice"
)

// appendAliasingFixture appends one event with PII in Data and a subject tag in
// Metadata.Custom and returns Append's result.
func appendAliasingFixture(t *testing.T, adapter *MemoryAdapter, streamID string) []adapters.StoredEvent {
	t.Helper()
	stored, err := adapter.Append(context.Background(), streamID, []adapters.EventRecord{{
		Type:     "UserRegistered",
		Data:     []byte(aliasingOriginalData),
		Metadata: adapters.Metadata{Custom: map[string]string{"$subjects": aliasingOriginalSubject}},
	}}, mink.NoStream)
	require.NoError(t, err)
	require.Len(t, stored, 1)
	return stored
}

// tamperEvent overwrites the event's Data in place (same length, so the write lands
// inside the existing backing array) and its Custom map, the way a buggy or
// malicious projection would.
func tamperEvent(e *adapters.StoredEvent) {
	copy(e.Data, []byte(`{"pii":"BBBBB"}`))
	e.Metadata.Custom["$subjects"] = "mallory"
}

// assertLogUnchanged reloads the stream through the per-stream and the global log
// and checks the stored event still carries the original Data and subject tag.
func assertLogUnchanged(t *testing.T, adapter *MemoryAdapter, streamID string) {
	t.Helper()
	ctx := context.Background()

	loaded, err := adapter.Load(ctx, streamID, 0)
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	assert.Equal(t, aliasingOriginalData, string(loaded[0].Data), "Load: stored Data was mutated through an alias")
	assert.Equal(t, aliasingOriginalSubject, loaded[0].Metadata.Custom["$subjects"], "Load: stored Custom was mutated through an alias")

	global, err := adapter.LoadFromPosition(ctx, 0, 10)
	require.NoError(t, err)
	require.Len(t, global, 1)
	assert.Equal(t, aliasingOriginalData, string(global[0].Data), "LoadFromPosition: global log was mutated through an alias")
	assert.Equal(t, aliasingOriginalSubject, global[0].Metadata.Custom["$subjects"], "LoadFromPosition: global log Custom was mutated through an alias")
}

func TestMemoryAdapter_Append_ReturnedEventsDoNotAliasLog(t *testing.T) {
	adapter := NewAdapter()
	stored := appendAliasingFixture(t, adapter, "User-1")
	tamperEvent(&stored[0])
	assertLogUnchanged(t, adapter, "User-1")
}

func TestMemoryAdapter_Load_ReturnsDetachedCopies(t *testing.T) {
	adapter := NewAdapter()
	appendAliasingFixture(t, adapter, "User-1")
	ctx := context.Background()

	loaded, err := adapter.Load(ctx, "User-1", 0)
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	tamperEvent(&loaded[0])
	assertLogUnchanged(t, adapter, "User-1")

	// Two loads never share a backing array either.
	a, err := adapter.Load(ctx, "User-1", 0)
	require.NoError(t, err)
	b, err := adapter.Load(ctx, "User-1", 0)
	require.NoError(t, err)
	a[0].Data[0] = 'X'
	a[0].Metadata.Custom["$subjects"] = "eve"
	assert.Equal(t, aliasingOriginalData, string(b[0].Data))
	assert.Equal(t, aliasingOriginalSubject, b[0].Metadata.Custom["$subjects"])
}

func TestMemoryAdapter_LoadFromPosition_ReturnsDetachedCopies(t *testing.T) {
	adapter := NewAdapter()
	appendAliasingFixture(t, adapter, "User-1")
	ctx := context.Background()

	events, err := adapter.LoadFromPosition(ctx, 0, 10)
	require.NoError(t, err)
	require.Len(t, events, 1)
	tamperEvent(&events[0])
	assertLogUnchanged(t, adapter, "User-1")

	filtered, err := adapter.LoadFromPositionFiltered(ctx, 0, 10, adapters.FeedFilter{EventTypes: []string{"UserRegistered"}})
	require.NoError(t, err)
	require.Len(t, filtered, 1)
	tamperEvent(&filtered[0])
	assertLogUnchanged(t, adapter, "User-1")
}

func TestMemoryAdapter_GetStreamEvents_ReturnsDetachedCopies(t *testing.T) {
	adapter := NewAdapter()
	appendAliasingFixture(t, adapter, "User-1")

	events, err := adapter.GetStreamEvents(context.Background(), "User-1", 0, 10)
	require.NoError(t, err)
	require.Len(t, events, 1)
	tamperEvent(&events[0])
	assertLogUnchanged(t, adapter, "User-1")
}

func receiveEvent(t *testing.T, ch <-chan adapters.StoredEvent) adapters.StoredEvent {
	t.Helper()
	select {
	case e, ok := <-ch:
		require.True(t, ok, "subscription closed before delivering the event")
		return e
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a subscription event")
		return adapters.StoredEvent{}
	}
}

func TestMemoryAdapter_SubscribeAll_ReplayDeliversDetachedCopies(t *testing.T) {
	adapter := NewAdapter()
	appendAliasingFixture(t, adapter, "User-1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := adapter.SubscribeAll(ctx, 0)
	require.NoError(t, err)
	e := receiveEvent(t, ch)
	tamperEvent(&e)
	assertLogUnchanged(t, adapter, "User-1")
}

func TestMemoryAdapter_SubscribeStream_DeliversDetachedCopies(t *testing.T) {
	adapter := NewAdapter()
	appendAliasingFixture(t, adapter, "User-1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := adapter.SubscribeStream(ctx, "User-1", 0)
	require.NoError(t, err)
	e := receiveEvent(t, ch)
	tamperEvent(&e)
	assertLogUnchanged(t, adapter, "User-1")
}

func TestMemoryAdapter_SubscribeAll_LiveDeliveryIsDetachedPerSubscriber(t *testing.T) {
	adapter := NewAdapter()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	first, err := adapter.SubscribeAll(ctx, 0)
	require.NoError(t, err)
	second, err := adapter.SubscribeAll(ctx, 0)
	require.NoError(t, err)

	stored := appendAliasingFixture(t, adapter, "User-1")

	e1 := receiveEvent(t, first)
	tamperEvent(&e1)

	// Neither the log, the Append caller's result, nor the other subscriber sees it.
	assertLogUnchanged(t, adapter, "User-1")
	assert.Equal(t, aliasingOriginalData, string(stored[0].Data))
	assert.Equal(t, aliasingOriginalSubject, stored[0].Metadata.Custom["$subjects"])
	e2 := receiveEvent(t, second)
	assert.Equal(t, aliasingOriginalData, string(e2.Data))
	assert.Equal(t, aliasingOriginalSubject, e2.Metadata.Custom["$subjects"])
}

func TestMemoryAdapter_RewriteEventData_DoesNotRetainCallerBuffers(t *testing.T) {
	adapter := NewAdapter()
	appendAliasingFixture(t, adapter, "User-1")
	ctx := context.Background()

	redacted := []byte(`{"pii":"[gone]"}`)
	md := adapters.Metadata{Custom: map[string]string{"$subjects": "alice", "$redacted": "true"}}
	require.NoError(t, adapter.RewriteEventData(ctx, "User-1", 1, redacted, md))

	// The caller keeps writing into its own buffer/map after the rewrite ...
	copy(redacted, []byte(`{"pii":"alice!"}`))
	md.Custom["$redacted"] = "false"
	md.Custom["$subjects"] = "mallory"

	// ... and neither log notices.
	reads := []func() ([]adapters.StoredEvent, error){
		func() ([]adapters.StoredEvent, error) { return adapter.Load(ctx, "User-1", 0) },
		func() ([]adapters.StoredEvent, error) { return adapter.LoadFromPosition(ctx, 0, 10) },
	}
	for _, read := range reads {
		events, err := read()
		require.NoError(t, err)
		require.Len(t, events, 1)
		assert.Equal(t, `{"pii":"[gone]"}`, string(events[0].Data))
		assert.Equal(t, "true", events[0].Metadata.Custom["$redacted"])
		assert.Equal(t, "alice", events[0].Metadata.Custom["$subjects"])

		// Rewritten events leave the adapter as detached copies too.
		copy(events[0].Data, []byte(`{"pii":"alice!"}`))
		events[0].Metadata.Custom["$redacted"] = "false"
	}
	again, err := adapter.Load(ctx, "User-1", 0)
	require.NoError(t, err)
	assert.Equal(t, `{"pii":"[gone]"}`, string(again[0].Data))
	assert.Equal(t, "true", again[0].Metadata.Custom["$redacted"])

	// nil Data/Custom stay nil (no spurious allocation): the crypto-shred case.
	require.NoError(t, adapter.RewriteEventData(ctx, "User-1", 1, nil, adapters.Metadata{}))
	again, err = adapter.Load(ctx, "User-1", 0)
	require.NoError(t, err)
	assert.Nil(t, again[0].Data)
	assert.Nil(t, again[0].Metadata.Custom)
}

func TestIdempotencyStore_ResponseDoesNotAliasStoredRecord(t *testing.T) {
	ctx := context.Background()
	s := NewIdempotencyStore()
	exp := time.Now().Add(time.Hour)

	rec := &adapters.IdempotencyRecord{Key: "k1", CommandType: "C", Response: []byte(`{"ok":true}`), Success: true, ProcessedAt: time.Now(), ExpiresAt: exp}
	require.NoError(t, s.Store(ctx, rec))
	copy(rec.Response, []byte(`{"ok":XXXX}`))

	got, err := s.Get(ctx, "k1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, `{"ok":true}`, string(got.Response), "Store must not retain the caller's Response buffer")

	copy(got.Response, []byte(`{"ok":XXXX}`))
	again, err := s.Get(ctx, "k1")
	require.NoError(t, err)
	assert.Equal(t, `{"ok":true}`, string(again.Response), "Get must hand out a detached Response")

	rec2 := &adapters.IdempotencyRecord{Key: "k2", CommandType: "C", Response: []byte(`{"n":1}`), ProcessedAt: time.Now(), ExpiresAt: exp}
	inserted, err := s.StoreIfAbsent(ctx, rec2)
	require.NoError(t, err)
	require.True(t, inserted)
	copy(rec2.Response, []byte(`{"n":9}`))
	got2, err := s.Get(ctx, "k2")
	require.NoError(t, err)
	require.NotNil(t, got2)
	assert.Equal(t, `{"n":1}`, string(got2.Response), "StoreIfAbsent must not retain the caller's Response buffer")

	// A record without a response stays nil (no spurious allocation).
	require.NoError(t, s.Store(ctx, &adapters.IdempotencyRecord{Key: "k3", CommandType: "C", ProcessedAt: time.Now(), ExpiresAt: exp}))
	got3, err := s.Get(ctx, "k3")
	require.NoError(t, err)
	require.NotNil(t, got3)
	assert.Nil(t, got3.Response)
}

func TestMemoryAdapter_GenerateSchema_SanitizesProjectName(t *testing.T) {
	adapter := NewAdapter()
	hostile := "proj\n-- end\nDROP TABLE events; --'\r\n SELECT 1;"

	hostileOut := adapter.GenerateSchema(hostile, "events", "snapshots", "outbox")
	benignOut := adapter.GenerateSchema("proj", "events", "snapshots", "outbox")

	hostileLines := strings.Split(hostileOut, "\n")
	benignLines := strings.Split(benignOut, "\n")
	require.Equal(t, len(benignLines), len(hostileLines), "a hostile project name must not add lines")
	assert.Equal(t, "-- Generated for: proj-- endDROP TABLE events; --''SELECT 1;", hostileLines[1])
	assert.Equal(t, benignLines[2:], hostileLines[2:])
	for _, line := range hostileLines {
		if line != "" {
			assert.True(t, strings.HasPrefix(line, "--"), "every line must remain a comment: %q", line)
		}
	}
}
