# Distributed Tracing Example

> Instrument a go-mink event store with OpenTelemetry spans for end-to-end tracing.

Distributed tracing ties a single business operation together across every step it touches, so you can see where time is spent and where failures originate. This example traces an order-creation workflow — one root span with a child span per event-store operation — and exports the spans to stdout via the OpenTelemetry stdout exporter.

## What this demonstrates
- **OTel tracer provider** — `initTracer()` builds an `sdktrace.NewTracerProvider` with a `stdouttrace` exporter and a service-named resource, registered globally via `otel.SetTracerProvider`.
- **go-mink tracer** — `minktracing.NewTracer(...)` with `WithTracerProvider(tp)` and `WithServiceName("order-service")` constructs the middleware tracer.
- **Command middleware** — `minktracing.CommandMiddleware(tracer)` returns a `mink.Middleware` (a package-level function taking the tracer) to auto-span command dispatch.
- **Manual spans** — a root `CreateOrderWorkflow` span wraps child spans (`EventStore.CreateOrder`, `EventStore.AddOrderItem`, `EventStore.SubmitOrder`), each carrying attributes and recording errors.
- **Stdout export** — spans print as pretty JSON so you can inspect them without external infrastructure.

## Running
```bash
go run ./examples/tracing
```
No infrastructure required — uses the in-memory adapter.

The stdout exporter is for the demo only. In production, swap it for a Jaeger, Zipkin, or Datadog exporter to visualize traces.

## What happens
1. Initializes OpenTelemetry with a pretty-printing stdout exporter and an `order-service` resource, then defers `tp.Shutdown`.
2. Creates a `memory.NewAdapter()` event store and a `minktracing` tracer.
3. Starts a root span `CreateOrderWorkflow` with `order.id` and `customer.id` attributes.
4. Appends `OrderCreated` for `ORD-12345` inside an `EventStore.CreateOrder` child span.
5. Adds three items (`WIDGET-001`, `GADGET-002`, `THING-003`) — each an `OrderItemAdded` event under an `EventStore.AddOrderItem` span with SKU/quantity/price attributes.
6. Appends `OrderSubmitted` inside an `EventStore.SubmitOrder` span, then loads the stream to record `events.total` on the span.
7. Ends the root span. The batched spans flush to stdout as JSON.
8. Loads and prints the order's event history (type + version per event) and trace-inspection tips.

## Key APIs
- `minktracing.NewTracer(opts ...TracerOption) *Tracer` — construct the middleware tracer.
- `minktracing.WithTracerProvider(trace.TracerProvider)` — supply the OTel tracer provider.
- `minktracing.WithServiceName(string)` — set the service name on emitted spans.
- `minktracing.CommandMiddleware(tracer *Tracer) mink.Middleware` — middleware to span command dispatch.
- `mink.New(adapter)` — create the event store over the memory adapter.
- `store.Append(ctx, streamID, events, ...)` / `store.Load(ctx, streamID)` — traced operations; `ctx` carries the span.
- `tracer.Start(ctx, name, trace.WithAttributes(...))` — open a span (from `go.opentelemetry.io/otel`).
- `stdouttrace.New(...)` / `sdktrace.NewTracerProvider(...)` — the OTel exporter and provider used in the demo.

## Related
- **Examples:** [metrics](../metrics) · [full-ecommerce](../full-ecommerce) · [basic](../basic)
- **Docs:** [Advanced Patterns](https://go-mink.dev/docs/advanced/advanced-patterns) · [API reference](https://pkg.go.dev/go-mink.dev/middleware/tracing)
