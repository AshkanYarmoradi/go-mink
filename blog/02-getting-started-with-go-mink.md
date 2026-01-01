# Part 2: Getting Started with go-mink

*This is Part 2 of an 8-part series on Event Sourcing and CQRS with Go. In this post, we'll set up go-mink and write our first event-sourced code.*

---

## What is go-mink?

**go-mink** is an Event Sourcing and CQRS library for Go, inspired by MartenDB for .NET. It provides everything you need to build event-sourced systems:

- **Event Store** for persisting and loading events
- **Aggregate support** for domain modeling
- **Command Bus** for CQRS patterns
- **Projection Engine** for building read models
- **Pluggable adapters** for PostgreSQL, in-memory, and more

The library follows Go idioms: explicit over implicit, composition over inheritance, and fail-fast with clear errors.

---

## Installation

First, add go-mink to your project:

```bash
go get github.com/AshkanYarmoradi/go-mink
```

For production use with PostgreSQL:

```bash
go get github.com/AshkanYarmoradi/go-mink/adapters/postgres
```

---

## Your First Event Store

Let's create a simple event store using the in-memory adapter (perfect for learning and testing).

```go
package main

import (
    "context"
    "fmt"
    "log"

    "github.com/AshkanYarmoradi/go-mink"
    "github.com/AshkanYarmoradi/go-mink/adapters/memory"
)

func main() {
    ctx := context.Background()

    // Create an in-memory adapter
    adapter := memory.NewAdapter()

    // Create the event store
    store := mink.New(adapter)

    fmt.Println("Event store created successfully!")
}
```

That's it! You have a working event store. But it's not very useful yet—let's add some events.

---

## Defining Events

Events are simple Go structs that represent something that happened in your domain. Let's model a task management system:

```go
// TaskCreated is recorded when a new task is created
type TaskCreated struct {
    TaskID      string `json:"taskId"`
    Title       string `json:"title"`
    Description string `json:"description"`
}

// TaskCompleted is recorded when a task is marked as done
type TaskCompleted struct {
    CompletedAt time.Time `json:"completedAt"`
}

// TaskRenamed is recorded when a task's title changes
type TaskRenamed struct {
    OldTitle string `json:"oldTitle"`
    NewTitle string `json:"newTitle"`
}
```

### Event Naming Conventions

Notice the naming pattern:

- Events are **past tense** — they describe what happened
- Events are **domain-specific** — `TaskCreated`, not `TaskInserted`
- Events contain **all relevant data** — everything needed to understand what happened

Good event names read like history:
- `OrderPlaced` ✓
- `PaymentReceived` ✓
- `UserRegistered` ✓
- `OrderUpdate` ✗ (vague, present tense)
- `SetStatus` ✗ (sounds like a command)

---

## Registering Events

Before you can deserialize events from the store, you must register them:

```go
store := mink.New(adapter)

// Register all event types your application uses
store.RegisterEvents(
    TaskCreated{},
    TaskCompleted{},
    TaskRenamed{},
)
```

Registration maps the event type name (e.g., `"TaskCreated"`) to the Go struct type. This enables proper deserialization when loading events.

---

## Appending Events

Events are stored in **streams**. A stream represents the history of a single entity—in our case, a task. Each task has its own stream identified by a stream ID.

```go
ctx := context.Background()

// Define the stream ID (typically: EntityType-EntityID)
streamID := "Task-task-001"

// Create events to append
events := []interface{}{
    TaskCreated{
        TaskID:      "task-001",
        Title:       "Learn Event Sourcing",
        Description: "Complete the go-mink tutorial series",
    },
}

// Append events to the stream
stored, err := store.Append(ctx, streamID, events)
if err != nil {
    log.Fatalf("Failed to append events: %v", err)
}

fmt.Printf("Appended %d event(s) to stream %s\n", len(stored), streamID)
fmt.Printf("Event ID: %s, Version: %d\n", stored[0].ID, stored[0].Version)
```

### What Happens During Append

When you append events, go-mink:

1. **Serializes** the event data to JSON
2. **Assigns metadata** including timestamp, version, and global position
3. **Validates** the expected version (for concurrency control)
4. **Persists** the events atomically to the store
5. **Returns** the stored events with all metadata filled in

---

## Loading Events

To get the current state, you load and replay events:

```go
// Load all events from the stream
events, err := store.Load(ctx, streamID)
if err != nil {
    log.Fatalf("Failed to load events: %v", err)
}

fmt.Printf("Loaded %d event(s) from stream %s\n", len(events), streamID)

// Examine each event
for _, event := range events {
    fmt.Printf("  [%d] %s at %s\n",
        event.Version,
        event.Type,
        event.Timestamp.Format(time.RFC3339))

    // The Data field contains the deserialized event
    switch e := event.Data.(type) {
    case TaskCreated:
        fmt.Printf("      Task: %s - %s\n", e.Title, e.Description)
    case TaskCompleted:
        fmt.Printf("      Completed at: %s\n", e.CompletedAt)
    }
}
```

---

## Building State from Events

Let's create a `Task` struct and rebuild its state from events:

```go
type Task struct {
    ID          string
    Title       string
    Description string
    IsCompleted bool
    CompletedAt time.Time
}

func rebuildTask(events []mink.Event) *Task {
    task := &Task{}

    for _, event := range events {
        switch e := event.Data.(type) {
        case TaskCreated:
            task.ID = e.TaskID
            task.Title = e.Title
            task.Description = e.Description
            task.IsCompleted = false

        case TaskRenamed:
            task.Title = e.NewTitle

        case TaskCompleted:
            task.IsCompleted = true
            task.CompletedAt = e.CompletedAt
        }
    }

    return task
}

// Usage
events, _ := store.Load(ctx, "Task-task-001")
task := rebuildTask(events)
fmt.Printf("Task: %s (completed: %v)\n", task.Title, task.IsCompleted)
```

This manual approach works, but go-mink provides a better way: **Aggregates**. We'll cover those in Part 3.

---

## Complete Example

Here's a complete working example that puts everything together:

```go
package main

import (
    "context"
    "fmt"
    "log"
    "time"

    "github.com/AshkanYarmoradi/go-mink"
    "github.com/AshkanYarmoradi/go-mink/adapters/memory"
)

// Events
type TaskCreated struct {
    TaskID      string `json:"taskId"`
    Title       string `json:"title"`
    Description string `json:"description"`
}

type TaskRenamed struct {
    OldTitle string `json:"oldTitle"`
    NewTitle string `json:"newTitle"`
}

type TaskCompleted struct {
    CompletedAt time.Time `json:"completedAt"`
}

// Task state
type Task struct {
    ID          string
    Title       string
    Description string
    IsCompleted bool
    CompletedAt time.Time
}

func rebuildTask(events []mink.Event) *Task {
    task := &Task{}
    for _, event := range events {
        switch e := event.Data.(type) {
        case TaskCreated:
            task.ID = e.TaskID
            task.Title = e.Title
            task.Description = e.Description
        case TaskRenamed:
            task.Title = e.NewTitle
        case TaskCompleted:
            task.IsCompleted = true
            task.CompletedAt = e.CompletedAt
        }
    }
    return task
}

func main() {
    ctx := context.Background()

    // Setup
    adapter := memory.NewAdapter()
    store := mink.New(adapter)
    store.RegisterEvents(TaskCreated{}, TaskRenamed{}, TaskCompleted{})

    streamID := "Task-task-001"

    // Create a task
    _, err := store.Append(ctx, streamID, []interface{}{
        TaskCreated{
            TaskID:      "task-001",
            Title:       "Learn Event Sourcing",
            Description: "Complete the go-mink tutorial",
        },
    })
    if err != nil {
        log.Fatal(err)
    }
    fmt.Println("Task created")

    // Rename the task
    _, err = store.Append(ctx, streamID, []interface{}{
        TaskRenamed{
            OldTitle: "Learn Event Sourcing",
            NewTitle: "Master Event Sourcing with go-mink",
        },
    })
    if err != nil {
        log.Fatal(err)
    }
    fmt.Println("Task renamed")

    // Complete the task
    _, err = store.Append(ctx, streamID, []interface{}{
        TaskCompleted{CompletedAt: time.Now()},
    })
    if err != nil {
        log.Fatal(err)
    }
    fmt.Println("Task completed")

    // Load and rebuild state
    events, _ := store.Load(ctx, streamID)
    task := rebuildTask(events)

    fmt.Println("\n--- Current State ---")
    fmt.Printf("ID:          %s\n", task.ID)
    fmt.Printf("Title:       %s\n", task.Title)
    fmt.Printf("Description: %s\n", task.Description)
    fmt.Printf("Completed:   %v\n", task.IsCompleted)

    fmt.Println("\n--- Event History ---")
    for _, e := range events {
        fmt.Printf("[v%d] %s\n", e.Version, e.Type)
    }
}
```

Output:

```
Task created
Task renamed
Task completed

--- Current State ---
ID:          task-001
Title:       Master Event Sourcing with go-mink
Description: Complete the go-mink tutorial
Completed:   true

--- Event History ---
[v1] TaskCreated
[v2] TaskRenamed
[v3] TaskCompleted
```

---

## Optimistic Concurrency

One of event sourcing's key features is **optimistic concurrency control**. This prevents conflicting writes to the same stream.

### How It Works

Each stream has a **version**—the count of events. When you append, you can specify the expected version:

```go
// Expect the stream to be at version 2
_, err := store.Append(ctx, streamID, events,
    mink.ExpectVersion(2))

if errors.Is(err, mink.ErrConcurrencyConflict) {
    // Someone else modified the stream!
    // Reload and retry
}
```

### Version Constants

go-mink provides helpful constants:

```go
mink.AnyVersion    // -1: Skip version check (dangerous but fast)
mink.NoStream      //  0: Stream must not exist (creating new)
mink.StreamExists  // -2: Stream must exist (updating existing)
```

Example: Creating a new stream safely:

```go
// This will fail if the stream already exists
_, err := store.Append(ctx, "Task-task-001", events,
    mink.ExpectVersion(mink.NoStream))

if errors.Is(err, mink.ErrConcurrencyConflict) {
    fmt.Println("Task already exists!")
}
```

---

## Working with Metadata

Events can carry metadata—contextual information that's useful for debugging, auditing, and distributed tracing.

```go
// Create metadata
metadata := mink.Metadata{
    CorrelationID: "request-12345",    // Links related events
    CausationID:   "command-67890",    // What caused this event
    UserID:        "user-alice",       // Who triggered it
    Custom: map[string]string{
        "source":    "web-app",
        "ip":        "192.168.1.100",
    },
}

// Append with metadata
_, err := store.Append(ctx, streamID, events,
    mink.WithAppendMetadata(metadata))
```

When you load events, the metadata is available:

```go
events, _ := store.Load(ctx, streamID)
for _, event := range events {
    fmt.Printf("Event: %s\n", event.Type)
    fmt.Printf("  Correlation ID: %s\n", event.Metadata.CorrelationID)
    fmt.Printf("  User: %s\n", event.Metadata.UserID)
}
```

---

## Using PostgreSQL in Production

The in-memory adapter is great for learning and testing, but for production you'll want PostgreSQL:

```go
import (
    "github.com/AshkanYarmoradi/go-mink"
    "github.com/AshkanYarmoradi/go-mink/adapters/postgres"
)

func main() {
    ctx := context.Background()

    // Create PostgreSQL adapter
    connStr := "postgres://user:pass@localhost:5432/mydb?sslmode=disable"
    adapter, err := postgres.NewAdapter(connStr)
    if err != nil {
        log.Fatal(err)
    }
    defer adapter.Close()

    // Initialize creates the required tables
    if err := adapter.Initialize(ctx); err != nil {
        log.Fatal(err)
    }

    // Create store with PostgreSQL backend
    store := mink.New(adapter)
    store.RegisterEvents(/* your events */)

    // Use exactly the same API as memory adapter!
}
```

The PostgreSQL adapter provides:
- ACID transactions for event appends
- Proper optimistic concurrency via database constraints
- Global ordering for projections
- Subscription support for real-time updates

---

## Error Handling

go-mink uses sentinel errors that work with `errors.Is()`:

```go
events, err := store.Load(ctx, "nonexistent-stream")
if errors.Is(err, mink.ErrStreamNotFound) {
    // Handle missing stream
    fmt.Println("Stream does not exist")
}

_, err = store.Append(ctx, streamID, events, mink.ExpectVersion(5))
if errors.Is(err, mink.ErrConcurrencyConflict) {
    // Handle concurrent modification
    fmt.Println("Concurrent write detected, please retry")
}
```

Common errors:
- `ErrStreamNotFound` — Stream doesn't exist
- `ErrConcurrencyConflict` — Version mismatch
- `ErrEventTypeNotRegistered` — Event type not registered
- `ErrEmptyStreamID` — Stream ID is empty
- `ErrNoEvents` — No events to append

---

## What's Next?

In this post, you learned:

- How to set up go-mink with the in-memory adapter
- How to define and register events
- How to append events to streams
- How to load and replay events to rebuild state
- How optimistic concurrency prevents conflicts
- How to add metadata to events

The manual approach to rebuilding state works, but it's error-prone and tedious. In **Part 3**, we'll introduce **Aggregates**—domain objects that encapsulate both state and behavior, making event sourcing much more elegant.

---

## Key Takeaways

1. **Events are structs**: Simple Go structs with JSON tags
2. **Streams group events**: One stream per domain entity
3. **Registration enables deserialization**: Always register your event types
4. **Optimistic concurrency is built-in**: Use version expectations to prevent conflicts
5. **Metadata enables observability**: Correlation IDs, user IDs, and custom data

---

*Previous: [← Part 1: Introduction to Event Sourcing](01-introduction-to-event-sourcing.md)*

*Next: [Part 3: Building Your First Aggregate →](03-building-your-first-aggregate.md)*
