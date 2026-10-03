// SPDX-License-Identifier: AGPL-3.0-only

package hermeswebui

import (
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// Registration is intentionally minimal: the host-agent registry
// instantiates this factory lazily on the first lookup of the node, so the
// configurator is only built when Hermes Web UI is actually being
// reconciled.
func init() {
	configurator.MustRegisterFactory(nodeName, func(deps configurator.Deps) configurator.NodeLifecycle {
		return NewConfigurator(0, deps)
	})
}
