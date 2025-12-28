package mink

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// E2E test commands

type E2ECreateOrderCommand struct {
	CommandBase
	CustomerID string `json:"customerId"`
	RequestID  string `json:"requestId"` // For idempotency
}

func (c E2ECreateOrderCommand) CommandType() string { return "E2ECreateOrder" }

func (c E2ECreateOrderCommand) Validate() error {
	if c.CustomerID == "" {
		return NewValidationError("E2ECreateOrder", "CustomerID", "is required")
	}
	return nil
}

func (c E2ECreateOrderCommand) IdempotencyKey() string {
	return c.RequestID
}

type E2EAddItemCommand struct {
	CommandBase
	OrderID  string  `json:"orderId"`
	SKU      string  `json:"sku"`
	Quantity int     `json:"quantity"`
	Price    float64 `json:"price"`
}

func (c E2EAddItemCommand) CommandType() string { return "E2EAddItem" }
func (c E2EAddItemCommand) AggregateID() string { return c.OrderID }
func (c E2EAddItemCommand) Validate() error {
	multiErr := NewMultiValidationError("E2EAddItem")
	if c.OrderID == "" {
		multiErr.AddField("OrderID", "is required")
	}
	if c.SKU == "" {
		multiErr.AddField("SKU", "is required")
	}
	if c.Quantity <= 0 {
		multiErr.AddField("Quantity", "must be positive")
	}
	if c.Price < 0 {
		multiErr.AddField("Price", "cannot be negative")
	}
	if multiErr.HasErrors() {
		return multiErr
	}
	return nil
}

// E2E mock idempotency store for testing
type e2eMockIdempotencyStore struct {
	records map[string]*IdempotencyRecord
}

func newE2EMockIdempotencyStore() *e2eMockIdempotencyStore {
	return &e2eMockIdempotencyStore{
		records: make(map[string]*IdempotencyRecord),
	}
}

func (s *e2eMockIdempotencyStore) Exists(ctx context.Context, key string) (bool, error) {
	_, ok := s.records[key]
	return ok, nil
}

func (s *e2eMockIdempotencyStore) Store(ctx context.Context, record *IdempotencyRecord) error {
	s.records[record.Key] = record
	return nil
}

func (s *e2eMockIdempotencyStore) Get(ctx context.Context, key string) (*IdempotencyRecord, error) {
	return s.records[key], nil
}

func (s *e2eMockIdempotencyStore) Delete(ctx context.Context, key string) error {
	delete(s.records, key)
	return nil
}

func (s *e2eMockIdempotencyStore) Cleanup(ctx context.Context, olderThan time.Duration) (int64, error) {
	var count int64
	cutoff := time.Now().Add(-olderThan)
	for key, record := range s.records {
		if record.ProcessedAt.Before(cutoff) {
			delete(s.records, key)
			count++
		}
	}
	return count, nil
}

func TestE2E_CommandBus_FullFlow(t *testing.T) {
	ctx := context.Background()

	// Create command bus with middleware
	bus := NewCommandBus()

	// Track middleware execution order
	var middlewareOrder []string

	// Add logging middleware (simplified)
	bus.Use(func(next MiddlewareFunc) MiddlewareFunc {
		return func(ctx context.Context, cmd Command) (CommandResult, error) {
			middlewareOrder = append(middlewareOrder, "logging-start")
			result, err := next(ctx, cmd)
			middlewareOrder = append(middlewareOrder, "logging-end")
			return result, err
		}
	})

	// Add validation middleware
	bus.Use(ValidationMiddleware())

	// Add recovery middleware
	panicRecovered := false
	bus.Use(RecoveryMiddleware())
	_ = panicRecovered // Suppress unused variable warning

	// Track processed orders
	processedOrders := make(map[string]int)

	// Register create order handler
	bus.RegisterFunc("E2ECreateOrder", func(ctx context.Context, cmd Command) (CommandResult, error) {
		c := cmd.(E2ECreateOrderCommand)
		orderID := "order-" + c.CustomerID
		processedOrders[orderID]++
		return NewSuccessResult(orderID, 1), nil
	})

	// Register add item handler
	bus.RegisterFunc("E2EAddItem", func(ctx context.Context, cmd Command) (CommandResult, error) {
		c := cmd.(E2EAddItemCommand)
		processedOrders[c.OrderID]++
		return NewSuccessResult(c.OrderID, int64(processedOrders[c.OrderID])), nil
	})

	t.Run("successful command flow", func(t *testing.T) {
		middlewareOrder = nil

		cmd := E2ECreateOrderCommand{
			CustomerID: "customer-1",
			RequestID:  "req-1",
		}
		cmd.CorrelationID = "corr-123"

		result, err := bus.Dispatch(ctx, cmd)

		require.NoError(t, err)
		assert.True(t, result.IsSuccess())
		assert.Equal(t, "order-customer-1", result.AggregateID)
		assert.Equal(t, int64(1), result.Version)

		// Verify middleware execution order
		assert.Contains(t, middlewareOrder, "logging-start")
		assert.Contains(t, middlewareOrder, "logging-end")
	})

	t.Run("validation failure", func(t *testing.T) {
		cmd := E2ECreateOrderCommand{
			CustomerID: "", // Invalid
		}

		result, err := bus.Dispatch(ctx, cmd)

		assert.Error(t, err)
		assert.True(t, result.IsError())
		assert.ErrorIs(t, err, ErrValidationFailed)
	})

	t.Run("multiple validation errors", func(t *testing.T) {
		cmd := E2EAddItemCommand{
			OrderID:  "", // Invalid
			SKU:      "", // Invalid
			Quantity: 0,  // Invalid
			Price:    -1, // Invalid
		}

		result, err := bus.Dispatch(ctx, cmd)

		assert.Error(t, err)
		assert.True(t, result.IsError())

		var multiErr *MultiValidationError
		assert.True(t, errors.As(err, &multiErr))
		assert.Len(t, multiErr.Errors, 4)
	})

	t.Run("handler not found", func(t *testing.T) {
		uc := unknownCommand{}

		result, err := bus.Dispatch(ctx, uc)

		assert.Error(t, err)
		assert.True(t, result.IsError())
		assert.ErrorIs(t, err, ErrHandlerNotFound)
	})
}

func TestE2E_CommandBus_WithIdempotency(t *testing.T) {
	ctx := context.Background()

	// Create idempotency store
	idempotencyStore := newE2EMockIdempotencyStore()

	// Create command bus
	bus := NewCommandBus()

	// Add idempotency middleware
	config := DefaultIdempotencyConfig(idempotencyStore)
	bus.Use(IdempotencyMiddleware(config))

	// Track handler calls
	handlerCalls := 0

	// Register handler
	bus.RegisterFunc("E2ECreateOrder", func(ctx context.Context, cmd Command) (CommandResult, error) {
		handlerCalls++
		c := cmd.(E2ECreateOrderCommand)
		return NewSuccessResult("order-"+c.CustomerID, int64(handlerCalls)), nil
	})

	t.Run("first call executes handler", func(t *testing.T) {
		handlerCalls = 0

		cmd := E2ECreateOrderCommand{
			CustomerID: "customer-idempotent",
			RequestID:  "idempotent-key-1",
		}

		result, err := bus.Dispatch(ctx, cmd)

		require.NoError(t, err)
		assert.True(t, result.IsSuccess())
		assert.Equal(t, 1, handlerCalls)
		assert.Equal(t, "order-customer-idempotent", result.AggregateID)
	})

	t.Run("second call returns cached result", func(t *testing.T) {
		// handlerCalls should still be 1 from previous test

		cmd := E2ECreateOrderCommand{
			CustomerID: "customer-idempotent",
			RequestID:  "idempotent-key-1",
		}

		result, err := bus.Dispatch(ctx, cmd)

		require.NoError(t, err)
		assert.True(t, result.IsSuccess())
		assert.Equal(t, 1, handlerCalls) // Not incremented
		assert.Equal(t, "order-customer-idempotent", result.AggregateID)
	})

	t.Run("different key executes handler", func(t *testing.T) {
		cmd := E2ECreateOrderCommand{
			CustomerID: "customer-idempotent-2",
			RequestID:  "idempotent-key-2",
		}

		result, err := bus.Dispatch(ctx, cmd)

		require.NoError(t, err)
		assert.True(t, result.IsSuccess())
		assert.Equal(t, 2, handlerCalls) // Incremented
	})
}

func TestE2E_CommandBus_ConcurrentDispatch(t *testing.T) {
	ctx := context.Background()

	bus := NewCommandBus()
	bus.Use(ValidationMiddleware())

	var counter int

	bus.RegisterFunc("E2ECreateOrder", func(ctx context.Context, cmd Command) (CommandResult, error) {
		counter++
		return NewSuccessResult("order-1", int64(counter)), nil
	})

	// Dispatch multiple commands concurrently
	results := make(chan CommandResult, 10)
	for i := 0; i < 10; i++ {
		go func(i int) {
			cmd := E2ECreateOrderCommand{
				CustomerID: "customer-1",
				RequestID:  "req-" + string(rune('0'+i)),
			}
			result, _ := bus.Dispatch(ctx, cmd)
			results <- result
		}(i)
	}

	// Collect results
	for i := 0; i < 10; i++ {
		result := <-results
		assert.True(t, result.IsSuccess())
	}
}

func TestE2E_CommandBus_DispatchAsync(t *testing.T) {
	ctx := context.Background()

	bus := NewCommandBus()

	var processed bool
	bus.RegisterFunc("E2ECreateOrder", func(ctx context.Context, cmd Command) (CommandResult, error) {
		processed = true
		return NewSuccessResult("order-1", 1), nil
	})

	cmd := E2ECreateOrderCommand{
		CustomerID: "customer-async",
		RequestID:  "req-async",
	}

	resultChan := bus.DispatchAsync(ctx, cmd)

	// Wait for result with timeout
	select {
	case result := <-resultChan:
		assert.True(t, result.IsSuccess())
		assert.True(t, processed)
	case <-time.After(time.Second):
		t.Fatal("Timeout waiting for async result")
	}
}

func TestE2E_CommandBus_DispatchAll(t *testing.T) {
	ctx := context.Background()

	bus := NewCommandBus()
	bus.Use(ValidationMiddleware())

	var processedCustomers []string

	bus.RegisterFunc("E2ECreateOrder", func(ctx context.Context, cmd Command) (CommandResult, error) {
		c := cmd.(E2ECreateOrderCommand)
		processedCustomers = append(processedCustomers, c.CustomerID)
		return NewSuccessResult("order-"+c.CustomerID, 1), nil
	})

	commands := []Command{
		E2ECreateOrderCommand{CustomerID: "c1", RequestID: "r1"},
		E2ECreateOrderCommand{CustomerID: "c2", RequestID: "r2"},
		E2ECreateOrderCommand{CustomerID: "c3", RequestID: "r3"},
	}

	results, err := bus.DispatchAll(ctx, commands...)

	require.NoError(t, err)
	assert.Len(t, results, 3)

	for i, result := range results {
		assert.True(t, result.IsSuccess(), "Result %d should be success", i)
		assert.Nil(t, result.Error, "Error %d should be nil", i)
	}
}

func TestE2E_MiddlewarePipeline(t *testing.T) {
	ctx := context.Background()

	var executionOrder []string

	bus := NewCommandBus()

	// Add multiple middleware in order
	bus.Use(func(next MiddlewareFunc) MiddlewareFunc {
		return func(ctx context.Context, cmd Command) (CommandResult, error) {
			executionOrder = append(executionOrder, "mw1-before")
			result, err := next(ctx, cmd)
			executionOrder = append(executionOrder, "mw1-after")
			return result, err
		}
	})

	bus.Use(func(next MiddlewareFunc) MiddlewareFunc {
		return func(ctx context.Context, cmd Command) (CommandResult, error) {
			executionOrder = append(executionOrder, "mw2-before")
			result, err := next(ctx, cmd)
			executionOrder = append(executionOrder, "mw2-after")
			return result, err
		}
	})

	bus.Use(func(next MiddlewareFunc) MiddlewareFunc {
		return func(ctx context.Context, cmd Command) (CommandResult, error) {
			executionOrder = append(executionOrder, "mw3-before")
			result, err := next(ctx, cmd)
			executionOrder = append(executionOrder, "mw3-after")
			return result, err
		}
	})

	bus.RegisterFunc("E2ECreateOrder", func(ctx context.Context, cmd Command) (CommandResult, error) {
		executionOrder = append(executionOrder, "handler")
		return NewSuccessResult("order-1", 1), nil
	})

	cmd := E2ECreateOrderCommand{CustomerID: "c1", RequestID: "r1"}
	_, _ = bus.Dispatch(ctx, cmd)

	// Verify onion middleware pattern
	expected := []string{
		"mw1-before",
		"mw2-before",
		"mw3-before",
		"handler",
		"mw3-after",
		"mw2-after",
		"mw1-after",
	}
	assert.Equal(t, expected, executionOrder)
}

func TestE2E_HandlerRegistry(t *testing.T) {
	registry := NewHandlerRegistry()

	// Register handler
	registry.RegisterFunc("TestCommand", func(ctx context.Context, cmd Command) (CommandResult, error) {
		return NewSuccessResult("agg-1", 1), nil
	})

	t.Run("get registered handler", func(t *testing.T) {
		h := registry.Get("TestCommand")
		assert.NotNil(t, h)
	})

	t.Run("get unregistered handler", func(t *testing.T) {
		h := registry.Get("UnknownCommand")
		assert.Nil(t, h)
	})

	t.Run("list handlers", func(t *testing.T) {
		has := registry.Has("TestCommand")
		assert.True(t, has)
	})

	t.Run("unregister handler", func(t *testing.T) {
		registry.Remove("TestCommand")
		h := registry.Get("TestCommand")
		assert.Nil(t, h)
	})
}

// unknownCommand for handler not found test
type unknownCommand struct {
	CommandBase
}

func (c unknownCommand) CommandType() string { return "UnknownCommand" }
func (c unknownCommand) Validate() error     { return nil }
