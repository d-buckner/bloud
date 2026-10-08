// SPDX-License-Identifier: AGPL-3.0-only

package catalog

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// appsOf flattens a DeclaredProviders result to the catalog app names it
// names, so a test reads as a list of providers rather than a list of structs.
func appsOf(providers []BoundProvider) []string {
	out := make([]string, 0, len(providers))
	for _, p := range providers {
		out = append(out, p.App)
	}
	return out
}

// The declaration is the set: every compatible provider, in declaration order.
// Nothing is picked, and `required` plays no part in the set.
func TestDeclaredProviders_ListsEveryCompatibleProvider(t *testing.T) {
	integration := Integration{
		Required:   true,
		Multi:      true,
		Compatible: []CompatibleApp{{App: "affine-mcp"}, {App: "dav-mcp"}},
	}

	assert.Equal(t, []string{"affine-mcp", "dav-mcp"}, appsOf(DeclaredProviders(integration)))
}

// The same declaration returns the same set whether the contract is required
// or optional. `required` only decides whether installing the consumer also
// installs the provider; it never trims the set.
func TestDeclaredProviders_RequiredAndOptionalAreTheSameSet(t *testing.T) {
	compatible := []CompatibleApp{{App: "radarr", Default: true}, {App: "sonarr"}}

	required := DeclaredProviders(Integration{Required: true, Compatible: compatible})
	optional := DeclaredProviders(Integration{Required: false, Compatible: compatible})

	assert.Equal(t, appsOf(required), appsOf(optional))
	assert.Equal(t, []string{"radarr", "sonarr"}, appsOf(required))
}

// A `source: instance` entry binds the instance, not an app named ”.
func TestDeclaredProviders_MapsInstanceSource(t *testing.T) {
	integration := Integration{
		Compatible: []CompatibleApp{{App: "radarr"}, {Source: InstanceProviderSource}},
	}

	providers := DeclaredProviders(integration)

	require.Len(t, providers, 2)
	assert.Equal(t, "radarr", providers[0].App)
	assert.False(t, providers[0].IsInstance())
	assert.True(t, providers[1].IsInstance())
	assert.Empty(t, providers[1].App,
		"the instance is not a catalog app, so it must not carry a name to look up")
}

// A duplicate declaration is one provider, not two: the dedup keeps a consumer
// from getting the same binding twice and the graph from getting a doubled
// edge.
func TestDeclaredProviders_Deduplicates(t *testing.T) {
	integration := Integration{
		Compatible: []CompatibleApp{{App: "radarr"}, {App: "radarr"}, {App: "sonarr"}},
	}

	assert.Equal(t, []string{"radarr", "sonarr"}, appsOf(DeclaredProviders(integration)))
}

// The instance is a provider the consumer never named, so it must not be
// mistaken for a catalog app on the way out.
func TestBoundProvider_IsInstance(t *testing.T) {
	assert.True(t, BoundProvider{Source: InstanceProviderSource}.IsInstance())
	assert.False(t, BoundProvider{App: "radarr"}.IsInstance())
	assert.False(t, BoundProvider{}.IsInstance())
}

// The rule has to hold against the shipped catalog, not just hand-built
// integrations. Hermes' mcp contract is optional and multi, so a provider
// installed after the other was wired (the #233 shape) is in the set from the
// declaration alone, before anything about installation is consulted.
func TestDeclaredProviders_RealCatalogDeclaresEveryMcpProvider(t *testing.T) {
	cache := NewMemoryCache()
	require.NoError(t, cache.Refresh(NewLoader(filepath.Join("..", "..", "..", "..", "apps"))))

	hermes, err := cache.Get("hermes")
	require.NoError(t, err)
	mcp := hermes.Integrations["mcp"]
	require.True(t, mcp.Multi, "hermes' mcp contract is expected to be multi")
	require.False(t, mcp.Required, "hermes' mcp contract is expected to be optional")

	providers := DeclaredProviders(mcp)

	var names []string
	for _, p := range providers {
		require.False(t, p.IsInstance(), "hermes' mcp contract declares no instance provider")
		names = append(names, p.App)
	}
	assert.Equal(t, []string{"affine-mcp", "dav-mcp", "jellyfin-mcp"}, names)
}
