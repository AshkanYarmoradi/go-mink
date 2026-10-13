// Package webhook provides a webhook publisher for the outbox pattern.
// It sends HTTP POST requests to configured endpoints for each outbox message.
package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go-mink.dev/adapters"
)

const (
	// HeaderTimestamp carries the unix-seconds timestamp a signed delivery was
	// produced at. It is only set when WithSigningSecret is configured.
	HeaderTimestamp = "X-Outbox-Timestamp"

	// HeaderSignature carries the HMAC-SHA256 signature of a signed delivery
	// ("sha256=" + hex). It is only set when WithSigningSecret is configured.
	HeaderSignature = "X-Outbox-Signature"

	// messageHeaderPrefix is prepended to every outbox message header key.
	messageHeaderPrefix = "X-Outbox-"

	// maxDrainBytes bounds how much of a response body is read when draining
	// it so the underlying connection can be reused. Anything beyond this is
	// left unread and the connection is simply not reused.
	maxDrainBytes = 1 << 20 // 1 MiB

	// wildcardPrefix marks a WithAllowedHosts entry that matches every
	// subdomain of the remainder (e.g. "*.example.com").
	wildcardPrefix = "*."
)

// Sentinel errors use the "webhook: " prefix that every error produced by this
// package has always carried (e.g. "webhook: request failed"), so callers can
// recognize them by prefix as well as with errors.Is.
var (
	// ErrInvalidDestination is returned when a destination is missing its URL,
	// cannot be parsed, has a scheme other than http/https, or has no host.
	// It is a configuration error, not a transient delivery failure.
	ErrInvalidDestination = errors.New("webhook: invalid destination")

	// ErrHostNotAllowed is returned when WithAllowedHosts is configured and the
	// destination host — or, for a client that follows redirects, a redirect
	// target's host — matches none of the allowed entries.
	ErrHostNotAllowed = errors.New("webhook: destination host not allowed")

	// ErrHTTPSRequired is returned when WithRequireHTTPS is configured and the
	// destination — or, for a client that follows redirects, a redirect
	// target — uses a scheme other than https.
	ErrHTTPSRequired = errors.New("webhook: destination must use https")

	// ErrInvalidHeader is returned when an outbox message header key or value
	// is empty or contains CR/LF characters (header injection).
	ErrInvalidHeader = errors.New("webhook: invalid message header")
)

// Publisher publishes outbox messages as HTTP POST requests.
// Destination format: "webhook:https://example.com/events"
//
// Security defaults:
//   - Redirects are never followed by default (a 3xx response fails
//     delivery), so the payload and default headers (which commonly carry a
//     bearer token) are never re-sent to a host the operator did not
//     configure. This holds for a client injected with WithHTTPClient too: the
//     publisher never follows a redirect the client's own CheckRedirect would
//     not, and every hop a redirect-following client takes is validated against
//     the same destination policy as the first request.
//   - Every destination is parsed before sending: the scheme must be http or
//     https and the host must be non-empty. Use WithAllowedHosts and
//     WithRequireHTTPS to further constrain where payloads may be delivered.
//   - Error strings never embed a URL's userinfo, path, query or fragment —
//     only "scheme://host" — because the outbox processor persists them
//     (last_error) on every failed row and most webhook providers put the
//     bearer secret in the path.
//   - Message header keys/values containing CR or LF are rejected before the
//     request is built.
type Publisher struct {
	client         *http.Client
	defaultHeaders map[string]string

	// allowedHosts is the lower-cased host allowlist. hostAllowlist records
	// whether WithAllowedHosts was called at all, so that an explicitly empty
	// allowlist denies everything (fail closed) instead of allowing everything.
	allowedHosts  []string
	hostAllowlist bool

	requireHTTPS  bool
	signingSecret []byte

	// now is the clock used for signature timestamps; overridable in tests.
	now func() time.Time
}

// Option configures a webhook Publisher.
type Option func(*Publisher)

// WithHTTPClient sets a custom HTTP client (for example to supply a transport,
// proxy or TLS configuration). A nil client selects the default client.
//
// The caller's client is never mutated: the publisher works on a shallow copy
// of it and enforces its redirect policy on that copy (see New):
//
//   - If the client's CheckRedirect is nil — net/http's default, which follows
//     up to 10 redirects to ANY host, re-sending the event payload (on
//     307/308) together with the default headers configured via
//     WithDefaultHeaders, which commonly include an Authorization bearer
//     token — the copy refuses redirects exactly like the default client, so a
//     3xx response fails delivery instead of leaking the payload.
//   - If the caller set its own CheckRedirect (to deliberately follow
//     redirects), every hop is first validated against the publisher's
//     destination policy — http/https scheme, non-empty host,
//     WithRequireHTTPS and WithAllowedHosts, as configured on the publisher at
//     the time of the request — and only then handed to the caller's
//     function. A redirect to a host or scheme the policy would have refused
//     as a destination fails the delivery with the same ErrHostNotAllowed /
//     ErrHTTPSRequired / ErrInvalidDestination, without contacting it.
func WithHTTPClient(client *http.Client) Option {
	return func(p *Publisher) {
		if client == nil {
			p.client = newDefaultClient()
			return
		}
		c := *client
		p.client = &c
	}
}

// WithTimeout sets the HTTP request timeout. Applied after WithHTTPClient it
// changes the publisher's copy of the injected client, never the caller's.
func WithTimeout(d time.Duration) Option {
	return func(p *Publisher) {
		if p.client == nil {
			p.client = newDefaultClient()
		}
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

// WithAllowedHosts restricts delivery to destinations whose hostname matches
// one of the given entries. Matching is case-insensitive and ignores the port.
// An entry is either an exact hostname ("hooks.example.com", "10.0.0.5",
// "::1") or a leading "*." wildcard that matches every subdomain of the
// remainder — "*.example.com" matches "a.example.com" and "a.b.example.com"
// but not "example.com" itself. A destination whose host matches no entry
// fails with ErrHostNotAllowed without any request being sent.
//
// The allowlist fails closed: once this option is applied, calling it with no
// (or only blank) hosts denies every destination rather than allowing all.
// Entries are hostnames only — a scheme, port, or path in an entry is not
// supported and such an entry never matches.
func WithAllowedHosts(hosts ...string) Option {
	return func(p *Publisher) {
		p.hostAllowlist = true
		for _, h := range hosts {
			h = normalizeHost(h)
			if h == "" || h == wildcardPrefix {
				continue
			}
			p.allowedHosts = append(p.allowedHosts, h)
		}
	}
}

// WithRequireHTTPS refuses to deliver to any destination whose scheme is not
// https, returning ErrHTTPSRequired without sending. Use it in production so a
// mis-typed "http://" route cannot leak payloads and default headers (such as
// bearer tokens) in cleartext.
func WithRequireHTTPS() Option {
	return func(p *Publisher) {
		p.requireHTTPS = true
	}
}

// WithSigningSecret enables HMAC-SHA256 payload signing so receivers can
// authenticate deliveries and reject replays. The secret is copied; the
// caller may zero its own copy afterwards. An empty secret disables signing.
//
// Every request carries two extra headers:
//
//	X-Outbox-Timestamp: <unix seconds at which the request was signed>
//	X-Outbox-Signature: sha256=<hex(HMAC-SHA256(secret, timestamp + "." + body))>
//
// where body is the raw request body (the outbox message payload) and "."
// is a literal ASCII period. To verify a delivery, a receiver should:
//
//  1. Read X-Outbox-Timestamp and reject the request if it is outside an
//     acceptable tolerance of the current time (for example ±5 minutes) to
//     limit replay.
//  2. Read the raw request body bytes exactly as received (before any JSON
//     decoding or re-encoding).
//  3. Compute hex(HMAC-SHA256(secret, timestamp + "." + body)), prefix it
//     with "sha256=", and compare it to X-Outbox-Signature using a
//     constant-time comparison (hmac.Equal in Go).
//  4. Reject the request on any mismatch.
//
// These two headers are always written after the per-message "X-Outbox-*"
// headers, so a message header named "Timestamp" or "Signature" can never
// override them.
func WithSigningSecret(secret []byte) Option {
	return func(p *Publisher) {
		if len(secret) == 0 {
			p.signingSecret = nil
			return
		}
		p.signingSecret = bytes.Clone(secret)
	}
}

// New creates a new webhook Publisher.
func New(opts ...Option) *Publisher {
	p := &Publisher{
		client: newDefaultClient(),
		defaultHeaders: map[string]string{
			"Content-Type": "application/json",
		},
		now: time.Now,
	}

	for _, opt := range opts {
		opt(p)
	}
	if p.client == nil {
		p.client = newDefaultClient()
	}
	// Installed last so the redirect guard sees the final client (whichever
	// option supplied it) and consults the final policy at request time.
	p.client = p.guardRedirects(p.client)

	return p
}

// newDefaultClient builds the publisher's default HTTP client: a 30s timeout.
// Its redirect policy (refuseRedirect) is installed by guardRedirects in New,
// which treats every client without a CheckRedirect the same way.
func newDefaultClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second}
}

// refuseRedirect is the CheckRedirect policy of the default client (and of any
// injected client that did not set one). Returning http.ErrUseLastResponse makes
// client.Do hand back the 3xx response itself, which sendMessage then reports as
// a failed delivery.
func refuseRedirect(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}

// guardRedirects returns a shallow copy of client whose CheckRedirect enforces
// the publisher's redirect policy: a client without a CheckRedirect gets
// refuseRedirect; a client with one keeps it, but every redirect hop is first
// validated against the destination policy (validateURL) so a
// redirect-following client can never carry the payload and default headers to
// a scheme or host the policy would refuse. The caller's client is left intact.
func (p *Publisher) guardRedirects(client *http.Client) *http.Client {
	c := *client
	userCheck := c.CheckRedirect
	if userCheck == nil {
		c.CheckRedirect = refuseRedirect
		return &c
	}
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req != nil && req.URL != nil {
			if err := p.validateURL(req.URL); err != nil {
				return err
			}
		}
		return userCheck(req, via)
	}
	return &c
}

// Destination returns the destination prefix this publisher handles.
func (p *Publisher) Destination() string {
	return "webhook"
}

// Publish sends each outbox message as an HTTP POST to the URL specified in the destination.
// The URL is extracted from the destination by removing the "webhook:" prefix.
// All messages are attempted even if some fail; errors are collected and returned as a joined error.
func (p *Publisher) Publish(ctx context.Context, messages []*adapters.OutboxMessage) error {
	var errs []error
	for _, msg := range messages {
		if err := p.sendMessage(ctx, msg); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// sendMessage sends a single outbox message via HTTP POST.
func (p *Publisher) sendMessage(ctx context.Context, msg *adapters.OutboxMessage) error {
	u, err := p.validateDestination(msg.Destination)
	if err != nil {
		return err
	}
	safe := redactURL(u)

	if err := validateHeaders(msg.Headers); err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(msg.Payload))
	if err != nil {
		return fmt.Errorf("webhook: failed to create request: %w", redactRequestError(err, safe))
	}

	for k, v := range p.defaultHeaders {
		req.Header.Set(k, v)
	}
	for k, v := range msg.Headers {
		req.Header.Set(messageHeaderPrefix+k, v)
	}
	// Signature headers are written last so a message header cannot override them.
	if p.signingSecret != nil {
		ts := strconv.FormatInt(p.now().Unix(), 10)
		req.Header.Set(HeaderTimestamp, ts)
		req.Header.Set(HeaderSignature, sign(p.signingSecret, ts, msg.Payload))
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("webhook: request failed: %w", redactRequestError(err, safe))
	}
	defer func() { _ = resp.Body.Close() }()
	// Drain (bounded) so the connection can be reused; never buffer an
	// unbounded response from a possibly hostile endpoint.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxDrainBytes))

	// Delivery is successful only on a 2xx status. A 3xx (never followed unless
	// the injected client opted in, see guardRedirects), 4xx, or 5xx response
	// means the endpoint did not accept the payload; return an error so the
	// outbox retries or dead-letters instead of silently marking the message
	// delivered.
	if resp.StatusCode >= 500 {
		return fmt.Errorf("webhook: server error %d from %s", resp.StatusCode, safe)
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("webhook: client error %d from %s", resp.StatusCode, safe)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook: unexpected status %d from %s (not 2xx)", resp.StatusCode, safe)
	}
	return nil
}

// validateDestination extracts and parses the destination URL and enforces the
// scheme/host rules plus the optional WithRequireHTTPS / WithAllowedHosts
// policies. The returned error never embeds the raw destination.
func (p *Publisher) validateDestination(destination string) (*url.URL, error) {
	raw := extractURL(destination)
	if raw == "" {
		return nil, fmt.Errorf("%w: missing URL", ErrInvalidDestination)
	}

	u, err := url.Parse(raw)
	if err != nil {
		// *url.Error embeds the raw URL in its message; report only the cause.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, fmt.Errorf("%w: cannot parse URL: %w", ErrInvalidDestination, err)
	}

	if err := p.validateURL(u); err != nil {
		return nil, err
	}
	return u, nil
}

// validateURL enforces the destination policy on a parsed URL: http/https
// scheme, non-empty host, then the optional WithRequireHTTPS and
// WithAllowedHosts rules. It is applied to every destination before sending
// and, for a redirect-following client, to every redirect hop (see
// guardRedirects). The returned error never embeds more than the URL's
// scheme and host.
func (p *Publisher) validateURL(u *url.URL) error {
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("%w: scheme %q is not http or https", ErrInvalidDestination, u.Scheme)
	}
	host := normalizeHost(u.Hostname())
	if host == "" {
		return fmt.Errorf("%w: missing host", ErrInvalidDestination)
	}

	if p.requireHTTPS && scheme != "https" {
		return fmt.Errorf("%w: %s", ErrHTTPSRequired, redactURL(u))
	}
	if p.hostAllowlist && !p.hostAllowed(host) {
		return fmt.Errorf("%w: %q", ErrHostNotAllowed, host)
	}
	return nil
}

// hostAllowed reports whether the (normalized) host matches the allowlist.
func (p *Publisher) hostAllowed(host string) bool {
	for _, allowed := range p.allowedHosts {
		if suffix, ok := strings.CutPrefix(allowed, wildcardPrefix); ok {
			// "*.example.com" matches any strict subdomain of example.com.
			if strings.HasSuffix(host, "."+suffix) {
				return true
			}
			continue
		}
		if host == allowed {
			return true
		}
	}
	return false
}

// normalizeHost lower-cases and trims a hostname for comparison.
func normalizeHost(h string) string {
	return strings.ToLower(strings.TrimSpace(h))
}

// validateHeaders rejects message headers that are empty or carry CR/LF, so a
// caller-controlled value (stream id, correlation id, ...) can never inject
// additional header lines. net/http would reject these at write time anyway;
// failing early yields a clear, redaction-safe error instead.
func validateHeaders(headers map[string]string) error {
	for k, v := range headers {
		if k == "" {
			return fmt.Errorf("%w: empty header key", ErrInvalidHeader)
		}
		if strings.ContainsAny(k, "\r\n") {
			return fmt.Errorf("%w: key contains CR/LF", ErrInvalidHeader)
		}
		if strings.ContainsAny(v, "\r\n") {
			return fmt.Errorf("%w: value of %q contains CR/LF", ErrInvalidHeader, messageHeaderPrefix+k)
		}
	}
	return nil
}

// sign computes the X-Outbox-Signature value for a timestamp and body.
func sign(secret []byte, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// redactURL renders a URL as "scheme://host" (host includes the port when one
// was given) for use in error strings that the outbox processor persists
// (last_error) and operators log. Userinfo, path, query and fragment are all
// dropped: webhook providers commonly put the bearer secret in the path
// ("https://hooks.slack.com/services/T0/B0/<secret>",
// "https://discord.com/api/webhooks/<id>/<token>"), so the path is as
// sensitive as the query string.
func redactURL(u *url.URL) string {
	c := url.URL{Scheme: u.Scheme, Host: u.Host}
	return c.String()
}

// redactRequestError rewrites the *url.Error that net/http wraps transport
// failures in so its message carries the redacted URL instead of the raw one
// (net/http masks only the password, keeping the username and query string).
// The error type and the underlying cause are preserved for errors.Is/As.
func redactRequestError(err error, safe string) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return &url.Error{Op: ue.Op, URL: safe, Err: ue.Err}
	}
	return err
}

// extractURL removes the "webhook:" prefix from a destination.
func extractURL(destination string) string {
	const prefix = "webhook:"
	if strings.HasPrefix(destination, prefix) {
		return destination[len(prefix):]
	}
	return ""
}
