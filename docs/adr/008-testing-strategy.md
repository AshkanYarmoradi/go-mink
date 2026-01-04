---
layout: default
title: "ADR-008: BDD Testing Strategy"
parent: Architecture Decision Records
nav_order: 8
---

# ADR-008: BDD Testing Strategy

| Status | Date | Deciders |
|--------|------|----------|
| Accepted | 2024-04-01 | Core Team |

## Context

Event-sourced systems require comprehensive testing at multiple levels:

1. **Aggregate Logic**: Domain rules and state transitions
2. **Projections**: Read model building from events
3. **Integration**: Full command→event→projection flow
4. **Infrastructure**: Database adapters, serialization

Traditional testing approaches have limitations:

- **State-based tests**: Don't verify events produced
- **Mock-heavy tests**: Don't catch integration issues
- **Integration tests only**: Too slow for TDD

We need a testing strategy that is:
- Expressive for domain logic
- Fast for development
- Comprehensive for production confidence

## Decision

We will implement a **BDD-style testing framework** with specialized utilities for event-sourced systems.

### Core Testing Package: `testing/bdd`

Given-When-Then pattern for aggregate tests:

```go
func TestOrder_Creation(t *testing.T) {
    order := NewOrder("order-123")
    
    bdd.Given(t, order).
        When(func() error {
            return order.Create("customer-456", items)
        }).
        Then(OrderCreated{
            OrderID:    "order-123",
            CustomerID: "customer-456",
            Items:      items,
        })
}

func TestOrder_CannotShipUnpaid(t *testing.T) {
    order := NewOrder("order-123")
    
    bdd.Given(t, order,
        OrderCreated{OrderID: "order-123"},
    ).
        When(func() error {
            return order.Ship("TRACK-123", "FedEx")
        }).
        ThenError(ErrOrderNotPaid)
}
```

### Implementation

```go
package bdd

type TestFixture struct {
    t         *testing.T
    aggregate Aggregate
}

func Given(t *testing.T, agg Aggregate, events ...interface{}) *TestFixture {
    t.Helper()
    
    // Apply given events
    for _, event := range events {
        if err := agg.ApplyEvent(event); err != nil {
            t.Fatalf("Failed to apply given event: %v", err)
        }
    }
    agg.ClearUncommittedEvents()
    
    return &TestFixture{t: t, aggregate: agg}
}

func (f *TestFixture) When(action func() error) *WhenResult {
    f.t.Helper()
    err := action()
    return &WhenResult{
        t:         f.t,
        aggregate: f.aggregate,
        err:       err,
    }
}

type WhenResult struct {
    t         *testing.T
    aggregate Aggregate
    err       error
}

func (r *WhenResult) Then(expectedEvents ...interface{}) {
    r.t.Helper()
    
    if r.err != nil {
        r.t.Fatalf("Action failed unexpectedly: %v", r.err)
    }
    
    actualEvents := r.aggregate.UncommittedEvents()
    
    if len(actualEvents) != len(expectedEvents) {
        r.t.Fatalf("Expected %d events, got %d", 
            len(expectedEvents), len(actualEvents))
    }
    
    for i, expected := range expectedEvents {
        if !eventsEqual(expected, actualEvents[i]) {
            r.t.Errorf("Event %d mismatch:\nExpected: %+v\nActual: %+v",
                i, expected, actualEvents[i])
        }
    }
}

func (r *WhenResult) ThenError(expectedErr error) {
    r.t.Helper()
    
    if r.err == nil {
        r.t.Fatal("Expected error but action succeeded")
    }
    
    if !errors.Is(r.err, expectedErr) {
        r.t.Fatalf("Expected error %v, got %v", expectedErr, r.err)
    }
}

func (r *WhenResult) ThenNoError() {
    r.t.Helper()
    
    if r.err != nil {
        r.t.Fatalf("Expected no error, got %v", r.err)
    }
}

func (r *WhenResult) ThenMatches(matcher func(events []interface{}) bool) {
    r.t.Helper()
    
    if r.err != nil {
        r.t.Fatalf("Action failed unexpectedly: %v", r.err)
    }
    
    events := r.aggregate.UncommittedEvents()
    if !matcher(events) {
        r.t.Fatalf("Events did not match custom matcher: %+v", events)
    }
}
```

### Event Assertions: `testing/assertions`

```go
package assertions

// Assert event types in order
func AssertEventTypes(t *testing.T, events []interface{}, types ...string) {
    t.Helper()
    
    if len(events) != len(types) {
        t.Fatalf("Expected %d events, got %d", len(types), len(events))
    }
    
    for i, typ := range types {
        actualType := reflect.TypeOf(events[i]).Name()
        if actualType != typ {
            t.Errorf("Event %d: expected type %s, got %s", i, typ, actualType)
        }
    }
}

// Deep field comparison
func AssertEvent(t *testing.T, actual interface{}, matcher EventMatcher) {
    t.Helper()
    
    actualType := reflect.TypeOf(actual).Name()
    if actualType != matcher.Type {
        t.Errorf("Expected type %s, got %s", matcher.Type, actualType)
        return
    }
    
    for field, expected := range matcher.Fields {
        actualValue := getField(actual, field)
        if !reflect.DeepEqual(actualValue, expected) {
            t.Errorf("Field %s: expected %v, got %v", field, expected, actualValue)
        }
    }
}

// Generate diff for debugging
func DiffEvents(expected, actual interface{}) string {
    expectedJSON, _ := json.MarshalIndent(expected, "", "  ")
    actualJSON, _ := json.MarshalIndent(actual, "", "  ")
    
    return fmt.Sprintf("Expected:\n%s\n\nActual:\n%s", expectedJSON, actualJSON)
}
```

### Projection Testing: `testing/projections`

```go
package projections

func NewTestEvent(eventType string, data interface{}) mink.StoredEvent {
    jsonData, _ := json.Marshal(data)
    return mink.StoredEvent{
        ID:        uuid.New().String(),
        StreamID:  "test-stream",
        Type:      eventType,
        Data:      jsonData,
        Version:   1,
        Timestamp: time.Now(),
    }
}

type ProjectionTestFixture struct {
    projection Projection
    events     []mink.StoredEvent
}

func GivenProjection(p Projection) *ProjectionTestFixture {
    return &ProjectionTestFixture{projection: p}
}

func (f *ProjectionTestFixture) WithEvents(events ...mink.StoredEvent) *ProjectionTestFixture {
    f.events = append(f.events, events...)
    return f
}

func (f *ProjectionTestFixture) ExpectState(t *testing.T, check func()) {
    ctx := context.Background()
    
    for _, event := range f.events {
        if err := f.projection.Apply(ctx, event); err != nil {
            t.Fatalf("Failed to apply event: %v", err)
        }
    }
    
    check()
}
```

### Test Containers: `testing/containers`

```go
package containers

type PostgresContainer struct {
    container testcontainers.Container
    host      string
    port      string
}

func NewPostgresContainer(ctx context.Context) (*PostgresContainer, error) {
    req := testcontainers.ContainerRequest{
        Image:        "postgres:16-alpine",
        ExposedPorts: []string{"5432/tcp"},
        Env: map[string]string{
            "POSTGRES_PASSWORD": "test",
            "POSTGRES_DB":       "mink_test",
        },
        WaitingFor: wait.ForListeningPort("5432/tcp"),
    }
    
    container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
        ContainerRequest: req,
        Started:          true,
    })
    if err != nil {
        return nil, err
    }
    
    host, _ := container.Host(ctx)
    port, _ := container.MappedPort(ctx, "5432")
    
    return &PostgresContainer{
        container: container,
        host:      host,
        port:      port.Port(),
    }, nil
}

func (c *PostgresContainer) ConnectionString() string {
    return fmt.Sprintf("postgres://postgres:test@%s:%s/mink_test?sslmode=disable",
        c.host, c.port)
}

func (c *PostgresContainer) Terminate(ctx context.Context) error {
    return c.container.Terminate(ctx)
}
```

## Consequences

### Positive

1. **Expressive Tests**: Given-When-Then reads like requirements
2. **Fast Feedback**: Unit tests run in milliseconds
3. **Comprehensive**: Cover domain, projections, and integration
4. **Debugging**: Diff output helps identify issues
5. **Documentation**: Tests document expected behavior

### Negative

1. **Learning Curve**: New DSL to learn
2. **Maintenance**: Testing utilities need maintenance
3. **Custom Matchers**: Sometimes need domain-specific matchers

### Neutral

1. **Test Organization**: Need conventions for test structure
2. **Coverage**: Should aim for high coverage of domain logic

## Test Organization

```
myapp/
├── internal/
│   └── domain/
│       └── order/
│           ├── order.go
│           ├── order_test.go          # BDD aggregate tests
│           ├── events.go
│           └── events_test.go         # Event serialization tests
├── internal/
│   └── projections/
│       ├── order_summary.go
│       └── order_summary_test.go      # Projection tests
└── internal/
    └── integration/
        └── integration_test.go        # Full flow tests
```

## References

- [BDD by Dan North](https://dannorth.net/introducing-bdd/)
- [Testcontainers Go](https://golang.testcontainers.org/)
- [Testing Event Sourced Systems](https://www.eventstore.com/blog/testing-event-sourced-systems)
- [Ginkgo BDD Framework](https://onsi.github.io/ginkgo/)
