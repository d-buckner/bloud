// SPDX-License-Identifier: AGPL-3.0-only

package authentik

import (
	"context"
	"fmt"
)

// CalendarServiceUsername is the login name of the account that owns the
// shared calendar collections: the aggregated Radarr/Sonarr feeds and the
// family calendar that Radicale map-shares to every Bloud user.
//
// It is a service account rather than a person for the reason the sharing model
// needs: the shared state must belong to nobody in particular. Under the
// alternative (the first-run operator owns it) the family's calendars are a
// side effect of who set the box up first, and renaming or removing that
// account orphans them.
//
// It is a separate account from CalDAVServiceUsername because that one is the
// agent's read-only credential. If the agent's own account owned the writable
// family calendar, the agent's read-only grant would be a policy note rather
// than a boundary.
const CalendarServiceUsername = "calendar-service"

// CalendarOwnerSecretKey is the app-secret key under which the shared-calendar
// owner credential is published in the radicale app scope. The Authentik
// configurator writes it; the Radicale configurator reads it back to create the
// family calendar as that account.
//
// It is not a contract secret on purpose. The `appApi` offer is the agent's
// read-only credential; this one can write, and putting it in a contract is how
// a write credential ends up in a container that was only meant to read.
const CalendarOwnerSecretKey = "calendarPassword"

// EnsureCalendarServiceAccount creates the shared-calendar owner account if it
// does not exist and sets its password. Like the other service accounts it is a
// plain directory user, so Radicale authenticates it over LDAP; what it may
// read and write is decided by the rights model and the shares, not by the
// account existing.
func (c *Client) EnsureCalendarServiceAccount(ctx context.Context, password string) error {
	return c.ensureServiceAccount(ctx, CalendarServiceUsername, "Calendar Service Account", password)
}

// ensureServiceAccount creates a service_account-type user with the given login
// name and display name if it is absent, then sets its password. Setting the
// password every time is what keeps the directory in step with the secret store
// across passes; the app_password token alone is not sufficient, because
// Authentik's LDAP outpost in direct-bind mode needs the user's real password.
//
// The password write is the one part of this pass that cannot be made a read.
// Authentik has no endpoint that answers "does this account already hold that
// password", so the diff has to be a write. It costs a server-side hash per
// account per pass, which is what the resync cost signal exists to keep visible.
func (c *Client) ensureServiceAccount(ctx context.Context, username, displayName, password string) error {
	userID, err := c.findUserID(ctx, username)
	if err != nil {
		return err
	}
	if userID == 0 {
		userID, err = c.createUserRecord(ctx, username, displayName, "", "service_account")
		if err != nil {
			return err
		}
	}

	if err := c.setUserPassword(ctx, userID, password); err != nil {
		return fmt.Errorf("setting service account %s password: %w", username, err)
	}
	return nil
}
