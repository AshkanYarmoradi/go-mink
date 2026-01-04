---
layout: default
title: "ADR-009: Sentinel Errors with Typed Wrappers"
parent: Architecture Decision Records
nav_order: 9
---

# ADR-009: Sentinel Errors with Typed Wrappers

| Status | Date | Deciders |
|--------|------|----------|
| Accepted | 2024-01-20 | Core Team |

## Context

Error handling in Go requires deliberate design. go-mink needs to:

1. **Identify Error Types**: Let callers check for specific errors
2. **Provide Context**: Include relevant details (stream ID, version)
3. **Support Wrapping**: Work with `errors.Is` and `errors.As`
4. **Be Idiomatic**: Follow Go conventions

Common approaches:
- Sentinel errors (`var ErrNotFound = errors.New("not found")`)
- Typed errors (`type NotFoundError struct{}`)
- Error codes
- Multiple return values

## Decision

We will use **Sentinel Errors** for error type identification combined with **Typed Error Wrappers** for additional context.

### Sentinel Errors

Define package-level sentinel errors for each error category:

```go
// errors.go
package mink

import "errors"

// Sentinel errors for error type identification
var (
    // ErrConcurrencyConflict indicates version mismatch during append
    ErrConcurrencyConflict = errors.New("mink: concurrency conflict")
    
    // ErrStreamNotFound indicates the stream does not exist
    ErrStreamNotFound = errors.New("mink: stream not found")
    
    // ErrEventNotFound indicates the event does not exist
    ErrEventNotFound = errors.New("mink: event not found")
    
    // ErrAggregateNotFound indicates the aggregate does not exist
    ErrAggregateNotFound = errors.New("mink: aggregate not found")
    
    // ErrValidation indicates command validation failed
    ErrValidation = errors.New("mink: validation error")
    
    // ErrHandlerNotFound indicates no handler registered for command
    ErrHandlerNotFound = errors.New("mink: handler not found")
    
    // ErrProjectionFailed indicates projection processing failed
    ErrProjectionFailed = errors.New("mink: projection failed")
    
    // ErrSerializationFailed indicates event serialization failed
    ErrSerializationFailed = errors.New("mink: serialization failed")
)
```

### Typed Error Wrappers

Provide typed errors with context that wrap sentinels:

```go
// ConcurrencyError provides details about a concurrency conflict
type ConcurrencyError struct {
    StreamID        string
    ExpectedVersion int64
    ActualVersion   int64
}

func (e *ConcurrencyError) Error() string {
    return fmt.Sprintf("mink: concurrency conflict on stream %s: expected version %d, got %d",
        e.StreamID, e.ExpectedVersion, e.ActualVersion)
}

// Is implements errors.Is support
func (e *ConcurrencyError) Is(target error) bool {
    return target == ErrConcurrencyConflict
}

// ValidationError provides details about validation failures
type ValidationError struct {
    Field   string
    Message string
    Value   interface{}
}

func (e *ValidationError) Error() string {
    if e.Field != "" {
        return fmt.Sprintf("mink: validation error on field %s: %s", e.Field, e.Message)
    }
    return fmt.Sprintf("mink: validation error: %s", e.Message)
}

func (e *ValidationError) Is(target error) bool {
    return target == ErrValidation
}

// NotFoundError provides details about missing resources
type NotFoundError struct {
    ResourceType string
    ResourceID   string
}

func (e *NotFoundError) Error() string {
    return fmt.Sprintf("mink: %s not found: %s", e.ResourceType, e.ResourceID)
}

func (e *NotFoundError) Is(target error) bool {
    switch e.ResourceType {
    case "stream":
        return target == ErrStreamNotFound
    case "event":
        return target == ErrEventNotFound
    case "aggregate":
        return target == ErrAggregateNotFound
    default:
        return false
    }
}
```

### Error Checking

```go
// Using errors.Is for type checking
if errors.Is(err, mink.ErrConcurrencyConflict) {
    // Handle retry logic
}

// Using errors.As for accessing details
var concErr *mink.ConcurrencyError
if errors.As(err, &concErr) {
    log.Printf("Conflict on stream %s: expected %d, got %d",
        concErr.StreamID, concErr.ExpectedVersion, concErr.ActualVersion)
}

// Convenience functions
if mink.IsConcurrencyError(err) {
    // Same as errors.Is(err, ErrConcurrencyConflict)
}
```

### Convenience Functions

```go
// Helper functions for common checks
func IsConcurrencyError(err error) bool {
    return errors.Is(err, ErrConcurrencyConflict)
}

func IsNotFoundError(err error) bool {
    return errors.Is(err, ErrStreamNotFound) ||
           errors.Is(err, ErrEventNotFound) ||
           errors.Is(err, ErrAggregateNotFound)
}

func IsValidationError(err error) bool {
    return errors.Is(err, ErrValidation)
}

func IsRetryable(err error) bool {
    return IsConcurrencyError(err)
}
```

### Creating Errors

```go
// Factory functions for creating errors
func NewConcurrencyError(streamID string, expected, actual int64) error {
    return &ConcurrencyError{
        StreamID:        streamID,
        ExpectedVersion: expected,
        ActualVersion:   actual,
    }
}

func NewValidationError(field, message string) error {
    return &ValidationError{
        Field:   field,
        Message: message,
    }
}

func NewStreamNotFoundError(streamID string) error {
    return &NotFoundError{
        ResourceType: "stream",
        ResourceID:   streamID,
    }
}
```

## Consequences

### Positive

1. **Simple Checks**: `errors.Is(err, ErrConcurrencyConflict)` is easy
2. **Rich Details**: Typed errors provide context
3. **Idiomatic**: Follows Go 1.13+ error handling
4. **Wrapping Support**: Works with error chains
5. **Discoverability**: Sentinels are easy to find in docs

### Negative

1. **Dual System**: Both sentinels and types to maintain
2. **Is() Implementation**: Must remember to implement `Is()`
3. **Documentation**: Need to document both forms

### Neutral

1. **Migration**: Old code checking error strings still works via `Error()`
2. **Testing**: Can test both sentinel and typed error

## Error Handling Patterns

### In Handlers

```go
func (h *Handler) Handle(ctx context.Context, cmd Command) (CommandResult, error) {
    // Validation errors - return typed error
    if cmd.CustomerID == "" {
        return CommandResult{}, mink.NewValidationError("customerID", "required")
    }
    
    // Domain errors - wrap with context
    if err := aggregate.DoSomething(); err != nil {
        return CommandResult{}, fmt.Errorf("failed to process: %w", err)
    }
    
    // Infrastructure errors - wrap and return
    if err := store.SaveAggregate(ctx, aggregate); err != nil {
        return CommandResult{}, fmt.Errorf("failed to save: %w", err)
    }
    
    return mink.NewSuccessResult(aggregate.ID(), aggregate.Version()), nil
}
```

### In API Layer

```go
func handleError(w http.ResponseWriter, err error) {
    switch {
    case mink.IsConcurrencyError(err):
        http.Error(w, "Resource was modified, please retry", http.StatusConflict)
        
    case mink.IsNotFoundError(err):
        http.Error(w, "Resource not found", http.StatusNotFound)
        
    case mink.IsValidationError(err):
        http.Error(w, err.Error(), http.StatusBadRequest)
        
    default:
        log.Printf("Internal error: %v", err)
        http.Error(w, "Internal server error", http.StatusInternalServerError)
    }
}
```

## Alternatives Considered

### Alternative 1: Sentinel Errors Only

**Description**: Only use `var ErrX = errors.New("x")`.

**Rejected because**:
- No way to include context (stream ID, version)
- Callers can't get details without parsing message

### Alternative 2: Typed Errors Only

**Description**: Only use `type XError struct{}`.

**Rejected because**:
- Requires `errors.As` for checking (more verbose)
- Less discoverable in documentation
- Harder to remember struct names

### Alternative 3: Error Codes

**Description**: Use numeric or string codes.

**Rejected because**:
- Not idiomatic Go
- Requires lookup tables
- Less type safety

### Alternative 4: Multiple Return Values

**Description**: Return `(result, errType, errDetails)`.

**Rejected because**:
- Not idiomatic Go
- Complicates function signatures
- Doesn't compose well

## References

- [Go 1.13 Error Handling](https://go.dev/blog/go1.13-errors)
- [Working with Errors in Go 1.13](https://go.dev/blog/go1.13-errors)
- [Error Handling in Go](https://go.dev/doc/effective_go#errors)
- [Don't Just Check Errors, Handle Them Gracefully](https://dave.cheney.net/2016/04/27/dont-just-check-errors-handle-them-gracefully)
