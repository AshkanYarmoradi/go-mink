---
layout: default
title: "ADR-004: Command Bus with Middleware Pipeline"
parent: Architecture Decision Records
nav_order: 4
---

# ADR-004: Command Bus with Middleware Pipeline

| Status | Date | Deciders |
|--------|------|----------|
| Accepted | 2024-02-15 | Core Team |

## Context

Command handling in CQRS applications involves cross-cutting concerns:

1. **Validation**: Ensure commands are valid before processing
2. **Authorization**: Check if user can execute command
3. **Logging**: Record command execution for debugging
4. **Metrics**: Track command performance
5. **Idempotency**: Prevent duplicate processing
6. **Error Handling**: Recover from panics gracefully
7. **Correlation**: Track requests across services

We need a flexible way to compose these concerns without polluting handler logic.

## Decision

We will implement a **Command Bus** with a **Middleware Pipeline** pattern, inspired by HTTP middleware in Go.

### Architecture

```
Command ──▶ Middleware 1 ──▶ Middleware 2 ──▶ ... ──▶ Handler
                │                 │                      │
                ▼                 ▼                      ▼
            (before)          (before)              (execute)
                │                 │                      │
                ◀─────────────────◀──────────────────────┘
            (after)           (after)
```

### Middleware Interface

```go
// MiddlewareFunc is a function that handles a command
type MiddlewareFunc func(ctx context.Context, cmd Command) (CommandResult, error)

// Middleware wraps a handler with additional behavior
type Middleware func(next MiddlewareFunc) MiddlewareFunc
```

### Command Bus Implementation

```go
type CommandBus struct {
    handlers   map[string]MiddlewareFunc
    middleware []Middleware
}

func (b *CommandBus) Use(m Middleware) {
    b.middleware = append(b.middleware, m)
}

func (b *CommandBus) Register(cmdType string, handler MiddlewareFunc) {
    b.handlers[cmdType] = handler
}

func (b *CommandBus) Dispatch(ctx context.Context, cmd Command) (CommandResult, error) {
    handler, ok := b.handlers[cmd.CommandType()]
    if !ok {
        return CommandResult{}, ErrHandlerNotFound
    }
    
    // Build middleware chain
    final := handler
    for i := len(b.middleware) - 1; i >= 0; i-- {
        final = b.middleware[i](final)
    }
    
    return final(ctx, cmd)
}
```

### Built-in Middleware

```go
// Validation
func ValidationMiddleware() Middleware {
    return func(next MiddlewareFunc) MiddlewareFunc {
        return func(ctx context.Context, cmd Command) (CommandResult, error) {
            if v, ok := cmd.(Validator); ok {
                if err := v.Validate(); err != nil {
                    return CommandResult{}, NewValidationError(err)
                }
            }
            return next(ctx, cmd)
        }
    }
}

// Recovery
func RecoveryMiddleware() Middleware {
    return func(next MiddlewareFunc) MiddlewareFunc {
        return func(ctx context.Context, cmd Command) (result CommandResult, err error) {
            defer func() {
                if r := recover(); r != nil {
                    err = fmt.Errorf("panic in command handler: %v", r)
                }
            }()
            return next(ctx, cmd)
        }
    }
}

// Logging
func LoggingMiddleware(logger Logger) Middleware {
    return func(next MiddlewareFunc) MiddlewareFunc {
        return func(ctx context.Context, cmd Command) (CommandResult, error) {
            start := time.Now()
            logger.Info("Dispatching command", "type", cmd.CommandType())
            
            result, err := next(ctx, cmd)
            
            logger.Info("Command completed",
                "type", cmd.CommandType(),
                "duration", time.Since(start),
                "error", err)
            
            return result, err
        }
    }
}

// Idempotency
func IdempotencyMiddleware(store IdempotencyStore) Middleware {
    return func(next MiddlewareFunc) MiddlewareFunc {
        return func(ctx context.Context, cmd Command) (CommandResult, error) {
            if idem, ok := cmd.(IdempotentCommand); ok {
                key := idem.IdempotencyKey()
                
                // Check for existing result
                if result, found := store.Get(ctx, key); found {
                    return result, nil
                }
                
                // Execute and store
                result, err := next(ctx, cmd)
                if err == nil {
                    store.Set(ctx, key, result, 24*time.Hour)
                }
                return result, err
            }
            return next(ctx, cmd)
        }
    }
}
```

## Consequences

### Positive

1. **Separation of Concerns**: Cross-cutting logic isolated from handlers
2. **Composability**: Mix and match middleware as needed
3. **Testability**: Test middleware independently
4. **Reusability**: Share middleware across commands
5. **Familiar Pattern**: Similar to HTTP middleware in Go
6. **Ordering Control**: Explicit middleware execution order

### Negative

1. **Debugging Complexity**: Stack traces span multiple functions
2. **Performance Overhead**: Each middleware adds function calls
3. **Order Sensitivity**: Middleware order matters (can be confusing)

### Neutral

1. **Configuration**: Need to configure middleware order explicitly
2. **Context Usage**: Heavy reliance on context for passing data

## Middleware Order

Recommended middleware order:

```go
bus := mink.NewCommandBus()

// 1. Recovery (outermost - catches panics)
bus.Use(mink.RecoveryMiddleware())

// 2. Metrics (track all attempts)
bus.Use(mink.MetricsMiddleware(metrics))

// 3. Tracing (create spans)
bus.Use(mink.TracingMiddleware(tracer))

// 4. Logging (log all attempts)
bus.Use(mink.LoggingMiddleware(logger))

// 5. Correlation ID (set correlation)
bus.Use(mink.CorrelationIDMiddleware(nil))

// 6. Timeout (prevent long-running commands)
bus.Use(mink.TimeoutMiddleware(30 * time.Second))

// 7. Validation (reject invalid commands early)
bus.Use(mink.ValidationMiddleware())

// 8. Authorization (check permissions)
bus.Use(AuthorizationMiddleware(authService))

// 9. Idempotency (deduplicate)
bus.Use(mink.IdempotencyMiddleware(store))

// 10. Retry (handle transient failures)
bus.Use(mink.RetryMiddleware(3, 100*time.Millisecond))
```

## Alternatives Considered

### Alternative 1: Decorator Pattern

**Description**: Wrap handlers with decorator classes.

**Pros**:
- Type-safe
- Clear interfaces

**Rejected because**:
- More verbose in Go
- Less flexible composition
- Doesn't match Go idioms

### Alternative 2: Event-Based Hooks

**Description**: Emit events before/after command execution.

**Pros**:
- Decoupled
- Can add listeners dynamically

**Rejected because**:
- Less control over execution order
- Harder to short-circuit (validation)
- Complex error handling

### Alternative 3: Aspect-Oriented Programming

**Description**: Use code generation for cross-cutting concerns.

**Rejected because**:
- Not idiomatic in Go
- Requires build-time tooling
- Magic behavior

### Alternative 4: Direct Handler Composition

**Description**: Each handler manually calls validation, logging, etc.

**Rejected because**:
- Code duplication
- Easy to forget steps
- Hard to enforce consistency

## References

- [Go HTTP Middleware Pattern](https://www.alexedwards.net/blog/making-and-using-middleware)
- [MediatR Pipeline Behaviors](https://github.com/jbogard/MediatR/wiki/Behaviors)
- [Axon Framework Interceptors](https://docs.axoniq.io/reference-guide/axon-framework/messaging-concepts/message-intercepting)
