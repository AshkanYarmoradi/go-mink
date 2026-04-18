// Package main demonstrates GDPR data export (right to access / data portability) in go-mink.
//
// This example shows:
// - Stream-based export: export specific streams by ID (efficient, no scan)
// - Scan-based export: filter all events using built-in and custom filters
// - Streaming export: memory-efficient export via handler callback
// - Crypto-shredding: exporting data after encryption key revocation (redacted events)
// - Time range filtering: export events within a date range
// - Combined filters: AND-compose multiple filters
package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"log"
	"time"

	"go-mink.dev"
	"go-mink.dev/adapters/memory"
	"go-mink.dev/encryption/local"
)

// Domain Events

type CustomerCreated struct {
	CustomerID string `json:"customer_id"`
	Name       string `json:"name"`
	Email      string `json:"email"`
	Phone      string `json:"phone"`
}

type OrderPlaced struct {
	OrderID    string  `json:"order_id"`
	CustomerID string  `json:"customer_id"`
	Amount     float64 `json:"amount"`
}

type PaymentReceived struct {
	PaymentID string  `json:"payment_id"`
	OrderID   string  `json:"order_id"`
	Amount    float64 `json:"amount"`
}

func main() {
	ctx := context.Background()

	// Set up encryption for PII fields
	key := generateKey()
	provider, err := local.New(
		local.WithKey("tenant-A", key),
		local.WithKey("tenant-B", generateKey()),
	)
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = provider.Close() }()

	encConfig := mink.NewFieldEncryptionConfig(
		mink.WithEncryptionProvider(provider),
		mink.WithDefaultKeyID("tenant-A"),
		mink.WithEncryptedFields("CustomerCreated", "email", "phone"),
		mink.WithTenantKeyResolver(func(tenantID string) string {
			return "tenant-" + tenantID
		}),
	)

	adapter := memory.NewAdapter()
	store := mink.New(adapter, mink.WithFieldEncryption(encConfig))
	store.RegisterEvents(CustomerCreated{}, OrderPlaced{}, PaymentReceived{})

	// Seed data for two tenants
	seedData(ctx, store)

	// ── Demo 1: Stream-based export ──
	streamBasedExport(ctx, store)

	// ── Demo 2: Scan-based export with filters ──
	scanBasedExport(ctx, store)

	// ── Demo 3: Streaming export ──
	streamingExport(ctx, store)

	// ── Demo 4: Time range filtering ──
	timeRangeExport(ctx, store)

	// ── Demo 5: Crypto-shredding and export ──
	cryptoShreddingExport(ctx, store, provider)

	fmt.Println("\nDone!")
}

func seedData(ctx context.Context, store *mink.EventStore) {
	// Tenant A — Alice
	must(store.Append(ctx, "Customer-alice-1", []interface{}{
		CustomerCreated{CustomerID: "alice-1", Name: "Alice Smith", Email: "alice@example.com", Phone: "+1-555-0100"},
	}, mink.WithAppendMetadata(mink.Metadata{TenantID: "A", UserID: "admin"})))

	must(store.Append(ctx, "Order-ord-1", []interface{}{
		OrderPlaced{OrderID: "ord-1", CustomerID: "alice-1", Amount: 149.99},
	}, mink.WithAppendMetadata(mink.Metadata{TenantID: "A", UserID: "alice-1"})))

	must(store.Append(ctx, "Payment-pay-1", []interface{}{
		PaymentReceived{PaymentID: "pay-1", OrderID: "ord-1", Amount: 149.99},
	}, mink.WithAppendMetadata(mink.Metadata{TenantID: "A", UserID: "system"})))

	// Tenant B — Bob
	must(store.Append(ctx, "Customer-bob-1", []interface{}{
		CustomerCreated{CustomerID: "bob-1", Name: "Bob Jones", Email: "bob@example.com", Phone: "+44-20-1234"},
	}, mink.WithAppendMetadata(mink.Metadata{TenantID: "B", UserID: "admin"})))

	must(store.Append(ctx, "Order-ord-2", []interface{}{
		OrderPlaced{OrderID: "ord-2", CustomerID: "bob-1", Amount: 79.99},
	}, mink.WithAppendMetadata(mink.Metadata{TenantID: "B", UserID: "bob-1"})))
}

// streamBasedExport shows how to export specific streams when you know the stream IDs.
func streamBasedExport(ctx context.Context, store *mink.EventStore) {
	fmt.Println("=== Stream-Based Export (GDPR Right to Access) ===")
	fmt.Println()

	exporter := mink.NewDataExporter(store)

	// Export Alice's data by listing her known streams
	result, err := exporter.Export(ctx, mink.ExportRequest{
		SubjectID: "alice-1",
		Streams:   []string{"Customer-alice-1", "Order-ord-1", "Payment-pay-1"},
	})
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("Subject: %s\n", result.SubjectID)
	fmt.Printf("Total events: %d\n", result.TotalEvents)
	fmt.Printf("Streams: %v\n", result.Streams)
	fmt.Printf("Exported at: %s\n", result.ExportedAt.Format(time.RFC3339))
	fmt.Println()

	for _, e := range result.Events {
		fmt.Printf("  [%s] %s (v%d, pos %d)\n", e.StreamID, e.EventType, e.Version, e.GlobalPosition)
		if !e.Redacted {
			fmt.Printf("    Data: %v\n", e.Data)
		}
	}
	fmt.Println()
}

// scanBasedExport shows how to scan all events with filters when you don't know stream IDs.
func scanBasedExport(ctx context.Context, store *mink.EventStore) {
	fmt.Println("=== Scan-Based Export (Filter All Events) ===")
	fmt.Println()

	exporter := mink.NewDataExporter(store, mink.WithExportBatchSize(100))

	// Export all events for tenant A
	result, err := exporter.Export(ctx, mink.ExportRequest{
		SubjectID: "tenant-A-all-data",
		Filter:    mink.FilterByTenantID("A"),
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Tenant A events: %d (across %d streams)\n", result.TotalEvents, len(result.Streams))

	// Export only orders using combined filters
	result, err = exporter.Export(ctx, mink.ExportRequest{
		SubjectID: "tenant-A-orders",
		Filter: mink.CombineFilters(
			mink.FilterByTenantID("A"),
			mink.FilterByEventTypes("OrderPlaced"),
		),
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Tenant A orders: %d\n", result.TotalEvents)

	// Export by stream prefix
	result, err = exporter.Export(ctx, mink.ExportRequest{
		SubjectID: "all-customers",
		Filter:    mink.FilterByStreamPrefix("Customer-"),
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("All customer events: %d\n", result.TotalEvents)

	// Export by user ID
	result, err = exporter.Export(ctx, mink.ExportRequest{
		SubjectID: "alice-activity",
		Filter:    mink.FilterByUserID("alice-1"),
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Events by user alice-1: %d\n", result.TotalEvents)
	fmt.Println()
}

// streamingExport shows memory-efficient export via handler callback.
func streamingExport(ctx context.Context, store *mink.EventStore) {
	fmt.Println("=== Streaming Export (Memory-Efficient) ===")
	fmt.Println()

	exporter := mink.NewDataExporter(store)

	// Stream events one by one — suitable for large exports
	count := 0
	err := exporter.ExportStream(ctx, mink.ExportRequest{
		SubjectID: "alice-1",
		Streams:   []string{"Customer-alice-1", "Order-ord-1"},
	}, func(_ context.Context, event mink.ExportedEvent) error {
		count++
		fmt.Printf("  Streamed event %d: [%s] %s\n", count, event.StreamID, event.EventType)
		// In production: write to JSON file, send via API, etc.
		return nil
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Streamed %d events total\n", count)
	fmt.Println()
}

// timeRangeExport shows how to limit export to a specific time window.
func timeRangeExport(ctx context.Context, store *mink.EventStore) {
	fmt.Println("=== Time Range Export ===")
	fmt.Println()

	exporter := mink.NewDataExporter(store)

	// Export events from the last hour only
	oneHourAgo := time.Now().Add(-1 * time.Hour)
	result, err := exporter.Export(ctx, mink.ExportRequest{
		SubjectID: "alice-recent",
		Streams:   []string{"Customer-alice-1", "Order-ord-1"},
		FromTime:  &oneHourAgo,
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Events in last hour: %d\n", result.TotalEvents)

	// Export events up to a specific cutoff
	cutoff := time.Now().Add(1 * time.Hour)
	result, err = exporter.Export(ctx, mink.ExportRequest{
		SubjectID: "alice-historical",
		Streams:   []string{"Customer-alice-1"},
		ToTime:    &cutoff,
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Events before cutoff: %d\n", result.TotalEvents)
	fmt.Println()
}

// cryptoShreddingExport shows how export handles events after key revocation.
func cryptoShreddingExport(ctx context.Context, store *mink.EventStore, provider *local.Provider) {
	fmt.Println("=== Crypto-Shredding + Export (GDPR Right to Erasure) ===")
	fmt.Println()

	exporter := mink.NewDataExporter(store)

	// Before revocation — Bob's data exports normally
	result, err := exporter.Export(ctx, mink.ExportRequest{
		SubjectID: "bob-1",
		Streams:   []string{"Customer-bob-1"},
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Before key revocation:\n")
	fmt.Printf("  Events: %d, Redacted: %d\n", result.TotalEvents, result.RedactedCount)
	if !result.Events[0].Redacted {
		fmt.Printf("  Data: %v\n", result.Events[0].Data)
	}
	fmt.Println()

	// Revoke tenant B's key — simulates GDPR deletion request
	fmt.Println("Revoking tenant B encryption key...")
	if err := provider.RevokeKey("tenant-B"); err != nil {
		log.Fatal(err)
	}

	// After revocation — encrypted events are exported as redacted
	result, err = exporter.Export(ctx, mink.ExportRequest{
		SubjectID: "bob-1",
		Streams:   []string{"Customer-bob-1", "Order-ord-2"},
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("\nAfter key revocation:\n")
	fmt.Printf("  Events: %d, Redacted: %d\n", result.TotalEvents, result.RedactedCount)
	for _, e := range result.Events {
		if e.Redacted {
			fmt.Printf("  [REDACTED] %s %s — encrypted data cannot be decrypted\n", e.StreamID, e.EventType)
		} else {
			fmt.Printf("  [OK] %s %s — %v\n", e.StreamID, e.EventType, e.Data)
		}
	}
}

func generateKey() []byte {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		log.Fatal(err)
	}
	return key
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
