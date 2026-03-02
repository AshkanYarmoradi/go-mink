package memory

import (
	"os"
	"testing"

	"github.com/AshkanYarmoradi/go-mink/adapters"
	"github.com/AshkanYarmoradi/go-mink/testing/benchmarks"
)

func newBenchmarkFactory() benchmarks.AdapterBenchmarkFactory {
	return benchmarks.AdapterBenchmarkFactory{
		Name: "memory",
		CreateAdapter: func() (adapters.EventStoreAdapter, func(), error) {
			a := NewAdapter()
			return a, func() { _ = a.Close() }, nil
		},
		Scale: &benchmarks.ScaleConfig{EventCount: 1_000_000},
	}
}

func BenchmarkAdapter(b *testing.B) {
	benchmarks.Run(b, newBenchmarkFactory())
}

func TestAdapterScale(t *testing.T) {
	if os.Getenv("MINK_SCALE_TESTS") != "1" {
		t.Skip("Skipping scale test; set MINK_SCALE_TESTS=1 to enable")
	}
	benchmarks.RunScale(t, newBenchmarkFactory())
}
