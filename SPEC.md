# Bloud First Release Specification

**Status:** Draft  
**Product:** Portable single-host home cloud  
**Initial target:** Debian 13, `x86_64`, systemd  
**Primary risk:** Unreliable implementation and architecture  
**Release strategy:** Preserve Bloud's integration model while replacing NixOS with a small, tested host runtime

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

#### Static Configuration

Static configuration runs before service start or when an integration change may affect
startup configuration.

It may write files, environment, certificates, credentials, or other startup inputs.
It runs only after the application's required providers are healthy, so it may also
idempotently provision provider-side resources such as a database, user, or API credential.
It returns whether managed output changed:

```go
StaticConfig(ctx, state) (changed bool, err error)
```

When static configuration changes, the reconciler restarts only affected services, in
dependency order.

#### Dynamic Configuration

Dynamic configuration runs after required providers and the consumer are healthy.

It performs idempotent runtime operations such as API calls, resource registration, and
inter-application linking:

```go
DynamicConfig(ctx, state) error
```

Dynamic configuration does not itself require a restart.

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

An integration is not reported as configured until required static configuration, health
verification, and dynamic configuration have succeeded.

### 6. Narrow Modules

Required architectural boundaries:

- `catalog`: loads and validates portable application manifests
- `planner`: calculates topology, integration, install, removal, and reconfiguration plans
- `state`: persists desired and observed application and integration state
- `runtime`: applies topology through host adapters
- `reconciler`: converges topology and integration state
- `configurator`: realizes static and dynamic application/integration configuration
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

## NixOS Transition

NixOS remains a temporary reference implementation while the portable runtime is built.

- Existing NixOS behavior is inventoried before replacement.
- Portable manifests and integration contracts become authoritative.
- New shared behavior is implemented in runtime-neutral core code.
- NixOS-specific modules may coexist only during migration.
- NixOS is not a permanent second release runtime.

The transition must inventory every responsibility currently hidden in `module.nix`,
activation scripts, native NixOS services, and systemd hooks.

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

- Static configuration from structured integration inputs
- Accurate `changed` reporting
- Idempotent repeated execution
- Dynamic configuration idempotency
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
- Static and dynamic configurator behavior
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
- Formalize static and dynamic configurator contracts.
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

When architecture and working behavior disagree, preserve user data and working behavior
first, then improve the boundary under tests.

Migration work also follows the rules in
[Portable Runtime Migration Design Rules](docs/migration-design-rules.md). Small,
fully-validated vertical slices are preferred over broad rewrites.
