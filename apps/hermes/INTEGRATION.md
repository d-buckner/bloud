# Hermes — Bloud integration notes

Upstream: [NousResearch/hermes-agent](https://github.com/NousResearch/hermes-agent)
(MIT). Bloud ships the official image `nousresearch/hermes-agent`, pinned to a
release tag. Hermes is a self-improving AI agent; the web dashboard is the
Bloud-facing surface.

## What Hermes is, in Bloud terms

Hermes is a single-container app whose image is an **s6-overlay stack**, not a
single process. The pieces that matter for the integration:

- The **web dashboard** (`hermes dashboard`) is the user-facing app. It runs as
  a supervised s6 service when `HERMES_DASHBOARD=1` and listens on
  `HERMES_DASHBOARD_PORT` (default 9119). Dashboard chat runs **in-process** —
  it reads `$HERMES_HOME` (config, memory, skills) directly and does not need a
  separate gateway server running.
- **Messaging gateways** (Telegram/Discord/etc.) are optional and registered
  dynamically by the user inside the app; Bloud neither configures nor needs
  them. They are out of scope for the install path.
- The container's **main program** (Docker CMD) is the interactive TUI when no
  args are given. Headless that TUI exits immediately. Bloud parks it with
  `command: ["sleep", "infinity"]` and lets the s6-supervised dashboard be the
  actual service. The orchestrator's `restartPolicy: always` keeps the whole
  stack up; s6 supervises the dashboard inside it.

## State / volume

`HERMES_HOME=/opt/data`, mounted from `{{appDataDir}}/data`. Hermes seeds its
own `config.yaml`, memory, skills, and SQLite session store there on first boot.
Bloud does **not** write Hermes config — the app owns `$HERMES_HOME` entirely.
That is why this configurator's `PreStart` returns `changed=false`: there is no
mounted file for it to manage.

## The credential contract (why `sso: strategy: none`)

Hermes enforces its **own** auth gate on any non-loopback bind (Bloud reaches it
through Traefik, which is non-loopback). The gate **fails closed**: a
non-loopback dashboard refuses to start unless an auth provider is registered.
Upstream offers exactly two zero-infrastructure providers:

1. A bundled **username/password** provider (`dashboard_auth/basic`), and
2. **Nous Portal OAuth** (`HERMES_DASHBOARD_OAUTH_CLIENT_ID`) — a vendor IDP.

There is no generic OIDC config, so Bloud's Authentik OIDC cannot back the
gate. That rules out `native-oidc`/`forward-auth` for this app. Bloud uses the
**password provider** with a per-deployment generated credential:

| Container env | Value |
|---|---|
| `HERMES_DASHBOARD_BASIC_AUTH_USERNAME` | `bloud` |
| `HERMES_DASHBOARD_BASIC_AUTH_PASSWORD` | `{{appAdminPassword}}` |

`{{appAdminPassword}}` is the Bloud per-app admin password, generated on first
use and persisted in `secrets.json` under `appSecrets.hermes.adminPassword`.
The same value is the one the configurator validates in `PreStart` (via
`GenerateAppAdminPassword("hermes")`) before the container is built, so the
credential that ships in the container and the one Bloud tracks are the same by
construction.

> **Note:** `HERMES_DASHBOARD_INSECURE` does **not** disable the gate (removed
> upstream in a June 2026 hardening pass — unauthenticated public dashboards
> were an attack vector). It is accepted and ignored. Bloud never sets it; the
> gate stays on and is satisfied by the password provider.

The password is never hardcoded. Retrieve it for a login:

```bash
bloud shell 'cat "$BLOUD_DATA_DIR/secrets.json"'   # appSecrets.hermes.adminPassword
```

The configurator logs the username and the secret's location (never the value)
at every `PreStart`, so a first-time operator discovers where the credential
lives without grepping secrets by hand.

## Health check

`GET /api/health` on `:9119`, loopback. Upstream declares this path a **public
liveness route** (exempt from the auth gate so external uptime probes work), so
the health check never trips the gate — it proves the dashboard process is up
without needing the credential. The configurator's `PostStart` waits on the
same path for the same reason.

## Pinning

The image is pinned to a `vYYYY.M.D` release tag (see `metadata.yaml`). Hermes
tags roughly weekly. The dashboard basic-auth env contract
(`HERMES_DASHBOARD_BASIC_AUTH_*`) has been stable across the tag window; re-read
`docker/s6-rc.d/dashboard/run` in the target tag before bumping.

## Testing

Unit tests in `apps/hermes/configurator_test.go` cover: the credential resolves
under the `hermes` catalog key; a missing/empty provider is rejected at
`PreStart`; `PostStart` waits on `/api/health` (and fails when it never comes
up). The `{{appAdminPassword}}` render + lazy-generation contract is covered
in the orchestrator (`TestSpecTemplateVars_*`).

Not covered here: a live end-to-end login through the dashboard (the password
provider's own round-trip is upstream's surface, not Bloud's). The Bloud
contract under test is: *the credential is generated once, is the value the
container runs, and the dashboard reaches healthy.*
