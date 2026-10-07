// SPDX-License-Identifier: AGPL-3.0-only

package radicale

import (
	"context"
	"sort"
	"strings"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// peoplePrefix namespaces every personal collection under the owner, so a
// username can never collide with a feed collection (`Movies`, `Shows`) or with
// `family`. It is also the boundary the purge reads: `reality - roster` is taken
// under this prefix and nowhere else.
const peoplePrefix = "people"

// personalMount is where a person's own calendar appears in their own tree.
//
// The name is a constant rather than the person's name on purpose: in your own
// client, "Bob's calendar" is noise. The mount point is where the
// personal-versus-shared distinction should show, and it shows by being called
// Personal next to Family and Movies.
//
// It is also the one mount name a user is likely to have chosen for themselves.
// That is what the occupied-mount check below exists for.
const personalMount = "Personal"

// DirectoryUser is one active Bloud user as the calendar plan sees them.
type DirectoryUser struct {
	Username string

	// DisplayName is the human name from the directory. It becomes the
	// displayname on that person's calendar, which is what a household member
	// reads when the agent shows them whose calendar it is.
	DisplayName string
}

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
	// every recipient reads the same name unless a mount overrides it.
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

	// DisplayName overrides the owned collection's displayname for this mount
	// only. Empty means inherit. This is the share properties overlay, and it
	// is what lets the same collection read "Personal" to its owner and
	// "Bob's calendar" to the agent looking at it.
	DisplayName string
}

// Conflict records a grant the plan dropped rather than issuing.
//
// It is on the plan rather than only in a log line because the caller has to be
// able to act on it and test it. A silently dropped mount is a calendar nobody
// can reach, which is exactly the kind of thing that should not depend on
// someone reading the log at the right moment.
type Conflict struct {
	Recipient string
	Mount     string
	Reason    string
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
	Conflicts []Conflict
}

// planCalendars builds the set from the live directory, the live feed
// bindings, and the mount paths already taken on disk.
//
// It is pure: the same inputs produce the same plan in the same order. The
// order is part of the contract rather than an accident, because renderShares
// sorts stably on the recipient alone and lets the plan's own order decide
// what comes next within a recipient's rows. The family calendar is planned
// first, then the feeds in app order, then the personal calendars by
// username, which is the order the sharing database has always been written in.
func planCalendars(users []DirectoryUser, feeds []configurator.ICSFeedBinding, complete bool, occupied map[string]bool) Plan {
	// The shared audience is every user plus the agent, with the owner
	// excluded. shareRecipients is what keeps the agent in the set even though
	// it is not in the directory listing the people come from, and without it
	// `list_calendars` shows the agent nothing at all.
	audience := shareRecipients(usernamesOf(users))

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

	// One collection per person, owned by the service account and mounted
	// twice: into the person's own tree as Personal, and into the agent's tree
	// under their username.
	//
	// The agent's mount is the whole feature. Consent lives in whether that
	// mount exists at all rather than in a check somewhere, which is why the
	// design can say "off" means the server refuses the write instead of
	// Bloud choosing not to make one.
	for _, u := range sortedUsers(users) {
		cal := Calendar{
			Segment:     peoplePrefix + "/" + u.Username,
			DisplayName: personalDisplayName(u),
		}

		// The person's own mount. Skipped, not silently omitted, when they
		// already have a collection there: a map share shadows whatever is
		// already at that path, and a person who loses the calendar they made
		// themselves to a provisioning step has every reason to be annoyed.
		if occupied[occupiedKey(u.Username, personalMount)] {
			plan.Conflicts = append(plan.Conflicts, Conflict{
				Recipient: u.Username,
				Mount:     personalMount,
				Reason:    "the recipient already owns a collection at this path",
			})
		} else {
			cal.Grants = append(cal.Grants, Grant{
				Principal:   u.Username,
				Mount:       personalMount,
				Perms:       writableShare,
				DisplayName: personalMount,
			})
		}

		// The agent's mount, keyed by username so the URL the agent sees
		// carries the identity the LLM matched on.
		cal.Grants = append(cal.Grants, Grant{
			Principal: agentUsername,
			Mount:     peoplePrefix + "/" + u.Username,
			Perms:     writableShare,
		})

		plan.Calendars = append(plan.Calendars, cal)
	}

	return plan
}

// personalDisplayName is what a household member sees for someone else's
// calendar. The directory's human name when there is one, the username
// otherwise, so a collection is never just "'s calendar". The first letter is
// capitalized because a username arrives lowercase and reads as one.
func personalDisplayName(u DirectoryUser) string {
	name := strings.TrimSpace(u.DisplayName)
	if name == "" {
		name = u.Username
	}
	runes := []rune(name)
	if len(runes) == 0 {
		return name + "'s calendar"
	}
	return strings.ToUpper(string(runes[0])) + string(runes[1:]) + "'s calendar"
}

// sortedUsers returns the people ordered by username, so the plan depends on
// who exists and not on the order the directory happened to hand them over in.
//
// The directory read already sorts, and that is exactly the problem: the plan
// would inherit order-independence only for as long as every caller remembers
// to sort. The personal mounts land inside the agent's own tree, and the render
// sort is stable on the principal alone, so a reshuffle here reorders the
// agent's rows, rewrites sharing.csv, and restarts Radicale for no reason.
// Same reason as sortedFeeds.
func sortedUsers(users []DirectoryUser) []DirectoryUser {
	out := append([]DirectoryUser{}, users...)
	sort.Slice(out, func(i, j int) bool { return out[i].Username < out[j].Username })
	return out
}

func usernamesOf(users []DirectoryUser) []string {
	out := make([]string, 0, len(users))
	for _, u := range users {
		out = append(out, u.Username)
	}
	return out
}

// occupiedKey keys the occupied-mount set. Both halves are path-safe by the
// time they get here, so a plain join cannot make two different pairs collide.
func occupiedKey(recipient, mount string) string {
	return recipient + "/" + mount
}

// shareEntry is one flattened grant: enough to render one row of the sharing
// database without reaching back for the calendar it came from.
type shareEntry struct {
	principal  string
	mount      string
	segment    string
	perms      string
	properties string
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
				principal:  g.Principal,
				mount:      g.Mount,
				segment:    cal.Segment,
				perms:      g.Perms,
				properties: displaynameOverlay(g.DisplayName),
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].principal < out[j].principal })
	return out
}

// displaynameOverlay renders the Properties column for a mount that overrides
// the owned collection's display name.
//
// The single quotes are not a mistake and not a style preference. Radicale's csv
// loader (`sharing/csv.py`, the `DB_TYPES_V1[fieldname] is dict` branch) runs a
// chain of string replacements that expects a Python dict repr and converts it
// toward JSON before calling `json.loads`. Proper JSON arrives with every double
// quote escaped by the first replacement and fails to parse; the single-quoted
// form survives the whole chain. Verified against the pinned image.
//
// The value is always a constant, never a name from the directory, which is what
// makes this safe: an apostrophe in an overlaid value would break the same
// replacement chain that this one survives.
func displaynameOverlay(name string) string {
	if name == "" {
		return "{}"
	}
	return "{'D:displayname': '" + name + "'}"
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
		b += shareRow(e.principal, e.mount, calendarOwner, e.segment, e.perms, e.properties)
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
