package mink

import (
	"errors"
	"fmt"
)

// Sentinel errors for event versioning and upcasting.
var (
	// ErrUpcastFailed indicates an upcaster failed to transform event data.
	ErrUpcastFailed = errors.New("mink: upcast failed")

	// ErrSchemaVersionGap indicates a gap in the upcaster chain for an event type.
	ErrSchemaVersionGap = errors.New("mink: schema version gap")

	// ErrIncompatibleSchema indicates a schema change is not backward compatible.
	ErrIncompatibleSchema = errors.New("mink: incompatible schema")

	// ErrSchemaNotFound indicates the requested schema was not found.
	ErrSchemaNotFound = errors.New("mink: schema not found")
)

// UpcastError provides detailed information about an upcasting failure.
type UpcastError struct {
	EventType   string
	FromVersion int
	ToVersion   int
	Cause       error
}

// Error returns the error message.
func (e *UpcastError) Error() string {
	return fmt.Sprintf("mink: failed to upcast event type %q from version %d to %d: %v",
		e.EventType, e.FromVersion, e.ToVersion, e.Cause)
}

// Is reports whether this error matches the target error.
func (e *UpcastError) Is(target error) bool {
	return target == ErrUpcastFailed
}

// Unwrap returns the underlying cause for errors.Unwrap().
func (e *UpcastError) Unwrap() error {
	return e.Cause
}

// NewUpcastError creates a new UpcastError.
func NewUpcastError(eventType string, fromVersion, toVersion int, cause error) *UpcastError {
	return &UpcastError{
		EventType:   eventType,
		FromVersion: fromVersion,
		ToVersion:   toVersion,
		Cause:       cause,
	}
}

// SchemaVersionGapError provides detailed information about a gap in the upcaster chain.
type SchemaVersionGapError struct {
	EventType       string
	MissingVersion  int
	ExpectedVersion int
}

// Error returns the error message.
func (e *SchemaVersionGapError) Error() string {
	return fmt.Sprintf("mink: schema version gap for event type %q: missing version %d (expected %d)",
		e.EventType, e.MissingVersion, e.ExpectedVersion)
}

// Is reports whether this error matches the target error.
func (e *SchemaVersionGapError) Is(target error) bool {
	return target == ErrSchemaVersionGap
}

// Unwrap returns the underlying error for errors.Unwrap().
func (e *SchemaVersionGapError) Unwrap() error {
	return ErrSchemaVersionGap
}

// NewSchemaVersionGapError creates a new SchemaVersionGapError.
func NewSchemaVersionGapError(eventType string, missingVersion, expectedVersion int) *SchemaVersionGapError {
	return &SchemaVersionGapError{
		EventType:       eventType,
		MissingVersion:  missingVersion,
		ExpectedVersion: expectedVersion,
	}
}

// IncompatibleSchemaError provides detailed information about a schema incompatibility.
type IncompatibleSchemaError struct {
	EventType     string
	OldVersion    int
	NewVersion    int
	Compatibility SchemaCompatibility
	Reason        string
}

// Error returns the error message.
func (e *IncompatibleSchemaError) Error() string {
	return fmt.Sprintf("mink: incompatible schema for event type %q (v%d → v%d): %s",
		e.EventType, e.OldVersion, e.NewVersion, e.Reason)
}

// Is reports whether this error matches the target error.
func (e *IncompatibleSchemaError) Is(target error) bool {
	return target == ErrIncompatibleSchema
}

// Unwrap returns the underlying error for errors.Unwrap().
func (e *IncompatibleSchemaError) Unwrap() error {
	return ErrIncompatibleSchema
}

// NewIncompatibleSchemaError creates a new IncompatibleSchemaError.
func NewIncompatibleSchemaError(eventType string, oldVersion, newVersion int, compatibility SchemaCompatibility, reason string) *IncompatibleSchemaError {
	return &IncompatibleSchemaError{
		EventType:     eventType,
		OldVersion:    oldVersion,
		NewVersion:    newVersion,
		Compatibility: compatibility,
		Reason:        reason,
	}
}
