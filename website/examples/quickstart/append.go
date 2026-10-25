package main

import (
	"context"

	"go-mink.dev"
)

// snippet:append:start
func appendOrder(ctx context.Context, store *mink.EventStore) error {
	// Register concrete types for typed replay.
	store.RegisterEvents(OrderCreated{}, ItemAdded{})

	events := []any{
		OrderCreated{CustomerID: "customer-456"},
		ItemAdded{SKU: "GO-01", Quantity: 2},
	}

	// NoStream rejects a second creation of this stream.
	return store.Append(ctx, "order-123", events,
		mink.ExpectVersion(mink.NoStream),
	)
}

// snippet:append:end
