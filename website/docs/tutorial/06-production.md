---
id: production
title: "Part 6: Production Ready"
sidebar_position: 6
---

# Part 6: Production Ready

Deploy your event-sourced application with confidence.



---

## Overview

In this part, you'll:

- Add Prometheus metrics
- Integrate OpenTelemetry tracing
- Implement proper error handling
- Configure for production deployment

**Time**: ~40 minutes

---

## Part 1: Prometheus Metrics

Add observability with the `middleware/metrics` package.

Create `internal/observability/metrics.go`:

```go
package observability

import (
	"context"
	"time"

	"go-mink.dev"
	"go-mink.dev/middleware/metrics"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// MinkMetrics holds all application metrics.
type MinkMetrics struct {
	// Command metrics
	CommandsTotal      *prometheus.CounterVec
	CommandDuration    *prometheus.HistogramVec
	CommandErrors      *prometheus.CounterVec

	// Event metrics
	EventsAppended     *prometheus.CounterVec
	EventsLoaded       prometheus.Counter
	StreamVersion      *prometheus.GaugeVec

	// Projection metrics
	ProjectionLag      *prometheus.GaugeVec
	ProjectionErrors   *prometheus.CounterVec
	ProjectionDuration *prometheus.HistogramVec

	// Business metrics
	OrdersPlaced       prometheus.Counter
	OrdersShipped      prometheus.Counter
	RevenueTotal       prometheus.Counter
	ProductsInStock    prometheus.Gauge
}

// NewMinkMetrics creates and registers all metrics.
func NewMinkMetrics(registry prometheus.Registerer) *MinkMetrics {
	factory := promauto.With(registry)

	return &MinkMetrics{
		// Command metrics
		CommandsTotal: factory.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "minkshop",
				Subsystem: "commands",
				Name:      "total",
				Help:      "Total number of commands processed",
			},
			[]string{"command_type", "status"},
		),
		CommandDuration: factory.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: "minkshop",
				Subsystem: "commands",
				Name:      "duration_seconds",
				Help:      "Command processing duration in seconds",
				Buckets:   []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5},
			},
			[]string{"command_type"},
		),
		CommandErrors: factory.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "minkshop",
				Subsystem: "commands",
				Name:      "errors_total",
				Help:      "Total number of command errors",
			},
			[]string{"command_type", "error_type"},
		),

		// Event metrics
		EventsAppended: factory.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "minkshop",
				Subsystem: "events",
				Name:      "appended_total",
				Help:      "Total number of events appended",
			},
			[]string{"event_type", "aggregate_type"},
		),
		EventsLoaded: factory.NewCounter(
			prometheus.CounterOpts{
				Namespace: "minkshop",
				Subsystem: "events",
				Name:      "loaded_total",
				Help:      "Total number of events loaded",
			},
		),
		StreamVersion: factory.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: "minkshop",
				Subsystem: "events",
				Name:      "stream_version",
				Help:      "Current version of event streams",
			},
			[]string{"stream_id"},
		),

		// Projection metrics
		ProjectionLag: factory.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: "minkshop",
				Subsystem: "projections",
				Name:      "lag_events",
				Help:      "Number of events behind for projections",
			},
			[]string{"projection_name"},
		),
		ProjectionErrors: factory.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "minkshop",
				Subsystem: "projections",
				Name:      "errors_total",
				Help:      "Total projection errors",
			},
			[]string{"projection_name", "error_type"},
		),
		ProjectionDuration: factory.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: "minkshop",
				Subsystem: "projections",
				Name:      "duration_seconds",
				Help:      "Projection processing duration",
				Buckets:   prometheus.DefBuckets,
			},
			[]string{"projection_name"},
		),

		// Business metrics
		OrdersPlaced: factory.NewCounter(
			prometheus.CounterOpts{
				Namespace: "minkshop",
				Subsystem: "business",
				Name:      "orders_placed_total",
				Help:      "Total orders placed",
			},
		),
		OrdersShipped: factory.NewCounter(
			prometheus.CounterOpts{
				Namespace: "minkshop",
				Subsystem: "business",
				Name:      "orders_shipped_total",
				Help:      "Total orders shipped",
			},
		),
		RevenueTotal: factory.NewCounter(
			prometheus.CounterOpts{
				Namespace: "minkshop",
				Subsystem: "business",
				Name:      "revenue_dollars_total",
				Help:      "Total revenue in dollars",
			},
		),
		ProductsInStock: factory.NewGauge(
			prometheus.GaugeOpts{
				Namespace: "minkshop",
				Subsystem: "business",
				Name:      "products_in_stock",
				Help:      "Number of products in stock",
			},
		),
	}
}

// MetricsMiddleware creates a command bus middleware that records metrics.
func (m *MinkMetrics) MetricsMiddleware() mink.Middleware {
	return func(next mink.MiddlewareFunc) mink.MiddlewareFunc {
		return func(ctx context.Context, cmd mink.Command) (mink.CommandResult, error) {
			cmdType := cmd.CommandType()
			start := time.Now()

			result, err := next(ctx, cmd)

			duration := time.Since(start).Seconds()
			m.CommandDuration.WithLabelValues(cmdType).Observe(duration)

			status := "success"
			if err != nil {
				status = "error"
				m.CommandErrors.WithLabelValues(cmdType, errorType(err)).Inc()
			} else if result.IsError() {
				status = "business_error"
				m.CommandErrors.WithLabelValues(cmdType, "business").Inc()
			}

			m.CommandsTotal.WithLabelValues(cmdType, status).Inc()

			return result, err
		}
	}
}

// RecordEventAppended records an event being appended.
func (m *MinkMetrics) RecordEventAppended(eventType, aggregateType string) {
	m.EventsAppended.WithLabelValues(eventType, aggregateType).Inc()
}

// RecordOrderPlaced records an order being placed.
func (m *MinkMetrics) RecordOrderPlaced(amount float64) {
	m.OrdersPlaced.Inc()
	m.RevenueTotal.Add(amount)
}

// RecordOrderShipped records an order being shipped.
func (m *MinkMetrics) RecordOrderShipped() {
	m.OrdersShipped.Inc()
}

func errorType(err error) string {
	switch {
	case mink.IsConcurrencyError(err):
		return "concurrency"
	case mink.IsNotFoundError(err):
		return "not_found"
	case mink.IsValidationError(err):
		return "validation"
	default:
		return "unknown"
	}
}
```

### Expose Metrics Endpoint

Update `cmd/server/main.go`:

```go
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	
	"minkshop/internal/app"
	"minkshop/internal/observability"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create Prometheus registry
	registry := prometheus.NewRegistry()
	registry.MustRegister(prometheus.NewGoCollector())
	registry.MustRegister(prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}))

	// Create metrics
	minkMetrics := observability.NewMinkMetrics(registry)

	// Create application
	cfg := app.Config{
		DatabaseURL:    getEnv("DATABASE_URL", "postgres://postgres:mink@localhost:5432/minkshop?sslmode=verify-full"), // TLS-only fallback; local dev overrides DATABASE_URL explicitly
		DatabaseSchema: getEnv("DATABASE_SCHEMA", "mink"),
		MaxConnections: 20,
	}

	application, err := app.New(ctx, cfg, app.WithMetrics(minkMetrics))
	if err != nil {
		log.Fatalf("Failed to create application: %v", err)
	}
	defer application.Close()

	// Start metrics server
	metricsServer := &http.Server{
		Addr:    ":9090",
		Handler: promhttp.HandlerFor(registry, promhttp.HandlerOpts{}),
	}

	go func() {
		log.Println("📊 Metrics server starting on :9090")
		if err := metricsServer.ListenAndServe(); err != http.ErrServerClosed {
			log.Printf("Metrics server error: %v", err)
		}
	}()

	// Start application server
	appServer := &http.Server{
		Addr:    ":8080",
		Handler: NewRouter(application),
	}

	go func() {
		log.Println("🚀 Application server starting on :8080")
		if err := appServer.ListenAndServe(); err != http.ErrServerClosed {
			log.Printf("Application server error: %v", err)
		}
	}()

	// Wait for shutdown signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("Shutting down servers...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	_ = appServer.Shutdown(shutdownCtx)
	_ = metricsServer.Shutdown(shutdownCtx)

	log.Println("Servers stopped")
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
```

---

## Part 2: OpenTelemetry Tracing

Add distributed tracing with the `middleware/tracing` package.

Create `internal/observability/tracing.go`:

```go
package observability

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.21.0"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/credentials"

	"go-mink.dev"
)

// TracingConfig holds tracing configuration.
type TracingConfig struct {
	ServiceName    string
	ServiceVersion string
	Environment    string
	OTLPEndpoint   string
	// OTLPCAFile is an optional PEM bundle for the private CA that signed the
	// collector's certificate. Empty means the system trust store.
	OTLPCAFile string
	// OTLPInsecure exports traces in PLAINTEXT. Development only. It defaults to
	// false so a missing setting can never downgrade transport security, and
	// otlpTransport refuses it when Environment == "production".
	OTLPInsecure bool
}

// otlpTransport selects the transport security for the collector connection:
// TLS 1.2+ with certificate verification by default; plaintext only when explicitly
// opted into for local development.
func otlpTransport(cfg TracingConfig) (otlptracegrpc.Option, error) {
	if cfg.OTLPInsecure {
		if cfg.Environment == "production" {
			return nil, fmt.Errorf("refusing plaintext OTLP export in production")
		}
		return otlptracegrpc.WithInsecure(), nil // DEV ONLY
	}
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if cfg.OTLPCAFile != "" {
		pem, err := os.ReadFile(cfg.OTLPCAFile)
		if err != nil {
			return nil, fmt.Errorf("read OTLP CA file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("OTLP CA file %q contains no certificates", cfg.OTLPCAFile)
		}
		tlsCfg.RootCAs = pool
	}
	return otlptracegrpc.WithTLSCredentials(credentials.NewTLS(tlsCfg)), nil
}

// InitTracing initializes OpenTelemetry tracing.
func InitTracing(ctx context.Context, cfg TracingConfig) (func(), error) {
	transport, err := otlpTransport(cfg)
	if err != nil {
		return nil, err
	}

	// Create OTLP exporter: TLS to the collector unless cfg.OTLPInsecure (dev only)
	exporter, err := otlptrace.New(ctx,
		otlptracegrpc.NewClient(
			otlptracegrpc.WithEndpoint(cfg.OTLPEndpoint),
			transport,
		),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create OTLP exporter: %w", err)
	}

	// Create resource
	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(cfg.ServiceName),
			semconv.ServiceVersion(cfg.ServiceVersion),
			attribute.String("environment", cfg.Environment),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create resource: %w", err)
	}

	// Create trace provider
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)

	// Set global trace provider
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	// Return shutdown function
	return func() {
		_ = tp.Shutdown(context.Background())
	}, nil
}

// TracingMiddleware creates a command bus middleware that adds tracing.
func TracingMiddleware(tracer trace.Tracer) mink.Middleware {
	return func(next mink.MiddlewareFunc) mink.MiddlewareFunc {
		return func(ctx context.Context, cmd mink.Command) (mink.CommandResult, error) {
			// Start span
			ctx, span := tracer.Start(ctx, fmt.Sprintf("command.%s", cmd.CommandType()),
				trace.WithAttributes(
					attribute.String("command.type", cmd.CommandType()),
				),
			)
			defer span.End()

			// Add aggregate info if available
			if agg, ok := cmd.(interface{ AggregateID() string }); ok {
				span.SetAttributes(attribute.String("aggregate.id", agg.AggregateID()))
			}

			result, err := next(ctx, cmd)

			// Record result
			if err != nil {
				span.RecordError(err)
				span.SetAttributes(attribute.String("command.status", "error"))
			} else if result.IsError() {
				span.SetAttributes(
					attribute.String("command.status", "business_error"),
					attribute.String("command.error", result.Error.Error()),
				)
			} else {
				span.SetAttributes(
					attribute.String("command.status", "success"),
					attribute.String("aggregate.id", result.AggregateID),
					attribute.Int64("aggregate.version", result.Version),
				)
			}

			return result, err
		}
	}
}

// EventStoreTracingWrapper wraps an event store with tracing.
type EventStoreTracingWrapper struct {
	store  *mink.EventStore
	tracer trace.Tracer
}

// NewEventStoreTracingWrapper creates a new tracing wrapper.
func NewEventStoreTracingWrapper(store *mink.EventStore, tracer trace.Tracer) *EventStoreTracingWrapper {
	return &EventStoreTracingWrapper{
		store:  store,
		tracer: tracer,
	}
}

// SaveAggregate saves an aggregate with tracing.
func (w *EventStoreTracingWrapper) SaveAggregate(ctx context.Context, agg mink.Aggregate) error {
	ctx, span := w.tracer.Start(ctx, "eventstore.save_aggregate",
		trace.WithAttributes(
			attribute.String("aggregate.id", agg.AggregateID()),
			attribute.String("aggregate.type", agg.AggregateType()),
			attribute.Int("events.count", len(agg.UncommittedEvents())),
		),
	)
	defer span.End()

	err := w.store.SaveAggregate(ctx, agg)
	if err != nil {
		span.RecordError(err)
	}

	return err
}

// LoadAggregate loads an aggregate with tracing.
func (w *EventStoreTracingWrapper) LoadAggregate(ctx context.Context, agg mink.Aggregate) error {
	ctx, span := w.tracer.Start(ctx, "eventstore.load_aggregate",
		trace.WithAttributes(
			attribute.String("aggregate.id", agg.AggregateID()),
			attribute.String("aggregate.type", agg.AggregateType()),
		),
	)
	defer span.End()

	err := w.store.LoadAggregate(ctx, agg)
	if err != nil {
		span.RecordError(err)
	} else {
		span.SetAttributes(attribute.Int64("aggregate.version", agg.Version()))
	}

	return err
}
```

### Initialize Tracing on Startup

Add to `main.go`:

```go
func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Initialize tracing
	shutdownTracing, err := observability.InitTracing(ctx, observability.TracingConfig{
		ServiceName:    "minkshop",
		ServiceVersion: "1.0.0",
		Environment:    getEnv("ENVIRONMENT", "development"),
		OTLPEndpoint:   getEnv("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317"),
		// Private CA that signed the collector's certificate (empty = system trust store).
		OTLPCAFile: getEnv("OTEL_EXPORTER_OTLP_CERTIFICATE", ""),
		// DEV ONLY: plaintext export. Off unless explicitly opted in, and refused when
		// ENVIRONMENT=production (see otlpTransport).
		OTLPInsecure: getEnv("OTEL_EXPORTER_OTLP_INSECURE", "false") == "true",
	})
	if err != nil {
		log.Printf("Warning: Failed to initialize tracing: %v", err)
	} else {
		defer shutdownTracing()
	}

	// Create tracer
	tracer := otel.Tracer("minkshop")

	// ... rest of setup ...
}
```

---

## Part 3: Error Handling

Create robust error handling for production.

Create `internal/errors/errors.go`:

```go
package errors

import (
	"errors"
	"fmt"
	"net/http"

	"go-mink.dev"
)

// AppError represents an application error with HTTP status.
type AppError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	Details    string `json:"details,omitempty"`
	StatusCode int    `json:"-"`
	Err        error  `json:"-"`
}

func (e *AppError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s (%v)", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *AppError) Unwrap() error {
	return e.Err
}

// Common error codes
const (
	ErrCodeValidation    = "VALIDATION_ERROR"
	ErrCodeNotFound      = "NOT_FOUND"
	ErrCodeConflict      = "CONFLICT"
	ErrCodeInternal      = "INTERNAL_ERROR"
	ErrCodeUnauthorized  = "UNAUTHORIZED"
	ErrCodeForbidden     = "FORBIDDEN"
	ErrCodeBadRequest    = "BAD_REQUEST"
)

// NewValidationError creates a validation error.
func NewValidationError(message string) *AppError {
	return &AppError{
		Code:       ErrCodeValidation,
		Message:    message,
		StatusCode: http.StatusBadRequest,
	}
}

// NewNotFoundError creates a not found error.
func NewNotFoundError(resource, id string) *AppError {
	return &AppError{
		Code:       ErrCodeNotFound,
		Message:    fmt.Sprintf("%s not found", resource),
		Details:    fmt.Sprintf("ID: %s", id),
		StatusCode: http.StatusNotFound,
	}
}

// NewConflictError creates a conflict error.
func NewConflictError(message string, err error) *AppError {
	return &AppError{
		Code:       ErrCodeConflict,
		Message:    message,
		StatusCode: http.StatusConflict,
		Err:        err,
	}
}

// NewInternalError creates an internal error.
func NewInternalError(err error) *AppError {
	return &AppError{
		Code:       ErrCodeInternal,
		Message:    "An internal error occurred",
		StatusCode: http.StatusInternalServerError,
		Err:        err,
	}
}

// FromError converts a mink error to an AppError.
func FromError(err error) *AppError {
	if err == nil {
		return nil
	}

	// Check for app error
	var appErr *AppError
	if errors.As(err, &appErr) {
		return appErr
	}

	// Check for mink errors
	if mink.IsConcurrencyError(err) {
		return NewConflictError("Resource was modified by another request", err)
	}

	if mink.IsNotFoundError(err) {
		return &AppError{
			Code:       ErrCodeNotFound,
			Message:    "Resource not found",
			StatusCode: http.StatusNotFound,
			Err:        err,
		}
	}

	if mink.IsValidationError(err) {
		return &AppError{
			Code:       ErrCodeValidation,
			Message:    err.Error(),
			StatusCode: http.StatusBadRequest,
			Err:        err,
		}
	}

	// Default to internal error
	return NewInternalError(err)
}

// ErrorResponse is the JSON response for errors.
type ErrorResponse struct {
	Error *AppError `json:"error"`
}
```

### HTTP Error Handler

Create `internal/api/middleware.go`:

```go
package api

import (
	"encoding/json"
	"log"
	"net/http"
	"runtime/debug"

	appErrors "minkshop/internal/errors"
)

// ErrorHandler wraps a handler and converts errors to HTTP responses.
func ErrorHandler(handler func(w http.ResponseWriter, r *http.Request) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		err := handler(w, r)
		if err != nil {
			appErr := appErrors.FromError(err)
			
			// Log internal errors
			if appErr.StatusCode >= 500 {
				log.Printf("Internal error: %v\nStack:\n%s", err, debug.Stack())
			}

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(appErr.StatusCode)
			json.NewEncoder(w).Encode(appErrors.ErrorResponse{Error: appErr})
		}
	}
}

// RecoveryMiddleware recovers from panics and returns a 500 error.
func RecoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("Panic recovered: %v\nStack:\n%s", rec, debug.Stack())
				
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				json.NewEncoder(w).Encode(appErrors.ErrorResponse{
					Error: appErrors.NewInternalError(nil),
				})
			}
		}()
		
		next.ServeHTTP(w, r)
	})
}
```

---

## Part 4: Health Checks

Add health check endpoints for orchestration.

Create `internal/api/health.go`:

```go
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"minkshop/internal/app"
)

// HealthCheck represents a component health check.
type HealthCheck struct {
	Status    string `json:"status"`
	Component string `json:"component"`
	Message   string `json:"message,omitempty"`
	Latency   string `json:"latency,omitempty"`
}

// HealthResponse is the health check response.
type HealthResponse struct {
	Status  string        `json:"status"`
	Version string        `json:"version"`
	Checks  []HealthCheck `json:"checks"`
}

// HealthHandler returns health check endpoints.
type HealthHandler struct {
	app     *app.Application
	version string
}

// NewHealthHandler creates a new health handler.
func NewHealthHandler(app *app.Application, version string) *HealthHandler {
	return &HealthHandler{app: app, version: version}
}

// Liveness returns basic liveness status.
func (h *HealthHandler) Liveness(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{
		"status": "alive",
	})
}

// Readiness returns readiness with dependency checks.
func (h *HealthHandler) Readiness(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	checks := []HealthCheck{}
	allHealthy := true

	// Check database
	dbCheck := h.checkDatabase(ctx)
	checks = append(checks, dbCheck)
	if dbCheck.Status != "healthy" {
		allHealthy = false
	}

	// Check projection engine
	projCheck := h.checkProjections(ctx)
	checks = append(checks, projCheck)
	if projCheck.Status != "healthy" {
		allHealthy = false
	}

	response := HealthResponse{
		Status:  "healthy",
		Version: h.version,
		Checks:  checks,
	}

	if !allHealthy {
		response.Status = "unhealthy"
		w.WriteHeader(http.StatusServiceUnavailable)
	} else {
		w.WriteHeader(http.StatusOK)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func (h *HealthHandler) checkDatabase(ctx context.Context) HealthCheck {
	start := time.Now()
	
	// Try to ping database
	if err := h.app.Store.Ping(ctx); err != nil {
		return HealthCheck{
			Status:    "unhealthy",
			Component: "database",
			Message:   err.Error(),
		}
	}

	return HealthCheck{
		Status:    "healthy",
		Component: "database",
		Latency:   time.Since(start).String(),
	}
}

func (h *HealthHandler) checkProjections(ctx context.Context) HealthCheck {
	// Check if projection engine is running
	if !h.app.ProjectionEngine.IsRunning() {
		return HealthCheck{
			Status:    "unhealthy",
			Component: "projections",
			Message:   "Projection engine is not running",
		}
	}

	return HealthCheck{
		Status:    "healthy",
		Component: "projections",
	}
}
```

---

## Part 5: Docker Deployment

Create production-ready Docker configuration.

Create `Dockerfile`:

```dockerfile
# Build stage
FROM golang:1.26-alpine AS builder

WORKDIR /app

# Install dependencies
RUN apk add --no-cache git ca-certificates tzdata

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source
COPY . .

# Build
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /minkshop ./cmd/server

# Runtime stage
FROM alpine:3.19

WORKDIR /app

# Add non-root user
RUN adduser -D -u 1000 appuser

# Copy binary
COPY --from=builder /minkshop /app/minkshop
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo

# Set ownership
RUN chown -R appuser:appuser /app

USER appuser

# Expose ports
EXPOSE 8080 9090

# Health check
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://localhost:8080/health/live || exit 1

ENTRYPOINT ["/app/minkshop"]
```

Create `docker-compose.prod.yml`. Every hop is encrypted: the app reaches PostgreSQL with
`sslmode=verify-full` (server certificate **and** hostname checked against your CA) and
ships traces to Jaeger over TLS. Put the certificates in `./certs/` before starting:
`ca.crt` (your CA), `postgres.crt`/`postgres.key` issued for the hostname `postgres`, and
`jaeger.crt`/`jaeger.key` issued for `jaeger`. PostgreSQL refuses a key file that other
users can read, so for the Alpine image run
`chown 70:70 certs/postgres.key && chmod 600 certs/postgres.key` on the host first.

```yaml
version: '3.8'

services:
  minkshop:
    build:
      context: .
      dockerfile: Dockerfile
    ports:
      - "8080:8080"
      # 9090 (metrics) is deliberately NOT published: Prometheus scrapes minkshop:9090
      # over the private compose network.
    environment:
      # verify-full = TLS + server-certificate + hostname verification against ca.crt.
      # sslmode=disable (plaintext) and sslmode=require (no certificate check) are
      # development-only settings and must never reach production.
      - DATABASE_URL=postgres://postgres:${POSTGRES_PASSWORD}@postgres:5432/minkshop?sslmode=verify-full&sslrootcert=/etc/ssl/certs/postgres-ca.crt
      - DATABASE_SCHEMA=mink
      - ENVIRONMENT=production
      # The OTLP exporter from Part 3 uses TLS unless OTEL_EXPORTER_OTLP_INSECURE=true,
      # which is a dev-only switch and is deliberately absent here.
      - OTEL_EXPORTER_OTLP_ENDPOINT=jaeger:4317
      - OTEL_EXPORTER_OTLP_CERTIFICATE=/etc/ssl/certs/otel-ca.crt
    volumes:
      - ./certs/ca.crt:/etc/ssl/certs/postgres-ca.crt:ro
      - ./certs/ca.crt:/etc/ssl/certs/otel-ca.crt:ro
    depends_on:
      postgres:
        condition: service_healthy
    restart: unless-stopped
    deploy:
      resources:
        limits:
          memory: 512M
          cpus: '1'

  postgres:
    image: postgres:16-alpine
    # Serve TLS; the client's verify-full rejects anything else.
    command: >
      postgres -c ssl=on
               -c ssl_cert_file=/certs/postgres.crt
               -c ssl_key_file=/certs/postgres.key
    environment:
      POSTGRES_DB: minkshop
      POSTGRES_PASSWORD: ${POSTGRES_PASSWORD}
    volumes:
      - postgres_data:/var/lib/postgresql/data
      - ./certs/postgres.crt:/certs/postgres.crt:ro
      - ./certs/postgres.key:/certs/postgres.key:ro
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U postgres"]
      interval: 5s
      timeout: 5s
      retries: 5
    restart: unless-stopped

  jaeger:
    image: jaegertracing/all-in-one:latest
    ports:
      - "127.0.0.1:16686:16686"  # UI on loopback only; reach it over SSH/VPN or put it behind auth
    environment:
      - COLLECTOR_OTLP_ENABLED=true
      # TLS on the OTLP gRPC receiver; the app verifies it against ca.crt (see above).
      - COLLECTOR_OTLP_GRPC_TLS_ENABLED=true
      - COLLECTOR_OTLP_GRPC_TLS_CERT=/certs/jaeger.crt
      - COLLECTOR_OTLP_GRPC_TLS_KEY=/certs/jaeger.key
    volumes:
      - ./certs/jaeger.crt:/certs/jaeger.crt:ro
      - ./certs/jaeger.key:/certs/jaeger.key:ro
    restart: unless-stopped

  prometheus:
    image: prom/prometheus:latest
    ports:
      - "127.0.0.1:9091:9090"
    volumes:
      - ./prometheus.yml:/etc/prometheus/prometheus.yml:ro
    restart: unless-stopped

  grafana:
    image: grafana/grafana:latest
    ports:
      - "127.0.0.1:3000:3000"
    environment:
      - GF_SECURITY_ADMIN_PASSWORD=${GRAFANA_PASSWORD}
    volumes:
      - grafana_data:/var/lib/grafana
    restart: unless-stopped

volumes:
  postgres_data:
  grafana_data:
```

Create `prometheus.yml`:

```yaml
global:
  scrape_interval: 15s
  evaluation_interval: 15s

scrape_configs:
  - job_name: 'minkshop'
    static_configs:
      - targets: ['minkshop:9090']
    metrics_path: /metrics

  - job_name: 'prometheus'
    static_configs:
      - targets: ['localhost:9090']
```

---

## Part 6: Deployment Checklist

### Pre-Production Checklist

- [ ] **Database**
  - [ ] Connection pooling configured
  - [ ] Indexes on `events` table
  - [ ] Backup strategy in place
  - [ ] Point-in-time recovery enabled

- [ ] **Application**
  - [ ] Health checks responding
  - [ ] Metrics being scraped
  - [ ] Tracing connected to collector
  - [ ] Graceful shutdown implemented
  - [ ] Error handling covers all cases

- [ ] **Security**
  - [ ] TLS enabled end-to-end: PostgreSQL `sslmode=verify-full` + `sslrootcert`, OTLP exporter over TLS (no `WithInsecure()`), HTTPS at the ingress
  - [ ] Metrics (`:9090`) and the Jaeger/Prometheus/Grafana UIs not published to the internet (loopback or private network, or behind auth)
  - [ ] Secrets in environment variables
  - [ ] Database credentials rotated
  - [ ] Non-root container user

- [ ] **Observability**
  - [ ] Dashboards created
  - [ ] Alerts configured
  - [ ] Log aggregation set up
  - [ ] Trace sampling configured

### Run Production

```bash
# Set environment variables
export POSTGRES_PASSWORD=your-secure-password
export GRAFANA_PASSWORD=your-grafana-password
# TLS is mandatory in production: verify-full checks the server certificate AND the
# hostname against the CA you mount (the same ca.crt the compose file gives the app).
# Never use sslmode=disable (plaintext) or sslmode=require (no certificate check) here.
#
# Inside the compose network the app reaches PostgreSQL as "postgres" — the hostname its
# certificate was issued for — and the service publishes no port. For the host-side CLI
# commands below, either:
#  (a) publish the port on loopback (add `ports: ["127.0.0.1:5432:5432"]` to the postgres
#      service) and resolve the certificate's name locally (`127.0.0.1 postgres` in
#      /etc/hosts) — verify-full then works unchanged:
export DATABASE_URL="postgres://postgres:${POSTGRES_PASSWORD}@postgres:5432/minkshop?sslmode=verify-full&sslrootcert=./certs/ca.crt"
#  (b) or connect to localhost with sslmode=verify-ca: the certificate is still checked
#      against ca.crt, only the hostname check is skipped (verify-full against localhost
#      fails that check). Acceptable on loopback only — sslmode=require would skip the CA
#      check entirely.
# export DATABASE_URL="postgres://postgres:${POSTGRES_PASSWORD}@localhost:5432/minkshop?sslmode=verify-ca&sslrootcert=./certs/ca.crt"

# Start services
docker-compose -f docker-compose.prod.yml up -d

# Run migrations using CLI
mink migrate up

# Verify setup
mink diagnose

# Check projection status
mink projection list

# Check health
curl http://localhost:8080/health/ready

# View metrics
open http://localhost:9091

# View traces
open http://localhost:16686

# View dashboards
open http://localhost:3000
```

### Using CLI for Operations

The `mink` CLI provides essential operational commands:

```bash
# Check system health
mink diagnose

# View projection status and lag
mink projection status OrderSummary

# Rebuild a projection after schema changes
mink projection rebuild OrderSummary --yes

# Pause projection for maintenance
mink projection pause OrderSummary

# Check migration status
mink migrate status

# View stream statistics
mink stream stats

# Export stream for debugging
mink stream export Order-12345 --output debug_order.json
```

:::tip
For CI/CD pipelines, use `--non-interactive` flags and exit codes to automate deployments.
:::

---

## Congratulations! 🎉

You've built a production-ready e-commerce system with:

- ✅ Event-sourced domain model
- ✅ CQRS command and query separation
- ✅ Optimized read models with projections
- ✅ Comprehensive testing
- ✅ Prometheus metrics
- ✅ OpenTelemetry tracing
- ✅ Docker deployment

### What's Next?

Explore advanced go-mink features:

- **Sagas**: Coordinate multi-aggregate workflows
- **Outbox Pattern**: Reliable event publishing
- **Event Versioning**: Upcasting and schema evolution
- **Multi-Tenancy**: Isolated tenant data

Check the [documentation](/docs/getting-started/introduction) for more advanced topics!

---


**Back to**: [Tutorial Index →](/docs/tutorial/setup)


---

:::tip
💡 **Production Tip**: Start with simple deployments and add complexity as needed. Monitor your metrics to understand your application's behavior before optimizing.
:::
