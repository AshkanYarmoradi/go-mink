# Prometheus Metrics Example

> Instrument a go-mink event store with Prometheus metrics for observability.

Production event-sourcing systems need visibility into throughput, latency, and error rates. This example wires the go-mink metrics middleware into an event store, exercises it with simulated user traffic, and exposes a live Prometheus scrape endpoint so you can watch the counters and histograms fill up.

## What this demonstrates
- **Metrics instance** — `minkmetrics.New(...)` builds a `*Metrics` configured with `WithNamespace("mink_example")` and `WithMetricsServiceName("user-service")`, which prefix and label every metric.
- **Prometheus registration** — `MustRegister()` registers the collectors with the default Prometheus registry so they can be gathered and scraped.
- **Command middleware** — `CommandMiddleware()` returns a `mink.Middleware` you plug into a command bus to record per-command counts, statuses, and durations.
- **Live scrape endpoint** — a background HTTP server serves `promhttp.Handler()` at `:9090/metrics` for Prometheus to poll.
- **Simulated workload** — appends registration, login, and profile-update events against the in-memory store to generate realistic metric activity.

## Running
```bash
go run ./examples/metrics
```
No infrastructure required — uses the in-memory adapter.

This example starts an HTTP server on `:9090/metrics` and then runs until you press Ctrl+C.

## What happens
1. Creates a `memory.NewAdapter()` event store and a metrics instance (`namespace=mink_example`, `service=user-service`), then calls `MustRegister()`.
2. Launches the Prometheus HTTP server in a goroutine at `http://localhost:9090/metrics`.
3. Registers 5 users (`user-001` … `user-005`) by appending a `UserRegistered` event per stream, printing each with its append latency in milliseconds.
4. Simulates 20 logins — loads a random user's stream, then appends a `UserLoggedIn` event.
5. Simulates 10 profile updates by appending `ProfileUpdated` events to random user streams.
6. Prints a metrics summary showing sample metric names, the final per-stream event counts, and the list of registered `mink`-prefixed metrics gathered from Prometheus.
7. Prints observability tips and keeps the `/metrics` endpoint alive until Ctrl+C.

## Key APIs
- `minkmetrics.New(opts ...MetricsOption) *Metrics` — construct the metrics collector set.
- `minkmetrics.WithNamespace(string)` — set the metric name prefix (e.g. `mink_example_...`).
- `minkmetrics.WithMetricsServiceName(string)` — set the `service` label on all metrics.
- `(*Metrics).MustRegister()` — register the collectors with the default Prometheus registry.
- `(*Metrics).CommandMiddleware() mink.Middleware` — middleware to instrument command dispatch.
- `mink.New(adapter)` — create the event store over the memory adapter.
- `store.Append(ctx, streamID, events, ...)` / `store.Load(ctx, streamID)` — the operations being measured.
- `promhttp.Handler()` — Prometheus HTTP handler exposed at `:9090/metrics`.

## Related
- **Examples:** [tracing](../tracing) · [full-ecommerce](../full-ecommerce) · [basic](../basic)
- **Docs:** [Advanced Patterns](https://go-mink.dev/docs/advanced/advanced-patterns) · [API reference](https://pkg.go.dev/go-mink.dev/middleware/metrics)
