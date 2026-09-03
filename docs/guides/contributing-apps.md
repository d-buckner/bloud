# Contributing Apps to Bloud

How to add a new app to Bloud. Read
[docs/architecture/overview.md](docs/architecture/overview.md) first for the
component overview.

## App Structure

Each app lives in `apps/<name>/` with these files:

```
apps/your-app/
  metadata.yaml     # identity, port, integrations, container spec, SSO
  configurator.go   # Go hooks for runtime configuration
  icon.png          # 256x256 PNG, transparent background
```

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

```go
package yourapp

import (
    "context"
    "fmt"
    "codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

type Configurator struct {
    port int
}

func NewConfigurator(port int) *Configurator {
    return &Configurator{port: port}
}

func (c *Configurator) Name() string { return "your-app" }

// PreStart: config files, directories. Runs before the container starts.
func (c *Configurator) PreStart(ctx context.Context, state *configurator.AppState) error {
    return nil
}

// HealthCheck: wait for the app to be ready.
func (c *Configurator) HealthCheck(ctx context.Context) error {
    return configurator.WaitForHTTP(ctx,
        fmt.Sprintf("http://localhost:%d/health", c.port),
        configurator.DefaultHealthCheckTimeout)
}

// PostStart: API calls, integrations, runtime setup. Runs after the container is healthy.
func (c *Configurator) PostStart(ctx context.Context, state *configurator.AppState) error {
    return nil
}
```

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
primary-base-URL resolver, Traefik port). Then add one blank import to
`services/host-agent/internal/appconfig/register.go` so the package is linked
into host-agent:

```go
_ "codeberg.org/d-buckner/bloud/apps/your-app"
```

That's the only host-agent-side change. System apps (Traefik, Authentik) are
wired eagerly in `RegisterSystem` and do not use factories.

## Step 3: Test

Add integration test assertions to `services/host-agent/internal/e2e/e2e_test.go`
(build-tag gated with `//go:build integration`). Tests run against real services on the
selected runtime (Lima/QEMU VM or native host).

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
