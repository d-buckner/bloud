// SPDX-License-Identifier: AGPL-3.0-only

package backrest

import (
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// registration is intentionally minimal: the host-agent registry instantiates
// this factory lazily on the first lookup of the node, so the configurator is
// only built when Backrest is actually being reconciled.
//
// Nothing here depends on the live host set: the seeded config carries
// container-local paths only, and Backrest reaches the rest of Bloud through
// the read-only mount, not through the public URL.
func init() {
	configurator.MustRegisterFactory(nodeName, func(deps configurator.Deps) configurator.NodeLifecycle {
		return NewConfigurator(0, deps)
	})
}
