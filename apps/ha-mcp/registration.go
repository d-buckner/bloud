// SPDX-License-Identifier: AGPL-3.0-only

package hamcp

import (
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// Both nodes of this app get their own configurator, the same way Radicale's
// feed-sync sidecar does: a node's config is written by the pass that owns the
// container reading it. The server node writes the env file its container is
// created from; the gate node writes the nginx config it mounts and proves the
// endpoint once both containers exist.
//
// The factories are registered lazily by the host-agent registry, so neither
// configurator is built unless this app is being reconciled.
func init() {
	configurator.MustRegisterFactory(nodeName, func(deps configurator.Deps) configurator.NodeLifecycle {
		return NewConfigurator(0, deps)
	})
	configurator.MustRegisterFactory(gateNodeName, func(deps configurator.Deps) configurator.NodeLifecycle {
		return NewGateConfigurator(0, deps)
	})
}
