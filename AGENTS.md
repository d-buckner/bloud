# AGENTS.md — Bloud

Operating instructions for AI coding agents (and humans) working in this repository.
Everything here is verified against the code — if it contradicts a doc, the code wins;
fix the doc in the same change.

## What Bloud is

Bloud is an open-source home server: you add an app and the reverse proxy, SSL, SSO,
and databases get set up for you. Apps declare what they **provide** and **consume**
in a portable `metadata.yaml`; a small Go service (**host-agent**) runs an
intent-driven orchestrator that continuously makes reality match intent, on install
and after every crash/reboot. Status: alpha. License: AGPL-3.0.

The differentiator is not container installation — it's that Bloud holds the
integration knowledge (API keys, OIDC clients, LDAP wiring) and keeps those
relationships working.

## Repo map

| Path | What it is |
|---|---|
| `cli/` | Go module (`.../bloud/cli`) — the `./bloud` dev/validation CLI. Builds to repo root `./bloud` (gitignored). |
| `services/host-agent/` | Go module — the runtime: API server (:3000), orchestrator, catalog, stores, container management. |
| `services/host-agent/web/` | SvelteKit 2 + Svelte 5 frontend (npm workspace `@bloud/host-agent-web`), static build served by host-agent. |
| `apps/` | Go module — the app catalog. One dir per app: `metadata.yaml` + `configurator.go` (+ assets). |
| `e2e/` | Playwright (TS) browser tests of the user-visible lifecycle. |
| `dev/` | VM configs (`lima.yaml`, `qemu.yaml`). |
| `validation.yaml` | Manifest for `./bloud validate`: tier commands + path→command inference + app registry. |
| `specs/` | `spec.md` (authoritative release plan), `reconciler-spec.md`, `review.md`. |
| `plans/` | Design plans (qemu-backend, tailnet-outpost, control-plane-auth, persistent-ports-fqdn-remote, layout-refactor). |
| `docs/` | architecture/, guides/, specifications/, features/, operations/tech-debt.md. |
| root `package.json` | npm workspaces + turbo; husky pre-commit runs `npm run test:precommit`. |

Go modules are linked by `replace` directives (host-agent ↔ apps). CI: GitHub Actions
(`.github/`) and Forgejo (`.forgejo/`), Go 1.25 / Node 22.

## Toolchain & first-time setup

- Go 1.25 (host-agent, apps), Go 1.24 (cli), Node ≥18 (CI uses 22), npm 10.
- Host tools: `go`, `node`, `limactl`, `podman` (plus `qemu-system-x86_64` for the QEMU backend).
  `./bloud setup` (or `npm run setup`) checks prerequisites and rebuilds `./bloud`.
- Build the CLI: `cd cli && go build -o ../bloud .`

Development runs **inside a VM** — macOS uses Lima (default), Linux uses QEMU
(`BLOUD_BACKEND=qemu`):

```bash
# Lima (macOS)
limactl create --name=bloud-dev dev/lima.yaml
limactl start bloud-dev

# QEMU (Linux) — self-provisioning
BLOUD_BACKEND=qemu ./bloud dev              # creates .bloud/qemu/bloud-qemu (gitignored)
# manual SSH: ssh -p 2222 -i .bloud/qemu/bloud-qemu/id_ed25519 bloud@127.0.0.1
```

`./bloud dev` is the whole loop: provisions the VM if needed, builds host-agent
(`CGO_ENABLED=0 GOOS=linux`) + frontend, deploys both into the VM, and runs
host-agent in the foreground (Ctrl-C stops it). **There is no hot reload —
re-run `./bloud dev` after any code change** (`./bloud rebuild` is a no-op; the
Nix runtime was removed).

VM data lives in `/var/tmp/bloud-dev-runtime` (Lima) or `/var/tmp/bloud-qemu-runtime`
(QEMU): `<dir>/host-agent` (binary + `web/build`), `<dir>/data` (BLOUD_DATA_DIR,
SQLite `bloud.db`, `secrets.json`), apps dir points at the repo's `apps/`.

### Ports (forwarded to host localhost)

| Port | What | Audience |
|---|---|---|
| **80** | **Front proxy** — a thin root-level reverse proxy (`host-agent front-proxy`, systemd unit `bloud-front.service`) that forwards to Traefik `:8080` and serves a "starting up" page while the stack boots. It owns port 80 so Traefik and every container stay rootless. | end users (default browser port) |
| **8080** | **Traefik — the public-facing port.** Users hit apps here (`jellyfin.localhost:8080`, `immich.localhost:8080`, …). Browser/e2e user journeys must go through this. | end users |
| **3000** | **host-agent internal API** (install/uninstall/status, session auth with loopback/trusted-net bypass). Operator/automation surface, not the user surface. | ops, CLI, e2e API helpers |
| 8096 | Jellyfin container (direct) | debugging |
| 9001 | Authentik server (direct) | debugging |
| 3389 | LDAP outpost (direct) | debugging |
| 2283 / 4533 | Immich / Navidrome (direct) | debugging |
| 3010 | AFFiNE (direct) | debugging |

QEMU note: slirp NAT presents host-forwarded connections from the gateway
(10.0.2.2), so `./bloud dev` sets `BLOUD_TRUSTED_LOCAL_NETS=10.0.2.0/24` for the
host-agent; Lima forwards to loopback and needs none.

Port-80 note: the guest's port 80 (front proxy) is host-forwarded like every
other port. Non-root hosts cannot bind host port 80, so when it is not
bindable `./bloud dev` automatically forwards guest 80 to host **8088** (then
8089/8090) with a note on stdout — no-port URLs like `http://jellyfin.localhost`
then need `sudo setcap 'cap_net_bind_service=+ep' $(command -v
qemu-system-x86_64)` (or `BLOUD_QEMU_FWD_80=<port>`). Existing VMs are
restarted automatically when the QEMU launch args change (recorded in
`.bloud/qemu/<instance>/<instance>.launch-args`). Lima VMs pick up the port-80
forward + `bloud-front.service` by recreating the instance
(`limactl delete bloud-dev && limactl create --name=bloud-dev dev/lima.yaml &&
limactl start bloud-dev`).

## Daily dev loop

```bash
./bloud dev               # build + deploy + run host-agent (Ctrl-C to stop)
./bloud status            # VM + host-agent health (GET :3000/api/health)
./bloud services          # app container status (systemd units apps-*)
./bloud logs              # stream host-agent logs (journalctl)
./bloud install <app>     # POST :3000/api/apps/<app>/install (needs running agent)
./bloud uninstall <app>   # POST :3000/api/apps/<app>/uninstall
./bloud attach            # shell on the VM
./bloud shell <cmd>       # run a command on the VM
./bloud stop              # stop host-agent
./bloud reset             # wipe all data in the VM, keep the VM
./bloud destroy           # delete the VM
```

## Validation & testing
`validation.yaml` is the single source of truth for what to run. `./bloud validate`
writes a JSON ledger per run to `.bloud/validation/` (timestamped + `latest.json`,
pruned to the newest 20).

| Tier | Command | What happens |
|---|---|---|
| `fast` (~30s) | `./bloud validate --tier fast` | host-agent go tests, orchestrator race tests, apps go tests, cli go tests, web vitest + svelte-check, license header check |
| `changed` (default) | `./bloud validate` | `git diff` (default base `HEAD`; `--since <ref>`) → infer commands via `inference.paths` globs in validation.yaml; reports risk areas + affected apps; unmapped files drop confidence to "medium" |
| `integration` | `./bloud validate --tier integration` | Requires the VM: builds host-agent, frontend, and the integration test binary locally; deploys them to the guest's `/var/tmp/bloud-validate-runtime` behind a systemd user service (`bloud-validate-host-agent.service`) plus `init-secrets`; waits for API convergence; then runs the prebuilt test binary in the VM (the tests install Jellyfin through the real API) |

Flags: `--tier fast|changed|integration`, `--app <name>`, `--dry-run`, `--explain`,
`--json`, `--since <ref>`.

Run individual suites directly (from the repo root unless noted):

```bash
cd services/host-agent && go test ./...          # backend unit tests
cd services/host-agent && go test -race ./internal/orchestrator/...
cd apps && go test ./...                          # configurator tests
cd cli && go test ./...
npm run test --workspace=@bloud/host-agent-web    # vitest
npm run check --workspace=@bloud/host-agent-web   # svelte-check (typecheck)
cd e2e && npx playwright test                     # browser e2e (see below)
```

**Pre-commit hook (husky) runs `npm run test:precommit`** = license header check
+ host-agent + apps Go tests + web TS tests. Don't commit without it passing;
don't disable the hook.

License headers: every source file starts with `// SPDX-License-Identifier: AGPL-3.0-only`
+ copyright line (syntax per language; `.svelte` files carry it inside `<script>`).
`npm run license:check` verifies, `npm run license:fix` stamps. Excluded: JSON,
go.mod/go.sum, docs, binaries, `*.golden.yml` testdata, and the runtime-managed
`apps-routes.yml` (see `scripts/license-header.mjs`).

### Playwright e2e (`e2e/`)

- Browser tests target the **public port**: `BLOUD_URL` (default
  `http://localhost:8080`) — user journeys go through Traefik.
- API helpers target the **internal port**: `BLOUD_API_URL` (default
  `http://localhost:3000`) — loopback, no auth needed.
- Specs: `jellyfin.spec.ts` (LDAP SSO), `navidrome.spec.ts` (forward-auth),
  `immich.spec.ts` (native-oidc + onboarding), `affine.spec.ts`
  (native-oidc, login via issuer origin). Fixtures: `lib/fixtures.ts`
  (`authenticatedPage`, `api`); shared login: `lib/auth.ts`, `lib/loginPage.ts`.
- Config: single worker, no retries, 10 min/test, trace/screenshot/video retained
  on failure. `./bloud e2e` runs the suite against a runtime started by
  `./bloud dev`.
- `./bloud e2e lifecycle [--host-only] [--keep]` is self-contained: deploys
  host-agent + catalog to the VM as systemd user service
  `bloud-e2e-host-agent.service` into `/var/tmp/bloud-e2e-runtime`, **installs
  Jellyfin through the real host-agent API (the dependency-graph path)**, runs
  Playwright, restarts services, re-runs Playwright, uninstalls and asserts
  cleanup. Key env: `BLOUD_E2E_LIMA_INSTANCE` (default `bloud-dev`),
  `BLOUD_E2E_QEMU_INSTANCE`, `BLOUD_E2E_SSH_TARGET`, `BLOUD_E2E_RUNTIME_DIR`,
  `BLOUD_E2E_GOARCH` (amd64|arm64), `BLOUD_E2E_USERNAME`/`BLOUD_E2E_PASSWORD`
  (defaults `e2etest`/`e2etest123`), `BLOUD_E2E_TRAEFIK_DYNAMIC_DIR`.

## `./bloud` CLI reference

```
Setup:       setup                Check prerequisites and build CLI
Dev (VM):    dev                  Build + deploy + run host-agent (Ctrl-C to stop)
            start                Show quick-start instructions
            stop | status | services | logs
            attach | shell [cmd] Shell / run command on the VM
            install <app> | uninstall <app>    via host-agent API (:3000)
            reset | destroy      Wipe VM data (keep VM) / delete VM
Validation:  validate [flags]     Tiered validation (default --tier changed)
            e2e [lifecycle]      Playwright against running runtime / full lifecycle
Other:       depgraph             Mermaid dependency graph from app metadata
```

The CLI resolves the project root by walking up from cwd looking for
`cli/main.go`, `specs/spec.md`, etc., and loads a gitignored root `.env`
(existing env vars win). Backend selection: `BLOUD_BACKEND=qemu|lima`
(default lima); instance overrides: `BLOUD_E2E_LIMA_INSTANCE` (default
`bloud-dev`), `BLOUD_QEMU_INSTANCE` (default `bloud-qemu`).

## Architecture invariants (do not break)

1. **Orchestrator is the single writer.** All mutations flow through the typed
   intent queue (`internal/orchestrator/intent.go`); the orchestrator is the only
   author of lifecycle status and the only executor of side effects. API handlers
   submit intents (202 accepted) and return current state — they must not write
   stores directly or advance app status.
2. **Configurators are idempotent.** `PreStart`/`PostStart` run on *every*
   reconciliation cycle (install, crash recovery, reboot). A configurator that
   can't run twice is a bug.
3. **Apps own their infrastructure.** Apps that need databases declare their own
   postgres/redis containers in `containers:` (e.g. Immich: pgvector postgres +
   redis + server + ML). There is no shared per-app database in the product path.
4. **The graph sees nodes, not apps.** Each `containers:` entry is one graph node;
   `dependsOn` builds the DAG; the reconciler converges topological levels
   (`INITIALIZING → PRESTART → STARTING → POSTSTART → RUNNING`) concurrently
   within a level. No "app grouping" concept in the orchestrator.
5. **Catalog is disk-driven.** Apps are discovered from `apps/*/metadata.yaml`
   into an in-memory cache; `POST /api/apps/refresh-catalog` or restart to pick
   up changes. System apps set `isSystem: true` (hidden from the user catalog).
   Bootstrap (system infra: Traefik + deps) runs **before** the HTTP listener
   opens; the orchestrator manages user apps only.
6. **SSO strategies** are exactly: `native-oidc`, `ldap`, `forward-auth`, `none`
   (Immich + AFFiNE: native-oidc, Jellyfin: ldap, Navidrome: forward-auth).
   Native-oidc providers use Bloud's verified-email scope mapping (Authentik's
   managed one reports `email_verified: false`, which apps like AFFiNE
   reject) — see `apps/affine/INTEGRATION.md`.
7. **Routing is regenerated after convergence** — the orchestrator rewrites the
   Traefik dynamic config (`BLOUD_TRAEFIK_DYNAMIC_DIR/apps-routes.yml`) before
   promoting nodes to RUNNING. (Route generation must not accumulate runtime
   side effects — see tech debt.)
8. **Config precedence**: env var > `secrets.json` (generated by
   `host-agent init-secrets`) > hardcoded dev fallback. Key env:
   `BLOUD_DATA_DIR`, `BLOUD_APPS_DIR`, `BLOUD_TRAEFIK_DYNAMIC_DIR`,
   `BLOUD_PODMAN_SOCKET`, `BLOUD_PORT` (3000), `BLOUD_BASE_DOMAIN`,
   `BLOUD_SSO_BASE_URL` / `BLOUD_SSO_AUTHENTIK_URL` / `BLOUD_SSO_ISSUER_URL`,
   `BLOUD_TRUSTED_LOCAL_NETS`.
9. **Hosts are a first-class setting.** The instance is reachable under a set
   of hostnames — built-ins `localhost` + `bloud.local`, plus admin-added
   custom domains (Settings → Hosts, `GET/PUT /api/settings/hosts`). One host
   is **primary** (drives the OIDC issuer + launch URLs). Admin-saved hosts
   win over `BLOUD_BASE_DOMAIN`/`BLOUD_SSO_BASE_URL`, which only seed the
   initial state. URL rules: `localhost` → `http://localhost:8080` (dev/e2e
   parity), any other host → `http://<host>` (port 80, served by the front
   proxy). Issuer: `http://sso.localhost:8080` for a localhost primary
   (containers resolve `sso.localhost` via `extraHosts`), `http://<primary>`
   otherwise (the orchestrator injects `<primary>:host-gateway` into
   native-oidc containers so the issuer resolves inside them). Host changes
   flow through the orchestrator (`SetHostsIntent`): persist, update the live
   `hostset.State`, reset SSO apps + `apps-authentik-server` so the lifecycle
   re-provisions Authentik (redirect URIs, outpost browser URL) and rewrites
   app configs, then re-ensure the dashboard OAuth app. Traefik routes stay
   domain-agnostic (`HostRegexp`), so they match every host without changes.
10. **The front proxy owns port 80** (`host-agent front-proxy`,
    `bloud-front.service`, root). It health-gates on the host-agent
    (`GET :3000/api/health`, which only opens after system convergence) and
    proxies to `:8080` preserving the `Host` header; while the stack is down
    it serves an auto-reloading "starting up" page. Do not bind :80 from any
    rootless container instead — that is the point of this split.
11. **Frontend is a static build** served by host-agent from
    `<host-agent-dir>/web/build` (embedded `dev_dashboard.html` is only the
    missing-build fallback). Rebuild the frontend before deploying.
12. **Managed containers are labeled** `io.bloud.managed=true` and
    `io.bloud.app=<name>`; container names follow `apps-<name>` /
    `apps-<name>-<component>`. e2e assertions rely on these labels.

## host-agent HTTP API (port 3000)

- Public: `GET /health`, `GET /auth/login`, `GET /auth/callback`,
  `POST /auth/logout`, `GET /api/health`, `GET /api/setup/status`,
  `GET /api/auth/me`, plus the public system-info router.
- Authenticated (session cookie, or loopback/`BLOUD_TRUSTED_LOCAL_NETS` bypass):
  `GET /api/apps` (catalog), `GET /api/apps/installed`,
  `GET /api/apps/{name}/metadata`, `POST /api/apps/{name}/install`,
  `POST /api/apps/{name}/uninstall`, `PATCH /api/apps/{name}/rename`,
  home + logs routers.
- Admin: `POST /api/apps/refresh-catalog`, `GET /api/system/rebuild/stream`,
  settings (incl. `GET/PUT /api/settings/hosts` — the multi-host setting),
  sharing, remote-apps routers.

## Adding an app

1. `apps/<name>/metadata.yaml` — full field reference in
   `services/host-agent/internal/catalog/models.go` (source of truth):
   `name`, `displayName`, `description`, `category` (media | productivity |
   security | infrastructure), `port`, `isSystem`, `sso`
   (`strategy`, `callbackPath`, `userCreation`, `bypassPaths`, `env` mappings),
   `integrations` (`proxy` / `sso` / `database`: `{required, multi,
   compatible: [{app, default}]}`), `containers[]`
   (`name`, `image` — **pin versions**, `command`, `network`/`networks`,
   `restartPolicy`, `environment`, `extraHosts`, `ports`, `volumes`,
   `dependsOn`, `healthCheck {test, interval, timeout, retries}`).
   Template vars in environment/volumes: `{{appDataDir}}`, `{{dataDir}}`,
   `{{postgresPassword}}`.
2. `apps/<name>/configurator.go` — implements `NodeLifecycle`
   (`Name()`, `PreStart(ctx, *AppState) (changed bool, err error)`,
   `PostStart(ctx, *AppState) error`, `Remove(ctx, *AppState, clearData bool)
   error`) from `pkg/configurator`. `AppState` carries `DataPath`,
   `BloudDataPath`, `SSOEnabled`, typed `LDAP` output. Register it in
   `services/host-agent/internal/appconfig/register.go`.
3. Tests: unit tests in the app package; integration assertions in
   `services/host-agent/internal/e2e/e2e_test.go` (build tag `integration`);
   user-journey spec in `e2e/tests/`. **Test behavioral outcomes** (verify via
   the app's own API), not config values.
4. Add the app to `validation.yaml` (`apps:` block: auth strategy,
   validation-level, file globs, optional `e2e-project`).
5. Reference patterns: `apps/jellyfin` (LDAP, setup wizard, plugins),
   `apps/authentik` (multi-container, LDAP infra), `apps/immich` (own
   postgres+redis, native-oidc), `apps/affine` (own postgres+redis, OIDC
   config file, first-run owner bootstrap), `apps/navidrome` (forward-auth).

## Integration validation runs the real dependency-graph path

The integration tier (`./bloud validate --tier integration`) and the Go tests
in `services/host-agent/internal/e2e/` provision their runtime the same way a
real install works: deploy host-agent + catalog into the VM as a systemd user
service, install Jellyfin through `POST /api/apps/jellyfin/install`, let the
orchestrator converge, then run the behavioral tests inside the VM.

The old `dev/compose.yml` static stack (shared postgres/redis/authentik/jellyfin)
was retired in 2026-08: it bypassed the catalog planner, the orchestrator
intent queue, and the `io.bloud.managed` container labels, so integration
tests could pass while the real install/reconcile flow was broken — and fail
for reasons the product path never hits (shared postgres vs per-app
postgres, compose service naming, no graph ordering).

`./bloud e2e lifecycle` is the full user-visible version of the same path
(install → browser journeys → service restart → reinstall → uninstall and
cleanup assertions). The Playwright suite (`e2e/tests/*.spec.ts`) remains the
mandatory regression gate for changes to install/reconcile behavior.

## Known debt (verified 2026-08-17)

Full backend-debt ledger with the repayment plan:
`docs/operations/tech-debt.md` (lifecycle state ownership, in-memory lifecycle
graph, route-generation side effects, ad hoc migrations). Review findings:
`specs/review.md` (e.g. §C1 production router constructs the orchestrator with
`CatalogGraph: nil`, §C2 in-memory `MapRepository`). Highlights:

- Sharing/guest API handlers write stores directly, bypassing the intent queue.
- Admin is granted to loopback requests without a credential; config ships
  hardcoded fallback secrets when env/`secrets.json` are unset.
- Keep the `apps:` registry in `validation.yaml` in sync with `apps/` when apps
  are added/removed (the changed tier infers affected apps from it).

## Environment conventions (by design)

- `dev/lima.yaml` hardcodes the repo mount at `~/Projects/bloud` (Lima reads
  the yaml verbatim) — adjust if the checkout lives elsewhere. The QEMU backend
  auto-detects the checkout dir; `dev/qemu.yaml` documents the spec only.
- The CLI loads a gitignored root `.env` before dispatching commands (existing
  env vars win).

## Docs map (read for…)

| Question | Read |
|---|---|
| What are we building / release plan | `specs/spec.md` |
| Orchestrator/reconciler design | `specs/reconciler-spec.md` |
| Component overview + data flows | `docs/architecture/overview.md` |
| How to add an app | `docs/guides/contributing-apps.md` |
| Multi-container app model | `docs/specifications/app-spec.md` |
| Backend debt + repayment plan | `docs/operations/tech-debt.md` |
| Sharing/federation (in progress) | `docs/features/sharing.md` |
| In-flight designs | `plans/*.md` |
