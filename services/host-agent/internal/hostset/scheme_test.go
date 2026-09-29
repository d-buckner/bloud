// SPDX-License-Identifier: AGPL-3.0-only

package hostset

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeScheme(t *testing.T) {
	cases := map[string]string{
		"https":  "https",
		"HTTPS":  "https",
		"  http": "http",
		"http ":  "http",
		"":       "",
		"ftp":    "",
		"htps":   "",
		"ws":     "",
	}
	for in, want := range cases {
		assert.Equal(t, want, NormalizeScheme(in), "NormalizeScheme(%q)", in)
	}
}

// A stored https scheme is what makes a TLS-terminated host reachable: the
// redirect URI and the issuer have to name https, and Bloud cannot learn that
// from its own socket.
func TestResolveStoredSchemeProducesHTTPS(t *testing.T) {
	hs, err := Resolve(Input{
		Stored: []StoredHost{
			{Hostname: "bloud.example.com", Primary: true, Scheme: "https"},
			{Hostname: "plain.example.com", Scheme: "http"},
			{Hostname: "unset.example.com"},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "bloud.example.com", hs.Primary())
	assert.Equal(t, "https://bloud.example.com", hs.BaseURLFor("bloud.example.com"))
	assert.Equal(t, "https://bloud.example.com", hs.PrimaryBaseURL())
	assert.Equal(t, "https://bloud.example.com", hs.IssuerBaseURL())
	// http and unset both land on the default mapping.
	assert.Equal(t, "http://plain.example.com", hs.BaseURLFor("plain.example.com"))
	assert.Equal(t, "http://unset.example.com", hs.BaseURLFor("unset.example.com"))
}

// Built-ins keep their fixed mapping. localhost is http://localhost:8080 by
// dev/e2e convention and no public CA issues for it or bloud.local, so a
// stored scheme on either would be a promise the install cannot keep.
func TestResolveSchemeIgnoredForBuiltinHosts(t *testing.T) {
	hs, err := Resolve(Input{
		Stored: []StoredHost{
			{Hostname: "localhost", Scheme: "https"},
			{Hostname: "bloud.local", Scheme: "https"},
			{Hostname: "real.example.com", Scheme: "https"},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "http://localhost:8080", hs.BaseURLFor("localhost"))
	assert.Equal(t, "http://bloud.local", hs.BaseURLFor("bloud.local"))
	assert.Equal(t, "https://real.example.com", hs.BaseURLFor("real.example.com"))
}

// The legacy env path is untouched by stored-scheme handling: with no stored
// hosts, BLOUD_SSO_BASE_URL still wins and seeds its own override.
func TestResolveEnvPathStillWorks(t *testing.T) {
	hs, err := Resolve(Input{SSOBaseURL: "https://sso.example.com"})
	require.NoError(t, err)
	assert.Equal(t, "sso.example.com", hs.Primary())
	assert.Equal(t, "https://sso.example.com", hs.PrimaryBaseURL())
}

func TestNewWithSchemes(t *testing.T) {
	hs := NewWithSchemes([]string{"localhost", "bloud.local", "a.example.com"}, "a.example.com", map[string]string{
		"a.example.com": "https",
	})
	assert.Equal(t, "https://a.example.com", hs.BaseURLFor("a.example.com"))

	// A scheme for a host that is not in the set is still applied by name,
	// but must not add the host to the set.
	hs2 := NewWithSchemes([]string{"localhost", "bloud.local"}, "localhost", map[string]string{
		"ghost.example.com": "https",
	})
	assert.Equal(t, []string{"localhost", "bloud.local"}, hs2.Hosts())
}

// The issuer and the primary must agree, and both must be on the scheme the
// operator's proxy actually serves. localhost stays in the redirect list on
// its own origin: that entry is for local access, not for the public host.
func TestHTTPSPrimaryKeepsIssuerAndPrimaryOnSameOrigin(t *testing.T) {
	hs := NewWithSchemes([]string{"localhost", "bloud.local", "bloud.example.com"}, "bloud.example.com", map[string]string{
		"bloud.example.com": "https",
	})
	assert.Equal(t, "https://bloud.example.com", hs.PrimaryBaseURL())
	assert.Equal(t, hs.PrimaryBaseURL(), hs.IssuerBaseURL(),
		"the issuer must be the primary's own base URL, not a differently-scheme'd one")
	assert.Equal(t, "bloud.example.com", hs.IssuerHost())

	// The primary is listed first, so the OAuth client registers it first.
	require.NotEmpty(t, hs.BaseURLs())
	assert.Equal(t, "https://bloud.example.com", hs.BaseURLs()[0])
	// Local access keeps working alongside the public host.
	assert.Contains(t, hs.BaseURLs(), "http://localhost:8080")
}
