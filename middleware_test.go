package mink

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test command for middleware tests

type middlewareTestCommand struct {
	CommandBase
	Value string
	Fail  bool
}

func (c middlewareTestCommand) CommandType() string { return "TestCommand" }
func (c middlewareTestCommand) Validate() error {
	if c.Value == "" {
		return NewValidationError("TestCommand", "Value", "value is required")
	}
	return nil
}

// middlewareTestCommandWithCorrelation is a command that has a GetCorrelationID method
type middlewareTestCommandWithCorrelation struct {
	middlewareTestCommand
	CorrelationIDValue string
}

func (c middlewareTestCommandWithCorrelation) GetCorrelationID() string {
	return c.CorrelationIDValue
}

func TestValidationMiddleware(t *testing.T) {
	t.Run("passes valid command", func(t *testing.T) {
		mw := ValidationMiddleware()

		handler := func(ctx context.Context, cmd Command) (CommandResult, error) {
			return NewSuccessResult("", 0), nil
		}

		result, err := mw(handler)(context.Background(), middlewareTestCommand{Value: "valid"})

		require.NoError(t, err)
		assert.True(t, result.IsSuccess())
	})

	t.Run("blocks invalid command", func(t *testing.T) {
		mw := ValidationMiddleware()
		handlerCalled := false

		handler := func(ctx context.Context, cmd Command) (CommandResult, error) {
			handlerCalled = true
			return NewSuccessResult("", 0), nil
		}

		result, err := mw(handler)(context.Background(), middlewareTestCommand{Value: ""})

		assert.False(t, handlerCalled)
		assert.Error(t, err)
		assert.ErrorIs(t, err, ErrValidationFailed)
		assert.True(t, result.IsError())
	})
}

func TestRecoveryMiddleware(t *testing.T) {
	t.Run("passes through successful handler", func(t *testing.T) {
		mw := RecoveryMiddleware()

		handler := func(ctx context.Context, cmd Command) (CommandResult, error) {
			return NewSuccessResult("agg-1", 1), nil
		}

		result, err := mw(handler)(context.Background(), middlewareTestCommand{Value: "test"})

		require.NoError(t, err)
		assert.True(t, result.IsSuccess())
	})

	t.Run("passes through handler error", func(t *testing.T) {
		mw := RecoveryMiddleware()
		expectedErr := errors.New("handler error")

		handler := func(ctx context.Context, cmd Command) (CommandResult, error) {
			return NewErrorResult(expectedErr), expectedErr
		}

		result, err := mw(handler)(context.Background(), middlewareTestCommand{Value: "test"})

		assert.Equal(t, expectedErr, err)
		assert.True(t, result.IsError())
	})

	t.Run("recovers from panic", func(t *testing.T) {
		mw := RecoveryMiddleware()

		handler := func(ctx context.Context, cmd Command) (CommandResult, error) {
			panic("something went wrong")
		}

		result, err := mw(handler)(context.Background(), middlewareTestCommand{Value: "test"})

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrHandlerPanicked)
		assert.True(t, result.IsError())

		var panicErr *PanicError
		require.ErrorAs(t, err, &panicErr)
		assert.Equal(t, "TestCommand", panicErr.CommandType)
		assert.Equal(t, "something went wrong", panicErr.Value)
		assert.NotEmpty(t, panicErr.Stack)
	})

	t.Run("recovers from panic with error value", func(t *testing.T) {
		mw := RecoveryMiddleware()

		handler := func(ctx context.Context, cmd Command) (CommandResult, error) {
			panic(errors.New("panic error"))
		}

		result, err := mw(handler)(context.Background(), middlewareTestCommand{Value: "test"})

		require.Error(t, err)
		assert.True(t, result.IsError())
	})
}

type testLogger struct {
	infoLogs  []string
	errorLogs []string
	warnLogs  []string
}

func (l *testLogger) Debug(msg string, args ...interface{}) {}
func (l *testLogger) Info(msg string, args ...interface{}) {
	l.infoLogs = append(l.infoLogs, msg)
}
func (l *testLogger) Warn(msg string, args ...interface{}) {
	l.warnLogs = append(l.warnLogs, msg)
}
func (l *testLogger) Error(msg string, args ...interface{}) {
	l.errorLogs = append(l.errorLogs, msg)
}

func TestLoggingMiddleware(t *testing.T) {
	t.Run("logs successful command", func(t *testing.T) {
		logger := &testLogger{}
		mw := NewLoggingMiddleware(logger).Middleware()

		handler := func(ctx context.Context, cmd Command) (CommandResult, error) {
			return NewSuccessResult("agg-1", 1), nil
		}

		_, _ = mw(handler)(context.Background(), middlewareTestCommand{Value: "test"})

		assert.Len(t, logger.infoLogs, 2)
		assert.Contains(t, logger.infoLogs[0], "Dispatching")
		assert.Contains(t, logger.infoLogs[1], "completed")
	})

	t.Run("logs failed command", func(t *testing.T) {
		logger := &testLogger{}
		mw := NewLoggingMiddleware(logger).Middleware()
		handlerErr := errors.New("handler error")

		handler := func(ctx context.Context, cmd Command) (CommandResult, error) {
			return NewErrorResult(handlerErr), handlerErr
		}

		_, _ = mw(handler)(context.Background(), middlewareTestCommand{Value: "test"})

		assert.Len(t, logger.infoLogs, 1)
		assert.Len(t, logger.errorLogs, 1)
	})

	t.Run("logs command with error result", func(t *testing.T) {
		logger := &testLogger{}
		mw := NewLoggingMiddleware(logger).Middleware()

		handler := func(ctx context.Context, cmd Command) (CommandResult, error) {
			return NewErrorResult(errors.New("result error")), nil
		}

		_, _ = mw(handler)(context.Background(), middlewareTestCommand{Value: "test"})

		assert.Len(t, logger.warnLogs, 1)
	})
}

func TestTimeoutMiddleware(t *testing.T) {
	t.Run("completes before timeout", func(t *testing.T) {
		mw := TimeoutMiddleware(1 * time.Second)

		handler := func(ctx context.Context, cmd Command) (CommandResult, error) {
			return NewSuccessResult("", 0), nil
		}

		result, err := mw(handler)(context.Background(), middlewareTestCommand{Value: "test"})

		require.NoError(t, err)
		assert.True(t, result.IsSuccess())
	})

	t.Run("times out", func(t *testing.T) {
		mw := TimeoutMiddleware(10 * time.Millisecond)

		handler := func(ctx context.Context, cmd Command) (CommandResult, error) {
			select {
			case <-ctx.Done():
				return NewErrorResult(ctx.Err()), ctx.Err()
			case <-time.After(100 * time.Millisecond):
				return NewSuccessResult("", 0), nil
			}
		}

		result, err := mw(handler)(context.Background(), middlewareTestCommand{Value: "test"})

		assert.ErrorIs(t, err, context.DeadlineExceeded)
		assert.True(t, result.IsError())
	})
}

func TestRetryMiddleware(t *testing.T) {
	t.Run("succeeds on first try", func(t *testing.T) {
		config := RetryConfig{MaxAttempts: 3}
		mw := RetryMiddleware(config)
		var attempts int

		handler := func(ctx context.Context, cmd Command) (CommandResult, error) {
			attempts++
			return NewSuccessResult("", 0), nil
		}

		result, err := mw(handler)(context.Background(), middlewareTestCommand{Value: "test"})

		require.NoError(t, err)
		assert.True(t, result.IsSuccess())
		assert.Equal(t, 1, attempts)
	})

	t.Run("retries on failure", func(t *testing.T) {
		config := RetryConfig{
			MaxAttempts:  3,
			InitialDelay: 1 * time.Millisecond,
		}
		mw := RetryMiddleware(config)
		var attempts int
		expectedErr := errors.New("fail")

		handler := func(ctx context.Context, cmd Command) (CommandResult, error) {
			attempts++
			if attempts < 3 {
				return NewErrorResult(expectedErr), expectedErr
			}
			return NewSuccessResult("", 0), nil
		}

		result, err := mw(handler)(context.Background(), middlewareTestCommand{Value: "test"})

		require.NoError(t, err)
		assert.True(t, result.IsSuccess())
		assert.Equal(t, 3, attempts)
	})

	t.Run("fails after max attempts", func(t *testing.T) {
		config := RetryConfig{
			MaxAttempts:  3,
			InitialDelay: 1 * time.Millisecond,
		}
		mw := RetryMiddleware(config)
		var attempts int
		expectedErr := errors.New("persistent failure")

		handler := func(ctx context.Context, cmd Command) (CommandResult, error) {
			attempts++
			return NewErrorResult(expectedErr), expectedErr
		}

		result, err := mw(handler)(context.Background(), middlewareTestCommand{Value: "test"})

		assert.Equal(t, expectedErr, err)
		assert.True(t, result.IsError())
		assert.Equal(t, 3, attempts)
	})

	t.Run("respects ShouldRetry", func(t *testing.T) {
		nonRetryableErr := errors.New("non-retryable")
		config := RetryConfig{
			MaxAttempts:  3,
			InitialDelay: 1 * time.Millisecond,
			ShouldRetry: func(err error) bool {
				return err != nonRetryableErr
			},
		}
		mw := RetryMiddleware(config)
		var attempts int

		handler := func(ctx context.Context, cmd Command) (CommandResult, error) {
			attempts++
			return NewErrorResult(nonRetryableErr), nonRetryableErr
		}

		result, err := mw(handler)(context.Background(), middlewareTestCommand{Value: "test"})

		assert.Equal(t, nonRetryableErr, err)
		assert.True(t, result.IsError())
		assert.Equal(t, 1, attempts) // No retry
	})

	t.Run("respects context cancellation", func(t *testing.T) {
		config := RetryConfig{
			MaxAttempts:  5,
			InitialDelay: 100 * time.Millisecond,
		}
		mw := RetryMiddleware(config)

		ctx, cancel := context.WithCancel(context.Background())

		var attempts int32
		handler := func(c context.Context, cmd Command) (CommandResult, error) {
			atomic.AddInt32(&attempts, 1)
			if atomic.LoadInt32(&attempts) == 1 {
				cancel()
			}
			return NewErrorResult(errors.New("fail")), errors.New("fail")
		}

		result, err := mw(handler)(ctx, middlewareTestCommand{Value: "test"})

		assert.ErrorIs(t, err, context.Canceled)
		assert.True(t, result.IsError())
	})

	t.Run("uses default config values", func(t *testing.T) {
		config := DefaultRetryConfig()
		assert.Equal(t, 3, config.MaxAttempts)
		assert.Equal(t, 100*time.Millisecond, config.InitialDelay)
		assert.Equal(t, 5*time.Second, config.MaxDelay)
		assert.Equal(t, 2.0, config.Multiplier)
	})

	t.Run("handles zero/negative config values", func(t *testing.T) {
		config := RetryConfig{
			MaxAttempts:  -1,
			InitialDelay: 0,
			MaxDelay:     0,
			Multiplier:   0,
		}
		mw := RetryMiddleware(config)
		var attempts int

		handler := func(ctx context.Context, cmd Command) (CommandResult, error) {
			attempts++
			return NewSuccessResult("", 0), nil
		}

		_, _ = mw(handler)(context.Background(), middlewareTestCommand{Value: "test"})

		assert.Equal(t, 1, attempts) // MaxAttempts defaults to 1
	})

	t.Run("caps delay at MaxDelay", func(t *testing.T) {
		config := RetryConfig{
			MaxAttempts:  4,
			InitialDelay: 1 * time.Millisecond,
			MaxDelay:     2 * time.Millisecond, // Very small max
			Multiplier:   10.0,                 // Large multiplier
		}
		mw := RetryMiddleware(config)
		var attempts int
		expectedErr := errors.New("fail")

		handler := func(ctx context.Context, cmd Command) (CommandResult, error) {
			attempts++
			return NewErrorResult(expectedErr), expectedErr
		}

		start := time.Now()
		_, _ = mw(handler)(context.Background(), middlewareTestCommand{Value: "test"})
		elapsed := time.Since(start)

		assert.Equal(t, 4, attempts)
		// With capping, delays would be: 1ms, 2ms (capped), 2ms (capped) = 5ms
		// Without capping: 1ms, 10ms, 100ms = 111ms
		assert.Less(t, elapsed, 50*time.Millisecond)
	})

	t.Run("retries on error result without Go error", func(t *testing.T) {
		config := RetryConfig{
			MaxAttempts:  3,
			InitialDelay: 1 * time.Millisecond,
		}
		mw := RetryMiddleware(config)
		var attempts int

		handler := func(ctx context.Context, cmd Command) (CommandResult, error) {
			attempts++
			if attempts < 3 {
				return NewErrorResult(errors.New("result error")), nil // No Go error, but error result
			}
			return NewSuccessResult("", 0), nil
		}

		result, err := mw(handler)(context.Background(), middlewareTestCommand{Value: "test"})

		require.NoError(t, err)
		assert.True(t, result.IsSuccess())
		assert.Equal(t, 3, attempts)
	})
}

type testMetricsCollector struct {
	records []struct {
		cmdType  string
		duration time.Duration
		success  bool
		err      error
	}
}

func (c *testMetricsCollector) RecordCommand(cmdType string, duration time.Duration, success bool, err error) {
	c.records = append(c.records, struct {
		cmdType  string
		duration time.Duration
		success  bool
		err      error
	}{cmdType, duration, success, err})
}

func TestMetricsMiddleware(t *testing.T) {
	t.Run("records successful command", func(t *testing.T) {
		collector := &testMetricsCollector{}
		mw := MetricsMiddleware(collector)

		handler := func(ctx context.Context, cmd Command) (CommandResult, error) {
			return NewSuccessResult("", 0), nil
		}

		_, _ = mw(handler)(context.Background(), middlewareTestCommand{Value: "test"})

		require.Len(t, collector.records, 1)
		assert.Equal(t, "TestCommand", collector.records[0].cmdType)
		assert.True(t, collector.records[0].success)
		assert.NoError(t, collector.records[0].err)
	})

	t.Run("records failed command", func(t *testing.T) {
		collector := &testMetricsCollector{}
		mw := MetricsMiddleware(collector)
		expectedErr := errors.New("fail")

		handler := func(ctx context.Context, cmd Command) (CommandResult, error) {
			return NewErrorResult(expectedErr), expectedErr
		}

		_, _ = mw(handler)(context.Background(), middlewareTestCommand{Value: "test"})

		require.Len(t, collector.records, 1)
		assert.False(t, collector.records[0].success)
		assert.Equal(t, expectedErr, collector.records[0].err)
	})

	t.Run("records error result without error return", func(t *testing.T) {
		collector := &testMetricsCollector{}
		mw := MetricsMiddleware(collector)
		resultErr := errors.New("result error")

		handler := func(ctx context.Context, cmd Command) (CommandResult, error) {
			return NewErrorResult(resultErr), nil
		}

		_, _ = mw(handler)(context.Background(), middlewareTestCommand{Value: "test"})

		require.Len(t, collector.records, 1)
		assert.False(t, collector.records[0].success)
		assert.Equal(t, resultErr, collector.records[0].err)
	})
}

func TestContextValueMiddleware(t *testing.T) {
	t.Run("adds value to context", func(t *testing.T) {
		type key string
		const testKey key = "test"

		mw := NewContextValueMiddleware(testKey, "test-value").Middleware()

		var capturedValue string
		handler := func(ctx context.Context, cmd Command) (CommandResult, error) {
			capturedValue = ctx.Value(testKey).(string)
			return NewSuccessResult("", 0), nil
		}

		_, _ = mw(handler)(context.Background(), middlewareTestCommand{Value: "test"})

		assert.Equal(t, "test-value", capturedValue)
	})
}

func TestCorrelationIDMiddleware(t *testing.T) {
	t.Run("generates correlation ID", func(t *testing.T) {
		mw := CorrelationIDMiddleware(func() string { return "generated-id" })

		var capturedID string
		handler := func(ctx context.Context, cmd Command) (CommandResult, error) {
			capturedID = CorrelationIDFromContext(ctx)
			return NewSuccessResult("", 0), nil
		}

		_, _ = mw(handler)(context.Background(), middlewareTestCommand{Value: "test"})

		assert.Equal(t, "generated-id", capturedID)
	})

	t.Run("preserves existing correlation ID", func(t *testing.T) {
		mw := CorrelationIDMiddleware(func() string { return "generated-id" })

		ctx := context.WithValue(context.Background(), correlationIDKey{}, "existing-id")

		var capturedID string
		handler := func(ctx context.Context, cmd Command) (CommandResult, error) {
			capturedID = CorrelationIDFromContext(ctx)
			return NewSuccessResult("", 0), nil
		}

		_, _ = mw(handler)(ctx, middlewareTestCommand{Value: "test"})

		assert.Equal(t, "existing-id", capturedID)
	})

	t.Run("uses default generator", func(t *testing.T) {
		mw := CorrelationIDMiddleware(nil)

		var capturedID string
		handler := func(ctx context.Context, cmd Command) (CommandResult, error) {
			capturedID = CorrelationIDFromContext(ctx)
			return NewSuccessResult("", 0), nil
		}

		_, _ = mw(handler)(context.Background(), middlewareTestCommand{Value: "test"})

		assert.NotEmpty(t, capturedID)
	})

	t.Run("uses correlation ID from command", func(t *testing.T) {
		mw := CorrelationIDMiddleware(func() string { return "generated-id" })

		var capturedID string
		handler := func(ctx context.Context, cmd Command) (CommandResult, error) {
			capturedID = CorrelationIDFromContext(ctx)
			return NewSuccessResult("", 0), nil
		}

		cmd := middlewareTestCommandWithCorrelation{
			middlewareTestCommand: middlewareTestCommand{Value: "test"},
			CorrelationIDValue:    "cmd-correlation-id",
		}
		_, _ = mw(handler)(context.Background(), cmd)

		assert.Equal(t, "cmd-correlation-id", capturedID)
	})
}

func TestCorrelationIDFromContext(t *testing.T) {
	t.Run("returns ID when present", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), correlationIDKey{}, "test-id")
		assert.Equal(t, "test-id", CorrelationIDFromContext(ctx))
	})

	t.Run("returns empty when not present", func(t *testing.T) {
		assert.Empty(t, CorrelationIDFromContext(context.Background()))
	})
}

func TestTenantMiddleware(t *testing.T) {
	type tenantCmd struct {
		middlewareTestCommand
		TenantID string
	}

	t.Run("extracts tenant ID", func(t *testing.T) {
		extractor := func(cmd Command) string {
			if tc, ok := cmd.(tenantCmd); ok {
				return tc.TenantID
			}
			return ""
		}

		mw := TenantMiddleware(extractor, false)

		var capturedTenantID string
		handler := func(ctx context.Context, cmd Command) (CommandResult, error) {
			capturedTenantID = TenantIDFromContext(ctx)
			return NewSuccessResult("", 0), nil
		}

		cmd := tenantCmd{middlewareTestCommand: middlewareTestCommand{Value: "test"}, TenantID: "tenant-1"}
		_, _ = mw(handler)(context.Background(), cmd)

		assert.Equal(t, "tenant-1", capturedTenantID)
	})

	t.Run("required tenant ID fails when missing", func(t *testing.T) {
		mw := TenantMiddleware(func(cmd Command) string { return "" }, true)
		handlerCalled := false

		handler := func(ctx context.Context, cmd Command) (CommandResult, error) {
			handlerCalled = true
			return NewSuccessResult("", 0), nil
		}

		result, err := mw(handler)(context.Background(), middlewareTestCommand{Value: "test"})

		assert.False(t, handlerCalled)
		assert.Error(t, err)
		assert.ErrorIs(t, err, ErrValidationFailed)
		assert.True(t, result.IsError())
	})

	t.Run("preserves existing tenant ID", func(t *testing.T) {
		mw := TenantMiddleware(func(cmd Command) string { return "new-tenant" }, false)
		ctx := WithTenantID(context.Background(), "existing-tenant")

		var capturedTenantID string
		handler := func(ctx context.Context, cmd Command) (CommandResult, error) {
			capturedTenantID = TenantIDFromContext(ctx)
			return NewSuccessResult("", 0), nil
		}

		_, _ = mw(handler)(ctx, middlewareTestCommand{Value: "test"})

		assert.Equal(t, "existing-tenant", capturedTenantID)
	})
}

func TestTenantIDFromContext(t *testing.T) {
	t.Run("returns ID when present", func(t *testing.T) {
		ctx := WithTenantID(context.Background(), "tenant-1")
		assert.Equal(t, "tenant-1", TenantIDFromContext(ctx))
	})

	t.Run("returns empty when not present", func(t *testing.T) {
		assert.Empty(t, TenantIDFromContext(context.Background()))
	})
}

func TestConditionalMiddleware(t *testing.T) {
	t.Run("applies when condition true", func(t *testing.T) {
		middlewareApplied := false
		innerMw := func(next MiddlewareFunc) MiddlewareFunc {
			return func(ctx context.Context, cmd Command) (CommandResult, error) {
				middlewareApplied = true
				return next(ctx, cmd)
			}
		}

		mw := ConditionalMiddleware(func(cmd Command) bool { return true }, innerMw)

		handler := func(ctx context.Context, cmd Command) (CommandResult, error) {
			return NewSuccessResult("", 0), nil
		}

		_, _ = mw(handler)(context.Background(), middlewareTestCommand{Value: "test"})

		assert.True(t, middlewareApplied)
	})

	t.Run("skips when condition false", func(t *testing.T) {
		middlewareApplied := false
		innerMw := func(next MiddlewareFunc) MiddlewareFunc {
			return func(ctx context.Context, cmd Command) (CommandResult, error) {
				middlewareApplied = true
				return next(ctx, cmd)
			}
		}

		mw := ConditionalMiddleware(func(cmd Command) bool { return false }, innerMw)

		handler := func(ctx context.Context, cmd Command) (CommandResult, error) {
			return NewSuccessResult("", 0), nil
		}

		_, _ = mw(handler)(context.Background(), middlewareTestCommand{Value: "test"})

		assert.False(t, middlewareApplied)
	})
}

func TestCommandTypeMiddleware(t *testing.T) {
	t.Run("applies for matching type", func(t *testing.T) {
		middlewareApplied := false
		innerMw := func(next MiddlewareFunc) MiddlewareFunc {
			return func(ctx context.Context, cmd Command) (CommandResult, error) {
				middlewareApplied = true
				return next(ctx, cmd)
			}
		}

		mw := CommandTypeMiddleware([]string{"TestCommand"}, innerMw)

		handler := func(ctx context.Context, cmd Command) (CommandResult, error) {
			return NewSuccessResult("", 0), nil
		}

		_, _ = mw(handler)(context.Background(), middlewareTestCommand{Value: "test"})

		assert.True(t, middlewareApplied)
	})

	t.Run("skips for non-matching type", func(t *testing.T) {
		middlewareApplied := false
		innerMw := func(next MiddlewareFunc) MiddlewareFunc {
			return func(ctx context.Context, cmd Command) (CommandResult, error) {
				middlewareApplied = true
				return next(ctx, cmd)
			}
		}

		mw := CommandTypeMiddleware([]string{"OtherCommand"}, innerMw)

		handler := func(ctx context.Context, cmd Command) (CommandResult, error) {
			return NewSuccessResult("", 0), nil
		}

		_, _ = mw(handler)(context.Background(), middlewareTestCommand{Value: "test"})

		assert.False(t, middlewareApplied)
	})
}
