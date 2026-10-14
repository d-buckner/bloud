// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect } from '../lib/fixtures';
import type { Page } from '@playwright/test';
import { describeApp } from '../lib/app-suite';
import { expectInstalledInCatalog, expectRunningTile } from '../lib/apps';
import { ensureInstalled } from '../lib/api';
import { LoginPage } from '../lib/loginPage';
import { appOrigin, appUrlPattern } from '../lib/origin';

const PENPOT_URL = appOrigin('penpot');

// Penpot renders the identity provider's button with the name Bloud gave the
// provider (PENPOT_OIDC_NAME in the generated env file), so this assertion is
// the browser-side half of the OIDC wiring: the label only appears if the
// container booted from the file Bloud wrote and the flag reached the SPA.
const PROVIDER_LABEL = /bloud sso/i;

// Penpot is a seven-container install (its own postgres, valkey, the JVM
// backend, the headless-chromium exporter, the MCP server, the admin console,
// and the frontend), so the convergence rung gets the longest budget in the
// suite. If this one fails, everything after it is skipped, which is what
// separates "the images never came down" from "the login is broken".
const CONVERGENCE_TIMEOUT = 14 * 60_000;

describeApp('penpot', (app) => {
  test('converges to running', async () => {
    test.setTimeout(CONVERGENCE_TIMEOUT);
    await ensureInstalled('penpot');
  });

  test('appears in the catalog as installed', async () => {
    test.setTimeout(60_000);
    await expectInstalledInCatalog(app.page, 'Penpot');
  });

  test('appears on the home screen as a converged tile', async () => {
    test.setTimeout(60_000);
    await app.page.goto('/');
    await expectRunningTile(app.page, 'Penpot');
  });

  test('SSO gates the app: the login page launches the OIDC flow to the prompt', async ({
    browser,
  }) => {
    test.setTimeout(180_000);
    // Penpot's own session is a cookie on this context, and completing the
    // flow consumes the gate. The rungs that exercise sign-in therefore take a
    // fresh context each, so neither depends on the other having run.
    const context = await browser.newContext();
    try {
      const penpot = await context.newPage();
      await penpot.goto(PENPOT_URL);
      await launchOidcFlow(penpot);

      // Penpot sends the browser to its configured issuer, where this context
      // has no session, so the round trip must settle on the login form.
      await penpot.waitForURL(/sso\.localhost/, { timeout: 60_000 });
      const loginPage = new LoginPage(penpot);
      await loginPage.usernameField.waitFor({ state: 'visible', timeout: 30_000 });
    } finally {
      await context.close();
    }
  });

  test('signs in through OIDC and reaches the Penpot dashboard', async ({
    browser,
  }) => {
    test.setTimeout(360_000);
    const context = await browser.newContext();
    try {
      const penpot = await context.newPage();
      await penpot.goto(PENPOT_URL);
      await launchOidcFlow(penpot);

      // Penpot's callback is server-side (/api/auth/oidc/callback), and the
      // Authentik flow can stop on a prompt step that re-renders after load, so
      // poll for the form within a deadline rather than checking once.
      const loginPage = new LoginPage(penpot);
      const backInPenpot = appUrlPattern('penpot', '/#/(?!auth/login)');
      const deadline = Date.now() + 240_000;
      for (;;) {
        if (backInPenpot.test(penpot.url())) break;
        if (Date.now() > deadline) break;
        if (await loginPage.isVisible()) {
          await loginPage.login();
          continue;
        }
        await penpot.waitForTimeout(500);
      }

      // Penpot created the account from the provider's claims (matched by
      // email) and landed the browser on the dashboard rather than back on the
      // login page.
      await expect(penpot).toHaveURL(backInPenpot, { timeout: 120_000 });
      await expect(penpot).toHaveURL(/#\/dashboard/, { timeout: 60_000 });

      // The login affordance is gone: the session is Penpot's own now.
      await expect(penpot.getByText(PROVIDER_LABEL)).toHaveCount(0);
    } finally {
      await context.close();
    }
  });

  test('serves the configuration Bloud generated', async ({ browser }) => {
    test.setTimeout(120_000);
    // The frontend image renders this file from its environment at container
    // start, so what the browser can fetch is what the install configured:
    // the flags, and the provider name shown on the login page.
    const context = await browser.newContext();
    try {
      const penpot = await context.newPage();
      const response = await penpot.request.get(PENPOT_URL + '/js/config.js');
      expect(response.ok()).toBeTruthy();
      const config = await response.text();
      expect(config).toContain('enable-mcp');
      expect(config).toContain('enable-login-with-oidc');
      expect(config).toContain('Bloud SSO');
      expect(config).toContain(`var penpotPublicURI = "${PENPOT_URL}";`);
    } finally {
      await context.close();
    }
  });
});

/**
 * Drive Penpot's unauthenticated UI to the OIDC redirect. An unauthenticated
 * visit lands on `#/auth/login`, whose provider button starts the
 * authorization-code flow at the issuer. Penpot's bundle is large, so wait
 * generously before clicking.
 */
async function launchOidcFlow(penpot: Page): Promise<void> {
  const providerButton = penpot.getByText(PROVIDER_LABEL);
  await providerButton.waitFor({ state: 'visible', timeout: 120_000 });
  await providerButton.click();
}
