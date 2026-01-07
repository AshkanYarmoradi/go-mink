---
layout: default
title: "Part 1: Project Setup"
parent: "Tutorial: Building an E-Commerce App"
nav_order: 1
permalink: /tutorial/01-setup
---

# Part 1: Project Setup
{: .no_toc }

Create your MinkShop project and configure the event store.
{: .fs-6 .fw-300 }

## Table of contents
{: .no_toc .text-delta }

1. TOC
{:toc}

---

## Overview

In this first part, you'll:

- Create the Go module and project structure
- Set up PostgreSQL using Docker
- Initialize go-mink and verify the connection
- Understand the basic architecture

**Time**: ~20 minutes

{: .note }
> **Alternative: Use CLI Tool**  
> You can use the `mink` CLI to scaffold your project quickly:
> ```bash
> # Install CLI
> go install github.com/AshkanYarmoradi/go-mink/cmd/mink@latest
>
> # Initialize project
> mkdir minkshop && cd minkshop
> go mod init minkshop
> mink init --name=minkshop --driver=postgres
>
> # Generate aggregates
> mink generate aggregate Product --events Created,StockAdded,StockReserved
> mink generate aggregate Cart --events Created,ItemAdded,ItemRemoved
> mink generate aggregate Order --events Created,Paid,Shipped
>
> # Run migrations
> export DATABASE_URL="postgres://minkshop:secret@localhost:5432/minkshop?sslmode=disable"
> mink migrate up
>
> # Verify setup
> mink diagnose
> ```
> This tutorial shows the manual approach to help you understand the internals.
> See the [CLI documentation](/docs/cli) for more details.

---

## Step 1: Create the Project

Let's start with a fresh Go module:

```bash
# Create project directory
mkdir minkshop
cd minkshop

# Initialize Go module
go mod init minkshop

# Install go-mink and dependencies
go get github.com/AshkanYarmoradi/go-mink
go get github.com/AshkanYarmoradi/go-mink/adapters/postgres
```

Create the initial project structure:

```bash
# Create directories
mkdir -p cmd/server
mkdir -p internal/domain/product
mkdir -p internal/domain/cart
mkdir -p internal/domain/order
mkdir -p internal/projections
mkdir -p internal/handlers
mkdir -p internal/api
mkdir -p tests
```

Your structure should now look like:

```
minkshop/
├── cmd/
│   └── server/
├── internal/
│   ├── domain/
│   │   ├── product/
│   │   ├── cart/
│   │   └── order/
│   ├── projections/
│   ├── handlers/
│   └── api/
├── tests/
└── go.mod
```

---

## Step 2: Set Up PostgreSQL

go-mink uses PostgreSQL as its primary event store. Let's set it up with Docker.

### Option A: Docker Compose (Recommended)

Create `docker-compose.yml` in your project root:

```yaml
version: '3.8'

services:
  postgres:
    image: postgres:16-alpine
    container_name: minkshop-db
    environment:
      POSTGRES_USER: minkshop
      POSTGRES_PASSWORD: secret
      POSTGRES_DB: minkshop
    ports:
      - "5432:5432"
    volumes:
      - minkshop_data:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U minkshop -d minkshop"]
      interval: 5s
      timeout: 5s
      retries: 5

volumes:
  minkshop_data:
```

Start the database:

```bash
docker-compose up -d
```

### Option B: Docker Run

If you prefer a simple docker run command:

```bash
docker run -d \
  --name minkshop-db \
  -e POSTGRES_USER=minkshop \
  -e POSTGRES_PASSWORD=secret \
  -e POSTGRES_DB=minkshop \
  -p 5432:5432 \
  postgres:16-alpine
```

### Verify Connection

```bash
# Test connection with psql (optional)
docker exec -it minkshop-db psql -U minkshop -d minkshop -c "SELECT version();"
```

---

## Step 3: Create the Main Application

Now let's create the entry point that initializes go-mink.

Create `cmd/server/main.go`:

```go
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/AshkanYarmoradi/go-mink"
	"github.com/AshkanYarmoradi/go-mink/adapters/postgres"
)

func main() {
	// Create a context that cancels on interrupt signals
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle graceful shutdown
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		fmt.Println("\nShutting down...")
		cancel()
	}()

	// Run the application
	if err := run(ctx); err != nil {
		log.Fatalf("Application error: %v", err)
	}
}

func run(ctx context.Context) error {
	fmt.Println("🛒 MinkShop - Event Sourced E-Commerce")
	fmt.Println("=====================================")
	fmt.Println()

	// Database connection string
	connStr := getEnvOrDefault("DATABASE_URL", 
		"postgres://minkshop:secret@localhost:5432/minkshop?sslmode=disable")

	// Create PostgreSQL adapter
	fmt.Println("📦 Connecting to PostgreSQL...")
	adapter, err := postgres.NewAdapter(connStr,
		postgres.WithSchema("minkshop"),
		postgres.WithMaxConnections(10),
	)
	if err != nil {
		return fmt.Errorf("failed to create adapter: %w", err)
	}
	defer adapter.Close()

	// Initialize the schema (creates tables if they don't exist)
	fmt.Println("🔧 Initializing event store schema...")
	if err := adapter.Initialize(ctx); err != nil {
		return fmt.Errorf("failed to initialize schema: %w", err)
	}

	// Create the event store
	store := mink.New(adapter)

	// Verify connection with a health check
	fmt.Println("❤️  Checking database health...")
	if err := adapter.HealthCheck(ctx); err != nil {
		return fmt.Errorf("health check failed: %w", err)
	}

	fmt.Println()
	fmt.Println("✅ Event store initialized successfully!")
	fmt.Println()
	
	// Display store information
	printStoreInfo(store)

	// In Part 2, we'll add domain aggregates here
	// In Part 3, we'll add the command bus and handlers
	// In Part 4, we'll add projections

	fmt.Println()
	fmt.Println("Press Ctrl+C to exit...")
	
	// Wait for shutdown signal
	<-ctx.Done()
	
	fmt.Println("👋 Goodbye!")
	return nil
}

func printStoreInfo(store *mink.EventStore) {
	fmt.Println("📊 Event Store Configuration:")
	fmt.Println("   - Adapter: PostgreSQL")
	fmt.Println("   - Schema: minkshop")
	fmt.Println("   - Serializer: JSON")
	fmt.Println()
}

func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
```

---

## Step 4: Run the Application

Let's verify everything works:

```bash
# Make sure PostgreSQL is running
docker-compose up -d

# Run the application
go run cmd/server/main.go
```

You should see:

```
🛒 MinkShop - Event Sourced E-Commerce
=====================================

📦 Connecting to PostgreSQL...
🔧 Initializing event store schema...
❤️  Checking database health...

✅ Event store initialized successfully!

📊 Event Store Configuration:
   - Adapter: PostgreSQL
   - Schema: minkshop
   - Serializer: JSON

Press Ctrl+C to exit...
```

---

## Step 5: Explore the Database Schema

Let's see what go-mink created in PostgreSQL:

```bash
# Connect to the database
docker exec -it minkshop-db psql -U minkshop -d minkshop

# List schemas
\dn

# List tables in the minkshop schema
\dt minkshop.*
```

You'll see these tables:

| Table | Purpose |
|-------|---------|
| `minkshop.events` | Stores all events with stream IDs and versions |
| `minkshop.snapshots` | Aggregate snapshots for fast loading (optional) |
| `minkshop.checkpoints` | Projection checkpoint tracking |

Let's examine the events table:

```sql
\d minkshop.events
```

```
                                           Table "minkshop.events"
     Column      |           Type           | Collation | Nullable |                Default
-----------------+--------------------------+-----------+----------+----------------------------------------
 id              | uuid                     |           | not null | gen_random_uuid()
 stream_id       | character varying(255)   |           | not null |
 version         | bigint                   |           | not null |
 type            | character varying(255)   |           | not null |
 data            | jsonb                    |           | not null |
 metadata        | jsonb                    |           |          | '{}'::jsonb
 global_position | bigint                   |           | not null | generated always as identity
 timestamp       | timestamp with time zone |           | not null | now()
Indexes:
    "events_pkey" PRIMARY KEY, btree (id)
    "events_stream_id_version_key" UNIQUE CONSTRAINT, btree (stream_id, version)
    "events_global_position_idx" btree (global_position)
    "events_stream_id_idx" btree (stream_id)
    "events_type_idx" btree (type)
```

Key columns:
- **stream_id**: Groups events by aggregate (e.g., `Product-SKU123`)
- **version**: Sequential within a stream, enables optimistic concurrency
- **type**: Event type name (e.g., `ProductCreated`)
- **data**: Event payload as JSONB
- **global_position**: Total ordering across all streams

Exit psql with `\q`.

---

## Step 6: Create a Configuration Package

Let's create a configuration helper for cleaner code.

Create `internal/config/config.go`:

```go
package config

import (
	"os"
	"time"
)

// Config holds application configuration.
type Config struct {
	// Database settings
	DatabaseURL    string
	DatabaseSchema string
	MaxConnections int
	
	// Server settings
	ServerPort string
	
	// Feature flags
	EnableMetrics bool
	EnableTracing bool
}

// Load reads configuration from environment variables.
func Load() *Config {
	return &Config{
		DatabaseURL:    getEnv("DATABASE_URL", "postgres://minkshop:secret@localhost:5432/minkshop?sslmode=disable"),
		DatabaseSchema: getEnv("DATABASE_SCHEMA", "minkshop"),
		MaxConnections: getEnvInt("DATABASE_MAX_CONNECTIONS", 10),
		
		ServerPort:    getEnv("SERVER_PORT", "8080"),
		
		EnableMetrics: getEnvBool("ENABLE_METRICS", false),
		EnableTracing: getEnvBool("ENABLE_TRACING", false),
	}
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getEnvInt(key string, defaultValue int) int {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	// Simple parsing - in production, use strconv.Atoi with error handling
	var result int
	for _, c := range value {
		if c >= '0' && c <= '9' {
			result = result*10 + int(c-'0')
		}
	}
	if result == 0 {
		return defaultValue
	}
	return result
}

func getEnvBool(key string, defaultValue bool) bool {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	return value == "true" || value == "1" || value == "yes"
}

// DefaultShutdownTimeout returns the default graceful shutdown timeout.
func DefaultShutdownTimeout() time.Duration {
	return 30 * time.Second
}
```

Create the directory first:

```bash
mkdir -p internal/config
```

---

## Step 7: Test Event Store Operations

Let's write a simple test to verify the event store works.

Create `tests/setup_test.go`:

```go
package tests

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/AshkanYarmoradi/go-mink"
	"github.com/AshkanYarmoradi/go-mink/adapters/memory"
)

// TestEventStoreBasics verifies basic event store operations.
func TestEventStoreBasics(t *testing.T) {
	// Use in-memory adapter for fast testing
	adapter := memory.NewAdapter()
	store := mink.New(adapter)
	ctx := context.Background()

	// Initialize the store
	if err := store.Initialize(ctx); err != nil {
		t.Fatalf("Failed to initialize store: %v", err)
	}
	defer store.Close()

	// Define a simple test event
	type TestEvent struct {
		Message   string    `json:"message"`
		Timestamp time.Time `json:"timestamp"`
	}

	// Register event types
	store.RegisterEvents(TestEvent{})

	// Append an event
	streamID := "Test-001"
	event := TestEvent{
		Message:   "Hello, Event Sourcing!",
		Timestamp: time.Now(),
	}

	err := store.Append(ctx, streamID, []interface{}{event}, 
		mink.ExpectVersion(mink.NoStream))
	if err != nil {
		t.Fatalf("Failed to append event: %v", err)
	}

	// Load events
	events, err := store.Load(ctx, streamID)
	if err != nil {
		t.Fatalf("Failed to load events: %v", err)
	}

	// Verify
	if len(events) != 1 {
		t.Errorf("Expected 1 event, got %d", len(events))
	}

	if events[0].Type != "TestEvent" {
		t.Errorf("Expected event type TestEvent, got %s", events[0].Type)
	}

	t.Logf("✅ Event stored and retrieved successfully!")
	t.Logf("   Stream: %s", streamID)
	t.Logf("   Type: %s", events[0].Type)
	t.Logf("   Version: %d", events[0].Version)
}

// TestConcurrencyControl verifies optimistic concurrency.
func TestConcurrencyControl(t *testing.T) {
	adapter := memory.NewAdapter()
	store := mink.New(adapter)
	ctx := context.Background()

	store.Initialize(ctx)
	defer store.Close()

	type DummyEvent struct {
		Value int `json:"value"`
	}
	store.RegisterEvents(DummyEvent{})

	streamID := "Concurrent-001"

	// First append should succeed (new stream)
	err := store.Append(ctx, streamID, []interface{}{DummyEvent{Value: 1}},
		mink.ExpectVersion(mink.NoStream))
	if err != nil {
		t.Fatalf("First append failed: %v", err)
	}

	// Second append expecting version 0 should fail (stream exists with version 1)
	err = store.Append(ctx, streamID, []interface{}{DummyEvent{Value: 2}},
		mink.ExpectVersion(mink.NoStream))
	if err == nil {
		t.Error("Expected concurrency error, got nil")
	}

	// Append with correct expected version should succeed
	err = store.Append(ctx, streamID, []interface{}{DummyEvent{Value: 2}},
		mink.ExpectVersion(1))
	if err != nil {
		t.Fatalf("Second append with correct version failed: %v", err)
	}

	// Verify final state
	events, _ := store.Load(ctx, streamID)
	if len(events) != 2 {
		t.Errorf("Expected 2 events, got %d", len(events))
	}

	t.Logf("✅ Concurrency control working correctly!")
}

// Integration test using PostgreSQL (skip if not available)
func TestPostgresIntegration(t *testing.T) {
	connStr := os.Getenv("TEST_DATABASE_URL")
	if connStr == "" {
		t.Skip("TEST_DATABASE_URL not set, skipping PostgreSQL integration test")
	}

	// This test would use the real PostgreSQL adapter
	// We'll implement this properly in Part 5
	t.Log("PostgreSQL integration test would run here")
}
```

Run the tests:

```bash
go test -v ./tests/...
```

Expected output:

```
=== RUN   TestEventStoreBasics
    setup_test.go:52: ✅ Event stored and retrieved successfully!
    setup_test.go:53:    Stream: Test-001
    setup_test.go:54:    Type: TestEvent
    setup_test.go:55:    Version: 1
--- PASS: TestEventStoreBasics (0.00s)
=== RUN   TestConcurrencyControl
    setup_test.go:89: ✅ Concurrency control working correctly!
--- PASS: TestConcurrencyControl (0.00s)
=== RUN   TestPostgresIntegration
    setup_test.go:95: TEST_DATABASE_URL not set, skipping PostgreSQL integration test
--- SKIP: TestPostgresIntegration (0.00s)
PASS
ok      minkshop/tests  0.003s
```

---

## Understanding Event Store Concepts

Before moving on, let's understand the key concepts:

### Streams

A **stream** is a sequence of events for a single entity (aggregate). In our e-commerce domain:

```
Stream: Product-SKU123
├── ProductCreated { name: "Widget", price: 29.99 }
├── StockAdded { quantity: 100 }
└── PriceChanged { newPrice: 24.99 }

Stream: Cart-CART456
├── CartCreated { customerId: "CUST789" }
├── ItemAdded { productId: "SKU123", quantity: 2 }
└── ItemRemoved { productId: "SKU123" }
```

### Version Numbers

Each event has a version number (1, 2, 3...) within its stream. This enables:

- **Optimistic concurrency**: Detect conflicting writes
- **Event ordering**: Replay events in correct order
- **Gap detection**: Identify missing events

### Global Position

Beyond stream versions, events have a `global_position` — a monotonically increasing number across all streams. This is crucial for:

- **Projections**: Process events in total order
- **Subscriptions**: Follow all events in the system
- **Exactly-once delivery**: Track processing position

---

## What's Next?

You've successfully set up your project with:

- ✅ Go module with go-mink
- ✅ PostgreSQL event store
- ✅ Basic application structure
- ✅ Working tests

In **Part 2: Domain Modeling**, you'll:

- Define events for products, carts, and orders
- Build aggregate roots with business rules
- Implement the event sourcing pattern

<div class="code-example" markdown="1">

**Next**: [Part 2: Domain Modeling →](/tutorial/02-domain-modeling)

</div>

---

## Troubleshooting

### PostgreSQL Connection Issues

```bash
# Check if container is running
docker ps

# View container logs
docker logs minkshop-db

# Restart container
docker-compose restart
```

### Permission Errors

If you get permission errors, ensure your PostgreSQL user has CREATE privileges:

```sql
GRANT ALL PRIVILEGES ON DATABASE minkshop TO minkshop;
```

### Import Errors

Make sure all dependencies are downloaded:

```bash
go mod tidy
```

---

## Summary

| Concept | Description |
|---------|-------------|
| **Event Store** | Database optimized for append-only event storage |
| **Stream** | Sequence of events for one aggregate instance |
| **Version** | Sequential number within a stream |
| **Global Position** | Total ordering across all events |
| **Adapter** | Database-specific implementation (PostgreSQL, Memory) |

---

{: .highlight }
> 💡 **Tip**: Keep the PostgreSQL container running as you work through the tutorial. You can stop it with `docker-compose down` and restart with `docker-compose up -d`.
