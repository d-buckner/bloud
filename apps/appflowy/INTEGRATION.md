# AppFlowy Integration

## Status: Complete (browser-verified: local sign-up + re-run login; full VM e2e via `./bloud e2e`)

AppFlowy (https://appflowy.com) is an open-source, AI-native Notion
alternative — docs, databases, and real-time collaboration. Bloud installs it
as a nine-container stack with an nginx entry point and a self-hosted GoTrue
(Supabase Auth) for identity. Users sign up locally; Bloud SSO is not used
(see "Why no SSO" below).

- Images (pinned): `appflowyinc/appflowy_cloud:0.17.12`,
  `appflowyinc/appflowy_web:0.16.18`, `appflowyinc/appflowy_worker:0.17.12`,
  `appflowyinc/appflowy_search:0.17.12`, `appflowyinc/gotrue:0.17.12`
- SSO strategy: `none` (local email/password, auto-confirm)
- Public URL: `http://appflowy.localhost:8080` (app subdomain on the Bloud
  base domain); `:8480` direct on the host for debugging

## Why no SSO

AppFlowy's auth layer is its own GoTrue container, and the cloud API
authenticates GoTrue-minted JWTs on every request. Two Bloud SSO strategies
were ruled out:

1. **forward-auth** — Traefik could authenticate the browser, but the SPA's
   API calls still need a valid GoTrue session token; proxy headers do not
   reach the API layer.
2. **native-oidc** — GoTrue *can* act as an OIDC relying party, but its
   custom-OIDC support enforces an HTTPS issuer, and Bloud's dev/self-host
   issuer is plain HTTP (self-signed at most).

So AppFlowy runs with `strategy: none`: `GOTRUE_DISABLE_SIGNUP=false` and
`GOTRUE_MAILER_AUTOCONFIRM=true` (no SMTP in the stack), which makes
email/password sign-up work immediately out of the box.

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
| `apps/appflowy/metadata.yaml` | Nine containers, private network, port 8480, strategy `none` |
| `apps/appflowy/configurator.go` | nginx.conf writer (PreStart), route verification (PostStart) |
| `apps/appflowy/configurator_test.go` | Unit tests: config drift/idempotency/mode, route polling + cancellation |
| `services/host-agent/cmd/host-agent/main.go` | Template vars (derived secrets + public URLs) |
| `services/host-agent/internal/appconfig/register.go` | Configurator registration |
| `services/host-agent/internal/e2e/e2e_test.go` | Go integration tests (install/configure/uninstall) |
| `e2e/tests/appflowy.spec.ts` | Playwright user journey (home tile → local login/sign-up → app) |
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

Behavioral assertions (not config values): the Playwright spec logs in (or,
on a fresh deployment, signs up) a fixed local user through the real
`/gotrue` proxy and lands in the app shell (`/app/:workspaceId`, sidebar
visible); the Go integration test checks all three proxied health routes,
performs a real GoTrue sign-up (200 + `access_token`), then asserts full
uninstall cleanup (nine containers, data dir, routes).

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
| Slow first boot | Expected: ~1.5GB of images plus Rust migrations before the cloud listener opens. The healthcheck window covers ~2 minutes of migrations after the pull. |
