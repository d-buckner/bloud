# Bloud First Release Specification

**Status:** Authoritative active plan
**Last updated:** 2026-06-22
**Product:** Portable single-host home cloud\
**Initial target:** Debian 13, `x86_64`, systemd\
**Primary risk:** Unreliable implementation and architecture\
**Release strategy:** Preserve Bloud's integration model while replacing NixOS with a small, tested host runtime

## Document Authority

This file is the source of truth for product scope, architecture, migration policy, delivery
order, and release gates. When another document or the current implementation conflicts with
this specification, update or replace the conflicting material. Do not silently change this
specification through implementation.

Supporting documents provide rationale, detail, and implementation records:

- `docs/portable-runtime-architecture.md`: component overview and diagram
- `docs/contributing-apps.md`: how to add a new app
- `docs/migration-design-rules.md`: review checklist and technical-debt inventory
- `docs/slices/`: scoped implementation records

Supporting documents must defer to this file and must not introduce new authoritative
requirements.

Any change to release scope, supported environment, architecture boundaries, migration
policy, phase order, or release gates must update this file in the same commit. Completed
slices update the migration checkpoint and phase-status table.

## Current Migration Checkpoint

Completed:

- First-release product scope and Debian runtime direction are defined.
- Typed integration resolution exists in a runtime-neutral package.
- Resolution accepts declared requirements, planner-created bindings, and installed-app
  membership, and returns one provider binding per integration type.
- Undeclared bindings, incompatible providers, and missing required bindings fail explicitly.
- Optional unbound integrations automatically resolve the first installed compatible provider
  in manifest order.
- The generic configurator integration map was removed. Current configurators receive only
  the state they consume: application data paths and whether their declared SSO strategy is
  enabled.
- SSO strategy and SSO provider identity are separate concepts.
- The repository fast validation tier passes, including host-agent race tests, app tests,
  frontend tests, and frontend checks.

Not yet implemented:

- Durable desired and observed integration instances
- Typed provider outputs and secret references
- PreStart/PostStart configurator contracts
- Portable application manifests
- Runtime-neutral desired topology and planner
- Debian/Podman/Quadlet/systemd adapters and `.deb` packaging
- Clean Debian VM acceptance

The next implementation slice must be selected from Phase 1 work and remain small enough to
fully validate. Current preferred order:

1. Define the minimal typed provider-output and secret-reference boundary required by one
   real integration.
2. Add a managed-file helper with atomic writes, explicit permissions, and accurate change
   detection.
3. Migrate one release-app configurator to the prestart/poststart contract under focused tests.
4. Add durable integration identity, desired/observed revisions, and invalidation only after
   their concrete consumers are defined.

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
| AdGuard Home | Network-wide DNS filtering | Host networking, protected dashboard, reversible host changes |
| Immich | Back up and browse photos | Native OIDC, PostgreSQL, Redis, multiple containers |

### Required Infrastructure

- Bloud host-agent binary and web dashboard
- Authentik
- Traefik
- PostgreSQL
- Redis
- Podman
- systemd and Quadlet

### Explicitly Deferred

- NixOS ISO and operating-system installer
- Additional Linux distributions
- Multi-host orchestration and migration
- Additional application catalog entries
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
- Rootful Podman
- One physical or virtual host
- Ethernet networking
- Local-network access
- One documented storage layout

Rootful Podman is an intentional first-release choice. It reduces complexity around
privileged ports, DNS, media directories, startup ordering, and host networking. Containers
should still run as non-root users internally where supported.

Unsupported environments must fail during preflight with actionable diagnostics.

## Core User Journey

1. Install the Bloud `.deb` package on a clean Debian server.
2. Run `sudo bloud init`.
3. Open the web dashboard.
4. Create the first account and sign in once.
5. Install Jellyfin, AdGuard Home, or Immich.
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

The portable runtime applies topology through Podman Quadlet, systemd, filesystem, and
host-network adapters.

### Integration Graph

Applications declare typed capabilities they provide and consume:

```text
Immich   -> PostgreSQL  database
Immich   -> Redis       cache
Immich   -> Authentik   sso
Jellyfin -> Authentik   ldap
```

A resolved relationship is a durable integration instance:

```text
consumer: immich
provider: postgres
type: database
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

When prestart configuration changes, the reconciler restarts only affected services, in
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

#### Two-Layer Tailscale Architecture

Sharing uses two independent layers of Tailscale instances:

| Layer | Purpose | Scope | Managed by |
|---|---|---|---|
| **App sidecars** | Granular per-app sharing (host publishes apps) | One per shared app | Orchestrator |
| **Gateway instances** | Network connectivity for consuming remote apps | One per tailnet connection | Tailnet settings |

App sidecars are per-app Tailscale instances that join a tailnet and serve the app via
Tailscale Serve. They give the host granular control over which apps are shared and to
which tailnets.

Gateway instances are per-tailnet Tailscale instances that provide network connectivity
so Traefik can reach remote sidecars. A Bloud host can have one Tailscale tailnet
connection plus unlimited Headscale tailnet connections, each running its own gateway
instance.

The `tailnet_connections` store in Settings is the single source of truth for both layers.
Each connection entry provides both the auth key for app sidecars (outbound sharing) and
the gateway connectivity for remote apps (inbound consumption).

#### Sharing Flow (Host Side)

1. Host installs an app (e.g., Jellyfin).
2. The orchestrator starts a Tailscale sidecar for the app, joining the configured tailnet.
3. The sidecar serves the app via Tailscale Serve at a tailnet address
   (e.g., `ts-jellyfin.tail1275sa.ts.net`).
4. Host creates an invite token containing the sidecar address and app metadata.
5. Guest receives the token.

#### Remote App Flow (Guest Side)

1. Guest clicks "Add shared app" in the catalog page.
2. Guest selects app type, enters the tailnet domain, and provides a label.
3. Bloud creates a remote app record in the `remote_apps` table.
4. Traefik route generation includes the remote app: subdomain
   `{appId}-{hostLabel-slug}.{baseDomain}` proxies to `https://{sidecarTailnetAddr}`.
5. A gateway Tailscale instance (from the tailnet connection) provides network connectivity
   so Traefik can reach the remote sidecar.
6. The app appears on the home page with a "shared" badge.
7. Local devices access the remote app at its local subdomain without needing Tailscale.

#### Data Model

- **`remote_apps`** — Stores remote app records: app identity, host label, sidecar tailnet
  address, SSO strategy, bypass paths, status. Each record generates a Traefik route.
- **`shares`** — Stores outbound share records: which local apps are shared and to whom.
- **`tailnet_connections`** — Stores tailnet connection config: auth key, control URL, type
  (Tailscale or Headscale). Used for both app sidecars and gateway instances.

#### Not Yet Implemented

- **Gateway Tailscale container** — The runtime component that joins a tailnet and provides
  network connectivity for Traefik to reach remote sidecars. Without this, remote app
  Traefik routes are written but not reachable.
- **Multiple tailnet connections** — The data model supports multiple entries, but the UI
  and runtime currently handle only one active connection.
- **Invite token redemption** — The guest-side flow for automatically creating a remote app
  from an invite token (currently manual via the catalog UI).

### Reconciliation Flow

The target reconciler executes this order from durable desired state:

```text
1. Load manifests and durable desired state
2. Resolve and validate provider bindings
3. Calculate desired topology, integration instances, and dependency levels
4. Ensure topology for each dependency level
5. Wait for required providers to become healthy
6. Run prestart configuration with typed provider outputs
7. Start or selectively restart changed consumers
8. Verify consumer health
9. Run poststart configuration
10. Record observed application and integration revisions
```

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

- Podman and Quadlet
- systemd
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
- `reconciler`: converges topology and integration state
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
  Podman + Quadlet + systemd + filesystem + host networking
```

The core decides what should exist and how relationships should resolve. Runtime adapters
apply that decision and report observed state. Adapters must not silently add dependencies,
integrations, or application policy.

### NixOS Responsibility Replacement

| Current responsibility | Portable replacement |
|---|---|
| Host-agent installation | Versioned `.deb` and `bloud.service` |
| Application enablement | Durable desired state and reconciler |
| Container definitions | Portable manifests and generated Quadlet |
| systemd units and ordering | Quadlet/systemd adapter |
| Directories and permissions | Filesystem adapter |
| Rootless Podman network | Managed rootful Podman networks |
| Native PostgreSQL and Redis | Bloud-managed containers |
| Native service configuration | Portable topology and configurators |
| Firewall and privileged ports | Host-network adapter and preflight |
| Nix activation scripts | Idempotent runtime adapters and configurators |
| NixOS rollback | Durable desired state, recorded host changes, and reconciliation |
| ISO installer | Debian package installation and `bloud init` |

Every release-critical behavior encoded in Nix modules, helpers, activation scripts, native
services, or systemd hooks must be inventoried and assigned to a portable replacement before
its existing implementation is removed.

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

Bloud generates Podman Quadlet units and related systemd configuration. The exact generated
format is an adapter detail; desired topology and integration state remain runtime-neutral.

### Infrastructure

PostgreSQL, Redis, Traefik, and Authentik run as Bloud-managed containers for consistent
behavior across supported distributions. Inter-service communication uses a managed internal
network rather than NixOS-native Unix sockets.

### Host Changes

Host-level changes must be explicit, recorded, and reversible. This especially applies to
AdGuard Home:

- Detect port 53 and resolver conflicts before applying changes.
- Record prior host resolver configuration.
- Apply DNS changes only after AdGuard Home is healthy.
- Restore prior state after failed apply or removal.
- Verify DNS behavior from a separate client.

## NixOS Transition

NixOS remains a temporary reference implementation while the portable runtime is built.

- Existing NixOS behavior is inventoried before replacement.
- Portable manifests and integration contracts become authoritative.
- New shared behavior is implemented in runtime-neutral core code.
- NixOS-specific modules may coexist only during migration.
- NixOS is not a permanent second release runtime.

The transition must inventory every responsibility currently hidden in `module.nix`,
activation scripts, native NixOS services, and systemd hooks.

## Migration Engineering Policy

The migration targets the clean portable architecture, not backward compatibility with
internal NixOS-era interfaces. Change internal callers in the same validated slice when an
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

Delete these after portable parity is proven:

- Nix generator and rebuilder
- ISO installer
- NixOS application modules and helpers
- Rootless Podman networking workarounds
- NixOS-native PostgreSQL and Redis assumptions
- ISO-only release validation
- NixOS-specific CLI commands

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

- Quadlet generation
- systemd operations
- Podman inspection
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

### Layer 5: Clean Debian VM Acceptance Tests

The release gate runs the real Debian package, Podman, dashboard, SSO, routing, providers,
configurators, and user-facing applications.

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

### AdGuard Home

- Installs from the dashboard
- Completes initial configuration automatically
- Protects its web interface through Bloud authentication
- Answers DNS requests from a separate client
- Detects port and DNS conflicts before applying changes
- Records and restores previous host DNS state
- Preserves configuration across reboot and upgrade

### Immich

- Installs PostgreSQL and Redis providers automatically
- Provisions its database and required extensions through integration configuration
- Starts all required services in dependency order
- Configures native OIDC idempotently
- Opens from the authenticated dashboard without another password prompt
- Supports photo upload, thumbnail generation, and retrieval
- Preserves photos and configuration across reboot and upgrade
- Does not damage shared providers during removal

Immich remains in the first-release set only while it meets this contract without making the
overall release unreliable.

## Delivery Phases

Each phase ends with an automated gate.

| Phase | Current status |
|---|---|
| Phase 0: Freeze, Inventory, and Measure | In progress; scope frozen, full NixOS responsibility inventory incomplete |
| Phase 1: Extract the Integration Engine | In progress; typed provider resolution slice completed |
| Phase 2: Define Portable Manifests and Desired Topology | Not started |
| Phase 3: Implement the Debian Runtime | Not started |
| Phase 4: Port Jellyfin | Not started |
| Phase 5: Port AdGuard Home | Not started |
| Phase 6: Port Immich | Not started |
| Phase 7: Package and Release | Not started |

Phase work may overlap only when it does not bypass an earlier phase's release gate or create
an interface whose requirements have not been established.

### Phase 0: Freeze, Inventory, and Measure

- Freeze this release boundary.
- Inventory all NixOS responsibilities for release apps and infrastructure.
- Map every `module.nix`, activation script, native service, hook, and special case to a
  portable replacement.
- Establish baseline tests and clean-VM reliability.

Gate:

- No release-critical NixOS behavior is hidden or unclassified.
- Existing behavior has characterization tests where practical.

### Phase 1: Extract the Integration Engine

- Define typed provider contracts and durable integration instances.
- Formalize prestart and poststart configurator contracts.
- Track desired and observed integration revisions.
- Implement invalidation and selective restart planning.
- Make reconciliation operate from desired integration state.

Gate:

- Provider changes, optional provider installation, failed configuration, retry, and selective
  restart are comprehensively tested without a real runtime.

### Phase 2: Define Portable Manifests and Desired Topology

- Define manifests capable of representing the complete release stack.
- Represent services, networks, volumes, ports, routes, health, providers, consumers, and
  configurator triggers.
- Produce deterministic install and removal plans.

Gate:

- Golden plans represent Jellyfin, AdGuard Home, Immich, PostgreSQL, Redis, Traefik, and
  Authentik without undocumented behavior.

### Phase 3: Implement the Debian Runtime

- Build Debian preflight checks.
- Implement filesystem, Podman, Quadlet, systemd, and networking adapters.
- Package and run the host agent as a systemd service.
- Prove create, health, reboot, reconcile, and remove with a trivial service.

Gate:

- A clean Debian VM reaches the dashboard and reconciles core infrastructure without drift.

### Phase 4: Port Jellyfin

- Port topology and LDAP integration to portable contracts.
- Verify dashboard access, native clients, media playback, persistence, reboot, and removal.

Gate:

- Jellyfin passes its full support contract repeatedly on a clean Debian VM.

### Phase 5: Port AdGuard Home

- Implement reversible host DNS changes and conflict detection.
- Verify protected dashboard, DNS from a separate client, reboot, failure recovery, and removal.

Gate:

- AdGuard Home and Jellyfin both pass repeatedly.

### Phase 6: Port Immich

- Port multi-service topology, database/cache integrations, extensions, and OIDC.
- Verify upload, retrieval, reboot, reconciliation, and safe removal.

Gate:

- All release apps pass repeatedly, and Immich does not introduce runtime-specific branches in
  the integration engine.

If this gate cannot be met reliably, Immich is marked experimental and does not block release.

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
