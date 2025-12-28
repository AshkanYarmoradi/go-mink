package mink

import (
	"context"
	"fmt"
)

// Command represents an intent to change state in the system.
// Commands are the write side of CQRS and should be validated before execution.
type Command interface {
	// CommandType returns the type identifier for this command (e.g., "CreateOrder").
	CommandType() string

	// Validate checks if the command is valid.
	// Returns nil if valid, or an error describing validation failures.
	Validate() error
}

// AggregateCommand is a command that targets a specific aggregate.
type AggregateCommand interface {
	Command

	// AggregateID returns the ID of the aggregate this command targets.
	// Returns empty string for commands that create new aggregates.
	AggregateID() string
}

// IdempotentCommand is a command that supports idempotency.
type IdempotentCommand interface {
	Command

	// IdempotencyKey returns a unique key for deduplication.
	// Commands with the same key will only be processed once.
	IdempotencyKey() string
}

// CommandBase provides a default partial implementation of Command.
// Embed this struct in your command types to get common functionality.
type CommandBase struct {
	// CommandID is an optional unique identifier for this command instance.
	CommandID string `json:"commandId,omitempty"`

	// CorrelationID links related commands and events for distributed tracing.
	CorrelationID string `json:"correlationId,omitempty"`

	// CausationID identifies the event or command that caused this command.
	CausationID string `json:"causationId,omitempty"`

	// Metadata contains arbitrary key-value pairs for application-specific data.
	Metadata map[string]string `json:"metadata,omitempty"`
}

// WithCommandID returns a copy of CommandBase with the command ID set.
func (c CommandBase) WithCommandID(id string) CommandBase {
	c.CommandID = id
	return c
}

// WithCorrelationID returns a copy of CommandBase with the correlation ID set.
func (c CommandBase) WithCorrelationID(id string) CommandBase {
	c.CorrelationID = id
	return c
}

// WithCausationID returns a copy of CommandBase with the causation ID set.
func (c CommandBase) WithCausationID(id string) CommandBase {
	c.CausationID = id
	return c
}

// WithMetadata returns a copy of CommandBase with a metadata key-value pair added.
func (c CommandBase) WithMetadata(key, value string) CommandBase {
	if c.Metadata == nil {
		c.Metadata = make(map[string]string)
	}
	newMeta := make(map[string]string, len(c.Metadata)+1)
	for k, v := range c.Metadata {
		newMeta[k] = v
	}
	newMeta[key] = value
	c.Metadata = newMeta
	return c
}

// GetMetadata returns the value for a metadata key, or empty string if not found.
func (c CommandBase) GetMetadata(key string) string {
	if c.Metadata == nil {
		return ""
	}
	return c.Metadata[key]
}

// GetCommandID returns the command ID.
func (c CommandBase) GetCommandID() string {
	return c.CommandID
}

// GetCorrelationID returns the correlation ID.
func (c CommandBase) GetCorrelationID() string {
	return c.CorrelationID
}

// GetCausationID returns the causation ID.
func (c CommandBase) GetCausationID() string {
	return c.CausationID
}

// CommandResult represents the result of command execution.
// It can contain either a successful result or an error.
type CommandResult struct {
	// Success indicates whether the command executed successfully.
	Success bool

	// AggregateID is the ID of the aggregate affected by the command.
	// For create commands, this is the ID of the newly created aggregate.
	AggregateID string

	// Version is the new version of the aggregate after command execution.
	Version int64

	// Data contains any additional result data.
	Data interface{}

	// Error contains the error if the command failed.
	Error error
}

// NewSuccessResult creates a successful CommandResult.
func NewSuccessResult(aggregateID string, version int64) CommandResult {
	return CommandResult{
		Success:     true,
		AggregateID: aggregateID,
		Version:     version,
	}
}

// NewSuccessResultWithData creates a successful CommandResult with additional data.
func NewSuccessResultWithData(aggregateID string, version int64, data interface{}) CommandResult {
	return CommandResult{
		Success:     true,
		AggregateID: aggregateID,
		Version:     version,
		Data:        data,
	}
}

// NewErrorResult creates a failed CommandResult.
func NewErrorResult(err error) CommandResult {
	return CommandResult{
		Success: false,
		Error:   err,
	}
}

// IsSuccess returns true if the command executed successfully.
func (r CommandResult) IsSuccess() bool {
	return r.Success && r.Error == nil
}

// IsError returns true if the command failed.
func (r CommandResult) IsError() bool {
	return !r.Success || r.Error != nil
}

// CommandContext carries command execution context through the middleware chain.
type CommandContext struct {
	// Context is the standard Go context.
	Context context.Context

	// Command is the command being executed.
	Command Command

	// Result is the command execution result (set by handler).
	Result CommandResult

	// Metadata contains additional context data that can be set by middleware.
	Metadata map[string]interface{}
}

// NewCommandContext creates a new CommandContext.
func NewCommandContext(ctx context.Context, cmd Command) *CommandContext {
	return &CommandContext{
		Context:  ctx,
		Command:  cmd,
		Metadata: make(map[string]interface{}),
	}
}

// Set stores a value in the context metadata.
func (c *CommandContext) Set(key string, value interface{}) {
	c.Metadata[key] = value
}

// Get retrieves a value from the context metadata.
func (c *CommandContext) Get(key string) (interface{}, bool) {
	v, ok := c.Metadata[key]
	return v, ok
}

// GetString retrieves a string value from the context metadata.
func (c *CommandContext) GetString(key string) string {
	if v, ok := c.Metadata[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// SetResult sets the command execution result.
func (c *CommandContext) SetResult(result CommandResult) {
	c.Result = result
}

// SetSuccess sets a successful result.
func (c *CommandContext) SetSuccess(aggregateID string, version int64) {
	c.Result = NewSuccessResult(aggregateID, version)
}

// SetError sets an error result.
func (c *CommandContext) SetError(err error) {
	c.Result = NewErrorResult(err)
}

// Validator provides command validation functionality.
type Validator interface {
	// Validate validates a command and returns validation errors.
	Validate(cmd Command) error
}

// ValidatorFunc is a function that implements Validator.
type ValidatorFunc func(cmd Command) error

// Validate implements Validator.
func (f ValidatorFunc) Validate(cmd Command) error {
	return f(cmd)
}

// ValidationError represents a command validation failure.
type ValidationError struct {
	// CommandType is the type of command that failed validation.
	CommandType string

	// Field is the field that failed validation (optional).
	Field string

	// Message describes the validation failure.
	Message string

	// Cause is the underlying error (optional).
	Cause error
}

// Error returns the error message.
func (e *ValidationError) Error() string {
	if e.Field != "" {
		return fmt.Sprintf("mink: validation failed for command %q field %q: %s",
			e.CommandType, e.Field, e.Message)
	}
	return fmt.Sprintf("mink: validation failed for command %q: %s",
		e.CommandType, e.Message)
}

// Is reports whether this error matches the target error.
func (e *ValidationError) Is(target error) bool {
	return target == ErrValidationFailed
}

// Unwrap returns the underlying cause for errors.Unwrap().
func (e *ValidationError) Unwrap() error {
	return e.Cause
}

// NewValidationError creates a new ValidationError.
func NewValidationError(cmdType, field, message string) *ValidationError {
	return &ValidationError{
		CommandType: cmdType,
		Field:       field,
		Message:     message,
	}
}

// NewValidationErrorWithCause creates a new ValidationError with an underlying cause.
func NewValidationErrorWithCause(cmdType, field, message string, cause error) *ValidationError {
	return &ValidationError{
		CommandType: cmdType,
		Field:       field,
		Message:     message,
		Cause:       cause,
	}
}

// MultiValidationError contains multiple validation errors.
type MultiValidationError struct {
	// CommandType is the type of command that failed validation.
	CommandType string

	// Errors contains all validation errors.
	Errors []*ValidationError
}

// Error returns the error message.
func (e *MultiValidationError) Error() string {
	return fmt.Sprintf("mink: validation failed for command %q: %d error(s)",
		e.CommandType, len(e.Errors))
}

// Is reports whether this error matches the target error.
func (e *MultiValidationError) Is(target error) bool {
	return target == ErrValidationFailed
}

// Unwrap returns the first error for errors.Unwrap().
func (e *MultiValidationError) Unwrap() error {
	if len(e.Errors) > 0 {
		return e.Errors[0]
	}
	return nil
}

// Add adds a validation error.
func (e *MultiValidationError) Add(err *ValidationError) {
	e.Errors = append(e.Errors, err)
}

// AddField adds a validation error for a specific field.
func (e *MultiValidationError) AddField(field, message string) {
	e.Add(&ValidationError{
		CommandType: e.CommandType,
		Field:       field,
		Message:     message,
	})
}

// HasErrors returns true if there are any validation errors.
func (e *MultiValidationError) HasErrors() bool {
	return len(e.Errors) > 0
}

// NewMultiValidationError creates a new MultiValidationError.
func NewMultiValidationError(cmdType string) *MultiValidationError {
	return &MultiValidationError{
		CommandType: cmdType,
		Errors:      make([]*ValidationError, 0),
	}
}
