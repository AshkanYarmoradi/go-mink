package tracing

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"go-mink.dev"
	"go-mink.dev/adapters"
	minktest "go-mink.dev/testing/testutil"
)

// setupTestTracerWith builds a tracer backed by an in-memory exporter without
// touching the global provider, so these tests can run alongside the others.
func setupTestTracerWith(t *testing.T, opts ...TracerOption) (*Tracer, *tracetest.InMemoryExporter) {
	t.Helper()
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	all := append([]TracerOption{WithTracerProvider(tp)}, opts...)
	return NewTracer(all...), exporter
}

// exceptionMessages returns the exception.message values of every exception
// event recorded on the span.
func exceptionMessages(span tracetest.SpanStub) []string {
	var out []string
	for _, ev := range span.Events {
		if ev.Name != "exception" {
			continue
		}
		for _, kv := range ev.Attributes {
			if kv.Key == "exception.message" {
				out = append(out, kv.Value.AsString())
			}
		}
	}
	return out
}

// attrValue returns the string value of key, and whether it was present.
func attrValue(attrs []attribute.KeyValue, key string) (string, bool) {
	for _, kv := range attrs {
		if string(kv.Key) == key {
			return kv.Value.AsString(), true
		}
	}
	return "", false
}

// spanMentions reports whether needle appears anywhere in the span's status
// description, attributes or event attributes.
func spanMentions(span tracetest.SpanStub, needle string) bool {
	if strings.Contains(span.Status.Description, needle) {
		return true
	}
	for _, kv := range span.Attributes {
		if strings.Contains(kv.Value.String(), needle) {
			return true
		}
	}
	for _, ev := range span.Events {
		for _, kv := range ev.Attributes {
			if strings.Contains(kv.Value.String(), needle) {
				return true
			}
		}
	}
	return false
}

// =============================================================================
// Options
// =============================================================================

func TestNewTracer_Defaults_NoRedactionAndDefaultCap(t *testing.T) {
	tracer := NewTracer()

	assert.Nil(t, tracer.redactError)
	assert.Equal(t, DefaultMaxAttributeLength, tracer.MaxAttributeLength())
	assert.Equal(t, 256, DefaultMaxAttributeLength)
}

func TestWithMaxAttributeLength_SetsCap(t *testing.T) {
	tests := []struct {
		name string
		n    int
		want int
	}{
		{name: "positive", n: 32, want: 32},
		{name: "zero disables", n: 0, want: 0},
		{name: "negative disables", n: -5, want: -5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tracer := NewTracer(WithMaxAttributeLength(tt.n))
			assert.Equal(t, tt.want, tracer.MaxAttributeLength())
		})
	}
}

func TestWithErrorRedaction_NilRestoresDefault(t *testing.T) {
	tracer := NewTracer(WithoutErrorDetails(), WithErrorRedaction(nil))
	assert.Nil(t, tracer.redactError)
}

// =============================================================================
// truncate
// =============================================================================

func Test_truncate(t *testing.T) {
	tests := []struct {
		name     string
		s        string
		maxRunes int
		want     string
	}{
		{name: "empty", s: "", maxRunes: 5, want: ""},
		{name: "shorter than cap", s: "abc", maxRunes: 5, want: "abc"},
		{name: "exactly cap", s: "abcde", maxRunes: 5, want: "abcde"},
		{name: "longer than cap", s: "abcdefgh", maxRunes: 5, want: "abcde"},
		{name: "cap of one", s: "abc", maxRunes: 1, want: "a"},
		{name: "zero cap disables", s: "abcdefgh", maxRunes: 0, want: "abcdefgh"},
		{name: "negative cap disables", s: "abcdefgh", maxRunes: -1, want: "abcdefgh"},
		{name: "multibyte runes counted as runes", s: "héllo wörld", maxRunes: 5, want: "héllo"},
		{name: "cut on rune boundary", s: "日本語テキスト", maxRunes: 3, want: "日本語"},
		{name: "multibyte within cap by rune count", s: "日本語", maxRunes: 3, want: "日本語"},
		{name: "emoji", s: "🙂🙂🙂🙂", maxRunes: 2, want: "🙂🙂"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncate(tt.s, tt.maxRunes)
			assert.Equal(t, tt.want, got)
			assert.True(t, utf8.ValidString(got), "result must stay valid UTF-8")
			if tt.maxRunes > 0 {
				assert.LessOrEqual(t, utf8.RuneCountInString(got), tt.maxRunes)
			}
		})
	}
}

// =============================================================================
// Error redaction: command middleware
// =============================================================================

func TestCommandMiddleware_ErrorRedaction_DefaultRecordsRawError(t *testing.T) {
	tracer, exporter := setupTestTracerWith(t)
	cmd := &minktest.TestCommand{ID: "test-123"}
	rawErr := errors.New("validation failed: email=alice@example.com")

	handler := CommandMiddleware(tracer)(func(ctx context.Context, cmd mink.Command) (mink.CommandResult, error) {
		return mink.NewErrorResult(rawErr), rawErr
	})
	_, err := handler(context.Background(), cmd)
	require.ErrorIs(t, err, rawErr)

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)
	assert.Equal(t, codes.Error, spans[0].Status.Code)
	assert.Equal(t, rawErr.Error(), spans[0].Status.Description)
	assert.Equal(t, []string{rawErr.Error()}, exceptionMessages(spans[0]))
}

func TestCommandMiddleware_ErrorRedaction_WithErrorRedaction(t *testing.T) {
	rawErr := errors.New("validation failed: email=alice@example.com")

	tests := []struct {
		name      string
		handler   mink.MiddlewareFunc
		returnErr bool
	}{
		{
			name: "handler error",
			handler: func(ctx context.Context, cmd mink.Command) (mink.CommandResult, error) {
				return mink.NewErrorResult(rawErr), rawErr
			},
			returnErr: true,
		},
		{
			name: "result error without handler error",
			handler: func(ctx context.Context, cmd mink.Command) (mink.CommandResult, error) {
				return mink.NewErrorResult(rawErr), nil
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var seen error
			tracer, exporter := setupTestTracerWith(t, WithErrorRedaction(func(err error) string {
				seen = err
				return "redacted: " + errorClass(err)
			}))
			cmd := &minktest.TestCommand{ID: "test-123"}

			result, err := CommandMiddleware(tracer)(tt.handler)(context.Background(), cmd)

			// The caller still receives the original error untouched.
			if tt.returnErr {
				require.ErrorIs(t, err, rawErr)
			} else {
				require.NoError(t, err)
				require.ErrorIs(t, result.Error, rawErr)
			}
			assert.Same(t, rawErr, seen, "redaction fn receives the original error")

			spans := exporter.GetSpans()
			require.Len(t, spans, 1)
			assert.Equal(t, codes.Error, spans[0].Status.Code)
			assert.Equal(t, "redacted: *errors.errorString", spans[0].Status.Description)
			assert.Equal(t, []string{"redacted: *errors.errorString"}, exceptionMessages(spans[0]))
			assert.False(t, spanMentions(spans[0], "alice@example.com"), "raw error text leaked into the span")
		})
	}
}

func TestCommandMiddleware_ErrorRedaction_WithoutErrorDetails(t *testing.T) {
	tracer, exporter := setupTestTracerWith(t, WithoutErrorDetails())
	cmd := &minktest.TestCommand{ID: "test-123"}
	rawErr := errors.New("validation failed: ssn=123-45-6789")

	_, err := CommandMiddleware(tracer)(func(ctx context.Context, cmd mink.Command) (mink.CommandResult, error) {
		return mink.NewErrorResult(rawErr), rawErr
	})(context.Background(), cmd)
	require.ErrorIs(t, err, rawErr)

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)
	assert.Equal(t, codes.Error, spans[0].Status.Code)
	assert.Equal(t, RedactedErrorMessage, spans[0].Status.Description)
	assert.Equal(t, []string{RedactedErrorMessage}, exceptionMessages(spans[0]))
	assert.False(t, spanMentions(spans[0], "123-45-6789"), "raw error text leaked into the span")
}

func TestCommandMiddleware_ErrorRedaction_SuccessUnaffected(t *testing.T) {
	tracer, exporter := setupTestTracerWith(t, WithoutErrorDetails())
	cmd := &minktest.TestCommand{ID: "test-123"}

	_, err := CommandMiddleware(tracer)(func(ctx context.Context, cmd mink.Command) (mink.CommandResult, error) {
		return mink.NewSuccessResult("test-123", 7), nil
	})(context.Background(), cmd)
	require.NoError(t, err)

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)
	assert.Equal(t, codes.Ok, spans[0].Status.Code)
	assert.Empty(t, spans[0].Events)
	assertAttribute(t, spans[0].Attributes, "mink.result.aggregate_id", "test-123")
}

// =============================================================================
// Error redaction: event store and projection middleware
// =============================================================================

func TestEventStoreMiddleware_ErrorRedaction_AllOperations(t *testing.T) {
	rawErr := errors.New("pq: duplicate key value violates unique constraint (stream_id)=(user-alice@example.com)")

	tests := []struct {
		name     string
		adapter  adapters.EventStoreAdapter
		call     func(m *EventStoreMiddleware) error
		spanName string
	}{
		{
			name:    "Append",
			adapter: &minktest.MockAdapter{AppendErr: rawErr},
			call: func(m *EventStoreMiddleware) error {
				_, err := m.Append(context.Background(), "s", []adapters.EventRecord{{Type: "E", Data: []byte("{}")}}, mink.AnyVersion)
				return err
			},
			spanName: "eventstore.append",
		},
		{
			name:    "Load",
			adapter: &minktest.MockAdapter{LoadErr: rawErr},
			call: func(m *EventStoreMiddleware) error {
				_, err := m.Load(context.Background(), "s", 0)
				return err
			},
			spanName: "eventstore.load",
		},
		{
			name:    "GetStreamInfo",
			adapter: &minktest.MockAdapter{GetStreamInfoErr: rawErr},
			call: func(m *EventStoreMiddleware) error {
				_, err := m.GetStreamInfo(context.Background(), "s")
				return err
			},
			spanName: "eventstore.get_stream_info",
		},
		{
			name:    "GetLastPosition",
			adapter: &minktest.MockAdapter{GetLastPositionErr: rawErr},
			call: func(m *EventStoreMiddleware) error {
				_, err := m.GetLastPosition(context.Background())
				return err
			},
			spanName: "eventstore.get_last_position",
		},
		{
			name:    "Initialize",
			adapter: &failingInitAdapter{MockAdapter: &minktest.MockAdapter{}, err: rawErr},
			call: func(m *EventStoreMiddleware) error {
				return m.Initialize(context.Background())
			},
			spanName: "eventstore.initialize",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name+" redacted", func(t *testing.T) {
			tracer, exporter := setupTestTracerWith(t, WithoutErrorDetails())
			middleware := NewEventStoreMiddleware(tt.adapter, tracer)

			err := tt.call(middleware)
			require.ErrorIs(t, err, rawErr)

			spans := exporter.GetSpans()
			require.Len(t, spans, 1)
			assert.Equal(t, tt.spanName, spans[0].Name)
			assert.Equal(t, codes.Error, spans[0].Status.Code)
			assert.Equal(t, RedactedErrorMessage, spans[0].Status.Description)
			assert.Equal(t, []string{RedactedErrorMessage}, exceptionMessages(spans[0]))
			assert.False(t, spanMentions(spans[0], "alice@example.com"), "raw error text leaked into the span")
		})

		t.Run(tt.name+" raw by default", func(t *testing.T) {
			tracer, exporter := setupTestTracerWith(t)
			middleware := NewEventStoreMiddleware(tt.adapter, tracer)

			err := tt.call(middleware)
			require.ErrorIs(t, err, rawErr)

			spans := exporter.GetSpans()
			require.Len(t, spans, 1)
			assert.Equal(t, rawErr.Error(), spans[0].Status.Description)
			assert.Equal(t, []string{rawErr.Error()}, exceptionMessages(spans[0]))
		})
	}
}

func TestProjectionMiddleware_ErrorRedaction(t *testing.T) {
	rawErr := errors.New("apply failed for customer alice@example.com")
	event := mink.StoredEvent{ID: "event-1", StreamID: "customer-1", Type: "CustomerRegistered"}

	t.Run("redacted", func(t *testing.T) {
		tracer, exporter := setupTestTracerWith(t, WithErrorRedaction(func(error) string { return "projection failed" }))
		projection := &minktest.MockProjection{ProjectionName: "Customers", ApplyErr: rawErr}

		err := NewProjectionMiddleware(projection, tracer).Apply(context.Background(), event)
		require.ErrorIs(t, err, rawErr)

		spans := exporter.GetSpans()
		require.Len(t, spans, 1)
		assert.Equal(t, codes.Error, spans[0].Status.Code)
		assert.Equal(t, "projection failed", spans[0].Status.Description)
		assert.Equal(t, []string{"projection failed"}, exceptionMessages(spans[0]))
		assert.False(t, spanMentions(spans[0], "alice@example.com"), "raw error text leaked into the span")
	})

	t.Run("raw by default", func(t *testing.T) {
		tracer, exporter := setupTestTracerWith(t)
		projection := &minktest.MockProjection{ProjectionName: "Customers", ApplyErr: rawErr}

		err := NewProjectionMiddleware(projection, tracer).Apply(context.Background(), event)
		require.ErrorIs(t, err, rawErr)

		spans := exporter.GetSpans()
		require.Len(t, spans, 1)
		assert.Equal(t, rawErr.Error(), spans[0].Status.Description)
		assert.Equal(t, []string{rawErr.Error()}, exceptionMessages(spans[0]))
	})
}

// SetError is a package-level helper with no Tracer, so redaction cannot
// apply; pin that documented behavior.
func TestSetError_RecordsRawError(t *testing.T) {
	tracer, exporter := setupTestTracerWith(t, WithoutErrorDetails())

	ctx, span := tracer.StartSpan(context.Background(), "test")
	SetError(ctx, errors.New("raw detail"))
	span.End()

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)
	assert.Equal(t, "raw detail", spans[0].Status.Description)
}

// =============================================================================
// Attribute truncation
// =============================================================================

func TestCommandMiddleware_AttributeTruncation_DefaultCap(t *testing.T) {
	tracer, exporter := setupTestTracerWith(t)
	longID := strings.Repeat("a", 1000)
	cmd := &minktest.TestCommand{ID: longID}

	_, err := CommandMiddleware(tracer)(func(ctx context.Context, cmd mink.Command) (mink.CommandResult, error) {
		return mink.NewSuccessResult(longID, 1), nil
	})(context.Background(), cmd)
	require.NoError(t, err)

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)

	got, ok := attrValue(spans[0].Attributes, "mink.command.aggregate_id")
	require.True(t, ok)
	assert.Len(t, got, DefaultMaxAttributeLength)
	assert.Equal(t, strings.Repeat("a", DefaultMaxAttributeLength), got)

	got, ok = attrValue(spans[0].Attributes, "mink.result.aggregate_id")
	require.True(t, ok)
	assert.Len(t, got, DefaultMaxAttributeLength)
}

func TestCommandMiddleware_AttributeTruncation_CorrelationID(t *testing.T) {
	tracer, exporter := setupTestTracerWith(t, WithMaxAttributeLength(8))
	cmd := &minktest.TestCommand{ID: "id"}

	correlation := mink.CorrelationIDMiddleware(func() string { return "correlation-id-that-is-long" })
	handler := correlation(CommandMiddleware(tracer)(func(ctx context.Context, cmd mink.Command) (mink.CommandResult, error) {
		return mink.NewSuccessResult("id", 1), nil
	}))

	_, err := handler(context.Background(), cmd)
	require.NoError(t, err)

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)
	assertAttribute(t, spans[0].Attributes, "mink.correlation_id", "correlat")
}

func TestCommandMiddleware_AttributeTruncation_CommandTypeAndSpanName(t *testing.T) {
	tracer, exporter := setupTestTracerWith(t, WithMaxAttributeLength(4))
	cmd := &minktest.TestCommand{ID: "id"}

	_, err := CommandMiddleware(tracer)(func(ctx context.Context, cmd mink.Command) (mink.CommandResult, error) {
		return mink.NewSuccessResult("id", 1), nil
	})(context.Background(), cmd)
	require.NoError(t, err)

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)
	assert.Equal(t, "command.Test", spans[0].Name)
	assertAttribute(t, spans[0].Attributes, "mink.command.type", "Test")
}

func TestCommandMiddleware_AttributeTruncation_Disabled(t *testing.T) {
	tracer, exporter := setupTestTracerWith(t, WithMaxAttributeLength(0))
	longID := strings.Repeat("a", 1000)
	cmd := &minktest.TestCommand{ID: longID}

	_, err := CommandMiddleware(tracer)(func(ctx context.Context, cmd mink.Command) (mink.CommandResult, error) {
		return mink.NewSuccessResult(longID, 1), nil
	})(context.Background(), cmd)
	require.NoError(t, err)

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)
	assertAttribute(t, spans[0].Attributes, "mink.command.aggregate_id", longID)
}

func TestEventStoreMiddleware_AttributeTruncation(t *testing.T) {
	longStream := strings.Repeat("s", 300)
	longType := strings.Repeat("T", 300)
	want := strings.Repeat("s", DefaultMaxAttributeLength)

	t.Run("Append truncates stream id and event types", func(t *testing.T) {
		tracer, exporter := setupTestTracerWith(t)
		middleware := NewEventStoreMiddleware(&minktest.MockAdapter{}, tracer)

		_, err := middleware.Append(context.Background(), longStream, []adapters.EventRecord{
			{Type: longType, Data: []byte("{}")},
			{Type: "Short", Data: []byte("{}")},
		}, mink.NoStream)
		require.NoError(t, err)

		spans := exporter.GetSpans()
		require.Len(t, spans, 1)
		assertAttribute(t, spans[0].Attributes, "mink.stream_id", want)

		var types []string
		for _, kv := range spans[0].Attributes {
			if kv.Key == "mink.events.types" {
				types = kv.Value.AsStringSlice()
			}
		}
		require.Len(t, types, 2)
		assert.Equal(t, strings.Repeat("T", DefaultMaxAttributeLength), types[0])
		assert.Equal(t, "Short", types[1])
	})

	t.Run("Load truncates stream id", func(t *testing.T) {
		tracer, exporter := setupTestTracerWith(t)
		middleware := NewEventStoreMiddleware(&minktest.MockAdapter{}, tracer)

		_, err := middleware.Load(context.Background(), longStream, 0)
		require.NoError(t, err)

		spans := exporter.GetSpans()
		require.Len(t, spans, 1)
		assertAttribute(t, spans[0].Attributes, "mink.stream_id", want)
	})

	t.Run("GetStreamInfo truncates stream id", func(t *testing.T) {
		tracer, exporter := setupTestTracerWith(t)
		middleware := NewEventStoreMiddleware(&minktest.MockAdapter{}, tracer)

		_, _ = middleware.GetStreamInfo(context.Background(), longStream)

		spans := exporter.GetSpans()
		require.Len(t, spans, 1)
		assertAttribute(t, spans[0].Attributes, "mink.stream_id", want)
	})

	t.Run("custom cap applies", func(t *testing.T) {
		tracer, exporter := setupTestTracerWith(t, WithMaxAttributeLength(3))
		middleware := NewEventStoreMiddleware(&minktest.MockAdapter{}, tracer)

		_, err := middleware.Load(context.Background(), "order-123", 0)
		require.NoError(t, err)

		spans := exporter.GetSpans()
		require.Len(t, spans, 1)
		assertAttribute(t, spans[0].Attributes, "mink.stream_id", "ord")
	})
}

func TestProjectionMiddleware_AttributeTruncation(t *testing.T) {
	tracer, exporter := setupTestTracerWith(t, WithMaxAttributeLength(5))
	projection := &minktest.MockProjection{ProjectionName: "OrderProjection"}
	middleware := NewProjectionMiddleware(projection, tracer)

	event := mink.StoredEvent{
		ID:       "event-123456",
		StreamID: "order-123456",
		Type:     "OrderCreated",
	}
	err := middleware.Apply(context.Background(), event)
	require.NoError(t, err)

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)
	assert.Equal(t, "projection.Order.apply", spans[0].Name)
	assertAttribute(t, spans[0].Attributes, "mink.projection.name", "Order")
	assertAttribute(t, spans[0].Attributes, "mink.event.type", "Order")
	assertAttribute(t, spans[0].Attributes, "mink.event.id", "event")
	assertAttribute(t, spans[0].Attributes, "mink.event.stream_id", "order")

	// Delegating methods are not truncated: they return the projection's own
	// values, not span attributes.
	assert.Equal(t, "OrderProjection", middleware.Name())
}

// =============================================================================
// Test doubles
// =============================================================================

// failingInitAdapter forces Initialize to fail; MockAdapter has no InitializeErr.
type failingInitAdapter struct {
	*minktest.MockAdapter
	err error
}

func (a *failingInitAdapter) Initialize(context.Context) error { return a.err }

// errorClass returns the dynamic type name of err: a PII-free description a
// redaction function might reasonably emit.
func errorClass(err error) string {
	return fmt.Sprintf("%T", err)
}

// RedactedErrorMessage is recorded on command, event-store AND projection spans,
// so it must not name any one of them.
func TestRedactedErrorMessage_IsNeutralAcrossSpanKinds(t *testing.T) {
	assert.Equal(t, "operation failed", RedactedErrorMessage)
	assert.NotContains(t, RedactedErrorMessage, "command")
	assert.NotContains(t, RedactedErrorMessage, "projection")

	rawErr := errors.New("load failed for stream user-alice@example.com")
	tracer, exporter := setupTestTracerWith(t, WithoutErrorDetails())

	_, err := NewEventStoreMiddleware(&minktest.MockAdapter{LoadErr: rawErr}, tracer).Load(context.Background(), "s", 0)
	require.ErrorIs(t, err, rawErr)
	projErr := NewProjectionMiddleware(&minktest.MockProjection{ProjectionName: "Customers", ApplyErr: rawErr}, tracer).
		Apply(context.Background(), mink.StoredEvent{ID: "e1", StreamID: "customer-1", Type: "CustomerRegistered"})
	require.ErrorIs(t, projErr, rawErr)

	spans := exporter.GetSpans()
	require.Len(t, spans, 2)
	for _, span := range spans {
		assert.Equal(t, "operation failed", span.Status.Description, span.Name)
		assert.Equal(t, []string{"operation failed"}, exceptionMessages(span), span.Name)
		assert.False(t, spanMentions(span, "command failed"), "%s: span must not claim a command failed", span.Name)
		assert.False(t, spanMentions(span, "alice@example.com"), "%s: raw error text leaked", span.Name)
	}
}
