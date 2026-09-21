// SPDX-License-Identifier: AGPL-3.0-only

package appclient

import (
	"log/slog"
	"net/http"
	"time"
)

// Spec configures one client. Zero values are valid: New fills defaults.
type Spec struct {
	// Name is the node/log identity, e.g. "jellyfin". It prefixes every
	// error and log record from this client.
	Name string
	// BaseURL is the fixed base URL, e.g. "http://localhost:8096".
	BaseURL string
	// BaseURLFn overrides BaseURL when set, for host-set-aware callers
	// (same reason Deps.PrimaryBaseURL is a func today).
	BaseURLFn func() string

	// Timeout is the per-request timeout. Default 15s.
	Timeout time.Duration
	// Retry is the retry policy. Zero value → DefaultRetry.
	Retry RetryPolicy
	// Headers are applied to every request from this client.
	Headers map[string]string

	// StaticAuth applies static credentials (no token lifecycle). Exactly
	// one of StaticAuth / Tokens is normally set.
	StaticAuth func(*http.Request)
	// Tokens is a managed token with 401 refresh. See TokenSpec.
	Tokens *TokenSpec

	// Transport is the HTTP transport. nil → a process-shared transport
	// (see DefaultTransport).
	Transport *http.Transport
	// Logger is the structured logger. nil → slog.Default().
	Logger *slog.Logger
	// Jar persists cookies between calls. Needed by apps whose login is
	// cookie-bound (e.g. a Django CSRF token: the token from a GET must be
	// presented back with the cookie it was issued against). nil means no
	// cookie storage, like a bare http.Client.
	Jar http.CookieJar

	// FollowRedirects defaults true. HA's trust/redirect probes need
	// ErrUseLastResponse; set *FollowRedirects=false to see the 3xx itself.
	FollowRedirects *bool
}

// Client is a configured HTTP client for one app/node. It is safe for
// concurrent use. Build one per node and hold it; it is stateless per node
// apart from its (optional) token source.
type Client struct {
	spec      Spec
	http      *http.Client
	transport *http.Transport
	sleeper   func(time.Duration)
	logger    *slog.Logger
	retry     RetryPolicy
	timeout   time.Duration
	name      string
}

// New builds a Client from a Spec, filling defaults for every zero value.
func New(spec Spec) *Client {
	c := &Client{spec: spec}

	c.name = spec.Name
	if spec.Timeout > 0 {
		c.timeout = spec.Timeout
	} else {
		c.timeout = 15 * time.Second
	}
	c.retry = spec.Retry.withDefaults()
	if isZeroPolicy(spec.Retry) {
		c.retry = DefaultRetry.withDefaults()
	}

	transport := spec.Transport
	if transport == nil {
		transport = DefaultTransport()
	}
	c.transport = transport
	c.http = &http.Client{
		Transport: transport,
		Timeout:   c.timeout,
		Jar:       spec.Jar,
	}
	if spec.FollowRedirects != nil && !*spec.FollowRedirects {
		c.http.CheckRedirect = func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}
	}

	c.logger = spec.Logger
	if c.logger == nil {
		c.logger = slog.Default()
	}
	if c.name != "" {
		c.logger = c.logger.With("app", c.name)
	}

	c.sleeper = time.Sleep
	return c
}

// WithSleeper replaces the sleep function (tests collapse all backoff to zero).
// It returns the client for chaining.
func (c *Client) WithSleeper(fn func(time.Duration)) *Client {
	if fn != nil {
		c.sleeper = fn
	}
	return c
}

// WithLogger overrides the logger after construction.
func (c *Client) WithLogger(l *slog.Logger) *Client {
	if l != nil {
		c.logger = l
		if c.name != "" {
			c.logger = c.logger.With("app", c.name)
		}
	}
	return c
}

// Name returns the client's app/node identity.
func (c *Client) Name() string { return c.name }

// Transport returns the *http.Transport this client uses. Exposed so callers
// (and tests) can assert connection-pool sharing across clients built from one
// factory.
func (c *Client) Transport() *http.Transport { return c.transport }

// resolveBaseURL returns the current base URL, honoring BaseURLFn.
func (c *Client) resolveBaseURL() string {
	if c.spec.BaseURLFn != nil {
		return c.spec.BaseURLFn()
	}
	return c.spec.BaseURL
}

// isZeroPolicy reports whether a RetryPolicy is the untouched zero value (so
// New can substitute DefaultRetry rather than a degenerate no-retry policy).
func isZeroPolicy(p RetryPolicy) bool {
	return p == RetryPolicy{}
}
