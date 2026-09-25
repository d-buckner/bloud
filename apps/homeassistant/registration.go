// SPDX-License-Identifier: AGPL-3.0-only

package homeassistant

import (
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// init registers the Home Assistant configurator factory. The host-agent
// registry instantiates it lazily on the first lookup of the node, so the
// configurator is only built when Home Assistant is actually being reconciled.
func init() {
	configurator.MustRegisterFactory(nodeName, func(deps configurator.Deps) configurator.NodeLifecycle {
		return NewConfigurator(0, deps)
	})
}
