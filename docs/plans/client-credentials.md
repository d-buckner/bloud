# Third-party client credentials

> Status: draft. This plan needs one explicit decision before any of it is
> built: whether Bloud offers a *persistent* credential reveal, or only a
> one-time reveal at enable time. Section "The fork: one-time versus persistent
> reveal" lays out why the answer is different for those two options, and why
> the answer is currently "one-time, until the auth bypass is repaid".
>
> This answers open questions 3 and 4 of
> [`hermes-gateway.md`](hermes-gateway.md) at the platform level rather than
> per app. Facts below were verified against the tree and the pinned Hermes image
> on 2026-10-06.
>
> **Pilot:** [`client-credentials-hermes-webui-pilot.md`](client-credentials-hermes-webui-pilot.md)
> builds this pattern against `hermes-webui`: native OIDC for browser sign-in,
> plus a Bloud-minted `clientPassword` that the Hermex iOS app authenticates
> with. That is the **provisioned** case rather than the relayed one, which is
> the half of the pattern nothing in the catalog has exercised yet. The pilot
> has already revised two things below: `reveal` is three-state rather than
> binary, and `rotate` needs to name who can rotate, because a credential
> Bloud relays is not the same case as one Bloud mints.

## The problem: Bloud already mints credentials nobody can see

Apps in the catalog generate credentials today and store them through
`AppSecretsProvider`. There is no endpoint that returns them and no UI that
shows them. The user's own server holds secrets the user cannot obtain:

| App | Credential | Minted by | Where it is written |
|---|---|---|---|
| hermes | `apiKey` (agent bearer) | Hermes, relayed by Bloud | `apps/hermes/configurator.go:483` |
| affine-mcp | `httpToken` | the wrapper | `apps/affine-mcp/configurator.go:187` |
| dav-mcp | `httpToken` | the wrapper | `apps/dav-mcp/configurator.go:179` |
| authentik | `apiToken` | Bloud | `apps/authentik/server_configurator.go:152` |
| jellyfin | admin password | `GenerateAppAdminPassword` | `apps/jellyfin/configurator.go:94` |
| affine | internal password | `GenerateAppAdminPassword` | `apps/affine/configurator.go:301` |
| homeassistant | admin password | `GenerateAppAdminPassword` | `apps/homeassistant/configurator.go:559` |
| radicale | service password | Bloud | `apps/authentik/server_configurator.go:264` |

All of it lands in `secrets.json` via
`services/host-agent/internal/secrets/manager.go:344`. The app menu today
offers Rename and Uninstall and nothing else
(`services/host-agent/web/src/lib/components/AppContextMenu.svelte`).

So "get the password" is a gap that exists independently of any particular
client. It is worth building for that reason alone, and Hermex is the sharpest
case rather than the only one.

## Two features, not one

The proposal collapses into two features with different risk, and they should be
split before anything is designed.

**Reveal.** The credential already exists; the UI reads it and hands it over.
Hermes' `apiKey` is this shape: Hermes minted it, Bloud relayed it, and the
only new work is showing it. A pure read over state Bloud already holds.

**Provision.** Bloud must *create* a credential the app did not create for
itself, and creating it changes the app's auth posture. Hermex is this shape:
it needs the dashboard's `basic` provider turned on, which means a username, a
password, and a session-signing secret that must stay stable across restarts
(`hermes-gateway.md` Goal 2). That is a write that opens a door.

Keeping them apart matters because a reveal viewer should not inherit
provisioning's risk profile. Reveal is generated from the catalog. Provisioning
is a capability an app declares and that stays off until the operator turns it
on.

## The contract system already has the schema, minus one field

`provides: <contract>: secrets: [...]` already says a credential exists and
what it authenticates, and the loader already validates both sides against
`internal/catalog/contracts.go`. A reveal UI can be generated from that with
zero per-app UI code, which is the shape every other Bloud surface takes.

What it does not say is whether the credential is safe to show a human. The
field currently means "what a consumer app gets through the graph", and those
are different audiences:

- A consumer app is inside the perimeter, ordered by the dependency graph, and
  scoped by what it declared under `requires`.
- A human copying a token into a phone takes it outside the perimeter
  permanently. Bloud never sees where it goes.

So the missing declaration is on the published secret, not on the consumer:

```yaml
provides:
  agentApi:
    port: gateway
    secrets:
      - apiKey              # consumer apps get this through the graph
    clientAccess:           # humans may get this
      reveal: true
      rotate: true
      label: "Agent API bearer"
      # The plain-language capability statement the UI renders before the
      # reveal. Data, not a string the frontend invents, so the person who
      # knows what the token opens is the person who writes it.
      reaches: "an agent that can run commands on this server"
```

Three things this buys:

1. The UI is generated. An app that declares nothing shows no third-party
   access section, and that is correct rather than missing.
2. The capability statement lives next to the credential it describes, in the
   file the person adding the app is already editing.
3. The loader can reject a `clientAccess` block on a contract that publishes
   no secrets, the same way it rejects an `extraPorts` name that does not
   resolve. An exemption that guards nothing is itself a failure.

## The fork: one-time versus persistent reveal

This is the decision the plan exists to force. The two options look like
variations on one feature and are not the same security object.

**Persistent reveal.** A `GET /api/apps/{name}/credentials` that returns
plaintext to any admin session, so the value can be read again later.

**One-time reveal.** The value is shown once, at the moment external access is
enabled, with "copy this now; it will not be shown again". Losing it means
rotating it. Thereafter the API can report *that* a credential exists, its
label, and when it was created, but never the value.

The argument for one-time is the shipping blocker already in `AGENTS.md`: the
auth bypass is remotely forgeable, so a `True-Client-IP: 127.0.0.1` header
grants admin. A persistent reveal endpoint is a vault added to a house whose
front door is known-broken. It also puts a plaintext-secret-returning route on
the same authenticated surface as everything else, which is a permanently
attractive target rather than a one-time exposure.

The argument against one-time is convenience, and it is real: people lose
things. But rotation is the answer to a lost credential in every other system
of this shape, and rotation is already on the list.

**Recommendation: one-time reveal now.** Persistent reveal is a follow-up that
is gated on repaying the auth bypass, and that gate should be written into the
plan rather than left as a judgment call made later under deadline. The
`clientAccess` schema above can carry the distinction from the start with a
`reveal: once | always` value, so choosing `once` today does not require a
schema change later.

## Naming: "Connect to apps" reads backwards

In Bloud's vocabulary the arrows are already fixed and opposite:

- `integrations:` means *this app consumes*
- `provides:` means *this app serves*

The menu item sits on `provides:`, but "Connect to apps" reads like the
consuming arrow. Someone who has read the docs will map it wrong, and someone
who has not will map it wrong differently.

What the user is actually doing is standing on a phone with another app and
wanting it to reach this one. Names that do not invert the arrow:

| Candidate | Read |
|---|---|
| External access | Neutral and accurate; slightly institutional |
| Third-party clients | Precise, matches this doc |
| Connect a client | Verb-first, matches the user's intent, no arrow inversion |

Recommend **"Connect a client"** as the menu label and "third-party client
credentials" as the platform term, so the docs and the UI each use the word
that fits them.

## What to ship

The minimum good version, in the app detail modal rather than the context menu,
because the content is rich rather than a single action.

1. **A toggle: "Allow third-party clients."** Off by default. The toggle is
   what triggers provisioning, so no door exists before someone asks for it.
2. **A plain-language blast radius on the confirm.** The `reaches:` string from
   the contract, rendered verbatim: "This token reaches an agent that can run
   commands on this server." Not boilerplate, and not a generic warning people
   click through. The person who knows what the token opens writes it.
3. **The whole connection, not a fragment.** Hermex needs host plus username
   plus password. An OpenAI-format client needs a base URL plus a bearer. Hand
   over a ready-made snippet per client type rather than a bare secret the user
   has to reassemble, and render a QR code. Typing a 32-character secret on a
   phone is the actual UX failure this feature will be judged on.
4. **Rotate and revoke, one click, always visible.** If a credential can be
   revealed it must be killable, and the control must be visible before it is
   needed rather than discovered during an incident. Rotation has to satisfy the
   stability constraint from `hermes-gateway.md`: a session-signing secret
   regenerated per boot invalidates every client session on every recreate,
   which is the login loop by another route. Rotation is deliberate, never
   incidental.
5. **Per-user, not shared.** Bloud has per-user accounts. A single shared app
   password collapses every user into one principal in the app's own audit
   log, which is the worst property available on a box running a
   terminal-capable agent. This is the item that costs real design work, and
   it is the one that decides whether this is a product feature or a hole with
   a nice UI. If per-user credentials are not achievable for a given app, the
   `clientAccess` block should say so explicitly rather than let the UI imply
   something better than it is.

## What this unblocks

| Client | What it needs | Credential state |
|---|---|---|
| OpenWebUI or any OpenAI-format client | `Authorization: Bearer` at the routed `/v1` | **Already minted** by #245; needs only the reveal |
| Hermex | Dashboard `basic` provider: username, password, stable signing secret | **Needs provisioning** |
| Any Bloud app consuming the agent | `agentApi` contract binding | Shipped, no human in the loop |

The OpenWebUI row is worth naming because it is the cheap half. Hermes derives a
stable session id from the conversation fingerprint specifically so
OpenWebUI-style clients map onto one Hermes session
(`_derive_chat_session_id`, `gateway/platforms/api_server.py:1012`), so the
integration works today and only the credential handoff is missing.

The Hermex row is the expensive half and is unchanged by #245: Hermex posts a
username and password form and cannot present a bearer or drive an OIDC
redirect, so no amount of contract work substitutes for the `basic` provider.

## Non-goals

- **Not a secrets manager.** Bloud's differentiator is holding *integration*
  knowledge, not being a general password vault. Scope is "credentials Bloud
  already knows because a contract says so". The moment this accepts arbitrary
  user secrets, it inherits backup, encryption-at-rest, and sync problems that
  are a different product.
- **No new SSO strategy.** Invariant 6's set stays exactly `native-oidc`,
  `ldap`, `forward-auth`, `none`. A client credential is not a Bloud ingress
  strategy; it is a credential inside an app that Bloud already authenticates
  through Authentik. The plan changes no strategy enum.
- **No hardcoded fallbacks.** Invariant 8 holds: env var, then `secrets.json`,
  then error. Note that one literal fallback already survives in
  `sso.DeriveSecret` (`services/host-agent/internal/api/router.go:500`); this
  feature must not add a second.

## Open questions

1. **Persistent reveal: when, and gated on what exactly?** The recommendation
   is one-time now. The gate for `always` should be a named condition (auth
   bypass repaid, per `docs/operations/tech-debt.md`) rather than a vibe.
2. **Can per-user client credentials actually work for the `basic` provider
   shape?** Hermes' bundled basic provider is one account, not N. If it cannot
   be made per-user, does Bloud surface it at all, or only surface credentials
   that can carry a user identity?
3. **Does `install_id` survive a Bloud container recreate?** Carried over
   from `hermes-gateway.md` open question 5. Hermex pins connection identity
   to it and refuses on mismatch, and it lives in the `config.yaml` Bloud
   rewrites every pass. This blocks Hermex regardless of the credential UI.
4. **Where does the reveal live in the API?** One-time reveal means the value
   crosses the boundary exactly once, which argues for it being part of the
   enable response rather than a GET that can be replayed.
5. **Does rotation need to be per-consumer?** Corrected by the pilot's
   findings: rotation does **not** log anyone out, because sessions are signed
   with a persisted key rather than derived from the credential. Revocation is
   the operation that ends sessions, and without per-consumer tokens it is
   indiscriminate: it logs out every live session, not the lost device. So the
   question is really about revocation rather than rotation, and per-consumer
   tokens remain the strongest argument for doing item 5 of "What to ship"
   first rather than last.

## Effort

| Work item | Unblocks | Effort | Blocked by |
|---|---|---|---|
| `clientAccess` block in the contract schema, loader validation, docs | reveal for already-minted credentials | 1 to 2 days | nothing |
| One-time reveal in the enable flow, snippet and QR rendering | OpenWebUI-class clients | 1 to 2 days | the schema |
| Rotate and revoke controls | operational safety | 1 day | the schema |
| **Revoke on rotate** | making `rotate` mean what it says | 0.5 to 1 day | the revoke control |
| hermes-webui client password provisioning | Hermex | see the pilot | the schema |
| Per-user client credentials | honest attribution | 2 to 4 days | a design decision per app shape |
| Persistent reveal | re-reading a lost credential | 1 day | the auth-bypass repayment |

The first three rows are the version worth shipping: they turn eight orphaned
credentials into something a user can actually use, they add no new door, and
they do not put a plaintext-secret endpoint on a forgeable-auth API.

**Revoke on rotate is the deferred item that matters most.** In the pilot's v1,
rotating a credential leaves live sessions running, because the session layer
records no binding to the credential that minted them. That makes `rotate` a
weaker primitive than the word implies, and every app adopting `clientAccess`
inherits the weakness until the pattern itself is strengthened. The mechanics
and the deferral reasoning are in
[the pilot](client-credentials-hermes-webui-pilot.md), under "Follow-up:
rotation should revoke".
