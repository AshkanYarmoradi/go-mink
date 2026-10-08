package mink

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go-mink.dev/adapters"
	"go-mink.dev/adapters/memory"
)

// ---------------------------------------------------------------------------
// Legacy-only wrappers: embedding the store INTERFACE promotes only its methods, so
// the wave-1 footprint purger / counter methods are hidden and the erasers must fall
// back to the bare-subject-id purge (and report the store as uncountable).
// ---------------------------------------------------------------------------

type legacyOutboxStore struct {
	adapters.OutboxStore
	inner *memory.OutboxStore
}

func (l legacyOutboxStore) DeleteOutboxBySubject(ctx context.Context, id string) (int64, error) {
	return l.inner.DeleteOutboxBySubject(ctx, id)
}

type legacyAuditStore struct {
	adapters.AuditStore
	inner *memory.AuditStore
}

func (l legacyAuditStore) DeleteAuditBySubject(ctx context.Context, id string) (int64, error) {
	return l.inner.DeleteAuditBySubject(ctx, id)
}

type legacySagaStore struct {
	adapters.SagaStore
	inner *memory.SagaStore
}

func (l legacySagaStore) DeleteSagasBySubject(ctx context.Context, id string) (int64, error) {
	return l.inner.DeleteSagasBySubject(ctx, id)
}

type legacyIdempotencyStore struct {
	adapters.IdempotencyStore
	inner *memory.IdempotencyStore
}

func (l legacyIdempotencyStore) DeleteIdempotencyBySubject(ctx context.Context, id string) (int64, error) {
	return l.inner.DeleteIdempotencyBySubject(ctx, id)
}

// footprintOnlyAuditStore offers the footprint purger but NOT the legacy actor purger.
type footprintOnlyAuditStore struct {
	adapters.AuditStore
	inner *memory.AuditStore
}

func (f footprintOnlyAuditStore) DeleteAuditByAggregateIDs(ctx context.Context, ids []string) (int64, error) {
	return f.inner.DeleteAuditByAggregateIDs(ctx, ids)
}

// Erroring doubles: the embedded nil interfaces are never invoked.
var errStoreDown = errors.New("store down")

type erroringOutboxStore struct{ adapters.OutboxStore }

func (erroringOutboxStore) DeleteOutboxByAggregateIDs(context.Context, []string) (int64, error) {
	return 0, errStoreDown
}
func (erroringOutboxStore) DeleteOutboxBySubject(context.Context, string) (int64, error) {
	return 0, errStoreDown
}

type erroringLegacyOutboxStore struct{ adapters.OutboxStore }

func (erroringLegacyOutboxStore) DeleteOutboxBySubject(context.Context, string) (int64, error) {
	return 0, errStoreDown
}

type erroringAuditStore struct{ adapters.AuditStore }

func (erroringAuditStore) DeleteAuditByAggregateIDs(context.Context, []string) (int64, error) {
	return 0, errStoreDown
}

type erroringAuditActorStore struct {
	adapters.AuditStore
	inner *memory.AuditStore
}

func (e erroringAuditActorStore) DeleteAuditByAggregateIDs(ctx context.Context, ids []string) (int64, error) {
	return e.inner.DeleteAuditByAggregateIDs(ctx, ids)
}
func (erroringAuditActorStore) DeleteAuditBySubject(context.Context, string) (int64, error) {
	return 0, errStoreDown
}

type erroringSagaStore struct{ adapters.SagaStore }

func (erroringSagaStore) DeleteSagasByCorrelationIDs(context.Context, []string) (int64, error) {
	return 0, errStoreDown
}

type erroringLegacySagaStore struct{ adapters.SagaStore }

func (erroringLegacySagaStore) DeleteSagasBySubject(context.Context, string) (int64, error) {
	return 0, errStoreDown
}

type erroringIdempotencyStore struct{ adapters.IdempotencyStore }

func (erroringIdempotencyStore) DeleteIdempotencyByAggregateIDs(context.Context, []string) (int64, error) {
	return 0, errStoreDown
}

type erroringLegacyIdempotencyStore struct{ adapters.IdempotencyStore }

func (erroringLegacyIdempotencyStore) DeleteIdempotencyBySubject(context.Context, string) (int64, error) {
	return 0, errStoreDown
}

// failingSnapshotAdapter fails LoadSnapshot (for the counter's error path).
type failingSnapshotAdapter struct{ adapters.SnapshotAdapter }

func (failingSnapshotAdapter) LoadSnapshot(context.Context, string) (*adapters.SnapshotRecord, error) {
	return nil, errStoreDown
}

// ---------------------------------------------------------------------------

func TestSubjectFootprintIDs(t *testing.T) {
	tests := []struct {
		name        string
		id          string
		fp          *SubjectFootprint
		want        []string // SubjectFootprintIDs: subject id + EXCLUSIVE stream ids
		wantDerived []string // SubjectFootprintIDsWithDerived: + aggregate ids derived from exclusive streams
	}{
		{name: "nil footprint yields the bare subject id", id: "u1", fp: nil, want: []string{"u1"}, wantDerived: []string{"u1"}},
		{name: "empty footprint yields the bare subject id", id: "u1", fp: &SubjectFootprint{}, want: []string{"u1"}, wantDerived: []string{"u1"}},
		{
			name:        "stream ids are added; derived aggregate ids only on the derived variant",
			id:          "u1",
			fp:          &SubjectFootprint{Streams: []string{"User-u1", "Order-o1"}},
			want:        []string{"u1", "User-u1", "Order-o1"},
			wantDerived: []string{"u1", "User-u1", "Order-o1", "o1"},
		},
		{
			name:        "aggregate ids may themselves contain dashes (split on the FIRST dash)",
			id:          "u1",
			fp:          &SubjectFootprint{Streams: []string{"Order-ord-42-x"}},
			want:        []string{"u1", "Order-ord-42-x"},
			wantDerived: []string{"u1", "Order-ord-42-x", "ord-42-x"},
		},
		{
			name:        "streams without a type prefix, or with an empty part, derive nothing",
			id:          "u1",
			fp:          &SubjectFootprint{Streams: []string{"plain", "-u9", "User-", ""}},
			want:        []string{"u1", "plain", "-u9", "User-"},
			wantDerived: []string{"u1", "plain", "-u9", "User-"},
		},
		{
			name:        "duplicates collapse and empty ids are dropped",
			id:          "u1",
			fp:          &SubjectFootprint{Streams: []string{"User-u1", "User-u1", "X-u1"}},
			want:        []string{"u1", "User-u1", "X-u1"},
			wantDerived: []string{"u1", "User-u1", "X-u1"},
		},
		{
			name:        "shared streams are excluded, and nothing is derived from them",
			id:          "u1",
			fp:          &SubjectFootprint{Streams: []string{"Order-o1", "Shared-s1", "User-u1"}, SharedStreams: []string{"Shared-s1"}},
			want:        []string{"u1", "Order-o1", "User-u1"},
			wantDerived: []string{"u1", "Order-o1", "o1", "User-u1"},
		},
		{
			name:        "a footprint whose every stream is shared yields the bare subject id",
			id:          "u1",
			fp:          &SubjectFootprint{Streams: []string{"Shared-s1"}, SharedStreams: []string{"Shared-s1"}},
			want:        []string{"u1"},
			wantDerived: []string{"u1"},
		},
		{name: "empty subject id is dropped", id: "", fp: &SubjectFootprint{Streams: []string{"A-b"}}, want: []string{"A-b"}, wantDerived: []string{"A-b", "b"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, SubjectFootprintIDs(tt.id, tt.fp))
			assert.Equal(t, tt.wantDerived, SubjectFootprintIDsWithDerived(tt.id, tt.fp))
		})
	}
}

func TestSubjectFootprint_ExclusiveStreams(t *testing.T) {
	var nilFP *SubjectFootprint
	assert.Nil(t, nilFP.ExclusiveStreams())
	assert.Nil(t, (&SubjectFootprint{}).ExclusiveStreams())

	all := &SubjectFootprint{Streams: []string{"A-1", "B-2"}}
	assert.Equal(t, []string{"A-1", "B-2"}, all.ExclusiveStreams(), "nothing shared: every stream is exclusive")

	some := &SubjectFootprint{Streams: []string{"A-1", "B-2", "C-3"}, SharedStreams: []string{"B-2"}}
	assert.Equal(t, []string{"A-1", "C-3"}, some.ExclusiveStreams())
	assert.Equal(t, 1, sharedStreamCount(some))
	assert.Equal(t, 0, sharedStreamCount(nil))
}

func TestOutboxSubjectEraser_FootprintAware_ReachesLibraryWrittenRows(t *testing.T) {
	ctx := context.Background()
	store := memory.NewOutboxStore()
	// The library writes AggregateID = the producing STREAM id (never the bare subject).
	require.NoError(t, store.Schedule(ctx, []*adapters.OutboxMessage{
		{AggregateID: "User-u1", EventType: "E", Destination: "webhook:x", Payload: []byte("{}")},
		{AggregateID: "Order-o1", EventType: "E", Destination: "webhook:x", Payload: []byte("{}")},
		{AggregateID: "User-u2", EventType: "E", Destination: "webhook:x", Payload: []byte("{}")},
	}))
	fp := &SubjectFootprint{SubjectID: "u1", Streams: []string{"Order-o1", "User-u1"}}

	out, err := NewOutboxSubjectEraser(store).EraseSubject(ctx, "u1", fp)
	require.NoError(t, err)
	assert.True(t, out.FootprintAware, "the footprint path ran")
	assert.Equal(t, int64(2), out.Erased, "both of the subject's stream-keyed rows are removed")
	assert.False(t, out.Skipped)
	assert.Equal(t, 1, store.Count(), "the other subject's row survives")
}

func TestOutboxSubjectEraser_LegacyPath_WhenFootprintPurgerHidden(t *testing.T) {
	ctx := context.Background()
	inner := memory.NewOutboxStore()
	require.NoError(t, inner.Schedule(ctx, []*adapters.OutboxMessage{
		{AggregateID: "User-u1", EventType: "E", Destination: "webhook:x", Payload: []byte("{}")},
		{AggregateID: "u1", EventType: "E", Destination: "webhook:x", Payload: []byte("{}")},
	}))
	legacy := legacyOutboxStore{OutboxStore: inner, inner: inner}
	fp := &SubjectFootprint{SubjectID: "u1", Streams: []string{"User-u1"}}

	out, err := NewOutboxSubjectEraser(legacy).EraseSubject(ctx, "u1", fp)
	require.NoError(t, err)
	assert.False(t, out.FootprintAware, "only the legacy bare-id purge is available")
	assert.Equal(t, int64(1), out.Erased, "legacy equality reaches only the bare-subject-id row")
	assert.Equal(t, 1, inner.Count(), "the library-written stream-keyed row is NOT reached by the legacy path")

	// Neither interface: skipped.
	out, err = NewOutboxSubjectEraser(struct{ adapters.OutboxStore }{}).EraseSubject(ctx, "u1", fp)
	require.NoError(t, err)
	assert.True(t, out.Skipped)
}

func TestAuditSubjectEraser_FootprintAware_ActorAndTargetRows(t *testing.T) {
	ctx := context.Background()
	store := memory.NewAuditStore()
	now := time.Now()
	require.NoError(t, store.Append(ctx, &adapters.AuditEntry{ID: "1", Actor: "u1", AggregateID: "Order-o1", Timestamp: now}))   // subject performed it
	require.NoError(t, store.Append(ctx, &adapters.AuditEntry{ID: "2", Actor: "admin", AggregateID: "u1", Timestamp: now}))      // raw aggregate id (library-written)
	require.NoError(t, store.Append(ctx, &adapters.AuditEntry{ID: "3", Actor: "admin", AggregateID: "User-u1", Timestamp: now})) // stream id
	require.NoError(t, store.Append(ctx, &adapters.AuditEntry{ID: "4", Actor: "admin", AggregateID: "u2", Timestamp: now}))      // another subject
	fp := &SubjectFootprint{SubjectID: "u1", Streams: []string{"User-u1"}}

	out, err := NewAuditSubjectEraser(store).EraseSubject(ctx, "u1", fp)
	require.NoError(t, err)
	assert.True(t, out.FootprintAware)
	assert.Equal(t, int64(3), out.Erased, "target rows (u1, User-u1) via the footprint pass + the actor row via the legacy pass, no double count")
	assert.Equal(t, 1, store.Len(), "only the other subject's row remains")
}

func TestAuditSubjectEraser_FootprintOnlyStore_DoesNotReachActorRows(t *testing.T) {
	ctx := context.Background()
	inner := memory.NewAuditStore()
	require.NoError(t, inner.Append(ctx, &adapters.AuditEntry{ID: "1", Actor: "u1", AggregateID: "Order-o1", Timestamp: time.Now()}))
	require.NoError(t, inner.Append(ctx, &adapters.AuditEntry{ID: "2", Actor: "admin", AggregateID: "u1", Timestamp: time.Now()}))
	fp := &SubjectFootprint{SubjectID: "u1", Streams: []string{"User-u1"}}

	out, err := NewAuditSubjectEraser(footprintOnlyAuditStore{AuditStore: inner, inner: inner}).EraseSubject(ctx, "u1", fp)
	require.NoError(t, err)
	assert.True(t, out.FootprintAware)
	assert.Equal(t, int64(1), out.Erased, "only the target-keyed row; actor matching needs SubjectAuditPurger")
	assert.Equal(t, 1, inner.Len())
}

func TestAuditSubjectEraser_LegacyPath_WhenFootprintPurgerHidden(t *testing.T) {
	ctx := context.Background()
	inner := memory.NewAuditStore()
	require.NoError(t, inner.Append(ctx, &adapters.AuditEntry{ID: "1", Actor: "u1", Timestamp: time.Now()}))
	require.NoError(t, inner.Append(ctx, &adapters.AuditEntry{ID: "2", Actor: "admin", AggregateID: "User-u1", Timestamp: time.Now()}))
	fp := &SubjectFootprint{SubjectID: "u1", Streams: []string{"User-u1"}}

	out, err := NewAuditSubjectEraser(legacyAuditStore{AuditStore: inner, inner: inner}).EraseSubject(ctx, "u1", fp)
	require.NoError(t, err)
	assert.False(t, out.FootprintAware)
	assert.Equal(t, int64(1), out.Erased, "the legacy pass reaches the actor row only")
	assert.Equal(t, 1, inner.Len(), "the stream-keyed row survives the legacy path")
}

func TestIdempotencySubjectEraser_FootprintAware(t *testing.T) {
	ctx := context.Background()
	store := memory.NewIdempotencyStore()
	exp := time.Now().Add(time.Hour)
	require.NoError(t, store.Store(ctx, &adapters.IdempotencyRecord{Key: "k1", AggregateID: "u1", ProcessedAt: time.Now(), ExpiresAt: exp}))      // raw aggregate id
	require.NoError(t, store.Store(ctx, &adapters.IdempotencyRecord{Key: "k2", AggregateID: "User-u1", ProcessedAt: time.Now(), ExpiresAt: exp})) // stream id
	require.NoError(t, store.Store(ctx, &adapters.IdempotencyRecord{Key: "k3", AggregateID: "u2", ProcessedAt: time.Now(), ExpiresAt: exp}))
	fp := &SubjectFootprint{SubjectID: "u1", Streams: []string{"User-u1"}}

	out, err := NewIdempotencySubjectEraser(store).EraseSubject(ctx, "u1", fp)
	require.NoError(t, err)
	assert.True(t, out.FootprintAware)
	assert.Equal(t, int64(2), out.Erased)
	assert.Equal(t, 1, store.Len())

	// Legacy fallback.
	inner := memory.NewIdempotencyStore()
	require.NoError(t, inner.Store(ctx, &adapters.IdempotencyRecord{Key: "k1", AggregateID: "u1", ProcessedAt: time.Now(), ExpiresAt: exp}))
	require.NoError(t, inner.Store(ctx, &adapters.IdempotencyRecord{Key: "k2", AggregateID: "User-u1", ProcessedAt: time.Now(), ExpiresAt: exp}))
	out, err = NewIdempotencySubjectEraser(legacyIdempotencyStore{IdempotencyStore: inner, inner: inner}).EraseSubject(ctx, "u1", fp)
	require.NoError(t, err)
	assert.False(t, out.FootprintAware)
	assert.Equal(t, int64(1), out.Erased)
	assert.Equal(t, 1, inner.Len(), "the stream-keyed record survives the legacy path")
}

func TestSagaSubjectEraser_FootprintAware(t *testing.T) {
	ctx := context.Background()
	store := memory.NewSagaStore()
	require.NoError(t, store.Save(ctx, &adapters.SagaState{ID: "s1", CorrelationID: "User-u1"})) // correlated on the stream id
	require.NoError(t, store.Save(ctx, &adapters.SagaState{ID: "s2", CorrelationID: "o1"}))      // correlated on the order aggregate id
	require.NoError(t, store.Save(ctx, &adapters.SagaState{ID: "s3", CorrelationID: "u2"}))
	fp := &SubjectFootprint{SubjectID: "u1", Streams: []string{"User-u1", "Order-o1"}}

	// Default: stream-id correlation is reached; a bare, type-less aggregate id is not.
	out, err := NewSagaSubjectEraser(store).EraseSubject(ctx, "u1", fp)
	require.NoError(t, err)
	assert.True(t, out.FootprintAware)
	assert.Equal(t, int64(1), out.Erased, "only the saga correlated on the stream id")
	assert.Equal(t, 2, store.Count())
	assert.Equal(t, 0, out.SharedStreamsSkipped)

	// Opted in to derived aggregate ids: "o1" is matched too.
	derived := NewSagaSubjectEraser(store).(derivedAggregateIDsOptIn).withDerivedAggregateIDs()
	out, err = derived.EraseSubject(ctx, "u1", fp)
	require.NoError(t, err)
	assert.Equal(t, int64(1), out.Erased, "the saga correlated on the derived aggregate id")
	assert.Equal(t, 1, store.Count(), "only u2's saga remains")

	// Legacy fallback.
	inner := memory.NewSagaStore()
	require.NoError(t, inner.Save(ctx, &adapters.SagaState{ID: "s1", CorrelationID: "User-u1"}))
	require.NoError(t, inner.Save(ctx, &adapters.SagaState{ID: "s2", CorrelationID: "u1"}))
	out, err = NewSagaSubjectEraser(legacySagaStore{SagaStore: inner, inner: inner}).EraseSubject(ctx, "u1", fp)
	require.NoError(t, err)
	assert.False(t, out.FootprintAware)
	assert.Equal(t, int64(1), out.Erased)
	assert.Equal(t, 1, inner.Count())
}

func TestSubjectErasers_PurgeErrorsPropagate(t *testing.T) {
	ctx := context.Background()
	fp := &SubjectFootprint{SubjectID: "u1", Streams: []string{"User-u1"}}
	tests := []struct {
		name   string
		eraser SubjectErasable
	}{
		{"outbox footprint", NewOutboxSubjectEraser(erroringOutboxStore{})},
		{"outbox legacy", NewOutboxSubjectEraser(erroringLegacyOutboxStore{})},
		{"audit footprint", NewAuditSubjectEraser(erroringAuditStore{})},
		{"audit actor pass", NewAuditSubjectEraser(erroringAuditActorStore{inner: memory.NewAuditStore()})},
		{"saga footprint", NewSagaSubjectEraser(erroringSagaStore{})},
		{"saga legacy", NewSagaSubjectEraser(erroringLegacySagaStore{})},
		{"idempotency footprint", NewIdempotencySubjectEraser(erroringIdempotencyStore{})},
		{"idempotency legacy", NewIdempotencySubjectEraser(erroringLegacyIdempotencyStore{})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := tt.eraser.EraseSubject(ctx, "u1", fp)
			assert.ErrorIs(t, err, errStoreDown)
			assert.Equal(t, tt.eraser.ErasableName(), out.Name)
			assert.False(t, out.Skipped)
		})
	}
}

func TestSubjectErasers_CountSubjectResidual(t *testing.T) {
	ctx := context.Background()
	exp := time.Now().Add(time.Hour)
	fp := &SubjectFootprint{SubjectID: "u1", Streams: []string{"User-u1"}}

	outbox := memory.NewOutboxStore()
	require.NoError(t, outbox.Schedule(ctx, []*adapters.OutboxMessage{
		{AggregateID: "User-u1", EventType: "E", Destination: "webhook:x", Payload: []byte("{}")},
		{AggregateID: "User-u2", EventType: "E", Destination: "webhook:x", Payload: []byte("{}")},
	}))
	audit := memory.NewAuditStore()
	require.NoError(t, audit.Append(ctx, &adapters.AuditEntry{ID: "1", Actor: "u1", Timestamp: time.Now()}))
	require.NoError(t, audit.Append(ctx, &adapters.AuditEntry{ID: "2", Actor: "admin", AggregateID: "u1", Timestamp: time.Now()}))
	require.NoError(t, audit.Append(ctx, &adapters.AuditEntry{ID: "3", Actor: "admin", AggregateID: "u2", Timestamp: time.Now()}))
	idem := memory.NewIdempotencyStore()
	require.NoError(t, idem.Store(ctx, &adapters.IdempotencyRecord{Key: "k1", AggregateID: "User-u1", ProcessedAt: time.Now(), ExpiresAt: exp}))
	saga := memory.NewSagaStore()
	require.NoError(t, saga.Save(ctx, &adapters.SagaState{ID: "s1", CorrelationID: "u1"}))
	snaps := memory.NewAdapter()
	require.NoError(t, snaps.SaveSnapshot(ctx, "User-u1", 1, []byte(`{}`)))

	tests := []struct {
		name   string
		eraser SubjectErasable
		before int64
	}{
		{"outbox", NewOutboxSubjectEraser(outbox), 1},
		{"audit", NewAuditSubjectEraser(audit), 2},
		{"idempotency", NewIdempotencySubjectEraser(idem), 1},
		{"saga", NewSagaSubjectEraser(saga), 1},
		{"snapshot", NewSnapshotSubjectEraser(snaps), 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			counter, ok := tt.eraser.(SubjectResidualCounter)
			require.True(t, ok, "built-in erasers count residuals")
			n, err := counter.CountSubjectResidual(ctx, "u1", fp)
			require.NoError(t, err)
			assert.Equal(t, tt.before, n, "residual rows before erasure")

			_, err = tt.eraser.EraseSubject(ctx, "u1", fp)
			require.NoError(t, err)

			n, err = counter.CountSubjectResidual(ctx, "u1", fp)
			require.NoError(t, err)
			assert.Equal(t, int64(0), n, "clean after erasure")
		})
	}
}

func TestSubjectErasers_CountSubjectResidual_Unsupported(t *testing.T) {
	ctx := context.Background()
	fp := &SubjectFootprint{SubjectID: "u1", Streams: []string{"User-u1"}}
	tests := []struct {
		name   string
		eraser SubjectErasable
	}{
		{"outbox", NewOutboxSubjectEraser(legacyOutboxStore{inner: memory.NewOutboxStore()})},
		{"audit", NewAuditSubjectEraser(legacyAuditStore{inner: memory.NewAuditStore()})},
		{"idempotency", NewIdempotencySubjectEraser(legacyIdempotencyStore{inner: memory.NewIdempotencyStore()})},
		{"saga", NewSagaSubjectEraser(legacySagaStore{inner: memory.NewSagaStore()})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.eraser.(SubjectResidualCounter).CountSubjectResidual(ctx, "u1", fp)
			assert.ErrorIs(t, err, ErrResidualCountUnsupported)
		})
	}
}

func TestSnapshotSubjectEraser_CountSubjectResidual_EdgeCases(t *testing.T) {
	ctx := context.Background()
	a := memory.NewAdapter()
	counter := NewSnapshotSubjectEraser(a).(SubjectResidualCounter)

	n, err := counter.CountSubjectResidual(ctx, "u1", nil)
	require.NoError(t, err)
	assert.Equal(t, int64(0), n, "no footprint, nothing to count")

	n, err = counter.CountSubjectResidual(ctx, "u1", &SubjectFootprint{Streams: []string{"User-u1"}})
	require.NoError(t, err)
	assert.Equal(t, int64(0), n, "no snapshot for the stream")

	// The eraser is footprint-driven even with nothing to do.
	out, err := NewSnapshotSubjectEraser(a).EraseSubject(ctx, "u1", nil)
	require.NoError(t, err)
	assert.True(t, out.FootprintAware)
	assert.Equal(t, int64(0), out.Erased)

	_, err = NewSnapshotSubjectEraser(failingSnapshotAdapter{}).(SubjectResidualCounter).
		CountSubjectResidual(ctx, "u1", &SubjectFootprint{Streams: []string{"User-u1"}})
	assert.ErrorIs(t, err, errStoreDown)
}

func TestDataEraser_FootprintAwareSiblingStores_EndToEnd(t *testing.T) {
	ctx := context.Background()
	store, _ := newSubjectTestStore(t, "k")
	appendUser(t, ctx, store, "User-u1", "u1")
	appendUser(t, ctx, store, "Order-o1", "u1")
	appendUser(t, ctx, store, "User-u2", "u2")

	// Rows written the way the library writes them.
	outbox := memory.NewOutboxStore()
	require.NoError(t, outbox.Schedule(ctx, []*adapters.OutboxMessage{
		{AggregateID: "User-u1", EventType: "E", Destination: "webhook:x", Payload: []byte(`{"email":"u1@example.com"}`)},
		{AggregateID: "User-u2", EventType: "E", Destination: "webhook:x", Payload: []byte(`{}`)},
	}))
	audit := memory.NewAuditStore()
	require.NoError(t, audit.Append(ctx, &adapters.AuditEntry{ID: "a1", Actor: "admin", AggregateID: "u1", Timestamp: time.Now()}))
	saga := memory.NewSagaStore()
	require.NoError(t, saga.Save(ctx, &adapters.SagaState{ID: "s1", CorrelationID: "o1"}))

	// The saga is correlated on the bare order aggregate id, which only the opt-in
	// derived-id set reaches (the option is handed to the erasers whatever its position).
	var cert ErasureCertificate
	res, err := NewDataEraser(store,
		WithEraseSubjectResolver(NewSubjectResolver(store)),
		WithSubjectStore(NewOutboxSubjectEraser(outbox), NewAuditSubjectEraser(audit), NewSagaSubjectEraser(saga)),
		WithCertificateSink(func(_ context.Context, c ErasureCertificate) error { cert = c; return nil }),
		WithDerivedAggregateIDs(),
	).Erase(ctx, ErasureRequest{SubjectID: "u1"})
	require.NoError(t, err)
	assert.False(t, res.Failed(), "%v", res.Errors)
	assert.Empty(t, res.SharedStreams)

	require.Len(t, res.SubjectStores, 3)
	for _, ss := range res.SubjectStores {
		assert.True(t, ss.FootprintAware, "%s ran the footprint path", ss.Name)
		assert.Equal(t, int64(1), ss.Erased, "%s removed the subject's row", ss.Name)
		assert.Equal(t, 0, ss.SharedStreamsSkipped, "%s: no shared streams", ss.Name)
	}
	assert.Equal(t, 1, outbox.Count(), "u2's outbox row survives")
	assert.Equal(t, 0, audit.Len())
	assert.Equal(t, 0, saga.Count())

	assert.True(t, cert.Verified)
	assert.Equal(t, []string{"outbox", "audit", "saga"}, cert.StoresVerified, "every sibling store was counted clean")
	assert.Empty(t, cert.StoresUnchecked)
}

// Without WithDerivedAggregateIDs the saga correlated on the bare order aggregate id is
// out of reach — and the certificate honestly counts it as a residual.
func TestDataEraser_SiblingStores_DerivedIDsAreOptIn(t *testing.T) {
	ctx := context.Background()
	store, _ := newSubjectTestStore(t, "k")
	appendUser(t, ctx, store, "User-u1", "u1")
	appendUser(t, ctx, store, "Order-o1", "u1")
	saga := memory.NewSagaStore()
	require.NoError(t, saga.Save(ctx, &adapters.SagaState{ID: "s1", CorrelationID: "o1"}))
	require.NoError(t, saga.Save(ctx, &adapters.SagaState{ID: "s2", CorrelationID: "Order-o1"}))

	var cert ErasureCertificate
	res, err := NewDataEraser(store,
		WithEraseSubjectResolver(NewSubjectResolver(store)),
		WithSubjectStore(NewSagaSubjectEraser(saga)),
		captureCert(&cert),
	).Erase(ctx, ErasureRequest{SubjectID: "u1"})
	require.NoError(t, err)
	require.Len(t, res.SubjectStores, 1)
	assert.Equal(t, int64(1), res.SubjectStores[0].Erased, "only the stream-id-correlated saga is purged by default")
	assert.Equal(t, 1, saga.Count(), "the saga correlated on the bare aggregate id survives")
	assert.True(t, cert.Verified, "the residual counter uses the same (exclusive, non-derived) id set, so the store counts clean")
	assert.Equal(t, []string{"saga"}, cert.StoresVerified)
}

// The derived bare aggregate id is NOT type-qualified: "Order-123" and "User-123" both
// derive "123". By default the derived id is not used, so another aggregate type's rows
// — another subject's audit trail and idempotency records — survive the erasure.
func TestDataEraser_FootprintPurge_NoCrossTypeIDCollision(t *testing.T) {
	ctx := context.Background()
	store, _ := newSubjectTestStore(t, "k")
	appendUser(t, ctx, store, "Order-123", "u1") // u1 owns Order-123 ...
	appendUser(t, ctx, store, "User-123", "u2")  // ... while a different subject owns User-123

	exp := time.Now().Add(time.Hour)
	audit := memory.NewAuditStore()
	require.NoError(t, audit.Append(ctx, &adapters.AuditEntry{ID: "a1", Actor: "admin", AggregateID: "123", Timestamp: time.Now()}))       // raw id: User-123 (u2) OR Order-123 (u1) — ambiguous
	require.NoError(t, audit.Append(ctx, &adapters.AuditEntry{ID: "a2", Actor: "admin", AggregateID: "Order-123", Timestamp: time.Now()})) // stream id: unambiguous
	idem := memory.NewIdempotencyStore()
	require.NoError(t, idem.Store(ctx, &adapters.IdempotencyRecord{Key: "k1", AggregateID: "123", ProcessedAt: time.Now(), ExpiresAt: exp}))
	require.NoError(t, idem.Store(ctx, &adapters.IdempotencyRecord{Key: "k2", AggregateID: "Order-123", ProcessedAt: time.Now(), ExpiresAt: exp}))

	res, err := NewDataEraser(store,
		WithEraseSubjectResolver(NewSubjectResolver(store)),
		WithSubjectStore(NewAuditSubjectEraser(audit), NewIdempotencySubjectEraser(idem)),
	).Erase(ctx, ErasureRequest{SubjectID: "u1"})
	require.NoError(t, err)
	assert.False(t, res.Failed(), "%v", res.Errors)
	assert.Equal(t, []string{"Order-123"}, res.Streams)
	for _, ss := range res.SubjectStores {
		assert.Equal(t, int64(1), ss.Erased, "%s: only the stream-keyed row", ss.Name)
	}
	assert.Equal(t, 1, audit.Len(), "the ambiguous raw-id audit row survives")
	assert.Equal(t, 1, idem.Len(), "the ambiguous raw-id idempotency record survives (no idempotency bypass)")

	// Opting in deletes them — documented as safe only for globally unique aggregate ids.
	res, err = NewDataEraser(store,
		WithDerivedAggregateIDs(), // before WithSubjectStore: the option order does not matter
		WithEraseSubjectResolver(NewSubjectResolver(store)),
		WithSubjectStore(NewAuditSubjectEraser(audit), NewIdempotencySubjectEraser(idem)),
	).Erase(ctx, ErasureRequest{SubjectID: "u1"})
	require.NoError(t, err)
	for _, ss := range res.SubjectStores {
		assert.Equal(t, int64(1), ss.Erased, "%s: the derived-id row this time", ss.Name)
	}
	assert.Equal(t, 0, audit.Len())
	assert.Equal(t, 0, idem.Len())
}

// Rows keyed by a stream shared with another subject are left in place (a co-tenant's
// pending outbox messages, an in-flight saga); exclusive-stream rows are purged; the
// skip is counted on the outcome, noted on the result and blocks the certificate.
func TestDataEraser_FootprintPurge_SkipsSharedStreams(t *testing.T) {
	ctx := context.Background()
	store, _ := newSubjectTestStore(t, "k")
	appendUser(t, ctx, store, "User-u1", "u1")
	appendUser(t, ctx, store, "Order-o1", "u1") // buyer ...
	appendUser(t, ctx, store, "Order-o1", "u2") // ... and seller on one order stream
	appendUser(t, ctx, store, "Order-o1", "u1")

	outbox := memory.NewOutboxStore()
	require.NoError(t, outbox.Schedule(ctx, []*adapters.OutboxMessage{
		{AggregateID: "User-u1", EventType: "E", Destination: "webhook:x", Payload: []byte(`{"email":"u1@example.com"}`)},
		{AggregateID: "Order-o1", EventType: "E", Destination: "webhook:x", Payload: []byte(`{"seller":"u2"}`)}, // pending, undelivered
	}))
	saga := memory.NewSagaStore()
	require.NoError(t, saga.Save(ctx, &adapters.SagaState{ID: "s1", CorrelationID: "Order-o1"})) // in flight
	require.NoError(t, saga.Save(ctx, &adapters.SagaState{ID: "s2", CorrelationID: "User-u1"}))
	snaps := memory.NewAdapter()
	require.NoError(t, snaps.SaveSnapshot(ctx, "Order-o1", 3, []byte(`{"buyer":"u1@example.com"}`)))

	var cert ErasureCertificate
	res, err := NewDataEraser(store,
		WithEraseSubjectResolver(NewSubjectResolver(store)),
		WithSubjectStore(NewOutboxSubjectEraser(outbox), NewSagaSubjectEraser(saga), NewSnapshotSubjectEraser(snaps)),
		captureCert(&cert),
	).Erase(ctx, ErasureRequest{SubjectID: "u1"})
	require.NoError(t, err)
	assert.False(t, res.Failed(), "skipping shared streams is not a failure: %v", res.Errors)
	assert.Equal(t, []string{"Order-o1", "User-u1"}, res.Streams)
	assert.Equal(t, []string{"Order-o1"}, res.SharedStreams, "the resolver flagged the stream the seller shares")

	byName := map[string]SubjectErasureOutcome{}
	for _, ss := range res.SubjectStores {
		byName[ss.Name] = ss
	}
	assert.Equal(t, int64(1), byName["outbox"].Erased, "the exclusive stream's row is purged")
	assert.Equal(t, 1, byName["outbox"].SharedStreamsSkipped)
	assert.False(t, byName["outbox"].Skipped, "SharedStreamsSkipped is not Skipped (the store is supported)")
	assert.Equal(t, 1, outbox.Count(), "the co-tenant's pending message on the shared stream survives")
	assert.Equal(t, int64(1), byName["saga"].Erased)
	assert.Equal(t, 1, byName["saga"].SharedStreamsSkipped)
	assert.Equal(t, 1, saga.Count(), "the in-flight saga correlated on the shared stream survives")
	assert.Equal(t, int64(2), byName["snapshot"].Erased, "a snapshot is a rebuildable cache: cleared on every footprint stream, the shared one included")
	assert.Equal(t, 0, byName["snapshot"].SharedStreamsSkipped)
	snap, err := snaps.LoadSnapshot(ctx, "Order-o1")
	require.NoError(t, err)
	assert.Nil(t, snap)

	assert.True(t, notesContain(res.Notes, "1 of 2 footprint stream(s) are shared with other subjects"), "%v", res.Notes)
	for _, n := range res.Notes {
		assert.NotContains(t, n, "Order-o1", "notes carry counts and store names, never stream ids")
	}

	assert.False(t, cert.Verified, "rows for the subject may remain on the shared stream")
	assert.True(t, notesContain(cert.Notes, `sibling store "outbox" left 1 footprint stream(s) shared with other subjects untouched`), "%v", cert.Notes)
	assert.Equal(t, []string{"snapshot"}, cert.StoresVerified, "stores that skipped shared streams are not listed as verified")
	assert.Empty(t, cert.StoresUnchecked)

	// Verify counts over the same exclusive id set and says so.
	rep, err := NewDataEraser(store,
		WithEraseSubjectResolver(NewSubjectResolver(store)),
		WithSubjectStore(NewOutboxSubjectEraser(outbox), NewSagaSubjectEraser(saga)),
	).Verify(ctx, "u1")
	require.NoError(t, err)
	assert.Empty(t, rep.ResidualStores, "the surviving rows are keyed by the shared stream, which is not counted")
	assert.True(t, notesContain(rep.Notes, "1 of 2 footprint stream(s) are shared with other subjects"), "%v", rep.Notes)
}

func (failingSnapshotAdapter) DeleteSnapshot(context.Context, string) error { return errStoreDown }

func TestSnapshotSubjectEraser_DeleteFailureReportsFirstError(t *testing.T) {
	ctx := context.Background()
	fp := &SubjectFootprint{SubjectID: "u1", Streams: []string{"User-u1", "Order-o1"}}
	out, err := NewSnapshotSubjectEraser(failingSnapshotAdapter{}).EraseSubject(ctx, "u1", fp)
	assert.ErrorIs(t, err, errStoreDown, "the first failure is returned after every stream was attempted")
	assert.Equal(t, int64(0), out.Erased)
	assert.True(t, out.FootprintAware)
	assert.False(t, out.Skipped)
}
