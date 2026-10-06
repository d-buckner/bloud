// SPDX-License-Identifier: AGPL-3.0-only

package catalog

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestShippedCatalog_AgentAPIProviderWiresToItsExtraPort pins the shipped
// agent provider to the port its contract is served on.
//
// The generic contract-completeness tests prove the offer matches the registry.
// They cannot prove the offer points at a port that exists. `provides.agentApi.port`
// is a name resolved against `extraPorts`, and the resolver composes the
// address a consumer dials from whatever it finds there. An offer naming a port
// the app never declared would load cleanly and hand every consumer an address
// for a surface that is not there, which reads as a broken provider rather
// than as a catalog that lost a line.
func TestShippedCatalog_AgentAPIProviderWiresToItsExtraPort(t *testing.T) {
	apps, err := NewLoader(realCatalogDir(t)).LoadAll()
	require.NoError(t, err)

	spec, known := ContractFor("agentApi")
	require.True(t, known, "the agentApi contract must be in the registry")
	require.Equal(t, []string{"apiKey"}, spec.Secrets,
		"an agent endpoint authenticates with one bearer")

	provider := apps["hermes"]
	require.NotNil(t, provider, "hermes must be in the shipped catalog")

	offered, ok := provider.Provides["agentApi"]
	require.True(t, ok, "hermes must declare provides.agentApi")
	assert.Equal(t, spec.Secrets, offered.Secrets,
		"the provider must offer exactly the contract's secrets")

	// The offer names a port; that name must resolve to a declared extra port.
	require.NotEmpty(t, offered.Port,
		"the agent API is not the UI, so the offer must name its own port")
	ep := provider.ExtraPort(offered.Port)
	require.NotNil(t, ep,
		"provides.agentApi.port %q must name an entry in extraPorts", offered.Port)

	// The prefix is what both the Traefik route and the consumer's URL are
	// built from, so it has to be a usable absolute path.
	assert.True(t, len(ep.PathPrefix) > 1 && ep.PathPrefix[0] == '/',
		"the gateway port needs a non-root absolute pathPrefix, got %q", ep.PathPrefix)

	// The whole point of the extra port: it is not the UI port.
	assert.NotEqual(t, provider.Port, ep.Port,
		"the agent API must sit on a port other than the dashboard's")
}

// TestShippedCatalog_AgentAPIProviderIsNotLanPublished proves the exposure
// claim the design rests on.
//
// The agent API grants a tool-using, terminal-capable agent to whoever holds
// the bearer. The design routes it through the proxy specifically so the port
// is never published to the host, because a published port is on the LAN.
// This asserts the container declares no host-publishing mapping for the
// gateway port, so the claim cannot rot into a comment while a `ports:` entry
// quietly appears.
func TestShippedCatalog_AgentAPIProviderIsNotLanPublished(t *testing.T) {
	apps, err := NewLoader(realCatalogDir(t)).LoadAll()
	require.NoError(t, err)

	provider := apps["hermes"]
	require.NotNil(t, provider)

	ep := provider.ExtraPort("gateway")
	require.NotNil(t, ep, "hermes must declare a gateway extraPort")

	for _, container := range provider.Containers {
		for _, port := range container.Ports {
			assert.NotEqual(t, ep.Port, port.Host,
				"the agent gateway port %d must not be published to the host; "+
					"it is reached through the routed prefix so it never reaches the LAN",
				ep.Port)
		}
	}
}

// TestExtraPortNameMustResolve guards the resolver's failure mode directly.
//
// Falling back to the UI port when a name does not resolve would hand a
// consumer an address that connects, speaks the wrong protocol, and points at
// an app that did nothing wrong. The load has to fail instead.
func TestExtraPortNameMustResolve(t *testing.T) {
	app := &App{
		CatalogID:   "thing",
		DisplayName: "Thing",
		Description: "A thing",
		Category:    "productivity",
		Port:        8080,
		Provides: Provides{
			"agentApi": ContractProvides{
				Port:    "nope",
				Secrets: []string{"apiKey"},
			},
		},
	}

	err := validateProvides(app)
	require.Error(t, err, "an offer naming an undeclared port must fail the load")
	assert.Contains(t, err.Error(), "nope")
	assert.Contains(t, err.Error(), "extraPorts")
}

// TestExtraPortsRejectCollisions pins the two ways a second port can collide
// with the first. Both produce a router that answers with the wrong surface's
// bytes, which reads as a broken app rather than as a bad catalog.
func TestExtraPortsRejectCollisions(t *testing.T) {
	base := func() *App {
		return &App{
			CatalogID:   "thing",
			DisplayName: "Thing",
			Description: "A thing",
			Category:    "productivity",
			Port:        8080,
		}
	}

	t.Run("same port as the UI", func(t *testing.T) {
		app := base()
		app.ExtraPorts = []ExtraPort{{Name: "api", Port: 8080, PathPrefix: "/v1"}}
		err := validateExtraPorts(app)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "collides")
	})

	t.Run("duplicate name", func(t *testing.T) {
		app := base()
		app.ExtraPorts = []ExtraPort{
			{Name: "api", Port: 9001, PathPrefix: "/a"},
			{Name: "api", Port: 9002, PathPrefix: "/b"},
		}
		err := validateExtraPorts(app)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "twice")
	})

	// A prefix of "/" would make the extra router claim the whole host the UI
	// already answers on.
	t.Run("root prefix", func(t *testing.T) {
		app := base()
		app.ExtraPorts = []ExtraPort{{Name: "api", Port: 9001, PathPrefix: "/"}}
		require.Error(t, validateExtraPorts(app), "the root prefix belongs to the UI")
	})

	// A prefix is required: without one there is nothing to key the route on.
	t.Run("missing prefix", func(t *testing.T) {
		app := base()
		app.ExtraPorts = []ExtraPort{{Name: "api", Port: 9001}}
		require.Error(t, validateExtraPorts(app))
	})

	t.Run("a well-formed extra port loads", func(t *testing.T) {
		app := base()
		app.ExtraPorts = []ExtraPort{{Name: "api", Port: 9001, PathPrefix: "/v1"}}
		require.NoError(t, validateExtraPorts(app))
	})
}
