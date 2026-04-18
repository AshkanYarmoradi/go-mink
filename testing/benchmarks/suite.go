// Package benchmarks provides a shared benchmark suite for EventStoreAdapter implementations.
//
// This package operates at the adapter level with pre-serialized EventRecord values,
// measuring pure adapter I/O throughput without importing the root mink package
// (which would cause an import cycle).
//
// Usage:
//
//	func BenchmarkAdapter(b *testing.B) {
//	    benchmarks.Run(b, benchmarks.AdapterBenchmarkFactory{
//	        Name: "memory",
//	        CreateAdapter: func() (adapters.EventStoreAdapter, func(), error) {
//	            a := memory.NewAdapter()
//	            return a, func() { a.Close() }, nil
//	        },
//	    })
//	}
package benchmarks

import (
	"context"
	"fmt"
	"math/rand/v2"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go-mink.dev/adapters"
)

// AnyVersion is an alias for adapters.AnyVersion, used to skip version checks.
const AnyVersion = adapters.AnyVersion

// AdapterBenchmarkFactory defines how to create an adapter for benchmarking.
type AdapterBenchmarkFactory struct {
	// Name identifies the adapter (e.g., "memory", "postgres").
	Name string

	// CreateAdapter returns a new adapter instance, a cleanup function, and any error.
	// Each benchmark gets a fresh adapter to avoid cross-contamination.
	CreateAdapter func() (adapters.EventStoreAdapter, func(), error)

	// Skip returns a non-empty reason to skip benchmarks (e.g., missing env var).
	// Return "" to run benchmarks.
	Skip func() string

	// Scale overrides the default event count for scale tests.
	// nil uses defaults (1,000,000 for memory, configurable per adapter).
	Scale *ScaleConfig
}

// ScaleConfig overrides scale test parameters.
type ScaleConfig struct {
	EventCount int // Number of events for scale tests.
}

// scaleN returns the event count for scale tests.
func (f *AdapterBenchmarkFactory) scaleN() int {
	if f.Scale != nil && f.Scale.EventCount > 0 {
		return f.Scale.EventCount
	}
	return 1_000_000
}

// newAdapter creates and initializes an adapter, failing the test on error.
func (f *AdapterBenchmarkFactory) newAdapter(tb testing.TB) (adapters.EventStoreAdapter, func()) {
	tb.Helper()
	if f.Skip != nil {
		if reason := f.Skip(); reason != "" {
			tb.Skip(reason)
		}
	}
	adapter, cleanup, err := f.CreateAdapter()
	if err != nil {
		tb.Fatalf("failed to create adapter: %v", err)
	}
	ctx := context.Background()
	if err := adapter.Initialize(ctx); err != nil {
		cleanup()
		tb.Fatalf("failed to initialize adapter: %v", err)
	}
	return adapter, cleanup
}

// Run executes the full Go benchmark suite against the given adapter factory.
func Run(b *testing.B, factory AdapterBenchmarkFactory) {
	b.Run("Append", func(b *testing.B) {
		runAppendBenchmarks(b, &factory)
	})
	b.Run("Load", func(b *testing.B) {
		runLoadBenchmarks(b, &factory)
	})
	b.Run("Concurrent", func(b *testing.B) {
		runConcurrentBenchmarks(b, &factory)
	})
	b.Run("Mixed", func(b *testing.B) {
		runMixedBenchmarks(b, &factory)
	})
	b.Run("StreamOps", func(b *testing.B) {
		runStreamOpsBenchmarks(b, &factory)
	})
}

// RunScale executes scale tests that measure throughput and latency at high event counts.
func RunScale(t *testing.T, factory AdapterBenchmarkFactory) {
	t.Run("Scale_SequentialAppend", func(t *testing.T) {
		runScaleSequentialAppend(t, &factory)
	})
	t.Run("Scale_BatchAppend", func(t *testing.T) {
		runScaleBatchAppend(t, &factory)
	})
	t.Run("Scale_ConcurrentAppend", func(t *testing.T) {
		runScaleConcurrentAppend(t, &factory)
	})
	t.Run("Scale_Load", func(t *testing.T) {
		runScaleLoad(t, &factory)
	})
	t.Run("Scale_ManyStreams", func(t *testing.T) {
		runScaleManyStreams(t, &factory)
	})
}

// ---------------------------------------------------------------------------
// Append benchmarks
// ---------------------------------------------------------------------------

func runAppendBenchmarks(b *testing.B, f *AdapterBenchmarkFactory) {
	b.Run("SingleEvent", func(b *testing.B) {
		adapter, cleanup := f.newAdapter(b)
		defer cleanup()
		ctx := context.Background()
		rec := SmallEventRecord()

		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, err := adapter.Append(ctx, MakeStreamID("bench", i), []adapters.EventRecord{rec}, AnyVersion); err != nil {
				b.Fatalf("append failed at %d: %v", i, err)
			}
		}
		b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "events/sec")
	})

	for _, size := range []int{10, 100} {
		b.Run(fmt.Sprintf("BatchOf%d", size), func(b *testing.B) {
			adapter, cleanup := f.newAdapter(b)
			defer cleanup()
			ctx := context.Background()
			batch := MakeBatch(size)

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := adapter.Append(ctx, MakeStreamID("bench-batch", i), batch, AnyVersion); err != nil {
					b.Fatalf("batch append failed at %d: %v", i, err)
				}
			}
			b.ReportMetric(float64(b.N*size)/b.Elapsed().Seconds(), "events/sec")
		})
	}

	b.Run("WithMetadata", func(b *testing.B) {
		adapter, cleanup := f.newAdapter(b)
		defer cleanup()
		ctx := context.Background()
		rec := MetadataEventRecord()

		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, err := adapter.Append(ctx, MakeStreamID("bench-meta", i), []adapters.EventRecord{rec}, AnyVersion); err != nil {
				b.Fatalf("append failed at %d: %v", i, err)
			}
		}
		b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "events/sec")
	})

	b.Run("WithConcurrencyCheck", func(b *testing.B) {
		adapter, cleanup := f.newAdapter(b)
		defer cleanup()
		ctx := context.Background()
		rec := SmallEventRecord()

		// Pre-populate streams so we can do version-checked appends.
		for i := 0; i < b.N; i++ {
			if _, err := adapter.Append(ctx, MakeStreamID("bench-cc", i), []adapters.EventRecord{rec}, AnyVersion); err != nil {
				b.Fatalf("pre-populate failed at %d: %v", i, err)
			}
		}

		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, err := adapter.Append(ctx, MakeStreamID("bench-cc", i), []adapters.EventRecord{rec}, 1); err != nil {
				b.Fatalf("version-checked append failed at %d: %v", i, err)
			}
		}
		b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "events/sec")
	})
}

// ---------------------------------------------------------------------------
// Load benchmarks
// ---------------------------------------------------------------------------

func runLoadBenchmarks(b *testing.B, f *AdapterBenchmarkFactory) {
	for _, size := range []int{10, 100, 1000} {
		b.Run(fmt.Sprintf("%dEvents", size), func(b *testing.B) {
			adapter, cleanup := f.newAdapter(b)
			defer cleanup()
			ctx := context.Background()

			// Seed the stream.
			batch := MakeBatch(size)
			_, err := adapter.Append(ctx, "bench-load", batch, AnyVersion)
			if err != nil {
				b.Fatalf("seed failed: %v", err)
			}

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := adapter.Load(ctx, "bench-load", 0); err != nil {
					b.Fatalf("load failed at %d: %v", i, err)
				}
			}
			b.ReportMetric(float64(b.N*size)/b.Elapsed().Seconds(), "events/sec")
		})
	}
}

// ---------------------------------------------------------------------------
// Concurrent benchmarks
// ---------------------------------------------------------------------------

func runConcurrentBenchmarks(b *testing.B, f *AdapterBenchmarkFactory) {
	for _, workers := range []int{8, 32} {
		b.Run(fmt.Sprintf("Append_%dWorkers", workers), func(b *testing.B) {
			adapter, cleanup := f.newAdapter(b)
			defer cleanup()
			ctx := context.Background()
			rec := SmallEventRecord()
			var counter atomic.Int64

			p := workers / runtime.GOMAXPROCS(0)
			if p < 1 {
				p = 1
			}
			b.SetParallelism(p)
			b.ReportAllocs()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					n := counter.Add(1)
					if _, err := adapter.Append(ctx, MakeStreamID("bench-conc", int(n)), []adapters.EventRecord{rec}, AnyVersion); err != nil {
						b.Fatalf("concurrent append failed: %v", err)
					}
				}
			})
			b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "events/sec")
		})
	}
}

// ---------------------------------------------------------------------------
// Mixed workload benchmarks
// ---------------------------------------------------------------------------

func runMixedBenchmarks(b *testing.B, f *AdapterBenchmarkFactory) {
	b.Run("ReadHeavy_80_20", func(b *testing.B) {
		runMixed(b, f, 80)
	})
	b.Run("WriteHeavy_20_80", func(b *testing.B) {
		runMixed(b, f, 20)
	})
}

func runMixed(b *testing.B, f *AdapterBenchmarkFactory, readPct int) {
	adapter, cleanup := f.newAdapter(b)
	defer cleanup()
	ctx := context.Background()
	rec := SmallEventRecord()

	// Seed 100 streams with 10 events each.
	const seedStreams = 100
	batch := MakeBatch(10)
	for i := 0; i < seedStreams; i++ {
		if _, err := adapter.Append(ctx, MakeStreamID("bench-mix", i), batch, AnyVersion); err != nil {
			b.Fatalf("seed failed at %d: %v", i, err)
		}
	}

	var writeCounter atomic.Int64
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if rand.IntN(100) < readPct {
				streamIdx := rand.IntN(seedStreams)
				if _, err := adapter.Load(ctx, MakeStreamID("bench-mix", streamIdx), 0); err != nil {
					b.Fatalf("mixed load failed: %v", err)
				}
			} else {
				n := writeCounter.Add(1)
				if _, err := adapter.Append(ctx, MakeStreamID("bench-mix-w", int(n)), []adapters.EventRecord{rec}, AnyVersion); err != nil {
					b.Fatalf("mixed append failed: %v", err)
				}
			}
		}
	})
	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "ops/sec")
}

// ---------------------------------------------------------------------------
// Stream operations benchmarks
// ---------------------------------------------------------------------------

func runStreamOpsBenchmarks(b *testing.B, f *AdapterBenchmarkFactory) {
	b.Run("GetStreamInfo", func(b *testing.B) {
		adapter, cleanup := f.newAdapter(b)
		defer cleanup()
		ctx := context.Background()

		// Seed a stream.
		batch := MakeBatch(50)
		if _, err := adapter.Append(ctx, "bench-info", batch, AnyVersion); err != nil {
			b.Fatalf("seed failed: %v", err)
		}

		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, err := adapter.GetStreamInfo(ctx, "bench-info"); err != nil {
				b.Fatalf("GetStreamInfo failed at %d: %v", i, err)
			}
		}
		b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "ops/sec")
	})

	b.Run("GetLastPosition", func(b *testing.B) {
		adapter, cleanup := f.newAdapter(b)
		defer cleanup()
		ctx := context.Background()

		// Seed some events.
		batch := MakeBatch(50)
		if _, err := adapter.Append(ctx, "bench-pos", batch, AnyVersion); err != nil {
			b.Fatalf("seed failed: %v", err)
		}

		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, err := adapter.GetLastPosition(ctx); err != nil {
				b.Fatalf("GetLastPosition failed at %d: %v", i, err)
			}
		}
		b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "ops/sec")
	})
}

// ---------------------------------------------------------------------------
// Scale tests
// ---------------------------------------------------------------------------

func runScaleSequentialAppend(t *testing.T, f *AdapterBenchmarkFactory) {
	if testing.Short() {
		t.Skip("Skipping scale test in short mode")
	}
	adapter, cleanup := f.newAdapter(t)
	defer cleanup()
	ctx := context.Background()
	rec := SmallEventRecord()
	n := f.scaleN()

	lc := NewLatencyCollector(n)
	var memBefore, memAfter runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&memBefore)

	start := time.Now()
	for i := 0; i < n; i++ {
		opStart := time.Now()
		_, err := adapter.Append(ctx, MakeStreamID("scale-seq", i), []adapters.EventRecord{rec}, AnyVersion)
		lc.Record(time.Since(opStart))
		if err != nil {
			t.Fatalf("append failed at %d: %v", i, err)
		}
	}
	elapsed := time.Since(start)
	runtime.ReadMemStats(&memAfter)

	report := lc.Report()
	throughput := float64(n) / elapsed.Seconds()
	allocMB, heapMB := memStatsDelta(memBefore, memAfter)

	t.Logf("Sequential Append: %d events in %v (%.0f events/sec)", n, elapsed, throughput)
	t.Logf("Latency: %s", report)
	t.Logf("Memory: allocs=%.1f MB, heap=%.1f MB", allocMB, heapMB)
}

func runScaleBatchAppend(t *testing.T, f *AdapterBenchmarkFactory) {
	if testing.Short() {
		t.Skip("Skipping scale test in short mode")
	}
	adapter, cleanup := f.newAdapter(t)
	defer cleanup()
	ctx := context.Background()
	n := f.scaleN()
	const batchSize = 100
	fullBatches := n / batchSize
	remainder := n % batchSize
	totalBatches := fullBatches
	if remainder > 0 {
		totalBatches++
	}

	lc := NewLatencyCollector(totalBatches)
	var memBefore, memAfter runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&memBefore)

	start := time.Now()
	for i := 0; i < fullBatches; i++ {
		batch := MakeBatch(batchSize)
		opStart := time.Now()
		_, err := adapter.Append(ctx, MakeStreamID("scale-batch", i), batch, AnyVersion)
		lc.Record(time.Since(opStart))
		if err != nil {
			t.Fatalf("batch append failed at %d: %v", i, err)
		}
	}
	if remainder > 0 {
		batch := MakeBatch(remainder)
		opStart := time.Now()
		_, err := adapter.Append(ctx, MakeStreamID("scale-batch", fullBatches), batch, AnyVersion)
		lc.Record(time.Since(opStart))
		if err != nil {
			t.Fatalf("remainder batch append failed: %v", err)
		}
	}
	elapsed := time.Since(start)
	runtime.ReadMemStats(&memAfter)

	report := lc.Report()
	throughput := float64(n) / elapsed.Seconds()
	allocMB, heapMB := memStatsDelta(memBefore, memAfter)

	t.Logf("Batch Append: %d events (%d batches of %d) in %v (%.0f events/sec)", n, totalBatches, batchSize, elapsed, throughput)
	t.Logf("Latency (per batch): %s", report)
	t.Logf("Memory: allocs=%.1f MB, heap=%.1f MB", allocMB, heapMB)
}

func runScaleConcurrentAppend(t *testing.T, f *AdapterBenchmarkFactory) {
	if testing.Short() {
		t.Skip("Skipping scale test in short mode")
	}
	adapter, cleanup := f.newAdapter(t)
	defer cleanup()
	ctx := context.Background()
	n := f.scaleN()
	const workers = 8
	perWorker := n / workers
	remainder := n - (perWorker * workers)

	var memBefore, memAfter runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&memBefore)

	start := time.Now()
	var wg sync.WaitGroup
	var errCount atomic.Int64

	for w := 0; w < workers; w++ {
		wg.Add(1)
		// Distribute remainder across the first workers.
		count := perWorker
		if w < remainder {
			count++
		}
		go func(workerID, count int) {
			defer wg.Done()
			rec := SmallEventRecord()
			base := workerID * perWorker
			if workerID < remainder {
				base += workerID
			} else {
				base += remainder
			}
			for i := 0; i < count; i++ {
				_, err := adapter.Append(ctx, MakeStreamID("scale-conc", base+i), []adapters.EventRecord{rec}, AnyVersion)
				if err != nil {
					errCount.Add(1)
				}
			}
		}(w, count)
	}
	wg.Wait()
	elapsed := time.Since(start)
	runtime.ReadMemStats(&memAfter)

	throughput := float64(n) / elapsed.Seconds()
	allocMB, heapMB := memStatsDelta(memBefore, memAfter)

	t.Logf("Concurrent Append: %d events across %d workers in %v (%.0f events/sec)", n, workers, elapsed, throughput)
	t.Logf("Errors: %d", errCount.Load())
	t.Logf("Memory: allocs=%.1f MB, heap=%.1f MB", allocMB, heapMB)

	if errCount.Load() > 0 {
		t.Fatalf("concurrent append had %d errors", errCount.Load())
	}
}

func runScaleLoad(t *testing.T, f *AdapterBenchmarkFactory) {
	if testing.Short() {
		t.Skip("Skipping scale test in short mode")
	}
	adapter, cleanup := f.newAdapter(t)
	defer cleanup()
	ctx := context.Background()
	n := f.scaleN()

	// Cap single-stream load at 100k to avoid excessive memory use.
	loadSize := n
	if loadSize > 100_000 {
		loadSize = 100_000
	}

	// Seed the stream in batches.
	const batchSize = 1000
	seeded := 0
	for i := 0; i < loadSize/batchSize; i++ {
		batch := MakeBatch(batchSize)
		_, err := adapter.Append(ctx, "scale-load-stream", batch, AnyVersion)
		if err != nil {
			t.Fatalf("seed failed at batch %d: %v", i, err)
		}
		seeded += batchSize
	}
	if remainder := loadSize - seeded; remainder > 0 {
		batch := MakeBatch(remainder)
		_, err := adapter.Append(ctx, "scale-load-stream", batch, AnyVersion)
		if err != nil {
			t.Fatalf("seed remainder failed: %v", err)
		}
	}

	// Measure load time.
	lc := NewLatencyCollector(10)
	const loadIterations = 10
	for i := 0; i < loadIterations; i++ {
		opStart := time.Now()
		events, err := adapter.Load(ctx, "scale-load-stream", 0)
		lc.Record(time.Since(opStart))
		if err != nil {
			t.Fatalf("load failed: %v", err)
		}
		if len(events) != loadSize {
			t.Fatalf("expected %d events, got %d", loadSize, len(events))
		}
	}

	report := lc.Report()
	avgThroughput := float64(loadSize) / report.P50.Seconds()

	t.Logf("Load: %d events, %d iterations", loadSize, loadIterations)
	t.Logf("Latency: %s", report)
	t.Logf("Throughput (at p50): %.0f events/sec", avgThroughput)
}

func runScaleManyStreams(t *testing.T, f *AdapterBenchmarkFactory) {
	if testing.Short() {
		t.Skip("Skipping scale test in short mode")
	}
	adapter, cleanup := f.newAdapter(t)
	defer cleanup()
	ctx := context.Background()
	n := f.scaleN()
	const streams = 1000
	perStream := n / streams
	if perStream < 1 {
		t.Fatalf("EventCount (%d) must be >= streams (%d)", n, streams)
	}
	// Distribute remainder events across the first streams.
	remainder := n - (perStream * streams)

	var memBefore, memAfter runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&memBefore)

	start := time.Now()
	for s := 0; s < streams; s++ {
		// Append in batches to each stream.
		target := perStream
		if s < remainder {
			target++
		}
		remaining := target
		for remaining > 0 {
			batchSize := remaining
			if batchSize > 100 {
				batchSize = 100
			}
			batch := MakeBatch(batchSize)
			_, err := adapter.Append(ctx, MakeStreamID("scale-many", s), batch, AnyVersion)
			if err != nil {
				t.Fatalf("append to stream %d failed: %v", s, err)
			}
			remaining -= batchSize
		}
	}
	elapsed := time.Since(start)
	runtime.ReadMemStats(&memAfter)

	throughput := float64(n) / elapsed.Seconds()
	allocMB, heapMB := memStatsDelta(memBefore, memAfter)

	t.Logf("Many Streams: %d events across %d streams in %v (%.0f events/sec)", n, streams, elapsed, throughput)
	t.Logf("Memory: allocs=%.1f MB, heap=%.1f MB", allocMB, heapMB)
}

// memStatsDelta computes the difference in memory stats.
func memStatsDelta(before, after runtime.MemStats) (allocsMB, heapMB float64) {
	allocsMB = float64(after.TotalAlloc-before.TotalAlloc) / (1024 * 1024)
	heapMB = float64(after.HeapInuse) / (1024 * 1024)
	return
}
