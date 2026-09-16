# Contributing Apps to Bloud

How to add a new app to Bloud. Read
[docs/architecture/overview.md](docs/architecture/overview.md) first for the
component overview.

## App Structure

Each app lives in `apps/<name>/` with these files:

```
apps/your-app/
  metadata.yaml     # identity, port, integrations, container spec, SSO
  configurator.go   # NodeLifecycle implementation only
  registration.go   # init() -> configurator.MustRegisterFactory
  lib/              # optional: app-specific helpers (package lib)
  icon.png          # 256x256 PNG, transparent background
  INTEGRATION.md    # integration notes for whoever maintains this app
```

Keep `configurator.go` to the lifecycle wiring. Everything else has its own
place — see [Where helper code goes](#where-helper-code-goes).

## Step 1: metadata.yaml

Declares what the app needs and how to run it. The catalog loads this at startup.

```yaml
name: your-app
displayName: Your App
description: What it does in one sentence
category: media            # media, productivity, security, infrastructure
port: 8080

integrations:
  database:
    required: true
    compatible: [{ app: postgres, default: true }]
  sso:
    required: false
    compatible: [{ app: authentik }]

sso:
  strategy: native-oidc    # native-oidc, ldap, forward-auth, none

containers:
  - name: apps-your-app
    image: someorg/someimage:1.2.3   # pin the version
    network: apps-net
    restartPolicy: always
    environment:
      TZ: Etc/UTC
    ports:
      - host: 8080
        container: 8080
    volumes:
      - source: "{{appDataDir}}/config"
        destination: /config
    healthCheck:
      test: ["CMD-SHELL", "curl -sf http://localhost:8080/health"]
      interval: 5
      timeout: 10
      retries: 12
```

If your app has no integrations: `integrations: {}`.

Available template variables in `containers[].environment` and `containers[].volumes`:
- `{{appDataDir}}` — app-specific data directory
- `{{dataDir}}` — shared Bloud data directory
- `{{postgresPassword}}` — per-app PostgreSQL password (for apps that bundle their own postgres container)

System apps (traefik, authentik) set `isSystem: true` to hide from the
user-facing catalog. Apps that need databases declare their own postgres and
redis containers in `containers:` — each app gets its own isolated database.

## Step 2: configurator.go

Implements runtime configuration that can't be expressed in the static container
definition. Every configurator must be idempotent — it runs on every reconciliation cycle.

Implement `configurator.NodeLifecycle` — four methods, all idempotent:

```go
package yourapp

import (
    "context"

    "codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

type Configurator struct {
    port int
}

func NewConfigurator(port int) *Configurator {
    return &Configurator{port: port}
}

func (c *Configurator) Name() string { return "apps-your-app" }

// PreStart runs before the container starts: directories, config files,
// certificates. Return changed=true when you modified a file the container
// read at boot, which signals that it must be restarted to pick it up.
func (c *Configurator) PreStart(ctx context.Context, state *configurator.AppState) (bool, error) {
    return false, nil
}

// PostStart runs after the container is healthy: API calls, integrations,
// runtime setup. Called on every reconciliation, so it must be a no-op
// when everything is already in place.
func (c *Configurator) PostStart(ctx context.Context, state *configurator.AppState) error {
    return nil
}

// Remove tears down app-owned state. clearData=true means persistent data
// should go too. Container removal itself is the orchestrator's job.
func (c *Configurator) Remove(ctx context.Context, state *configurator.AppState, clearData bool) error {
    return nil
}
```

There is no `HealthCheck` method on `NodeLifecycle` — readiness comes from the
`healthCheck:` block you declared in `metadata.yaml`, which the orchestrator
enforces between PreStart and PostStart.

### AppState

The orchestrator passes resolved integration outputs to each configurator:

| Field | Description |
|---|---|
| `state.DataPath` | App data dir (`~/bloud-data/your-app`) |
| `state.BloudDataPath` | Shared data dir (`~/bloud-data`) |
| `state.SSOEnabled` | Whether SSO integration is active for this app |
| `state.LDAP` | Typed LDAP output (host, port, baseDN, bindUser, bindPassword) |

### Register your configurator (self-registering)

Create `apps/your-app/registration.go`. The package registers a factory in
`init()`; the host-agent registry instantiates it lazily on the first lookup
of the node, so your configurator is only built when the app is actually
reconciled:

```go
package yourapp

import "codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"

func init() {
    configurator.MustRegisterFactory("apps-your-app", func(deps configurator.Deps) configurator.NodeLifecycle {
        return NewConfigurator(0, deps.PrimaryBaseURL, deps.Secrets, deps.Logger)
    })
}
```

`configurator.Deps` carries the host-side inputs (logger, secrets provider,
primary-base-URL resolver, Traefik port, restart-container callback).

Then add your app to **`apps/registry.go`** — the single place that lists the
catalog. Two edits, both in the `apps/` module:

```go
import (
    _ "codeberg.org/d-buckner/bloud/apps/your-app"
)

func NodeNames() []string {
    return []string{
        // ...
        "apps-your-app",
    }
}
```

You do **not** touch `services/host-agent/internal/appconfig/register.go`
anymore. That file wires only the system configurators (Traefik, Authentik),
which are runtime-dependent and registered eagerly in `RegisterSystem`.

`TestRegisterAll` in `apps/registry_test.go` asserts every `NodeNames()`
entry actually has a registered factory, so a missing `registration.go` or a
typo fails `go test ./...` instead of surfacing as a missing configurator
during an install.

## Where helper code goes

A configurator accretes fast: a typed client for the app's own HTTP API, a
config-file builder, parsers, fixtures. Three tiers decide where each piece
lives.

| Helper kind | Goes in |
|---|---|
| Lifecycle flow (`PreStart`/`PostStart` steps, wizard logic) | `apps/<name>/` top level, same package as the configurator |
| App-specific reusable helper (own-API client, config builder, parser, fixtures) | `apps/<name>/lib/` |
| Needed by 2+ apps, **or** by host-agent itself | `services/host-agent/pkg/` (existing: `xmlutil`, `slug`, `authentik`) |

`lib/` is an ordinary Go sub-package (`package lib`), imported as
`bloud/apps/<name>/lib`. Two hard rules:

- It must **not** import its parent app package — that is an import cycle.
- It must **not** import anything under `services/host-agent/internal/`.
  The compiler enforces this; if a helper needs an `internal/` type, it
  belongs in the parent package, not in `lib/`.

Pass what it needs in as arguments rather than reaching for host-agent state.
A `lib/` package that takes `(baseURL, token, logger)` is testable on its own;
one that imports the orchestrator is not.

New `lib/` files carry the standard two-line SPDX header, same as everything
else (`npm run license:check` enforces it).

Migration is opportunistic — existing apps are not being reshaped in one go.
New apps should start in this shape, and when a fat `configurator.go` gets
split, the non-lifecycle helpers move into `lib/` as part of that work.

## Step 3: Test

Add integration test assertions under
`services/host-agent/internal/e2e/`, which is split per scenario
(`system_apps_test.go`, `jellyfin_test.go`, `affine_test.go`,
`crash_recovery_test.go`, `uninstall_test.go`, …). Add a new
`<yourapp>_test.go` for your app. Shared harness helpers — env resolution,
`waitHTTP`, `getInstalledApps`, `postJSON`, `TestMain` — live in
`e2e_test.go`; reuse them rather than writing your own.

Every file in this package is build-tag gated and must carry the tag, or it
will silently stop running:

```go
//go:build integration
```

Tests run against real services on the selected runtime (Lima/QEMU VM or
native host).

Test the behavioral outcome, not config values:

```go
func TestYourApp_PostStartConfiguresCorrectly(t *testing.T) {
    // Install the app, then verify via its own API that integration took effect
}
```

Run with: `./bloud validate --tier integration`

Also add:

- A user-journey Playwright spec in `e2e/tests/your-app.spec.ts` (user flows go
  through the public port `http://localhost:8080`; API helpers use the internal
  API at `BLOUD_API_URL` / `:3000`). Wire it up with
  `./bloud e2e app` + `BLOUD_E2E_APP=your-app`.
  Use `describeApp` from `e2e/lib/app-suite.ts`: it is a real
  `test.describe` that also provides one shared authenticated page
  (`app.page`) and serial execution, so a red run stops at the stage that
  broke. Write one test case per observable behavior — converge → catalog
  → home tile → open → sign-in (`openAppFromHome` opens the app popup;
  only the sign-in body is app-specific). See `e2e/tests/jellyfin.spec.ts`.
- An entry for the app in `validation.yaml` under `apps:` (auth strategy,
  validation level, file globs, `e2e-project`). `./bloud validate` infers the
  affected apps from this registry — keep it in sync.

## Reference Apps

| App | Pattern |
|---|---|
| `apps/jellyfin` | LDAP SSO, setup wizard, plugin config, media libraries |
| `apps/authentik` | Multi-container, LDAP infrastructure, API token management |
| `apps/immich` | Database integration, OIDC SSO |
| `apps/affine` | Own postgres+redis, OIDC config file, first-run owner bootstrap (see `INTEGRATION.md`) |
| `apps/navidrome` | Forward-auth SSO with bypass paths, simple single-container app |
