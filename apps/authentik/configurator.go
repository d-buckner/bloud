// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package authentik

import _ "embed"

// The Django shell scripts run inside the Authentik server container via
// Deps.Exec (see ServerConfigurator.runDjangoShell). Each must print "OK" on
// success.
//
//go:embed scripts/set_admin_password.py
var setAdminPasswordScript string

//go:embed scripts/ensure_api_token.py
var ensureAPITokenScript string
