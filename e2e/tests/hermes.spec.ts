// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect } from '../lib/fixtures';
import type { BrowserContext, Page } from '@playwright/test';
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
// dashboard enforces its own auth gate on the bind Bloud reaches it through,
// and that gate hands the browser to the shared Authentik issuer, so the
// observable Bloud contract is the same shape as the other native-oidc apps:
// the gate is on, and a user can complete the Authentik round-trip and land
// back authenticated. The issuer origin is the host loopback
// (localhost:8080), which the dashboard reaches because the container runs
// with the host network namespace (catalog sso.loopbackIssuer).
//
// The sign-in rungs use an isolated browser context. The shared suite context
// authenticated as the E2E user carries an Authentik session, and the
// dashboard's gate hands such a visitor straight through; only a session-less
// context observes the sign-in prompt and exercises the credential exchange.
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

  test('the auth gate is on: a session-less visitor is handed to sign-in', async () => {
    test.setTimeout(120_000);
    const ctx = await newIsolatedContext(app);
    const hermes = await ctx.newPage();
    try {
      // No Bloud and no Authentik session here, so the dashboard's gate must
      // intercept: its single registered provider hands the visitor to the
      // shared Authentik flow on the loopback issuer origin instead of
      // serving the app.
      await hermes.goto(`${HERMES_ORIGIN}/`);
      await new LoginPage(hermes).usernameField.waitFor({
        state: 'visible',
        timeout: 60_000,
      });
    } finally {
      await ctx.close();
    }
  });

  test('signs in through Authentik and lands back authenticated', async () => {
    test.setTimeout(360_000);
    const ctx = await newIsolatedContext(app);
    const hermes = await ctx.newPage();
    try {
      // Start at the app origin: the gate bounces an unauthenticated visitor
      // into the self-hosted provider's authorization-code + PKCE flow, which
      // is the shared Authentik issuer, not the Nous Portal.
      await hermes.goto(`${HERMES_ORIGIN}/`);

      // Complete the Authentik login on the issuer origin. The flow hops
      // through steps that re-render after document load, so poll within a
      // deadline rather than checking once.
      const loginPage = new LoginPage(hermes);
      const deadline = Date.now() + 240_000;
      for (;;) {
        const url = hermes.url();
        if (url.startsWith(HERMES_ORIGIN) && !url.includes('/login') && !url.includes('/auth/')) break;
        if (Date.now() > deadline) break;
        if (await loginPage.isVisible()) {
          await loginPage.login();
          continue;
        }
        await hermes.waitForTimeout(500);
      }

      // Back on the app origin, off the sign-in route.
      await expect(hermes).toHaveURL(/hermes\.localhost:8080/, {
        timeout: 120_000,
      });
      await expect(hermes).not.toHaveURL(/\/login/, { timeout: 120_000 });

      // The dashboard authenticates the session against the issuer, so a
      // successful /api/auth/me is the proof the round-trip established a
      // real session (landing off /login alone would also match a dead page).
      const status = await hermes.evaluate(
        async () => (await fetch('/api/auth/me')).status,
      );
      expect(status).toBe(200);
    } finally {
      await ctx.close();
    }
  });
});

// newIsolatedContext opens a browser context with no inherited cookies, so the
// gate and sign-in rungs observe a genuinely session-less visitor.
async function newIsolatedContext(app: { page: Page }): Promise<BrowserContext> {
  const browser = app.page.context().browser();
  if (!browser) {
    throw new Error('no browser handle for an isolated context');
  }
  return browser.newContext();
}
