// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

// Package static exposes embedded static assets for use by Go code.
package static

import _ "embed"

// AuthentikBrandingCSS is the full content of authentik-branding.css,
// embedded at build time. The configurator pushes this inline to the
// Authentik brand API (Constructable Stylesheets forbid @import rules).
//
//go:embed authentik-branding.css
var AuthentikBrandingCSS string
