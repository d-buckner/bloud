// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
import { test, expect } from '../lib/fixtures';
import { LoginPage } from '../lib/loginPage';

test.describe('affine (native-oidc)', () => {
  test.beforeEach(async ({ api }) => {
    await api.ensureInstalled('affine');
  });

  test('SSO login reaches the AFFiNE workspace', async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Open AFFiNE from the Bloud home screen — opens in a new tab
    await page.goto('/');
    const affinePagePromise = page.waitForEvent('popup');
    await page.getByText(/affine/i).first().click();
    const affinePage = await affinePagePromise;
    await affinePage.waitForLoadState();

    // Unauthenticated AFFiNE renders its editor with a "Sign in and enable"
    // button (the local workspace is read-only until sign-in). Clicking it
    // opens the sign-in modal; "Continue with OIDC" starts the OIDC flow on
    // the issuer origin (sso.localhost), where the Bloud identity provider
    // (Authentik) completes the login.
    //
    // The AFFiNE bundle is large, so give the app shell a generous deadline
    // before interacting with it; the 10-minute test timeout is the outer
    // bound.
    const signInButton = affinePage
      .getByRole('button', { name: /sign in/i })
      .first();
    await signInButton.waitFor({ state: 'visible', timeout: 120_000 });
    await signInButton.click();

    const oidcButton = affinePage.getByRole('button', {
      name: /continue with oidc/i,
    });
    await oidcButton.waitFor({ state: 'visible', timeout: 30_000 });
    await oidcButton.click();

    // Complete the Authentik login on the issuer origin. The Bloud session
    // from the home screen does not carry over (different origin), so the
    // identifier-first login page is expected here.
    await affinePage.waitForURL(/sso\.localhost/, { timeout: 60_000 });
    const loginPage = new LoginPage(affinePage);
    await loginPage.login();

    // AFFiNE exchanges the code at /oauth/callback, creates the app account
    // on first login (matched by email), and redirects into the user's
    // workspace.
    await expect(affinePage).toHaveURL(/\/workspace\//, { timeout: 120_000 });

    // The signed-in shell no longer offers the sign-in button.
    await expect(
      affinePage.getByRole('button', { name: /sign in and enable/i }),
    ).toBeHidden({ timeout: 30_000 });
  });
});
