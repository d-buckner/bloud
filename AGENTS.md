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
| root `package.json` | npm workspaces + turbo; husky `pre-commit` runs staged-file hygiene, `pre-push` runs lint + tests (see "Validation & testing"). |

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
`BLOUD_BACKEND` overrides it.

> ### Never run the `native` backend on Fedora
>
> **`native` is forbidden on Fedora. Use `qemu`.** It runs host-agent
> directly on the machine you invoked it from: systemd user units, real port
> bindings, and containers on your own rootless podman, with no VM boundary.
> A startup that fails partway leaves Bloud containers and systemd units
> running against your real host, where nothing about them is disposable.
> CI runs `native` only on a throwaway runner, which is the sole place that
> trade-off is acceptable.
>
> The CLI enforces this, not just this document. On a Fedora host (detected
> from `/etc/os-release` `ID` or `ID_LIKE`, so derivatives such as Bazzite
> are covered too) `native` is dropped from the offered backends, a stored
> `native` preference is discarded as inapplicable, and an explicit
> `BLOUD_BACKEND=native` is refused with a nonzero exit rather than run. See
> `cli/distro.go`. Do not work around that guard, and do not run the
> host-agent binary by hand on a Fedora workstation to get the same effect.

On a Fedora machine the choice is already made: `qemu` is the only backend
the CLI offers.

```bash
# Lima (macOS, default)
limactl create --name=bloud-dev dev/lima.yaml
limactl start bloud-dev

# QEMU (Linux, self-provisioning)
./bloud dev                           # creates .bloud/qemu/bloud-qemu (gitignored)
# manual SSH: ssh -p 2222 -i .bloud/qemu/bloud-qemu/id_ed25519 bloud@127.0.0.1

# Native (Linux CI ONLY, never on a developer workstation, and refused on
# Fedora): no VM; runtime in /var/tmp/bloud-native-runtime
BLOUD_BACKEND=native ./bloud dev
```

Opt-in development switches are environment variables named `BLOUD_DEV_<APP>_...`;
the CLI forwards the ones listed in `devPassthroughEnv` (`cli/devenv.go`) to the
host-agent on every backend, and each is off unless set. Today there is one:
`BLOUD_DEV_VAULTWARDEN_ALLOW_HTTP=1 ./bloud dev` lets the Vaultwarden web vault
work over Bloud's plain HTTP (it refuses any non-`https://` server otherwise;
localhost names only, see `apps/vaultwarden/INTEGRATION.md`).

`./bloud dev` is the hot-reload loop on the native backend: it builds host-agent
(`CGO_ENABLED=0 GOOS=linux`), starts a vite dev server for the dashboard, and
restarts only the host-agent process when a watched file changes. Containers are
never touched by a reload, so the next reconciliation simply runs against the
new binary. The frontend hot-reloads through vite; the browser stays on the
Traefik origin, which is what keeps the OIDC round trip real.

Killing the dev loop takes the dev servers with it. Each child (vite and
host-agent) starts in its own process group and the loop signals the group, not
just the direct child: `npm run dev` runs a shell that runs vite, and npm
exits on `SIGTERM` without forwarding it, so a signal sent only to the child
leaked the `node` process holding 5173 and the next run could not bind it. A second
`Ctrl-C` (or a second `SIGTERM`) forces both groups down with `SIGKILL`
instead of waiting out the grace period. Containers are still left alone:
stopping the dev loop never stops an app.

Watched: `services/host-agent/**/*.go` (except `_test.go`) and
`apps/**/{*.go,metadata.yaml}`. A failed build leaves the running host-agent
alone, so a typo never takes the dashboard down.

Two dev-only switches make the reload fast, both set by `./bloud dev` and both
documented at their definition:

- `BLOUD_DEV_VITE_URL` (host-agent) proxies the dashboard to vite instead of
  the static build. See `internal/api/dev_vite_proxy.go`.
- `BLOUD_DEV_FAST_GATE` (host-agent) opens the API as soon as the system
  containers are already running, with the first convergence pass finishing in
  the background. This is a deliberate, opt-in relaxation of invariant 5 for
  the reload case only. See `cmd/host-agent/dev_gate.go`.

Measured on a warm native stack: a backend reload is about 3s to a live API
(1-2s incremental build plus a process restart), against ~160s for a cold
start. The frontend reload is vite's own sub-second HMR.

`--no-watch` runs the old one-shot loop: build, deploy, run in the foreground.
Hot reload is wired for the native backend only; on Lima and QEMU `./bloud dev`
says so and falls back to the one-shot loop, because the same loop over SSH
needs remote process supervision and a file copy per reload.

A cold start still takes a minute or more: the host-agent brings every installed
app up (first convergence pass) before it reports ready. Port 3000 is open from
the start but serves a loading page (and `503 {"error":"starting"}` for `/api`,
also through Traefik on :8080) until that pass ends (invariant 5). `./bloud dev`
shows a quiet console (see "Dev console output") and the terminal then stays in
the foreground by design.

### Dev console output

`./bloud dev` streams four producers into one terminal: the CLI's own
orchestration, the Go build, the dashboard's vite dev server, and the
host-agent's structured log (300+ call sites, one JSON object per line).
Piping all of it through unreadable is the whole problem, so the console
carries decisions and the log file carries evidence (`cli/dev_output.go`):

- Bring-up is one aligned line per step with its duration, then the ready
  line with the UI URL.
- A reload is **one line**: trigger, build time, and the fact that no
  container was disturbed. A failed build prints its compiler output as an
  indented tail and leaves the running host-agent up.
- Every raw byte from every subprocess is mirrored to
  `.bloud/logs/dev.log` (rotated to a single `.1` past 5 MB), whose path is
  printed once at bring-up.
- The quiet console still surfaces the host-agent's `WARN`/`ERROR` records
  and vite's error lines, reformatted as `HH:MM:SS  LEVEL  msg  k=v`.
- `--verbose` / `-v` / `BLOUD_DEV_VERBOSE=1` streams the raw output too.
- Colors are off when stdout is not a TTY (`NO_COLOR` and `TERM=dumb`
  respected), so a redirected console stays plain text.

While the CLI is *deliberately* stopping the host-agent, its stream is muted
on the console (`devConsole.SuppressAgentLog`). Tearing the process down out
from under its own orchestrator produces a burst of "database is closed"
warnings on every reload; they stay in the log. Warnings from a host-agent
that is actually running, including one that fails to come back up, still
show.

VM data lives in `/var/tmp/bloud-dev-runtime` (Lima), `/var/tmp/bloud-qemu-runtime`
(QEMU), or `/var/tmp/bloud-native-runtime` (native): `<dir>/host-agent` (binary +
`web/build`), `<dir>/data` (BLOUD_DATA_DIR: SQLite `bloud.db`, `secrets.json`,
and `apps/<app>/` holding every app's private tree). The catalog is a different
directory that happens to share the name: the apps dir points at the repo's
`apps/`, which is read-only source, never app state.

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
| `fast` (~30s) | `./bloud validate --tier fast` | host-agent go tests, orchestrator race tests, apps go tests, cli go tests, Go lint (golangci-lint cyclop complexity gate + nolintlint, `.golangci.yml`), Go formatting (gofmt), web vitest + svelte-check, license header check, image pin check, file length ratchet, prose lint (Vale), em dash check, docs link check, generated-doc check (`depgraph --check` + `catalogdoc --check`) |
| `changed` (default) | `./bloud validate` | `git diff` (default base `HEAD`; `--since <ref>`) → infer commands via `inference.paths` globs in validation.yaml; reports risk areas + affected apps; unmapped files drop confidence to "medium" |
| `integration` | `./bloud validate --tier integration` | Requires the VM: builds host-agent and the integration test binary locally (no frontend build, see below); deploys them plus the app catalog to the guest's `/var/tmp/bloud-validate-runtime` behind a systemd user service (`bloud-validate-host-agent.service`) with `init-secrets`; waits for API convergence; then runs the prebuilt test binary in the VM (the tests install Jellyfin through the real API) |

Flags: `--tier fast|changed|integration`, `--app <name>`, `--dry-run`, `--explain`,
`--json`, `--since <ref>`, `--verbose` / `-v` (stream each command's raw output;
`BLOUD_VALIDATE_VERBOSE=1` does the same). The console is quiet by default: one
line per command plus a pass/fail summary, with the full per-command output
dumped only for failures and mirrored to `.bloud/logs/validate-<tier>.log`.
The integration tier uses the same split for its bring-up phases (provision,
preflight, build, deploy, start, wait-for-convergence) as for the test command
itself, so a 10-minute run that goes well is ten lines.

The integration tier builds no frontend. The fast tier's `web-build` command
already gates the production bundle on every push, and no integration test
reads the dashboard, so rebuilding the same artifact here would prove the same
thing twice and add a minute of vite chunk listing to the log. The validation
runtime is deployed without `web/build`, so host-agent serves its documented
missing-build fallback page (invariant 11), and the CI job that runs the tier
installs no Node.js at all.

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
npm run check:image-pins                        # every container image names a specific version (see "Image pins" below)
npm run check:file-length                       # no source file grows past its recorded ceiling (see "File length" below)
npm run check:file-length:update                # lower the ratchet baseline after a split (it cannot raise one)
npm run test --workspace=@bloud/host-agent-web    # vitest
npm run check --workspace=@bloud/host-agent-web   # svelte-check (typecheck)
cd e2e && npx playwright test                     # browser e2e (see below)
```

**Hooks (husky) split by event, not by importance.** A commit happens hundreds of
times a week and a push once per branch of work, so the cheap tier is charged at
the frequent event and the expensive tier at the rare one. Putting the test
suites in `pre-commit` charged the expensive tier at the frequent one: fixing a
markdown typo ran three golangci-lint passes, eslint and both test suites.

| Hook | What it runs | Cost |
|---|---|---|
| `pre-commit` | license header, gofmt, em dash, prose, doc links, file length, over the **staged files only** | ~1-2s |
| `pre-push` | the same hygiene over the **pushed range**, plus Go lint, the host-agent + apps Go tests, eslint and vitest, each triggered only when its area is in the range | ~5-25s |

Both tiers run their checks concurrently (`scripts/checks.mjs`), so the wall
clock is the slowest check rather than the sum. Scope narrows only where the
failure lives in the files you touched: `check:docs-links`, `check:image-pins`
and `check:file-length` scan the whole tree on purpose, because a relative link
is broken by the file you *deleted*, an image-pin exception table goes stale
when an app is removed rather than edited, and the file-length baseline has to
be reconciled against every governed file or a deleted exempt file leaves a
stale ceiling behind. All three cost well under a second.

Neither hook is the gate. CI runs the full `./bloud validate --tier fast` plus
integration and e2e on every push, so `git commit --no-verify` costs you a few
seconds of feedback, not correctness. That is deliberate: a hook nobody can
bypass is a hook people disable globally, and then the hygiene layer is gone for
everyone.

By hand: `npm run test:precommit` runs every check over the whole tree (the
shape CI mirrors); `npm run checks:commit` and `npm run checks:push` run the two
tiers against the staged files and the pushed range. `BLOUD_CHECK_JOBS=n` caps
concurrency. The per-file checks also take a list directly:
`npm run check:gofmt -- apps/jellyfin/configurator.go`.

Image pins: every container image Bloud runs must name a specific version
(`npm run check:image-pins`, `scripts/pinned-images.mjs`). A rolling tag is
republished upstream, so `:release` today and `:release` next month are different
bytes: a crash stops being reproducible, a bump stops being reviewable, and one bad
upstream push lands on every install at once. Pinned means a version tag in any
registry shape (`1.2.3`, `v3.4`, `7-alpine`, `pg16`, `2.6.5.5623-ls161`) or an
`@sha256:` digest. It rejects no tag at all (the runtime reads that as `:latest`),
`:latest`, and the rolling channels (`stable`, `release`, `staging`, `edge`,
`nightly`, `main`, `dev`, `canary`, `beta`, and friends, including prefixed
variants like `release-cuda`). The check reads `containers[].image` in
`apps/*/metadata.yaml` and registry-qualified image literals in non-test Go under
`apps/` and `services/`. Exceptions are declared in the `EXCEPTIONS` table in that
script with a reason; the check prints them on every run so a floating tag is never
invisible, and it fails an exception that matches nothing, so the list cannot rot.
The one live exception is the Tailscale sidecar: a pinned Tailscale client drifts
from its coordination server and its peers, and that failure shows up as broken
transport rather than as a version mismatch, so it tracks `:stable` on purpose.

File length: no source file may grow past 500 code lines, where a code line is a
non-blank line that is not entirely a comment (`npm run check:file-length`,
`scripts/file-length.mjs`). It governs Go, TypeScript, and Svelte; Go tests are
exempt for the reason already recorded in `.golangci.yml` for `funlen`, and
Markdown is out because a long spec is not the same problem as a long module.

It is a ratchet, not a cap. The nine files already over 500 are recorded in the
`BASELINE` block with their current size as a ceiling they may not exceed, so the
check is green now and gets stricter on its own every time someone splits a file.
A plain cap would have been red for reasons nobody introduced, which makes it safe
to ignore. Four things fail: a new file over 500 with no exemption; an exempt file
grown past its recorded ceiling; a baseline entry for a file that no longer
exists; and a baseline entry looser than the file it guards, because that leaves
headroom for the file to grow back. `npm run check:file-length:update` fixes the
last two and can only ever lower a number or drop an entry. It cannot add one, so
a new exemption has to be typed into the block by hand and shows up in the diff.
This is the same shape as the `EXCEPTIONS` table above and the `internal/wire`
completeness test: the exemption is data, it is reviewed, and an exemption that
guards nothing is itself a failure.

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

`npm run lint:prose` (`scripts/prose-lint.mjs`) takes an optional file list, so
the hooks lint only what changed: `npm run lint:prose -- docs/plans/x.md`. With
no arguments it checks every tracked prose file. The Vale style package is not
committed, so the first run provisions it with `vale sync`.

Gotestsum is a pinned `tool` dependency in `services/host-agent/go.mod` and
`apps/go.mod`, not a `@latest` argument. `go run pkg@latest` resolves over the
network on every invocation and fails outright with `GOPROXY=off`, and an
upstream release makes the first commit after it pay a re-download and rebuild.
The `tool` directive pins the version in `go.mod` and resolves from the module
cache, so `go run gotest.tools/gotestsum` works offline.

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
  `apps/<name>/`-only change runs just that app's spec, a markdown-only change
  runs none, and any other change runs every app (and the Jellyfin lifecycle
  run). On a pull request the change set is the whole PR (`--base origin/main`,
  diffed from the merge base), not just the latest push; on `main` it is the
  pushed range (`--since`).

## `./bloud` CLI reference

```
Setup:       setup                Select runtime backend, check prerequisites, build CLI
Dev (VM):    dev [--reset]       Build + deploy + run host-agent (Ctrl-C to stop)
                                 --reset wipes the runtime first (same as reset -y, no prompt)
                                 --verbose / -v streams raw subprocess output
                                 (all of it is mirrored to .bloud/logs/dev.log)
            start                Show quick-start instructions
            stop | status | services | logs
            attach | shell [cmd] Shell / run command on the VM
            install <app> | uninstall <app>    via host-agent API (:3000)
            reset [-y]           Wipe VM data (keep VM); -y skips the prompt
            destroy              Delete the VM (prompts)
Validation:  validate [flags]     Tiered validation (default --tier changed)
            e2e                  Playwright against the running runtime
            e2e lifecycle        Self-contained install→restart→uninstall lifecycle
            e2e app              Single app's spec (BLOUD_E2E_APP=jellyfin|navidrome|
                                 immich|affine|install-streaming) on a
                                 self-contained runtime; used by CI
            e2e affected         Print the Playwright projects a change set needs
                                 (sizes the CI e2e matrix)
Other:       depgraph             Full dependency graph from app metadata
                                 (no flag: print the Mermaid form to stdout)
            depgraph --write     Refresh the generated block in the target doc
                                 (default docs/architecture/dependency-graph.md)
            depgraph --check     Exit 1 when that doc is not what the catalog
                                 produces (the fast-tier gate; the
                                 merge-to-main job commits the refresh)
            depgraph --json      The whole catalog as the developer-graph JSON
                                 (nodes + edges) the browser renderer consumes
            depgraph --target F  File to write or check
            catalogdoc           README's catalog list + one-login table,
                                 generated from apps/*/metadata.yaml
                                 (no flag: print both generated blocks)
            catalogdoc --write   Replace both generated blocks in README.md
            catalogdoc --check   Exit 1 when either block is not what the
                                 catalog produces (the fast-tier gate)
            catalogdoc --target F File to write or check
```

The README's graph is an image, not a text diagram: `docs/assets/dependency-graph.png`,
rendered in a headless browser from the `--json` snapshot by `scripts/render-graph.mjs`,
using the dashboard's own graph components (`services/host-agent/web/src/routes/graph/`).
`npm run graph:image` rebuilds it locally (it needs Playwright's Chromium). The text form of
the graph is what `depgraph --write` / `--check` govern.

The README's `## catalog` list and `## one login` table are generated the same way, from the
same `apps/*/metadata.yaml`, by `catalogdoc`. Each block sits between HTML comment markers
and everything outside them is hand-written prose, so the voice of the section is not
generated and the entries are. Neither block carries a count of how many apps there are: a
count is one more thing to keep in sync, and the list underneath it answers the question.

The login table has one override, the `loginQuirks` table in `cli/catalogdoc.go`, for the
case where the strategy an app declares at the ingress is not the login a reader would
perform. Seerr declares `none` because it cannot delegate authentication, but its users sign
in with their Jellyfin account, which is LDAP-backed through Authentik, so it renders under
**LDAP** rather than under "App-local accounts". It is an override in the generator rather
than a new value in the strategy enum because invariant 6 is a runtime contract and Seerr's
`none` is correct for it; only the README's grouping was wrong. It is data, the same shape
as the `EXCEPTIONS` table in `scripts/pinned-images.mjs`: every entry carries its reason,
and `checkLoginQuirks` fails an entry that names an app that is gone, targets a system app,
duplicates another entry, points at a strategy with no README label, or agrees with what the
app already declares and so does nothing. An override that guards nothing is itself a
failure, so the table cannot quietly keep asserting something the catalog stopped being true
of. The overrides are also printed on every `--write` / `--check` run.

The `generated-docs` workflow regenerates all of it on merge to `main`, scoped by path to
what the artifacts depend on: `apps/**/metadata.yaml`, the generators (`cli/depgraph.go`,
`cli/catalogdoc.go`, `cli/genblock.go`), the graph components
(`web/src/routes/graph/`, `web/src/lib/graph/`, `graphLayout.ts`, `statusColor.ts`),
`scripts/render-graph.mjs`, and the workflow itself. A merge that touches none of those
leaves the committed artifacts untouched.

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
   can't run twice is a bug. This is also what makes the periodic self-healing
   pass safe: it re-runs the same cycle on a timer (~60s, see invariant 8), so
   a pass that finds nothing to change must change nothing. `PreStart` reports
   `RestartNeeded` rather than restarting on its own, and `managedfile.Write`
   reports `changed=false` when the bytes already match, for exactly this
   reason.
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
   page (and 503 for `/api` and `/health`) until the orchestrator reports
   ready, so a browser hitting Traefik during bootstrap sees the page instead
   of a 502. The
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
   (`http://localhost:8080`) instead of `sso.localhost` **only while the
   deployment is plain http**, and runs with the host network namespace: its
   OIDC client accepts a plain http issuer only on a literal loopback
   hostname (the Hermes dashboard), so the container needs `localhost` to be
   Traefik. Under a **https** public URL it gets the public issuer like every
   other native-oidc app: the provider accepts https anywhere, and the issuer
   string is where the browser is redirected, so `localhost` there would be
   the visitor's own machine. See
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
   `BLOUD_TRUSTED_LOCAL_NETS` (host-agent admin position),
   `BLOUD_TRUSTED_PROXY_NETS` (Traefik forwarded headers, see invariant 10),
   `BLOUD_PUBLIC_SCHEME` (deployment-wide `http`|`https` for derived URLs; it
   only fills a scheme the address itself did not state), and
   `BLOUD_RECONCILE_INTERVAL` (how long the instance may go without a
   convergence pass before the self-healing timer submits one; Go duration
   syntax, default 60s, `off` disables. The timer is idle-based: any pass
   resets it, so the value is a floor on the gap between passes rather than a
   cadence. See `services/host-agent/internal/engine/orchestrator/selfheal.go`).
9. **The address is a first-class setting, and it is one URL.** The instance
   has exactly one configured address, the **public URL**, typed as a bare
   origin in Settings → Address (`GET/PUT /api/settings/public-url`):
   `https://bloud.example.com:8443`. The scheme, the host, and the port all
   live in that string, and the port belongs to the proxy: it is the port the
   public entrypoint is dialed on from outside, not anything Bloud binds
   internally. There is no host list, no primary selection, and no per-host
   scheme; `MaxHosts`, `SetHostsIntent`, and the `hosts` table are gone
   (schema migration 9 collapses the old primary row into
   `settings['public_url']`).

   The built-ins `localhost` and `bloud.local` are **not** configurable and
   are not a second knob. They stay reachable because Traefik routes are
   domain-agnostic (`HostRegexp`), they render on a fixed plain-http mapping
   (`http://localhost:8080` by the dev/e2e convention, `http://bloud.local`
   on 80), and they appear in the derived redirect-URI list so local access
   survives a public domain. The UI shows them read-only under "Also
   reachable"; nothing there is editable.

   Resolution precedence (`hostset.Resolve`), most specific first: the stored
   public URL > `BLOUD_SSO_BASE_URL` > `BLOUD_BASE_DOMAIN` >
   `DefaultPublicURL` (`http://localhost:8080`). `BLOUD_PUBLIC_SCHEME` fills
   in a scheme **only** when the winning source did not state one, so it
   applies to a bare `BLOUD_BASE_DOMAIN` and never rewrites a stored origin
   or a full `BLOUD_SSO_BASE_URL`. It is rejected outright for an IP literal:
   no CA issues a certificate for a bare address, so `https://<ip>` is an
   origin nothing can complete a handshake against. `ParsePublicURL` is the
   single gate on what may become the address: it rejects a path, a query, a
   fragment, credentials, an unknown scheme, and an out-of-range port, because
   a URL that is only partly understood registers a redirect URI that never matches what
   the browser sends.

   **An address is not a name.** `ValidHostname` accepts a dotted quad (a run
   of valid RFC 1123 labels), so an IP literal can enter through first-run
   adoption or Settings, and `hostset.IsAddress` is what tells the two
   families apart. A **name** gets its port from its scheme, because DNS is
   what makes the origin: `https://<host>` means 443 and something answers
   there. An **address** has no such contract and no certificate story, so the
   detected LAN entries in `AllBaseURLs()` are always plain http on the
   **served port** (`BLOUD_TRAEFIK_PORT`, carried as `Input.ServedPort`;
   `0`/`80` render as the http default). The public URL itself is different
   again: it carries the operator's own port verbatim, which is why adopting
   the origin an install was created from keeps `:8443` instead of collapsing
   to 80. The served port must survive every rebuild of the set
   (`WithServedPort`, `applySetPublicURLIntent`), or a change drops it and the
   LAN URLs are back on port 80. Pinned by `internal/hostset/resolve_test.go`,
   `TestAuthModule_LANIPLoginStaysPlainHTTPUnderAnHTTPSPublicScheme`, and
   `TestAuthModule_AddressPrimaryHostKeepsTheEntrypointPort`.

   **The agent's own port is never a public address.** First-run adoption
   reads the request's `Host` so a browser on `:8443` gets a redirect URI for
   `:8443`, but a first admin created through the loopback API arrives as
   `Host: localhost:3000`, which says nothing about public reachability.
   Adopting it stores an origin that every later OAuth redirect is refused on,
   because login on the agent bind port is exactly what
   `isDirectAgentRequest` rejects, so the install cannot log itself in. The
   adoption guard compares the observed port to the agent's own and keeps the
   existing address when they match. Pinned by
   `TestCreateFirstUser_NeverAdoptsTheAgentPort`.

   Issuer: `http://sso.localhost:8080` for a localhost public URL
   (containers resolve `sso.localhost` via `extraHosts`), otherwise the
   public URL itself. Under a **https** issuer the orchestrator emits **no**
   `extraHosts` pin: the container resolves the issuer by real DNS and reaches
   the TLS terminator that serves it, because Bloud serves no certificate at
   the gateway and a pinned TLS dial lands on a port nothing answers. The pin
   stays for plain-http issuers. `HostSet.ProxyConsistency` /
   `Deployability` report the layers that disagree at startup; see
   [`docs/plans/proxied-scheme-urls.md`](docs/plans/proxied-scheme-urls.md). An app with
   `sso.loopbackIssuer` instead takes `http://localhost:<compat port>` and gets
   no `extraHosts` entry: it shares the host network namespace, where localhost
   is already the host. That substitution is gated on the deployment being plain
   http (`HostSet.LoopbackIssuerBaseURL` returns "" under a https public URL),
   because the issuer is also the browser's redirect target and a loopback
   issuer would strand every visitor who is not on the Bloud machine. Address changes
   flow through the orchestrator (`SetPublicURLIntent`): persist to
   `settings['public_url']`, update the live `hostset.State`, reset SSO apps +
   `apps-authentik-server` so the lifecycle re-provisions Authentik (redirect
   URIs, outpost browser URL) and rewrites app configs, then re-ensure the
   dashboard OAuth app. The intent carries the canonical origin, re-rendered
   from the parsed value at the API boundary, so a save that only changed
   capitalization, a trailing slash, or an explicit default port is caught by
   the no-op guard instead of restarting every SSO app. The PUT response
   returns that canonical `url` so the UI can poll for convergence without
   duplicating the parser.
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
    on Traefik, and/or Tailscale Serve) remains the planned follow-up. When a
    TLS terminator sits in front of Traefik, `BLOUD_TRUSTED_PROXY_NETS` names
    that proxy's address (IP or CIDR, as Traefik sees it) so Traefik accepts
    its `X-Forwarded-*` and the original scheme reaches Authentik; without it
    Traefik rewrites `X-Forwarded-Proto` to `http` and the Authentik login flow
    stalls on mixed content. **An empty list is not "trust nobody": it selects
    the private-range default** (`127.0.0.0/8`, `10.0.0.0/8`, `172.16.0.0/12`,
    `192.168.0.0/16`, `100.64.0.0/10`), because a home server's terminator is
    on the LAN and naming it must not be a prerequisite for https. An explicit
    list replaces the default rather than widening it, so naming one terminator
    trusts exactly that one. The cost of the default: on a flat LAN the client is
    also an internal address, so a device in those ranges can assert
    `X-Forwarded-Proto`, `Host`, and `X-Forwarded-For` for requests it sends
    itself. Accepted for a home deployment, but it means an Authentik IP-based
    access policy is forgeable from inside those ranges, so do not write one that
    matters. Trust is still scoped to the source address: the generated config
    never emits `forwardedHeaders.insecure: true`. See
    [`docs/plans/upstream-proxy-headers.md`](docs/plans/upstream-proxy-headers.md).
    Measured on the real proxied install: before the default, 55 of 55 requests
    from the terminator arrived at Authentik as `scheme: "http"`; after, the
    same request arrives as `scheme: "https"`.
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
15. **Cross-app wiring goes through typed integration contracts.** A contract
    (`internal/catalog/contracts.go`) is the one place the vocabulary lives: the
    label a consumer declares under `integrations:`, the secret names a provider
    must publish for it, and the values it must declare. Providers offer a
    contract in their metadata (`provides: <contract>: {secrets, values}`) and
    the catalog loader rejects a declaration that does not match it. A consumer
    declares what it reads with `integrations.<contract>.requires` (a list of
    secret names, validated against the contract), and **only those are
    resolved**: declaring a contract gets an app the provider's address, never a
    credential by default. The orchestrator resolves each declared contract into
    `AppState.Integrations`, **one typed slice per contract** (`PVRs`,
    `MediaServers`, `DownloadClients`, `SSO`, `ModelSources`, `Inference`),
    each binding embedding `ProviderRef` (`App`, `Installed`, `Node`, `Port`,
    `BaseURL`, `LocalURL` where `BaseURL` is what the app stores and
    `LocalURL` what the configurator calls) plus that contract's payload.
    Credentials come from the
    host store (`AppSecretsProvider.SetAppSecret`); values are static metadata.
    Never add a field to a shared binding struct: a new capability is a contract
    entry, a payload type in `pkg/configurator`, and one arm in
    `bindContract`. An app **never** reads or writes another app's files: not
    that app's data directory (`dataDir/<app>`), not a config file, whatever its
    format. The shared trees Bloud owns for the whole stack (`media/`,
    `downloads/`) are declared as volumes in metadata; they are not an app's
    private state. It also **never** probes a provider's port to decide whether
    it is installed: `Installed` is that answer, and it is the same condition as
    the graph edge. A probe cannot separate "not installed" from "restarting",
    so keying a prune off it deletes wiring that is still wanted. The bindings
    for a contract mirror `computeAppDeps`, so they never describe a provider
    the graph does not order.

## host-agent HTTP API (port 3000)

- Public: `GET /health`, `GET /auth/login`, `GET /auth/callback`,
  `POST /auth/logout`, `GET /api/health`, `GET /api/setup/status`,
  `GET /api/auth/me`, plus the public system-info router.
- Until the first convergence pass finishes, `/api/*` and the root `/health`
  probe answer 503 `{"error":"starting"}` (`bootstrapGate`,
  `internal/api/loading.go`); `/fonts/` and `/favicon.*` pass through so the
  waiting page renders in the brand. A 200 from `GET /api/health` means the
  gate is open, **not** that SSO came up: `close(o.ready)` fires when the first
  convergence pass *returns*, and `converge` reports no error. Confirm with
  `GET /api/setup/status`, whose `authentikReady` is a live probe of the
  identity provider.
- Once the gate is open, health answers on its own merits. `checkSystemHealth`
  (`internal/api/server.go`) is the single implementation behind both
  `Server.CheckSystemHealth` and the HTTP handler: database reachable and the
  orchestrator intent loop alive. Failure is 503 `{"status":"unhealthy"}`, with
  the reason logged and never put on the wire (the endpoint is public). That body
  is how a client tells "up and broken" apart from the gate's "starting".
- Authenticated (session cookie, or loopback/`BLOUD_TRUSTED_LOCAL_NETS` bypass):
  `GET /api/apps` (catalog), `GET /api/apps/installed`,
  `GET /api/apps/{name}/metadata`, `POST /api/apps/{name}/install`,
  `POST /api/apps/{name}/uninstall`, `PATCH /api/apps/{name}/rename`,
  home + logs routers.
- Admin: `POST /api/apps/refresh-catalog`, `GET /api/system/rebuild/stream`,
  settings (incl. `GET/PUT /api/settings/public-url`: the address setting),
  sharing, remote-apps routers.

## Adding an app

1. `apps/<name>/metadata.yaml`. Full field reference in
   `services/host-agent/internal/catalog/models.go` (source of truth):
   `name`, `displayName`, `description`, `category` (media | productivity |
   security | infrastructure), `port`, `isSystem`, `sso`
   (`strategy`, `callbackPath`, `userCreation`, `bypassPaths`, `env` mappings),
   `integrations` (a contract name: `{required, multi,
   compatible: [{app, default}]}`), `provides` (per contract: the `secrets` this
   app publishes and the `values` it declares for the apps that integrate with
   it, see invariant 15), `containers[]`
   (`name`, `image` (**pin versions**; a rolling tag fails `npm run
   check:image-pins`), `command`, `network`/`networks`,
   `restartPolicy`, `environment`, `extraHosts`, `ports`, `volumes`,
   `dependsOn`, `healthCheck {test, interval, timeout, retries}`).
   Template vars in environment/volumes: `{{appDataDir}}` (resolves to
   `$BLOUD_DATA_DIR/apps/<app>`, defined once in `internal/dirs`),
   `{{dataDir}}`, and
   `{{postgresPassword}}` (a per-app PostgreSQL password for apps that bundle
   their own postgres). There is no per-app admin-password template var; per-app
   admin credentials are generated by the secrets provider on demand
   (`GenerateAppAdminPassword`) and delivered through a file the configurator
   writes, never through the container-spec template.
2. `apps/<name>/icon.png`, taken from [selfh.st/icons](https://selfh.st/icons/),
   the community icon set the catalog's icons come from. Fetch the PNG through
   the jsDelivr mirror and commit it unchanged:
   `https://cdn.jsdelivr.net/gh/selfhst/icons@main/png/<app>.png`. No resize,
   no recompression, no format conversion. `IconHandler`
   (`internal/api/apps_module.go`) serves that file verbatim and returns 404
   when it is absent, which is what makes the frontend fall back to a letter
   avatar; the `icon:` field in `models.go` is not the mechanism and no app in
   the catalog sets it. An app the set does not cover keeps its upstream icon,
   and its `INTEGRATION.md` says where the file came from.
3. `apps/<name>/configurator.go`. Implements `NodeLifecycle`
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
4. Tests: unit tests in the app package; integration assertions in
   `services/host-agent/internal/e2e/e2e_test.go` (build tag `integration`);
   user-journey spec in `e2e/tests/`. **Test behavioral outcomes** (verify via
   the app's own API), not config values.
5. Add the app to `validation.yaml` (`apps:` block: auth strategy,
   validation-level, file globs, optional `e2e-project`).
6. Reference patterns: `apps/jellyfin` (LDAP, setup wizard, plugins),
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
That is the shipping blocker. Everything else this paragraph used to list is
closed in the ledger: the engine's silent-failure paths (catalog nil-deref,
lock-free `MemoryCache`, an intent queue that could exit permanently),
`appclient.Call.Timeout`, container drift never being repaired while the
process is alive, and duplicated orchestrator wiring (CLI vs router).
Recently paid: durable lifecycle operation state
(`store/operations.go` + orchestrator recorder, plan archived), versioned schema
migrations, route-generation purity (PR #88), per-connection SQLite pragmas
moved into the DSN (PR 7), and the periodic self-healing pass
(`orchestrator/selfheal.go`, 2026-09-30: a `ReconcileIntent` on an idle
~60s timer, which is also what gives `retryable` on the operation row a
driver). Two earlier claims are
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
| The full app + container graph (generated) | [architecture/dependency-graph.md](docs/architecture/dependency-graph.md) (image in [README.md#the-full-graph](README.md#the-full-graph)) |
| How to add an app | [guides/contributing-apps.md](docs/guides/contributing-apps.md) |
| Multi-container app model | [specs/app-spec.md](docs/specs/app-spec.md) |
|Backend debt + repayment plan|[operations/tech-debt.md](docs/operations/tech-debt.md)|
|Build the .deb release package|[operations/packaging.md](docs/operations/packaging.md)|
|Sharing/federation (in progress)|[features/sharing.md](docs/features/sharing.md)|
|MCP servers as catalog apps (design; not built)|[features/mcp.md](docs/features/mcp.md)|
|Dashboard grid + widgets|[features/dashboard.md](docs/features/dashboard.md)|
| Dated review findings|[specs/review.md](docs/specs/review.md)|
| Latest architecture/code review (2026-09-19)|[specs/review-2026-09-19.md](docs/specs/review-2026-09-19.md)|
| In-flight designs | [plans/](docs/plans/) |
