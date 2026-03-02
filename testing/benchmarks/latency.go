package benchmarks

import (
	"fmt"
	"sort"
	"time"
)

// LatencyReport contains percentile latency statistics.
type LatencyReport struct {
	Count int
	P50   time.Duration
	P90   time.Duration
	P95   time.Duration
	P99   time.Duration
	Max   time.Duration
}

// String returns a human-readable summary of the latency report.
func (r LatencyReport) String() string {
	return fmt.Sprintf("n=%d p50=%v p90=%v p95=%v p99=%v max=%v",
		r.Count, r.P50, r.P90, r.P95, r.P99, r.Max)
}

// LatencyCollector records durations and computes percentile statistics.
type LatencyCollector struct {
	samples []time.Duration
}

// NewLatencyCollector creates a collector pre-allocated for the expected number of samples.
func NewLatencyCollector(capacity int) *LatencyCollector {
	return &LatencyCollector{
		samples: make([]time.Duration, 0, capacity),
	}
}

// Record adds a duration sample.
func (lc *LatencyCollector) Record(d time.Duration) {
	lc.samples = append(lc.samples, d)
}

// Report computes percentile statistics from recorded samples.
// The collector must have at least one sample; otherwise Report returns a zero LatencyReport.
func (lc *LatencyCollector) Report() LatencyReport {
	n := len(lc.samples)
	if n == 0 {
		return LatencyReport{}
	}

	sort.Slice(lc.samples, func(i, j int) bool {
		return lc.samples[i] < lc.samples[j]
	})

	return LatencyReport{
		Count: n,
		P50:   lc.samples[percentileIndex(n, 50)],
		P90:   lc.samples[percentileIndex(n, 90)],
		P95:   lc.samples[percentileIndex(n, 95)],
		P99:   lc.samples[percentileIndex(n, 99)],
		Max:   lc.samples[n-1],
	}
}

// percentileIndex returns the index for the given percentile (0-100) in a sorted slice of length n.
func percentileIndex(n, percentile int) int {
	idx := (n * percentile) / 100
	if idx >= n {
		idx = n - 1
	}
	return idx
}
