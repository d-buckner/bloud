// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
import { test, expect } from '../lib/fixtures';
import { LoginPage } from '../lib/loginPage';
import { TEST_CREDS } from './constants';

// AppFlowy's auth layer is its own GoTrue (Supabase Auth) container; the
// cloud API authenticates GoTrue-minted JWTs, so proxy-level forward-auth
// cannot grant access. Bloud wires SSO into GoTrue instead: an Authentik
// OAuth2 application plus a GoTrue custom OIDC provider ("Bloud SSO"
// login button), managed idempotently by the app configurator.
//
// Deployment classes (GoTrue rejects local/loopback issuers — verified):
//   - public BLOUD_BASE_DOMAIN: the SSO journey below runs (Bloud SSO
//     button -> Authentik -> callback -> /app), and local sign-up stays
//     available as a fallback.
//   - localhost/loopback (dev VM): the wiring is skipped and the local
//     email/password journey below is the contract; it also proves the
//     nginx /gotrue proxy and the web->cloud wiring end to end.
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

test.describe('appflowy (SSO: Bloud SSO button; fallback: local sign-up)', () => {
  test.beforeEach(async ({ api }) => {
    await api.ensureInstalled('appflowy');
  });

  test('SSO journey: sign in with Bloud SSO', async ({ browser }) => {
    const bloudUrl = new URL(process.env.BLOUD_URL ?? 'http://localhost:8080');
    const host = bloudUrl.hostname;
    test.skip(
      host === 'localhost' || host === '127.0.0.1',
      'GoTrue rejects local issuers (localhost/loopback): the SSO wiring is skipped on this deployment; local sign-up is the contract (covered below)',
    );

    const appOrigin = `http://appflowy.${host}:${bloudUrl.port || 8080}`;
    const tlsPort = process.env.BLOUD_TRAEFIK_TLS_PORT ?? '8443';

    // Fresh context: no Bloud or Authentik session yet. The self-signed
    // SSO origin requires ignoreHTTPSErrors.
    const context = await browser.newContext({ ignoreHTTPSErrors: true });
    const page = await context.newPage();

    // Login screen: the "Bloud SSO" button only renders when the
    // configurator registered the custom provider in GoTrue, so its
    // presence is the wiring assertion.
    await page.goto(`${appOrigin}/login`, { waitUntil: 'domcontentloaded' });
    const loginHeading = page.getByText(/welcome to appflowy/i);
    await loginHeading.waitFor({ state: 'visible', timeout: 120_000 });

    const ssoButton = page.getByRole('button', { name: /bloud sso/i });
    await expect(ssoButton).toBeVisible({ timeout: 30_000 });
    await ssoButton.click();

    // The browser bounces to the SSO issuer (Authentik) over TLS.
    await page.waitForURL(
      (u) =>
        u.hostname === `sso.${host}` &&
        u.pathname.startsWith('/application/o/appflowy/'),
      { timeout: 30_000 },
    );

    // Log in with the Bloud identity on the Authentik login page.
    const loginPage = new LoginPage(page);
    await expect(loginPage.usernameField).toBeVisible({ timeout: 60_000 });
    await loginPage.login(TEST_CREDS.USERNAME, TEST_CREDS.PASSWORD);

    // After authorization the browser is sent back to the GoTrue
    // callback on the app's public origin, where the session is
    // established and the SPA navigates into the signed-in shell.
    await expect(page).toHaveURL(inAppShell, { timeout: 120_000 });
    await expect(page.getByTestId('sidebar-page-header')).toBeVisible({
      timeout: 60_000,
    });

    await context.close();
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
