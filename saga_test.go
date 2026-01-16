package mink

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestSagaStatus_String(t *testing.T) {
	tests := []struct {
		status   SagaStatus
		expected string
	}{
		{SagaStatusStarted, "started"},
		{SagaStatusRunning, "running"},
		{SagaStatusCompleted, "completed"},
		{SagaStatusFailed, "failed"},
		{SagaStatusCompensating, "compensating"},
		{SagaStatusCompensated, "compensated"},
		{SagaStatusCompensationFailed, "compensation_failed"},
		{SagaStatus(99), "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.status.String())
		})
	}
}

func TestSagaStatus_IsTerminal(t *testing.T) {
	tests := []struct {
		status   SagaStatus
		terminal bool
	}{
		{SagaStatusStarted, false},
		{SagaStatusRunning, false},
		{SagaStatusCompleted, true},
		{SagaStatusFailed, true},
		{SagaStatusCompensating, false},
		{SagaStatusCompensated, true},
		{SagaStatusCompensationFailed, true},
	}

	for _, tt := range tests {
		t.Run(tt.status.String(), func(t *testing.T) {
			assert.Equal(t, tt.terminal, tt.status.IsTerminal())
		})
	}
}

func TestSagaStepStatus_String(t *testing.T) {
	tests := []struct {
		status   SagaStepStatus
		expected string
	}{
		{SagaStepPending, "pending"},
		{SagaStepRunning, "running"},
		{SagaStepCompleted, "completed"},
		{SagaStepFailed, "failed"},
		{SagaStepCompensated, "compensated"},
		{SagaStepStatus(99), "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.status.String())
		})
	}
}

func TestSagaBase_NewSagaBase(t *testing.T) {
	base := NewSagaBase("saga-123", "OrderFulfillment")

	assert.Equal(t, "saga-123", base.SagaID())
	assert.Equal(t, "OrderFulfillment", base.SagaType())
	assert.Equal(t, SagaStatusStarted, base.Status())
	assert.Equal(t, 0, base.CurrentStep())
	assert.Empty(t, base.CorrelationID())
	assert.NotZero(t, base.StartedAt())
	assert.Nil(t, base.CompletedAt())
	assert.Equal(t, int64(0), base.Version())
}

func TestSagaBase_SetID(t *testing.T) {
	base := &SagaBase{}
	base.SetID("new-id")
	assert.Equal(t, "new-id", base.SagaID())
}

func TestSagaBase_SetType(t *testing.T) {
	base := &SagaBase{}
	base.SetType("NewType")
	assert.Equal(t, "NewType", base.SagaType())
}

func TestSagaBase_Status(t *testing.T) {
	base := NewSagaBase("saga-1", "Test")

	// Initial status
	assert.Equal(t, SagaStatusStarted, base.Status())

	// Set status
	base.SetStatus(SagaStatusRunning)
	assert.Equal(t, SagaStatusRunning, base.Status())
}

func TestSagaBase_CurrentStep(t *testing.T) {
	base := NewSagaBase("saga-1", "Test")

	assert.Equal(t, 0, base.CurrentStep())

	base.SetCurrentStep(5)
	assert.Equal(t, 5, base.CurrentStep())
}

func TestSagaBase_CorrelationID(t *testing.T) {
	base := NewSagaBase("saga-1", "Test")

	assert.Empty(t, base.CorrelationID())

	base.SetCorrelationID("corr-123")
	assert.Equal(t, "corr-123", base.CorrelationID())
}

func TestSagaBase_StartedAt(t *testing.T) {
	before := time.Now()
	base := NewSagaBase("saga-1", "Test")
	after := time.Now()

	assert.True(t, base.StartedAt().After(before) || base.StartedAt().Equal(before))
	assert.True(t, base.StartedAt().Before(after) || base.StartedAt().Equal(after))

	customTime := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	base.SetStartedAt(customTime)
	assert.Equal(t, customTime, base.StartedAt())
}

func TestSagaBase_CompletedAt(t *testing.T) {
	base := NewSagaBase("saga-1", "Test")

	assert.Nil(t, base.CompletedAt())

	now := time.Now()
	base.SetCompletedAt(&now)
	assert.NotNil(t, base.CompletedAt())
	assert.Equal(t, now, *base.CompletedAt())
}

func TestSagaBase_Version(t *testing.T) {
	base := NewSagaBase("saga-1", "Test")

	assert.Equal(t, int64(0), base.Version())

	base.SetVersion(5)
	assert.Equal(t, int64(5), base.Version())

	base.IncrementVersion()
	assert.Equal(t, int64(6), base.Version())
}

func TestSagaBase_Complete(t *testing.T) {
	base := NewSagaBase("saga-1", "Test")
	base.SetStatus(SagaStatusRunning)

	before := time.Now()
	base.Complete()
	after := time.Now()

	assert.Equal(t, SagaStatusCompleted, base.Status())
	assert.NotNil(t, base.CompletedAt())
	assert.True(t, base.CompletedAt().After(before) || base.CompletedAt().Equal(before))
	assert.True(t, base.CompletedAt().Before(after) || base.CompletedAt().Equal(after))
}

func TestSagaBase_Fail(t *testing.T) {
	base := NewSagaBase("saga-1", "Test")
	base.SetStatus(SagaStatusRunning)

	before := time.Now()
	base.Fail()
	after := time.Now()

	assert.Equal(t, SagaStatusFailed, base.Status())
	assert.NotNil(t, base.CompletedAt())
	assert.True(t, base.CompletedAt().After(before) || base.CompletedAt().Equal(before))
	assert.True(t, base.CompletedAt().Before(after) || base.CompletedAt().Equal(after))
}

func TestSagaBase_StartCompensation(t *testing.T) {
	base := NewSagaBase("saga-1", "Test")
	base.SetStatus(SagaStatusRunning)

	base.StartCompensation()

	assert.Equal(t, SagaStatusCompensating, base.Status())
}

func TestSagaBase_MarkCompensated(t *testing.T) {
	base := NewSagaBase("saga-1", "Test")
	base.SetStatus(SagaStatusCompensating)

	before := time.Now()
	base.MarkCompensated()
	after := time.Now()

	assert.Equal(t, SagaStatusCompensated, base.Status())
	assert.NotNil(t, base.CompletedAt())
	assert.True(t, base.CompletedAt().After(before) || base.CompletedAt().Equal(before))
	assert.True(t, base.CompletedAt().Before(after) || base.CompletedAt().Equal(after))
}

func TestSagaState_IsTerminal(t *testing.T) {
	tests := []struct {
		name     string
		status   SagaStatus
		terminal bool
	}{
		{"started", SagaStatusStarted, false},
		{"running", SagaStatusRunning, false},
		{"completed", SagaStatusCompleted, true},
		{"failed", SagaStatusFailed, true},
		{"compensating", SagaStatusCompensating, false},
		{"compensated", SagaStatusCompensated, true},
		{"compensation_failed", SagaStatusCompensationFailed, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := &SagaState{Status: tt.status}
			assert.Equal(t, tt.terminal, state.IsTerminal())
		})
	}
}

func TestSagaFailedError(t *testing.T) {
	err := NewSagaFailedError("saga-123", "OrderFulfillment", 2, "payment failed", true)

	assert.Equal(t, "saga-123", err.SagaID)
	assert.Equal(t, "OrderFulfillment", err.SagaType)
	assert.Equal(t, 2, err.FailedStep)
	assert.Equal(t, "payment failed", err.Reason)
	assert.True(t, err.Recoverable)

	assert.Contains(t, err.Error(), "saga-123")
	assert.Contains(t, err.Error(), "OrderFulfillment")
	assert.Contains(t, err.Error(), "step 2")
	assert.Contains(t, err.Error(), "payment failed")

	assert.ErrorIs(t, err, ErrSagaFailed)
}

func TestSagaNotFoundError(t *testing.T) {
	t.Run("by saga ID", func(t *testing.T) {
		err := &SagaNotFoundError{SagaID: "saga-123"}

		assert.Contains(t, err.Error(), "saga-123")
		assert.ErrorIs(t, err, ErrSagaNotFound)
	})

	t.Run("by correlation ID", func(t *testing.T) {
		err := &SagaNotFoundError{CorrelationID: "corr-456"}

		assert.Contains(t, err.Error(), "corr-456")
		assert.ErrorIs(t, err, ErrSagaNotFound)
	})
}

func TestSagaCorrelation(t *testing.T) {
	correlation := SagaCorrelation{
		SagaType:       "OrderFulfillment",
		StartingEvents: []string{"OrderCreated"},
		CorrelationIDFunc: func(event StoredEvent) string {
			return event.StreamID
		},
	}

	assert.Equal(t, "OrderFulfillment", correlation.SagaType)
	assert.Equal(t, []string{"OrderCreated"}, correlation.StartingEvents)

	event := StoredEvent{StreamID: "Order-123"}
	assert.Equal(t, "Order-123", correlation.CorrelationIDFunc(event))
}
