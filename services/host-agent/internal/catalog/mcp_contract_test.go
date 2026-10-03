// SPDX-License-Identifier: AGPL-3.0-only

package catalog

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestShippedCatalog_MCPProviderPublishesEveryBindingField pins the shipped
// MCP provider against the fields the harness binding reads.
//
// The generic completeness test above proves the pairing exists. It cannot prove
// the provider fills the whole binding: `path` is a runtime value the provider
// fills from a workspace id AFFiNE creates, and `serverName` is the tool
// namespace the harness keys its config on. Drop either declaration and the
// binding still loads, with an empty field the consumer filters out, so the
// agent silently loses the namespace rather than failing loudly.
func TestShippedCatalog_MCPProviderPublishesEveryBindingField(t *testing.T) {
	apps, err := NewLoader(realCatalogDir(t)).LoadAll()
	require.NoError(t, err)

	spec, known := ContractFor("mcp")
	require.True(t, known, "the mcp contract must be in the registry")
	require.Equal(t, []string{"httpToken"}, spec.Secrets)

	provider := apps["affine-mcp"]
	require.NotNil(t, provider, "affine-mcp must be in the shipped catalog")

	offered, ok := provider.Provides["mcp"]
	require.True(t, ok, "affine-mcp must declare provides.mcp")

	assert.Equal(t, spec.Secrets, offered.Secrets,
		"the provider must offer exactly the contract's secrets")

	// Every value the contract declares must be covered by one of the two
	// channels: static metadata, or a runtime value the configurator publishes.
	// A value in neither channel can never reach a binding.
	for _, declared := range spec.Values {
		_, static := offered.Values[declared.Key]
		runtime := false
		for _, rv := range offered.RuntimeValues {
			if rv == declared.Key {
				runtime = true
			}
		}
		assert.True(t, static || runtime,
			"affine-mcp provides mcp but never fills %q: declare it in values or runtimeValues", declared.Key)
	}

	assert.Equal(t, "affine-mcp", offered.Values["serverName"],
		"the tool namespace is the app's own name")
	assert.Equal(t, "/mcp", offered.Values["path"],
		"the wrapper serves one endpoint for every workspace, so its path is static")

	// AFFiNE's own MCP server is deliberately not exposed: it is read-only and
	// workspace-scoped, and the wrapper provides the contract instead.
	affine := apps["affine"]
	require.NotNil(t, affine, "affine must still be in the shipped catalog")
	_, exposed := affine.Provides["mcp"]
	assert.False(t, exposed, "affine must not provide mcp")
}

// TestShippedCatalog_MCPConsumerAsksForNoMoreThanItUses pins the harness side
// of the same boundary: declaring a contract must not resolve credentials the
// consumer will not read.
func TestShippedCatalog_MCPConsumerAsksForNoMoreThanItUses(t *testing.T) {
	apps, err := NewLoader(realCatalogDir(t)).LoadAll()
	require.NoError(t, err)

	hermes := apps["hermes"]
	require.NotNil(t, hermes, "hermes must be in the shipped catalog")

	integration, ok := hermes.Integrations["mcp"]
	require.True(t, ok, "hermes must declare integrations.mcp")

	assert.True(t, integration.Multi,
		"a harness takes as many namespaces as the instance provides")
	assert.False(t, integration.Required,
		"hermes is a working agent with no MCP servers at all")
	assert.Equal(t, []string{"httpToken"}, integration.Requires,
		"the bearer is the whole consumer surface")

	found := false
	for _, c := range integration.Compatible {
		if c.App == "affine-mcp" {
			found = true
		}
	}
	assert.True(t, found, "the shipped harness must list the shipped provider")
}
