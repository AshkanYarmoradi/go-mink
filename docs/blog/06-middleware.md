---
layout: default
title: "Part 6: Middleware and Cross-Cutting Concerns"
parent: Blog
nav_order: 6
permalink: /blog/06-middleware
---

# Part 6: Middleware and Cross-Cutting Concerns
{: .no_toc }

Adding logging, validation, retries, idempotency, and more.
{: .fs-6 .fw-300 }

## Table of contents
{: .no_toc .text-delta }

1. TOC
{:toc}

---

*This is Part 6 of an 8-part series on Event Sourcing and CQRS with Go.*

---

## What is Middleware?

Middleware wraps command execution—think of it as an onion:

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

## Key Takeaways

{: .highlight }
> 1. **Middleware is an onion**: Commands flow in, results flow out
> 2. **Order matters**: Place broad concerns outside, specific inside
> 3. **Recovery first**: Catch panics before anything else
> 4. **Idempotency last**: Closest to handler for caching
> 5. **Custom middleware is simple**: Just a function wrapping a function

---

[← Part 5: CQRS](/blog/05-cqrs){: .btn .fs-5 .mb-4 .mb-md-0 .mr-2 }
[Part 7: Projections →](/blog/07-projections){: .btn .btn-primary .fs-5 .mb-4 .mb-md-0 }
