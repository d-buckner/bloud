# Bloud End-to-End Tests

Playwright suite that verifies the user-visible lifecycle of apps on an
already-provisioned Bloud host.

## Tests

Each app spec is a behavior ladder: one test case per observable stage,
declared via `describeApp` (`lib/app-suite.ts`): a real `test.describe`
that supplies a shared authenticated page and runs the block serially, so
the first failed case skips the rest and the report names the stage that
broke. Convergence runs as a named first case (`converges to running`);
only the sign-in case is app-specific (`jellyfin.spec.ts` is the
reference).

- **Jellyfin**: converges to running, appears in catalog/home, opens from
  the home tile, and logs in via LDAP to reach the dashboard.
- **Navidrome**: converges to running, appears in catalog/home, is gated by
  forward-auth (the popup lands on the Authentik prompt), then completes that
  login and verifies the Navidrome UI renders.
- **Immich**: completes the native-oidc SSO round-trip (auto-launched from
  the login page), walks first-login onboarding, and verifies the photos
  page renders.
- **Paperless-ngx**: converges to running, appears in catalog/home, hands an
  unauthenticated visitor straight to the issuer (native-oidc through
  django-allauth; its own sign-in page offers only the provider), and after
  the Authentik login reaches the dashboard with the session authenticating
  the app's API.
- **Hermes**: converges to running (as a native-oidc *public* PKCE client),
  appears in catalog/home, has its own auth gate on (an unauthenticated
  visitor is handed to a sign-in surface, never the dashboard), and completes
  the Authentik round-trip to land back authenticated on the Hermes origin.
- **Vaultwarden**: converges to running, appears in catalog/home, offers only
  "Use single sign-on" on its sign-in page (`SSO_ONLY`), and signs in through
  OIDC to the vault (setting or entering the master password). The web vault
  refuses plain-HTTP servers, so that last step needs the opt-in dev switch
  (`BLOUD_DEV_VAULTWARDEN_ALLOW_HTTP=1` on the host-agent and for Playwright) and
  is skipped without it; CI sets it for the `vaultwarden` leg.
- **Sonarr / Radarr / Prowlarr**: gated by forward-auth (the popup lands on
  the Authentik prompt), then the app's own UI renders with no login form of
  its own, since Bloud provisions `AuthenticationMethod=External`.
- **qBittorrent**: the same forward-auth ladder; its post-sign-in case also
  proves the WebUI subnet whitelist took effect (qBittorrent's private UI
  loads, not its login page).
- **Seerr**: declares no SSO (`sso.strategy: none`), so the popup lands on
  Seerr's own login page with a Jellyfin sign-in affordance and *not* on the
  first-run setup wizard: the observable proof that Bloud's onboarding
  (Jellyfin connection + `settings/initialize`) completed.

The media-stack wiring (Sonarr/Radarr → qBittorrent, Prowlarr → the PVRs) is
proven in the **integration tier** rather than here:
`./bloud validate --tier integration` deploys a runtime and runs the
`services/host-agent/internal/e2e` binary inside it, whose
`media_stack_test.go` installs qBittorrent, Sonarr, Radarr and Prowlarr through
the real API and then asserts through each app's own API (reading the
instances' `X-Api-Key` from the runtime data dir the way the consumers do)
that Sonarr and Radarr list a connectable `QBittorrent` download client, that
their root folders (`/shows`, `/movies`) are registered, and that Prowlarr
lists and can connect both PVRs. Seerr is deliberately absent from that tier
(its onboarding is Jellyfin-bound and is asserted by the spec above), and its
own PVR list is an admin-only page reachable only with the per-deployment
account Bloud onboards, so Seerr → the PVRs is covered by the configurator's
unit tests and by the spec above, and no tier asserts it end to end.

The forward-auth ladders (Sonarr, Radarr, Prowlarr, qBittorrent) share
`lib/forwardAuth.ts`; app-specific selectors and title patterns stay in each
spec so a failure names the app and the rung.

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

## Full Lifecycle

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
| `BLOUD_URL` | `http://localhost:8080` | Public ingress (Traefik): browser tests go through this port |
| `BLOUD_API_URL` | `http://localhost:3000` | Internal host-agent API used by the test helpers |
| `BLOUD_E2E_USERNAME` | `e2etest` | Authentik test user |
| `BLOUD_E2E_PASSWORD` | `e2etest123` | Authentik test password |
| `BLOUD_E2E_LIMA_INSTANCE` | `bloud-dev` | Lima instance name |
