package kafka

import (
	"crypto/tls"
	"testing"

	kafkago "github.com/segmentio/kafka-go"
	"github.com/segmentio/kafka-go/sasl/plain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// realWriter materialises the kafka-go writer the default factory builds for
// a topic so tests can inspect the transport it was handed. No broker is
// contacted: the writer only dials on WriteMessages.
func realWriter(t *testing.T, p *Publisher, topic string) *kafkago.Writer {
	t.Helper()
	w, ok := p.newWriter(topic).(*kafkago.Writer)
	require.True(t, ok, "default factory must build a *kafkago.Writer")
	return w
}

func TestNew_Default_IsPlaintextUnauthenticated(t *testing.T) {
	p := New()
	// An untyped nil transport makes kafka-go use its DefaultTransport
	// (plaintext, no SASL) — the documented default.
	assert.True(t, p.transport == nil, "default transport must be untyped nil")
	w := realWriter(t, p, "orders")
	assert.True(t, w.Transport == nil)
}

func TestNew_WithTLS_PopulatesTransport(t *testing.T) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: "kafka.example.com"}
	p := New(WithTLS(cfg))

	tr, ok := p.transport.(*kafkago.Transport)
	require.True(t, ok, "WithTLS must create a *kafkago.Transport when none is configured")
	assert.Same(t, cfg, tr.TLS)
	assert.Nil(t, tr.SASL)

	// The writer built for any topic carries that same transport.
	w := realWriter(t, p, "orders")
	assert.Same(t, tr, w.Transport)
}

func TestNew_WithSASL_PopulatesTransport(t *testing.T) {
	mech := plain.Mechanism{Username: "svc-outbox", Password: "pw"}
	p := New(WithSASL(mech))

	tr, ok := p.transport.(*kafkago.Transport)
	require.True(t, ok)
	assert.Equal(t, mech, tr.SASL)
	assert.Nil(t, tr.TLS)

	w := realWriter(t, p, "orders")
	assert.Same(t, tr, w.Transport)
}

func TestNew_WithTLSAndSASL_ShareOneTransport(t *testing.T) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS13}
	mech := plain.Mechanism{Username: "u", Password: "p"}
	p := New(WithTLS(cfg), WithSASL(mech))

	tr, ok := p.transport.(*kafkago.Transport)
	require.True(t, ok)
	assert.Same(t, cfg, tr.TLS)
	assert.Equal(t, mech, tr.SASL)
}

func TestNew_WithTransport_ThenTLSAndSASL_Layered(t *testing.T) {
	custom := &kafkago.Transport{ClientID: "outbox-publisher"}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	mech := plain.Mechanism{Username: "u", Password: "p"}

	p := New(WithTransport(custom), WithTLS(cfg), WithSASL(mech))

	assert.Same(t, custom, p.transport, "later options must mutate the injected transport, not replace it")
	assert.Same(t, cfg, custom.TLS)
	assert.Equal(t, mech, custom.SASL)
	assert.Equal(t, "outbox-publisher", custom.ClientID)
	assert.Same(t, custom, realWriter(t, p, "t").Transport)
}

func TestNew_WithTransport_ReplacesEarlierTLS(t *testing.T) {
	// Documented ordering: WithTransport replaces whatever came before it.
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	custom := &kafkago.Transport{}
	p := New(WithTLS(cfg), WithTransport(custom))

	assert.Same(t, custom, p.transport)
	assert.Nil(t, custom.TLS)
}

func TestNew_WithTransport_NilRestoresDefault(t *testing.T) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	p := New(WithTLS(cfg), WithTransport(nil))

	// Must be an untyped nil: a typed (*Transport)(nil) stored in the
	// RoundTripper interface would make kafka-go dereference a nil pointer.
	assert.True(t, p.transport == nil)
	assert.True(t, realWriter(t, p, "t").Transport == nil)
}

func TestNew_WithTLS_NilDisables(t *testing.T) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	p := New(WithTLS(cfg), WithTLS(nil))

	tr, ok := p.transport.(*kafkago.Transport)
	require.True(t, ok)
	assert.Nil(t, tr.TLS)
}

func TestNew_WithSASL_NilDisables(t *testing.T) {
	p := New(WithSASL(plain.Mechanism{Username: "u"}), WithSASL(nil))

	tr, ok := p.transport.(*kafkago.Transport)
	require.True(t, ok)
	assert.Nil(t, tr.SASL)
}

func TestPublisher_OwnedTransport_ReusesExisting(t *testing.T) {
	p := New()
	first := p.ownedTransport()
	second := p.ownedTransport()
	assert.Same(t, first, second)
	assert.Same(t, first, p.transport)
}
