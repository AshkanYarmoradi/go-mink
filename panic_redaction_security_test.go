package mink

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests pin the panic-value redaction contract: every panic recovery in
// mink (RecoveryMiddleware, the projection engine's inline/async/live paths and
// the upcaster chain) logs and wraps only PanicValueSummary of the recovered
// value, so a handler, projection or upcaster that panics with the command, the
// event or its data never copies that payload into an error or a log.

const redactionSecret = "SENSITIVE-4111111111111111"

// redactionOrder is a payload-bearing value a handler might panic with.
type redactionOrder struct {
	ID   string
	Card string
}

// redactionRuntimeError is a runtime.Error implemented outside package runtime;
// its message is application-controlled and must be treated as a payload.
type redactionRuntimeError struct{ payload string }

func (e redactionRuntimeError) Error() string { return "custom runtime error: " + e.payload }
func (e redactionRuntimeError) RuntimeError() {}

// triggerRuntimePanic performs fn and returns the recovered runtime panic value.
func triggerRuntimePanic(t *testing.T, fn func()) (recovered interface{}) {
	t.Helper()
	defer func() { recovered = recover() }()
	fn()
	t.Fatal("fn did not panic")
	return nil
}

// panicArgsLogger records every log call with its message and formatted args,
// rendering []byte args as text, so a leaked payload is detectable whether it was
// logged as a string, a struct or raw bytes.
type panicArgsLogger struct {
	mu      sync.Mutex
	entries []string
}

func (l *panicArgsLogger) record(level, msg string, args []interface{}) {
	var b strings.Builder
	b.WriteString(level)
	b.WriteString(" ")
	b.WriteString(msg)
	for _, a := range args {
		b.WriteString(" ")
		if raw, ok := a.([]byte); ok {
			b.WriteString(string(raw))
			continue
		}
		fmt.Fprintf(&b, "%+v", a)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, b.String())
}

func (l *panicArgsLogger) Debug(msg string, args ...interface{}) { l.record("debug", msg, args) }
func (l *panicArgsLogger) Info(msg string, args ...interface{})  { l.record("info", msg, args) }
func (l *panicArgsLogger) Warn(msg string, args ...interface{})  { l.record("warn", msg, args) }
func (l *panicArgsLogger) Error(msg string, args ...interface{}) { l.record("error", msg, args) }

// all returns every recorded entry joined, for Contains / NotContains assertions.
func (l *panicArgsLogger) all() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.entries, "\n")
}

func (l *panicArgsLogger) has(msg string) bool {
	return strings.Contains(l.all(), msg)
}

// =============================================================================
// PanicValueSummary
// =============================================================================

func TestPanicValueSummary(t *testing.T) {
	nilDeref := triggerRuntimePanic(t, func() {
		var o *redactionOrder
		_ = o.Card
	})
	outOfRange := triggerRuntimePanic(t, func() {
		s := []string{redactionSecret}
		i := len(s)
		_ = s[i]
	})
	badAssertion := triggerRuntimePanic(t, func() {
		var v interface{} = redactionSecret
		_ = v.(int)
	})

	tests := []struct {
		name        string
		value       interface{}
		want        string // exact, when non-empty
		contains    []string
		notContains []string
	}{
		{
			name:  "nil value",
			value: nil,
			want:  "nil panic value",
		},
		{
			name:        "string is redacted to its type",
			value:       "order " + redactionSecret + " failed",
			want:        "redacted panic value of type string",
			notContains: []string{redactionSecret},
		},
		{
			name:        "struct pointer is redacted to its type",
			value:       &redactionOrder{ID: "o-1", Card: redactionSecret},
			want:        "redacted panic value of type *mink.redactionOrder",
			notContains: []string{redactionSecret, "o-1"},
		},
		{
			name:        "struct value is redacted to its type",
			value:       redactionOrder{ID: "o-1", Card: redactionSecret},
			want:        "redacted panic value of type mink.redactionOrder",
			notContains: []string{redactionSecret},
		},
		{
			name:        "plain error is redacted to its type",
			value:       errors.New("lookup failed for " + redactionSecret),
			want:        "redacted panic value of type *errors.errorString",
			notContains: []string{redactionSecret},
		},
		{
			name:  "integer is redacted to its type",
			value: 42,
			want:  "redacted panic value of type int",
		},
		{
			name:        "custom runtime.Error outside package runtime is redacted",
			value:       redactionRuntimeError{payload: redactionSecret},
			want:        "redacted panic value of type mink.redactionRuntimeError",
			notContains: []string{redactionSecret, "custom runtime error"},
		},
		{
			name:     "nil dereference keeps type and message",
			value:    nilDeref,
			contains: []string{"runtime.", "invalid memory address or nil pointer dereference"},
		},
		{
			name:        "index out of range keeps type and message",
			value:       outOfRange,
			contains:    []string{"runtime.", "index out of range [1] with length 1"},
			notContains: []string{redactionSecret},
		},
		{
			name:        "failed type assertion keeps type and message",
			value:       badAssertion,
			contains:    []string{"*runtime.TypeAssertionError", "interface conversion"},
			notContains: []string{redactionSecret},
		},
		{
			// Calling Error() on this value panics; the summary must not.
			name:  "typed nil runtime error is reported as its type without calling Error",
			value: (*runtime.TypeAssertionError)(nil),
			want:  "redacted panic value of type *runtime.TypeAssertionError (nil)",
		},
		{
			name:  "typed nil PanicNilError is reported as its type",
			value: (*runtime.PanicNilError)(nil),
			want:  "redacted panic value of type *runtime.PanicNilError (nil)",
		},
		{
			name:  "typed nil runtime.Error implemented outside package runtime",
			value: (*redactionRuntimeError)(nil),
			want:  "redacted panic value of type *mink.redactionRuntimeError (nil)",
		},
		{
			name:  "typed nil plain pointer is reported as its type",
			value: (*redactionOrder)(nil),
			want:  "redacted panic value of type *mink.redactionOrder (nil)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got string
			require.NotPanics(t, func() { got = PanicValueSummary(tt.value) })
			if tt.want != "" {
				assert.Equal(t, tt.want, got)
			}
			for _, s := range tt.contains {
				assert.Contains(t, got, s)
			}
			for _, s := range tt.notContains {
				assert.NotContains(t, got, s)
			}
		})
	}
}

func TestIsRuntimePackageType(t *testing.T) {
	nilDeref := triggerRuntimePanic(t, func() {
		var o *redactionOrder
		_ = o.Card
	})
	re, ok := nilDeref.(error)
	require.True(t, ok)

	assert.Contains(t, PanicValueSummary(re), re.Error(), "a runtime package error keeps its message")
	assert.Equal(t, "redacted panic value of type *mink.redactionRuntimeError",
		PanicValueSummary(&redactionRuntimeError{payload: "x"}),
		"a pointer to a non-runtime type is followed to its element and still redacted")
}

// misbehavingError is an error whose Error method panics.
type misbehavingError struct{}

func (misbehavingError) Error() string { panic("Error() misbehaved") }

func TestSafeErrorMessage(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		wantMsg string
		wantOK  bool
	}{
		{
			name:    "message is returned",
			err:     errors.New("boom"),
			wantMsg: "boom",
			wantOK:  true,
		},
		{
			name:   "panicking Error method is contained",
			err:    misbehavingError{},
			wantOK: false,
		},
		{
			name:   "typed nil receiver that dereferences itself is contained",
			err:    (*runtime.TypeAssertionError)(nil),
			wantOK: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var msg string
			var ok bool
			require.NotPanics(t, func() { msg, ok = safeErrorMessage(tt.err) })
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.wantMsg, msg)
		})
	}
}

// TestPanicValueSummary_NeverPanics pins that the summary is safe to call from
// inside a deferred recover for runtime-package values whose Error method
// cannot be trusted: typed nils and a zero value whose Error method may
// dereference nil type descriptors depending on the Go version. The result is
// either the message or the type-only fallback, but never a second panic.
func TestPanicValueSummary_NeverPanics(t *testing.T) {
	tests := []struct {
		name  string
		value interface{}
	}{
		{name: "typed nil TypeAssertionError", value: (*runtime.TypeAssertionError)(nil)},
		{name: "zero value TypeAssertionError", value: &runtime.TypeAssertionError{}},
		{name: "typed nil PanicNilError", value: (*runtime.PanicNilError)(nil)},
		{name: "zero value PanicNilError", value: &runtime.PanicNilError{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got string
			require.NotPanics(t, func() { got = PanicValueSummary(tt.value) })
			assert.Contains(t, got, fmt.Sprintf("%T", tt.value))
		})
	}
}

// =============================================================================
// PanicError.Error
// =============================================================================

func TestPanicError_Error_NeverPrintsMoreThanSummary(t *testing.T) {
	nilDeref := triggerRuntimePanic(t, func() {
		var o *redactionOrder
		_ = o.Card
	})

	tests := []struct {
		name        string
		err         *PanicError
		contains    []string
		notContains []string
	}{
		{
			name:     "string value is printed as given (it is the summary by default)",
			err:      NewPanicError("CreateOrder", "redacted panic value of type string", ""),
			contains: []string{`"CreateOrder"`, "redacted panic value of type string"},
		},
		{
			name:        "raw struct value is summarized",
			err:         NewPanicError("CreateOrder", &redactionOrder{ID: "o-1", Card: redactionSecret}, ""),
			contains:    []string{"redacted panic value of type *mink.redactionOrder"},
			notContains: []string{redactionSecret, "o-1"},
		},
		{
			name:        "raw error value is summarized",
			err:         NewPanicErrorWithCommand("CreateOrder", errors.New(redactionSecret), "", "{}"),
			contains:    []string{"redacted panic value of type *errors.errorString"},
			notContains: []string{redactionSecret},
		},
		{
			name:     "runtime error keeps its message",
			err:      NewPanicError("CreateOrder", nilDeref, ""),
			contains: []string{"nil pointer dereference"},
		},
		{
			name:        "retained summary wins over a raw string value",
			err:         &PanicError{CommandType: "CreateOrder", Value: "order " + redactionSecret, summary: "redacted panic value of type string"},
			contains:    []string{"redacted panic value of type string"},
			notContains: []string{redactionSecret},
		},
		{
			name:     "nil value",
			err:      NewPanicError("CreateOrder", nil, ""),
			contains: []string{"nil panic value"},
		},
		{
			name:     "raw typed nil runtime error is summarized without calling its Error method",
			err:      NewPanicError("CreateOrder", (*runtime.TypeAssertionError)(nil), ""),
			contains: []string{"redacted panic value of type *runtime.TypeAssertionError (nil)"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var msg string
			require.NotPanics(t, func() { msg = tt.err.Error() })
			assert.True(t, strings.HasPrefix(msg, `mink: handler panicked while processing "CreateOrder": `), msg)
			for _, s := range tt.contains {
				assert.Contains(t, msg, s)
			}
			for _, s := range tt.notContains {
				assert.NotContains(t, msg, s)
			}
			assert.ErrorIs(t, tt.err, ErrHandlerPanicked)
		})
	}
}

// =============================================================================
// RecoveryMiddleware
// =============================================================================

func panicWithCommandHandler(ctx context.Context, cmd Command) (CommandResult, error) {
	panic(cmd)
}

func TestRecoveryMiddleware_DefaultValue_CommandStructPanicIsRedacted(t *testing.T) {
	cmd := recoverySensitiveCommand{OrderID: "order-1", CardNumber: redactionSecret}

	result, err := RecoveryMiddleware()(panicWithCommandHandler)(context.Background(), cmd)
	require.Error(t, err)
	assert.True(t, result.IsError())
	var panicErr *PanicError
	require.ErrorAs(t, err, &panicErr)

	value, ok := panicErr.Value.(string)
	require.True(t, ok, "by default Value holds the summary string, got %T", panicErr.Value)
	assert.Equal(t, "redacted panic value of type mink.recoverySensitiveCommand", value)

	for _, text := range []string{value, panicErr.Error(), result.Error.Error()} {
		assert.Contains(t, text, "mink.recoverySensitiveCommand", "the type name is kept for debugging")
		assert.NotContains(t, text, redactionSecret, "command field values must not leak")
		assert.NotContains(t, text, "order-1")
	}
	assert.Equal(t, `{"commandType":"RecoverySensitiveCommand","aggregateId":"order-1"}`, panicErr.CommandData,
		"the command-data summary is unaffected")
	assert.NotEmpty(t, panicErr.Stack)
}

func TestRecoveryMiddleware_DefaultValue_StringPanicIsRedacted(t *testing.T) {
	cmd := recoverySensitiveCommand{OrderID: "order-1", CardNumber: redactionSecret}
	handler := func(ctx context.Context, cmd Command) (CommandResult, error) {
		c := cmd.(recoverySensitiveCommand)
		panic(fmt.Sprintf("cannot charge card %s for %s", c.CardNumber, c.OrderID))
	}

	result, err := RecoveryMiddleware()(handler)(context.Background(), cmd)
	require.Error(t, err)
	assert.True(t, result.IsError())
	var panicErr *PanicError
	require.ErrorAs(t, err, &panicErr)

	assert.Equal(t, "redacted panic value of type string", panicErr.Value,
		"a string panic may be a message formatted from the command, so only its type is kept")
	for _, text := range []string{err.Error(), result.Error.Error()} {
		assert.Contains(t, text, "redacted panic value of type string")
		assert.NotContains(t, text, redactionSecret)
		assert.NotContains(t, text, "cannot charge card")
	}
}

func TestRecoveryMiddleware_DefaultValue_RuntimeErrorKeepsMessage(t *testing.T) {
	handler := func(ctx context.Context, cmd Command) (CommandResult, error) {
		var o *redactionOrder
		return NewSuccessResult(o.ID, 0), nil // nil dereference
	}

	result, err := RecoveryMiddleware()(handler)(context.Background(), middlewareTestCommand{Value: redactionSecret})
	require.Error(t, err)
	assert.True(t, result.IsError())
	var panicErr *PanicError
	require.ErrorAs(t, err, &panicErr)

	value, ok := panicErr.Value.(string)
	require.True(t, ok, "a runtime error is still recorded as its summary string")
	assert.Contains(t, value, "invalid memory address or nil pointer dereference")
	assert.Contains(t, value, "runtime.")
	assert.Contains(t, err.Error(), "invalid memory address or nil pointer dereference",
		"runtime error messages carry no user data and stay in the error")
	assert.NotContains(t, err.Error(), redactionSecret)
}

// TestRecoveryMiddleware_TypedNilRuntimeErrorPanic_DoesNotUnwind pins the
// regression where a handler panicking with a typed nil runtime error — whose
// Error method panics when called — unwound through the recovery path itself.
func TestRecoveryMiddleware_TypedNilRuntimeErrorPanic_DoesNotUnwind(t *testing.T) {
	handler := func(ctx context.Context, cmd Command) (CommandResult, error) {
		panic((*runtime.TypeAssertionError)(nil))
	}

	tests := []struct {
		name string
		mw   Middleware
	}{
		{name: "default", mw: RecoveryMiddleware()},
		{name: "with value capture", mw: RecoveryMiddlewareWithOptions(WithPanicValueCapture())},
		{name: "with value and command capture", mw: RecoveryMiddlewareWithOptions(WithPanicValueCapture(), WithPanicCommandCapture())},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var (
				result CommandResult
				err    error
			)
			require.NotPanics(t, func() {
				result, err = tt.mw(handler)(context.Background(), middlewareTestCommand{Value: redactionSecret})
			})
			require.Error(t, err)
			assert.True(t, result.IsError())
			var panicErr *PanicError
			require.ErrorAs(t, err, &panicErr)
			assert.ErrorIs(t, err, ErrHandlerPanicked)
			assert.Contains(t, panicErr.Error(), "redacted panic value of type *runtime.TypeAssertionError (nil)")
			assert.NotContains(t, panicErr.Error(), redactionSecret)
			assert.NotEmpty(t, panicErr.Stack)
		})
	}
}

func TestRecoveryMiddleware_WithPanicValueCapture_KeepsRawValue(t *testing.T) {
	cmd := recoverySensitiveCommand{OrderID: "order-1", CardNumber: redactionSecret}

	result, err := RecoveryMiddlewareWithOptions(WithPanicValueCapture())(panicWithCommandHandler)(context.Background(), cmd)
	require.Error(t, err)
	assert.True(t, result.IsError())
	var panicErr *PanicError
	require.ErrorAs(t, err, &panicErr)

	assert.Equal(t, cmd, panicErr.Value, "the raw recovered value is kept in Value")
	assert.Contains(t, panicErr.Error(), "redacted panic value of type mink.recoverySensitiveCommand",
		"Error() still prints no more than the summary")
	assert.NotContains(t, panicErr.Error(), redactionSecret)
	assert.NotContains(t, result.Error.Error(), redactionSecret)
	assert.Equal(t, `{"commandType":"RecoverySensitiveCommand","aggregateId":"order-1"}`, panicErr.CommandData,
		"capturing the value does not imply capturing the command")
}

func TestRecoveryMiddleware_WithPanicValueCapture_StringPanic(t *testing.T) {
	raw := "cannot charge card " + redactionSecret
	handler := func(ctx context.Context, cmd Command) (CommandResult, error) { panic(raw) }

	_, err := RecoveryMiddlewareWithOptions(WithPanicValueCapture())(handler)(context.Background(), middlewareTestCommand{})
	var panicErr *PanicError
	require.ErrorAs(t, err, &panicErr)

	assert.Equal(t, raw, panicErr.Value)
	assert.Contains(t, err.Error(), "redacted panic value of type string",
		"a captured raw string is not printed by Error()")
	assert.NotContains(t, err.Error(), redactionSecret)
}

func TestRecoveryMiddleware_WithPanicValueCapture_RuntimeError(t *testing.T) {
	handler := func(ctx context.Context, cmd Command) (CommandResult, error) {
		var o *redactionOrder
		return NewSuccessResult(o.ID, 0), nil
	}

	_, err := RecoveryMiddlewareWithOptions(WithPanicValueCapture())(handler)(context.Background(), middlewareTestCommand{})
	var panicErr *PanicError
	require.ErrorAs(t, err, &panicErr)

	re, ok := panicErr.Value.(error)
	require.True(t, ok, "the raw runtime.Error is kept, got %T", panicErr.Value)
	assert.Contains(t, re.Error(), "nil pointer dereference")
	assert.Contains(t, err.Error(), "nil pointer dereference")
}

func TestRecoveryMiddleware_WithPanicValueCapture_AndCommandCapture(t *testing.T) {
	cmd := recoverySensitiveCommand{OrderID: "order-1", CardNumber: redactionSecret}

	panicErr := recoveredPanicValue(t, RecoveryMiddlewareWithOptions(WithPanicValueCapture(), WithPanicCommandCapture()), cmd)

	assert.Equal(t, cmd, panicErr.Value)
	assert.Contains(t, panicErr.CommandData, `"cardNumber":"`+redactionSecret+`"`)
	assert.NotContains(t, panicErr.Error(), redactionSecret, "Error() prints neither the value nor the command")
}

// recoveredPanicValue runs mw around a handler that panics with the command itself.
func recoveredPanicValue(t *testing.T, mw Middleware, cmd Command) *PanicError {
	t.Helper()
	_, err := mw(panicWithCommandHandler)(context.Background(), cmd)
	require.Error(t, err)
	var panicErr *PanicError
	require.ErrorAs(t, err, &panicErr)
	return panicErr
}

func TestNewRecoveredPanicError(t *testing.T) {
	cmd := recoverySensitiveCommand{OrderID: "order-1", CardNumber: redactionSecret}
	raw := &redactionOrder{ID: "o-1", Card: redactionSecret}

	tests := []struct {
		name        string
		cfg         recoveryConfig
		wantValue   interface{}
		wantSummary string
	}{
		{
			name:        "default stores the summary and no separate summary",
			wantValue:   "redacted panic value of type *mink.redactionOrder",
			wantSummary: "",
		},
		{
			name:        "capture stores the raw value and retains the summary",
			cfg:         recoveryConfig{captureValue: true},
			wantValue:   raw,
			wantSummary: "redacted panic value of type *mink.redactionOrder",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			panicErr := newRecoveredPanicError(cmd, raw, "stack", tt.cfg)
			assert.Equal(t, tt.wantValue, panicErr.Value)
			assert.Equal(t, tt.wantSummary, panicErr.summary)
			assert.Equal(t, "stack", panicErr.Stack)
			assert.Equal(t, "RecoverySensitiveCommand", panicErr.CommandType)
			assert.NotContains(t, panicErr.Error(), redactionSecret)
		})
	}
}

// =============================================================================
// Projection engine: inline, async and live recovery sites
// =============================================================================

// payloadPanickingInlineProjection panics with a value derived from the event.
type payloadPanickingInlineProjection struct {
	ProjectionBase
	panicWith func(StoredEvent) interface{}
}

func (p *payloadPanickingInlineProjection) Apply(_ context.Context, event StoredEvent) error {
	panic(p.panicWith(event))
}

func TestProjectionEngine_applyInline_PanicValueRedacted(t *testing.T) {
	event := StoredEvent{
		ID: "e1", StreamID: "Order-redact", Type: "ProjectionTestEvent",
		Data: []byte(`{"orderId":"` + redactionSecret + `"}`), Version: 1, GlobalPosition: 9,
	}

	tests := []struct {
		name      string
		panicWith func(StoredEvent) interface{}
		wantInErr string
	}{
		{
			name:      "panics with the event data as a string",
			panicWith: func(e StoredEvent) interface{} { return "bad event: " + string(e.Data) },
			wantInErr: "redacted panic value of type string",
		},
		{
			name:      "panics with the event itself",
			panicWith: func(e StoredEvent) interface{} { return e },
			wantInErr: "redacted panic value of type mink.StoredEvent",
		},
		{
			name:      "panics with a decoded payload struct",
			panicWith: func(e StoredEvent) interface{} { return &redactionOrder{ID: redactionSecret} },
			wantInErr: "redacted panic value of type *mink.redactionOrder",
		},
		{
			name: "runtime error keeps its message",
			panicWith: func(e StoredEvent) interface{} {
				var o *redactionOrder
				return o.Card
			},
			wantInErr: "invalid memory address or nil pointer dereference",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine, _, _ := newTestEngineWithStore()
			logger := &panicArgsLogger{}
			engine.logger = logger

			projection := &payloadPanickingInlineProjection{
				ProjectionBase: NewProjectionBase("InlineRedact", "ProjectionTestEvent"),
				panicWith:      tt.panicWith,
			}

			var err error
			require.NotPanics(t, func() { err = engine.applyInline(context.Background(), projection, event) })
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrProjectionFailed)
			assert.Contains(t, err.Error(), tt.wantInErr)
			assert.Contains(t, err.Error(), "position 9")
			assert.NotContains(t, err.Error(), redactionSecret, "the payload must not reach the error")

			logs := logger.all()
			assert.True(t, logger.has("Inline projection panicked"), logs)
			assert.Contains(t, logs, tt.wantInErr)
			assert.NotContains(t, logs, redactionSecret, "the payload must not reach the log")
		})
	}
}

// payloadPanickingAsyncProjection panics with the event's data as a string.
type payloadPanickingAsyncProjection struct {
	AsyncProjectionBase
}

func (p *payloadPanickingAsyncProjection) Apply(_ context.Context, event StoredEvent) error {
	panic("bad event: " + string(event.Data))
}

func TestProjectionEngine_AsyncWorker_PanicValueRedacted(t *testing.T) {
	engine, store, _ := newTestEngineWithStore()
	logger := &panicArgsLogger{}
	engine.logger = logger

	require.NoError(t, store.Append(context.Background(), "Order-redact-async",
		[]interface{}{&ProjectionTestEvent{OrderID: redactionSecret}}))

	projection := &payloadPanickingAsyncProjection{
		AsyncProjectionBase: NewAsyncProjectionBase("AsyncRedact", "ProjectionTestEvent"),
	}
	opts := fastAsyncOpts()
	opts.RetryPolicy = ExponentialBackoffRetry(2, 10*time.Millisecond, 20*time.Millisecond)
	require.NoError(t, engine.RegisterAsync(projection, opts))

	ctx, cancel := context.WithCancel(context.Background())
	require.NoError(t, engine.Start(ctx))

	require.Eventually(t, func() bool { return logger.has("Async projection panicked") },
		2*time.Second, 10*time.Millisecond, "expected the panic to be logged")

	status, err := engine.GetStatus("AsyncRedact")
	require.NoError(t, err)

	cancel()
	require.NoError(t, engine.Stop(context.Background()))

	logs := logger.all()
	assert.Contains(t, logs, "redacted panic value of type string")
	assert.NotContains(t, logs, redactionSecret, "the event payload must not reach the log")
	assert.NotContains(t, logs, "bad event:", "the formatted panic message must not reach the log")

	if status.Error != "" {
		assert.Contains(t, status.Error, "redacted panic value of type string")
		assert.NotContains(t, status.Error, redactionSecret, "the event payload must not reach the projection status")
	}
}

// payloadPanickingLiveProjection panics with the event's data as a string.
type payloadPanickingLiveProjection struct {
	LiveProjectionBase
}

func (p *payloadPanickingLiveProjection) OnEvent(_ context.Context, event StoredEvent) {
	panic("bad event: " + string(event.Data))
}

func TestProjectionEngine_LiveWorker_PanicValueRedacted(t *testing.T) {
	engine, _, _ := newTestEngineWithStore()
	logger := &panicArgsLogger{}
	engine.logger = logger

	projection := &payloadPanickingLiveProjection{
		LiveProjectionBase: NewLiveProjectionBase("LiveRedact", true, "ProjectionTestEvent"),
	}
	require.NoError(t, engine.RegisterLive(projection))

	ctx, cancel := context.WithCancel(context.Background())
	require.NoError(t, engine.Start(ctx))

	// A live projection has no catch-up: events notified before the worker is
	// Running are dropped, so wait for it before delivering the poison event.
	require.Eventually(t, func() bool {
		status, err := engine.GetStatus("LiveRedact")
		return err == nil && status.State == ProjectionStateRunning
	}, 2*time.Second, 5*time.Millisecond, "live worker did not reach Running")

	events := []StoredEvent{{ID: "1", Type: "ProjectionTestEvent", Data: []byte(`{"orderId":"` + redactionSecret + `"}`)}}
	engine.NotifyLiveProjections(ctx, events)

	require.Eventually(t, func() bool { return logger.has("Live projection panicked") },
		2*time.Second, 10*time.Millisecond, "expected the panic to be logged")

	cancel()
	require.NoError(t, engine.Stop(context.Background()))

	logs := logger.all()
	assert.Contains(t, logs, "redacted panic value of type string")
	assert.NotContains(t, logs, redactionSecret, "the event payload must not reach the log")
	assert.NotContains(t, logs, "bad event:")
}

func TestProjectionEngine_deliverLiveEvent_RuntimeErrorKeepsMessage(t *testing.T) {
	engine, _, _ := newTestEngineWithStore()
	logger := &panicArgsLogger{}
	engine.logger = logger

	projection := &nilDerefLiveProjection{LiveProjectionBase: NewLiveProjectionBase("LiveNilDeref", true, "ProjectionTestEvent")}
	worker := &liveProjectionWorker{projection: projection}

	require.NotPanics(t, func() {
		engine.deliverLiveEvent(context.Background(), worker, StoredEvent{Type: "ProjectionTestEvent", Data: []byte(redactionSecret)})
	})
	logs := logger.all()
	assert.Contains(t, logs, "Live projection panicked")
	assert.Contains(t, logs, "invalid memory address or nil pointer dereference")
	assert.NotContains(t, logs, redactionSecret)
}

type nilDerefLiveProjection struct {
	LiveProjectionBase
}

func (p *nilDerefLiveProjection) OnEvent(_ context.Context, _ StoredEvent) {
	var o *redactionOrder
	_ = o.Card
}

// =============================================================================
// Upcaster chain
// =============================================================================

func TestSafeUpcast_PanicValueRedacted(t *testing.T) {
	data := []byte(`{"id":"` + redactionSecret + `"}`)

	tests := []struct {
		name      string
		upcast    func([]byte, Metadata) ([]byte, error)
		wantInErr string
	}{
		{
			name:      "panics with the payload as a string",
			upcast:    func(d []byte, _ Metadata) ([]byte, error) { panic("cannot upcast " + string(d)) },
			wantInErr: "redacted panic value of type string",
		},
		{
			name:      "panics with the raw bytes",
			upcast:    func(d []byte, _ Metadata) ([]byte, error) { panic(d) },
			wantInErr: "redacted panic value of type []uint8",
		},
		{
			name: "panics with an error wrapping the payload",
			upcast: func(d []byte, _ Metadata) ([]byte, error) {
				panic(fmt.Errorf("bad payload %s: %w", d, assert.AnError))
			},
			wantInErr: "redacted panic value of type *fmt.wrapError",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := newTestUpcaster("OrderCreated", 1, 2, tt.upcast)

			var out []byte
			var err error
			require.NotPanics(t, func() { out, err = safeUpcast(u, data, Metadata{}) })
			require.Error(t, err)
			assert.Nil(t, out)
			assert.Contains(t, err.Error(), `mink: upcaster for "OrderCreated" (version 1 -> 2) panicked: `)
			assert.Contains(t, err.Error(), tt.wantInErr)
			assert.NotContains(t, err.Error(), redactionSecret, "the event payload must not reach the error")
			assert.NotContains(t, err.Error(), "cannot upcast")
			assert.NotContains(t, err.Error(), "bad payload")
		})
	}
}

func TestSafeUpcast_RuntimeErrorKeepsMessage(t *testing.T) {
	u := newTestUpcaster("OrderCreated", 1, 2, func(d []byte, _ Metadata) ([]byte, error) {
		i := len(d) + 1
		return d[i:], nil // slice bounds out of range
	})

	_, err := safeUpcast(u, []byte(`{"id":"`+redactionSecret+`"}`), Metadata{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "slice bounds out of range")
	assert.Contains(t, err.Error(), "runtime.")
	assert.NotContains(t, err.Error(), redactionSecret)
}

func TestUpcasterChain_Upcast_PanicValueRedacted(t *testing.T) {
	chain := NewUpcasterChain()
	require.NoError(t, chain.Register(newTestUpcaster("OrderCreated", 1, 2, func(d []byte, _ Metadata) ([]byte, error) {
		panic("cannot upcast " + string(d))
	})))

	_, _, err := chain.Upcast("OrderCreated", 1, []byte(`{"id":"`+redactionSecret+`"}`), Metadata{})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUpcastFailed)
	var ue *UpcastError
	require.ErrorAs(t, err, &ue)
	assert.Equal(t, "OrderCreated", ue.EventType)
	assert.Contains(t, err.Error(), "redacted panic value of type string")
	assert.NotContains(t, err.Error(), redactionSecret, "the event payload must not reach the UpcastError")
	assert.NotContains(t, err.Error(), "cannot upcast")
}
