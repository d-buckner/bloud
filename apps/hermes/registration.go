// SPDX-License-Identifier: AGPL-3.0-only

package hermes

import (
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// registration is intentionally minimal: the host-agent registry instantiates
// this factory lazily on the first lookup of the node, so the configurator is
// only built when Hermes is actually being reconciled.
func init() {
	configurator.MustRegisterFactory("apps-hermes", func(deps configurator.Deps) configurator.NodeLifecycle {
		return NewConfigurator(0, deps)
	})
}
