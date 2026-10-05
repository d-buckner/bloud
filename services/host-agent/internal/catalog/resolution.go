// SPDX-License-Identifier: AGPL-3.0-only

package catalog

// BoundProviders returns the providers one integration binds, given the
// provider the install recorded for it. This is the whole rule, in one place,
// because three consumers need to agree on it and two separate bugs (#227,
// #233) came from copies of it drifting:
//
//   - the orchestrator's resolver, which hands a consumer its bindings
//     (`engine/orchestrator/integrations.go:resolveProviders`);
//   - the dependency edges that order those bindings on the graph
//     (`engine/orchestrator/graph_build.go:computeAppDeps`);
//   - the developer graph, which has to draw what the resolver actually
//     wired rather than what it guesses.
//
// The rule: the recorded choice is authoritative only for a *required*
// contract, which is a slot with one occupant and the operator filled it. An
// *optional* contract binds the recorded choice **and** every compatible
// provider the metadata declares, because the consumer iterates the whole
// slice and a provider installed later is wiring that starts existing on the
// next pass. Hermes recorded `mcp: affine-mcp` back when that was the only
// MCP provider in the catalog; installing `caldav-mcp` wired it into the
// agent without touching the recorded value, and a display that stopped at
// the recorded value showed one edge where the resolver bound two.
//
// A required contract with nothing recorded binds nothing: no choice was ever
// made, and inventing one here would put a provider in a consumer's binding
// that nobody picked. A display that wants to show the provider a required
// contract cannot install without should say so from the declaration, not by
// widening this function.
//
// Entries are returned in binding order (the recorded choice first, then the
// declaration order of the compatible list) and deduplicated, so a choice
// that also appears in `compatible` yields one provider, not two.
func BoundProviders(integration Integration, choice string) []BoundProvider {
	var out []BoundProvider
	add := func(p BoundProvider) {
		for _, existing := range out {
			if existing == p {
				return
			}
		}
		out = append(out, p)
	}

	if choice != "" {
		add(BoundProvider{App: choice})
	}
	if integration.Required {
		return out
	}
	for _, compatible := range integration.Compatible {
		if compatible.Source != "" {
			// A non-app provider carries no catalog ID: naming one here would
			// invite a lookup that resolves to nothing.
			add(BoundProvider{Source: compatible.Source})
			continue
		}
		add(BoundProvider{App: compatible.App})
	}
	return out
}

// BoundProvider is one provider an integration binds: a catalog app, or the
// instance itself for a `source: instance` entry.
//
// Source carries the provider-source kind for a non-app provider and is empty
// for a catalog app, which is what lets each consumer map this to its own
// vocabulary (a `providerSource` in the orchestrator, a graph node ID in the
// display) without either of them re-deriving the rule.
type BoundProvider struct {
	// App is the catalog app ID, or the instance source name when Source is
	// set.
	App string
	// Source is the provider source as declared (`InstanceProviderSource`),
	// empty for a catalog app.
	Source string
}

// IsInstance reports whether this provider is the instance rather than a
// catalog app.
func (p BoundProvider) IsInstance() bool {
	return p.Source == InstanceProviderSource
}
