package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go-mink.dev/adapters"
)

// countingServer returns an httptest server that records how many requests it
// received and the last body it saw, and answers with the given status.
func countingServer(t *testing.T, status int) (*httptest.Server, *atomic.Int32, *atomic.Pointer[string]) {
	t.Helper()
	var hits atomic.Int32
	var lastBody atomic.Pointer[string]
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		b, _ := io.ReadAll(r.Body)
		s := string(b)
		lastBody.Store(&s)
		w.WriteHeader(status)
	}))
	t.Cleanup(server.Close)
	return server, &hits, &lastBody
}

func oneMessage(dest string, payload string) []*adapters.OutboxMessage {
	return []*adapters.OutboxMessage{
		{ID: "msg-1", Destination: dest, Payload: []byte(payload)},
	}
}

// =============================================================================
// Redirects
// =============================================================================

func TestPublisher_Publish_RedirectRefused_DefaultClient(t *testing.T) {
	// The attacker-controlled "target" must never see the payload.
	target, targetHits, _ := countingServer(t, http.StatusOK)

	// 307 preserves method+body, i.e. the worst case for payload re-sending.
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/stolen", http.StatusTemporaryRedirect)
	}))
	defer origin.Close()

	p := New(WithDefaultHeaders(map[string]string{"Authorization": "Bearer secret-token"}))
	err := p.Publish(context.Background(), oneMessage("webhook:"+origin.URL, `{"secret":"payload"}`))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected status 307")
	assert.Contains(t, err.Error(), "not 2xx")
	assert.Equal(t, int32(0), targetHits.Load(), "redirect target must never be contacted")
}

// redirectingServer returns an httptest server that answers every request with
// a 307 (method and body preserving — the worst case for payload re-sending)
// redirect to target.
func redirectingServer(t *testing.T, target string) *httptest.Server {
	t.Helper()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(origin.Close)
	return origin
}

// followAll is a caller-supplied CheckRedirect that follows every redirect.
func followAll(*http.Request, []*http.Request) error { return nil }

func TestPublisher_Publish_Redirect_InjectedClientWithoutCheckRedirect_Refused(t *testing.T) {
	// An injected client with the zero-value CheckRedirect would follow the
	// redirect and re-send the body; the publisher's copy of it must not.
	target, targetHits, _ := countingServer(t, http.StatusOK)
	origin := redirectingServer(t, target.URL+"/elsewhere")

	injected := &http.Client{Timeout: 5 * time.Second}
	p := New(WithHTTPClient(injected), WithDefaultHeaders(map[string]string{"Authorization": "Bearer secret-token"}))
	err := p.Publish(context.Background(), oneMessage("webhook:"+origin.URL, `{"n":1}`))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected status 307")
	assert.Equal(t, int32(0), targetHits.Load(), "redirect target must never be contacted")
	assert.Nil(t, injected.CheckRedirect, "the caller's client must not be mutated")
	assert.NotSame(t, injected, p.client, "the publisher works on a copy")
}

func TestPublisher_Publish_Redirect_InjectedClientWithCheckRedirect_PolicyValidatesEveryHop(t *testing.T) {
	target, targetHits, targetBody := countingServer(t, http.StatusOK)
	targetHost := mustHost(t, target.URL) // 127.0.0.1:<port>

	t.Run("hop to a host outside the allowlist is refused before it is contacted", func(t *testing.T) {
		before := targetHits.Load()
		// Same server, reached through a hostname the allowlist does not know.
		origin := redirectingServer(t, "http://localhost:"+mustPort(t, target.URL)+"/stolen")

		p := New(WithHTTPClient(&http.Client{CheckRedirect: followAll}), WithAllowedHosts("127.0.0.1"))
		err := p.Publish(context.Background(), oneMessage("webhook:"+origin.URL, `{"n":1}`))

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrHostNotAllowed)
		assert.Contains(t, err.Error(), "localhost")
		assert.NotContains(t, err.Error(), "/stolen", "the hop's path never reaches the error string")
		assert.Equal(t, before, targetHits.Load(), "a refused hop must never be contacted")
	})

	t.Run("hop downgrading to http is refused under WithRequireHTTPS", func(t *testing.T) {
		before := targetHits.Load()
		origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL+"/downgraded", http.StatusTemporaryRedirect)
		}))
		defer origin.Close()
		client := origin.Client() // trusts the test certificate
		client.CheckRedirect = followAll

		p := New(WithHTTPClient(client), WithRequireHTTPS())
		err := p.Publish(context.Background(), oneMessage("webhook:"+origin.URL, `{"n":1}`))

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrHTTPSRequired)
		assert.Equal(t, before, targetHits.Load())
	})

	t.Run("hop to an allowed host is handed to the caller's policy and followed", func(t *testing.T) {
		before := targetHits.Load()
		origin := redirectingServer(t, target.URL+"/moved")

		var userChecks atomic.Int32
		client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
			userChecks.Add(1)
			return nil
		}}
		p := New(WithHTTPClient(client), WithAllowedHosts("127.0.0.1"))
		require.NoError(t, p.Publish(context.Background(), oneMessage("webhook:"+origin.URL, `{"n":2}`)))

		assert.Equal(t, before+1, targetHits.Load(), "an allowed hop is followed")
		assert.Equal(t, `{"n":2}`, *targetBody.Load())
		assert.Equal(t, int32(1), userChecks.Load(), "the caller's CheckRedirect still runs")
	})

	t.Run("caller's CheckRedirect error is preserved", func(t *testing.T) {
		before := targetHits.Load()
		origin := redirectingServer(t, target.URL+"/moved")
		userErr := errors.New("no redirects today")

		client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return userErr }}
		p := New(WithHTTPClient(client))
		err := p.Publish(context.Background(), oneMessage("webhook:"+origin.URL, `{}`))

		require.Error(t, err)
		assert.ErrorIs(t, err, userErr)
		assert.Equal(t, before, targetHits.Load())
	})

	t.Run("caller's client is not mutated", func(t *testing.T) {
		client := &http.Client{CheckRedirect: followAll, Timeout: 3 * time.Second}
		p := New(WithHTTPClient(client), WithTimeout(9*time.Second), WithAllowedHosts(targetHost))

		assert.Equal(t, 3*time.Second, client.Timeout, "WithTimeout must act on the copy")
		assert.Equal(t, 9*time.Second, p.client.Timeout)
		assert.NotSame(t, client, p.client)
		// The caller's function is still exactly the permissive one it set.
		assert.NoError(t, client.CheckRedirect(&http.Request{URL: &url.URL{Scheme: "http", Host: "evil.example.com"}}, nil))
		// The publisher's copy enforces the policy first.
		err := p.client.CheckRedirect(&http.Request{URL: &url.URL{Scheme: "http", Host: "evil.example.com"}}, nil)
		assert.ErrorIs(t, err, ErrHostNotAllowed)
	})
}

func TestPublisher_guardRedirects_ConsultsPolicyAtRequestTime(t *testing.T) {
	// Options applied after WithHTTPClient (allowlist, HTTPS) must still govern
	// redirect hops: the guard reads the publisher's policy when the hop happens.
	p := New(WithHTTPClient(&http.Client{CheckRedirect: followAll}), WithAllowedHosts("hooks.example.com"), WithRequireHTTPS())

	hop := func(raw string) error {
		u, err := url.Parse(raw)
		require.NoError(t, err)
		return p.client.CheckRedirect(&http.Request{URL: u}, nil)
	}
	assert.NoError(t, hop("https://hooks.example.com/v2"))
	assert.ErrorIs(t, hop("https://evil.example.com/v2"), ErrHostNotAllowed)
	assert.ErrorIs(t, hop("http://hooks.example.com/v2"), ErrHTTPSRequired)
	assert.ErrorIs(t, hop("ftp://hooks.example.com/v2"), ErrInvalidDestination)
}

func TestNewDefaultClient_HasRedirectPolicy(t *testing.T) {
	p := New()
	require.NotNil(t, p.client.CheckRedirect)
	assert.ErrorIs(t, p.client.CheckRedirect(nil, nil), http.ErrUseLastResponse)
	assert.Equal(t, 30*time.Second, p.client.Timeout)
}

func TestWithTimeout_NilInjectedClient_UsesSecureDefault(t *testing.T) {
	p := New(WithHTTPClient(nil), WithTimeout(7*time.Second))
	require.NotNil(t, p.client)
	assert.Equal(t, 7*time.Second, p.client.Timeout)
	require.NotNil(t, p.client.CheckRedirect)
	assert.ErrorIs(t, p.client.CheckRedirect(nil, nil), http.ErrUseLastResponse)
}

// =============================================================================
// Destination validation
// =============================================================================

func TestPublisher_Publish_InvalidDestinations(t *testing.T) {
	server, hits, _ := countingServer(t, http.StatusOK)
	_ = server

	tests := []struct {
		name        string
		destination string
		wantErr     error
		wantSubstr  string
		notContains string
	}{
		{"not a webhook destination", "kafka:topic", ErrInvalidDestination, "missing URL", ""},
		{"empty url", "webhook:", ErrInvalidDestination, "missing URL", ""},
		{"ftp scheme", "webhook:ftp://files.example.com/x", ErrInvalidDestination, "not http or https", ""},
		{"file scheme", "webhook:file:///etc/passwd", ErrInvalidDestination, "not http or https", ""},
		{"no host", "webhook:http:///path", ErrInvalidDestination, "missing host", ""},
		{"relative url", "webhook:/just/a/path", ErrInvalidDestination, "not http or https", ""},
		{"unparseable", "webhook:http://[::1/path?secret=1", ErrInvalidDestination, "cannot parse URL", "secret=1"},
	}

	p := New()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := p.Publish(context.Background(), oneMessage(tt.destination, `{}`))
			require.Error(t, err)
			assert.ErrorIs(t, err, tt.wantErr)
			assert.Contains(t, err.Error(), tt.wantSubstr)
			if tt.notContains != "" {
				assert.NotContains(t, err.Error(), tt.notContains)
			}
		})
	}
	assert.Equal(t, int32(0), hits.Load())
}

func TestPublisher_WithAllowedHosts(t *testing.T) {
	server, hits, _ := countingServer(t, http.StatusOK)
	host := mustHostname(t, server.URL) // 127.0.0.1

	t.Run("exact match delivers", func(t *testing.T) {
		p := New(WithAllowedHosts("hooks.example.com", strings.ToUpper(host)))
		require.NoError(t, p.Publish(context.Background(), oneMessage("webhook:"+server.URL+"/hook", `{}`)))
		assert.Equal(t, int32(1), hits.Load())
	})

	t.Run("non-matching host refused without sending", func(t *testing.T) {
		before := hits.Load()
		p := New(WithAllowedHosts("hooks.example.com", "*.example.com"))
		err := p.Publish(context.Background(), oneMessage("webhook:"+server.URL+"/hook", `{}`))
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrHostNotAllowed)
		assert.Contains(t, err.Error(), host)
		assert.Equal(t, before, hits.Load())
	})

	t.Run("empty allowlist denies everything", func(t *testing.T) {
		before := hits.Load()
		for _, p := range []*Publisher{New(WithAllowedHosts()), New(WithAllowedHosts("", "  ", "*."))} {
			err := p.Publish(context.Background(), oneMessage("webhook:"+server.URL, `{}`))
			assert.ErrorIs(t, err, ErrHostNotAllowed)
		}
		assert.Equal(t, before, hits.Load())
	})
}

func TestPublisher_HostAllowed_Matching(t *testing.T) {
	tests := []struct {
		name    string
		allowed []string
		host    string
		want    bool
	}{
		{"exact", []string{"hooks.example.com"}, "hooks.example.com", true},
		{"exact is case-insensitive", []string{"Hooks.Example.COM"}, "hooks.example.com", true},
		{"exact does not match subdomain", []string{"example.com"}, "a.example.com", false},
		{"exact does not match superdomain", []string{"a.example.com"}, "example.com", false},
		{"wildcard matches subdomain", []string{"*.example.com"}, "a.example.com", true},
		{"wildcard matches nested subdomain", []string{"*.example.com"}, "a.b.example.com", true},
		{"wildcard does not match apex", []string{"*.example.com"}, "example.com", false},
		{"wildcard does not match lookalike", []string{"*.example.com"}, "evil-example.com", false},
		{"wildcard does not match suffix-only", []string{"*.example.com"}, "notexample.com", false},
		{"ipv4 literal", []string{"10.0.0.5"}, "10.0.0.5", true},
		{"ipv6 literal", []string{"::1"}, "::1", true},
		{"ipv4 near miss", []string{"10.0.0.5"}, "10.0.0.50", false},
		{"any of several", []string{"a.com", "*.b.com", "c.com"}, "x.b.com", true},
		{"none of several", []string{"a.com", "*.b.com", "c.com"}, "b.com", false},
		{"bare wildcard entry is ignored", []string{"*."}, "anything.com", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := New(WithAllowedHosts(tt.allowed...))
			assert.Equal(t, tt.want, p.hostAllowed(normalizeHost(tt.host)))
		})
	}
}

func TestPublisher_WithRequireHTTPS(t *testing.T) {
	t.Run("http destination refused without sending", func(t *testing.T) {
		server, hits, _ := countingServer(t, http.StatusOK)
		p := New(WithRequireHTTPS())
		err := p.Publish(context.Background(), oneMessage("webhook:"+server.URL+"/hook?token=abc", `{}`))
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrHTTPSRequired)
		assert.NotContains(t, err.Error(), "token=abc")
		assert.NotContains(t, err.Error(), "/hook", "the path is dropped from error strings")
		assert.Equal(t, int32(0), hits.Load())
	})

	t.Run("https destination delivers", func(t *testing.T) {
		var hits atomic.Int32
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		p := New(WithRequireHTTPS(), WithHTTPClient(server.Client()))
		require.NoError(t, p.Publish(context.Background(), oneMessage("webhook:"+server.URL+"/hook", `{}`)))
		assert.Equal(t, int32(1), hits.Load())
	})

	t.Run("precedence: scheme check runs before allowlist", func(t *testing.T) {
		p := New(WithRequireHTTPS(), WithAllowedHosts("nowhere.example.com"))
		err := p.Publish(context.Background(), oneMessage("webhook:http://nowhere.example.com/x", `{}`))
		assert.ErrorIs(t, err, ErrHTTPSRequired)
	})
}

// =============================================================================
// Signing
// =============================================================================

func TestPublisher_WithSigningSecret_HeadersCorrect(t *testing.T) {
	var gotHeaders http.Header
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	secret := []byte("super-secret")
	fixed := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	p := New(WithSigningSecret(secret))
	p.now = func() time.Time { return fixed }

	// Mutating the caller's copy after configuration must not affect signing.
	secret[0] = 'X'

	payload := `{"event":"OrderCreated","id":"123"}`
	require.NoError(t, p.Publish(context.Background(), oneMessage("webhook:"+server.URL, payload)))

	ts := gotHeaders.Get(HeaderTimestamp)
	assert.Equal(t, "1790942400", ts)
	assert.Equal(t, payload, string(gotBody))

	// Verification recipe as documented on WithSigningSecret.
	mac := hmac.New(sha256.New, []byte("super-secret"))
	mac.Write([]byte(ts + "." + string(gotBody)))
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	got := gotHeaders.Get(HeaderSignature)
	assert.Equal(t, want, got)
	assert.True(t, hmac.Equal([]byte(want), []byte(got)))

	// A different secret must not verify.
	other := hmac.New(sha256.New, []byte("wrong"))
	other.Write([]byte(ts + "." + string(gotBody)))
	assert.NotEqual(t, "sha256="+hex.EncodeToString(other.Sum(nil)), got)
}

func TestPublisher_WithSigningSecret_UsesWallClockByDefault(t *testing.T) {
	var ts string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ts = r.Header.Get(HeaderTimestamp)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	before := time.Now().Unix()
	p := New(WithSigningSecret([]byte("s")))
	require.NoError(t, p.Publish(context.Background(), oneMessage("webhook:"+server.URL, `{}`)))
	after := time.Now().Unix()

	require.NotEmpty(t, ts)
	var unix int64
	for _, c := range ts {
		require.True(t, c >= '0' && c <= '9', "timestamp must be unix seconds, got %q", ts)
		unix = unix*10 + int64(c-'0')
	}
	assert.GreaterOrEqual(t, unix, before)
	assert.LessOrEqual(t, unix, after)
}

func TestPublisher_Signing_NotSetWhenUnconfigured(t *testing.T) {
	var gotHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	for _, p := range []*Publisher{New(), New(WithSigningSecret(nil)), New(WithSigningSecret([]byte{}))} {
		gotHeaders = nil
		require.NoError(t, p.Publish(context.Background(), oneMessage("webhook:"+server.URL, `{}`)))
		assert.Empty(t, gotHeaders.Get(HeaderTimestamp))
		assert.Empty(t, gotHeaders.Get(HeaderSignature))
	}
}

func TestPublisher_Signing_MessageHeadersCannotOverride(t *testing.T) {
	var gotHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	p := New(WithSigningSecret([]byte("s")))
	p.now = func() time.Time { return time.Unix(1700000000, 0) }
	msgs := []*adapters.OutboxMessage{{
		ID: "m", Destination: "webhook:" + server.URL, Payload: []byte(`{}`),
		Headers: map[string]string{"Timestamp": "1", "Signature": "sha256=forged"},
	}}
	require.NoError(t, p.Publish(context.Background(), msgs))

	assert.Equal(t, "1700000000", gotHeaders.Get(HeaderTimestamp))
	assert.Equal(t, sign([]byte("s"), "1700000000", []byte(`{}`)), gotHeaders.Get(HeaderSignature))
}

// =============================================================================
// Header injection
// =============================================================================

func TestPublisher_Publish_RejectsCRLFHeaders(t *testing.T) {
	server, hits, _ := countingServer(t, http.StatusOK)

	tests := []struct {
		name    string
		headers map[string]string
		substr  string
	}{
		{"CRLF in value", map[string]string{"correlation-id": "abc\r\nX-Injected: 1"}, "contains CR/LF"},
		{"LF in value", map[string]string{"stream-id": "a\nb"}, "contains CR/LF"},
		{"CR in key", map[string]string{"bad\rkey": "v"}, "key contains CR/LF"},
		{"LF in key", map[string]string{"bad\nkey": "v"}, "key contains CR/LF"},
		{"empty key", map[string]string{"": "v"}, "empty header key"},
	}

	p := New()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msgs := []*adapters.OutboxMessage{{
				ID: "m", Destination: "webhook:" + server.URL, Payload: []byte(`{}`), Headers: tt.headers,
			}}
			err := p.Publish(context.Background(), msgs)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrInvalidHeader)
			assert.Contains(t, err.Error(), tt.substr)
			// The injected value itself must not be echoed into the error.
			assert.NotContains(t, err.Error(), "X-Injected")
		})
	}
	assert.Equal(t, int32(0), hits.Load())
}

func TestValidateHeaders_AcceptsNormalHeaders(t *testing.T) {
	assert.NoError(t, validateHeaders(nil))
	assert.NoError(t, validateHeaders(map[string]string{}))
	assert.NoError(t, validateHeaders(map[string]string{"correlation-id": "abc-123", "event-type": "OrderCreated", "empty": ""}))
}

// =============================================================================
// Error redaction
// =============================================================================

func TestPublisher_Publish_ErrorRedactsCredentials_StatusError(t *testing.T) {
	server, _, _ := countingServer(t, http.StatusInternalServerError)
	u, err := url.Parse(server.URL)
	require.NoError(t, err)
	dest := "webhook:http://alice:hunter2@" + u.Host + "/hooks/orders?api_key=s3cret&x=1#frag"

	err = New().Publish(context.Background(), oneMessage(dest, `{}`))
	require.Error(t, err)

	msg := err.Error()
	assert.Contains(t, msg, "server error 500")
	assert.Contains(t, msg, "http://"+u.Host)
	for _, leaked := range []string{"hunter2", "alice", "api_key", "s3cret", "frag", "xxxxx", "/hooks/orders", "orders"} {
		assert.NotContains(t, msg, leaked)
	}
}

func TestPublisher_Publish_ErrorRedactsCredentials_TransportError(t *testing.T) {
	// Closed server -> connection refused -> net/http wraps it in *url.Error
	// carrying the (only password-masked) URL. Ours must carry the redacted one.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	addr := mustHost(t, server.URL)
	server.Close()

	dest := "webhook:http://alice:hunter2@" + addr + "/hooks?token=s3cret"
	err := New().Publish(context.Background(), oneMessage(dest, `{}`))
	require.Error(t, err)

	msg := err.Error()
	assert.Contains(t, msg, "request failed")
	assert.Contains(t, msg, "http://"+addr)
	for _, leaked := range []string{"hunter2", "alice", "token", "s3cret", "***", "/hooks"} {
		assert.NotContains(t, msg, leaked)
	}

	// The *url.Error type and the underlying cause survive for errors.As/Is.
	var ue *url.Error
	require.True(t, errors.As(err, &ue))
	assert.Equal(t, "Post", ue.Op)
	assert.Equal(t, "http://"+addr, ue.URL)
	assert.NotNil(t, ue.Err)
}

func TestPublisher_Publish_ErrorRedactsCredentials_ContextCancelled(t *testing.T) {
	server, _, _ := countingServer(t, http.StatusOK)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	dest := "webhook:" + strings.Replace(server.URL, "http://", "http://bob:pw@", 1) + "/h?k=v"
	err := New().Publish(ctx, oneMessage(dest, `{}`))
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.NotContains(t, err.Error(), "pw")
	assert.NotContains(t, err.Error(), "k=v")
	assert.NotContains(t, err.Error(), "/h?", "the path is dropped from error strings")
}

func TestRedactURL(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{"https://hooks.example.com/events", "https://hooks.example.com"},
		{"https://user:pass@hooks.example.com/events?token=1#x", "https://hooks.example.com"},
		{"https://token@hooks.example.com/", "https://hooks.example.com"},
		{"http://localhost:8080/hook?", "http://localhost:8080"},
		{"HTTPS://Example.COM:443/A?b=c", "https://Example.COM:443"},
		{"https://hooks.slack.com/services/T000/B000/XXXXXXXX", "https://hooks.slack.com"},
		{"https://discord.com/api/webhooks/123/s3cret-token", "https://discord.com"},
		{"https://[::1]:8443/p?x=y", "https://[::1]:8443"},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			u, err := url.Parse(tt.raw)
			require.NoError(t, err)
			assert.Equal(t, tt.want, redactURL(u))
		})
	}
}

func TestRedactRequestError_NonURLError(t *testing.T) {
	plain := errors.New("boom")
	assert.Same(t, plain, redactRequestError(plain, "http://x/y"))
}

// =============================================================================
// Response draining
// =============================================================================

func TestPublisher_Publish_DrainsOversizedResponseBody(t *testing.T) {
	// A hostile endpoint streaming a huge body must not be buffered unbounded;
	// the publisher reads at most 1 MiB and still reports success on 2xx.
	chunk := strings.Repeat("x", 64*1024)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		for i := 0; i < 40; i++ { // 2.5 MiB
			if _, err := io.WriteString(w, chunk); err != nil {
				return
			}
		}
	}))
	defer server.Close()

	err := New().Publish(context.Background(), oneMessage("webhook:"+server.URL, `{}`))
	require.NoError(t, err)
}

// =============================================================================
// helpers
// =============================================================================

func mustHostname(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	require.NoError(t, err)
	return u.Hostname()
}

func mustHost(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	require.NoError(t, err)
	return u.Host
}

func mustPort(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	require.NoError(t, err)
	return u.Port()
}
