// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect } from '../lib/fixtures';
import type { Page } from '@playwright/test';
import { describeApp } from '../lib/app-suite';
import {
  expectInstalledInCatalog,
  expectRunningTile,
  openAppFromHome,
} from '../lib/apps';
import { ensureInstalled } from '../lib/api';
import { LoginPage } from '../lib/loginPage';

const PAPERLESS_NGX_URL = 'http://paperless-ngx.localhost:8080';

// One test case per observable behavior; serial mode (from describeApp)
// means the first failure skips the rungs behind it. Paperless-ngx uses
// native-oidc through django-allauth: the unauthenticated user lands on the
// app's own sign-in page, whose "Bloud SSO" button is a form that posts the
// authorization request, and the Authentik login happens on the issuer
// origin, where this context has no session.
describeApp('paperless-ngx', (app) => {
  test('converges to running', async () => {
    // Infrastructure rung: fresh-VM image pull of five containers (webserver,
    // postgres, redis, gotenberg, tika) + first-run migrations. When this
    // fails, the UI rungs below are skipped, which distinguishes a broken
    // install from a misbehaving app.
    test.setTimeout(15 * 60_000);
    await ensureInstalled('paperless-ngx');
  });

  test('appears in the catalog as installed', async () => {
    test.setTimeout(60_000);
    await expectInstalledInCatalog(app.page, 'Paperless-ngx');
  });

  test('appears on the home screen as a converged tile', async () => {
    test.setTimeout(60_000);
    await app.page.goto('/');
    await expectRunningTile(app.page, 'Paperless-ngx');
  });

  test('SSO gates the app: unauthenticated visitors land on the provider', async () => {
    test.setTimeout(180_000);
    const paperless = await openAppFromHome(app.page, 'Paperless-ngx');
    try {
      // An unauthenticated visitor is handed straight to the issuer: the app
      // disables its own password form and redirects to the provider, so the
      // first thing the tab shows is Authentik's prompt.
      await paperless.waitForURL(/sso\.localhost/, { timeout: 60_000 });
      const loginPage = new LoginPage(paperless);
      await loginPage.usernameField.waitFor({
        state: 'visible',
        timeout: 30_000,
      });

      // The sign-in page itself is only observable with the app's own
      // "just logged out" flag, which suppresses the automatic redirect. It
      // must offer the provider and no password form of its own.
      await paperless.goto(`${PAPERLESS_NGX_URL}/accounts/login/?loggedout=1`);
      await expect(paperless).toHaveURL(/\/accounts\/login\//);
      await expect(
        paperless.locator('#social-login button[type="submit"]'),
      ).toBeVisible();
      await expect(paperless.locator('input#inputUsername')).toHaveCount(0);

      // The button still starts the flow by hand.
      await startOidcLogin(paperless);
      await paperless.waitForURL(/sso\.localhost/, { timeout: 60_000 });
    } finally {
      await paperless.close();
    }
  });

  test('signs in through OIDC and reaches the dashboard', async () => {
    test.setTimeout(360_000);
    const paperless = await app.page.context().newPage();
    try {
      await paperless.goto(PAPERLESS_NGX_URL);

      await startOidcLogin(paperless);

      // Complete the Authentik login on the issuer origin. The flow hops
      // through /if/flow/... steps that re-render after document load, so
      // poll for the form within a deadline instead of checking once.
      const loginPage = new LoginPage(paperless);
      const deadline = Date.now() + 240_000;
      for (;;) {
        if (paperless.url().includes('/dashboard')) break;
        if (Date.now() > deadline) break;

        if (await loginPage.isVisible()) {
          await loginPage.login();
          continue;
        }

        await paperless.waitForTimeout(500);
      }

      // allauth creates the account on first sign-in (auto-signup) and the
      // app redirects to its dashboard. That URL is the session check: the
      // dashboard is only served to an authenticated session, and the
      // sign-in page is where an unauthenticated one lands.
      await expect(paperless).toHaveURL(/paperless-ngx\.localhost:8080\/dashboard/, {
        timeout: 120_000,
      });
      await expect(paperless.locator('input#inputUsername')).toHaveCount(0);

      // Rendering the shell is not enough: the dashboard's own first calls are
      // API calls, and Paperless-ngx grants a new account no permissions, so an
      // account Bloud did not put in its baseline group gets 403 from every one
      // of them. These two are the endpoints a signed-in but permissionless
      // account fails on, which is what this asserts against.
      const statuses = await paperless.evaluate(async () => {
        const paths = ['/api/ui_settings/', '/api/saved_views/'];
        const codes: Record<string, number> = {};
        for (const path of paths) {
          const res = await fetch(path, { credentials: 'include' });
          codes[path] = res.status;
        }
        return codes;
      });
      expect(statuses).toEqual({
        '/api/ui_settings/': 200,
        '/api/saved_views/': 200,
      });
    } finally {
      await paperless.close();
    }
  });
});

/**
 * Start the authorization-code flow from the app's sign-in page. allauth
 * renders one button per configured provider, and that button submits a form
 * (the POST is what hands the browser to the issuer).
 *
 * The app redirects to the issuer on its own (PAPERLESS_REDIRECT_LOGIN_TO_SSO),
 * so normally the button is never clickable: the page submits its own form as
 * soon as it renders. The click below is for the sign-in page that does show it
 * (after a logout, when the app suppresses the redirect), and a click that
 * loses the race against the automatic submission is not a failure.
 */
async function startOidcLogin(paperless: Page): Promise<void> {
  const providerButton = paperless.locator('#social-login button[type="submit"]');
  try {
    await providerButton.click({ timeout: 5_000 });
  } catch {
    // Already on its way to the issuer.
  }
}
