# Contributing to go-mink

Thank you for your interest in contributing to go-mink! This document provides guidelines for contributing to the project.

## Table of Contents

- [Code of Conduct](#code-of-conduct)
- [Getting Started](#getting-started)
- [Development Setup](#development-setup)
- [Making Changes](#making-changes)
- [Testing](#testing)
- [Pull Request Process](#pull-request-process)
- [Style Guidelines](#style-guidelines)

## Code of Conduct

This project adheres to a Code of Conduct that all contributors are expected to follow. Please be respectful and constructive in all interactions.

## Getting Started

### Prerequisites

- Go 1.21 or later
- PostgreSQL 14+ (for integration tests)
- Docker (optional, for test containers)
- Git

### Fork and Clone

```bash
# Fork the repository on GitHub, then clone your fork
git clone https://github.com/YOUR_USERNAME/go-mink.git
cd go-mink

# Add upstream remote
git remote add upstream https://github.com/AshkanYarmoradi/go-mink.git
```

## Development Setup

### Install Dependencies

```bash
go mod download
```

### Set Up Test Database (PostgreSQL)

```bash
# Using Docker
docker run -d \
  --name mink-postgres \
  -e POSTGRES_USER=mink \
  -e POSTGRES_PASSWORD=mink \
  -e POSTGRES_DB=mink_test \
  -p 5432:5432 \
  postgres:16

# Set environment variable
export TEST_DATABASE_URL="postgres://mink:mink@localhost:5432/mink_test?sslmode=disable"
```

### Run Tests

```bash
# Unit tests only (fast)
go test -short ./...

# All tests including integration
go test ./...

# With coverage
go test -coverprofile=coverage.out ./...
go tool cover -html=coverage.out
```

## Making Changes

### Branch Naming

```
feature/short-description   # New features
fix/issue-description       # Bug fixes
docs/what-changed          # Documentation updates
refactor/what-changed      # Code refactoring
```

### Commit Messages

Follow conventional commits format:

```
type(scope): brief description

Longer explanation if needed.

Closes #123
```

Types: `feat`, `fix`, `docs`, `style`, `refactor`, `test`, `chore`

Examples:
```
feat(eventstore): add PostgreSQL adapter
fix(projection): handle nil events gracefully
docs(readme): add quick start example
test(aggregate): add table-driven tests for Order
```

## Testing

### Test Organization

```
package_test.go          # Unit tests
package_integration_test.go  # Integration tests (require DB)
```

### Test Requirements

1. **Unit Tests**: Required for all new code
   - No external dependencies
   - Fast execution (< 100ms per test)
   - Table-driven format preferred

2. **Integration Tests**: Required for adapters
   - Use `testing.Short()` skip pattern
   - Clean up test data
   - Use test containers when possible

3. **BDD Tests**: Recommended for aggregate behavior
   - Use Given/When/Then format
   - Focus on business scenarios

### Example Test

```go
func TestEventStore_Append(t *testing.T) {
    tests := []struct {
        name            string
        streamID        string
        events          []mink.EventData
        expectedVersion int64
        wantErr         error
    }{
        {
            name:            "append to new stream",
            streamID:        "test-123",
            events:          []mink.EventData{{Type: "TestEvent"}},
            expectedVersion: mink.NoStream,
            wantErr:         nil,
        },
        {
            name:            "concurrency conflict",
            streamID:        "existing-stream",
            events:          []mink.EventData{{Type: "TestEvent"}},
            expectedVersion: 0,
            wantErr:         mink.ErrConcurrencyConflict,
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            store := setupTestStore(t)
            
            _, err := store.Append(context.Background(), 
                tt.streamID, tt.events, tt.expectedVersion)
            
            if tt.wantErr != nil {
                assert.ErrorIs(t, err, tt.wantErr)
            } else {
                assert.NoError(t, err)
            }
        })
    }
}
```

## Pull Request Process

### Before Submitting

1. [ ] Sync with upstream main branch
2. [ ] All tests pass locally
3. [ ] New code has tests
4. [ ] Documentation updated if needed
5. [ ] Code follows style guidelines
6. [ ] Commit messages follow convention

### PR Template

```markdown
## Summary
Brief description of changes.

## Type of Change
- [ ] Bug fix (non-breaking change fixing an issue)
- [ ] New feature (non-breaking change adding functionality)
- [ ] Breaking change (fix or feature that would break existing functionality)
- [ ] Documentation update

## Changes Made
- Change 1
- Change 2

## Testing
Describe testing performed.

## Related Issues
Closes #123

## Checklist
- [ ] My code follows the project style guidelines
- [ ] I have performed a self-review
- [ ] I have added tests proving my fix/feature works
- [ ] New and existing tests pass locally
- [ ] I have updated documentation as needed
```

### Review Process

1. Submit PR against `main` branch
2. CI checks must pass
3. At least one maintainer approval required
4. Address review feedback
5. Squash and merge

## Style Guidelines

### Go Code Style

```go
// Package documentation at the top
// Package mink provides event sourcing primitives.
package mink

// Exported types have documentation
// EventStore manages event persistence and aggregate lifecycle.
type EventStore struct {
    adapter EventStoreAdapter
    serializer Serializer
}

// Constructor functions use New prefix
func NewEventStore(adapter EventStoreAdapter, opts ...Option) *EventStore {
    es := &EventStore{
        adapter: adapter,
        serializer: JSONSerializer{},
    }
    for _, opt := range opts {
        opt(es)
    }
    return es
}

// Option pattern for configuration
type Option func(*EventStore)

func WithSerializer(s Serializer) Option {
    return func(es *EventStore) {
        es.serializer = s
    }
}

// Context is always first parameter
func (es *EventStore) Append(ctx context.Context, streamID string, 
    events []EventData, expectedVersion int64) error {
    // Implementation
}

// Error handling
var ErrNotFound = errors.New("mink: not found")

func (es *EventStore) Load(ctx context.Context, streamID string) ([]StoredEvent, error) {
    events, err := es.adapter.Load(ctx, streamID, 0)
    if err != nil {
        return nil, fmt.Errorf("load stream %s: %w", streamID, err)
    }
    if len(events) == 0 {
        return nil, ErrNotFound
    }
    return events, nil
}
```

### Interface Design

```go
// Small, focused interfaces
type EventReader interface {
    Load(ctx context.Context, streamID string, fromVersion int64) ([]StoredEvent, error)
}

type EventWriter interface {
    Append(ctx context.Context, streamID string, events []EventData, expectedVersion int64) ([]StoredEvent, error)
}

// Compose larger interfaces from smaller ones
type EventStoreAdapter interface {
    EventReader
    EventWriter
}
```

### Error Handling

```go
// Sentinel errors for comparison
var (
    ErrConcurrencyConflict = errors.New("mink: concurrency conflict")
    ErrStreamNotFound      = errors.New("mink: stream not found")
)

// Typed errors for details
type ConcurrencyError struct {
    StreamID        string
    ExpectedVersion int64
    ActualVersion   int64
}

func (e *ConcurrencyError) Error() string {
    return fmt.Sprintf("concurrency conflict on stream %s: expected %d, got %d",
        e.StreamID, e.ExpectedVersion, e.ActualVersion)
}

// Implement Is() for errors.Is() compatibility
func (e *ConcurrencyError) Is(target error) bool {
    return target == ErrConcurrencyConflict
}
```

### Documentation

```go
// Every exported item needs documentation.
// Start with the name and use complete sentences.

// Append stores events to the specified stream with optimistic concurrency control.
//
// The expectedVersion parameter controls concurrency:
//   - AnyVersion (-1): No version check performed
//   - NoStream (0): Stream must not exist
//   - StreamExists (-2): Stream must already exist
//   - Positive value: Must match current stream version
//
// Returns ErrConcurrencyConflict if version check fails.
func (es *EventStore) Append(ctx context.Context, streamID string,
    events []EventData, expectedVersion int64) error {
    // ...
}
```

## Getting Help

- **Questions**: Open a GitHub Discussion
- **Bugs**: Open a GitHub Issue with reproduction steps
- **Features**: Open an Issue to discuss before implementing

## Recognition

Contributors will be recognized in:
- CHANGELOG.md for each release
- README.md Contributors section
- GitHub contributors page

Thank you for contributing to go-mink! 🎉
