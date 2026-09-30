// SPDX-License-Identifier: AGPL-3.0-only

package hostset

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The dial plan is the layer that made a proxied https deployment work: under
// https the container gets no host-gateway pin, because the terminator is not
// this box and Bloud serves no certificate at the gateway. Pinning sends the
// container to a port nothing answers on TLS.
func TestIssuerExtraHostFollowsTheScheme(t *testing.T) {
	tls := New(mustParse(t, "https://home.thebloud.org"))
	assert.Equal(t, "", tls.IssuerExtraHost(),
		"an https issuer must be resolved by real DNS, not pinned to the gateway")

	plain := New(mustParse(t, "http://home.thebloud.org"))
	assert.Equal(t, "home.thebloud.org:host-gateway", plain.IssuerExtraHost())
}

// Each ProxyConsistency issue names one layer, so a failed login points at one
// setting instead of three candidates.
func TestProxyConsistency(t *testing.T) {
	t.Run("https with no named proxy is reported", func(t *testing.T) {
		hs := New(mustParse(t, "https://home.test"))
		codes := issueCodes(hs.ProxyConsistency(nil, false))
		assert.Contains(t, codes, "https_without_trusted_proxy_nets")
	})

	t.Run("https with a named proxy is clean", func(t *testing.T) {
		hs := New(mustParse(t, "https://home.test"))
		assert.Empty(t, issueCodes(hs.ProxyConsistency([]string{"10.0.0.1/32"}, false)))
	})

	t.Run("a proxy present but the scheme still http is reported", func(t *testing.T) {
		hs := New(mustParse(t, "http://home.test"))
		codes := issueCodes(hs.ProxyConsistency([]string{"10.0.0.1/32"}, false))
		assert.Contains(t, codes, "proxy_present_but_scheme_is_http")
	})

	t.Run("plain http with no proxy is clean", func(t *testing.T) {
		hs := New(mustParse(t, "http://home.test"))
		assert.Empty(t, issueCodes(hs.ProxyConsistency(nil, false)))
	})
}

// A https issuer on a special-use name is genuinely undeployable: .local is
// mDNS and the localhost family names the container's own loopback, so neither
// resolves to a TLS endpoint from inside a container. Naming it here beats a
// stalled login later.
func TestDeployabilitySpecialUseNames(t *testing.T) {
	// The parser accepts a .local host only as a built-in alias, so reach the
	// derivation through Resolve with a base domain that is not special.
	hs := New(mustParse(t, "https://home.test"))
	assert.Empty(t, hs.Deployability(false), "a real name resolves fine")

	local := New(PublicURL{Scheme: SchemeHTTPS, Host: "bloud.local"})
	codes := issueCodes(local.Deployability(false))
	assert.Contains(t, codes, "https_issuer_host_not_resolvable")

	// Terminating TLS at Traefik makes the gateway hop valid again.
	assert.Empty(t, local.Deployability(true))
}

// The canary: if the scheme stops reaching the derivation, every https
// assertion above would still pass while the derived URLs silently reverted to
// plain http. This fails if setting the scheme changes nothing.
func TestCanarySchemeActuallyChangesDerivedURLs(t *testing.T) {
	plain := New(mustParse(t, "http://home.test"))
	tls := New(mustParse(t, "https://home.test"))

	assert.NotEqual(t, plain.PrimaryBaseURL(), tls.PrimaryBaseURL())
	assert.NotEqual(t, plain.IssuerBaseURL(), tls.IssuerBaseURL())
	assert.NotEqual(t, plain.IssuerExtraHost(), tls.IssuerExtraHost())
	assert.NotEqual(t, plain.PublicScheme(), tls.PublicScheme())
}

func issueCodes(issues []Issue) []string {
	out := make([]string, 0, len(issues))
	for _, i := range issues {
		out = append(out, i.Code)
	}
	return out
}
