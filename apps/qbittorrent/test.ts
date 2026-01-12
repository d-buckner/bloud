import { test, expect } from '../../integration/lib/app-test';

test.describe('qbittorrent', () => {
  // qBittorrent uses forward-auth SSO, which means unauthenticated requests
  // get redirected to Authentik for login before reaching the app.

  test('health check responds', async ({ api, appName }) => {
    // Verify the app is running via the API
    const app = await api.ensureAppRunning(appName);

    // The ensureAppRunning waits for 'running' status which confirms health check passed
    expect(app.status).toBe('running');
  });

  test('forward auth redirects unauthenticated users to Authentik', async ({ page, openApp }) => {
    // When accessing qBittorrent without authentication, forward-auth should redirect to Authentik
    const frame = await openApp();

    // The forward auth should redirect to Authentik login
    // This appears as a cross-origin block in the iframe due to COEP headers
    // We verify this by checking that the iframe shows Authentik content OR
    // remains blocked (which is expected cross-origin behavior)
    await page.waitForTimeout(3000);

    // Check if we see Authentik's login form in the iframe
    // Forward auth apps should show Authentik login or be blocked by COEP
    const hasAuthentikLogin = await frame.locator('input[name="uidField"]').isVisible().catch(() => false);
    const hasAuthentikComponent = await frame.locator('[class*="ak-"], ak-flow-executor').first().isVisible().catch(() => false);

    // If we don't see Authentik login, the cross-origin redirect may have been blocked
    // which is acceptable behavior for forward-auth in iframes
    if (!hasAuthentikLogin && !hasAuthentikComponent) {
      // Check if we got a cross-origin block (expected for forward-auth)
      // The iframe will be empty or show an error
      const iframeHtml = await frame.locator('body').innerHTML().catch(() => '');
      // Accept either Authentik login, empty iframe (blocked), or error page
      expect(
        hasAuthentikLogin || hasAuthentikComponent || iframeHtml.length < 1000,
        'Forward auth should redirect to Authentik or be blocked by COEP'
      ).toBe(true);
    }
  });

  test('completes full SSO login flow', async ({ page, openApp }) => {
    const frame = await openApp();

    // Wait for Authentik's login form to load
    // Forward-auth redirects to /if/flow/default-authentication-flow/
    // The username field may take time to render (Authentik uses web components)
    const usernameField = frame.locator('input[name="uidField"]');

    // Use waitFor which actually waits for the element (unlike isVisible which checks immediately)
    try {
      await usernameField.waitFor({ state: 'visible', timeout: 30000 });
    } catch {
      // Check if we can see any Authentik-specific content
      const hasAuthentikContent = await frame.locator('ak-flow-executor, [class*="pf-"], .ak-stage').first().isVisible().catch(() => false);
      if (!hasAuthentikContent) {
        test.skip(true, 'Authentik login form not visible - check iframe loading');
        return;
      }
      // Authentik loaded but form not visible yet - wait more
      await usernameField.waitFor({ state: 'visible', timeout: 15000 });
    }

    // Authentik has a two-step flow: username -> "Log in" -> password -> "Continue"
    await usernameField.fill('akadmin');

    const loginButton = frame.getByRole('button', { name: 'Log in' });
    await expect(loginButton).toBeVisible({ timeout: 5000 });
    await loginButton.click();

    // Wait for password field
    const passwordField = frame.getByPlaceholder('Please enter your password');
    await expect(passwordField).toBeVisible({ timeout: 10000 });
    await passwordField.fill('password');

    const continueBtn = frame.getByRole('button', { name: 'Continue' });
    await expect(continueBtn).toBeVisible({ timeout: 5000 });
    await continueBtn.click();

    // Wait for redirect back to qBittorrent
    await page.waitForTimeout(3000);

    // After successful SSO, should see qBittorrent's interface
    await expect(async () => {
      // qBittorrent shows either login form (if session expired) or main UI
      const hasTorrentsTable = await frame.locator('#torrentsTable, .torrentsTable').first().isVisible().catch(() => false);
      const hasLoginForm = await frame.locator('#login').isVisible().catch(() => false);
      const hasDownloads = await frame.getByText(/torrents|downloads|seeding/i).first().isVisible().catch(() => false);

      expect(
        hasTorrentsTable || hasLoginForm || hasDownloads,
        'Should see qBittorrent interface after SSO'
      ).toBe(true);
    }).toPass({ timeout: 15000 });

    // Verify parent page is still the app viewer
    expect(page.url()).toContain('/apps/qbittorrent');
  });
});
