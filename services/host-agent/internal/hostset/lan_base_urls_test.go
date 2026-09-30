// SPDX-License-Identifier: AGPL-3.0-only

package hostset

import (
	"net"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The LAN IP entries in the registered base URL set are the only derived URLs
// that describe a socket rather than a name, so they take the entrypoint port
// and never the primary host's scheme.
//
// The regression these pin: with a https primary (home.thebloud.org behind a
// TLS terminator) the IP entries were built from PrimaryBaseURL(), which gave
// them https and dropped the port. http://10.0.0.210:8080 then redirected its
// OAuth login to https://10.0.0.210, and LAN access was dead for anyone not
// using the domain.

func lanURLsFor(t *testing.T, hs HostSet, servedPort int) []string {
	t.Helper()
	var out []string
	for _, raw := range hs.AllBaseURLs(servedPort) {
		u, err := url.Parse(raw)
		require.NoError(t, err, "base URL %q must parse", raw)
		if net.ParseIP(u.Hostname()) != nil {
			out = append(out, raw)
		}
	}
	if len(out) == 0 {
		t.Skip("no non-loopback IPv4 on this host")
	}
	return out
}

func TestAllBaseURLs_LANEntriesUseTheServedPort(t *testing.T) {
	hs := New([]string{"home.thebloud.org", "localhost"}, "home.thebloud.org").
		WithScheme("home.thebloud.org", SchemeHTTPS)

	for _, raw := range lanURLsFor(t, hs, 8080) {
		assert.True(t, strings.HasPrefix(raw, "http://"), "%q must be plain http", raw)
		u, err := url.Parse(raw)
		require.NoError(t, err)
		assert.Equal(t, "8080", u.Port(), "%q must name the entrypoint port", raw)
	}
}

func TestAllBaseURLs_LANEntriesNeverHTTPSWhateverThePublicScheme(t *testing.T) {
	hs := New([]string{"home.thebloud.org"}, "home.thebloud.org").
		WithPublicScheme(SchemeHTTPS)

	for _, port := range []int{0, 80, 443, 8080} {
		for _, raw := range hs.AllBaseURLs(port) {
			u, err := url.Parse(raw)
			require.NoError(t, err)
			if net.ParseIP(u.Hostname()) == nil {
				continue
			}
			assert.NotEqual(t, "https", u.Scheme,
				"a LAN IP base URL must never be https (%q, servedPort %d)", raw, port)
		}
	}
}

func TestAllBaseURLs_DefaultServedPortOmitsPort(t *testing.T) {
	hs := New([]string{"bloud.example.com"}, "bloud.example.com")

	for _, raw := range lanURLsFor(t, hs, 80) {
		u, err := url.Parse(raw)
		require.NoError(t, err)
		assert.Empty(t, u.Port(), "%q should not carry an explicit :80", raw)
	}
}

// The named hosts are untouched by any of this: the scheme a host carries still
// drives its own base URL, and only the IP entries are forced to plain http.
func TestAllBaseURLs_NamedHostsKeepTheirScheme(t *testing.T) {
	hs := New([]string{"home.thebloud.org", "lan.local", "localhost"}, "home.thebloud.org").
		WithScheme("home.thebloud.org", SchemeHTTPS)

	assert.Equal(t, "https://home.thebloud.org", hs.BaseURLFor("home.thebloud.org"))
	assert.Equal(t, "http://lan.local", hs.BaseURLFor("lan.local"))
	assert.Equal(t, "http://localhost:8080", hs.BaseURLFor("localhost"))

	bases := hs.AllBaseURLs(8080)
	require.Contains(t, bases, "https://home.thebloud.org")
	require.Contains(t, bases, "http://lan.local")
	require.Contains(t, bases, "http://localhost:8080")
}
