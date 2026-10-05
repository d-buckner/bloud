// SPDX-License-Identifier: AGPL-3.0-only

package catalog

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// appsOf flattens a BoundProviders result to the catalog app names it binds,
// so a test reads as a list of providers rather than a list of structs.
func appsOf(bound []BoundProvider) []string {
	out := make([]string, 0, len(bound))
	for _, p := range bound {
		out = append(out, p.App)
	}
	return out
}

// A required contract is a slot with one occupant, and the operator filled it:
// the recorded choice is the whole binding set.
func TestBoundProviders_RequiredContractBindsOnlyTheChoice(t *testing.T) {
	integration := Integration{
		Required:   true,
		Compatible: []CompatibleApp{{App: "sonarr"}, {App: "radarr"}},
	}

	assert.Equal(t, []string{"radarr"}, appsOf(BoundProviders(integration, "radarr")))
}

// An optional contract binds the recorded choice *and* every compatible
// provider, because the consumer iterates the whole slice and a provider
// installed after the choice was recorded starts being wiring that exists.
// This is the rule #227 and #233 each got half of.
func TestBoundProviders_OptionalContractBindsChoiceAndEveryCompatible(t *testing.T) {
	integration := Integration{
		Multi:      true,
		Compatible: []CompatibleApp{{App: "affine-mcp"}, {App: "caldav-mcp"}},
	}

	assert.Equal(t,
		[]string{"affine-mcp", "caldav-mcp"},
		appsOf(BoundProviders(integration, "affine-mcp")),
		"the recorded choice binds, and the declaration adds the provider installed later")
}

// With nothing recorded, an optional contract binds the whole declared list.
// The instance provider reaches the binding set without a `default: true`
// anywhere, which is what lets an app's `source: instance` wiring show up the
// moment Settings -> AI is populated.
func TestBoundProviders_OptionalContractWithoutChoiceBindsTheDeclaration(t *testing.T) {
	integration := Integration{
		Compatible: []CompatibleApp{{App: "radarr"}, {Source: InstanceProviderSource}},
	}

	bound := BoundProviders(integration, "")

	require.Len(t, bound, 2)
	assert.Equal(t, "radarr", bound[0].App)
	assert.False(t, bound[0].IsInstance())
	assert.True(t, bound[1].IsInstance(),
		"a source: instance entry binds the instance, not an app named ''")
	assert.Empty(t, bound[1].App,
		"the instance is not a catalog app, so it must not carry a name to look up")
}

// A required contract with nothing recorded binds nothing. Inventing a provider
// here would put a binding in a consumer's hands that nobody chose; a display
// that wants to show the provider a required contract cannot install without
// says so from the declaration instead.
func TestBoundProviders_RequiredContractWithoutChoiceBindsNothing(t *testing.T) {
	integration := Integration{
		Required:   true,
		Compatible: []CompatibleApp{{App: "traefik", Default: true}},
	}

	assert.Empty(t, BoundProviders(integration, ""))
}

// A recorded choice that also appears in the compatible list is one provider,
// not two: the dedup is what keeps a consumer from getting the same binding
// twice and the graph from getting a doubled edge.
func TestBoundProviders_DeduplicatesTheChoiceAgainstTheDeclaration(t *testing.T) {
	integration := Integration{
		Multi:      true,
		Compatible: []CompatibleApp{{App: "radarr"}, {App: "sonarr"}},
	}

	assert.Equal(t, []string{"radarr", "sonarr"}, appsOf(BoundProviders(integration, "radarr")))
}

// The instance is a provider the consumer never named, so it must not be
// mistaken for a catalog app on the way out.
func TestBoundProvider_IsInstance(t *testing.T) {
	assert.True(t, BoundProvider{Source: InstanceProviderSource}.IsInstance())
	assert.False(t, BoundProvider{App: "radarr"}.IsInstance())
	assert.False(t, BoundProvider{}.IsInstance())
}

// The rule has to hold against the shipped catalog, not just against
// hand-built integrations. Hermes' mcp contract is optional and multi, so a
// stale `mcp: affine-mcp` record from before caldav-mcp shipped must still
// leave caldav-mcp bound.
func TestBoundProviders_RealCatalogKeepsLateMcpProvidersBound(t *testing.T) {
	cache := NewMemoryCache()
	require.NoError(t, cache.Refresh(NewLoader(filepath.Join("..", "..", "..", "..", "apps"))))

	hermes, err := cache.Get("hermes")
	require.NoError(t, err)
	mcp := hermes.Integrations["mcp"]
	require.True(t, mcp.Multi, "hermes' mcp contract is expected to be multi")
	require.False(t, mcp.Required, "hermes' mcp contract is expected to be optional")

	bound := BoundProviders(mcp, "affine-mcp")

	var names []string
	for _, p := range bound {
		require.False(t, p.IsInstance(), "hermes' mcp contract declares no instance provider")
		names = append(names, p.App)
	}
	assert.Equal(t, []string{"affine-mcp", "caldav-mcp"}, names,
		"the recorded choice plus every declared compatible provider")
}
