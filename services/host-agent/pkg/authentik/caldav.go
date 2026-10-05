// SPDX-License-Identifier: AGPL-3.0-only

package authentik

import (
	"context"
)

// CalDAVServiceUsername is the login name of the service account the dav-mcp
// wrapper uses to read the operator's calendars. It is a plain directory user
// (type service_account), so Radicale authenticates it over LDAP like any other
// account and the rights model decides what it can see. The matching rights
// rule is rendered by the Radicale configurator.
const CalDAVServiceUsername = "caldav-service"

// EnsureCalDAVServiceAccount creates the CalDAV service account if it does not
// exist and sets its password for LDAP direct bind. It mirrors the service
// account half of EnsureLDAPInfrastructure: the app_password token alone is not
// sufficient, because Authentik's LDAP outpost in direct bind mode requires the
// user's actual password.
func (c *Client) EnsureCalDAVServiceAccount(ctx context.Context, password string) error {
	return c.ensureServiceAccount(ctx, CalDAVServiceUsername, "CalDAV Service Account", password)
}
