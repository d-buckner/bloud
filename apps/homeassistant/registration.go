// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package homeassistant

import (
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// init registers the Home Assistant configurator factory. The host-agent
// registry instantiates it lazily on the first lookup of the node, so the
// configurator is only built when Home Assistant is actually being reconciled.
func init() {
	configurator.MustRegisterFactory("apps-homeassistant", func(deps configurator.Deps) configurator.NodeLifecycle {
		return NewConfigurator(8123, deps.Logger)
	})
}
