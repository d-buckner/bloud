# Hermes: Bloud integration notes

Upstream: [NousResearch/hermes-agent](https://github.com/NousResearch/hermes-agent)
(MIT). Bloud ships the official image `nousresearch/hermes-agent`, pinned to a
release tag. Hermes is a self-improving AI agent; the web dashboard is the
Bloud-facing surface.

## What Hermes is, in Bloud terms

Hermes is a single-container app whose image is an **s6-overlay stack**, not a
single process. The pieces that matter for the integration:

- The **web dashboard** (`hermes dashboard`) is the user-facing app. It runs as
  a supervised s6 service when `HERMES_DASHBOARD=1` and listens on
  `HERMES_DASHBOARD_PORT` (default 9119). Dashboard chat runs **in-process**:
  it reads `$HERMES_HOME` (config, memory, skills) directly and does not need a
  separate gateway server running.
- **Messaging gateways** (Telegram/Discord/etc.) are optional and registered
  dynamically by the user inside the app; Bloud neither configures nor needs
  them. They are out of scope for the install path.
- The container's **main program** (Docker CMD) is the interactive TUI when no
  arguments are given. Headless that TUI exits immediately. Bloud parks it with
  `command: ["sleep", "infinity"]` and lets the s6-supervised dashboard be the
  actual service. The orchestrator's `restartPolicy: always` keeps the whole
  stack up; s6 supervises the dashboard inside it.

## State / volume

`HERMES_HOME=/opt/data`, mounted from `{{appDataDir}}/data`. Hermes seeds its
own `config.yaml`, memory, skills, and SQLite session store there on first boot.
Bloud writes **only** the SSO keys into that `config.yaml` (see below); every
other key belongs to the user and is preserved untouched.

## The SSO contract (`sso: strategy: native-oidc`, `clientType: public`)

Hermes enforces its **own** auth gate on any non-loopback bind (Bloud reaches it
through Traefik, which is non-loopback). The gate **fails closed**: a
non-loopback dashboard refuses to start unless an auth provider is registered.
The upstream [self-hosted OIDC
provider](https://github.com/NousResearch/hermes-agent/blob/main/website/docs/user-guide/features/web-dashboard.md#self-hosted-oidc-provider)
authenticates the dashboard against **any OIDC-compliant issuer** via standard
authorization-code + PKCE, which is exactly what Bloud provisions in
Authentik. So Hermes joins the identity provider as a `native-oidc` app.

The provider is a **public PKCE client** (`sso.clientType: public`). This is a
Hermes constraint, not a Bloud choice: the self-hosted plugin supports **only a
public client** and rejects a `client_secret` ("Confidential clients … are not
supported yet; configure a public + PKCE client"). Bloud's OIDC provisioning
defaults to a confidential client for every app; the `clientType: public` field
makes the blueprint generator register a public, secret-less Authentik provider
for this app only. See `docs/architecture` / `internal/sso` for the field's
plumbing.

Provider config (issuer + client id) is per-install, and the container spec can
only render static `{{dataDir}}`/`{{appDataDir}}`/`TemplateVars`: it cannot
carry per-install OIDC values. Bloud therefore delivers them through Hermes' own
`config.yaml`, which the configurator merges in `PreStart`:

| Hermes `config.yaml` key | Value |
|---|---|
| `dashboard.oauth.provider` | `self-hosted` |
| `dashboard.oauth.self_hosted.issuer` | Bloud OIDC issuer (discovery) URL |
| `dashboard.oauth.self_hosted.client_id` | `hermes-client` |
| `dashboard.oauth.self_hosted.scopes` | `openid profile email` |
| `dashboard.public_url` | the dashboard's public URL (`http://hermes.<host>…`) |

`public_url` is what makes the callback URL deterministic: the dashboard derives
its OIDC callback as `<public_url>/auth/callback`, which must match the
`callbackPath: /auth/callback` the host-agent registers with Authentik (for
every configured host/IP). The OIDC issuer hostname resolves inside the
container via the framework's `sso.localhost:host-gateway` extra-host mapping
(`applyIssuerExtraHost`), so discovery/token exchange work by the same name the
browser uses.

The merge is **whole-document and semantically compared**: Hermes' unrelated
settings survive, and when the SSO keys already match on disk the configurator
reports `changed=false` so reconciliation never churns the file or needlessly
recreates the container. When SSO is turned off (Authentik removed), the
managed keys are **stripped** so a stale provider can't gate the dashboard on a
dead issuer.

> **Note:** `HERMES_DASHBOARD_INSECURE` does **not** disable the gate (removed
> upstream in a June 2026 hardening pass: unauthenticated public dashboards
> were an attack vector). It is accepted and ignored. Bloud never sets it; the
> gate stays on and is satisfied by the self-hosted OIDC provider.

## Health check

`GET /api/health` on `:9119`, loopback. Upstream declares this path a **public
liveness route** (exempt from the auth gate so external uptime probes work), so
the health check never trips the gate: it proves the dashboard process is up
without needing a session.

## Pinning

The image is pinned to a `vYYYY.M.D` release tag (see `metadata.yaml`). Hermes
tags roughly weekly. The self-hosted OIDC env/config contract
(`dashboard.oauth.self_hosted.*`, `/auth/callback`, and the `auth_providers`
field of `/api/status`) has been stable across the tag window; re-read
`website/docs/user-guide/features/web-dashboard.md` (`#self-hosted-oidc-provider`)
and the `plugins/dashboard_auth/self_hosted` plugin in the target tag before
bumping.

## Testing

Unit tests in `apps/hermes/configurator_test.go` cover the config-merge
contract: SSO on writes the self-hosted provider keys + `public_url`; a second
pass over an unchanged file reports `changed=false` (no churn); the user's own
keys survive the merge; SSO off strips the managed keys and creates nothing
when there is nothing to strip; a corrupt existing file errors rather than being
clobbered. PostStart is covered against a fake dashboard: it passes when
`/api/status` reports the gate on with the `self-hosted` provider, fails when
the provider is absent (e.g. only `basic`), and skips the provider check when
SSO is off.

Not covered here: the live OIDC round-trip through the dashboard (the
self-hosted plugin's own PKCE exchange is upstream's surface, not Bloud's). The
Bloud contract under test is: *Bloud's issuer/client/callback are the values the
dashboard runs with, they land without disturbing the user's config, and they
register cleanly as a public PKCE client*: the last confirmed by the
blueprint render test in `internal/sso` (`client_type: public`, no
`client_secret`).
