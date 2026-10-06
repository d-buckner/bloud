// SPDX-License-Identifier: AGPL-3.0-only

package traefikgen

import (
	"strconv"
	"strings"
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGenerate_ExtraPortGetsItsOwnRouter pins the second-surface route shape.
//
// An app with a declared extra port has to get a router that branches on that
// port's prefix to that port's backend, while the app's own router keeps the
// root for the UI. The two coexist on one host, so the rule and the priority
// are the whole contract: get the priority wrong and the UI catch-all wins,
// and the prefix silently never reaches the service it names.
func TestGenerate_ExtraPortGetsItsOwnRouter(t *testing.T) {
	g := NewGenerator("/tmp/unused-routes.yml")
	out := g.Preview([]*catalog.App{{
		CatalogID: "hermes",
		Port:      9119,
		ExtraPorts: []catalog.ExtraPort{
			{Name: "gateway", Port: 8642, PathPrefix: "/v1"},
		},
	}})

	// The router: same host as the UI, branched by prefix, higher priority.
	assert.Contains(t, out, "    hermes-gateway:\n",
		"the extra port needs a router named <app>-<port>")
	assert.Contains(t, out, "HostRegexp(`^hermes\\\\.`) && PathPrefix(`/v1`)",
		"the router must match the app host AND the declared prefix")
	assert.Contains(t, out, "      service: hermes-gateway\n",
		"the router must point at its own service")

	// The service: dialed on the extra port, not the UI port.
	assert.Contains(t, out, `          - url: "http://localhost:8642"`,
		"the service must target the extra port")

	// The UI route is untouched.
	assert.Contains(t, out, "    hermes:\n")
	assert.Contains(t, out, `          - url: "http://localhost:9119"`)
}

// TestGenerate_ExtraPortRouterOutranksTheUIRouter is the priority check.
//
// The UI router is a catch-all on the app's host. If the extra-port router did
// not outrank it, requests under the prefix would be served by the dashboard,
// which is a wrong-answer failure rather than an error: the consumer gets a
// 200 with HTML where it expected JSON.
func TestGenerate_ExtraPortRouterOutranksTheUIRouter(t *testing.T) {
	g := NewGenerator("/tmp/unused-routes.yml")
	out := g.Preview([]*catalog.App{{
		CatalogID:  "hermes",
		Port:       9119,
		ExtraPorts: []catalog.ExtraPort{{Name: "gateway", Port: 8642, PathPrefix: "/v1"}},
	}})

	uiPriority := routerPriority(t, out, "    hermes:\n")
	extraPriority := routerPriority(t, out, "    hermes-gateway:\n")
	assert.Greater(t, extraPriority, uiPriority,
		"the prefix router must outrank the UI catch-all")
}

// TestGenerate_NoExtraPortsIsUnchanged guards against the primitive changing
// the output for every app that does not use it.
//
// The generator's output is watched by Traefik and compared byte-for-byte for
// the idempotence skip, so an unconditional extra section would reshuffle
// every existing route and reload the whole dynamic config on every pass.
func TestGenerate_NoExtraPortsIsUnchanged(t *testing.T) {
	g := NewGenerator("/tmp/unused-routes.yml")
	out := g.Preview([]*catalog.App{{CatalogID: "sonarr", Port: 8989}})

	assert.NotContains(t, out, "sonarr-", "no extra-port artifacts for an app that declares none")
	assert.Contains(t, out, `          - url: "http://localhost:8989"`)
}

// TestGenerate_ExtraPortIsIdempotent keeps the steady state silent.
func TestGenerate_ExtraPortIsIdempotent(t *testing.T) {
	apps := []*catalog.App{{
		CatalogID:  "hermes",
		Port:       9119,
		ExtraPorts: []catalog.ExtraPort{{Name: "gateway", Port: 8642, PathPrefix: "/v1"}},
	}}
	g := NewGenerator("/tmp/unused-routes.yml")
	assert.Equal(t, g.Preview(apps), g.Preview(apps),
		"the same catalog must render byte-identical config")
}

// routerPriority reads the `priority:` line of the named router block.
func routerPriority(t *testing.T, out, header string) int {
	t.Helper()
	start := strings.Index(out, header)
	require.NotEqual(t, -1, out, "router %q not found in generated config", header)
	rest := out[start+len(header):]
	for _, line := range strings.Split(rest, "\n") {
		if strings.HasPrefix(line, "      priority:") {
			p, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "      priority:")))
			require.NoError(t, err, "priority must parse as a number")
			return p
		}
		if strings.HasPrefix(line, "    ") && !strings.HasPrefix(line, "      ") {
			break
		}
	}
	require.Fail(t, "no priority line found for router %q", header)
	return 0
}
