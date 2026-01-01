// Package adapters provides interfaces and shared utilities for event store backends.
package adapters

import (
	"fmt"
	"strings"
)

// Version constants for optimistic concurrency control.
// These constants define special version values used in Append operations.
const (
	// AnyVersion skips version checking. Use when you don't care about concurrent modifications.
	AnyVersion int64 = -1

	// NoStream requires the stream to not exist. Use for creating new streams.
	NoStream int64 = 0

	// StreamExists requires the stream to exist. Use when you expect to append to an existing stream.
	StreamExists int64 = -2
)

// ExtractCategory extracts the category from a stream ID.
// Stream IDs are expected to follow the format "Category-ID" (e.g., "Order-123").
// The category is the portion before the first hyphen.
//
// Behavior:
//   - "Order-123" returns "Order"
//   - "User-abc-def" returns "User" (only splits on first hyphen)
//   - "NoHyphen" returns "NoHyphen" (entire ID if no hyphen)
//   - "" returns "" (empty string for empty input)
func ExtractCategory(streamID string) string {
	if streamID == "" {
		return ""
	}
	parts := strings.SplitN(streamID, "-", 2)
	return parts[0]
}

// ConcurrencyError provides details about a concurrency conflict.
// It is returned when an optimistic concurrency check fails during Append operations.
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
	return fmt.Sprintf("mink: concurrency conflict on stream %q: expected version %d, got %d",
		e.StreamID, e.ExpectedVersion, e.ActualVersion)
}

// Is implements errors.Is compatibility.
// Returns true when compared with ErrConcurrencyConflict.
func (e *ConcurrencyError) Is(target error) bool {
	return target == ErrConcurrencyConflict
}

// StreamNotFoundError provides details about a missing stream.
// It is returned when an operation requires an existing stream that doesn't exist.
type StreamNotFoundError struct {
	StreamID string
}

// NewStreamNotFoundError creates a new StreamNotFoundError.
func NewStreamNotFoundError(streamID string) *StreamNotFoundError {
	return &StreamNotFoundError{StreamID: streamID}
}

// Error implements the error interface.
func (e *StreamNotFoundError) Error() string {
	return fmt.Sprintf("mink: stream %q not found", e.StreamID)
}

// Is implements errors.Is compatibility.
// Returns true when compared with ErrStreamNotFound.
func (e *StreamNotFoundError) Is(target error) bool {
	return target == ErrStreamNotFound
}

// CheckVersion validates the expected version against the current version.
// This implements the optimistic concurrency control logic shared by all adapters.
//
// Parameters:
//   - streamID: The stream identifier (used for error messages)
//   - expected: The expected version (can be AnyVersion, NoStream, StreamExists, or a positive version)
//   - current: The current version of the stream
//   - exists: Whether the stream currently exists
//
// Returns nil if the version check passes, or an appropriate error otherwise.
func CheckVersion(streamID string, expected, current int64, exists bool) error {
	switch expected {
	case AnyVersion:
		return nil
	case NoStream:
		if exists {
			return NewConcurrencyError(streamID, expected, current)
		}
		return nil
	case StreamExists:
		if !exists {
			return NewStreamNotFoundError(streamID)
		}
		return nil
	default:
		if expected < 0 {
			return ErrInvalidVersion
		}
		if current != expected {
			return NewConcurrencyError(streamID, expected, current)
		}
		return nil
	}
}

// CopyIdempotencyRecord creates a deep copy of an IdempotencyRecord.
// This is useful to avoid external mutations of stored records.
func CopyIdempotencyRecord(record *IdempotencyRecord) *IdempotencyRecord {
	if record == nil {
		return nil
	}
	return &IdempotencyRecord{
		Key:         record.Key,
		CommandType: record.CommandType,
		AggregateID: record.AggregateID,
		Version:     record.Version,
		Response:    record.Response,
		Success:     record.Success,
		Error:       record.Error,
		ProcessedAt: record.ProcessedAt,
		ExpiresAt:   record.ExpiresAt,
	}
}

// DefaultLimit returns a default limit value if the provided limit is invalid.
// Used for pagination in LoadFromPosition and similar methods.
func DefaultLimit(limit, defaultValue int) int {
	if limit <= 0 {
		return defaultValue
	}
	return limit
}
