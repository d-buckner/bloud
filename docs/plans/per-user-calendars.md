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
  /calendar-service/people/bob/        new: Bob's personal calendar
  /calendar-service/people/alice/      new
  /calendar-service/inbox/bob/         new: "From Bloud", see Consent

bob's tree (what bob's phone enumerates)
  /bob/Personal/        map -> /calendar-service/people/bob/      RWrw
  /bob/From Bloud/      map -> /calendar-service/inbox/bob/      RWrw
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

## One collection or two: the product consequences

The question looks like a schema choice. It is a question about what a todo *is*
to the person receiving it, and the two answers are visible to them every day.

One collection means one CalDAV calendar holding `VEVENT` and `VTODO` side by
side. Two means a calendar plus a separate "Bob's tasks" collection per person.

| What the person notices | One collection | Calendar + Tasks |
|---|---|---|
| Their month view | A to-do with a due date renders on the day it is due, next to their appointments. Task noise in the schedule. | Tasks live in a collection they can uncheck. The grid stays clean. |
| Can they separate the two views? | Mostly no. Most clients filter by *calendar*, not by component within one calendar, so "hide my to-dos from my schedule" is not generally available. | Yes, and this is the only way to get it. |
| Their task app | The collection shows up as a task list because it contains `VTODO`. Same collection, read two ways in two apps. | Same, except the reading matches the name. |
| What the agent must disambiguate | Exactly one target per person. "Add it to Bob" cannot land wrong. | Two plausible targets. "Dentist at 3" is an event, "buy milk" is a task, and a wrong pick shows up in the wrong list. |
| Whether the name is honest | "Bob's calendar" is doing two jobs and lies about one of them. | "Bob's calendar" and "Bob's tasks" are each honest. |
| Filing mistakes later | Nothing to move. | The agent files a task, the person wanted a calendar entry, and that is a manual move. |
| Share surface | N collections | 2N, so every new person adds twice the rows and every change restarts Radicale. At household scale this is noise, not a limit. |

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

## Consent: the recipient decides

"Where does an agent-written todo land" is answered by the person whose calendar
it is, not by the operator and not by the agent. That is a better answer than a
global switch, and it is also the answer this architecture enforces most
cheaply.

Two collections per person make it expressible:

- `/calendar-service/people/<user>/`, the personal calendar
- `/calendar-service/inbox/<user>/`, "From Bloud", the agent's alternative
  write target

The recipient's preference selects which of the two the agent receives an RW
mount on:

| Preference | The agent's tree gets | The agent's tree never gets |
|---|---|---|
| `direct` | `/caldav-service/people/<user>/` mapped to the personal calendar | nothing beyond it |
| `inbox` | `/caldav-service/inbox/<user>/` mapped to From Bloud | any share on the personal calendar |
| `off` | nothing for this person | both |

The person always holds read-write on both of their own collections. The inbox
is theirs to read and to clear; it is simply not a collection the agent may
write into unless they said so.

**Why this is enforcement and not policy.** `owner_only` means the agent can
touch nothing that is not mounted in its own tree. A preference of `inbox`
means the personal calendar is not mounted for the agent, so a write to it is
refused by the server rather than by a check the agent might forget to run. The
consent is the absence of a mount. That is the strongest guarantee available at
this layer, and it costs one condition in `renderShares`.

Where the preference lives: Bloud already keeps per-user local preferences
alongside the directory record (`CreateManagedUserHandler` "ensures local
preferences"), so this is a field on that, with an instance-level default for
people who never set one. The Radicale configurator reads the set when it
renders, the same way it reads the user list.

What the preference does not control: the family calendar, which is a separate
grant with a separate purpose, and the feeds, which are read-only for
everyone. It governs the personal surface only.

## Decisions

1. **One collection per person**, carrying events and to-dos. The consequences
   that led here are in the section above; the case that would flip it is named
   there too.
2. **The recipient chooses** where agent writes land: `direct`, `inbox`, or
   `off`. Enforced by which mount the agent receives, not by a check.
3. **Personal collections are never shared with another user.** Assert this by
   test rather than trusting it: a rendered row that shares one person's
   personal collection with a second person is a bug with a victim.
4. **Purge on deletion.** See below.

## Purging a deleted person

Purging means the reconcile can delete, and that changes its safety profile
completely. A create that is wrong leaves a stray collection. A delete that is
wrong destroys a person's data.

**Where it runs: not in the user-delete handler.** That handler would need the
Radicale address and the `calendar-service` credential, which crosses the
settings boundary and can fail half way through leaving shares removed and data
behind. Instead the same pass that creates missing collections deletes orphan
ones: an orphan is a collection under `people/` or `inbox/` whose key is not
an active user. Symmetric, idempotent, self-healing, and the orchestrator stays
the single writer.

Every one of these conditions is required before any delete:

- **Only on a confirmed enumeration.** `calendarRecipients` already returns
  `(users, enumerated)` and the existing code refuses to render an empty set
  when enumeration fails. The purge gates on the same flag, and `false` must
  mean "delete nothing", never "delete everything".
- **Only under the owned prefixes.** Never `family`, never a feed collection.
  The prefix list is a constant, and a purge that can reach outside it is not a
  purge.
- **Only on a complete enumeration.** This is the one that is easy to get
  wrong. `pkg/authentik.ListUsers` requests `page_size=200` and does not
  paginate. On an instance with more than 200 users, everyone past the first
  page is invisible, and every invisible user's collections are orphans by the
  rule above. At household scale this never fires, and it is still the exact
  shape of the bug that turns a bad reconcile into data loss. The purge should
  compare the API's reported total against the length of the list it got back
  and refuse to delete when they disagree. A delete that is only safe because
  the deployment is small is not safe.
- **After the create step in the same pass.** A purge that runs before creates
  could delete a collection the same pass was about to adopt.

What a purge is concretely: a DAV `DELETE` on the collection as
`calendar-service`. The share rows need no cleanup because they were rendered
for the recipient who is now gone.

What is lost: everything in that person's personal calendar and inbox, including
any agent-written history. That is the decision, and it belongs in the delete
confirmation in the UI rather than in a doc nobody reads at the moment it
matters. If that confirmation is not acceptable, the alternative is a timed
purge, which needs a retention clock Bloud has no home for today.

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
