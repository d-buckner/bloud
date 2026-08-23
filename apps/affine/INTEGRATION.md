# AFFiNE Integration

## Status: Complete (e2e-verified)

AFFiNE (https://github.com/toeverything/AFFiNE) is an AI-native knowledge base
that unifies docs, databases, and whiteboards. Bloud installs it as three
containers, wires its built-in OIDC client to the Bloud identity provider
(Authentik), and bootstraps the first-run owner account — so users reach a
sign-in button, not a setup wizard.

- Image: `ghcr.io/toeverything/affine:0.27.4` (pinned)
- SSO strategy: `native-oidc` (callback `/oauth/callback`)
- Public URL: `http://affine.localhost:8080` (app subdomain on the Bloud base
  domain; `affine.<host>:3010` direct for debugging)

## Architecture

### Container graph

```
apps-affine-postgres (pgvector/pg16)  ─┐
apps-affine-redis    (redis 7)        ─┼─> apps-affine (server, :3010)
                                       └── dependsOn both
```

Apps own their infrastructure (repo invariant): AFFiNE declares its own
Postgres (pgvector) and Redis rather than sharing a Bloud-wide database. The
server joins both the internal `affine-internal` network and `apps-net` so
Traefik can route to it.

### Migrations run inside the server container

AFFiNE's official self-host deployment runs a **one-shot migration job**
(`prisma migrate deploy` + data migrations + `private.key` generation) as a
separate container before the server starts. Bloud's orchestrator manages
long-lived containers and has no one-shot job primitive, so the migration step
is chained into the server's startup command:

```sh
node ./scripts/self-host-predeploy.js && exec node ./dist/main.js
```

The script is idempotent (safe to run on every reconciliation), and chaining
preserves the ordering guarantee (migrations complete before the HTTP
listener opens) within a single graph node. First boot is slow — the
healthcheck (node `fetch` against `/info`; the slim image has no curl) allows
a long startup window (`retries: 90` × 5s).

### Config file (`config.json`)

PreStart writes `<dataDir>/affine/config/config.json`, mounted at
`/root/.affine/config`. Only non-default keys are set; AFFiNE merges the file
over its built-in defaults. The content is deterministic (sorted JSON) so an
unchanged config never churns the file across reconciliation cycles.

```json
{
  "server": { "externalUrl": "http://affine.localhost:8080" },
  "oauth": {
    "providers": {
      "oidc": {
        "clientId": "affine-client",
        "clientSecret": "<derived>",
        "issuer": "http://sso.localhost:8080/application/o/affine/",
        "allowPrivateNetwork": true
      }
    }
  }
}
```

- `externalUrl` must match the OIDC redirect URI base (app subdomain +
  callback path) or the browser round-trip fails.
- `allowPrivateNetwork: true` lets the server reach the issuer by the
  in-container name `sso.localhost` (mapped to the host gateway via
  `extraHosts`) — the same name browsers use, so no second URL is needed.
- Client ID/secret are derived deterministically by the host-agent (the app
  and the IdP agree without a shared store).

### OIDC login flow

1. User opens `http://affine.localhost:8080/` — AFFiNE renders the workspace
   read-only with a **Sign in and enable** button (local accounts are not
   part of the Bloud story).
2. Clicking it opens the sign-in modal; **Continue with OIDC** starts the
   authorization-code + PKCE flow at the issuer
   (`http://sso.localhost:8080/application/o/affine/`).
3. The browser authenticates at Authentik (the Bloud identity).
4. AFFiNE's `/oauth/callback` exchanges the code, then **creates the app
   account on first login** (matched by the `email` claim). No per-user
   provisioning in Bloud: Authentik users appear in AFFiNE on first sign-in.
5. PostStart verifies the wiring behaviorally: `POST /api/oauth/preflight`
   must return an authorization URL (proves config.json loaded, issuer
   discovery succeeded, and PKCE is ready).

### Verified-email scope mapping (important)

Authentik's managed `scope-email` property mapping hardcodes
`email_verified: false`, and AFFiNE's OIDC provider **rejects logins with
`email_verified: false` or a missing/invalid email claim**. Bloud therefore
creates a custom scope mapping —
`Bloud OIDC: OpenID 'email' (verified)`
(`pkg/authentik.Client.ensureBloudEmailScopeMapping`) — that returns
`email_verified: true`, and every native-oidc provider is reconciled to use
it (`ensureProviderEmailScopeMapping`, idempotent: no PATCH without drift).

This is accurate rather than a hack: Bloud *is* the identity provider and
user identities are operator-managed, so from the OP's point of view the
email is verified.

### Identity emails must be TLD-bearing

AFFiNE validates the `email` claim with an RFC-style email regex that rejects
single-label domains. Bloud accordingly gives every managed user a valid
identity email:

- New users (`CreateUser`): `username@<baseDomain>` — with the dev default
  base domain `localhost` mapped to `localhost.local`
  (`authentik.UserEmailDomain`).
- Adopted users (setup wizard adopts the bootstrap admin): email is repaired
  on adoption; the legacy `admin@localhost` is self-healed to
  `admin@localhost.local` by the Authentik bootstrap script on every start
  (operator-set emails are untouched).
- Default bootstrap admin email (`BLOUD_AUTHENTIK_ADMIN_EMAIL` unset) is
  derived the same way.

## First-run owner account

Until a first user exists, every AFFiNE request redirects to `/admin/setup`,
which would block all end users. PostStart therefore calls
`POST /api/setup/create-admin-user` to create an internal owner account
(`Bloud Admin` / `bloud-admin@affine.localhost`, password generated from the
secrets provider and never exposed). The endpoint accepts the call only
before any user exists and answers `403 First user already created`
otherwise — that response is the idempotency signal for later reconciliation
passes. End users never use this account; they authenticate via SSO.

## Files

| File | Purpose |
|------|---------|
| `apps/affine/metadata.yaml` | Containers (postgres/redis/server), native-oidc SSO, port 3010 |
| `apps/affine/configurator.go` | config.json writer, owner bootstrap, OIDC preflight verification |
| `apps/affine/configurator_test.go` | Unit tests for config rendering + bootstrap idempotency |
| `services/host-agent/internal/appconfig/register.go` | Configurator registration |
| `services/host-agent/pkg/authentik/client.go` | Verified-email scope mapping + provider reconciliation |
| `services/host-agent/internal/e2e/e2e_test.go` | Go integration tests (install/configure/uninstall) |
| `e2e/tests/affine.spec.ts` | Playwright user journey (home tile → OIDC login → workspace) |

## Key API endpoints (server, :3010)

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/info` | GET | Public health/version (used by the container healthcheck) |
| `/api/setup/create-admin-user` | POST | First-run owner bootstrap (403 once a user exists) |
| `/api/oauth/preflight` | POST | Returns the authorization URL; SSO wiring check |
| `/oauth/callback` | GET | OIDC redirect target (PKCE code exchange) |

## Verification

```bash
# User journey (requires a running dev runtime + installed user):
cd e2e && npx playwright test affine

# Full Go integration path (fresh VM, real install/reconcile/uninstall):
./bloud validate --tier integration
```

Behavioral assertions (not config values): the Playwright spec lands in the
user's workspace after a real Authentik login; the Go integration test checks
`/info`, the owner bootstrap (403 on second call), and the OIDC preflight
redirect URI, then asserts full uninstall cleanup (three containers, data
dir, routes).

## Troubleshooting

| Symptom | Cause / fix |
|---------|-------------|
| `Missing valid email claim in OIDC response` | The provider lost the verified-email scope mapping (or the user has no email). Reconcile re-applies it on the next PostStart; check the provider's `property_mappings` for the `Bloud OIDC: OpenID 'email' (verified)` mapping. |
| `Email for this account is not verified` | Same mapping missing — `email_verified` came back false. |
| `Invalid OAuth response` with an email complaint | The user's identity email lacks a TLD (e.g. legacy `admin@localhost`). Upgrade path self-heals it; set `BLOUD_AUTHENTIK_ADMIN_EMAIL` explicitly otherwise. |
| Stuck at `/admin/setup` | Owner bootstrap did not run (PostStart failed earlier) — check host-agent logs for `bootstrapping owner account`. |
| Slow first boot | Expected: image pull + prisma migrations run before the listener opens. The healthcheck window covers ~7.5 minutes. |
