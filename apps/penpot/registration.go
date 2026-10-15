// SPDX-License-Identifier: AGPL-3.0-only

package penpot

import (
	"codeberg.org/d-buckner/bloud/pkg/configurator"
)

// Two nodes of one install get configurators, and the other five do not: the
// database, cache, exporter, and admin console need nothing beyond what
// metadata.yaml declares, and the MCP server needs nothing at all (its image
// binds 0.0.0.0 and runs multi-user by default).
func init() {
	configurator.MustRegisterFactory(backendNode, func(deps configurator.Deps) configurator.NodeLifecycle {
		return newEnvWriter(deps)
	})
	configurator.MustRegisterFactory(frontendNode, func(deps configurator.Deps) configurator.NodeLifecycle {
		return newFrontendConfigurator(deps)
	})
}
