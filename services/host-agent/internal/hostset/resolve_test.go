// SPDX-License-Identifier: AGPL-3.0-only

package hostset

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Resolve is the startup path: one stored value, three legacy env knobs, and a
// default. The precedence is what decides whether an operator's Settings save
// survives a restart, so each pair of sources needs its own case.
func TestResolvePrecedence(t *testing.T) {
	t.Run("stored wins over every env knob", func(t *testing.T) {
		hs, err := Resolve(Input{
			StoredURL:  "https://stored.test",
			SSOBaseURL: "https://env-sso.test",
			BaseDomain: "env-domain.test",
		})
		require.NoError(t, err)
		assert.Equal(t, "https://stored.test", hs.PrimaryBaseURL())
	})

	t.Run("sso base url wins over base domain", func(t *testing.T) {
		hs, err := Resolve(Input{
			SSOBaseURL: "https://env-sso.test:8443",
			BaseDomain: "env-domain.test",
		})
		require.NoError(t, err)
		assert.Equal(t, "https://env-sso.test:8443", hs.PrimaryBaseURL())
	})

	t.Run("base domain is the last env knob", func(t *testing.T) {
		hs, err := Resolve(Input{BaseDomain: "env-domain.test"})
		require.NoError(t, err)
		assert.Equal(t, "http://env-domain.test", hs.PrimaryBaseURL())
	})

	t.Run("nothing configured lands on the default", func(t *testing.T) {
		hs, err := Resolve(Input{})
		require.NoError(t, err)
		assert.Equal(t, DefaultPublicURL, hs.PrimaryBaseURL())
	})
}

// BLOUD_PUBLIC_SCHEME fills in a scheme only when the winning source did not
// state one. A stored URL and BLOUD_SSO_BASE_URL are full origins and carry
// their own, so the env knob must not silently rewrite them: that would move
// every redirect URI out from under a value the operator typed deliberately.
func TestPublicSchemeOnlyFillsAnUnstatedScheme(t *testing.T) {
	t.Run("applies to a bare base domain", func(t *testing.T) {
		hs, err := Resolve(Input{BaseDomain: "home.test", PublicScheme: "https"})
		require.NoError(t, err)
		assert.Equal(t, "https://home.test", hs.PrimaryBaseURL())
		assert.Equal(t, SchemeHTTPS, hs.PublicScheme())
	})

	t.Run("does not override a stored origin", func(t *testing.T) {
		hs, err := Resolve(Input{StoredURL: "http://plain.test", PublicScheme: "https"})
		require.NoError(t, err)
		assert.Equal(t, "http://plain.test", hs.PrimaryBaseURL(),
			"a stored http must survive a deployment-wide https")
	})

	t.Run("does not override an sso base url", func(t *testing.T) {
		hs, err := Resolve(Input{SSOBaseURL: "http://plain.test", PublicScheme: "https"})
		require.NoError(t, err)
		assert.Equal(t, "http://plain.test", hs.PrimaryBaseURL())
	})
}

// A scheme the operator never typed still has to be one the address can honor.
// Turning a bare address into an https origin is how a login ends up pointed at
// a port nothing answers on TLS, so this is an error rather than a surprise.
func TestPublicSchemeCannotMakeAnAddressHTTPS(t *testing.T) {
	_, err := Resolve(Input{BaseDomain: "10.0.0.210", PublicScheme: "https"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no CA issues a certificate")
}

func TestResolveRejectsBadValues(t *testing.T) {
	_, err := Resolve(Input{StoredURL: "https://stored.test/path"})
	require.Error(t, err, "a stored value with a path must not resolve")

	_, err = Resolve(Input{BaseDomain: "NOT A HOST"})
	require.Error(t, err)

	_, err = Resolve(Input{BaseDomain: "home.test", PublicScheme: "sftp"})
	require.Error(t, err)
}

// The served port has to survive the set being built, or the detected LAN URLs
// render on 80 while the entrypoint serves something else and the redirect
// URIs get re-registered against a port nothing answers on.
func TestServedPortReachesTheLANURLs(t *testing.T) {
	hs, err := Resolve(Input{StoredURL: "https://home.test", ServedPort: 8080})
	require.NoError(t, err)
	assert.Equal(t, 8080, hs.ServedPort())
	assert.Equal(t, hs.WithServedPort(8080).AllBaseURLs(), hs.AllBaseURLs())
}

// The public origin, the aliases, and the LAN addresses all get registered, so
// login works from the domain, from the box itself, and by address.
func TestAllBaseURLsCoversEveryReachableOrigin(t *testing.T) {
	hs, err := Resolve(Input{
		StoredURL:  "https://home.thebloud.org",
		ServedPort: 8080,
	})
	require.NoError(t, err)

	bases := hs.AllBaseURLs()
	assert.Contains(t, bases, "https://home.thebloud.org")
	assert.Contains(t, bases, "http://localhost:8080")
	assert.Contains(t, bases, "http://bloud.local")
	assert.Equal(t, bases[0], "https://home.thebloud.org", "the public origin registers first")
}

// AllBaseURLs dedupes, so an address that is both the public host and a
// detected LAN address appears once rather than as two redirect URIs.
func TestAllBaseURLsDedupes(t *testing.T) {
	hs := New(mustParse(t, "http://localhost:8080")).WithServedPort(8080)
	seen := map[string]bool{}
	for _, u := range hs.AllBaseURLs() {
		assert.False(t, seen[u], "%q appears twice", u)
		seen[u] = true
	}
}
