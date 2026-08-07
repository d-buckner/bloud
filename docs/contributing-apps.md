# Contributing Apps to Bloud

How to add a new app to Bloud. Read
[portable-runtime-architecture.md](portable-runtime-architecture.md) first for the
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

healthCheck:
  path: /health
  interval: 5
  timeout: 60

container:
  name: apps-your-app
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
```

If your app has no integrations: `integrations: {}`.

Available template variables in `container.environment` and `container.volumes`:
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

### Register your configurator

In `services/host-agent/internal/appconfig/register.go`:

```go
import yourapp "codeberg.org/d-buckner/bloud/apps/your-app"

func RegisterAll(
    registry *configurator.Registry,
    cfg *config.Config,
    runtime containerruntime.Runtime,
    catalogApps map[string]*catalog.App,
    logger *slog.Logger,
    templateVars map[string]string,
) {
    // ... existing ...
    registry.Register(yourapp.NewConfigurator(8080))
}
```

## Step 3: Test

Add integration test assertions to `services/host-agent/internal/e2e/e2e_test.go`
(build-tag gated with `//go:build integration`). Tests run against real services in the
Lima VM.

Test the behavioral outcome, not config values:

```go
func TestYourApp_PostStartConfiguresCorrectly(t *testing.T) {
    // Install the app, then verify via its own API that integration took effect
}
```

Run with: `./bloud validate --tier integration`

## Reference Apps

| App | Pattern |
|---|---|
| `apps/jellyfin` | LDAP SSO, setup wizard, plugin config, media libraries |
| `apps/authentik` | Multi-container, LDAP infrastructure, API token management |
| `apps/immich` | Database integration, OIDC SSO |
| `apps/qbittorrent` | Simple app, INI config patching |
