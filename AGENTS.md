# AGENTS.md: Bloud

Operating instructions for AI coding agents (and humans) working in this repository.
Everything here is verified against the code: if it contradicts a doc, the code wins;
fix the doc in the same change.

## What Bloud is

Bloud is an open-source home server: you add an app and the reverse proxy, SSL, SSO,
and databases get set up for you. Apps declare what they **provide** and **consume**
in a declarative `metadata.yaml`; a small Go service (**host-agent**) runs an
intent-driven orchestrator that continuously makes reality match intent, on install
and after every crash/reboot. Status: alpha. License: AGPL-3.0.

The differentiator is not container installation: it's that Bloud holds the
integration knowledge (API keys, OIDC clients, LDAP wiring) and keeps those
relationships working.

## Repo map

| Path | What it is |
|---|---|
| `cli/` | Go module (`.../bloud/cli`): the `./bloud` dev/validation CLI. Builds to repo root `./bloud` (gitignored). |
| `services/host-agent/` | Go module. The runtime: API server (:3000), orchestrator, catalog, stores, container management. |
| `services/host-agent/web/` | SvelteKit 2 + Svelte 5 frontend (npm workspace `@bloud/host-agent-web`), static build served by host-agent. |
| `apps/` | Go module: the app catalog. One dir per app: `metadata.yaml` + `configurator.go` (+ assets). |
| `e2e/` | Playwright (TS) browser tests of the user-visible lifecycle. |
| `dev/` | VM configs (`lima.yaml`, `qemu.yaml`). |
| `validation.yaml` | Manifest for `./bloud validate`: tier commands + path→command inference + app registry. |
| `docs/` | All documentation. Index and "read for..." table: [`docs/README.md`](docs/README.md). Contains `specs/` (release plan, reconciler spec, app spec, dated review), `architecture/`, `guides/`, `features/`, `operations/` (tech-debt ledger), `plans/`. |
| root `package.json` | npm workspaces + turbo; husky pre-commit runs `npm run test:precommit`. |

Go modules are linked by `replace` directives (host-agent ↔ apps). CI: GitHub Actions
(`.github/`) and Forgejo (`.forgejo/`), Go 1.25 / Node 22.

## Toolchain & first-time setup

- Go 1.25 (host-agent, apps), Go 1.24 (cli), Node ≥18 (CI uses 22), npm 10.
- Host tools: `go`, `node`, `limactl`, `podman` (plus `qemu-system-x86_64` for the QEMU backend).
  `./bloud setup` (or `npm run setup`) selects the runtime backend (stored in
  gitignored `.bloud/preferences.yaml`), checks prerequisites, and rebuilds `./bloud`.
- Build the CLI: `cd cli && go build -o ../bloud .`

Development runs **inside a VM** on developer machines: macOS uses Lima (the
only applicable backend, chosen automatically), Linux uses QEMU (or `native`,
running host-agent directly on the host; needs podman + user-level systemd:
`NativeBackend.Create` enables `podman.socket` and linger as needed). The
backend preference is picked by `./bloud setup` (or prompted on first use of
any runtime command) and stored in gitignored `.bloud/preferences.yaml`;
`BLOUD_BACKEND` overrides it:

```bash
# Lima (macOS, default)
limactl create --name=bloud-dev dev/lima.yaml
limactl start bloud-dev

# QEMU (Linux, self-provisioning)
./bloud dev                           # creates .bloud/qemu/bloud-qemu (gitignored)
# manual SSH: ssh -p 2222 -i .bloud/qemu/bloud-qemu/id_ed25519 bloud@127.0.0.1

# Native (Linux CI): no VM; runtime in /var/tmp/bloud-native-runtime
BLOUD_BACKEND=native ./bloud dev
```

Opt-in development switches are environment variables named `BLOUD_DEV_<APP>_...`;
the CLI forwards the ones listed in `devPassthroughEnv` (`cli/devenv.go`) to the
host-agent on every backend, and each is off unless set. Today there is one:
`BLOUD_DEV_VAULTWARDEN_ALLOW_HTTP=1 ./bloud dev` lets the Vaultwarden web vault
work over Bloud's plain HTTP (it refuses any non-`https://` server otherwise;
localhost names only, see `apps/vaultwarden/INTEGRATION.md`).

`./bloud dev` is the whole loop: provisions the VM if needed, builds host-agent
(`CGO_ENABLED=0 GOOS=linux`) + frontend, deploys both into the VM, and runs
host-agent in the foreground (Ctrl-C stops it). **There is no hot reload:
re-run `./bloud dev` after any code change** (`./bloud rebuild` is a no-op; the
Nix runtime was removed).

Startup takes a minute or more: the host-agent brings every installed app up
(first convergence pass) before it reports ready. Port 3000 is open from the
start but serves a loading page (and `503 {"error":"starting"}` for `/api`, also
through Traefik on :8080) until that pass ends (invariant 5). `./bloud dev`
prints a progress line every ~15s and finally
`==> Bloud is ready: http://localhost:8080 ...`; the terminal then stays in the
foreground by design.

VM data lives in `/var/tmp/bloud-dev-runtime` (Lima), `/var/tmp/bloud-qemu-runtime`
(QEMU), or `/var/tmp/bloud-native-runtime` (native): `<dir>/host-agent` (binary + `web/build`), `<dir>/data` (BLOUD_DATA_DIR,
SQLite `bloud.db`, `secrets.json`), apps dir points at the repo's `apps/`.

### Ports (forwarded to host localhost)

| Port | What | Audience |
|---|---|---|
| **8080** | **Traefik: the user-facing port on the host.** Traefik's canonical entrypoint is `:80` inside the runtime; the dev VMs forward guest `:80` to host `:8080`, so browser/e2e journeys stay on `http://localhost:8080` (`jellyfin.localhost:8080`, `immich.localhost:8080`, …). A real deployment serves `:80` directly. | end users |
| **3000** | **host-agent internal API** (install/uninstall/status, session auth with loopback/trusted-net bypass). Operator/automation surface, not the user surface. | ops, CLI, e2e API helpers |
| 8096 | Jellyfin container (direct) | debugging |
| 9001 | Authentik server (direct) | debugging |
| 3389 | LDAP outpost (direct) | debugging |
| 2283 / 4533 | Immich / Navidrome (direct) | debugging |
| 3010 | AFFiNE (direct) | debugging |
| 8000 | Paperless-ngx (direct) | debugging |
| 8222 | Vaultwarden (direct) | debugging |

Inside the runtime Traefik also binds `:8080` (the `web-local` entrypoint), for
two reasons: app containers resolve `sso.localhost` to the guest and reach the
OIDC issuer there, and the native backend (which runs unprivileged and cannot
bind `:80`) sets `BLOUD_TRAEFIK_PORT=8080` and serves on that entrypoint alone.

An existing dev VM keeps its old port forwards and guest sysctls (both are
provisioning-time settings), so recreate it once after pulling this change:
`./bloud destroy && ./bloud dev`.

QEMU note: slirp NAT presents host-forwarded connections from the gateway
(10.0.2.2), so `./bloud dev` sets `BLOUD_TRUSTED_LOCAL_NETS=10.0.2.0/24` for the
host-agent; Lima forwards to loopback and needs none.

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
| `fast` (~30s) | `./bloud validate --tier fast` | host-agent go tests, orchestrator race tests, apps go tests, cli go tests, Go lint (golangci-lint cyclop complexity gate, `.golangci.yml`), Go formatting (gofmt), web vitest + svelte-check, license header check, prose lint (Vale), em dash check, docs link check |
| `changed` (default) | `./bloud validate` | `git diff` (default base `HEAD`; `--since <ref>`) → infer commands via `inference.paths` globs in validation.yaml; reports risk areas + affected apps; unmapped files drop confidence to "medium" |
| `integration` | `./bloud validate --tier integration` | Requires the VM: builds host-agent, frontend, and the integration test binary locally; deploys them to the guest's `/var/tmp/bloud-validate-runtime` behind a systemd user service (`bloud-validate-host-agent.service`) plus `init-secrets`; waits for API convergence; then runs the prebuilt test binary in the VM (the tests install Jellyfin through the real API) |

Flags: `--tier fast|changed|integration`, `--app <name>`, `--dry-run`, `--explain`,
`--json`, `--since <ref>`.

Run individual suites directly (from the repo root unless noted):

```bash
cd services/host-agent && go test ./...          # backend unit tests
cd services/host-agent && go test -race ./internal/engine/orchestrator/...
cd apps && go test ./...                          # configurator tests
cd cli && go test ./...
npm run lint:go                                 # golangci-lint v2 / cyclop (all three Go modules; pinned v2.13.2 via go run)
npm run check:gofmt                             # gofmt over every tracked *.go (the Go version CI installs stays authoritative)
npm run lint:prose                              # Vale: tracked *.md, *.go, *.ts, *.js, *.svelte, *.yml, *.yaml, *.css, *.html, *.sql
npm run check:no-emdash                         # em dashes anywhere in tracked files (covers what Vale cannot read)
npm run check:docs-links                        # relative links and their #anchors
npm run test --workspace=@bloud/host-agent-web    # vitest
npm run check --workspace=@bloud/host-agent-web   # svelte-check (typecheck)
cd e2e && npx playwright test                     # browser e2e (see below)
```

**Pre-commit hook (husky) runs `npm run test:precommit`** = license header check
+ Go lint (`npm run lint:go`) + Go formatting (`npm run check:gofmt`) + prose lint
(`npm run lint:prose`) + the em dash
and docs-link checks + host-agent
+ apps Go tests + web TS tests. Don't commit without it passing;
don't disable the hook.

License headers: every source file starts with a single
`SPDX-License-Identifier: AGPL-3.0-only` comment line (syntax per language;
`.svelte` files carry it inside `<script>`). No per-file copyright line:
a notice is informational, and a name there would claim sole authorship of
files contributors wrote; attribution comes from the git history, and
`LICENSE` stays the verbatim AGPL-3.0 text.
`npm run license:check` verifies, `npm run license:fix` stamps (and strips
retired per-file copyright lines). Excluded: JSON, go.mod/go.sum, docs,
binaries, `*.golden.yml` testdata, and the runtime-managed
`apps-routes.yml` (see `scripts/license-header.mjs`).

### Prose lint (`npm run lint:prose`)

Vale checks everything in the repository that is written for humans: every
tracked `*.md` file, plus the comments, YAML text, and markup inside `*.go`,
`*.ts`, `*.js`, `*.svelte`, `*.yml`/`*.yaml`, `*.css`, `*.html`, and `*.sql`.
It is pinned in
`package.json` and run through `go run`, so there is no install step;
`npm run lint:prose:sync` re-installs the pinned style package after a bump.

- `.vale.ini` is the rule manifest: the styles in use, the Google rules switched
  off (each with its reason), the rules pinned to `error`, and the source-file
  sections (only the house `Bloud` style, plus the vocabulary-backed
  `Vale.Avoid` guard, runs on code).
- `.vale/styles/config/vocabularies/Bloud/accept.txt` is deliberately small.
  `Vale.Spelling` -- the built-in dictionary spell-checker -- is disabled in
  `.vale.ini`: with no domain dictionary it flags every technical word and
  product name, forcing an unbounded allowlist treadmill. So a new word is
  normally *not* added to `accept.txt`. What the list keeps is the exact-case
  `Vale.Terms` set (`Forgejo`, `OAuth`, `PostgreSQL`, `Tailscale`), which
  enforces a product's canonical spelling, plus a couple of deliberate
  exemptions another rule would clobber (e.g. `break-glass` vs `Google.Jargon`).
  `reject.txt` is the opposite list: identifiers of removed components
  (`front-proxy`, `internal/mdns`, `compose.yml`, `.air.toml`, `front.service`).
  `.vale.ini` switches that rule off for whole files, not for individual lines:
  AGENTS.md, every archived plan, and the dated 2026-09-19 review, which are the
  record of the removals and therefore quote the retired names legitimately.
  Vale matches vocabulary entries against
  whole words, so an entry must start and end on a word character and must not
  contain a word that `accept.txt` already accepts.
- `.vale/styles/Bloud/` holds the house rules. `Bloud.EmDash` rejects em dashes;
  write a colon, semicolon, comma, or parentheses instead.
- Vale exits nonzero only on `error`-severity alerts, which is why a rule can
  gate CI only when its level is `error` (see the pinning note in `.vale.ini`).

Two things Vale cannot do, both covered by standalone checks:

- It cannot read string literals or `.mjs`, so `npm run check:no-emdash`
  (`scripts/no-emdash.mjs`) enforces the em dash ban over every tracked file,
  including the code fences inside docs.
- It cannot resolve links, so `npm run check:docs-links`
  (`scripts/docs-links.mjs`) checks that every relative link and its `#anchor`
  points at something that exists. Plans move between `plans/` and
  `plans/archive/`, which is what breaks these.

### Playwright e2e (`e2e/`)

- Browser tests target the **public port**: `BLOUD_URL` (default
  `http://localhost:8080`): user journeys go through Traefik.
- API helpers target the **internal port**: `BLOUD_API_URL` (default
  `http://localhost:3000`): loopback, no auth needed.
- Specs: `jellyfin.spec.ts` (LDAP SSO), `navidrome.spec.ts` (forward-auth),
  `immich.spec.ts` (native-oidc + onboarding), `affine.spec.ts`
  (native-oidc, login via issuer origin), `paperless-ngx.spec.ts` (native-oidc
  through django-allauth), `hermes.spec.ts` (native-oidc over the loopback
  issuer). Fixtures: `lib/fixtures.ts`
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
  `./bloud e2e app` (used by `.github/workflows/e2e-apps.yml` on the native
  backend) adds `BLOUD_E2E_APP` (required) and `BLOUD_E2E_PLAYWRIGHT_FILTER`.
- CI sizes the e2e runs to the change. The reusable
  `.github/workflows/e2e-affected.yml` runs `./bloud e2e affected`, whose logic
  reuses the `apps:` file globs and `e2e-project` in `validation.yaml`: an
  `apps/<name>/`-only push runs just that app's spec, a markdown-only push runs
  none, and any other change runs every app (and the Jellyfin lifecycle run).

## `./bloud` CLI reference

```
Setup:       setup                Select runtime backend, check prerequisites, build CLI
Dev (VM):    dev                  Build + deploy + run host-agent (Ctrl-C to stop)
            start                Show quick-start instructions
            stop | status | services | logs
            attach | shell [cmd] Shell / run command on the VM
            install <app> | uninstall <app>    via host-agent API (:3000)
            reset | destroy      Wipe VM data (keep VM) / delete VM
Validation:  validate [flags]     Tiered validation (default --tier changed)
            e2e                  Playwright against the running runtime
            e2e lifecycle        Self-contained install→restart→uninstall lifecycle
            e2e app              Single app's spec (BLOUD_E2E_APP=jellyfin|navidrome|
                                 immich|affine|install-streaming) on a
                                 self-contained runtime; used by CI
            e2e affected         Print the Playwright projects a change set needs
                                 (sizes the CI e2e matrix)
Other:       depgraph             Mermaid dependency graph from app metadata
```

The CLI resolves the project root from cwd using the `rootMarkers` list in
`cli/dev.go` (stable root-level files such as `validation.yaml` and
`AGENTS.md`), and loads a gitignored root `.env` (existing env vars win). Backend selection: `./bloud setup` stores the choice
in gitignored `.bloud/preferences.yaml` (macOS: `lima` automatically; Linux:
`qemu` | `native`, prompted if unset); `BLOUD_BACKEND=lima|qemu|native`
overrides the stored preference (CI relies on the override; `native` cannot be
combined with instance/SSH-target env vars). Instance overrides:
`BLOUD_E2E_LIMA_INSTANCE` (default `bloud-dev`), `BLOUD_QEMU_INSTANCE`
(default `bloud-qemu`).

## Architecture invariants (do not break)

1. **Orchestrator is the single writer.** All mutations flow through the typed
   intent queue (`internal/engine/orchestrator/intent.go`); the orchestrator is the only
   author of lifecycle status and the only executor of side effects. API handlers
   submit intents (202 accepted) and return current state; they must not write
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
   Bootstrap (system infra: Traefik + deps) converges **before the API is
   usable**: the listener opens at process start but serves a static loading
   page (and 503 for `/api`) until the orchestrator reports ready, so a browser
   hitting Traefik during bootstrap sees the page instead of a 502. The
   orchestrator manages user apps only.
6. **SSO strategies** are exactly: `native-oidc`, `ldap`, `forward-auth`, `none`
   (Immich + AFFiNE + Hermes + Paperless-ngx: native-oidc, Jellyfin: ldap,
   Navidrome: forward-auth). `none` means the app does not join the identity
   provider and carries its own credential end-to-end (no current catalog app).
   Native-oidc apps use Bloud's verified-email scope mapping (Authentik's
   managed one reports `email_verified: false`, which apps like AFFiNE
   reject). Native-oidc clients are confidential by default; a `sso.clientType:
   public` app is registered as a public PKCE client with no `client_secret`
   (the Hermes dashboard rejects a confidential client). A `sso.loopbackIssuer:
   true` app is served its issuer from the host loopback
   (`http://localhost:8080`) instead of `sso.localhost`, and runs with the host
   network namespace: its OIDC client accepts a plain http issuer only on a
   literal loopback hostname (the Hermes dashboard), so the container needs
   `localhost` to be Traefik. See
   `apps/hermes/INTEGRATION.md` and `apps/affine/INTEGRATION.md`.
7. **Routing is regenerated after convergence.** The orchestrator rewrites the
   Traefik dynamic config (`BLOUD_TRAEFIK_DYNAMIC_DIR/apps-routes.yml`) before
   promoting nodes to RUNNING. (Route generation must not accumulate runtime
   side effects; see tech debt.)
8. **Config precedence**: env var > `secrets.json` (auto-generated on first boot,
   or by `host-agent init-secrets`) > **error**: there is no hardcoded fallback. Key env:
   `BLOUD_DATA_DIR`, `BLOUD_APPS_DIR`, `BLOUD_TRAEFIK_DYNAMIC_DIR`,
   `BLOUD_PODMAN_SOCKET`, `BLOUD_PORT` (3000), `BLOUD_BASE_DOMAIN`,
   `BLOUD_TRAEFIK_PORT` (80; the dev VMs expose it on the host as 8080, and
   `native` sets 8080),
   `BLOUD_SSO_BASE_URL` / `BLOUD_SSO_AUTHENTIK_URL` / `BLOUD_SSO_ISSUER_URL`,
   `BLOUD_TRUSTED_LOCAL_NETS`.
9. **Hosts are a first-class setting.** The instance is reachable under a set
   of hostnames: built-ins `localhost` + `bloud.local`, plus admin-added
   custom domains (Settings → Hosts, `GET/PUT /api/settings/hosts`). One host
   is **primary** (drives the OIDC issuer + launch URLs). Admin-saved hosts
   win over `BLOUD_BASE_DOMAIN`/`BLOUD_SSO_BASE_URL`, which only seed the
   initial state. URL rules: `localhost` → `http://localhost:8080` (dev/e2e
   parity), any other host → `http://<host>` (port 80). Issuer:
   `http://sso.localhost:8080` for a localhost primary
   (containers resolve `sso.localhost` via `extraHosts`), `http://<primary>`
   otherwise (the orchestrator injects `<primary>:host-gateway` into
   native-oidc containers so the issuer resolves inside them). An app with
   `sso.loopbackIssuer` instead takes `http://localhost:<Traefik port>` and gets
   no `extraHosts` entry: it shares the host network namespace, where localhost
   is already the host. Host changes
   flow through the orchestrator (`SetHostsIntent`): persist, update the live
   `hostset.State`, reset SSO apps + `apps-authentik-server` so the lifecycle
   re-provisions Authentik (redirect URIs, outpost browser URL) and rewrites
   app configs, then re-ensure the dashboard OAuth app. Traefik routes stay
   domain-agnostic (`HostRegexp`), so they match every host without changes.
10. **Traefik owns port 80; reach-by-name services stay deferred.** Traefik's
    canonical entrypoint is `:80` (a real deployment serves it directly), so
    custom domains with real DNS reach the instance at `http://<host>`. The dev
    VMs map guest `:80` to host `:8080` (dev/e2e parity), and Traefik also binds
    a `:8080` convenience entrypoint (`web-local`) that app containers use to
    resolve `sso.localhost` for OIDC discovery, and that the unprivileged native
    backend runs on alone. `hostset`'s `http://<host>` (port 80) mapping for
    non-localhost hosts is therefore real, not aspirational. Bloud still ships
    no `.local` mDNS announcer (`internal/mdns`) or root front proxy
    (`front-proxy` subcommand + `bloud-front.service`): both were removed
    because the announcer couldn't cross the dev VM, fought the host's own
    responder, and served only http/LAN. A TLS story (real-domain Let's Encrypt
    on Traefik, and/or Tailscale Serve) remains the planned follow-up.
11. **Frontend is a static build** served by host-agent from
    `<host-agent-dir>/web/build` (embedded `dev_dashboard.html` is only the
    missing-build fallback). Rebuild the frontend before deploying.
12. **Managed containers are labeled** `io.bloud.managed=true` and
    `io.bloud.app=<name>`; container names follow `apps-<name>` /
    `apps-<name>-<component>`. e2e assertions rely on these labels.
13. **A new `services/<name>` requires shipping to a machine where host-agent
    does not run.** Deploy location (not code concern) is what earns a
    directory under `services/`. SSO, orchestrator, API, and store run on the
    same box as host-agent → they stay packages under `internal/` or
    subcommands of the host-agent binary. A remote tailnet outpost or control
    plane (a different machine) earns its own `services/<name>/` module when
    it gets built.
14. **Wire contracts stay stdlib-only.** Token formats, gateway protocol
    constants, and share-envelope types must not import
    `store`/`config`/host-agent-internal machinery, so a future extraction to
    a shared `pkg/` or module is a move, not a surgery.
    `internal/sharing/token.go` (pure stdlib) is the exemplar.

## host-agent HTTP API (port 3000)

- Public: `GET /health`, `GET /auth/login`, `GET /auth/callback`,
  `POST /auth/logout`, `GET /api/health`, `GET /api/setup/status`,
  `GET /api/auth/me`, plus the public system-info router.
- Until the first convergence pass finishes, `/api/*` (health included) answers
  503 (`bootstrapGate`, `internal/api/loading.go`). So a 200 from
  `GET /api/health` means "ready", not merely "listening"; poll it for readiness
  (`./bloud dev` does).
- Authenticated (session cookie, or loopback/`BLOUD_TRUSTED_LOCAL_NETS` bypass):
  `GET /api/apps` (catalog), `GET /api/apps/installed`,
  `GET /api/apps/{name}/metadata`, `POST /api/apps/{name}/install`,
  `POST /api/apps/{name}/uninstall`, `PATCH /api/apps/{name}/rename`,
  home + logs routers.
- Admin: `POST /api/apps/refresh-catalog`, `GET /api/system/rebuild/stream`,
  settings (incl. `GET/PUT /api/settings/hosts`: the multi-host setting),
  sharing, remote-apps routers.

## Adding an app

1. `apps/<name>/metadata.yaml`. Full field reference in
   `services/host-agent/internal/catalog/models.go` (source of truth):
   `name`, `displayName`, `description`, `category` (media | productivity |
   security | infrastructure), `port`, `isSystem`, `sso`
   (`strategy`, `callbackPath`, `userCreation`, `bypassPaths`, `env` mappings),
   `integrations` (`proxy` / `sso` / `database`: `{required, multi,
   compatible: [{app, default}]}`), `containers[]`
   (`name`, `image` (**pin versions**), `command`, `network`/`networks`,
   `restartPolicy`, `environment`, `extraHosts`, `ports`, `volumes`,
   `dependsOn`, `healthCheck {test, interval, timeout, retries}`).
   Template vars in environment/volumes: `{{appDataDir}}`, `{{dataDir}}`, and
   `{{postgresPassword}}` (a per-app PostgreSQL password for apps that bundle
   their own postgres). There is no per-app admin-password template var; per-app
   admin credentials are generated by the secrets provider on demand
   (`GenerateAppAdminPassword`) and delivered through a file the configurator
   writes, never through the container-spec template.
2. `apps/<name>/configurator.go`. Implements `NodeLifecycle`
   (`Name()`, `PreStart(ctx, *AppState) (changed bool, err error)`,
   `PostStart(ctx, *AppState) error`) from `pkg/configurator`. Teardown is
   optional: the orchestrator removes containers and data itself, and calls the
   `configurator.Remover` method (`Remove(ctx, *AppState, clearData bool)
   error`) only when a configurator implements it. `AppState` carries `DataPath`,
   `BloudDataPath`, `SSOEnabled`, typed `LDAP` / `OIDC` outputs. Self-register
   it: add `apps/<name>/registration.go` with an `init()` calling
   `configurator.MustRegisterFactory("<node-name>", ...)` (factory is
   instantiated lazily on first lookup), and add the blank import and node name
   to `apps/registry.go`; `TestRegisterAll` fails if the two drift apart.
   Host-agent's `internal/appconfig/register.go` is for system apps only
   (Traefik, Authentik, registered as lazy factories in `RegisterSystem`); user
   apps never touch it.
3. Tests: unit tests in the app package; integration assertions in
   `services/host-agent/internal/e2e/e2e_test.go` (build tag `integration`);
   user-journey spec in `e2e/tests/`. **Test behavioral outcomes** (verify via
   the app's own API), not config values.
4. Add the app to `validation.yaml` (`apps:` block: auth strategy,
   validation-level, file globs, optional `e2e-project`).
5. Reference patterns: `apps/jellyfin` (LDAP, setup wizard, plugins),
   `apps/authentik` (multi-container, LDAP infra), `apps/immich` (own
   postgres+redis, native-oidc), `apps/affine` (own postgres+redis, OIDC
   config file, first-run owner bootstrap), `apps/navidrome` (forward-auth),
   `apps/homeassistant` (pinned remote asset via `pkg/appasset` + provenance,
   YAML marker merge, `RestartContainer`-driven config reload),
   `apps/paperless-ngx` (own postgres+redis plus gotenberg/tika sidecars,
   generated dotenv config file for django-allauth OIDC, internal admin
   bootstrap), `apps/vaultwarden` (single container, generated dotenv file for
   built-in OIDC with `sso.scopes`/`sso.accessTokenMinutes`, `SSO_ONLY`, an
   opt-in plain-HTTP dev switch; see its `INTEGRATION.md`).

## Integration validation runs the real dependency-graph path

The integration tier (`./bloud validate --tier integration`) and the Go tests
in `services/host-agent/internal/e2e/` provision their runtime the same way a
real install works: deploy host-agent + catalog into the VM as a systemd user
service, install Jellyfin through `POST /api/apps/jellyfin/install`, let the
orchestrator converge, then run the behavioral tests inside the VM.

The old `dev/compose.yml` static stack (shared postgres/redis/authentik/jellyfin)
was retired in 2026-08: it bypassed the catalog planner, the orchestrator
intent queue, and the `io.bloud.managed` container labels, so integration
tests could pass while the real install/reconcile flow was broken, and fail
for reasons the product path never hits (shared postgres vs per-app
postgres, compose service naming, no graph ordering).

`./bloud e2e lifecycle` is the full user-visible version of the same path
(install → browser journeys → service restart → reinstall → uninstall and
cleanup assertions). The Playwright suite (`e2e/tests/*.spec.ts`) remains the
mandatory regression gate for changes to install/reconcile behavior.

## Known debt (re-verified 2026-09-19)

The ledger is the source of truth; the notes below are a pointer, not a
mirror. Full backend-debt ledger with the repayment plan:
[`docs/operations/tech-debt.md`](docs/operations/tech-debt.md). Top open
items: the **auth bypass is remotely forgeable**, not just a local-process
concern (`middleware.RealIP` + Traefik `forwardedHeaders.insecure: true` +
loopback=admin), so a single `True-Client-IP: 127.0.0.1` header grants admin.
That is the shipping blocker; the engine's silent-failure paths (catalog
nil-deref, lock-free `MemoryCache`, an intent queue that can exit permanently);
`appclient.Call.Timeout` being a no-op; container
drift never repaired while the process is alive; duplicated orchestrator wiring
(CLI vs router). Recently paid: durable lifecycle operation state
(`store/operations.go` + orchestrator recorder, plan archived), versioned schema
migrations, route-generation purity (PR #88), and per-connection SQLite pragmas
moved into the DSN (PR 7). Two earlier claims are
corrected in the ledger: the `user_app_positions` fork fix is a no-op (the grid
shape already existed), and the derived OAuth client secret *is* currently
persisted. Review findings:
[`docs/specs/review.md`](docs/specs/review.md) and the newer
[`docs/specs/review-2026-09-19.md`](docs/specs/review-2026-09-19.md) (e.g. §C2
in-memory `MapRepository`, which the 2026-09-16 re-audit reframes:
HKDF-derived credentials make restart reconstruction work, so only
ERROR-terminal semantics is lost. §C1's inert install path is fixed: the router
wires the catalog graph). Highlights:

- Sharing/guest API handlers write stores directly: a deliberate, documented
  boundary (pure store writes, synchronous invite tokens), not intent-queue drift.
- ~~Config ships hardcoded fallback secrets~~ **Fixed 2026-09-14**: `config.Load`
  is fallible with no static fallback (env > `secrets.json` > error). Still open:
  one literal fallback survives in `sso.DeriveSecret`, and loopback requests are
  granted admin, and that rule is forgeable from any client (see above).
- Keep the `apps:` registry in `validation.yaml` in sync with `apps/` when apps
  are added/removed (the changed tier infers affected apps from it).

## Environment conventions (by design)

- `dev/lima.yaml` hardcodes the repo mount at `~/Projects/bloud` (Lima reads
  the yaml verbatim); adjust if the checkout lives elsewhere. The QEMU backend
  auto-detects the checkout dir; `dev/qemu.yaml` documents the spec only.
- The CLI loads a gitignored root `.env` before dispatching commands (existing
  env vars win).

## Docs map (read for…)

`docs/` is the documentation tree; [`docs/README.md`](docs/README.md) is the
canonical index. The routes below are mirrored here so the guide links straight
to the right doc. When a doc moves, update it in both places.

| Question | Read |
|---|---|
| What are we building / release plan | [specs/spec.md](docs/specs/spec.md) |
| Orchestrator/reconciler design | [specs/reconciler-spec.md](docs/specs/reconciler-spec.md) |
| Component overview + data flows | [architecture/overview.md](docs/architecture/overview.md) |
| How to add an app | [guides/contributing-apps.md](docs/guides/contributing-apps.md) |
| Multi-container app model | [specs/app-spec.md](docs/specs/app-spec.md) |
|Backend debt + repayment plan|[operations/tech-debt.md](docs/operations/tech-debt.md)|
|Build the .deb release package|[operations/packaging.md](docs/operations/packaging.md)|
|Sharing/federation (in progress)|[features/sharing.md](docs/features/sharing.md)|
|Dashboard grid + widgets|[features/dashboard.md](docs/features/dashboard.md)|
| Dated review findings|[specs/review.md](docs/specs/review.md)|
| Latest architecture/code review (2026-09-19)|[specs/review-2026-09-19.md](docs/specs/review-2026-09-19.md)|
| In-flight designs | [plans/](docs/plans/) |
