// SPDX-License-Identifier: AGPL-3.0-only

package hostset

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

// A stored per-host scheme is the more specific statement than the
// deployment-wide BLOUD_PUBLIC_SCHEME, so it wins for that host. This is the
// precedence the UI depends on: what Settings -> Hosts shows for a host is
// what its redirect URI is registered as, and a deployment-wide default must
// not silently overwrite a per-host choice.
func TestStoredSchemeBeatsDeploymentWidePublicScheme(t *testing.T) {
	hs, err := Resolve(Input{
		Stored: []StoredHost{
			{Hostname: "secure.example.com", Primary: true, Scheme: "https"},
			{Hostname: "plain.example.com", Scheme: "http"},
			{Hostname: "inherit.example.com"},
		},
		PublicScheme: "http",
	})
	require.NoError(t, err)
	assert.Equal(t, "https://secure.example.com", hs.BaseURLFor("secure.example.com"),
		"the stored https must survive a deployment-wide http default")
	assert.Equal(t, "http://plain.example.com", hs.BaseURLFor("plain.example.com"))
	// No stored statement means the deployment-wide value applies.
	assert.Equal(t, "http://inherit.example.com", hs.BaseURLFor("inherit.example.com"))
}

// The reverse direction: a deployment-wide https reaches a stored host that
// never stated a scheme, so an operator behind a TLS terminator does not have
// to set every host by hand.
func TestPublicSchemeReachesStoredHostsWithoutAStoredScheme(t *testing.T) {
	hs, err := Resolve(Input{
		Stored: []StoredHost{
			{Hostname: "a.example.com", Primary: true},
			{Hostname: "b.example.com"},
		},
		PublicScheme: "https",
	})
	require.NoError(t, err)
	assert.Equal(t, "https://a.example.com", hs.BaseURLFor("a.example.com"))
	assert.Equal(t, "https://b.example.com", hs.BaseURLFor("b.example.com"))
	assert.Equal(t, SchemeHTTPS, hs.PublicScheme())
}

// The issuer and the primary must agree, and both must be on the scheme the
// operator's proxy actually serves. localhost stays in the redirect list on
// its own origin: that entry is for local access, not for the public host.
func TestHTTPSPrimaryKeepsIssuerAndPrimaryOnSameOrigin(t *testing.T) {
	hs, err := Resolve(Input{
		Stored: []StoredHost{{Hostname: "bloud.example.com", Primary: true, Scheme: "https"}},
	})
	require.NoError(t, err)
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
