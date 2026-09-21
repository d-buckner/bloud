// SPDX-License-Identifier: AGPL-3.0-only

package qbittorrent

import (
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// registration is intentionally minimal: the host-agent registry instantiates
// this factory lazily on the first lookup of the node, so the configurator is
// only built when qBittorrent is actually being reconciled.
//
// Port 0 selects the default WebUI port (8081), which must match the port
// published in metadata.yaml: Traefik routes <id>.<host> to it.
func init() {
	configurator.MustRegisterFactory("apps-qbittorrent", func(deps configurator.Deps) configurator.NodeLifecycle {
		return NewConfigurator(0, deps)
	})
}
