package mink_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	kafkago "github.com/segmentio/kafka-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mink "go-mink.dev"
	"go-mink.dev/adapters/postgres"
	"go-mink.dev/outbox/kafka"
	"go-mink.dev/outbox/webhook"
	"go-mink.dev/testing/containers"
)

// e2eOrderPlaced is the event delivered through the outbox in these suites.
type e2eOrderPlaced struct {
	OrderID string `json:"orderId"`
	Amount  int    `json:"amount"`
}

// newOutboxStack wires the postgres OutboxStore (initialized) and an EventStoreWithOutbox that
// routes every appended event to destination. maxAttempts caps per-message delivery attempts.
func newOutboxStack(t *testing.T, p *e2ePG, destination string, maxAttempts int) (*mink.EventStoreWithOutbox, *postgres.OutboxStore) {
	t.Helper()
	outboxStore := postgres.NewOutboxStoreFromAdapter(p.Adapter)
	require.NoError(t, outboxStore.Initialize(p.Ctx)) // creates mink_outbox — the adapter migration does not
	routes := []mink.OutboxRoute{{Destination: destination}}
	esOut := mink.NewEventStoreWithOutbox(p.Store, outboxStore, routes, mink.WithOutboxMaxAttempts(maxAttempts))
	return esOut, outboxStore
}

// outboxCount returns how many rows in the isolated schema's outbox table have the given status.
func outboxCount(t *testing.T, p *e2ePG, outbox *postgres.OutboxStore, status int) int {
	t.Helper()
	var n int
	require.NoError(t, outbox.DB().QueryRowContext(p.Ctx,
		"SELECT count(*) FROM "+p.table("mink_outbox")+" WHERE status = $1", status).Scan(&n))
	return n
}

// TestE2E_OutboxDelivery_Webhook: PG AppendWithOutbox -> OutboxProcessor -> real webhook.
func TestE2E_OutboxDelivery_Webhook(t *testing.T) {
	p := newE2EPG(t, e2eOrderPlaced{})

	var received atomic.Int32
	var mu sync.Mutex
	var gotBody []byte
	var gotEventType string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotBody = b
		gotEventType = r.Header.Get("X-Outbox-event-type")
		mu.Unlock()
		received.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	esOut, outbox := newOutboxStack(t, p, "webhook:"+server.URL, 5)
	require.NoError(t, esOut.Append(p.Ctx, "order-1", []interface{}{e2eOrderPlaced{OrderID: "1", Amount: 100}}))

	proc := mink.NewOutboxProcessor(outbox,
		mink.WithPublisher(webhook.New()),
		mink.WithPollInterval(50*time.Millisecond),
	)
	require.NoError(t, proc.Start(p.Ctx))
	defer func() { _ = proc.Stop(context.Background()) }()

	eventually(t, 15*time.Second, func() bool { return received.Load() >= 1 })
	eventually(t, 5*time.Second, func() bool { return outboxCount(t, p, outbox, int(mink.OutboxCompleted)) == 1 })

	mu.Lock()
	defer mu.Unlock()
	assert.NotEmpty(t, gotBody, "webhook received a non-empty payload")
	assert.Equal(t, "e2eOrderPlaced", gotEventType, "event-type header propagated (X-Outbox- prefix)")
}

// TestE2E_OutboxDelivery_Kafka: PG AppendWithOutbox -> OutboxProcessor -> real Kafka broker.
func TestE2E_OutboxDelivery_Kafka(t *testing.T) {
	p := newE2EPG(t, e2eOrderPlaced{})
	k := containers.StartKafka(t)
	topic := fmt.Sprintf("e2e-orders-%d", time.Now().UnixNano())
	k.CreateTopic(t, topic)

	esOut, outbox := newOutboxStack(t, p, "kafka:"+topic, 5)
	require.NoError(t, esOut.Append(p.Ctx, "order-9", []interface{}{e2eOrderPlaced{OrderID: "9", Amount: 42}}))

	pub := kafka.New(kafka.WithBrokers(k.BrokerList()...))
	defer func() { _ = pub.Close() }()
	proc := mink.NewOutboxProcessor(outbox,
		mink.WithPublisher(pub),
		mink.WithPollInterval(50*time.Millisecond),
	)
	require.NoError(t, proc.Start(p.Ctx))
	defer func() { _ = proc.Stop(context.Background()) }()

	reader := k.NewReader(topic)
	defer func() { _ = reader.Close() }()
	readCtx, cancel := context.WithTimeout(p.Ctx, 20*time.Second)
	defer cancel()
	msg, err := reader.ReadMessage(readCtx)
	require.NoError(t, err, "message delivered to Kafka topic %s", topic)

	assert.NotEmpty(t, msg.Value, "Kafka message carries the event payload")
	assert.Equal(t, "e2eOrderPlaced", kafkaHeader(msg, "event-type"), "event-type header propagated to Kafka")
	eventually(t, 5*time.Second, func() bool { return outboxCount(t, p, outbox, int(mink.OutboxCompleted)) == 1 })
}

// TestE2E_OutboxDelivery_DeadLetter: a permanently-failing endpoint exhausts retries and the
// message is dead-lettered — while the appended event row is never mutated (append-only).
func TestE2E_OutboxDelivery_DeadLetter(t *testing.T) {
	p := newE2EPG(t, e2eOrderPlaced{})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError) // always fails
	}))
	defer server.Close()

	esOut, outbox := newOutboxStack(t, p, "webhook:"+server.URL, 1)
	require.NoError(t, esOut.Append(p.Ctx, "order-x", []interface{}{e2eOrderPlaced{OrderID: "x", Amount: 1}}))
	eventsBefore := p.countRows(t, "events")

	proc := mink.NewOutboxProcessor(outbox,
		mink.WithPublisher(webhook.New()),
		mink.WithPollInterval(50*time.Millisecond),
		mink.WithMaxRetries(1),
		mink.WithRetryBackoff(50*time.Millisecond),
	)
	require.NoError(t, proc.Start(p.Ctx))
	defer func() { _ = proc.Stop(context.Background()) }()

	eventually(t, 20*time.Second, func() bool {
		dl, err := outbox.GetDeadLetterMessages(p.Ctx, 10)
		return err == nil && len(dl) >= 1
	})
	assert.Zero(t, outboxCount(t, p, outbox, int(mink.OutboxCompleted)), "a failing message is never marked completed")
	assert.Equal(t, eventsBefore, p.countRows(t, "events"), "append-only: the event row is untouched by outbox failure")
}

// kafkaHeader returns the first Kafka header value for key, or "".
func kafkaHeader(msg kafkago.Message, key string) string {
	for _, h := range msg.Headers {
		if h.Key == key {
			return string(h.Value)
		}
	}
	return ""
}
