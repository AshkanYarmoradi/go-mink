package mink

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go-mink.dev/adapters"
	"go-mink.dev/adapters/memory"
)

func TestAuditSubjectEraser(t *testing.T) {
	ctx := context.Background()
	store := memory.NewAuditStore()
	require.NoError(t, store.Append(ctx, &adapters.AuditEntry{ID: "1", Actor: "u1", Timestamp: time.Now()}))
	require.NoError(t, store.Append(ctx, &adapters.AuditEntry{ID: "2", AggregateID: "u1", Timestamp: time.Now()}))
	require.NoError(t, store.Append(ctx, &adapters.AuditEntry{ID: "3", Actor: "u2", Timestamp: time.Now()}))

	out, err := NewAuditSubjectEraser(store).EraseSubject(ctx, "u1", nil)
	require.NoError(t, err)
	assert.Equal(t, "audit", out.Name)
	assert.Equal(t, 2, out.Erased) // actor==u1 AND aggregate_id==u1
	assert.False(t, out.Skipped)
	assert.Equal(t, 1, store.Len(), "only u2's entry remains")
}

// auditNoPurger is an AuditStore that does NOT implement SubjectAuditPurger.
type auditNoPurger struct{}

func (auditNoPurger) Append(context.Context, *adapters.AuditEntry) error { return nil }
func (auditNoPurger) Find(context.Context, adapters.AuditQuery) ([]*adapters.AuditEntry, error) {
	return nil, nil
}
func (auditNoPurger) Count(context.Context, adapters.AuditQuery) (int64, error) { return 0, nil }
func (auditNoPurger) Cleanup(context.Context, time.Duration) (int64, error)     { return 0, nil }
func (auditNoPurger) Initialize(context.Context) error                          { return nil }
func (auditNoPurger) Close() error                                              { return nil }

func TestAuditSubjectEraser_SkipsWhenUnsupported(t *testing.T) {
	out, err := NewAuditSubjectEraser(auditNoPurger{}).EraseSubject(context.Background(), "u1", nil)
	require.NoError(t, err)
	assert.True(t, out.Skipped)
	assert.Equal(t, 0, out.Erased)
}

func TestSagaSubjectEraser(t *testing.T) {
	ctx := context.Background()
	store := memory.NewSagaStore()
	require.NoError(t, store.Save(ctx, &adapters.SagaState{ID: "s1", CorrelationID: "u1"}))
	require.NoError(t, store.Save(ctx, &adapters.SagaState{ID: "s2", CorrelationID: "u2"}))

	out, err := NewSagaSubjectEraser(store).EraseSubject(ctx, "u1", nil)
	require.NoError(t, err)
	assert.Equal(t, "saga", out.Name)
	assert.Equal(t, 1, out.Erased)

	_, err = store.Load(ctx, "s1")
	assert.Error(t, err, "u1's saga is gone")
	_, err = store.Load(ctx, "s2")
	assert.NoError(t, err, "u2's saga remains")
}

func TestSnapshotSubjectEraser(t *testing.T) {
	ctx := context.Background()
	a := memory.NewAdapter()
	require.NoError(t, a.SaveSnapshot(ctx, "User-u1", 1, []byte(`{"email":"alice@example.com"}`)))
	require.NoError(t, a.SaveSnapshot(ctx, "Order-o9", 1, []byte(`{}`)))

	fp := &SubjectFootprint{SubjectID: "u1", Streams: []string{"User-u1"}}
	out, err := NewSnapshotSubjectEraser(a).EraseSubject(ctx, "u1", fp)
	require.NoError(t, err)
	assert.Equal(t, 1, out.Erased)

	snap, err := a.LoadSnapshot(ctx, "User-u1")
	require.NoError(t, err)
	assert.Nil(t, snap, "the subject's snapshot (plaintext state) is deleted")

	other, err := a.LoadSnapshot(ctx, "Order-o9")
	require.NoError(t, err)
	assert.NotNil(t, other, "unrelated snapshots survive")
}

func TestDataEraser_WithAuditSubjectStore(t *testing.T) {
	ctx := context.Background()
	store, _ := newSubjectTestStore(t, "k")
	appendUser(t, ctx, store, "User-u1", "u1")

	audit := memory.NewAuditStore()
	require.NoError(t, audit.Append(ctx, &adapters.AuditEntry{ID: "a1", Actor: "u1", Error: "email alice@example.com invalid", Timestamp: time.Now()}))

	res, err := NewDataEraser(store,
		WithEraseSubjectResolver(NewSubjectResolver(store)),
		WithSubjectStore(NewAuditSubjectEraser(audit)),
	).Erase(ctx, ErasureRequest{SubjectID: "u1"})
	require.NoError(t, err)

	require.Len(t, res.SubjectStores, 1)
	assert.Equal(t, "audit", res.SubjectStores[0].Name)
	assert.Equal(t, 1, res.SubjectStores[0].Erased)
	assert.Equal(t, 0, audit.Len(), "the subject's PII-bearing audit row is erased alongside the events")
}
