# Plan: Persistent Proxy Ports, Gateway FQDN, and Owner Remote Access

**Status:** Draft
**Last updated:** 2026-06-23

---

## Summary

Three related changes to the sharing/gateway infrastructure, each building on the
previous:

1. **Persistent proxy ports** — Store `proxy_port` on the `remote_apps` table so ports
   survive restarts and don't shift when apps are added/removed.
2. **Gateway FQDN discovery** — After the gateway joins the tailnet, discover and persist
   its FQDN (e.g. `ts-gateway.tail1275sa.ts.net`) on the `tailnet_connections` record.
3. **Owner remote access** — The gateway exposes local Traefik via Tailscale Serve so the
   owner can access the full dashboard and app subdomains remotely.

These are the "Not Yet Implemented" items from SPEC.md §Sharing.

---

## 1. Persistent Proxy Ports

### Problem

`RemoteProxyManager.Reconcile()` assigns ports ephemerally by sorting targets by ID and
counting from `basePort`. Adding or removing a remote app shifts every port above the
insertion/deletion point. This means:

- Traefik routes point to stale ports until regenerated
- Any client holding a port reference breaks
- Non-deterministic across restarts if the set of remote apps changes

### Design

Add a `proxy_port` column to `remote_apps`. Assign monotonically at creation time using
`MAX(proxy_port) + 1`, starting from 10100. Ports are never reused.

#### Schema Change

```sql
CREATE TABLE IF NOT EXISTS remote_apps (
    id                   TEXT PRIMARY KEY,
    host_label           TEXT NOT NULL,
    app_id               TEXT NOT NULL,
    app_name             TEXT NOT NULL,
    sso_strategy         TEXT NOT NULL,
    bypass_paths         TEXT NOT NULL DEFAULT '[]',
    sidecar_tailnet_addr TEXT NOT NULL,
    encrypted_cred       BLOB,
    proxy_port           INTEGER NOT NULL,          -- NEW
    status               TEXT NOT NULL DEFAULT 'pending_credential',
    created_at           TEXT DEFAULT (datetime('now'))
);
```

Not a migration — this is pre-release, so the table definition changes directly.

#### Store Changes (`store/remote_apps.go`)

- Add `ProxyPort int` to `RemoteApp` struct.
- `Create()`: call `NextProxyPort()` if `ProxyPort` is 0, then INSERT.
- `NextProxyPort() int`: `SELECT COALESCE(MAX(proxy_port), 10099) + 1 FROM remote_apps`.
- Update all scan functions to include `proxy_port`.

#### API Changes (`api/remote_apps.go`)

- `handleAddRemoteApp`: call `store.NextProxyPort()` to assign the port before
  `store.Create()`. The port is part of the created record.
- `handleListRemoteApps`: `proxy_port` is already on the struct, serialized in JSON.

#### Orchestrator Changes (`orchestrator_portable.go`)

- `RegenerateRoutes()`: read `proxy_port` from each `RemoteApp` record and pass it to the
  `RemoteProxyManager` instead of letting Reconcile compute ports.

#### RemoteProxyManager Changes (`sharing/remoteproxy.go`)

- `ProxyTarget` gains a `Port int` field.
- `Reconcile()` uses `target.Port` directly instead of computing `basePort + i`. The sort
  and index-based assignment logic is removed.
- `basePort` field is removed from the struct (no longer needed).

#### Tests

- `store/remote_apps_test.go`: test `NextProxyPort()` returns 10100 for first app,
  monotonically increments, doesn't reuse after delete.
- `sharing/remoteproxy_test.go`: update to pass explicit ports in `ProxyTarget`.
- `orchestrator_portable_test.go`: update `RegenerateRoutes` test to verify ports from
  store are passed through.

### Files

| File | Change |
|------|--------|
| `internal/db/schema.sql` | Add `proxy_port` column |
| `internal/store/remote_apps.go` | Add `ProxyPort` field, `NextProxyPort()`, update scans |
| `internal/store/remote_apps_test.go` | Test monotonic port allocation |
| `internal/api/remote_apps.go` | Assign port on create |
| `internal/sharing/remoteproxy.go` | Use explicit port from target, remove basePort |
| `internal/sharing/remoteproxy_test.go` | Update for explicit ports |
| `internal/orchestrator/orchestrator_portable.go` | Pass `proxy_port` from store to proxy target |

---

## 2. Gateway FQDN Discovery

### Problem

After the gateway joins the tailnet, we need its FQDN (e.g. `ts-gateway.tail1275sa.ts.net`)
for owner remote access. Currently the gateway container starts but we never query its
identity.

### Design

After `gateway.EnsureRunning()` succeeds, exec into the container to discover the FQDN
and persist it on the `tailnet_connections` record.

#### Schema Change

```sql
CREATE TABLE IF NOT EXISTS tailnet_connections (
    id           TEXT PRIMARY KEY,
    name         TEXT NOT NULL,
    type         TEXT NOT NULL,
    auth_key     TEXT NOT NULL,
    control_url  TEXT NOT NULL DEFAULT '',
    gateway_fqdn TEXT NOT NULL DEFAULT '',    -- NEW
    status       TEXT NOT NULL DEFAULT 'active',
    created_at   TEXT DEFAULT (datetime('now'))
);
```

#### Store Changes (`store/tailnet.go`)

- Add `GatewayFQDN string` to `TailnetConnection` struct.
- Add `SetGatewayFQDN(id, fqdn string) error` method.
- Update all scan functions to include `gateway_fqdn`.

#### GatewayManager Changes (`sharing/gateway.go`)

The gateway needs a way to discover its FQDN. Two approaches:

**Option A — Exec into container:** Add a `GetFQDN(ctx) (string, error)` method that runs
`tailscale status --json` inside the container and parses the `Self.DNSName` field. This
requires the `ContainerExec` interface (already used by `SidecarManager`).

**Option B — Poll from outside:** The orchestrator calls `tailscale status --json` from
outside and matches the gateway node by hostname. Less clean — requires the orchestrator
to know container exec details.

**Recommendation: Option A.** The gateway manager already owns the container lifecycle;
it should also own identity discovery. Add `ContainerExec` to `GatewayManager` (same
interface `SidecarManager` uses).

```go
// GetFQDN returns the gateway's tailnet FQDN by querying tailscale status inside
// the container. Returns empty string if the gateway hasn't joined yet.
func (m *GatewayManager) GetFQDN(ctx context.Context) (string, error) {
    out, err := m.exec.Exec(ctx, gatewayContainerName, []string{"tailscale", "status", "--json"})
    if err != nil {
        return "", fmt.Errorf("exec tailscale status: %w", err)
    }
    var status struct {
        Self struct {
            DNSName string `json:"DNSName"`
        } `json:"Self"`
    }
    if err := json.Unmarshal(out, &status); err != nil {
        return "", fmt.Errorf("parse tailscale status: %w", err)
    }
    // DNSName has a trailing dot (e.g. "ts-gateway.tail1275sa.ts.net.")
    return strings.TrimSuffix(status.Self.DNSName, "."), nil
}
```

#### Orchestrator Integration

In `RegenerateRoutes()`, after `gateway.EnsureRunning()` succeeds:

```go
if o.gateway != nil && o.tailnetStore != nil {
    fqdn, err := o.gateway.GetFQDN(ctx)
    if err == nil && fqdn != "" {
        active, _ := o.tailnetStore.GetActive()
        if active != nil && active.GatewayFQDN != fqdn {
            o.tailnetStore.SetGatewayFQDN(active.ID, fqdn)
        }
    }
}
```

This is idempotent — if the FQDN hasn't changed, no write. If the gateway isn't ready
yet, the error is logged and we proceed without it.

#### Tests

- `store/tailnet_test.go`: test `SetGatewayFQDN()`.
- `sharing/gateway_test.go`: test `GetFQDN()` with mock exec returning valid/invalid JSON.

### Files

| File | Change |
|------|--------|
| `internal/db/schema.sql` | Add `gateway_fqdn` column |
| `internal/store/tailnet.go` | Add `GatewayFQDN` field, `SetGatewayFQDN()`, update scans |
| `internal/store/tailnet_test.go` | Test FQDN persistence |
| `internal/sharing/gateway.go` | Add `ContainerExec` dependency, `GetFQDN()` method |
| `internal/sharing/gateway_test.go` | Test FQDN parsing |
| `internal/orchestrator/orchestrator_portable.go` | Discover + persist FQDN after EnsureRunning |

---

## 3. Owner Remote Access

### Problem

The owner wants to access their Bloud dashboard and app subdomains remotely (away from
the LAN) through the tailnet, without installing Tailscale on every device. The gateway
container is already on the tailnet — it just needs to serve traffic.

### Design

The gateway runs Tailscale Serve to forward incoming tailnet HTTPS requests to local
Traefik (`localhost:8080`). The owner accesses `https://ts-gateway.tail1275sa.ts.net`
from any tailnet-connected device and gets the full dashboard. App subdomains
(e.g. `jellyfin.bloud.local`) are resolved via Tailscale's MagicDNS and sslip.io.

#### Architecture

```text
Owner's phone (on tailnet)
    ↓ https://ts-gateway.tail1275sa.ts.net
ts-gateway container (Tailscale Serve: 443 → localhost:8080)
    ↓
Traefik (:8080)
    ↓ routes based on Host header
App containers (local) or remote proxies
```

#### DNS for App Subdomains (Open Question)

The dashboard at `https://ts-gateway.tail1275sa.ts.net` works immediately — Tailscale
Serve handles the TLS cert and proxies to Traefik. But app subdomains like
`jellyfin.bloud.local` won't resolve from outside the LAN.

**Options:**

**A. Single-origin mode.** The gateway FQDN serves everything. Traefik matches all routes
on a single wildcard host, or the owner navigates embedded app frames from the dashboard.
No DNS changes needed. Simplest.

**B. sslip.io.** Encode the gateway's tailnet IP in a sslip.io domain. E.g., if the
gateway has IP `100.64.1.5`, then `jellyfin.100-64-1-5.sslip.io` resolves to `100.64.1.5`.
Traefik needs routes matching these hostnames. Each app gets a sslip.io-based route in
addition to its `.bloud.local` route. More complex, requires gateway IP discovery.

**C. Tailscale split DNS.** Configure the tailnet's DNS to resolve `*.bloud.local` to the
gateway. Requires Tailscale admin console or Headscale config. Not automatable from Bloud.

**Recommendation: Option A for now.** Single-origin via the gateway FQDN is simplest and
doesn't require DNS changes. The dashboard already embeds apps under `/embed/{appName}/`
paths, so all apps are accessible from the single origin. If subdomain-based remote
access is needed later, Option B (sslip.io) can be added incrementally.

#### Tailscale Serve Config on Gateway

The gateway container needs a Tailscale Serve config, same pattern as app sidecars.
Mount a serve config JSON file into the container:

```json
{
  "TCP": { "443": { "HTTPS": true } },
  "Web": {
    "${TS_CERT_DOMAIN}:443": {
      "Handlers": {
        "/": { "Proxy": "http://localhost:8080" }
      }
    }
  }
}
```

This serves the entire local Traefik frontend via the gateway's tailnet HTTPS endpoint.
Because the gateway runs on host network, `localhost:8080` reaches Traefik directly.

#### GatewayManager Changes

- Write serve config to `{dataDir}/ts-gateway/ts-serve/serve.json`.
- Add `TS_SERVE_CONFIG=/etc/ts-serve/serve.json` to container env.
- Add mount: `{dataDir}/ts-gateway/ts-serve` → `/etc/ts-serve` (ro).
- This means the gateway now needs `dataDir` as a constructor parameter (same as
  SidecarManager).

Updated container spec:

```go
spec := container.Spec{
    Name:    gatewayContainerName,
    Image:   "docker.io/tailscale/tailscale:latest",
    Network: "host",
    Environment: map[string]string{
        "TS_AUTHKEY":       authKey,
        "TS_HOSTNAME":      gatewayContainerName,
        "TS_USERSPACE":     "true",
        "TS_SOCKS5_SERVER": fmt.Sprintf(":%d", m.socksPort),
        "TS_EXTRA_ARGS":    "--accept-routes",
        "TS_SERVE_CONFIG":  "/etc/ts-serve/serve.json",
    },
    Mounts: []container.Mount{
        {
            Source:      serveConfigDir,
            Destination: "/etc/ts-serve",
            Options:     []string{"ro"},
        },
    },
    Labels: map[string]string{
        "io.bloud.gateway": "true",
    },
    RestartPolicy: "always",
}
```

#### Traefik Route Changes

Traefik already serves all apps on `*.bloud.local` subdomains. For owner remote access
via the gateway FQDN, the dashboard route needs to also match the gateway's hostname.

In `RegenerateRoutes()`, if a gateway FQDN is known, add it as an alternative host match
on the dashboard router:

```yaml
# In apps-routes.yml, the dashboard router
http:
  routers:
    dashboard:
      rule: "Host(`bloud.local`) || Host(`ts-gateway.tail1275sa.ts.net`)"
      service: dashboard
```

For Option A (single-origin), this is all that's needed. The dashboard serves embedded
apps under `/embed/` paths, so all apps are accessible through the gateway FQDN.

#### TraefikGen Changes

- `GenerateAll()` accepts an optional `gatewayFQDN string` parameter (or it's added to the
  struct as config). When non-empty, the dashboard router adds the gateway FQDN as an
  alternative Host match.
- No per-app route changes needed for Option A — apps are embedded.

#### Settings API Changes

- `tailnetResponse` gains a `GatewayFQDN string` field (read-only, discovered by system).
- Frontend can display "Remote access URL: https://ts-gateway.tail1275sa.ts.net" on the
  Settings page once the FQDN is known.

#### Frontend Changes

- Settings page: show the gateway FQDN as a read-only field when available (with a label
  like "Remote Access URL").
- No other UI changes for Option A — the existing embedded app routing handles everything.

#### Tests

- `sharing/gateway_test.go`: verify serve config is written and mounted.
- `traefikgen/generator_test.go`: verify gateway FQDN appears in dashboard router rule.
- `api/settings_test.go`: verify `gatewayFqdn` in response when set.

### Files

| File | Change |
|------|--------|
| `internal/sharing/gateway.go` | Add `dataDir`, write serve config, mount it |
| `internal/sharing/gateway_test.go` | Test serve config generation and mount |
| `internal/traefikgen/generator.go` | Add gateway FQDN to dashboard router |
| `internal/traefikgen/generator_test.go` | Test gateway FQDN in route |
| `internal/traefikgen/interfaces.go` | Add `SetGatewayFQDN(fqdn string)` or param |
| `internal/orchestrator/orchestrator_portable.go` | Pass FQDN to traefik gen |
| `internal/api/settings.go` | Include `gatewayFqdn` in response |
| `web/src/lib/clients/settingsClient.ts` | Add `gatewayFqdn` to type |
| `web/src/routes/settings/+page.svelte` | Display remote access URL |

---

## Dependency Graph

```
1. Persistent proxy ports
   └─ standalone, no dependencies

2. Gateway FQDN discovery
   └─ standalone, no dependencies on (1)

3. Owner remote access
   ├─ depends on (2): needs gateway FQDN for Traefik route + Serve config
   └─ no dependency on (1)
```

Items 1 and 2 can be implemented in parallel. Item 3 depends on 2.

---

## Verification

```bash
# After each item, all existing tests must pass:
cd services/host-agent && go build ./... && go test ./... -count=1

# Item 1 — persistent ports:
#   - Create two remote apps, note their proxy_ports (10100, 10101)
#   - Delete the first, create a third
#   - Third app gets 10102 (not 10100 — never reuse)
#   - Restart host-agent — ports unchanged

# Item 2 — gateway FQDN:
#   - Save tailnet connection, add a remote app (triggers gateway start)
#   - After gateway joins: GET /api/settings/tailnet → gatewayFqdn populated
#   - limactl shell bloud-dev podman exec ts-gateway tailscale status --json
#     → Self.DNSName matches stored FQDN

# Item 3 — owner remote access:
#   - From a tailnet-connected device, curl https://<gateway-fqdn>
#   - Should return the Bloud dashboard HTML
#   - Embedded apps (/embed/jellyfin/) should work through the gateway
```
