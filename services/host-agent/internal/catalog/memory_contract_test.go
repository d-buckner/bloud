// SPDX-License-Identifier: AGPL-3.0-only

package catalog

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// commitSHA is the shape `hermes plugins install --ref` accepts: a full 40
// character commit, because the command rejects a tag name outright. A
// declaration that carried anything else would install nothing and report a
// permission-free failure at runtime, which is the kind of mistake a test reads
// better than a log line.
var commitSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

// TestShippedCatalog_MemoryProviderPublishesEveryBindingField pins the shipped
// memory provider against the fields the harness's binding reads.
//
// The generic contract-declaration test proves the pairing exists. It cannot prove
// the provider fills the three plugin facts, and an empty one is not a load error:
// a consumer that received no `pluginCommit` installs nothing, and a consumer that
// received no `pluginConfigPath` has nowhere to write the server's address. Both
// end as an agent with no memory and no failure to point at.
func TestShippedCatalog_MemoryProviderPublishesEveryBindingField(t *testing.T) {
	apps, err := NewLoader(realCatalogDir(t)).LoadAll()
	require.NoError(t, err)

	spec, known := ContractFor("memory")
	require.True(t, known, "the memory contract must be in the registry")
	require.Equal(t, []string{"httpToken"}, spec.Secrets)
	require.Len(t, spec.Values, 3, "the contract carries the three plugin facts")

	provider := apps["hindsight"]
	require.NotNil(t, provider, "hindsight must be in the shipped catalog")
	offered, ok := provider.Provides["memory"]
	require.True(t, ok, "hindsight must declare provides.memory")

	assert.Equal(t, spec.Secrets, offered.Secrets,
		"the provider must offer exactly the contract's secrets")

	for _, declared := range spec.Values {
		_, static := offered.Values[declared.Key]
		runtime := false
		for _, rv := range offered.RuntimeValues {
			if rv == declared.Key {
				runtime = true
			}
		}
		assert.True(t, static || runtime,
			"hindsight provides memory but never fills %q: declare it in values or runtimeValues", declared.Key)
	}

	assert.Equal(t, "hindsight", offered.Values["pluginName"],
		"the plugin's catalog name is the app's own name here, and a test pins it rather than assuming it")
	assert.Regexp(t, commitSHA, offered.Values["pluginCommit"],
		"the pin must be a full 40 character commit: `--ref` rejects a tag")
	assert.Equal(t, "hindsight/config.json", offered.Values["pluginConfigPath"],
		"the plugin reads its settings from its own directory under the harness home")
}

// TestShippedCatalog_MemoryConsumerAsksForNoMoreThanItUses is the harness side of
// the same boundary: the contract is exclusive, optional, and carries the bearer
// and nothing else into the consumer.
func TestShippedCatalog_MemoryConsumerAsksForNoMoreThanItUses(t *testing.T) {
	apps, err := NewLoader(realCatalogDir(t)).LoadAll()
	require.NoError(t, err)

	hermes := apps["hermes"]
	require.NotNil(t, hermes, "hermes must be in the shipped catalog")

	integration, ok := hermes.Integrations["memory"]
	require.True(t, ok, "hermes must declare integrations.memory")
	assert.False(t, integration.Multi,
		"the harness activates one memory provider, so a second binding has no meaning")
	assert.False(t, integration.Required,
		"hermes is a working agent with no memory server at all")
	assert.Equal(t, []string{"httpToken"}, integration.Requires,
		"the bearer is the whole consumer surface")
}

// TestShippedCatalog_RetiredMCPProviderIsGone keeps the retirement honest.
//
// Hindsight served the `mcp` contract until it gained `memory`, and Bloud stopped
// routing the MCP endpoint at the same time. Both halves have to stay retired: a
// provider that offered `mcp` again while no harness declared it compatible would
// be a namespace nothing wires, which the harness-readiness test treats as a
// catalog fault, and the consumer leaving the declaration behind would put the
// model back in front of two memory surfaces.
func TestShippedCatalog_RetiredMCPProviderIsGone(t *testing.T) {
	apps, err := NewLoader(realCatalogDir(t)).LoadAll()
	require.NoError(t, err)

	hindsight := apps["hindsight"]
	require.NotNil(t, hindsight, "hindsight must still be in the shipped catalog")
	_, offers := hindsight.Provides["mcp"]
	assert.False(t, offers, "hindsight's MCP namespace is retired; its memory API is the surface it publishes")

	hermes := apps["hermes"]
	require.NotNil(t, hermes)
	for _, c := range hermes.Integrations["mcp"].Compatible {
		assert.NotEqual(t, "hindsight", c.App,
			"the harness must not wire a namespace the provider no longer offers")
	}
}
