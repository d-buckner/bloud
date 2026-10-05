// SPDX-License-Identifier: AGPL-3.0-only

package catalog

// DeclaredProviders returns the providers an integration declares, in
// declaration order, deduplicated, with the instance provider mapped like a
// catalog one.
//
// This is the whole integration model in one place, and the model is: a
// dependency set, not a menu. An app declares every provider that can satisfy
// a contract under `compatible:`, and every one of them is a provider. Bloud
// never picks one. Whether each provider is actually wired is answered the
// same way by every consumer: a catalog app is wired when it is installed,
// and the instance is wired when the operator has configured it. `required`
// only says whether installing the consumer also installs the provider; it
// plays no part in the set returned here.
//
// Three consumers have to agree on this, and two separate bugs (#227 in the
// CLI's graph generator, #233 in the developer graph) came from copies of it
// drifting:
//
//   - the orchestrator's resolver, which hands a consumer its bindings
//     (`engine/orchestrator/integrations.go:resolveProviders`);
//   - the dependency edges that order those bindings on the graph
//     (`engine/orchestrator/graph_build.go:computeAppDeps`);
//   - the developer graph, which has to draw what the resolver actually
//     wired rather than what it guesses.
//
// There used to be a fourth input, a recorded "choice" per contract in
// `IntegrationConfig`. That choice system was never built: there is no UI for
// it, the install path always passes nil user choices, and treating the
// recorded value as authoritative hid a provider installed after the record
// was written (Hermes recorded `mcp: affine-mcp` when that was the only MCP
// provider, so `dav-mcp` never drew). The record is now write-only
// provenance, and resolution reads this declaration plus what is installed.
func DeclaredProviders(integration Integration) []BoundProvider {
	var out []BoundProvider
	add := func(p BoundProvider) {
		for _, existing := range out {
			if existing == p {
				return
			}
		}
		out = append(out, p)
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

// BoundProvider is one provider an integration declares: a catalog app, or
// the instance itself for a `source: instance` entry.
//
// Source carries the provider-source kind for a non-app provider and is empty
// for a catalog app, which is what lets each consumer map this to its own
// vocabulary (a `providerSource` in the orchestrator, a graph node ID in the
// display) without either of them re-deriving the rule.
type BoundProvider struct {
	// App is the catalog app ID. Empty when Source is set.
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
