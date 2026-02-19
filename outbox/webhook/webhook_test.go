package webhook

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/AshkanYarmoradi/go-mink/adapters"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPublisher_Destination(t *testing.T) {
	p := New()
	assert.Equal(t, "webhook", p.Destination())
}

func TestPublisher_Publish_Success(t *testing.T) {
	var receivedBody []byte
	var receivedHeaders http.Header

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header
		body, _ := io.ReadAll(r.Body)
		receivedBody = body
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	p := New()
	ctx := context.Background()

	messages := []*adapters.OutboxMessage{
		{
			ID:          "msg-1",
			Destination: "webhook:" + server.URL,
			Payload:     []byte(`{"event":"OrderCreated","id":"123"}`),
			Headers: map[string]string{
				"correlation-id": "abc-123",
			},
		},
	}

	err := p.Publish(ctx, messages)
	require.NoError(t, err)

	assert.Equal(t, `{"event":"OrderCreated","id":"123"}`, string(receivedBody))
	assert.Equal(t, "application/json", receivedHeaders.Get("Content-Type"))
	assert.Equal(t, "abc-123", receivedHeaders.Get("X-Outbox-correlation-id"))
}

func TestPublisher_Publish_MultipleMessages(t *testing.T) {
	var callCount int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	p := New()
	ctx := context.Background()

	messages := []*adapters.OutboxMessage{
		{ID: "msg-1", Destination: "webhook:" + server.URL, Payload: []byte(`{"id":"1"}`)},
		{ID: "msg-2", Destination: "webhook:" + server.URL, Payload: []byte(`{"id":"2"}`)},
		{ID: "msg-3", Destination: "webhook:" + server.URL, Payload: []byte(`{"id":"3"}`)},
	}

	err := p.Publish(ctx, messages)
	require.NoError(t, err)
	assert.Equal(t, 3, callCount)
}

func TestPublisher_Publish_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	p := New()
	ctx := context.Background()

	messages := []*adapters.OutboxMessage{
		{ID: "msg-1", Destination: "webhook:" + server.URL, Payload: []byte(`{}`)},
	}

	err := p.Publish(ctx, messages)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "server error 500")
}

func TestPublisher_Publish_ClientError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	p := New()
	ctx := context.Background()

	messages := []*adapters.OutboxMessage{
		{ID: "msg-1", Destination: "webhook:" + server.URL, Payload: []byte(`{}`)},
	}

	err := p.Publish(ctx, messages)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "client error 404")
}

func TestPublisher_Publish_InvalidDestination(t *testing.T) {
	p := New()
	ctx := context.Background()

	messages := []*adapters.OutboxMessage{
		{ID: "msg-1", Destination: "invalid", Payload: []byte(`{}`)},
	}

	err := p.Publish(ctx, messages)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "missing URL")
}

func TestPublisher_Publish_WithCustomHeaders(t *testing.T) {
	var receivedHeaders http.Header

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	p := New(WithDefaultHeaders(map[string]string{
		"Authorization": "Bearer token123",
		"X-Custom":      "value",
	}))
	ctx := context.Background()

	messages := []*adapters.OutboxMessage{
		{ID: "msg-1", Destination: "webhook:" + server.URL, Payload: []byte(`{}`)},
	}

	err := p.Publish(ctx, messages)
	require.NoError(t, err)

	assert.Equal(t, "Bearer token123", receivedHeaders.Get("Authorization"))
	assert.Equal(t, "value", receivedHeaders.Get("X-Custom"))
}

func TestPublisher_Publish_ContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	p := New()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	messages := []*adapters.OutboxMessage{
		{ID: "msg-1", Destination: "webhook:" + server.URL, Payload: []byte(`{}`)},
	}

	err := p.Publish(ctx, messages)
	assert.Error(t, err)
}

func TestExtractURL(t *testing.T) {
	tests := []struct {
		destination string
		want        string
	}{
		{"webhook:https://example.com/events", "https://example.com/events"},
		{"webhook:http://localhost:8080/hook", "http://localhost:8080/hook"},
		{"kafka:topic", ""},
		{"invalid", ""},
		{"webhook:", ""},
	}

	for _, tt := range tests {
		t.Run(tt.destination, func(t *testing.T) {
			got := extractURL(tt.destination)
			assert.Equal(t, tt.want, got)
		})
	}
}

// =============================================================================
// New tests
// =============================================================================

func TestPublisher_WithHTTPClient(t *testing.T) {
	customClient := &http.Client{Timeout: 10 * time.Second}
	p := New(WithHTTPClient(customClient))
	assert.Equal(t, customClient, p.client)
}

func TestPublisher_WithTimeout(t *testing.T) {
	p := New(WithTimeout(5 * time.Second))
	assert.Equal(t, 5*time.Second, p.client.Timeout)
}

func TestPublisher_Publish_EmptyMessages(t *testing.T) {
	p := New()
	ctx := context.Background()

	// Nil messages — should not panic
	err := p.Publish(ctx, nil)
	assert.NoError(t, err)

	// Empty slice
	err = p.Publish(ctx, []*adapters.OutboxMessage{})
	assert.NoError(t, err)
}

func TestPublisher_Publish_Status399(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(399) // Just below 400 — no error
	}))
	defer server.Close()

	p := New()
	ctx := context.Background()

	messages := []*adapters.OutboxMessage{
		{ID: "msg-1", Destination: "webhook:" + server.URL, Payload: []byte(`{}`)},
	}

	err := p.Publish(ctx, messages)
	assert.NoError(t, err)
}

func TestPublisher_Publish_Status400(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
	}))
	defer server.Close()

	p := New()
	ctx := context.Background()

	messages := []*adapters.OutboxMessage{
		{ID: "msg-1", Destination: "webhook:" + server.URL, Payload: []byte(`{}`)},
	}

	err := p.Publish(ctx, messages)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "client error 400")
}
