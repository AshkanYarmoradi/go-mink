package mink

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test handlers

type handlerTestCreateOrder struct {
	CommandBase
	CustomerID string
}

func (c handlerTestCreateOrder) CommandType() string { return "CreateOrder" }
func (c handlerTestCreateOrder) Validate() error {
	if c.CustomerID == "" {
		return NewValidationError("CreateOrder", "CustomerID", "required")
	}
	return nil
}
func (c handlerTestCreateOrder) AggregateID() string { return "" }

type handlerTestAddItem struct {
	CommandBase
	OrderID  string
	SKU      string
	Quantity int
}

func (c handlerTestAddItem) CommandType() string { return "AddItem" }
func (c handlerTestAddItem) Validate() error     { return nil }
func (c handlerTestAddItem) AggregateID() string { return c.OrderID }

func TestCommandHandlerFunc(t *testing.T) {
	t.Run("implements CommandHandler", func(t *testing.T) {
		handler := NewCommandHandlerFunc("TestCommand", func(ctx context.Context, cmd Command) (CommandResult, error) {
			return NewSuccessResult("agg-1", 1), nil
		})

		var _ CommandHandler = handler
		assert.Equal(t, "TestCommand", handler.CommandType())
	})

	t.Run("Handle returns result", func(t *testing.T) {
		handler := NewCommandHandlerFunc("CreateOrder", func(ctx context.Context, cmd Command) (CommandResult, error) {
			c := cmd.(handlerTestCreateOrder)
			return NewSuccessResult("order-"+c.CustomerID, 1), nil
		})

		cmd := handlerTestCreateOrder{CustomerID: "cust-123"}
		result, err := handler.Handle(context.Background(), cmd)

		require.NoError(t, err)
		assert.True(t, result.IsSuccess())
		assert.Equal(t, "order-cust-123", result.AggregateID)
	})

	t.Run("Handle returns error", func(t *testing.T) {
		expectedErr := errors.New("test error")
		handler := NewCommandHandlerFunc("FailCommand", func(ctx context.Context, cmd Command) (CommandResult, error) {
			return NewErrorResult(expectedErr), expectedErr
		})

		result, err := handler.Handle(context.Background(), handlerTestCreateOrder{})

		require.Error(t, err)
		assert.True(t, result.IsError())
		assert.Equal(t, expectedErr, err)
	})
}

func TestGenericHandler(t *testing.T) {
	t.Run("creates handler for command type", func(t *testing.T) {
		handler := NewGenericHandler(func(ctx context.Context, cmd handlerTestCreateOrder) (CommandResult, error) {
			return NewSuccessResult("order-1", 1), nil
		})

		assert.Equal(t, "CreateOrder", handler.CommandType())
	})

	t.Run("Handle with correct type", func(t *testing.T) {
		handler := NewGenericHandler(func(ctx context.Context, cmd handlerTestCreateOrder) (CommandResult, error) {
			return NewSuccessResult("order-"+cmd.CustomerID, 1), nil
		})

		cmd := handlerTestCreateOrder{CustomerID: "cust-123"}
		result, err := handler.Handle(context.Background(), cmd)

		require.NoError(t, err)
		assert.True(t, result.IsSuccess())
		assert.Equal(t, "order-cust-123", result.AggregateID)
	})

	t.Run("Handle with wrong type returns error result", func(t *testing.T) {
		handler := NewGenericHandler(func(ctx context.Context, cmd handlerTestCreateOrder) (CommandResult, error) {
			return NewSuccessResult("order-1", 1), nil
		})

		wrongCmd := handlerTestAddItem{OrderID: "order-1"}
		result, err := handler.Handle(context.Background(), wrongCmd)

		// No Go error returned, but result indicates failure
		require.NoError(t, err)
		assert.True(t, result.IsError())
		assert.Contains(t, result.Error.Error(), "expected command type")
	})
}

func TestHandlerRegistry(t *testing.T) {
	t.Run("Register and Get", func(t *testing.T) {
		registry := NewHandlerRegistry()
		handler := NewCommandHandlerFunc("CreateOrder", func(ctx context.Context, cmd Command) (CommandResult, error) {
			return NewSuccessResult("", 0), nil
		})

		registry.Register(handler)

		got := registry.Get("CreateOrder")
		assert.NotNil(t, got)
		assert.Equal(t, "CreateOrder", got.CommandType())
	})

	t.Run("Get returns nil for unknown type", func(t *testing.T) {
		registry := NewHandlerRegistry()
		assert.Nil(t, registry.Get("Unknown"))
	})

	t.Run("Has", func(t *testing.T) {
		registry := NewHandlerRegistry()
		handler := NewCommandHandlerFunc("CreateOrder", func(ctx context.Context, cmd Command) (CommandResult, error) {
			return NewSuccessResult("", 0), nil
		})

		assert.False(t, registry.Has("CreateOrder"))
		registry.Register(handler)
		assert.True(t, registry.Has("CreateOrder"))
	})

	t.Run("RegisterFunc", func(t *testing.T) {
		registry := NewHandlerRegistry()
		registry.RegisterFunc("CreateOrder", func(ctx context.Context, cmd Command) (CommandResult, error) {
			return NewSuccessResult("order-1", 1), nil
		})

		assert.True(t, registry.Has("CreateOrder"))
	})

	t.Run("Remove", func(t *testing.T) {
		registry := NewHandlerRegistry()
		registry.RegisterFunc("CreateOrder", func(ctx context.Context, cmd Command) (CommandResult, error) {
			return NewSuccessResult("", 0), nil
		})

		assert.True(t, registry.Has("CreateOrder"))
		registry.Remove("CreateOrder")
		assert.False(t, registry.Has("CreateOrder"))
	})

	t.Run("Clear", func(t *testing.T) {
		registry := NewHandlerRegistry()
		registry.RegisterFunc("Cmd1", func(ctx context.Context, cmd Command) (CommandResult, error) {
			return NewSuccessResult("", 0), nil
		})
		registry.RegisterFunc("Cmd2", func(ctx context.Context, cmd Command) (CommandResult, error) {
			return NewSuccessResult("", 0), nil
		})

		assert.Equal(t, 2, registry.Count())
		registry.Clear()
		assert.Equal(t, 0, registry.Count())
	})

	t.Run("Count", func(t *testing.T) {
		registry := NewHandlerRegistry()
		assert.Equal(t, 0, registry.Count())

		registry.RegisterFunc("Cmd1", func(ctx context.Context, cmd Command) (CommandResult, error) {
			return NewSuccessResult("", 0), nil
		})
		assert.Equal(t, 1, registry.Count())

		registry.RegisterFunc("Cmd2", func(ctx context.Context, cmd Command) (CommandResult, error) {
			return NewSuccessResult("", 0), nil
		})
		assert.Equal(t, 2, registry.Count())
	})

	t.Run("CommandTypes", func(t *testing.T) {
		registry := NewHandlerRegistry()
		registry.RegisterFunc("CreateOrder", func(ctx context.Context, cmd Command) (CommandResult, error) {
			return NewSuccessResult("", 0), nil
		})
		registry.RegisterFunc("AddItem", func(ctx context.Context, cmd Command) (CommandResult, error) {
			return NewSuccessResult("", 0), nil
		})

		types := registry.CommandTypes()
		assert.Len(t, types, 2)
		assert.Contains(t, types, "CreateOrder")
		assert.Contains(t, types, "AddItem")
	})

	t.Run("Register replaces existing", func(t *testing.T) {
		registry := NewHandlerRegistry()

		handler1 := NewCommandHandlerFunc("CreateOrder", func(ctx context.Context, cmd Command) (CommandResult, error) {
			return NewSuccessResult("handler1", 1), nil
		})
		handler2 := NewCommandHandlerFunc("CreateOrder", func(ctx context.Context, cmd Command) (CommandResult, error) {
			return NewSuccessResult("handler2", 2), nil
		})

		registry.Register(handler1)
		registry.Register(handler2)

		got := registry.Get("CreateOrder")
		result, _ := got.Handle(context.Background(), handlerTestCreateOrder{})
		assert.Equal(t, "handler2", result.AggregateID)
	})

	t.Run("concurrent access", func(t *testing.T) {
		registry := NewHandlerRegistry()
		var wg sync.WaitGroup

		// Writers
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func(n int) {
				defer wg.Done()
				registry.RegisterFunc("Cmd"+string(rune('A'+n)), func(ctx context.Context, cmd Command) (CommandResult, error) {
					return NewSuccessResult("", 0), nil
				})
			}(i)
		}

		// Readers
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				registry.Get("CmdA")
				registry.Has("CmdB")
				registry.Count()
				registry.CommandTypes()
			}()
		}

		wg.Wait()
	})
}

func TestRegisterGenericHandler(t *testing.T) {
	t.Run("registers handler", func(t *testing.T) {
		registry := NewHandlerRegistry()

		RegisterGenericHandler(registry, func(ctx context.Context, cmd handlerTestCreateOrder) (CommandResult, error) {
			return NewSuccessResult("order-1", 1), nil
		})

		assert.True(t, registry.Has("CreateOrder"))
	})

	t.Run("registered handler works", func(t *testing.T) {
		registry := NewHandlerRegistry()

		RegisterGenericHandler(registry, func(ctx context.Context, cmd handlerTestCreateOrder) (CommandResult, error) {
			return NewSuccessResult("order-"+cmd.CustomerID, 1), nil
		})

		handler := registry.Get("CreateOrder")
		require.NotNil(t, handler)

		result, err := handler.Handle(context.Background(), handlerTestCreateOrder{CustomerID: "123"})
		require.NoError(t, err)
		assert.Equal(t, "order-123", result.AggregateID)
	})
}

func TestSimpleDispatcher(t *testing.T) {
	t.Run("dispatches to handler", func(t *testing.T) {
		registry := NewHandlerRegistry()
		registry.RegisterFunc("CreateOrder", func(ctx context.Context, cmd Command) (CommandResult, error) {
			c := cmd.(handlerTestCreateOrder)
			return NewSuccessResult("order-"+c.CustomerID, 1), nil
		})

		dispatcher := NewSimpleDispatcher(registry)

		result, err := dispatcher.Dispatch(context.Background(), handlerTestCreateOrder{CustomerID: "cust-1"})

		require.NoError(t, err)
		assert.True(t, result.IsSuccess())
		assert.Equal(t, "order-cust-1", result.AggregateID)
	})

	t.Run("returns error for nil command", func(t *testing.T) {
		registry := NewHandlerRegistry()
		dispatcher := NewSimpleDispatcher(registry)

		result, err := dispatcher.Dispatch(context.Background(), nil)

		require.ErrorIs(t, err, ErrNilCommand)
		assert.True(t, result.IsError())
	})

	t.Run("returns error for unknown command", func(t *testing.T) {
		registry := NewHandlerRegistry()
		dispatcher := NewSimpleDispatcher(registry)

		result, err := dispatcher.Dispatch(context.Background(), handlerTestCreateOrder{})

		require.ErrorIs(t, err, ErrHandlerNotFound)
		assert.True(t, result.IsError())

		var handlerErr *HandlerNotFoundError
		require.ErrorAs(t, err, &handlerErr)
		assert.Equal(t, "CreateOrder", handlerErr.CommandType)
	})

	t.Run("implements CommandDispatcher", func(t *testing.T) {
		registry := NewHandlerRegistry()
		var dispatcher CommandDispatcher = NewSimpleDispatcher(registry)
		assert.NotNil(t, dispatcher)
	})
}

func TestGetCommandType(t *testing.T) {
	t.Run("returns type name", func(t *testing.T) {
		cmd := handlerTestCreateOrder{CustomerID: "cust-1"}
		assert.Equal(t, "handlerTestCreateOrder", GetCommandType(cmd))
	})

	t.Run("handles pointer", func(t *testing.T) {
		cmd := &handlerTestCreateOrder{CustomerID: "cust-1"}
		assert.Equal(t, "handlerTestCreateOrder", GetCommandType(cmd))
	})

	t.Run("returns empty for nil", func(t *testing.T) {
		assert.Empty(t, GetCommandType(nil))
	})
}

func TestHandlerNotFoundError(t *testing.T) {
	t.Run("Error message", func(t *testing.T) {
		err := NewHandlerNotFoundError("CreateOrder")
		assert.Contains(t, err.Error(), "CreateOrder")
		assert.Contains(t, err.Error(), "no handler registered")
	})

	t.Run("Is ErrHandlerNotFound", func(t *testing.T) {
		err := NewHandlerNotFoundError("CreateOrder")
		assert.ErrorIs(t, err, ErrHandlerNotFound)
	})

	t.Run("Unwrap", func(t *testing.T) {
		err := NewHandlerNotFoundError("CreateOrder")
		assert.Equal(t, ErrHandlerNotFound, err.Unwrap())
	})
}

func TestPanicError(t *testing.T) {
	t.Run("Error message", func(t *testing.T) {
		err := NewPanicError("CreateOrder", "something bad", "stack trace")
		assert.Contains(t, err.Error(), "CreateOrder")
		assert.Contains(t, err.Error(), "panicked")
		assert.Contains(t, err.Error(), "something bad")
	})

	t.Run("Is ErrHandlerPanicked", func(t *testing.T) {
		err := NewPanicError("CreateOrder", "panic", "")
		assert.ErrorIs(t, err, ErrHandlerPanicked)
	})

	t.Run("Unwrap", func(t *testing.T) {
		err := NewPanicError("CreateOrder", "panic", "")
		assert.Equal(t, ErrHandlerPanicked, err.Unwrap())
	})

	t.Run("Stack is captured", func(t *testing.T) {
		err := NewPanicError("CreateOrder", "panic", "full stack trace here")
		assert.Equal(t, "full stack trace here", err.Stack)
	})
}
