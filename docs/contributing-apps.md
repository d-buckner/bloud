# Contributing Apps to Bloud

This guide walks through everything you need to add a new app to Bloud — from the first file to a passing test.

## Overview

Each app lives in `apps/<name>/` and consists of six files:

```
apps/your-app/
  metadata.yaml     # app identity, port, integrations, routing
  module.nix        # NixOS module that runs the container
  configurator.go   # Go hooks for setup that can't happen in Nix
  test.ts           # Playwright integration tests
  icon.png          # app icon (256×256 PNG)
  integration.md    # quirks, debugging tips, architecture notes
```

The host-agent reads `metadata.yaml` to understand what the app needs. `module.nix` creates the systemd services. `configurator.go` handles config files, API calls, and anything that needs to happen at runtime.

---

## Quick Start

The fastest path: copy the simplest existing app and adapt it.

```bash
cp -r apps/qbittorrent apps/your-app
# edit metadata.yaml, module.nix, configurator.go
git add apps/your-app/   # required — Nix flakes only see git-tracked files
./bloud rebuild
./bloud install your-app
```

---

## Step 1: metadata.yaml

`metadata.yaml` declares your app's identity and tells Bloud how to route traffic to it.

### Minimal example

```yaml
name: your-app
displayName: Your App
description: What it does in one sentence
category: productivity
port: 8080

image: someorg/someimage:1.2.3
integrations: {}

healthCheck:
  path: /health
  interval: 5
  timeout: 60
```

### Required fields

| Field | Description |
|---|---|
| `name` | Lowercase, hyphenated identifier — must match the directory name |
| `displayName` | Human-readable name shown in the UI |
| `description` | One-liner for the app catalog |
| `category` | `productivity`, `media`, `security`, or `infrastructure` |
| `port` | Host port the app listens on |
| `image` | Docker image — pin the version, no floating `:latest` |
| `integrations` | Dependencies on other apps (see below) |
| `healthCheck` | How to verify the app is up |

### Integrations

Declare what your app depends on. The two types are `database` and `sso`:

```yaml
integrations:
  database:
    required: true    # app won't work without this
    multi: false      # only connects to one provider at a time
    compatible:
      - app: postgres
        default: true

  sso:
    required: false
    multi: false
    compatible:
      - app: authentik
        default: true
```

If your app has no integrations:

```yaml
integrations: {}
```

### SSO configuration

Apps supporting OpenID Connect:

```yaml
sso:
  strategy: native-oidc
  callbackPath: /oauth2/oidc/callback
  providerName: Bloud SSO
  userCreation: true
  env:
    clientId: OAUTH2_CLIENT_ID
    clientSecret: OAUTH2_CLIENT_SECRET
    discoveryUrl: OAUTH2_OIDC_DISCOVERY_ENDPOINT
    redirectUrl: OAUTH2_REDIRECT_URL
    provider: OAUTH2_PROVIDER
    providerName: OAUTH2_OIDC_PROVIDER_NAME
    userCreation: OAUTH2_USER_CREATION
```

The `env` section maps Bloud's SSO fields to the environment variable names your specific app expects — different apps use different names for the same values.

Apps that authenticate TV clients and mobile apps via LDAP:

```yaml
sso:
  strategy: ldap
```

### Routing

Apps are embedded in iframes at `/embed/<name>`. By default, Traefik strips the `/embed/<name>` prefix before forwarding to your container, so your app sees requests at `/`.

**Apps that support `BASE_URL`** (like Miniflux) can serve from a subpath. Disable prefix stripping so the full path is forwarded:

```yaml
routing:
  stripPrefix: false
```

Then set `BASE_URL` in your `module.nix` environment to match:

```nix
BASE_URL = "${cfg.externalHost}/embed/your-app";
```

**Apps without `BASE_URL` support** that hardcode absolute paths (like AdGuard Home redirecting to `/install.html`) are handled transparently by Bloud's service worker — it intercepts iframe requests and rewrites absolute paths to include the embed prefix. You don't need to do anything special.

**Apps that need custom HTTP headers** (e.g. WASM apps requiring cross-origin isolation):

```yaml
routing:
  headers:
    Cross-Origin-Opener-Policy: same-origin
    Cross-Origin-Embedder-Policy: credentialless
```

**Apps that need OAuth callback routes at the root level:**

```yaml
routing:
  absolutePaths:
    - rule: "PathPrefix(`/openid`)"
      priority: 90
      headers:
        X-Frame-Options: ""
```

Use `absolutePaths` sparingly — most apps work fine through `/embed/<name>`.

### Bootstrap

Some apps need client-side pre-configuration before they load, such as setting a server URL in IndexedDB. Use the `bootstrap` field:

```yaml
bootstrap:
  indexedDB:
    database: actual
    entries:
      - store: asyncStorage
        key: server-url
        value: "{{embedUrl}}"
```

The `{{embedUrl}}` placeholder is replaced with the app's embed URL at runtime.

### System apps

Infrastructure apps that users don't interact with directly (Postgres, Traefik) shouldn't appear in the catalog:

```yaml
isSystem: true
```

System apps don't appear in the user-facing catalog and don't get Traefik routes.

---

## Step 2: module.nix

`module.nix` defines the NixOS module that runs your app as a rootless Podman container. Use `mkPodmanApp` — it handles directory creation, database init, service ordering, and configurator hook wiring.

### Standard pattern

```nix
{ config, pkgs, lib, ... }:

let
  mkPodmanApp = import ../../nixos/lib/podman-app.nix { inherit config pkgs lib; appDir = ./.; };
in
mkPodmanApp {
  name = "your-app";
  description = "Your App description";

  image = "someorg/someimage:1.2.3";
  port = 8080;

  environment = cfg: {
    BASE_URL = "${cfg.externalHost}/embed/your-app";
    TZ = "UTC";
  };

  dataDir = true;  # mounts ~/.local/share/bloud/your-app → /data inside container
}
```

Pass `appDir = ./.` so `mkPodmanApp` can read `metadata.yaml` and auto-wire native service dependencies.

### mkPodmanApp parameter reference

| Parameter | Required | Description |
|---|---|---|
| `name` | yes | Must match `metadata.yaml` |
| `description` | yes | Used for the NixOS `enable` option description |
| `image` | yes | Container image |
| `port` | no | Host port (omit for infrastructure-only apps) |
| `containerPort` | no | Container's internal port (defaults to `port`) |
| `options` | no | Custom NixOS options `{ optName = { default, description, type? }; }` |
| `environment` | no | Function `cfg → attrset` returning env vars |
| `volumes` | no | Volume mounts as list, or function `cfg → list` |
| `dataDir` | no | `true` mounts `~/.local/share/bloud/<name>:/data`; string sets custom container path |
| `database` | no | Database name — auto-creates Postgres DB and init service |
| `dependsOn` | no | Container dependencies (`"postgres"` → `"apps-postgres"`) |
| `waitFor` | no | Health checks: `[{ container, command }]` |
| `network` | no | Podman network (default: `"apps-net"`) |
| `userns` | no | User namespace (default: `"keep-id"` for bridge networking) |
| `envFile` | no | Path to secrets file loaded at container start |
| `extraServices` | no | Additional systemd services — attrset or function `cfg → attrset` |
| `extraConfig` | no | Additional NixOS config — attrset or function `cfg → attrset` |

The `cfg` object passed to `environment`, `volumes`, and `extraConfig` includes:

- Your custom options (`cfg.adminUser`, etc.)
- `cfg.externalHost` — e.g. `http://bloud.local`
- `cfg.traefikPort` — Traefik's port
- `cfg.configPath` — `~/.local/share/bloud`
- `cfg.appDataPath` — `~/.local/share/bloud/your-app`
- `cfg.postgresUser` — Postgres username
- `cfg.authentikEnabled` — whether Authentik is installed

### App with a database

Set `database = "yourapp"` and `mkPodmanApp` handles everything: waiting for Postgres, creating the database, and ordering the init service before your container starts.

```nix
mkPodmanApp {
  name = "your-app";
  description = "Your App";
  image = "someorg/someimage:1.2.3";
  port = 8080;
  database = "yourapp";

  volumes = cfg: [
    "/run/postgresql:/run/postgresql:ro"  # socket mount for host postgres
  ];

  environment = cfg: {
    DATABASE_URL = "postgres://${cfg.postgresUser}@/yourapp?host=/run/postgresql";
  };

  dataDir = true;
}
```

Note the socket-style database URL (`host=/run/postgresql`). Rootless Podman containers can't reach host services by IP — use the Unix socket mounted into the container instead. See [Rootless Podman and Host Services](#rootless-podman-and-host-services).

### App with custom options

Custom options become `bloud.apps.your-app.<option>` NixOS settings:

```nix
mkPodmanApp {
  name = "your-app";
  description = "Your App";
  image = "someorg/someimage:1.2.3";
  port = 8080;

  options = {
    adminUser = {
      default = "admin";
      description = "Admin username";
    };
  };

  environment = cfg: {
    ADMIN_USER = cfg.adminUser;
  };
}
```

### App with conditional SSO

```nix
mkPodmanApp {
  name = "your-app";
  # ...

  environment = cfg: {
    APP_URL = cfg.externalHost;
  } // lib.optionalAttrs cfg.authentikEnabled {
    OIDC_CLIENT_ID = cfg.openidClientId;
    OIDC_CLIENT_SECRET = cfg.openidClientSecret;
    OIDC_DISCOVERY_URL = cfg.openidDiscoveryUrl;
  };
}
```

### App with extra services

Use `extraServices` for things like plugin installation that must run before the main container starts:

```nix
mkPodmanApp {
  name = "your-app";
  # ...

  extraServices = cfg: {
    your-app-plugin-install = {
      description = "Install your-app plugin";
      before = [ "podman-your-app.service" ];
      wantedBy = [ "bloud-apps.target" ];
      partOf = [ "bloud-apps.target" ];
      serviceConfig = {
        Type = "oneshot";
        RemainAfterExit = true;
        ExecStart = pkgs.writeShellScript "install-plugin" ''
          set -e
          PLUGIN_DIR="${cfg.appDataPath}/plugins"
          mkdir -p "$PLUGIN_DIR"
          if [ ! -f "$PLUGIN_DIR/plugin.dll" ]; then
            ${pkgs.unzip}/bin/unzip -o ${pluginZip} -d "$PLUGIN_DIR"
          fi
        '';
      };
    };
  };
}
```

### App with host networking

Apps that need to bind to privileged ports or bypass the container network (e.g. DNS):

```nix
mkPodmanApp {
  name = "your-app";
  image = "someorg/someimage:1.2.3";
  port = 3080;
  network = "host";
  userns = null;  # keep-id doesn't apply to host networking

  extraConfig = {
    boot.kernel.sysctl."net.ipv4.ip_unprivileged_port_start" = 53;
  };
}
```

### Volume labels

Always use `:z` or `:Z` on volume mounts for SELinux compatibility:

```nix
volumes = [ "${configPath}/your-app:/data:z" ];
```

`:z` allows sharing between containers. `:Z` is private to one container.

### Advanced: raw module pattern

For apps that need full control — multiple containers, complex ordering, or things `mkPodmanApp` can't express — write the NixOS module directly using `mkPodmanService`:

```nix
{ config, pkgs, lib, ... }:

let
  appCfg = config.bloud.apps.your-app;
  bloudCfg = config.bloud;
  mkPodmanService = import ../../nixos/lib/podman-service.nix { inherit pkgs lib; };
  configPath = "/home/${bloudCfg.user}/.local/share/bloud";
in
{
  options.bloud.apps.your-app = {
    enable = lib.mkEnableOption "Your App";
    port = lib.mkOption {
      type = lib.types.int;
      default = 8080;
      description = "Port to expose the app on";
    };
  };

  config = lib.mkIf appCfg.enable {
    system.activationScripts.bloud-your-app-dirs = lib.stringAfter [ "users" ] ''
      mkdir -p ${configPath}/your-app
      chown -R ${bloudCfg.user}:users ${configPath}/your-app
    '';

    systemd.user.services.podman-your-app = mkPodmanService {
      name = "your-app";
      image = "someorg/someimage:1.2.3";
      ports = [ "${toString appCfg.port}:8080" ];
      environment = { SOME_VAR = "value"; };
      volumes = [ "${configPath}/your-app:/data:z" ];
      network = "apps-net";
      dependsOn = [ "apps-network" ];
    };
  };
}
```

`mkPodmanService` parameters:

```nix
mkPodmanService {
  name = "your-app";
  image = "org/image:tag";
  ports = [ "8080:80" ];              # host:container
  environment = { KEY = "value"; };
  volumes = [ "host:container:z" ];
  network = "apps-net";
  dependsOn = [ "apps-network" ];     # container dependencies
  waitFor = [                         # health check before starting
    { container = "postgres"; command = "pg_isready -U apps"; }
  ];
  extraAfter = [ "some.service" ];    # systemd ordering
  extraRequires = [ "some.service" ]; # hard systemd dependencies
}
```

---

## Step 3: configurator.go

The configurator runs as hooks around the container lifecycle. It handles setup that can't happen in Nix: writing config files, patching INI settings, making API calls, integrating with other apps.

Every configurator implements three hooks that the host-agent calls on every service start:

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

func (c *Configurator) Name() string {
    return "your-app"
}

// PreStart runs before the container starts.
// Use for: config files, directories, certificates.
// Must be idempotent — runs on every service start.
func (c *Configurator) PreStart(ctx context.Context, state *configurator.AppState) error {
    return nil
}

// HealthCheck waits for the app to be ready for configuration.
// Return nil when ready, error on timeout.
func (c *Configurator) HealthCheck(ctx context.Context) error {
    url := fmt.Sprintf("http://localhost:%d/health", c.port)
    return configurator.WaitForHTTP(ctx, url, configurator.DefaultHealthCheckTimeout)
}

// PostStart runs after the container is healthy.
// Use for: API calls, integrations, runtime configuration.
// Must be idempotent — runs on every service start.
func (c *Configurator) PostStart(ctx context.Context, state *configurator.AppState) error {
    return nil
}
```

### AppState fields

| Field | Description |
|---|---|
| `state.Name` | App name (e.g. `"qbittorrent"`) |
| `state.DataPath` | App data dir (e.g. `~/.local/share/bloud/your-app`) |
| `state.BloudDataPath` | Shared Bloud data dir (e.g. `~/.local/share/bloud`) |
| `state.Port` | Host port the app is exposed on |
| `state.Integrations` | Map of integration name → source app names |
| `state.Options` | App-specific config options from NixOS |

### Common PreStart patterns

**Write a default config file, preserve user edits:**

```go
func (c *Configurator) PreStart(ctx context.Context, state *configurator.AppState) error {
    configPath := filepath.Join(state.DataPath, "config.yaml")
    if _, err := os.Stat(configPath); os.IsNotExist(err) {
        if err := os.WriteFile(configPath, []byte(defaultConfig), 0644); err != nil {
            return fmt.Errorf("writing default config: %w", err)
        }
    }
    return nil
}
```

**Ensure required keys in an INI file without clobbering user settings:**

```go
func (c *Configurator) PreStart(ctx context.Context, state *configurator.AppState) error {
    configPath := filepath.Join(state.DataPath, "app.conf")
    ini, err := configurator.LoadINI(configPath)
    if err != nil {
        return fmt.Errorf("loading config: %w", err)
    }

    ini.EnsureKeys("Preferences", map[string]string{
        "WebUI\\HostHeaderValidation": "false",
        "WebUI\\CSRFProtection":       "false",
    })

    return ini.Save(configPath)
}
```

**Do something only when SSO is configured:**

```go
func (c *Configurator) PreStart(ctx context.Context, state *configurator.AppState) error {
    if _, hasSSO := state.Integrations["sso"]; !hasSSO {
        return nil
    }
    configPath := filepath.Join(c.traefikDir, "your-app-sso.yml")
    return os.WriteFile(configPath, []byte(ssoConfig), 0644)
}
```

### Common PostStart patterns

**Make API calls idempotently:**

```go
func (c *Configurator) PostStart(ctx context.Context, state *configurator.AppState) error {
    status, err := c.getStatus(ctx)
    if err != nil {
        return fmt.Errorf("getting status: %w", err)
    }
    if status.Configured {
        return nil // already done
    }
    return c.configure(ctx)
}
```

### Registering your configurator

After writing `configurator.go`, register it in `services/host-agent/internal/appconfig/register.go`:

```go
import (
    youapp "codeberg.org/d-buckner/bloud-v3/apps/your-app"
)

func RegisterAll(registry *configurator.Registry, cfg *config.Config) {
    // ... existing registrations ...
    registry.Register(youapp.NewConfigurator(8080))
}
```

The signature of `NewConfigurator` is up to you — pass whatever your configurator needs (port, data dir, API tokens, etc.).

---

## Step 4: test.ts

Integration tests verify your app works inside Bloud's embedding system. They don't test the app's own functionality — they test the integration surface: does it load? Are there CORS errors? Does the health check respond?

```typescript
import { test, expect, criticalErrors } from '../../integration/lib/app-test';

test.describe('your-app', () => {
  test('loads in iframe without errors', async ({ openApp, resourceErrors }) => {
    const frame = await openApp();

    await expect(frame.locator('body')).toBeVisible();
    expect(criticalErrors(resourceErrors)).toHaveLength(0);
  });

  test('health check responds', async ({ api, appName, embedPath, request }) => {
    await api.ensureAppRunning(appName);

    const response = await request.get(`${embedPath}health`);
    expect(response.ok()).toBe(true);
  });
});
```

The `app-test` helper auto-detects your app name from the file path (`apps/your-app/test.ts` → `"your-app"`).

### What the fixture provides

| Fixture | Description |
|---|---|
| `appName` | Auto-detected app name |
| `appPath` | `/apps/{appName}/` (UI route) |
| `embedPath` | `/embed/{appName}/` (iframe route) |
| `openApp()` | Navigates to the app page, waits for the iframe, returns a frame locator |
| `resourceErrors` | Tracks 404s and network failures during the test |
| `api` | Client for host-agent API interactions |
| `request` | Playwright `APIRequestContext` for HTTP calls |

### What to test

**Do test:**
- App loads at `/embed/<name>` without errors
- Health check endpoint responds
- No CSS/JS resource loading failures (404s, network errors)
- Basic navigation confirming the app works

**Don't test:**
- Internal app logic (login flows, data operations, settings)
- Features requiring external setup (API keys, database contents)
- Visual appearance or exact text content

### Running tests

```bash
# Make sure the dev environment is running first
./bloud start

# Run tests for your specific app
npx playwright test apps/your-app/test.ts

# Run all integration tests
npx playwright test
```

---

## Step 5: icon.png

A 256×256 PNG displayed in the app catalog and dashboard. Transparent background recommended — it looks good on both light and dark themes.

---

## Step 6: integration.md

Document anything that would help the next person when something breaks or needs changing.

Good things to include:

- **Port & network**: what ports are used, why host vs bridge networking
- **Auth**: first-run credentials, how SSO works
- **Volume mounts**: what's stored where
- **Non-obvious behavior**: quirks that required special handling
- **Traefik routes**: anything beyond the default embed route
- **Troubleshooting**: commands to run when things go wrong

See `apps/qbittorrent/integration.md` or `apps/adguard-home/integration.md` for examples.

---

## Rootless Podman and Host Services

Rootless Podman containers run in a user network namespace. System services (Postgres, Redis) run in the root network namespace. **There is no direct IP route between them.**

The `apps-net` bridge gateway is in the user netns — containers can't reach host services by IP. `host.containers.internal` DNS doesn't resolve. The host's external IP doesn't work either.

**The solution: mount the Unix socket.**

For Postgres:
- The socket lives at `/run/postgresql/.s.PGSQL.5432` (mode `0777`, world-accessible)
- Mount `/run/postgresql:/run/postgresql:ro` into the container
- Use a socket-style DATABASE_URL: `postgres://user@/dbname?host=/run/postgresql`

```nix
# module.nix
volumes = cfg: [
  "/run/postgresql:/run/postgresql:ro"
];
```

```go
// configurator.go or module.nix environment
DATABASE_URL = "postgres://apps@/yourapp?host=/run/postgresql"
```

---

## Checklist

### Files
- [ ] `metadata.yaml` — all required fields, image version pinned
- [ ] `module.nix` — creates working systemd service with `mkPodmanApp`
- [ ] `configurator.go` — implements `Configurator` interface, registered in `register.go`
- [ ] `test.ts` — integration tests using `app-test` fixture
- [ ] `icon.png` — 256×256 PNG, transparent background
- [ ] `integration.md` — documents quirks and debugging tips

### Configuration
- [ ] `port` in `metadata.yaml` matches default in `module.nix`
- [ ] `healthCheck.path` is correct and accessible through `/embed/<name>`
- [ ] Database URL uses socket path if connecting to host Postgres
- [ ] `git add apps/your-app/` — Nix flakes only see tracked files

### Testing
- [ ] `./bloud rebuild` succeeds (no Nix eval errors)
- [ ] `./bloud install your-app` succeeds
- [ ] App loads at `http://localhost/embed/your-app/`
- [ ] `./bloud shell "journalctl --user -u podman-your-app -n 50"` shows no errors
- [ ] `test.ts` passes

---

## Troubleshooting

### Service won't start

```bash
./bloud shell "systemctl --user status podman-your-app"
./bloud shell "journalctl --user -u podman-your-app --no-pager -n 50"
./bloud shell "podman ps -a"
./bloud shell "podman logs your-app"
```

Common causes:
- **Port conflict** — check if another service uses the same port
- **Image pull failed** — verify the image name and tag exist
- **Permission denied on volumes** — check `system.activationScripts` in your module
- **Stale failed state** — `systemctl --user reset-failed podman-your-app && systemctl --user start podman-your-app`

### App starts but isn't reachable

Test from inside the VM directly, then through Traefik:

```bash
./bloud shell "curl -v http://localhost:YOUR_PORT/"
./bloud shell "curl -v http://localhost/embed/your-app/"
```

If the first works but the second doesn't, it's a routing issue.

### Routing returns 404

```bash
# Verify Traefik generated the route
./bloud shell "cat ~/.local/share/bloud/traefik/dynamic/apps-routes.yml"
```

Check that the app is installed (`./bloud install your-app`) and that `metadata.yaml` `name` matches the directory name. Run `./bloud rebuild` after any Nix changes.

### Database connection fails

```bash
./bloud shell "psql -U apps -h 127.0.0.1 -l"
./bloud shell "podman exec your-app psql -U apps -h /run/postgresql -c 'SELECT 1'"
```

Verify your DATABASE_URL uses the socket path (`host=/run/postgresql`), not an IP address.

### Configurator isn't running

```bash
./bloud shell "journalctl --user -u podman-your-app --no-pager | grep -i configurator"
```

Verify `register.go` has `registry.Register(youapp.NewConfigurator(...))`.

### Nix file not found

Nix flakes only see git-tracked files. New files must be staged:

```bash
git add apps/your-app/
./bloud rebuild
```

### Nix syntax errors

```bash
nix-instantiate --parse apps/your-app/module.nix
```

Common mistakes: missing semicolons, unmatched braces, `=` where `:` is expected in function arguments.

---

## Reference: Existing Apps

Working examples are often the fastest way to understand a pattern.

| App | Interesting for... |
|---|---|
| `apps/qbittorrent` | Simple app; INI config patching in `PreStart` |
| `apps/miniflux` | Database integration; `BASE_URL` routing; conditional SSO env vars; env file for secrets |
| `apps/adguard-home` | Host networking; default config generation; unprivileged ports |
| `apps/jellyfin` | `extraServices` (plugin install); `extraConfig`; LDAP SSO; API-based setup wizard in `PostStart` |
| `apps/authentik` | Complex multi-container app; blueprint-based config; branding |
