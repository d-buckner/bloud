// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

// Package apps is the Bloud user-app catalog. Importing it links every
// user-app configurator into the configurator registry: each app package
// registers its own factory from an init() (see apps/<name>/registration.go),
// and Go runs those inits when this package is linked into the binary.
//
// This is the single place to touch when adding or removing an app. Before it
// existed, adding an app meant also editing
// services/host-agent/internal/appconfig/register.go -- a cross-module edit
// that was easy to forget, yielding an app that installed but had no
// configurator at runtime.
package apps

import (
	// User-app configurators. Each blank import runs that app's init(), which
	// calls configurator.MustRegisterFactory for its node(s). Keep this list
	// and NodeNames() in sync with the app directories next to this file;
	// TestRegisterAll fails if the two drift apart.
	//
	// System apps (Traefik, Authentik) are deliberately NOT here: host-agent
	// registers those eagerly in appconfig.RegisterSystem, because they are
	// runtime-dependent and always needed.
	_ "codeberg.org/d-buckner/bloud/apps/affine"
	_ "codeberg.org/d-buckner/bloud/apps/homeassistant"
	_ "codeberg.org/d-buckner/bloud/apps/immich"
	_ "codeberg.org/d-buckner/bloud/apps/jellyfin"
	_ "codeberg.org/d-buckner/bloud/apps/navidrome"
	_ "codeberg.org/d-buckner/bloud/apps/paperless-ngx"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// NodeNames returns the node name each user app registers a configurator
// factory for. These are the graph node names the orchestrator looks up, not
// the catalog IDs (Immich's main node is "apps-immich-server", for example).
//
// Adding an app means adding both its blank import above and its node name
// here. TestRegisterAll asserts every name here is actually registered, so a
// typo or a missing registration.go fails the build rather than surfacing as
// a missing configurator during an install.
func NodeNames() []string {
	return []string{
		"apps-affine",
		"apps-homeassistant",
		"apps-immich-server",
		"apps-jellyfin",
		"apps-navidrome",
		"apps-paperless-ngx",
	}
}

// RegisterAll links every user-app configurator factory.
//
// The registration itself happens in each app package's init(), which Go
// runs before any caller of this function, provided the package is linked.
// So this function does no work of its own -- its purpose is to make the
// dependency explicit, greppable, and testable instead of leaving it as
// invisible init magic at the import site.
//
// It is idempotent and cheap. Call it at every entry point that builds a
// configurator registry.
func RegisterAll() {
	// Intentionally empty: the blank imports above are the side effect.
}

// MissingConfigurators returns the NodeNames entries with no factory
// registered against reg, i.e. the apps that would install but never get
// configured. Empty means the catalog and the registry agree.
func MissingConfigurators(reg *configurator.Registry) []string {
	var missing []string
	for _, name := range NodeNames() {
		if !reg.Has(name) {
			missing = append(missing, name)
		}
	}
	return missing
}
