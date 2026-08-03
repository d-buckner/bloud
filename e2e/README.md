# Portable Runtime End-to-End Tests

Playwright suite that verifies the user-visible lifecycle of apps on an
already-provisioned portable runtime host.

## Tests

- **Jellyfin** — Installs Jellyfin via host-agent API, signs in to Bloud via
  Authentik, opens Jellyfin in the embedded iframe, and logs in via LDAP.
- **Navidrome** — Installs Navidrome via host-agent API, navigates to
  `navidrome.localhost:8080` through Traefik, completes Authentik forward-auth
  login, and verifies the Navidrome UI renders.

## Running

Against a prepared runtime (Lima VM with `./bloud dev`):

```bash
cd e2e && npx playwright test
```

Or via the CLI:

```bash
./bloud e2e              # Run Playwright tests against existing runtime
./bloud e2e lifecycle    # Deploy host-agent + catalog, then run full install/verify/uninstall cycle
```

`./bloud e2e` runs Playwright tests against the already-running host-agent on
the Lima VM. `./bloud e2e lifecycle` is a self-contained deploy→test→uninstall
flow that doesn't require `./bloud dev` to be running first.

## Full Portable Lifecycle

`./bloud e2e lifecycle` deploys the current host-agent binary and app catalog to
a Lima VM, installs the host-agent as a user systemd service, and runs the full
install/verify/uninstall cycle.

```bash
./bloud e2e lifecycle              # Full lifecycle
./bloud e2e lifecycle --host-only  # Skip Playwright browser tests
./bloud e2e lifecycle --keep       # Leave deployment running after tests
```

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `BLOUD_URL` | `http://localhost:3000` | Host-agent base URL |
| `BLOUD_E2E_USERNAME` | `e2etest` | Authentik test user |
| `BLOUD_E2E_PASSWORD` | `e2etest123` | Authentik test password |
| `BLOUD_E2E_LIMA_INSTANCE` | `bloud-dev` | Lima instance name |
