package mink

import (
	"context"
	"sync"
	"sync/atomic"
)

// CommandBus orchestrates command dispatching with middleware support.
// It routes commands to their handlers through a configurable middleware pipeline.
type CommandBus struct {
	registry   *HandlerRegistry
	middleware []Middleware
	closed     atomic.Bool
	mu         sync.RWMutex
}

// CommandBusOption configures a CommandBus.
type CommandBusOption func(*CommandBus)

// WithMiddleware adds middleware to the command bus.
func WithMiddleware(middleware ...Middleware) CommandBusOption {
	return func(b *CommandBus) {
		b.middleware = append(b.middleware, middleware...)
	}
}

// WithHandlerRegistry sets a custom handler registry.
func WithHandlerRegistry(registry *HandlerRegistry) CommandBusOption {
	return func(b *CommandBus) {
		b.registry = registry
	}
}

// NewCommandBus creates a new CommandBus with the given options.
func NewCommandBus(opts ...CommandBusOption) *CommandBus {
	bus := &CommandBus{
		registry:   NewHandlerRegistry(),
		middleware: make([]Middleware, 0),
	}

	for _, opt := range opts {
		opt(bus)
	}

	return bus
}

// Register adds a handler to the command bus.
func (b *CommandBus) Register(handler CommandHandler) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.registry.Register(handler)
}

// RegisterFunc registers a handler function for a command type.
func (b *CommandBus) RegisterFunc(cmdType string, fn func(ctx context.Context, cmd Command) (CommandResult, error)) {
	b.Register(NewCommandHandlerFunc(cmdType, fn))
}

// Use adds middleware to the command bus.
// Middleware is executed in the order it was added.
func (b *CommandBus) Use(middleware ...Middleware) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.middleware = append(b.middleware, middleware...)
}

// Dispatch sends a command through the middleware pipeline to its handler.
func (b *CommandBus) Dispatch(ctx context.Context, cmd Command) (CommandResult, error) {
	if b.closed.Load() {
		return NewErrorResult(ErrCommandBusClosed), ErrCommandBusClosed
	}

	if cmd == nil {
		return NewErrorResult(ErrNilCommand), ErrNilCommand
	}

	b.mu.RLock()
	handler := b.registry.Get(cmd.CommandType())
	middleware := make([]Middleware, len(b.middleware))
	copy(middleware, b.middleware)
	b.mu.RUnlock()

	if handler == nil {
		err := NewHandlerNotFoundError(cmd.CommandType())
		return NewErrorResult(err), err
	}

	// Build the middleware chain
	finalHandler := func(ctx context.Context, cmd Command) (CommandResult, error) {
		return handler.Handle(ctx, cmd)
	}

	// Apply middleware in reverse order so they execute in the order they were added
	chain := finalHandler
	for i := len(middleware) - 1; i >= 0; i-- {
		chain = middleware[i](chain)
	}

	return chain(ctx, cmd)
}

// DispatchAsync sends a command asynchronously and returns immediately.
// The result can be retrieved through the returned channel.
func (b *CommandBus) DispatchAsync(ctx context.Context, cmd Command) <-chan DispatchResult {
	resultCh := make(chan DispatchResult, 1)

	go func() {
		defer close(resultCh)

		result, err := b.Dispatch(ctx, cmd)
		resultCh <- DispatchResult{
			CommandResult: result,
			Error:         err,
		}
	}()

	return resultCh
}

// DispatchAll dispatches multiple commands and returns all results.
// Commands are dispatched sequentially in order.
func (b *CommandBus) DispatchAll(ctx context.Context, cmds ...Command) ([]DispatchResult, error) {
	results := make([]DispatchResult, len(cmds))

	for i, cmd := range cmds {
		result, err := b.Dispatch(ctx, cmd)
		results[i] = DispatchResult{
			CommandResult: result,
			Error:         err,
		}

		// Stop on first error if context is done
		if ctx.Err() != nil {
			return results[:i+1], ctx.Err()
		}
	}

	return results, nil
}

// HasHandler returns true if a handler is registered for the command type.
func (b *CommandBus) HasHandler(cmdType string) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.registry.Has(cmdType)
}

// HandlerCount returns the number of registered handlers.
func (b *CommandBus) HandlerCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.registry.Count()
}

// MiddlewareCount returns the number of registered middleware.
func (b *CommandBus) MiddlewareCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.middleware)
}

// Close closes the command bus, preventing further dispatch operations.
func (b *CommandBus) Close() error {
	b.closed.Store(true)
	return nil
}

// IsClosed returns true if the command bus has been closed.
func (b *CommandBus) IsClosed() bool {
	return b.closed.Load()
}

// DispatchResult contains the result of an asynchronous dispatch operation.
type DispatchResult struct {
	CommandResult
	Error error
}

// IsSuccess returns true if the dispatch was successful.
func (r DispatchResult) IsSuccess() bool {
	return r.Error == nil && r.CommandResult.IsSuccess()
}

// MiddlewareFunc is the function signature for command middleware.
type MiddlewareFunc func(ctx context.Context, cmd Command) (CommandResult, error)

// Middleware wraps a handler function with additional functionality.
type Middleware func(next MiddlewareFunc) MiddlewareFunc

// ChainMiddleware creates a single middleware from multiple middleware.
func ChainMiddleware(middleware ...Middleware) Middleware {
	return func(next MiddlewareFunc) MiddlewareFunc {
		for i := len(middleware) - 1; i >= 0; i-- {
			next = middleware[i](next)
		}
		return next
	}
}
