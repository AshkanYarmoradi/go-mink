package mongodb

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	mink "go-mink.dev"
	"go-mink.dev/adapters"
	"go-mink.dev/testing/benchmarks"
)

type benchmarkOrderSummary struct {
	ID          string    `mink:"id,pk"`
	CustomerID  string    `mink:"customer_id,index"`
	Status      string    `mink:"status,index"`
	TotalAmount float64   `mink:"total_amount"`
	UpdatedAt   time.Time `mink:"updated_at"`
}

func benchmarkMongoURL() string {
	return os.Getenv("TEST_MONGODB_URL")
}

func benchmarkDatabase() string {
	return fmt.Sprintf("bench_%d", time.Now().UnixNano())
}

func newBenchmarkAdapter(b *testing.B, opts ...Option) (*MongoAdapter, context.Context) {
	b.Helper()
	if testing.Short() {
		b.Skip("Skipping integration benchmark in short mode")
	}
	if benchmarkMongoURL() == "" {
		b.Skip("TEST_MONGODB_URL not set")
	}

	ctx := context.Background()
	all := append([]Option{WithDatabase(benchmarkDatabase())}, opts...)
	adapter, err := NewAdapter(benchmarkMongoURL(), all...)
	if err != nil {
		b.Fatalf("failed to create adapter: %v", err)
	}
	b.Cleanup(func() {
		_ = adapter.Database().Drop(context.Background())
		_ = adapter.Close()
	})
	if err := adapter.Initialize(ctx); err != nil {
		b.Fatalf("failed to initialize adapter: %v", err)
	}
	return adapter, ctx
}

func newBenchmarkFactory() benchmarks.AdapterBenchmarkFactory {
	return benchmarks.AdapterBenchmarkFactory{
		Name: "mongodb",
		CreateAdapter: func() (adapters.EventStoreAdapter, func(), error) {
			uri := benchmarkMongoURL()
			if uri == "" {
				return nil, nil, fmt.Errorf("TEST_MONGODB_URL not set")
			}
			a, err := NewAdapter(uri, WithDatabase(benchmarkDatabase()), WithTransactionMode(TransactionModeAuto))
			if err != nil {
				return nil, nil, err
			}
			cleanup := func() {
				_ = a.Database().Drop(context.Background())
				_ = a.Close()
			}
			return a, cleanup, nil
		},
		Skip: func() string {
			if testing.Short() {
				return "Skipping integration benchmark in short mode"
			}
			if benchmarkMongoURL() == "" {
				return "TEST_MONGODB_URL not set"
			}
			return ""
		},
		Scale: &benchmarks.ScaleConfig{EventCount: 100_000},
	}
}

func BenchmarkAdapter(b *testing.B) {
	benchmarks.Run(b, newBenchmarkFactory())
}

func BenchmarkReadModelRepository(b *testing.B) {
	adapter, ctx := newBenchmarkAdapter(b)
	repo, err := NewMongoRepositoryFromAdapter[benchmarkOrderSummary](adapter, WithReadModelCollection("order_summaries"))
	if err != nil {
		b.Fatalf("failed to create repository: %v", err)
	}

	b.Run("Upsert", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			err := repo.Upsert(ctx, &benchmarkOrderSummary{
				ID:          uuid.NewString(),
				CustomerID:  fmt.Sprintf("customer-%d", i%100),
				Status:      "pending",
				TotalAmount: float64(i),
				UpdatedAt:   time.Now().UTC(),
			})
			if err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("Get", func(b *testing.B) {
		id := uuid.NewString()
		if err := repo.Upsert(ctx, &benchmarkOrderSummary{ID: id, CustomerID: "customer-get", Status: "pending"}); err != nil {
			b.Fatal(err)
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, err := repo.Get(ctx, id); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("Find", func(b *testing.B) {
		query := mink.NewQuery().
			Where("customer_id", mink.FilterOpEq, "customer-10").
			WithLimit(20).
			Build()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, err := repo.Find(ctx, query); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkAppendTransactionModes(b *testing.B) {
	tests := []struct {
		name string
		mode TransactionMode
	}{
		{name: "transactions_required", mode: TransactionModeRequired},
		{name: "transactions_disabled", mode: TransactionModeDisabled},
	}

	for _, tt := range tests {
		b.Run(tt.name, func(b *testing.B) {
			adapter, ctx := newBenchmarkAdapter(b, WithTransactionMode(tt.mode))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, err := adapter.Append(ctx, fmt.Sprintf("order-%s-%d", tt.name, i), []adapters.EventRecord{{
					Type: "OrderCreated",
					Data: []byte(`{"status":"created"}`),
				}}, NoStream)
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkReadModelTransactions(b *testing.B) {
	adapter, ctx := newBenchmarkAdapter(b, WithTransactionMode(TransactionModeRequired))
	repo, err := NewMongoRepositoryFromAdapter[benchmarkOrderSummary](adapter, WithReadModelCollection("tx_order_summaries"))
	if err != nil {
		b.Fatalf("failed to create repository: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		err := repo.RunTransaction(ctx, func(txCtx context.Context, tx *TxRepository[benchmarkOrderSummary]) error {
			return tx.Upsert(txCtx, &benchmarkOrderSummary{
				ID:          uuid.NewString(),
				CustomerID:  fmt.Sprintf("customer-%d", i%100),
				Status:      "pending",
				TotalAmount: float64(i),
				UpdatedAt:   time.Now().UTC(),
			})
		})
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkProjectionTransactions(b *testing.B) {
	adapter, ctx := newBenchmarkAdapter(b, WithTransactionMode(TransactionModeRequired))
	repo, err := NewMongoRepositoryFromAdapter[benchmarkOrderSummary](adapter, WithReadModelCollection("projection_order_summaries"))
	if err != nil {
		b.Fatalf("failed to create repository: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		err := adapter.RunProjectionTransaction(ctx, "benchmark-projection", func(txCtx context.Context) error {
			if err := repo.Upsert(txCtx, &benchmarkOrderSummary{
				ID:          uuid.NewString(),
				CustomerID:  fmt.Sprintf("customer-%d", i%100),
				Status:      "projected",
				TotalAmount: float64(i),
				UpdatedAt:   time.Now().UTC(),
			}); err != nil {
				return err
			}
			return adapter.SetCheckpoint(txCtx, "benchmark-projection", uint64(i+1))
		})
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSubscriptionWakeLatency(b *testing.B) {
	adapter, ctx := newBenchmarkAdapter(
		b,
		WithTransactionMode(TransactionModeRequired),
		WithSubscriptionMode(SubscriptionModeChangeStream),
	)

	subCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	ch, err := adapter.SubscribeAll(subCtx, 0, adapters.SubscriptionOptions{
		ResumeTokenKey: "benchmark-wake-latency",
		OnError: func(err error) {
			if err != nil && !errors.Is(err, context.Canceled) {
				b.Logf("subscription wake benchmark error: %v", err)
			}
		},
	})
	if err != nil {
		b.Fatalf("failed to subscribe: %v", err)
	}

	var totalWake time.Duration
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		start := time.Now()
		_, err := adapter.Append(ctx, fmt.Sprintf("wake-%d", i), []adapters.EventRecord{{
			Type: "WakeEvent",
			Data: []byte(`{}`),
		}}, NoStream)
		if err != nil {
			b.Fatal(err)
		}
		select {
		case <-ch:
			totalWake += time.Since(start)
		case <-time.After(10 * time.Second):
			b.Fatal("timed out waiting for subscription wake")
		}
	}
	b.StopTimer()
	if b.N > 0 {
		b.ReportMetric(float64(totalWake.Microseconds())/float64(b.N), "wake_us/op")
	}
}

func TestAdapterScale(t *testing.T) {
	if os.Getenv("MINK_SCALE_TESTS") != "1" {
		t.Skip("Skipping scale test; set MINK_SCALE_TESTS=1 to enable")
	}
	benchmarks.RunScale(t, newBenchmarkFactory())
}
