// SPDX-License-Identifier: AGPL-3.0-only

package hostset

import (
	"net"
	"net/url"
	"strings"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/netutil"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An address is not a name, and the two derive their URLs differently.
//
// A name's port comes from its scheme, because DNS is what makes the origin:
// https://<host> means 443 and something answers there. An address has no such
// contract. It means whatever port the socket was opened on, and it has no
// certificate story at all.
//
// The regressions these pin, in the order they were reported:
//
//  1. An https primary made every detected LAN IP URL https://<ip>, a scheme
//     nothing serves on an address.
//  2. First-run adoption took the address the install was created from as the
//     primary host, and BaseURLFor rendered it as http://<ip> on port 80 while
//     Traefik served 8080, so the login redirect was refused.

func lanURLsFor(t *testing.T, hs HostSet) []string {
	t.Helper()
	var out []string
	for _, raw := range hs.AllBaseURLs() {
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
		WithScheme("home.thebloud.org", SchemeHTTPS).
		WithServedPort(8080)

	for _, raw := range lanURLsFor(t, hs) {
		assert.True(t, strings.HasPrefix(raw, "http://"), "%q must be plain http", raw)
		u, err := url.Parse(raw)
		require.NoError(t, err)
		assert.Equal(t, "8080", u.Port(), "%q must name the entrypoint port", raw)
	}
}

func TestAllBaseURLs_LANEntriesNeverHTTPSWhateverThePublicScheme(t *testing.T) {
	for _, public := range []Scheme{SchemeHTTP, SchemeHTTPS} {
		for _, port := range []int{0, 80, 443, 8080} {
			hs := New([]string{"home.thebloud.org"}, "home.thebloud.org").
				WithPublicScheme(public).
				WithServedPort(port)
			for _, raw := range hs.AllBaseURLs() {
				u, err := url.Parse(raw)
				require.NoError(t, err)
				if net.ParseIP(u.Hostname()) == nil {
					continue
				}
				assert.NotEqual(t, "https", u.Scheme,
					"a LAN IP base URL must never be https (%q, public %s, servedPort %d)", raw, public, port)
			}
		}
	}
}

func TestAllBaseURLs_DefaultServedPortOmitsPort(t *testing.T) {
	hs := New([]string{"bloud.example.com"}, "bloud.example.com").WithServedPort(80)

	for _, raw := range lanURLsFor(t, hs) {
		u, err := url.Parse(raw)
		require.NoError(t, err)
		assert.Empty(t, u.Port(), "%q should not carry an explicit :80", raw)
	}
}

// The named hosts are untouched by any of this: the scheme a host carries still
// drives its own base URL, and only the address entries are forced onto the
// entrypoint port.
func TestAllBaseURLs_NamedHostsKeepTheirScheme(t *testing.T) {
	hs := New([]string{"home.thebloud.org", "lan.local", "localhost"}, "home.thebloud.org").
		WithScheme("home.thebloud.org", SchemeHTTPS).
		WithServedPort(8080)

	assert.Equal(t, "https://home.thebloud.org", hs.BaseURLFor("home.thebloud.org"))
	assert.Equal(t, "http://lan.local", hs.BaseURLFor("lan.local"))
	assert.Equal(t, "http://localhost:8080", hs.BaseURLFor("localhost"))

	bases := hs.AllBaseURLs()
	require.Contains(t, bases, "https://home.thebloud.org")
	require.Contains(t, bases, "http://lan.local")
	require.Contains(t, bases, "http://localhost:8080")
}

// The reported case: the install was created from the LAN address, so the
// address became the primary host. It has to render on the port the entrypoint
// actually serves, not on the http default.
func TestBaseURLFor_AddressHostRendersOnTheServedPort(t *testing.T) {
	hs := New([]string{"10.0.0.210", "localhost"}, "10.0.0.210").WithServedPort(8080)

	assert.Equal(t, "http://10.0.0.210:8080", hs.BaseURLFor("10.0.0.210"))
	assert.Equal(t, "http://10.0.0.210:8080", hs.PrimaryBaseURL())
	assert.Equal(t, "http://10.0.0.210:8080", hs.IssuerBaseURL())
}

func TestBaseURLFor_AddressHostOnDefaultPortOmitsPort(t *testing.T) {
	hs := New([]string{"10.0.0.210"}, "10.0.0.210").WithServedPort(80)
	assert.Equal(t, "http://10.0.0.210", hs.BaseURLFor("10.0.0.210"))

	// An unstated port is the http default too, never a bare colon.
	bare := New([]string{"10.0.0.210"}, "10.0.0.210")
	assert.Equal(t, "http://10.0.0.210", bare.BaseURLFor("10.0.0.210"))
}

// The address and the name must not disagree about the same host. Before the
// split was drawn in BaseURLFor, AllBaseURLs said http://<ip>:8080 and
// BaseURLFor said http://<ip> for the very same address, and whichever path a
// caller took decided whether the login worked.
func TestBaseURLFor_AddressAgreesWithTheLANEntry(t *testing.T) {
	ips := netutil.DetectLocalIPs()
	if len(ips) == 0 {
		t.Skip("no non-loopback IPv4 on this host")
	}
	hs := New([]string{ips[0]}, ips[0]).WithServedPort(8080)

	base := hs.BaseURLFor(ips[0])
	require.Contains(t, hs.AllBaseURLs(), base,
		"an address in the set must derive to the same URL the LAN entries produce")
}

// A deployment-wide https must not turn a LAN address into an https origin: no
// CA issues for a bare address in the story Bloud ships.
func TestWithPublicScheme_SkipsAddressHosts(t *testing.T) {
	hs := New([]string{"10.0.0.210", "home.thebloud.org"}, "home.thebloud.org").
		WithPublicScheme(SchemeHTTPS)

	assert.Equal(t, "https://home.thebloud.org", hs.BaseURLFor("home.thebloud.org"))
	assert.Equal(t, "http://10.0.0.210:8080", hs.WithServedPort(8080).BaseURLFor("10.0.0.210"))
}

func TestIsAddress(t *testing.T) {
	assert.True(t, IsAddress("10.0.0.210"))
	assert.True(t, IsAddress("192.168.1.1"))
	assert.False(t, IsAddress("localhost"))
	assert.False(t, IsAddress("home.thebloud.org"))
	assert.False(t, IsAddress("bloud.local"))
	assert.False(t, IsAddress(""))

	// IPv6 cannot reach this path at all: Normalize rejects it, so an IPv6
	// literal never enters the host set. Worth stating rather than testing
	// IsAddress on a string the set cannot hold.
	assert.Empty(t, Normalize("::1"))
}

// The served port has to survive every copy the set goes through, or a SetHosts
// intent silently drops it and an address-hosted install is back on port 80.
func TestServedPortSurvivesCopies(t *testing.T) {
	hs := New([]string{"10.0.0.210", "home.thebloud.org"}, "10.0.0.210").WithServedPort(8080)

	assert.Equal(t, 8080, hs.WithSchemes(map[string]Scheme{"home.thebloud.org": SchemeHTTPS}).ServedPort())
	assert.Equal(t, 8080, hs.WithScheme("home.thebloud.org", SchemeHTTPS).ServedPort())
	assert.Equal(t, 8080, hs.WithPublicScheme(SchemeHTTPS).ServedPort())
	assert.Equal(t, 8080, hs.WithURLOverride("home.thebloud.org", "https://home.thebloud.org").ServedPort())
	assert.Equal(t, "http://10.0.0.210:8080",
		hs.WithSchemes(map[string]Scheme{"home.thebloud.org": SchemeHTTPS}).BaseURLFor("10.0.0.210"))
}

func TestResolve_AppliesServedPort(t *testing.T) {
	hs, err := Resolve(Input{
		Stored:     []StoredHost{{Hostname: "10.0.0.210", Primary: true, Scheme: "http"}},
		ServedPort: 8080,
	})
	require.NoError(t, err)
	assert.Equal(t, 8080, hs.ServedPort())
	assert.Equal(t, "http://10.0.0.210:8080", hs.PrimaryBaseURL())

	// The env-only path carries it too.
	hs, err = Resolve(Input{BaseDomain: "home.thebloud.org", ServedPort: 9000})
	require.NoError(t, err)
	assert.Equal(t, 9000, hs.ServedPort())

	// And the legacy SSO base URL early-return path.
	hs, err = Resolve(Input{SSOBaseURL: "https://home.thebloud.org", ServedPort: 9000})
	require.NoError(t, err)
	assert.Equal(t, 9000, hs.ServedPort())
}
