# Plan: Replace the caldav-mcp wrapper with dav-mcp

> Status: implemented 2026-10-05. Supersedes the implementation half of
> [caldav-mcp.md](archive/caldav-mcp.md); that plan's design of the service
> account and the `caldav`/`appApi` contract split stands unchanged. The catalog
> id did not survive, see "Renamed during implementation" below.

## Why

The current wrapper (`supergateway` bridging `caldav-mcp@0.10.0` from stdio) does
not come up. Measured on a live native install, the cause is not the bridge:

| Measurement | Value |
|---|---|
| LDAP bind against Authentik's outpost | ~1.8s each (5 samples: 1.85 / 1.97 / 1.77 / 1.81 / 1.77) |
| LDAP search | 0.17-0.27s |
| Unauthenticated request to Radicale | 0.002s |
| **Authenticated request to Radicale** | **3.5-4.3s** |
| `ts-caldav` `CalDAVClient.create()` | 14.95s |
| `getCalendars()` | 3.5s |
| **`initialize` over direct stdio, no supergateway** | **24.37s** |

`caldav-mcp@0.10.0`'s `main()` runs `CalDAVClient.create()` and
`await client.getCalendars()` *before* `server.connect(transport)`. The MCP
handshake therefore cannot be answered until ~24s of CalDAV I/O finishes, while
`pkg/appclient` defaults to a 15s per-request deadline. The probe gets its 200
headers (supergateway opens the SSE stream in 18ms) and then dies in
`read body: context deadline exceeded`, 7 attempts out of 7.

`dav-mcp` (`PhilflowIO/dav-mcp` v4.1.2, MIT) was measured against the same
Radicale on the same network:

| Measurement | Value |
|---|---|
| Container start to `/health` 200 | 18.4 / 18.0 / 17.7s (warm x3) |
| MCP `initialize` | 0.03s |
| `tools/list` | 0.005-0.025s |
| `POST /mcp` with no bearer | 401 |
| Unreachable DAV server | exits 1 in <8s |

It wins on structure, not speed: it does its blocking **before the port binds**,
so nothing ever waits on it mid-request, and a bad configuration fails the
process instead of answering 200 with a broken bridge.

**What this does not fix:** the ~1.8s-per-bind Authentik cost survives the port.
Real tool calls still pay it: `list_calendars` 7.5s, `list_addressbooks` 4.0s.
That is tracked separately as
[#237](https://github.com/d-buckner/bloud/issues/237) and is out of scope here.

## The one real piece of work: contract-driven container env

`dav-mcp` is configured **exclusively** through process environment variables
(`src/auth-config.js` reads `process.env` and nothing else). Two of the values it
needs are resolved bindings, not static metadata:

- `CALDAV_PASSWORD` - the `appApi` contract secret published by Radicale
- `BEARER_TOKEN` - this app's own `httpToken`, generated in `PreStart`

Bloud has no way to put a resolved binding into a container's environment.
`ContainerSpecFromDef` renders `environment:` with `{{dataDir}}`,
`{{appDataDir}}`, and the global `TemplateVars` built once at process start from
`config.Config`. The resolved `AppState` is not in that path.

### Option A: `envFile` on the container spec (recommended)

A new `envFile` field on `catalog.ContainerDef`, carried to
`container.Spec`, passed to podman as `--env-file`.

- The configurator writes `{{appDataDir}}/config/env` with `managedfile.Write`
  at mode 0600 and returns `RestartIf(changed, ...)`.
- The rendered spec contains only the *path*, so `spec.Revision()` stays stable
  when the credential rotates. The catalog-update drift check
  (`resetSpecChangedNode`) keeps comparing like for like.
- Works with a distroless image: podman reads the file, the image needs no shell.
- Same mental model the app already uses for `run.sh`: a generated file in the
  mounted config dir, a recreate when it changes.

Cost: one field through `catalog.ContainerDef` -> `container.Spec` -> the podman
runtime, plus the `internal/wire` completeness test and a runtime unit test.

### Option B: `ContainerEnv` on `PreStartResult`

Rejected. `PreStart` already holds the resolved `AppState`, so the plumbing looks
free, but `computeContainerSpec` is also called from `catalog_update.go` with no
`PreStart` in the path. That comparison would render a spec without the
contributed env and mismatch the running container on every pass, or the revision
would have to exclude contributed env, which quietly weakens drift detection. It
also breaks the documented purity of `computeContainerSpec` as "what the catalog
produces".

### Option C: global template vars for the two secrets

Rejected outright. It bypasses the typed contract, loses the graph edge that
orders Radicale before this app, and makes a cross-app secret renderable by every
container template in the catalog. Invariant 15 exists for exactly this.

## Changes

### Framework (the only non-app change)

| File | Change |
|---|---|
| `services/host-agent/internal/catalog/models.go` | `ContainerDef.EnvFile string` |
| `services/host-agent/internal/container/runtime.go` | `Spec.EnvFile string` |
| podman runtime create path | pass `--env-file` (or read and inject) |
| `services/host-agent/internal/wire/completeness_test.go` | cover the new field |
| runtime unit test | env file reaches the created container |

### `apps/dav-mcp/metadata.yaml`

- Image: `ghcr.io/philflowio/dav-mcp:4.1.2`. Consider a digest pin for the same
  provenance reason `apps-radicale-pimsync` uses one.
- **Delete** the `entrypoint`/`command` supergateway block, the `/runtime` npm
  prefix volume, and the `config` volume's role as a script drop.
- `envFile: "{{appDataDir}}/config/env"`.
- Static `environment:` only: `NODE_ENV: production`, `AUTH_METHOD: Basic`,
  `PORT: "9333"`.
- Healthcheck must switch to exec form. Distroless has no shell and no `wget`, so
  the current `CMD-SHELL` would fail outright:
  `["CMD", "/nodejs/bin/node", "-e", "<GET /health>"]`.
  Keep `interval: 5`, `retries: 30` to cover the ~18s boot.
- Host port stays 9333. Container port becomes 9333 via `PORT`, so there is no
  collision with host-agent's 3000 on the native backend.

### `apps/dav-mcp/configurator.go`

- `PreStart`: unchanged in shape. Ensure the bearer, read the `caldav` and
  `appApi` bindings, render one env file instead of three files.
  `managedfile.Write` still gives the `changed` signal.
- Quote-free rendering is no longer a concern: the env-file format is
  `KEY=value` per line, so the single-quote escaping in `renderRunScript` goes
  away. Newline-in-value still has to be rejected at render time.
- The "no address yet / no credential yet" branches stay: write what is resolved,
  restart when the rest lands.
- `PostStart`: keep the MCP handshake probe unchanged. It is the right gate and
  it now passes in milliseconds.

### `apps/dav-mcp/api.go`

- Keep the `initialize` probe and the JSON-RPC envelope check unchanged.
- The 120s `Within` budget can drop substantially; the port binds only after the
  DAV login, so the wait is for the container, not for the handshake.
  `apps/configtest/waitbudget_test.go` must still pass against `PhaseBudget`.

### Unchanged

- The `caldav`, `appApi`, and `mcp` contracts and their registry entries.
- Radicale's `provides:` block and the `caldav-service` account provisioning.
- The `headless: true`, `category`, and `port` fields.

### Renamed during implementation (decision reversed)

This plan said the catalog id would stay `caldav-mcp`. It did not: the app ships
as **`dav-mcp`**, and `apps/caldav-mcp` is gone rather than renamed-in-place.

The reasoning that held the old id was cost avoidance: the Hermes declaration, the
README blocks, the secret namespace, and the installed-app rows all name it. That
cost turned out to be small and one-time, and the thing it bought was a name that
lies. `caldav-mcp` is the name of the npm package that was removed; keeping it
would mean a catalog entry whose name points at a package that is not in the tree,
and a container called `apps-caldav-mcp` running `dav-mcp`. A reader cannot tell
the two apart from the name, and the old shape is the one that never converged.

What the rename touched:

- `apps/dav-mcp/` -> `apps/dav-mcp/`, package `caldavmcp` -> `davmcp`
- node and container `apps-caldav-mcp` -> `apps-dav-mcp`
- published secret namespace and the `mcp` `serverName` (so the Hermes tool
  namespace is `dav-mcp`)
- Hermes' `integrations.mcp` compatible list
- `validation.yaml` app key and its file glob
- comments across host-agent that named the CalDAV consumer

No migration ships with it. The old app was uninstalled before the rename, so
there is no installed row, no published secret, and no Hermes namespace under the
old name to carry over. An operator who still has `caldav-mcp` installed gets an
orphaned row and must uninstall it and install `dav-mcp`.

### Docs and generated artifacts

- `apps/dav-mcp/INTEGRATION.md`: rewrite. Upstream identity, the measured
  numbers above, the distroless constraints, the env-file mechanism.
- `docs/features/mcp.md`: update the provider description and the tool count.
- `README.md`: regenerate both blocks (`catalogdoc --write`, `depgraph --write`).
- `docs/plans/caldav-mcp.md`: move to `plans/archive/` with a pointer here.

### Tests

- `apps/dav-mcp/configurator_test.go`: rewrite for one env file.
- `apps/conformance_test.go`: re-run, adjust if it asserts on the wrapper shape.
- Runtime test for `envFile` reaching a created container.
- No Playwright spec exists for this app (headless, no UI); the MCP handshake
  probe in `PostStart` remains the behavioral gate.

## Risks and open questions

## Decisions

- **The wider write surface is wanted, not a risk to mitigate.** The tool count
  goes from 10 to 27 and gains CardDAV, so the agent can write contacts as well
  as read them through the `caldav-service` account. That is the point of the
  swap: a calendar namespace the agent can only read is half a namespace. Do not
  pin the account read-only. The account's existing rights model is what bounds
  its reach, and it bounds CardDAV the same way it bounds CalDAV.

## Risks

1. **Rate limiter.** `dav-mcp` applies 100 req/15min to non-private source IPs
   and 10000 to private. Hermes dials from the host network, which is private,
   so this is fine today; it becomes a ceiling if the endpoint is ever exposed
   publicly.
2. **Upstream maturity.** 36 stars, created 2025-10-02, last pushed 2026-10-02,
   MIT, and it carries a real test suite (39 test files). Young but active.
3. **`GET /mcp` and `DELETE /mcp` are unsupported** (stateless mode). Anything
   expecting a session-carrying streamable-HTTP client will not get one.

## Explicitly out of scope for this PR

**The ~1.8s-per-bind Authentik LDAP cost** ([#237](https://github.com/d-buckner/bloud/issues/237)).

This is the actual root cause of the numbers above, and this port does not fix
it. Startup stays at roughly 18s because the port binds only after ~4 binds, and
every tool call still pays the tax: `list_calendars` 7.5s, `list_addressbooks`
4.0s. It is tracked separately because it is an identity-layer investigation that
affects every calendar client on the LAN, not just MCP, and folding it into an
app-swap PR would bury it.

Keeping it out also keeps this PR honest about what it changed: readiness goes
from never-converging to converged, latency is unchanged.

## Suggested order

1. Framework `envFile` field with its own tests, landed first and independently.
2. `metadata.yaml` + `configurator.go` + `api.go` swap.
3. `INTEGRATION.md` and the generated docs.
4. Archive the old plan.
