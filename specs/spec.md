# Bloud First Release Specification

**Status:** Authoritative active plan
**Last updated:** 2026-07-03
**Product:** Portable single-host home cloud\
**Initial target:** Debian 13, `x86_64`, systemd\
**Primary risk:** Unreliable implementation and architecture\
**Release strategy:** Preserve Bloud's integration model on a small, tested host runtime

## Document Authority

This file is the source of truth for product scope, architecture, migration policy, delivery
order, and release gates. When another document or the current implementation conflicts with
this specification, update or replace the conflicting material. Do not silently change this
specification through implementation.

Supporting documents provide rationale, detail, and implementation records:

- `docs/architecture/overview.md`: component overview and diagram
- `docs/guides/contributing-apps.md`: how to add a new app
- `docs/features/sharing.md`: sharing architecture and federation design
- `specs/reconciler-spec.md`: intent-driven reconciler architecture (implemented)

Supporting documents must defer to this file and must not introduce new authoritative
requirements.

Any change to release scope, supported environment, architecture boundaries, migration
policy, phase order, or release gates must update this file in the same commit. Completed
slices update the migration checkpoint and phase-status table.

## Current Migration Checkpoint

Completed:

- First-release product scope and Debian runtime direction are defined.
- Typed integration resolution exists in a runtime-neutral package. Resolution accepts
  declared requirements, planner-created bindings, and installed-app membership, and returns
  one provider binding per integration type.
- Undeclared bindings, incompatible providers, and missing required bindings fail explicitly.
- Optional unbound integrations automatically resolve the first installed compatible provider
  in manifest order.
- The generic configurator integration map was removed. Current configurators receive only
  the state they consume: application data paths and whether their declared SSO strategy is
  enabled.
- SSO strategy and SSO provider identity are separate concepts.
- The repository fast validation tier passes, including host-agent race tests, app tests,
  frontend tests, and frontend checks.
- Intent-driven reconciler architecture is implemented and merged. All mutations flow through
  a typed intent queue with debounce. The reconciler is the single writer to all stores and
  the single executor of all side effects. Intent types cover install, uninstall, rename,
  tailnet, remote apps, shares, and clear-data.
- PreStart/PostStart configurator contracts exist as optional interfaces. Jellyfin and
  Navidrome configurators implement them.
- Portable application manifests exist as `metadata.yaml` per app with container specs,
  integrations, health checks, and routing configuration.
- The Podman API adapter is working. The portable orchestrator creates containers and
  manages the container lifecycle directly through Podman.
- Domain-agnostic Traefik routing with HostRegexp patterns is implemented. Apps are
  accessible via any origin (localhost, tailnet FQDN, custom domain).
- Developer graph API and frontend visualization are implemented with app nodes, connection
  nodes, and integration edges.
- Two-layer Tailscale sharing architecture is implemented: per-app tailnet nodes for
  outbound sharing, gateway with SOCKS5 proxy for inbound remote app consumption on LAN.
- Sharing data model is implemented: remote_apps, guests, shares, tailnet_connections
  tables in SQLite.
- E2E lifecycle testing framework works against Lima VM with Playwright browser tests.

Not yet implemented:

- Durable desired and observed integration revisions with invalidation tracking
- Typed provider outputs and secret references passed to configurators
- Configurator `changed` return value for selective restart
- Persistent proxy port assignment on `remote_apps` table (currently ephemeral)
- Standalone proxy outpost for tailnet forward-auth (in development)
- Multiple tailnet connections (data model supports it, UI/runtime handle only one)
- `.deb` packaging and `bloud init` with preflight checks
- Clean Debian VM acceptance testing
- SSO identity model for guests (Authentik-backed accounts, per-app auth provisioning)

## Product Promise

Install Bloud on an existing Debian server and get:

- One web dashboard
- One account and shared login
- One-click installation of supported applications
- Automatic dependency provisioning
- Automatic inter-application configuration
- Automatic routing, credentials, and SSO
- Reliable reconciliation after dependency changes, failures, and reboot

Bloud's differentiator is not container installation. It is that applications declare
what they provide and consume, and Bloud continuously keeps those relationships working.

## First Release Scope

### User-Facing Applications

| Application | Primary value | Architecture exercised |
|---|---|---|
| Jellyfin | Stream personal media | LDAP integration, persistent media, native clients |
| Navidrome | Stream personal music | Forward-auth SSO, Subsonic API bypass, trusted header auth |

### Required Infrastructure

- Bloud host-agent binary and web dashboard
- Authentik
- Traefik
- PostgreSQL
- Redis
- Podman

### Explicitly Deferred

- Operating-system installer
- Additional Linux distributions
- Multi-host orchestration and migration
- Additional application catalog entries (AdGuard Home, Immich, etc.)
- Community application submissions
- Dashboard widgets and customization
- Mobile application
- Arbitrary custom containers
- Multiple container runtimes

Debian is the only supported release target. Ubuntu and other distributions become
separate targets only after they pass the complete release acceptance contract.

## Supported Environment

- Clean Debian 13 installation
- `x86_64`
- systemd
- Rootless Podman (host-agent manages containers via the Podman socket)
- One physical or virtual host
- Ethernet networking
- Local-network access
- One documented storage layout

Rootless Podman is an intentional first-release choice. It avoids requiring root access for
container management, isolates the Bloud runtime to a dedicated user, and aligns with
Podman's default security model. `loginctl enable-linger` keeps the user's systemd
instance (and the Podman socket the host-agent talks to) alive across logout.
Traefik binds to unprivileged ports (8080/8443) behind a
host firewall or reverse proxy for port 80/443 access.

Unsupported environments must fail during preflight with actionable diagnostics.

## Core User Journey

1. Install the Bloud `.deb` package on a clean Debian server.
2. Run `sudo bloud init`.
3. Open the web dashboard.
4. Create the first account and sign in once.
5. Install Jellyfin or Navidrome.
6. Let Bloud install providers and configure all required relationships.
7. Open installed applications from the dashboard without another password prompt.
8. Reboot without losing application state, configuration, or integrations.
9. Remove an application without damaging shared infrastructure or user data.

Work that does not improve the reliability of this journey is not release-critical.

## Core Architecture

Bloud separates three concerns:

```text
desired topology       which services, networks, volumes, ports, and routes exist
integration graph      which applications provide and consume capabilities
configuration engine  how each resolved integration edge becomes working configuration
```

### Desired Topology

The desired topology describes runtime resources:

- Services and containers
- Images and commands
- Networks
- Volumes and persistent paths
- Ports
- Routes
- Health checks
- Process-level startup dependencies

The portable runtime applies topology through the Podman API, filesystem, and
host-network adapters.

### Integration Graph

Applications declare typed capabilities they provide and consume:

```text
Jellyfin  -> Traefik    proxy
Jellyfin  -> Authentik  sso (ldap)
Navidrome -> Traefik    proxy
Navidrome -> Authentik  sso (forward-auth)
```

A resolved relationship is a durable integration instance:

```text
consumer: jellyfin
provider: authentik
type: sso
desired revision: 4
observed revision: 4
status: configured
```

Integration edges are active desired state. Installing, removing, reconfiguring, or
recovering a provider must invalidate and reconcile affected consumers.

#### Provider Resolution Policy

- Provider bindings are created by planning and persisted as desired state.
- There is no user-facing UI for selecting or customizing provider resolution.
- A consumer currently resolves at most one provider for each integration type.
- A persisted binding is authoritative and optional discovery never overrides it.
- Required integrations must have a declared compatible binding. Missing, undeclared, or
  incompatible required state is an error and must not be silently preserved.
- Optional integrations without a binding automatically use the first installed compatible
  provider in manifest order.
- Provider health and readiness are separate from provider identity resolution.
- Multi-provider integrations are not supported until a concrete release requirement defines
  their semantics.

### Configuration Engine

Configurators realize application and integration desired state. They are not limited to
SSO. They handle any relationship-specific work that cannot be expressed by topology alone,
including:

- Creating database users, databases, schemas, and extensions
- Writing provider endpoints and generated credentials
- Creating API keys
- Registering one application with another
- Updating application config files
- Calling runtime APIs
- Creating Authentik providers, applications, outposts, and LDAP configuration
- Applying application defaults required by Bloud

Configurators receive structured state and resolved integrations. They must not discover
dependencies implicitly.

Configurator inputs are explicit capabilities and typed provider contracts, not generic
integration or option maps. The current interim configurator state contains only data paths
and `SSOEnabled` because those are the only inputs current configurators consume. Provider
bindings must not be passed to configurators until a configurator requires a typed provider
contract.

An SSO strategy is not an SSO provider binding. A provider binding alone must not enable SSO
configuration; the application manifest must explicitly declare the supported strategy.

#### PreStart Configuration

PreStart configuration runs before service start or when an integration change may affect
startup configuration.

It may write files, environment, certificates, credentials, or other startup inputs.
It runs only after the application's required providers are healthy, so it may also
idempotently provision provider-side resources such as a database, user, or API credential.
It returns whether managed output changed:

```go
PreStartConfig(ctx, state) (changed bool, err error)
```

When prestart configuration changes, the orchestrator restarts only affected services, in
dependency order.

#### PostStart Configuration

PostStart configuration runs after required providers and the consumer are healthy.

It performs idempotent runtime operations such as API calls, resource registration, and
inter-application linking:

```go
PostStartConfig(ctx, state) error
```

PostStart configuration does not itself require a restart.

### Routing and Shared Login

The dashboard, authentication endpoints, and embedded application routes use one browser
origin with path-based routing. First-release applications do not use per-application
subdomains.

This is an architectural requirement, not a presentation preference: signing into Bloud and
then opening another supported application should not require another login prompt. Routing,
cookies, redirects, service-worker behavior, and application SSO configuration must preserve
that same-origin shared-login experience.

Native clients may use their application's documented native authentication flow. Each
application support contract must distinguish browser shared login from native-client login.

#### Domain-Agnostic Traefik Routing

Traefik routes are **domain-agnostic** — they work from any origin (localhost, tailnet FQDN,
tailnet IP, custom domain) without configuration changes.

**App routes** use `HostRegexp('^{appId}\\.')` with priority 200. This matches any host
starting with the app's subdomain prefix: `jellyfin.localhost`, `jellyfin.bloud.co`,
`jellyfin.<tailnet-fqdn>`, etc.

**Base routes** (dashboard, API, auth, UI catch-all) use `PathPrefix` only — no Host
constraint. Their priorities (1–96) are lower than app routes (200), so app subdomain routes
always win when a subdomain prefix matches.

**Forward-auth plumbing routes** (outpost callback, bypass paths) use `HostRegexp` with
priority 300, higher than app routes so they take precedence for their specific paths.

| Route type | Rule pattern | Priority |
|---|---|---|
| UI catch-all | `PathPrefix('/')` | 1 |
| Base routes (API, auth, dashboard) | `PathPrefix('/api')`, etc. | 85–96 |
| App routes | `HostRegexp('^{appId}\\.')` | 200 |
| Outpost/bypass routes | `HostRegexp('^{appId}\\.') && PathPrefix(...)` | 300 |

**Subdomain access by network:**

- **LAN (`*.localhost`):** Works out of the box — browsers resolve `*.localhost` to
  `127.0.0.1` per RFC 6761.
- **Custom domain:** Configure wildcard DNS (`*.bloud.co → host IP`). Subdomains work
  immediately via HostRegexp.
- **Tailnet (gateway FQDN):** The gateway container runs Tailscale Serve
  (`TS_SERVE_CONFIG`), serving HTTPS on port 443 with Tailscale-issued TLS certs, proxying
  to Traefik on localhost. Dashboard and embedded apps work at the gateway FQDN. App
  subdomain access over tailnet (e.g., `jellyfin.<tailnet-fqdn>`) requires wildcard DNS
  resolution — a future change will add self-hosted CoreDNS with Tailscale split DNS.

### Developer Graph

The developer graph is a live dependency visualization exposed at `/api/system/developer`
and rendered in the dashboard. It shows installed applications, their integration edges,
and external connection points.

#### Node Types

The graph contains two node types:

- **App nodes** represent installed applications (both user-facing and system
  infrastructure). Each carries identity, display name, runtime status, and a system flag.
- **Connection nodes** represent ingress points through which users reach applications.
  Connection nodes sit outside the app subgraph and have edges pointing inward to the
  applications they serve.

Current connection node types:

| Connection | Node ID | Source |
|---|---|---|
| Local access | `conn:local` | Synthetic; present when Traefik is installed. Display name is the browser's `window.location.hostname`. Routes to Traefik. |
| Tailnet | `conn:tailnet:<id>` | One per active tailnet connection. Display name and status from `tailnet_connections` store. Edges point to each app whose `tailnet_id` matches. |

#### Edge Derivation

Integration edges are derived from the app's `IntegrationConfig` (runtime bindings) with
catalog defaults as fallback. Edge labels use the integration type name, with two
exceptions:

- **SSO edges** use the catalog SSO strategy as the label (`forward-auth`, `ldap`,
  `native-oidc`) instead of the generic `sso`.
- **Proxy edges** have reversed direction: the proxy (e.g., Traefik) is the source and
  the proxied app is the target.

Connection edges use the connection type as the label (`route` for local, `tailnet` for
tailnet connections). Connection nodes are always the edge source; apps are the target.

#### Subgraph Layout

App nodes are grouped in a single subgraph. Connection nodes are positioned outside and
above the subgraph. The frontend uses dagre for deterministic layout of app nodes within
the subgraph, and manual positioning for connection nodes.

#### Future Direction: Connection Subgraphs

Connection nodes are designed to evolve into subgraphs. Each connection will eventually
contain user nodes representing the individuals who access applications through that
ingress point. This models the relationship between communities (local users, tailnet
members, shared-link recipients) and the applications they can reach.

Sharing within a community is bidirectional. The host shares apps outward to community
members, and members may share their own apps back. A tailnet connection subgraph would
contain user nodes with edges in both directions: outbound edges from local apps to
remote users who can access them, and inbound edges from remote users' shared apps to
the local host. This makes the developer graph a complete view of what is available
across a community, not just what the host is serving.

This future structure supports:

- Per-connection access control and visibility
- User-to-application permission edges
- Community-scoped sharing policies
- Bidirectional app sharing within a community

The current flat connection-node model is forward-compatible with this expansion.

### Sharing and Federation

Bloud acts as a Tailscale gateway: it proxies shared apps through Traefik so that devices
on the local network (TVs, phones, game consoles) can access remote apps without needing
Tailscale installed. Remote apps appear at local subdomains like
`jellyfin-johan.bloud.local`.

#### Sharing Identity Model

Sharing identity works in two tiers:

**Current (first release):** Social trust + Tailscale node sharing. Invite tokens are
unsigned base64 JSON containing app metadata and a Tailscale node share link. Network access
is gated by Tailscale's own node sharing auth. A `guests` table tracks who has been shared
what, purely for the host's reference (a contact book). No Bloud account creation on the
remote side.

**Future:** Full SSO identity model backed by Authentik. The owner creates an invite that
authorizes creation of a Bloud user on the host instance. The guest redeems the token,
creates or binds a Bloud account, and receives access through Bloud's authentication layer.
Downstream applications are provisioned from that Bloud identity according to their declared
authentication capability:

| Capability | Bloud behavior |
|---|---|
| Native OIDC/SAML | Register the app with Authentik and use real browser SSO. |
| Trusted header auth | Protect the app with Authentik forward auth, strip client identity headers at the proxy, inject a mapped identity header, and pre-create or auto-create the app-local user as needed. |
| LDAP | Provision or expose the Bloud/Authentik identity through the LDAP integration contract. |
| App admin API only | Create an app-local user with an app-specific random secret; the user never supplies or sees that secret unless the app has no better login model. |
| No external auth or provisioning API | Gate network access through Bloud, but treat app-local login as a degraded/manual integration. |

For trusted header apps, forward auth and header auth are distinct requirements. Forward auth
only decides whether a request may pass. The application is logged in as the mapped user only
if it explicitly supports a trusted identity header such as `Remote-User`,
`X-WEBAUTH-USER`, or an app-configured equivalent. Manifests must declare the supported
header name, trusted-proxy requirements, auto-user behavior, and any bypass paths for native
client APIs.

The proxy must always remove inbound identity headers from client requests, inject Bloud's
own identity header only after successful authentication, and ensure the upstream app is
reachable only through the trusted proxy path. Bloud must not make user-supplied downstream
passwords the default provisioning primitive.

#### Two-Layer Tailscale Architecture

Sharing uses two independent layers of Tailscale instances:

| Layer | Purpose | Scope | Managed by |
|---|---|---|---|
| **App tailnet nodes** | Granular per-app sharing (host publishes apps) | One per shared app | Orchestrator |
| **Gateway** (`bloud`) | Network connectivity for consuming remote apps (LAN proxy) + dashboard access | One per tailnet connection | Orchestrator via `RegenerateRoutes()` |

Both layers run on the **host network** and proxy upstream to Traefik. This keeps Traefik
as the single routing/middleware layer for all traffic — local, tailnet, and remote.

```text
Upstream topology (per-app tailnet node):
  tailnet user → ts-{app} (host network) → Traefik (localhost:8080) → app container

Gateway:
  tailnet user → ts-gateway (host network, hostname "bloud") → Traefik (localhost:8080) → dashboard
```

App tailnet nodes are per-app Tailscale instances that join a tailnet and serve the app via
Tailscale Serve. Each tailnet node runs on the host network and proxies HTTPS traffic to
Traefik, which routes to the correct app based on the Host header. The tailnet node's
`TS_HOSTNAME` is the bare app name (e.g., `jellyfin`), giving the app a clean MagicDNS
name like `jellyfin.tailnet.ts.net`. The HostRegexp routing already in Traefik
(`^jellyfin\.`) matches this naturally.

The gateway (hostname `bloud`, container name `ts-gateway`) is a Tailscale instance that
runs in userspace mode on the host network. It serves two purposes:
1. **Dashboard access** via `bloud.tailnet.ts.net` (proxies to Traefik like tailnet nodes).
2. **SOCKS5 proxy for remote app consumption on the LAN**. The gateway exposes a SOCKS5
   proxy at `localhost:1055`. Per-remote-app reverse proxies (managed by
   `RemoteProxyManager`) listen on localhost ports and dial through the SOCKS5 proxy to
   reach remote tailnet nodes. Traefik routes to `http://localhost:{proxyPort}` instead of
   directly to tailnet URLs. This means devices on the local network (TVs, phones, game
   consoles) can access remote apps through normal subdomain URLs without needing
   Tailscale installed — the gateway handles the tailnet hop on their behalf.

The `tailnet_connections` store in Settings is the single source of truth for both layers.
Each connection entry provides the auth key for app tailnet nodes (outbound sharing) and the
gateway connectivity for remote apps (inbound consumption to LAN).

#### Sharing Flow (Host Side)

1. Host right-clicks an installed app and selects "Share".
2. ShareModal opens: host selects an existing guest from the dropdown (or creates a new
   one), then enters the Tailscale node share link.
3. Host-agent creates a share record (linking guest + app) and generates an unsigned base64
   invite token containing: appId, appName, hostLabel, tailnetAddr, nodeShareLink.
4. Host copies the token and sends it to the guest out-of-band.

#### Remote App Flow (Guest Side)

1. Guest opens "Add Shared App" modal (defaults to token paste mode).
2. Guest pastes the invite token — modal decodes it and shows confirmation:
   "{hostLabel} wants to share {appName}".
3. Guest clicks the Tailscale share link to accept network access.
4. Guest clicks "Add" — Bloud creates a remote app record in the `remote_apps` table with
   a monotonically assigned `proxy_port` (starting from 10100, never reused).
5. `RegenerateRoutes()` ensures the gateway container is running, then reconciles
   reverse proxies: each remote app gets a localhost listener on its assigned port
   that dials through the gateway's SOCKS5 proxy to reach the remote tailnet node.
6. Traefik route generation includes the remote app: subdomain
   `{appId}-{hostLabel-slug}.<any-domain>` proxies to `http://localhost:{proxyPort}`.
7. The app appears on the home page in the "Shared Apps" section.
8. Local devices access the remote app at its local subdomain without needing Tailscale.
9. Manual entry mode is still available as a fallback.

```text
Browser → Traefik (:8080)
             ↓ HostRegexp(`^jellyfin-johan\.`)
         localhost:{proxyPort}          ← host-agent reverse proxy
             ↓ SOCKS5 (localhost:1055)
         ts-gateway container           ← userspace Tailscale
             ↓ tailnet
         ts-jellyfin.tail1275sa.ts.net  ← remote tailnet node
             ↓
         Jellyfin on remote host
```

#### Data Model

- **`remote_apps`** — Stores remote app records: app identity, host label, tailnet node
  address, proxy port, status. Each record generates a Traefik route. The `proxy_port` is
  monotonically assigned at creation time (`MAX(proxy_port) + 1`, starting from 10100) and
  never reused, ensuring stable port assignments across restarts.
- **`guests`** — Contact book of people the host shares apps with. Each guest has a name
  and UUID. The `shares` table references guests by ID, enabling "who has access to what"
  queries.
- **`shares`** — Stores outbound share records: which local apps are shared and to whom.
  References `guests.id` via `guest_id`.
- **`tailnet_connections`** — Stores tailnet connection config: auth key, control URL, type
  (Tailscale or Headscale). Used for app tailnet nodes (outbound sharing) and the gateway
  (inbound remote app consumption on LAN).

The `apps` and `remote_apps` tables remain separate. Local apps have container lifecycle,
integration configs, dependency graph participation, and SSO provisioning. Remote apps are
a tailnet address, a proxy port, and an access binding. These are fundamentally different
lifecycles, and merging them would require type-discriminator guards on every query touching
local app state. Route generation in `RegenerateRoutes()` queries both tables and derives
routes at generation time — no separate routes table is needed.

Auth keys are never exposed through the API. The frontend receives only a boolean
`hasAuthKey` indicating whether a key is configured.

#### Not Yet Implemented

- **Persistent proxy port on `remote_apps`** — The `proxy_port` column needs to be added
  to the `remote_apps` table schema. Currently, proxy ports are assigned ephemerally by
  `RemoteProxyManager` at reconciliation time and may shift when apps are added or removed.
- **Standalone proxy outpost for tailnet forward-auth** — A dedicated Authentik outpost
  container for tailnet auth, separate from the embedded outpost that handles local auth.
  This enables remote users to log in via `bloud.{tailnet_domain}` instead of being
  redirected to unreachable `localhost:8080`. In active development.
- **Multiple tailnet connections** — The data model supports multiple entries, but the UI
  and runtime currently handle only one active connection.
- **SSO identity model** — Guest Bloud accounts backed by Authentik, per-app auth
  provisioning via OIDC/SAML/LDAP/header auth.
- **Guest management UI** — Dedicated view showing guests and their active shares.

### Reconciliation Flow

The orchestrator uses an intent-driven architecture. All mutations flow through a typed intent
queue. The orchestrator is the single writer to all stores and the single executor of all side
effects. API handlers are thin intent submitters that return 202 Accepted.

Each orchestrator cycle has two phases:

**Phase 1: Drain Queue (Apply Intents to Stores)**

Pull all pending intents from the FIFO queue. For each intent, apply the corresponding
store mutations. No side effects — just store writes that represent desired state.

**Phase 2: Converge (Make Actual Match Desired)**

```text
1. Sync container state — reconcile DB status with actual container reality
2. Resolve dependencies — auto-install required providers for installing apps
3. Ensure apps in dependency order:
   a. PreStart configuration (dirs, config files, credentials)
   b. Ensure container (via Podman API)
   c. Health check
   d. PostStart configuration (API calls, integration setup)
   e. SSO provisioning
   f. Tailnet node management (if tailnet active)
4. Handle uninstalls — stop tailnet node, remove container, delete from store
5. Routing convergence — ensure gateway, reconcile remote proxies, regenerate Traefik routes
6. Optional dependency dispatch — reconfigure apps when optional providers become healthy
7. Tailnet teardown — if tailnet deleted, stop and purge all nodes and gateway
```

Intents are debounced (~5 seconds) so rapid mutations coalesce into a single convergence
pass. Multiple installs that share a dependency produce one install of the shared provider.

Independent applications within one dependency level may run concurrently. Required-provider
health must precede consumer prestart or poststart integration configuration.

Events that invalidate an integration include provider install/removal, provider output or
secret changes, consumer manifest changes, configurator version changes, an optional provider
becoming healthy, and prior configuration failure. Invalidations are durable and remain
pending until reconciliation succeeds or the relationship is removed.

An integration is `configured` only after every required phase succeeds. Durable failure
state identifies the application, provider, integration type, phase, retryability, and cause.
Restarting the host agent or host must resume reconciliation rather than lose progress.

## Architecture Principles

### 1. Pure Decisions, Effectful Adapters

Planning and policy code must be deterministic and independently testable.

Pure core responsibilities:

- Manifest and catalog validation
- Dependency graph construction
- Provider resolution
- Install and removal planning
- Desired topology calculation
- Integration instance calculation
- Dependency and restart ordering
- Invalidation calculation
- Change detection

Effectful operations sit behind narrow interfaces:

- Podman
- Filesystem and permissions
- Host networking and firewall
- Secret persistence
- State persistence
- Health checks
- Configurator execution

Pure-core tests must not require Podman, systemd, Authentik, a database server, or network
access.

Interfaces contain only information required by their current consumers or policy. Do not
add speculative fields, generic option maps, or compatibility representations to target
architecture boundaries. Change internal callers together when replacing an old contract.

### 2. One Authoritative Application Model

A portable application manifest is the authoritative description of:

- Identity and version
- Service and container topology
- Provided capabilities
- Consumed integrations
- Configurator triggers
- Routing and embedding behavior
- SSO behavior
- Health contracts
- Persistent data
- Host requirements

Runtime adapters may implement this model, but must not silently add dependencies or
behavior. Application-specific behavior must be isolated, documented, and directly tested.

### 3. Typed Provider Contracts

Providers expose typed outputs instead of requiring consumers to know implementation
details. A database provider contract may expose an endpoint, credentials reference,
database name, and capabilities such as `pgvector`.

Consumers receive only resolved provider contracts. They must not depend on container names,
filesystem layout, or undocumented provider internals.

### 4. Desired State and Reconciliation

Bloud manages desired state, not a sequence of assumed-success commands.

Every operation must be safe to retry after:

- Host-agent termination
- Service creation or startup failure
- Configurator failure
- Provider unavailability
- Partial integration configuration
- Host reboot

Reconciliation against an already-correct system must make no changes.

### 5. Durable Integration State

Bloud persists desired and observed revisions for applications and integration instances.
Failures identify the affected phase, application, provider, and integration.

An integration is not reported as configured until required prestart configuration, health
verification, and poststart configuration have succeeded.

### 6. Narrow Modules

Required architectural boundaries:

- `catalog`: loads and validates portable application manifests
- `planner`: calculates topology, integration, install, removal, and reconfiguration plans
- `state`: persists desired and observed application and integration state
- `runtime`: applies topology through host adapters
- `orchestrator`: drains intents and converges topology and integration state
- `configurator`: realizes prestart and poststart application/integration configuration
- `routing`: generates and verifies routes
- `secrets`: creates and persists stable credentials
- `health`: verifies readiness

Large functions that mix planning, persistence, runtime application, configurators, routing,
and health must be decomposed before adding new behavior.

### 7. Stable Secrets, Data, and Host State

- Generated secrets remain stable across reconciliation, upgrades, and reboot.
- Secrets are never written to logs or API responses.
- Application data is preserved by default during uninstall.
- Destructive deletion requires explicit user intent.
- Shared providers cannot be removed while required.
- Host-level changes, especially DNS changes, are recorded and reversible.

### 8. Evidence Before Expansion

An application is supported only when its complete topology and integration lifecycle is
automated and repeatably verified. Existing code or a successful manual test is insufficient.

## Portable Runtime

### Runtime Boundary

```text
runtime-neutral core
  catalog + planner + integration graph + state + reconciler + configurators
                              |
                              v
Debian runtime adapters
  Podman + filesystem + host networking
```

The core decides what should exist and how relationships should resolve. Runtime adapters
apply that decision and report observed state. Adapters must not silently add dependencies,
integrations, or application policy.

### Packaging

The first release ships as a versioned Debian package containing:

- `/usr/bin/bloud`
- `bloud.service`
- The web dashboard
- Portable application manifests
- Required static assets
- Default configuration

`bloud init` performs preflight checks and installs the core desired state.

### Storage Layout

```text
/etc/bloud/
  config.yaml

/var/lib/bloud/
  state/
  secrets/
  apps/
  shared/
  generated/
  host-backups/
```

### Runtime Application

Bloud creates and manages containers through the Podman API. The exact container format
is an adapter detail; desired topology and integration state remain runtime-neutral.

### Infrastructure

PostgreSQL, Redis, Traefik, and Authentik run as Bloud-managed containers for consistent
behavior across supported distributions. Inter-service communication uses a managed internal
network rather than host-native service sockets.

### Host Changes

Host-level changes must be explicit, recorded, and reversible. Any future application that
modifies host state (DNS, firewall, networking) must:

- Detect conflicts before applying changes.
- Record prior host configuration.
- Apply changes only after the application is healthy.
- Restore prior state after failed apply or removal.
- Verify behavior from a separate client.

## Migration Engineering Policy

The migration targets the clean portable architecture, not backward compatibility with
legacy runtime-specific interfaces. Change internal callers in the same validated slice when an
old contract does not belong in the target design.

Every migration slice must leave behind:

1. A runtime-neutral contract or a measurable reduction in runtime-specific coupling.
2. Characterization, unit, or contract tests at the lowest useful layer.
3. No new application-specific branch in shared orchestration.
4. Explicit failure and retry semantics.
5. A clear deletion path for replaced code.

### Priority Design Improvements

- Decompose the existing orchestrator into planner, executor/runtime adapter, reconciler, and
  durable operation-state responsibilities.
- Introduce explicit domain types incrementally and translate to persistence, API, and
  runtime representations at boundaries.
- Design target interfaces from actual consumers. Do not add speculative fields, generic
  option maps, or compatibility adapters by default.
- Keep planning pure, deterministic, serializable, inspectable, and independently testable.
- Separate durable operations from observed application status.
- Make portable manifests authoritative and reject hidden behavior.
- Allow application configuration and individual integration-edge configuration to reconcile
  independently.
- Use typed errors at domain boundaries to drive retry policy, diagnostics, and tests.
- Centralize atomic managed-file writes, permissions, change detection, and secret redaction.
- Prefer small contract-oriented fakes over expanding behavior-heavy mock suites.

### Deletion Targets

Most legacy paths have been removed. Remaining cleanup targets:

- Any remaining host-native PostgreSQL or Redis assumptions in test fixtures
- Legacy runtime-specific CLI commands if any survive
- Stale documentation references

Deprecated runtime paths must not remain indefinitely.

### Migration Non-Goals

Do not use the migration to:

- Rewrite the dashboard
- Replace the API framework
- Replace the database without demonstrated need
- Rename or reorganize unrelated packages
- Support multiple distributions or container runtimes
- Build a generic plugin ecosystem
- Refactor code without characterization or contract tests

## Testing Strategy

### Layer 1: Pure Unit Tests

- Manifest validation
- Provider and consumer compatibility
- Dependency cycles and missing providers
- Install and removal plans
- Desired topology
- Integration instances and revisions
- Invalidation propagation
- Dependency and selective-restart ordering
- State-transition rules

### Layer 2: Configurator Contract Tests

Every configurator tests:

- PreStart configuration from structured integration inputs
- Accurate `changed` reporting
- Idempotent repeated execution
- PostStart configuration idempotency
- Provider unavailability
- Malformed provider outputs
- Partial prior configuration
- Secret stability

### Layer 3: Runtime Adapter Contract Tests

- Podman create/inspect lifecycle
- Filesystem permissions
- Port conflict detection
- Host-network changes and rollback
- State and secret persistence

### Layer 4: Reconciler Integration Tests

Using fake adapters and configurators:

- Correct phase and dependency ordering
- Provider-change propagation
- Optional provider installation
- Selective consumer restart
- Retry and recovery
- No partial-success reporting
- Removal blockers
- Concurrent requests

### Layer 5: E2E Lifecycle Tests

The `./bloud e2e lifecycle` command builds the host-agent, deploys it to a Lima VM, and runs
the full install/uninstall lifecycle with Playwright browser tests for SSO verification.

Current coverage:
- Jellyfin: install, Bloud login, LDAP SSO login in embedded iframe, uninstall
- Navidrome: install, forward-auth login via Traefik, uninstall

The release gate will run on a clean Debian VM with the real `.deb` package.

Tests verify behavior, not screenshots alone.

### Test Reliability Requirements

- Images and dependencies are pinned.
- Tests do not depend on developer-machine state.
- Failed acceptance runs retain logs and durable operation state.
- Flaky release-gate tests are release blockers.
- Every production bug gets a regression test at the lowest useful layer.

## Application Support Contracts

Every supported application defines and verifies:

- Portable manifest
- Provided and consumed capabilities
- Service topology
- PreStart and PostStart configurator behavior
- Persistent data
- Health checks
- Routing and embedding
- Shared-login behavior
- Install, reconciliation, reboot, upgrade, and removal behavior
- Failure diagnostics

### Jellyfin

- Installs from the dashboard
- Creates required persistent directories
- Configures LDAP integration idempotently
- Opens from the authenticated dashboard without another password prompt
- Supports documented native-client login
- Plays a test media file
- Preserves media and configuration across reboot and upgrade
- Preserves data by default during uninstall

### Navidrome

- Installs from the dashboard
- Creates required persistent directories (data, music)
- Configures forward-auth SSO through Authentik
- Opens from the authenticated dashboard without another password prompt
- Bypasses forward-auth for `/rest/` paths so Subsonic API clients can authenticate directly
- Accepts trusted `X-authentik-username` header for browser SSO identity
- Supports documented Subsonic-compatible client login
- Preserves music library and configuration across reboot and upgrade
- Preserves data by default during uninstall

## Delivery Phases

Each phase ends with an automated gate.

| Phase | Current status |
|---|---|
| Phase 0: Freeze, Inventory, and Measure | Complete |
| Phase 1: Extract the Integration Engine | Complete |
| Phase 2: Implement Reconciler Architecture | Complete |
| Phase 3: Implement the Portable Runtime | Complete; Podman management working on Lima VM |
| Phase 4: Port Jellyfin | Complete; LDAP SSO, E2E lifecycle tests passing |
| Phase 5: Port Navidrome | Complete; forward-auth SSO, E2E tests passing |
| Phase 6: Implement Sharing and Federation | In progress; core sharing works, tailnet outpost auth in development |
| Phase 7: Package and Release | Not started |

Phase work may overlap only when it does not bypass an earlier phase's release gate or create
an interface whose requirements have not been established.

### Phase 0: Freeze, Inventory, and Measure

- Freeze this release boundary.
- Establish baseline tests and clean-VM reliability.

Gate:

- Existing behavior has characterization tests where practical.

**Status:** Complete. Scope frozen, fast validation tier passing.

### Phase 1: Extract the Integration Engine

- Define typed provider contracts.
- Formalize prestart and poststart configurator contracts.
- Make resolution operate from declared integration state.

Gate:

- Provider changes, optional provider installation, and missing required bindings are
  thoroughly tested without a real runtime.

**Status:** Complete. Typed integration resolver with required/optional/compatibility
semantics implemented and tested. PreStart/PostStart configurator interfaces defined.

### Phase 2: Implement Reconciler Architecture

- Collapse all mutation paths into an intent-driven reconciler.
- Make the reconciler the single writer to all stores and single executor of side effects.
- Implement intent queue with debounce, convergence loop, and dependency-ordered execution.
- Cover all operations: install, uninstall, rename, tailnet, remote apps, shares, clear-data.

Gate:

- All mutations flow through the intent queue. No API handler writes to stores or executes
  side effects directly. E2E lifecycle tests pass through the new architecture.

**Status:** Complete. Merged in PR #2 (`reconciler-architecture`). All intent types
implemented. Old `EnqueueInstall`/`EnqueueUninstall` paths removed.

### Phase 3: Implement the Portable Runtime

- Implement filesystem and Podman adapters.
- Run the host agent as a systemd user service.
- Prove create, health, reboot, reconcile, and remove with real services.

Gate:

- A Lima VM reaches the dashboard and reconciles core infrastructure without drift.

**Status:** Complete for development. Rootless Podman and container lifecycle all working
on Lima VM. Not yet validated on a clean Debian VM without Lima.

### Phase 4: Port Jellyfin

- Port topology and LDAP integration to portable contracts.
- Verify dashboard access, native clients, media playback, persistence, reboot, and removal.

Gate:

- Jellyfin passes its full support contract repeatedly on the Lima VM.

**Status:** Complete. Jellyfin configurator implements PreStart (dirs, LDAP plugin, network
config, setup wizard) and PostStart (LDAP integration via API). E2E lifecycle test covers
install, Bloud login, LDAP SSO login, and uninstall.

### Phase 5: Port Navidrome

- Port topology and forward-auth SSO to portable contracts.
- Verify dashboard access, Subsonic API bypass, persistence, reboot, and removal.

Gate:

- Navidrome and Jellyfin both pass repeatedly.

**Status:** Complete. Navidrome uses forward-auth via Authentik with `/rest/` bypass for
Subsonic clients. E2E test covers install and forward-auth login flow.

### Phase 6: Implement Sharing and Federation

- Implement per-app tailnet nodes for outbound sharing.
- Implement gateway with SOCKS5 proxy for inbound remote app consumption.
- Implement domain-agnostic routing so apps work from any origin.
- Add sharing UI (invite tokens, guest management, remote app addition).
- Implement standalone proxy outpost for tailnet forward-auth.

Gate:

- A shared app is accessible from a remote tailnet peer. Remote apps are accessible from
  local network devices through the gateway proxy. Forward-auth apps authenticate correctly
  over tailnet.

**Status:** In progress. Core sharing infrastructure implemented: tailnet nodes, gateway,
SOCKS5 reverse proxies, remote app management, invite tokens, domain-agnostic routing.
Standalone proxy outpost for tailnet forward-auth is in active development.

### Phase 7: Package and Release

Required acceptance flow:

1. Create a clean Debian 13 VM.
2. Install the exact release-candidate `.deb`.
3. Run `bloud init`.
4. Create the first account and sign in.
5. Install and verify every release app and its integrations.
6. Reboot and verify dashboard, shared login, routes, apps, and data.
7. Reconcile twice and verify no unintended changes.
8. Verify required-provider removal is blocked.
9. Remove user-facing apps and verify shared infrastructure.
10. Upgrade the Bloud package and verify the complete system again.

Gate:

- The exact package later published passes repeatedly without manual intervention.

**Status:** Not started. Requires `.deb` packaging, `bloud init`, and clean Debian VM
validation.

## Release Criteria

The first release is ready only when:

- The portable integration engine is the authoritative execution model.
- The supported app set satisfies its complete contracts.
- Clean Debian acceptance passes repeatedly.
- Install, integration configuration, reconciliation, reboot, upgrade, and removal are
  recoverable and observable.
- Shared login works across every included app.
- No critical workflow depends on undocumented manual intervention.
- There are no known flaky release-gate tests.
- Documentation matches implemented behavior.

Reliability of the supported experience, not application count, is the release criterion.

## Decision Rule

Prefer the simpler implementation with stronger automated evidence.

Before adding application-specific behavior, determine whether it represents a reusable
topology or integration contract. If it does not, isolate it, document it, and test it
directly.

When architecture and current implementation disagree, preserve user data first, then move
the implementation to this specification under tests. Do not preserve obsolete internal
contracts solely for backward compatibility.

Small, fully validated vertical slices are preferred over broad rewrites. Supporting
documents may explain implementation detail, but this specification remains authoritative.
