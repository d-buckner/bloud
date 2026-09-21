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

Hermes enforces its **own** auth gate on any non-loopback bind, and the gate
**fails closed**: a non-loopback dashboard refuses to start unless an auth
provider is registered. The upstream [self-hosted OIDC
provider](https://github.com/NousResearch/hermes-agent/blob/main/website/docs/user-guide/features/web-dashboard.md#self-hosted-oidc-provider)
authenticates the dashboard against any OIDC-compliant issuer via standard
authorization-code + PKCE, which is exactly what Bloud provisions in Authentik.
So Hermes joins the identity provider as a `native-oidc` app.

The provider is a **public PKCE client** (`sso.clientType: public`). This is a
Hermes constraint, not a Bloud choice: the self-hosted plugin supports only a
public client and rejects a `client_secret`. Bloud's OIDC provisioning defaults
to a confidential client for every app; the `clientType: public` field makes the
blueprint generator register a public, secret-less Authentik provider for this
app only.

### Why the issuer is the host loopback (`sso.loopbackIssuer`)

The self-hosted provider accepts an `https` issuer anywhere, but a plain `http`
issuer **only on a literal loopback hostname** (`localhost`, `127.0.0.1`,
`::1`). It validates the issuer at provider construction and every discovered
endpoint URL the same way, so Bloud's shared issuer host (`sso.localhost`) is
rejected: the provider never registers, and the dashboard then fails closed at
startup with `Refusing to bind dashboard to 0.0.0.0 ... but no auth providers
are registered`. That is exactly the failure the first CI run of this app's e2e
leg hit.

Bloud therefore hands Hermes the **loopback issuer**, set by the app's
`sso.loopbackIssuer: true` (see `internal/hostset.LoopbackIssuerBaseURL` and
`internal/engine/orchestrator.oidcInputsForApp`):

| Hermes `config.yaml` key | Value |
|---|---|
| `dashboard.oauth.provider` | `self-hosted` |
| `dashboard.oauth.self_hosted.issuer` | `http://localhost:8080/application/o/hermes/` |
| `dashboard.oauth.self_hosted.client_id` | `hermes-client` |
| `dashboard.oauth.self_hosted.scopes` | `openid profile email` |
| `dashboard.public_url` | the dashboard's public URL (`http://hermes.<host>:8080`) |

Reaching `localhost:8080` **inside** the container is what makes this work, and
that is why the container runs with `network: host`: only in the host network
namespace is `localhost` the machine running Traefik. In exchange:

- The dashboard binds the host loopback (`HERMES_DASHBOARD_HOST=127.0.0.1`), so
  it is reachable only through Traefik. It is never exposed on a network
  interface, and host networking cannot publish ports, so the app declares none.
- Traefik routes to `http://localhost:9119` (the generated app route uses the
  app port), which that loopback bind satisfies.
- The browser is sent to the issuer on `http://localhost:8080`. That is the same
  reach the `*.localhost` issuer host has: both resolve to the local machine, so
  Hermes SSO works from a browser on the Bloud machine.

`public_url` does double duty. It makes the OIDC callback deterministic
(`<public_url>/auth/callback`, matching the `callbackPath: /auth/callback` the
host-agent registers with Authentik), and it is the **exact** non-loopback
`Host` header the dashboard's DNS-rebinding guard accepts. Setting a
non-loopback `public_url` also engages the auth gate even on a loopback bind,
which is what we want: the gate is satisfied by the self-hosted provider, so
every proxied request is authenticated.

The merge is **whole-document and semantically compared**: Hermes' unrelated
settings survive, and when the SSO keys already match on disk the configurator
reports `changed=false` so reconciliation never churns the file or needlessly
recreates the container. When SSO is turned off (Authentik removed), the managed
keys are **stripped** so a stale provider can't gate the dashboard on a dead
issuer.

> **Note:** `HERMES_DASHBOARD_INSECURE` does **not** disable the gate (removed
> upstream in a June 2026 hardening pass: unauthenticated public dashboards were
> an attack vector). It is accepted and ignored. Bloud never sets it; the gate
> stays on and is satisfied by the self-hosted OIDC provider.

## Health check

`GET /api/health` on `:9119`, loopback. Upstream declares this path a **public
liveness route** (exempt from the auth gate so external uptime probes work), so
the health check never trips the gate: it proves the dashboard process is up
without needing a session.

## Pinning

The image is pinned to a `vYYYY.M.D` release tag (see `metadata.yaml`). Hermes
tags roughly weekly. Before bumping, re-read
`website/docs/user-guide/features/web-dashboard.md`
(`#self-hosted-oidc-provider`) and the `plugins/dashboard_auth/self_hosted`
plugin in the target tag, and confirm two things that this integration depends
on:

1. The issuer rule is still "https, or http only on a literal loopback
   hostname". A change here decides whether `sso.loopbackIssuer` is still
   needed.
2. The `dashboard.oauth.self_hosted.*` config keys, the `/auth/callback` path,
   and the `auth_providers` field of `/api/status` are unchanged.

## Testing

Unit tests in `apps/hermes/configurator_test.go` cover the config-merge
contract: SSO on writes the self-hosted provider keys (issuer, client_id,
scopes) plus `public_url`; a second pass over an unchanged file reports
`changed=false` (no churn); the user's own keys survive the merge; SSO off
strips the managed keys and creates nothing when there is nothing to strip; a
corrupt existing file errors rather than being clobbered. PostStart is covered
against a fake dashboard: it passes when `/api/status` reports the gate on with
the `self-hosted` provider, fails when the provider is absent (e.g. only
`basic`), and skips the provider check when SSO is off.

The issuer choice itself is covered in the orchestrator:
`oidc_loopback_issuer_test.go` asserts a `sso.loopbackIssuer` app is handed
`http://localhost:8080/application/o/<app>/` while a normal native-oidc app
keeps `sso.localhost`, and that the loopback-issuer app gets no
`sso.localhost:host-gateway` extra host.

`e2e/tests/hermes.spec.ts` runs the user-visible contract against a real
install: the gate is on (an unauthenticated visitor is handed to a sign-in
surface: the dashboard's own chooser or, with a single registered provider, the
shared Authentik flow), and completing the Authentik login on the loopback
issuer origin returns the user to the authenticated dashboard. Not covered here:
the live OIDC round-trip's own PKCE exchange, which is upstream's surface, not
Bloud's.
