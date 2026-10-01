// SPDX-License-Identifier: AGPL-3.0-only

package calino

import (
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// Registration is intentionally minimal: the host-agent registry instantiates
// this factory lazily on the first lookup of the node, so the configurator is
// only built when Calino is actually being reconciled.
//
// The app has no configuration to carry and no credential to publish, so
// nothing here depends on the live host set or on the secrets store.
func init() {
	configurator.MustRegisterFactory(nodeName, func(deps configurator.Deps) configurator.NodeLifecycle {
		return NewConfigurator(0, deps)
	})
}
