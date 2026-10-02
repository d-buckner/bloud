// SPDX-License-Identifier: AGPL-3.0-only

package affine

import (
	"context"
	"sort"
	"strings"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/authentik"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// memberPageLimit is how many member rows the diff reads. The AFFiNE resolver
// defaults to 8, which would hide the users past the page and re-invite them
// every pass; 500 is comfortably above any home instance and still one query.
const memberPageLimit = 500

// bloudUserEmails turns the identity provider's user list into the addresses
// that belong in the shared workspace.
//
// The provider query already asks for internal accounts only, and the client
// drops service accounts, but two guards still belong here. An inactive account
// is not a user, and an account with no email cannot be invited: AFFiNE keys
// the invitation to the address, and that same address is what later links the
// user's OIDC sign-in to the account it was invited as. An empty address would
// be a hole, not a user.
func bloudUserEmails(users []authentik.ManagedUserInfo) []string {
	seen := make(map[string]bool, len(users))
	emails := make([]string, 0, len(users))
	for _, u := range users {
		if !u.IsActive {
			continue
		}
		email := strings.ToLower(strings.TrimSpace(u.Email))
		if email == "" || seen[email] {
			continue
		}
		seen[email] = true
		emails = append(emails, email)
	}
	// Sorted so a diff that did invite someone reads the same way every time.
	sort.Strings(emails)
	return emails
}

// sharedIdentity picks the identity provider the shared-workspace membership is
// read from: the resolved `sso` binding that carries an API token. The bool
// reports whether there is one at all, which is what separates "invite nobody,
// there is no identity provider" from "invite nobody, they are all already in".
func sharedIdentity(state *configurator.AppState) (configurator.SSOBinding, bool) {
	if state == nil {
		return configurator.SSOBinding{}, false
	}
	for _, s := range state.Integrations.SSO {
		if s.APIToken != "" {
			return s, true
		}
	}
	return configurator.SSOBinding{}, false
}

// ensureSharedMembers makes the shared workspace hold every Bloud user.
//
// It is a diff, not a broadcast. The workspace's own member list already
// includes outstanding invitations, so a user who was invited and has not yet
// accepted is not invited twice, and one who accepted is not touched. A steady
// state therefore costs two reads and no writes.
//
// The user's side of this needs no mail and no Bloud screen. `inviteMembers`
// writes an in-app notification before it tries to send anything, so the
// invitation is waiting in AFFiNE's own notification center when the user first
// signs in through Bloud's SSO, and accepting it there is what makes them an
// active member. Bloud cannot make them one without that click: `grantMember`
// refuses on a non-team workspace, and the team plan is a paid entitlement.
//
// Failures are logged and swallowed, exactly like the MCP credential and the BYOK
// profile. A knowledge base that serves its users fine must not land in ERROR
// because a membership pass failed, and the next pass retries.
func (c *Configurator) ensureSharedMembers(ctx context.Context, state *configurator.AppState) {
	idp, bound := sharedIdentity(state)
	if !bound {
		c.logger.Info("affine shared workspace: no identity provider token, inviting nobody")
		return
	}
	workspaceID, ok := c.ownerSessionForInvites(ctx)
	if !ok {
		return
	}
	wanted, ok := c.wantedMemberEmails(ctx, idp)
	if !ok {
		return
	}
	missing, total, err := c.missingMembers(ctx, workspaceID, wanted)
	if err != nil || len(missing) == 0 {
		return
	}

	results, err := c.api.inviteMembers(ctx, workspaceID, missing)
	if err != nil {
		c.logger.Warn("affine shared workspace: the invite call failed",
			"workspace", workspaceID, "attempted", len(missing), "err", err)
		return
	}
	if invited := c.collectInvites(results); len(invited) > 0 {
		c.logger.Info("invited the Bloud users into the shared workspace",
			"workspace", workspaceID, "invited", len(invited), "members", total)
	}
}

// ownerSessionForInvites signs in as the Bloud-owned AFFiNE account and
// settles on the shared workspace, which is what the invite calls are made
// against. Each step is a hard prerequisite for the next, so the whole prologue
// is one yes/no.
func (c *Configurator) ownerSessionForInvites(ctx context.Context) (string, bool) {
	if c.secrets == nil {
		c.logger.Warn("cannot invite into the affine shared workspace: no secrets provider")
		return "", false
	}
	password, err := c.secrets.GenerateAppAdminPassword(appName)
	if err != nil {
		c.logger.Warn("affine shared workspace: no owner password", "err", err)
		return "", false
	}
	if err := c.api.signIn(ctx, c.adminEmail, password); err != nil {
		c.logger.Warn("affine shared workspace: owner sign-in failed, inviting nobody", "err", err)
		return "", false
	}
	workspaceID, err := c.ensureWorkspace(ctx)
	if err != nil {
		c.logger.Warn("affine shared workspace: could not settle a workspace", "err", err)
		return "", false
	}
	return workspaceID, true
}

// wantedMemberEmails reads the accounts the identity provider holds and keeps
// the ones that belong to Bloud.
func (c *Configurator) wantedMemberEmails(ctx context.Context, idp configurator.SSOBinding) ([]string, bool) {
	// LocalURL, not BaseURL: this call is made by the host-agent process, which
	// is not on the app's container network, while the identity provider's
	// port is published to the host.
	ak := authentik.NewClient(idp.LocalURL, idp.APIToken)
	managed, err := ak.ListUsers(ctx)
	if err != nil {
		c.logger.Warn("affine shared workspace: could not read the identity provider's users",
			"provider", idp.App, "url", idp.LocalURL, "err", err)
		return nil, false
	}
	wanted := bloudUserEmails(managed)
	if len(wanted) == 0 {
		c.logger.Info("affine shared workspace: no Bloud users to invite", "provider", idp.App)
		return nil, false
	}
	return wanted, true
}

// missingMembers diffs the wanted addresses against the workspace's member
// list, and reports how many members the workspace holds so the summary line
// can say what the invites were added to.
func (c *Configurator) missingMembers(ctx context.Context, workspaceID string, wanted []string) ([]string, int, error) {
	members, err := c.api.workspaceMembers(ctx, workspaceID, memberPageLimit)
	if err != nil {
		c.logger.Warn("affine shared workspace: could not read the member list", "err", err)
		return nil, 0, err
	}
	present := make(map[string]bool, len(members))
	for _, m := range members {
		present[strings.ToLower(strings.TrimSpace(m.Email))] = true
	}
	var missing []string
	for _, email := range wanted {
		if !present[email] {
			missing = append(missing, email)
		}
	}
	return missing, len(members), nil
}

// collectInvites keeps the addresses the instance actually accepted, and logs
// the ones it refused. A partial invite is not a failure: the refused addresses
// are retried on the next pass.
func (c *Configurator) collectInvites(results []inviteResult) []string {
	invited := make([]string, 0, len(results))
	for _, r := range results {
		if r.InviteID == "" || r.Error != nil {
			c.logger.Warn("affine shared workspace: an address was not invited",
				"email", r.Email, "error", r.Error)
			continue
		}
		invited = append(invited, r.Email)
	}
	return invited
}
