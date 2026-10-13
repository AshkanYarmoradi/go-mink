// Package kafka provides a Kafka publisher for the outbox pattern.
// It publishes outbox messages to Kafka topics using github.com/segmentio/kafka-go.
package kafka

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	kafkago "github.com/segmentio/kafka-go"
	"github.com/segmentio/kafka-go/sasl"
	"go-mink.dev/adapters"
)

// kafkaWriter is the subset of the kafka-go *Writer API used by the publisher.
// It exists as a seam so Publish/getWriter/Close can be unit-tested with a fake
// writer without a real broker. The real *kafkago.Writer satisfies it.
type kafkaWriter interface {
	WriteMessages(ctx context.Context, msgs ...kafkago.Message) error
	Close() error
}

// writerFactory creates a writer for the given topic.
type writerFactory func(topic string) kafkaWriter

// Publisher publishes outbox messages to Kafka topics.
// Destination format: "kafka:topic-name"
//
// Security default: with no WithTransport / WithTLS / WithSASL option the
// publisher uses the kafka-go default transport, which connects to the
// brokers over PLAINTEXT without authentication. Any network observer can
// read the event payloads and any client can write to the topics. For
// production use configure WithTLS (encryption + broker authentication) and,
// where the cluster requires it, WithSASL (client authentication).
type Publisher struct {
	brokers      []string
	balancer     kafkago.Balancer
	batchTimeout time.Duration
	transport    kafkago.RoundTripper
	mu           sync.RWMutex
	writers      map[string]kafkaWriter
	// newWriter creates a writer for a topic. Defaults to building a real
	// kafka-go writer; tests override it to inject a fake. Unexported so the
	// public API stays backward compatible.
	newWriter writerFactory
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

// WithTransport sets the kafka-go Transport shared by every writer this
// publisher creates — connection pooling, dial/idle timeouts, client id,
// TLS, SASL, and so on. Passing nil restores the kafka-go default transport
// (plaintext, unauthenticated).
//
// It replaces whatever transport was configured so far, including TLS/SASL
// settings applied by an earlier WithTLS/WithSASL. To layer TLS or SASL onto
// a custom transport, apply WithTransport first and WithTLS/WithSASL after it.
func WithTransport(t *kafkago.Transport) Option {
	return func(p *Publisher) {
		if t == nil {
			// Store an untyped nil so kafka-go falls back to its default
			// transport instead of calling methods on a nil *Transport.
			p.transport = nil
			return
		}
		p.transport = t
	}
}

// WithTLS enables TLS for every broker connection using the given
// configuration (server-certificate verification, client certificates for
// mTLS, minimum version, ...). A nil config disables TLS again.
//
// The setting is applied to the publisher's transport, creating a transport
// when none has been configured yet; combine it with WithSASL for an
// encrypted and authenticated connection. Without this option the publisher
// talks PLAINTEXT to the brokers.
func WithTLS(cfg *tls.Config) Option {
	return func(p *Publisher) {
		p.ownedTransport().TLS = cfg
	}
}

// WithSASL enables SASL client authentication for every broker connection
// using the given mechanism — for example plain.Mechanism{...} from
// github.com/segmentio/kafka-go/sasl/plain, or scram.Mechanism(...) from
// .../sasl/scram. A nil mechanism disables SASL again.
//
// SASL/PLAIN sends the credentials in the clear during the handshake, so
// always pair it with WithTLS. The setting is applied to the publisher's
// transport, creating a transport when none has been configured yet. Without
// this option the publisher connects to the brokers unauthenticated.
func WithSASL(m sasl.Mechanism) Option {
	return func(p *Publisher) {
		p.ownedTransport().SASL = m
	}
}

// ownedTransport returns the *kafkago.Transport the publisher's writers use,
// creating one when no concrete transport has been configured yet.
func (p *Publisher) ownedTransport() *kafkago.Transport {
	if t, ok := p.transport.(*kafkago.Transport); ok && t != nil {
		return t
	}
	t := &kafkago.Transport{}
	p.transport = t
	return t
}

// New creates a new Kafka Publisher.
//
// By default it connects to localhost:9092 over plaintext without
// authentication; see WithTLS and WithSASL to secure the connection.
func New(opts ...Option) *Publisher {
	p := &Publisher{
		brokers:      []string{"localhost:9092"},
		balancer:     &kafkago.LeastBytes{},
		batchTimeout: 10 * time.Millisecond,
		writers:      make(map[string]kafkaWriter),
	}

	for _, opt := range opts {
		opt(p)
	}

	// Default factory builds a real kafka-go writer. Set after applying options
	// so it reads the final broker/balancer/transport configuration. Tests
	// override p.newWriter to inject a fake writer.
	if p.newWriter == nil {
		p.newWriter = func(topic string) kafkaWriter {
			return &kafkago.Writer{
				Addr:                   kafkago.TCP(p.brokers...),
				Topic:                  topic,
				Balancer:               p.balancer,
				BatchTimeout:           p.batchTimeout,
				Transport:              p.transport,
				AllowAutoTopicCreation: true,
			}
		}
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

// Close closes all Kafka writers. Every writer is closed regardless of
// individual failures; errors are collected and returned as a joined error so a
// single failing writer cannot leak the remaining ones.
func (p *Publisher) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	var errs []error
	for topic, w := range p.writers {
		if err := w.Close(); err != nil {
			errs = append(errs, fmt.Errorf("kafka: failed to close writer for topic %s: %w", topic, err))
		}
		delete(p.writers, topic)
	}
	return errors.Join(errs...)
}

// getWriter returns or creates a Kafka writer for the given topic.
func (p *Publisher) getWriter(topic string) kafkaWriter {
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

	w := p.newWriter(topic)
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
