> Status: exploring. Nothing here is built. The artifact is the analysis: what the
> catalog can already do, the one constraint that decides the ownership question,
> and the four decisions that are genuinely open.
>
> Facts verified against the tree at `d711f54` and against the pinned
> `ghcr.io/kozea/radicale:3.8.1` image on 2026-10-07.

# Per-user calendars

The ask: **provision a personal calendar for each user, so that in Hermes I can
ask to add a todo to a specific person.**

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
  /calendar-service/people/bob/        new
  /calendar-service/people/alice/      new

bob's tree (what bob's phone enumerates)
  /bob/Personal/        map -> /calendar-service/people/bob/      RWrw
  /bob/Family/          map -> /calendar-service/family/          RWrw   (exists today)
  /bob/Movies/          map -> /calendar-service/Movies/          Rr     (exists today)

caldav-service's tree (what the agent enumerates)
  /caldav-service/family/                                        RWrw   (exists today)
  /caldav-service/people/bob/     map -> /calendar-service/people/bob/   RWrw
  /caldav-service/people/alice/   map -> /calendar-service/people/alice/ RWrw
```

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

## Provisioning mechanics

Everything below is the existing pattern applied to a bigger set. No new
lifecycle phase, no new contract.

**Trigger.** The user list is already enumerated on every pass by
`calendarRecipients`. That list is the provisioning input: a user appears in
the directory, the next pass creates their calendar. A pass that cannot read
the directory already leaves the shares file untouched rather than rendering
an empty set, and the same rule has to cover creation: unknown is not empty, and
a blinking identity provider must not stop provisioning nor imply that people
were deleted.

**Ordering is load-bearing, not stylistic.** Radicale's sharing verifier treats
a share whose target does not exist as a hard failure:

```python
# radicale/sharing/__init__.py, database_verify()
item = next(iter(self._storage.discover(entry['PathMapped'])), None)
if not item:
    logger.error("sharing database verification content failed: %r", ...)
    return False
```

`radicale --verify-sharing` exits 1 on the whole database for one dangling
row. So the invariant is: **no share row for a collection that does not
exist.** The pass order that holds it:

1. `PreStart`: render the shares file for the set of collections *confirmed to
   exist*, not for the set of users that *should* have one.
2. `PostStart`: `ensurePersonalCalendars(recipients)`, PROPFIND then
   MKCALENDAR as `calendar-service`, next to `ensureFamilyCalendar`.
3. `PostStart`: if any collection was created, re-render the shares file with
   the now-confirmed set. One restart for a new person, not one per collection.

That is a change to what `syncShares` renders today (users) into what it
renders (confirmed collections), and it is the one place where this design is
not a pure extension of the family calendar. The feed path already learned this
lesson the hard way: the aggregation notes record that a share rendered before
its collection existed left "a calendar mounted that pointed at nothing".

**Idempotency.** `managedfile.Write` reports `changed=false` on identical
bytes, and the shares file is sorted by recipient then mount, so a steady-state
pass writes nothing and restarts nothing. Invariant 2 holds: creation is a
read-only diff once everyone has a calendar.

**Deactivation.** An inactive user drops out of `calendarRecipients`, their
share rows disappear, and their phone loses the calendars on the next pass.
Their data stays at `/calendar-service/people/bob/`. Retain it: it is a
person's data, and reactivation restores the mount. A purge is a separate,
explicit decision (see open questions).

**Rename.** There is no user-rename endpoint today (`settings_users.go` offers
create, delete, and role change), so keying by username is safe now. When
rename arrives, the key breaks: the collection is orphaned under the old name
and a fresh one appears under the new. The fix is a DAV `MOVE` keyed on the
Authentik PK, which is stable across renames. Out of scope for this slice, and
it should be recorded in the app's `INTEGRATION.md` under "What is not wired"
rather than discovered later.

**Deletion.** Deleting the user removes the shares. The collection should not
be silently deleted with it. Same open question as deactivation.

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
- **An operator switch is cheap and should exist.** "The agent may write
  personal calendars" as a setting, defaulting to off, means the household
  decides rather than inherits. The share rows are rendered from one function,
  so the switch is one condition in one place.

## The semantics question, restated

If the goal is "hand Bob a task", a personal VTODO is a good delivery
mechanism: Bob sees it in his task app, on his phone, with a due date.

If the goal is "Bob should know I asked him", a calendar entry is a weak
notification and a shared list is a better one.

If the goal is "the agent should not be silently editing my calendar", the
answer is not a permission system, it is a separate collection: a
`From Bloud` list per person that the agent owns and the human reads. Nothing
appears inside anyone's personal calendar without them having put it there.

These are not compatible answers to one question, and picking between them is a
product call rather than a technical one. The shape above supports all three:
it is the same collection-creation and share-rendering machinery, differing
only in which collections get created and which permissions the rows carry.

## Open decisions

1. **One collection per person, or a calendar plus a separate task list?** One
   is simpler and clients can filter. Two keeps to-dos out of the month view,
   which is a real complaint, and doubles the share rows and the naming
   surface. Recommendation: one, with the upgrade path open.
2. **Does the agent write into the person's personal calendar, or into a
   per-person `From Bloud` inbox?** Direct is what was asked for. The inbox is
   the answer to "I did not put that there". Recommendation: direct, gated by
   the operator switch, with attribution properties.
3. **Are personal calendars private by default?** The share set above gives the
   person and the agent, and never another user. That should be asserted by a
   test rather than trusted: a rendered row that shares one user's personal
   collection with a second user is a bug with a victim.
4. **What happens to a deleted or deactivated person's calendar?** Retained is
   the default proposed here. A purge policy is a separate decision with a
   retention period attached.

## What this does not solve

- **Per-user agent identity.** The agent still has one credential for the whole
  instance. See `client-credentials.md`.
- **Cross-person authorization.** "Alice may ask for things to be added to
  Bob's calendar" is not expressible until the requester has an identity.
- **Notifications.** A todo appearing in Bob's task list is the only signal.
  Nothing pings him.
- **Migration.** Existing installs have no `people/` collections; they get
  provisioned on the next pass. Nothing to migrate, but nothing to roll back
  either: created collections stay on disk after the shares are removed.
- **Contacts as a directory.** Bloud can provision a household address book
  (option B) but does not today; Radicale serves contacts as collections, not
  as a directory other apps query. See the "not wired" list in
  `apps/radicale/INTEGRATION.md`.
