// Package mink provides event sourcing and CQRS primitives for Go applications.
// It offers a simple, flexible API for building event-sourced systems with
// support for multiple database backends.
package mink

import (
	"errors"
	"fmt"

	"github.com/AshkanYarmoradi/go-mink/adapters"
)

// Sentinel errors for common error conditions.
// Use errors.Is() to check for these errors.
// These errors are aliases to the adapters package errors for compatibility.
var (
	// ErrStreamNotFound indicates the requested stream does not exist.
	ErrStreamNotFound = adapters.ErrStreamNotFound

	// ErrConcurrencyConflict indicates an optimistic concurrency violation.
	ErrConcurrencyConflict = adapters.ErrConcurrencyConflict

	// ErrEventNotFound indicates the requested event does not exist.
	ErrEventNotFound = errors.New("mink: event not found")

	// ErrSerializationFailed indicates event serialization/deserialization failed.
	ErrSerializationFailed = errors.New("mink: serialization failed")

	// ErrEventTypeNotRegistered indicates an unknown event type was encountered.
	ErrEventTypeNotRegistered = errors.New("mink: event type not registered")

	// ErrNilAggregate indicates a nil aggregate was passed.
	ErrNilAggregate = errors.New("mink: nil aggregate")

	// ErrNilStore indicates a nil event store was passed.
	ErrNilStore = errors.New("mink: nil event store")

	// ErrEmptyStreamID indicates an empty stream ID was provided.
	ErrEmptyStreamID = adapters.ErrEmptyStreamID

	// ErrNoEvents indicates no events were provided for append.
	ErrNoEvents = adapters.ErrNoEvents

	// ErrInvalidVersion indicates an invalid version number was provided.
	ErrInvalidVersion = adapters.ErrInvalidVersion

	// ErrAdapterClosed indicates the adapter has been closed.
	ErrAdapterClosed = adapters.ErrAdapterClosed

	// ErrSubscriptionNotSupported indicates the adapter does not support subscriptions.
	ErrSubscriptionNotSupported = errors.New("mink: adapter does not support subscriptions")

	// Command and handler related errors

	// ErrHandlerNotFound indicates no handler is registered for a command type.
	ErrHandlerNotFound = errors.New("mink: handler not found")

	// ErrValidationFailed indicates command validation failed.
	ErrValidationFailed = errors.New("mink: validation failed")

	// ErrCommandAlreadyProcessed indicates an idempotent command was already processed.
	ErrCommandAlreadyProcessed = errors.New("mink: command already processed")

	// ErrNilCommand indicates a nil command was passed.
	ErrNilCommand = errors.New("mink: nil command")

	// ErrHandlerPanicked indicates a handler panicked during execution.
	ErrHandlerPanicked = errors.New("mink: handler panicked")

	// ErrCommandBusClosed indicates the command bus has been closed.
	ErrCommandBusClosed = errors.New("mink: command bus closed")

	// Outbox-related errors

	// ErrOutboxMessageNotFound indicates the requested outbox message does not exist.
	ErrOutboxMessageNotFound = adapters.ErrOutboxMessageNotFound

	// ErrOutboxStoreClosed indicates the outbox store has been closed.
	ErrOutboxStoreClosed = errors.New("mink: outbox store closed")

	// ErrPublisherNotFound indicates no publisher is registered for a destination.
	ErrPublisherNotFound = errors.New("mink: publisher not found for destination")

	// ErrOutboxProcessorRunning indicates the outbox processor is already running.
	ErrOutboxProcessorRunning = errors.New("mink: outbox processor already running")
)

// ConcurrencyError provides detailed information about a concurrency conflict.
type ConcurrencyError struct {
	StreamID        string
	ExpectedVersion int64
	ActualVersion   int64
}

// Error returns the error message.
func (e *ConcurrencyError) Error() string {
	return fmt.Sprintf("mink: concurrency conflict on stream %q: expected version %d, actual version %d",
		e.StreamID, e.ExpectedVersion, e.ActualVersion)
}

// Is reports whether this error matches the target error.
func (e *ConcurrencyError) Is(target error) bool {
	return target == ErrConcurrencyConflict || target == adapters.ErrConcurrencyConflict
}

// Unwrap returns the underlying error for errors.Unwrap().
func (e *ConcurrencyError) Unwrap() error {
	return ErrConcurrencyConflict
}

// NewConcurrencyError creates a new ConcurrencyError.
func NewConcurrencyError(streamID string, expected, actual int64) *ConcurrencyError {
	return &ConcurrencyError{
		StreamID:        streamID,
		ExpectedVersion: expected,
		ActualVersion:   actual,
	}
}

// StreamNotFoundError provides detailed information about a missing stream.
type StreamNotFoundError struct {
	StreamID string
}

// Error returns the error message.
func (e *StreamNotFoundError) Error() string {
	return fmt.Sprintf("mink: stream %q not found", e.StreamID)
}

// Is reports whether this error matches the target error.
func (e *StreamNotFoundError) Is(target error) bool {
	return target == ErrStreamNotFound || target == adapters.ErrStreamNotFound
}

// Unwrap returns the underlying error for errors.Unwrap().
func (e *StreamNotFoundError) Unwrap() error {
	return ErrStreamNotFound
}

// NewStreamNotFoundError creates a new StreamNotFoundError.
func NewStreamNotFoundError(streamID string) *StreamNotFoundError {
	return &StreamNotFoundError{StreamID: streamID}
}

// SerializationError provides detailed information about a serialization failure.
type SerializationError struct {
	EventType string
	Operation string // "serialize" or "deserialize"
	Cause     error
}

// Error returns the error message.
func (e *SerializationError) Error() string {
	return fmt.Sprintf("mink: failed to %s event type %q: %v",
		e.Operation, e.EventType, e.Cause)
}

// Is reports whether this error matches the target error.
func (e *SerializationError) Is(target error) bool {
	return target == ErrSerializationFailed
}

// Unwrap returns the underlying cause for errors.Unwrap().
func (e *SerializationError) Unwrap() error {
	return e.Cause
}

// NewSerializationError creates a new SerializationError.
func NewSerializationError(eventType, operation string, cause error) *SerializationError {
	return &SerializationError{
		EventType: eventType,
		Operation: operation,
		Cause:     cause,
	}
}

// EventTypeNotRegisteredError provides detailed information about an unregistered event type.
type EventTypeNotRegisteredError struct {
	EventType string
}

// Error returns the error message.
func (e *EventTypeNotRegisteredError) Error() string {
	return fmt.Sprintf("mink: event type %q not registered", e.EventType)
}

// Is reports whether this error matches the target error.
func (e *EventTypeNotRegisteredError) Is(target error) bool {
	return target == ErrEventTypeNotRegistered
}

// Unwrap returns the underlying error for errors.Unwrap().
func (e *EventTypeNotRegisteredError) Unwrap() error {
	return ErrEventTypeNotRegistered
}

// NewEventTypeNotRegisteredError creates a new EventTypeNotRegisteredError.
func NewEventTypeNotRegisteredError(eventType string) *EventTypeNotRegisteredError {
	return &EventTypeNotRegisteredError{EventType: eventType}
}

// HandlerNotFoundError provides detailed information about a missing handler.
type HandlerNotFoundError struct {
	CommandType string
}

// Error returns the error message.
func (e *HandlerNotFoundError) Error() string {
	return fmt.Sprintf("mink: no handler registered for command type %q", e.CommandType)
}

// Is reports whether this error matches the target error.
func (e *HandlerNotFoundError) Is(target error) bool {
	return target == ErrHandlerNotFound
}

// Unwrap returns the underlying error for errors.Unwrap().
func (e *HandlerNotFoundError) Unwrap() error {
	return ErrHandlerNotFound
}

// NewHandlerNotFoundError creates a new HandlerNotFoundError.
func NewHandlerNotFoundError(cmdType string) *HandlerNotFoundError {
	return &HandlerNotFoundError{CommandType: cmdType}
}

// PanicError provides detailed information about a handler panic.
type PanicError struct {
	CommandType string
	Value       interface{}
	Stack       string
	// CommandData contains a sanitized JSON representation of the command for debugging.
	// Sensitive fields should be masked by the caller before setting this field.
	CommandData string
}

// Error returns the error message.
func (e *PanicError) Error() string {
	return fmt.Sprintf("mink: handler panicked while processing %q: %v", e.CommandType, e.Value)
}

// Is reports whether this error matches the target error.
func (e *PanicError) Is(target error) bool {
	return target == ErrHandlerPanicked
}

// Unwrap returns the underlying error for errors.Unwrap().
func (e *PanicError) Unwrap() error {
	return ErrHandlerPanicked
}

// NewPanicError creates a new PanicError.
func NewPanicError(cmdType string, value interface{}, stack string) *PanicError {
	return &PanicError{
		CommandType: cmdType,
		Value:       value,
		Stack:       stack,
	}
}

// NewPanicErrorWithCommand creates a new PanicError with command data for debugging.
// The commandData should be a sanitized representation of the command (sensitive fields masked).
func NewPanicErrorWithCommand(cmdType string, value interface{}, stack string, commandData string) *PanicError {
	return &PanicError{
		CommandType: cmdType,
		Value:       value,
		Stack:       stack,
		CommandData: commandData,
	}
}

// Projection-related errors

var (
	// ErrNilProjection indicates a nil projection was passed.
	ErrNilProjection = errors.New("mink: nil projection")

	// ErrEmptyProjectionName indicates a projection has no name.
	ErrEmptyProjectionName = errors.New("mink: projection name is required")

	// ErrProjectionNotFound indicates the requested projection does not exist.
	ErrProjectionNotFound = errors.New("mink: projection not found")

	// ErrProjectionAlreadyRegistered indicates a projection with the same name is already registered.
	ErrProjectionAlreadyRegistered = errors.New("mink: projection already registered")

	// ErrProjectionEngineAlreadyRunning indicates the projection engine is already running.
	ErrProjectionEngineAlreadyRunning = errors.New("mink: projection engine already running")

	// ErrProjectionEngineStopped indicates the projection engine has been stopped.
	ErrProjectionEngineStopped = errors.New("mink: projection engine stopped")

	// ErrNoCheckpointStore indicates no checkpoint store was configured.
	ErrNoCheckpointStore = errors.New("mink: checkpoint store is required")

	// ErrNotImplemented indicates a method is not implemented.
	ErrNotImplemented = errors.New("mink: not implemented")

	// ErrProjectionFailed indicates a projection failed to process an event.
	ErrProjectionFailed = errors.New("mink: projection failed")
)

// ProjectionError provides detailed information about a projection failure.
type ProjectionError struct {
	ProjectionName string
	EventType      string
	Position       uint64
	Cause          error
}

// Error returns the error message.
func (e *ProjectionError) Error() string {
	return fmt.Sprintf("mink: projection %q failed at position %d processing event %q: %v",
		e.ProjectionName, e.Position, e.EventType, e.Cause)
}

// Is reports whether this error matches the target error.
func (e *ProjectionError) Is(target error) bool {
	return target == ErrProjectionFailed
}

// Unwrap returns the underlying cause for errors.Unwrap().
func (e *ProjectionError) Unwrap() error {
	return e.Cause
}

// NewProjectionError creates a new ProjectionError.
func NewProjectionError(projectionName, eventType string, position uint64, cause error) *ProjectionError {
	return &ProjectionError{
		ProjectionName: projectionName,
		EventType:      eventType,
		Position:       position,
		Cause:          cause,
	}
}
