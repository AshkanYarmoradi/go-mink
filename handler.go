package mink

import (
	"context"
	"fmt"
	"reflect"
	"sync"
)

// CommandHandler is the interface for handling a specific command type.
// Handlers contain the business logic for processing commands.
type CommandHandler interface {
	// CommandType returns the type of command this handler processes.
	CommandType() string

	// Handle processes the command and returns a result.
	Handle(ctx context.Context, cmd Command) (CommandResult, error)
}

// CommandHandlerFunc is a function type that implements CommandHandler.
type CommandHandlerFunc struct {
	cmdType string
	fn      func(ctx context.Context, cmd Command) (CommandResult, error)
}

// NewCommandHandlerFunc creates a new CommandHandlerFunc.
func NewCommandHandlerFunc(cmdType string, fn func(ctx context.Context, cmd Command) (CommandResult, error)) *CommandHandlerFunc {
	return &CommandHandlerFunc{
		cmdType: cmdType,
		fn:      fn,
	}
}

// CommandType returns the command type this handler processes.
func (h *CommandHandlerFunc) CommandType() string {
	return h.cmdType
}

// Handle processes the command.
func (h *CommandHandlerFunc) Handle(ctx context.Context, cmd Command) (CommandResult, error) {
	return h.fn(ctx, cmd)
}

// GenericHandler is a type-safe command handler for a specific command type.
// Use this to create handlers with compile-time type checking.
type GenericHandler[C Command] struct {
	handler func(ctx context.Context, cmd C) (CommandResult, error)
	cmdType string
}

// NewGenericHandler creates a new GenericHandler for the specified command type.
func NewGenericHandler[C Command](handler func(ctx context.Context, cmd C) (CommandResult, error)) *GenericHandler[C] {
	var zero C
	return &GenericHandler[C]{
		handler: handler,
		cmdType: zero.CommandType(),
	}
}

// CommandType returns the command type this handler processes.
func (h *GenericHandler[C]) CommandType() string {
	return h.cmdType
}

// Handle processes the command with type checking.
func (h *GenericHandler[C]) Handle(ctx context.Context, cmd Command) (CommandResult, error) {
	typedCmd, ok := cmd.(C)
	if !ok {
		return NewErrorResult(fmt.Errorf("mink: expected command type %T, got %T", *new(C), cmd)), nil
	}
	return h.handler(ctx, typedCmd)
}

// AggregateHandler is a handler that works with aggregates and an event store.
// It loads the aggregate, executes the command, and saves the results.
type AggregateHandler[C AggregateCommand, A Aggregate] struct {
	store     *EventStore
	factory   func(id string) A
	executor  func(ctx context.Context, agg A, cmd C) error
	newIDFunc func() string
}

// AggregateHandlerConfig configures an AggregateHandler.
type AggregateHandlerConfig[C AggregateCommand, A Aggregate] struct {
	Store     *EventStore
	Factory   func(id string) A
	Executor  func(ctx context.Context, agg A, cmd C) error
	NewIDFunc func() string
}

// NewAggregateHandler creates a new AggregateHandler.
func NewAggregateHandler[C AggregateCommand, A Aggregate](config AggregateHandlerConfig[C, A]) *AggregateHandler[C, A] {
	return &AggregateHandler[C, A]{
		store:     config.Store,
		factory:   config.Factory,
		executor:  config.Executor,
		newIDFunc: config.NewIDFunc,
	}
}

// CommandType returns the command type this handler processes.
func (h *AggregateHandler[C, A]) CommandType() string {
	var zero C
	return zero.CommandType()
}

// Handle loads the aggregate, executes the command, and saves the aggregate.
func (h *AggregateHandler[C, A]) Handle(ctx context.Context, cmd Command) (CommandResult, error) {
	typedCmd, ok := cmd.(C)
	if !ok {
		return NewErrorResult(fmt.Errorf("mink: expected command type %T, got %T", *new(C), cmd)), nil
	}

	// Determine aggregate ID
	aggID := typedCmd.AggregateID()
	isNew := aggID == ""
	if isNew {
		if h.newIDFunc != nil {
			aggID = h.newIDFunc()
		} else {
			return NewErrorResult(fmt.Errorf("mink: command has no aggregate ID and no ID generator configured")), nil
		}
	}

	// Create or load aggregate
	agg := h.factory(aggID)
	if !isNew {
		if err := h.store.LoadAggregate(ctx, agg); err != nil {
			return NewErrorResult(fmt.Errorf("mink: failed to load aggregate: %w", err)), nil
		}
	}

	// Execute command
	if err := h.executor(ctx, agg, typedCmd); err != nil {
		return NewErrorResult(err), nil
	}

	// Save aggregate
	if err := h.store.SaveAggregate(ctx, agg); err != nil {
		return NewErrorResult(fmt.Errorf("mink: failed to save aggregate: %w", err)), nil
	}

	return NewSuccessResult(agg.AggregateID(), agg.Version()), nil
}

// HandlerRegistry manages command handler registration and lookup.
type HandlerRegistry struct {
	mu       sync.RWMutex
	handlers map[string]CommandHandler
}

// NewHandlerRegistry creates a new HandlerRegistry.
func NewHandlerRegistry() *HandlerRegistry {
	return &HandlerRegistry{
		handlers: make(map[string]CommandHandler),
	}
}

// Register adds a handler for a command type.
// If a handler is already registered for this type, it will be replaced.
func (r *HandlerRegistry) Register(handler CommandHandler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.handlers[handler.CommandType()] = handler
}

// RegisterFunc registers a handler function for a command type.
func (r *HandlerRegistry) RegisterFunc(cmdType string, fn func(ctx context.Context, cmd Command) (CommandResult, error)) {
	r.Register(NewCommandHandlerFunc(cmdType, fn))
}

// Get returns the handler for a command type.
// Returns nil if no handler is registered.
func (r *HandlerRegistry) Get(cmdType string) CommandHandler {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.handlers[cmdType]
}

// Has returns true if a handler is registered for the command type.
func (r *HandlerRegistry) Has(cmdType string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.handlers[cmdType]
	return ok
}

// Remove removes a handler for a command type.
func (r *HandlerRegistry) Remove(cmdType string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.handlers, cmdType)
}

// Clear removes all handlers.
func (r *HandlerRegistry) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.handlers = make(map[string]CommandHandler)
}

// Count returns the number of registered handlers.
func (r *HandlerRegistry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.handlers)
}

// CommandTypes returns all registered command types.
func (r *HandlerRegistry) CommandTypes() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	types := make([]string, 0, len(r.handlers))
	for t := range r.handlers {
		types = append(types, t)
	}
	return types
}

// RegisterGenericHandler is a convenience function to register a generic handler.
func RegisterGenericHandler[C Command](registry *HandlerRegistry, handler func(ctx context.Context, cmd C) (CommandResult, error)) {
	registry.Register(NewGenericHandler(handler))
}

// CommandDispatcher can dispatch commands to handlers.
type CommandDispatcher interface {
	// Dispatch sends a command to its handler and returns the result.
	Dispatch(ctx context.Context, cmd Command) (CommandResult, error)
}

// SimpleDispatcher is a basic dispatcher that forwards commands to handlers.
type SimpleDispatcher struct {
	registry *HandlerRegistry
}

// NewSimpleDispatcher creates a new SimpleDispatcher.
func NewSimpleDispatcher(registry *HandlerRegistry) *SimpleDispatcher {
	return &SimpleDispatcher{registry: registry}
}

// Dispatch sends a command to its handler.
func (d *SimpleDispatcher) Dispatch(ctx context.Context, cmd Command) (CommandResult, error) {
	if cmd == nil {
		return NewErrorResult(ErrNilCommand), ErrNilCommand
	}

	handler := d.registry.Get(cmd.CommandType())
	if handler == nil {
		err := NewHandlerNotFoundError(cmd.CommandType())
		return NewErrorResult(err), err
	}

	return handler.Handle(ctx, cmd)
}

// GetCommandType returns the type name of a command using reflection.
// This is useful for commands that don't embed CommandBase.
func GetCommandType(cmd interface{}) string {
	if cmd == nil {
		return ""
	}
	t := reflect.TypeOf(cmd)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return t.Name()
}
