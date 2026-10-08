package mink

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go-mink.dev/adapters"
	"go-mink.dev/adapters/memory"
)

// argsRecordingLogger records every log call including its key/value args,
// so tests can assert on what the processor actually puts in the log line.
type argsRecordingLogger struct {
	mu      sync.Mutex
	entries []loggedEntry
}

type loggedEntry struct {
	level string
	msg   string
	args  []interface{}
}

func (l *argsRecordingLogger) record(level, msg string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, loggedEntry{level: level, msg: msg, args: args})
}

func (l *argsRecordingLogger) Debug(msg string, args ...interface{}) { l.record("debug", msg, args...) }
func (l *argsRecordingLogger) Info(msg string, args ...interface{})  { l.record("info", msg, args...) }
func (l *argsRecordingLogger) Warn(msg string, args ...interface{})  { l.record("warn", msg, args...) }
func (l *argsRecordingLogger) Error(msg string, args ...interface{}) { l.record("error", msg, args...) }

// find returns the first entry with the given message.
func (l *argsRecordingLogger) find(msg string) (loggedEntry, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range l.entries {
		if e.msg == msg {
			return e, true
		}
	}
	return loggedEntry{}, false
}

// argValue returns the value following key in the entry's args.
func (e loggedEntry) argValue(key string) (interface{}, bool) {
	for i := 0; i+1 < len(e.args); i += 2 {
		if k, ok := e.args[i].(string); ok && k == key {
			return e.args[i+1], true
		}
	}
	return nil, false
}

// rendered flattens the whole entry into one string for leak checks.
func (e loggedEntry) rendered() string {
	parts := []string{e.msg}
	for _, a := range e.args {
		parts = append(parts, fmt.Sprint(a))
	}
	return strings.Join(parts, " ")
}

func TestRedactDestination(t *testing.T) {
	tests := []struct {
		name string
		dest string
		want string
	}{
		{"userinfo query fragment", "webhook:https://user:secret@hooks.example.com/path?token=abc#frag", "webhook:https://hooks.example.com"},
		{"token as username", "webhook:https://tok3n@hooks.example.com/", "webhook:https://hooks.example.com"},
		{"clean url reduced to its origin", "webhook:https://hooks.example.com/path", "webhook:https://hooks.example.com"},
		{"slack-style path secret dropped", "webhook:https://hooks.slack.com/services/T000/B000/s3cret", "webhook:https://hooks.slack.com"},
		{"discord-style path token dropped", "webhook:https://discord.com/api/webhooks/123/t0ken", "webhook:https://discord.com"},
		{"port kept query dropped", "webhook:http://host:8080/p?x=1", "webhook:http://host:8080"},
		{"empty query marker dropped", "webhook:https://h/?", "webhook:https://h"},
		{"no prefix url", "https://user:pw@h/p?q=1", "https://h"},
		{"custom prefix", "my-hook:https://a:b@h/p?q", "my-hook:https://h"},
		{"kafka unchanged", "kafka:orders", "kafka:orders"},
		{"sns arn unchanged", "sns:arn:aws:sns:us-east-1:123456789012:topic", "sns:arn:aws:sns:us-east-1:123456789012:topic"},
		{"empty", "", ""},
		{"bare word", "invalid", "invalid"},
		{"prefix only", "webhook:", "webhook:"},
		{"unparseable url", "webhook:http://[::1/p?secret=1", "webhook:<redacted>"},
		{"missing scheme before separator", "webhook:://user:pw@h/p", "webhook:<redacted>"},
		{"ipv6 host", "webhook:https://u:p@[::1]:8443/p?x=y", "webhook:https://[::1]:8443"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, redactDestination(tt.dest))
		})
	}
}

func TestRedactDestination_NeverLeaksSecrets(t *testing.T) {
	secrets := []string{"hunter2", "alice", "api_key=", "s3cret", "frag", "/x", "T000/B000"}
	dests := []string{
		"webhook:https://alice:hunter2@h.example.com/x?api_key=s3cret#frag",
		"https://alice:hunter2@h.example.com/x?api_key=s3cret#frag",
		"webhook:http://alice:hunter2@[::1/x?api_key=s3cret#frag",   // unparseable
		"webhook:https://hooks.slack.com/services/T000/B000/s3cret", // secret in the path
	}
	for _, d := range dests {
		got := redactDestination(d)
		for _, s := range secrets {
			assert.NotContains(t, got, s, "redactDestination(%q)", d)
		}
	}
}

func TestOutboxProcessor_ProcessBatch_LogsRedactedDestination(t *testing.T) {
	store := memory.NewOutboxStore()
	ctx := context.Background()

	// No publisher is registered for "unknown", so the processor logs the
	// destination on the failure path; it must be the redacted form.
	dest := "unknown:https://alice:hunter2@hooks.example.com/hook?token=s3cret"
	require.NoError(t, store.Schedule(ctx, []*adapters.OutboxMessage{{
		AggregateID: "order-1",
		EventType:   "OrderCreated",
		Destination: dest,
		Payload:     []byte(`{"id":"1"}`),
	}}))

	logger := &argsRecordingLogger{}
	processor := NewOutboxProcessor(store, WithProcessorLogger(logger))
	require.NoError(t, processor.processBatch(ctx))

	entry, ok := logger.find("No publisher for destination")
	require.True(t, ok, "expected the no-publisher error to be logged")

	logged, ok := entry.argValue("destination")
	require.True(t, ok)
	assert.Equal(t, "unknown:https://hooks.example.com", logged)

	prefix, ok := entry.argValue("prefix")
	require.True(t, ok)
	assert.Equal(t, "unknown", prefix)

	for _, leaked := range []string{"alice", "hunter2", "token", "s3cret"} {
		assert.NotContains(t, entry.rendered(), leaked)
	}

	// The message itself is still marked failed exactly as before.
	assert.Equal(t, 1, store.CountByStatus()[adapters.OutboxFailed])
}

// TestOutboxProcessor_Metrics_ReceiveOnlyPrefix pins the contract the metrics
// layer relies on: the processor hands OutboxMetrics the publisher prefix, never
// the full destination (which may carry URL credentials and is unbounded).
func TestOutboxProcessor_Metrics_ReceiveOnlyPrefix(t *testing.T) {
	store := memory.NewOutboxStore()
	ctx := context.Background()

	schedule := func(dest string) {
		require.NoError(t, store.Schedule(ctx, []*adapters.OutboxMessage{{
			AggregateID: "order-1", EventType: "OrderCreated", Destination: dest, Payload: []byte(`{}`),
		}}))
	}
	// Delivered, failed at the publisher, and no publisher at all.
	schedule("webhook:https://alice:hunter2@hooks.example.com/services/T0/B0/s3cret")
	schedule("failing:https://u:p@h.example.com/x?k=v")
	schedule("unknown:https://u:p@nowhere.example.com/path")

	metrics := &mockOutboxMetrics{}
	failing := newMockPublisher("failing")
	failing.publishErr = errors.New("boom")
	processor := NewOutboxProcessor(store,
		WithPublisher(newMockPublisher("webhook")),
		WithPublisher(failing),
		WithOutboxMetrics(metrics),
		WithProcessorLogger(&argsRecordingLogger{}))
	require.NoError(t, processor.processBatch(ctx))

	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	assert.ElementsMatch(t, []string{"webhook", "failing"}, metrics.processedCalls)
	assert.ElementsMatch(t, []string{"failing", "unknown"}, metrics.failedCalls)
	for _, label := range append(append([]string{}, metrics.processedCalls...), metrics.failedCalls...) {
		assert.NotContains(t, label, "://", "a full destination must never reach the metrics layer: %q", label)
	}
}
