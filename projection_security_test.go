package mink

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// panickingInlineProjection panics on Apply, to exercise the inline panic recovery.
type panickingInlineProjection struct {
	ProjectionBase
}

func (p *panickingInlineProjection) Apply(context.Context, StoredEvent) error {
	panic("boom: inline apply")
}

func TestProjectionEngine_ProcessInlineProjections_PanicRecovery(t *testing.T) {
	engine, _, _ := newTestEngineWithStore()
	logger := newTestLogger()
	engine.logger = logger

	healthy := newTestInlineProjection("InlineHealthy", "ProjectionTestEvent")
	require.NoError(t, engine.RegisterInline(healthy))
	require.NoError(t, engine.RegisterInline(&panickingInlineProjection{
		ProjectionBase: NewProjectionBase("InlinePanic", "ProjectionTestEvent"),
	}))

	event := StoredEvent{
		ID: "e1", StreamID: "Order-panic-inline", Type: "ProjectionTestEvent",
		Data: []byte(`{"orderId":"SENSITIVE-ORDER"}`), Version: 1, GlobalPosition: 7,
	}

	var err error
	require.NotPanics(t, func() {
		err = engine.ProcessInlineProjections(context.Background(), []StoredEvent{event})
	}, "a panicking inline projection must not unwind into the caller's append")
	require.Error(t, err)

	// Same contract as the async/live paths: recovered into a ProjectionError that
	// names the projection, event type and position — never the payload.
	assert.ErrorIs(t, err, ErrProjectionFailed)
	var pe *ProjectionError
	require.ErrorAs(t, err, &pe)
	assert.Equal(t, "InlinePanic", pe.ProjectionName)
	assert.Equal(t, "ProjectionTestEvent", pe.EventType)
	assert.Equal(t, uint64(7), pe.Position)
	assert.Contains(t, err.Error(), "boom: inline apply")
	assert.Contains(t, err.Error(), "inline projection InlinePanic failed")
	assert.NotContains(t, err.Error(), "SENSITIVE-ORDER")
	assert.True(t, logger.hasLogMessage("error", "Inline projection panicked"), "expected the panic to be logged")

	// The projection registered before the panicking one still received the event
	// (inline projections run in registration order and the failure is reported).
	assert.Len(t, healthy.Events(), 1)
}

func TestProjectionEngine_applyInline_NoPanicPassesThrough(t *testing.T) {
	engine, _, _ := newTestEngineWithStore()

	ok := newTestInlineProjection("InlineOK", "ProjectionTestEvent")
	require.NoError(t, engine.applyInline(context.Background(), ok, StoredEvent{Type: "ProjectionTestEvent"}))
	assert.Len(t, ok.Events(), 1)

	failing := newTestInlineProjection("InlineErr", "ProjectionTestEvent")
	failing.applyErr = assert.AnError
	err := engine.applyInline(context.Background(), failing, StoredEvent{Type: "ProjectionTestEvent"})
	assert.ErrorIs(t, err, assert.AnError)
	assert.NotErrorIs(t, err, ErrProjectionFailed, "an ordinary Apply error is returned unchanged")
}
