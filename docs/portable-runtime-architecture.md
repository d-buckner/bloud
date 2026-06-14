# Portable Runtime Architecture

**Status:** Target architecture  
**Initial target:** Debian 13, `x86_64`, systemd  
**Replaces for release:** NixOS ISO and NixOS rebuild execution

## Purpose

Bloud is moving from a NixOS distribution to a binary installed on an existing Linux server.
The portable runtime replaces NixOS as the mechanism that creates and manages services. It
does not replace the dashboard, same-origin routing, dependency graph, configurators, or SSO.

The first supported host is Debian 13. Other distributions are separate compatibility
targets, not implied support.

## Runtime Boundary

```text
runtime-neutral core
  catalog + planner + integration graph + reconciler + configurators
                              |
                              v
Debian runtime adapters
  Podman + Quadlet + systemd + filesystem + host networking
```

The core calculates desired state. Runtime adapters apply it and report observed state.
Adapters do not decide which dependencies or integrations should exist.

## NixOS Responsibility Replacement

| Current NixOS responsibility | Portable replacement |
|---|---|
| Host-agent installation | Versioned `.deb` and `bloud.service` |
| App enablement | Desired-state store and reconciler |
| Container definitions | Portable manifests and generated Quadlet |
| systemd units and ordering | Quadlet/systemd adapter |
| Directories and permissions | Filesystem adapter |
| Rootless Podman network | Managed rootful Podman networks |
| Native PostgreSQL and Redis | Bloud-managed containers |
| Native service configuration | Portable topology plus configurators |
| Firewall and privileged ports | Host-network adapter and preflight |
| Nix activation scripts | Idempotent runtime adapters/configurators |
| NixOS rollback | Durable desired state, recorded host changes, and reconciliation |
| ISO installer | Debian package installation and `bloud init` |

Every release-critical behavior currently encoded in `module.nix`, Nix helpers, activation
scripts, or native services must be inventoried before its NixOS implementation is removed.

## Privilege Model

The initial portable runtime uses rootful Podman. This is a deliberate scope decision:

- AdGuard Home requires port 53 and reversible host DNS changes.
- Traefik requires privileged web ports.
- Media apps need predictable shared-directory access.
- system-level services must start reliably before user login.
- Rootless host/service networking is currently a significant source of complexity.

Containers should use non-root users internally when supported. Rootless Podman may become a
future runtime profile after the rootful runtime is reliable.

## Packaging and Layout

```text
/usr/bin/bloud
/usr/lib/systemd/system/bloud.service

/etc/bloud/
  config.yaml

/var/lib/bloud/
  state/
  secrets/
  apps/
  shared/
  generated/
    quadlet/
    routing/
  host-backups/
```

The `.deb` package installs the binary and service. `sudo bloud init` performs host preflight,
records host state that may need restoration, and establishes core desired state.

## Portable Application Manifest

The portable manifest must represent the entire release stack without hidden runtime
behavior:

```yaml
name: immich

services:
  server:
    image: ghcr.io/immich-app/immich-server:v1.130.3
    health:
      http: /api/server/ping
  machine-learning:
    image: ghcr.io/immich-app/immich-machine-learning:v1.130.3

consumes:
  database:
    required: true
    providers: [postgres]
    capabilities: [pgvector]
    configure: [static, dynamic]
  cache:
    required: true
    providers: [redis]
    configure: [static]
  sso:
    required: false
    providers: [authentik]
    configure: [dynamic]

routing:
  embed: true
  port: 2283

data:
  preserveOnRemove: true
```

The final schema is not fixed by this example. The requirement is that topology,
capabilities, integrations, health, routes, and persistence are explicit and testable.

## Runtime Application Flow

```text
desired topology
      |
      v
generate Quadlet and managed files
      |
      v
systemctl daemon-reload
      |
      v
start/restart affected services in dependency order
      |
      v
observe health and report actual state
```

Runtime application is separate from integration configuration. Starting two healthy
containers does not mean their relationship is configured.

## Infrastructure

PostgreSQL, Redis, Traefik, and Authentik run as Bloud-managed containers. This avoids
depending on distribution package versions and provides a consistent provider contract.

Inter-service communication uses managed Podman networks. Consumer configurators receive
typed provider outputs rather than container names or hard-coded addresses.

## Host Changes

Host-level changes must be explicit, recorded, and reversible. This especially applies to
AdGuard Home:

- Detect port 53 conflicts before apply.
- Record prior resolver configuration.
- Apply DNS changes only after AdGuard health is verified.
- Restore prior state on failed apply or removal.
- Verify DNS from a separate test client.

## Migration Strategy

NixOS remains a temporary reference runtime during migration:

1. Inventory existing NixOS behavior.
2. Add characterization tests.
3. Define the equivalent portable manifest and integration contracts.
4. Implement runtime adapters.
5. Verify on a clean Debian VM.
6. Remove the NixOS behavior only after parity is proven.

Bloud will not permanently maintain NixOS and Debian as equal release runtimes.

Implementation work follows
[Portable Runtime Migration Design Rules](migration-design-rules.md) and begins with small,
fully-validated slices such as
[Slice 001: Typed Integration Resolution](slices/001-typed-integration-instances.md).
