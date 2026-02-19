// Package webhook provides a webhook publisher for the outbox pattern.
// It sends HTTP POST requests to configured endpoints for each outbox message.
package webhook

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/AshkanYarmoradi/go-mink/adapters"
)

// Publisher publishes outbox messages as HTTP POST requests.
// Destination format: "webhook:https://example.com/events"
type Publisher struct {
	client         *http.Client
	defaultHeaders map[string]string
}

// Option configures a webhook Publisher.
type Option func(*Publisher)

// WithHTTPClient sets a custom HTTP client.
func WithHTTPClient(client *http.Client) Option {
	return func(p *Publisher) {
		p.client = client
	}
}

// WithTimeout sets the HTTP request timeout.
func WithTimeout(d time.Duration) Option {
	return func(p *Publisher) {
		p.client.Timeout = d
	}
}

// WithDefaultHeaders sets default headers added to all requests.
func WithDefaultHeaders(headers map[string]string) Option {
	return func(p *Publisher) {
		for k, v := range headers {
			p.defaultHeaders[k] = v
		}
	}
}

// New creates a new webhook Publisher.
func New(opts ...Option) *Publisher {
	p := &Publisher{
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		defaultHeaders: map[string]string{
			"Content-Type": "application/json",
		},
	}

	for _, opt := range opts {
		opt(p)
	}

	return p
}

// Destination returns the destination prefix this publisher handles.
func (p *Publisher) Destination() string {
	return "webhook"
}

// Publish sends each outbox message as an HTTP POST to the URL specified in the destination.
// The URL is extracted from the destination by removing the "webhook:" prefix.
func (p *Publisher) Publish(ctx context.Context, messages []*adapters.OutboxMessage) error {
	for _, msg := range messages {
		url := extractURL(msg.Destination)
		if url == "" {
			return fmt.Errorf("webhook: invalid destination %q: missing URL", msg.Destination)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(msg.Payload))
		if err != nil {
			return fmt.Errorf("webhook: failed to create request: %w", err)
		}

		// Set default headers
		for k, v := range p.defaultHeaders {
			req.Header.Set(k, v)
		}

		// Set message headers (override defaults)
		for k, v := range msg.Headers {
			req.Header.Set("X-Outbox-"+k, v)
		}

		resp, err := p.client.Do(req)
		if err != nil {
			return fmt.Errorf("webhook: request failed for %s: %w", url, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()

		if resp.StatusCode >= 500 {
			return fmt.Errorf("webhook: server error %d from %s", resp.StatusCode, url)
		}
		if resp.StatusCode >= 400 {
			return fmt.Errorf("webhook: client error %d from %s", resp.StatusCode, url)
		}
	}

	return nil
}

// extractURL removes the "webhook:" prefix from a destination.
func extractURL(destination string) string {
	const prefix = "webhook:"
	if strings.HasPrefix(destination, prefix) {
		return destination[len(prefix):]
	}
	return ""
}
