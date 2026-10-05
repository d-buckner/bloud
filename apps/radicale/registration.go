// SPDX-License-Identifier: AGPL-3.0-only

package radicale

import (
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// registration is intentionally minimal: the host-agent registry instantiates
// this factory lazily on the first lookup of the node, so the configurator is
// only built when Radicale is actually being reconciled.
//
// The generated config carries no URLs -- the app is reached by path through
// the shared proxy and authenticates against the LDAP outpost by container name
// -- so nothing here depends on the live host set.
func init() {
	configurator.MustRegisterFactory(nodeName, func(deps configurator.Deps) configurator.NodeLifecycle {
		return NewConfigurator(0, deps)
	})
	// The feed-sync sidecar is a second node in the same app. It gets its own
	// configurator rather than being configured by the Radicale one, so its
	// config is rendered by the pass that owns the container that reads it.
	// Both resolve to this app's data tree and bindings, because the
	// orchestrator maps a component node to its owning catalog app.
	configurator.MustRegisterFactory(pimsyncNodeName, func(deps configurator.Deps) configurator.NodeLifecycle {
		return NewPimsyncConfigurator(deps)
	})
}
