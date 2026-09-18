// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
import { test, expect } from '../lib/fixtures';
import { describeApp } from '../lib/app-suite';
import {
  expectInstalledInCatalog,
  expectRunningTile,
  openAppFromHome,
} from '../lib/apps';
import { ensureInstalled } from '../lib/api';

const HERMES_ORIGIN = 'http://hermes.localhost:8080';

// One test case per observable behavior; serial mode (from describeApp)
// means the first failure skips the rungs behind it.
//
// Hermes is `sso: none`: it does not join Authentik and carries its own
// dashboard credential, so the Bloud-owned contract is exactly two things,
// not a login round-trip:
//
//   1. The install converges through the real dependency-graph path. This
//      rung is itself the credential contract proved end to end: if
//      {{appAdminPassword}} failed to resolve the container fails closed
//      (empty basic-auth password => the gate refuses to start), and if the
//      gate-exempt /api/health never answered the orchestrator would never
//      promote the node to RUNNING. A green "converges to running" is
//      therefore the credential + health + route contract, not a tautology.
//
//   2. Bloud attaches NO SSO middleware to the route. That is the whole
//      point of `sso: none` vs the SSO-backed apps: a request reaching the
//      app must land on the Hermes origin and never round-trip the shared
//      Authentik flow (/if/flow). A route accidentally wired into the
//      forward-auth/oidc chain would bounce to /if/flow and fail this.
//
// The live dashboard login through the generated password is NOT drivable
// from the browser harness: the credential lives only in the host's
// secrets.json (appSecrets.hermes.adminPassword) and no host-agent API
// exposes it — by design, the secret is not a browser-visible surface. That
// login round-trip is the app's own upstream surface, not Bloud's. The
// gate-exempt /api/health is what the e2e suite can deterministically
// observe over the real public route.
describeApp('hermes', (app) => {
  test('converges to running', async () => {
    // Infrastructure rung: fresh-VM image pull + first-run convergence
    // through the dependency-graph install path. If this fails, the UI
    // rungs below are skipped, distinguishing a broken install from a
    // misbehaving app.
    test.setTimeout(12 * 60_000);
    await ensureInstalled('hermes');
  });

  test('appears in the catalog as installed', async () => {
    test.setTimeout(60_000);
    await expectInstalledInCatalog(app.page, 'Hermes');
  });

  test('appears on the home screen as a converged tile', async () => {
    test.setTimeout(60_000);
    await app.page.goto('/');
    await expectRunningTile(app.page, 'Hermes');
  });

  test('launching from the home tile opens the Hermes origin, not the Bloud SSO flow', async () => {
    test.setTimeout(120_000);
    const hermes = await openAppFromHome(app.page, 'Hermes');
    try {
      // The tile launches the app on its own origin. Because sso is none,
      // there is no Bloud session hand-off: the popup stays on
      // hermes.localhost and never carries the user through /if/flow.
      await expect(hermes).toHaveURL(/hermes\.localhost:8080/, {
        timeout: 30_000,
      });
      expect(hermes.url()).not.toContain('/if/flow');
    } finally {
      await hermes.close();
    }
  });

  test('is reachable on the public route: the gate-exempt health path answers directly', async ({
    request,
  }) => {
    test.setTimeout(60_000);
    // Traefik routes hermes.localhost:8080 straight to the dashboard with
    // no auth middleware in front. /api/health is exempt from the Hermes
    // gate upstream, so a direct request over the public route returns 200:
    // this proves (a) the regenerated route points at the Hermes backend,
    // and (b) the dashboard is actually serving — without needing the
    // generated credential. The request is not redirected to the SSO host.
    const health = await request.get(`${HERMES_ORIGIN}/api/health`);
    expect(health.status()).toBe(200);
    expect(health.url()).not.toContain('/if/flow');
  });
});
