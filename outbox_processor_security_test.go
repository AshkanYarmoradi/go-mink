package mink

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

func TestDestinationDigest(t *testing.T) {
	const dest = "unknown:https://alice:hunter2@hooks.example.com/hook?token=s3cret"
	sum := sha256.Sum256([]byte(dest))
	got := destinationDigest(dest)
	assert.Equal(t, hex.EncodeToString(sum[:])[:12], got, "first 12 hex digits of sha256(destination)")
	assert.Len(t, got, destinationDigestHexLen)
	assert.Regexp(t, `^[0-9a-f]{12}$`, got)
	assert.Equal(t, got, destinationDigest(dest), "deterministic, so an operator can match it to the stored row")
	assert.NotEqual(t, destinationDigest("kafka:orders"), destinationDigest("kafka:order"), "distinct inputs are told apart")
	assert.Regexp(t, `^[0-9a-f]{12}$`, destinationDigest(""), "an empty destination has a digest too")
	for _, part := range []string{"unknown", "alice", "hunter2", "hooks", "s3cret", "://"} {
		assert.NotContains(t, got, part)
	}
}

// capturingMarkStore records the error the processor persists through MarkFailed,
// keyed by message id, so tests can assert on what lands in last_error.
type capturingMarkStore struct {
	*memory.OutboxStore
	mu     sync.Mutex
	failed map[string]error
}

func (s *capturingMarkStore) MarkFailed(ctx context.Context, id string, lastErr error) error {
	s.mu.Lock()
	if s.failed == nil {
		s.failed = make(map[string]error)
	}
	s.failed[id] = lastErr
	s.mu.Unlock()
	return s.OutboxStore.MarkFailed(ctx, id, lastErr)
}

func (s *capturingMarkStore) persisted(id string) (error, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	err, ok := s.failed[id]
	return err, ok
}

// TestOutboxProcessor_ProcessBatch_UnroutableDestination_ReportsOnlyDigest pins
// the contract for a destination with NO registered publisher: nothing is known
// about its format, so a poisoned row may carry a credential in its prefix or as
// its bare value — exactly what redactDestination (URL-only) would echo. The log
// line and the persisted ErrPublisherNotFound therefore carry only a one-way
// digest and the length, never the prefix or the value.
func TestOutboxProcessor_ProcessBatch_UnroutableDestination_ReportsOnlyDigest(t *testing.T) {
	ctx := context.Background()
	store := &capturingMarkStore{OutboxStore: memory.NewOutboxStore()}

	// secrets are fragments that must appear nowhere in a log line or last_error.
	secrets := []string{"AKIAIOSFODNN7EXAMPLE", "xoxb-", "bare-secret", "alice", "hunter2", "s3cret", "hooks.example.com", "123456789012", "topic-with-secret", "://"}
	dests := []string{
		"AKIAIOSFODNN7EXAMPLE:https://alice:hunter2@hooks.example.com/hook?token=s3cret", // credential as the prefix
		"xoxb-bare-secret-token-value",                                      // bare credential, no separator
		"unknown:https://alice:hunter2@hooks.example.com/hook?token=s3cret", // URL with credentials
		"sns:arn:aws:sns:us-east-1:123456789012:topic-with-secret",          // non-URL, redactDestination would pass it through
	}
	msgs := make([]*adapters.OutboxMessage, 0, len(dests))
	for _, dest := range dests {
		msgs = append(msgs, &adapters.OutboxMessage{
			AggregateID: "order-1", EventType: "OrderCreated", Destination: dest, Payload: []byte(`{"id":"1"}`),
		})
	}
	require.NoError(t, store.Schedule(ctx, msgs))

	logger := &argsRecordingLogger{}
	// Only "webhook" is registered; none of the stored prefixes match it.
	processor := NewOutboxProcessor(store, WithPublisher(newMockPublisher("webhook")), WithProcessorLogger(logger))
	require.NoError(t, processor.processBatch(ctx))
	assert.Equal(t, len(dests), store.CountByStatus()[adapters.OutboxFailed], "every unroutable message is marked failed")

	logger.mu.Lock()
	defer logger.mu.Unlock()
	entriesByID := make(map[string]loggedEntry)
	for _, e := range logger.entries {
		if e.msg != "No publisher for destination" {
			continue
		}
		id, ok := e.argValue("id")
		require.True(t, ok, "the log line identifies the row by message id")
		entriesByID[id.(string)] = e
	}
	require.Len(t, entriesByID, len(dests), "one log line per unroutable message")

	for _, msg := range msgs {
		dest := msg.Destination
		digest := destinationDigest(dest)
		entry, ok := entriesByID[msg.ID]
		require.True(t, ok, "message %q was logged", msg.ID)

		// Log line: fixed message, digest, length — no destination, no prefix.
		gotDigest, ok := entry.argValue("destination_digest")
		require.True(t, ok)
		assert.Equal(t, digest, gotDigest)
		gotLen, ok := entry.argValue("destination_length")
		require.True(t, ok)
		assert.Equal(t, len(dest), gotLen)
		_, hasDest := entry.argValue("destination")
		assert.False(t, hasDest, "the destination (even redacted) is not logged for an unroutable row")
		_, hasPrefix := entry.argValue("prefix")
		assert.False(t, hasPrefix, "the prefix is not logged for an unroutable row")
		rendered := entry.rendered()
		assert.NotContains(t, rendered, dest)
		assert.NotContains(t, rendered, destinationPrefix(dest))
		for _, s := range secrets {
			assert.NotContains(t, rendered, s, "log line for %q", dest)
		}

		// Persisted last_error: the sentinel plus the digest, nothing else.
		persisted, ok := store.persisted(msg.ID)
		require.True(t, ok, "MarkFailed was called for %q", msg.ID)
		assert.ErrorIs(t, persisted, ErrPublisherNotFound)
		assert.Equal(t, "mink: publisher not found for destination: destination digest "+digest, persisted.Error())
		assert.NotContains(t, persisted.Error(), destinationPrefix(dest))
		for _, s := range secrets {
			assert.NotContains(t, persisted.Error(), s, "last_error for %q", dest)
		}
	}
}

// TestOutboxProcessor_Metrics_ReceiveOnlyPrefix pins the contract the metrics
// layer relies on: the processor hands OutboxMetrics the publisher prefix, never
// the full destination (which may carry URL credentials and is unbounded), and
// a prefix with no registered publisher is collapsed onto the fixed
// UnregisteredDestinationLabel.
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
	assert.ElementsMatch(t, []string{"failing", UnregisteredDestinationLabel}, metrics.failedCalls)
	for _, label := range append(append([]string{}, metrics.processedCalls...), metrics.failedCalls...) {
		assert.NotContains(t, label, "://", "a full destination must never reach the metrics layer: %q", label)
	}
}

// TestOutboxProcessor_Metrics_UnregisteredPrefixUsesFixedLabel pins the
// cardinality bound the metrics package documents: a stored destination whose
// prefix has no registered publisher is tenant- or attacker-controlled, so its
// failure is recorded under the fixed UnregisteredDestinationLabel — never under
// the prefix itself — while the log line identifies the row by digest for diagnosis.
func TestOutboxProcessor_Metrics_UnregisteredPrefixUsesFixedLabel(t *testing.T) {
	store := memory.NewOutboxStore()
	ctx := context.Background()

	hostile := []string{
		"tenant-123:https://alice:hunter2@evil.example.com/x?k=v",
		"tenant-456:https://evil.example.com/y",
		"'; DROP TABLE outbox; --:kafka",
		"unknown-prefix-without-separator",
	}
	for _, dest := range hostile {
		require.NoError(t, store.Schedule(ctx, []*adapters.OutboxMessage{{
			AggregateID: "order-1", EventType: "OrderCreated", Destination: dest, Payload: []byte(`{}`),
		}}))
	}

	metrics := &mockOutboxMetrics{}
	logger := &argsRecordingLogger{}
	// Only "webhook" is registered; none of the stored prefixes match it.
	processor := NewOutboxProcessor(store,
		WithPublisher(newMockPublisher("webhook")),
		WithOutboxMetrics(metrics),
		WithProcessorLogger(logger))
	require.NoError(t, processor.processBatch(ctx))

	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	require.Len(t, metrics.failedCalls, len(hostile), "every unroutable message is reported failed once")
	for _, label := range metrics.failedCalls {
		assert.Equal(t, UnregisteredDestinationLabel, label)
	}
	for _, dest := range hostile {
		assert.NotContains(t, metrics.failedCalls, destinationPrefix(dest), "the stored prefix must never become a label value")
		assert.NotContains(t, metrics.failedCalls, dest, "the stored destination must never become a label value")
	}
	assert.Empty(t, metrics.processedCalls, "an unroutable message is never reported as processed")
	assert.Equal(t, len(hostile), store.CountByStatus()[adapters.OutboxFailed])

	// The log line identifies each row by its destination digest — never by the
	// stored prefix or value, which are hostile data of unknown shape.
	logger.mu.Lock()
	defer logger.mu.Unlock()
	var loggedDigests []string
	for _, e := range logger.entries {
		if e.msg != "No publisher for destination" {
			continue
		}
		digest, ok := e.argValue("destination_digest")
		require.True(t, ok, "the no-publisher log line must carry the destination digest")
		loggedDigests = append(loggedDigests, digest.(string))
		_, hasPrefix := e.argValue("prefix")
		assert.False(t, hasPrefix, "the no-publisher log line must not name the prefix")
		rendered := e.rendered()
		assert.NotContains(t, rendered, "hunter2", "the log line must not leak URL credentials")
		for _, dest := range hostile {
			assert.NotContains(t, rendered, dest)
			assert.NotContains(t, rendered, destinationPrefix(dest))
		}
	}
	wantDigests := make([]string, 0, len(hostile))
	for _, dest := range hostile {
		wantDigests = append(wantDigests, destinationDigest(dest))
	}
	assert.ElementsMatch(t, wantDigests, loggedDigests)
}

// TestUnregisteredDestinationLabel_IsAPlainLabelValue guards the constant the
// metrics package references: a short, fixed, credential-free identifier that
// cannot be confused with a real "<prefix>:<url>" destination.
func TestUnregisteredDestinationLabel_IsAPlainLabelValue(t *testing.T) {
	assert.Equal(t, "unregistered", UnregisteredDestinationLabel)
	assert.Regexp(t, `^[a-z_]+$`, UnregisteredDestinationLabel)
	assert.NotContains(t, UnregisteredDestinationLabel, ":")
}
