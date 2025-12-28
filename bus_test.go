package mink

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test commands for bus tests

type busTestCreateOrder struct {
	CommandBase
	CustomerID string
}

func (c busTestCreateOrder) CommandType() string { return "CreateOrder" }
func (c busTestCreateOrder) Validate() error {
	if c.CustomerID == "" {
		return NewValidationError("CreateOrder", "CustomerID", "required")
	}
	return nil
}

func TestCommandBus_New(t *testing.T) {
	t.Run("creates with defaults", func(t *testing.T) {
		bus := NewCommandBus()
		assert.NotNil(t, bus)
		assert.Equal(t, 0, bus.HandlerCount())
		assert.Equal(t, 0, bus.MiddlewareCount())
		assert.False(t, bus.IsClosed())
	})

	t.Run("creates with middleware option", func(t *testing.T) {
		mw := func(next MiddlewareFunc) MiddlewareFunc {
			return next
		}
		bus := NewCommandBus(WithMiddleware(mw))
		assert.Equal(t, 1, bus.MiddlewareCount())
	})

	t.Run("creates with registry option", func(t *testing.T) {
		registry := NewHandlerRegistry()
		registry.RegisterFunc("Test", func(ctx context.Context, cmd Command) (CommandResult, error) {
			return NewSuccessResult("", 0), nil
		})

		bus := NewCommandBus(WithHandlerRegistry(registry))
		assert.True(t, bus.HasHandler("Test"))
	})
}

func TestCommandBus_Register(t *testing.T) {
	t.Run("registers handler", func(t *testing.T) {
		bus := NewCommandBus()
		bus.RegisterFunc("CreateOrder", func(ctx context.Context, cmd Command) (CommandResult, error) {
			return NewSuccessResult("order-1", 1), nil
		})

		assert.True(t, bus.HasHandler("CreateOrder"))
		assert.Equal(t, 1, bus.HandlerCount())
	})

	t.Run("registers CommandHandler", func(t *testing.T) {
		bus := NewCommandBus()
		handler := NewGenericHandler(func(ctx context.Context, cmd busTestCreateOrder) (CommandResult, error) {
			return NewSuccessResult("order-1", 1), nil
		})
		bus.Register(handler)

		assert.True(t, bus.HasHandler("CreateOrder"))
	})
}

func TestCommandBus_Use(t *testing.T) {
	t.Run("adds middleware", func(t *testing.T) {
		bus := NewCommandBus()

		mw := func(next MiddlewareFunc) MiddlewareFunc {
			return next
		}

		bus.Use(mw)
		assert.Equal(t, 1, bus.MiddlewareCount())

		bus.Use(mw, mw)
		assert.Equal(t, 3, bus.MiddlewareCount())
	})
}

func TestCommandBus_Dispatch(t *testing.T) {
	t.Run("dispatches to handler", func(t *testing.T) {
		bus := NewCommandBus()
		bus.RegisterFunc("CreateOrder", func(ctx context.Context, cmd Command) (CommandResult, error) {
			c := cmd.(busTestCreateOrder)
			return NewSuccessResult("order-"+c.CustomerID, 1), nil
		})

		result, err := bus.Dispatch(context.Background(), busTestCreateOrder{CustomerID: "cust-123"})

		require.NoError(t, err)
		assert.True(t, result.IsSuccess())
		assert.Equal(t, "order-cust-123", result.AggregateID)
	})

	t.Run("returns error for nil command", func(t *testing.T) {
		bus := NewCommandBus()

		result, err := bus.Dispatch(context.Background(), nil)

		require.ErrorIs(t, err, ErrNilCommand)
		assert.True(t, result.IsError())
	})

	t.Run("returns error for unknown command", func(t *testing.T) {
		bus := NewCommandBus()

		result, err := bus.Dispatch(context.Background(), busTestCreateOrder{})

		require.ErrorIs(t, err, ErrHandlerNotFound)
		assert.True(t, result.IsError())
	})

	t.Run("returns error when closed", func(t *testing.T) {
		bus := NewCommandBus()
		bus.RegisterFunc("CreateOrder", func(ctx context.Context, cmd Command) (CommandResult, error) {
			return NewSuccessResult("", 0), nil
		})

		bus.Close()

		result, err := bus.Dispatch(context.Background(), busTestCreateOrder{CustomerID: "cust-1"})

		require.ErrorIs(t, err, ErrCommandBusClosed)
		assert.True(t, result.IsError())
	})

	t.Run("executes middleware in order", func(t *testing.T) {
		var order []string
		bus := NewCommandBus()

		bus.Use(func(next MiddlewareFunc) MiddlewareFunc {
			return func(ctx context.Context, cmd Command) (CommandResult, error) {
				order = append(order, "mw1-before")
				result, err := next(ctx, cmd)
				order = append(order, "mw1-after")
				return result, err
			}
		})

		bus.Use(func(next MiddlewareFunc) MiddlewareFunc {
			return func(ctx context.Context, cmd Command) (CommandResult, error) {
				order = append(order, "mw2-before")
				result, err := next(ctx, cmd)
				order = append(order, "mw2-after")
				return result, err
			}
		})

		bus.RegisterFunc("CreateOrder", func(ctx context.Context, cmd Command) (CommandResult, error) {
			order = append(order, "handler")
			return NewSuccessResult("", 0), nil
		})

		_, err := bus.Dispatch(context.Background(), busTestCreateOrder{CustomerID: "cust-1"})
		require.NoError(t, err)

		assert.Equal(t, []string{"mw1-before", "mw2-before", "handler", "mw2-after", "mw1-after"}, order)
	})

	t.Run("middleware can short-circuit", func(t *testing.T) {
		handlerCalled := false
		bus := NewCommandBus()

		shortCircuitErr := errors.New("short circuit")
		bus.Use(func(next MiddlewareFunc) MiddlewareFunc {
			return func(ctx context.Context, cmd Command) (CommandResult, error) {
				return NewErrorResult(shortCircuitErr), shortCircuitErr
			}
		})

		bus.RegisterFunc("CreateOrder", func(ctx context.Context, cmd Command) (CommandResult, error) {
			handlerCalled = true
			return NewSuccessResult("", 0), nil
		})

		result, err := bus.Dispatch(context.Background(), busTestCreateOrder{CustomerID: "cust-1"})

		assert.False(t, handlerCalled)
		assert.Equal(t, shortCircuitErr, err)
		assert.True(t, result.IsError())
	})

	t.Run("middleware can modify context", func(t *testing.T) {
		type ctxKey string
		const key ctxKey = "test-key"

		bus := NewCommandBus()

		bus.Use(func(next MiddlewareFunc) MiddlewareFunc {
			return func(ctx context.Context, cmd Command) (CommandResult, error) {
				ctx = context.WithValue(ctx, key, "test-value")
				return next(ctx, cmd)
			}
		})

		var capturedValue string
		bus.RegisterFunc("CreateOrder", func(ctx context.Context, cmd Command) (CommandResult, error) {
			capturedValue = ctx.Value(key).(string)
			return NewSuccessResult("", 0), nil
		})

		_, err := bus.Dispatch(context.Background(), busTestCreateOrder{CustomerID: "cust-1"})
		require.NoError(t, err)
		assert.Equal(t, "test-value", capturedValue)
	})
}

func TestCommandBus_DispatchAsync(t *testing.T) {
	t.Run("returns channel with result", func(t *testing.T) {
		bus := NewCommandBus()
		bus.RegisterFunc("CreateOrder", func(ctx context.Context, cmd Command) (CommandResult, error) {
			return NewSuccessResult("order-1", 1), nil
		})

		resultCh := bus.DispatchAsync(context.Background(), busTestCreateOrder{CustomerID: "cust-1"})

		result := <-resultCh
		assert.True(t, result.IsSuccess())
		assert.Equal(t, "order-1", result.AggregateID)
		assert.NoError(t, result.Error)
	})

	t.Run("returns channel with error", func(t *testing.T) {
		bus := NewCommandBus()

		resultCh := bus.DispatchAsync(context.Background(), busTestCreateOrder{})

		result := <-resultCh
		assert.False(t, result.IsSuccess())
		assert.ErrorIs(t, result.Error, ErrHandlerNotFound)
	})

	t.Run("channel is closed after result", func(t *testing.T) {
		bus := NewCommandBus()
		bus.RegisterFunc("CreateOrder", func(ctx context.Context, cmd Command) (CommandResult, error) {
			return NewSuccessResult("", 0), nil
		})

		resultCh := bus.DispatchAsync(context.Background(), busTestCreateOrder{CustomerID: "cust-1"})

		<-resultCh

		// Channel should be closed
		_, ok := <-resultCh
		assert.False(t, ok)
	})
}

func TestCommandBus_DispatchAll(t *testing.T) {
	t.Run("dispatches all commands", func(t *testing.T) {
		bus := NewCommandBus()
		var processed []string

		bus.RegisterFunc("CreateOrder", func(ctx context.Context, cmd Command) (CommandResult, error) {
			c := cmd.(busTestCreateOrder)
			processed = append(processed, c.CustomerID)
			return NewSuccessResult("order-"+c.CustomerID, 1), nil
		})

		results, err := bus.DispatchAll(context.Background(),
			busTestCreateOrder{CustomerID: "cust-1"},
			busTestCreateOrder{CustomerID: "cust-2"},
			busTestCreateOrder{CustomerID: "cust-3"},
		)

		require.NoError(t, err)
		assert.Len(t, results, 3)
		assert.Equal(t, []string{"cust-1", "cust-2", "cust-3"}, processed)

		for _, r := range results {
			assert.True(t, r.IsSuccess())
		}
	})

	t.Run("returns partial results on context cancel", func(t *testing.T) {
		bus := NewCommandBus()
		ctx, cancel := context.WithCancel(context.Background())

		var count int
		bus.RegisterFunc("CreateOrder", func(c context.Context, cmd Command) (CommandResult, error) {
			count++
			if count == 2 {
				cancel() // Cancel after second command
			}
			return NewSuccessResult("", 0), nil
		})

		results, err := bus.DispatchAll(ctx,
			busTestCreateOrder{CustomerID: "cust-1"},
			busTestCreateOrder{CustomerID: "cust-2"},
			busTestCreateOrder{CustomerID: "cust-3"},
		)

		assert.ErrorIs(t, err, context.Canceled)
		assert.Len(t, results, 2) // Only two processed
	})

	t.Run("continues on handler error", func(t *testing.T) {
		bus := NewCommandBus()
		handlerErr := errors.New("handler error")

		bus.RegisterFunc("CreateOrder", func(ctx context.Context, cmd Command) (CommandResult, error) {
			c := cmd.(busTestCreateOrder)
			if c.CustomerID == "fail" {
				return NewErrorResult(handlerErr), handlerErr
			}
			return NewSuccessResult("order-"+c.CustomerID, 1), nil
		})

		results, err := bus.DispatchAll(context.Background(),
			busTestCreateOrder{CustomerID: "cust-1"},
			busTestCreateOrder{CustomerID: "fail"},
			busTestCreateOrder{CustomerID: "cust-3"},
		)

		require.NoError(t, err) // No context error
		assert.Len(t, results, 3)
		assert.True(t, results[0].IsSuccess())
		assert.False(t, results[1].IsSuccess())
		assert.True(t, results[2].IsSuccess())
	})
}

func TestCommandBus_Close(t *testing.T) {
	t.Run("marks bus as closed", func(t *testing.T) {
		bus := NewCommandBus()
		assert.False(t, bus.IsClosed())

		err := bus.Close()
		assert.NoError(t, err)
		assert.True(t, bus.IsClosed())
	})

	t.Run("Close is idempotent", func(t *testing.T) {
		bus := NewCommandBus()

		err1 := bus.Close()
		err2 := bus.Close()

		assert.NoError(t, err1)
		assert.NoError(t, err2)
		assert.True(t, bus.IsClosed())
	})
}

func TestCommandBus_Concurrent(t *testing.T) {
	t.Run("concurrent dispatch", func(t *testing.T) {
		bus := NewCommandBus()
		var counter int32

		bus.RegisterFunc("CreateOrder", func(ctx context.Context, cmd Command) (CommandResult, error) {
			atomic.AddInt32(&counter, 1)
			return NewSuccessResult("", 0), nil
		})

		var wg sync.WaitGroup
		for i := 0; i < 100; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, _ = bus.Dispatch(context.Background(), busTestCreateOrder{CustomerID: "cust-1"})
			}()
		}

		wg.Wait()
		assert.Equal(t, int32(100), counter)
	})

	t.Run("concurrent register and dispatch", func(t *testing.T) {
		bus := NewCommandBus()
		var wg sync.WaitGroup

		// Register handlers concurrently
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				bus.RegisterFunc("CreateOrder", func(ctx context.Context, cmd Command) (CommandResult, error) {
					return NewSuccessResult("", 0), nil
				})
			}()
		}

		// Dispatch concurrently
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, _ = bus.Dispatch(context.Background(), busTestCreateOrder{CustomerID: "cust-1"})
			}()
		}

		wg.Wait()
	})
}

func TestDispatchResult(t *testing.T) {
	t.Run("IsSuccess with success", func(t *testing.T) {
		result := DispatchResult{
			CommandResult: NewSuccessResult("agg-1", 1),
			Error:         nil,
		}
		assert.True(t, result.IsSuccess())
	})

	t.Run("IsSuccess with error", func(t *testing.T) {
		result := DispatchResult{
			CommandResult: NewSuccessResult("agg-1", 1),
			Error:         errors.New("dispatch error"),
		}
		assert.False(t, result.IsSuccess())
	})

	t.Run("IsSuccess with command error", func(t *testing.T) {
		result := DispatchResult{
			CommandResult: NewErrorResult(errors.New("command error")),
			Error:         nil,
		}
		assert.False(t, result.IsSuccess())
	})
}

func TestChainMiddleware(t *testing.T) {
	t.Run("chains middleware in order", func(t *testing.T) {
		var order []string

		mw1 := func(next MiddlewareFunc) MiddlewareFunc {
			return func(ctx context.Context, cmd Command) (CommandResult, error) {
				order = append(order, "mw1-before")
				result, err := next(ctx, cmd)
				order = append(order, "mw1-after")
				return result, err
			}
		}

		mw2 := func(next MiddlewareFunc) MiddlewareFunc {
			return func(ctx context.Context, cmd Command) (CommandResult, error) {
				order = append(order, "mw2-before")
				result, err := next(ctx, cmd)
				order = append(order, "mw2-after")
				return result, err
			}
		}

		chain := ChainMiddleware(mw1, mw2)

		handler := func(ctx context.Context, cmd Command) (CommandResult, error) {
			order = append(order, "handler")
			return NewSuccessResult("", 0), nil
		}

		wrappedHandler := chain(handler)
		_, _ = wrappedHandler(context.Background(), busTestCreateOrder{})

		assert.Equal(t, []string{"mw1-before", "mw2-before", "handler", "mw2-after", "mw1-after"}, order)
	})

	t.Run("empty chain returns handler", func(t *testing.T) {
		chain := ChainMiddleware()

		handler := func(ctx context.Context, cmd Command) (CommandResult, error) {
			return NewSuccessResult("test", 1), nil
		}

		wrappedHandler := chain(handler)
		result, err := wrappedHandler(context.Background(), busTestCreateOrder{})

		require.NoError(t, err)
		assert.Equal(t, "test", result.AggregateID)
	})
}

func TestCommandBus_WithHandlerTimeout(t *testing.T) {
	t.Run("context deadline propagates", func(t *testing.T) {
		bus := NewCommandBus()

		bus.RegisterFunc("CreateOrder", func(ctx context.Context, cmd Command) (CommandResult, error) {
			select {
			case <-ctx.Done():
				return NewErrorResult(ctx.Err()), ctx.Err()
			case <-time.After(100 * time.Millisecond):
				return NewSuccessResult("", 0), nil
			}
		})

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer cancel()

		result, err := bus.Dispatch(ctx, busTestCreateOrder{CustomerID: "cust-1"})

		assert.ErrorIs(t, err, context.DeadlineExceeded)
		assert.True(t, result.IsError())
	})
}
