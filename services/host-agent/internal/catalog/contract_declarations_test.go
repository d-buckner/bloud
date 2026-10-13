// SPDX-License-Identifier: AGPL-3.0-only

package catalog

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestShippedCatalog_ContractDeclarationsAreComplete checks the shipped catalog
// for the pairing no compiler sees: every app that integrates with another under
// a contract carrying a payload must find that provider declaring the same
// contract.
//
// A missing declaration is not an error anywhere: the consumer receives an empty
// payload, and an empty payload is also the legitimate "the provider has not
// published yet" state. So the stack would keep running with the link silently
// unwired and a warning per pass, which is exactly the failure a test has to
// catch. Contracts that carry only an address (proxy, downloadClient, database)
// need no declaration: the address is the edge.
func TestShippedCatalog_ContractDeclarationsAreComplete(t *testing.T) {
	apps, err := NewLoader(realCatalogDir(t)).LoadAll()
	require.NoError(t, err)

	checked := 0
	for consumerID, consumer := range apps {
		for contract, integration := range consumer.Integrations {
			spec, known := ContractFor(contract)
			if !known || (len(spec.Secrets) == 0 && len(spec.Values) == 0) {
				// An address-only contract, or a label whose contract is not
				// part of the framework's vocabulary yet.
				continue
			}
			for _, compatible := range integration.Compatible {
				provider, shipped := apps[compatible.App]
				if !shipped {
					continue
				}
				checked++
				_, declared := provider.Provides[contract]
				assert.True(t, declared,
					"%s integrates with %s as %q, but %s does not declare provides.%s",
					consumerID, compatible.App, contract, compatible.App, contract)
			}
		}
	}
	assert.NotZero(t, checked, "the shipped catalog should contain at least one payload-carrying contract")
}

// TestShippedCatalog_AppAPIWiring pins the wrapper chain the `appApi` contract
// exists for: affine-mcp consumes it and affine provides it, with the one secret
// and the one runtime value the wrapper's generated config needs.
//
// The generic pairing test above cannot see this. A provider that forgets the
// runtime `username` still loads; the consumer receives an empty username beside
// a real password and writes no credential at all, because the wrapper treats a
// half-filled binding as "not ready". That is a silently unwired integration, so
// the declaration is asserted here rather than left to a runtime warning.
func TestShippedCatalog_AppAPIWiring(t *testing.T) {
	apps, err := NewLoader(realCatalogDir(t)).LoadAll()
	require.NoError(t, err)

	spec, known := ContractFor("appApi")
	require.True(t, known, "appApi must be in the registry")
	require.Equal(t, []string{"password"}, spec.Secrets)

	affine := apps["affine"]
	require.NotNil(t, affine, "affine must be in the shipped catalog")
	offer, ok := affine.Provides["appApi"]
	require.True(t, ok, "affine must provide appApi")
	assert.Equal(t, spec.Secrets, offer.Secrets,
		"the provider must publish exactly the contract's secret")
	assert.Contains(t, offer.RuntimeValues, "username",
		"the owner identity is the operator's address, so it is published at runtime")
	assert.Contains(t, offer.RuntimeValues, "workspaceId",
		"the shared workspace id is published so a wrapper can pin its default scope")

	wrapper := apps["affine-mcp"]
	require.NotNil(t, wrapper, "affine-mcp must be in the shipped catalog")
	integration, ok := wrapper.Integrations["appApi"]
	require.True(t, ok, "affine-mcp must declare integrations.appApi")
	assert.True(t, integration.Required,
		"the wrapper cannot authenticate without AFFiNE's credential, so the contract is required")
	assert.Equal(t, []string{"password"}, integration.Requires,
		"the password is the whole consumer surface; the username arrives unasked")

	offered, ok := wrapper.Provides["mcp"]
	require.True(t, ok, "affine-mcp must provide mcp")
	assert.Equal(t, "affine-mcp", offered.Values["serverName"],
		"the wrapper's namespace must differ from affine's or a harness would collide")
	assert.Equal(t, "/mcp", offered.Values["path"])
}

// TestShippedCatalog_AppAPIWiring_HomeAssistant pins the second `appApi` chain:
// homeassistant provides it and ha-mcp consumes it.
//
// The provider side differs from AFFiNE's on purpose, and the difference is the
// reason this is a separate assertion rather than a loop. AFFiNE publishes
// `username` as a runtime value because its owner's address is an email the
// operator typed and Bloud cannot know it until AFFiNE is up. Home Assistant's
// owner is the account Bloud itself created, with a name that has been metadata
// since the app was added, so the value is static and there is nothing to
// discover. A consumer waiting for a runtime username here would wait forever for
// a fact that was always known.
//
// What the wrapper publishes matters more than what it consumes: the `mcp` offer
// is the whole security promise to every harness, and it must name the bearer and
// nothing else. The server's internal path is deliberately absent, and it cannot
// be added without also adding it to the contract, which is a change to a core
// vocabulary rather than to this app.
func TestShippedCatalog_AppAPIWiring_HomeAssistant(t *testing.T) {
	apps, err := NewLoader(realCatalogDir(t)).LoadAll()
	require.NoError(t, err)

	spec, known := ContractFor("appApi")
	require.True(t, known, "appApi must be in the registry")

	ha := apps["homeassistant"]
	require.NotNil(t, ha, "homeassistant must be in the shipped catalog")
	offer, ok := ha.Provides["appApi"]
	require.True(t, ok, "homeassistant must provide appApi")
	assert.Equal(t, spec.Secrets, offer.Secrets,
		"the provider must publish exactly the contract's secret")
	assert.Equal(t, "bloud-bootstrap-admin", offer.Values["username"],
		"the owner account is Bloud's own bootstrap admin, named in metadata")
	assert.NotContains(t, offer.RuntimeValues, "username",
		"the account name is known before Home Assistant is up, so it is not a runtime value")

	wrapper := apps["ha-mcp"]
	require.NotNil(t, wrapper, "ha-mcp must be in the shipped catalog")
	integration, ok := wrapper.Integrations["appApi"]
	require.True(t, ok, "ha-mcp must declare integrations.appApi")
	assert.True(t, integration.Required,
		"the wrapper cannot authenticate without Home Assistant's credential, so the contract is required")
	assert.Equal(t, []string{"password"}, integration.Requires,
		"the token is the whole consumer surface; the username arrives unasked")

	provided, ok := wrapper.Provides["mcp"]
	require.True(t, ok, "ha-mcp must provide mcp")
	assert.Equal(t, "ha-mcp", provided.Values["serverName"],
		"the namespace must name this app, not its target")
	assert.Equal(t, "/mcp", provided.Values["path"],
		"the published path is the gate's public path, which carries no secrecy")
	assert.Equal(t, []string{"httpToken"}, provided.Secrets,
		"the bearer is the only published credential")
}
