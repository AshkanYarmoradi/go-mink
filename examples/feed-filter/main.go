// Package main demonstrates filtered feed reads — reading the global event feed by
// an INDEXED axis for introspection.
//
// EventStore.LoadEventsFromPositionFiltered filters the load-from-position read by
// event type, stream id, and/or stream category, pushing the predicate down to
// storage instead of loading the whole feed and filtering in Go. It is meant for
// introspection tooling — audit browsers, migration/backfill scanners, diagnostics —
// not application read paths (project a read model for those).
//
// Runs on the in-memory adapter, so it needs NO database:
//
//	go run ./examples/feed-filter
package main

import (
	"context"
	"fmt"
	"log"

	"go-mink.dev"
	"go-mink.dev/adapters"
	"go-mink.dev/adapters/memory"
)

func main() {
	ctx := context.Background()

	// A store over the in-memory adapter. We seed a small cross-stream, cross-type
	// feed directly through the adapter (no serializer setup needed for a read demo),
	// then read it back through the store's public filtered API.
	adapter := memory.NewAdapter()
	store := mink.New(adapter)

	seed := []struct{ stream, typ string }{
		{"order-1001", "OrderPlaced"},
		{"order-1001", "OrderPaid"},
		{"order-1002", "OrderPlaced"},
		{"invoice-77", "InvoiceIssued"},
		{"order-1002", "OrderShipped"},
	}
	for _, e := range seed {
		if _, err := adapter.Append(ctx, e.stream,
			[]adapters.EventRecord{{Type: e.typ, Data: []byte(`{}`)}},
			mink.AnyVersion); err != nil {
			log.Fatalf("seed append: %v", err)
		}
	}

	// 1) By event type — "show me every OrderPlaced across all streams".
	byType, err := store.LoadEventsFromPositionFiltered(ctx, 0, 100,
		mink.FeedFilter{EventTypes: []string{"OrderPlaced"}})
	must(err)
	dump("OrderPlaced events (any stream)", byType)

	// 2) By category — the whole "order" aggregate feed, not invoices.
	byCategory, err := store.LoadEventsFromPositionFiltered(ctx, 0, 100,
		mink.FeedFilter{Category: "order"})
	must(err)
	dump("All order-* events", byCategory)

	// 3) Combined — axes AND-compose: one stream AND one type.
	combined, err := store.LoadEventsFromPositionFiltered(ctx, 0, 100,
		mink.FeedFilter{StreamIDs: []string{"order-1002"}, EventTypes: []string{"OrderShipped"}})
	must(err)
	dump("order-1002 / OrderShipped only", combined)

	// An empty filter is exactly LoadEventsFromPosition — the whole feed.
	all, err := store.LoadEventsFromPositionFiltered(ctx, 0, 100, mink.FeedFilter{})
	must(err)
	fmt.Printf("\nWhole feed (empty filter): %d events\n", len(all))
}

func dump(title string, events []mink.StoredEvent) {
	fmt.Printf("\n%s:\n", title)
	for _, e := range events {
		fmt.Printf("  #%d  %-11s  %s\n", e.GlobalPosition, e.StreamID, e.Type)
	}
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
