---
id: middleware
title: "Part 6: Middleware and Cross-Cutting Concerns"
sidebar_position: 6
---

# Part 6: Middleware and Cross-Cutting Concerns

Adding logging, validation, retries, idempotency, and more.

---

*This is Part 6 of an 8-part series on Event Sourcing and CQRS with Go.*

---

## What is Middleware?

Middleware wraps command execution--think of it as an onion:

```
┌─────────────────────────────────────────────────────────────┐
│ Logging Middleware                                          │
│  ┌───────────────────────────────────────────────────────┐  │
│  │ Validation Middleware                                 │  │
│  │  ┌─────────────────────────────────────────────────┐  │  │
│  │  │ Recovery Middleware                             │  │  │
│  │  │  ┌───────────────────────────────────────────┐  │  │  │
│  │  │  │            Handler                        │  │  │  │
│  │  │  └───────────────────────────────────────────┘  │  │  │
│  │  └─────────────────────────────────────────────────┘  │  │
│  └───────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────┘
```

---

## Middleware Signature

```go
type MiddlewareFunc func(ctx context.Context, cmd Command) (CommandResult, error)
type Middleware func(next MiddlewareFunc) MiddlewareFunc
```

### Basic Structure

```go
func MyMiddleware() mink.Middleware {
    return func(next mink.MiddlewareFunc) mink.MiddlewareFunc {
        return func(ctx context.Context, cmd mink.Command) (mink.CommandResult, error) {
            // Before
            fmt.Printf("About to handle %s\n", cmd.CommandType())

            result, err := next(ctx, cmd)

            // After
            fmt.Printf("Finished handling %s\n", cmd.CommandType())

            return result, err
        }
    }
}
```

---

## Built-in Middleware

### Validation

```go
bus := mink.NewCommandBus(
    mink.WithMiddleware(mink.ValidationMiddleware()),
)
```

### Recovery (Panic Handler)

```go
mink.RecoveryMiddleware()  // Catches panics, returns clean errors
```

### Logging

```go
mink.LoggingMiddleware(logger)
```

### Retry with Backoff

```go
mink.RetryMiddleware(mink.RetryConfig{
    MaxAttempts:  3,
    InitialDelay: 100 * time.Millisecond,
    MaxDelay:     2 * time.Second,
    Multiplier:   2.0,
})
```

### Correlation ID

```go
mink.CorrelationIDMiddleware(nil)  // Auto-generates if not present
```

### Idempotency

```go
mink.IdempotencyMiddleware(mink.IdempotencyConfig{
    Store: idempotencyStore,
    TTL:   24 * time.Hour,
})
```

---

## Recommended Order

```go
bus := mink.NewCommandBus(
    mink.WithMiddleware(
        mink.RecoveryMiddleware(),        // 1. Catch panics
        mink.LoggingMiddleware(logger),   // 2. Log everything
        mink.MetricsMiddleware(collector),// 3. Track metrics
        mink.TimeoutMiddleware(30*time.Second),
        mink.CorrelationIDMiddleware(nil),
        mink.ValidationMiddleware(),      // 7. Validate
        mink.RetryMiddleware(config),     // 8. Retry transient failures
        mink.IdempotencyMiddleware(config),// 9. Prevent duplicates
    ),
)
```

---

## Custom Middleware

### Timing

```go
func TimingMiddleware() mink.Middleware {
    return func(next mink.MiddlewareFunc) mink.MiddlewareFunc {
        return func(ctx context.Context, cmd mink.Command) (mink.CommandResult, error) {
            start := time.Now()
            result, err := next(ctx, cmd)
            log.Printf("%s took %v", cmd.CommandType(), time.Since(start))
            return result, err
        }
    }
}
```

### Authentication

```go
func AuthMiddleware(authService AuthService) mink.Middleware {
    return func(next mink.MiddlewareFunc) mink.MiddlewareFunc {
        return func(ctx context.Context, cmd mink.Command) (mink.CommandResult, error) {
            userID := UserIDFromContext(ctx)
            if !authService.CanExecute(userID, cmd.CommandType()) {
                return mink.NewErrorResult(ErrForbidden), ErrForbidden
            }
            return next(ctx, cmd)
        }
    }
}
```

---

## Observability Middleware

### Prometheus Metrics

```go
import "github.com/AshkanYarmoradi/go-mink/middleware/metrics"

// Create metrics
m := metrics.New(
    metrics.WithNamespace("myapp"),
    metrics.WithMetricsServiceName("order-service"),
)

// Register with Prometheus
m.MustRegister()

// Add to command bus
bus.Use(m.CommandMiddleware())

// Wrap event store
metricsStore := m.WrapEventStore(adapter)

// Wrap projections
metricsProjection := m.WrapProjection(projection)
```

**Collected Metrics:**
- `mink_commands_total` - Command count by type/status
- `mink_command_duration_seconds` - Command execution histogram
- `mink_commands_in_flight` - Currently executing commands
- `mink_eventstore_operations_total` - Event store operations
- `mink_projections_processed_total` - Projection processing
- `mink_projection_lag_events` - Projection lag

### OpenTelemetry Tracing

```go
import "github.com/AshkanYarmoradi/go-mink/middleware/tracing"

// Create tracer
tracer := tracing.NewTracer(
    tracing.WithServiceName("order-service"),
    tracing.WithTracerProvider(provider), // Optional custom provider
)

// Add to command bus
bus.Use(tracer.CommandMiddleware())

// Wrap event store
tracedStore := tracing.NewEventStoreMiddleware(adapter, tracer)

// Add events to current span
tracing.AddEvent(ctx, "Processing order", map[string]string{"order_id": "123"})
```

---

## Key Takeaways

:::tip
1. **Middleware is an onion**: Commands flow in, results flow out
2. **Order matters**: Place broad concerns outside, specific inside
3. **Recovery first**: Catch panics before anything else
4. **Idempotency last**: Closest to handler for caching
5. **Custom middleware is simple**: Just a function wrapping a function
:::

---

[<-- Part 5: CQRS](/docs/blog-series/cqrs) | [Part 7: Projections -->](/docs/blog-series/projections)
