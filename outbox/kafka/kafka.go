// Package kafka provides a Kafka publisher for the outbox pattern.
// It publishes outbox messages to Kafka topics using github.com/segmentio/kafka-go.
package kafka

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/AshkanYarmoradi/go-mink/adapters"
	kafkago "github.com/segmentio/kafka-go"
)

// Publisher publishes outbox messages to Kafka topics.
// Destination format: "kafka:topic-name"
type Publisher struct {
	brokers      []string
	balancer     kafkago.Balancer
	batchTimeout time.Duration
	transport    kafkago.RoundTripper
	mu           sync.RWMutex
	writers      map[string]*kafkago.Writer
}

// Option configures a Kafka Publisher.
type Option func(*Publisher)

// WithBrokers sets the Kafka broker addresses.
func WithBrokers(brokers ...string) Option {
	return func(p *Publisher) {
		p.brokers = brokers
	}
}

// WithBalancer sets the message balancer (partitioner).
func WithBalancer(balancer kafkago.Balancer) Option {
	return func(p *Publisher) {
		p.balancer = balancer
	}
}

// WithBatchTimeout sets the batch timeout for the writer.
func WithBatchTimeout(d time.Duration) Option {
	return func(p *Publisher) {
		p.batchTimeout = d
	}
}

// New creates a new Kafka Publisher.
func New(opts ...Option) *Publisher {
	p := &Publisher{
		brokers:      []string{"localhost:9092"},
		balancer:     &kafkago.LeastBytes{},
		batchTimeout: 10 * time.Millisecond,
		writers:      make(map[string]*kafkago.Writer),
	}

	for _, opt := range opts {
		opt(p)
	}

	return p
}

// Destination returns the destination prefix this publisher handles.
func (p *Publisher) Destination() string {
	return "kafka"
}

// Publish writes outbox messages to the Kafka topic specified in the destination.
// All topics are attempted even if some fail; errors are collected and returned as a joined error.
func (p *Publisher) Publish(ctx context.Context, messages []*adapters.OutboxMessage) error {
	// Group by topic
	grouped := make(map[string][]kafkago.Message)
	var errs []error
	for _, msg := range messages {
		topic := extractTopic(msg.Destination)
		if topic == "" {
			errs = append(errs, fmt.Errorf("kafka: invalid destination %q: missing topic", msg.Destination))
			continue
		}

		kafkaMsg := kafkago.Message{
			Key:   []byte(msg.AggregateID),
			Value: msg.Payload,
		}

		// Add headers from outbox message
		for k, v := range msg.Headers {
			kafkaMsg.Headers = append(kafkaMsg.Headers, kafkago.Header{
				Key:   k,
				Value: []byte(v),
			})
		}

		grouped[topic] = append(grouped[topic], kafkaMsg)
	}

	// Write to each topic
	for topic, msgs := range grouped {
		writer := p.getWriter(topic)
		if err := writer.WriteMessages(ctx, msgs...); err != nil {
			errs = append(errs, fmt.Errorf("kafka: failed to write to topic %s: %w", topic, err))
		}
	}

	return errors.Join(errs...)
}

// Close closes all Kafka writers.
func (p *Publisher) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	for topic, w := range p.writers {
		if err := w.Close(); err != nil {
			return err
		}
		delete(p.writers, topic)
	}
	return nil
}

// getWriter returns or creates a Kafka writer for the given topic.
func (p *Publisher) getWriter(topic string) *kafkago.Writer {
	p.mu.RLock()
	if w, ok := p.writers[topic]; ok {
		p.mu.RUnlock()
		return w
	}
	p.mu.RUnlock()

	p.mu.Lock()
	defer p.mu.Unlock()

	// Double-check after acquiring write lock
	if w, ok := p.writers[topic]; ok {
		return w
	}

	w := &kafkago.Writer{
		Addr:                   kafkago.TCP(p.brokers...),
		Topic:                  topic,
		Balancer:               p.balancer,
		BatchTimeout:           p.batchTimeout,
		Transport:              p.transport,
		AllowAutoTopicCreation: true,
	}

	p.writers[topic] = w
	return w
}

// extractTopic removes the "kafka:" prefix from a destination.
func extractTopic(destination string) string {
	const prefix = "kafka:"
	if strings.HasPrefix(destination, prefix) {
		return destination[len(prefix):]
	}
	return ""
}
