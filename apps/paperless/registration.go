// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package paperless

import (
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// registration is intentionally minimal: the host-agent registry instantiates
// this factory lazily on the first lookup of the node, so the configurator is
// only built when Paperless-ngx is actually being reconciled.
func init() {
	configurator.MustRegisterFactory("apps-paperless", func(deps configurator.Deps) configurator.NodeLifecycle {
		return NewConfigurator(0, deps)
	})
}
