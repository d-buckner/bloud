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
