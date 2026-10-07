// SPDX-License-Identifier: AGPL-3.0-only

package radicale

import (
	"context"
	"sort"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// A Calendar is one collection Bloud owns and provisions: its identity under
// the owner, what a client calls it, and who it is mounted for.
//
// Making it a value rather than a constant plus a bespoke create function is
// what lets the family calendar, the feed collections, and per-user calendars be
// the same thing. Nothing downstream branches on which kind a calendar is: the
// renderer walks Grants, the creator walks Segment, and a singleton and a
// per-user calendar are indistinguishable to both.
type Calendar struct {
	// Segment is the collection's path under the owner: "family", "Movies",
	// "people/bob". It is the identity of the collection, so two calendars
	// with the same Segment are the same collection.
	Segment string

	// DisplayName is the DAV displayname set on the owned collection at
	// creation. It belongs to the collection rather than to any one mount, so
	// every recipient reads the same name unless a share property overlay says
	// otherwise.
	DisplayName string

	// Grants are the mounts this calendar produces, one per principal. A
	// calendar with no grants is still a collection that exists; it is simply
	// mounted for nobody.
	Grants []Grant
}

// Grant is one principal's mount of a calendar.
type Grant struct {
	// Principal is whose tree the mount appears in: a login, or the agent's
	// service account.
	Principal string

	// Mount is the path segment inside that principal's tree.
	Mount string

	// Perms is the Radicale permission string, writableShare or
	// readOnlyShare.
	Perms string
}

// Plan is the full set of calendars the instance should have, and whether it
// was computed from a directory read that actually succeeded.
//
// Complete is not decoration. Creation can proceed on partial information
// because it adds nothing that was not asked for; deletion cannot, because the
// purge half is a set difference and a silently short plan is a silently large
// purge set. Any consumer that subtracts from this plan must check the flag
// first.
type Plan struct {
	Calendars []Calendar
	Complete  bool
}

// planCalendars builds the set from the live directory recipients and the live
// feed bindings.
//
// It is pure: the same inputs produce the same plan in the same order. The
// order is part of the contract rather than an accident, because renderShares
// sorts stably on the recipient alone and lets the plan's own order decide
// what comes next within a recipient's rows. The family calendar is planned
// first and the feeds follow in app order, which is the order the sharing
// database has always been written in.
func planCalendars(recipients []string, feeds []configurator.ICSFeedBinding, complete bool) Plan {
	// The audience is the same for every shared collection today: every user
	// plus the agent, with the owner excluded. shareRecipients is what keeps
	// the agent in the set even though it is not in the directory listing the
	// people come from, and without it `list-calendars` shows the agent
	// nothing at all.
	audience := shareRecipients(recipients)

	family := Calendar{Segment: familyCollection, DisplayName: familyDisplayName}
	for _, principal := range audience {
		family.Grants = append(family.Grants, Grant{
			Principal: principal,
			Mount:     familyCollection,
			Perms:     writableShare,
		})
	}

	plan := Plan{Complete: complete, Calendars: []Calendar{family}}

	for _, feed := range sortedFeeds(feeds) {
		cal := Calendar{Segment: feed.CalendarName, DisplayName: feed.CalendarName}
		for _, principal := range audience {
			cal.Grants = append(cal.Grants, Grant{
				Principal: principal,
				Mount:     feed.CalendarName,
				Perms:     readOnlyShare,
			})
		}
		plan.Calendars = append(plan.Calendars, cal)
	}

	return plan
}

// shareEntry is one flattened grant: enough to render one row of the sharing
// database without reaching back for the calendar it came from.
type shareEntry struct {
	principal string
	mount     string
	segment   string
	perms     string
}

// orderedGrants flattens the plan into one entry per grant, ordered by
// recipient and then by the plan's own order.
//
// The sort is stable and keyed on the recipient alone on purpose: that keeps
// the plan's order authoritative within a recipient, so the family row still
// precedes the feed rows exactly as it did when the renderer walked the
// constants directly. A total sort on mount name would reorder those rows and
// rewrite sharing.csv, which restarts Radicale on every pass for no reason.
func (p Plan) orderedGrants() []shareEntry {
	var out []shareEntry
	for _, cal := range p.Calendars {
		for _, g := range cal.Grants {
			out = append(out, shareEntry{
				principal: g.Principal,
				mount:     g.Mount,
				segment:   cal.Segment,
				perms:     g.Perms,
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].principal < out[j].principal })
	return out
}

// renderShares renders the csv sharing database from the plan: every granted
// collection mounted into its recipient's own tree.
//
// A map share is what makes discovery work. CalDAV clients enumerate the
// authenticated principal's home to find their calendars, so a collection
// that lives in someone else's tree is invisible until it is mapped in. The
// shared collections live under the service account, and each recipient gets a
// virtual copy under their own.
func renderShares(plan Plan) string {
	var b string
	b += sharesCSVHeader + "\n"
	for _, e := range plan.orderedGrants() {
		b += shareRow(e.principal, e.mount, calendarOwner, e.segment, e.perms)
	}
	return b
}

// ensureCalendars creates every collection the plan calls for that is not on
// the server yet.
//
// It has to be created over DAV rather than written into the storage tree: the
// server owns its own storage format, and the account that owns the collection
// is the one that has to ask for it. That is why this runs in PostStart with
// the owner's credential and not in PreStart with the rest of the files.
//
// Creating is the safe half of the reconcile: a collection that should not have
// been made is a stray directory, so every failure here is a warning rather
// than a fault. The shares are rendered regardless, so until this succeeds
// they point at a collection that is not there yet; the next pass tries again
// and the node keeps serving everything that does work.
//
// One calendar failing does not stop the others. A server that rejects one
// MKCALENDAR should not take the rest of the household's calendars with it.
func (c *Configurator) ensureCalendars(ctx context.Context, plan Plan) error {
	ownerPassword := c.calendarOwnerPassword()
	if ownerPassword == "" {
		c.logger.Warn("radicale sharing: no shared-calendar owner credential yet; no calendars created")
		return nil
	}

	for _, cal := range plan.Calendars {
		path := "/" + calendarOwner + "/" + cal.Segment + "/"

		exists, err := c.api.collectionExists(ctx, path, calendarOwner, ownerPassword)
		if err != nil {
			c.logger.Warn("radicale sharing: could not check for a calendar", "path", path, "err", err)
			continue
		}
		if exists {
			continue
		}
		if err := c.api.createCalendar(ctx, path, calendarOwner, ownerPassword, cal.DisplayName); err != nil {
			c.logger.Warn("radicale sharing: could not create a calendar", "path", path, "err", err)
			continue
		}
		c.logger.Info("created a provisioned calendar", "path", path, "owner", calendarOwner)
	}
	return nil
}
