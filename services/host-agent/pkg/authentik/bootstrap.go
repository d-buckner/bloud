// SPDX-License-Identifier: AGPL-3.0-only

package authentik

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/appclient"
)

// Bloud's own identity inside Authentik: the admin account the setup wizard
// starts from, and the API token every call in this package authenticates with.
//
// Both were created through the server container's Django shell, which is the
// only way in before the token exists. It is the wrong tool once it does: a bare
// `ak shell` spawn costs ~3.3s of Django startup on a home box, and the
// reconciler re-runs PostStart on every convergence pass, so an idle instance
// paid ~6.6s a minute asserting two facts that had not changed (issue #306). The
// API has endpoints for both, so the steady-state path is now a couple of reads
// and the shell is what it should always have been: the bootstrap, and the repair
// for a token that was deleted or rotated by hand.
const (
	// AdminUsername is the account Bloud creates on first boot. Its password is
	// the bootstrap password only until the operator replaces it in Settings.
	AdminUsername = "admin"
	// AdminsGroup is Authentik's built-in superuser group. Membership in a group
	// with is_superuser is what makes a user a superuser, so it is also the
	// answer to "can this token call the API at all".
	AdminsGroup = "authentik Admins"

	apiServiceUsername = "bloud-api"
	apiTokenIdentifier = "bloud-api-token"

	// legacyAdminEmail is the default this project shipped with before the
	// identity email needed a TLD. It is healed on sight; nothing writes it now.
	legacyAdminEmail = "admin@localhost"

	// Authentik creates AdminsGroup from a blueprint that runs after the health
	// endpoint reports ready, so the first PostStart of a fresh install can
	// arrive before the group exists.
	adminsGroupTimeout = 2 * time.Minute
	adminsGroupPoll    = 2 * time.Second
)

// ErrUnauthenticated means Authentik refused the API token (401/403). It is a
// result, not a failure: it says the credential the API is authenticated with
// does not work yet, which is exactly the condition only the container's own
// Django shell can fix. Callers branch on it with errors.Is.
var ErrUnauthenticated = errors.New("authentik rejected the API token")

// userRef is the slice of a directory account this package diffs.
type userRef struct {
	PK          int    `json:"pk"`
	Email       string `json:"email"`
	IsSuperuser bool   `json:"is_superuser"`
}

// apiTokenRef is the slice of an Authentik token this package diffs. user_obj
// rides along on the token list, so one GET answers both "does the token exist"
// and "is its owner still an admin".
type apiTokenRef struct {
	Identifier string `json:"identifier"`
	UserObj    struct {
		PK          int  `json:"pk"`
		IsSuperuser bool `json:"is_superuser"`
	} `json:"user_obj"`
}

// EnsureAdminUser makes Bloud's admin account exist and be an Authentik admin.
//
// The password is set only when the account is created. Re-applying the bootstrap
// password on every pass would overwrite the password the operator chose in
// Bloud's setup wizard and lock them out on the next resync, which is the
// behaviour the Django shell this replaced documented and enforced.
func (c *Client) EnsureAdminUser(ctx context.Context, password, email string) error {
	user, err := c.lookupUser(ctx, AdminUsername)
	if err != nil {
		return err
	}
	if user == nil {
		return c.createAdminUser(ctx, password, email)
	}

	// Self-heal the legacy default email: SSO apps validate identity emails with
	// an RFC-style validator that requires a TLD, so the bare "admin@localhost"
	// breaks OIDC login. An email the operator set is untouched.
	if user.Email == "" || user.Email == legacyAdminEmail {
		if err := c.SetUserEmail(ctx, user.PK, email); err != nil {
			return fmt.Errorf("repairing admin email: %w", err)
		}
	}
	if !user.IsSuperuser {
		if err := c.addUserToAdmins(ctx, user.PK); err != nil {
			return err
		}
	}
	return nil
}

// EnsureAPIToken makes the identifier/key pair host-agent authenticates with
// exist and hold the given key.
//
// It returns ErrUnauthenticated when Authentik will not accept the token at all,
// including on the very first PostStart, when nothing has been created yet. That
// is the caller's cue to mint it out of band.
func (c *Client) EnsureAPIToken(ctx context.Context, key string) error {
	token, err := c.lookupAPIToken(ctx)
	if err != nil {
		return err
	}
	if token == nil {
		return c.createAPIToken(ctx, key)
	}
	if !token.UserObj.IsSuperuser {
		// The token is there and its owner is not an admin, so every call this
		// package makes comes back denied for a reason that reads like a bad
		// token. Restore the membership rather than pass the confusion on.
		if err := c.addUserToAdmins(ctx, token.UserObj.PK); err != nil {
			return err
		}
	}
	return c.ensureTokenKey(ctx, apiTokenIdentifier, key)
}

// createAdminUser provisions the admin account from nothing.
func (c *Client) createAdminUser(ctx context.Context, password, email string) error {
	groupID, err := c.waitForAdminsGroup(ctx)
	if err != nil {
		return err
	}
	userID, err := c.createUserRecord(ctx, AdminUsername, "Admin", email, "internal")
	if err != nil {
		return err
	}
	// The password is set after the create: the user API has no writable
	// password field, only the set_password action.
	if err := c.setUserPassword(ctx, userID, password); err != nil {
		return fmt.Errorf("setting admin password: %w", err)
	}
	return c.addUserToGroupID(ctx, userID, groupID)
}

// createAPIToken provisions the service account and token Bloud calls the API
// with, in that order: a token needs a user, and the user needs the admin group
// for the token to be worth anything.
func (c *Client) createAPIToken(ctx context.Context, key string) error {
	groupID, err := c.waitForAdminsGroup(ctx)
	if err != nil {
		return err
	}
	userID, err := c.createUserRecord(ctx, apiServiceUsername, "Bloud API Service Account", "", "internal_service_account")
	if err != nil {
		return err
	}
	if err := c.addUserToGroupID(ctx, userID, groupID); err != nil {
		return err
	}
	payload := map[string]any{
		"identifier":  apiTokenIdentifier,
		"user":        userID,
		"intent":      "api",
		"expiring":    false,
		"description": "Bloud host-agent API token",
	}
	if err := c.cl.POST("/api/v3/core/tokens/").JSON(payload).OK(http.StatusCreated).Exec(ctx); err != nil {
		return fmt.Errorf("creating Bloud API token: %w", err)
	}
	// A token created over the API always gets a random key: the serializer only
	// exposes `key` in the blueprint context. set_key is the API's one way to
	// impose the value host-agent actually authenticates with.
	return c.setTokenKey(ctx, apiTokenIdentifier, key)
}

// waitForAdminsGroup polls until Authentik has created its superuser group.
//
// The wait is what keeps a cold install from parking the SSO stack in ERROR over
// a group that turns up seconds later. The Django shell this replaced polled for
// the same window.
func (c *Client) waitForAdminsGroup(ctx context.Context) (string, error) {
	deadline := time.Now().Add(adminsGroupTimeout)
	for {
		id, err := c.findGroupID(ctx, AdminsGroup)
		if err == nil {
			return id, nil
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("waiting for the %s group: %w", AdminsGroup, err)
		}
		if err := sleepCtx(ctx, adminsGroupPoll); err != nil {
			return "", err
		}
	}
}

// lookupUser returns the account whose username matches exactly, or nil when
// there is none.
//
// `username` is Authentik's exact filter. `search` is the DjangoQL full-text one,
// which scans email, name, uuid and username and costs an order of magnitude
// more per call; every caller here wants one specific account, so the exact
// filter is both cheaper and more precise. include_groups=false skips the
// group prefetch the list view otherwise does for every row.
func (c *Client) lookupUser(ctx context.Context, username string) (*userRef, error) {
	var result struct {
		Results []userRef `json:"results"`
	}
	if err := c.cl.GET("/api/v3/core/users/").
		Query("username", username).
		Query("include_groups", "false").
		OK(http.StatusOK).
		DoInto(ctx, &result); err != nil {
		return nil, fmt.Errorf("looking up user %s: %w", username, err)
	}
	if len(result.Results) == 0 {
		return nil, nil
	}
	user := result.Results[0]
	return &user, nil
}

// createUserRecord creates a directory account and returns its ID. An empty
// email is left out of the payload rather than sent blank: service accounts have
// no mailbox, and Authentik's own service accounts have no email at all.
func (c *Client) createUserRecord(ctx context.Context, username, name, email, userType string) (int, error) {
	payload := map[string]any{
		"username":  username,
		"name":      name,
		"path":      "users",
		"type":      userType,
		"is_active": true,
	}
	if email != "" {
		payload["email"] = email
	}
	var result struct {
		PK int `json:"pk"`
	}
	if err := c.cl.POST("/api/v3/core/users/").JSON(payload).OK(http.StatusCreated).DoInto(ctx, &result); err != nil {
		return 0, fmt.Errorf("creating user %s: %w", username, err)
	}
	return result.PK, nil
}

// lookupAPIToken returns Bloud's API token, or nil when it does not exist.
// A refusal to authenticate is reported as ErrUnauthenticated, because "no
// token" and "the token we were given is not valid here" need the same repair.
func (c *Client) lookupAPIToken(ctx context.Context) (*apiTokenRef, error) {
	var result struct {
		Results []apiTokenRef `json:"results"`
	}
	err := c.cl.GET("/api/v3/core/tokens/").
		Query("identifier", apiTokenIdentifier).
		OK(http.StatusOK).
		DoInto(ctx, &result)
	if err != nil {
		if status := appclient.StatusOf(err); status == http.StatusUnauthorized || status == http.StatusForbidden {
			return nil, fmt.Errorf("%w (HTTP %d)", ErrUnauthenticated, status)
		}
		return nil, fmt.Errorf("looking up the Bloud API token: %w", err)
	}
	if len(result.Results) == 0 {
		return nil, nil
	}
	token := result.Results[0]
	return &token, nil
}

// tokenKey reads a token's current key. Authentik records every read as a
// SECRET_VIEW event, which is the honest price of diffing a secret instead of
// overwriting it: the alternative is a write on every pass.
func (c *Client) tokenKey(ctx context.Context, identifier string) (string, error) {
	var result struct {
		Key string `json:"key"`
	}
	if err := c.cl.GET("/api/v3/core/tokens/"+url.PathEscape(identifier)+"/view_key/").
		OK(http.StatusOK).
		DoInto(ctx, &result); err != nil {
		return "", fmt.Errorf("reading key of token %s: %w", identifier, err)
	}
	return result.Key, nil
}

func (c *Client) setTokenKey(ctx context.Context, identifier, key string) error {
	return c.cl.POST("/api/v3/core/tokens/" + url.PathEscape(identifier) + "/set_key/").
		JSON(map[string]string{"key": key}).
		OK(http.StatusNoContent).
		Exec(ctx)
}

// ensureTokenKey writes only on a mismatch.
//
// In the ordinary case the mismatch cannot be reached: the bearer on this very
// request is the key being checked, so a token holding some other key would have
// been refused at the lookup and sent the caller to the shell. The read is still
// worth its one GET, because "which credential authenticated that request" is
// not something the client can see: a second token carrying Bloud's key would
// satisfy the lookup while bloud-api-token itself stayed wrong, and this is what
// notices.
func (c *Client) ensureTokenKey(ctx context.Context, identifier, key string) error {
	current, err := c.tokenKey(ctx, identifier)
	if err != nil {
		return err
	}
	if current == key {
		return nil
	}
	return c.setTokenKey(ctx, identifier, key)
}

// addUserToAdmins grants the superuser group by name.
func (c *Client) addUserToAdmins(ctx context.Context, userID int) error {
	groupID, err := c.findGroupID(ctx, AdminsGroup)
	if err != nil {
		return err
	}
	return c.addUserToGroupID(ctx, userID, groupID)
}
