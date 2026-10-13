package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	mink "go-mink.dev"
	"go-mink.dev/adapters"
)

// hostileID exercises every character textArrayLiteral must escape or quote for the
// id to be matched literally: comma, braces, double quote, backslash, space and the
// bare word NULL.
const hostileID = `we,ird"{id}\x NULL`

// pgCanceledContext returns an already-cancelled context. Passing it together with
// an empty id slice proves the "(0, nil) without touching the database" contract: a
// method that issued a query would fail on the cancelled context instead.
func pgCanceledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// pgEmptyIDInputs are every shape of "no ids" the footprint methods must treat as a
// no-op.
var pgEmptyIDInputs = [][]string{nil, {}, {""}, {"", ""}}

func TestDedupeIDs(t *testing.T) {
	tests := []struct {
		name string
		ids  []string
		want []string
	}{
		{"nil", nil, nil},
		{"empty", []string{}, nil},
		{"only empties", []string{"", ""}, nil},
		{"dedupes preserving first-seen order", []string{"b", "", "a", "b", "a"}, []string{"b", "a"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, dedupeIDs(tt.ids))
		})
	}
}

func TestTextArrayLiteral(t *testing.T) {
	tests := []struct {
		name string
		ids  []string
		want string
	}{
		{"single", []string{"User-u1"}, `{"User-u1"}`},
		{"several", []string{"a", "b"}, `{"a","b"}`},
		{"empty string element is quoted, not NULL", []string{""}, `{""}`},
		{"NULL word is quoted so it stays a string", []string{"NULL"}, `{"NULL"}`},
		{"quotes, backslashes and delimiters escaped", []string{hostileID}, `{"we,ird\"{id}\\x NULL"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, textArrayLiteral(tt.ids))
		})
	}
}

func TestPostgresStores_ImplementFootprintInterfaces(t *testing.T) {
	assert.Implements(t, (*adapters.SubjectOutboxFootprintPurger)(nil), (*OutboxStore)(nil))
	assert.Implements(t, (*adapters.SubjectOutboxCounter)(nil), (*OutboxStore)(nil))
	assert.Implements(t, (*adapters.SubjectAuditFootprintPurger)(nil), (*AuditStore)(nil))
	assert.Implements(t, (*adapters.SubjectAuditCounter)(nil), (*AuditStore)(nil))
	assert.Implements(t, (*adapters.SubjectIdempotencyFootprintPurger)(nil), (*IdempotencyStore)(nil))
	assert.Implements(t, (*adapters.SubjectIdempotencyCounter)(nil), (*IdempotencyStore)(nil))
	assert.Implements(t, (*adapters.SubjectSagaFootprintPurger)(nil), (*SagaStore)(nil))
	assert.Implements(t, (*adapters.SubjectSagaCounter)(nil), (*SagaStore)(nil))
	assert.Implements(t, (*adapters.SagaCorrelationTypeFinder)(nil), (*SagaStore)(nil))
}

// ---------------------------------------------------------------------------
// OutboxStore
// ---------------------------------------------------------------------------

func TestPostgresOutboxStore_FootprintPurgeAndCount(t *testing.T) {
	store, ctx := setupOutboxStore(t)
	msg := func(aggregateID string) *adapters.OutboxMessage {
		return &adapters.OutboxMessage{AggregateID: aggregateID, EventType: "E", Destination: "webhook:x", Payload: []byte("{}"), MaxAttempts: 5}
	}
	// Library-produced rows carry the producing STREAM id, never the bare subject id.
	require.NoError(t, store.Schedule(ctx, []*adapters.OutboxMessage{msg("User-u1"), msg("Order-o1"), msg("User-u2"), msg(hostileID)}))

	// The id-equality purger keyed on the bare subject id is a silent no-op here.
	n, err := store.DeleteOutboxBySubject(ctx, "u1")
	require.NoError(t, err)
	assert.Zero(t, n)

	n, err = store.CountOutboxByAggregateIDs(ctx, []string{"User-u1", "Order-o1", "User-u1", ""})
	require.NoError(t, err)
	assert.Equal(t, int64(2), n, "duplicates must not double-count")

	// Ids are bound as data: delimiters, quotes and backslashes match literally.
	n, err = store.CountOutboxByAggregateIDs(ctx, []string{hostileID})
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)
	n, err = store.CountOutboxByAggregateIDs(ctx, []string{"User-u", "User-u22", "NULL"})
	require.NoError(t, err)
	assert.Zero(t, n, "exact match only")

	n, err = store.DeleteOutboxByAggregateIDs(ctx, []string{"User-u1", "", "Order-o1", "User-u1"})
	require.NoError(t, err)
	assert.Equal(t, int64(2), n)

	n, err = store.CountOutboxByAggregateIDs(ctx, []string{"User-u1", "Order-o1"})
	require.NoError(t, err)
	assert.Zero(t, n)
	n, err = store.CountOutboxByAggregateIDs(ctx, []string{"User-u2"})
	require.NoError(t, err)
	assert.Equal(t, int64(1), n, "another subject's row is untouched")

	n, err = store.DeleteOutboxByAggregateIDs(ctx, []string{hostileID})
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)

	canceled := pgCanceledContext()
	for _, ids := range pgEmptyIDInputs {
		n, err := store.DeleteOutboxByAggregateIDs(canceled, ids)
		require.NoError(t, err)
		assert.Zero(t, n)
		n, err = store.CountOutboxByAggregateIDs(canceled, ids)
		require.NoError(t, err)
		assert.Zero(t, n)
	}
	_, err = store.DeleteOutboxByAggregateIDs(canceled, []string{"User-u2"})
	assert.Error(t, err)
	_, err = store.CountOutboxByAggregateIDs(canceled, []string{"User-u2"})
	assert.Error(t, err)
	n, err = store.CountOutboxByAggregateIDs(ctx, []string{"User-u2"})
	require.NoError(t, err)
	assert.Equal(t, int64(1), n, "a cancelled purge must not delete")
}

// ---------------------------------------------------------------------------
// AuditStore
// ---------------------------------------------------------------------------

func TestAuditStore_FootprintPurgeAndCount(t *testing.T) {
	store := setupAuditTestStore(t)
	seedPGAudit(t, store)
	ctx := context.Background()
	// Seed: order-1 (alice, bob), order-2 (alice), order-3 (carol, alice).

	n, err := store.CountAuditBySubject(ctx, "alice", []string{"order-3", "order-3"})
	require.NoError(t, err)
	assert.Equal(t, int64(4), n, "alice's three rows plus carol's order-3 row; alice's own order-3 row counted once")

	n, err = store.CountAuditBySubject(ctx, "alice", nil)
	require.NoError(t, err)
	assert.Equal(t, int64(3), n, "actor predicate only")

	n, err = store.CountAuditBySubject(ctx, "", []string{"order-1", "order-3"})
	require.NoError(t, err)
	assert.Equal(t, int64(4), n, "aggregate predicate only")

	n, err = store.CountAuditBySubject(ctx, "nobody", []string{"order-"})
	require.NoError(t, err)
	assert.Zero(t, n, "exact match only")

	// Ids are bound as data: delimiters, quotes and backslashes match literally.
	require.NoError(t, store.Append(ctx, &adapters.AuditEntry{
		ID: "66666666-6666-6666-6666-666666666666", CommandType: "X", Actor: "dave", AggregateID: hostileID, Success: true, Timestamp: time.Now(),
	}))
	n, err = store.CountAuditBySubject(ctx, "", []string{hostileID})
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)

	n, err = store.DeleteAuditByAggregateIDs(ctx, []string{"order-1", "", "order-1", hostileID})
	require.NoError(t, err)
	assert.Equal(t, int64(3), n)
	n, err = store.CountAuditBySubject(ctx, "", []string{"order-1", hostileID})
	require.NoError(t, err)
	assert.Zero(t, n)

	// Actor-keyed rows remain for the id-equality purger (ids 3 and 5).
	n, err = store.DeleteAuditBySubject(ctx, "alice")
	require.NoError(t, err)
	assert.Equal(t, int64(2), n)

	canceled := pgCanceledContext()
	for _, ids := range pgEmptyIDInputs {
		n, err := store.DeleteAuditByAggregateIDs(canceled, ids)
		require.NoError(t, err)
		assert.Zero(t, n)
		n, err = store.CountAuditBySubject(canceled, "", ids)
		require.NoError(t, err, "both predicates empty must be a no-op that never touches the database")
		assert.Zero(t, n)
	}
	_, err = store.CountAuditBySubject(canceled, "carol", nil)
	assert.Error(t, err)
	_, err = store.CountAuditBySubject(canceled, "", []string{"order-3"})
	assert.Error(t, err)
	_, err = store.DeleteAuditByAggregateIDs(canceled, []string{"order-3"})
	assert.Error(t, err)
	n, err = store.CountAuditBySubject(ctx, "", []string{"order-3"})
	require.NoError(t, err)
	assert.Equal(t, int64(1), n, "a cancelled purge must not delete")
}

// ---------------------------------------------------------------------------
// IdempotencyStore
// ---------------------------------------------------------------------------

func TestIdempotencyStore_FootprintPurgeAndCount(t *testing.T) {
	store := setupIdempotencyTestStore(t)
	ctx := context.Background()
	exp := time.Now().Add(time.Hour)
	put := func(key, aggregateID string) {
		t.Helper()
		require.NoError(t, store.Store(ctx, &adapters.IdempotencyRecord{Key: key, CommandType: "C", AggregateID: aggregateID, ProcessedAt: time.Now(), ExpiresAt: exp}))
	}
	put("k1", "User-u1")
	put("k2", "Order-o1")
	put("k3", "User-u2")
	put("k4", hostileID)

	n, err := store.DeleteIdempotencyBySubject(ctx, "u1")
	require.NoError(t, err)
	assert.Zero(t, n, "the bare subject id never matches a stream-keyed record")

	n, err = store.CountIdempotencyByAggregateIDs(ctx, []string{"User-u1", "Order-o1", "User-u1", ""})
	require.NoError(t, err)
	assert.Equal(t, int64(2), n)
	n, err = store.CountIdempotencyByAggregateIDs(ctx, []string{hostileID})
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)

	n, err = store.DeleteIdempotencyByAggregateIDs(ctx, []string{"User-u1", "", "Order-o1"})
	require.NoError(t, err)
	assert.Equal(t, int64(2), n)

	rec, err := store.Get(ctx, "k1")
	require.NoError(t, err)
	assert.Nil(t, rec)
	rec, err = store.Get(ctx, "k3")
	require.NoError(t, err)
	require.NotNil(t, rec)
	assert.Equal(t, "User-u2", rec.AggregateID)

	n, err = store.CountIdempotencyByAggregateIDs(ctx, []string{"User-u1", "Order-o1"})
	require.NoError(t, err)
	assert.Zero(t, n)
	n, err = store.DeleteIdempotencyByAggregateIDs(ctx, []string{hostileID})
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)

	canceled := pgCanceledContext()
	for _, ids := range pgEmptyIDInputs {
		n, err := store.DeleteIdempotencyByAggregateIDs(canceled, ids)
		require.NoError(t, err)
		assert.Zero(t, n)
		n, err = store.CountIdempotencyByAggregateIDs(canceled, ids)
		require.NoError(t, err)
		assert.Zero(t, n)
	}
	_, err = store.DeleteIdempotencyByAggregateIDs(canceled, []string{"User-u2"})
	assert.Error(t, err)
	_, err = store.CountIdempotencyByAggregateIDs(canceled, []string{"User-u2"})
	assert.Error(t, err)
	rec, err = store.Get(ctx, "k3")
	require.NoError(t, err)
	assert.NotNil(t, rec, "a cancelled purge must not delete")
}

// ---------------------------------------------------------------------------
// SagaStore
// ---------------------------------------------------------------------------

func savePGSaga(t *testing.T, store *SagaStore, id, sagaType, correlationID string, startedAt time.Time) {
	t.Helper()
	require.NoError(t, store.Save(context.Background(), &mink.SagaState{
		ID: id, Type: sagaType, CorrelationID: correlationID, Status: mink.SagaStatusStarted,
		StartedAt: startedAt, Version: 0,
	}))
}

func TestSagaStore_FootprintPurgeAndCount(t *testing.T) {
	store, cleanup := setupTestSagaStore(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now()
	savePGSaga(t, store, "s1", "OrderSaga", "Order-o1", now)
	savePGSaga(t, store, "s2", "ShippingSaga", "Order-o1", now)
	savePGSaga(t, store, "s3", "OrderSaga", "Order-o2", now)
	savePGSaga(t, store, "s4", "OrderSaga", hostileID, now)

	n, err := store.CountSagasByCorrelationIDs(ctx, []string{"Order-o1", "Order-o1", ""})
	require.NoError(t, err)
	assert.Equal(t, int64(2), n)
	n, err = store.CountSagasByCorrelationIDs(ctx, []string{hostileID})
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)
	n, err = store.CountSagasByCorrelationIDs(ctx, []string{"Order-o", "Order-o11"})
	require.NoError(t, err)
	assert.Zero(t, n, "exact match only")

	n, err = store.DeleteSagasByCorrelationIDs(ctx, []string{"Order-o1", "", "Order-o1"})
	require.NoError(t, err)
	assert.Equal(t, int64(2), n)

	_, err = store.Load(ctx, "s1")
	assert.Error(t, err)
	_, err = store.Load(ctx, "s2")
	assert.Error(t, err)
	got, err := store.Load(ctx, "s3")
	require.NoError(t, err)
	assert.Equal(t, "Order-o2", got.CorrelationID)

	n, err = store.CountSagasByCorrelationIDs(ctx, []string{"Order-o1"})
	require.NoError(t, err)
	assert.Zero(t, n)
	n, err = store.DeleteSagasByCorrelationIDs(ctx, []string{hostileID})
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)

	canceled := pgCanceledContext()
	for _, ids := range pgEmptyIDInputs {
		n, err := store.DeleteSagasByCorrelationIDs(canceled, ids)
		require.NoError(t, err)
		assert.Zero(t, n)
		n, err = store.CountSagasByCorrelationIDs(canceled, ids)
		require.NoError(t, err)
		assert.Zero(t, n)
	}
	_, err = store.DeleteSagasByCorrelationIDs(canceled, []string{"Order-o2"})
	assert.Error(t, err)
	_, err = store.CountSagasByCorrelationIDs(canceled, []string{"Order-o2"})
	assert.Error(t, err)
	_, err = store.Load(ctx, "s3")
	require.NoError(t, err, "a cancelled purge must not delete")
}

func TestSagaStore_FindByCorrelationIDAndType(t *testing.T) {
	store, cleanup := setupTestSagaStore(t)
	defer cleanup()
	ctx := context.Background()
	base := time.Now().Add(-3 * time.Hour)
	savePGSaga(t, store, "order-old", "OrderSaga", "corr-1", base)
	savePGSaga(t, store, "order-new", "OrderSaga", "corr-1", base.Add(time.Hour))
	savePGSaga(t, store, "ship-1", "ShippingSaga", "corr-1", base.Add(2*time.Hour))

	// The type-agnostic finder hands the newest row, a ShippingSaga, to an
	// OrderSaga manager asking for corr-1: the type confusion being fixed.
	found, err := store.FindByCorrelationID(ctx, "corr-1")
	require.NoError(t, err)
	assert.Equal(t, "ship-1", found.ID)

	// Scoped by type, each manager gets its own newest row.
	found, err = store.FindByCorrelationIDAndType(ctx, "corr-1", "OrderSaga")
	require.NoError(t, err)
	assert.Equal(t, "order-new", found.ID)
	assert.Equal(t, "OrderSaga", found.Type)
	assert.Equal(t, "corr-1", found.CorrelationID)

	found, err = store.FindByCorrelationIDAndType(ctx, "corr-1", "ShippingSaga")
	require.NoError(t, err)
	assert.Equal(t, "ship-1", found.ID)

	// A type with no row for the correlation id is NOT satisfied by another type's row.
	_, err = store.FindByCorrelationIDAndType(ctx, "corr-1", "PaymentSaga")
	assert.ErrorIs(t, err, mink.ErrSagaNotFound)
	var nf *mink.SagaNotFoundError
	require.ErrorAs(t, err, &nf)
	assert.Equal(t, "corr-1", nf.CorrelationID)

	_, err = store.FindByCorrelationIDAndType(ctx, "corr-none", "OrderSaga")
	assert.ErrorIs(t, err, mink.ErrSagaNotFound)

	// Both identifiers are required.
	_, err = store.FindByCorrelationIDAndType(ctx, "", "OrderSaga")
	assert.Error(t, err)
	_, err = store.FindByCorrelationIDAndType(ctx, "corr-1", "")
	assert.Error(t, err)

	_, err = store.FindByCorrelationIDAndType(pgCanceledContext(), "corr-1", "OrderSaga")
	assert.Error(t, err)
}
