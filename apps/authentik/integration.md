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

## Admin Account

`PostStart` runs `apps/authentik/scripts/set_admin_password.py` in the server
container. It creates the `admin` user (in the configured `BLOUD_ADMIN_EMAIL`,
added to `authentik Admins`) and sets its password from `BLOUD_ADMIN_PASSWORD`;
both values come from the host agent's `config.AuthentikAdminPassword` and
`config.AuthentikAdminEmail`, which are the `BLOUD_AUTHENTIK_ADMIN_PASSWORD`
env var when set and the generated `authentikBootstrapPassword` in `secrets.json`
otherwise.

- The password is set only when the user is created, and the script saves the
  object explicitly. Authentik's `User.set_password` only mutates the instance
  (it bumps `password_change_date` and emits the `password_changed` signal), so
  a missing save leaves an empty password column: `has_usable_password()` still
  reports `True`, and no password authenticates. The failure surfaces as
  "Invalid password" from our LDAP outpost's bind flow and as a re-prompt from
  the login flow, not as a configuration error.
- An operator who changes the password (Bloud's setup wizard sets it through
  Authentik's API, which saves) keeps it: every later reconciliation finds the
  user present and leaves its password alone.
- `admin` is the product's administrator, not Authentik's own bootstrap
  `akadmin`. Authentik 2025.10.x does not consume `AUTHENTIK_BOOTSTRAP_PASSWORD`,
  so that built-in user keeps Authentik's default credential; anything that
  authenticates as the administrator uses `admin` and the password above.

## Health Check
- **Endpoint:** `/-/health/live/`
- **Timeout:** 90 seconds (slow startup)
