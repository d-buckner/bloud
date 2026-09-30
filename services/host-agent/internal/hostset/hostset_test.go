// SPDX-License-Identifier: AGPL-3.0-only

package hostset

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustParse(t *testing.T, raw string) PublicURL {
	t.Helper()
	u, err := ParsePublicURL(raw)
	if err != nil {
		t.Fatalf("ParsePublicURL(%q) failed: %v", raw, err)
	}
	return u
}

// The whole setting is one origin, so the parser is where the operator's typed
// string becomes something the derivation can trust.
func TestParsePublicURL(t *testing.T) {
	cases := []struct {
		in     string
		scheme Scheme
		host   string
		port   int
		origin string
	}{
		{"https://bloud.example.com", SchemeHTTPS, "bloud.example.com", 0, "https://bloud.example.com"},
		{"http://bloud.example.com", SchemeHTTP, "bloud.example.com", 0, "http://bloud.example.com"},
		{"bloud.example.com", SchemeHTTP, "bloud.example.com", 0, "http://bloud.example.com"},
		{"https://bloud.example.com:8443", SchemeHTTPS, "bloud.example.com", 8443, "https://bloud.example.com:8443"},
		{"http://localhost:8080", SchemeHTTP, "localhost", 8080, "http://localhost:8080"},
		{"  HTTPS://Bloud.Example.COM  ", SchemeHTTPS, "bloud.example.com", 0, "https://bloud.example.com"},
		{"https://bloud.example.com/", SchemeHTTPS, "bloud.example.com", 0, "https://bloud.example.com"},
		// A port that is the scheme default renders without it, so a stored
		// value and a derived one look identical.
		{"https://bloud.example.com:443", SchemeHTTPS, "bloud.example.com", 443, "https://bloud.example.com"},
		{"http://bloud.example.com:80", SchemeHTTP, "bloud.example.com", 80, "http://bloud.example.com"},
		{"10.0.0.210:8080", SchemeHTTP, "10.0.0.210", 8080, "http://10.0.0.210:8080"},
	}
	for _, c := range cases {
		u := mustParse(t, c.in)
		assert.Equal(t, c.scheme, u.Scheme, c.in)
		assert.Equal(t, c.host, u.Host, c.in)
		assert.Equal(t, c.port, u.Port, c.in)
		assert.Equal(t, c.origin, u.Origin(), c.in)
	}
}

// A partially-understood URL would register a redirect URI that never matches
// what the browser sends, so anything that is not a bare origin is rejected by
// name rather than quietly truncated.
func TestParsePublicURLRejects(t *testing.T) {
	for _, bad := range []string{
		"",
		"   ",
		"ftp://bloud.example.com",
		"http://bloud.example.com/some/path",
		"http://bloud.example.com?a=b",
		"http://bloud.example.com#frag",
		"http://user:pass@bloud.example.com",
		"http://",
		"http://:8080",
		"http://bloud.example.com:99999",
		"http://bloud.example.com:notaport",
		// No CA issues a certificate for a bare address, so https://<ip> is
		// an origin nothing can complete a TLS handshake against.
		"https://10.0.0.210",
	} {
		_, err := ParsePublicURL(bad)
		assert.Error(t, err, "ParsePublicURL(%q) must be rejected", bad)
	}
}

// Parsing is idempotent: re-parsing a rendered origin gives the same value, so
// a save loop cannot drift.
func TestParseOriginRoundTrips(t *testing.T) {
	for _, raw := range []string{
		"https://bloud.example.com",
		"http://localhost:8080",
		"https://bloud.example.com:8443",
	} {
		once := mustParse(t, raw)
		twice := mustParse(t, once.Origin())
		assert.Equal(t, once, twice, raw)
	}
}

// The public host renders as the configured origin, port carried verbatim:
// that port is the operator's own statement about where the proxy is dialed.
// The built-in aliases render on their fixed plain-http mapping.
func TestBaseURLFor(t *testing.T) {
	hs := New(mustParse(t, "https://bloud.example.com:8443"))

	assert.Equal(t, "https://bloud.example.com:8443", hs.BaseURLFor("bloud.example.com"))
	assert.Equal(t, "http://localhost:8080", hs.BaseURLFor("localhost"))
	assert.Equal(t, "http://bloud.local", hs.BaseURLFor("bloud.local"))
	assert.Equal(t, "", hs.BaseURLFor("someone.else.test"),
		"a host that is not part of this instance has no base URL")
	assert.Equal(t, "", hs.BaseURLFor("NOT A HOST"))
}

// SchemeFor follows the same split: the public host carries the configured
// scheme, the aliases are always plain http.
func TestSchemeFor(t *testing.T) {
	hs := New(mustParse(t, "https://bloud.example.com"))

	assert.Equal(t, SchemeHTTPS, hs.SchemeFor("bloud.example.com"))
	assert.Equal(t, SchemeHTTP, hs.SchemeFor("localhost"))
	assert.Equal(t, SchemeHTTP, hs.SchemeFor("bloud.local"))
	assert.Equal(t, Scheme(""), hs.SchemeFor("someone.else.test"))
}

func TestHostsAndContains(t *testing.T) {
	hs := New(mustParse(t, "https://bloud.example.com"))

	assert.Equal(t, []string{"bloud.example.com", "localhost", "bloud.local"}, hs.Hosts())
	assert.Equal(t, "bloud.example.com", hs.Primary())
	assert.True(t, hs.Contains("Bloud.Example.COM"))
	assert.True(t, hs.Contains("localhost"))
	assert.False(t, hs.Contains("someone.else.test"))
	assert.True(t, hs.IsBuiltin("bloud.local"))
	assert.False(t, hs.IsBuiltin("bloud.example.com"))
}

// The issuer and the primary must agree and be on the same origin: a mismatch
// here is an OIDC discovery failure with no obvious cause.
func TestIssuerFollowsThePublicURL(t *testing.T) {
	hs := New(mustParse(t, "https://bloud.example.com:8443"))
	assert.Equal(t, "https://bloud.example.com:8443", hs.PrimaryBaseURL())
	assert.Equal(t, hs.PrimaryBaseURL(), hs.IssuerBaseURL())
	assert.Equal(t, "bloud.example.com", hs.IssuerHost())
	assert.Equal(t, SchemeHTTPS, hs.PublicScheme())
}

// A localhost public URL cannot be the issuer for app containers, because
// inside a container localhost is the container. sso.localhost, resolved via
// extraHosts to the host gateway, is what makes the round trip work.
func TestLocalhostIssuerUsesSSOAlias(t *testing.T) {
	hs := New(mustParse(t, "http://localhost:8080"))
	assert.Equal(t, "http://sso.localhost:8080", hs.IssuerBaseURL())
	assert.Equal(t, "sso.localhost", hs.IssuerHost())
	assert.Equal(t, "sso.localhost:host-gateway", hs.IssuerExtraHost())
}

// Loopback-issuer apps run in the host network namespace, so for them
// localhost really is Traefik and the plain loopback URL is the right issuer.
func TestLoopbackIssuerBaseURL(t *testing.T) {
	hs := New(mustParse(t, "https://bloud.example.com"))
	assert.Equal(t, "http://localhost:8080", hs.LoopbackIssuerBaseURL())
}

// BaseURLs lists the public origin first so the OAuth client registers it
// first, with the local aliases alongside.
func TestBaseURLsOrder(t *testing.T) {
	hs := New(mustParse(t, "https://bloud.example.com"))
	bases := hs.BaseURLs()
	require.Len(t, bases, 3)
	assert.Equal(t, "https://bloud.example.com", bases[0])
	assert.Contains(t, bases, "http://localhost:8080")
	assert.Contains(t, bases, "http://bloud.local")
}

// New() with a zero-value URL still produces a usable set, so a caller that
// forgot to populate one cannot produce a URL with no host.
func TestNewDefaultsToLocalhost(t *testing.T) {
	hs := New(PublicURL{})
	assert.Equal(t, "http://localhost:8080", hs.PrimaryBaseURL())
}

func TestStateRoundTrip(t *testing.T) {
	st := NewState(New(mustParse(t, "http://localhost:8080")))
	assert.Equal(t, "http://localhost:8080", st.Get().PrimaryBaseURL())

	next := New(mustParse(t, "https://bloud.example.com"))
	st.Set(next)
	assert.Equal(t, "https://bloud.example.com", st.Get().PrimaryBaseURL())
}

func TestIsAddress(t *testing.T) {
	assert.True(t, IsAddress("10.0.0.210"))
	assert.False(t, IsAddress("bloud.example.com"))
	assert.False(t, IsAddress(""))
}
