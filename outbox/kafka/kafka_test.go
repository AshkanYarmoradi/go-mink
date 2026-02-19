package kafka

import (
	"context"
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	"github.com/AshkanYarmoradi/go-mink/adapters"
	kafkago "github.com/segmentio/kafka-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
// Integration tests (require Kafka)
// =============================================================================

func kafkaBrokers(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("Skipping integration test (short mode)")
	}
	brokers := os.Getenv("TEST_KAFKA_BROKERS")
	if brokers == "" {
		t.Skip("TEST_KAFKA_BROKERS not set")
	}
	return brokers
}

func uniqueTopic(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("test-%s-%d", t.Name(), time.Now().UnixNano())
}

// createTopic pre-creates a Kafka topic and waits until it's available.
func createTopic(t *testing.T, brokers string, topic string) {
	t.Helper()
	conn, err := kafkago.Dial("tcp", brokers)
	require.NoError(t, err)
	defer conn.Close()

	controller, err := conn.Controller()
	require.NoError(t, err)

	controllerConn, err := kafkago.Dial("tcp", net.JoinHostPort(controller.Host, fmt.Sprintf("%d", controller.Port)))
	require.NoError(t, err)
	defer controllerConn.Close()

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

// newTestPublisher creates a Publisher with a dedicated Transport to avoid
// shared metadata cache issues between tests.
func newTestPublisher(t *testing.T, brokers string) *Publisher {
	t.Helper()
	p := New(WithBrokers(brokers), WithBatchTimeout(10*time.Millisecond))
	p.transport = &kafkago.Transport{}
	return p
}

func TestKafkaPublisher_Publish_Integration(t *testing.T) {
	brokers := kafkaBrokers(t)
	topic := uniqueTopic(t)
	createTopic(t, brokers, topic)

	p := newTestPublisher(t, brokers)
	ctx := context.Background()

	messages := []*adapters.OutboxMessage{
		{
			ID:          "msg-1",
			AggregateID: "order-123",
			EventType:   "OrderCreated",
			Destination: "kafka:" + topic,
			Payload:     []byte(`{"id":"123"}`),
		},
	}

	err := p.Publish(ctx, messages)
	require.NoError(t, err)

	// Read back the message
	reader := kafkago.NewReader(kafkago.ReaderConfig{
		Brokers:   []string{brokers},
		Topic:     topic,
		Partition: 0,
		MinBytes:  1,
		MaxBytes:  10e6,
		MaxWait:   5 * time.Second,
	})
	defer reader.Close()

	readCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	msg, err := reader.ReadMessage(readCtx)
	require.NoError(t, err)
	assert.Equal(t, []byte("order-123"), msg.Key)
	assert.Equal(t, []byte(`{"id":"123"}`), msg.Value)

	require.NoError(t, p.Close())
}

func TestKafkaPublisher_Publish_WithHeaders_Integration(t *testing.T) {
	brokers := kafkaBrokers(t)
	topic := uniqueTopic(t)
	createTopic(t, brokers, topic)

	p := newTestPublisher(t, brokers)
	ctx := context.Background()

	messages := []*adapters.OutboxMessage{
		{
			ID:          "msg-1",
			AggregateID: "order-456",
			Destination: "kafka:" + topic,
			Payload:     []byte(`{"id":"456"}`),
			Headers: map[string]string{
				"correlation-id": "corr-abc",
				"event-type":     "OrderShipped",
			},
		},
	}

	err := p.Publish(ctx, messages)
	require.NoError(t, err)

	reader := kafkago.NewReader(kafkago.ReaderConfig{
		Brokers:   []string{brokers},
		Topic:     topic,
		Partition: 0,
		MinBytes:  1,
		MaxBytes:  10e6,
		MaxWait:   5 * time.Second,
	})
	defer reader.Close()

	readCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	msg, err := reader.ReadMessage(readCtx)
	require.NoError(t, err)

	headerMap := make(map[string]string)
	for _, h := range msg.Headers {
		headerMap[h.Key] = string(h.Value)
	}
	assert.Equal(t, "corr-abc", headerMap["correlation-id"])
	assert.Equal(t, "OrderShipped", headerMap["event-type"])

	require.NoError(t, p.Close())
}

func TestKafkaPublisher_Publish_MultipleTopics_Integration(t *testing.T) {
	brokers := kafkaBrokers(t)
	topic1 := uniqueTopic(t) + "-a"
	topic2 := uniqueTopic(t) + "-b"
	createTopic(t, brokers, topic1)
	createTopic(t, brokers, topic2)

	p := newTestPublisher(t, brokers)
	ctx := context.Background()

	messages := []*adapters.OutboxMessage{
		{
			ID:          "msg-1",
			AggregateID: "order-1",
			Destination: "kafka:" + topic1,
			Payload:     []byte(`{"topic":"1"}`),
		},
		{
			ID:          "msg-2",
			AggregateID: "order-2",
			Destination: "kafka:" + topic2,
			Payload:     []byte(`{"topic":"2"}`),
		},
	}

	err := p.Publish(ctx, messages)
	require.NoError(t, err)

	// Verify topic 1
	reader1 := kafkago.NewReader(kafkago.ReaderConfig{
		Brokers: []string{brokers}, Topic: topic1, Partition: 0,
		MinBytes: 1, MaxBytes: 10e6, MaxWait: 5 * time.Second,
	})
	defer reader1.Close()

	readCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	msg1, err := reader1.ReadMessage(readCtx)
	require.NoError(t, err)
	assert.Equal(t, []byte(`{"topic":"1"}`), msg1.Value)

	// Verify topic 2
	reader2 := kafkago.NewReader(kafkago.ReaderConfig{
		Brokers: []string{brokers}, Topic: topic2, Partition: 0,
		MinBytes: 1, MaxBytes: 10e6, MaxWait: 5 * time.Second,
	})
	defer reader2.Close()

	msg2, err := reader2.ReadMessage(readCtx)
	require.NoError(t, err)
	assert.Equal(t, []byte(`{"topic":"2"}`), msg2.Value)

	require.NoError(t, p.Close())
}

func TestKafkaPublisher_Close_Integration(t *testing.T) {
	brokers := kafkaBrokers(t)
	topic := uniqueTopic(t)
	createTopic(t, brokers, topic)

	p := newTestPublisher(t, brokers)
	ctx := context.Background()

	// Publish a message to force writer creation
	err := p.Publish(ctx, []*adapters.OutboxMessage{
		{
			ID:          "msg-1",
			AggregateID: "order-1",
			Destination: "kafka:" + topic,
			Payload:     []byte(`{}`),
		},
	})
	require.NoError(t, err)

	// Close should succeed
	err = p.Close()
	assert.NoError(t, err)
}

func TestKafkaPublisher_Close_Idempotent_Integration(t *testing.T) {
	brokers := kafkaBrokers(t)
	topic := uniqueTopic(t)
	createTopic(t, brokers, topic)

	p := newTestPublisher(t, brokers)
	ctx := context.Background()

	// Create a writer by publishing
	err := p.Publish(ctx, []*adapters.OutboxMessage{
		{
			ID:          "msg-1",
			AggregateID: "order-1",
			Destination: "kafka:" + topic,
			Payload:     []byte(`{}`),
		},
	})
	require.NoError(t, err)

	// First close
	err = p.Close()
	assert.NoError(t, err)

	// Second close — no writers left, should succeed
	err = p.Close()
	assert.NoError(t, err)
}

func TestKafkaPublisher_GetWriter_Caching_Integration(t *testing.T) {
	brokers := kafkaBrokers(t)

	p := New(WithBrokers(brokers))

	topic := "cache-test-topic"
	writer1 := p.getWriter(topic)
	writer2 := p.getWriter(topic)

	// Same pointer should be returned
	assert.Same(t, writer1, writer2)

	// Different topic should return different writer
	writer3 := p.getWriter("other-topic")
	assert.NotSame(t, writer1, writer3)

	// Clean up
	_ = p.Close()
}
