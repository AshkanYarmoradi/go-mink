// Package main demonstrates GDPR data export (right to access / data portability) in go-mink.
//
// This example shows:
//   - Subject tagging: a SubjectTagger records which data subject each event concerns
//   - Subject export: SubjectResolver + DataExporter export a subject's complete footprint
//   - Stream-based export: export specific streams by ID (efficient, no scan)
//   - Scan-based export: filter all events using built-in and custom filters
//   - Streaming export: memory-efficient export via handler callback
//   - Crypto-shredding: exporting data after encryption key revocation (redacted events)
//   - Time range filtering: export events within a date range (subject-scoped by default;
//     an explicit Filter such as FilterByStreams exports whole streams)
//   - Combined filters: AND-compose multiple filters
//
// NOTE: encryption/local keeps keys in process memory and exists for development and
// tests. Production deployments use encryption/kms or encryption/vault.
package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
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
	PaymentID  string  `json:"payment_id"`
	OrderID    string  `json:"order_id"`
	CustomerID string  `json:"customer_id"`
	Amount     float64 `json:"amount"`
}

// subjectTagger tells the store which data subject each event concerns. The store applies
// it at append time (Append, SaveAggregate and the outbox alike) and records the ids in
// Metadata.Custom["$subjects"], which is what SubjectFilter and SubjectResolver match on.
//
// The subject is derived from the event PAYLOAD (customer_id), never from a
// caller-supplied metadata value, so a writer cannot attribute an event to someone else.
// Events without a customer_id are left untagged.
func subjectTagger(_ string, data []byte, _ mink.Metadata) []string {
	var v struct {
		CustomerID string `json:"customer_id"`
	}
	if err := json.Unmarshal(data, &v); err != nil || v.CustomerID == "" {
		return nil
	}
	return []string{v.CustomerID}
}

// newProvider builds the development/testing key provider used by this example.
func newProvider() (*local.Provider, error) {
	return local.New(
		local.WithKey("tenant-A", generateKey()),
		local.WithKey("tenant-B", generateKey()),
	)
}

// newStore wires field-level encryption and subject tagging into an in-memory store.
func newStore(provider *local.Provider) *mink.EventStore {
	encConfig := mink.NewFieldEncryptionConfig(
		mink.WithEncryptionProvider(provider),
		mink.WithDefaultKeyID("tenant-A"),
		mink.WithEncryptedFields("CustomerCreated", "email", "phone"),
		mink.WithTenantKeyResolver(func(tenantID string) string {
			return "tenant-" + tenantID
		}),
	)

	store := mink.New(memory.NewAdapter(),
		mink.WithFieldEncryption(encConfig),
		// Subject tags are what make a subject-scoped export (and erasure) possible.
		mink.WithSubjectTagger(subjectTagger),
	)
	store.RegisterEvents(CustomerCreated{}, OrderPlaced{}, PaymentReceived{})
	return store
}

func main() {
	ctx := context.Background()

	provider, err := newProvider()
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = provider.Close() }()

	store := newStore(provider)

	// Seed data for two tenants
	must(seedData(ctx, store))

	// ── Demo 1: Subject export (the GDPR path) ──
	subjectExport(ctx, store)

	// ── Demo 2: Stream-based export ──
	streamBasedExport(ctx, store)

	// ── Demo 3: Scan-based export with filters ──
	scanBasedExport(ctx, store)

	// ── Demo 4: Streaming export ──
	streamingExport(ctx, store)

	// ── Demo 5: Time range filtering ──
	timeRangeExport(ctx, store)

	// ── Demo 6: Crypto-shredding and export ──
	cryptoShreddingExport(ctx, store, provider)

	fmt.Println("\nDone!")
}

func seedData(ctx context.Context, store *mink.EventStore) error {
	type seed struct {
		stream string
		event  interface{}
		md     mink.Metadata
	}
	seeds := []seed{
		// Tenant A — Alice. Note the ACTORS: her customer record is written by "admin" and
		// her payment by "system"; only the order is written by Alice herself. That is why an
		// actor filter (FilterByUserID) is not a substitute for a subject footprint.
		{"Customer-alice-1", CustomerCreated{CustomerID: "alice-1", Name: "Alice Smith", Email: "alice@example.com", Phone: "+1-555-0100"}, mink.Metadata{TenantID: "A", UserID: "admin"}},
		{"Order-ord-1", OrderPlaced{OrderID: "ord-1", CustomerID: "alice-1", Amount: 149.99}, mink.Metadata{TenantID: "A", UserID: "alice-1"}},
		{"Payment-pay-1", PaymentReceived{PaymentID: "pay-1", OrderID: "ord-1", CustomerID: "alice-1", Amount: 149.99}, mink.Metadata{TenantID: "A", UserID: "system"}},
		// Tenant B — Bob
		{"Customer-bob-1", CustomerCreated{CustomerID: "bob-1", Name: "Bob Jones", Email: "bob@example.com", Phone: "+44-20-1234"}, mink.Metadata{TenantID: "B", UserID: "admin"}},
		{"Order-ord-2", OrderPlaced{OrderID: "ord-2", CustomerID: "bob-1", Amount: 79.99}, mink.Metadata{TenantID: "B", UserID: "bob-1"}},
	}
	for _, s := range seeds {
		if err := store.Append(ctx, s.stream, []interface{}{s.event}, mink.WithAppendMetadata(s.md)); err != nil {
			return err
		}
	}
	return nil
}

// subjectExport is the recommended way to answer a data-subject access request. A
// SubjectResolver turns the subject id into its complete cross-stream footprint (from the
// $subjects tags the tagger recorded), and the exporter constrains the result to that
// subject's events — so a stream shared with other subjects never leaks their data. The
// caller supplies neither stream IDs nor a filter, and the result says whether
// completeness could be proven (Partial).
func subjectExport(ctx context.Context, store *mink.EventStore) {
	fmt.Println("=== Subject Export (GDPR Right to Access, resolver-based) ===")
	fmt.Println()

	resolver := mink.NewSubjectResolver(store)
	exporter := mink.NewDataExporter(store, mink.WithExportSubjectResolver(resolver))

	result, err := exporter.Export(ctx, mink.ExportRequest{SubjectID: "alice-1"})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Subject: %s\n", result.SubjectID)
	fmt.Printf("Footprint: %d events across %v\n", result.TotalEvents, result.Streams)
	fmt.Printf("Partial (completeness unproven): %v\n", result.Partial)
	for _, e := range result.Events {
		fmt.Printf("  [%s] %s (v%d)\n", e.StreamID, e.EventType, e.Version)
	}
	fmt.Println()
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

	// Export a data subject by scan. SubjectFilter matches the $subjects tags the store's
	// SubjectTagger recorded at append time — the subject-scoped predicate.
	result, err = exporter.Export(ctx, mink.ExportRequest{
		SubjectID: "alice-1",
		Filter:    mink.SubjectFilter("alice-1"),
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Events tagged for subject alice-1: %d (across %d streams)\n", result.TotalEvents, len(result.Streams))

	// For contrast: FilterByUserID is ACTOR-scoped (Metadata.UserID = who issued the
	// command). It is NOT a subject footprint: Alice's customer record was written by
	// "admin" and her payment by "system", so the actor view misses both. Never answer a
	// data-subject request with an actor filter.
	result, err = exporter.Export(ctx, mink.ExportRequest{
		SubjectID: "alice-1-as-actor",
		Filter:    mink.FilterByUserID("alice-1"),
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Events performed BY alice-1 (actor-scoped, not a footprint): %d\n", result.TotalEvents)
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

// timeRangeDemo is one time-bounded export request and the label it is printed under.
type timeRangeDemo struct {
	label string
	req   mink.ExportRequest
}

// timeRangeDemos builds the requests timeRangeExport runs, relative to now.
//
// SubjectID is never just a label: when Streams is given and Filter is nil, the exporter
// keeps only events tagged for that subject (plus untagged ones), so a request that names
// the wrong subject returns nothing even on the right streams. To export whole streams on
// purpose — an operational snapshot, not a data-subject request — pass an explicit Filter
// such as FilterByStreams, which replaces the default subject scoping.
func timeRangeDemos(now time.Time) []timeRangeDemo {
	oneHourAgo := now.Add(-1 * time.Hour)
	cutoff := now.Add(1 * time.Hour)
	aliceStreams := []string{"Customer-alice-1", "Order-ord-1"}
	return []timeRangeDemo{
		{
			label: "Alice's events in the last hour (SubjectID alice-1 scopes her streams)",
			req:   mink.ExportRequest{SubjectID: "alice-1", Streams: aliceStreams, FromTime: &oneHourAgo},
		},
		{
			label: "Alice's customer events before the cutoff",
			req:   mink.ExportRequest{SubjectID: "alice-1", Streams: []string{"Customer-alice-1"}, ToTime: &cutoff},
		},
		{
			label: "Same streams requested for bob-1 (default scoping drops events tagged for other subjects)",
			req:   mink.ExportRequest{SubjectID: "bob-1", Streams: aliceStreams, FromTime: &oneHourAgo},
		},
		{
			label: "Whole Customer-alice-1 stream before the cutoff (explicit FilterByStreams; SubjectID is only a label here)",
			req: mink.ExportRequest{
				SubjectID: "ops-snapshot",
				Streams:   []string{"Customer-alice-1"},
				Filter:    mink.FilterByStreams("Customer-alice-1"),
				ToTime:    &cutoff,
			},
		},
	}
}

// timeRangeExport shows how to limit an export to a time window — and that the window
// composes with the subject scoping described on timeRangeDemos.
func timeRangeExport(ctx context.Context, store *mink.EventStore) {
	fmt.Println("=== Time Range Export ===")
	fmt.Println()

	exporter := mink.NewDataExporter(store)
	for _, d := range timeRangeDemos(time.Now()) {
		result, err := exporter.Export(ctx, d.req)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("%s: %d\n", d.label, result.TotalEvents)
	}
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
