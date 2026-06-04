package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go-mink.dev"
)

// TestSagaStore_E2E_FindByRunningExcludesCompensating verifies the store-level
// guarantee that the saga manager's re-compensation prevention relies on: the
// timeout sweep queries for Running sagas, so a saga already Compensating is never
// returned (hence never re-swept and never re-dispatched for compensation).
func TestSagaStore_E2E_FindByRunningExcludesCompensating(t *testing.T) {
	store, cleanup := setupTestSagaStore(t)
	defer cleanup()

	ctx := context.Background()
	sagaType := "RecompSaga-" + time.Now().Format("20060102150405.000000")

	running := &mink.SagaState{
		ID:            "running-" + sagaType,
		Type:          sagaType,
		CorrelationID: "corr-running",
		Status:        mink.SagaStatusRunning,
		StartedAt:     time.Now(),
	}
	compensating := &mink.SagaState{
		ID:            "compensating-" + sagaType,
		Type:          sagaType,
		CorrelationID: "corr-compensating",
		Status:        mink.SagaStatusCompensating,
		StartedAt:     time.Now(),
	}
	require.NoError(t, store.Save(ctx, running))
	require.NoError(t, store.Save(ctx, compensating))

	// The timeout sweep looks for Running sagas only.
	found, err := store.FindByType(ctx, sagaType, mink.SagaStatusRunning)
	require.NoError(t, err)

	ids := make(map[string]bool, len(found))
	for _, s := range found {
		ids[s.ID] = true
	}
	assert.True(t, ids[running.ID], "the Running saga must be returned to the sweep")
	assert.False(t, ids[compensating.ID],
		"a Compensating saga must not be returned by a Running-status query (no re-compensation)")
}
