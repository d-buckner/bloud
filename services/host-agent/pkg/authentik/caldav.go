// SPDX-License-Identifier: AGPL-3.0-only

package authentik

import (
	"context"
	"fmt"
	"net/http"
)

// CalDAVServiceUsername is the login name of the service account the caldav-mcp
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
	userID, err := c.findUserID(ctx, CalDAVServiceUsername)
	if err != nil {
		return err
	}
	if userID == 0 {
		payload := map[string]interface{}{
			"username":  CalDAVServiceUsername,
			"name":      "CalDAV Service Account",
			"path":      "users",
			"type":      "service_account",
			"is_active": true,
		}
		var result struct {
			PK int `json:"pk"`
		}
		if err := c.cl.POST("/api/v3/core/users/").JSON(payload).OK(http.StatusCreated).DoInto(ctx, &result); err != nil {
			return fmt.Errorf("creating CalDAV service account: %w", err)
		}
		userID = result.PK
	}

	if err := c.setUserPassword(ctx, userID, password); err != nil {
		return fmt.Errorf("setting CalDAV service account password: %w", err)
	}
	return nil
}
