// SPDX-License-Identifier: AGPL-3.0-only

package sonarr

import (
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// registration is intentionally minimal: the host-agent registry instantiates
// this factory lazily on the first lookup of the node, so the configurator is
// only built when Sonarr is actually being reconciled.
//
// Sonarr's config.xml is written by PreStart and its API is reached from the
// host at localhost:<port>, so nothing here needs the public host set.
func init() {
	configurator.MustRegisterFactory(nodeName, func(deps configurator.Deps) configurator.NodeLifecycle {
		return NewConfigurator(0, deps)
	})
}
