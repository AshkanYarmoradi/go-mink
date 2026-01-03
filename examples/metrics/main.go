// Example: Metrics Middleware
//
// This example demonstrates how to instrument your event store
// with Prometheus metrics for observability.
//
// Run with: go run .
package main

import (
	"context"
	"fmt"
	"math/rand"
	"net/http"
	"time"

	"github.com/AshkanYarmoradi/go-mink"
	"github.com/AshkanYarmoradi/go-mink/adapters/memory"
	"github.com/AshkanYarmoradi/go-mink/middleware/metrics"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// =============================================================================
// Domain Events
// =============================================================================

type UserRegistered struct {
	UserID    string    `json:"userId"`
	Email     string    `json:"email"`
	Timestamp time.Time `json:"timestamp"`
}

type UserLoggedIn struct {
	UserID    string    `json:"userId"`
	IPAddress string    `json:"ipAddress"`
	Timestamp time.Time `json:"timestamp"`
}

type ProfileUpdated struct {
	UserID    string    `json:"userId"`
	Field     string    `json:"field"`
	OldValue  string    `json:"oldValue"`
	NewValue  string    `json:"newValue"`
	Timestamp time.Time `json:"timestamp"`
}

// =============================================================================
// Main
// =============================================================================

func main() {
	fmt.Println("=== Metrics Middleware Example ===")
	fmt.Println()

	// Create memory adapter and event store
	adapter := memory.NewAdapter()
	store := mink.New(adapter)
	ctx := context.Background()

	// Create metrics instance
	metricsInstance := metrics.New(
		metrics.WithNamespace("mink_example"),
		metrics.WithMetricsServiceName("user-service"),
	)

	// Register metrics with Prometheus
	metricsInstance.MustRegister()

	fmt.Println("📊 Metrics middleware initialized")
	fmt.Println("   - Prometheus metrics available at :9090/metrics")
	fmt.Println()

	// Start Prometheus HTTP server in background
	go func() {
		http.Handle("/metrics", promhttp.Handler())
		http.ListenAndServe(":9090", nil)
	}()

	// Simulate various event store operations
	fmt.Println("🔄 Simulating event store operations...")
	fmt.Println()

	// Register some users
	users := []string{"user-001", "user-002", "user-003", "user-004", "user-005"}

	for _, userID := range users {
		// Record operation timing
		startTime := time.Now()

		// Create registration event
		events := []interface{}{
			UserRegistered{
				UserID:    userID,
				Email:     userID + "@example.com",
				Timestamp: time.Now(),
			},
		}

		// Append events
		err := store.Append(ctx, "user-"+userID, events, mink.ExpectVersion(mink.NoStream))
		duration := time.Since(startTime)

		// Log result
		if err == nil {
			fmt.Printf("   ✅ Registered %s (%.2fms)\n", userID, float64(duration.Microseconds())/1000)
		} else {
			fmt.Printf("   ❌ Failed to register %s: %v\n", userID, err)
		}

		// Small delay to simulate real traffic
		time.Sleep(time.Duration(rand.Intn(50)) * time.Millisecond)
	}

	fmt.Println()

	// Simulate login events
	fmt.Println("🔑 Simulating user logins...")
	for i := 0; i < 20; i++ {
		userID := users[rand.Intn(len(users))]
		streamID := "user-" + userID

		// Load user events first
		_, err := store.Load(ctx, streamID)
		if err != nil {
			fmt.Printf("   ❌ Failed to load %s: %v\n", streamID, err)
			continue
		}

		// Append login event
		events := []interface{}{
			UserLoggedIn{
				UserID:    userID,
				IPAddress: fmt.Sprintf("192.168.1.%d", rand.Intn(255)),
				Timestamp: time.Now(),
			},
		}

		err = store.Append(ctx, streamID, events)
		if err != nil {
			fmt.Printf("   ❌ Failed to append login: %v\n", err)
		}

		time.Sleep(time.Duration(rand.Intn(100)) * time.Millisecond)
	}

	fmt.Println("   ✅ Completed 20 login simulations")
	fmt.Println()

	// Simulate profile updates
	fmt.Println("📝 Simulating profile updates...")
	for i := 0; i < 10; i++ {
		userID := users[rand.Intn(len(users))]
		streamID := "user-" + userID

		events := []interface{}{
			ProfileUpdated{
				UserID:    userID,
				Field:     "displayName",
				OldValue:  "OldName",
				NewValue:  "NewName",
				Timestamp: time.Now(),
			},
		}

		err := store.Append(ctx, streamID, events)
		if err != nil {
			fmt.Printf("   ❌ Failed to update profile: %v\n", err)
		}

		time.Sleep(time.Duration(rand.Intn(50)) * time.Millisecond)
	}

	fmt.Println("   ✅ Completed 10 profile updates")
	fmt.Println()

	// Print metrics summary
	fmt.Println("---")
	fmt.Println()
	fmt.Println("📈 Metrics Summary")
	fmt.Println()

	// In a real application, these would be scraped by Prometheus
	fmt.Println("   Sample metrics (view full metrics at http://localhost:9090/metrics):")
	fmt.Println()
	fmt.Println("   mink_example_commands_total{service=\"user-service\",command_type=\"...\",status=\"success\"}")
	fmt.Println("   mink_example_eventstore_operations_total{service=\"user-service\",operation=\"append\"}")
	fmt.Println("   mink_example_eventstore_operation_duration_seconds{service=\"user-service\",operation=\"append\"}")
	fmt.Println()

	// Load all events to show final state
	fmt.Println("📊 Final Event Store State:")
	for _, userID := range users {
		events, _ := store.Load(ctx, "user-"+userID)
		fmt.Printf("   - user-%s: %d events\n", userID, len(events))
	}

	// Gather and display some metrics
	fmt.Println()
	gathered, err := prometheus.DefaultGatherer.Gather()
	if err == nil {
		fmt.Println("📊 Registered Prometheus Metrics:")
		for _, mf := range gathered {
			if len(mf.GetName()) > 0 && mf.GetName()[:4] == "mink" {
				fmt.Printf("   - %s\n", mf.GetName())
			}
		}
	}

	fmt.Println()
	fmt.Println("💡 Tips:")
	fmt.Println("   - Add metrics middleware to track operation latency")
	fmt.Println("   - Monitor error rates by aggregate type")
	fmt.Println("   - Set up alerts for high latency or error spikes")
	fmt.Println("   - Use Grafana dashboards for visualization")
	fmt.Println()

	fmt.Println("=== Example Complete ===")
	fmt.Println()
	fmt.Println("Prometheus metrics endpoint still running at http://localhost:9090/metrics")
	fmt.Println("Press Ctrl+C to exit")

	// Keep server running
	select {}
}
