// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
import { test, expect } from '../lib/fixtures';

// AppFlowy runs without Bloud SSO (strategy "none"): its auth layer is a
// self-hosted GoTrue (Supabase Auth) container, and its cloud API
// authenticates GoTrue-minted JWTs, so proxy-level forward-auth cannot
// grant access. The user journey is therefore local email/password
// authentication, which also proves the nginx /gotrue proxy and the
// web->cloud wiring end to end.
//
// AppFlowy's free self-hosted license allows exactly one Member/Owner
// seat across all workspaces, so the e2e account is a fixed identity:
// the first run creates it through the sign-up flow, and re-runs against
// the same runtime log in with the same credentials.
const E2E_EMAIL = 'e2e@appflowy.local';
const E2E_PASSWORD = 'E2e-appflowy-123!';

// The signed-in shell lives at /app (the SPA then redirects to
// /app/:workspaceId). Match on the pathname: the host itself
// (appflowy.*) would match a naive /\/app/ regex.
const inAppShell = (u: URL) =>
  u.pathname === '/app' || u.pathname.startsWith('/app/');

test.describe('appflowy (no SSO — local sign-up/login)', () => {
  test.beforeEach(async ({ api }) => {
    await api.ensureInstalled('appflowy');
  });

  test('local auth reaches the AppFlowy app', async ({ authenticatedPage }) => {
    const page = authenticatedPage;

    // Open AppFlowy from the Bloud home screen — opens in a new tab
    await page.goto('/');
    const appflowyPagePromise = page.waitForEvent('popup');
    await page.getByText(/appflowy/i).first().click();
    const appPage = await appflowyPagePromise;

    // The SPA boots and, unauthenticated, renders the login screen. The
    // screen only renders once the web container and the /gotrue proxy
    // answer, so this doubles as a stack-wiring check. The bundle is large,
    // so give the app shell a generous deadline before interacting.
    const loginHeading = appPage.getByText(/welcome to appflowy/i);
    await loginHeading.waitFor({ state: 'visible', timeout: 120_000 });

    // Try logging in with the fixed e2e credentials first — re-runs
    // against the same runtime take this path.
    await appPage.getByTestId('login-email-input').fill(E2E_EMAIL);
    await appPage.getByTestId('login-password-button').click();
    await expect(appPage).toHaveURL(/enterPassword/, { timeout: 15_000 });
    await appPage.getByTestId('password-input').fill(E2E_PASSWORD);
    await appPage.getByTestId('password-submit-button').click();

    const loggedIn = await appPage
      .waitForURL(inAppShell, { timeout: 20_000 })
      .then(() => true)
      .catch(() => false);

    if (!loggedIn) {
      // The account does not exist yet (fresh deployment): create it
      // through the password sign-up form. The goto must be absolute —
      // the config baseURL (Bloud home) would swallow the relative path.
      const origin = new URL(appPage.url()).origin;
      await appPage.goto(`${origin}/login?action=signUpPassword`, {
        waitUntil: 'domcontentloaded',
      });
      await appPage.getByTestId('signup-email-input').fill(E2E_EMAIL);
      await appPage.getByTestId('signup-password-input').fill(E2E_PASSWORD);
      await appPage
        .getByTestId('signup-confirm-password-input')
        .fill(E2E_PASSWORD);
      await appPage.getByTestId('signup-submit-button').click();

      // GoTrue runs with auto-confirm (no SMTP), so sign-up establishes
      // the session immediately and the app navigates into the signed-in
      // shell at /app.
      await expect(appPage).toHaveURL(inAppShell, { timeout: 120_000 });
    }

    await expect(appPage.getByTestId('sidebar-page-header')).toBeVisible({
      timeout: 60_000,
    });
  });
});
