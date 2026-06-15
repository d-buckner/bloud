# Contributing Apps to Bloud

How to add a new app to Bloud. Read
[portable-runtime-architecture.md](portable-runtime-architecture.md) first for the
component overview.

## App Structure

Each app lives in `apps/<name>/` with these files:

```
apps/your-app/
  metadata.yaml     # identity, port, integrations, routing, SSO
  configurator.go   # Go hooks for runtime configuration
  icon.png          # 256x256 PNG, transparent background
```

## Step 1: metadata.yaml

Declares what the app needs. The catalog loads this at startup.

```yaml
name: your-app
displayName: Your App
description: What it does in one sentence
category: media            # media, productivity, security, infrastructure
port: 8080
image: someorg/someimage:1.2.3   # pin the version

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
```

If your app has no integrations: `integrations: {}`.
System apps (postgres, redis, traefik) set `isSystem: true` to hide from the catalog.

## Step 2: configurator.go

Implements runtime configuration that can't be expressed in static container definitions.
Every configurator must be idempotent — it runs on every reconciliation cycle.

```go
package yourapp

import (
    "context"
    "fmt"
    "codeberg.org/d-buckner/bloud-v3/services/host-agent/pkg/configurator"
)

type Configurator struct {
    port int
}

func NewConfigurator(port int) *Configurator {
    return &Configurator{port: port}
}

func (c *Configurator) Name() string { return "your-app" }

// PreStart: config files, directories, certificates.
func (c *Configurator) PreStart(ctx context.Context, state *configurator.AppState) error {
    return nil
}

// HealthCheck: wait for the app to be ready.
func (c *Configurator) HealthCheck(ctx context.Context) error {
    return configurator.WaitForHTTP(ctx,
        fmt.Sprintf("http://localhost:%d/health", c.port),
        configurator.DefaultHealthCheckTimeout)
}

// PostStart: API calls, integrations, runtime setup.
func (c *Configurator) PostStart(ctx context.Context, state *configurator.AppState) error {
    return nil
}
```

### AppState

The reconciler passes resolved integration outputs to each configurator:

| Field | Description |
|---|---|
| `state.DataPath` | App data dir (`~/.local/share/bloud/your-app`) |
| `state.BloudDataPath` | Shared data dir (`~/.local/share/bloud`) |
| `state.SSOEnabled` | Whether SSO integration is active |
| `state.LDAP` | Typed LDAP output (host, port, baseDN, bindUser, bindPassword) |

### Register your configurator

In `services/host-agent/internal/appconfig/register.go`:

```go
import yourapp "codeberg.org/d-buckner/bloud-v3/apps/your-app"

func RegisterAll(registry *configurator.Registry, cfg *config.Config) {
    // ... existing ...
    registry.Register(yourapp.NewConfigurator(8080))
}
```

## Step 3: Test

Add integration test assertions to `services/host-agent/internal/e2e/e2e_test.go`
(build-tag gated with `//go:build integration`). Tests run against real services in the
Lima VM compose stack.

Test the behavioral outcome, not config values:

```go
func TestYourApp_PostStartConfiguresCorrectly(t *testing.T) {
    runHostAgent(t, "configure", "poststart", "your-app")
    // Verify via the app's own API that the config took effect
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
