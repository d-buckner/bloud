# Plan: Standalone Proxy Outpost for Tailnet Auth

## Problem

When a remote user accesses a shared app via tailnet (e.g. `navidrome.tail12756a.ts.net`),
the Authentik embedded outpost redirects the browser to `http://localhost:8080` for login.
This is unreachable from a remote device. The embedded outpost has a single `authentik_host`
config that controls browser redirects for ALL providers — it can't serve both
`localhost:8080` (local) and `https://bloud.{tailnet_domain}` (remote) simultaneously.

## Solution

Run a **standalone proxy outpost container** dedicated to tailnet auth, paralleling the
existing LDAP outpost pattern. The embedded outpost continues handling local auth unchanged.

### Request Flow (Remote User)

```
Browser → navidrome.tail12756a.ts.net
  → ts-navidrome (TS Serve) → Traefik (localhost:8080)
  → tailnet router (priority 250) → tailnet-forwardauth middleware
  → standalone outpost (localhost:9002) → "not authenticated"
  → 302 → bloud.tail12756a.ts.net/application/o/authorize/...

Browser → bloud.tail12756a.ts.net
  → ts-gateway (TS Serve) → Traefik
  → bloud.* router → Authentik (localhost:9001)
  → user logs in → redirect to bloud.tail12756a.ts.net/outpost.goauthentik.io/callback

Browser → bloud.tail12756a.ts.net/outpost.goauthentik.io/callback
  → ts-gateway → Traefik
  → outpost callback router (priority 300) → standalone outpost (localhost:9002)
  → code exchange, set cookie (Domain=tail12756a.ts.net)
  → 302 back to navidrome.tail12756a.ts.net

Browser → navidrome.tail12756a.ts.net (with cookie)
  → forward-auth succeeds → proxied to app
```

Local auth (`navidrome.localhost:8080`) is completely unchanged — existing routers at
priority 200 with the embedded outpost handle it.

## Implementation Steps

### Step 1: Authentik Client — Create/Ensure Proxy Outpost

**File: `services/host-agent/pkg/authentik/client.go`**

Modify `EnsureForwardDomainAuth(cookieDomain string)` to return the outpost token and
stop adding the provider to the embedded outpost:

```go
func (c *Client) EnsureForwardDomainAuth(cookieDomain string) (token string, err error)
```

Changes:
- After creating/finding the forward_domain provider and application (unchanged)
- Replace `AddProviderToEmbeddedOutpost` with `ensureProxyOutpost` (new method)
- `ensureProxyOutpost`: follows the `ensureLDAPOutpost` pattern — creates a proxy-type
  outpost named "Bloud Tailnet Proxy Outpost" with the forward_domain provider attached
- Retrieve the outpost token via `GetProxyOutpostToken` (follows `GetLDAPOutpostToken` pattern)
- Return the token

New methods:
- `ensureProxyOutpost(providerID int) error` — creates outpost if not exists
- `GetProxyOutpostToken() (string, error)` — retrieves auto-generated `ak-outpost-{pk}-api` token

### Step 2: Update ForwardDomainProvisioner Interface

**File: `services/host-agent/internal/reconciler/converge.go`**

Change the interface to return the outpost token and tailnet domain:

```go
type ForwardDomainProvisioner interface {
    EnsureForwardDomainAuth(cookieDomain string) (token string, err error)
}
```

Update `provisionTailnetSSO` to:
1. Discover the tailnet domain (unchanged)
2. Call `EnsureForwardDomainAuth(domain)` → get the outpost token
3. Return the token + domain for container startup and route generation

### Step 3: Standalone Outpost Container Lifecycle

**File: `services/host-agent/internal/sharing/proxy_outpost.go` (new)**

Create a `ProxyOutpostManager` that manages the standalone proxy outpost container.
Follows the `GatewayManager` / `TailnetNodeManager` pattern.

```go
type ProxyOutpostManagerInterface interface {
    EnsureRunning(ctx context.Context, token, tailnetDomain string) error
    Stop(ctx context.Context) error
}

type ProxyOutpostManager struct {
    containers  container.Runtime
    dataDir     string
    logger      *slog.Logger
}
```

Container spec:
- Name: `apps-authentik-proxy-outpost`
- Image: `ghcr.io/goauthentik/proxy:2025.10.3` (same version as the authentik server)
- Network: `apps-net` (to reach `apps-authentik-server:9000`)
- Port: `9002:9000`
- Environment:
  - `AUTHENTIK_HOST=http://apps-authentik-server:9000`
  - `AUTHENTIK_HOST_BROWSER=https://bloud.{tailnet_domain}`
  - `AUTHENTIK_TOKEN={outpost_token}`
  - `AUTHENTIK_INSECURE=true`

### Step 4: Wire Outpost into Convergence Loop

**File: `services/host-agent/internal/reconciler/converge.go`**

Add `ProxyOutpost ProxyOutpostEnsurer` to the `Config` struct.

Update `provisionTailnetSSO` to start the outpost container after provisioning SSO:

```go
func (r *Reconciler) provisionTailnetSSO(ctx context.Context) {
    // ... discover domain, provision forward_domain SSO ...
    token, err := cfg.ForwardDomainSSO.EnsureForwardDomainAuth(domain)
    // ... start standalone outpost container ...
    if cfg.ProxyOutpost != nil {
        cfg.ProxyOutpost.EnsureRunning(ctx, token, domain)
    }
}
```

Also update `convergeTailnet` teardown to stop the proxy outpost when tailnet is torn down.

**File: `services/host-agent/internal/api/server.go`**

Wire the `ProxyOutpostManager` into the reconciler config, similar to how
`gateway` and `tailnetNode` are wired.

### Step 5: Traefik Route Generation for Tailnet

**File: `services/host-agent/internal/traefikgen/generator.go`**

Add a `tailnetDomain` field to the Generator (set during route generation).

Extend `GenerateAll` signature:

```go
func (g *Generator) GenerateAll(apps []*catalog.App, remoteApps []RemoteAppRoute, tailnetDomain string) error
```

When `tailnetDomain` is non-empty AND authentik is enabled, generate additional routes:

**a) Per forward-auth app — tailnet-specific router (priority 250):**
```yaml
navidrome-tailnet:
  rule: "Host(`navidrome.{tailnetDomain}`)"
  priority: 250
  middlewares:
    - tailnet-forwardauth
  service: navidrome

navidrome-tailnet-outpost:
  rule: "Host(`navidrome.{tailnetDomain}`) && PathPrefix(`/outpost.goauthentik.io/`)"
  priority: 300
  service: tailnet-outpost
```

**b) Gateway domain routes — Authentik login + outpost callback:**
```yaml
tailnet-outpost-callback:
  rule: "Host(`bloud.{tailnetDomain}`) && PathPrefix(`/outpost.goauthentik.io/`)"
  priority: 300
  service: tailnet-outpost

tailnet-authentik:
  rule: "Host(`bloud.{tailnetDomain}`)"
  priority: 200
  service: authentik-web
```

**c) Middleware + services:**
```yaml
middlewares:
  tailnet-forwardauth:
    forwardAuth:
      address: "http://localhost:9002/outpost.goauthentik.io/auth/traefik"
      trustForwardHeader: true
      authResponseHeaders:
        - X-authentik-username
        - X-authentik-groups
        - X-authentik-email
        - X-authentik-name
        - X-authentik-uid

services:
  tailnet-outpost:
    loadBalancer:
      servers:
        - url: "http://localhost:9002"
  authentik-web:
    loadBalancer:
      servers:
        - url: "http://localhost:9001"
```

### Step 6: Pass Tailnet Domain to Route Generation

**File: `services/host-agent/internal/orchestrator/orchestrator_portable.go`**

In `RegenerateRoutes()`, discover the tailnet domain (if gateway is running) and pass
it to `GenerateAll`:

```go
var tailnetDomain string
if o.gateway != nil && o.activeTailnetID != nil && o.activeTailnetID() != "" {
    if domain, err := o.gateway.GetTailnetDomain(context.Background()); err == nil {
        tailnetDomain = domain
    }
}
return o.traefikGen.GenerateAll(apps, remoteRoutes, tailnetDomain)
```

This requires the orchestrator to hold a reference to the gateway (it already does).

### Step 7: Update Tests

- `traefikgen/generator_test.go` — test tailnet route generation
- `reconciler/converge_test.go` — test provisionTailnetSSO starts outpost container
- `reconciler/fakes_test.go` — add FakeProxyOutpost, update ForwardDomainProvisioner fake
- `authentik/client.go` — test proxy outpost creation and token retrieval
- `sharing/proxy_outpost_test.go` — test container spec generation

## Design Decisions

1. **Port 9002** for the standalone outpost (9001 is Authentik server, 9000 is internal)
2. **`apps-net` network** — outpost reaches Authentik via container name
   (`apps-authentik-server:9000`), matches LDAP outpost pattern
3. **Same Authentik version** (`2025.10.3`) for proxy outpost image to avoid
   version skew
4. **Per-app tailnet routers** rather than a catch-all — gives correct Traefik
   service routing per app and limits scope to forward-auth apps only
5. **Outpost lifecycle coupled to tailnet** — starts in `provisionTailnetSSO`,
   stops in `convergeTailnet` teardown
6. **`GenerateAll` gains a `tailnetDomain` parameter** — cleanest way to pass the
   domain down to route generation without adding state to the Generator struct

## Files Changed

| File | Change |
|------|--------|
| `pkg/authentik/client.go` | New `ensureProxyOutpost`, `GetProxyOutpostToken`; modify `EnsureForwardDomainAuth` return type |
| `internal/reconciler/converge.go` | Update `ForwardDomainProvisioner` interface; add `ProxyOutpost` to Config; update `provisionTailnetSSO` |
| `internal/sharing/proxy_outpost.go` | **New file** — `ProxyOutpostManager` container lifecycle |
| `internal/traefikgen/generator.go` | Tailnet router/middleware/service generation |
| `internal/traefikgen/interfaces.go` | Update `GeneratorInterface` if it exists |
| `internal/orchestrator/orchestrator_portable.go` | Pass tailnet domain to `GenerateAll`; hold gateway ref for domain lookup |
| `internal/api/server.go` | Wire `ProxyOutpostManager` into reconciler config |
| `internal/reconciler/fakes_test.go` | Update fakes |
| `internal/traefikgen/generator_test.go` | Tailnet route tests |
| `internal/reconciler/converge_test.go` | Outpost lifecycle tests |
