// SPDX-License-Identifier: AGPL-3.0-only

package jellyfinmcp

import "codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"

// init registers the jellyfin-mcp node. The factory is instantiated lazily on
// first lookup; see pkg/configurator for the registry contract.
func init() {
	configurator.MustRegisterFactory(nodeName, func(deps configurator.Deps) configurator.NodeLifecycle {
		return NewConfigurator(0, deps)
	})
}
