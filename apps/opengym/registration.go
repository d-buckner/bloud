// SPDX-License-Identifier: AGPL-3.0-only

package opengym

import "codeberg.org/d-buckner/bloud/pkg/configurator"

// Both containers of this install get a configurator, and for different reasons. The
// API node writes the address file its own container is created from; the web node
// writes nothing and proves the composed path instead. The split follows the rule the
// Radicale and Penpot installs already use: a node's configuration is written by the
// pass that owns the container reading it.
func init() {
	configurator.MustRegisterFactory(apiNodeName, func(deps configurator.Deps) configurator.NodeLifecycle {
		return NewConfigurator(deps)
	})
	configurator.MustRegisterFactory(webNodeName, func(deps configurator.Deps) configurator.NodeLifecycle {
		return NewWebConfigurator(0, deps)
	})
}
