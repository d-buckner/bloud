// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
import { test, expect } from '../lib/fixtures';
import { describeApp } from '../lib/app-suite';
import {
  expectInstalledInCatalog,
  expectRunningTile,
} from '../lib/apps';
import { ensureInstalled } from '../lib/api';
import { LoginPage } from '../lib/loginPage';

const HERMES_ORIGIN = 'http://hermes.localhost:8080';

// One test case per observable behavior; serial mode (from describeApp)
// means the first failure skips the rungs behind it.
//
// Hermes is a native-oidc app that joins Authentik as a PUBLIC PKCE client
// through the Hermes dashboard's bundled self-hosted OIDC provider. The
// dashboard enforces its own auth gate on the non-loopback bind Bloud
// reaches it through, and that gate hands the browser to the shared
// Authentik issuer, so the observable Bloud contract is the same shape as
// the other native-oidc apps: the gate is on, and a user can complete the
// Authentik round-trip and land back authenticated.
describeApp('hermes', (app) => {
  test('converges to running', async () => {
    // Infrastructure rung: fresh-VM image pull + first-run convergence
    // through the dependency-graph install path, including the public
    // native-oidc client registration with Authentik and the config.yaml
    // the configurator writes. A green converge means the SSO-backed
    // dashboard booted with its provider configured (the gate would fail
    // closed otherwise) and the orchestrator promoted the node to RUNNING.
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

  test('the auth gate is on: an unauthenticated visitor is bounced to sign-in', async () => {
    test.setTimeout(120_000);
    const hermes = await app.page.context().newPage();
    try {
      // The dashboard's non-loopback bind engages the gate; the SPA has no
      // session, so the first navigation lands on the dashboard's own
      // /login page rather than serving the app.
      await hermes.goto(`${HERMES_ORIGIN}/`);
      await hermes.waitForURL(/\/login/, { timeout: 60_000 });
    } finally {
      await hermes.close();
    }
  });

  test('signs in through Authentik and lands back authenticated', async () => {
    test.setTimeout(360_000);
    const hermes = await app.page.context().newPage();
    try {
      // Start the authorization-code + PKCE flow from the dashboard's
      // self-hosted provider. This hands the browser to the shared
      // Authentik issuer (sso.localhost), the same identity provider the
      // other SSO apps use, not the Nous Portal.
      await hermes.goto(`${HERMES_ORIGIN}/auth/login?provider=self-hosted`);
      await hermes.waitForURL(/sso\.localhost/, { timeout: 60_000 });

      // Complete the Authentik login on the issuer origin. The flow hops
      // through steps that re-render after document load, so poll within a
      // deadline rather than checking once.
      const loginPage = new LoginPage(hermes);
      const deadline = Date.now() + 240_000;
      for (;;) {
        if (hermes.url().includes('/auth/callback')) break;
        if (Date.now() > deadline) break;
        if (await loginPage.isVisible()) {
          await loginPage.login();
          continue;
        }
        await hermes.waitForTimeout(500);
      }

      // After the callback the dashboard returns the user to the app, and a
      // valid session cookie keeps them out of /login. Landing back on the
      // Hermes origin (off /login) is the observable proof the OIDC
      // round-trip authenticated the session: an unauthenticated user can
      // never stay off /login.
      await expect(hermes).toHaveURL(/hermes\.localhost:8080/, {
        timeout: 120_000,
      });
      await expect(hermes).not.toHaveURL(/\/login/, { timeout: 120_000 });
    } finally {
      await hermes.close();
    }
  });
});
