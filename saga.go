package mink

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/AshkanYarmoradi/go-mink/adapters"
)

// Saga-related sentinel errors.
var (
	// ErrSagaNotFound indicates the requested saga does not exist.
	ErrSagaNotFound = adapters.ErrSagaNotFound

	// ErrSagaAlreadyExists indicates a saga with the same ID already exists.
	ErrSagaAlreadyExists = adapters.ErrSagaAlreadyExists

	// ErrSagaCompleted indicates the saga has already completed.
	ErrSagaCompleted = errors.New("mink: saga already completed")

	// ErrSagaFailed indicates the saga has failed.
	ErrSagaFailed = errors.New("mink: saga failed")

	// ErrSagaCompensating indicates the saga is currently compensating.
	ErrSagaCompensating = errors.New("mink: saga is compensating")

	// ErrNoSagaHandler indicates no handler is registered for the event type.
	ErrNoSagaHandler = errors.New("mink: no saga handler for event")
)

// Type aliases for adapter types - these provide the public API
// while allowing the adapters package to define the storage-level types.
type (
	// SagaStatus represents the current status of a saga.
	SagaStatus = adapters.SagaStatus

	// SagaStepStatus represents the status of a saga step.
	SagaStepStatus = adapters.SagaStepStatus

	// SagaStep represents a single step in a saga.
	SagaStep = adapters.SagaStep

	// SagaState represents the persisted state of a saga.
	SagaState = adapters.SagaState

	// SagaStore defines the interface for saga persistence.
	SagaStore = adapters.SagaStore
)

// Re-export saga status constants from adapters.
const (
	SagaStatusStarted      = adapters.SagaStatusStarted
	SagaStatusRunning      = adapters.SagaStatusRunning
	SagaStatusCompleted    = adapters.SagaStatusCompleted
	SagaStatusFailed       = adapters.SagaStatusFailed
	SagaStatusCompensating = adapters.SagaStatusCompensating
	SagaStatusCompensated  = adapters.SagaStatusCompensated
)

// Re-export saga step status constants from adapters.
const (
	SagaStepPending     = adapters.SagaStepPending
	SagaStepRunning     = adapters.SagaStepRunning
	SagaStepCompleted   = adapters.SagaStepCompleted
	SagaStepFailed      = adapters.SagaStepFailed
	SagaStepCompensated = adapters.SagaStepCompensated
)

// Saga defines the interface for saga implementations.
// A saga coordinates long-running business processes across multiple aggregates.
type Saga interface {
	// SagaID returns the unique identifier for this saga instance.
	SagaID() string

	// SagaType returns the type of this saga (e.g., "OrderFulfillment").
	SagaType() string

	// Status returns the current status of the saga.
	Status() SagaStatus

	// SetStatus sets the saga status.
	SetStatus(status SagaStatus)

	// CurrentStep returns the current step number (0-based).
	CurrentStep() int

	// SetCurrentStep sets the current step number.
	SetCurrentStep(step int)

	// CorrelationID returns the correlation ID for this saga.
	// Used to correlate events to this saga instance.
	CorrelationID() string

	// SetCorrelationID sets the correlation ID.
	SetCorrelationID(id string)

	// HandledEvents returns the list of event types this saga handles.
	HandledEvents() []string

	// HandleEvent processes an event and returns commands to dispatch.
	// The returned commands will be executed by the saga manager.
	HandleEvent(ctx context.Context, event StoredEvent) ([]Command, error)

	// Compensate is called when the saga needs to rollback.
	// It returns compensating commands to undo previous steps.
	Compensate(ctx context.Context, failedStep int, failureReason error) ([]Command, error)

	// IsComplete returns true if the saga has completed successfully.
	IsComplete() bool

	// StartedAt returns when the saga started.
	StartedAt() time.Time

	// SetStartedAt sets when the saga started.
	SetStartedAt(t time.Time)

	// CompletedAt returns when the saga completed (nil if not completed).
	CompletedAt() *time.Time

	// SetCompletedAt sets when the saga completed.
	SetCompletedAt(t *time.Time)

	// Data returns the saga's internal state as a map.
	// This is serialized and stored in the saga store.
	Data() map[string]interface{}

	// SetData restores the saga's internal state from a map.
	SetData(data map[string]interface{})

	// Version returns the saga version for optimistic concurrency.
	Version() int64

	// SetVersion sets the saga version.
	SetVersion(v int64)

	// IncrementVersion increments the saga version.
	IncrementVersion()
}

// SagaBase provides a default partial implementation of the Saga interface.
// Embed this struct in your saga types to get default behavior.
type SagaBase struct {
	id            string
	sagaType      string
	status        SagaStatus
	currentStep   int
	correlationID string
	startedAt     time.Time
	completedAt   *time.Time
	version       int64
}

// NewSagaBase creates a new SagaBase with the given ID and type.
func NewSagaBase(id, sagaType string) SagaBase {
	return SagaBase{
		id:        id,
		sagaType:  sagaType,
		status:    SagaStatusStarted,
		startedAt: time.Now(),
	}
}

// SagaID returns the saga's unique identifier.
func (s *SagaBase) SagaID() string {
	return s.id
}

// SetID sets the saga's ID.
func (s *SagaBase) SetID(id string) {
	s.id = id
}

// SagaType returns the saga type.
func (s *SagaBase) SagaType() string {
	return s.sagaType
}

// SetType sets the saga type.
func (s *SagaBase) SetType(t string) {
	s.sagaType = t
}

// Status returns the current status.
func (s *SagaBase) Status() SagaStatus {
	return s.status
}

// SetStatus sets the saga status.
func (s *SagaBase) SetStatus(status SagaStatus) {
	s.status = status
}

// CurrentStep returns the current step number.
func (s *SagaBase) CurrentStep() int {
	return s.currentStep
}

// SetCurrentStep sets the current step number.
func (s *SagaBase) SetCurrentStep(step int) {
	s.currentStep = step
}

// CorrelationID returns the correlation ID.
func (s *SagaBase) CorrelationID() string {
	return s.correlationID
}

// SetCorrelationID sets the correlation ID.
func (s *SagaBase) SetCorrelationID(id string) {
	s.correlationID = id
}

// StartedAt returns when the saga started.
func (s *SagaBase) StartedAt() time.Time {
	return s.startedAt
}

// SetStartedAt sets when the saga started.
func (s *SagaBase) SetStartedAt(t time.Time) {
	s.startedAt = t
}

// CompletedAt returns when the saga completed.
func (s *SagaBase) CompletedAt() *time.Time {
	return s.completedAt
}

// SetCompletedAt sets when the saga completed.
func (s *SagaBase) SetCompletedAt(t *time.Time) {
	s.completedAt = t
}

// Version returns the saga version.
func (s *SagaBase) Version() int64 {
	return s.version
}

// SetVersion sets the saga version.
func (s *SagaBase) SetVersion(v int64) {
	s.version = v
}

// IncrementVersion increments the saga version.
func (s *SagaBase) IncrementVersion() {
	s.version++
}

// Complete marks the saga as completed.
func (s *SagaBase) Complete() {
	s.status = SagaStatusCompleted
	now := time.Now()
	s.completedAt = &now
}

// Fail marks the saga as failed.
func (s *SagaBase) Fail() {
	s.status = SagaStatusFailed
	now := time.Now()
	s.completedAt = &now
}

// StartCompensation marks the saga as compensating.
func (s *SagaBase) StartCompensation() {
	s.status = SagaStatusCompensating
}

// MarkCompensated marks the saga as compensated.
func (s *SagaBase) MarkCompensated() {
	s.status = SagaStatusCompensated
	now := time.Now()
	s.completedAt = &now
}

// SagaFactory creates new saga instances.
type SagaFactory func(id string) Saga

// SagaFailedError provides detailed information about a saga failure.
type SagaFailedError struct {
	SagaID      string
	SagaType    string
	FailedStep  int
	Reason      string
	Recoverable bool
}

// Error returns the error message.
func (e *SagaFailedError) Error() string {
	return fmt.Sprintf("mink: saga %q (type=%s) failed at step %d: %s",
		e.SagaID, e.SagaType, e.FailedStep, e.Reason)
}

// Is reports whether this error matches the target error.
func (e *SagaFailedError) Is(target error) bool {
	return target == ErrSagaFailed
}

// NewSagaFailedError creates a new SagaFailedError.
func NewSagaFailedError(sagaID, sagaType string, failedStep int, reason string, recoverable bool) *SagaFailedError {
	return &SagaFailedError{
		SagaID:      sagaID,
		SagaType:    sagaType,
		FailedStep:  failedStep,
		Reason:      reason,
		Recoverable: recoverable,
	}
}

// SagaNotFoundError provides detailed information about a missing saga.
type SagaNotFoundError struct {
	SagaID        string
	CorrelationID string
}

// Error returns the error message.
func (e *SagaNotFoundError) Error() string {
	if e.CorrelationID != "" {
		return fmt.Sprintf("mink: saga with correlation ID %q not found", e.CorrelationID)
	}
	return fmt.Sprintf("mink: saga %q not found", e.SagaID)
}

// Is reports whether this error matches the target error.
func (e *SagaNotFoundError) Is(target error) bool {
	return target == ErrSagaNotFound
}

// SagaCorrelation provides strategies for correlating events to sagas.
type SagaCorrelation struct {
	// SagaType is the type of saga this correlation applies to.
	SagaType string

	// EventTypes are the event types that can start this saga.
	StartingEvents []string

	// CorrelationIDFunc extracts the correlation ID from an event.
	// This is used to find existing sagas or create new ones.
	CorrelationIDFunc func(event StoredEvent) string
}
