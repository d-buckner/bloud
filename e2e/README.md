# Portable Runtime End-to-End Tests

This Playwright suite verifies the user-visible lifecycle of apps on an already-provisioned
portable runtime host. Environment provisioning and teardown belong outside Playwright.

The runtime host must provide Podman, systemd with Quadlet support, and an accessible
Podman API socket through `BLOUD_PODMAN_SOCKET` or `XDG_RUNTIME_DIR`.

The current release slice covers Jellyfin:

1. Sign in to Bloud.
2. Install Jellyfin through the catalog UI if it is not already installed.
3. Verify the Jellyfin embed endpoint is reachable.
4. Open Jellyfin from the Bloud dashboard.
5. Sign in to Jellyfin using the Bloud account through LDAP.

Run against a prepared runtime:

```bash
BLOUD_URL=http://localhost:3000 \
BLOUD_E2E_USERNAME=e2etest \
BLOUD_E2E_PASSWORD=e2etest123 \
npm --prefix e2e test -- --project=jellyfin
```

The repository CLI provides the same entry point:

```bash
BLOUD_URL=http://localhost:3000 ./bloud e2e
```

## Full Portable Lifecycle

`./bloud e2e lifecycle` deploys the current host-agent binary and app catalog to an
SSH-accessible Linux host, installs the host-agent as a user systemd service, and verifies:

1. Jellyfin installation through the UI or host-local API.
2. Managed Podman container, Quadlet unit, route, and database state.
3. Jellyfin and host-agent restart recovery.
4. Jellyfin uninstall and data cleanup.

The target must be isolated test infrastructure with user systemd, Podman/Quadlet, and
the core Bloud services already provisioned: PostgreSQL, Redis, Authentik with LDAP, and
ingress routing. Ingress must watch `BLOUD_E2E_TRAEFIK_DYNAMIC_DIR`, which defaults to
the lifecycle runtime's `data/traefik/dynamic` directory. The lifecycle runner does not
modify or tear down those core services. Use an isolated Bloud database because the
runner removes any existing Jellyfin state.

The default target is the repository's `bloud-dev` Lima VM using
`dev/host-agent.env`:

```bash
./bloud e2e lifecycle
```

Set `BLOUD_E2E_LIMA_INSTANCE` to use another Lima instance. For a generic Linux
host, set `BLOUD_E2E_SSH_TARGET`, `BLOUD_E2E_ENV_FILE`, and `BLOUD_URL`.
On Lima, the runner ensures the compose Redis service is published on
`127.0.0.1:6379`; override with `BLOUD_E2E_REDIS_ADDR` if needed.

Use `--host-only` to skip Playwright or `--keep` to leave the deployed host-agent
service and remote runtime directory running for inspection. Without `--keep`, the
runner removes its host-agent service, managed Jellyfin state, and remote runtime
directory after collecting any failure diagnostics under
`e2e/test-results/runtime-lifecycle/`. It refuses to use or remove a remote runtime
directory that it did not mark as runner-owned.
