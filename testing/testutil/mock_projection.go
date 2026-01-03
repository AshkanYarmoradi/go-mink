// Package testutil provides test utilities and fixtures for testing go-mink applications.
package testutil

import (
	"context"
	"errors"

	"github.com/AshkanYarmoradi/go-mink"
)

// MockProjection is a mock implementation of mink.InlineProjection for testing.
type MockProjection struct {
	ProjectionName string
	EventTypes     []string
	ApplyErr       error
	Applied        []mink.StoredEvent
}

// Name implements mink.InlineProjection.
func (p *MockProjection) Name() string {
	return p.ProjectionName
}

// HandledEvents implements mink.InlineProjection.
func (p *MockProjection) HandledEvents() []string {
	return p.EventTypes
}

// Apply implements mink.InlineProjection.
func (p *MockProjection) Apply(ctx context.Context, event mink.StoredEvent) error {
	p.Applied = append(p.Applied, event)
	return p.ApplyErr
}

// Ensure MockProjection implements mink.InlineProjection.
var _ mink.InlineProjection = (*MockProjection)(nil)

// TestCommand is a mock command for testing middleware.
type TestCommand struct {
	ID         string
	ShouldFail bool
}

// AggregateID implements mink.Command.
func (c *TestCommand) AggregateID() string { return c.ID }

// AggregateType implements mink.Command.
func (c *TestCommand) AggregateType() string { return "Test" }

// CommandType implements mink.Command.
func (c *TestCommand) CommandType() string { return "TestCommand" }

// Validate implements mink.Command.
func (c *TestCommand) Validate() error {
	if c.ShouldFail {
		return errors.New("validation failed")
	}
	return nil
}
