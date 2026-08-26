# AppFlowy Integration

## Status: Complete (SSO wired for public deployments; local sign-up verified in dev VM; full VM e2e via `./bloud e2e`)

AppFlowy (https://appflowy.com) is an open-source, AI-native Notion
alternative — docs, databases, and real-time collaboration. Bloud installs it
as a nine-container stack with an nginx entry point and a self-hosted GoTrue
(Supabase Auth) for identity. Bloud SSO is wired **inside GoTrue** as a
custom OIDC provider ("Bloud SSO" login button) so identities stay in
Authentik; on local dev deployments (where GoTrue rejects localhost
issuers) the stack falls back to local email/password sign-up.

- Images (pinned): `appflowyinc/appflowy_cloud:0.17.12`,
  `appflowyinc/appflowy_web:0.16.18`, `appflowyinc/appflowy_worker:0.17.12`,
  `appflowyinc/appflowy_search:0.17.12`, `appflowyinc/gotrue:0.17.12`
- SSO strategy: `none` at the routing layer (Traefik does not forward-auth
  AppFlowy's routes — the SPA's API calls need a GoTrue session); Bloud SSO
  is provided by the GoTrue custom OIDC provider managed by the configurator
  (see "How SSO works" below)
- Public URL: `http://appflowy.localhost:8080` (app subdomain on the Bloud
  base domain); `:8480` direct on the host for debugging

## How SSO works (and why the routing strategy is `none`)

AppFlowy's auth layer is its own GoTrue container, and the cloud API
authenticates GoTrue-minted JWTs on every request. That rules out the
proxy-level strategies:

- **forward-auth** — Traefik could authenticate the browser, but the SPA's
  API calls still need a valid GoTrue session token; proxy headers do not
  reach the API layer.
- **native-oidc (routing)** — same problem: the app's own auth layer must
  mint the JWT, so the SSO relationship has to live *inside* GoTrue.

GoTrue provides exactly that: this fork (`appflowyinc/gotrue:0.17.12`)
ships admin-registered **custom OIDC providers**
(`POST /admin/custom-providers`) that render as named "Continue with …"
buttons in AppFlowy's login UI. The configurator registers one against
Bloud's SSO issuer on every reconcile:

1. **Authentik side** — ensures the `AppFlowy OAuth2 Provider` application
   (client id `appflowy-client`, redirect URI
   `http://appflowy.<base>/gotrue/callback`, launch URL the app's public
   origin) exists, idempotently.
2. **GoTrue side** — authenticates as the GoTrue admin (derived password)
   and ensures the `custom:bloud-sso` provider: issuer
   `https://sso.<base>:<tls-port>/application/o/appflowy/`, client id
   `appflowy-client`, client secret (HKDF-derived, stable), scopes
   `openid email profile`, attribute mapping `email → {claims.email}`
   (Authentik's verified-email scope mapping). A drifted provider is
   deleted and re-created.

The issuer must be HTTPS (GoTrue schema, migration `20260219120000`), so
Bloud serves it through the Traefik `websecure` entrypoint with the local
CA leaf (`plans/self-signed-tls.md`). GoTrue's discovery/JWKS/token calls
reach `https://sso.localhost:8443` via `extraHosts: sso.localhost:host-gateway`
and trust the CA through the mounted bundle (`SSL_CERT_FILE`, a Go-runtime
override that keeps system roots too).

**Deployment classes.** GoTrue additionally *rejects local issuers* at
registration (literal localhost/loopback hosts and hostnames resolving to
private/loopback addresses — verified against the image). So:

- **Public `BLOUD_BASE_DOMAIN`**: the wiring converges and users sign in
  with the "Bloud SSO" button; identities are created in the Bloud
  directory (Authentik), not a second store.
- **localhost/loopback (dev VM)**: the configurator skips the wiring with
  an info log — it is skipped, *not* failing. `GOTRUE_DISABLE_SIGNUP=false`
  + `GOTRUE_MAILER_AUTOCONFIRM=true` (no SMTP) keep local email/password
  sign-up working, so the app is fully usable either way.

The wiring is best-effort by design: any failure (Authentik or GoTrue
unreachable, a 400 validation rejection) is logged and retried on the next
reconcile cycle — it never blocks the app from starting.

## Architecture

### Container graph

```
apps-appflowy-postgres (pgvector/pg16)  ─┬─> gotrue ─┬─> cloud ─> web ─> nginx (:80→8480)
apps-appflowy-redis    (redis 7)        ─┼─> cloud ─┘       ▲
apps-appflowy-minio    (minio)           ─┼─> cloud          │
                                          ├─> worker         │
                                          └─> search ────────┘ (API points at search)
```

Only `apps-appflowy-nginx` publishes a port (8480 → 80). Everything else
talks over the private `appflowy-internal` network using the fixed container
names. Apps own their infrastructure (repo invariant): AppFlowy declares its
own Postgres, Redis, and MinIO — no shared Bloud-wide services.

- **cloud** is the API + realtime backend; its Rust migrations run on first
  boot before the listener opens, so first boot is slow (healthcheck window:
  24 × 5s).
- **worker** (import/processing) and **search** (keyword index) are side
  services the cloud API points at; nothing depends on the worker, so it has
  no healthcheck (the runtime image ships no curl/wget/nc and dash has no
  `/dev/tcp` — no in-container probe is possible).
- **search**'s healthcheck uses bash `/dev/tcp` (mirrors the official
  docker-compose), since its image has no curl.
- **gotrue** is the auth service (port 9999 in-container); the web SPA and
  browser talk to it through the `/gotrue` nginx prefix.

### Secrets (derived, stable)

Per-deployment secrets are derived from the SSO host secret via
`sso.DeriveSecret` in `cmd/host-agent/main.go` and injected as template
vars — stable across reboots, no extra persistence:

| Template var | Purpose |
|---|---|
| `{{appflowyGotrueJwtSecret}}` | GoTrue JWT signing key (shared by cloud + search for verification) |
| `{{appflowyGotrueAdminPassword}}` | GoTrue admin account (`admin@appflowy.local`) |
| `{{appflowyMinioAccessKey}}` | MinIO root user (20 chars) |
| `{{appflowyMinioSecretKey}}` | MinIO root password |

Public URLs are derived from `BLOUD_SSO_BASE_URL` the same way Traefik routes
are (`appSubdomainURL` / `appWSSubdomainURL` in `main.go`):

| Template var | Example |
|---|---|
| `{{appflowyPublicURL}}` | `http://appflowy.localhost:8080` |
| `{{appflowyWsURL}}` | `ws://appflowy.localhost:8080/ws/v2` |

### nginx reverse proxy

PreStart (bound to the nginx node, which starts last) writes
`<dataDir>/appflowy/config/nginx.conf` (mode 0644 — the unprivileged nginx
worker reads it through the "other" bits), mounted at `/etc/nginx/nginx.conf`.
`changed=true` only on content/mode drift, so steady-state reconciles never
churn the container.

| Browser path | Upstream | Notes |
|---|---|---|
| `/` | web (:80) | SPA + SSR |
| `/api` | cloud (:8000) | `/api/chat` and `/api/import` get long timeouts + buffering off |
| `/ws` | cloud (:8000) | WebSocket upgrade, 24h read timeout |
| `/gotrue/*` | gotrue (:9999) | prefix stripped |
| `/minio-api/*` | minio (:9000) | prefix stripped; **Host rewritten to the internal name** — presigned URLs are SigV4-signed against it |
| `/health` | web (:80) | stack healthcheck |

PostStart verifies the wiring behaviorally: `/health`, `/api/health`, and
`/gotrue/health` must all answer 200 through the published port (5-minute
deadline; container healthchecks already gate convergence, so this is fast in
practice).

## Files

| File | Purpose |
|------|---------|
| `apps/appflowy/metadata.yaml` | Nine containers, private network, port 8480, strategy `none`; GoTrue CA bundle mount + `extraHosts` |
| `apps/appflowy/configurator.go` | nginx.conf writer (PreStart), route verification + SSO wiring (PostStart) |
| `apps/appflowy/sso.go` | SSO wiring: Authentik application + GoTrue custom provider (idempotent, drift-replacing, best-effort); `IssuerRegistrable` |
| `apps/appflowy/configurator_test.go` | Unit tests: config drift/idempotency/mode, route polling + cancellation |
| `apps/appflowy/sso_test.go` | Unit tests: SSO config derivation, provider create/idempotency/drift, local-issuer skip, failure survival |
| `services/host-agent/cmd/host-agent/main.go` | Template vars (derived secrets + public URLs + CA bundle path) |
| `services/host-agent/internal/appconfig/register.go` | Configurator registration (`appflowySSOConfig`) |
| `services/host-agent/internal/e2e/e2e_test.go` | Go integration tests (install/configure/SSO-wiring/uninstall) |
| `e2e/tests/appflowy.spec.ts` | Playwright journeys (SSO button on public deployments; local login/sign-up on localhost) |
| `dev/lima.yaml`, `cli/backend/{lima,qemu}.go` | Host port 8480 forwarding |

## Key endpoints (through nginx, :8480 / :8080)

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/health` | GET | nginx → web (stack health) |
| `/api/health` | GET | cloud API health |
| `/gotrue/health` | GET | GoTrue health |
| `/gotrue/signup` | POST | local sign-up (auto-confirm) |
| `/gotrue/token?grant_type=password` | POST | login (used by the SPA) |

## Verification

```bash
# User journey (requires a running dev runtime):
cd e2e && npx playwright test appflowy

# Full Go integration path (fresh VM, real install/reconcile/uninstall):
./bloud validate --tier integration
```

Behavioral assertions (not config values):

- The Playwright spec runs the deployment-class journey: on a public
  deployment it clicks the "Bloud SSO" button, completes the Authentik
  login, and lands in the app shell (`/app/:workspaceId`); on a localhost
  deployment it logs in (or, on a fresh deployment, signs up) a fixed local
  user through the real `/gotrue` proxy and lands in the app shell.
- The Go integration test (`TestAppFlowySSOWiring`) asserts the wiring
  outcome per class: for a local issuer, no `custom:bloud-sso` provider in
  GoTrue and no `AppFlowy OAuth2 Provider` in Authentik (wiring skipped,
  app still RUNNING); for a public issuer, the provider exists with the
  exact issuer, client id, scopes, and email attribute mapping. The
  uninstall test then asserts full cleanup (nine containers, data dir,
  routes).

## Known limitations

- **Free license = one seat.** AppFlowy Cloud's built-in free license allows
  exactly one Member/Owner user *across all workspaces*; a second local
  sign-up is rejected with "Seat limit reached". The e2e spec therefore uses
  a fixed account (`e2e@appflowy.local`) with a login-first flow: the first
  run signs up, re-runs log in. Multi-user deployments need a purchased
  AppFlowy Cloud license.
- **Client-side password policy.** The sign-up form enforces ≥6 chars with
  upper-, lower- and special-case (GoTrue itself only requires length ≥6).
  The e2e password satisfies the UI policy.
- **Signed-in URL check must use the pathname.** The SPA redirects `/` →
  `/app` → `/app/:workspaceId`. Assert on `URL.pathname` — the host
  (`appflowy.*`) matches a naive `/\/app/` regex — and use an absolute goto
  for the sign-up form (the e2e config `baseURL` is the Bloud home, not the
  app subdomain).

## Troubleshooting

| Symptom | Cause / fix |
|---------|-------------|
| Sign-up succeeds but the app stays on the login screen | The cloud API cannot verify the GoTrue JWT — `{{appflowyGotrueJwtSecret}}` differs between gotrue and cloud (should not happen: both derive from the same template var). Check the two containers' env. |
| Presigned file links 403 | The `/minio-api` Host rewrite is broken (SigV4 mismatch) — compare `nginx.conf` with the version in `configurator.go`. |
| Realtime/collab disconnects | `/ws` upgrade headers missing, or `APPFLOWY_WS_BASE_URL` points at the wrong origin — it must be `ws(s)://<public host>/ws/v2` (derived from `BLOUD_SSO_BASE_URL`). |
| Worker never processes imports | Worker image has no healthcheck, so its failures are silent — check `podman logs apps-appflowy-worker` (Redis + DB URLs must be reachable on the internal network). |
| "Bloud SSO" button missing from the login screen | The GoTrue provider is not registered. On localhost/loopback deployments this is expected (local issuers are rejected — local sign-up is the contract). On a public deployment: check the host-agent logs for `appflowy SSO` warnings (Authentik/GoTrue unreachable, or a 400 rejection) and confirm the issuer `https://sso.<base>:<tls-port>/application/o/appflowy/` is reachable from the VM. |
| SSO login bounces back with an error | The GoTrue→Authentik round trip failed: verify the CA bundle is mounted in the gotrue container (`SSL_CERT_FILE`), that `https://sso.<base>` answers from inside the VM, and that the Authentik application's redirect URI matches `http://appflowy.<base>/gotrue/callback`. |
| Slow first boot | Expected: ~1.5GB of images plus Rust migrations before the cloud listener opens. The healthcheck window covers ~2 minutes of migrations after the pull. |
