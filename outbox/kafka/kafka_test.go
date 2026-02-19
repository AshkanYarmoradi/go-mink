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
	defer reader.Close()

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
		defer reader.Close()
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
