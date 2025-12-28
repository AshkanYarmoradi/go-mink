package mink

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime/debug"
	"time"
)

// ValidationMiddleware validates commands before they reach the handler.
// If validation fails, the command is not dispatched.
func ValidationMiddleware() Middleware {
	return func(next MiddlewareFunc) MiddlewareFunc {
		return func(ctx context.Context, cmd Command) (CommandResult, error) {
			if err := cmd.Validate(); err != nil {
				return NewErrorResult(err), err
			}
			return next(ctx, cmd)
		}
	}
}

// RecoveryMiddleware recovers from panics in handlers and returns them as errors.
// It captures a sanitized representation of the command data for debugging.
func RecoveryMiddleware() Middleware {
	return func(next MiddlewareFunc) MiddlewareFunc {
		return func(ctx context.Context, cmd Command) (result CommandResult, err error) {
			defer func() {
				if r := recover(); r != nil {
					stack := string(debug.Stack())
					// Capture command data for debugging (best effort, ignore errors)
					var commandData string
					if data, jsonErr := json.Marshal(cmd); jsonErr == nil {
						commandData = string(data)
					}
					panicErr := NewPanicErrorWithCommand(cmd.CommandType(), r, stack, commandData)
					result = NewErrorResult(panicErr)
					err = panicErr
				}
			}()
			return next(ctx, cmd)
		}
	}
}

// LoggingMiddleware logs command execution.
type LoggingMiddleware struct {
	logger Logger
}

// NewLoggingMiddleware creates a new LoggingMiddleware.
func NewLoggingMiddleware(logger Logger) *LoggingMiddleware {
	return &LoggingMiddleware{logger: logger}
}

// Middleware returns the middleware function.
func (m *LoggingMiddleware) Middleware() Middleware {
	return func(next MiddlewareFunc) MiddlewareFunc {
		return func(ctx context.Context, cmd Command) (CommandResult, error) {
			start := time.Now()

			m.logger.Info("Dispatching command",
				"type", cmd.CommandType(),
			)

			result, err := next(ctx, cmd)

			duration := time.Since(start)

			if err != nil {
				m.logger.Error("Command failed",
					"type", cmd.CommandType(),
					"duration", duration,
					"error", err,
				)
			} else if result.IsError() {
				m.logger.Warn("Command returned error result",
					"type", cmd.CommandType(),
					"duration", duration,
					"error", result.Error,
				)
			} else {
				m.logger.Info("Command completed",
					"type", cmd.CommandType(),
					"duration", duration,
					"aggregateId", result.AggregateID,
					"version", result.Version,
				)
			}

			return result, err
		}
	}
}

// TimeoutMiddleware adds a timeout to command execution.
func TimeoutMiddleware(timeout time.Duration) Middleware {
	return func(next MiddlewareFunc) MiddlewareFunc {
		return func(ctx context.Context, cmd Command) (CommandResult, error) {
			ctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			return next(ctx, cmd)
		}
	}
}

// RetryMiddleware retries failed commands.
type RetryConfig struct {
	// MaxAttempts is the maximum number of attempts (including the first one).
	MaxAttempts int

	// InitialDelay is the initial delay between retries.
	InitialDelay time.Duration

	// MaxDelay is the maximum delay between retries.
	MaxDelay time.Duration

	// Multiplier is the factor by which the delay increases on each retry.
	Multiplier float64

	// ShouldRetry determines if an error should be retried.
	// If nil, all errors are retried.
	ShouldRetry func(err error) bool
}

// DefaultRetryConfig returns a default retry configuration.
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxAttempts:  3,
		InitialDelay: 100 * time.Millisecond,
		MaxDelay:     5 * time.Second,
		Multiplier:   2.0,
		ShouldRetry:  nil,
	}
}

// RetryMiddleware creates middleware that retries failed commands.
func RetryMiddleware(config RetryConfig) Middleware {
	if config.MaxAttempts <= 0 {
		config.MaxAttempts = 1
	}
	if config.InitialDelay <= 0 {
		config.InitialDelay = 100 * time.Millisecond
	}
	if config.MaxDelay <= 0 {
		config.MaxDelay = 5 * time.Second
	}
	if config.Multiplier <= 0 {
		config.Multiplier = 1.0
	}

	return func(next MiddlewareFunc) MiddlewareFunc {
		return func(ctx context.Context, cmd Command) (CommandResult, error) {
			var lastResult CommandResult
			var lastErr error
			delay := config.InitialDelay

			for attempt := 1; attempt <= config.MaxAttempts; attempt++ {
				lastResult, lastErr = next(ctx, cmd)

				// Success
				if lastErr == nil && lastResult.IsSuccess() {
					return lastResult, nil
				}

				// Check if we should retry
				if attempt == config.MaxAttempts {
					break
				}

				errToCheck := lastErr
				if errToCheck == nil && lastResult.Error != nil {
					errToCheck = lastResult.Error
				}

				if config.ShouldRetry != nil && !config.ShouldRetry(errToCheck) {
					break
				}

				// Wait before retry
				select {
				case <-ctx.Done():
					return NewErrorResult(ctx.Err()), ctx.Err()
				case <-time.After(delay):
				}

				// Increase delay for next retry
				delay = time.Duration(float64(delay) * config.Multiplier)
				if delay > config.MaxDelay {
					delay = config.MaxDelay
				}
			}

			return lastResult, lastErr
		}
	}
}

// MetricsMiddleware collects metrics about command execution.
type MetricsCollector interface {
	// RecordCommand records a command execution.
	RecordCommand(cmdType string, duration time.Duration, success bool, err error)
}

// MetricsMiddleware creates middleware that records metrics.
func MetricsMiddleware(collector MetricsCollector) Middleware {
	return func(next MiddlewareFunc) MiddlewareFunc {
		return func(ctx context.Context, cmd Command) (CommandResult, error) {
			start := time.Now()
			result, err := next(ctx, cmd)
			duration := time.Since(start)

			success := err == nil && result.IsSuccess()
			recordErr := err
			if recordErr == nil && result.Error != nil {
				recordErr = result.Error
			}

			collector.RecordCommand(cmd.CommandType(), duration, success, recordErr)

			return result, err
		}
	}
}

// ContextValueMiddleware adds values to the context.
type ContextValueMiddleware struct {
	key   interface{}
	value interface{}
}

// NewContextValueMiddleware creates middleware that adds a value to the context.
func NewContextValueMiddleware(key, value interface{}) *ContextValueMiddleware {
	return &ContextValueMiddleware{key: key, value: value}
}

// Middleware returns the middleware function.
func (m *ContextValueMiddleware) Middleware() Middleware {
	return func(next MiddlewareFunc) MiddlewareFunc {
		return func(ctx context.Context, cmd Command) (CommandResult, error) {
			ctx = context.WithValue(ctx, m.key, m.value)
			return next(ctx, cmd)
		}
	}
}

// CorrelationIDMiddleware ensures commands have a correlation ID in context.
type correlationIDKey struct{}

// CorrelationIDFromContext returns the correlation ID from context.
func CorrelationIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(correlationIDKey{}).(string); ok {
		return id
	}
	return ""
}

// CorrelationIDMiddleware creates middleware that propagates correlation IDs.
func CorrelationIDMiddleware(generator func() string) Middleware {
	if generator == nil {
		generator = func() string {
			return fmt.Sprintf("%d", time.Now().UnixNano())
		}
	}

	return func(next MiddlewareFunc) MiddlewareFunc {
		return func(ctx context.Context, cmd Command) (CommandResult, error) {
			// Check if correlation ID already exists
			if CorrelationIDFromContext(ctx) != "" {
				return next(ctx, cmd)
			}

			// Try to get from command
			var correlationID string
			if base, ok := cmd.(interface{ GetCorrelationID() string }); ok {
				correlationID = base.GetCorrelationID()
			}

			// Generate if not present
			if correlationID == "" {
				correlationID = generator()
			}

			ctx = context.WithValue(ctx, correlationIDKey{}, correlationID)
			return next(ctx, cmd)
		}
	}
}

// =============================================================================
// Causation ID Middleware
// =============================================================================

// causationIDKey is the context key for causation ID.
type causationIDKey struct{}

// CausationIDFromContext returns the causation ID from context.
func CausationIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(causationIDKey{}).(string); ok {
		return id
	}
	return ""
}

// WithCausationID returns a context with the causation ID set.
func WithCausationID(ctx context.Context, causationID string) context.Context {
	return context.WithValue(ctx, causationIDKey{}, causationID)
}

// CausationIDMiddleware creates middleware that propagates causation IDs.
// The causation ID links events/commands to the command that caused them.
// This is essential for tracking the chain of causality in event sourcing.
func CausationIDMiddleware() Middleware {
	return func(next MiddlewareFunc) MiddlewareFunc {
		return func(ctx context.Context, cmd Command) (CommandResult, error) {
			// Check if causation ID already exists in context
			if CausationIDFromContext(ctx) != "" {
				return next(ctx, cmd)
			}

			// Try to get from command
			var causationID string
			if base, ok := cmd.(interface{ GetCausationID() string }); ok {
				causationID = base.GetCausationID()
			}

			// If command has a command ID, use it as causation for downstream
			if causationID == "" {
				if base, ok := cmd.(interface{ GetCommandID() string }); ok {
					causationID = base.GetCommandID()
				}
			}

			if causationID != "" {
				ctx = WithCausationID(ctx, causationID)
			}

			return next(ctx, cmd)
		}
	}
}

// =============================================================================
// Tenant Middleware
// =============================================================================

// TenantIDKey is the context key for tenant ID.
type tenantIDKey struct{}

// TenantIDFromContext returns the tenant ID from context.
func TenantIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(tenantIDKey{}).(string); ok {
		return id
	}
	return ""
}

// WithTenantID returns a context with the tenant ID set.
func WithTenantID(ctx context.Context, tenantID string) context.Context {
	return context.WithValue(ctx, tenantIDKey{}, tenantID)
}

// TenantMiddleware extracts and validates tenant ID.
func TenantMiddleware(extractor func(Command) string, required bool) Middleware {
	return func(next MiddlewareFunc) MiddlewareFunc {
		return func(ctx context.Context, cmd Command) (CommandResult, error) {
			// Check if tenant ID already in context
			if TenantIDFromContext(ctx) != "" {
				return next(ctx, cmd)
			}

			// Extract tenant ID from command
			tenantID := ""
			if extractor != nil {
				tenantID = extractor(cmd)
			}

			if tenantID == "" && required {
				err := NewValidationError(cmd.CommandType(), "tenantId", "tenant ID is required")
				return NewErrorResult(err), err
			}

			if tenantID != "" {
				ctx = WithTenantID(ctx, tenantID)
			}

			return next(ctx, cmd)
		}
	}
}

// ConditionalMiddleware applies middleware only if the condition is true.
func ConditionalMiddleware(condition func(Command) bool, middleware Middleware) Middleware {
	return func(next MiddlewareFunc) MiddlewareFunc {
		return func(ctx context.Context, cmd Command) (CommandResult, error) {
			if condition(cmd) {
				return middleware(next)(ctx, cmd)
			}
			return next(ctx, cmd)
		}
	}
}

// CommandTypeMiddleware applies middleware only for specific command types.
func CommandTypeMiddleware(types []string, middleware Middleware) Middleware {
	typeSet := make(map[string]bool, len(types))
	for _, t := range types {
		typeSet[t] = true
	}

	return ConditionalMiddleware(func(cmd Command) bool {
		return typeSet[cmd.CommandType()]
	}, middleware)
}
