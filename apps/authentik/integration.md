# Authentik - Bloud Integration

## System App

Marked as `isSystem: true` - core infrastructure, not user-facing.

## Port & Network
- **HTTP:** 9001
- **HTTPS:** 9443
- **Network:** `apps-net`

## Data Storage
- **PostgreSQL:** `~/.local/share/bloud/authentik-postgres/`
- **Media:** `~/.local/share/bloud/authentik-media/`
- **Templates:** `~/.local/share/bloud/authentik-templates/`
- **Certificates:** `~/.local/share/bloud/authentik-certs/`
- **Blueprints:** `~/.local/share/bloud/authentik-blueprints/`

## Container Architecture

| Container | Purpose |
|-----------|---------|
| `apps-postgres` | Shared PostgreSQL |
| `apps-redis` | Session cache/task queue |
| `apps-authentik-server` | Web server |
| `apps-authentik-worker` | Background tasks |

Dependencies:
```
apps-postgres ───┐
                 ├─► apps-authentik-server
apps-redis ──────┤
                 └─► apps-authentik-worker
```

## Special Requirements

- **userns:** `keep-id` for proper bind mount permissions
- **Health waits:** PostgreSQL (`pg_isready`), Redis (`redis-cli ping`)
- **Secret key:** Minimum 50 characters

## Blueprint System

Auto-generates OAuth2/OIDC configs for apps. Example:
`~/.local/share/bloud/authentik-blueprints/actual-budget.yaml`

## Login Session Duration

A Bloud login lasts **90 days**. `apps/authentik/auth.yaml` declares
`session_duration: days=90` on the `default-authentication-login` stage, the
stage that calls `request.session.set_expiry()` and therefore decides how long
the session behind every SSO round trip lives. Upstream authentik ships that
stage with `seconds=0`, which ends the session when the browser closes.

The blueprint is the mechanism rather than a `PostStart` PATCH because
authentik's own worker reconciles it: the file watcher applies the blueprint
when the file hash changes and the hourly discovery schedule catches anything
missed. Verified against a live instance: a `session_duration` patched away from
the blueprint's value through the API is put back by the next apply, so the
declaration in the repo is what wins.

What this does not change:

- **Access and refresh tokens.** Those are per-app and stay short (`minutes=5`
  access, `days=30` refresh). An app whose own token expired redirects to
  authentik, which mints a new one silently while the session above is valid, so
  the user does not see a login page.
- **Remember me.** `remember_me_offset` stays zero, so the toggle is not shown.
  The 90 days is granted on every login rather than offered as a choice, which
  keeps a single lifetime in play instead of two.
- **Existing sessions.** A session issued before the change keeps its old
  expiry; the new duration applies to logins from that point on.

## SSO Integration for Other Apps

1. Add to app's `metadata.yaml`:
   ```yaml
   sso:
     strategy: native-oidc
     callbackPath: /oauth2/callback
   ```

## API Token for App Integrations

The API token this app generates for the host agent is also published to its
consumers, and the consumers that read it declare that they do, so no app has to
open another app's files to get it:

- `metadata.yaml` declares the token as the `apiToken` secret of its `sso`
  offer (`provides: {sso: {secrets: [apiToken]}}`), and `PostStart` in
  `server_configurator.go` publishes the value through
  `AppSecretsProvider.SetAppSecret("authentik", "apiToken", …)`. The
  orchestrator resolves that declaration into each consumer's
  `configurator.SSOBinding`, in `AppState.Integrations.SSO`, where the token is
  `binding.APIToken`.
- Only a consumer that declares `requires: [apiToken]` under `integrations.sso`
  is handed the token: that declaration is what makes the resolver put the token
  in the binding, so an app that declares `sso` merely to be reached (the ones
  behind forward-auth, for instance) receives the address and no credential. The
  name has to be one the contract carries, and a `requires` entry the contract
  does not name fails the catalog load.
- What a contract requires is defined once, in `internal/catalog/contracts.go`:
  the `sso` contract is where the `apiToken` secret is named, and a provider
  whose declaration does not match fails the catalog load, so an offer that
  would reach a consumer half-empty never loads.
- `apps/navidrome` is the consumer today: it declares
  `requires: [apiToken]` and reads `binding.APIToken` from its `sso` binding to
  authenticate its Authentik user sync, so it never reads
  `<dataDir>/authentik/api-token`. An empty `binding.APIToken` has two causes:
  the token has not been published yet (the sync is skipped and the next
  reconciliation retries), or the app did not declare the requirement, which is
  a metadata mistake rather than a wait.
- The `api-token` file (written by the same `PostStart` step) is kept for the
  host's own tooling only: `config.getAuthentikToken` reads it (the host agent
  wires that into its API server's token refresh) and the e2e helpers read it
  too. It is not the interface apps consume.

## Identity bootstrap

Bloud needs two things inside Authentik before any of the above is reachable: the
`admin` account, and an API token to call the API with. Both are created by
`ensureAPIToken` and `EnsureAdminUser`, and which mechanism does it depends on
whether the API will already accept the token:

- **The API, normally.** `EnsureAPIToken` reads
  `/api/v3/core/tokens/?identifier=bloud-api-token`, reads the key back with
  `view_key`, and writes only on a mismatch. `EnsureAdminUser` reads
  `/api/v3/core/users/?username=admin` and writes only what is missing. A
  steady-state pass is four GETs and no writes.
- **The Django shell, at bootstrap.** When Authentik answers 401/403 the token
  does not work, and nothing about the API can be called before it does, so
  `scripts/set_admin_password.py` and `scripts/ensure_api_token.py` run through
  `Deps.Exec`. That is once per install, and once more if the token is deleted or
  rotated by hand in the Authentik admin: the scripts re-impose Bloud's own
  value, so the repair is the same code as the bootstrap.

Two details the API forces that the shell did not need:

- **`set_key` after create.** `TokenSerializer` only exposes `key` in the
  blueprint context, so a token created over the API always gets a random key.
  `POST /core/tokens/{id}/set_key/` is the API's only way to impose the value
  host-agent authenticates with, and it is a separate call.
- **Waiting for `authentik Admins`.** The group is created by a blueprint that
  runs after the health endpoint reports ready. `waitForAdminsGroup` polls for
  the same window the shell script polled for, so a cold install cannot park the
  SSO stack in ERROR over a group that turns up seconds later.

The admin password keeps the rule the shell script documented: it is set only
when the account is created, so an operator who changed it in Bloud's setup
wizard keeps it. What changed is where that rule lives, not what it protects.

## Resync cost

`PostStart` re-runs on every convergence pass, on an idle box roughly every
minute. That makes its cost a property of the whole instance rather than of one
app, and this configurator was the worst offender: measured on an idle native
instance, `apps-authentik-server`'s `PostStart` ran on 97% of all passes at a
15.6s average, which was about a fifth of the reconciler's wall clock spent on a
no-op (issue #306). The pieces were two `ak shell` spawns (~3.3s each), a 3 second
sleep inside `EnsureLoginConfiguration` that only makes sense when something was
just patched, unconditional `PATCH`es of the brand CSS and the flow settings, and
about twenty API round trips, several of them the slow `?search=` full-text
filter.

What the fix is, and what it deliberately is not: the no-op path was made cheap,
not skipped. Skipping the diff is what lets drift in the provider go unseen, and
the diff is the entire reason the resync exists. So every step reads first and
writes on a mismatch, and the two steps that cannot read first (see below) are
now watched instead of invisible.

The read-first conversions, in order of what they were worth:

- the two `ak shell` spawns, which are gone from this path entirely;
- the 3 second sleep, which now only runs when a flow was actually patched;
- the brand CSS `PATCH` and the flow settings `PATCH`, both skipped when the
  value already matches;
- the group membership `add_user`, which now comes free: `lookupGroup` reads the
  group and its members in one request, so a service account already in
  `authentik Admins` costs no write, and the provider's `search_group` reuses the
  same read instead of resolving the group a second time;
- the embedded outpost's detail `GET`, which the list response already answers:
  the list serializer carries `config`, so one request serves both the read and
  the write;
- the `?search=` lookups, replaced by Authentik's exact `?username=` / `?name=`
  filters, which are both more precise and cheaper.

Still unconditional, because Authentik offers no read for them:

- **The three service-account passwords** (`ldap-service`, `caldav-service`,
  `calendar-service`). There is no endpoint that answers "does this account
  already hold that password", so keeping the directory in step with the secret
  store is a write. Each costs a server-side hash per pass.
- **The LDAP outpost token read** (`GetLDAPOutpostToken`), which is a read of a
  value the LDAP container's spec needs every pass anyway.

The engine now measures this rather than trusting it: `resync_cost.go` times each
node's resync and raises a signal after consecutive passes over
`DefaultResyncCostBudget`, surfaced on the developer status as
`resyncCostSignals`. A slow no-op never restarts a container, so the restart
watchdog could not see it.

## Admin account

The `admin` user is created in the configured `BLOUD_ADMIN_EMAIL`, added to
`authentik Admins`, with its password from `BLOUD_ADMIN_PASSWORD`; both values
come from the host agent's `config.AuthentikAdminPassword` and
`config.AuthentikAdminEmail`, which are the `BLOUD_AUTHENTIK_ADMIN_PASSWORD` env
var when set and the generated `authentikBootstrapPassword` in `secrets.json`
otherwise. See **Identity bootstrap** above for which mechanism creates it.

- When the shell path does create the user, `User.set_password` only mutates the
  instance (it bumps `password_change_date` and emits the `password_changed`
  signal), so a missing save leaves an empty password column:
  `has_usable_password()` still reports `True`, and no password authenticates.
  The failure surfaces as "Invalid password" from our LDAP outpost's bind flow
  and as a re-prompt from the login flow, not as a configuration error. Over the
  API the same care is structural: the create cannot carry a password, so it is a
  `set_password` call.
- An operator who changes the password (Bloud's setup wizard sets it through
  Authentik's API, which saves) keeps it: every later reconciliation finds the
  user present, sees `is_superuser` already true, and leaves its password alone.
- `admin` is the product's administrator, not Authentik's own bootstrap
  `akadmin`. Authentik 2025.10.x does not consume `AUTHENTIK_BOOTSTRAP_PASSWORD`,
  so that built-in user keeps Authentik's default credential; anything that
  authenticates as the administrator uses `admin` and the password above.

## Health Check
- **Endpoint:** `/-/health/live/`
- **Timeout:** 90 seconds (slow startup)
