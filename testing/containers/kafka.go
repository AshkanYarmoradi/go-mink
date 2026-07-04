package containers

import (
	"context"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	kafkago "github.com/segmentio/kafka-go"
)

// KafkaContainer is a handle to an already-running Kafka broker for integration tests.
// Like PostgresContainer it does not provision anything — the broker is expected to be
// running out of band (docker-compose.test.yml / a CI service), addressed by the
// TEST_KAFKA_BROKERS environment variable.
type KafkaContainer struct {
	// Brokers is the raw TEST_KAFKA_BROKERS value (may be a comma-separated list).
	Brokers string
}

// StartKafka connects to an already-running Kafka broker addressed by TEST_KAFKA_BROKERS
// and returns a handle for integration tests. It mirrors StartPostgres: it does NOT
// provision a container, and it SKIPS the calling test (never fails it) when the test runs
// under -short, TEST_KAFKA_BROKERS is unset, or the broker is unreachable — so unit-only
// environments stay green and the default build needs no broker.
func StartKafka(t *testing.T) *KafkaContainer {
	t.Helper()

	if testing.Short() {
		t.Skip("Skipping Kafka integration test in short mode")
	}
	brokers := getEnvOrDefault("TEST_KAFKA_BROKERS", "")
	if brokers == "" {
		t.Skip("TEST_KAFKA_BROKERS not set; skipping Kafka integration test")
	}

	k := &KafkaContainer{Brokers: brokers}

	// Verify reachability so an unreachable broker skips rather than fails the test.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := (&kafkago.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "tcp", k.firstBroker())
	if err != nil {
		t.Skipf("Kafka not reachable at %s (run `make infra-up`): %v", k.firstBroker(), err)
	}
	_ = conn.Close()

	return k
}

// BrokerList returns the configured brokers as a slice.
func (k *KafkaContainer) BrokerList() []string {
	parts := strings.Split(k.Brokers, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// firstBroker returns the first broker address (for control-plane dials).
func (k *KafkaContainer) firstBroker() string {
	if list := k.BrokerList(); len(list) > 0 {
		return list[0]
	}
	return "localhost:9092"
}

// CreateTopic creates a single-partition topic and waits until it is visible, then registers
// a cleanup that deletes it. Use a per-test unique topic name to avoid cross-test interference.
func (k *KafkaContainer) CreateTopic(t *testing.T, topic string) {
	t.Helper()

	conn, err := kafkago.Dial("tcp", k.firstBroker())
	if err != nil {
		t.Fatalf("kafka dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	controller, err := conn.Controller()
	if err != nil {
		t.Fatalf("kafka controller: %v", err)
	}
	controllerConn, err := kafkago.Dial("tcp", net.JoinHostPort(controller.Host, strconv.Itoa(controller.Port)))
	if err != nil {
		t.Fatalf("kafka controller dial: %v", err)
	}
	defer func() { _ = controllerConn.Close() }()

	if err := controllerConn.CreateTopics(kafkago.TopicConfig{
		Topic:             topic,
		NumPartitions:     1,
		ReplicationFactor: 1,
	}); err != nil {
		t.Fatalf("kafka create topic %q: %v", topic, err)
	}

	// Poll until the topic's partitions are visible before returning.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		parts, err := conn.ReadPartitions(topic)
		if err == nil && len(parts) > 0 {
			t.Cleanup(func() { _ = k.deleteTopic(topic) })
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("kafka topic %q did not become visible in time", topic)
}

// deleteTopic best-effort removes a topic on cleanup.
func (k *KafkaContainer) deleteTopic(topic string) error {
	conn, err := kafkago.Dial("tcp", k.firstBroker())
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	return conn.DeleteTopics(topic)
}

// NewReader returns a partition-0 reader for the topic (no consumer group), suitable for
// asserting a single delivered message with ReadMessage under a bounded context.
func (k *KafkaContainer) NewReader(topic string) *kafkago.Reader {
	return kafkago.NewReader(kafkago.ReaderConfig{
		Brokers:   k.BrokerList(),
		Topic:     topic,
		Partition: 0,
		MinBytes:  1,
		MaxBytes:  10e6,
		MaxWait:   500 * time.Millisecond,
	})
}
