// SPDX-License-Identifier: AGPL-3.0-only

package hostset

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Scenario: the real deployment. Stored primary host is a name served over https
// behind a TLS terminator, Traefik serves 8080.
//
// This is the topology that was reported broken. Every derived URL for the named
// host must be https on the default port, and the LAN entries must stay plain
// http on the served port.
func TestScenario_ProxiedHTTPSDomainPrimary(t *testing.T) {
	hs, err := Resolve(Input{
		Stored:     []StoredHost{{Hostname: "home.thebloud.org", Primary: true, Scheme: "https"}},
		ServedPort: 8080,
	})
	require.NoError(t, err)

	assert.Equal(t, "https://home.thebloud.org", hs.PrimaryBaseURL())
	assert.Equal(t, "https://home.thebloud.org", hs.IssuerBaseURL())
	assert.Equal(t, SchemeHTTPS, hs.PublicScheme())

	bases := hs.AllBaseURLs()
	assert.Contains(t, bases, "https://home.thebloud.org")
	assert.Contains(t, bases, "http://localhost:8080")
	assert.Contains(t, bases, "http://bloud.local")
}
