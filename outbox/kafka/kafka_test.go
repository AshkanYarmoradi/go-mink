package kafka

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	kafkago "github.com/segmentio/kafka-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go-mink.dev/adapters"
)

func TestPublisher_Destination(t *testing.T) {
	p := New()
	assert.Equal(t, "kafka", p.Destination())
}

func TestExtractTopic(t *testing.T) {
	tests := []struct {
		destination string
		want        string
	}{
		{"kafka:orders", "orders"},
		{"kafka:events.user.created", "events.user.created"},
		{"webhook:https://example.com", ""},
		{"invalid", ""},
		{"kafka:", ""},
	}

	for _, tt := range tests {
		t.Run(tt.destination, func(t *testing.T) {
			got := extractTopic(tt.destination)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestNew_Defaults(t *testing.T) {
	p := New()
	assert.Equal(t, []string{"localhost:9092"}, p.brokers)
	assert.NotNil(t, p.balancer)
}

func TestNew_WithBrokers(t *testing.T) {
	p := New(WithBrokers("broker1:9092", "broker2:9092"))
	assert.Equal(t, []string{"broker1:9092", "broker2:9092"}, p.brokers)
}

func TestNew_WithBatchTimeout(t *testing.T) {
	p := New(WithBatchTimeout(500 * time.Millisecond))
	assert.Equal(t, 500*time.Millisecond, p.batchTimeout)
}

func TestNew_WithBalancer(t *testing.T) {
	balancer := &kafkago.RoundRobin{}
	p := New(WithBalancer(balancer))
	assert.Equal(t, balancer, p.balancer)
}

func TestPublisher_Publish_EmptyTopic(t *testing.T) {
	p := New()
	ctx := context.Background()

	messages := []*adapters.OutboxMessage{
		{
			ID:          "msg-1",
			Destination: "kafka:",
			Payload:     []byte(`{"id":"1"}`),
		},
	}

	err := p.Publish(ctx, messages)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "missing topic")
}

// =============================================================================
// Unit tests with a fake writer (no broker required)
// =============================================================================

// fakeWriter is an in-memory kafkaWriter used to unit-test the publisher
// without a real Kafka broker.
type fakeWriter struct {
	topic     string
	written   [][]kafkago.Message // one entry per WriteMessages call
	writeErr  error               // returned by WriteMessages when set
	closeErr  error               // returned by Close when set
	closed    int                 // number of times Close was called
	writeCtxs []context.Context   // contexts passed to WriteMessages
}

func (f *fakeWriter) WriteMessages(ctx context.Context, msgs ...kafkago.Message) error {
	f.writeCtxs = append(f.writeCtxs, ctx)
	if f.writeErr != nil {
		return f.writeErr
	}
	// Copy to avoid aliasing the caller's slice.
	batch := make([]kafkago.Message, len(msgs))
	copy(batch, msgs)
	f.written = append(f.written, batch)
	return nil
}

func (f *fakeWriter) Close() error {
	f.closed++
	return f.closeErr
}

// withFakeWriters overrides the publisher's writer factory so that getWriter
// returns the supplied per-topic fakes. Topics without an entry get a fresh
// fakeWriter created on demand (recorded in created).
func withFakeWriters(p *Publisher, fakes map[string]*fakeWriter) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.newWriter = func(topic string) kafkaWriter {
		if fakes == nil {
			fakes = make(map[string]*fakeWriter)
		}
		if f, ok := fakes[topic]; ok {
			return f
		}
		f := &fakeWriter{topic: topic}
		fakes[topic] = f
		return f
	}
}

func TestPublisher_Publish_SingleTopic_Unit(t *testing.T) {
	p := New()
	fakes := map[string]*fakeWriter{}
	withFakeWriters(p, fakes)
	ctx := context.Background()

	err := p.Publish(ctx, []*adapters.OutboxMessage{
		{ID: "msg-1", AggregateID: "order-123", Destination: "kafka:orders", Payload: []byte(`{"id":"123"}`)},
	})
	require.NoError(t, err)

	w := fakes["orders"]
	require.NotNil(t, w)
	require.Len(t, w.written, 1)
	require.Len(t, w.written[0], 1)
	assert.Equal(t, []byte("order-123"), w.written[0][0].Key)
	assert.Equal(t, []byte(`{"id":"123"}`), w.written[0][0].Value)
}

func TestPublisher_Publish_HeaderPropagation_Unit(t *testing.T) {
	p := New()
	fakes := map[string]*fakeWriter{}
	withFakeWriters(p, fakes)
	ctx := context.Background()

	err := p.Publish(ctx, []*adapters.OutboxMessage{
		{
			ID: "msg-1", AggregateID: "order-456", Destination: "kafka:orders",
			Payload: []byte(`{"id":"456"}`),
			Headers: map[string]string{
				"correlation-id": "corr-abc",
				"event-type":     "OrderShipped",
			},
		},
	})
	require.NoError(t, err)

	w := fakes["orders"]
	require.NotNil(t, w)
	require.Len(t, w.written, 1)
	headerMap := make(map[string]string)
	for _, h := range w.written[0][0].Headers {
		headerMap[h.Key] = string(h.Value)
	}
	assert.Equal(t, "corr-abc", headerMap["correlation-id"])
	assert.Equal(t, "OrderShipped", headerMap["event-type"])
}

func TestPublisher_Publish_MultiTopicGrouping_Unit(t *testing.T) {
	p := New()
	fakes := map[string]*fakeWriter{}
	withFakeWriters(p, fakes)
	ctx := context.Background()

	err := p.Publish(ctx, []*adapters.OutboxMessage{
		{ID: "m1", AggregateID: "a1", Destination: "kafka:topic-a", Payload: []byte(`{"n":1}`)},
		{ID: "m2", AggregateID: "a2", Destination: "kafka:topic-b", Payload: []byte(`{"n":2}`)},
		{ID: "m3", AggregateID: "a3", Destination: "kafka:topic-a", Payload: []byte(`{"n":3}`)},
	})
	require.NoError(t, err)

	wa := fakes["topic-a"]
	wb := fakes["topic-b"]
	require.NotNil(t, wa)
	require.NotNil(t, wb)

	// topic-a received both of its messages in a single grouped batch.
	require.Len(t, wa.written, 1)
	require.Len(t, wa.written[0], 2)
	assert.Equal(t, []byte(`{"n":1}`), wa.written[0][0].Value)
	assert.Equal(t, []byte(`{"n":3}`), wa.written[0][1].Value)

	// topic-b received its single message.
	require.Len(t, wb.written, 1)
	require.Len(t, wb.written[0], 1)
	assert.Equal(t, []byte(`{"n":2}`), wb.written[0][0].Value)
}

func TestPublisher_Publish_PerTopicErrorAggregation_Unit(t *testing.T) {
	p := New()
	failA := errors.New("boom-a")
	failB := errors.New("boom-b")
	fakes := map[string]*fakeWriter{
		"topic-a":  {topic: "topic-a", writeErr: failA},
		"topic-b":  {topic: "topic-b", writeErr: failB},
		"topic-ok": {topic: "topic-ok"},
	}
	withFakeWriters(p, fakes)
	ctx := context.Background()

	err := p.Publish(ctx, []*adapters.OutboxMessage{
		{ID: "m1", AggregateID: "a1", Destination: "kafka:topic-a", Payload: []byte(`{}`)},
		{ID: "m2", AggregateID: "a2", Destination: "kafka:topic-b", Payload: []byte(`{}`)},
		{ID: "m3", AggregateID: "a3", Destination: "kafka:topic-ok", Payload: []byte(`{}`)},
	})

	require.Error(t, err)
	// errors.Join wraps both failures; both must be discoverable.
	assert.ErrorIs(t, err, failA)
	assert.ErrorIs(t, err, failB)
	assert.Contains(t, err.Error(), "topic-a")
	assert.Contains(t, err.Error(), "topic-b")
	// The healthy topic still got its message.
	require.Len(t, fakes["topic-ok"].written, 1)
}

func TestPublisher_GetWriter_Caching_Unit(t *testing.T) {
	p := New()
	withFakeWriters(p, nil)

	w1 := p.getWriter("cache-topic")
	w2 := p.getWriter("cache-topic")
	assert.Same(t, w1, w2)

	w3 := p.getWriter("other-topic")
	assert.NotSame(t, w1, w3)
}

func TestPublisher_Close_ClosesAllWriters_Unit(t *testing.T) {
	p := New()
	withFakeWriters(p, nil)

	// Materialize three cached writers.
	wa := p.getWriter("topic-a").(*fakeWriter)
	wb := p.getWriter("topic-b").(*fakeWriter)
	wc := p.getWriter("topic-c").(*fakeWriter)

	require.NoError(t, p.Close())
	assert.Equal(t, 1, wa.closed)
	assert.Equal(t, 1, wb.closed)
	assert.Equal(t, 1, wc.closed)

	// Map drained: a second Close is a no-op and writers are not re-closed.
	require.NoError(t, p.Close())
	assert.Equal(t, 1, wa.closed)
	assert.Empty(t, p.writers)
}

func TestPublisher_Close_AllWritersClosedOnError_Unit(t *testing.T) {
	// a failing writer must not leak the remaining ones.
	p := New()
	failA := errors.New("close-fail-a")
	failC := errors.New("close-fail-c")
	fakes := map[string]*fakeWriter{
		"topic-a": {topic: "topic-a", closeErr: failA},
		"topic-b": {topic: "topic-b"},
		"topic-c": {topic: "topic-c", closeErr: failC},
	}
	withFakeWriters(p, fakes)

	// Cache all three writers.
	for topic := range fakes {
		_ = p.getWriter(topic)
	}

	err := p.Close()
	require.Error(t, err)
	// Both close failures are aggregated via errors.Join.
	assert.ErrorIs(t, err, failA)
	assert.ErrorIs(t, err, failC)

	// Every writer was closed exactly once despite the failures...
	assert.Equal(t, 1, fakes["topic-a"].closed)
	assert.Equal(t, 1, fakes["topic-b"].closed)
	assert.Equal(t, 1, fakes["topic-c"].closed)
	// ...and all were removed from the map (none leaked).
	assert.Empty(t, p.writers)
}

// =============================================================================
// Integration test helpers
// =============================================================================

// integrationSetup prepares a Kafka integration test by checking prerequisites,
// creating a unique topic, and returning a Publisher with a dedicated transport.
type integrationEnv struct {
	brokers   string
	topic     string
	publisher *Publisher
	ctx       context.Context
}

func setupIntegration(t *testing.T) *integrationEnv {
	t.Helper()
	if testing.Short() {
		t.Skip("Skipping integration test (short mode)")
	}
	brokers := os.Getenv("TEST_KAFKA_BROKERS")
	if brokers == "" {
		t.Skip("TEST_KAFKA_BROKERS not set")
	}

	topic := fmt.Sprintf("test-%s-%d", t.Name(), time.Now().UnixNano())
	createTopic(t, brokers, topic)

	p := New(WithBrokers(brokers), WithBatchTimeout(10*time.Millisecond))
	p.transport = &kafkago.Transport{}

	return &integrationEnv{
		brokers:   brokers,
		topic:     topic,
		publisher: p,
		ctx:       context.Background(),
	}
}

// publish sends a single outbox message to the env's topic.
func (e *integrationEnv) publish(t *testing.T, msg *adapters.OutboxMessage) {
	t.Helper()
	msg.Destination = "kafka:" + e.topic
	err := e.publisher.Publish(e.ctx, []*adapters.OutboxMessage{msg})
	require.NoError(t, err)
}

// readMessage reads one message from the env's topic.
func (e *integrationEnv) readMessage(t *testing.T) kafkago.Message {
	t.Helper()
	reader := kafkago.NewReader(kafkago.ReaderConfig{
		Brokers:   []string{e.brokers},
		Topic:     e.topic,
		Partition: 0,
		MinBytes:  1,
		MaxBytes:  10e6,
		MaxWait:   5 * time.Second,
	})
	defer func() { _ = reader.Close() }()

	readCtx, cancel := context.WithTimeout(e.ctx, 10*time.Second)
	defer cancel()

	msg, err := reader.ReadMessage(readCtx)
	require.NoError(t, err)
	return msg
}

// createTopic pre-creates a Kafka topic and waits until it's available.
func createTopic(t *testing.T, brokers string, topic string) {
	t.Helper()
	conn, err := kafkago.Dial("tcp", brokers)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	controller, err := conn.Controller()
	require.NoError(t, err)

	controllerConn, err := kafkago.Dial("tcp", net.JoinHostPort(controller.Host, fmt.Sprintf("%d", controller.Port)))
	require.NoError(t, err)
	defer func() { _ = controllerConn.Close() }()

	err = controllerConn.CreateTopics(kafkago.TopicConfig{
		Topic:             topic,
		NumPartitions:     1,
		ReplicationFactor: 1,
	})
	require.NoError(t, err)

	// Wait until the topic is visible in metadata (poll up to 10s)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		partitions, err := conn.ReadPartitions(topic)
		if err == nil && len(partitions) > 0 {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("topic %s not available after 10s", topic)
}

// =============================================================================
// Integration tests
// =============================================================================

func TestKafkaPublisher_Publish_Integration(t *testing.T) {
	env := setupIntegration(t)
	env.publish(t, &adapters.OutboxMessage{
		ID: "msg-1", AggregateID: "order-123", EventType: "OrderCreated",
		Payload: []byte(`{"id":"123"}`),
	})

	msg := env.readMessage(t)
	assert.Equal(t, []byte("order-123"), msg.Key)
	assert.Equal(t, []byte(`{"id":"123"}`), msg.Value)

	require.NoError(t, env.publisher.Close())
}

func TestKafkaPublisher_Publish_WithHeaders_Integration(t *testing.T) {
	env := setupIntegration(t)
	env.publish(t, &adapters.OutboxMessage{
		ID: "msg-1", AggregateID: "order-456",
		Payload: []byte(`{"id":"456"}`),
		Headers: map[string]string{
			"correlation-id": "corr-abc",
			"event-type":     "OrderShipped",
		},
	})

	msg := env.readMessage(t)
	headerMap := make(map[string]string)
	for _, h := range msg.Headers {
		headerMap[h.Key] = string(h.Value)
	}
	assert.Equal(t, "corr-abc", headerMap["correlation-id"])
	assert.Equal(t, "OrderShipped", headerMap["event-type"])

	require.NoError(t, env.publisher.Close())
}

func TestKafkaPublisher_Publish_MultipleTopics_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test (short mode)")
	}
	brokers := os.Getenv("TEST_KAFKA_BROKERS")
	if brokers == "" {
		t.Skip("TEST_KAFKA_BROKERS not set")
	}

	topic1 := fmt.Sprintf("test-%s-%d-a", t.Name(), time.Now().UnixNano())
	topic2 := fmt.Sprintf("test-%s-%d-b", t.Name(), time.Now().UnixNano())
	createTopic(t, brokers, topic1)
	createTopic(t, brokers, topic2)

	p := New(WithBrokers(brokers), WithBatchTimeout(10*time.Millisecond))
	p.transport = &kafkago.Transport{}
	ctx := context.Background()

	err := p.Publish(ctx, []*adapters.OutboxMessage{
		{ID: "msg-1", AggregateID: "order-1", Destination: "kafka:" + topic1, Payload: []byte(`{"topic":"1"}`)},
		{ID: "msg-2", AggregateID: "order-2", Destination: "kafka:" + topic2, Payload: []byte(`{"topic":"2"}`)},
	})
	require.NoError(t, err)

	readFromTopic := func(topic string) kafkago.Message {
		reader := kafkago.NewReader(kafkago.ReaderConfig{
			Brokers: []string{brokers}, Topic: topic, Partition: 0,
			MinBytes: 1, MaxBytes: 10e6, MaxWait: 5 * time.Second,
		})
		defer func() { _ = reader.Close() }()
		readCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		msg, err := reader.ReadMessage(readCtx)
		require.NoError(t, err)
		return msg
	}

	assert.Equal(t, []byte(`{"topic":"1"}`), readFromTopic(topic1).Value)
	assert.Equal(t, []byte(`{"topic":"2"}`), readFromTopic(topic2).Value)

	require.NoError(t, p.Close())
}

func TestKafkaPublisher_Close_Integration(t *testing.T) {
	env := setupIntegration(t)
	env.publish(t, &adapters.OutboxMessage{
		ID: "msg-1", AggregateID: "order-1", Payload: []byte(`{}`),
	})

	assert.NoError(t, env.publisher.Close())
}

func TestKafkaPublisher_Close_Idempotent_Integration(t *testing.T) {
	env := setupIntegration(t)
	env.publish(t, &adapters.OutboxMessage{
		ID: "msg-1", AggregateID: "order-1", Payload: []byte(`{}`),
	})

	assert.NoError(t, env.publisher.Close())
	assert.NoError(t, env.publisher.Close())
}

func TestKafkaPublisher_GetWriter_Caching_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test (short mode)")
	}
	brokers := os.Getenv("TEST_KAFKA_BROKERS")
	if brokers == "" {
		t.Skip("TEST_KAFKA_BROKERS not set")
	}

	p := New(WithBrokers(brokers))

	writer1 := p.getWriter("cache-test-topic")
	writer2 := p.getWriter("cache-test-topic")
	assert.Same(t, writer1, writer2)

	writer3 := p.getWriter("other-topic")
	assert.NotSame(t, writer1, writer3)

	_ = p.Close()
}
