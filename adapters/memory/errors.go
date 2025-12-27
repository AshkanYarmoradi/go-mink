package memory

import (
	"errors"
	"fmt"

	"github.com/AshkanYarmoradi/go-mink/adapters"
)

// Sentinel errors for the memory adapter.
// These are local errors that are compatible with the adapters package errors via errors.Is().
var (
	// ErrAdapterClosed is returned when an operation is attempted on a closed adapter.
	ErrAdapterClosed = errors.New("mink/memory: adapter is closed")

	// ErrEmptyStreamID is returned when an empty stream ID is provided.
	ErrEmptyStreamID = errors.New("mink/memory: stream ID is required")

	// ErrNoEvents is returned when attempting to append zero events.
	ErrNoEvents = errors.New("mink/memory: no events to append")

	// ErrConcurrencyConflict is returned when optimistic concurrency check fails.
	ErrConcurrencyConflict = errors.New("mink/memory: concurrency conflict")

	// ErrStreamNotFound is returned when a stream does not exist.
	ErrStreamNotFound = errors.New("mink/memory: stream not found")

	// ErrInvalidVersion is returned when an invalid version is specified.
	ErrInvalidVersion = errors.New("mink/memory: invalid version")
)

// ConcurrencyError provides details about a concurrency conflict.
type ConcurrencyError struct {
	StreamID        string
	ExpectedVersion int64
	ActualVersion   int64
}

// NewConcurrencyError creates a new ConcurrencyError.
func NewConcurrencyError(streamID string, expected, actual int64) *ConcurrencyError {
	return &ConcurrencyError{
		StreamID:        streamID,
		ExpectedVersion: expected,
		ActualVersion:   actual,
	}
}

// Error implements the error interface.
func (e *ConcurrencyError) Error() string {
	return fmt.Sprintf("mink/memory: concurrency conflict on stream %q: expected version %d, got %d",
		e.StreamID, e.ExpectedVersion, e.ActualVersion)
}

// Is implements errors.Is compatibility.
// Returns true for both local ErrConcurrencyConflict and adapters.ErrConcurrencyConflict.
func (e *ConcurrencyError) Is(target error) bool {
	return target == ErrConcurrencyConflict || target == adapters.ErrConcurrencyConflict
}

// StreamNotFoundError provides details about a missing stream.
type StreamNotFoundError struct {
	StreamID string
}

// NewStreamNotFoundError creates a new StreamNotFoundError.
func NewStreamNotFoundError(streamID string) *StreamNotFoundError {
	return &StreamNotFoundError{StreamID: streamID}
}

// Error implements the error interface.
func (e *StreamNotFoundError) Error() string {
	return fmt.Sprintf("mink/memory: stream %q not found", e.StreamID)
}

// Is implements errors.Is compatibility.
// Returns true for both local ErrStreamNotFound and adapters.ErrStreamNotFound.
func (e *StreamNotFoundError) Is(target error) bool {
	return target == ErrStreamNotFound || target == adapters.ErrStreamNotFound
}
