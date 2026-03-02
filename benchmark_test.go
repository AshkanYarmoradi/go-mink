package mink

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AshkanYarmoradi/go-mink/adapters/memory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// Benchmark Domain Types
// ============================================================================

type BenchOrderCreated struct {
	OrderID    string  `json:"orderId"`
	CustomerID string  `json:"customerId"`
	Total      float64 `json:"total"`
	Currency   string  `json:"currency"`
}

type BenchItemAdded struct {
	OrderID  string  `json:"orderId"`
	SKU      string  `json:"sku"`
	Name     string  `json:"name"`
	Quantity int     `json:"quantity"`
	Price    float64 `json:"price"`
}

type BenchOrderShipped struct {
	OrderID    string `json:"orderId"`
	TrackingID string `json:"trackingId"`
	Carrier    string `json:"carrier"`
}

type BenchPaymentReceived struct {
	OrderID       string  `json:"orderId"`
	PaymentID     string  `json:"paymentId"`
	Amount        float64 `json:"amount"`
	Method        string  `json:"method"`
	TransactionID string  `json:"transactionId"`
}

type BenchAddressChanged struct {
	OrderID string `json:"orderId"`
	Street  string `json:"street"`
	City    string `json:"city"`
	State   string `json:"state"`
	Zip     string `json:"zip"`
	Country string `json:"country"`
}

// BenchOrder is a benchmark aggregate with realistic fields.
type BenchOrder struct {
	AggregateBase
	CustomerID string
	Items      []benchItem
	Status     string
	Total      float64
	Shipped    bool
}

type benchItem struct {
	SKU      string
	Name     string
	Quantity int
	Price    float64
}

func newBenchOrder(id string) *BenchOrder {
	return &BenchOrder{
		AggregateBase: NewAggregateBase(id, "BenchOrder"),
	}
}

func (o *BenchOrder) Create(customerID string, total float64) {
	o.Apply(BenchOrderCreated{
		OrderID:    o.AggregateID(),
		CustomerID: customerID,
		Total:      total,
		Currency:   "USD",
	})
	o.CustomerID = customerID
	o.Total = total
	o.Status = "created"
}

func (o *BenchOrder) AddItem(sku, name string, qty int, price float64) {
	o.Apply(BenchItemAdded{
		OrderID:  o.AggregateID(),
		SKU:      sku,
		Name:     name,
		Quantity: qty,
		Price:    price,
	})
	o.Items = append(o.Items, benchItem{SKU: sku, Name: name, Quantity: qty, Price: price})
	o.Total += price * float64(qty)
}

func (o *BenchOrder) Ship(trackingID, carrier string) {
	o.Apply(BenchOrderShipped{
		OrderID:    o.AggregateID(),
		TrackingID: trackingID,
		Carrier:    carrier,
	})
	o.Shipped = true
	o.Status = "shipped"
}

func (o *BenchOrder) ApplyEvent(event interface{}) error {
	switch e := event.(type) {
	case BenchOrderCreated:
		o.CustomerID = e.CustomerID
		o.Total = e.Total
		o.Status = "created"
	case BenchItemAdded:
		o.Items = append(o.Items, benchItem{SKU: e.SKU, Name: e.Name, Quantity: e.Quantity, Price: e.Price})
		o.Total += e.Price * float64(e.Quantity)
	case BenchOrderShipped:
		o.Shipped = true
		o.Status = "shipped"
	case BenchPaymentReceived:
		o.Status = "paid"
	case BenchAddressChanged:
		// state update
	}
	o.IncrementVersion()
	return nil
}

// BenchCreateOrderCmd is a benchmark command.
type BenchCreateOrderCmd struct {
	CommandBase
	TargetID   string
	CustomerID string
	Total      float64
}

func (c BenchCreateOrderCmd) CommandType() string { return "BenchCreateOrder" }
func (c BenchCreateOrderCmd) AggregateID() string { return c.TargetID }
func (c BenchCreateOrderCmd) Validate() error     { return nil }

// BenchAddItemCmd is a benchmark command.
type BenchAddItemCmd struct {
	CommandBase
	TargetID string
	SKU      string
	Name     string
	Quantity int
	Price    float64
}

func (c BenchAddItemCmd) CommandType() string { return "BenchAddItem" }
func (c BenchAddItemCmd) AggregateID() string { return c.TargetID }
func (c BenchAddItemCmd) Validate() error     { return nil }

// benchRegisterEvents registers all benchmark event types.
func benchRegisterEvents(store *EventStore) {
	store.RegisterEvents(
		BenchOrderCreated{},
		BenchItemAdded{},
		BenchOrderShipped{},
		BenchPaymentReceived{},
		BenchAddressChanged{},
	)
}

// newBenchAggregateHandlerBus creates a CommandBus with an AggregateHandler for BenchOrder.
func newBenchAggregateHandlerBus(store *EventStore, middleware ...Middleware) *CommandBus {
	idCounter := atomic.Int64{}
	handler := NewAggregateHandler[BenchCreateOrderCmd, *BenchOrder](AggregateHandlerConfig[BenchCreateOrderCmd, *BenchOrder]{
		Store:   store,
		Factory: func(id string) *BenchOrder { return newBenchOrder(id) },
		Executor: func(ctx context.Context, agg *BenchOrder, cmd BenchCreateOrderCmd) error {
			agg.Create(cmd.CustomerID, cmd.Total)
			return nil
		},
		NewIDFunc: func() string {
			return "order-" + strconv.FormatInt(idCounter.Add(1), 10)
		},
	})

	bus := NewCommandBus(WithMiddleware(middleware...))
	bus.Register(handler)
	return bus
}

// ============================================================================
// Section 1: EventStore — Append Benchmarks
// ============================================================================

func BenchmarkAppend_SingleEvent(b *testing.B) {
	adapter := memory.NewAdapter()
	store := New(adapter)
	ctx := context.Background()
	event := BenchOrderCreated{OrderID: "o1", CustomerID: "c1", Total: 99.99, Currency: "USD"}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		if err := store.Append(ctx, "BenchOrder-"+strconv.Itoa(i), []interface{}{event}, ExpectVersion(AnyVersion)); err != nil {
			b.Fatalf("append failed at %d: %v", i, err)
		}
	}

	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "events/sec")
}

func BenchmarkAppend_BatchSize(b *testing.B) {
	for _, batchSize := range []int{1, 5, 10, 50, 100} {
		b.Run(fmt.Sprintf("batch_%d", batchSize), func(b *testing.B) {
			adapter := memory.NewAdapter()
			store := New(adapter)
			ctx := context.Background()

			events := make([]interface{}, batchSize)
			for j := 0; j < batchSize; j++ {
				events[j] = BenchItemAdded{
					OrderID:  "o1",
					SKU:      "SKU-" + strconv.Itoa(j),
					Name:     "Product " + strconv.Itoa(j),
					Quantity: j + 1,
					Price:    float64(j) * 9.99,
				}
			}

			b.ReportAllocs()
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				if err := store.Append(ctx, "BenchOrder-"+strconv.Itoa(i), events, ExpectVersion(AnyVersion)); err != nil {
					b.Fatalf("batch append failed at %d: %v", i, err)
				}
			}

			totalEvents := float64(b.N) * float64(batchSize)
			b.ReportMetric(totalEvents/b.Elapsed().Seconds(), "events/sec")
		})
	}
}

func BenchmarkAppend_WithMetadata(b *testing.B) {
	adapter := memory.NewAdapter()
	store := New(adapter)
	ctx := context.Background()
	event := BenchOrderCreated{OrderID: "o1", CustomerID: "c1", Total: 99.99, Currency: "USD"}
	meta := Metadata{}.
		WithCorrelationID("corr-123").
		WithCausationID("cause-456").
		WithUserID("user-789").
		WithTenantID("tenant-abc").
		WithCustom("source", "benchmark").
		WithCustom("version", "1")

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		if err := store.Append(ctx, "BenchOrder-"+strconv.Itoa(i), []interface{}{event},
			ExpectVersion(AnyVersion), WithAppendMetadata(meta)); err != nil {
			b.Fatalf("append with metadata failed at %d: %v", i, err)
		}
	}

	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "events/sec")
}

func BenchmarkAppend_SameStream_Sequential(b *testing.B) {
	adapter := memory.NewAdapter()
	store := New(adapter)
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		event := BenchItemAdded{OrderID: "o1", SKU: "SKU-" + strconv.Itoa(i), Quantity: 1, Price: 10.0}
		if err := store.Append(ctx, "BenchOrder-same", []interface{}{event}, ExpectVersion(AnyVersion)); err != nil {
			b.Fatalf("same-stream append failed at %d: %v", i, err)
		}
	}

	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "events/sec")
}

// ============================================================================
// Section 2: EventStore — Load Benchmarks
// ============================================================================

func BenchmarkLoad_StreamSize(b *testing.B) {
	for _, size := range []int{10, 100, 1000, 10000} {
		b.Run(fmt.Sprintf("events_%d", size), func(b *testing.B) {
			adapter := memory.NewAdapter()
			store := New(adapter)
			benchRegisterEvents(store)
			ctx := context.Background()

			// Seed the stream with events
			streamID := "BenchOrder-load-" + strconv.Itoa(size)
			for j := 0; j < size; j++ {
				event := BenchItemAdded{
					OrderID: "o1", SKU: "SKU-" + strconv.Itoa(j),
					Name: "Product", Quantity: 1, Price: 10.0,
				}
				if err := store.Append(ctx, streamID, []interface{}{event}, ExpectVersion(AnyVersion)); err != nil {
					b.Fatalf("seed failed at %d: %v", j, err)
				}
			}

			b.ReportAllocs()
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				if _, err := store.Load(ctx, streamID); err != nil {
					b.Fatalf("load failed at %d: %v", i, err)
				}
			}

			b.ReportMetric(float64(size)*float64(b.N)/b.Elapsed().Seconds(), "events/sec")
		})
	}
}

func BenchmarkLoadFrom_MidStream(b *testing.B) {
	adapter := memory.NewAdapter()
	store := New(adapter)
	benchRegisterEvents(store)
	ctx := context.Background()

	streamID := "BenchOrder-loadfrom"
	for j := 0; j < 1000; j++ {
		event := BenchItemAdded{OrderID: "o1", SKU: "SKU-" + strconv.Itoa(j), Quantity: 1, Price: 10.0}
		if err := store.Append(ctx, streamID, []interface{}{event}, ExpectVersion(AnyVersion)); err != nil {
			b.Fatalf("seed failed at %d: %v", j, err)
		}
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		if _, err := store.LoadFrom(ctx, streamID, 500); err != nil {
			b.Fatalf("load from failed at %d: %v", i, err)
		}
	}

	b.ReportMetric(500.0*float64(b.N)/b.Elapsed().Seconds(), "events/sec")
}

// ============================================================================
// Section 3: Aggregate Lifecycle Benchmarks
// ============================================================================

func BenchmarkAggregate_Save(b *testing.B) {
	for _, numEvents := range []int{1, 5, 10, 50} {
		b.Run(fmt.Sprintf("events_%d", numEvents), func(b *testing.B) {
			adapter := memory.NewAdapter()
			store := New(adapter)
			ctx := context.Background()

			b.ReportAllocs()
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				order := newBenchOrder("order-" + strconv.Itoa(i))
				order.Create("cust-1", 100.0)
				for j := 1; j < numEvents; j++ {
					order.AddItem("SKU-"+strconv.Itoa(j), "Product", j, 10.0)
				}
				if err := store.SaveAggregate(ctx, order); err != nil {
					b.Fatalf("save aggregate failed at %d: %v", i, err)
				}
			}

			b.ReportMetric(float64(numEvents)*float64(b.N)/b.Elapsed().Seconds(), "events/sec")
		})
	}
}

func BenchmarkAggregate_Load(b *testing.B) {
	for _, numEvents := range []int{10, 100, 1000, 10000} {
		b.Run(fmt.Sprintf("events_%d", numEvents), func(b *testing.B) {
			adapter := memory.NewAdapter()
			store := New(adapter)
			benchRegisterEvents(store)
			ctx := context.Background()

			// Seed aggregate
			order := newBenchOrder("bench-load")
			order.Create("cust-1", 100.0)
			for j := 1; j < numEvents; j++ {
				order.AddItem("SKU-"+strconv.Itoa(j), "Product", 1, 10.0)
			}
			if err := store.SaveAggregate(ctx, order); err != nil {
				b.Fatalf("seed aggregate failed: %v", err)
			}

			b.ReportAllocs()
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				loaded := newBenchOrder("bench-load")
				if err := store.LoadAggregate(ctx, loaded); err != nil {
					b.Fatalf("load aggregate failed at %d: %v", i, err)
				}
			}

			b.ReportMetric(float64(numEvents)*float64(b.N)/b.Elapsed().Seconds(), "events/sec")
		})
	}
}

func BenchmarkAggregate_FullLifecycle(b *testing.B) {
	adapter := memory.NewAdapter()
	store := New(adapter)
	benchRegisterEvents(store)
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		id := "lifecycle-" + strconv.Itoa(i)

		// Create and save
		order := newBenchOrder(id)
		order.Create("cust-1", 100.0)
		order.AddItem("SKU-1", "Widget", 2, 29.99)
		order.AddItem("SKU-2", "Gadget", 1, 49.99)
		if err := store.SaveAggregate(ctx, order); err != nil {
			b.Fatalf("save failed at %d: %v", i, err)
		}

		// Load and modify
		loaded := newBenchOrder(id)
		if err := store.LoadAggregate(ctx, loaded); err != nil {
			b.Fatalf("load failed at %d: %v", i, err)
		}
		loaded.Ship("TRACK-"+strconv.Itoa(i), "UPS")
		if err := store.SaveAggregate(ctx, loaded); err != nil {
			b.Fatalf("save modified failed at %d: %v", i, err)
		}
	}
}

// ============================================================================
// Section 4: Command Bus Benchmarks
// ============================================================================

func BenchmarkCommandBus_Dispatch(b *testing.B) {
	tests := []struct {
		name       string
		middleware []Middleware
	}{
		{"Bare", nil},
		{"WithValidation", []Middleware{ValidationMiddleware()}},
		{"FullPipeline", []Middleware{
			ValidationMiddleware(), RecoveryMiddleware(),
			CorrelationIDMiddleware(nil), CausationIDMiddleware(),
		}},
	}
	for _, tt := range tests {
		b.Run(tt.name, func(b *testing.B) {
			bus := NewCommandBus(WithMiddleware(tt.middleware...))
			bus.RegisterFunc("BenchCreateOrder", func(ctx context.Context, cmd Command) (CommandResult, error) {
				return NewSuccessResult("agg-1", 1), nil
			})
			ctx := context.Background()
			cmd := BenchCreateOrderCmd{CustomerID: "c1", Total: 99.99}

			b.ReportAllocs()
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				_, _ = bus.Dispatch(ctx, cmd)
			}

			b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "commands/sec")
		})
	}
}

func BenchmarkCommandBus_Dispatch_AggregateHandler(b *testing.B) {
	adapter := memory.NewAdapter()
	store := New(adapter)
	benchRegisterEvents(store)
	bus := newBenchAggregateHandlerBus(store, ValidationMiddleware())
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		cmd := BenchCreateOrderCmd{CustomerID: "c1", Total: 99.99}
		_, _ = bus.Dispatch(ctx, cmd)
	}

	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "commands/sec")
}

// ============================================================================
// Section 5: Serialization Benchmarks
// ============================================================================

func BenchmarkSerializer_Serialize_SmallEvent(b *testing.B) {
	s := NewJSONSerializer()
	event := BenchOrderCreated{OrderID: "o-1", CustomerID: "c-1", Total: 99.99, Currency: "USD"}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		data, _ := s.Serialize(event)
		_ = data
	}
}

func BenchmarkSerializer_Serialize_LargeEvent(b *testing.B) {
	s := NewJSONSerializer()
	event := BenchAddressChanged{
		OrderID: "o-1",
		Street:  "1234 Elm Street, Apartment 567B, Building Complex North",
		City:    "San Francisco",
		State:   "California",
		Zip:     "94102-1234",
		Country: "United States of America",
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		data, _ := s.Serialize(event)
		_ = data
	}
}

func BenchmarkSerializer_Deserialize(b *testing.B) {
	s := NewJSONSerializer()
	s.RegisterAll(BenchOrderCreated{})
	data := []byte(`{"orderId":"o-1","customerId":"c-1","total":99.99,"currency":"USD"}`)

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		event, _ := s.Deserialize(data, "BenchOrderCreated")
		_ = event
	}
}

func BenchmarkSerializer_RoundTrip(b *testing.B) {
	s := NewJSONSerializer()
	s.RegisterAll(BenchOrderCreated{})
	event := BenchOrderCreated{OrderID: "o-1", CustomerID: "c-1", Total: 99.99, Currency: "USD"}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		data, _ := s.Serialize(event)
		result, _ := s.Deserialize(data, "BenchOrderCreated")
		_ = result
	}
}

func BenchmarkEventRegistry_Lookup(b *testing.B) {
	registry := NewEventRegistry()
	registry.RegisterAll(
		BenchOrderCreated{}, BenchItemAdded{}, BenchOrderShipped{},
		BenchPaymentReceived{}, BenchAddressChanged{},
	)

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		t, _ := registry.Lookup("BenchOrderCreated")
		_ = t
	}
}

// ============================================================================
// Section 6: Concurrent Access Benchmarks
// ============================================================================

func BenchmarkConcurrent_AppendDifferentStreams(b *testing.B) {
	adapter := memory.NewAdapter()
	store := New(adapter)
	ctx := context.Background()
	event := BenchOrderCreated{OrderID: "o1", CustomerID: "c1", Total: 99.99, Currency: "USD"}

	counter := atomic.Int64{}

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			id := counter.Add(1)
			if err := store.Append(ctx, "BenchOrder-"+strconv.FormatInt(id, 10),
				[]interface{}{event}, ExpectVersion(AnyVersion)); err != nil {
				b.Fatalf("concurrent append failed: %v", err)
			}
		}
	})

	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "events/sec")
}

func BenchmarkConcurrent_LoadWhileWriting(b *testing.B) {
	adapter := memory.NewAdapter()
	store := New(adapter)
	benchRegisterEvents(store)
	ctx := context.Background()

	// Seed 100 streams with 10 events each
	for s := 0; s < 100; s++ {
		streamID := "BenchOrder-rw-" + strconv.Itoa(s)
		for e := 0; e < 10; e++ {
			event := BenchItemAdded{OrderID: "o1", SKU: "SKU-" + strconv.Itoa(e), Quantity: 1, Price: 10.0}
			if err := store.Append(ctx, streamID, []interface{}{event}, ExpectVersion(AnyVersion)); err != nil {
				b.Fatalf("seed failed: %v", err)
			}
		}
	}

	counter := atomic.Int64{}

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			id := counter.Add(1)
			if id%3 == 0 {
				// 1/3 writes
				streamID := "BenchOrder-rw-new-" + strconv.FormatInt(id, 10)
				event := BenchOrderCreated{OrderID: "o1", CustomerID: "c1", Total: 99.99}
				if err := store.Append(ctx, streamID, []interface{}{event}, ExpectVersion(AnyVersion)); err != nil {
					b.Fatalf("concurrent write failed: %v", err)
				}
			} else {
				// 2/3 reads
				streamID := "BenchOrder-rw-" + strconv.Itoa(int(id%100))
				if _, err := store.Load(ctx, streamID); err != nil {
					b.Fatalf("concurrent load failed: %v", err)
				}
			}
		}
	})

	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "ops/sec")
}

func BenchmarkConcurrent_CommandBus(b *testing.B) {
	bus := NewCommandBus(WithMiddleware(ValidationMiddleware()))
	bus.RegisterFunc("BenchCreateOrder", func(ctx context.Context, cmd Command) (CommandResult, error) {
		return NewSuccessResult("agg-1", 1), nil
	})
	bus.RegisterFunc("BenchAddItem", func(ctx context.Context, cmd Command) (CommandResult, error) {
		return NewSuccessResult("agg-1", 2), nil
	})
	ctx := context.Background()

	counter := atomic.Int64{}

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			id := counter.Add(1)
			if id%2 == 0 {
				_, _ = bus.Dispatch(ctx, BenchCreateOrderCmd{CustomerID: "c1", Total: 99.99})
			} else {
				_, _ = bus.Dispatch(ctx, BenchAddItemCmd{TargetID: "o1", SKU: "SKU-1", Quantity: 1, Price: 10.0})
			}
		}
	})

	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "commands/sec")
}

// ============================================================================
// Section 7: Metadata & Event Construction Benchmarks
// ============================================================================

func BenchmarkMetadata_WithChain(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		m := Metadata{}.
			WithCorrelationID("corr-123").
			WithCausationID("cause-456").
			WithUserID("user-789").
			WithTenantID("tenant-abc").
			WithCustom("key1", "value1").
			WithCustom("key2", "value2")
		_ = m
	}
}

func BenchmarkStreamID_Parse(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		sid, _ := ParseStreamID("BenchOrder-12345")
		_ = sid
	}
}

// ============================================================================
// Section 8: Scale Tests — 1 Million Operations
//
// These tests verify the library handles production-scale workloads.
// Skipped in -short mode. Run via: make benchmark
// ============================================================================

const (
	scaleDefault = 1_000_000
	scaleShort   = 10_000
)

// skipUnlessScale skips the test unless scale tests are explicitly enabled.
func skipUnlessScale(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping scale test in -short mode")
	}
	if os.Getenv("MINK_SCALE_TESTS") != "1" {
		t.Skip("skipping scale test; set MINK_SCALE_TESTS=1 to enable (run with: make benchmark)")
	}
}

func scaleN(t *testing.T) int {
	t.Helper()
	skipUnlessScale(t)
	return scaleDefault
}

// memStatsDelta returns the difference in heap allocations.
func memStatsDelta(before, after runtime.MemStats) (allocsMB float64, heapMB float64) {
	allocsMB = float64(after.TotalAlloc-before.TotalAlloc) / (1024 * 1024)
	heapMB = float64(after.HeapInuse) / (1024 * 1024)
	return
}

// scaleTimer measures wall-clock time and memory allocation for scale tests.
type scaleTimer struct {
	start     time.Time
	memBefore runtime.MemStats
}

func newScaleTimer() scaleTimer {
	var m runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m)
	return scaleTimer{start: time.Now(), memBefore: m}
}

func (s scaleTimer) stop() (elapsed time.Duration, allocMB, heapMB float64) {
	elapsed = time.Since(s.start)
	var memAfter runtime.MemStats
	runtime.ReadMemStats(&memAfter)
	allocMB, heapMB = memStatsDelta(s.memBefore, memAfter)
	return
}

// logScaleResult logs common scale test metrics with optional extra lines.
func logScaleResult(t *testing.T, title string, n int, elapsed time.Duration, allocMB, heapMB float64, unit string, extra ...string) {
	t.Helper()
	t.Logf("")
	t.Logf("=== SCALE TEST: %s ===", title)
	t.Logf("  Total time:     %v", elapsed.Round(time.Millisecond))
	t.Logf("  Throughput:     %.0f %s", float64(n)/elapsed.Seconds(), unit)
	t.Logf("  Heap allocated: %.1f MB", allocMB)
	t.Logf("  Heap in-use:    %.1f MB", heapMB)
	for _, line := range extra {
		t.Logf("  %s", line)
	}
	t.Logf("================================================")
}

// sampleLatencies measures per-op latency by running batches of opsPerSample invocations.
func sampleLatencies(numSamples, opsPerSample int, op func(globalIdx int)) []time.Duration {
	latencies := make([]time.Duration, numSamples)
	for s := 0; s < numSamples; s++ {
		start := time.Now()
		base := s * opsPerSample
		for i := 0; i < opsPerSample; i++ {
			op(base + i)
		}
		latencies[s] = time.Since(start) / time.Duration(opsPerSample)
	}
	return latencies
}

// latencyStats holds percentile statistics from a slice of latency samples.
type latencyStats struct {
	avg, p50, p90, p95, p99, p999 time.Duration
	min, max                      time.Duration
}

func (ls latencyStats) log(t *testing.T, title string) {
	t.Helper()
	t.Logf("")
	t.Logf("=== %s ===", title)
	t.Logf("  Average:  %v", ls.avg)
	t.Logf("  p50:      %v", ls.p50)
	t.Logf("  p90:      %v", ls.p90)
	t.Logf("  p95:      %v", ls.p95)
	t.Logf("  p99:      %v", ls.p99)
	t.Logf("  p99.9:    %v", ls.p999)
	t.Logf("  Min:      %v", ls.min)
	t.Logf("  Max:      %v", ls.max)
	t.Logf("================================================")
}

func computeLatencyStats(latencies []time.Duration) latencyStats {
	n := len(latencies)
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })

	var total time.Duration
	for _, l := range latencies {
		total += l
	}

	return latencyStats{
		avg:  total / time.Duration(n),
		p50:  latencies[n*50/100],
		p90:  latencies[n*90/100],
		p95:  latencies[n*95/100],
		p99:  latencies[n*99/100],
		p999: latencies[n*999/1000],
		min:  latencies[0],
		max:  latencies[n-1],
	}
}

func TestScale_MillionEventsAppend(t *testing.T) {
	n := scaleN(t)

	adapter := memory.NewAdapter()
	store := New(adapter)
	ctx := context.Background()
	event := BenchOrderCreated{OrderID: "o1", CustomerID: "c1", Total: 99.99, Currency: "USD"}

	timer := newScaleTimer()

	for i := 0; i < n; i++ {
		err := store.Append(ctx, "BenchOrder-"+strconv.Itoa(i), []interface{}{event}, ExpectVersion(AnyVersion))
		if err != nil {
			t.Fatalf("append failed at iteration %d: %v", i, err)
		}
	}

	elapsed, allocMB, heapMB := timer.stop()

	logScaleResult(t, fmt.Sprintf("%d Event Appends (unique streams)", n), n, elapsed, allocMB, heapMB, "events/sec",
		fmt.Sprintf("Avg latency:    %v/op", elapsed/time.Duration(n)),
		fmt.Sprintf("Streams:        %d", adapter.StreamCount()),
		fmt.Sprintf("Events stored:  %d", adapter.EventCount()),
	)

	assert.Equal(t, n, adapter.EventCount())
	assert.Equal(t, n, adapter.StreamCount())
}

func TestScale_MillionEventsSingleStream(t *testing.T) {
	n := scaleN(t)

	adapter := memory.NewAdapter()
	store := New(adapter)
	ctx := context.Background()

	timer := newScaleTimer()

	// Append in batches of 100 to a single stream for realistic throughput
	batchSize := 100
	for i := 0; i < n; i += batchSize {
		remaining := batchSize
		if i+remaining > n {
			remaining = n - i
		}
		events := make([]interface{}, remaining)
		for j := 0; j < remaining; j++ {
			events[j] = BenchItemAdded{
				OrderID:  "o1",
				SKU:      "SKU-" + strconv.Itoa(i+j),
				Name:     "Product",
				Quantity: 1,
				Price:    9.99,
			}
		}
		err := store.Append(ctx, "BenchOrder-mega", events, ExpectVersion(AnyVersion))
		if err != nil {
			t.Fatalf("batch append failed at offset %d: %v", i, err)
		}
	}

	elapsed, allocMB, heapMB := timer.stop()

	logScaleResult(t, fmt.Sprintf("%d Events into Single Stream (batch=%d)", n, batchSize), n, elapsed, allocMB, heapMB, "events/sec",
		fmt.Sprintf("Events stored:  %d", adapter.EventCount()),
	)

	assert.Equal(t, n, adapter.EventCount())
}

func TestScale_MillionEventsAcrossStreams(t *testing.T) {
	n := scaleN(t)

	adapter := memory.NewAdapter()
	store := New(adapter)
	ctx := context.Background()

	numStreams := 10_000
	eventsPerStream := n / numStreams

	timer := newScaleTimer()

	for s := 0; s < numStreams; s++ {
		streamID := "BenchOrder-" + strconv.Itoa(s)
		events := make([]interface{}, eventsPerStream)
		for e := 0; e < eventsPerStream; e++ {
			events[e] = BenchItemAdded{
				OrderID:  streamID,
				SKU:      "SKU-" + strconv.Itoa(e),
				Name:     "Product",
				Quantity: 1,
				Price:    9.99,
			}
		}
		err := store.Append(ctx, streamID, events, ExpectVersion(AnyVersion))
		if err != nil {
			t.Fatalf("append to stream %d failed: %v", s, err)
		}
	}

	elapsed, allocMB, heapMB := timer.stop()

	logScaleResult(t, fmt.Sprintf("%d Events across %d Streams (%d events/stream)", n, numStreams, eventsPerStream), n, elapsed, allocMB, heapMB, "events/sec",
		fmt.Sprintf("Streams:        %d", adapter.StreamCount()),
		fmt.Sprintf("Events stored:  %d", adapter.EventCount()),
	)

	assert.Equal(t, n, adapter.EventCount())
	assert.Equal(t, numStreams, adapter.StreamCount())
}

func TestScale_MillionCommandDispatches(t *testing.T) {
	n := scaleN(t)

	bus := NewCommandBus(WithMiddleware(
		ValidationMiddleware(),
		RecoveryMiddleware(),
		CorrelationIDMiddleware(nil),
	))
	bus.RegisterFunc("BenchCreateOrder", func(ctx context.Context, cmd Command) (CommandResult, error) {
		return NewSuccessResult("agg-1", 1), nil
	})
	ctx := context.Background()
	cmd := BenchCreateOrderCmd{CustomerID: "c1", Total: 99.99}

	start := time.Now()

	successCount := 0
	for i := 0; i < n; i++ {
		result, err := bus.Dispatch(ctx, cmd)
		if err == nil && result.IsSuccess() {
			successCount++
		}
	}

	elapsed := time.Since(start)
	throughput := float64(n) / elapsed.Seconds()
	avgLatency := elapsed / time.Duration(n)

	t.Logf("")
	t.Logf("=== SCALE TEST: %d Command Dispatches (3 middleware) ===", n)
	t.Logf("  Total time:     %v", elapsed.Round(time.Millisecond))
	t.Logf("  Throughput:     %.0f commands/sec", throughput)
	t.Logf("  Avg latency:    %v/op", avgLatency)
	t.Logf("  Success rate:   %d/%d (%.2f%%)", successCount, n, float64(successCount)/float64(n)*100)
	t.Logf("================================================")

	assert.Equal(t, n, successCount)
}

func TestScale_LargeAggregateReplay(t *testing.T) {
	skipUnlessScale(t)

	numEvents := 100_000

	adapter := memory.NewAdapter()
	store := New(adapter)
	benchRegisterEvents(store)
	ctx := context.Background()

	// Build a large aggregate
	order := newBenchOrder("mega-order")
	order.Create("cust-1", 100.0)
	for i := 1; i < numEvents; i++ {
		order.AddItem("SKU-"+strconv.Itoa(i), "Product "+strconv.Itoa(i), 1, 9.99)
	}

	saveStart := time.Now()
	err := store.SaveAggregate(ctx, order)
	require.NoError(t, err)
	saveElapsed := time.Since(saveStart)

	// Now benchmark loading (replaying) this large aggregate
	var loadElapsed time.Duration
	numLoads := 10
	for i := 0; i < numLoads; i++ {
		loaded := newBenchOrder("mega-order")
		loadStart := time.Now()
		err = store.LoadAggregate(ctx, loaded)
		loadElapsed += time.Since(loadStart)
		require.NoError(t, err)
		assert.Equal(t, int64(numEvents), loaded.Version())
		assert.Equal(t, numEvents-1, len(loaded.Items))
	}

	avgLoad := loadElapsed / time.Duration(numLoads)
	replayRate := float64(numEvents) / avgLoad.Seconds()

	t.Logf("")
	t.Logf("=== SCALE TEST: Aggregate with %d Events ===", numEvents)
	t.Logf("  Save time:      %v", saveElapsed.Round(time.Millisecond))
	t.Logf("  Avg load time:  %v (over %d loads)", avgLoad.Round(time.Millisecond), numLoads)
	t.Logf("  Replay rate:    %.0f events/sec", replayRate)
	t.Logf("  Final version:  %d", numEvents)
	t.Logf("  Items in aggregate: %d", numEvents-1)
	t.Logf("================================================")
}

func TestScale_ConcurrentMillionEvents(t *testing.T) {
	n := scaleN(t)

	adapter := memory.NewAdapter()
	store := New(adapter)
	ctx := context.Background()
	event := BenchOrderCreated{OrderID: "o1", CustomerID: "c1", Total: 99.99, Currency: "USD"}

	numWorkers := runtime.GOMAXPROCS(0)
	eventsPerWorker := n / numWorkers

	timer := newScaleTimer()

	var wg sync.WaitGroup
	var errorCount atomic.Int64

	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			base := workerID * eventsPerWorker
			for i := 0; i < eventsPerWorker; i++ {
				streamID := "BenchOrder-" + strconv.Itoa(base+i)
				if err := store.Append(ctx, streamID, []interface{}{event}, ExpectVersion(AnyVersion)); err != nil {
					errorCount.Add(1)
				}
			}
		}(w)
	}
	wg.Wait()

	elapsed, allocMB, heapMB := timer.stop()
	actualEvents := numWorkers * eventsPerWorker

	logScaleResult(t, fmt.Sprintf("%d Concurrent Events (%d workers)", actualEvents, numWorkers), actualEvents, elapsed, allocMB, heapMB, "events/sec",
		fmt.Sprintf("Errors:         %d", errorCount.Load()),
		fmt.Sprintf("Workers:        %d", numWorkers),
		fmt.Sprintf("Events stored:  %d", adapter.EventCount()),
	)

	assert.Equal(t, int64(0), errorCount.Load())
	assert.Equal(t, actualEvents, adapter.EventCount())
}

func TestScale_LatencyDistribution(t *testing.T) {
	skipUnlessScale(t)

	// Measure batches of opsPerSample to get accurate sub-µs latencies on all platforms.
	// Windows timer resolution is ~100ns; batching 100 ops ensures measurable durations.
	const totalOps = 100_000
	const opsPerSample = 100
	const numSamples = totalOps / opsPerSample

	adapter := memory.NewAdapter()
	store := New(adapter)
	ctx := context.Background()
	event := BenchOrderCreated{OrderID: "o1", CustomerID: "c1", Total: 99.99, Currency: "USD"}

	latencies := sampleLatencies(numSamples, opsPerSample, func(idx int) {
		_ = store.Append(ctx, "BenchOrder-"+strconv.Itoa(idx), []interface{}{event}, ExpectVersion(AnyVersion))
	})

	ls := computeLatencyStats(latencies)
	ls.log(t, fmt.Sprintf("LATENCY DISTRIBUTION: %d Appends (sampled per %d ops)", totalOps, opsPerSample))
}

func TestScale_MixedWorkloadConcurrent(t *testing.T) {
	skipUnlessScale(t)

	n := 500_000

	adapter := memory.NewAdapter()
	store := New(adapter)
	benchRegisterEvents(store)
	ctx := context.Background()

	// Pre-seed 1000 streams with 10 events each
	for s := 0; s < 1000; s++ {
		events := make([]interface{}, 10)
		for e := 0; e < 10; e++ {
			events[e] = BenchItemAdded{OrderID: "o1", SKU: "SKU-" + strconv.Itoa(e), Quantity: 1, Price: 9.99}
		}
		_ = store.Append(ctx, "BenchOrder-seed-"+strconv.Itoa(s), events, ExpectVersion(AnyVersion))
	}

	numWorkers := runtime.GOMAXPROCS(0)
	opsPerWorker := n / numWorkers

	var writeCount, readCount atomic.Int64

	start := time.Now()

	var wg sync.WaitGroup
	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			base := workerID * opsPerWorker
			for i := 0; i < opsPerWorker; i++ {
				op := (base + i) % 10
				switch {
				case op < 3: // 30% writes
					streamID := "BenchOrder-w" + strconv.Itoa(workerID) + "-" + strconv.Itoa(i)
					event := BenchOrderCreated{OrderID: "o1", CustomerID: "c1", Total: 99.99}
					_ = store.Append(ctx, streamID, []interface{}{event}, ExpectVersion(AnyVersion))
					writeCount.Add(1)
				default: // 70% reads
					streamID := "BenchOrder-seed-" + strconv.Itoa((base+i)%1000)
					events, _ := store.Load(ctx, streamID)
					_ = events
					readCount.Add(1)
				}
			}
		}(w)
	}
	wg.Wait()

	elapsed := time.Since(start)
	totalOps := numWorkers * opsPerWorker
	throughput := float64(totalOps) / elapsed.Seconds()

	t.Logf("")
	t.Logf("=== SCALE TEST: Mixed Workload (%d ops, %d workers) ===", totalOps, numWorkers)
	t.Logf("  Total time:     %v", elapsed.Round(time.Millisecond))
	t.Logf("  Throughput:     %.0f ops/sec", throughput)
	t.Logf("  Writes:         %d (30%%)", writeCount.Load())
	t.Logf("  Reads:          %d (70%%)", readCount.Load())
	t.Logf("  Events stored:  %d", adapter.EventCount())
	t.Logf("================================================")
}

func TestScale_SerializerThroughput(t *testing.T) {
	n := scaleN(t)

	s := NewJSONSerializer()
	s.RegisterAll(BenchOrderCreated{})
	event := BenchOrderCreated{OrderID: "o-1", CustomerID: "c-1", Total: 99.99, Currency: "USD"}

	// Serialize throughput
	serStart := time.Now()
	var totalBytes int64
	for i := 0; i < n; i++ {
		data, _ := s.Serialize(event)
		totalBytes += int64(len(data))
	}
	serElapsed := time.Since(serStart)

	// Deserialize throughput
	data, _ := s.Serialize(event)
	deserStart := time.Now()
	for i := 0; i < n; i++ {
		result, _ := s.Deserialize(data, "BenchOrderCreated")
		_ = result
	}
	deserElapsed := time.Since(deserStart)

	serThroughput := float64(n) / serElapsed.Seconds()
	deserThroughput := float64(n) / deserElapsed.Seconds()
	serMBps := float64(totalBytes) / serElapsed.Seconds() / (1024 * 1024)

	t.Logf("")
	t.Logf("=== SCALE TEST: Serializer Throughput (%d operations) ===", n)
	t.Logf("  Serialize:      %.0f ops/sec (%.1f MB/s)", serThroughput, serMBps)
	t.Logf("  Deserialize:    %.0f ops/sec", deserThroughput)
	t.Logf("  Payload size:   %d bytes", len(data))
	t.Logf("================================================")
}

func TestScale_CommandBusLatencyDistribution(t *testing.T) {
	skipUnlessScale(t)

	const totalOps = 100_000
	const opsPerSample = 100
	const numSamples = totalOps / opsPerSample

	adapter := memory.NewAdapter()
	store := New(adapter)
	benchRegisterEvents(store)
	bus := newBenchAggregateHandlerBus(store, ValidationMiddleware(), RecoveryMiddleware())
	ctx := context.Background()

	latencies := sampleLatencies(numSamples, opsPerSample, func(_ int) {
		cmd := BenchCreateOrderCmd{CustomerID: "c1", Total: 99.99}
		_, _ = bus.Dispatch(ctx, cmd)
	})

	ls := computeLatencyStats(latencies)
	ls.log(t, fmt.Sprintf("LATENCY: Command Bus (AggregateHandler, %d dispatches)", totalOps))
}
