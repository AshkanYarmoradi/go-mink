# Part 6: Middleware and Cross-Cutting Concerns

*This is Part 6 of an 8-part series on Event Sourcing and CQRS with Go. In this post, we'll explore go-mink's middleware system for handling logging, validation, retries, idempotency, and other cross-cutting concerns.*

---

## What is Middleware?

Middleware wraps command execution, allowing you to add behavior before and after handlers run. Think of it as an onion—each middleware layer wraps the next:

```
┌─────────────────────────────────────────────────────────────┐
│ Logging Middleware                                          │
│  ┌───────────────────────────────────────────────────────┐  │
│  │ Validation Middleware                                 │  │
│  │  ┌─────────────────────────────────────────────────┐  │  │
│  │  │ Recovery Middleware                             │  │  │
│  │  │  ┌───────────────────────────────────────────┐  │  │  │
│  │  │  │ Metrics Middleware                        │  │  │  │
│  │  │  │  ┌─────────────────────────────────────┐  │  │  │  │
│  │  │  │  │ Correlation Middleware              │  │  │  │  │
│  │  │  │  │  ┌───────────────────────────────┐  │  │  │  │  │
│  │  │  │  │  │ Idempotency Middleware        │  │  │  │  │  │
│  │  │  │  │  │  ┌─────────────────────────┐  │  │  │  │  │  │
│  │  │  │  │  │  │     Handler            │  │  │  │  │  │  │
│  │  │  │  │  │  └─────────────────────────┘  │  │  │  │  │  │
│  │  │  │  │  └───────────────────────────────┘  │  │  │  │  │
│  │  │  │  └─────────────────────────────────────┘  │  │  │  │
│  │  │  └───────────────────────────────────────────┘  │  │  │
│  │  └─────────────────────────────────────────────────┘  │  │
│  └───────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────┘
```

Commands flow inward through each layer, and responses flow outward.

---

## Middleware Signature

go-mink uses a functional middleware pattern:

```go
type MiddlewareFunc func(ctx context.Context, cmd Command) (CommandResult, error)
type Middleware func(next MiddlewareFunc) MiddlewareFunc
```

A middleware:
1. Receives the `next` handler in the chain
2. Returns a new handler that wraps it
3. Can do work before/after calling `next`

### Basic Middleware Structure

```go
func MyMiddleware() mink.Middleware {
    return func(next mink.MiddlewareFunc) mink.MiddlewareFunc {
        return func(ctx context.Context, cmd mink.Command) (mink.CommandResult, error) {
            // Before: runs before the handler
            fmt.Printf("About to handle %s\n", cmd.CommandType())

            // Call the next middleware/handler
            result, err := next(ctx, cmd)

            // After: runs after the handler
            fmt.Printf("Finished handling %s\n", cmd.CommandType())

            return result, err
        }
    }
}
```

---

## Registering Middleware

Add middleware when creating the command bus:

```go
bus := mink.NewCommandBus(
    mink.WithHandlerRegistry(registry),
    mink.WithMiddleware(
        mink.LoggingMiddleware(logger),     // Outermost
        mink.ValidationMiddleware(),
        mink.RecoveryMiddleware(),
        mink.MetricsMiddleware(collector),
        mink.CorrelationIDMiddleware(nil),
        mink.IdempotencyMiddleware(config), // Innermost (closest to handler)
    ),
)
```

Middleware executes in the order listed. The first middleware is outermost (runs first on the way in, last on the way out).

---

## Built-in Middleware

### Validation Middleware

Validates commands before execution:

```go
bus := mink.NewCommandBus(
    mink.WithMiddleware(
        mink.ValidationMiddleware(),
    ),
)

// Commands must implement Validate()
type CreateOrderCommand struct {
    CustomerID string
}

func (c CreateOrderCommand) Validate() error {
    if c.CustomerID == "" {
        return errors.New("customer ID is required")
    }
    return nil
}

// Dispatch with invalid command
result, err := bus.Dispatch(ctx, CreateOrderCommand{CustomerID: ""})
// err: validation failed: customer ID is required
```

The middleware calls `cmd.Validate()` and returns early if it fails.

---

### Recovery Middleware

Catches panics and converts them to errors:

```go
bus := mink.NewCommandBus(
    mink.WithMiddleware(
        mink.RecoveryMiddleware(),
    ),
)

// Even if handler panics, you get a clean error
handler := func(ctx context.Context, cmd mink.Command) (mink.CommandResult, error) {
    panic("something went terribly wrong")
}

result, err := bus.Dispatch(ctx, myCommand)
// err is a PanicError with stack trace
```

The `PanicError` includes:
- `CommandType`: Which command panicked
- `Value`: The panic value
- `Stack`: Full stack trace
- `CommandData`: Sanitized command data for debugging

```go
var panicErr *mink.PanicError
if errors.As(err, &panicErr) {
    log.Printf("Panic in %s: %v\n%s",
        panicErr.CommandType,
        panicErr.Value,
        panicErr.Stack)
}
```

---

### Logging Middleware

Logs command execution:

```go
logger := log.New(os.Stdout, "[CMD] ", log.LstdFlags)

bus := mink.NewCommandBus(
    mink.WithMiddleware(
        mink.LoggingMiddleware(logger),
    ),
)

// Output:
// [CMD] Starting: CreateOrder
// [CMD] Completed: CreateOrder (15.234ms) -> order-123 v1
// or
// [CMD] Failed: CreateOrder (2.456ms) -> validation failed
```

---

### Metrics Middleware

Collects execution metrics:

```go
type MetricsCollector interface {
    CommandExecuted(commandType string, duration time.Duration, err error)
}

type prometheusCollector struct {
    counter   *prometheus.CounterVec
    histogram *prometheus.HistogramVec
}

func (c *prometheusCollector) CommandExecuted(cmdType string, duration time.Duration, err error) {
    status := "success"
    if err != nil {
        status = "error"
    }

    c.counter.WithLabelValues(cmdType, status).Inc()
    c.histogram.WithLabelValues(cmdType).Observe(duration.Seconds())
}

bus := mink.NewCommandBus(
    mink.WithMiddleware(
        mink.MetricsMiddleware(&prometheusCollector{...}),
    ),
)
```

---

### Timeout Middleware

Enforces maximum execution time:

```go
bus := mink.NewCommandBus(
    mink.WithMiddleware(
        mink.TimeoutMiddleware(5 * time.Second),
    ),
)

// If handler takes > 5 seconds, context is cancelled
result, err := bus.Dispatch(ctx, slowCommand)
// err: context deadline exceeded
```

---

### Retry Middleware

Retries failed commands with exponential backoff:

```go
bus := mink.NewCommandBus(
    mink.WithMiddleware(
        mink.RetryMiddleware(mink.RetryConfig{
            MaxAttempts:  3,
            InitialDelay: 100 * time.Millisecond,
            MaxDelay:     2 * time.Second,
            Multiplier:   2.0,
            ShouldRetry: func(err error) bool {
                // Only retry transient errors
                return errors.Is(err, mink.ErrConcurrencyConflict)
            },
        }),
    ),
)
```

Configuration:
- `MaxAttempts`: Total attempts (including first)
- `InitialDelay`: Wait before first retry
- `MaxDelay`: Maximum wait between retries
- `Multiplier`: Delay multiplier (exponential backoff)
- `ShouldRetry`: Predicate for retryable errors

---

### Correlation ID Middleware

Ensures every command has a correlation ID for distributed tracing:

```go
bus := mink.NewCommandBus(
    mink.WithMiddleware(
        mink.CorrelationIDMiddleware(nil), // Use default UUID generator
    ),
)

// In your handler:
handler := func(ctx context.Context, cmd mink.Command) (mink.CommandResult, error) {
    correlationID := mink.CorrelationIDFromContext(ctx)
    log.Printf("[%s] Processing command", correlationID)

    // Pass correlation ID to events
    metadata := mink.Metadata{CorrelationID: correlationID}
    store.Append(ctx, streamID, events, mink.WithAppendMetadata(metadata))

    return result, nil
}
```

With custom generator:

```go
generator := func() string {
    return fmt.Sprintf("req-%d", time.Now().UnixNano())
}

mink.CorrelationIDMiddleware(generator)
```

If the command already has a correlation ID (via `CommandBase`), it's preserved.

---

### Causation ID Middleware

Tracks the chain of causation:

```go
bus := mink.NewCommandBus(
    mink.WithMiddleware(
        mink.CausationIDMiddleware(),
    ),
)

// In handler:
causationID := mink.CausationIDFromContext(ctx)
// causationID is the command's ID (what caused these events)
```

This links events to the commands that created them:

```
Command: cmd-123
  └── Event: evt-456 (CausationID: cmd-123)
      └── (triggers another command)
          └── Event: evt-789 (CausationID: evt-456)
```

---

### Tenant Middleware

For multi-tenant applications:

```go
bus := mink.NewCommandBus(
    mink.WithMiddleware(
        mink.TenantMiddleware(
            func(cmd mink.Command) string {
                // Extract tenant from command
                if tc, ok := cmd.(TenantCommand); ok {
                    return tc.TenantID()
                }
                return ""
            },
            true, // required: fail if no tenant
        ),
    ),
)

// In handler:
tenantID := mink.TenantIDFromContext(ctx)
// Use tenant for isolation, filtering, etc.
```

---

### Idempotency Middleware

Prevents duplicate command execution:

```go
// Create idempotency store (in-memory for example)
idempotencyStore := memory.NewIdempotencyStore()

bus := mink.NewCommandBus(
    mink.WithMiddleware(
        mink.IdempotencyMiddleware(mink.IdempotencyConfig{
            Store: idempotencyStore,
            TTL:   24 * time.Hour,
            KeyGenerator: func(cmd mink.Command) string {
                if ic, ok := cmd.(mink.IdempotentCommand); ok {
                    return ic.IdempotencyKey()
                }
                return ""
            },
        }),
    ),
)

// Commands with the same idempotency key return cached result
cmd := ProcessPaymentCommand{
    OrderID:       "order-123",
    Amount:        99.99,
    TransactionID: "txn-abc-123", // Idempotency key
}

result1, _ := bus.Dispatch(ctx, cmd) // Executes handler
result2, _ := bus.Dispatch(ctx, cmd) // Returns cached result

// Both results are identical
```

This is crucial for:
- At-least-once delivery guarantees
- Retry safety
- Webhook handlers
- Payment processing

---

## Conditional Middleware

### Apply Based on Condition

```go
bus := mink.NewCommandBus(
    mink.WithMiddleware(
        mink.ConditionalMiddleware(
            func(cmd mink.Command) bool {
                // Only apply to payment commands
                return strings.HasPrefix(cmd.CommandType(), "Payment")
            },
            mink.IdempotencyMiddleware(config),
        ),
    ),
)
```

### Apply to Specific Command Types

```go
bus := mink.NewCommandBus(
    mink.WithMiddleware(
        mink.CommandTypeMiddleware(
            []string{"ProcessPayment", "RefundPayment"},
            mink.IdempotencyMiddleware(config),
        ),
    ),
)
```

---

## Custom Middleware

### Timing Middleware

```go
func TimingMiddleware() mink.Middleware {
    return func(next mink.MiddlewareFunc) mink.MiddlewareFunc {
        return func(ctx context.Context, cmd mink.Command) (mink.CommandResult, error) {
            start := time.Now()

            result, err := next(ctx, cmd)

            duration := time.Since(start)
            log.Printf("%s took %v", cmd.CommandType(), duration)

            return result, err
        }
    }
}
```

### Authentication Middleware

```go
func AuthMiddleware(authService AuthService) mink.Middleware {
    return func(next mink.MiddlewareFunc) mink.MiddlewareFunc {
        return func(ctx context.Context, cmd mink.Command) (mink.CommandResult, error) {
            userID := UserIDFromContext(ctx)
            if userID == "" {
                return mink.NewErrorResult(ErrUnauthorized), ErrUnauthorized
            }

            // Check permissions
            if !authService.CanExecute(userID, cmd.CommandType()) {
                return mink.NewErrorResult(ErrForbidden), ErrForbidden
            }

            return next(ctx, cmd)
        }
    }
}
```

### Rate Limiting Middleware

```go
func RateLimitMiddleware(limiter *rate.Limiter) mink.Middleware {
    return func(next mink.MiddlewareFunc) mink.MiddlewareFunc {
        return func(ctx context.Context, cmd mink.Command) (mink.CommandResult, error) {
            if !limiter.Allow() {
                err := errors.New("rate limit exceeded")
                return mink.NewErrorResult(err), err
            }

            return next(ctx, cmd)
        }
    }
}
```

### Context Enrichment Middleware

```go
func ContextValueMiddleware(key, value interface{}) mink.Middleware {
    return func(next mink.MiddlewareFunc) mink.MiddlewareFunc {
        return func(ctx context.Context, cmd mink.Command) (mink.CommandResult, error) {
            enrichedCtx := context.WithValue(ctx, key, value)
            return next(enrichedCtx, cmd)
        }
    }
}

// Usage
bus := mink.NewCommandBus(
    mink.WithMiddleware(
        ContextValueMiddleware("requestID", requestID),
    ),
)
```

### Audit Trail Middleware

```go
func AuditMiddleware(auditLog AuditLogger) mink.Middleware {
    return func(next mink.MiddlewareFunc) mink.MiddlewareFunc {
        return func(ctx context.Context, cmd mink.Command) (mink.CommandResult, error) {
            userID := mink.UserIDFromContext(ctx)
            correlationID := mink.CorrelationIDFromContext(ctx)

            // Log before
            auditLog.LogAttempt(AuditEntry{
                Timestamp:     time.Now(),
                UserID:        userID,
                CorrelationID: correlationID,
                CommandType:   cmd.CommandType(),
                CommandData:   sanitize(cmd),
            })

            result, err := next(ctx, cmd)

            // Log after
            auditLog.LogResult(AuditEntry{
                Timestamp:     time.Now(),
                UserID:        userID,
                CorrelationID: correlationID,
                CommandType:   cmd.CommandType(),
                Success:       err == nil,
                Error:         err,
            })

            return result, err
        }
    }
}
```

---

## Recommended Middleware Order

Order matters. Here's a recommended configuration:

```go
bus := mink.NewCommandBus(
    mink.WithHandlerRegistry(registry),
    mink.WithMiddleware(
        // 1. Recovery (outermost - catches all panics)
        mink.RecoveryMiddleware(),

        // 2. Logging (see all commands, even failed ones)
        mink.LoggingMiddleware(logger),

        // 3. Metrics (track all attempts)
        mink.MetricsMiddleware(collector),

        // 4. Timeout (don't let commands run forever)
        mink.TimeoutMiddleware(30 * time.Second),

        // 5. Correlation ID (ensure traceability)
        mink.CorrelationIDMiddleware(nil),

        // 6. Authentication (early rejection)
        AuthMiddleware(authService),

        // 7. Validation (before spending resources)
        mink.ValidationMiddleware(),

        // 8. Retry (for transient failures)
        mink.RetryMiddleware(retryConfig),

        // 9. Idempotency (innermost - prevent duplicate work)
        mink.IdempotencyMiddleware(idempotencyConfig),
    ),
)
```

### Why This Order?

1. **Recovery** first catches panics from any middleware
2. **Logging** sees everything, including validation failures
3. **Metrics** track all attempts, not just successful ones
4. **Timeout** prevents runaway commands early
5. **Correlation** ensures all work is traceable
6. **Authentication** rejects unauthorized before validation
7. **Validation** fails fast before expensive operations
8. **Retry** wraps idempotency for transient failures
9. **Idempotency** closest to handler, returns cached results

---

## Testing Middleware

### Test Individual Middleware

```go
func TestValidationMiddleware(t *testing.T) {
    middleware := mink.ValidationMiddleware()

    // Create a handler that should never be called
    handler := func(ctx context.Context, cmd mink.Command) (mink.CommandResult, error) {
        t.Fatal("Handler should not be called for invalid command")
        return mink.CommandResult{}, nil
    }

    wrapped := middleware(handler)

    // Invalid command
    invalidCmd := CreateOrderCommand{CustomerID: ""}
    _, err := wrapped(context.Background(), invalidCmd)

    if err == nil {
        t.Error("Expected validation error")
    }
}
```

### Test Middleware Chain

```go
func TestMiddlewareChain(t *testing.T) {
    var order []string

    first := func(next mink.MiddlewareFunc) mink.MiddlewareFunc {
        return func(ctx context.Context, cmd mink.Command) (mink.CommandResult, error) {
            order = append(order, "first-before")
            result, err := next(ctx, cmd)
            order = append(order, "first-after")
            return result, err
        }
    }

    second := func(next mink.MiddlewareFunc) mink.MiddlewareFunc {
        return func(ctx context.Context, cmd mink.Command) (mink.CommandResult, error) {
            order = append(order, "second-before")
            result, err := next(ctx, cmd)
            order = append(order, "second-after")
            return result, err
        }
    }

    handler := func(ctx context.Context, cmd mink.Command) (mink.CommandResult, error) {
        order = append(order, "handler")
        return mink.NewSuccessResult("test", 1), nil
    }

    bus := mink.NewCommandBus(
        mink.WithMiddleware(first, second),
    )
    bus.RegisterFunc("Test", handler)

    bus.Dispatch(context.Background(), TestCommand{})

    expected := []string{
        "first-before", "second-before", "handler", "second-after", "first-after",
    }

    if !reflect.DeepEqual(order, expected) {
        t.Errorf("Expected %v, got %v", expected, order)
    }
}
```

---

## What's Next?

In this post, you learned:

- How middleware wraps command execution
- Built-in middleware: logging, validation, recovery, metrics, retry
- Cross-cutting concerns: correlation, causation, tenancy, idempotency
- How to write custom middleware
- Recommended middleware ordering
- Testing strategies

In **Part 7**, we'll explore **Projections and Read Models**—building optimized query views from your event stream.

---

## Key Takeaways

1. **Middleware is an onion**: Commands flow in, results flow out
2. **Order matters**: Place broad concerns outside, specific inside
3. **Recovery first**: Catch panics before anything else
4. **Idempotency last**: Closest to handler for caching
5. **Custom middleware is simple**: Just a function wrapping a function

---

*Previous: [← Part 5: CQRS and the Command Bus](05-cqrs-and-command-bus.md)*

*Next: [Part 7: Projections and Read Models →](07-projections-and-read-models.md)*
