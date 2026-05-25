// Package main demonstrates MongoDB-backed event storage, outbox messages, and read models.
//
// Prerequisites (from the repository root):
//
//	docker compose -f docker-compose.test.yml up -d --wait mongodb
//
// Run:
//
//	cd examples/mongodb
//	go run main.go
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	mink "go-mink.dev"
	"go-mink.dev/adapters/mongodb"
)

type OrderCreated struct {
	OrderID    string    `json:"orderId"`
	CustomerID string    `json:"customerId"`
	CreatedAt  time.Time `json:"createdAt"`
}

type ItemAdded struct {
	OrderID  string  `json:"orderId"`
	SKU      string  `json:"sku"`
	Quantity int     `json:"quantity"`
	Price    float64 `json:"price"`
}

type OrderSummary struct {
	OrderID     string    `mink:"order_id,pk"`
	CustomerID  string    `mink:"customer_id,index"`
	Status      string    `mink:"status,index"`
	ItemCount   int       `mink:"item_count"`
	TotalAmount float64   `mink:"total_amount"`
	UpdatedAt   time.Time `mink:"updated_at"`
}

func main() {
	ctx := context.Background()
	uri := getenv("TEST_MONGODB_URL", "mongodb://localhost:27017/mink_example?replicaSet=rs0")

	adapter, err := mongodb.NewAdapter(
		uri,
		mongodb.WithDatabase("mink_example"),
		mongodb.WithTransactionMode(mongodb.TransactionModeAuto),
	)
	if err != nil {
		log.Fatal(err)
	}
	defer adapter.Close()
	defer adapter.Database().Drop(context.Background())

	if err := adapter.Database().Drop(ctx); err != nil {
		log.Fatal(err)
	}
	if err := adapter.Initialize(ctx); err != nil {
		log.Fatal(err)
	}

	store := mink.New(adapter)
	store.RegisterEvents(OrderCreated{}, ItemAdded{})

	outboxStore := mongodb.NewOutboxStoreFromAdapter(adapter)
	if err := outboxStore.Initialize(ctx); err != nil {
		log.Fatal(err)
	}

	storeWithOutbox := mink.NewEventStoreWithOutbox(store, outboxStore, []mink.OutboxRoute{
		{
			EventTypes:  []string{"OrderCreated", "ItemAdded"},
			Destination: "webhook:https://example.test/orders",
		},
	})

	streamID := "Order-order-mongo-001"
	if err := storeWithOutbox.Append(ctx, streamID, []interface{}{
		OrderCreated{
			OrderID:    "order-mongo-001",
			CustomerID: "customer-001",
			CreatedAt:  time.Now().UTC(),
		},
	}, mink.ExpectVersion(mink.NoStream)); err != nil {
		log.Fatal(err)
	}

	if err := storeWithOutbox.Append(ctx, streamID, []interface{}{
		ItemAdded{
			OrderID:  "order-mongo-001",
			SKU:      "SKU-MONGO",
			Quantity: 2,
			Price:    19.99,
		},
	}, mink.ExpectVersion(mink.StreamExists)); err != nil {
		log.Fatal(err)
	}

	events, err := store.Load(ctx, streamID)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("loaded %d MongoDB-backed events\n", len(events))

	repo, err := mongodb.NewMongoRepositoryFromAdapter[OrderSummary](adapter,
		mongodb.WithReadModelCollection("order_summaries"),
	)
	if err != nil {
		log.Fatal(err)
	}
	for _, event := range events {
		if err := applyOrderSummary(ctx, repo, event); err != nil {
			log.Fatal(err)
		}
	}

	summaries, err := repo.Find(ctx, mink.NewQuery().
		Where("customer_id", mink.FilterOpEq, "customer-001").
		OrderByDesc("updated_at").
		Build())
	if err != nil {
		log.Fatal(err)
	}
	for _, summary := range summaries {
		fmt.Printf("summary %s: %d item(s), total %.2f\n", summary.OrderID, summary.ItemCount, summary.TotalAmount)
	}

	messages, err := outboxStore.FetchPending(ctx, 10)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("claimed %d pending outbox message(s)\n", len(messages))
}

func applyOrderSummary(ctx context.Context, repo *mongodb.MongoRepository[OrderSummary], event mink.Event) error {
	switch e := event.Data.(type) {
	case OrderCreated:
		return repo.Upsert(ctx, &OrderSummary{
			OrderID:    e.OrderID,
			CustomerID: e.CustomerID,
			Status:     "created",
			UpdatedAt:  event.Timestamp,
		})
	case *OrderCreated:
		return repo.Upsert(ctx, &OrderSummary{
			OrderID:    e.OrderID,
			CustomerID: e.CustomerID,
			Status:     "created",
			UpdatedAt:  event.Timestamp,
		})
	case ItemAdded:
		return addItem(ctx, repo, e.OrderID, e.Quantity, e.Price, event.Timestamp)
	case *ItemAdded:
		return addItem(ctx, repo, e.OrderID, e.Quantity, e.Price, event.Timestamp)
	default:
		return nil
	}
}

func addItem(ctx context.Context, repo *mongodb.MongoRepository[OrderSummary], orderID string, quantity int, price float64, updatedAt time.Time) error {
	summary, err := repo.Get(ctx, orderID)
	if errors.Is(err, mink.ErrNotFound) {
		summary = &OrderSummary{OrderID: orderID, Status: "created"}
	} else if err != nil {
		return err
	}

	summary.ItemCount += quantity
	summary.TotalAmount += float64(quantity) * price
	summary.UpdatedAt = updatedAt
	return repo.Upsert(ctx, summary)
}

func getenv(key, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}
