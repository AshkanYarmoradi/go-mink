package main

import (
	"context"
	"fmt"

	"go-mink.dev"
)

// snippet:replay:start
func replayOrder(ctx context.Context, store *mink.EventStore) (OrderSummary, error) {
	events, err := store.Load(ctx, "order-123")
	if err != nil {
		return OrderSummary{}, err
	}

	var summary OrderSummary
	for _, event := range events {
		switch data := event.Data.(type) {
		case OrderCreated:
			summary.CustomerID = data.CustomerID
		case ItemAdded:
			summary.Items += data.Quantity
		default:
			return OrderSummary{}, fmt.Errorf("unexpected event: %T", data)
		}
	}
	return summary, nil
}

// snippet:replay:end
