// SPDX-License-Identifier: AGPL-3.0-only

package radicale

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/authentik"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

const (
	// calendarOwner is the principal that owns every collection the instance
	// shares: the aggregated feeds and the family calendar. It is a Bloud
	// service account rather than a person so the shared state belongs to
	// nobody in particular, and it is a different account from the agent's
	// because that one is read-only by grant and this one is writable by
	// design.
	calendarOwner = authentik.CalendarServiceUsername

	// familyCollection is the shared collection people add events to. The
	// feeds are read-only projections beside it; this is the one that gets
	// written.
	familyCollection = "family"

	// familyDisplayName is what a client shows for the shared calendar.
	familyDisplayName = "Family"

	// agentUsername is the MCP agent's service account. It is not in the
	// directory listing the human recipients come from, because that list
	// exists to enumerate people and deliberately drops service accounts. The
	// agent still needs the shares mounted in its own tree for `list-calendars`
	// to see anything, so it is added to the set here rather than being
	// something the directory has to remember.
	agentUsername = authentik.CalDAVServiceUsername

	// readOnlyShare and writableShare are Radicale permission strings.
	//
	// Radicale spells calendar permissions in lowercase and generic-collection
	// permissions in uppercase (radicale/rights/__init__.py: `r` reads address
	// book and calendar collections, `w` writes them, `R` and `W` are the same
	// for everything else). Both cases go in each grant so the share means the
	// same thing whichever type the collection ends up carrying.
	readOnlyShare = "Rr"
	writableShare = "RWrw"
)

// renderShares renders the csv sharing database: every shared collection
// mounted into every recipient's own tree.
//
// A map share is what makes discovery work. CalDAV clients enumerate the
// authenticated principal's home to find their calendars, so a collection
// that lives in someone else's tree is invisible until it is mapped in. The
// feeds and the family calendar live under the service account, and each
// recipient gets a virtual copy under their own.
//
// The output is ordered by recipient and then by mount name so re-rendering
// the same set is byte-identical and asks for no restart.
func renderShares(recipients []string, feeds []configurator.ICSFeedBinding) string {
	var b strings.Builder
	b.WriteString(sharesCSVHeader)
	b.WriteString("\n")

	for _, recipient := range shareRecipients(recipients) {
		b.WriteString(shareRow(recipient, familyCollection, calendarOwner, familyCollection, writableShare))
		for _, feed := range sortedFeeds(feeds) {
			if !feedComplete(feed) {
				continue
			}
			b.WriteString(shareRow(recipient, feed.App, calendarOwner, feed.App, readOnlyShare))
		}
	}
	return b.String()
}

// shareRow renders one map share: the owner's real collection mounted at
// `recipient/mount/` in the recipient's tree.
//
// EnabledByOwner and EnabledByUser are both pre-set true. A share that needs
// the recipient to accept it is a share nobody accepts: Bloud has no screen
// for that, and the whole point is that the calendars are simply there.
func shareRow(recipient, mount, owner, owned string, perms string) string {
	return fmt.Sprintf("map;/%s/%s/;/%s/%s/;none;%s;%s;%s;True;True;False;False;0;0;{};{}\n",
		recipient, mount, owner, owned, owner, recipient, perms)
}

// shareRecipients composes the full set of principals the shared collections
// are mounted for: every user passed in, plus the agent's service account.
//
// The agent gets the same grants as the family: read-only on the feeds, and
// read-write on the family calendar. That write grant is where agent-created
// events land, and it is the reason the shared tree is owned by a service
// account rather than by a person: an agent writing into somebody's personal
// calendar is the failure this ownership model exists to prevent.
func shareRecipients(users []string) []string {
	withAgent := append(append([]string{}, users...), agentUsername)
	return sortedRecipients(withAgent)
}

// sortedFeeds returns the complete feeds ordered by app name, so the rendered
// database depends on which feeds exist and not on the order the resolver
// happened to put them in. Without this a reshuffle of the binding list
// rewrites sharing.csv and restarts Radicale for no reason.
func sortedFeeds(feeds []configurator.ICSFeedBinding) []configurator.ICSFeedBinding {
	out := make([]configurator.ICSFeedBinding, 0, len(feeds))
	for _, f := range feeds {
		if feedComplete(f) {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].App < out[j].App })
	return out
}

// sortedRecipients returns the recipients deduplicated and in a stable order,
// with the owner removed. The owner owns its own collections and does not need
// a share pointing at them; a self-share would render a second entry for the
// same collection in the same tree.
func sortedRecipients(recipients []string) []string {
	seen := make(map[string]bool, len(recipients))
	out := make([]string, 0, len(recipients))
	for _, r := range recipients {
		r = strings.TrimSpace(r)
		if r == "" || r == calendarOwner || seen[r] {
			continue
		}
		seen[r] = true
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

// feedComplete is the bar a feed has to clear before it is synced and shared:
// installed, addressed, and holding a published key. A share for a feed with
// no collection behind it is a calendar that shows up empty forever, which
// reads as a bug in the feed rather than as a feed that is not ready.
func feedComplete(feed configurator.ICSFeedBinding) bool {
	return feed.Installed && feed.APIKey != "" && feed.Path != "" && feed.BaseURL != ""
}

// calendarRecipients lists the logins that every shared collection is mounted
// for: everyone on the instance.
//
// The identity provider is the only place this answer lives, and its user list
// already excludes service accounts and its own built-in admin. Inactive
// accounts are dropped here too: a deactivated account is not somebody's
// family member, and a share for it is a live grant to a door that is shut for
// a reason.
//
// The bool is the load-bearing part. It separates "there is nobody to share
// with" from "we could not ask", and the caller must not treat the second as
// the first: rendering an empty list because the provider was briefly
// unreachable would strip every user's calendars.
func (c *Configurator) calendarRecipients(ctx context.Context, state *configurator.AppState) ([]string, bool) {
	idp, bound := identityProvider(state)
	if !bound {
		c.logger.Info("radicale sharing: no identity provider token, cannot list users")
		return nil, false
	}

	// LocalURL, not BaseURL: this call is made by the host-agent process, which
	// is not on the app's container network, while the provider's port is
	// published to the host.
	ak := authentik.NewClient(idp.LocalURL, idp.APIToken)
	users, err := ak.ListUsers(ctx)
	if err != nil {
		c.logger.Warn("radicale sharing: could not read the identity provider's users",
			"provider", idp.App, "err", err)
		return nil, false
	}

	recipients := make([]string, 0, len(users))
	seen := make(map[string]bool, len(users))
	for _, u := range users {
		if !u.IsActive {
			continue
		}
		name := strings.TrimSpace(u.Username)
		if name == "" || name == calendarOwner || name == agentUsername || seen[name] {
			continue
		}
		seen[name] = true
		recipients = append(recipients, name)
	}
	sort.Strings(recipients)
	return recipients, true
}

// identityProvider picks the resolved `sso` binding that carries an API token,
// which is the one the user list can be read with. Declaring the contract alone
// gets the provider's address; `apiToken` is what opens the directory.
func identityProvider(state *configurator.AppState) (configurator.SSOBinding, bool) {
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

// ensureFamilyCalendar creates the shared family calendar under the service
// account if it is not there yet.
//
// It has to be created over DAV rather than written into the storage tree: the
// server owns its own storage format, and the account that owns the collection
// is the one that has to ask for it. That is why this runs in PostStart with
// the owner's credential and not in PreStart with the rest of the files.
//
// A failure is a warning, not a fault. The shares for the family calendar are
// rendered regardless, so until this succeeds they point at a collection that
// is not there yet; the next pass tries again, and the node keeps serving
// everything that does work.
func (c *Configurator) ensureFamilyCalendar(ctx context.Context) error {
	ownerPassword := c.calendarOwnerPassword()
	if ownerPassword == "" {
		c.logger.Warn("radicale sharing: no shared-calendar owner credential yet; family calendar not created")
		return nil
	}

	path := "/" + calendarOwner + "/" + familyCollection + "/"
	exists, err := c.api.collectionExists(ctx, path, calendarOwner, ownerPassword)
	if err != nil {
		c.logger.Warn("radicale sharing: could not check for the family calendar", "err", err)
		return nil
	}
	if exists {
		return nil
	}
	if err := c.api.createCalendar(ctx, path, calendarOwner, ownerPassword, familyDisplayName); err != nil {
		c.logger.Warn("radicale sharing: could not create the family calendar", "path", path, "err", err)
		return nil
	}
	c.logger.Info("created the shared family calendar", "path", path, "owner", calendarOwner)
	return nil
}

// calendarOwnerPassword reads the shared-calendar owner credential out of this
// app's secret scope. The Authentik configurator publishes it there during its
// own convergence, which happens before this node is reached.
func (c *Configurator) calendarOwnerPassword() string {
	if c.secrets == nil {
		return ""
	}
	return c.secrets.GetAppSecret(appName, authentik.CalendarOwnerSecretKey)
}
