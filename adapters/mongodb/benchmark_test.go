package mongodb

import (
	"context"
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
	if testing.Short() {
		b.Skip("Skipping integration benchmark in short mode")
	}
	if benchmarkMongoURL() == "" {
		b.Skip("TEST_MONGODB_URL not set")
	}

	ctx := context.Background()
	adapter, err := NewAdapter(benchmarkMongoURL(), WithDatabase(benchmarkDatabase()))
	if err != nil {
		b.Fatalf("failed to create adapter: %v", err)
	}
	defer func() {
		_ = adapter.Database().Drop(context.Background())
		_ = adapter.Close()
	}()
	if err := adapter.Initialize(ctx); err != nil {
		b.Fatalf("failed to initialize adapter: %v", err)
	}

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

func TestAdapterScale(t *testing.T) {
	if os.Getenv("MINK_SCALE_TESTS") != "1" {
		t.Skip("Skipping scale test; set MINK_SCALE_TESTS=1 to enable")
	}
	benchmarks.RunScale(t, newBenchmarkFactory())
}
