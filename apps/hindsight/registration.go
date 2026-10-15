// SPDX-License-Identifier: AGPL-3.0-only

package hindsight

import (
	"codeberg.org/d-buckner/bloud/pkg/configurator"
)

// The two nodes that have configuration to write get their own configurator, the
// same way apps/ha-mcp splits its server and its gate: a node's config is written
// by the pass that owns the container reading it. The server node writes the env
// file its container is created from; the gate node writes the nginx config it
// mounts and proves the endpoint once both containers exist.
//
// apps-hindsight-postgres is deliberately absent. It is a declared container with
// a declared health check and no value that only a configurator can supply, and
// the orchestrator drives a node with no registered configurator.
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
