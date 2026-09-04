// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package navidrome

import (
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// registration is intentionally minimal: the host-agent registry instantiates
// this factory lazily on the first lookup of the node, so the configurator is
// only built when Navidrome is actually being reconciled.
//
// Navidrome's user sync calls the Authentik API from the host itself, so it
// always uses the local Traefik URL regardless of the public host set.
func init() {
	configurator.MustRegisterFactory("apps-navidrome", func(deps configurator.Deps) configurator.NodeLifecycle {
		return NewConfigurator(0, deps.LocalTraefikURL(), deps.Secrets, deps.Logger)
	})
}
