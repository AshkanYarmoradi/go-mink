---
layout: default
title: Benchmarks
nav_order: 14
permalink: /docs/benchmarks
---

# Benchmarks
{: .no_toc }

Performance characteristics of go-mink adapters under various workloads.
{: .fs-6 .fw-300 }

## Table of Contents
{: .no_toc .text-delta }

1. TOC
{:toc}

---

## Overview

go-mink includes a shared benchmark suite (`testing/benchmarks/`) that runs identical workloads against every adapter. This ensures fair, apples-to-apples comparisons and makes it easy to add benchmarks for new adapters.

The suite measures:

- **Append throughput** — single events, batches, with metadata, with concurrency checks
- **Load throughput** — streams of 10, 100, and 1,000 events
- **Concurrent access** — 8 and 32 parallel workers appending to different streams
- **Mixed workloads** — 80/20 and 20/80 read/write ratios
- **Stream operations** — `GetStreamInfo` and `GetLastPosition`
- **Scale tests** — sequential, batch, concurrent, load, and many-stream scenarios at high event counts

All results below are from CI (ubuntu-latest, GitHub Actions) for reproducibility. Your local numbers will vary depending on hardware.

---

## Memory Adapter Results

The in-memory adapter provides a baseline for pure Go overhead without I/O. Scale tests run with **1,000,000 events**.

### Go Benchmarks

| Benchmark | ops/sec | allocs/op | bytes/op |
|-----------|---------|-----------|----------|
| Append/SingleEvent | ~2,500,000 | ~10 | ~800 |
| Append/BatchOf10 | ~500,000 (5M events/sec) | ~30 | ~4,000 |
| Append/BatchOf100 | ~60,000 (6M events/sec) | ~200 | ~35,000 |
| Load/10Events | ~3,000,000 | ~5 | ~600 |
| Load/100Events | ~500,000 | ~5 | ~5,000 |
| Load/1000Events | ~60,000 | ~5 | ~50,000 |
| Concurrent/Append_8Workers | ~1,500,000 | ~10 | ~800 |
| Concurrent/Append_32Workers | ~1,000,000 | ~10 | ~800 |

### Scale Tests

| Test | Events | Throughput | p50 | p99 |
|------|--------|------------|-----|-----|
| Sequential Append | 1,000,000 | ~2M events/sec | <1us | ~5us |
| Batch Append (x100) | 1,000,000 | ~5M events/sec | ~10us | ~50us |
| Concurrent (8 workers) | 1,000,000 | ~3M events/sec | — | — |
| Load (full stream) | 100,000 | ~20M events/sec | ~5ms | ~10ms |
| Many Streams (1000) | 1,000,000 | ~2M events/sec | — | — |

---

## PostgreSQL Adapter Results

PostgreSQL benchmarks measure real I/O against a local PostgreSQL 16 instance. Scale tests run with **100,000 events**.

### Go Benchmarks

| Benchmark | ops/sec | allocs/op | bytes/op |
|-----------|---------|-----------|----------|
| Append/SingleEvent | ~5,000 | ~40 | ~3,000 |
| Append/BatchOf10 | ~2,000 (20K events/sec) | ~200 | ~20,000 |
| Append/BatchOf100 | ~300 (30K events/sec) | ~1,500 | ~150,000 |
| Load/10Events | ~8,000 | ~50 | ~4,000 |
| Load/100Events | ~2,000 | ~200 | ~30,000 |
| Load/1000Events | ~300 | ~1,500 | ~250,000 |
| Concurrent/Append_8Workers | ~15,000 | ~40 | ~3,000 |
| Concurrent/Append_32Workers | ~20,000 | ~40 | ~3,000 |

### Scale Tests

| Test | Events | Throughput | p50 | p99 |
|------|--------|------------|-----|-----|
| Sequential Append | 100,000 | ~5K events/sec | ~200us | ~2ms |
| Batch Append (x100) | 100,000 | ~30K events/sec | ~3ms | ~15ms |
| Concurrent (8 workers) | 100,000 | ~15K events/sec | — | — |
| Load (full stream) | 100,000 | ~500K events/sec | ~200ms | ~500ms |
| Many Streams (1000) | 100,000 | ~10K events/sec | — | — |

> **Note**: PostgreSQL numbers are approximate and vary significantly with connection pooling settings, disk speed, and network latency. The concurrent append benchmarks often show *higher* throughput than sequential because PostgreSQL can pipeline concurrent transactions.

---

## How to Run

### Memory adapter (no infrastructure required)

```bash
# Go benchmarks
CGO_ENABLED=0 go test -run='^$' -bench=. -benchmem ./adapters/memory/

# Scale tests (1M events)
CGO_ENABLED=0 go test -run='TestAdapterScale' -v -timeout=10m ./adapters/memory/

# Or use the Makefile
make benchmark-adapters
```

### PostgreSQL adapter (requires PostgreSQL)

```bash
# Start infrastructure
make infra-up

# Go benchmarks
TEST_DATABASE_URL="postgres://postgres:postgres@localhost:5432/mink_test?sslmode=disable" \
  go test -run='^$' -bench=. -benchmem ./adapters/postgres/

# Scale tests (100K events)
TEST_DATABASE_URL="postgres://postgres:postgres@localhost:5432/mink_test?sslmode=disable" \
  go test -run='TestAdapterScale' -v -timeout=10m ./adapters/postgres/

# Or use the Makefile
make benchmark-adapters-pg
```

### Root-level benchmarks (EventStore + CommandBus + Serializer)

```bash
# Full suite including 1M-event scale tests
make benchmark

# Quick run (no scale tests)
make benchmark-quick
```

---

## Adding Benchmarks for a New Adapter

Adding benchmarks for a new adapter requires just three steps:

### 1. Create a benchmark test file

Create `adapters/youradapter/benchmark_test.go`:

```go
package youradapter

import (
    "testing"

    "github.com/AshkanYarmoradi/go-mink/adapters"
    "github.com/AshkanYarmoradi/go-mink/testing/benchmarks"
)

func newBenchmarkFactory() benchmarks.AdapterBenchmarkFactory {
    return benchmarks.AdapterBenchmarkFactory{
        Name: "youradapter",
        CreateAdapter: func() (adapters.EventStoreAdapter, func(), error) {
            a := NewAdapter(/* ... */)
            return a, func() { a.Close() }, nil
        },
        Skip: func() string {
            // Return non-empty to skip (e.g., missing env var)
            return ""
        },
        Scale: &benchmarks.ScaleConfig{EventCount: 100_000},
    }
}

func BenchmarkAdapter(b *testing.B) {
    benchmarks.Run(b, newBenchmarkFactory())
}

func TestAdapterScale(t *testing.T) {
    benchmarks.RunScale(t, newBenchmarkFactory())
}
```

### 2. Add a Makefile target

```makefile
benchmark-adapters-yours:
	go test -run='^$$' -bench=. -benchmem -benchtime=3s ./adapters/youradapter/
	go test -run='TestAdapterScale' -v -timeout=10m ./adapters/youradapter/
```

### 3. Add a CI job (optional)

Add a `benchmark-youradapter` job to `.github/workflows/test.yml` following the pattern of the existing `benchmark-postgres` job.

---

## Benchmark Architecture

```
testing/benchmarks/
├── suite.go      # Run() and RunScale() entry points, all benchmark implementations
├── records.go    # Pre-serialized EventRecord builders (Small, Medium, Metadata, Batch)
└── latency.go    # LatencyCollector for percentile measurements (p50/p90/p95/p99)

adapters/memory/benchmark_test.go      # ~25 lines, wires factory to Run/RunScale
adapters/postgres/benchmark_test.go    # ~35 lines, wires factory to Run/RunScale
```

The shared suite operates at the `adapters.EventStoreAdapter` level with pre-serialized `adapters.EventRecord` values. This avoids importing the root `mink` package (which would create an import cycle) while measuring pure adapter I/O throughput.

Each adapter provides an `AdapterBenchmarkFactory` that knows how to create, configure, and clean up adapter instances. The `Skip` function allows adapters to skip benchmarks when required infrastructure is unavailable.
