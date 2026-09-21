// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
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

const PAPERLESS_URL = 'http://paperless.localhost:8080';

// One test case per observable behavior; serial mode (from describeApp)
// means the first failure skips the rungs behind it. Paperless-ngx uses
// native-oidc through django-allauth: the unauthenticated user lands on the
// app's own sign-in page, whose "Bloud SSO" button is a form that posts the
// authorization request, and the Authentik login happens on the issuer
// origin, where this context has no session.
describeApp('paperless', (app) => {
  test('converges to running', async () => {
    // Infrastructure rung: fresh-VM image pull of five containers (webserver,
    // postgres, redis, gotenberg, tika) + first-run migrations. When this
    // fails, the UI rungs below are skipped, which distinguishes a broken
    // install from a misbehaving app.
    test.setTimeout(15 * 60_000);
    await ensureInstalled('paperless');
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

  test('SSO gates the app: the sign-in page offers the provider and reaches the prompt', async () => {
    test.setTimeout(180_000);
    const paperless = await openAppFromHome(app.page, 'Paperless-ngx');
    try {
      // Unauthenticated requests are redirected to the app's sign-in page,
      // which renders one button per configured allauth provider.
      await expect(paperless).toHaveURL(/\/accounts\/login\//, {
        timeout: 60_000,
      });
      await expect(paperless.locator('input#inputUsername')).toBeVisible();

      await startOidcLogin(paperless);

      // The flow leaves the app origin for the issuer, where this context has
      // no session, so it must settle on the Authentik prompt.
      await paperless.waitForURL(/sso\.localhost/, { timeout: 60_000 });
      const loginPage = new LoginPage(paperless);
      await loginPage.usernameField.waitFor({
        state: 'visible',
        timeout: 30_000,
      });
    } finally {
      await paperless.close();
    }
  });

  test('signs in through OIDC and reaches the dashboard', async () => {
    test.setTimeout(360_000);
    const paperless = await app.page.context().newPage();
    try {
      await paperless.goto(PAPERLESS_URL);

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
      await expect(paperless).toHaveURL(/paperless\.localhost:8080\/dashboard/, {
        timeout: 120_000,
      });
      await expect(paperless.locator('input#inputUsername')).toHaveCount(0);
      // No API probe here: a freshly auto-created account has no document
      // permissions yet, so the REST API answers 403 for it by design. The Go
      // integration test covers the API path with the internal admin.
    } finally {
      await paperless.close();
    }
  });
});

/**
 * Start the authorization-code flow from the app's sign-in page. allauth
 * renders one button per configured provider, and that button submits a form
 * (the POST is what hands the browser to the issuer), so the spec clicks it
 * the way a user does.
 */
async function startOidcLogin(paperless: Page): Promise<void> {
  const providerButton = paperless.locator('#social-login button[type="submit"]');
  await providerButton.waitFor({ state: 'visible', timeout: 60_000 });
  await providerButton.click();
}
