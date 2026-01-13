# adding apps to bloud

bloud apps live in `apps/<name>/` and require five files: metadata, nix module, tests, icon, and documentation. this guide walks through each one.

## app directory structure

```
apps/your-app/
  metadata.yaml   # app definition and integrations
  module.nix      # nixos module to run the container
  test.ts         # playwright integration tests
  icon.png        # app icon for the ui (256x256)
  integration.md  # app-specific documentation
```

### what each file does

**metadata.yaml** - declares the app's identity, port, integrations, sso support, and routing behavior. the host-agent reads this to understand what the app needs and how to route traffic to it.

**module.nix** - the nixos module that creates systemd services for the container. handles startup ordering, health checks, environment variables, and volume mounts.

**test.ts** - playwright tests verifying the app works within bloud's embedding system. tests the integration surface (loading, health checks, no cors errors), not the app's full functionality.

**icon.png** - displayed in the app catalog and dashboard. 256x256 png with transparency works best.

**integration.md** - documents app-specific quirks, routing behavior, and debugging tips. useful for future you (or other contributors) when something breaks.

## metadata.yaml

here's a minimal example:

```yaml
name: your-app
displayName: Your App
description: What it does in one sentence
category: productivity
port: 8080

image: someorg/someimage:latest
integrations: {}

healthCheck:
  path: /health
  interval: 5
  timeout: 60
```

### required fields

| field | description |
|-------|-------------|
| `name` | lowercase, hyphenated identifier (`actual-budget`, `adguard-home`) |
| `displayName` | human-readable name for the ui |
| `description` | one-liner for the app catalog |
| `category` | `productivity`, `media`, `security`, `infrastructure` |
| `port` | host port the app listens on |
| `image` | docker image to run |
| `integrations` | what the app needs (see below) |
| `healthCheck` | how to verify the app is running |

### integrations

apps can declare dependencies on other apps. the two main integration types are `database` and `sso`:

```yaml
integrations:
  database:
    required: true
    multi: false
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

`required: true` means the app won't work without this integration. `multi: false` means it only connects to one provider (you wouldn't connect to two postgres instances).

if an app has no integrations, use an empty object:

```yaml
integrations: {}
```

### sso configuration

apps that support openid connect can declare their sso settings:

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

the `env` section maps bloud's sso configuration to the app's specific environment variable names. different apps expect different variable names for the same values.

### routing

apps are embedded in iframes at `/embed/<app-name>`. by default, traefik strips this prefix before forwarding to the app. some apps (like miniflux) can handle a base url and should receive the full path:

```yaml
routing:
  stripPrefix: false
```

apps that need special http headers (like wasm apps requiring cross-origin isolation):

```yaml
routing:
  headers:
    Cross-Origin-Opener-Policy: same-origin
    Cross-Origin-Embedder-Policy: credentialless
```

apps that need routes at the root level (like oauth callbacks that can't be prefixed) use `absolutePaths`. use sparingly—most apps should work through `/embed/<app-name>`:

```yaml
routing:
  absolutePaths:
    - rule: "PathPrefix(`/openid`)"
      priority: 90
      headers:
        X-Frame-Options: ""
```

### bootstrap

some apps need client-side pre-configuration before they load (like setting a server url in indexeddb). use the `bootstrap` field:

```yaml
bootstrap:
  indexedDB:
    database: actual
    entries:
      - store: asyncStorage
        key: server-url
        value: "{{embedUrl}}"
```

the `{{embedUrl}}` placeholder is replaced with the app's embed url at runtime.

### system apps

infrastructure apps that users don't interact with directly (postgres, traefik) should be marked as system apps:

```yaml
isSystem: true
```

system apps don't appear in the user-facing catalog and don't get traefik routes.

## module.nix

use the `mkBloudApp` helper to define your app:

```nix
{ config, pkgs, lib, ... }:

let
  mkBloudApp = import ../../nixos/lib/bloud-app.nix { inherit config pkgs lib; };
in
mkBloudApp {
  name = "your-app";
  description = "Your App description";

  image = "someorg/someimage:latest";
  port = 8080;
  containerPort = 80;  # container's internal port (defaults to port if same)

  # custom options (enable + port are automatic)
  options = {
    adminUser = { default = "admin"; description = "Admin username"; };
    adminPass = { default = "changeme"; description = "Admin password"; };
  };

  # environment variables - cfg contains all resolved values
  environment = cfg: {
    ADMIN_USER = cfg.adminUser;
    ADMIN_PASS = cfg.adminPass;
    BASE_URL = "${cfg.externalHost}:${toString cfg.traefikPort}/embed/your-app";
  };

  # auto-creates ~/.local/share/bloud/your-app:/data
  dataDir = true;

  # auto-creates database + init service
  database = "yourapp";

  dependsOn = [ "postgres" ];
  waitFor = [ { container = "apps-postgres"; command = "pg_isready -U apps"; } ];
}
```

the `cfg` object passed to `environment` includes:
- all custom options you defined (`cfg.adminUser`, etc.)
- `cfg.externalHost` - from `bloud.externalHost` (e.g., `http://localhost`)
- `cfg.traefikPort` - traefik's port (default 8080)
- `cfg.postgresUser` / `cfg.postgresPassword` - database credentials
- `cfg.configPath` - `~/.local/share/bloud`
- `cfg.appDataPath` - `~/.local/share/bloud/your-app`

#### mkBloudApp parameters

| parameter | required | description |
|-----------|----------|-------------|
| `name` | yes | app identifier (matches metadata.yaml) |
| `description` | yes | for the enable option |
| `image` | yes | container image |
| `port` | no | host port (omit for internal-only services like postgres) |
| `containerPort` | no | container's internal port (defaults to `port`) |
| `options` | no | custom nixos options `{ name = { default, description, type? }; }` |
| `environment` | no | function `cfg -> attrset` returning env vars |
| `volumes` | no | list of volume mounts, or function `cfg -> list` |
| `dataDir` | no | `true` for `/data`, or string for custom container path |
| `database` | no | database name (auto-creates postgres db + init service) |
| `dependsOn` | no | container dependencies (`"postgres"` becomes `"apps-postgres"`) |
| `waitFor` | no | health checks `[{ container, command }]` |
| `network` | no | podman network (default: `"apps-net"`) |
| `userns` | no | user namespace (e.g., `"keep-id:uid=70,gid=70"`) |
| `extraConfig` | no | additional nixos config, or function `cfg -> attrset` |

see `apps/miniflux/module.nix` and `apps/postgres/module.nix` for real examples.

### advanced: full module pattern

for complex apps that need more control (multiple containers, custom systemd config), you can use the full nix pattern:

```nix
{ config, pkgs, lib, ... }:

let
  appCfg = config.bloud.apps.your-app;
  bloudCfg = config.bloud;
  mkPodmanService = import ../../nixos/lib/podman-service.nix { inherit pkgs lib; };

  userHome = "/home/${bloudCfg.user}";
  configPath = "${userHome}/.local/share/bloud";
in
{
  options.bloud.apps.your-app = {
    enable = lib.mkEnableOption "Your App";

    port = lib.mkOption {
      type = lib.types.int;
      default = 8080;  # match metadata.yaml
      description = "Port to expose the app on";
    };
  };

  config = lib.mkIf appCfg.enable {
    # create data directories
    system.activationScripts.bloud-your-app-dirs = lib.stringAfter [ "users" ] ''
      mkdir -p ${configPath}/your-app
      chown -R ${bloudCfg.user}:users ${configPath}/your-app
    '';

    # run the container
    systemd.user.services.podman-your-app = mkPodmanService {
      name = "your-app";
      image = "someorg/someimage:latest";
      ports = [ "${toString appCfg.port}:8080" ];
      environment = {
        SOME_VAR = "value";
      };
      volumes = [ "${configPath}/your-app:/data:z" ];
      network = "apps-net";
      dependsOn = [ "apps-network" ];
    };
  };
}
```

### mkPodmanService

the `mkPodmanService` helper creates a systemd service for a podman container. it handles the common boilerplate: cleanup, health checks, restart policies.

```nix
mkPodmanService {
  name = "your-app";           # container name
  image = "org/image:tag";     # docker image
  ports = [ "8080:80" ];       # host:container port mapping
  environment = { ... };       # environment variables
  volumes = [ "host:container:z" ];  # volume mounts (z for selinux)
  network = "apps-net";        # podman network
  dependsOn = [ "apps-network" ];    # wait for these containers
  waitFor = [                  # health check before starting
    { container = "postgres"; command = "pg_isready -U apps"; }
  ];
  extraAfter = [ "some.service" ];    # systemd ordering
  extraRequires = [ "some.service" ]; # hard dependencies
}
```

### database integration

> **tip:** if using `mkBloudApp`, just set `database = "yourapp"` and it handles all of this automatically.

apps that need postgres follow this pattern:

```nix
{ config, pkgs, lib, ... }:

let
  # ... standard setup ...
in
{
  # ... options ...

  config = lib.mkIf appCfg.enable {
    # database initialization (runs once before app starts)
    systemd.user.services.your-app-db-init = {
      description = "Initialize your-app database";
      after = [ "podman-apps-postgres.service" ];
      requires = [ "podman-apps-postgres.service" ];
      before = [ "podman-your-app.service" ];
      wantedBy = [ "bloud-apps.target" ];
      partOf = [ "bloud-apps.target" ];
      serviceConfig = {
        Type = "oneshot";
        RemainAfterExit = true;
        ExecStart = pkgs.writeShellScript "your-app-db-init" ''
          set -e

          # wait for postgres
          for i in {1..30}; do
            if ${pkgs.podman}/bin/podman exec apps-postgres psql -U apps -d apps -c "SELECT 1" &>/dev/null; then
              break
            fi
            sleep 2
          done

          # create database (ignore if exists)
          ${pkgs.podman}/bin/podman exec apps-postgres psql -U apps -c "CREATE DATABASE yourapp;" 2>/dev/null || true
          ${pkgs.podman}/bin/podman exec apps-postgres psql -U apps -c "GRANT ALL PRIVILEGES ON DATABASE yourapp TO apps;" || true
        '';
      };
    };

    # main container
    systemd.user.services.podman-your-app = mkPodmanService {
      name = "your-app";
      image = "...";
      environment = {
        DATABASE_URL = "postgres://apps:testpass123@apps-postgres:5432/yourapp?sslmode=disable";
      };
      network = "apps-net";
      dependsOn = [ "apps-network" "apps-postgres" ];
      waitFor = [
        { container = "apps-postgres"; command = "pg_isready -U apps"; }
      ];
      extraAfter = [ "your-app-db-init.service" ];
      extraRequires = [ "your-app-db-init.service" ];
    };
  };
}
```

the key pieces:
1. a oneshot service creates the database before the app starts
2. `waitFor` ensures postgres is actually accepting connections
3. `extraAfter` and `extraRequires` ensure the db-init runs first
4. the app connects via the container network (`apps-postgres:5432`)

### conditional sso

apps that optionally support sso:

```nix
let
  authentikEnabled = config.bloud.apps.authentik.enable or false;
in
{
  # ...

  systemd.user.services.podman-your-app = mkPodmanService {
    environment = {
      # always set
      APP_URL = "http://localhost:${toString appCfg.port}";
    } // lib.optionalAttrs authentikEnabled {
      # only when authentik is enabled
      OIDC_CLIENT_ID = appCfg.openidClientId;
      OIDC_CLIENT_SECRET = appCfg.openidClientSecret;
      OIDC_DISCOVERY_URL = appCfg.openidDiscoveryUrl;
    };
    dependsOn = [ "apps-network" ] ++ lib.optional authentikEnabled "apps-authentik-server";
  };
}
```

## test.ts

integration tests verify your app works within bloud's embedding system. they don't test the app's full functionality—that's upstream's responsibility. focus on the integration surface: does it load? does it work in an iframe? are there cors issues?

use the `app-test` helper which automatically detects your app name from the file path:

```typescript
import { test, expect, criticalErrors } from '../../integration/lib/app-test';

test.describe('your-app', () => {
  test('loads in iframe without errors', async ({ openApp, resourceErrors }) => {
    const frame = await openApp();

    // wait for app-specific content (customize selector)
    await expect(frame.locator('body')).toBeVisible();

    // no CSS/JS loading failures
    expect(criticalErrors(resourceErrors)).toHaveLength(0);
  });

  test('health check responds', async ({ api, appName, embedPath, request }) => {
    await api.ensureAppRunning(appName);

    const response = await request.get(`${embedPath}health`);
    expect(response.ok()).toBe(true);
  });
});
```

the helper provides:
- `appName` - auto-detected from file path (`apps/miniflux/test.ts` → `"miniflux"`)
- `appPath` - `/apps/{appName}/` (UI route)
- `embedPath` - `/embed/{appName}/` (iframe route)
- `openApp()` - navigates to app, waits for iframe, returns frame locator
- `resourceErrors` - tracks 404s and network failures
- `api` - client for backend interactions
- `request` - playwright request context for HTTP calls

see `apps/miniflux/test.ts` and `apps/actual-budget/test.ts` for real examples.

### what to test

**do test:**
- app loads at `/embed/<app-name>` without errors
- health check endpoint responds
- no CSS/JS resource loading failures
- basic navigation works

**don't test:**
- the app's internal functionality (login flows, data operations)
- features that require external setup (database contents, api keys)
- visual appearance or styling

### running tests

```bash
# from project root
./test your-app          # test specific app
./test                   # run all tests
```

make sure the dev environment is running (`./lima/dev start`) before running tests.

## routing and embedding

all apps are embedded in the bloud ui via iframes. requests flow like this:

```
browser → traefik (8080) → /embed/your-app → your-app container
```

traefik automatically generates routes based on metadata.yaml. by default it strips `/embed/your-app` before forwarding, so your app receives requests at `/`.

### apps with BASE_URL support

some apps (like miniflux) can serve from a subpath. set `BASE_URL` and disable prefix stripping:

```yaml
# metadata.yaml
routing:
  stripPrefix: false
```

```nix
# module.nix
environment = {
  BASE_URL = "http://localhost:8080/embed/your-app";
};
```

now miniflux generates links like `/embed/miniflux/feeds` instead of `/feeds`.

### apps without BASE_URL support

apps that hardcode absolute paths (like adguard home redirecting to `/install.html`) are handled by a service worker. you don't need to do anything special—just don't set routing options.

the service worker intercepts requests from the iframe and rewrites absolute paths to include the app prefix. this happens transparently.

## testing your app

1. add your app to `nixos/generated/apps.nix`:

```nix
bloud.apps.your-app.enable = true;
```

2. rebuild and start services:

```bash
./lima/dev rebuild
./lima/dev restart
```

3. check the service:

```bash
./lima/dev shell "systemctl --user status podman-your-app"
./lima/dev shell "journalctl --user -u podman-your-app -f"
```

4. verify routing:

```bash
curl http://localhost:8080/embed/your-app/
```

5. check in the ui at `http://localhost:8080`

## common patterns

### host networking

apps that need to bind to specific ports (like dns on port 53) use host networking instead of the apps-net bridge:

```nix
systemd.user.services.podman-your-app = {
  # manual service definition instead of mkPodmanService
  serviceConfig = {
    ExecStart = ''
      ${pkgs.podman}/bin/podman run \
        --network=host \
        ...
    '';
  };
};
```

### kernel parameters

apps needing low ports (< 1024) with rootless podman:

```nix
config = lib.mkIf appCfg.enable {
  boot.kernel.sysctl."net.ipv4.ip_unprivileged_port_start" = 53;
  # ...
};
```

### selinux volume labels

always use `:z` or `:Z` suffix on volume mounts for selinux compatibility:

```nix
volumes = [ "${configPath}/your-app:/data:z" ];
```

`:z` allows sharing between containers, `:Z` is private to one container.

## checklist

before submitting:

**files:**
- [ ] `metadata.yaml` has all required fields
- [ ] `module.nix` creates working systemd service
- [ ] `test.ts` has integration tests
- [ ] `icon.png` added (256x256, transparent background)
- [ ] `integration.md` documents the app

**configuration:**
- [ ] `port` in metadata matches default in module.nix
- [ ] container image is pinned or uses `:latest`
- [ ] data directories created with correct ownership
- [ ] database integration uses the standard pattern (if applicable)
- [ ] healthCheck path is correct

**testing:**
- [ ] service starts cleanly after `./lima/dev rebuild`
- [ ] app works in iframe embedding
- [ ] health check endpoint responds
- [ ] `test.ts` passes (`npx playwright test apps/your-app/test.ts`)

## troubleshooting

### service won't start

check the service status and logs:

```bash
./lima/dev shell "systemctl --user status podman-your-app"
./lima/dev shell "journalctl --user -u podman-your-app --no-pager -n 50"
```

common issues:
- **port conflict**: another service using the same port
- **image pull failed**: check network connectivity, image name
- **permission denied**: volumes need correct ownership (see activation scripts)

### container starts but app doesn't respond

check if the container is actually running:

```bash
./lima/dev shell "podman ps -a"
./lima/dev shell "podman logs your-app"
```

test from inside the vm:

```bash
./lima/dev shell "curl -v http://localhost:YOUR_PORT/"
```

### routing issues

verify traefik routes are generated:

```bash
./lima/dev shell "cat ~/.local/share/bloud/traefik/dynamic/apps-routes.yml"
```

test the embed path:

```bash
curl -v http://localhost:8080/embed/your-app/
```

if you get 404, check:
- metadata.yaml `name` matches the path
- app is enabled in `nixos/generated/apps.nix`
- traefik config was regenerated (`./lima/dev rebuild`)

### database connection fails

verify postgres is running and database exists:

```bash
./lima/dev shell "podman exec apps-postgres psql -U apps -l"
./lima/dev shell "podman exec apps-postgres psql -U apps -d yourapp -c 'SELECT 1'"
```

check your app's DATABASE_URL matches the database name.

### nix syntax errors

if rebuild fails with nix errors, check syntax:

```bash
./lima/dev shell "nix-instantiate --parse /home/bloud.linux/bloud-v3/apps/your-app/module.nix"
```

common mistakes:
- missing semicolons after attribute values
- unmatched braces or brackets
- using `=` instead of `:` in function arguments

### new file not found by nix

nix flakes only see git-tracked files. if you created a new file:

```bash
git add apps/your-app/module.nix
./lima/dev rebuild
```
