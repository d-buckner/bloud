> Status: designed. The decisions are recorded and the build order is sequenced;
> nothing is built yet.
>
> Facts verified against the tree at `d711f54` and against the pinned
> `ghcr.io/kozea/radicale:3.8.1` image on 2026-10-07.

# Per-user calendars

The ask: **provision a personal calendar for each user, so that in Hermes I can
ask to add a todo to a specific person.**

## The design in seven lines

1. A calendar is a **value**: a path segment under a service-account owner, a
   display name, and a list of grants. Family is a singleton instance of it;
   personal calendars are a per-user instance; the Radarr and Sonarr feeds are
   a third.
2. One pure function, `planCalendars`, produces the whole set from the live
   directory and the live feed bindings. Creation and sharing both consume that
   one value, so they cannot disagree.
3. Personal collections are owned by `calendar-service` at
   `/calendar-service/people/<user>/` and map-shared into the user's tree and
   the agent's tree, because Bloud cannot authenticate as the user and
   therefore cannot create anything in it.
4. The agent resolves "Bob" from the display name and the URL that
   `list_calendars` already returns. `dav-mcp` is third-party: Bloud shapes the
   data it sees, not the tools it offers.
5. Consent is a two-state switch on the agent's mount, `direct` or `off`, set
   per person. `off` means no mount, and `owner_only` enforces that the agent
   cannot reach the calendar at all.
6. Ship creation first. The purge half is the same diff pointed the other way,
   and it is the only part of this design that can lose data.
7. **No migration is required.** The feature is additive. What needs care is
   the mount-name collision with calendars users already made, and keeping
   deactivation out of the purge set.

## The ask, decomposed

That sentence is four problems, and they get conflated.

| # | Problem | What it actually asks |
|---|---|---|
| 1 | Provisioning | Does a collection exist for each person before that person ever opens a calendar client? |
| 2 | Resolution | What turns the word "Bob" into a collection the agent can address? |
| 3 | Authorization | Who may write into Bob's personal calendar, and can anyone tell afterwards? |
| 4 | Semantics | Is "add a todo to Bob" a calendar write, or a hand-off? |

Problem 4 is worth arguing first, because it changes what 1 through 3 are for.
"Put this on *my* calendar" is a calendar write. "Add a todo to *Bob*" is a
message that happens to be delivered through Bob's task list. The two look
identical in the tool call and are not identical in what they require: writing
your own calendar needs no permission, writing someone else's is a household
policy.

Bloud should be able to do both. It should not pretend they are the same
operation.

## What exists today

| Fact | Where |
|---|---|
| Radicale serves CalDAV/CardDAV, authenticated against Authentik over LDAP (`ldap` strategy) | `apps/radicale/metadata.yaml` |
| Rights backend is `owner_only`: a principal may touch only its own top-level path | `apps/radicale/configurator.go`, generated `[rights]` section |
| Cross-tree access is csv **map shares**, re-rendered every pass | `apps/radicale/sharing.go:renderShares` |
| The people who get shares come from the identity provider's user list, active accounts only | `apps/radicale/sharing.go:calendarRecipients` → `pkg/authentik.ListUsers` |
| Shared collections are owned by `calendar-service`, a Bloud service account | `pkg/authentik/calendar_service.go` |
| The agent authenticates as `caldav-service` and sees its own tree | `pkg/authentik/caldav.go`, `apps/dav-mcp/INTEGRATION.md` |
| Today the agent's tree holds one collection: `/caldav-service/family/`, components `VTODO, VEVENT, VJOURNAL` | live `list_calendars` output in `apps/dav-mcp/INTEGRATION.md` |
| Writes into an existing collection work end to end; `MKCALENDAR` into a foreign tree is refused | `apps/dav-mcp/INTEGRATION.md`, "Collection creation is denied by the rights backend" |
| Collections Bloud wants are created over DAV in `PostStart`, PROPFIND then MKCALENDAR | `apps/radicale/configurator.go:ensureFamilyCalendar`, `ensureFeedCalendars` |
| `dav-mcp` exposes 27 tools including to-do and free/busy tools | `apps/dav-mcp/INTEGRATION.md` |

So the machinery for "a collection exists that Bloud created and several people
can write" already ships. It is called the family calendar. What follows is
mostly generalizing it, plus two things that are not generalizations at all:
resolution and attribution.

## This is the family calendar, N times

The family calendar already does the whole thing: a collection Bloud creates,
owned by a service account, shared to the people who use it and to the agent,
re-rendered from the directory on every pass. A personal calendar is that same
object with a computed name and a narrower recipient list.

| Aspect | Family calendar today | Per-user |
|---|---|---|
| Owner | `calendar-service` | `calendar-service`, same |
| Created by | `ensureFamilyCalendar`, `PostStart`, PROPFIND then MKCALENDAR | the same loop, once per user |
| Path | `/calendar-service/family/` | `/calendar-service/people/<user>/` |
| Display name | `"Family"`, a constant | `"Bob's calendar"`, computed |
| Share render | `renderShares`, every pass, from the directory | the same function |
| Recipients | every active user plus the agent | that user plus the agent |
| Permissions | `RWrw` | `RWrw` |
| Restart on change | yes, Radicale reads shares at process start | same |

Only three things are genuinely new, and none of them is the mechanism:

1. **The name is computed rather than a constant.** `familyCollection` becomes a
   function of the user.
2. **The recipient set is 1:1 rather than a fan-out.** Family shares to
   everyone; a personal calendar shares to one person and the agent. That is a
   different argument to the same `shareRow`, not a different row shape.
3. **It can be deleted.** The family calendar never dies. A personal one does,
   with the user, and that is where all the real risk in this design lives.

Everything else is a loop around code that already works. That is the argument
for this shape: it inherits a proven path rather than opening a new one.

**The consent switch is optional, which is what makes the first slice exactly
this.** Ship without it and every personal calendar behaves like the family
calendar: created for the user, mounted for the user, writable by the agent.
The preference only decides whether the agent keeps that write mount, so it is
a subtraction from the shipped shape rather than an addition to it, and it can
arrive in its own change later.

## The constraint that decides ownership

**Bloud cannot authenticate as a user over DAV.**

A Bloud password lives in Authentik. LDAP verifies it and never reveals it, and
Bloud stores no copy. The configurator can create a service account because it
mints that credential; it cannot mint Alice's, and it cannot borrow hers.

Combine that with `owner_only`, verified in the pinned image:

```python
# radicale/rights/owner_only.py
def authorization(self, user: str, path: str) -> str:
    ...
    if self._verify_user and user != sane_path.split("/", maxsplit=1)[0]:
        return ""
```

Nobody, including `calendar-service`, gets rights in a path whose first segment
is not their own name. So **Bloud cannot create `/alice/Personal/`**. Only
Alice can, and only after she configures a client, which is exactly the thing
the ask wants to not depend on.

Three ways out, and only one of them is good.

| Approach | Verdict |
|---|---|
| Let the user's client create its own calendar on first sync | The collection may not exist when the agent needs it, and its display name and component set are whatever the first device chose. Fails problem 1 outright. |
| Switch `[rights] type = from_file` and grant `calendar-service` write into every tree | Works, and is the wrong tool. It grants the agent's owner read and write over everything in every user's tree, including the collection they create next month that they meant to keep private. The share list is narrower by construction: the agent gets exactly the collections Bloud declares, one row at a time. |
| Own it with a service account and map-share it into the user's tree | **This is the one.** It is the family calendar's existing shape, per person. The user's client sees a normal calendar in its own home; the agent can address it; nothing is broader than the row that grants it. |

The cost of that choice, stated plainly: a person's calendar data lives under a
Bloud service account rather than under their own principal. On a single-tenant
home server that is a placement detail, not a confidentiality change. What it is
not is a limitation to design around later: it is what makes the calendar
provisionable at all.

## Proposed shape

Owner: `calendar-service`. One collection per active Bloud user, keyed by
username, under a `people/` prefix so a username can never collide with a feed
collection (`Movies`, `Shows`) or with `family`.

```
calendar-service's tree (the real storage)
  /calendar-service/family/            shared, exists today
  /calendar-service/people/bob/        new: Bob's personal calendar
  /calendar-service/people/alice/      new

bob's tree (what bob's phone enumerates)
  /bob/Personal/        map -> /calendar-service/people/bob/      RWrw
  /bob/Family/          map -> /calendar-service/family/          RWrw   (exists today)
  /bob/Movies/          map -> /calendar-service/Movies/          Rr     (exists today)

caldav-service's tree (what the agent enumerates, for a `direct` preference)
  /caldav-service/family/                                        RWrw   (exists today)
  /caldav-service/people/bob/     map -> /calendar-service/people/bob/   RWrw
  /caldav-service/people/alice/   map -> /calendar-service/people/alice/ RWrw
```

The agent's tree is the one that carries the consent decision, and it is why the
diagram is drawn per-tree: what the agent can write is exactly what is mounted
for it, and nothing else.

Two properties of Radicale's map implementation matter here, both verified in
the pinned image:

- **A map share resolves by prefix replacement**
  (`sharing/__init__.py:660`, `path.replace(parent_path, result['PathMapped'])`),
  so nesting under `people/` works, and so does a mount at depth 2 in the
  recipient's tree.
- **Sharing a parent shares everything beneath it.** A row for
  `/calendar-service/people/` would hand the recipient every person's calendar.
  Shares must be rendered per leaf, never per prefix. This is a rule worth a
  test, because it is invisible in the metadata and total when broken.

Display names are the part that carries user-visible weight. Proposal:

| Collection | `displayname` | Rationale |
|---|---|---|
| `/calendar-service/people/bob/` | `Bob's calendar` | What a household member sees in the shared list. |
| mounted at `/bob/Personal/` | `Personal` | In your own client, "Bob's calendar" is noise. The mount point is where the personal-vs-shared distinction should show. |

Radicale derives supported components from the collection, and the family
calendar already advertises `VTODO, VEVENT, VJOURNAL` from a `MKCALENDAR` that
set nothing but a display name, so a personal calendar gets to-do support
without a change to `createCalendar`.

## The abstraction: a calendar is a value

Today a calendar's identity is smeared across three places. `familyCollection`
and `familyDisplayName` are constants, `ensureFamilyCalendar` knows how to make
that one collection, and `renderShares` knows who it goes to. The feeds are the
same shape again with a different source of names. Adding a per-user calendar to
that structure means adding a fourth place that has to agree.

The fix is to make a calendar a value and have exactly one function decide what
the set of them is.

```go
// A Calendar is one collection Bloud owns: its identity under the owner, what
// it is called, and who it is mounted for. Singleton and per-user calendars are
// the same type; nothing downstream branches on which kind it is.
type Calendar struct {
    // Segment is the path under the owner: "family", "people/bob".
    Segment string
    // DisplayName is the DAV displayname on the owned collection.
    DisplayName string
    // Grants are the mounts this calendar produces, one per recipient.
    Grants []Grant
}

// Grant is one recipient's view of a calendar.
type Grant struct {
    Principal string // "bob", "caldav-service"
    Mount     string // segment in the recipient's tree: "family", "Personal"
    Perms     string // writableShare, readOnlyShare
}

// planCalendars is the only place that knows the difference between a
// singleton and a per-user calendar. It is pure: the same directory and the
// same feed bindings always produce the same set, in the same order.
func planCalendars(users []string, feeds []ICSFeedBinding) []Calendar
```

Two consumers, both dumb loops over the plan:

```go
// PROPFIND then MKCALENDAR, once per calendar, as the owner.
func ensureCalendars(ctx context.Context, plan []Calendar) ([]string, error)

// One nested loop: calendar, then grant. Sorted for byte stability.
func renderShares(plan []Calendar) string
```

**Why the plan function is the point, not the types.** Creation and sharing
cannot disagree about the set of calendars, because they read the same value.
Today they can: `ensureFamilyCalendar` and `renderShares` agree only because
both reference the same constant, and the feed path agrees only because both
read the same contract. A plan makes the agreement structural. It is also the
whole design surface in one pure function, which means the interesting
questions (who sees what, what is called what) are answerable by reading one
function and testing it without a server.

The three kinds the plan emits, which is why this is not speculative
generalization:

| Kind | Segment | DisplayName | Grants |
|---|---|---|---|
| Singleton, shared | `family` | `Family` | every user `RWrw`, agent `RWrw` |
| Singleton, from a feed | `Movies`, `Shows` | from `icsFeed.calendarName` | every user `Rr`, agent `Rr` |
| Per-user | `people/bob` | `Bob's calendar` | that user `RWrw`, agent `RWrw` |

The feed's *content source*, the pimsync sidecar and its sync pairs, is not
part of this abstraction and should not be. A calendar value says a collection
exists and who may see it. Where its contents come from is a separate concern
that happens to have a container attached.

### One thing the value has to account for

**A display name belongs to the collection, not to the mount.** The current
family setup gets away with one name because the mount segment and the display
name are effectively the same word. A personal calendar wants them different: the
agent should read "Bob's calendar" and Bob should read "Personal" in his own
tree.

That is expressible, and it is not currently used. The share row has a
`Properties` column, always `{}`, gated by `[sharing]
permit_properties_overlay`, which lets a share override the display name for
its recipient. So the design has three options and should pick one explicitly
rather than discover the mismatch during implementation:

1. **One name, accept it.** The collection is "Bob's calendar" and Bob sees
   "Bob's calendar" in his own tree. Simplest, mildly redundant.
2. **Name it "Personal" and let the agent's list be ambiguous.** Bob sees
   "Personal", and the agent disambiguates by URL rather than by name.
3. **Use the properties overlay.** Different display names per recipient. The
   most correct and the only option that touches a Radicale feature Bloud does
   not currently exercise, so it needs its own verification.

Recommendation: start with 1. It costs nothing, it is honest, and the agent's
resolution already works off the URL. Options 2 and 3 are both reachable later
without changing the type, because `DisplayName` and `Mount` are already
separate fields.

## Purging is the same diff, pointed the other way

Once the plan exists, deletion stops being a special case. The reconcile has two
halves:

```
create: plan - reality        (in the plan, not on the server)
purge:  reality - roster     (on the server, not in the roster, under owned prefixes)
```

Note that the two halves read **different things**, and that is not an
asymmetry to smooth over.

**The purge must not read the plan.** `calendarRecipients` filters on
`IsActive`, so a deactivated user is absent from the plan, and `reality - plan`
would purge their calendar the moment somebody is deactivated. That directly
contradicts the decision that deactivation is not deletion. The create half
wants *active* users. The purge half wants *confirmed absent from the
directory*, which includes deactivated accounts. `ListUsers` returns both, so
the distinction is available; what it is not is something the current recipient
list can express. The purge's negative space is the roster, not the plan.

That framing still puts the risk where it belongs: **the purge set is a set
difference, so anything that wrongly shrinks the roster wrongly grows the purge
set.** A truncated user list, a failed directory read, a feed binding that
resolved empty: each is a smaller set, and each is a bigger pile of things to
delete.

Which is what makes the safety conditions mechanical rather than advisory:

- The roster must be marked complete. `calendarRecipients` already returns
  `(users, enumerated)`; the purge half must refuse to run on an incomplete
  read. Creation can proceed on partial information because it adds nothing
  that was not asked for. Deletion cannot.
- The roster must be *complete*, not merely non-empty. `pkg/authentik.ListUsers`
  requests `page_size=200` and does not paginate, so past 200 users the roster
  is silently short and everyone beyond is in the purge set. Compare the API's
  reported total against the length of the returned list and refuse to delete
  on disagreement.
- The purge is bounded to owned prefixes. Today that is `people/`. Family and
  feed collections are never in scope, so a wrong roster cannot reach them.

The asymmetry is the whole lesson: the create half of this design is safe by
construction, and the delete half is safe only if the plan is provably whole.
Ship the create half first, let it run for a while, and only then wire the half
that can lose data.

**Where it runs: not in the user-delete handler.** That handler would need the
Radicale address and the `calendar-service` credential, which crosses the
settings boundary and can fail half way through, leaving shares removed and the
data behind. Running it as the purge half of the reconcile keeps it symmetric
with creation, idempotent, self-healing, and inside the orchestrator as the
single writer.

**What a purge is concretely:** a DAV `DELETE` on the collection as
`calendar-service`. The share rows need no separate cleanup, because they were
rendered for the recipient who is now gone and drop out of the next plan.

**What is lost:** everything in that person's personal calendar, including any
agent-written history. That belongs in the delete confirmation in the UI rather
than in a doc nobody reads at the moment it matters. If that confirmation is
not acceptable to the household, the alternative is a timed purge, which needs
a retention clock Bloud has no home for today.

## Migration

**The core feature needs none.** Creating per-user calendars is purely
additive: new empty collections under a prefix that does not exist yet, plus new
share rows. No existing collection is touched, renamed, or moved. Phase 1's
byte-identical requirement means the refactor does not even produce a
share-file change, so an upgrade that ships phases 1 and 2 does not restart
Radicale for a reason nobody can explain.

Four things are migration-adjacent and need an explicit answer rather than a
shrug.

### A mount name that collides with a calendar the user already made

This is not a new problem. It is a known one, multiplied. The family calendar's
own integration notes already record it:

> A user who already owns a collection named `family`. The family calendar is
> mounted at `/<user>/family/`, so a personal collection with that name
> collides with the mount. Nothing detects it; the name is reserved by
> convention.

Reserved-by-convention is tolerable for one collection with one name. It is not
tolerable when every user gets a mount whose entire purpose is being *their*
calendar. And the failure mode is worse than a warning in a log: the map
resolver intercepts the path before the storage lookup, so the share shadows the
user's own collection. Alice keeps writing into what she believes is her
calendar while actually writing into the mapped one, or her existing events stop
being reachable. The data is not lost, but it lands in the wrong place and she
has no way to know.

Two mitigations:

- **Detect before granting.** PROPFIND the recipient's own mount path during
  the plan pass; if a collection already sits there and is not the mapped one,
  skip the grant and warn. Costs one round trip per user per pass, and it turns
  a silent shadowing into a log line and a missing mount.
- **Pick a mount name no client would choose.** Less likely to collide, but
  "less likely" is the same class of argument that produced the current note.

Recommendation: detect. The round trip is cheap against the alternative, and it
is the same PROPFIND the create half already issues.

### The `people/` prefix against an account named `people`

A directory account literally named `people` would make
`/calendar-service/people/` both a user home and the prefix this design treats
as reserved. Reject it at the plan level with a clear message rather than
discovering it in the storage tree: the prefix is reserved, and a username that
collides with it is a directory problem that should say so out loud.

### Rollback leaves residue

Reverting the feature after it ships removes the share rows, so the collections
become invisible, but they and their data stay on disk. That is less a
migration problem than a reason not to describe the feature as reversible. The
existing alpha precedent is the same shape: installs made before the shared
calendar owner existed left their synced feeds under the operator's tree, and
the notes say plainly that they are "documented, not migrated".

### What the purge must not do on upgrade

Phase 5 must not treat legacy collections as orphans. Today the purge prefix is
`people/`, which contains only what the plan created, so `reality - roster` is
empty on the first purge pass and the upgrade is safe. That is a property of
the prefix choice, not of the logic, and it stops being true the moment anyone
widens the prefix to sweep legacy collections. If the sweep is ever widened, it
needs an adoption pass first: existing collections are claimed into the roster,
not deleted.

## Splitting events from to-dos: the product consequences

This is a separate axis from the consent question below. The question here is
whether a person's `VEVENT` and `VTODO` share one collection or get two.

It looks like a schema choice. It is a question about what a todo *is* to the
person receiving it, and the two answers are visible to them every day.

| What the person notices | One collection | Calendar + Tasks |
|---|---|---|
| Their month view | A to-do with a due date renders on the day it is due, next to their appointments. Task noise in the schedule. | Tasks live in a collection they can uncheck. The grid stays clean. |
| Can they separate the two views? | Mostly no. Most clients filter by *calendar*, not by component within one calendar, so "hide my to-dos from my schedule" is not generally available. | Yes, and this is the only way to get it. |
| Their task app | The collection shows up as a task list because it contains `VTODO`. Same collection, read two ways in two apps. | Same, except the reading matches the name. |
| What the agent must disambiguate | Exactly one target per person. "Add it to Bob" cannot land wrong. | Two plausible targets. "Dentist at 3" is an event, "buy milk" is a task, and a wrong pick shows up in the wrong list. |
| Whether the name is honest | "Bob's calendar" is doing two jobs and lies about one of them. | "Bob's calendar" and "Bob's tasks" are each honest. |
| Filing mistakes later | Nothing to move. | The agent files a task, the person wanted a calendar entry, and that is a manual move. |
| Share surface | 2 rows per person: one mapped to them, one to the agent | 4 rows per person. Every new person adds rows and every change restarts Radicale. At household scale this is noise, not a limit. |

One row deserves its own paragraph because it undercuts the second column more
than the table shows: **the split is not enforced.** Radicale derives a
collection's advertised components from what it is, and the family calendar was
created with nothing but a display name yet reports `VTODO, VEVENT, VJOURNAL`.
A collection named "Bob's tasks" accepts events exactly as happily as one named
"Bob's calendar". The separation is a naming convention the agent has to
respect, not a wall the server holds up.

So the second collection buys one thing, the clean month view, and that thing
is worth something every day to a person who lives in their calendar. It costs
a permanent disambiguation the agent has to win on every request, for a
boundary that nothing enforces.

**Recommendation: one collection.** The agent has a single target so the ask
cannot misfire; the separation the second collection offers is not enforced
anyway; and a person who genuinely wants their tasks separated can create a
second collection in their own tree, which their client will do without Bloud.
The upgrade path stays open because the machinery is identical: adding the
second collection later is one more entry in the same create-and-share loop,
not a redesign.

**The case that should flip it:** a household that lives in a dedicated task
client rather than a calendar client. Those clients want a to-do collection and
behave oddly with a mixed one, so for that household the two-collection shape is
the right call. That is a fact about the household, not about the code, and it
is worth asking before the first collection is created rather than after.

## Provisioning mechanics

Everything below is the existing pattern applied to a bigger set. No new
lifecycle phase, no new contract, and no new shape in the configurator: the
plan function replaces the constants and the two loops replace the two
bespoke `ensure*` functions.

**Trigger.** The user list is already enumerated on every pass by
`calendarRecipients`, and that list is the plan's input: a user appears in the
directory, the next pass creates their calendar. A pass that cannot read the
directory already leaves the shares file untouched rather than rendering an
empty set, and the same rule has to cover creation: unknown is not empty, and a
blinking identity provider must not stop provisioning nor imply that people
were deleted.

**Ordering: the window is real, small, and already shipped.** Radicale's
`database_verify()` does reject a share whose `PathMapped` target does not
exist:

```python
# radicale/sharing/__init__.py, database_verify()
item = next(iter(self._storage.discover(entry['PathMapped'])), None)
if not item:
    logger.error("sharing database verification content failed: %r", ...)
    return False
```

But `verify()` is only reachable from the `--verify-sharing` CLI
(`__main__.py:219`). The runtime `sharing.load()` path never calls it: it
checks whether sharing is enabled and initializes the database, and nothing
else. A dangling share at runtime resolves to a collection that 404s until it
appears.

Which is why the family calendar works today with exactly that window. The
shipped order is shares-then-collection: `PreStart` writes `sharing.csv` at
line 155, the container starts and reads it, and only then does `PostStart`
create `/calendar-service/family/` at line 267. So **no restructuring of
`syncShares` is needed.** The per-user case inherits the same one-pass window
the family case has always had, and it resolves the same way: the collection
exists before anything a person would notice.

What `--verify-sharing` is good for is the opposite of a hazard: it is a free
post-provision assertion. Running it after a provisioning pass and failing
loudly when the sharing database is inconsistent catches a render bug before a
household does.

The one ordering rule worth keeping is the cheap one: create in `PostStart`
before the next pass re-renders, so the window never widens. Not a new
invariant, just the existing order.

**Idempotency.** `managedfile.Write` reports `changed=false` on identical
bytes, and the shares file is sorted by recipient then mount, so a steady-state
pass writes nothing and restarts nothing. Invariant 2 holds: creation is a
read-only diff once everyone has a calendar.

**Deactivation.** An inactive user drops out of `calendarRecipients`, their
share rows disappear, and their phone loses the calendars on the next pass.
Deactivation is not deletion: the collections stay, and reactivation restores
the mounts. Purging is reserved for actual deletion, and it is a much more
dangerous operation than the create half of this design. See "Purging a deleted
person".

**Rename.** There is no user-rename endpoint today (`settings_users.go` offers
create, delete, and role change), so keying by username is safe now. When
rename arrives, the key breaks: the collection is orphaned under the old name
and a fresh one appears under the new. The fix is a DAV `MOVE` keyed on the
Authentik PK, which is stable across renames. Out of scope for this slice, and
it should be recorded in the app's `INTEGRATION.md` under "What is not wired"
rather than discovered later.

**Deletion** purges the collections. That is a decision with a safety section
of its own rather than a paragraph here.

## Resolution: how the agent finds Bob

The binding constraint: **`dav-mcp` is a third-party app.** Bloud shapes the
data it is pointed at; it cannot add tools to it. So resolution has to ride on
what the existing 27 tools already return, which is calendars (display name
and URL) and contacts.

| Option | How it works | Assessment |
|---|---|---|
| A. Display-name matching over `list_calendars` | The agent's tree literally contains one collection per person. `list_calendars` returns "Bob's calendar" and its URL. The LLM matches the name. | Free, and better than it sounds for an LLM. The URL carries the username (`/caldav-service/people/bob/`), so the mapping is machine-checkable, not just vibes. Fails on nicknames and on two people named Bob. |
| B. A Bloud-provisioned household address book | CardDAV contacts generated from the directory, each contact carrying the person's calendar URL. The agent does `search_contacts("Bob")` and reads the URL off the contact. | Fuzzy matching for free, and a shared address book is worth having anyway. Costs a second provisioning surface, and puts the mapping in user-editable data that can drift from the calendar set. |
| C. A `peopleDirectory` contract rendered into the wrapper's config | Radicale provides the roster; `dav-mcp` exposes a `list_people` tool. | The most structured, and it needs a fork. Rejected for this slice on the third-party constraint above. |

Recommendation: **A now, B as a follow-up that is independently useful.** A is
a naming convention plus a prompt convention. If the agent needs help, the
`displayname` can carry both the human name and the handle (`Bob Mitchell
@bob`), which makes the match explicit rather than inferred.

What A does require is that the convention be *stated somewhere the agent
reads*. Hermes has persistent memory and a configurable prompt; the
`dav-mcp` app's own description is what Hermes sees for the namespace. That is
the place to say "one collection per household member, named after them; the
username is in the URL".

## Trust, attribution, and what is not provable

`docs/features/mcp.md` already states the limit: *one published credential
means one principal. No per-user MCP authorization.* Nothing here changes that,
but personal calendars make it newly sensitive. Anyone who can talk to Hermes
can write anyone's personal calendar, and after the fact there is no way to say
who asked.

That gap can be narrowed but not closed at this layer:

- **Attribution is possible.** Agent-written items can carry an
  `ORGANIZER;CN=Bloud Agent`, a `CATEGORIES:bloud-agent`, and a
  `X-BLOUD-AGENT-REQUESTED-BY:<name>` property. The user can then see, in
  their own client, that a thing arrived through the agent.
- **Provenance is not.** `X-BLOUD-AGENT-REQUESTED-BY` is a claim the agent
  makes, not a fact Bloud verified, because the bearer has no user attached to
  it. Writing it down is still worth doing, but it must not be described as an
  audit trail. The real thing needs per-user agent identity, which is what
  [`client-credentials.md`](client-credentials.md) is working toward, and the
  auth bypass in `docs/operations/tech-debt.md` is what has to be repaid
  before any of it can be trusted.
- **Consent is enforced structurally, which is the one thing that is not
  negotiable here.** See the next section. Whatever the household decides, the
  agent's reach is the set of mounts it is given, and that set is rendered by
  Bloud.

## Consent: the recipient decides, with one switch

"Where does an agent-written todo land" is answered by the person whose calendar
it is, not by the operator and not by the agent. That is a better answer than a
global switch, and it is also the answer this architecture enforces most
cheaply.

With one collection per person, that answer is a two-state switch on the agent's
mount:

| Preference | The agent's tree gets | Consequence |
|---|---|---|
| `direct` | `/caldav-service/people/<user>/` mapped to the personal calendar | "Add a todo to Bob" writes into Bob's calendar. |
| `off` | no mount for this person | The agent cannot reach Bob's calendar at all, and says so. |

**Why this is enforcement and not policy.** `owner_only` means the agent can
touch nothing that is not mounted in its own tree. A preference of `off` means
the collection is not mounted for the agent, so a write to it is refused by the
server rather than by a check the agent might forget to run. The consent is the
absence of a mount. That is the strongest guarantee available at this layer,
and it costs one condition in `renderShares`.

Where the preference lives is the expensive part, and it is not what this plan
first claimed. `store.PreferencesStore` keys a `user_preferences` row by
username, but its only payload is `layout`, a JSON blob holding the dashboard
grid. There is no generic per-user preference field to hang this on. So Phase 4
is not "a field on that": it is a versioned schema migration, an API endpoint
to set it, a dashboard control, and the plan reading it. That is a real slice,
and it is the strongest reason the consent switch stays separate from the
provisioning slice rather than riding along with it.

What the preference does not control: the family calendar, which is a separate
grant with a separate purpose, and the feeds, which are read-only for
everyone. It governs the personal surface only.

### The "From Bloud" inbox, and why it is not provisioned

The obvious third state is an inbox: a separate `From Bloud` collection the
agent writes and the human reads, so agent-authored items sit in the calendar
stack without being *in* the calendar. It is a coherent design. It is not the
one to build.

What the inbox actually buys over `off` is narrow: agent delivery for someone
who does not want agent writes in their curated calendar. What it costs is a
second collection per person, permanently.

- **It is a queue with no workflow.** Nothing reads it, nothing marks it seen,
  nothing promotes an item into the real calendar. It is a pile that happens to
  sync to a phone.
- **It creates the support question it was meant to prevent.** "I asked Hermes
  to add something, where did it go?" only exists in the world where there are
  two places it could have gone.
- **The thing it is for is already there.** An agent-written item carries
  `CATEGORIES:bloud-agent` and an `ORGANIZER` of the agent's own. Inside one
  collection the person can still tell that they did not type it.
- **It is permanently cheap to add and expensive to remove.** Every collection
  is a display name, a share row, a sidebar entry, and a thing the agent
  disambiguates against forever.

The decisive property is that the consent model can go from two states to three
later without touching anyone who is already installed. The preference is data
and the collections come from the same create-and-share loop: a household that
later wants `inbox` gets the collection created on the next pass, existing
users keep `direct`, and nothing moves or renames. Shipping the inbox now means
every household has a "From Bloud" collection sitting empty that nobody asked
for and nobody understands.

So: **provision one collection, ship `direct` and `off`, and leave `inbox` as
a named deferred option rather than a provisioned default.** If the household
that wants it turns out to exist, the second collection is one more entry in
the same loop.

## Build order

Each phase is independently shippable and each one proves something the next one
depends on.

**Phase 1: the plan type, with no new calendars.** Introduce `Calendar`,
`Grant`, and `planCalendars`, and route the *existing* family and feed
calendars through it. Nothing new is provisioned.

What it proves: `renderShares(plan)` produces a `sharing.csv` that is
**byte-identical** to what the current constants produce, and
`ensureCalendars(plan)` creates exactly the collections the current
`ensure*` functions create. If a single byte moves, the refactor changed
behavior and is wrong. That assertion is the whole reason to do this phase
before adding anything: it proves the abstraction is faithful against a set
that is already known good.

**Phase 2: per-user calendars in the plan.** Add the per-user kind.
`planCalendars` emits one `people/<user>` calendar per active directory user,
with grants to that user and to the agent, and it checks the mount path before
granting it so an existing collection the user made is shadowed by nothing.

What it proves: a new user in the directory gets a calendar and two mounts on
the next pass without a reinstall; the shares file is stable across passes with
no churn and no restart; a user who is deactivated loses the mounts and the
collection survives; a user who already owns a collection at the mount path
gets a warning and no mount rather than a shadowed calendar. This is the
feature, and it is the phase that makes "add a todo to Bob" possible.

**Phase 3: resolution.** Display names, the URL convention, and the
`dav-mcp` namespace description that Hermes reads, so the agent can go from a
name to a collection without guessing.

What it proves: against a live stack, `list_calendars` as the agent returns one
entry per household member with a name and a URL that carry the same identity,
and a `create_todo` against one of them lands where it should. This is the
phase that turns "the collection exists" into "the agent can find it".

**Phase 4: the consent switch.** The per-user `direct` / `off` preference
gating the agent's grant in the plan.

This is the one phase that is not contained inside the calendar app. It needs a
versioned schema migration for the preference, an API endpoint to set it, a
dashboard control, and the plan reading it. The existing `user_preferences`
table carries only the dashboard `layout` JSON, so there is nothing to reuse.
That cost is the reason this phase is separable, and it is worth keeping that
way: the provisioning feature is complete and useful without it.

What it proves: with `off`, the agent's tree has no mount for that person and a
write is refused by the server rather than by a check.

**Phase 5: the purge.** `reality - plan` under `people/`, gated on a plan
marked complete.

This phase goes last on purpose, and it does not go in at all until phases 1
through 4 have run for a while and the plan is demonstrably stable. The
completeness check on the directory read is a hard prerequisite, not a
follow-up: the purge set is a set difference, so shipping it against a plan
that can silently shrink is shipping a way to delete a household's calendars
with a truncated API response.

## Decisions

1. **One collection per person**, carrying events and to-dos. The consequences
   that led here are in the section above; the case that would flip it is named
   there too.
2. **A calendar is a value, and one pure function produces the set.** Family is
   a singleton instance of it, personal calendars are a per-user instance of it,
   and the feed collections are a third. Creation and sharing both consume the
   same plan so they cannot disagree, and nothing downstream branches on the
   kind.
3. **The recipient chooses** whether the agent may write their calendar at all:
   `direct` or `off`. Enforced by which mount the agent receives, not by a
   check. The `inbox` middle state is deferred, not provisioned, for the
   reasons above. This is a subtraction from the shipped shape, so it can ship
   after the provisioning slice rather than with it.
4. **Personal collections are never shared with another user.** Assert this by
   test rather than trusting it: a rendered row that shares one person's
   personal collection with a second person is a bug with a victim.
5. **Purge on deletion, shipped separately from creation.** It is the same diff
   pointed the other way, and it is the only half of this design that can lose
   data.
6. **No migration.** The feature adds empty collections and share rows and
   touches nothing existing. The mount collision is detected rather than
   assumed, and the purge reads the directory roster rather than the plan so a
   deactivated account is never mistaken for a deleted one.

## What this does not solve

- **Per-user agent identity.** The agent still has one credential for the whole
  instance. See `client-credentials.md`.
- **Cross-person authorization.** "Alice may ask for things to be added to
  Bob's calendar" is not expressible until the requester has an identity.
- **Notifications.** A todo appearing in Bob's task list is the only signal.
  Nothing pings him.
- **Rollback.** Existing installs have no `people/` collections and get them on
  the next pass. Nothing to migrate, and nothing to roll back: once a
  collection exists it holds real data, so reverting the feature leaves the
  collections and their data behind rather than removing them.
- **Contacts as a directory.** Bloud can provision a household address book
  (option B) but does not today; Radicale serves contacts as collections, not
  as a directory other apps query. See the "not wired" list in
  `apps/radicale/INTEGRATION.md`.
