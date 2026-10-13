package memory

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go-mink.dev/adapters"
)

// securityCanceledContext returns an already-cancelled context. Passing it together
// with an empty id slice proves the "(0, nil) without touching the store" contract:
// a method that reached the store would observe the cancellation and fail instead.
func securityCanceledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// emptyIDInputs are every shape of "no ids" the footprint methods must treat as a
// no-op.
var emptyIDInputs = [][]string{nil, {}, {""}, {"", ""}}

func TestIdSet(t *testing.T) {
	tests := []struct {
		name string
		ids  []string
		want []string // nil means idSet must return nil
	}{
		{"nil slice", nil, nil},
		{"empty slice", []string{}, nil},
		{"only empty strings", []string{"", ""}, nil},
		{"dedupes and drops empties", []string{"a", "", "a", "b"}, []string{"a", "b"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := idSet(tt.ids)
			if tt.want == nil {
				assert.Nil(t, got)
				return
			}
			require.Len(t, got, len(tt.want))
			for _, id := range tt.want {
				_, ok := got[id]
				assert.True(t, ok, "missing %q", id)
			}
		})
	}
}

func TestMemoryStores_ImplementFootprintInterfaces(t *testing.T) {
	assert.Implements(t, (*adapters.SubjectOutboxFootprintPurger)(nil), NewOutboxStore())
	assert.Implements(t, (*adapters.SubjectOutboxCounter)(nil), NewOutboxStore())
	assert.Implements(t, (*adapters.SubjectAuditFootprintPurger)(nil), NewAuditStore())
	assert.Implements(t, (*adapters.SubjectAuditCounter)(nil), NewAuditStore())
	assert.Implements(t, (*adapters.SubjectIdempotencyFootprintPurger)(nil), NewIdempotencyStore())
	assert.Implements(t, (*adapters.SubjectIdempotencyCounter)(nil), NewIdempotencyStore())
	assert.Implements(t, (*adapters.SubjectSagaFootprintPurger)(nil), NewSagaStore())
	assert.Implements(t, (*adapters.SubjectSagaCounter)(nil), NewSagaStore())
	assert.Implements(t, (*adapters.SagaCorrelationTypeFinder)(nil), NewSagaStore())
}

// ---------------------------------------------------------------------------
// OutboxStore
// ---------------------------------------------------------------------------

func scheduleOutboxFor(t *testing.T, s *OutboxStore, aggregateIDs ...string) {
	t.Helper()
	msgs := make([]*adapters.OutboxMessage, 0, len(aggregateIDs))
	for _, id := range aggregateIDs {
		msgs = append(msgs, &adapters.OutboxMessage{AggregateID: id, EventType: "E", Destination: "webhook:x", Payload: []byte("{}")})
	}
	require.NoError(t, s.Schedule(context.Background(), msgs))
}

func TestOutboxStore_DeleteOutboxByAggregateIDs_PurgesResolvedFootprint(t *testing.T) {
	ctx := context.Background()
	s := NewOutboxStore()
	// Library-produced rows carry the producing STREAM id, never the bare subject id.
	scheduleOutboxFor(t, s, "User-u1", "Order-o1", "User-u2")

	// The id-equality purger keyed on the bare subject id is a silent no-op here.
	n, err := s.DeleteOutboxBySubject(ctx, "u1")
	require.NoError(t, err)
	assert.Zero(t, n)
	assert.Equal(t, 3, s.Count())

	// The footprint-aware counter sees the subject's rows ...
	n, err = s.CountOutboxByAggregateIDs(ctx, []string{"User-u1", "Order-o1", "User-u1", ""})
	require.NoError(t, err)
	assert.Equal(t, int64(2), n, "duplicates must not double-count")

	// ... and the purge removes exactly them, tolerating duplicate and empty ids.
	n, err = s.DeleteOutboxByAggregateIDs(ctx, []string{"User-u1", "", "Order-o1", "User-u1"})
	require.NoError(t, err)
	assert.Equal(t, int64(2), n)
	assert.Equal(t, 1, s.Count())

	n, err = s.CountOutboxByAggregateIDs(ctx, []string{"User-u1", "Order-o1"})
	require.NoError(t, err)
	assert.Zero(t, n)

	// Another subject's row is untouched, and ids match exactly (no prefix semantics).
	n, err = s.CountOutboxByAggregateIDs(ctx, []string{"User-u2"})
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)
	n, err = s.CountOutboxByAggregateIDs(ctx, []string{"User-u", "User-u22"})
	require.NoError(t, err)
	assert.Zero(t, n)
}

func TestOutboxStore_FootprintMethods_EmptyIDsAndCanceledContext(t *testing.T) {
	s := NewOutboxStore()
	scheduleOutboxFor(t, s, "User-u1")
	canceled := securityCanceledContext()

	for _, ids := range emptyIDInputs {
		n, err := s.DeleteOutboxByAggregateIDs(canceled, ids)
		require.NoError(t, err)
		assert.Zero(t, n)
		n, err = s.CountOutboxByAggregateIDs(canceled, ids)
		require.NoError(t, err)
		assert.Zero(t, n)
	}
	assert.Equal(t, 1, s.Count())

	_, err := s.DeleteOutboxByAggregateIDs(canceled, []string{"User-u1"})
	assert.ErrorIs(t, err, context.Canceled)
	_, err = s.CountOutboxByAggregateIDs(canceled, []string{"User-u1"})
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 1, s.Count(), "a cancelled purge must not delete")
}

// ---------------------------------------------------------------------------
// AuditStore
// ---------------------------------------------------------------------------

func appendAuditFor(t *testing.T, s *AuditStore, id, actor, aggregateID string) {
	t.Helper()
	require.NoError(t, s.Append(context.Background(), &adapters.AuditEntry{
		ID: id, CommandType: "C", Actor: actor, AggregateID: aggregateID, Success: true, Timestamp: time.Now(),
	}))
}

func TestAuditStore_DeleteAuditByAggregateIDs(t *testing.T) {
	ctx := context.Background()
	s := NewAuditStore()
	appendAuditFor(t, s, "1", "u1", "User-u1")
	appendAuditFor(t, s, "2", "admin", "Order-o1")
	appendAuditFor(t, s, "3", "admin", "User-u2")
	appendAuditFor(t, s, "4", "u1", "")

	n, err := s.DeleteAuditByAggregateIDs(ctx, []string{"User-u1", "Order-o1", "Order-o1", ""})
	require.NoError(t, err)
	assert.Equal(t, int64(2), n)
	assert.Equal(t, 2, s.Len())

	// An empty id never matches an entry whose AggregateID is empty.
	n, err = s.DeleteAuditByAggregateIDs(ctx, []string{""})
	require.NoError(t, err)
	assert.Zero(t, n)
	assert.Equal(t, 2, s.Len())

	// The remaining actor-keyed row is the id-equality purger's job.
	n, err = s.DeleteAuditBySubject(ctx, "u1")
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)
	assert.Equal(t, 1, s.Len())
}

func TestAuditStore_DeleteAuditByAggregateIDs_EmptyIDsAndCanceledContext(t *testing.T) {
	s := NewAuditStore()
	appendAuditFor(t, s, "1", "u1", "User-u1")
	canceled := securityCanceledContext()

	for _, ids := range emptyIDInputs {
		n, err := s.DeleteAuditByAggregateIDs(canceled, ids)
		require.NoError(t, err)
		assert.Zero(t, n)
	}
	_, err := s.DeleteAuditByAggregateIDs(canceled, []string{"User-u1"})
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 1, s.Len())
}

func TestAuditStore_CountAuditBySubject(t *testing.T) {
	ctx := context.Background()
	s := NewAuditStore()
	appendAuditFor(t, s, "1", "u1", "User-u1")     // actor AND aggregate match
	appendAuditFor(t, s, "2", "admin", "Order-o1") // aggregate only
	appendAuditFor(t, s, "3", "u1", "Order-o9")    // actor only
	appendAuditFor(t, s, "4", "admin", "User-u2")  // neither

	tests := []struct {
		name    string
		subject string
		ids     []string
		want    int64
	}{
		{"actor OR aggregate, each row counted once", "u1", []string{"User-u1", "Order-o1"}, 3},
		{"actor predicate only", "u1", nil, 2},
		{"aggregate predicate only, duplicates tolerated", "", []string{"User-u1", "Order-o1", "Order-o1"}, 2},
		{"exact match only", "u", []string{"User-u", "Order-o"}, 0},
		{"unknown subject and ids", "nobody", []string{"Nope-1"}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n, err := s.CountAuditBySubject(ctx, tt.subject, tt.ids)
			require.NoError(t, err)
			assert.Equal(t, tt.want, n)
		})
	}

	canceled := securityCanceledContext()
	for _, ids := range emptyIDInputs {
		n, err := s.CountAuditBySubject(canceled, "", ids)
		require.NoError(t, err, "both predicates empty must be a no-op that never touches the store")
		assert.Zero(t, n)
	}
	_, err := s.CountAuditBySubject(canceled, "u1", nil)
	assert.ErrorIs(t, err, context.Canceled)
	_, err = s.CountAuditBySubject(canceled, "", []string{"User-u1"})
	assert.ErrorIs(t, err, context.Canceled)
}

// ---------------------------------------------------------------------------
// IdempotencyStore
// ---------------------------------------------------------------------------

func storeIdempotencyFor(t *testing.T, s *IdempotencyStore, key, aggregateID string) {
	t.Helper()
	require.NoError(t, s.Store(context.Background(), &adapters.IdempotencyRecord{
		Key: key, CommandType: "C", AggregateID: aggregateID, ProcessedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	}))
}

func TestIdempotencyStore_FootprintPurgeAndCount(t *testing.T) {
	ctx := context.Background()
	s := NewIdempotencyStore()
	storeIdempotencyFor(t, s, "k1", "User-u1")
	storeIdempotencyFor(t, s, "k2", "Order-o1")
	storeIdempotencyFor(t, s, "k3", "User-u2")

	n, err := s.DeleteIdempotencyBySubject(ctx, "u1")
	require.NoError(t, err)
	assert.Zero(t, n, "the bare subject id never matches a stream-keyed record")

	n, err = s.CountIdempotencyByAggregateIDs(ctx, []string{"User-u1", "Order-o1", "User-u1", ""})
	require.NoError(t, err)
	assert.Equal(t, int64(2), n)

	n, err = s.DeleteIdempotencyByAggregateIDs(ctx, []string{"User-u1", "", "Order-o1"})
	require.NoError(t, err)
	assert.Equal(t, int64(2), n)
	assert.Equal(t, 1, s.Len())

	rec, err := s.Get(ctx, "k1")
	require.NoError(t, err)
	assert.Nil(t, rec)
	rec, err = s.Get(ctx, "k3")
	require.NoError(t, err)
	require.NotNil(t, rec)
	assert.Equal(t, "User-u2", rec.AggregateID)

	n, err = s.CountIdempotencyByAggregateIDs(ctx, []string{"User-u1", "Order-o1"})
	require.NoError(t, err)
	assert.Zero(t, n)
}

func TestIdempotencyStore_FootprintMethods_EmptyIDsAndCanceledContext(t *testing.T) {
	s := NewIdempotencyStore()
	storeIdempotencyFor(t, s, "k1", "User-u1")
	canceled := securityCanceledContext()

	for _, ids := range emptyIDInputs {
		n, err := s.DeleteIdempotencyByAggregateIDs(canceled, ids)
		require.NoError(t, err)
		assert.Zero(t, n)
		n, err = s.CountIdempotencyByAggregateIDs(canceled, ids)
		require.NoError(t, err)
		assert.Zero(t, n)
	}
	_, err := s.DeleteIdempotencyByAggregateIDs(canceled, []string{"User-u1"})
	assert.ErrorIs(t, err, context.Canceled)
	_, err = s.CountIdempotencyByAggregateIDs(canceled, []string{"User-u1"})
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 1, s.Len())
}

// ---------------------------------------------------------------------------
// SagaStore
// ---------------------------------------------------------------------------

func saveSagaFor(t *testing.T, s *SagaStore, id, sagaType, correlationID string, startedAt time.Time) {
	t.Helper()
	require.NoError(t, s.Save(context.Background(), &adapters.SagaState{
		ID: id, Type: sagaType, CorrelationID: correlationID, Status: adapters.SagaStatusRunning,
		StartedAt: startedAt, Data: map[string]interface{}{"step": "one"},
	}))
}

func TestSagaStore_FootprintPurgeAndCount(t *testing.T) {
	ctx := context.Background()
	s := NewSagaStore()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	saveSagaFor(t, s, "s1", "OrderSaga", "Order-o1", base)
	saveSagaFor(t, s, "s2", "ShippingSaga", "Order-o1", base)
	saveSagaFor(t, s, "s3", "OrderSaga", "Order-o2", base)

	n, err := s.CountSagasByCorrelationIDs(ctx, []string{"Order-o1", "Order-o1", ""})
	require.NoError(t, err)
	assert.Equal(t, int64(2), n)

	n, err = s.DeleteSagasByCorrelationIDs(ctx, []string{"Order-o1", "", "Order-o1"})
	require.NoError(t, err)
	assert.Equal(t, int64(2), n)

	_, err = s.Load(ctx, "s1")
	assert.Error(t, err)
	_, err = s.Load(ctx, "s2")
	assert.Error(t, err)
	got, err := s.Load(ctx, "s3")
	require.NoError(t, err)
	assert.Equal(t, "Order-o2", got.CorrelationID)

	n, err = s.CountSagasByCorrelationIDs(ctx, []string{"Order-o1"})
	require.NoError(t, err)
	assert.Zero(t, n)
}

func TestSagaStore_FootprintMethods_EmptyIDsAndCanceledContext(t *testing.T) {
	s := NewSagaStore()
	saveSagaFor(t, s, "s1", "OrderSaga", "Order-o1", time.Now())
	canceled := securityCanceledContext()

	for _, ids := range emptyIDInputs {
		n, err := s.DeleteSagasByCorrelationIDs(canceled, ids)
		require.NoError(t, err)
		assert.Zero(t, n)
		n, err = s.CountSagasByCorrelationIDs(canceled, ids)
		require.NoError(t, err)
		assert.Zero(t, n)
	}
	_, err := s.DeleteSagasByCorrelationIDs(canceled, []string{"Order-o1"})
	assert.ErrorIs(t, err, context.Canceled)
	_, err = s.CountSagasByCorrelationIDs(canceled, []string{"Order-o1"})
	assert.ErrorIs(t, err, context.Canceled)
	_, err = s.Load(context.Background(), "s1")
	require.NoError(t, err, "a cancelled purge must not delete")
}

func TestSagaStore_FindByCorrelationIDAndType(t *testing.T) {
	ctx := context.Background()
	s := NewSagaStore()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	saveSagaFor(t, s, "order-old", "OrderSaga", "corr-1", base)
	saveSagaFor(t, s, "order-new", "OrderSaga", "corr-1", base.Add(time.Hour))
	saveSagaFor(t, s, "ship-1", "ShippingSaga", "corr-1", base.Add(2*time.Hour))

	// The type-agnostic finder hands the newest row, a ShippingSaga, to an
	// OrderSaga manager asking for corr-1: the type confusion being fixed.
	found, err := s.FindByCorrelationID(ctx, "corr-1")
	require.NoError(t, err)
	assert.Equal(t, "ship-1", found.ID)

	// Scoped by type, each manager gets its own newest row.
	found, err = s.FindByCorrelationIDAndType(ctx, "corr-1", "OrderSaga")
	require.NoError(t, err)
	assert.Equal(t, "order-new", found.ID)
	assert.Equal(t, "OrderSaga", found.Type)

	found, err = s.FindByCorrelationIDAndType(ctx, "corr-1", "ShippingSaga")
	require.NoError(t, err)
	assert.Equal(t, "ship-1", found.ID)

	// A type with no row for the correlation id is NOT satisfied by another type's row.
	_, err = s.FindByCorrelationIDAndType(ctx, "corr-1", "PaymentSaga")
	assert.ErrorIs(t, err, adapters.ErrSagaNotFound)
	var nf *adapters.SagaNotFoundError
	require.ErrorAs(t, err, &nf)
	assert.Equal(t, "corr-1", nf.CorrelationID)

	_, err = s.FindByCorrelationIDAndType(ctx, "corr-none", "OrderSaga")
	assert.ErrorIs(t, err, adapters.ErrSagaNotFound)

	// Both identifiers are required.
	_, err = s.FindByCorrelationIDAndType(ctx, "", "OrderSaga")
	assert.ErrorIs(t, err, adapters.ErrEmptyStreamID)
	_, err = s.FindByCorrelationIDAndType(ctx, "corr-1", "")
	assert.ErrorIs(t, err, adapters.ErrEmptyStreamID)

	// The result is a detached copy: mutating it cannot reach the store.
	found, err = s.FindByCorrelationIDAndType(ctx, "corr-1", "OrderSaga")
	require.NoError(t, err)
	found.Data["step"] = "tampered"
	again, err := s.FindByCorrelationIDAndType(ctx, "corr-1", "OrderSaga")
	require.NoError(t, err)
	assert.Equal(t, "one", again.Data["step"])

	_, err = s.FindByCorrelationIDAndType(securityCanceledContext(), "corr-1", "OrderSaga")
	assert.ErrorIs(t, err, context.Canceled)
}
